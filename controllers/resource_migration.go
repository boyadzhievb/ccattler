// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strconv"

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

// plannedResourceRewrite is one bare value to rewrite in unit form.
type plannedResourceRewrite struct {
	serviceName string // serviceName groups the rewrites of one service into one batch
	factKey     string // factKey is the intent or effective key
	revision    int64  // revision is the key's revision at scan time, the CAS guard
	unitValue   string // unitValue is the rewritten value, e.g. "750m" or "768Mi"
}

// MigrateUnitlessResourceRecommendations rewrites every bare (digits-only)
// autoscaler recommendation, and effective value of a resource that has one,
// in unit form clamped to the vertical bounds, in CAS transactions; idempotent.
// Returns the total rewritten over all batches and attempts (review N3).
func MigrateUnitlessResourceRecommendations(ctx context.Context, factStore store.StateStore) (int, error) {
	totalRewritten := 0
	for attempt := 0; attempt < resourceMigrationMaxAttempts; attempt++ {
		plannedRewrites, planError := planUnitlessResourceRewrites(ctx, factStore)
		if planError != nil {
			return totalRewritten, planError
		}
		committedCount, allCommitted, commitError := commitResourceRewrites(ctx, factStore, plannedRewrites)
		totalRewritten += committedCount
		if commitError != nil {
			return totalRewritten, commitError
		}
		if allCommitted {
			if totalRewritten > 0 {
				logging.Default().Info("migrated resource recommendations stored without a unit", "values", strconv.Itoa(totalRewritten))
			}
			return totalRewritten, nil
		}
	}
	return totalRewritten, fmt.Errorf("resource unit migration: keys kept changing over %d attempts", resourceMigrationMaxAttempts)
}

// planUnitlessResourceRewrites returns the bare values to rewrite, sorted by
// key, in two explicit passes (P-N8): the set of resources with a
// recommendation, then both layers. An effective value is planned only when
// its resource has a recommendation: without one it was copied from desired
// and already reads right (D9). desired/ is only read, for the bounds.
func planUnitlessResourceRewrites(ctx context.Context, factStore store.StateStore) ([]plannedResourceRewrite, error) {
	desiredFacts, scanError := scanMigrationLayer(ctx, factStore, types.ScanDesiredServices)
	if scanError != nil {
		return nil, scanError
	}
	intentFacts, scanError := scanMigrationLayer(ctx, factStore, types.ScanIntentAutoscalerServices)
	if scanError != nil {
		return nil, scanError
	}
	effectiveFacts, scanError := scanMigrationLayer(ctx, factStore, types.ScanEffectiveServices)
	if scanError != nil {
		return nil, scanError
	}
	verticalBounds := extractVerticalBoundsByResource(desiredFacts)
	recommendedResources := collectRecommendedResources(intentFacts)
	plannedRewrites := planLayerRewrites(intentFacts, types.ScanIntentAutoscalerServices, verticalBounds, nil)
	plannedRewrites = append(plannedRewrites,
		planLayerRewrites(effectiveFacts, types.ScanEffectiveServices, verticalBounds, recommendedResources)...)
	sort.Slice(plannedRewrites, func(i, j int) bool { return plannedRewrites[i].factKey < plannedRewrites[j].factKey })
	return plannedRewrites, nil
}

// collectRecommendedResources returns the resources with an autoscaler
// recommendation key, whatever its value (the first planning pass).
func collectRecommendedResources(intentFacts []store.Fact) map[types.ServiceResourceKey]bool {
	recommendedResources := make(map[types.ServiceResourceKey]bool)
	for _, fact := range intentFacts {
		if resourceKey, matched := types.SplitServiceResourceKey(fact.Key, types.ScanIntentAutoscalerServices); matched {
			recommendedResources[resourceKey] = true
		}
	}
	return recommendedResources
}

// scanMigrationLayer scans one layer for the migration, naming the prefix in
// the error.
func scanMigrationLayer(ctx context.Context, factStore store.StateStore, layerPrefix string) ([]store.Fact, error) {
	layerFacts, scanError := factStore.Scan(ctx, layerPrefix)
	if scanError != nil {
		return nil, fmt.Errorf("resource unit migration: scan %s: %w", layerPrefix, scanError)
	}
	return layerFacts, nil
}

