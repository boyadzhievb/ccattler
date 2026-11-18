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
// emits 4 store operations (marker, service, state, recorded image), so
// 14 creations = 56 ops — near the 58-change hard cap. Remaining deficit
// converges in subsequent cycles.
const defaultMaxInstanceCreationsPerCycle = 14

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
// stateful flag), desired services (the image recorded for each new instance,
// M241 D1) and observed instances (which represent what actually exists).
func (instanceController *InstanceController) Watch() []string {
	return []string{
		types.ScanEffectiveServices,
		types.ScanDesiredServices,
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
		types.ScanDerivedServices,
	}
}

// Reconcile compares desired instance counts (from effective service facts)
// against active (non-stopped) observed instances and emits changes to
// create or stop instances until the counts match. Stateful services use
// ordinal IDs and ordered startup; stateless services use random IDs.
func (instanceController *InstanceController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	desiredCounts, statefulServices, perNodeServices := parseEffectiveServiceFacts(facts)
	fieldsByInstanceID := parseInstanceFieldsFromFacts(facts)
	creation := instanceCreationFacts{desiredImages: extractDesiredImages(facts), fieldsByInstanceID: fieldsByInstanceID}
	stateByInstanceID, serviceByInstanceID := buildInstanceMapsFromFields(fieldsByInstanceID)
	activeInstanceIDs := buildActiveInstanceIDsByService(serviceByInstanceID, stateByInstanceID)
	for serviceName, surgeCount := range extractSurgeAllowances(facts) {
		if statefulServices[serviceName] || perNodeServices[serviceName] {
			continue
		}
		oldImageCount := countOldImageInstances(activeInstanceIDs[serviceName], creation.fieldsByInstanceID, creation.desiredImages[serviceName])
		desiredCounts[serviceName] += min(surgeCount, oldImageCount)
	}

	var changes []Change
	creationsRemaining := instanceController.MaxCreationsPerCycle
	sortedServiceNames := sortedMapKeys(desiredCounts)

	scaleDownChanges := instanceController.reconcileScaleDowns(
		sortedServiceNames, desiredCounts, activeInstanceIDs, stateByInstanceID, statefulServices,
		perNodeServices, fieldsByInstanceID,
	)
	changes = append(changes, scaleDownChanges...)

	serviceDeficits := computeServiceDeficits(
		sortedServiceNames, desiredCounts, activeInstanceIDs, stateByInstanceID, creation, statefulServices,
	)
	fairShareChanges := instanceController.allocateCreationsFairly(
		serviceDeficits, statefulServices, stateByInstanceID, creation, &creationsRemaining,
	)
	changes = append(changes, fairShareChanges...)

	if len(changes) > maxInstanceControllerChangesPerCycle {
		var deferredCount int
		var groupError error
		changes, deferredCount, groupError = takeWholeGroups(changes, maxInstanceControllerChangesPerCycle)
		if groupError != nil {
			return nil, groupError
		}
		if deferredCount > 0 {
			logging.Default().Warn("instance controller output capped",
				"committed_changes", fmt.Sprintf("%d", len(changes)),
				"deferred_changes", fmt.Sprintf("%d", deferredCount))
		}
	}

	return changes, nil
}

// serviceDeficit tracks the remaining creation demand for a single service.
type serviceDeficit struct {
	serviceName string
	deficit     int
}

// reconcileScaleDowns collects stop-changes for all services where the active
// count exceeds the desired count. Scale-downs are not subject to the creation
// budget and are processed in full each cycle.
func (instanceController *InstanceController) reconcileScaleDowns(
	sortedServiceNames []string,
	desiredCounts map[string]int,
	activeInstanceIDs map[string][]string,
	stateByInstanceID map[string]types.InstanceState,
	statefulServices map[string]bool,
	perNodeServices map[string]bool,
	fieldsByInstanceID map[string]map[string]string,
) []Change {
	var changes []Change
	for _, serviceName := range sortedServiceNames {
		wantCount := desiredCounts[serviceName]
		activeIDs := activeInstanceIDs[serviceName]
		haveCount := len(activeIDs)
		if haveCount > wantCount {
			excessCount := haveCount - wantCount
			logging.Default().Info("scaling down service",
				"service", serviceName,
				"have", strconv.Itoa(haveCount),
				"want", strconv.Itoa(wantCount),
				"stopping", strconv.Itoa(excessCount))
			switch {
			case perNodeServices[serviceName]:
				changes = append(changes, markPerNodeExcessInstancesAsStopped(
					activeIDs, fieldsByInstanceID, excessCount,
				)...)
			case statefulServices[serviceName]:
				changes = append(changes, stopHighestOrdinalInstances(
					serviceName, parseExistingOrdinals(serviceName, activeIDs), excessCount,
				)...)
			default:
				changes = append(changes, markExcessStatelessInstancesAsStopped(
					activeIDs, stateByInstanceID, excessCount,
				)...)
			}
		}
	}
	return changes
}

