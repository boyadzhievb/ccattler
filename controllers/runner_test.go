// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tracing"
	"github.com/boyadzhievb/ccattler/types"
)

func waitForInstances(t *testing.T, s store.StateStore, service string, count int, timeout time.Duration) []types.Instance {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		instances, _ := types.ListInstances(context.Background(), s)
		var matching []types.Instance
		for _, inst := range instances {
			if inst.Service == service && inst.State != types.InstanceStopped {
				matching = append(matching, inst)
			}
		}
		if len(matching) == count {
			return matching
		}
		time.Sleep(10 * time.Millisecond)
	}
	instances, _ := types.ListInstances(context.Background(), s)
	t.Fatalf("timed out waiting for %d %s instances, have %d total instances", count, service, len(instances))
	return nil
}

func TestRunnerCreatesInstances(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	runner := NewRunner(factStore, instanceController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	// Write desired state: web service wants 3 instances.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	instances := waitForInstances(t, factStore, "web", 3, 2*time.Second)
	for _, inst := range instances {
		if inst.State != types.InstancePending {
			t.Errorf("instance %s: expected pending, got %s", inst.ID, inst.State)
		}
	}
}

func TestRunnerScalesUp(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	runner := NewRunner(factStore, instanceController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Seed with 2 running instances.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("2"))
	for _, id := range []string{"existing-1", "existing-2"} {
		types.WriteInstance(ctx, factStore, types.Instance{
			ID: id, Service: "web", State: types.InstanceRunning,
		})
	}

	go runner.Run(ctx)

	// Already satisfied — should stay at 2.
	time.Sleep(100 * time.Millisecond)
	instances := waitForInstances(t, factStore, "web", 2, 500*time.Millisecond)
	if len(instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(instances))
	}

	// Scale up to 5.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("5"))

	waitForInstances(t, factStore, "web", 5, 2*time.Second)
}

func TestRunnerScalesDown(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	instanceController := NewInstanceController()

	runner := NewRunner(factStore, instanceController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Seed with 4 running instances, desired is 4.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("4"))
	for i := 1; i <= 4; i++ {
		types.WriteInstance(ctx, factStore, types.Instance{
			ID: fmt.Sprintf("api-%d", i), Service: "api", State: types.InstanceRunning,
		})
	}

	go runner.Run(ctx)

	// Scale down to 2.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("2"))

	waitForInstances(t, factStore, "api", 2, 2*time.Second)
}

func TestRunnerMultipleControllers(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	// A trivial second controller that just counts reconcile calls.
	noop := &noopController{}

	runner := NewRunner(factStore, instanceController, noop)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("1"))

	waitForInstances(t, factStore, "web", 1, 2*time.Second)
}

type noopController struct{}

func (n *noopController) Name() string    { return "noop" }
func (n *noopController) Watch() []string { return []string{types.ScanObservedInstances} }
func (n *noopController) Reconcile(_ context.Context, _ []store.Fact) ([]Change, error) {
	return nil, nil
}

// traceCapturingController records the trace context seen during Reconcile.
type traceCapturingController struct {
	capturedTraceIDs atomic.Value
	reconcileCount   atomic.Int64
}

func (controller *traceCapturingController) Name() string    { return "trace-capture" }
func (controller *traceCapturingController) Watch() []string { return []string{"desired/"} }
func (controller *traceCapturingController) Reconcile(ctx context.Context, _ []store.Fact) ([]Change, error) {
	traceContext := tracing.TraceFromContext(ctx)
	if traceContext.TraceID != "" {
		controller.capturedTraceIDs.Store(traceContext.TraceID)
	}
	controller.reconcileCount.Add(1)
	return nil, nil
}

