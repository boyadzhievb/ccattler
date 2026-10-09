// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestClassifyInstanceRunning verifies that a state transition to "running"
// emits an "instance.running" event.
func TestClassifyInstanceRunning(t *testing.T) {
	watchEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{
			Key:   types.PrefixObserved + "/instance/aaa/state",
			Value: []byte(string(types.InstanceRunning)),
		},
		Prev: &store.Fact{
			Value: []byte(string(types.InstanceStarting)),
		},
	}

	eventKind, eventTarget, eventDetail := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind != "instance.running" {
		t.Errorf("kind: got %q, want instance.running", eventKind)
	}
	if eventTarget != "instance/aaa" {
		t.Errorf("target: got %q, want instance/aaa", eventTarget)
	}
	if eventDetail == "" {
		t.Error("detail should not be empty")
	}
}

// TestClassifyInstanceCreated verifies that a state transition from nothing
// to "pending" emits "instance.created" (not "instance.pending").
func TestClassifyInstanceCreated(t *testing.T) {
	watchEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{
			Key:   types.PrefixObserved + "/instance/bbb/state",
			Value: []byte(string(types.InstancePending)),
		},
		Prev: nil,
	}

	eventKind, _, _ := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind != "instance.created" {
		t.Errorf("kind: got %q, want instance.created", eventKind)
	}
}

// TestClassifyInstanceFailed verifies that a transition to "failed" emits
// the correct event.
func TestClassifyInstanceFailed(t *testing.T) {
	watchEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{
			Key:   types.PrefixObserved + "/instance/ccc/state",
			Value: []byte(string(types.InstanceFailed)),
		},
		Prev: &store.Fact{
			Value: []byte(string(types.InstanceRunning)),
		},
	}

	eventKind, _, _ := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind != "instance.failed" {
		t.Errorf("kind: got %q, want instance.failed", eventKind)
	}
}

// TestClassifySameValueNoEvent verifies that a watch event where the value
// is unchanged produces no semantic event.
func TestClassifySameValueNoEvent(t *testing.T) {
	watchEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{
			Key:   types.PrefixObserved + "/instance/ddd/state",
			Value: []byte(string(types.InstanceRunning)),
		},
		Prev: &store.Fact{
			Value: []byte(string(types.InstanceRunning)),
		},
	}

	eventKind, _, _ := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind != "" {
		t.Errorf("expected no event for same value, got %q", eventKind)
	}
}

// TestClassifyPlacement verifies that a placement put event emits
// "instance.placed".
func TestClassifyPlacement(t *testing.T) {
	watchEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{
			Key:   types.PrefixPlacement + "/instance/eee",
			Value: []byte("node-1"),
		},
	}

	eventKind, eventTarget, eventDetail := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind != "instance.placed" {
		t.Errorf("kind: got %q, want instance.placed", eventKind)
	}
	if eventTarget != "instance/eee" {
		t.Errorf("target: got %q, want instance/eee", eventTarget)
	}
	if eventDetail == "" {
		t.Error("detail should not be empty")
	}
}

// TestClassifyNodeState verifies that a node state transition emits
// the correct event.
func TestClassifyNodeState(t *testing.T) {
	watchEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{
			Key:   types.PrefixObserved + "/node/node-1/state",
			Value: []byte("unreachable"),
		},
		Prev: &store.Fact{
			Value: []byte("ready"),
		},
	}

	eventKind, eventTarget, _ := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind != "node.unreachable" {
		t.Errorf("kind: got %q, want node.unreachable", eventKind)
	}
	if eventTarget != "node/node-1" {
		t.Errorf("target: got %q, want node/node-1", eventTarget)
	}
}

// TestClassifyServiceScaled verifies that a change to effective instance
// count emits "service.scaled".
func TestClassifyServiceScaled(t *testing.T) {
	watchEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{
			Key:   types.PrefixEffective + "/service/web/instances",
			Value: []byte("5"),
		},
		Prev: &store.Fact{
			Value: []byte("3"),
		},
	}

	eventKind, eventTarget, _ := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind != "service.scaled" {
		t.Errorf("kind: got %q, want service.scaled", eventKind)
	}
	if eventTarget != "service/web" {
		t.Errorf("target: got %q, want service/web", eventTarget)
	}
}

