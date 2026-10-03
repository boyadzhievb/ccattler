package controllers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

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
}

// NewInstanceController returns an InstanceController wired to the default
// ID generator.
func NewInstanceController() *InstanceController {
	return &InstanceController{NewID: types.NewInstanceID}
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
	sortedServiceNames := sortedMapKeys(desiredCounts)

	for _, serviceName := range sortedServiceNames {
		wantCount := desiredCounts[serviceName]
		activeIDs := activeInstanceIDs[serviceName]
		haveCount := len(activeIDs)

		if statefulServices[serviceName] {
			changes = append(changes, instanceController.reconcileStatefulService(
				serviceName, wantCount, activeIDs, stateByInstanceID,
			)...)
		} else {
			changes = append(changes, instanceController.reconcileStatelessService(
				serviceName, wantCount, haveCount, activeIDs, stateByInstanceID,
			)...)
		}
	}

	return changes, nil
}

// parseEffectiveServiceFacts extracts desired instance counts and stateful
// flags from effective service facts.
func parseEffectiveServiceFacts(facts []store.Fact) (map[string]int, map[string]bool) {
	desiredCounts := make(map[string]int)
	statefulServices := make(map[string]bool)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanEffectiveServices) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanEffectiveServices)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) != 2 {
			continue
		}
		switch pathParts[1] {
		case "instances":
			parsedCount, _ := strconv.Atoi(string(fact.Value))
			desiredCounts[pathParts[0]] = parsedCount
		case "stateful":
			statefulServices[pathParts[0]] = string(fact.Value) == "true"
		}
	}

	return desiredCounts, statefulServices
}

// parseObservedInstanceFacts extracts instance states and service associations
// from observed instance facts.
func parseObservedInstanceFacts(facts []store.Fact) (map[string]types.InstanceState, map[string]string) {
	stateByInstanceID := make(map[string]types.InstanceState)
	serviceByInstanceID := make(map[string]string)

	for _, fact := range store.FactsWithPrefix(facts, types.ScanObservedInstances) {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
		pathParts := strings.SplitN(relativePath, "/", 2)
		if len(pathParts) != 2 {
			continue
		}
		instanceID := pathParts[0]
		switch pathParts[1] {
		case "state":
			stateByInstanceID[instanceID] = types.InstanceState(fact.Value)
		case "service":
			serviceByInstanceID[instanceID] = string(fact.Value)
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

// reconcileStatelessService handles instance count reconciliation for
// stateless services using random IDs and pending-first removal.
func (instanceController *InstanceController) reconcileStatelessService(
	serviceName string, wantCount int, haveCount int,
	activeIDs []string, stateByInstanceID map[string]types.InstanceState,
) []Change {
	if haveCount < wantCount {
		return instanceController.createPendingInstances(serviceName, wantCount-haveCount)
	}
	if haveCount > wantCount {
		return markExcessStatelessInstancesAsStopped(activeIDs, stateByInstanceID, haveCount-wantCount)
	}
	return nil
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
