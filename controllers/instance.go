// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// defaultMaxInstanceCreationsPerCycle limits how many new instances the
// InstanceController creates per reconciliation cycle. Each creation
// emits 3 store operations, so 18 creations = 54 ops — leaving room for
// scale-down changes in the same cycle within the runner's 60-change
// transaction budget. Remaining deficit converges in subsequent cycles.
const defaultMaxInstanceCreationsPerCycle = 18

// maxInstanceControllerChangesPerCycle is the hard output cap. If creations
// plus scale-down changes exceed this, excess changes are deferred to the
// next cycle. This guarantees the runner's transaction budget is never
// exceeded regardless of how many services need simultaneous adjustment.
const maxInstanceControllerChangesPerCycle = 58

// InstanceController reconciles the desired instance count for each service
// against the actually observed instances. For stateless services it creates
// new instances with random IDs and stops excess instances (pending first).
// For stateful services it uses ordinal IDs ({service}-0, {service}-1, ...),
// enforces ordered startup (N+1 only after N is running), and removes the
// highest ordinal first on scale-down.
type InstanceController struct {
	// NewID is a function that generates unique instance identifiers for
	// stateless services. It defaults to types.NewInstanceID but can be
	// replaced in tests for deterministic output.
	NewID types.IDFunc

	// MaxCreationsPerCycle limits how many new instances are created in a
	// single reconciliation cycle across all services. This prevents
	// transaction overflow under mass-scale-up scenarios.
	MaxCreationsPerCycle int
}

// NewInstanceController returns an InstanceController wired to the default
// ID generator.
func NewInstanceController() *InstanceController {
	return &InstanceController{
		NewID:                types.NewInstanceID,
		MaxCreationsPerCycle: defaultMaxInstanceCreationsPerCycle,
	}
}

// Name returns "instance", identifying this controller in logs and runner
// bookkeeping.
func (instanceController *InstanceController) Name() string { return "instance" }

// Watch returns the fact prefixes that drive instance reconciliation:
// effective service definitions (which carry the desired instance count and
// stateful flag) and observed instances (which represent what actually exists).
func (instanceController *InstanceController) Watch() []string {
	return []string{
		types.ScanEffectiveServices,
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
	}
}

// Reconcile compares desired instance counts (from effective service facts)
// against active (non-stopped) observed instances and emits changes to
// create or stop instances until the counts match. Stateful services use
// ordinal IDs and ordered startup; stateless services use random IDs.
func (instanceController *InstanceController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	desiredCounts, statefulServices := parseEffectiveServiceFacts(facts)
	stateByInstanceID, serviceByInstanceID := parseObservedInstanceFacts(facts)
	activeInstanceIDs := buildActiveInstanceIDsByService(serviceByInstanceID, stateByInstanceID)

	var changes []Change
	creationsRemaining := instanceController.MaxCreationsPerCycle
	sortedServiceNames := sortedMapKeys(desiredCounts)

	for _, serviceName := range sortedServiceNames {
		wantCount := desiredCounts[serviceName]
		activeIDs := activeInstanceIDs[serviceName]
		haveCount := len(activeIDs)

		if statefulServices[serviceName] {
			if creationsRemaining <= 0 {
				continue
			}
			serviceChanges := instanceController.reconcileStatefulService(
				serviceName, wantCount, activeIDs, stateByInstanceID,
			)
			changes = append(changes, serviceChanges...)
			creationsRemaining -= countInstanceCreations(serviceChanges)
		} else {
			deficit := wantCount - haveCount
			if deficit > 0 && deficit > creationsRemaining {
				deficit = creationsRemaining
			}
			if deficit > 0 {
				changes = append(changes, instanceController.createPendingInstances(serviceName, deficit)...)
				creationsRemaining -= deficit
			} else if haveCount > wantCount {
				changes = append(changes, markExcessStatelessInstancesAsStopped(
					activeIDs, stateByInstanceID, haveCount-wantCount,
				)...)
			}
		}

		if creationsRemaining <= 0 {
			creationsRemaining = 0
		}
	}

	if len(changes) > maxInstanceControllerChangesPerCycle {
		logging.Default().Warn("instance controller output capped",
			"total_changes", fmt.Sprintf("%d", len(changes)),
			"cap", fmt.Sprintf("%d", maxInstanceControllerChangesPerCycle))
		changes = changes[:maxInstanceControllerChangesPerCycle]
	}

	return changes, nil
}

