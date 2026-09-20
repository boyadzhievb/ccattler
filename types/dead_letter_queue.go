package types

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// DeadLetterEntry represents a failed event that has been moved to the DLQ.
type DeadLetterEntry struct {
	OriginalKey   string    `json:"original_key"`
	OriginalValue string    `json:"original_value"`
	FailureReason string    `json:"failure_reason"`
	Source        string    `json:"source"`
	Attempts      int       `json:"attempts"`
	FirstFailure  time.Time `json:"first_failure"`
	LastFailure   time.Time `json:"last_failure"`
}

const deadLetterPrefix = "dlq/"

// DeadLetterQueue stores events that failed processing for later inspection
// and replay. Entries are stored as facts under the dlq/ prefix.
type DeadLetterQueue struct {
	factStore store.StateStore
	maxAge    time.Duration
	timeNow   func() time.Time
}

// NewDeadLetterQueue creates a DLQ backed by the given store. Entries older
// than maxAge are eligible for cleanup.
func NewDeadLetterQueue(factStore store.StateStore, maxAge time.Duration) *DeadLetterQueue {
	return &DeadLetterQueue{
		factStore: factStore,
		maxAge:    maxAge,
		timeNow:   time.Now,
	}
}

// Enqueue adds a failed event to the DLQ. If the same original key already
// has a DLQ entry, it increments the attempt counter instead of creating
// a duplicate.
func (deadLetterQueue *DeadLetterQueue) Enqueue(ctx context.Context, originalKey string, originalValue string, failureReason string, source string) error {
	dlqKey := deadLetterPrefix + sanitizeDLQKey(originalKey)
	currentTime := deadLetterQueue.timeNow()

	existingFact, getError := deadLetterQueue.factStore.Get(ctx, dlqKey)
	if getError == nil && existingFact != nil {
		var existingEntry DeadLetterEntry
		if json.Unmarshal(existingFact.Value, &existingEntry) == nil {
			existingEntry.Attempts++
			existingEntry.LastFailure = currentTime
			existingEntry.FailureReason = failureReason
			encoded, _ := json.Marshal(existingEntry)
			_, putError := deadLetterQueue.factStore.Put(ctx, dlqKey, encoded)
			return putError
		}
	}

	entry := DeadLetterEntry{
		OriginalKey:   originalKey,
		OriginalValue: originalValue,
		FailureReason: failureReason,
		Source:        source,
		Attempts:      1,
		FirstFailure:  currentTime,
		LastFailure:   currentTime,
	}

	encoded, marshalError := json.Marshal(entry)
	if marshalError != nil {
		return fmt.Errorf("marshal DLQ entry: %w", marshalError)
	}

	_, putError := deadLetterQueue.factStore.Put(ctx, dlqKey, encoded)
	return putError
}

// List returns all entries currently in the DLQ.
func (deadLetterQueue *DeadLetterQueue) List(ctx context.Context) ([]DeadLetterEntry, error) {
	facts, scanError := deadLetterQueue.factStore.Scan(ctx, deadLetterPrefix)
	if scanError != nil {
		return nil, fmt.Errorf("scan DLQ: %w", scanError)
	}

	entries := make([]DeadLetterEntry, 0, len(facts))
	for _, fact := range facts {
		var entry DeadLetterEntry
		if json.Unmarshal(fact.Value, &entry) == nil {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// Remove deletes a specific entry from the DLQ by its original key.
func (deadLetterQueue *DeadLetterQueue) Remove(ctx context.Context, originalKey string) error {
	dlqKey := deadLetterPrefix + sanitizeDLQKey(originalKey)
	return deadLetterQueue.factStore.Delete(ctx, dlqKey)
}

// CleanupOld removes DLQ entries older than maxAge.
func (deadLetterQueue *DeadLetterQueue) CleanupOld(ctx context.Context) (int, error) {
	facts, scanError := deadLetterQueue.factStore.Scan(ctx, deadLetterPrefix)
	if scanError != nil {
		return 0, scanError
	}

	removedCount := 0
	currentTime := deadLetterQueue.timeNow()
	for _, fact := range facts {
		var entry DeadLetterEntry
		if json.Unmarshal(fact.Value, &entry) != nil {
			continue
		}
		if currentTime.Sub(entry.LastFailure) > deadLetterQueue.maxAge {
			deadLetterQueue.factStore.Delete(ctx, fact.Key)
			removedCount++
		}
	}
	return removedCount, nil
}

func sanitizeDLQKey(originalKey string) string {
	return strings.ReplaceAll(originalKey, "/", "_")
}
