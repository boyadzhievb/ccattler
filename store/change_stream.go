package store

import (
	"context"
	"sync"
	"time"
)

// ChangeRecord represents a single change in the CDC stream, enriched with
// metadata beyond what the raw watch event provides.
type ChangeRecord struct {
	Key       string
	Value     []byte
	PrevValue []byte
	Revision  int64
	Type      EventType
	Timestamp time.Time
}

// ChangeStreamSubscriber is a callback that processes change records.
// Return an error to signal that processing failed (the record may be retried
// or sent to a DLQ depending on the caller's policy).
type ChangeStreamSubscriber func(record ChangeRecord) error

// ChangeStream provides a CDC (Change Data Capture) interface over the fact
// store's watch mechanism. Multiple subscribers can register to receive all
// changes matching a prefix. Subscribers are called synchronously per change;
// slow subscribers should offload work to a channel.
type ChangeStream struct {
	factStore   StateStore
	prefix      string
	subscribers []ChangeStreamSubscriber
	mutex       sync.RWMutex
	timeNow     func() time.Time
}

// NewChangeStream creates a CDC stream that watches all changes under the
// given prefix. Call Start() to begin consuming.
func NewChangeStream(factStore StateStore, prefix string) *ChangeStream {
	return &ChangeStream{
		factStore: factStore,
		prefix:    prefix,
		timeNow:   time.Now,
	}
}

// Subscribe adds a subscriber that will receive all future change records.
// Must be called before Start().
func (changeStream *ChangeStream) Subscribe(subscriber ChangeStreamSubscriber) {
	changeStream.mutex.Lock()
	defer changeStream.mutex.Unlock()
	changeStream.subscribers = append(changeStream.subscribers, subscriber)
}

// Start begins watching the store and dispatching changes to all subscribers.
// Blocks until the context is cancelled. Returns any watch setup error.
func (changeStream *ChangeStream) Start(ctx context.Context) error {
	eventChannel, watchError := changeStream.factStore.Watch(ctx, changeStream.prefix, WatchOption{Prefix: true})
	if watchError != nil {
		return watchError
	}

	for {
		select {
		case event, channelOpen := <-eventChannel:
			if !channelOpen {
				return nil
			}
			if event.Type == EventOverflow {
				continue
			}
			record := changeStream.eventToRecord(event)
			changeStream.dispatchToSubscribers(record)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (changeStream *ChangeStream) eventToRecord(event Event) ChangeRecord {
	record := ChangeRecord{
		Key:       event.Fact.Key,
		Value:     event.Fact.Value,
		Revision:  event.Fact.Revision,
		Type:      event.Type,
		Timestamp: changeStream.timeNow(),
	}
	if event.Prev != nil {
		record.PrevValue = event.Prev.Value
	}
	return record
}

func (changeStream *ChangeStream) dispatchToSubscribers(record ChangeRecord) {
	changeStream.mutex.RLock()
	defer changeStream.mutex.RUnlock()

	for _, subscriber := range changeStream.subscribers {
		subscriber(record)
	}
}
