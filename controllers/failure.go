package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// DefaultDrainGracePeriod is the time an instance remains draining (removed
// from endpoints) before it is stopped and replaced.
const DefaultDrainGracePeriod = 5 * time.Second

// FailureController watches for instances that need replacement and
// handles three failure scenarios:
//   - Instance state is "failed" (runtime crash) — immediate replacement
//   - Liveness probe state is "unhealthy" (stuck process) — graceful drain then replace
//   - Startup probe state is "failed" (exceeded threshold) — immediate replacement
//
// Probe-triggered failures on running instances go through a graceful drain:
// the controller first marks the instance's readiness as not-ready (removing
// it from endpoints) and records a drain timestamp. On the next reconciliation,
// once the grace period has elapsed, the instance is stopped and replaced.
type FailureController struct {
	// NewID is a function that generates unique instance identifiers.
	// It defaults to types.NewInstanceID but can be replaced in tests
	// for deterministic output.
	NewID types.IDFunc

	// NowFunc returns the current time. Defaults to time.Now but can be
	// overridden in tests for deterministic drain timing.
	NowFunc func() time.Time

	// DrainGracePeriod is how long to wait after marking an instance as
	// draining before stopping it. Defaults to DefaultDrainGracePeriod.
	DrainGracePeriod time.Duration
}

// NewFailureController returns a FailureController wired to the default
// ID generator and clock.
func NewFailureController() *FailureController {
	return &FailureController{
		NewID:            types.NewInstanceID,
		NowFunc:          time.Now,
		DrainGracePeriod: DefaultDrainGracePeriod,
	}
}

// Name returns "failure", identifying this controller in logs and runner
// bookkeeping.
func (failureController *FailureController) Name() string { return "failure" }

// Watch returns the fact prefix for observed instances, which is the only
// prefix the failure controller needs to detect failed instances and drain
// timestamps.
func (failureController *FailureController) Watch() []string {
	return []string{
		types.ScanObservedInstances,
	}
}

// Reconcile scans observed instance facts and handles three scenarios:
//  1. Instance state "failed" — immediate stop and replace
//  2. Liveness unhealthy on running instance — begin graceful drain (or complete
//     it if the grace period has elapsed)
//  3. Startup failed on running instance — immediate stop and replace
func (failureController *FailureController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
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

	now := failureController.NowFunc()
	var changes []Change

	for instanceID, fields := range instanceFields {
		instanceState := types.InstanceState(fields["state"])
		serviceName := fields["service"]
		if serviceName == "" {
			continue
		}

		// Case 1: instance already failed — immediate replacement.
		if instanceState == types.InstanceFailed {
			changes = append(changes, failureController.stopAndReplace(instanceID, serviceName)...)
			continue
		}

		if instanceState != types.InstanceRunning {
			continue
		}

		// Case 2: startup failed — immediate replacement (no drain needed,
		// the instance never served traffic).
		if fields["probe/startup"] == string(types.StartupProbeFailed) {
			changes = append(changes, failureController.stopAndReplace(instanceID, serviceName)...)
			continue
		}

		// Case 3: liveness unhealthy — graceful drain.
		if fields["probe/liveness"] == string(types.LivenessProbeUnhealthy) {
			drainSince := fields["drain_since"]
			if drainSince == "" {
				changes = append(changes, failureController.beginDrain(instanceID, now)...)
			} else {
				drainStartMillis, parseErr := strconv.ParseInt(drainSince, 10, 64)
				if parseErr != nil {
					changes = append(changes, failureController.stopAndReplace(instanceID, serviceName)...)
					continue
				}
				drainStartTime := time.UnixMilli(drainStartMillis)
				if now.Sub(drainStartTime) >= failureController.DrainGracePeriod {
					changes = append(changes, failureController.stopAndReplace(instanceID, serviceName)...)
				}
			}
		}
	}

	return changes, nil
}

// beginDrain emits changes that mark an instance as draining: sets readiness
// to not-ready (removing it from endpoints) and records the drain start time.
func (failureController *FailureController) beginDrain(instanceID string, now time.Time) []Change {
	return []Change{
		{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceProbeState(instanceID, "readiness"),
			Value: []byte(string(types.ReadinessProbeNotReady)),
		},
		{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceDrainSince(instanceID),
			Value: []byte(fmt.Sprintf("%d", now.UnixMilli())),
		},
	}
}

// stopAndReplace emits changes that mark a failed instance as stopped and
// create a new pending replacement instance for the same service.
func (failureController *FailureController) stopAndReplace(instanceID string, serviceName string) []Change {
	replacementID := failureController.NewID()
	return []Change{
		{Type: store.OpPut, Key: types.KeyObservedInstanceState(instanceID), Value: []byte(string(types.InstanceStopped))},
		{Type: store.OpPut, Key: types.KeyObservedInstance(replacementID), Value: []byte("")},
		{Type: store.OpPut, Key: types.KeyObservedInstanceService(replacementID), Value: []byte(serviceName)},
		{Type: store.OpPut, Key: types.KeyObservedInstanceState(replacementID), Value: []byte(string(types.InstancePending))},
	}
}