// parseEffectiveServiceFacts extracts desired instance counts and stateful
// flags from effective service facts.
func parseEffectiveServiceFacts(facts []store.Fact) (map[string]int, map[string]bool) {
	desiredCounts := make(map[string]int)
	statefulServices := make(map[string]bool)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanEffectiveServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanEffectiveServices)
		if !hasSuffix {
			continue
		}
		switch suffix {
		case "instances":
			parsedCount, parseErr := strconv.Atoi(string(fact.Value))
			if parseErr != nil {
				logging.Default().Warn("corrupt instance count fact", "key", fact.Key, "value", string(fact.Value))
				continue
			}
			desiredCounts[serviceName] = parsedCount
		case "stateful":
			statefulServices[serviceName] = string(fact.Value) == "true"
		}
	}

	return desiredCounts, statefulServices
}

// parseObservedInstanceFacts extracts effective instance states and service
// associations from observed and derived instance facts.
func parseObservedInstanceFacts(facts []store.Fact) (map[string]types.InstanceState, map[string]string) {
	instanceFields := parseInstanceFieldsFromFacts(facts)
	stateByInstanceID := make(map[string]types.InstanceState, len(instanceFields))
	serviceByInstanceID := make(map[string]string, len(instanceFields))

	for instanceID, fields := range instanceFields {
		stateByInstanceID[instanceID] = effectiveInstanceState(fields)
		if serviceName := fields["service"]; serviceName != "" {
			serviceByInstanceID[instanceID] = serviceName
		}
	}

	return stateByInstanceID, serviceByInstanceID
}

// buildActiveInstanceIDsByService groups non-stopped instance IDs by their
// owning service. The returned slices are sorted for deterministic output.
func buildActiveInstanceIDsByService(
	serviceByInstanceID map[string]string,
	stateByInstanceID map[string]types.InstanceState,
) map[string][]string {
	sortedInstanceIDs := sortedMapKeys(serviceByInstanceID)

	activeInstanceIDs := make(map[string][]string)
	for _, instanceID := range sortedInstanceIDs {
		serviceName := serviceByInstanceID[instanceID]
		state := stateByInstanceID[instanceID]
		if state == types.InstanceStopped {
			continue
		}
		activeInstanceIDs[serviceName] = append(activeInstanceIDs[serviceName], instanceID)
	}
	return activeInstanceIDs
}

// countInstanceCreations counts how many new instance creations are in a
// change set by looking for pending-state writes (each creation has one).
func countInstanceCreations(changes []Change) int {
	creationCount := 0
	for _, change := range changes {
		if change.Type == store.OpPut && string(change.Value) == string(types.InstancePending) {
			creationCount++
		}
	}
	return creationCount
}

// reconcileStatefulService handles instance count reconciliation for stateful
// services. It uses ordinal IDs ({service}-0, {service}-1, ...), creates at
// most one new instance per reconciliation cycle (the next ordinal in sequence,
// only if all lower ordinals are running), and removes the highest ordinal
// first on scale-down.
func (instanceController *InstanceController) reconcileStatefulService(
	serviceName string, wantCount int,
	activeIDs []string, stateByInstanceID map[string]types.InstanceState,
) []Change {
	existingOrdinals := parseExistingOrdinals(serviceName, activeIDs)
	haveCount := len(existingOrdinals)

	if haveCount < wantCount {
		return createNextStatefulInstance(serviceName, wantCount, existingOrdinals, stateByInstanceID)
	}
	if haveCount > wantCount {
		return stopHighestOrdinalInstances(serviceName, existingOrdinals, haveCount-wantCount)
	}
	return nil
}

// parseExistingOrdinals extracts and returns the sorted ordinal indices for
// active instances of a stateful service. Instances whose ID does not follow
// the "{service}-{ordinal}" pattern are ignored.
func parseExistingOrdinals(serviceName string, activeIDs []string) []int {
	prefix := serviceName + "-"
	var ordinals []int
	for _, instanceID := range activeIDs {
		if !strings.HasPrefix(instanceID, prefix) {
			continue
		}
		ordinalStr := strings.TrimPrefix(instanceID, prefix)
		ordinal, parseErr := strconv.Atoi(ordinalStr)
		if parseErr != nil {
			continue
		}
		ordinals = append(ordinals, ordinal)
	}
	sort.Ints(ordinals)
	return ordinals
}

