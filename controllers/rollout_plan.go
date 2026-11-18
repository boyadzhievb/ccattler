// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/boyadzhievb/ccattler/types"
)

// rolloutReasonMaxLength caps rollout/reason and rollout/rollback_reason so a
// long status reason never makes a large fact (M241 D7).
const rolloutReasonMaxLength = 300

// rolloutStatusReasonRetrySeparator separates an exit from the agent's back-off
// note in a status reason ("exited with code 1; restarting in 40s").
const rolloutStatusReasonRetrySeparator = "; "

// rolloutClassification is one service's instances split by image (D3, D4).
type rolloutClassification struct {
	oldInstances      []rolloutInstanceInfo // oldInstances run another image than the desired one, sorted by ID.
	newInstances      []rolloutInstanceInfo // newInstances run (or will run) the desired image, sorted by ID.
	runningNewCount   int                   // runningNewCount counts new instances that are running.
	failureCount      int                   // failureCount is restarts plus failed new instances, lost nodes excluded.
	unavailableNewIDs []string              // unavailableNewIDs lists new instances that use up max_unavailable.
}

// classifyRolloutInstances splits the active instances (sorted by ID) into old
// and new image and counts running new instances, new-image failures (D3) and
// unavailable new instances (D4). An instance without an image yet is new.
func classifyRolloutInstances(instances []rolloutInstanceInfo, desiredImage string) rolloutClassification {
	var classification rolloutClassification
	for _, instanceInfo := range instances {
		if instanceInfo.state == types.InstanceStopped {
			continue
		}
		if instanceInfo.image != "" && instanceInfo.image != desiredImage {
			classification.oldInstances = append(classification.oldInstances, instanceInfo)
			continue
		}
		classification.newInstances = append(classification.newInstances, instanceInfo)
		if instanceInfo.state == types.InstanceRunning {
			classification.runningNewCount++
		}
		if isUnavailableForRollout(instanceInfo) {
			classification.unavailableNewIDs = append(classification.unavailableNewIDs, instanceInfo.id)
		}
		if instanceInfo.nodeFailure {
			continue
		}
		classification.failureCount += instanceInfo.restarts
		if instanceInfo.state == types.InstanceFailed {
			classification.failureCount++
		}
	}
	return classification
}

// isFinished reports whether the rollout reached its image: no old instance is
// left, every new instance runs, and at least expectedCount of them do, so a
// replacement that has not been created or started yet keeps it going.
func (classification rolloutClassification) isFinished(expectedCount int) bool {
	if len(classification.oldInstances) > 0 || classification.runningNewCount < expectedCount {
		return false
	}
	return classification.runningNewCount == len(classification.newInstances)
}

// countRunningOldOnImage counts old instances running the given image, the
// running instances of the target a rollback goes back to.
func (classification rolloutClassification) countRunningOldOnImage(image string) int {
	var runningCount int
	for _, instanceInfo := range classification.oldInstances {
		if instanceInfo.image == image && instanceInfo.state == types.InstanceRunning {
			runningCount++
		}
	}
	return runningCount
}

// isUnavailableForRollout reports whether an instance of any image is down for
// a reason of its own: failed, crash-looping, failing to pull, or on a lost
// node (D4). Pending, pulling, starting and waiting instances are not.
func isUnavailableForRollout(instanceInfo rolloutInstanceInfo) bool {
	if instanceInfo.nodeFailure {
		return true
	}
	switch instanceInfo.state {
	case types.InstanceFailed, types.InstanceCrashLoopBackoff, types.InstanceImagePullBackoff:
		return true
	default:
		return false
	}
}

// rolloutStopPlan is the outcome of one pass's stop decision.
type rolloutStopPlan struct {
	stoppedIDs     []string // stoppedIDs are the old instances to stop, unhealthy first, each group in ID order.
	budgetUsedUp   bool     // budgetUsedUp is true when healthy old instances wait only for max_unavailable.
	maxUnavailable int      // maxUnavailable is the effective budget of the pass.
}

// planOldInstanceStops stops every unavailable old instance without budget,
// then healthy old instances up to max_unavailable minus the unavailable new
// instances (D4). Healthy ones are stopped only while a new instance runs, or
// none exists yet and no unhealthy one is being replaced this pass.
func planOldInstanceStops(classification rolloutClassification, maxUnavailable int) rolloutStopPlan {
	plan := rolloutStopPlan{maxUnavailable: maxUnavailable}
	var healthyOldIDs []string
	for _, instanceInfo := range classification.oldInstances {
		if isUnavailableForRollout(instanceInfo) {
			plan.stoppedIDs = append(plan.stoppedIDs, instanceInfo.id)
		} else {
			healthyOldIDs = append(healthyOldIDs, instanceInfo.id)
		}
	}
	replacementsPending := len(classification.newInstances) > 0 || len(plan.stoppedIDs) > 0
	if classification.runningNewCount == 0 && replacementsPending {
		return plan
	}
	stopBudget := maxUnavailable - len(classification.unavailableNewIDs)
	if stopBudget <= 0 && len(healthyOldIDs) > 0 {
		plan.budgetUsedUp = true
		return plan
	}
	plan.stoppedIDs = append(plan.stoppedIDs, healthyOldIDs[:min(stopBudget, len(healthyOldIDs))]...)
	return plan
}

