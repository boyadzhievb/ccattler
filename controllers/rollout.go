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

// rolloutDefaultProgressDeadline is how long a rollout may go without progress
// before it is rolled back, unless the service sets update { progress_deadline }.
// Kubernetes' default progressDeadlineSeconds (M241 D7).
const rolloutDefaultProgressDeadline = 10 * time.Minute

// rolloutDefaultMinHealthyTime is how long a new instance must stay running
// (and ready, when probed) before a rollout counts it, unless the service sets
// update { min_healthy_time }. Nomad's default min_healthy_time (M241 D10).
// No fact changes when the window ends; the runner's periodic resync gives
// the pass that counts the instance.
const rolloutDefaultMinHealthyTime = 10 * time.Second

// rolloutMinimumFailureThreshold is the lowest number of new-image failures that
// rolls a rollout back, whatever the service size (ECS circuit breaker, M241 D3).
const rolloutMinimumFailureThreshold = 3

// rolloutMaximumFailureThreshold is the highest number of new-image failures a
// rollout waits for before it rolls back (ECS circuit breaker, M241 D3).
const rolloutMaximumFailureThreshold = 200

// rolloutFailureThresholdDivisor divides the desired instance count (rounded
// up) into the failure threshold: half the service (M241 D3).
const rolloutFailureThresholdDivisor = 2

// RolloutController manages rolling updates when a service's desired image
// changes. It stops old-image instances (unhealthy ones first, healthy ones
// within max_unavailable), lets the InstanceController create replacements with
// the new image, rolls back on new-image failures or a missed progress deadline,
// and publishes the rollout state and the reason it waits (Flow 34, M241).
type RolloutController struct {
	// Now returns the current time for the progress deadline and min_healthy_time; tests set it.
	Now func() time.Time
}

// NewRolloutController returns a new RolloutController using the wall clock.
func NewRolloutController() *RolloutController {
	return &RolloutController{Now: time.Now}
}

// Name returns "rollout", identifying this controller in logs.
func (rolloutController *RolloutController) Name() string { return "rollout" }

// Watch returns the fact prefixes that drive rolling updates, including the
// effective instance counts (completion) and the scheduler's unplaced reasons
// (the rollout reason, M241 D7).
func (rolloutController *RolloutController) Watch() []string {
	return []string{
		types.ScanDesiredServices,
		types.ScanEffectiveServices,
		types.ScanObservedInstances,
		types.ScanDerivedInstances,
		types.ScanDerivedServices,
		types.ScanDerivedSchedulerUnplaced,
		types.ScanPlacements,
	}
}

// Reconcile runs the rollout state machine of every service in sorted name
// order and returns the bookkeeping changes, image reverts and instance stops.
// The same facts and clock always give the same changes (M241 D6).
func (rolloutController *RolloutController) Reconcile(_ context.Context, facts []store.Fact) ([]Change, error) {
	clusterFacts := extractRolloutClusterFacts(facts)
	now := rolloutController.Now()
	var changes []Change
	for _, serviceName := range sortedMapKeys(clusterFacts.desiredImages) {
		if len(clusterFacts.instancesByService[serviceName]) == 0 {
			continue
		}
		changes = append(changes, reconcileServiceRollout(clusterFacts.buildServiceInput(serviceName, now))...)
	}
	return changes, nil
}

