package controllers

import (
	"context"
	"fmt"
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
	if changes[0].Key != types.KeyObservedInstanceState("aaa") {
		t.Errorf("expected state key for aaa, got %s", changes[0].Key)
	}
	if string(changes[0].Value) != "stopped" {
		t.Errorf("expected stopped, got %s", changes[0].Value)
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

	stopped := 0
	pending := 0
	for _, ch := range changes {
		if ch.Type == store.OpPut && string(ch.Value) == "stopped" {
			stopped++
		}
		if ch.Type == store.OpPut && string(ch.Value) == "pending" {
			pending++
		}
	}
	if stopped != 2 {
		t.Errorf("expected 2 stopped, got %d", stopped)
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
	if string(changes[0].Value) != "stopped" {
		t.Errorf("expected stopped, got %s", changes[0].Value)
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

	if changes[0].Key != types.KeyObservedInstanceProbeState("aaa", "readiness") {
		t.Errorf("first change should set readiness, got key %s", changes[0].Key)
	}
	if string(changes[0].Value) != string(types.ReadinessProbeNotReady) {
		t.Errorf("readiness should be not-ready, got %s", changes[0].Value)
	}

	if changes[1].Key != types.KeyObservedInstanceDrainSince("aaa") {
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
		kv(types.KeyObservedInstanceDrainSince("aaa"), fmt.Sprintf("%d", drainStart.UnixMilli())),
	)

	changes, err := failureController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 4 {
		t.Fatalf("expected 4 changes (stop + replacement), got %d", len(changes))
	}
	if string(changes[0].Value) != "stopped" {
		t.Errorf("expected stopped, got %s", changes[0].Value)
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
		kv(types.KeyObservedInstanceDrainSince("aaa"), fmt.Sprintf("%d", drainStart.UnixMilli())),
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
