// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package scheduler

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// unplacedQuietWindow is how long the runner-level test watches for
// self-triggered scheduler cycles once the unplaced facts exist.
const unplacedQuietWindow = 300 * time.Millisecond

// maxReconcilesInQuietWindow bounds the scheduler cycles allowed in that
// window; a write loop with a 10 ms debounce gives dozens.
const maxReconcilesInQuietWindow = 3

// unplacedInstanceFacts returns one alive node too small for web's cpu 2000m
// request and one pending web instance, plus the extra facts.
func unplacedInstanceFacts(extraFacts ...store.Fact) []store.Fact {
	facts := buildFacts(
		kv(types.KeyDesiredService("web"), ""),
		kv(types.KeyDesiredServiceResourcesCPU("web"), "2000m"),
		kv(types.KeyObservedInstanceService("web-1"), "web"),
		kv(types.KeyObservedInstanceState("web-1"), "pending"),
		kv(types.KeyObservedNodeState("node-1"), "alive"),
		kv(types.KeyObservedNodeAvailableCPU("node-1"), "1000"),
		kv(types.KeyObservedNodeAvailableMemory("node-1"), "4096"),
	)
	facts = append(facts, extraFacts...)
	store.SortFacts(facts)
	return facts
}

// TestSchedulerDoesNotRewriteUnchangedUnplacedFacts: 3g.1 (testbed F1). The
// first cycle writes web-1's unplaced reason and requirements; the second
// cycle, which reads them back, proposes no change for either key.
func TestSchedulerDoesNotRewriteUnchangedUnplacedFacts(t *testing.T) {
	placementScheduler := newTestScheduler()
	firstChanges, firstError := placementScheduler.Reconcile(context.Background(), unplacedInstanceFacts())
	if firstError != nil {
		t.Fatal(firstError)
	}
	unplacedKeys := []string{types.KeyDerivedSchedulerUnplacedReason("web-1"), types.KeyDerivedSchedulerUnplacedRequirements("web-1")}
	var writtenFacts []store.Fact
	for _, unplacedKey := range unplacedKeys {
		writtenValue, written := putValueOf(firstChanges, unplacedKey)
		if !written {
			t.Fatalf("first cycle wrote no %s; the test needs it", unplacedKey)
		}
		writtenFacts = append(writtenFacts, store.Fact{Key: unplacedKey, Value: []byte(writtenValue)})
	}

	secondChanges, secondError := placementScheduler.Reconcile(context.Background(), unplacedInstanceFacts(writtenFacts...))
	if secondError != nil {
		t.Fatal(secondError)
	}
	for _, change := range secondChanges {
		if strings.HasPrefix(change.Key, types.ScanDerivedSchedulerUnplaced) {
			t.Errorf("second cycle proposed %v %s=%q although the stored value is unchanged", change.Type, change.Key, change.Value)
		}
	}
}

// etcdLikeStore wraps a store so a put of an unchanged value still produces a
// watch event, as etcd does (a new revision); the memory store drops such puts,
// which would hide a write loop.
type etcdLikeStore struct {
	store.StateStore
}

// Transaction turns each put of a key's current value into a delete and a put
// of that value in the same transaction, so watchers see the write.
func (etcdStore *etcdLikeStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	var operations []store.Op
	for _, operation := range onSuccess {
		if operation.Type == store.OpPut {
			if currentFact, getError := etcdStore.Get(ctx, operation.Key); getError == nil && bytes.Equal(currentFact.Value, operation.Value) {
				operations = append(operations, store.Op{Type: store.OpDelete, Key: operation.Key})
			}
		}
		operations = append(operations, operation)
	}
	return etcdStore.StateStore.Transaction(ctx, compares, operations, onFailure)
}

// countingScheduler counts the scheduler's reconcile cycles.
type countingScheduler struct {
	*Scheduler
	reconcileCount atomic.Int64 // reconcileCount is the number of Reconcile calls so far
}

// Reconcile counts the cycle and delegates to the scheduler.
func (counting *countingScheduler) Reconcile(ctx context.Context, facts []store.Fact) ([]controllers.Change, error) {
	counting.reconcileCount.Add(1)
	return counting.Scheduler.Reconcile(ctx, facts)
}

// TestSchedulerDoesNotRetriggerItselfWithAnUnplacedInstance: 3g.1 (testbed
// F1), runner level with a real watch and resync off. Once web-1's unplaced
// facts exist, the scheduler's own writes do not wake it again and again.
func TestSchedulerDoesNotRetriggerItselfWithAnUnplacedInstance(t *testing.T) {
	factStore := &etcdLikeStore{StateStore: store.NewMemoryStore()}
	defer func() { _ = factStore.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, fact := range unplacedInstanceFacts() {
		if _, putError := factStore.Put(ctx, fact.Key, fact.Value); putError != nil {
			t.Fatal(putError)
		}
	}

	scheduler := &countingScheduler{Scheduler: newTestScheduler()}
	controllerRunner := controllers.NewRunner(factStore, scheduler)
	controllerRunner.SetDebounce(schedulerRunnerPollInterval)
	controllerRunner.SetResyncInterval(0)
	runnerDone := make(chan struct{})
	go func() {
		defer close(runnerDone)
		_ = controllerRunner.Run(ctx)
	}()
	defer func() {
		cancel()
		<-runnerDone
	}()

	waitForStoreCondition(t, "web-1's unplaced facts are written", func() bool {
		_, reasonError := factStore.Get(ctx, types.KeyDerivedSchedulerUnplacedReason("web-1"))
		_, requirementsError := factStore.Get(ctx, types.KeyDerivedSchedulerUnplacedRequirements("web-1"))
		return reasonError == nil && requirementsError == nil
	})
	reconcilesBefore := scheduler.reconcileCount.Load()
	<-time.After(unplacedQuietWindow)
	if extraReconciles := scheduler.reconcileCount.Load() - reconcilesBefore; extraReconciles > maxReconcilesInQuietWindow {
		t.Errorf("%d scheduler cycles in %v with nothing changed, want at most %d (a write loop)",
			extraReconciles, unplacedQuietWindow, maxReconcilesInQuietWindow)
	}
}
