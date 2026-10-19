// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package store

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// secretKeyPattern matches /secret/ and everything after it for redaction in log messages.
var secretKeyPattern = regexp.MustCompile(`/secret/.*`)

// ErrKeyNotFound is returned when a Get or Delete targets a key that does not exist in the store.
var ErrKeyNotFound = fmt.Errorf("key not found")

// ErrStoreClosed is returned when an operation is attempted on a store that has been closed.
var ErrStoreClosed = fmt.Errorf("store closed")

// ErrTransactionTooLarge is returned when a transaction exceeds the maximum
// number of operations allowed by the backend. etcd enforces a 128-operation
// limit; MemoryStore enforces the same limit so tests surface overflow bugs
// before production.
var ErrTransactionTooLarge = fmt.Errorf("transaction too large")

// memoryStoreTransactionOperationLimit mirrors etcd's default 128-operation
// transaction cap so that tests using MemoryStore surface the same overflow
// failures that would occur in production with etcd.
const memoryStoreTransactionOperationLimit = 128

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

// defaultEventHistoryCapacity is the number of events retained in the ring
// buffer for revision-based watch replay. Sized to cover brief gaps between
// Scan and Watch — not meant to hold the entire history.
const defaultEventHistoryCapacity = 4096

// eventHistoryEntry stores a single event with its store-global revision for
// replay by revision-based watches.
type eventHistoryEntry struct {
	// revision is the store-global revision when this event was recorded.
	revision int64
	// event is the watch event that was broadcast.
	event Event
}

// eventHistoryBuffer is a bounded ring buffer of recent watch events, enabling
// revision-based watch replay. When full, the oldest entries are overwritten.
type eventHistoryBuffer struct {
	// entries is the fixed-size ring buffer of event history entries.
	entries []eventHistoryEntry
	// writeIndex is the next position to write in the ring buffer.
	writeIndex int
	// entryCount is the number of valid entries (capped at len(entries)).
	entryCount int
}

// newEventHistoryBuffer creates a ring buffer with the given capacity.
func newEventHistoryBuffer(capacity int) *eventHistoryBuffer {
	return &eventHistoryBuffer{
		entries: make([]eventHistoryEntry, capacity),
	}
}

// append adds an event to the ring buffer, overwriting the oldest if full.
func (historyBuffer *eventHistoryBuffer) append(revision int64, event Event) {
	historyBuffer.entries[historyBuffer.writeIndex] = eventHistoryEntry{
		revision: revision,
		event:    event,
	}
	historyBuffer.writeIndex = (historyBuffer.writeIndex + 1) % len(historyBuffer.entries)
	if historyBuffer.entryCount < len(historyBuffer.entries) {
		historyBuffer.entryCount++
	}
}

// eventsFromRevision returns all events with revision >= startRevision in order.
// Returns nil, false if the requested revision has been evicted from the buffer.
func (historyBuffer *eventHistoryBuffer) eventsFromRevision(startRevision int64) ([]Event, bool) {
	if historyBuffer.entryCount == 0 {
		return nil, true
	}

	oldestIndex := 0
	if historyBuffer.entryCount == len(historyBuffer.entries) {
		oldestIndex = historyBuffer.writeIndex
	}
	oldestRevision := historyBuffer.entries[oldestIndex].revision

	if startRevision < oldestRevision {
		return nil, false
	}

	var matchingEvents []Event
	for iterationIndex := 0; iterationIndex < historyBuffer.entryCount; iterationIndex++ {
		bufferIndex := (oldestIndex + iterationIndex) % len(historyBuffer.entries)
		entry := historyBuffer.entries[bufferIndex]
		if entry.revision >= startRevision {
			matchingEvents = append(matchingEvents, entry.event)
		}
	}
	return matchingEvents, true
}