// planLayerRewrites plans the rewrite of every bare request value of one
// layer. With requiredResources set (the effective layer), only resources in
// it are planned; nil plans every resource (the intent layer).
func planLayerRewrites(
	layerFacts []store.Fact,
	layerPrefix string,
	verticalBounds map[types.ServiceResourceKey]verticalResourceBounds,
	requiredResources map[types.ServiceResourceKey]bool,
) []plannedResourceRewrite {
	var plannedRewrites []plannedResourceRewrite
	for _, fact := range layerFacts {
		resourceKey, matched := types.SplitServiceResourceKey(fact.Key, layerPrefix)
		if !matched || !types.IsBareInteger(string(fact.Value)) {
			continue
		}
		if requiredResources != nil && !requiredResources[resourceKey] {
			continue
		}
		legacyQuantity, parseError := strconv.ParseInt(string(fact.Value), 10, 64)
		if parseError != nil {
			continue // beyond int64: left for the readers, which report it
		}
		plannedRewrites = append(plannedRewrites, plannedResourceRewrite{
			serviceName: resourceKey.ServiceName, factKey: fact.Key, revision: fact.Revision,
			unitValue: formatLegacyQuantity(lookupVerticalResourceSpec(resourceKey.Kind), legacyQuantity, verticalBounds[resourceKey]),
		})
	}
	return plannedRewrites
}

// formatLegacyQuantity writes a legacy quantity in unit form, clamped to the
// bounds in the kind's sizing unit (B2, D7): "{n}m", or whole MiB rounded up.
// A legacy 0 (no limit) under a declared max becomes the max (D9, R5-N1). A max
// below one sizing unit (memory under 1 MiB) is clamped and written exactly.
func formatLegacyQuantity(kindSpec verticalResourceSpec, legacyQuantity int64, bounds verticalResourceBounds) string {
	hasDeclaredMaximum := !bounds.unreadable && bounds.maximum > 0
	if hasDeclaredMaximum && legacyQuantity == 0 {
		legacyQuantity = bounds.maximum // 0 meant no limit: the max is the closest bounded value
	}
	boundsInUnit := kindSpec.boundsInVerticalUnit(bounds)
	if hasDeclaredMaximum && boundsInUnit.maximum == 0 {
		return types.FormatMemoryBytesExactly(bounds.clamp(legacyQuantity)) // only memory: CPU's unit is its parsed unit
	}
	return kindSpec.formatVerticalUnit(boundsInUnit.clamp(kindSpec.toVerticalUnit(legacyQuantity)))
}

// commitResourceRewrites writes the rewrites in CAS transactions of whole
// services, each within the etcd operation limit. Returns how many rewrites
// committed, and false when another writer changed a guarded key, so the
// caller scans again.
func commitResourceRewrites(ctx context.Context, factStore store.StateStore, plannedRewrites []plannedResourceRewrite) (int, bool, error) {
	committedCount := 0
	if len(plannedRewrites) == 0 {
		return 0, true, nil
	}
	for _, batch := range batchRewritesByService(plannedRewrites) {
		var compares []store.Compare
		var operations []store.Op
		for _, rewrite := range batch {
			compares = append(compares, store.Compare{Key: rewrite.factKey, Revision: rewrite.revision})
			operations = append(operations, store.Op{Type: store.OpPut, Key: rewrite.factKey, Value: []byte(rewrite.unitValue)})
		}
		committed, transactionError := factStore.Transaction(ctx, compares, operations, nil)
		if transactionError != nil {
			return committedCount, false, fmt.Errorf("resource unit migration: transaction: %w", transactionError)
		}
		if !committed {
			return committedCount, false, nil
		}
		committedCount += len(batch)
	}
	return committedCount, true, nil
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

// ResourceMigrationPrecondition returns the resource unit migration as a
// named precondition: an error until every legacy value is migrated (D9). The
// server runs it before starting controllers, at start and on becoming leader
// (passed to NewHARunner).
func ResourceMigrationPrecondition(factStore store.StateStore) ControllerPrecondition {
	return ControllerPrecondition{Name: "resource unit migration", Check: func(ctx context.Context) error {
		_, migrationError := MigrateUnitlessResourceRecommendations(ctx, factStore)
		return migrationError
	}}
}

// RunControllersAfterResourceMigration runs the startup migration until it
// succeeds, retrying with bounded exponential back-off, and only then calls
// startControllers (D9, C2). Blocks; returns false if ctx ends first, in which
// case the controllers never start. Callers run it in a goroutine.
func RunControllersAfterResourceMigration(ctx context.Context, factStore store.StateStore, startControllers func()) bool {
	return runControllersAfterResourceMigration(ctx, factStore, defaultPreconditionRetryBackoff(), startControllers)
}

// runControllersAfterResourceMigration is RunControllersAfterResourceMigration
// with the back-off as a parameter, so tests can use short delays.
func runControllersAfterResourceMigration(ctx context.Context, factStore store.StateStore, backoff preconditionRetryBackoff, startControllers func()) bool {
	return runAfterPreconditionSucceeds(ctx, ResourceMigrationPrecondition(factStore), backoff, startControllers)
}
