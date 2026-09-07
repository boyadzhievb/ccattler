package controllers

import (
	"context"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// InstanceController reconciles the desired instance count for each service
// against the actually observed instances. When there are fewer active
// instances than desired it creates new pending instances; when there are
// more it marks excess instances as stopped, preferring to remove pending
// instances before running ones.
type InstanceController struct {
	// NewID is a function that generates unique instance identifiers.
	// It defaults to types.NewInstanceID but can be replaced in tests
	// for deterministic output.
	NewID types.IDFunc
}

// NewInstanceController returns an InstanceController wired to the default
// ID generator.
func NewInstanceController() *InstanceController {
	return &InstanceController{NewID: types.NewInstanceID}
}

// Name returns "instance", identifying this controller in logs and runner
// bookkeeping.
func (ctrl *InstanceController) Name() string { return "instance" }

// Watch returns the fact prefixes that drive instance reconciliation:
// effective service definitions (which carry the desired instance count) and
// observed instances (which represent what actually exists).
func (ctrl *InstanceController) Watch() []string {
	return []string{
		types.ScanEffectiveServices,
		types.ScanObservedInstances,
	}
}

// Reconcile compares desired instance counts (from effective service facts)
// against active (non-stopped) observed instances and emits changes to
// create or stop instances until the counts match.
func (ctrl *InstanceController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	desiredCounts := make(map[string]int)              // service name -> desired instance count
	observedEntries := make(map[string][]instanceEntry) // service name -> active instances

	for _, fact := range facts {
		switch {
		case strings.HasPrefix(fact.Key, types.ScanEffectiveServices):
			relativePath := strings.TrimPrefix(fact.Key, types.ScanEffectiveServices)
			// relativePath = "{name}/instances"
			pathParts := strings.SplitN(relativePath, "/", 2)
			if len(pathParts) == 2 && pathParts[1] == "instances" {
				parsedCount, _ := strconv.Atoi(string(fact.Value))
				desiredCounts[pathParts[0]] = parsedCount
			}

		case strings.HasPrefix(fact.Key, types.ScanObservedInstances):
			relativePath := strings.TrimPrefix(fact.Key, types.ScanObservedInstances)
			pathParts := strings.SplitN(relativePath, "/", 2)
			if len(pathParts) != 2 {
				continue
			}
			instanceID := pathParts[0]
			fieldSuffix := pathParts[1]

			switch fieldSuffix {
			case "service":
				serviceName := string(fact.Value)
				entries := observedEntries[serviceName]
				matchIndex := findOrAddInstanceEntry(&entries, instanceID)
				entries[matchIndex].service = serviceName
				observedEntries[serviceName] = entries
			case "state":
				// We need to associate state with the instance, but we don't
				// know the service yet. Use a separate pass.
			}
		}
	}

	// Second pass: collect instance states by ID, then filter.
	stateByInstanceID := make(map[string]types.InstanceState)
	serviceByInstanceID := make(map[string]string)
	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, types.ScanObservedInstances) {
			continue
		}
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

	// Build active instance counts per service (pending + running count as active).
	activeCount := make(map[string]int)
	activeInstanceIDs := make(map[string][]string) // service name -> instance IDs
	for instanceID, serviceName := range serviceByInstanceID {
		state := stateByInstanceID[instanceID]
		if state == types.InstanceStopped {
			continue
		}
		activeCount[serviceName]++
		activeInstanceIDs[serviceName] = append(activeInstanceIDs[serviceName], instanceID)
	}

	var changes []Change

	for serviceName, wantCount := range desiredCounts {
		haveCount := activeCount[serviceName]
		if haveCount < wantCount {
			changes = append(changes, ctrl.createPendingInstances(serviceName, wantCount-haveCount)...)
		} else if haveCount > wantCount {
			changes = append(changes, ctrl.markExcessInstancesAsStopped(serviceName, activeInstanceIDs[serviceName], stateByInstanceID, haveCount-wantCount)...)
		}
	}

	return changes, nil
}

// createPendingInstances generates Change entries that create the given number
// of new instances for a service, each in the "pending" state. Every new
// instance gets three facts: a marker key, a service association, and a state.
func (ctrl *InstanceController) createPendingInstances(service string, count int) []Change {
	var changes []Change
	for range count {
		instanceID := ctrl.NewID()
		changes = append(changes,
			Change{Type: store.OpPut, Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceService(instanceID), Value: []byte(service)},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstancePending))},
		)
	}
	return changes
}

// markExcessInstancesAsStopped produces Change entries that set the state of
// excess instances to "stopped". It prefers stopping pending instances over
// running ones to minimize disruption.
func (ctrl *InstanceController) markExcessInstancesAsStopped(service string, ids []string, states map[string]types.InstanceState, count int) []Change {
	// Prefer removing pending instances over running ones.
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
		changes = append(changes,
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstanceStopped))},
		)
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

// instanceEntry holds the parsed identity of an observed instance during
// the first pass of fact scanning. It associates an instance ID with the
// service it belongs to.
type instanceEntry struct {
	// id is the unique instance identifier (e.g. "inst-001").
	id string
	// service is the name of the service this instance belongs to (e.g. "web").
	service string
}

// findOrAddInstanceEntry searches the entries slice for an instanceEntry with
// the given id. If found, it returns the index; otherwise it appends a new
// entry and returns its index. The slice pointer is updated in place.
func findOrAddInstanceEntry(entries *[]instanceEntry, id string) int {
	for i, existing := range *entries {
		if existing.id == id {
			return i
		}
	}
	*entries = append(*entries, instanceEntry{id: id})
	return len(*entries) - 1
}