// computeServiceDeficits returns a list of services that need more instances
// along with their deficit counts. Services with zero or negative deficit are
// excluded. Stateful services contribute at most 1 per cycle (ordered startup).
func computeServiceDeficits(
	sortedServiceNames []string,
	desiredCounts map[string]int,
	activeInstanceIDs map[string][]string,
	stateByInstanceID map[string]types.InstanceState,
	creation instanceCreationFacts,
	statefulServices map[string]bool,
) []serviceDeficit {
	var deficits []serviceDeficit
	for _, serviceName := range sortedServiceNames {
		wantCount := desiredCounts[serviceName]
		activeIDs := activeInstanceIDs[serviceName]
		haveCount := len(activeIDs)
		if haveCount >= wantCount {
			continue
		}
		if statefulServices[serviceName] {
			existingOrdinals := parseExistingOrdinals(serviceName, activeIDs)
			nextChanges := createNextStatefulInstance(serviceName, wantCount, existingOrdinals, stateByInstanceID, creation)
			if len(nextChanges) > 0 {
				deficits = append(deficits, serviceDeficit{serviceName: serviceName, deficit: 1})
			}
		} else {
			deficits = append(deficits, serviceDeficit{serviceName: serviceName, deficit: wantCount - haveCount})
		}
	}
	return deficits
}

// allocateCreationsFairly distributes the per-cycle creation budget across all
// services with outstanding deficits. Each service receives an equal base share
// (budget / serviceCount), capped at its deficit. Remainder slots are assigned
// one at a time to the service with the largest unmet demand; ties are broken
// by highest index so lower-indexed services receive fewer slots in one cycle
// but accumulate larger deficits that earn them priority in subsequent cycles.
func (instanceController *InstanceController) allocateCreationsFairly(
	deficits []serviceDeficit,
	statefulServices map[string]bool,
	stateByInstanceID map[string]types.InstanceState,
	creation instanceCreationFacts,
	creationsRemaining *int,
) []Change {
	var changes []Change
	if len(deficits) == 0 || *creationsRemaining <= 0 {
		return changes
	}

	allocated := make([]int, len(deficits))
	budget := *creationsRemaining
	serviceCount := len(deficits)

	baseShare := budget / serviceCount
	for deficitIndex, entry := range deficits {
		if baseShare > entry.deficit {
			allocated[deficitIndex] = entry.deficit
		} else {
			allocated[deficitIndex] = baseShare
		}
	}

	budgetUsed := 0
	for _, allocationCount := range allocated {
		budgetUsed += allocationCount
	}
	remainingBudget := budget - budgetUsed

	for remainingBudget > 0 {
		bestIndex := -1
		bestUnmetDemand := 0
		for deficitIndex, entry := range deficits {
			unmetDemand := entry.deficit - allocated[deficitIndex]
			if unmetDemand > bestUnmetDemand || (unmetDemand == bestUnmetDemand && unmetDemand > 0) {
				bestUnmetDemand = unmetDemand
				bestIndex = deficitIndex
			}
		}
		if bestIndex < 0 {
			break
		}
		allocated[bestIndex]++
		remainingBudget--
	}

	totalUsed := 0
	for _, allocationCount := range allocated {
		totalUsed += allocationCount
	}
	*creationsRemaining = budget - totalUsed

	for deficitIndex, entry := range deficits {
		if allocated[deficitIndex] <= 0 {
			continue
		}
		logging.Default().Info("scaling up service",
			"service", entry.serviceName,
			"creating", strconv.Itoa(allocated[deficitIndex]),
			"deficit", strconv.Itoa(entry.deficit))
		if statefulServices[entry.serviceName] {
			activeIDs := buildActiveIDsFromFields(entry.serviceName, creation.fieldsByInstanceID, stateByInstanceID)
			existingOrdinals := parseExistingOrdinals(entry.serviceName, activeIDs)
			nextChanges := createNextStatefulInstance(entry.serviceName,
				len(existingOrdinals)+allocated[deficitIndex],
				existingOrdinals, stateByInstanceID, creation)
			changes = append(changes, nextChanges...)
		} else {
			changes = append(changes, instanceController.createPendingInstances(
				entry.serviceName, allocated[deficitIndex], creation,
			)...)
		}
	}
	return changes
}