// buildRolloutReason returns what the rollout waits for, the first match of:
// paused; a rollback whose image also fails; a new instance not running yet
// (its state, status reason or scheduler reason); max_unavailable used up.
// Otherwise it says how many old instances are left to replace (D7).
func buildRolloutReason(serviceInput rolloutServiceInput, classification rolloutClassification, stopPlan rolloutStopPlan, currentState types.RolloutState) string {
	if serviceInput.paused {
		return fmt.Sprintf("paused with %d old-image instances left", len(classification.oldInstances))
	}
	if currentState != types.RolloutRolling && classification.failureCount > 0 {
		return fmt.Sprintf("rolling back, but the previous image also fails: %d failures on %s",
			classification.failureCount, serviceInput.desiredImage)
	}
	for _, instanceInfo := range classification.newInstances {
		if instanceInfo.state != types.InstanceRunning && !instanceInfo.nodeFailure {
			return "waiting for " + describeWaitingInstance(instanceInfo, serviceInput.unplacedInstances[instanceInfo.id])
		}
	}
	if stopPlan.budgetUsedUp {
		return fmt.Sprintf("max_unavailable %d used up by %s", stopPlan.maxUnavailable, strings.Join(classification.unavailableNewIDs, ", "))
	}
	return fmt.Sprintf("replacing %d old-image instances", len(classification.oldInstances))
}

// describeWaitingInstance names a new instance that is not running: the
// scheduler's unplaced reason and requirements when it cannot be placed,
// otherwise its state and status reason.
func describeWaitingInstance(instanceInfo rolloutInstanceInfo, unplaced *unplacedDemandEntry) string {
	if unplaced != nil && unplaced.reason != "" {
		description := fmt.Sprintf("%s unplaced: %s", instanceInfo.id, unplaced.reason)
		if unplaced.requirements.CPU > 0 || unplaced.requirements.Memory > 0 {
			description += fmt.Sprintf(" (cpu %dm, memory %dMi)", unplaced.requirements.CPU, unplaced.requirements.Memory)
		}
		return description
	}
	description := fmt.Sprintf("%s %s", instanceInfo.id, instanceInfo.state)
	if instanceInfo.statusReason != "" {
		description += ": " + instanceInfo.statusReason
	}
	return description
}

// describeWorstFailingInstance returns the exit reason of the new instance with
// the most restarts (lowest ID on a tie; a failed one counts one more), for the
// rollback reason. The back-off note of the status reason is dropped.
func describeWorstFailingInstance(newInstances []rolloutInstanceInfo) string {
	failingInstances := make([]rolloutInstanceInfo, 0, len(newInstances))
	for _, instanceInfo := range newInstances {
		if !instanceInfo.nodeFailure && (instanceInfo.restarts > 0 || instanceInfo.state == types.InstanceFailed) {
			failingInstances = append(failingInstances, instanceInfo)
		}
	}
	if len(failingInstances) == 0 {
		return "no exit reason recorded"
	}
	sort.SliceStable(failingInstances, func(leftIndex, rightIndex int) bool {
		return failingInstances[leftIndex].failureWeight() > failingInstances[rightIndex].failureWeight()
	})
	worstInstance := failingInstances[0]
	exitReason, _, _ := strings.Cut(worstInstance.statusReason, rolloutStatusReasonRetrySeparator)
	if exitReason == "" {
		exitReason = worstInstance.exitReason
	}
	if exitReason == "" {
		exitReason = string(worstInstance.state)
	}
	return exitReason
}

// failureWeight is how much an instance adds to the failure count: its
// restarts, plus one when it is failed.
func (instanceInfo rolloutInstanceInfo) failureWeight() int {
	if instanceInfo.state == types.InstanceFailed {
		return instanceInfo.restarts + 1
	}
	return instanceInfo.restarts
}

// mostCommonImage returns the image most old instances run, the previous image
// of a new rollout; a tie goes to the image that sorts first (D5).
func mostCommonImage(oldInstances []rolloutInstanceInfo) string {
	instanceCountByImage := make(map[string]int)
	for _, instanceInfo := range oldInstances {
		instanceCountByImage[instanceInfo.image]++
	}
	var commonestImage string
	for _, image := range sortedMapKeys(instanceCountByImage) {
		if instanceCountByImage[image] > instanceCountByImage[commonestImage] {
			commonestImage = image
		}
	}
	return commonestImage
}

// truncateRolloutReason cuts a reason to rolloutReasonMaxLength bytes on a
// character boundary, marking the cut with "...".
func truncateRolloutReason(reason string) string {
	if len(reason) <= rolloutReasonMaxLength {
		return reason
	}
	const truncationMarker = "..."
	cutIndex := rolloutReasonMaxLength - len(truncationMarker)
	for cutIndex > 0 && !utf8.RuneStart(reason[cutIndex]) {
		cutIndex--
	}
	return reason[:cutIndex] + truncationMarker
}
