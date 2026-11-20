// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// Startup migration of resource values stored without a unit (M243 D9): the
// vertical autoscaler before M243 wrote bare millicores (cpu) and bytes
// (memory), which the shared parser now reads as cores and bytes.

// resourceMigrationMaxAttempts bounds the scan-and-CAS retries when another
// writer changes a key between the migration's scan and its transaction.
const resourceMigrationMaxAttempts = 5

// transactionItemsPerRewrite is the etcd transaction items one rewrite uses: a
// revision compare and a put. etcdTransactionOperationLimit counts both (C1).
const transactionItemsPerRewrite = 2

// resourceMigrationScanPrefixes are the layers whose bare values are migrated;
// desired/ is never rewritten (D9). A fixed-size array: it cannot grow.
var resourceMigrationScanPrefixes = [...]string{types.ScanIntentAutoscalerServices, types.ScanEffectiveServices}

// resourceKindSuffix pairs a migrated key suffix with its resource kind.
type resourceKindSuffix struct {
	keySuffix    string // keySuffix ends the key, e.g. "/resources/cpu"
	resourceKind string // resourceKind is "cpu" or "memory"
}

// resourceKindSuffixes lists the migrated key suffixes in a fixed order (a
// fixed-size array: it cannot grow, and iteration is deterministic).
var resourceKindSuffixes = [...]resourceKindSuffix{{"/resources/cpu", "cpu"}, {"/resources/memory", "memory"}}

// plannedResourceRewrite is one bare value to rewrite in unit form.
type plannedResourceRewrite struct {
	serviceName string // serviceName groups the rewrites of one service into one batch
	factKey     string // factKey is the intent or effective key
	revision    int64  // revision is the key's revision at scan time, the CAS guard
	unitValue   string // unitValue is the rewritten value, e.g. "750m" or "768Mi"
}

// MigrateUnitlessResourceRecommendations rewrites every bare (digits-only)
// intent/autoscaler and effective CPU and memory value in unit form, clamped
// to the service's vertical bounds, in CAS transactions. Run at server start
// before controllers; idempotent. Returns how many values it rewrote.
func MigrateUnitlessResourceRecommendations(ctx context.Context, factStore store.StateStore) (int, error) {
	for attempt := 0; attempt < resourceMigrationMaxAttempts; attempt++ {
		plannedRewrites, planError := planUnitlessResourceRewrites(ctx, factStore)
		if planError != nil || len(plannedRewrites) == 0 {
			return 0, planError
		}
		committed, commitError := commitResourceRewrites(ctx, factStore, plannedRewrites)
		if commitError != nil {
			return 0, commitError
		}
		if committed {
			logging.Default().Info("migrated resource recommendations stored without a unit", "values", strconv.Itoa(len(plannedRewrites)))
			return len(plannedRewrites), nil
		}
	}
	return 0, fmt.Errorf("resource unit migration: keys kept changing over %d attempts", resourceMigrationMaxAttempts)
}

// planUnitlessResourceRewrites scans the intent and effective layers and the
// vertical bounds, and returns the bare values to rewrite, sorted by key.
func planUnitlessResourceRewrites(ctx context.Context, factStore store.StateStore) ([]plannedResourceRewrite, error) {
	desiredFacts, scanError := factStore.Scan(ctx, types.ScanDesiredServices)
	if scanError != nil {
		return nil, fmt.Errorf("resource unit migration: scan %s: %w", types.ScanDesiredServices, scanError)
	}
	verticalBounds := extractVerticalBoundsByResource(desiredFacts)
	var plannedRewrites []plannedResourceRewrite
	for _, layerPrefix := range resourceMigrationScanPrefixes {
		layerFacts, layerError := factStore.Scan(ctx, layerPrefix)
		if layerError != nil {
			return nil, fmt.Errorf("resource unit migration: scan %s: %w", layerPrefix, layerError)
		}
		for _, fact := range layerFacts {
			serviceName, resourceKind := splitServiceResourceKey(fact.Key, layerPrefix)
			if resourceKind == "" || !types.IsBareInteger(string(fact.Value)) {
				continue
			}
			legacyQuantity, parseError := strconv.ParseInt(string(fact.Value), 10, 64)
			if parseError != nil {
				continue // beyond int64: left for the readers, which report it
			}
			unitValue := formatLegacyQuantity(resourceKind, verticalBounds[serviceName+"/"+resourceKind].clamp(legacyQuantity))
			plannedRewrites = append(plannedRewrites, plannedResourceRewrite{
				serviceName: serviceName, factKey: fact.Key, revision: fact.Revision, unitValue: unitValue,
			})
		}
	}
	sort.Slice(plannedRewrites, func(i, j int) bool { return plannedRewrites[i].factKey < plannedRewrites[j].factKey })
	return plannedRewrites, nil
}

// splitServiceResourceKey returns the service name and resource kind ("cpu"
// or "memory") of a {prefix}{service}/resources/{kind} key, matching the
// suffix so hierarchical names (team/web) work; kind is "" otherwise.
func splitServiceResourceKey(factKey string, prefix string) (string, string) {
	relativePath := strings.TrimPrefix(factKey, prefix)
	for _, kindSuffix := range resourceKindSuffixes {
		if serviceName, matched := strings.CutSuffix(relativePath, kindSuffix.keySuffix); matched && serviceName != "" {
			return serviceName, kindSuffix.resourceKind
		}
	}
	return "", ""
}

