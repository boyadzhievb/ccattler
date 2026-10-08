// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestFailureReplacesFailedInstance verifies that an instance in the "failed"
// state is immediately stopped and a replacement is created.
func TestFailureReplacesFailedInstance(t *testing.T) {
	failureController := NewFailureController()
	failureController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "failed"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 4 {
		t.Fatalf("expected 4 changes, got %d", len(changes))
	}
	if changes[0].Key != types.KeyDerivedInstanceControllerStopped("aaa") {
		t.Errorf("expected controller_stopped key for aaa, got %s", changes[0].Key)
	}
	if string(changes[0].Value) != "true" {
		t.Errorf("expected true, got %s", changes[0].Value)
	}
	if string(changes[2].Value) != "web" {
		t.Errorf("replacement service: got %s, want web", changes[2].Value)
	}
	if string(changes[3].Value) != "pending" {
		t.Errorf("replacement state: got %s, want pending", changes[3].Value)
	}
}

// TestFailureIgnoresRunning verifies that a healthy running instance is left alone.
func TestFailureIgnoresRunning(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for running instance, got %d", len(changes))
	}
}

// TestFailureIgnoresPending verifies that a pending instance is left alone.
func TestFailureIgnoresPending(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "pending"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for pending instance, got %d", len(changes))
	}
}

// TestFailureIgnoresStopped verifies that a stopped instance is left alone.
func TestFailureIgnoresStopped(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "stopped"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for stopped instance, got %d", len(changes))
	}
}

// TestFailureMultipleFailed verifies that multiple failed instances are all
// replaced in a single reconciliation.
func TestFailureMultipleFailed(t *testing.T) {
	failureController := NewFailureController()
	failureController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "failed"),
		kv(types.KeyObservedInstanceService("bbb"), "api"),
		kv(types.KeyObservedInstanceState("bbb"), "failed"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "running"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 8 {
		t.Fatalf("expected 8 changes (2 replacements), got %d", len(changes))
	}

	controllerStoppedCount := 0
	pending := 0
	for _, ch := range changes {
		if ch.Type == store.OpPut && strings.HasSuffix(ch.Key, "/controller_stopped") && string(ch.Value) == "true" {
			controllerStoppedCount++
		}
		if ch.Type == store.OpPut && string(ch.Value) == "pending" {
			pending++
		}
	}
	if controllerStoppedCount != 2 {
		t.Errorf("expected 2 controller_stopped markers, got %d", controllerStoppedCount)
	}
	if pending != 2 {
		t.Errorf("expected 2 pending replacements, got %d", pending)
	}
}

// TestFailureReplacesStartupFailed verifies that a running instance with
// startup probe state "failed" is immediately stopped and replaced (no drain).
func TestFailureReplacesStartupFailed(t *testing.T) {
	failureController := NewFailureController()
	failureController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "api"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceProbeState("aaa", "startup"), string(types.StartupProbeFailed)),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 4 {
		t.Fatalf("expected 4 changes (immediate stop + replacement), got %d", len(changes))
	}
	if string(changes[0].Value) != "true" || !strings.HasSuffix(changes[0].Key, "/controller_stopped") {
		t.Errorf("expected controller_stopped=true, got key=%s value=%s", changes[0].Key, changes[0].Value)
	}
	if string(changes[2].Value) != "api" {
		t.Errorf("replacement service: got %s, want api", changes[2].Value)
	}
}

