// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// TestDeltaBufferCoalescesEvents verifies that multiple events for the same
// key are coalesced, keeping the original Prev and latest Fact.
func TestDeltaBufferCoalescesEvents(testHandle *testing.T) {
	buffer := newDeltaBuffer()

	firstEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{Key: "observed/instance/abc/state", Value: []byte("pending")},
		Prev: nil,
	}
	secondEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{Key: "observed/instance/abc/state", Value: []byte("running")},
		Prev: &store.Fact{Value: []byte("pending")},
	}

	buffer.appendEvent(firstEvent)
	buffer.appendEvent(secondEvent)

	drainedEvents := buffer.drain()
	if len(drainedEvents) != 1 {
		testHandle.Fatalf("expected 1 coalesced event, got %d", len(drainedEvents))
	}

	coalescedEvent := drainedEvents[0]
	if string(coalescedEvent.Fact.Value) != "running" {
		testHandle.Errorf("expected latest value 'running', got %q", string(coalescedEvent.Fact.Value))
	}
	if coalescedEvent.Prev != nil {
		testHandle.Error("expected nil Prev (original first event had no Prev)")
	}
}

// TestDeltaBufferOverflowReturnsNil verifies that drain returns nil after
// overflow is marked, signaling a full reconcile is needed.
func TestDeltaBufferOverflowReturnsNil(testHandle *testing.T) {
	buffer := newDeltaBuffer()

	buffer.appendEvent(store.Event{
		Type: store.EventPut,
		Fact: store.Fact{Key: "test/key", Value: []byte("value")},
	})
	buffer.markOverflow()

	drainedEvents := buffer.drain()
	if drainedEvents != nil {
		testHandle.Fatalf("expected nil after overflow, got %d events", len(drainedEvents))
	}

	buffer.appendEvent(store.Event{
		Type: store.EventPut,
		Fact: store.Fact{Key: "test/key2", Value: []byte("value2")},
	})
	drainedEvents = buffer.drain()
	if len(drainedEvents) != 1 {
		testHandle.Fatalf("expected 1 event after overflow reset, got %d", len(drainedEvents))
	}
}

// TestDeltaBufferDrainClearsBuffer verifies that drain empties the buffer.
func TestDeltaBufferDrainClearsBuffer(testHandle *testing.T) {
	buffer := newDeltaBuffer()

	buffer.appendEvent(store.Event{
		Type: store.EventPut,
		Fact: store.Fact{Key: "test/key", Value: []byte("value")},
	})

	firstDrain := buffer.drain()
	if len(firstDrain) != 1 {
		testHandle.Fatalf("first drain: expected 1 event, got %d", len(firstDrain))
	}

	secondDrain := buffer.drain()
	if len(secondDrain) != 0 {
		testHandle.Fatalf("second drain: expected 0 events, got %d", len(secondDrain))
	}
}

// mockDeltaController implements both Controller and DeltaController for
// testing the runner's delta integration.
type mockDeltaController struct {
	reconcileCallCount      atomic.Int32
	deltaReconcileCallCount atomic.Int32
	lastDeltaEvents         []store.Event
	deltaEventsMutex        sync.Mutex
	fallbackOnDelta         bool
}

func (mockController *mockDeltaController) Name() string { return "mock-delta" }

func (mockController *mockDeltaController) Watch() []string {
	return []string{"test/"}
}

func (mockController *mockDeltaController) Reconcile(_ context.Context, _ []store.Fact) ([]Change, error) {
	mockController.reconcileCallCount.Add(1)
	return nil, nil
}

func (mockController *mockDeltaController) ReconcileDelta(_ context.Context, _ []store.Fact, changedEvents []store.Event) ([]Change, error) {
	mockController.deltaReconcileCallCount.Add(1)
	mockController.deltaEventsMutex.Lock()
	mockController.lastDeltaEvents = changedEvents
	mockController.deltaEventsMutex.Unlock()
	if mockController.fallbackOnDelta {
		return nil, ErrDeltaFallback
	}
	return nil, nil
}

// TestRunnerCallsDeltaReconcileWithEvents verifies that when a DeltaController
// is used and a watched key changes, the runner calls ReconcileDelta with the
// accumulated events instead of Reconcile.
func TestRunnerCallsDeltaReconcileWithEvents(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	mockController := &mockDeltaController{}
	controllerRunner := NewRunner(factStore, mockController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	controllerRunner.SetResyncInterval(0)

	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()

	runnerDone := make(chan error, 1)
	go func() { runnerDone <- controllerRunner.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)

	factStore.Put(ctx, "test/instance/x", []byte("hello"))

	time.Sleep(200 * time.Millisecond)

	deltaCallCount := mockController.deltaReconcileCallCount.Load()
	if deltaCallCount == 0 {
		testHandle.Error("expected at least 1 ReconcileDelta call, got 0")
	}

	mockController.deltaEventsMutex.Lock()
	lastEvents := mockController.lastDeltaEvents
	mockController.deltaEventsMutex.Unlock()

	if len(lastEvents) == 0 {
		testHandle.Error("expected ReconcileDelta to receive events, got empty slice")
	}

	cancelContext()
	<-runnerDone
}

// TestRunnerDeltaFallbackCallsFullReconcile verifies that when ReconcileDelta
// returns ErrDeltaFallback, the runner falls back to calling Reconcile.
func TestRunnerDeltaFallbackCallsFullReconcile(testHandle *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	mockController := &mockDeltaController{fallbackOnDelta: true}
	controllerRunner := NewRunner(factStore, mockController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	controllerRunner.SetResyncInterval(0)

	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()

	runnerDone := make(chan error, 1)
	go func() { runnerDone <- controllerRunner.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)

	factStore.Put(ctx, "test/key", []byte("trigger"))

	time.Sleep(200 * time.Millisecond)

	deltaCount := mockController.deltaReconcileCallCount.Load()
	fullCount := mockController.reconcileCallCount.Load()

	if deltaCount == 0 {
		testHandle.Error("expected ReconcileDelta to be called (before fallback)")
	}
	if fullCount < 2 {
		testHandle.Errorf("expected at least 2 full Reconcile calls (initial + fallback), got %d", fullCount)
	}

	cancelContext()
	<-runnerDone
}
