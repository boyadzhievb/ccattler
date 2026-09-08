package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

func TestEventLogEmitAndQuery(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 100)

	event, err := eventLog.Emit(ctx, "instance.created", "service/web", "created instance inst-001 for service web", "instance-controller")
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if event.Kind != "instance.created" {
		t.Errorf("kind = %q, want instance.created", event.Kind)
	}
	if event.Target != "service/web" {
		t.Errorf("target = %q, want service/web", event.Target)
	}
	if event.ID == "" {
		t.Error("event ID should not be empty")
	}

	events, err := eventLog.Query(ctx, "instance.created", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Source != "instance-controller" {
		t.Errorf("source = %q, want instance-controller", events[0].Source)
	}
}

func TestEventLogQueryByKind(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 100)

	eventLog.Emit(ctx, "instance.created", "service/web", "new instance", "instance-ctrl")
	eventLog.Emit(ctx, "node.failed", "node/n1", "heartbeat expired", "failure-ctrl")
	eventLog.Emit(ctx, "instance.created", "service/api", "new instance", "instance-ctrl")

	instanceEvents, _ := eventLog.Query(ctx, "instance.created", 10)
	if len(instanceEvents) != 2 {
		t.Fatalf("expected 2 instance.created events, got %d", len(instanceEvents))
	}

	nodeEvents, _ := eventLog.Query(ctx, "node.failed", 10)
	if len(nodeEvents) != 1 {
		t.Fatalf("expected 1 node.failed event, got %d", len(nodeEvents))
	}
}

func TestEventLogQueryAllKinds(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 100)

	eventLog.Emit(ctx, "instance.created", "web", "new", "ctrl")
	eventLog.Emit(ctx, "node.failed", "n1", "down", "ctrl")

	allEvents, _ := eventLog.Query(ctx, "", 10)
	if len(allEvents) != 2 {
		t.Fatalf("expected 2 events, got %d", len(allEvents))
	}
}

func TestEventLogQueryWithLimit(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 100)

	for i := 0; i < 5; i++ {
		eventLog.Emit(ctx, "test.event", "target", "detail", "source")
	}

	events, _ := eventLog.Query(ctx, "", 3)
	if len(events) != 3 {
		t.Fatalf("expected 3 events with limit, got %d", len(events))
	}
}

func TestEventLogSince(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 100)

	baseTime := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	callCount := 0
	eventLog.timeFunc = func() time.Time {
		callCount++
		return baseTime.Add(time.Duration(callCount) * time.Minute)
	}

	eventLog.Emit(ctx, "early.event", "target", "happened early", "ctrl")
	eventLog.Emit(ctx, "late.event", "target", "happened late", "ctrl")

	// Query events after the first event's time.
	cutoff := baseTime.Add(90 * time.Second)
	events, err := eventLog.Since(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event after cutoff, got %d", len(events))
	}
	if events[0].Kind != "late.event" {
		t.Errorf("kind = %q, want late.event", events[0].Kind)
	}
}

func TestEventLogForTarget(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 100)

	eventLog.Emit(ctx, "instance.created", "service/web", "new instance", "ctrl")
	eventLog.Emit(ctx, "instance.failed", "service/web", "health check failed", "ctrl")
	eventLog.Emit(ctx, "instance.created", "service/api", "new instance", "ctrl")

	webEvents, _ := eventLog.ForTarget(ctx, "service/web", 10)
	if len(webEvents) != 2 {
		t.Fatalf("expected 2 web events, got %d", len(webEvents))
	}
}

func TestEventLogTrimming(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 3)

	for i := 0; i < 5; i++ {
		eventLog.Emit(ctx, "test.event", "target", "detail", "source")
	}

	count, err := eventLog.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count > 3 {
		t.Fatalf("expected at most 3 events after trimming, got %d", count)
	}
}

func TestEventLogCount(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 100)

	count, _ := eventLog.Count(ctx)
	if count != 0 {
		t.Fatalf("expected 0 events initially, got %d", count)
	}

	eventLog.Emit(ctx, "test", "target", "detail", "source")
	eventLog.Emit(ctx, "test", "target", "detail", "source")

	count, _ = eventLog.Count(ctx)
	if count != 2 {
		t.Fatalf("expected 2 events, got %d", count)
	}
}

func TestEventLogUnlimitedRetention(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	eventLog := NewEventLog(memoryStore, 0)

	for i := 0; i < 10; i++ {
		eventLog.Emit(ctx, "test", "target", "detail", "source")
	}

	count, _ := eventLog.Count(ctx)
	if count != 10 {
		t.Fatalf("expected 10 events with unlimited retention, got %d", count)
	}
}
