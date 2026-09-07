package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// ErrKeyNotFound is returned when a Get or Delete targets a key that does not exist in the store.
var ErrKeyNotFound = errors.New("key not found")

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

	existingFact, found := memStore.facts[key]
	if !found {
		return nil, ErrKeyNotFound
	}
	factCopy := *existingFact
	return &factCopy, nil
}

// Put creates or updates the fact at the given key. The value is defensively copied
// to prevent external mutation of the stored data. Returns the new store-global
// revision. Any watchers matching this key are notified with an EventPut event.
func (memStore *MemoryStore) Put(_ context.Context, key string, value []byte) (int64, error) {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

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

	factCopy := *newFact
	memStore.broadcastEventToWatchers(Event{Type: EventPut, Fact: factCopy, Prev: previousFact})

	return newRevision, nil
}

// Delete removes the fact at the given key from the store. Returns ErrKeyNotFound
// if the key does not exist. Any watchers matching this key are notified with an
// EventDelete event that includes the previous fact value.
func (memStore *MemoryStore) Delete(_ context.Context, key string) error {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

	existingFact, found := memStore.facts[key]
	if !found {
		return ErrKeyNotFound
	}

	memStore.currentRevision++
	previousFact := *existingFact
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

	var matchingFacts []Fact
	for factKey, factEntry := range memStore.facts {
		if strings.HasPrefix(factKey, prefix) {
			matchingFacts = append(matchingFacts, *factEntry)
		}
	}
	sort.Slice(matchingFacts, func(i, j int) bool {
		return matchingFacts[i].Key < matchingFacts[j].Key
	})
	return matchingFacts, nil
}

// Watch creates a new watch subscription for changes to the specified key (or key
// prefix if opts.Prefix is true). Returns a buffered channel that will receive events
// for matching changes. The channel is closed when the store is closed via Close.
func (memStore *MemoryStore) Watch(_ context.Context, key string, opts WatchOption) (<-chan Event, error) {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

	eventChannel := make(chan Event, 64)
	memStore.activeWatchers = append(memStore.activeWatchers, factWatcher{
		keyPattern:    key,
		matchByPrefix: opts.Prefix,
		eventChannel:  eventChannel,
	})
	return eventChannel, nil
}

// Transaction atomically evaluates all compare preconditions against the current
// store state. If every precondition passes, the onSuccess operations are executed;
// otherwise the onFailure operations are executed. A compare with Revision == 0
// asserts the key must not exist; any other revision value asserts the key must
// exist at exactly that revision. Returns true if all preconditions were satisfied.
func (memStore *MemoryStore) Transaction(_ context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error) {
	memStore.mutex.Lock()
	defer memStore.mutex.Unlock()

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

	for _, operation := range operationsToExecute {
		switch operation.Type {
		case OpPut:
			memStore.currentRevision++
			newRevision := memStore.currentRevision

			var previousFact *Fact
			if existingFact, factExists := memStore.facts[operation.Key]; factExists {
				previousFactCopy := *existingFact
				previousFact = &previousFactCopy
			}

			createRevision := newRevision
			if previousFact != nil {
				createRevision = previousFact.CreateRevision
			}

			valueCopy := make([]byte, len(operation.Value))
			copy(valueCopy, operation.Value)

			newFact := &Fact{
				Key:            operation.Key,
				Value:          valueCopy,
				Revision:       newRevision,
				CreateRevision: createRevision,
			}
			memStore.facts[operation.Key] = newFact

			factCopy := *newFact
			memStore.broadcastEventToWatchers(Event{Type: EventPut, Fact: factCopy, Prev: previousFact})

		case OpDelete:
			if existingFact, factExists := memStore.facts[operation.Key]; factExists {
				memStore.currentRevision++
				previousFact := *existingFact
				delete(memStore.facts, operation.Key)
				deletedFact := Fact{Key: operation.Key, Revision: memStore.currentRevision}
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

// broadcastEventToWatchers sends the given event to all watchers whose key pattern
// matches the event's fact key. Prefix watchers match if the fact key starts with
// their pattern; exact watchers match only on key equality. Events are sent
// non-blocking -- if a watcher's channel buffer is full, the event is silently
// dropped for that watcher. Must be called with mutex held.
func (memStore *MemoryStore) broadcastEventToWatchers(event Event) {
	for _, activeWatcher := range memStore.activeWatchers {
		if activeWatcher.matchByPrefix {
			if !strings.HasPrefix(event.Fact.Key, activeWatcher.keyPattern) {
				continue
			}
		} else {
			if event.Fact.Key != activeWatcher.keyPattern {
				continue
			}
		}
		select {
		case activeWatcher.eventChannel <- event:
		default:
		}
	}
}
