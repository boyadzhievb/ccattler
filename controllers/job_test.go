// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// TestJobControllerCreatesInstance verifies that a desired job with no
// existing instances creates the first instance.
func TestJobControllerCreatesInstance(testContext *testing.T) {
	jobController := NewJobController()

	facts := buildFacts(
		kv(types.KeyDesiredJob("migrate"), ""),
		kv(types.KeyDesiredJobImage("migrate"), "app:v2"),
		kv(types.KeyDesiredJobCommand("migrate"), "migrate up"),
		kv(types.KeyDesiredJobCompletions("migrate"), "1"),
		kv(types.KeyDesiredJobParallelism("migrate"), "1"),
		kv(types.KeyDesiredJobBackoffLimit("migrate"), "3"),
	)

	changes, reconcileError := jobController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	if len(changes) < 4 {
		testContext.Fatalf("expected at least 4 changes (instance creation), got %d", len(changes))
	}

	foundInstanceMarker := false
	foundServiceAssignment := false
	foundPendingState := false
	foundJobOwner := false
	for _, change := range changes {
		if change.Key == types.KeyObservedInstance("migrate-0") {
			foundInstanceMarker = true
		}
		if change.Key == types.KeyObservedInstanceService("migrate-0") && string(change.Value) == "migrate" {
			foundServiceAssignment = true
		}
		if change.Key == types.KeyObservedInstanceState("migrate-0") && string(change.Value) == "pending" {
			foundPendingState = true
		}
		if change.Key == types.KeyDerivedJobInstanceOwner("migrate-0") && string(change.Value) == "migrate" {
			foundJobOwner = true
		}
	}
	if !foundInstanceMarker {
		testContext.Error("expected instance marker for migrate-0")
	}
	if !foundServiceAssignment {
		testContext.Error("expected service assignment for migrate-0")
	}
	if !foundPendingState {
		testContext.Error("expected pending state for migrate-0")
	}
	if !foundJobOwner {
		testContext.Error("expected job_owner marker for migrate-0")
	}
}

// TestJobControllerMarksComplete verifies that a job with enough
// succeeded instances is marked complete.
func TestJobControllerMarksComplete(testContext *testing.T) {
	jobController := NewJobController()

	facts := buildFacts(
		kv(types.KeyDesiredJob("migrate"), ""),
		kv(types.KeyDesiredJobImage("migrate"), "app:v2"),
		kv(types.KeyDesiredJobCompletions("migrate"), "1"),
		kv(types.KeyDesiredJobParallelism("migrate"), "1"),
		kv(types.KeyDesiredJobBackoffLimit("migrate"), "3"),
		// One stopped instance (= success for job)
		kv(types.KeyObservedInstance("migrate-0"), ""),
		kv(types.KeyObservedInstanceService("migrate-0"), "migrate"),
		kv(types.KeyObservedInstanceState("migrate-0"), string(types.InstanceStopped)),
		kv(types.KeyDerivedJobInstanceOwner("migrate-0"), "migrate"),
	)

	changes, reconcileError := jobController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	foundComplete := false
	for _, change := range changes {
		if change.Key == types.KeyDerivedJobStatus("migrate") && string(change.Value) == jobStatusComplete {
			foundComplete = true
		}
	}
	if !foundComplete {
		testContext.Error("expected job status to be set to complete")
	}
}