// TestFailureLivenessUnhealthyBeginsDrain verifies that a running instance
// with liveness=unhealthy begins graceful drain: readiness set to not-ready
// and drain_since timestamp written, but the instance is NOT stopped yet.
func TestFailureLivenessUnhealthyBeginsDrain(t *testing.T) {
	fixedTime := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	failureController := NewFailureController()
	failureController.NowFunc = func() time.Time { return fixedTime }

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceProbeState("aaa", "liveness"), string(types.LivenessProbeUnhealthy)),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (readiness + drain_since), got %d", len(changes))
	}

	if changes[0].Key != types.KeyDerivedInstanceDrainReadiness("aaa") {
		t.Errorf("first change should set drain_readiness, got key %s", changes[0].Key)
	}
	if string(changes[0].Value) != string(types.ReadinessProbeNotReady) {
		t.Errorf("readiness should be not-ready, got %s", changes[0].Value)
	}

	if changes[1].Key != types.KeyDerivedInstanceDrainSince("aaa") {
		t.Errorf("second change should set drain_since, got key %s", changes[1].Key)
	}
	expectedTimestamp := fmt.Sprintf("%d", fixedTime.UnixMilli())
	if string(changes[1].Value) != expectedTimestamp {
		t.Errorf("drain_since: got %s, want %s", changes[1].Value, expectedTimestamp)
	}
}

// TestFailureLivenessDrainCompletesAfterGracePeriod verifies that once the
// grace period has elapsed, a draining instance is stopped and replaced.
func TestFailureLivenessDrainCompletesAfterGracePeriod(t *testing.T) {
	drainStart := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	afterGrace := drainStart.Add(6 * time.Second)

	failureController := NewFailureController()
	failureController.NewID = seqIDGen()
	failureController.NowFunc = func() time.Time { return afterGrace }
	failureController.DrainGracePeriod = 5 * time.Second

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceProbeState("aaa", "liveness"), string(types.LivenessProbeUnhealthy)),
		kv(types.KeyDerivedInstanceDrainSince("aaa"), fmt.Sprintf("%d", drainStart.UnixMilli())),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 4 {
		t.Fatalf("expected 4 changes (stop + replacement), got %d", len(changes))
	}
	if string(changes[0].Value) != "true" || !strings.HasSuffix(changes[0].Key, "/controller_stopped") {
		t.Errorf("expected controller_stopped=true, got key=%s value=%s", changes[0].Key, changes[0].Value)
	}
	if string(changes[3].Value) != "pending" {
		t.Errorf("replacement state: got %s, want pending", changes[3].Value)
	}
}

// TestFailureLivenessDrainWaitsBeforeGracePeriod verifies that a draining
// instance is NOT stopped while the grace period is still active.
func TestFailureLivenessDrainWaitsBeforeGracePeriod(t *testing.T) {
	drainStart := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	beforeGrace := drainStart.Add(3 * time.Second)

	failureController := NewFailureController()
	failureController.NowFunc = func() time.Time { return beforeGrace }
	failureController.DrainGracePeriod = 5 * time.Second

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceProbeState("aaa", "liveness"), string(types.LivenessProbeUnhealthy)),
		kv(types.KeyDerivedInstanceDrainSince("aaa"), fmt.Sprintf("%d", drainStart.UnixMilli())),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 0 {
		t.Fatalf("expected 0 changes while grace period is active, got %d", len(changes))
	}
}

// TestFailureIgnoresHealthyLiveness verifies that a running instance with
// liveness probe state "healthy" is not replaced or drained.
func TestFailureIgnoresHealthyLiveness(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceProbeState("aaa", "liveness"), string(types.LivenessProbeHealthy)),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for healthy liveness, got %d", len(changes))
	}
}

// TestFailureIgnoresStartupPending verifies that a running instance with
// startup probe still pending is not replaced.
func TestFailureIgnoresStartupPending(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceProbeState("aaa", "startup"), string(types.StartupProbePending)),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for pending startup, got %d", len(changes))
	}
}

// TestFailureIgnoresNotReadyReadiness verifies that readiness probe failure
// does NOT trigger instance replacement — readiness only gates endpoints.
func TestFailureIgnoresNotReadyReadiness(t *testing.T) {
	failureController := NewFailureController()

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceProbeState("aaa", "readiness"), string(types.ReadinessProbeNotReady)),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for not-ready readiness (readiness never restarts), got %d", len(changes))
	}
}

// TestFailureControllerInterface verifies that FailureController satisfies
// the Controller interface and has the correct name.
func TestFailureControllerInterface(t *testing.T) {
	failureController := NewFailureController()
	var _ Controller = failureController
	if failureController.Name() != "failure" {
		t.Fatalf("name: got %s, want failure", failureController.Name())
	}
}

