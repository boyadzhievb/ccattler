// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// DefaultDrainGracePeriod is the time an instance remains draining (removed
// from endpoints) before it is stopped and replaced.
const DefaultDrainGracePeriod = 5 * time.Second

// crashLoopBaseDelay is the initial backoff delay after the first rapid failure.
const crashLoopBaseDelay = 10 * time.Second

// crashLoopMaxDelay is the maximum backoff between crash-loop replacement retries.
const crashLoopMaxDelay = 5 * time.Minute

// crashLoopCounter is the JSON-serialized per-service crash-loop backoff state.
type crashLoopCounter struct {
	Count int   `json:"count"` // Count is the number of consecutive rapid failures.
	Since int64 `json:"since"` // Since is the Unix-millisecond timestamp of the last replacement.
}

// defaultMaxReplacementsPerCycle limits how many failed instances the
// FailureController replaces in a single reconciliation cycle. Each
// replacement emits 4 store operations, so 14 replacements = 56 ops —
// near the 58-change hard cap. Remaining failures converge in
// subsequent cycles via watch re-trigger.
const defaultMaxReplacementsPerCycle = 14

// defaultMaxDrainsPerCycle limits how many instances the FailureController
// begins draining in a single reconciliation cycle. Each drain emits 2
// store operations (readiness + drain_since), so 10 drains = 20 ops.
// The maxFailureControllerChangesPerCycle hard cap truncates the combined
// output if replacements + drains + recoveries exceed the budget.
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
		types.ScanDerivedCrashLoops,
	}
}

// Reconcile scans observed instance facts and handles three scenarios:
//  1. Instance state "failed" — immediate stop and replace
//  2. Liveness unhealthy on running instance — begin graceful drain (or complete
//     it if the grace period has elapsed)
//  3. Startup failed on running instance — immediate stop and replace
func (failureController *FailureController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	instanceFields := parseInstanceFieldsFromFacts(facts)
	crashLoopCounters := parseCrashLoopCounters(facts)

	now := failureController.NowFunc()
	var changes []Change
	cycleCounters := &failureCycleCounters{
		replacedServices: make(map[string]int),
	}

	failedServiceNames := make(map[string]bool)
	sortedInstanceIDs := sortedMapKeys(instanceFields)
	for _, instanceID := range sortedInstanceIDs {
		fields := instanceFields[instanceID]
		instanceChanges := failureController.processInstanceForRecovery(instanceID, fields, now, cycleCounters, crashLoopCounters)
		changes = append(changes, instanceChanges...)
		if effectiveInstanceState(fields) == types.InstanceFailed {
			failedServiceNames[fields["service"]] = true
		}
	}

	// Emit one crash-loop counter per service that had replacements this cycle.
	for serviceName, replacementCount := range cycleCounters.replacedServices {
		existingCount := 0
		if existing, hasCounter := crashLoopCounters[serviceName]; hasCounter {
			existingCount = existing.Count
		}
		counterJSON, _ := json.Marshal(crashLoopCounter{
			Count: existingCount + replacementCount,
			Since: now.UnixMilli(),
		})
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyDerivedCrashLoopCounter(serviceName),
			Value: counterJSON,
		})
	}
	// Reset crash-loop counters for services that have no failed instances.
	for serviceName := range crashLoopCounters {
		if !failedServiceNames[serviceName] {
			changes = append(changes, Change{
				Type: store.OpDelete,
				Key:  types.KeyDerivedCrashLoopCounter(serviceName),
			})
		}
	}

	return failureController.capOutputChanges(changes)
}

// failureCycleCounters tracks how many of each action type have been emitted
// in the current reconciliation cycle to enforce per-type caps.
type failureCycleCounters struct {
	replacements     int
	drains           int
	recoveries       int
	replacedServices map[string]int // per-service replacement count for crash-loop tracking
}

