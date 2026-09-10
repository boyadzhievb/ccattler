package types

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// SystemEvent records a single occurrence in the cluster's lifecycle. Events
// are append-only, immutable records stored in the fact store under the
// /ccattler/event/ prefix. They provide an audit trail of what happened, when,
// and why — complementing the authoritative state in the fact store.
type SystemEvent struct {
	// ID uniquely identifies this event (timestamp-based for ordering).
	ID string `json:"id"`
	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"timestamp"`
	// Kind categorizes the event (e.g. "instance.created", "node.failed").
	Kind string `json:"kind"`
	// Target identifies the affected resource (e.g. "service/web", "node/n1").
	Target string `json:"target"`
	// Detail provides a human-readable description of what happened.
	Detail string `json:"detail"`
	// Source identifies the controller or component that generated the event.
	Source string `json:"source"`
}

// EventLog provides an append-only event stream backed by the fact store.
// Events are stored as JSON under /ccattler/event/{id} and are never modified
// after creation. The log trims old events when it exceeds the configured
// maximum.
type EventLog struct {
	// factStore is the backing store for event persistence.
	factStore store.StateStore
	// maxEvents limits the number of events retained. Zero means unlimited.
	maxEvents int
	// TimeFunc returns the current time. Replaceable in tests.
	TimeFunc func() time.Time
	// sequence ensures unique event IDs even when timestamps collide.
	sequence atomic.Uint64
}

// NewEventLog creates an event log backed by the given store. Events beyond
// maxEvents are trimmed on each Emit call. Pass 0 for unlimited retention.
func NewEventLog(factStore store.StateStore, maxEvents int) *EventLog {
	return &EventLog{
		factStore: factStore,
		maxEvents: maxEvents,
		TimeFunc:  time.Now,
	}
}

// Emit records a new event to the log. The event ID and timestamp are set
// automatically. If the log exceeds maxEvents, the oldest events are trimmed.
func (eventLog *EventLog) Emit(ctx context.Context, kind, target, detail, source string) (*SystemEvent, error) {
	now := eventLog.TimeFunc()
	sequenceNumber := eventLog.sequence.Add(1)
	eventID := fmt.Sprintf("%d-%04d-%s", now.UnixNano(), sequenceNumber, kind)

	event := SystemEvent{
		ID:        eventID,
		Timestamp: now,
		Kind:      kind,
		Target:    target,
		Detail:    detail,
		Source:    source,
	}

	eventJSON, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("marshal event: %w", err)
	}

	eventKey := fmt.Sprintf("%s/%s", PrefixEvent, eventID)
	if _, err := eventLog.factStore.Put(ctx, eventKey, eventJSON); err != nil {
		return nil, fmt.Errorf("store event: %w", err)
	}

	if eventLog.maxEvents > 0 {
		eventLog.trimOldEvents(ctx)
	}

	return &event, nil
}

// Query returns events matching the given kind, up to the specified limit.
// An empty kind returns events of all kinds. Results are ordered by key
// (chronologically by timestamp-based ID).
func (eventLog *EventLog) Query(ctx context.Context, kind string, limit int) ([]SystemEvent, error) {
	allFacts, err := eventLog.factStore.Scan(ctx, PrefixEvent+"/")
	if err != nil {
		return nil, fmt.Errorf("scan events: %w", err)
	}

	events := make([]SystemEvent, 0, len(allFacts))
	for _, fact := range allFacts {
		var event SystemEvent
		if err := json.Unmarshal(fact.Value, &event); err != nil {
			continue
		}
		if kind != "" && event.Kind != kind {
			continue
		}
		events = append(events, event)
	}

	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}

	return events, nil
}

// Since returns events that occurred after the given timestamp, up to the
// specified limit. Results are ordered chronologically.
func (eventLog *EventLog) Since(ctx context.Context, after time.Time, limit int) ([]SystemEvent, error) {
	allFacts, err := eventLog.factStore.Scan(ctx, PrefixEvent+"/")
	if err != nil {
		return nil, fmt.Errorf("scan events: %w", err)
	}

	events := make([]SystemEvent, 0)
	for _, fact := range allFacts {
		var event SystemEvent
		if err := json.Unmarshal(fact.Value, &event); err != nil {
			continue
		}
		if event.Timestamp.After(after) {
			events = append(events, event)
		}
	}

	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}

	return events, nil
}

// ForTarget returns all events affecting the specified target.
func (eventLog *EventLog) ForTarget(ctx context.Context, target string, limit int) ([]SystemEvent, error) {
	allFacts, err := eventLog.factStore.Scan(ctx, PrefixEvent+"/")
	if err != nil {
		return nil, fmt.Errorf("scan events: %w", err)
	}

	events := make([]SystemEvent, 0)
	for _, fact := range allFacts {
		var event SystemEvent
		if err := json.Unmarshal(fact.Value, &event); err != nil {
			continue
		}
		if strings.Contains(event.Target, target) {
			events = append(events, event)
		}
	}

	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}

	return events, nil
}

// Count returns the total number of events in the log.
func (eventLog *EventLog) Count(ctx context.Context) (int, error) {
	allFacts, err := eventLog.factStore.Scan(ctx, PrefixEvent+"/")
	if err != nil {
		return 0, err
	}
	return len(allFacts), nil
}

// trimOldEvents removes the oldest events when the log exceeds maxEvents.
func (eventLog *EventLog) trimOldEvents(ctx context.Context) {
	allFacts, err := eventLog.factStore.Scan(ctx, PrefixEvent+"/")
	if err != nil {
		return
	}

	excess := len(allFacts) - eventLog.maxEvents
	if excess <= 0 {
		return
	}

	for i := 0; i < excess; i++ {
		eventLog.factStore.Delete(ctx, allFacts[i].Key)
	}
}