// TestJobControllerMarksFailed verifies that a job with too many failures
// is marked failed.
func TestJobControllerMarksFailed(testContext *testing.T) {
	jobController := NewJobController()

	facts := buildFacts(
		kv(types.KeyDesiredJob("migrate"), ""),
		kv(types.KeyDesiredJobImage("migrate"), "app:v2"),
		kv(types.KeyDesiredJobCompletions("migrate"), "1"),
		kv(types.KeyDesiredJobParallelism("migrate"), "1"),
		kv(types.KeyDesiredJobBackoffLimit("migrate"), "3"),
		// Three failed instances
		kv(types.KeyObservedInstance("migrate-0"), ""),
		kv(types.KeyObservedInstanceService("migrate-0"), "migrate"),
		kv(types.KeyObservedInstanceState("migrate-0"), string(types.InstanceFailed)),
		kv(types.KeyDerivedJobInstanceOwner("migrate-0"), "migrate"),
		kv(types.KeyObservedInstance("migrate-1"), ""),
		kv(types.KeyObservedInstanceService("migrate-1"), "migrate"),
		kv(types.KeyObservedInstanceState("migrate-1"), string(types.InstanceFailed)),
		kv(types.KeyDerivedJobInstanceOwner("migrate-1"), "migrate"),
		kv(types.KeyObservedInstance("migrate-2"), ""),
		kv(types.KeyObservedInstanceService("migrate-2"), "migrate"),
		kv(types.KeyObservedInstanceState("migrate-2"), string(types.InstanceFailed)),
		kv(types.KeyDerivedJobInstanceOwner("migrate-2"), "migrate"),
	)

	changes, reconcileError := jobController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	foundFailed := false
	for _, change := range changes {
		if change.Key == types.KeyDerivedJobStatus("migrate") && string(change.Value) == jobStatusFailed {
			foundFailed = true
		}
	}
	if !foundFailed {
		testContext.Error("expected job status to be set to failed")
	}
}

// TestJobControllerRespectsParallelism verifies that the controller
// creates up to parallelism instances concurrently.
func TestJobControllerRespectsParallelism(testContext *testing.T) {
	jobController := NewJobController()

	facts := buildFacts(
		kv(types.KeyDesiredJob("batch"), ""),
		kv(types.KeyDesiredJobImage("batch"), "worker:latest"),
		kv(types.KeyDesiredJobCompletions("batch"), "5"),
		kv(types.KeyDesiredJobParallelism("batch"), "2"),
		kv(types.KeyDesiredJobBackoffLimit("batch"), "6"),
	)

	changes, reconcileError := jobController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	instanceCreationCount := 0
	for _, change := range changes {
		if change.Key == types.KeyObservedInstanceState("batch-0") ||
			change.Key == types.KeyObservedInstanceState("batch-1") {
			instanceCreationCount++
		}
	}
	if instanceCreationCount != 2 {
		testContext.Errorf("expected 2 instances created (parallelism=2), got %d", instanceCreationCount)
	}

	// Verify no third instance was created
	for _, change := range changes {
		if change.Key == types.KeyObservedInstanceState("batch-2") {
			testContext.Error("should not create third instance when parallelism=2")
		}
	}
}

// TestJobControllerSkipsCompletedJob verifies that a job already marked
// complete produces no changes.
func TestJobControllerSkipsCompletedJob(testContext *testing.T) {
	jobController := NewJobController()

	facts := buildFacts(
		kv(types.KeyDesiredJob("migrate"), ""),
		kv(types.KeyDesiredJobImage("migrate"), "app:v2"),
		kv(types.KeyDesiredJobCompletions("migrate"), "1"),
		kv(types.KeyDerivedJobStatus("migrate"), jobStatusComplete),
	)

	changes, reconcileError := jobController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	if len(changes) != 0 {
		testContext.Errorf("expected 0 changes for completed job, got %d", len(changes))
	}
}

// TestJobControllerDoesNotExceedCompletions verifies that the controller
// does not create more instances than needed to reach completions.
func TestJobControllerDoesNotExceedCompletions(testContext *testing.T) {
	jobController := NewJobController()

	facts := buildFacts(
		kv(types.KeyDesiredJob("batch"), ""),
		kv(types.KeyDesiredJobImage("batch"), "worker:latest"),
		kv(types.KeyDesiredJobCompletions("batch"), "3"),
		kv(types.KeyDesiredJobParallelism("batch"), "5"),
		kv(types.KeyDesiredJobBackoffLimit("batch"), "6"),
	)

	changes, reconcileError := jobController.Reconcile(context.Background(), facts)
	if reconcileError != nil {
		testContext.Fatal(reconcileError)
	}

	instanceCreationCount := 0
	for _, change := range changes {
		if change.Key == types.KeyObservedInstanceState("batch-0") ||
			change.Key == types.KeyObservedInstanceState("batch-1") ||
			change.Key == types.KeyObservedInstanceState("batch-2") {
			instanceCreationCount++
		}
	}
	if instanceCreationCount != 3 {
		testContext.Errorf("expected 3 instances (completions=3, parallelism=5), got %d", instanceCreationCount)
	}
}