// processInstanceForRecovery evaluates a single instance and returns changes
// for failure replacement, drain initiation, drain completion, or recovery.
func (failureController *FailureController) processInstanceForRecovery(instanceID string, fields map[string]string, now time.Time, counters *failureCycleCounters, crashLoopCounters map[string]*crashLoopCounter) []Change {
	instanceState := effectiveInstanceState(fields)
	serviceName := fields["service"]
	if serviceName == "" {
		return nil
	}

	if instanceState == types.InstanceFailed {
		if counters.replacements >= failureController.MaxReplacementsPerCycle {
			return nil
		}

		if counter, hasCrashLoop := crashLoopCounters[serviceName]; hasCrashLoop {
			backoffDelay := calculateCrashLoopBackoff(counter.Count)
			retryAfter := time.UnixMilli(counter.Since).Add(backoffDelay)
			if now.Before(retryAfter) {
				logging.Default().Info("crash-loop-backoff, delaying replacement",
					"instance", instanceID, "service", serviceName,
					"attempt", strconv.Itoa(counter.Count),
					"retry_after", retryAfter.Format(time.RFC3339))
				return nil
			}
		}

		counters.replacements++
		counters.replacedServices[serviceName]++
		logging.Default().Info("replacing failed instance",
			"instance", instanceID, "service", serviceName, "reason", "runtime_crash")
		return failureController.stopAndReplace(instanceID, serviceName)
	}

	if instanceState != types.InstanceRunning {
		return nil
	}

	if fields["probe/startup"] == string(types.StartupProbeFailed) {
		if counters.replacements >= failureController.MaxReplacementsPerCycle {
			return nil
		}
		counters.replacements++
		counters.replacedServices[serviceName]++
		logging.Default().Info("replacing instance with failed startup probe",
			"instance", instanceID, "service", serviceName, "reason", "startup_probe_failed")
		return failureController.stopAndReplace(instanceID, serviceName)
	}

	drainSince := fields["drain_since"]
	if drainSince != "" && fields["probe/liveness"] != string(types.LivenessProbeUnhealthy) {
		if counters.recoveries >= failureController.MaxRecoveriesPerCycle {
			return nil
		}
		counters.recoveries++
		logging.Default().Info("instance liveness recovered, clearing drain",
			"instance", instanceID, "service", fields["service"])
		return failureController.clearDrainState(instanceID)
	}

	if fields["probe/liveness"] == string(types.LivenessProbeUnhealthy) {
		return failureController.handleUnhealthyLiveness(instanceID, serviceName, drainSince, now, counters)
	}

	return nil
}

// handleUnhealthyLiveness processes an instance with unhealthy liveness: begins
// a drain if not already draining, or completes the replacement if the grace
// period has elapsed.
func (failureController *FailureController) handleUnhealthyLiveness(instanceID string, serviceName string, drainSince string, now time.Time, counters *failureCycleCounters) []Change {
	if drainSince == "" {
		if counters.drains >= failureController.MaxDrainsPerCycle {
			return nil
		}
		counters.drains++
		logging.Default().Info("liveness unhealthy, beginning drain",
			"instance", instanceID, "service", serviceName,
			"grace_period", failureController.DrainGracePeriod.String())
		return failureController.beginDrain(instanceID, now)
	}

	drainStartMillis, parseErr := strconv.ParseInt(drainSince, 10, 64)
	if parseErr != nil {
		if counters.replacements >= failureController.MaxReplacementsPerCycle {
			return nil
		}
		counters.replacements++
		logging.Default().Warn("replacing instance with corrupt drain timestamp",
			"instance", instanceID, "service", serviceName)
		return failureController.stopAndReplace(instanceID, serviceName)
	}
	drainStartTime := time.UnixMilli(drainStartMillis)
	drainDuration := now.Sub(drainStartTime)
	if drainDuration >= failureController.DrainGracePeriod {
		if counters.replacements >= failureController.MaxReplacementsPerCycle {
			return nil
		}
		counters.replacements++
		logging.Default().Info("drain grace period elapsed, replacing instance",
			"instance", instanceID, "service", serviceName,
			"drained_for", drainDuration.String())
		return failureController.stopAndReplace(instanceID, serviceName)
	}
	return nil
}

// capOutputChanges truncates the combined change set to fit within the
// runner's transaction budget using atomic group boundaries.
func (failureController *FailureController) capOutputChanges(changes []Change) ([]Change, error) {
	if len(changes) > maxFailureControllerChangesPerCycle {
		var deferredCount int
		var groupError error
		changes, deferredCount, groupError = takeWholeGroups(changes, maxFailureControllerChangesPerCycle)
		if groupError != nil {
			return nil, groupError
		}
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

// calculateCrashLoopBackoff returns the backoff delay for a given crash count.
func calculateCrashLoopBackoff(crashCount int) time.Duration {
	delaySeconds := float64(crashLoopBaseDelay/time.Second) * math.Pow(2, float64(crashCount-1))
	maxSeconds := float64(crashLoopMaxDelay / time.Second)
	if delaySeconds > maxSeconds {
		delaySeconds = maxSeconds
	}
	return time.Duration(delaySeconds) * time.Second
}

// parseCrashLoopCounters extracts per-service crash-loop backoff state from
// the input facts.
func parseCrashLoopCounters(facts []store.Fact) map[string]*crashLoopCounter {
	counters := make(map[string]*crashLoopCounter)
	for _, fact := range store.FactsWithPrefix(facts, types.ScanDerivedCrashLoops) {
		serviceName := strings.TrimPrefix(fact.Key, types.ScanDerivedCrashLoops)
		var counter crashLoopCounter
		if json.Unmarshal(fact.Value, &counter) == nil {
			counters[serviceName] = &counter
		}
	}
	return counters
}
