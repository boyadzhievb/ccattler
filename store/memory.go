package store

import (
	"bytes"
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
)

// ErrKeyNotFound is returned when a Get or Delete targets a key that does not exist in the store.
var ErrKeyNotFound = errors.New("key not found")

// ErrStoreClosed is returned when an operation is attempted on a store that has been closed.
var ErrStoreClosed = errors.New("store closed")

// factWatcher represents an active watch subscription on the store. Each watcher
// monitors either a single exact key or all keys sharing a common prefix and
// receives matching events on its buffered channel.
type factWatcher struct {
	// keyPattern is the key or key prefix this watcher is subscribed to.
	keyPattern string
	// matchByPrefix, when true, matches any key that starts with keyPattern
	// rather than requiring an exact match.
	matchByPrefix bool
	// eventChannel is the buffered channel on which matching events are delivered.
	eventChannel chan Event
	// overflowDetected is set when an event could not be delivered because
	// the channel buffer was full. The next successful send delivers an
	// EventOverflow marker before the real event.
	overflowDetected bool
}

// MemoryStore is an in-memory implementation of StateStore, used for tests and
// local development. It maintains a map of facts, a monotonically increasing
// revision counter, and a list of active watchers. All operations are protected
// by a read-write mutex for concurrent access.
type MemoryStore struct {
	// mutex guards all fields below for concurrent read-write access.
	mutex sync.RWMutex
	// facts holds all stored key-value facts, keyed by their path string.
	facts map[string]*Fact
	// currentRevision is the store-global monotonically increasing revision counter.
	currentRevision int64
	// activeWatchers is the list of currently registered watch subscriptions.
	activeWatchers []factWatcher
	// isClosed tracks whether Close has been called, preventing double-close.
	isClosed bool
}

// NewMemoryStore creates and returns a new empty in-memory fact store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		facts:           make(map[string]*Fact),
		currentRevision: 0,
	}
}

// Get retrieves the fact stored at the given key. Returns a defensive copy of the
// fact to ensure callers cannot mutate internal state. Returns ErrKeyNotFound if
// the key does not exist.
func (memStore *MemoryStore) Get(_ context.Context, key string) (*Fact, error) {
	memStore.mutex.RLock()
	defer memStore.mutex.RUnlock()

	if memStore.isClosed {
		return nil, ErrStoreClosed
	}

	existingFact, found := memStore.facts[key]
	if !found {
		return nil, ErrKeyNotFound
	}
	factCopy := *existingFact
	factCopy.Value = cloneBytes(existingFact.Value)
	return &factCopy, nil
}

// Put creates or updates the fact at the given key. The value is defensively copied
// to prevent external mutation of the stored data. Returns the new store-global
// revision. Any watchers matching this key are notified with an EventPut event.
func (memStore *MemoryStore) Put(_ context.Context, key string, value []byte) (int64, error) {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

	if memStore.isClosed {
		return 0, ErrStoreClosed
	}

	if existingFact, found := memStore.facts[key]; found && bytes.Equal(existingFact.Value, value) {
		return existingFact.Revision, nil
	}

	memStore.currentRevision++
	newRevision := memStore.currentRevision

	var previousFact *Fact
	if existingFact, found := memStore.facts[key]; found {
		previousFactCopy := *existingFact
		previousFact = &previousFactCopy
	}

	createRevision := newRevision
	if previousFact != nil {
		createRevision = previousFact.CreateRevision
	}

	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)

	newFact := &Fact{
		Key:            key,
		Value:          valueCopy,
		Revision:       newRevision,
		CreateRevision: createRevision,
	}
	memStore.facts[key] = newFact

	eventFact := *newFact
	eventFact.Value = cloneBytes(newFact.Value)
	var eventPrev *Fact
	if previousFact != nil {
		prevCopy := *previousFact
		prevCopy.Value = cloneBytes(previousFact.Value)
		eventPrev = &prevCopy
	}
	memStore.broadcastEventToWatchers(Event{Type: EventPut, Fact: eventFact, Prev: eventPrev})

	return newRevision, nil
}

// Delete removes the fact at the given key from the store. Returns ErrKeyNotFound
// if the key does not exist. Any watchers matching this key are notified with an
// EventDelete event that includes the previous fact value.
func (memStore *MemoryStore) Delete(_ context.Context, key string) error {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

	if memStore.isClosed {
		return ErrStoreClosed
	}

	existingFact, found := memStore.facts[key]
	if !found {
		return ErrKeyNotFound
	}

	memStore.currentRevision++
	previousFact := *existingFact
	previousFact.Value = cloneBytes(existingFact.Value)
	delete(memStore.facts, key)

	deletedFact := Fact{
		Key:      key,
		Revision: memStore.currentRevision,
	}
	memStore.broadcastEventToWatchers(Event{Type: EventDelete, Fact: deletedFact, Prev: &previousFact})

	return nil
}

