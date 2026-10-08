// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// DefaultDrainGracePeriod is the time an instance remains draining (removed
// from endpoints) before it is stopped and replaced.
const DefaultDrainGracePeriod = 5 * time.Second

// defaultMaxReplacementsPerCycle limits how many failed instances the
// FailureController replaces in a single reconciliation cycle. Each
// replacement emits 4 store operations, so 10 replacements = 40 ops —
// safely under the 128-op etcd transaction limit. Remaining failures
// converge in subsequent cycles via watch re-trigger.
const defaultMaxReplacementsPerCycle = 10

// defaultMaxDrainsPerCycle limits how many instances the FailureController
// begins draining in a single reconciliation cycle. Each drain emits 2
// store operations (readiness + drain_since), so 10 drains = 20 ops.
// Combined with max replacements (40 ops), the total stays within the
// runner's 60-change transaction budget.
const defaultMaxDrainsPerCycle = 10

// defaultMaxRecoveriesPerCycle limits how many drain-recovery cleanups the
// FailureController emits per cycle. Each recovery emits 2 delete operations
// (drain_readiness + drain_since). Without a cap, mass recovery (e.g. 200
// instances simultaneously regaining liveness) would exceed the transaction
// budget and cause permanent rejection stalls.
const defaultMaxRecoveriesPerCycle = 10

// maxFailureControllerChangesPerCycle is the hard total output cap. The
// per-type caps (replacements, drains, recoveries) are independent and
// their worst-case sum can exceed 60. This final guard truncates the
// combined output to fit the runner's transaction budget.
const maxFailureControllerChangesPerCycle = 58

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

	// MaxReplacementsPerCycle limits how many failed instances are replaced
	// in a single reconciliation cycle. Each replacement emits 4 store
	// operations (stop old + create new root/service/state), so this cap
	// prevents transaction overflow under mass-failure scenarios. Remaining
	// failures converge in subsequent cycles via watch re-trigger.
	MaxReplacementsPerCycle int

	// MaxDrainsPerCycle limits how many instances can begin draining in a
	// single reconciliation cycle. Each drain emits 2 store operations
	// (readiness + drain_since). Combined with MaxReplacementsPerCycle,
	// this keeps the total change count within the runner's transaction budget.
	MaxDrainsPerCycle int

	// MaxRecoveriesPerCycle limits how many drain-recovery cleanups are
	// emitted per cycle. Each recovery deletes drain_readiness + drain_since
	// (2 ops). Prevents transaction overflow during mass recovery events.
	MaxRecoveriesPerCycle int
}

// NewFailureController returns a FailureController wired to the default
// ID generator and clock.
func NewFailureController() *FailureController {
	return &FailureController{
		NewID:                   types.NewInstanceID,
		NowFunc:                 time.Now,
		DrainGracePeriod:        DefaultDrainGracePeriod,
		MaxReplacementsPerCycle: defaultMaxReplacementsPerCycle,
		MaxDrainsPerCycle:       defaultMaxDrainsPerCycle,
		MaxRecoveriesPerCycle:   defaultMaxRecoveriesPerCycle,
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
		types.ScanDerivedInstances,
	}
}