// TestFailureRateLimitsReplacements verifies that the FailureController caps
// the number of replacements per cycle to MaxReplacementsPerCycle.
func TestFailureRateLimitsReplacements(t *testing.T) {
	failureController := NewFailureController()
	failureController.NewID = seqIDGen()
	failureController.MaxReplacementsPerCycle = 3

	var entries []struct{ k, v string }
	for instanceIndex := 0; instanceIndex < 10; instanceIndex++ {
		instanceID := fmt.Sprintf("inst-%03d", instanceIndex)
		entries = append(entries,
			kv(types.KeyObservedInstanceService(instanceID), "web"),
			kv(types.KeyObservedInstanceState(instanceID), "failed"),
		)
	}
	facts := buildFacts(entries...)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	replacementCount := 0
	for _, change := range changes {
		if string(change.Value) == "pending" {
			replacementCount++
		}
	}
	if replacementCount != 3 {
		t.Fatalf("expected 3 replacements (rate-limited), got %d", replacementCount)
	}
}

// TestFailureDefaultMaxReplacements verifies the default is 10.
func TestFailureDefaultMaxReplacements(t *testing.T) {
	failureController := NewFailureController()
	if failureController.MaxReplacementsPerCycle != defaultMaxReplacementsPerCycle {
		t.Fatalf("expected default %d, got %d", defaultMaxReplacementsPerCycle, failureController.MaxReplacementsPerCycle)
	}
}

// TestFailureControllerCapsDrainsPerCycle verifies that the FailureController
// begins draining at most MaxDrainsPerCycle instances per reconciliation cycle.
func TestFailureControllerCapsDrainsPerCycle(t *testing.T) {
	fixedTime := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	failureController := NewFailureController()
	failureController.NowFunc = func() time.Time { return fixedTime }
	failureController.MaxDrainsPerCycle = 5

	// Create 50 running instances with liveness=unhealthy and no drain_since.
	var entries []struct{ k, v string }
	for i := 0; i < 50; i++ {
		instanceID := fmt.Sprintf("drain-%03d", i)
		entries = append(entries,
			kv(types.KeyObservedInstanceService(instanceID), "web"),
			kv(types.KeyObservedInstanceState(instanceID), "running"),
			kv(types.KeyObservedInstanceProbeState(instanceID, "liveness"), string(types.LivenessProbeUnhealthy)),
		)
	}
	facts := buildFacts(entries...)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Each drain produces 2 changes (readiness + drain_since).
	expectedMaxChanges := failureController.MaxDrainsPerCycle * 2
	if len(changes) > expectedMaxChanges {
		t.Fatalf("expected at most %d drain changes (cap=%d), got %d",
			expectedMaxChanges, failureController.MaxDrainsPerCycle, len(changes))
	}
	if len(changes) != expectedMaxChanges {
		t.Errorf("expected exactly %d drain changes, got %d", expectedMaxChanges, len(changes))
	}
}

// TestFailureDefaultMaxDrains verifies the default drain cap.
func TestFailureDefaultMaxDrains(t *testing.T) {
	failureController := NewFailureController()
	if failureController.MaxDrainsPerCycle != defaultMaxDrainsPerCycle {
		t.Fatalf("expected default %d, got %d", defaultMaxDrainsPerCycle, failureController.MaxDrainsPerCycle)
	}
}