// Scan returns all facts whose keys begin with the given prefix, sorted
// alphabetically by key. Each returned fact is a value copy to prevent external
// mutation of internal state.
func (memStore *MemoryStore) Scan(_ context.Context, prefix string) ([]Fact, error) {
	memStore.mutex.RLock()
	defer memStore.mutex.RUnlock()

	if memStore.isClosed {
		return nil, ErrStoreClosed
	}

	var matchingFacts []Fact
	for factKey, factEntry := range memStore.facts {
		if strings.HasPrefix(factKey, prefix) {
			factCopy := *factEntry
			factCopy.Value = cloneBytes(factEntry.Value)
			matchingFacts = append(matchingFacts, factCopy)
		}
	}
	sort.Slice(matchingFacts, func(i, j int) bool {
		return matchingFacts[i].Key < matchingFacts[j].Key
	})
	return matchingFacts, nil
}

// ScanWithRevision returns all facts matching the prefix along with the
// store-global revision at the time of the scan, both read under the same
// lock to guarantee consistency.
func (memStore *MemoryStore) ScanWithRevision(_ context.Context, prefix string) (*ScanResult, error) {
	memStore.mutex.RLock()
	defer memStore.mutex.RUnlock()

	if memStore.isClosed {
		return nil, ErrStoreClosed
	}

	var matchingFacts []Fact
	for factKey, factEntry := range memStore.facts {
		if strings.HasPrefix(factKey, prefix) {
			factCopy := *factEntry
			factCopy.Value = cloneBytes(factEntry.Value)
			matchingFacts = append(matchingFacts, factCopy)
		}
	}
	sort.Slice(matchingFacts, func(i, j int) bool {
		return matchingFacts[i].Key < matchingFacts[j].Key
	})
	return &ScanResult{
		Facts:    matchingFacts,
		Revision: memStore.currentRevision,
	}, nil
}

// Watch creates a new watch subscription for changes to the specified key (or key
// prefix if opts.Prefix is true). Returns a buffered channel that will receive events
// for matching changes. The channel is closed when the store is closed via Close.
func (memStore *MemoryStore) Watch(ctx context.Context, key string, opts WatchOption) (<-chan Event, error) {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

	if memStore.isClosed {
		return nil, ErrStoreClosed
	}

	eventChannel := make(chan Event, 64)
	memStore.activeWatchers = append(memStore.activeWatchers, factWatcher{
		keyPattern:    key,
		matchByPrefix: opts.Prefix,
		eventChannel:  eventChannel,
	})

	if ctx != nil && ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			memStore.removeWatcher(eventChannel)
		}()
	}

	return eventChannel, nil
}

// removeWatcher unregisters the watcher with the given channel and closes it.
func (memStore *MemoryStore) removeWatcher(eventChannel chan Event) {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

	for index, activeWatcher := range memStore.activeWatchers {
		if activeWatcher.eventChannel == eventChannel {
			memStore.activeWatchers = append(memStore.activeWatchers[:index], memStore.activeWatchers[index+1:]...)
			close(eventChannel)
			return
		}
	}
}

