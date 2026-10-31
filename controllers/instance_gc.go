// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultGCRetentionPeriod is the minimum time a stopped instance must have
// existed before it becomes eligible for garbage collection. This gives
// operators a window to inspect post-mortem state.
const defaultGCRetentionPeriod = 10 * time.Minute

// defaultMaxGCDeletionsPerCycle caps how many instance fact trees the GC
// controller removes in a single reconciliation cycle. Each instance tree
// contains ~15 facts, so 4 trees = ~60 deletes — near the transaction budget.
const defaultMaxGCDeletionsPerCycle = 4

// InstanceGCController garbage-collects fact trees of stopped instances once
// the owning service is fully converged and the retention period has elapsed.
// Without this controller, every failure replacement leaves the old instance's
// observed/, derived/, and placement/ facts in etcd indefinitely.
type InstanceGCController struct {
	// RetentionPeriod is the minimum age (since creation) of a stopped instance
	// before it becomes eligible for garbage collection.
	RetentionPeriod time.Duration

	// MaxDeletionsPerCycle limits how many instance trees are deleted per
	// reconciliation cycle to stay within the transaction budget.
	MaxDeletionsPerCycle int

	// NowFunc returns the current time. Replaced in tests for determinism.
	NowFunc func() time.Time
}

// NewInstanceGCController returns a controller with default retention and
// deletion limits.
func NewInstanceGCController() *InstanceGCController {
	return &InstanceGCController{
		RetentionPeriod:      defaultGCRetentionPeriod,
		MaxDeletionsPerCycle: defaultMaxGCDeletionsPerCycle,
		NowFunc:              time.Now,
	}
}

// Name returns "instance-gc", identifying this controller in logs and runner
// bookkeeping.
func (instanceGCController *InstanceGCController) Name() string { return "instance-gc" }

// Watch returns the fact prefixes needed to identify stopped instances, their
// owning services, desired counts, and placement facts to clean up.
func (instanceGCController *InstanceGCController) Watch() []string {
	return []string{
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
		types.ScanEffectiveServices,
		types.ScanPlacements,
	}
}

// Reconcile scans for stopped instances whose owning service has enough running
// replacements and whose creation timestamp exceeds the retention period. For
// each eligible instance, it emits Delete changes for every fact in the
// instance's tree (observed, derived, and placement prefixes).
func (instanceGCController *InstanceGCController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	fieldsByInstanceID := parseInstanceFieldsFromFacts(facts)
	stateByInstanceID, serviceByInstanceID := buildInstanceMapsFromFields(fieldsByInstanceID)
	desiredCounts, _, _ := parseEffectiveServiceFacts(facts)

	runningCountByService := countRunningInstancesByService(serviceByInstanceID, stateByInstanceID)
	placementByInstanceID := parsePlacementFacts(facts)

	now := instanceGCController.NowFunc()
	var candidates []gcCandidate
	for instanceID, state := range stateByInstanceID {
		serviceName := serviceByInstanceID[instanceID]
		if serviceName == "" {
			// Orphaned instance with no service field — always eligible for GC.
			candidates = append(candidates, gcCandidate{
				instanceID:  instanceID,
				serviceName: "",
			})
			continue
		}
		if state != types.InstanceStopped && state != types.InstanceFailed {
			continue
		}
		desiredCount, hasDesired := desiredCounts[serviceName]
		if !hasDesired {
			// Service deleted — all its instances are eligible for GC.
			desiredCount = 0
		}
		runningCount := runningCountByService[serviceName]
		if runningCount < desiredCount {
			continue
		}
		createdAtString := fieldsByInstanceID[instanceID]["created-at"]
		if createdAtString != "" {
			createdAt, parseError := time.Parse(time.RFC3339, createdAtString)
			if parseError == nil && now.Sub(createdAt) < instanceGCController.RetentionPeriod {
				continue
			}
		}
		candidates = append(candidates, gcCandidate{
			instanceID:  instanceID,
			serviceName: serviceName,
		})
	}

	sort.Slice(candidates, func(indexA, indexB int) bool {
		return candidates[indexA].instanceID < candidates[indexB].instanceID
	})

	var changes []Change
	deletedCount := 0
	for _, candidate := range candidates {
		if deletedCount >= instanceGCController.MaxDeletionsPerCycle {
			break
		}
		instanceChanges := buildInstanceDeletionChanges(candidate.instanceID, facts, placementByInstanceID)
		changes = append(changes, instanceChanges...)
		deletedCount++
	}

	if deletedCount > 0 {
		orphanedCount := 0
		for _, candidate := range candidates[:deletedCount] {
			if candidate.serviceName == "" {
				orphanedCount++
			}
		}
		logging.Default().Info("instance-gc collecting instances",
			"count", strconv.Itoa(deletedCount),
			"orphaned", strconv.Itoa(orphanedCount),
			"remaining", strconv.Itoa(len(candidates)-deletedCount))
	}

	return changes, nil
}

// gcCandidate holds a stopped instance eligible for garbage collection.
type gcCandidate struct {
	instanceID  string
	serviceName string
}

// countRunningInstancesByService counts instances in the running state per
// service.
func countRunningInstancesByService(
	serviceByInstanceID map[string]string,
	stateByInstanceID map[string]types.InstanceState,
) map[string]int {
	counts := make(map[string]int)
	for instanceID, serviceName := range serviceByInstanceID {
		if stateByInstanceID[instanceID] == types.InstanceRunning {
			counts[serviceName]++
		}
	}
	return counts
}

// parsePlacementFacts extracts placement/instance/{id} → nodeID mappings from
// the fact list.
func parsePlacementFacts(facts []store.Fact) map[string]string {
	placements := make(map[string]string)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanPlacements) {
		instanceID := strings.TrimPrefix(fact.Key, types.ScanPlacements)
		if instanceID != "" && !strings.Contains(instanceID, "/") {
			placements[instanceID] = string(fact.Value)
		}
	}
	return placements
}

// buildInstanceDeletionChanges produces Delete changes for every fact key
// belonging to the given instance: observed/instance/{id}/*, derived/instance/{id}/*,
// placement/instance/{id}, and placement/node/{nodeID}/{id}.
func buildInstanceDeletionChanges(instanceID string, facts []store.Fact, placementByInstanceID map[string]string) []Change {
	observedPrefix := types.ScanObservedInstances + instanceID + "/"
	derivedPrefix := types.ScanDerivedInstances + instanceID + "/"

	var changes []Change

	// Delete the instance marker key itself.
	changes = append(changes, Change{Type: store.OpDelete, Key: types.KeyObservedInstance(instanceID)})

	for _, fact := range facts {
		if strings.HasPrefix(fact.Key, observedPrefix) || strings.HasPrefix(fact.Key, derivedPrefix) {
			changes = append(changes, Change{Type: store.OpDelete, Key: fact.Key})
		}
	}

	// Delete placement facts.
	changes = append(changes, Change{Type: store.OpDelete, Key: types.KeyPlacementInstance(instanceID)})
	if nodeID, hasPlacement := placementByInstanceID[instanceID]; hasPlacement && nodeID != "" {
		changes = append(changes, Change{Type: store.OpDelete, Key: types.KeyPlacementNodeInstance(nodeID, instanceID)})
	}

	groupID := "gc-" + instanceID
	return groupedChanges(groupID, changes...)
}