// TestFailureCapsRecoveriesPerCycle verifies that the controller limits how
// many drain-recovery cleanups it emits in a single cycle, preventing
// transaction overflow during mass-recovery events.
func TestFailureCapsRecoveriesPerCycle(t *testing.T) {
	fixedTime := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	failureController := NewFailureController()
	failureController.NowFunc = func() time.Time { return fixedTime }
	failureController.MaxRecoveriesPerCycle = 5

	// Create 20 running instances with drain state but healthy liveness
	// (i.e. liveness recovered while draining).
	var entries []struct{ k, v string }
	for instanceIndex := 0; instanceIndex < 20; instanceIndex++ {
		instanceID := fmt.Sprintf("recover-%03d", instanceIndex)
		entries = append(entries,
			kv(types.KeyObservedInstanceService(instanceID), "web"),
			kv(types.KeyObservedInstanceState(instanceID), "running"),
			kv(types.KeyDerivedInstanceDrainSince(instanceID), "1726000000000"),
		)
	}
	facts := buildFacts(entries...)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Each recovery emits 2 deletes. Capped at 5 recoveries = 10 changes.
	expectedMaxChanges := failureController.MaxRecoveriesPerCycle * 2
	if len(changes) > expectedMaxChanges {
		t.Fatalf("expected at most %d recovery changes (cap=%d), got %d",
			expectedMaxChanges, failureController.MaxRecoveriesPerCycle, len(changes))
	}
	if len(changes) != expectedMaxChanges {
		t.Errorf("expected exactly %d recovery changes, got %d", expectedMaxChanges, len(changes))
	}

	// All emitted changes should be deletes (clearing drain state).
	for _, change := range changes {
		if change.Type != store.OpDelete {
			t.Errorf("expected delete change, got type %d for key %s", change.Type, change.Key)
		}
	}
}

// TestFailureTotalOutputWithinBudget verifies that the hard cap prevents
// combined output from exceeding the transaction budget even when per-type
// caps allow it.
func TestFailureTotalOutputWithinBudget(t *testing.T) {
	fixedTime := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	drainStart := fixedTime.Add(-10 * time.Second)
	failureController := NewFailureController()
	failureController.NewID = seqIDGen()
	failureController.NowFunc = func() time.Time { return fixedTime }
	failureController.DrainGracePeriod = 5 * time.Second
	// Set per-type caps high enough that their sum can exceed the hard limit.
	failureController.MaxReplacementsPerCycle = 30
	failureController.MaxDrainsPerCycle = 30
	failureController.MaxRecoveriesPerCycle = 30

	var entries []struct{ k, v string }
	// 15 failed instances → 15 replacements × 4 = 60 changes.
	for instanceIndex := 0; instanceIndex < 15; instanceIndex++ {
		instanceID := fmt.Sprintf("fail-%03d", instanceIndex)
		entries = append(entries,
			kv(types.KeyObservedInstanceService(instanceID), "web"),
			kv(types.KeyObservedInstanceState(instanceID), "failed"),
		)
	}
	// 10 liveness-unhealthy draining instances past grace → 10 replacements × 4 = 40 more.
	for instanceIndex := 0; instanceIndex < 10; instanceIndex++ {
		instanceID := fmt.Sprintf("drain-%03d", instanceIndex)
		entries = append(entries,
			kv(types.KeyObservedInstanceService(instanceID), "web"),
			kv(types.KeyObservedInstanceState(instanceID), "running"),
			kv(types.KeyObservedInstanceProbeState(instanceID, "liveness"), string(types.LivenessProbeUnhealthy)),
			kv(types.KeyDerivedInstanceDrainSince(instanceID), fmt.Sprintf("%d", drainStart.UnixMilli())),
		)
	}
	facts := buildFacts(entries...)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) > maxFailureControllerChangesPerCycle {
		t.Fatalf("total changes %d exceeds hard cap %d", len(changes), maxFailureControllerChangesPerCycle)
	}
}

// TestFailureClearsDrainOnRecovery verifies that when an instance's liveness
// recovers (no longer unhealthy) while drain state exists, the controller
// emits delete changes to clear drain_readiness and drain_since.
func TestFailureClearsDrainOnRecovery(t *testing.T) {
	fixedTime := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	failureController := NewFailureController()
	failureController.NowFunc = func() time.Time { return fixedTime }

	facts := buildFacts(
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyDerivedInstanceDrainSince("aaa"), "1726000000000"),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 delete changes, got %d", len(changes))
	}
	for _, change := range changes {
		if change.Type != store.OpDelete {
			t.Errorf("expected delete, got type %d for key %s", change.Type, change.Key)
		}
	}
}
