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

// TestMergeWatchChannelsFanIn verifies that mergeWatchChannels correctly
// combines multiple input channels into a single output.
func TestMergeWatchChannelsFanIn(t *testing.T) {
	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()

	firstChannel := make(chan store.Event, 2)
	secondChannel := make(chan store.Event, 2)

	mergedChannel := mergeWatchChannels(ctx, []<-chan store.Event{firstChannel, secondChannel})

	firstChannel <- store.Event{Fact: store.Fact{Key: "a"}}
	secondChannel <- store.Event{Fact: store.Fact{Key: "b"}}

	close(firstChannel)
	close(secondChannel)

	received := make(map[string]bool)
	timeout := time.After(1 * time.Second)
	for len(received) < 2 {
		select {
		case watchEvent, channelOpen := <-mergedChannel:
			if !channelOpen {
				break
			}
			received[watchEvent.Fact.Key] = true
		case <-timeout:
			t.Fatal("timed out waiting for merged events")
		}
	}

	if !received["a"] || !received["b"] {
		t.Errorf("expected events from both channels, got %v", received)
	}
}

// TestMergeWatchChannelsCloseOnAllDone verifies that the merged channel
// closes when all input channels are closed.
func TestMergeWatchChannelsCloseOnAllDone(t *testing.T) {
	ctx := context.Background()

	firstChannel := make(chan store.Event)
	secondChannel := make(chan store.Event)

	mergedChannel := mergeWatchChannels(ctx, []<-chan store.Event{firstChannel, secondChannel})

	close(firstChannel)
	close(secondChannel)

	timeout := time.After(1 * time.Second)
	select {
	case _, channelOpen := <-mergedChannel:
		if channelOpen {
			t.Error("expected merged channel to close")
		}
	case <-timeout:
		t.Fatal("timed out waiting for merged channel close")
	}
}
