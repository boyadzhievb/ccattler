// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/boyadzhievb/ccattler/backoff"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// crashLoopBaseDelay is the wait before the first in-place restart of an exited container.
const crashLoopBaseDelay = 10 * time.Second

// crashLoopMaxDelay is the longest wait between two in-place restarts.
const crashLoopMaxDelay = 5 * time.Minute

// crashLoopResetPeriod is how long an instance must run without exiting
// before its next exit waits the base delay again.
const crashLoopResetPeriod = 10 * time.Minute

// crashLoopBackoffEntry tracks the in-place restart back-off of one instance (M233).
type crashLoopBackoffEntry struct {
	attempts       int       // attempts is the number of consecutive exits that were followed by a wait.
	retryAfter     time.Time // retryAfter is the earliest time the exited container may be started again.
	lastStartedAt  time.Time // lastStartedAt is when the agent last tried to start the instance.
	lastExitReason string    // lastExitReason describes the most recent exit, as published in the status reason.
	waiting        bool      // waiting is true while the most recent exit has been recorded and its wait is running.
	startFailed    bool      // startFailed is true when the most recent exit is a start whose workload process could not start (M238); no container is left.
}

// reconcileInstanceWithoutRunningContainer starts an instance whose container
// is missing or has exited. A missing container is started at once; an exited
// one, or one whose process could not start, is restarted in place only when
// its back-off wait is over (Flow 33). Only an attempted start is counted.
func (nodeAgent *Agent) reconcileInstanceWithoutRunningContainer(ctx context.Context, instanceInfo placedInstanceInfo, runtimeStatus runtime.Status, containerExited bool) {
	backoffEntry, isTracked := nodeAgent.crashLoopBackoffs[instanceInfo.id]
	retryAfterStartFailure := isTracked && backoffEntry.startFailed
	if (containerExited || retryAfterStartFailure) && nodeAgent.forgetOldIncarnationBackoff(ctx, instanceInfo) {
		containerExited, retryAfterStartFailure = false, false
	}
	if containerExited || retryAfterStartFailure {
		if !nodeAgent.isExitedInstanceDueForRestart(ctx, instanceInfo, runtimeStatus) {
			return
		}
	} else if isTracked {
		backoffEntry.waiting = false // the exited container is gone; the next exit is a new one
	}
	logging.Default().Info("starting instance",
		"agent", nodeAgent.nodeID,
		"instance", instanceInfo.id,
		"service", instanceInfo.service)
	if !nodeAgent.reconcileDesiredInstance(ctx, instanceInfo) {
		return
	}
	if containerExited || retryAfterStartFailure {
		nodeAgent.incrementRestartCountInStore(ctx, instanceInfo)
	}
}

// forgetOldIncarnationBackoff reports whether the exited (or start-failed)
// container ran another image than the instance's recorded one: the ID was
// re-created on another image (M241 2f). Its exit, back-off and restart belong
// to the old image, so the back-off entry is dropped and the start is not counted.
func (nodeAgent *Agent) forgetOldIncarnationBackoff(ctx context.Context, instanceInfo placedInstanceInfo) bool {
	startedImage, recordedImage, isNewIncarnation := nodeAgent.findNewIncarnationOnAnotherImage(ctx, instanceInfo)
	if !isNewIncarnation {
		return false
	}
	if nodeAgent.isFirstSightOfIncarnation(instanceInfo.id, recordedImage) {
		logging.Default().Info("exited container ran another image than recorded, starting the recorded image at once",
			"agent", nodeAgent.nodeID, "instance", instanceInfo.id, "exited_image", startedImage, "recorded_image", recordedImage)
	}
	delete(nodeAgent.crashLoopBackoffs, instanceInfo.id)
	return true
}

// isFirstSightOfIncarnation reports whether this is the first time the agent
// sees the instance's new incarnation on recordedImage, and remembers it, so
// the new incarnation is logged once, not every cycle until it starts (2g.4).
func (nodeAgent *Agent) isFirstSightOfIncarnation(instanceID string, recordedImage string) bool {
	if nodeAgent.incarnationLoggedImage[instanceID] == recordedImage {
		return false
	}
	nodeAgent.incarnationLoggedImage[instanceID] = recordedImage
	return true
}