// MemoryStore is an in-memory implementation of StateStore, used for tests and
// local development. A single RWMutex protects all data operations — readers
// (Get, Scan) can proceed concurrently while writers (Put, Delete, Transaction)
// hold exclusive access. Event delivery and watcher management use a separate
// eventMutex so broadcasts never block data readers.
type MemoryStore struct {
	// dataMutex guards facts and keyIndex for all data operations.
	dataMutex sync.RWMutex
	// facts holds all stored key-value facts.
	facts map[string]*Fact
	// keyIndex is a trie over fact keys for prefix scans.
	keyIndex *prefixTrie
	// currentRevision is the store-global monotonically increasing revision counter.
	currentRevision atomic.Int64
	// eventMutex serializes event history appends, watcher delivery, and
	// watcher registration/removal to preserve causal event ordering.
	eventMutex sync.Mutex
	// activeWatchers is the list of currently registered watch subscriptions.
	activeWatchers []factWatcher
	// eventHistory is a bounded ring buffer of recent events for revision-based
	// watch replay, closing the gap between Scan and Watch.
	eventHistory *eventHistoryBuffer
	// watchChannelBufferSize is the per-watcher event channel capacity.
	watchChannelBufferSize int
	// isClosed tracks whether Close has been called, checked atomically.
	isClosed atomic.Bool
	// closeMutex prevents concurrent Close calls from double-closing channels.
	closeMutex sync.Mutex
}

// defaultWatchChannelBufferSize is the per-watcher event channel capacity.
const defaultWatchChannelBufferSize = 256

// NewMemoryStore creates and returns a new empty in-memory fact store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		facts:                  make(map[string]*Fact),
		keyIndex:               newPrefixTrie(),
		eventHistory:           newEventHistoryBuffer(defaultEventHistoryCapacity),
		watchChannelBufferSize: defaultWatchChannelBufferSize,
	}
}

// SetWatchChannelBufferSize overrides the per-watcher channel capacity.
// Must be called before any Watch calls.
func (memStore *MemoryStore) SetWatchChannelBufferSize(bufferSize int) {
	memStore.watchChannelBufferSize = bufferSize
}

// Get retrieves the fact stored at the given key. Returns a defensive copy of
// the fact. Returns ErrKeyNotFound if the key does not exist.
func (memStore *MemoryStore) Get(_ context.Context, key string) (*Fact, error) {
	if memStore.isClosed.Load() {
		return nil, ErrStoreClosed
	}
	memStore.dataMutex.RLock()
	defer memStore.dataMutex.RUnlock()

	existingFact, found := memStore.facts[key]
	if !found {
		return nil, ErrKeyNotFound
	}
	factCopy := *existingFact
	factCopy.Value = cloneBytes(existingFact.Value)
	return &factCopy, nil
}

// Put creates or updates the fact at the given key. Acquires the data lock
// for the write, then broadcasts the event outside the data lock.
func (memStore *MemoryStore) Put(_ context.Context, key string, value []byte) (int64, error) {
	if memStore.isClosed.Load() {
		return 0, ErrStoreClosed
	}
	memStore.dataMutex.Lock()

	if existingFact, found := memStore.facts[key]; found && bytes.Equal(existingFact.Value, value) {
		revision := existingFact.Revision
		memStore.dataMutex.Unlock()
		return revision, nil
	}

	newRevision := memStore.currentRevision.Add(1)
	event := memStore.applyPut(key, value, newRevision)
	memStore.dataMutex.Unlock()

	memStore.broadcastEvent(event)
	return newRevision, nil
}

// applyPut writes a fact at the specified revision and returns the event to
// broadcast. Must be called with dataMutex held for writing.
func (memStore *MemoryStore) applyPut(key string, value []byte, revision int64) Event {
	var previousFact *Fact
	if existingFact, found := memStore.facts[key]; found {
		previousFactCopy := *existingFact
		previousFact = &previousFactCopy
	}

	createRevision := revision
	if previousFact != nil {
		createRevision = previousFact.CreateRevision
	}

	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)

	newFact := &Fact{
		Key:            key,
		Value:          valueCopy,
		Revision:       revision,
		CreateRevision: createRevision,
	}
	memStore.facts[key] = newFact
	memStore.keyIndex.Insert(key)

	eventFact := *newFact
	eventFact.Value = cloneBytes(newFact.Value)
	var eventPrev *Fact
	if previousFact != nil {
		prevCopy := *previousFact
		prevCopy.Value = cloneBytes(previousFact.Value)
		eventPrev = &prevCopy
	}
	return Event{Type: EventPut, Fact: eventFact, Prev: eventPrev}
}