// reconcileServiceRollout runs one pass of the rollout state machine (D5) for
// one service: start or retarget tracking, finish, roll back on failures, stop
// old instances, and track progress, the deadline and the reason (D7). The
// instances are classified again after tracking starts, so failures count
// from the new rollout/started_at (D13).
func reconcileServiceRollout(serviceInput rolloutServiceInput) []Change {
	writer := serviceInput.writer
	classification := classifyRolloutInstances(serviceInput.instances, serviceInput.desiredImage, serviceInput.healthRule,
		parseRolloutTimestamp(writer.value(rolloutFieldStartedAt)))
	currentState := types.RolloutState(writer.value(rolloutFieldState))
	switch {
	case !isRolloutInProgress(currentState):
		if len(classification.oldInstances) == 0 {
			return nil
		}
		beginRolloutTowardDesiredImage(serviceInput, classification, mostCommonImage(classification.oldInstances))
		currentState = types.RolloutRolling
	case writer.value(rolloutFieldTargetImage) == "":
		writer.put(rolloutFieldTargetImage, serviceInput.desiredImage)
	case writer.value(rolloutFieldTargetImage) != serviceInput.desiredImage:
		knownGoodImage := writer.value(rolloutFieldPreviousImage)
		if knownGoodImage == "" {
			knownGoodImage = mostCommonImage(classification.oldInstances)
		}
		beginRolloutTowardDesiredImage(serviceInput, classification, knownGoodImage)
		currentState = types.RolloutRolling
	}
	classification = classifyRolloutInstances(serviceInput.instances, serviceInput.desiredImage, serviceInput.healthRule,
		parseRolloutTimestamp(writer.value(rolloutFieldStartedAt)))

	if classification.isFinished(serviceInput.expectedInstanceCount) {
		finishRollout(serviceInput, currentState)
		return writer.changes
	}
	if currentState == types.RolloutRolling {
		writer.put(rolloutFieldFailures, strconv.Itoa(classification.failureCount))
		failureThreshold := computeRolloutFailureThreshold(serviceInput.desiredInstanceCount)
		if classification.failureCount >= failureThreshold && writer.value(rolloutFieldPreviousImage) != "" {
			rollbackReason := fmt.Sprintf("%d failures ≥ %d on %s: %s", classification.failureCount, failureThreshold,
				serviceInput.desiredImage, describeWorstFailingInstance(classification.failingInstances))
			rollBackToPreviousImage(serviceInput, classification, rollbackReason)
			return writer.changes
		}
	}
	return advanceRollout(serviceInput, classification, currentState)
}

// advanceRollout stops old instances (unless paused), records progress, applies
// the progress deadline (rollback from rolling, stalled from rollback) and
// writes the reason the rollout waits. Returns all changes of the pass.
func advanceRollout(serviceInput rolloutServiceInput, classification rolloutClassification, currentState types.RolloutState) []Change {
	writer := serviceInput.writer
	var stopPlan rolloutStopPlan
	if !serviceInput.paused {
		stopPlan = planOldInstanceStops(classification, serviceInput.maxUnavailable, serviceInput.expectedInstanceCount)
	}
	storedRunningNew, _ := strconv.Atoi(writer.value(rolloutFieldRunningNew))
	progressed := len(stopPlan.stoppedIDs) > 0 || classification.healthyNewCount > storedRunningNew
	waitingReason := buildRolloutReason(serviceInput, classification, stopPlan, currentState)

	if !serviceInput.paused && !progressed && isProgressDeadlineExceeded(writer.value(rolloutFieldProgressAt), serviceInput) {
		deadlineReason := fmt.Sprintf("no progress for %s: %s", types.FormatCompactDuration(serviceInput.progressDeadline), waitingReason)
		if currentState == types.RolloutRolling && writer.value(rolloutFieldPreviousImage) != "" {
			rollBackToPreviousImage(serviceInput, classification, deadlineReason)
			return writer.changes
		}
		if currentState == types.RolloutRollback {
			logging.Default().Warn("rollback stalled", "service", serviceInput.serviceName, "reason", deadlineReason)
			writer.put(rolloutFieldState, string(types.RolloutStalled))
		}
	}
	if progressed && currentState == types.RolloutStalled {
		logging.Default().Info("stalled rollback progresses again", "service", serviceInput.serviceName)
		writer.put(rolloutFieldState, string(types.RolloutRollback))
	}
	recordRolloutProgress(serviceInput, classification.healthyNewCount, progressed)
	writer.put(rolloutFieldReason, truncateRolloutReason(waitingReason))
	return append(writer.changes, buildInstanceStopChanges(stopPlan.stoppedIDs)...)
}