// isExitedInstanceDueForRestart reports whether the exited container of an
// instance may be started again now. A new exit is recorded and starts a wait;
// a config change ends the wait; an instance the agent has not tracked (the
// agent restarted) is restarted once at once.
func (nodeAgent *Agent) isExitedInstanceDueForRestart(ctx context.Context, instanceInfo placedInstanceInfo, runtimeStatus runtime.Status) bool {
	backoffEntry, isTracked := nodeAgent.crashLoopBackoffs[instanceInfo.id]
	if !isTracked {
		return true
	}
	if !backoffEntry.waiting {
		nodeAgent.recordInstanceExit(ctx, instanceInfo, backoffEntry, runtimeStatus)
		return false
	}
	if !time.Now().Before(backoffEntry.retryAfter) {
		return true
	}
	if nodeAgent.hasConfigChangedSinceStart(ctx, instanceInfo) {
		logging.Default().Info("config changed during crash-loop back-off, restarting at once",
			"agent", nodeAgent.nodeID, "instance", instanceInfo.id)
		delete(nodeAgent.crashLoopBackoffs, instanceInfo.id)
		return true
	}
	return false
}

// recordInstanceExit counts a new exit of the instance's container, publishes
// its exit reason for a non-zero code and starts the back-off wait.
func (nodeAgent *Agent) recordInstanceExit(ctx context.Context, instanceInfo placedInstanceInfo, backoffEntry *crashLoopBackoffEntry, runtimeStatus runtime.Status) {
	nodeAgent.beginCrashLoopWait(ctx, instanceInfo, backoffEntry, describeContainerExit(runtimeStatus),
		exitReasonChanges(instanceInfo.id, runtimeStatus)...)
}

// exitReasonChanges returns the put of observed/instance/{id}/exit-reason for a
// non-zero exit (the runtime's error, else "exit(N)"), else none; it is written
// in the crash-loop publish's guarded transaction (M267 3e).
func exitReasonChanges(instanceID string, runtimeStatus runtime.Status) []store.Op {
	if runtimeStatus.ExitCode == 0 {
		return nil
	}
	exitReason := runtimeStatus.Error
	if exitReason == "" {
		exitReason = fmt.Sprintf("exit(%d)", runtimeStatus.ExitCode)
	}
	return []store.Op{exitReasonChange(instanceID, exitReason)}
}

// exitReasonChange returns the put of observed/instance/{id}/exit-reason.
func exitReasonChange(instanceID string, exitReason string) store.Op {
	return store.Op{Type: store.OpPut, Key: types.KeyObservedInstanceExitReason(instanceID), Value: []byte(exitReason)}
}

// recordInstanceStartFailure records a start whose workload process could not
// start (bad entrypoint, missing binary) as an attempt followed by an exit:
// processError (runc's `exec: "…": …`) is the exit reason and the retry waits
// out the back-off (M238). The reset decision uses the previous start, so a
// failure after a long stable run waits the base delay again.
func (nodeAgent *Agent) recordInstanceStartFailure(ctx context.Context, instanceInfo placedInstanceInfo, processError string) {
	backoffEntry := nodeAgent.ensureCrashLoopBackoffEntry(instanceInfo.id)
	nodeAgent.beginCrashLoopWait(ctx, instanceInfo, backoffEntry, processError, exitReasonChange(instanceInfo.id, processError))
	backoffEntry.startFailed = true
	backoffEntry.lastStartedAt = time.Now()
}