func TestReconciliationCyclePropagatesToController(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	captureController := &traceCapturingController{}
	runner := NewRunner(factStore, captureController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	// Trigger a reconciliation by writing a fact.
	factStore.Put(ctx, "desired/service/web/instances", []byte("1"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if captureController.reconcileCount.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if captureController.reconcileCount.Load() == 0 {
		t.Fatal("controller was never reconciled")
	}

	capturedID, ok := captureController.capturedTraceIDs.Load().(string)
	if !ok || capturedID == "" {
		t.Fatal("expected trace context to be propagated to controller Reconcile, got empty trace ID")
	}
	if len(capturedID) != 32 {
		t.Errorf("expected 32-char trace ID, got %d chars: %s", len(capturedID), capturedID)
	}
}

func TestEachReconciliationCycleGetsUniqueTraceID(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	captureController := &traceCapturingController{}
	runner := NewRunner(factStore, captureController)
	runner.SetDebounce(5 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runner.Run(ctx)

	// Trigger first reconciliation.
	factStore.Put(ctx, "desired/service/web/instances", []byte("1"))
	time.Sleep(100 * time.Millisecond)
	firstTraceID, _ := captureController.capturedTraceIDs.Load().(string)

	// Trigger second reconciliation.
	factStore.Put(ctx, "desired/service/web/instances", []byte("2"))
	time.Sleep(100 * time.Millisecond)
	secondTraceID, _ := captureController.capturedTraceIDs.Load().(string)

	if firstTraceID == "" || secondTraceID == "" {
		t.Fatal("expected both reconciliation cycles to have trace IDs")
	}
	if firstTraceID == secondTraceID {
		t.Errorf("expected different trace IDs per reconciliation cycle, both were %s", firstTraceID)
	}
}

// oversizedController returns more changes than maxTransactionChanges.
type oversizedController struct {
	changeCount int
}

func (controller *oversizedController) Name() string    { return "oversized-test" }
func (controller *oversizedController) Watch() []string { return []string{"desired/"} }
func (controller *oversizedController) Reconcile(_ context.Context, _ []store.Fact) ([]Change, error) {
	changes := make([]Change, controller.changeCount)
	for i := range changes {
		changes[i] = Change{
			Type:  store.OpPut,
			Key:   fmt.Sprintf("test/key/%04d", i),
			Value: []byte("v"),
		}
	}
	return changes, nil
}

// TestRunnerRejectsOversizedChangeSets verifies that the runner skips a
// reconciliation cycle instead of committing a partial transaction when the
// controller produces more changes than the transaction budget allows.
func TestRunnerRejectsOversizedChangeSets(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	controller := &oversizedController{changeCount: 200}
	runner := NewRunner(factStore, controller)

	ctx := context.Background()

	// Seed a fact so the store is non-empty for the scan.
	factStore.Put(ctx, "desired/service/web/instances", []byte("1"))

	conflictDetected, reconcileError := runner.attemptSingleReconciliation(ctx, controller)
	if reconcileError != nil {
		t.Fatalf("expected no error, got %v", reconcileError)
	}
	if conflictDetected {
		t.Fatal("expected no conflict, got conflict")
	}

	// Verify no facts were written by the transaction (the only fact should be
	// the one we seeded above).
	allFacts, _ := factStore.Scan(ctx, "test/")
	if len(allFacts) > 0 {
		t.Fatalf("expected zero test facts committed, got %d", len(allFacts))
	}
}

// TestRunnerCommitsWithinBudgetChangeSets verifies that change sets at or
// below the transaction budget are committed normally.
func TestRunnerCommitsWithinBudgetChangeSets(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	controller := &oversizedController{changeCount: maxTransactionChanges}
	runner := NewRunner(factStore, controller)

	ctx := context.Background()

	factStore.Put(ctx, "desired/service/web/instances", []byte("1"))

	conflictDetected, reconcileError := runner.attemptSingleReconciliation(ctx, controller)
	if reconcileError != nil {
		t.Fatalf("expected no error, got %v", reconcileError)
	}
	if conflictDetected {
		t.Fatal("expected no conflict")
	}

	allFacts, _ := factStore.Scan(ctx, "test/key/")
	if len(allFacts) != maxTransactionChanges {
		t.Fatalf("expected %d facts committed, got %d", maxTransactionChanges, len(allFacts))
	}
}