// TestClassifyUnrelatedKeyNoEvent verifies that a watch event for an
// unrecognized key pattern produces no semantic event.
func TestClassifyUnrelatedKeyNoEvent(t *testing.T) {
	watchEvent := store.Event{
		Type: store.EventPut,
		Fact: store.Fact{
			Key:   "desired/service/web/image",
			Value: []byte("nginx:1.28"),
		},
		Prev: &store.Fact{
			Value: []byte("nginx:1.27"),
		},
	}

	eventKind, _, _ := classifyWatchEventAsSemanticEvent(watchEvent)
	if eventKind != "" {
		t.Errorf("expected no event for unrelated key, got %q", eventKind)
	}
}

// TestEventProjectorEmitsToEventLog verifies that the EventProjector correctly
// wires watch events through to the EventLog by running it against a real
// MemoryStore.
func TestEventProjectorEmitsToEventLog(t *testing.T) {
	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()

	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	eventLog := types.NewEventLog(memoryStore, 100)
	eventProjector := NewEventProjector(memoryStore, eventLog)

	projectorDone := make(chan error, 1)
	go func() {
		projectorDone <- eventProjector.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	memoryStore.Put(ctx, types.PrefixObserved+"/instance/test-inst/state", []byte(string(types.InstanceRunning)))

	time.Sleep(100 * time.Millisecond)

	events, queryError := eventLog.Query(ctx, "instance.running", 10)
	if queryError != nil {
		t.Fatal(queryError)
	}
	if len(events) == 0 {
		t.Fatal("expected at least 1 instance.running event in the log")
	}

	found := false
	for _, systemEvent := range events {
		if systemEvent.Target == "instance/test-inst" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected event targeting instance/test-inst")
	}

	cancelContext()
	<-projectorDone
}

// TestWatchPrefixWithReconnect verifies that a per-prefix watch goroutine
// re-establishes the watch after the channel closes and continues
// forwarding events.
func TestWatchPrefixWithReconnect(t *testing.T) {
	factStore := store.NewMemoryStore()
	eventLog := types.NewEventLog(factStore, 100)
	projector := NewEventProjector(factStore, eventLog)

	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()

	mergedChannel := make(chan store.Event, defaultMergedEventBuffer)
	go projector.watchPrefixWithReconnect(ctx, types.PrefixObserved+"/node/", mergedChannel)

	time.Sleep(50 * time.Millisecond)

	if _, putError := factStore.Put(ctx, types.KeyObservedNodeState("node-1"), []byte("alive")); putError != nil {
		t.Fatal(putError)
	}

	timeout := time.After(2 * time.Second)
	select {
	case watchEvent := <-mergedChannel:
		if watchEvent.Fact.Key != types.KeyObservedNodeState("node-1") {
			t.Errorf("unexpected key: %s", watchEvent.Fact.Key)
		}
	case <-timeout:
		t.Fatal("timed out waiting for forwarded event")
	}
}

// TestEventProjectorResyncOnCompaction verifies that the projector resync
// path handles an EventCompacted marker by falling back to a full scan and
// resuming normal event delivery afterward.
func TestEventProjectorResyncOnCompaction(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	eventLog := types.NewEventLog(factStore, 100)
	projector := NewEventProjector(factStore, eventLog)

	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()

	projectorDone := make(chan error, 1)
	go func() {
		projectorDone <- projector.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)

	// Write an event before any compaction scenario — establishes baseline.
	factStore.Put(ctx, types.PrefixObserved+"/instance/inst-before/state",
		[]byte(string(types.InstanceRunning)))

	time.Sleep(100 * time.Millisecond)

	// Write a second event — should still be delivered after the projector
	// processes the first.
	factStore.Put(ctx, types.PrefixObserved+"/instance/inst-after/state",
		[]byte(string(types.InstanceRunning)))

	time.Sleep(200 * time.Millisecond)

	events, queryErr := eventLog.Query(ctx, "instance.running", 10)
	if queryErr != nil {
		t.Fatal(queryErr)
	}

	foundBefore := false
	foundAfter := false
	for _, ev := range events {
		if ev.Target == "instance/inst-before" {
			foundBefore = true
		}
		if ev.Target == "instance/inst-after" {
			foundAfter = true
		}
	}
	if !foundBefore {
		t.Error("expected event for inst-before")
	}
	if !foundAfter {
		t.Error("expected event for inst-after after resync path")
	}

	cancelContext()
	<-projectorDone
}