// beginCrashLoopWait counts an exit described by exitDescription, starts the
// wait before the next restart and publishes the crash-loop-backoff state with
// the exit and the wait as its reason, last_exit and exitFacts in one guarded
// transaction (M267 3e).
func (nodeAgent *Agent) beginCrashLoopWait(ctx context.Context, instanceInfo placedInstanceInfo, backoffEntry *crashLoopBackoffEntry, exitDescription string, exitFacts ...store.Op) {
	now := time.Now()
	if now.Sub(backoffEntry.lastStartedAt) >= nodeAgent.crashLoopResetPeriod {
		backoffEntry.attempts = 0
	}
	backoffEntry.attempts++
	waitDelay := backoff.Delay(nodeAgent.crashLoopBaseDelay, nodeAgent.crashLoopMaxDelay, backoffEntry.attempts)
	backoffEntry.retryAfter = now.Add(waitDelay)
	backoffEntry.waiting = true
	backoffEntry.lastExitReason = exitDescription

	logging.Default().Warn("container exited, restarting after back-off",
		"agent", nodeAgent.nodeID, "instance", instanceInfo.id, "service", instanceInfo.service,
		"attempt", strconv.Itoa(backoffEntry.attempts), "wait", waitDelay.String(), "exit", backoffEntry.lastExitReason)
	// last_exit outlives the restart (the status reason becomes "running"), so a
	// rollback can still name the exit (M241, testbed 4.1 a).
	crashLoopFacts := append([]store.Op{
		statusReasonChange(instanceInfo.id, fmt.Sprintf("%s; restarting in %s", backoffEntry.lastExitReason, waitDelay)),
		{Type: store.OpPut, Key: types.KeyObservedInstanceLastExit(instanceInfo.id), Value: []byte(backoffEntry.lastExitReason)},
	}, exitFacts...)
	nodeAgent.publishInstanceStateToStore(ctx, instanceInfo, types.InstanceCrashLoopBackoff, "", crashLoopFacts...)
}

// describeContainerExit returns "exited with code N", followed by the runtime's
// error text in parentheses when it reported one.
func describeContainerExit(runtimeStatus runtime.Status) string {
	if runtimeStatus.Error == "" {
		return fmt.Sprintf("exited with code %d", runtimeStatus.ExitCode)
	}
	return fmt.Sprintf("exited with code %d (%s)", runtimeStatus.ExitCode, runtimeStatus.Error)
}

// recordInstanceStarted notes that the runtime started the instance's container
// now: the back-off wait is over and the stable-run period begins.
func (nodeAgent *Agent) recordInstanceStarted(instanceID string) {
	backoffEntry := nodeAgent.ensureCrashLoopBackoffEntry(instanceID)
	backoffEntry.waiting = false
	backoffEntry.startFailed = false
	backoffEntry.lastStartedAt = time.Now()
}

// ensureCrashLoopBackoffEntry returns the back-off entry of an instance,
// creating an empty one when the agent does not track it yet.
func (nodeAgent *Agent) ensureCrashLoopBackoffEntry(instanceID string) *crashLoopBackoffEntry {
	backoffEntry, isTracked := nodeAgent.crashLoopBackoffs[instanceID]
	if !isTracked {
		backoffEntry = &crashLoopBackoffEntry{}
		nodeAgent.crashLoopBackoffs[instanceID] = backoffEntry
	}
	return backoffEntry
}

// incrementRestartCountInStore adds one to observed/instance/{id}/restarts.
// The agent is the only writer for its instances; a failed write is logged
// and the restart still happens.
func (nodeAgent *Agent) incrementRestartCountInStore(ctx context.Context, instanceInfo placedInstanceInfo) {
	instanceID := instanceInfo.id
	restartKey := types.KeyObservedInstanceRestarts(instanceID)
	restartCount := 0
	if restartFact, getError := nodeAgent.store.Get(ctx, restartKey); getError == nil {
		parsedCount, parseError := strconv.Atoi(string(restartFact.Value))
		if parseError != nil {
			logging.Default().Error("corrupt restart count, counting from 0", "instance", instanceID, "value", string(restartFact.Value), "error", parseError.Error())
		}
		restartCount = parsedCount
	} else if !errors.Is(getError, store.ErrKeyNotFound) {
		logging.Default().Error("failed to read restart count", "instance", instanceID, "error", getError.Error())
		return
	}
	nodeAgent.writeInstanceFacts(ctx, instanceInfo, store.Op{Type: store.OpPut, Key: restartKey, Value: []byte(strconv.Itoa(restartCount + 1))})
}

// cleanupStaleCrashLoopBackoffs drops the back-off state of instances that are
// no longer placed on this node.
func (nodeAgent *Agent) cleanupStaleCrashLoopBackoffs(desiredByID map[string]bool) {
	for backoffInstanceID := range nodeAgent.crashLoopBackoffs {
		if !desiredByID[backoffInstanceID] {
			delete(nodeAgent.crashLoopBackoffs, backoffInstanceID)
		}
	}
}