// createNextStatefulInstance creates a single pending instance at the next
// ordinal position if all lower ordinals are running. Stateful services
// enforce ordered startup: instance N+1 is only created when instance N is
// running. Returns at most one instance creation per reconciliation cycle.
func createNextStatefulInstance(
	serviceName string, wantCount int,
	existingOrdinals []int, stateByInstanceID map[string]types.InstanceState,
) []Change {
	ordinalSet := make(map[int]bool, len(existingOrdinals))
	for _, ordinal := range existingOrdinals {
		ordinalSet[ordinal] = true
	}

	for nextOrdinal := range wantCount {
		if ordinalSet[nextOrdinal] {
			instanceID := fmt.Sprintf("%s-%d", serviceName, nextOrdinal)
			if stateByInstanceID[instanceID] != types.InstanceRunning {
				return nil
			}
			continue
		}
		instanceID := fmt.Sprintf("%s-%d", serviceName, nextOrdinal)
		return []Change{
			{Type: store.OpPut, Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
			{Type: store.OpPut, Key: types.KeyObservedInstanceService(instanceID), Value: []byte(serviceName)},
			{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstancePending))},
			{Type: store.OpPut, Key: types.KeyObservedInstanceOrdinal(instanceID), Value: []byte(strconv.Itoa(nextOrdinal))},
		}
	}
	return nil
}

// stopHighestOrdinalInstances marks the highest-ordinal instances as stopped,
// removing them in reverse ordinal order for clean scale-down of stateful
// services.
func stopHighestOrdinalInstances(serviceName string, existingOrdinals []int, count int) []Change {
	var changes []Change
	for removeIndex := len(existingOrdinals) - 1; removeIndex >= 0 && len(changes) < count; removeIndex-- {
		instanceID := fmt.Sprintf("%s-%d", serviceName, existingOrdinals[removeIndex])
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceState(instanceID),
			Value: []byte(string(types.InstanceStopped)),
		})
	}
	return changes
}

// createPendingInstances generates Change entries that create the given number
// of new instances for a stateless service, each in the "pending" state.
// Every new instance gets three facts: a marker key, a service association,
// and a state.
func (instanceController *InstanceController) createPendingInstances(service string, count int) []Change {
	var changes []Change
	for range count {
		instanceID := instanceController.NewID()
		changes = append(changes,
			Change{Type: store.OpPut, Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceService(instanceID), Value: []byte(service)},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstancePending))},
		)
	}
	return changes
}

// markExcessStatelessInstancesAsStopped produces Change entries that set the
// state of excess instances to "stopped". It prefers stopping pending instances
// over running ones to minimize disruption.
func markExcessStatelessInstancesAsStopped(ids []string, states map[string]types.InstanceState, count int) []Change {
	var pendingIDs, runningIDs []string
	for _, instanceID := range ids {
		switch states[instanceID] {
		case types.InstancePending:
			pendingIDs = append(pendingIDs, instanceID)
		default:
			runningIDs = append(runningIDs, instanceID)
		}
	}
	instancesToRemove := selectInstancesForRemoval(pendingIDs, runningIDs, count)

	var changes []Change
	for _, instanceID := range instancesToRemove {
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceState(instanceID),
			Value: []byte(string(types.InstanceStopped)),
		})
	}
	return changes
}

// selectInstancesForRemoval picks up to count instance IDs for removal,
// draining from pendingIDs first and then from runningIDs. This minimizes
// disruption by preferring instances that have not yet started doing work.
func selectInstancesForRemoval(pendingIDs, runningIDs []string, count int) []string {
	var result []string
	for _, instanceID := range pendingIDs {
		if len(result) >= count {
			break
		}
		result = append(result, instanceID)
	}
	for _, instanceID := range runningIDs {
		if len(result) >= count {
			break
		}
		result = append(result, instanceID)
	}
	return result
}

// sortedMapKeys returns the keys of a string-keyed map sorted alphabetically.
func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