// buildActiveIDsFromFields reconstructs the sorted active instance IDs for a
// service from the fields map, used when the pre-computed map is not available.
func buildActiveIDsFromFields(
	serviceName string,
	fieldsByInstanceID map[string]map[string]string,
	stateByInstanceID map[string]types.InstanceState,
) []string {
	var activeIDs []string
	for instanceID, fields := range fieldsByInstanceID {
		if fields["service"] != serviceName {
			continue
		}
		state := stateByInstanceID[instanceID]
		if state != types.InstanceStopped {
			activeIDs = append(activeIDs, instanceID)
		}
	}
	sort.Strings(activeIDs)
	return activeIDs
}

// parseEffectiveServiceFacts extracts desired instance counts, stateful flags,
// and per-node placement modes from effective service facts.
func parseEffectiveServiceFacts(facts []store.Fact) (map[string]int, map[string]bool, map[string]bool) {
	desiredCounts := make(map[string]int)
	statefulServices := make(map[string]bool)
	perNodeServices := make(map[string]bool)

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
		case "placement_mode":
			perNodeServices[serviceName] = types.PlacementMode(fact.Value) == types.PlacementModePerNode
		}
	}

	return desiredCounts, statefulServices, perNodeServices
}

// countOldImageInstances counts the active instances that run (or were
// recorded with) another image than desiredImage. A rollout's surge never
// exceeds it, so the new instances never outnumber the expected count once the
// old ones stop and none is scaled down (M241 D14, review round 3 M1).
func countOldImageInstances(activeIDs []string, fieldsByInstanceID map[string]map[string]string, desiredImage string) int {
	var oldImageCount int
	for _, instanceID := range activeIDs {
		if instanceImage := fieldsByInstanceID[instanceID]["image"]; instanceImage != "" && instanceImage != desiredImage {
			oldImageCount++
		}
	}
	return oldImageCount
}

// extractSurgeAllowances reads rollout surge allowance facts emitted by the
// rollout controller. Returns a map of service name to surge count.
func extractSurgeAllowances(facts []store.Fact) map[string]int {
	allowances := make(map[string]int)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedServices) {
		serviceName, suffix, hasSuffix := splitFactKeyIntoEntityAndSuffix(fact.Key, types.ScanDerivedServices)
		if !hasSuffix || suffix != "rollout/surge_allowance" {
			continue
		}
		parsedValue, parseError := strconv.Atoi(string(fact.Value))
		if parseError != nil {
			logging.Default().Warn("corrupt surge allowance", "service", serviceName, "value", string(fact.Value))
			continue
		}
		allowances[serviceName] = parsedValue
	}
	return allowances
}

// buildInstanceMapsFromFields derives effective instance states and service
// associations from pre-parsed instance field maps.
func buildInstanceMapsFromFields(fieldsByInstanceID map[string]map[string]string) (map[string]types.InstanceState, map[string]string) {
	stateByInstanceID := make(map[string]types.InstanceState, len(fieldsByInstanceID))
	serviceByInstanceID := make(map[string]string, len(fieldsByInstanceID))

	for instanceID, fields := range fieldsByInstanceID {
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
// Stale derived markers (controller_stopped, node_failure) and the restart
// count of a previous incarnation of the same ordinal ID are deleted
// atomically with recreation, and the service's image is recorded (M241).
func createNextStatefulInstance(
	serviceName string, wantCount int,
	existingOrdinals []int, stateByInstanceID map[string]types.InstanceState,
	creation instanceCreationFacts,
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
		groupID := "instance-create/" + instanceID
		changes := groupedChanges(groupID,
			Change{Type: store.OpPut, Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceService(instanceID), Value: []byte(serviceName)},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstancePending))},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceOrdinal(instanceID), Value: []byte(strconv.Itoa(nextOrdinal))},
		)
		previousFields := creation.fieldsByInstanceID[instanceID]
		changes = append(changes, creation.buildIncarnationChanges(groupID, serviceName, instanceID)...)
		if previousFields["controller_stopped"] == "true" {
			changes = append(changes, Change{
				Type: store.OpDelete, Key: types.KeyDerivedInstanceControllerStopped(instanceID), Group: groupID,
			})
		}
		if previousFields["node_failure"] == "true" {
			changes = append(changes, Change{
				Type: store.OpDelete, Key: types.KeyDerivedInstanceNodeFailure(instanceID), Group: groupID,
			})
		}
		return changes
	}
	return nil
}