// recordRolloutProgress keeps rollout/progress_at and rollout/running_new: a
// paused rollout has no progress_at (it does not time out, and resuming starts
// the deadline anew); otherwise progress or a missing value sets it to now.
func recordRolloutProgress(serviceInput rolloutServiceInput, healthyNewCount int, progressed bool) {
	writer := serviceInput.writer
	switch {
	case serviceInput.paused:
		writer.remove(rolloutFieldProgressAt)
	case progressed || writer.value(rolloutFieldProgressAt) == "":
		writer.put(rolloutFieldProgressAt, serviceInput.now.UTC().Format(time.RFC3339))
	}
	writer.put(rolloutFieldRunningNew, strconv.Itoa(healthyNewCount))
}

// isProgressDeadlineExceeded reports whether more than the progress deadline
// has passed since progressAt. A missing or unreadable time never exceeds it:
// the deadline then starts at this pass.
func isProgressDeadlineExceeded(progressAtText string, serviceInput rolloutServiceInput) bool {
	progressAt := parseRolloutTimestamp(progressAtText)
	if progressAt.IsZero() {
		return false
	}
	return serviceInput.now.Sub(progressAt) > serviceInput.progressDeadline
}

// parseRolloutTimestamp parses an RFC 3339 rollout field; a missing or
// unreadable value is the zero time.
func parseRolloutTimestamp(timestampText string) time.Time {
	timestamp, parseError := time.Parse(time.RFC3339, timestampText)
	if parseError != nil {
		return time.Time{}
	}
	return timestamp
}

// isRolloutInProgress reports whether a rollout state is one the state machine
// keeps working on: rolling, rollback or stalled.
func isRolloutInProgress(rolloutState types.RolloutState) bool {
	return rolloutState == types.RolloutRolling || rolloutState == types.RolloutRollback || rolloutState == types.RolloutStalled
}

// beginRolloutTowardDesiredImage starts tracking a rollout to the desired image
// (D5 rows 1 and 2): rolling, the known-good previous image, the target, the
// next revision with its history entry, the surge allowance, a fresh deadline
// and started_at (D13). The reason of an earlier rollback is deleted. A revert
// to the known-good image has no previous image and cannot roll back (D13).
func beginRolloutTowardDesiredImage(serviceInput rolloutServiceInput, classification rolloutClassification, knownGoodImage string) {
	writer := serviceInput.writer
	currentRevision, _ := strconv.Atoi(writer.value(rolloutFieldRevision))
	nextRevision := currentRevision + 1
	historyImage := knownGoodImage
	writer.put(rolloutFieldState, string(types.RolloutRolling))
	if knownGoodImage == serviceInput.desiredImage {
		historyImage = mostCommonImage(classification.oldInstances)
		writer.remove(rolloutFieldPreviousImage)
	} else {
		writer.put(rolloutFieldPreviousImage, knownGoodImage)
	}
	writer.put(rolloutFieldTargetImage, serviceInput.desiredImage)
	writer.put(rolloutFieldRevision, strconv.Itoa(nextRevision))
	writer.putKey(types.KeyDerivedServiceRolloutHistory(serviceInput.serviceName, nextRevision), historyImage)
	if serviceInput.maxExtra > 0 {
		writer.put(rolloutFieldSurgeAllowance, strconv.Itoa(serviceInput.maxExtra))
	} else {
		writer.remove(rolloutFieldSurgeAllowance)
	}
	for _, staleField := range []rolloutField{rolloutFieldRollbackReason, rolloutFieldRolledBackImage, rolloutFieldRolledBackAt, rolloutFieldFailures} {
		writer.remove(staleField)
	}
	writer.put(rolloutFieldProgressAt, serviceInput.now.UTC().Format(time.RFC3339))
	writer.put(rolloutFieldStartedAt, serviceInput.now.UTC().Format(time.RFC3339))
	writer.put(rolloutFieldRunningNew, strconv.Itoa(classification.healthyNewCount))
	logging.Default().Info("rollout started", "service", serviceInput.serviceName,
		"from", historyImage, "to", serviceInput.desiredImage, "revision", strconv.Itoa(nextRevision))
}

