package types

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

func TestDeadLetterQueueEnqueueAndList(t *testing.T) {
	factStore := store.NewMemoryStore()
	deadLetterQueue := NewDeadLetterQueue(factStore, 24*time.Hour)

	ctx := context.Background()
	enqueueError := deadLetterQueue.Enqueue(ctx, "events/12345", `{"kind":"scale"}`, "webhook delivery failed", "event-projector")
	if enqueueError != nil {
		t.Fatal(enqueueError)
	}

	entries, listError := deadLetterQueue.List(ctx)
	if listError != nil {
		t.Fatal(listError)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.OriginalKey != "events/12345" {
		t.Errorf("original key: got %s, want events/12345", entry.OriginalKey)
	}
	if entry.FailureReason != "webhook delivery failed" {
		t.Errorf("failure reason: got %s", entry.FailureReason)
	}
	if entry.Attempts != 1 {
		t.Errorf("attempts: got %d, want 1", entry.Attempts)
	}
}

func TestDeadLetterQueueIncrementRetries(t *testing.T) {
	factStore := store.NewMemoryStore()
	deadLetterQueue := NewDeadLetterQueue(factStore, 24*time.Hour)

	ctx := context.Background()
	deadLetterQueue.Enqueue(ctx, "events/12345", `{"kind":"scale"}`, "first failure", "projector")
	deadLetterQueue.Enqueue(ctx, "events/12345", `{"kind":"scale"}`, "second failure", "projector")
	deadLetterQueue.Enqueue(ctx, "events/12345", `{"kind":"scale"}`, "third failure", "projector")

	entries, _ := deadLetterQueue.List(ctx)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (deduplicated), got %d", len(entries))
	}

	if entries[0].Attempts != 3 {
		t.Errorf("attempts: got %d, want 3", entries[0].Attempts)
	}
	if entries[0].FailureReason != "third failure" {
		t.Errorf("failure reason should be latest: got %s", entries[0].FailureReason)
	}
}

func TestDeadLetterQueueRemove(t *testing.T) {
	factStore := store.NewMemoryStore()
	deadLetterQueue := NewDeadLetterQueue(factStore, 24*time.Hour)

	ctx := context.Background()
	deadLetterQueue.Enqueue(ctx, "events/12345", `{"kind":"scale"}`, "failed", "projector")
	deadLetterQueue.Remove(ctx, "events/12345")

	entries, _ := deadLetterQueue.List(ctx)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after remove, got %d", len(entries))
	}
}

func TestDeadLetterQueueCleanupOld(t *testing.T) {
	currentTime := time.Now()
	factStore := store.NewMemoryStore()
	deadLetterQueue := NewDeadLetterQueue(factStore, 1*time.Hour)
	deadLetterQueue.timeNow = func() time.Time { return currentTime }

	ctx := context.Background()
	deadLetterQueue.Enqueue(ctx, "events/old", `data`, "failed", "projector")

	currentTime = currentTime.Add(2 * time.Hour)
	deadLetterQueue.Enqueue(ctx, "events/recent", `data`, "failed", "projector")

	removed, cleanupError := deadLetterQueue.CleanupOld(ctx)
	if cleanupError != nil {
		t.Fatal(cleanupError)
	}

	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	entries, _ := deadLetterQueue.List(ctx)
	if len(entries) != 1 {
		t.Fatalf("expected 1 remaining entry, got %d", len(entries))
	}
	if entries[0].OriginalKey != "events/recent" {
		t.Errorf("expected recent entry to remain, got %s", entries[0].OriginalKey)
	}
}

func TestDeadLetterQueueMultipleEntries(t *testing.T) {
	factStore := store.NewMemoryStore()
	deadLetterQueue := NewDeadLetterQueue(factStore, 24*time.Hour)

	ctx := context.Background()
	deadLetterQueue.Enqueue(ctx, "events/aaa", `data-a`, "failure-a", "source-a")
	deadLetterQueue.Enqueue(ctx, "events/bbb", `data-b`, "failure-b", "source-b")

	entries, _ := deadLetterQueue.List(ctx)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}