// stopHighestOrdinalInstances marks the highest-ordinal instances via the
// derived controller_stopped key, removing them in reverse ordinal order for
// clean scale-down of stateful services.
func stopHighestOrdinalInstances(serviceName string, existingOrdinals []int, count int) []Change {
	var changes []Change
	for removeIndex := len(existingOrdinals) - 1; removeIndex >= 0 && len(changes) < count; removeIndex-- {
		instanceID := fmt.Sprintf("%s-%d", serviceName, existingOrdinals[removeIndex])
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedInstanceControllerStopped(instanceID),
			Value: []byte("true"),
		})
	}
	return changes
}

// createPendingInstances generates Change entries that create the given number
// of new instances for a stateless service, each in the "pending" state.
// Every new instance gets grouped facts: a marker key, a service association,
// a state, and the service's image when known (M241 D1).
func (instanceController *InstanceController) createPendingInstances(service string, count int, creation instanceCreationFacts) []Change {
	var changes []Change
	for range count {
		instanceID := instanceController.NewID()
		groupID := "instance-create/" + instanceID
		changes = append(changes, groupedChanges(groupID,
			Change{Type: store.OpPut, Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceService(instanceID), Value: []byte(service)},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstancePending))},
		)...)
		changes = append(changes, creation.buildIncarnationChanges(groupID, service, instanceID)...)
	}
	return changes
}

// instanceCreationFacts holds what the instance controller needs to create an
// instance besides its count: the image to record and earlier instance fields.
type instanceCreationFacts struct {
	desiredImages      map[string]string            // desiredImages maps service name to desired/service/{svc}/image.
	fieldsByInstanceID map[string]map[string]string // fieldsByInstanceID holds the observed and derived fields of existing instance IDs.
}

// buildIncarnationChanges returns the creation-group changes that record the
// service's current image for the instance (D1) and delete the facts an earlier
// instance with the same ID left (D2, D13, review B4).
func (creation instanceCreationFacts) buildIncarnationChanges(groupID string, serviceName string, instanceID string) []Change {
	return buildInstanceIncarnationChanges(groupID, instanceID, creation.desiredImages[serviceName], creation.fieldsByInstanceID[instanceID])
}

// markExcessStatelessInstancesAsStopped produces Change entries that mark
// excess instances via the derived controller_stopped key. It prefers stopping
// pending instances over running ones to minimize disruption. Uses the derived
// marker instead of writing observed state directly, because the node agent
// continuously publishes the container's actual runtime state to the same
// observed key — writing "stopped" there races with the agent writing "running".
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
			Key:   types.KeyDerivedInstanceControllerStopped(instanceID),
			Value: []byte("true"),
		})
	}
	return changes
}

// markPerNodeExcessInstancesAsStopped produces Change entries for per-node
// services being scaled down. Instances on failed nodes (node_failure marker)
// are stopped first, then pending, then running — this ensures per-node
// scale-down removes the right instances when nodes leave the cluster.
func markPerNodeExcessInstancesAsStopped(
	activeIDs []string,
	fieldsByInstanceID map[string]map[string]string,
	count int,
) []Change {
	var failedNodeIDs, pendingIDs, runningIDs []string
	for _, instanceID := range activeIDs {
		fields := fieldsByInstanceID[instanceID]
		switch {
		case fields["node_failure"] == "true":
			failedNodeIDs = append(failedNodeIDs, instanceID)
		case fields["state"] == string(types.InstancePending):
			pendingIDs = append(pendingIDs, instanceID)
		default:
			runningIDs = append(runningIDs, instanceID)
		}
	}

	var instancesToStop []string
	for _, bucket := range [][]string{failedNodeIDs, pendingIDs, runningIDs} {
		for _, instanceID := range bucket {
			if len(instancesToStop) >= count {
				break
			}
			instancesToStop = append(instancesToStop, instanceID)
		}
	}

	var changes []Change
	for _, instanceID := range instancesToStop {
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedInstanceControllerStopped(instanceID),
			Value: []byte("true"),
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
