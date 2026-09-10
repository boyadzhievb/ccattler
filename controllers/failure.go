package controllers

import (
	"context"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// FailureController watches for instances that need replacement and
// handles three failure scenarios:
//   - Instance state is "failed" (runtime crash or start failure)
//   - Liveness probe state is "unhealthy" (stuck process detected)
//   - Startup probe state is "failed" (exceeded failure threshold)
//
// For each, the failed instance is marked as stopped and a new pending
// instance is created for the same service.
type FailureController struct {
	// NewID is a function that generates unique instance identifiers.
	// It defaults to types.NewInstanceID but can be replaced in tests
	// for deterministic output.
	NewID types.IDFunc
}

// NewFailureController returns a FailureController wired to the default
// ID generator.
func NewFailureController() *FailureController {
	return &FailureController{NewID: types.NewInstanceID}
}

// Name returns "failure", identifying this controller in logs and runner
// bookkeeping.
func (failureController *FailureController) Name() string { return "failure" }

// Watch returns the fact prefix for observed instances, which is the only
// prefix the failure controller needs to detect failed instances.
func (failureController *FailureController) Watch() []string {
	return []string{
		types.ScanObservedInstances,
	}
}

// Reconcile scans observed instance facts for any instance in the "failed"
// state. For each failed instance it emits two groups of changes: one to
// mark the failed instance as stopped, and another to create a replacement
// instance in the "pending" state for the same service.
func (failureController *FailureController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	// Parse instance fields: instanceID -> {field -> value}.
	instanceFields := make(map[string]map[string]string)
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
		if instanceFields[instanceID] == nil {
			instanceFields[instanceID] = make(map[string]string)
		}
		instanceFields[instanceID][pathParts[1]] = string(fact.Value)
	}

	var changes []Change

	for instanceID, fields := range instanceFields {
		instanceState := types.InstanceState(fields["state"])
		serviceName := fields["service"]
		if serviceName == "" {
			continue
		}

		needsReplacement := false

		if instanceState == types.InstanceFailed {
			needsReplacement = true
		}

		if instanceState == types.InstanceRunning {
			if fields["probe/liveness"] == string(types.LivenessProbeUnhealthy) {
				needsReplacement = true
			}
			if fields["probe/startup"] == string(types.StartupProbeFailed) {
				needsReplacement = true
			}
		}

		if !needsReplacement {
			continue
		}

		// Mark the failed instance as stopped.
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceState(instanceID),
			Value: []byte(string(types.InstanceStopped)),
		})

		// Create a replacement instance in pending state.
		replacementID := failureController.NewID()
		changes = append(changes,
			Change{Type: store.OpPut, Key: types.KeyObservedInstance(replacementID), Value: []byte("")},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceService(replacementID), Value: []byte(serviceName)},
			Change{Type: store.OpPut, Key: types.KeyObservedInstanceState(replacementID), Value: []byte(string(types.InstancePending))},
		)
	}

	return changes, nil
}