// Delete removes the fact at the given key from the store. Returns
// ErrKeyNotFound if the key does not exist.
func (memStore *MemoryStore) Delete(_ context.Context, key string) error {
	if memStore.isClosed.Load() {
		return ErrStoreClosed
	}
	memStore.dataMutex.Lock()

	existingFact, found := memStore.facts[key]
	if !found {
		memStore.dataMutex.Unlock()
		return ErrKeyNotFound
	}

	newRevision := memStore.currentRevision.Add(1)
	previousFact := *existingFact
	previousFact.Value = cloneBytes(existingFact.Value)
	delete(memStore.facts, key)
	memStore.keyIndex.Remove(key)

	deletedFact := Fact{Key: key, Revision: newRevision}
	event := Event{Type: EventDelete, Fact: deletedFact, Prev: &previousFact}
	memStore.dataMutex.Unlock()

	memStore.broadcastEvent(event)
	return nil
}

// Scan returns all facts whose keys begin with the given prefix, sorted
// alphabetically by key. The trie returns keys in sorted order so no
// post-sort is needed.
func (memStore *MemoryStore) Scan(_ context.Context, prefix string) ([]Fact, error) {
	if memStore.isClosed.Load() {
		return nil, ErrStoreClosed
	}
	factPointers := memStore.snapshotMatchingFacts(prefix)
	return copyFactPointers(factPointers), nil
}

// ScanWithRevision returns all facts matching the prefix along with the
// store-global revision, both under a consistent snapshot.
func (memStore *MemoryStore) ScanWithRevision(_ context.Context, prefix string) (*ScanResult, error) {
	if memStore.isClosed.Load() {
		return nil, ErrStoreClosed
	}
	memStore.dataMutex.RLock()
	snapshotRevision := memStore.currentRevision.Load()
	factPointers := memStore.collectFactPointers(prefix)
	memStore.dataMutex.RUnlock()

	return &ScanResult{
		Facts:    copyFactPointers(factPointers),
		Revision: snapshotRevision,
	}, nil
}

// snapshotMatchingFacts grabs pointers to matching facts under the read lock,
// then releases the lock immediately. Fact structs pointed to by the returned
// slice are immutable (Puts create new Fact structs, never modify in place),
// so copying can happen outside the lock without data races.
func (memStore *MemoryStore) snapshotMatchingFacts(prefix string) []*Fact {
	memStore.dataMutex.RLock()
	factPointers := memStore.collectFactPointers(prefix)
	memStore.dataMutex.RUnlock()
	return factPointers
}

// collectFactPointers returns pointers to all facts matching the prefix.
// Must be called with dataMutex read-locked. The returned pointers are safe
// to dereference after releasing the lock because Puts replace the map entry
// with a new Fact rather than modifying the existing one.
func (memStore *MemoryStore) collectFactPointers(prefix string) []*Fact {
	matchingKeys := memStore.keyIndex.KeysWithPrefix(prefix)
	factPointers := make([]*Fact, 0, len(matchingKeys))
	for _, factKey := range matchingKeys {
		factPointers = append(factPointers, memStore.facts[factKey])
	}
	return factPointers
}

// copyFactPointers produces defensive copies of the pointed-to facts. Called
// outside any lock — the source Fact structs are immutable once stored.
func copyFactPointers(factPointers []*Fact) []Fact {
	copiedFacts := make([]Fact, len(factPointers))
	for index, factPointer := range factPointers {
		copiedFacts[index] = *factPointer
		copiedFacts[index].Value = cloneBytes(factPointer.Value)
	}
	return copiedFacts
}