// Transaction atomically evaluates all compare preconditions against the current
// store state. If every precondition passes, the onSuccess operations are executed;
// otherwise the onFailure operations are executed. A compare with Revision == 0
// asserts the key must not exist; any other revision value asserts the key must
// exist at exactly that revision. Returns true if all preconditions were satisfied.
func (memStore *MemoryStore) Transaction(_ context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error) {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

	if memStore.isClosed {
		return false, ErrStoreClosed
	}

	allPreconditionsMet := true
	for _, comparison := range compares {
		existingFact, factExists := memStore.facts[comparison.Key]
		if comparison.Revision == 0 {
			if factExists {
				allPreconditionsMet = false
				break
			}
		} else {
			if !factExists || existingFact.Revision != comparison.Revision {
				allPreconditionsMet = false
				break
			}
		}
	}

	operationsToExecute := onSuccess
	if !allPreconditionsMet {
		operationsToExecute = onFailure
	}

	// Determine if any operation will actually mutate state.
	hasMutation := false
	for _, operation := range operationsToExecute {
		switch operation.Type {
		case OpPut:
			if existingFact, factExists := memStore.facts[operation.Key]; !factExists || !bytes.Equal(existingFact.Value, operation.Value) {
				hasMutation = true
			}
		case OpDelete:
			if _, factExists := memStore.facts[operation.Key]; factExists {
				hasMutation = true
			}
		}
		if hasMutation {
			break
		}
	}

	// All operations in a transaction share a single revision.
	var transactionRevision int64
	if hasMutation {
		memStore.currentRevision++
		transactionRevision = memStore.currentRevision
	}

	for _, operation := range operationsToExecute {
		switch operation.Type {
		case OpPut:
			if existingFact, factExists := memStore.facts[operation.Key]; factExists && bytes.Equal(existingFact.Value, operation.Value) {
				continue
			}

			var previousFact *Fact
			if existingFact, factExists := memStore.facts[operation.Key]; factExists {
				previousFactCopy := *existingFact
				previousFact = &previousFactCopy
			}

			createRevision := transactionRevision
			if previousFact != nil {
				createRevision = previousFact.CreateRevision
			}

			valueCopy := make([]byte, len(operation.Value))
			copy(valueCopy, operation.Value)

			newFact := &Fact{
				Key:            operation.Key,
				Value:          valueCopy,
				Revision:       transactionRevision,
				CreateRevision: createRevision,
			}
			memStore.facts[operation.Key] = newFact

			eventFact := *newFact
			eventFact.Value = cloneBytes(newFact.Value)
			var eventPrev *Fact
			if previousFact != nil {
				prevCopy := *previousFact
				prevCopy.Value = cloneBytes(previousFact.Value)
				eventPrev = &prevCopy
			}
			memStore.broadcastEventToWatchers(Event{Type: EventPut, Fact: eventFact, Prev: eventPrev})

		case OpDelete:
			if existingFact, factExists := memStore.facts[operation.Key]; factExists {
				previousFact := *existingFact
				previousFact.Value = cloneBytes(existingFact.Value)
				delete(memStore.facts, operation.Key)
				deletedFact := Fact{Key: operation.Key, Revision: transactionRevision}
				memStore.broadcastEventToWatchers(Event{Type: EventDelete, Fact: deletedFact, Prev: &previousFact})
			}
		}
	}

	return allPreconditionsMet, nil
}

// Revision returns the current store-global revision counter, which increases
// monotonically with each Put or Delete operation.
func (memStore *MemoryStore) Revision(_ context.Context) (int64, error) {
	memStore.mutex.RLock()
	defer memStore.mutex.RUnlock()

	if memStore.isClosed {
		return 0, ErrStoreClosed
	}

	return memStore.currentRevision, nil
}

// Close shuts down the memory store by closing all active watcher event channels
// and clearing the watcher list. Subsequent calls to Close are safe no-ops.
func (memStore *MemoryStore) Close() error {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

	if memStore.isClosed {
		return nil
	}
	memStore.isClosed = true
	for _, activeWatcher := range memStore.activeWatchers {
		close(activeWatcher.eventChannel)
	}
	memStore.activeWatchers = nil
	return nil
}

// cloneBytes returns a deep copy of the given byte slice. Returns nil for nil input.
func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	copied := make([]byte, len(value))
	copy(copied, value)
	return copied
}

// broadcastEventToWatchers sends the given event to all watchers whose key pattern
// matches the event's fact key. Prefix watchers match if the fact key starts with
// their pattern; exact watchers match only on key equality. When a watcher's
// channel buffer is full, the event is dropped and an overflow flag is set.
// On the next successful delivery, an EventOverflow marker is sent first so
// the consumer knows events were missed. Must be called with mutex held.
func (memStore *MemoryStore) broadcastEventToWatchers(event Event) {
	for watcherIndex := range memStore.activeWatchers {
		activeWatcher := &memStore.activeWatchers[watcherIndex]
		if activeWatcher.matchByPrefix {
			if !strings.HasPrefix(event.Fact.Key, activeWatcher.keyPattern) {
				continue
			}
		} else {
			if event.Fact.Key != activeWatcher.keyPattern {
				continue
			}
		}

		if activeWatcher.overflowDetected {
			select {
			case activeWatcher.eventChannel <- Event{Type: EventOverflow}:
				activeWatcher.overflowDetected = false
			default:
			}
		}

		select {
		case activeWatcher.eventChannel <- event:
		default:
			if !activeWatcher.overflowDetected {
				log.Printf("WARNING: watch event dropped for key %s (channel buffer full)", event.Fact.Key)
			}
			activeWatcher.overflowDetected = true
		}
	}
}