// rollBackToPreviousImage reverts the desired image to the previous image (D5
// row 3), records why and which image was rolled back (D7, D9), restarts the
// deadline and started_at (failures of the previous image count from now, D13)
// and logs one warning.
func rollBackToPreviousImage(serviceInput rolloutServiceInput, classification rolloutClassification, rollbackReason string) {
	writer := serviceInput.writer
	previousImage := writer.value(rolloutFieldPreviousImage)
	writer.putKey(types.KeyDesiredServiceImage(serviceInput.serviceName), previousImage)
	writer.put(rolloutFieldState, string(types.RolloutRollback))
	writer.put(rolloutFieldTargetImage, previousImage)
	writer.remove(rolloutFieldSurgeAllowance)
	writer.put(rolloutFieldRollbackReason, truncateRolloutReason(rollbackReason))
	writer.put(rolloutFieldRolledBackImage, serviceInput.desiredImage)
	writer.put(rolloutFieldRolledBackAt, serviceInput.now.UTC().Format(time.RFC3339))
	writer.put(rolloutFieldProgressAt, serviceInput.now.UTC().Format(time.RFC3339))
	writer.put(rolloutFieldStartedAt, serviceInput.now.UTC().Format(time.RFC3339))
	writer.put(rolloutFieldRunningNew, strconv.Itoa(classification.countHealthyOldOnImage(previousImage)))
	writer.put(rolloutFieldReason, truncateRolloutReason("rolling back to "+previousImage))
	logging.Default().Warn("rollout rolled back", "service", serviceInput.serviceName,
		"from", serviceInput.desiredImage, "to", previousImage, "reason", rollbackReason)
}

// finishRollout ends a rollout (complete) or a rollback (rolled-back) once every
// instance runs the desired image (D5 rows 4 and 5) and deletes the bookkeeping
// of the run. The rollback reason and rolled-back image stay for the user. A
// rollback stored by an older server, without a reason, ends complete (L1).
func finishRollout(serviceInput rolloutServiceInput, currentState types.RolloutState) {
	writer := serviceInput.writer
	finalState := types.RolloutRolledBack
	if currentState == types.RolloutRolling || writer.value(rolloutFieldRollbackReason) == "" {
		finalState = types.RolloutComplete
	}
	writer.put(rolloutFieldState, string(finalState))
	for _, finishedField := range []rolloutField{
		rolloutFieldPreviousImage, rolloutFieldTargetImage, rolloutFieldFailures, rolloutFieldSurgeAllowance,
		rolloutFieldReason, rolloutFieldProgressAt, rolloutFieldRunningNew, rolloutFieldStartedAt,
	} {
		writer.remove(finishedField)
	}
	logging.Default().Info("rollout finished", "service", serviceInput.serviceName,
		"state", string(finalState), "image", serviceInput.desiredImage)
}

// computeRolloutFailureThreshold returns half the desired instance count,
// rounded up, kept between the minimum and maximum threshold (D3). A missing
// count counts as 1.
func computeRolloutFailureThreshold(desiredInstanceCount int) int {
	if desiredInstanceCount < 1 {
		desiredInstanceCount = 1
	}
	failureThreshold := (desiredInstanceCount + rolloutFailureThresholdDivisor - 1) / rolloutFailureThresholdDivisor
	return min(max(failureThreshold, rolloutMinimumFailureThreshold), rolloutMaximumFailureThreshold)
}

// buildInstanceStopChanges returns the changes that stop the given instances
// by writing their observed state as stopped, in the given (ID) order.
func buildInstanceStopChanges(instanceIDs []string) []Change {
	var changes []Change
	for _, instanceID := range instanceIDs {
		changes = append(changes, Change{
			Type:  store.OpPut,
			Key:   types.KeyObservedInstanceState(instanceID),
			Value: []byte(string(types.InstanceStopped)),
		})
	}
	return changes
}