// Watch creates a new watch subscription for changes to the specified key (or
// key prefix if opts.Prefix is true). Returns a buffered channel that will
// receive events for matching changes. Uses the event mutex, not the data lock.
func (memStore *MemoryStore) Watch(ctx context.Context, key string, opts WatchOption) (<-chan Event, error) {
	if memStore.isClosed.Load() {
		return nil, ErrStoreClosed
	}
	memStore.eventMutex.Lock()
	defer memStore.eventMutex.Unlock()

	eventChannel := make(chan Event, memStore.watchChannelBufferSize)

	if opts.StartRevision > 0 {
		historicalEvents, historyAvailable := memStore.eventHistory.eventsFromRevision(opts.StartRevision)
		if !historyAvailable {
			go func() {
				eventChannel <- Event{Type: EventCompacted}
				close(eventChannel)
			}()
			return eventChannel, nil
		}
		for _, historicalEvent := range historicalEvents {
			if matchesWatchPattern(historicalEvent, key, opts.Prefix) {
				select {
				case eventChannel <- historicalEvent:
				default:
				}
			}
		}
	}

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

// matchesWatchPattern returns true if the event's key matches the watch pattern.
func matchesWatchPattern(event Event, keyPattern string, matchByPrefix bool) bool {
	if matchByPrefix {
		return strings.HasPrefix(event.Fact.Key, keyPattern)
	}
	return event.Fact.Key == keyPattern
}

// removeWatcher unregisters the watcher with the given channel and closes it.
func (memStore *MemoryStore) removeWatcher(eventChannel chan Event) {
	memStore.eventMutex.Lock()
	defer memStore.eventMutex.Unlock()

	for index, activeWatcher := range memStore.activeWatchers {
		if activeWatcher.eventChannel == eventChannel {
			memStore.activeWatchers = append(memStore.activeWatchers[:index], memStore.activeWatchers[index+1:]...)
			close(eventChannel)
			return
		}
	}
}

// Transaction atomically evaluates compare preconditions and executes the
// appropriate branch under a single data lock.
func (memStore *MemoryStore) Transaction(_ context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error) {
	if memStore.isClosed.Load() {
		return false, ErrStoreClosed
	}

	totalTransactionOperations := len(compares) + len(onSuccess) + len(onFailure)
	if totalTransactionOperations > memoryStoreTransactionOperationLimit {
		return false, fmt.Errorf("%w: %d operations exceeds limit of %d",
			ErrTransactionTooLarge, totalTransactionOperations, memoryStoreTransactionOperationLimit)
	}

	memStore.dataMutex.Lock()

	allPreconditionsMet := memStore.evaluateTransactionPreconditions(compares)

	operationsToExecute := onSuccess
	if !allPreconditionsMet {
		operationsToExecute = onFailure
	}

	hasMutation := memStore.transactionWouldMutateState(operationsToExecute)

	var transactionRevision int64
	if hasMutation {
		transactionRevision = memStore.currentRevision.Add(1)
	}

	pendingEvents := memStore.executeTransactionOperations(operationsToExecute, transactionRevision)

	memStore.dataMutex.Unlock()

	for _, event := range pendingEvents {
		memStore.broadcastEvent(event)
	}

	return allPreconditionsMet, nil
}

// evaluateTransactionPreconditions checks whether all compare preconditions
// are satisfied. Must be called with dataMutex locked.
func (memStore *MemoryStore) evaluateTransactionPreconditions(compares []Compare) bool {
	for _, comparison := range compares {
		existingFact, factExists := memStore.facts[comparison.Key]
		if comparison.Revision == 0 {
			if factExists {
				return false
			}
		} else {
			if !factExists || existingFact.Revision != comparison.Revision {
				return false
			}
		}
	}
	return true
}

// transactionWouldMutateState determines whether any operation would change
// the store state. Must be called with dataMutex locked.
func (memStore *MemoryStore) transactionWouldMutateState(operations []Op) bool {
	for _, operation := range operations {
		switch operation.Type {
		case OpPut:
			if existingFact, factExists := memStore.facts[operation.Key]; !factExists || !bytes.Equal(existingFact.Value, operation.Value) {
				return true
			}
		case OpDelete:
			if _, factExists := memStore.facts[operation.Key]; factExists {
				return true
			}
		}
	}
	return false
}

// executeTransactionOperations applies put and delete operations at a single
// revision, returning events to broadcast after the data lock is released.
// Must be called with dataMutex locked.
func (memStore *MemoryStore) executeTransactionOperations(operations []Op, transactionRevision int64) []Event {
	var pendingEvents []Event
	for _, operation := range operations {
		switch operation.Type {
		case OpPut:
			event, changed := memStore.executePutOperation(operation, transactionRevision)
			if changed {
				pendingEvents = append(pendingEvents, event)
			}
		case OpDelete:
			event, deleted := memStore.executeDeleteOperation(operation, transactionRevision)
			if deleted {
				pendingEvents = append(pendingEvents, event)
			}
		}
	}
	return pendingEvents
}

// executePutOperation applies a single put within a transaction. Returns the
// event and true if the fact was actually modified. Must be called with
// dataMutex locked.
func (memStore *MemoryStore) executePutOperation(operation Op, transactionRevision int64) (Event, bool) {
	if existingFact, factExists := memStore.facts[operation.Key]; factExists && bytes.Equal(existingFact.Value, operation.Value) {
		return Event{}, false
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
	memStore.keyIndex.Insert(operation.Key)

	eventFact := *newFact
	eventFact.Value = cloneBytes(newFact.Value)
	var eventPrev *Fact
	if previousFact != nil {
		prevCopy := *previousFact
		prevCopy.Value = cloneBytes(previousFact.Value)
		eventPrev = &prevCopy
	}
	return Event{Type: EventPut, Fact: eventFact, Prev: eventPrev}, true
}

// executeDeleteOperation applies a single delete within a transaction. Returns
// the event and true if the key existed and was removed.
func (memStore *MemoryStore) executeDeleteOperation(operation Op, transactionRevision int64) (Event, bool) {
	existingFact, factExists := memStore.facts[operation.Key]
	if !factExists {
		return Event{}, false
	}
	previousFact := *existingFact
	previousFact.Value = cloneBytes(existingFact.Value)
	delete(memStore.facts, operation.Key)
	memStore.keyIndex.Remove(operation.Key)
	deletedFact := Fact{Key: operation.Key, Revision: transactionRevision}
	return Event{Type: EventDelete, Fact: deletedFact, Prev: &previousFact}, true
}

// Revision returns the current store-global revision counter.
func (memStore *MemoryStore) Revision(_ context.Context) (int64, error) {
	if memStore.isClosed.Load() {
		return 0, ErrStoreClosed
	}
	return memStore.currentRevision.Load(), nil
}

// GrantLease returns a synthetic lease ID. MemoryStore does not enforce TTLs —
// keys written with PutWithLease behave identically to regular Put.
func (memStore *MemoryStore) GrantLease(_ context.Context, _ int64) (int64, error) {
	return memStore.currentRevision.Add(1), nil
}

// PutWithLease writes a key-value pair. The leaseID is stored but the memory
// store does not enforce TTL expiration — keys persist until explicitly deleted.
func (memStore *MemoryStore) PutWithLease(ctx context.Context, key string, value []byte, _ int64) (int64, error) {
	return memStore.Put(ctx, key, value)
}

// RenewLease is a no-op in MemoryStore. Returns a synthetic TTL of 900 seconds.
func (memStore *MemoryStore) RenewLease(_ context.Context, _ int64) (int64, error) {
	return 900, nil
}

// RevokeLease is a no-op in MemoryStore.
func (memStore *MemoryStore) RevokeLease(_ context.Context, _ int64) error {
	return nil
}

// Close shuts down the memory store by closing all active watcher event
// channels. Subsequent operations return ErrStoreClosed.
func (memStore *MemoryStore) Close() error {
	memStore.closeMutex.Lock()
	defer memStore.closeMutex.Unlock()

	if memStore.isClosed.Load() {
		return nil
	}
	memStore.isClosed.Store(true)

	memStore.eventMutex.Lock()
	for _, activeWatcher := range memStore.activeWatchers {
		close(activeWatcher.eventChannel)
	}
	memStore.activeWatchers = nil
	memStore.eventMutex.Unlock()

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

// broadcastEvent appends the event to the history ring buffer and delivers it
// to all matching watchers. Serialized by eventMutex to preserve causal order.
func (memStore *MemoryStore) broadcastEvent(event Event) {
	memStore.eventMutex.Lock()
	defer memStore.eventMutex.Unlock()

	memStore.eventHistory.append(event.Fact.Revision, event)

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
				sanitizedKey := strings.NewReplacer("\n", "\\n", "\r", "\\r").Replace(event.Fact.Key)
				sanitizedKey = secretKeyPattern.ReplaceAllString(sanitizedKey, "/secret/[REDACTED]")
				log.Printf("WARNING: watch event dropped for key %s (channel buffer full)", sanitizedKey)
			}
			activeWatcher.overflowDetected = true
		}
	}
}