// formatLegacyQuantity writes a legacy quantity in unit form: CPU millicores
// as "{n}m", memory bytes as whole MiB rounded up.
func formatLegacyQuantity(resourceKind string, legacyQuantity int64) string {
	if resourceKind == "cpu" {
		return types.FormatCPUMillicores(legacyQuantity)
	}
	return types.FormatMemoryMiB(legacyQuantity)
}

// commitResourceRewrites writes the rewrites in CAS transactions of whole
// services, each within the etcd operation limit. Returns false when another
// writer changed a guarded key, so the caller scans again.
func commitResourceRewrites(ctx context.Context, factStore store.StateStore, plannedRewrites []plannedResourceRewrite) (bool, error) {
	for _, batch := range batchRewritesByService(plannedRewrites) {
		var compares []store.Compare
		var operations []store.Op
		for _, rewrite := range batch {
			compares = append(compares, store.Compare{Key: rewrite.factKey, Revision: rewrite.revision})
			operations = append(operations, store.Op{Type: store.OpPut, Key: rewrite.factKey, Value: []byte(rewrite.unitValue)})
		}
		committed, transactionError := factStore.Transaction(ctx, compares, operations, nil)
		if transactionError != nil {
			return false, fmt.Errorf("resource unit migration: transaction: %w", transactionError)
		}
		if !committed {
			return false, nil
		}
	}
	return true, nil
}

// batchRewritesByService groups the rewrites into batches of whole services
// whose items (one compare and one put per rewrite) fit
// etcdTransactionOperationLimit; a service is never split (C1).
func batchRewritesByService(plannedRewrites []plannedResourceRewrite) [][]plannedResourceRewrite {
	rewritesByService := make(map[string][]plannedResourceRewrite)
	for _, rewrite := range plannedRewrites {
		rewritesByService[rewrite.serviceName] = append(rewritesByService[rewrite.serviceName], rewrite)
	}
	var batches [][]plannedResourceRewrite
	var currentBatch []plannedResourceRewrite
	for _, serviceName := range sortedMapKeys(rewritesByService) {
		serviceRewrites := rewritesByService[serviceName]
		if len(currentBatch) > 0 && (len(currentBatch)+len(serviceRewrites))*transactionItemsPerRewrite > etcdTransactionOperationLimit {
			batches = append(batches, currentBatch)
			currentBatch = nil
		}
		currentBatch = append(currentBatch, serviceRewrites...)
	}
	return append(batches, currentBatch)
}

const (
	// resourceMigrationInitialRetryDelay is the wait after the first failed
	// startup migration (C2).
	resourceMigrationInitialRetryDelay = time.Second

	// resourceMigrationMaximumRetryDelay caps the doubling retry delay.
	resourceMigrationMaximumRetryDelay = time.Minute

	// resourceMigrationRetryGrowthFactor multiplies the delay after each failure.
	resourceMigrationRetryGrowthFactor = 2
)

// resourceMigrationBackoff is the bounded exponential back-off between
// startup migration attempts.
type resourceMigrationBackoff struct {
	initialDelay time.Duration // initialDelay is the wait after the first failure
	maximumDelay time.Duration // maximumDelay caps the wait
}

// delayAfterAttempt returns the wait after failed attempt number attempt
// (1-based): the initial delay doubled per earlier failure, at most the maximum.
func (backoff resourceMigrationBackoff) delayAfterAttempt(attempt int) time.Duration {
	retryDelay := backoff.initialDelay
	for failedAttempt := 1; failedAttempt < attempt && retryDelay < backoff.maximumDelay; failedAttempt++ {
		retryDelay *= resourceMigrationRetryGrowthFactor
	}
	return min(retryDelay, backoff.maximumDelay)
}

// RunControllersAfterResourceMigration runs the startup migration until it
// succeeds, retrying with bounded exponential back-off, and only then calls
// startControllers (D9, C2). Blocks; returns false if ctx ends first, in which
// case the controllers never start. Callers run it in a goroutine.
func RunControllersAfterResourceMigration(ctx context.Context, factStore store.StateStore, startControllers func()) bool {
	return runControllersAfterResourceMigration(ctx, factStore, resourceMigrationBackoff{
		initialDelay: resourceMigrationInitialRetryDelay, maximumDelay: resourceMigrationMaximumRetryDelay,
	}, startControllers)
}

// runControllersAfterResourceMigration is RunControllersAfterResourceMigration
// with the back-off as a parameter, so tests can use short delays.
func runControllersAfterResourceMigration(ctx context.Context, factStore store.StateStore, backoff resourceMigrationBackoff, startControllers func()) bool {
	for attempt := 1; ; attempt++ {
		_, migrationError := MigrateUnitlessResourceRecommendations(ctx, factStore)
		if migrationError == nil {
			startControllers()
			return true
		}
		retryDelay := backoff.delayAfterAttempt(attempt)
		logging.Default().Error("resource unit migration failed; controllers wait for it",
			"attempt", strconv.Itoa(attempt), "next_retry_in", retryDelay.String(), "error", migrationError.Error())
		retryTimer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			retryTimer.Stop()
			return false
		case <-retryTimer.C:
		}
	}
}
