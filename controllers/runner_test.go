// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"fmt"
	"strings"
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

// TestRunnerCapsOversizedChangeSets verifies that the runner truncates
// oversized change sets to the transaction budget and commits the capped
// set rather than rejecting the entire cycle.
func TestRunnerCapsOversizedChangeSets(t *testing.T) {
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

	// Verify exactly maxTransactionChanges facts were committed (capped, not rejected).
	allFacts, _ := factStore.Scan(ctx, "test/")
	if len(allFacts) != maxTransactionChanges {
		t.Fatalf("expected %d facts committed (capped), got %d", maxTransactionChanges, len(allFacts))
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

// duplicateKeyController returns changes with duplicate keys, which etcd
// would reject.
type duplicateKeyController struct{}

func (controller *duplicateKeyController) Name() string    { return "duplicate-key-test" }
func (controller *duplicateKeyController) Watch() []string { return []string{"desired/"} }
func (controller *duplicateKeyController) Reconcile(_ context.Context, _ []store.Fact) ([]Change, error) {
	return []Change{
		{Type: store.OpPut, Key: "test/dup", Value: []byte("first")},
		{Type: store.OpPut, Key: "test/dup", Value: []byte("second")},
		{Type: store.OpPut, Key: "test/unique", Value: []byte("ok")},
	}, nil
}

// TestRunnerRejectsDuplicateKeys verifies that the runner catches duplicate
// keys in a change set before attempting to commit. etcd requires unique
// mutation keys per transaction.
func TestRunnerRejectsDuplicateKeys(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	controller := &duplicateKeyController{}
	runner := NewRunner(factStore, controller)

	ctx := context.Background()

	factStore.Put(ctx, "desired/service/web/instances", []byte("1"))

	_, reconcileError := runner.attemptSingleReconciliation(ctx, controller)
	if reconcileError == nil {
		t.Fatal("expected error for duplicate keys, got nil")
	}
	if !strings.Contains(reconcileError.Error(), "duplicate key") {
		t.Fatalf("expected 'duplicate key' in error, got: %s", reconcileError.Error())
	}

	// No facts should have been committed.
	allFacts, _ := factStore.Scan(ctx, "test/")
	if len(allFacts) != 0 {
		t.Fatalf("expected zero facts committed after duplicate key rejection, got %d", len(allFacts))
	}
}

// TestRunnerClampsMinAttempts verifies that SetMaxReconciliationAttempts
// clamps values below 1 to prevent silent no-op reconciliation.
func TestRunnerClampsMinAttempts(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	runner := NewRunner(factStore)
	runner.SetMaxReconciliationAttempts(0)
	if runner.maxReconciliationAttempts != 1 {
		t.Fatalf("expected clamped to 1, got %d", runner.maxReconciliationAttempts)
	}
	runner.SetMaxReconciliationAttempts(-5)
	if runner.maxReconciliationAttempts != 1 {
		t.Fatalf("expected clamped to 1 for negative, got %d", runner.maxReconciliationAttempts)
	}
	runner.SetMaxReconciliationAttempts(10)
	if runner.maxReconciliationAttempts != 10 {
		t.Fatalf("expected 10, got %d", runner.maxReconciliationAttempts)
	}
}

// takeWholeGroups tests

func TestTakeWholeGroupsPreservesAtomicGroups(t *testing.T) {
	changes := []Change{
		{Type: store.OpPut, Key: "a/1", Value: []byte("v"), Group: "g1"},
		{Type: store.OpPut, Key: "a/2", Value: []byte("v"), Group: "g1"},
		{Type: store.OpPut, Key: "a/3", Value: []byte("v"), Group: "g1"},
		{Type: store.OpPut, Key: "b/1", Value: []byte("v"), Group: "g2"},
		{Type: store.OpPut, Key: "b/2", Value: []byte("v"), Group: "g2"},
	}

	selected, deferred, groupErr := takeWholeGroups(changes, 4)
	if groupErr != nil {
		t.Fatalf("unexpected error: %v", groupErr)
	}
	if len(selected) != 3 {
		t.Fatalf("expected 3 selected (one complete group), got %d", len(selected))
	}
	if deferred != 2 {
		t.Fatalf("expected 2 deferred, got %d", deferred)
	}
	for _, change := range selected {
		if change.Group != "g1" {
			t.Fatalf("expected all selected from g1, got %s", change.Group)
		}
	}
}

func TestTakeWholeGroupsHandlesUngroupedChanges(t *testing.T) {
	changes := make([]Change, 10)
	for changeIndex := range changes {
		changes[changeIndex] = Change{
			Type: store.OpPut, Key: fmt.Sprintf("k/%d", changeIndex), Value: []byte("v"),
		}
	}

	selected, deferred, groupErr := takeWholeGroups(changes, 7)
	if groupErr != nil {
		t.Fatalf("unexpected error: %v", groupErr)
	}
	if len(selected) != 7 {
		t.Fatalf("expected 7 standalone changes, got %d", len(selected))
	}
	if deferred != 3 {
		t.Fatalf("expected 3 deferred, got %d", deferred)
	}
}

func TestTakeWholeGroupsRejectsOversizedGroup(t *testing.T) {
	changes := []Change{
		{Type: store.OpPut, Key: "big/1", Value: []byte("v"), Group: "toobig"},
		{Type: store.OpPut, Key: "big/2", Value: []byte("v"), Group: "toobig"},
		{Type: store.OpPut, Key: "big/3", Value: []byte("v"), Group: "toobig"},
		{Type: store.OpPut, Key: "small/1", Value: []byte("v"), Group: "fits"},
	}

	_, _, groupErr := takeWholeGroups(changes, 2)
	if groupErr == nil {
		t.Fatal("expected error for oversized atomic group, got nil")
	}
	if !strings.Contains(groupErr.Error(), "toobig") {
		t.Fatalf("expected error to mention group ID 'toobig', got: %v", groupErr)
	}
}

// groupedController produces changes with explicit atomic groups.
type groupedController struct {
	groups [][]Change
}

func (controller *groupedController) Name() string    { return "grouped-test" }
func (controller *groupedController) Watch() []string { return []string{"desired/"} }
func (controller *groupedController) Reconcile(_ context.Context, _ []store.Fact) ([]Change, error) {
	var allChanges []Change
	for _, groupChanges := range controller.groups {
		allChanges = append(allChanges, groupChanges...)
	}
	return allChanges, nil
}

func TestRunnerGroupAwareTruncation(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	// Create 20 groups of 4 changes each = 80 total, exceeds 60 budget.
	var groups [][]Change
	for groupIndex := range 20 {
		groupID := fmt.Sprintf("grp-%02d", groupIndex)
		group := groupedChanges(groupID,
			Change{Type: store.OpPut, Key: fmt.Sprintf("test/%02d/a", groupIndex), Value: []byte("v")},
			Change{Type: store.OpPut, Key: fmt.Sprintf("test/%02d/b", groupIndex), Value: []byte("v")},
			Change{Type: store.OpPut, Key: fmt.Sprintf("test/%02d/c", groupIndex), Value: []byte("v")},
			Change{Type: store.OpPut, Key: fmt.Sprintf("test/%02d/d", groupIndex), Value: []byte("v")},
		)
		groups = append(groups, group)
	}

	controller := &groupedController{groups: groups}
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

	allFacts, _ := factStore.Scan(ctx, "test/")
	// 60 / 4 = 15 complete groups = 60 changes committed.
	if len(allFacts) != 60 {
		t.Fatalf("expected 60 facts (15 complete groups × 4), got %d", len(allFacts))
	}

	// Verify no partial group: every committed group should have all 4 keys.
	groupCounts := make(map[string]int)
	for _, fact := range allFacts {
		parts := strings.SplitN(strings.TrimPrefix(fact.Key, "test/"), "/", 2)
		groupCounts[parts[0]]++
	}
	for groupKey, keyCount := range groupCounts {
		if keyCount != 4 {
			t.Fatalf("group %s has %d keys, expected 4 (partial group committed)", groupKey, keyCount)
		}
	}
}