// Reconcile scans observed instance facts and handles three scenarios:
//  1. Instance state "failed" — immediate stop and replace
//  2. Liveness unhealthy on running instance — begin graceful drain (or complete
//     it if the grace period has elapsed)
//  3. Startup failed on running instance — immediate stop and replace
func (failureController *FailureController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	instanceFields := parseInstanceFieldsFromFacts(facts)

	now := failureController.NowFunc()
	var changes []Change
	replacementCount := 0
	drainCount := 0
	recoveryCount := 0

	// Iterate instances in sorted order for deterministic output.
	sortedInstanceIDs := sortedMapKeys(instanceFields)
	for _, instanceID := range sortedInstanceIDs {
		fields := instanceFields[instanceID]
		instanceState := effectiveInstanceState(fields)
		serviceName := fields["service"]
		if serviceName == "" {
			continue
		}

		// Case 1: instance already failed — immediate replacement.
		if instanceState == types.InstanceFailed {
			if replacementCount >= failureController.MaxReplacementsPerCycle {
				continue
			}
			changes = append(changes, failureController.stopAndReplace(instanceID, serviceName)...)
			replacementCount++
			continue
		}

		if instanceState != types.InstanceRunning {
			continue
		}

		// Case 2: startup failed — immediate replacement (no drain needed,
		// the instance never served traffic).
		if fields["probe/startup"] == string(types.StartupProbeFailed) {
			if replacementCount >= failureController.MaxReplacementsPerCycle {
				continue
			}
			changes = append(changes, failureController.stopAndReplace(instanceID, serviceName)...)
			replacementCount++
			continue
		}

		// Case 3: liveness recovered while draining — clear stale drain state.
		drainSince := fields["drain_since"]
		if drainSince != "" && fields["probe/liveness"] != string(types.LivenessProbeUnhealthy) {
			if recoveryCount >= failureController.MaxRecoveriesPerCycle {
				continue
			}
			changes = append(changes, failureController.clearDrainState(instanceID)...)
			recoveryCount++
			continue
		}

		// Case 4: liveness unhealthy — graceful drain.
		if fields["probe/liveness"] == string(types.LivenessProbeUnhealthy) {
			if drainSince == "" {
				if drainCount >= failureController.MaxDrainsPerCycle {
					continue
				}
				changes = append(changes, failureController.beginDrain(instanceID, now)...)
				drainCount++
			} else {
				drainStartMillis, parseErr := strconv.ParseInt(drainSince, 10, 64)
				if parseErr != nil {
					if replacementCount >= failureController.MaxReplacementsPerCycle {
						continue
					}
					changes = append(changes, failureController.stopAndReplace(instanceID, serviceName)...)
					replacementCount++
					continue
				}
				drainStartTime := time.UnixMilli(drainStartMillis)
				if now.Sub(drainStartTime) >= failureController.DrainGracePeriod {
					if replacementCount >= failureController.MaxReplacementsPerCycle {
						continue
					}
					changes = append(changes, failureController.stopAndReplace(instanceID, serviceName)...)
					replacementCount++
				}
			}
		}
	}

	if len(changes) > maxFailureControllerChangesPerCycle {
		var deferredCount int
		changes, deferredCount = takeWholeGroups(changes, maxFailureControllerChangesPerCycle)
		if deferredCount > 0 {
			logging.Default().Warn("failure controller output capped",
				"committed_changes", fmt.Sprintf("%d", len(changes)),
				"deferred_changes", fmt.Sprintf("%d", deferredCount))
		}
	}

	return changes, nil
}

// beginDrain emits grouped changes that mark an instance as draining: sets
// readiness to not-ready (removing it from endpoints) and records the drain
// start time.
func (failureController *FailureController) beginDrain(instanceID string, now time.Time) []Change {
	return groupedChanges("drain/"+instanceID,
		Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedInstanceDrainReadiness(instanceID),
			Value: []byte(string(types.ReadinessProbeNotReady)),
		},
		Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedInstanceDrainSince(instanceID),
			Value: []byte(fmt.Sprintf("%d", now.UnixMilli())),
		},
	)
}

// clearDrainState emits grouped delete changes for drain_readiness and
// drain_since, cleaning up stale drain state when liveness recovers.
func (failureController *FailureController) clearDrainState(instanceID string) []Change {
	return groupedChanges("drain-clear/"+instanceID,
		Change{Type: store.OpDelete, Key: types.KeyDerivedInstanceDrainReadiness(instanceID)},
		Change{Type: store.OpDelete, Key: types.KeyDerivedInstanceDrainSince(instanceID)},
	)
}

// stopAndReplace marks a failed instance via derived/ and creates a grouped
// pending replacement in observed/ for the same service.
func (failureController *FailureController) stopAndReplace(instanceID string, serviceName string) []Change {
	replacementID := failureController.NewID()
	return groupedChanges("replace/"+instanceID,
		Change{Type: store.OpPut, Key: types.KeyDerivedInstanceControllerStopped(instanceID), Value: []byte("true")},
		Change{Type: store.OpPut, Key: types.KeyObservedInstance(replacementID), Value: []byte("")},
		Change{Type: store.OpPut, Key: types.KeyObservedInstanceService(replacementID), Value: []byte(serviceName)},
		Change{Type: store.OpPut, Key: types.KeyObservedInstanceState(replacementID), Value: []byte(string(types.InstancePending))},
	)
}
