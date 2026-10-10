// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package store

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
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

// memoryStoreShardCount is the number of data shards. Must be a power of 2.
// With 64 shards, 200 concurrent agents contend ~3 per shard instead of 200.
const memoryStoreShardCount = 64

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

// memoryStoreShard holds a partition of the fact store data, protected by its
// own read-write mutex. Sharding allows concurrent writes to different key
// ranges without contention on a single global lock.
type memoryStoreShard struct {
	// mutex guards facts and keyIndex in this shard.
	mutex sync.RWMutex
	// facts holds stored key-value facts assigned to this shard by key hash.
	facts map[string]*Fact
	// keyIndex is a trie over this shard's fact keys for prefix scans.
	keyIndex *prefixTrie
}

// MemoryStore is an in-memory implementation of StateStore, used for tests and
// local development. Data is partitioned across 64 shards by key hash, allowing
// concurrent reads and writes to different keys without contention. Event
// delivery and watcher management are protected by a separate event mutex.
type MemoryStore struct {
	// shards partitions facts by key hash for concurrent access.
	shards [memoryStoreShardCount]memoryStoreShard
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

// shardForKey returns the shard index for a key using FNV-1a hash.
func shardForKey(key string) uint32 {
	hash := uint32(2166136261)
	for index := 0; index < len(key); index++ {
		hash ^= uint32(key[index])
		hash *= 16777619
	}
	return hash & (memoryStoreShardCount - 1)
}

// NewMemoryStore creates and returns a new empty in-memory fact store with
// 64 data shards for concurrent access.
func NewMemoryStore() *MemoryStore {
	memStore := &MemoryStore{
		eventHistory:           newEventHistoryBuffer(defaultEventHistoryCapacity),
		watchChannelBufferSize: defaultWatchChannelBufferSize,
	}
	for shardIndex := range memStore.shards {
		memStore.shards[shardIndex].facts = make(map[string]*Fact)
		memStore.shards[shardIndex].keyIndex = newPrefixTrie()
	}
	return memStore
}

// SetWatchChannelBufferSize overrides the per-watcher channel capacity.
// Must be called before any Watch calls.
func (memStore *MemoryStore) SetWatchChannelBufferSize(bufferSize int) {
	memStore.watchChannelBufferSize = bufferSize
}

// Get retrieves the fact stored at the given key. Returns a defensive copy of
// the fact. Returns ErrKeyNotFound if the key does not exist. Only locks the
// single shard containing this key.
func (memStore *MemoryStore) Get(_ context.Context, key string) (*Fact, error) {
	if memStore.isClosed.Load() {
		return nil, ErrStoreClosed
	}
	shard := &memStore.shards[shardForKey(key)]
	shard.mutex.RLock()
	defer shard.mutex.RUnlock()

	existingFact, found := shard.facts[key]
	if !found {
		return nil, ErrKeyNotFound
	}
	factCopy := *existingFact
	factCopy.Value = cloneBytes(existingFact.Value)
	return &factCopy, nil
}

// Put creates or updates the fact at the given key. Locks only the single
// shard containing this key, then broadcasts the event outside the shard lock.
func (memStore *MemoryStore) Put(_ context.Context, key string, value []byte) (int64, error) {
	if memStore.isClosed.Load() {
		return 0, ErrStoreClosed
	}
	shard := &memStore.shards[shardForKey(key)]
	shard.mutex.Lock()

	if existingFact, found := shard.facts[key]; found && bytes.Equal(existingFact.Value, value) {
		revision := existingFact.Revision
		shard.mutex.Unlock()
		return revision, nil
	}

	newRevision := memStore.currentRevision.Add(1)
	event := memStore.applyPutToShard(shard, key, value, newRevision)
	shard.mutex.Unlock()

	memStore.broadcastEvent(event)
	return newRevision, nil
}

// applyPutToShard writes a fact to the given shard at the specified revision
// and returns the event to broadcast. Must be called with the shard locked.
func (memStore *MemoryStore) applyPutToShard(shard *memoryStoreShard, key string, value []byte, revision int64) Event {
	var previousFact *Fact
	if existingFact, found := shard.facts[key]; found {
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
	shard.facts[key] = newFact
	shard.keyIndex.Insert(key)

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
// ErrKeyNotFound if the key does not exist. Only locks the single shard.
func (memStore *MemoryStore) Delete(_ context.Context, key string) error {
	if memStore.isClosed.Load() {
		return ErrStoreClosed
	}
	shard := &memStore.shards[shardForKey(key)]
	shard.mutex.Lock()

	existingFact, found := shard.facts[key]
	if !found {
		shard.mutex.Unlock()
		return ErrKeyNotFound
	}

	newRevision := memStore.currentRevision.Add(1)
	previousFact := *existingFact
	previousFact.Value = cloneBytes(existingFact.Value)
	delete(shard.facts, key)
	shard.keyIndex.Remove(key)

	deletedFact := Fact{Key: key, Revision: newRevision}
	event := Event{Type: EventDelete, Fact: deletedFact, Prev: &previousFact}
	shard.mutex.Unlock()

	memStore.broadcastEvent(event)
	return nil
}

// Scan returns all facts whose keys begin with the given prefix, sorted
// alphabetically by key. Read-locks all shards for a consistent snapshot.
func (memStore *MemoryStore) Scan(_ context.Context, prefix string) ([]Fact, error) {
	if memStore.isClosed.Load() {
		return nil, ErrStoreClosed
	}
	for shardIndex := range memStore.shards {
		memStore.shards[shardIndex].mutex.RLock()
	}
	defer func() {
		for shardIndex := range memStore.shards {
			memStore.shards[shardIndex].mutex.RUnlock()
		}
	}()

	return memStore.collectFactsFromAllShards(prefix), nil
}

// ScanWithRevision returns all facts matching the prefix along with the
// store-global revision, both under a consistent snapshot across all shards.
func (memStore *MemoryStore) ScanWithRevision(_ context.Context, prefix string) (*ScanResult, error) {
	if memStore.isClosed.Load() {
		return nil, ErrStoreClosed
	}
	for shardIndex := range memStore.shards {
		memStore.shards[shardIndex].mutex.RLock()
	}
	defer func() {
		for shardIndex := range memStore.shards {
			memStore.shards[shardIndex].mutex.RUnlock()
		}
	}()

	snapshotRevision := memStore.currentRevision.Load()
	matchingFacts := memStore.collectFactsFromAllShards(prefix)

	return &ScanResult{
		Facts:    matchingFacts,
		Revision: snapshotRevision,
	}, nil
}

// collectFactsFromAllShards gathers and sorts facts matching the prefix from
// every shard. Must be called with all shard read-locks held.
func (memStore *MemoryStore) collectFactsFromAllShards(prefix string) []Fact {
	var allFacts []Fact
	for shardIndex := range memStore.shards {
		shard := &memStore.shards[shardIndex]
		matchingKeys := shard.keyIndex.KeysWithPrefix(prefix)
		for _, factKey := range matchingKeys {
			factEntry := shard.facts[factKey]
			factCopy := *factEntry
			factCopy.Value = cloneBytes(factEntry.Value)
			allFacts = append(allFacts, factCopy)
		}
	}
	sort.Slice(allFacts, func(indexA, indexB int) bool {
		return allFacts[indexA].Key < allFacts[indexB].Key
	})
	return allFacts
}

// Watch creates a new watch subscription for changes to the specified key (or
// key prefix if opts.Prefix is true). Returns a buffered channel that will
// receive events for matching changes. Uses the event mutex, not shard locks.
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
// appropriate branch. Locks only the shards involved in the transaction (in
// sorted order to prevent deadlock), not the entire store.
func (memStore *MemoryStore) Transaction(_ context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error) {
	if memStore.isClosed.Load() {
		return false, ErrStoreClosed
	}

	totalTransactionOperations := len(compares) + len(onSuccess) + len(onFailure)
	if totalTransactionOperations > memoryStoreTransactionOperationLimit {
		return false, fmt.Errorf("%w: %d operations exceeds limit of %d",
			ErrTransactionTooLarge, totalTransactionOperations, memoryStoreTransactionOperationLimit)
	}

	sortedShardIndices := memStore.collectInvolvedShards(compares, onSuccess, onFailure)

	for _, shardIndex := range sortedShardIndices {
		memStore.shards[shardIndex].mutex.Lock()
	}

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

	for _, shardIndex := range sortedShardIndices {
		memStore.shards[shardIndex].mutex.Unlock()
	}

	for _, event := range pendingEvents {
		memStore.broadcastEvent(event)
	}

	return allPreconditionsMet, nil
}

// collectInvolvedShards returns the deduplicated, sorted shard indices touched
// by any key in the compares, onSuccess, or onFailure slices.
func (memStore *MemoryStore) collectInvolvedShards(compares []Compare, onSuccess []Op, onFailure []Op) []uint32 {
	involvedShardSet := make(map[uint32]struct{})
	for _, comparison := range compares {
		involvedShardSet[shardForKey(comparison.Key)] = struct{}{}
	}
	for _, operation := range onSuccess {
		involvedShardSet[shardForKey(operation.Key)] = struct{}{}
	}
	for _, operation := range onFailure {
		involvedShardSet[shardForKey(operation.Key)] = struct{}{}
	}
	sortedShardIndices := make([]uint32, 0, len(involvedShardSet))
	for shardIndex := range involvedShardSet {
		sortedShardIndices = append(sortedShardIndices, shardIndex)
	}
	sort.Slice(sortedShardIndices, func(indexA, indexB int) bool {
		return sortedShardIndices[indexA] < sortedShardIndices[indexB]
	})
	return sortedShardIndices
}

// evaluateTransactionPreconditions checks whether all compare preconditions
// are satisfied. Must be called with involved shards locked.
func (memStore *MemoryStore) evaluateTransactionPreconditions(compares []Compare) bool {
	for _, comparison := range compares {
		shard := &memStore.shards[shardForKey(comparison.Key)]
		existingFact, factExists := shard.facts[comparison.Key]
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
// the store state. Must be called with involved shards locked.
func (memStore *MemoryStore) transactionWouldMutateState(operations []Op) bool {
	for _, operation := range operations {
		shard := &memStore.shards[shardForKey(operation.Key)]
		switch operation.Type {
		case OpPut:
			if existingFact, factExists := shard.facts[operation.Key]; !factExists || !bytes.Equal(existingFact.Value, operation.Value) {
				return true
			}
		case OpDelete:
			if _, factExists := shard.facts[operation.Key]; factExists {
				return true
			}
		}
	}
	return false
}

// executeTransactionOperations applies put and delete operations at a single
// revision, returning events to broadcast after shard locks are released.
// Must be called with involved shards locked.
func (memStore *MemoryStore) executeTransactionOperations(operations []Op, transactionRevision int64) []Event {
	var pendingEvents []Event
	for _, operation := range operations {
		shard := &memStore.shards[shardForKey(operation.Key)]
		switch operation.Type {
		case OpPut:
			event, changed := memStore.executePutOperation(shard, operation, transactionRevision)
			if changed {
				pendingEvents = append(pendingEvents, event)
			}
		case OpDelete:
			event, deleted := memStore.executeDeleteOperation(shard, operation, transactionRevision)
			if deleted {
				pendingEvents = append(pendingEvents, event)
			}
		}
	}
	return pendingEvents
}

// executePutOperation applies a single put within a transaction. Returns the
// event and true if the fact was actually modified. Must be called with the
// shard locked.
func (memStore *MemoryStore) executePutOperation(shard *memoryStoreShard, operation Op, transactionRevision int64) (Event, bool) {
	if existingFact, factExists := shard.facts[operation.Key]; factExists && bytes.Equal(existingFact.Value, operation.Value) {
		return Event{}, false
	}

	var previousFact *Fact
	if existingFact, factExists := shard.facts[operation.Key]; factExists {
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
	shard.facts[operation.Key] = newFact
	shard.keyIndex.Insert(operation.Key)

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
func (memStore *MemoryStore) executeDeleteOperation(shard *memoryStoreShard, operation Op, transactionRevision int64) (Event, bool) {
	existingFact, factExists := shard.facts[operation.Key]
	if !factExists {
		return Event{}, false
	}
	previousFact := *existingFact
	previousFact.Value = cloneBytes(existingFact.Value)
	delete(shard.facts, operation.Key)
	shard.keyIndex.Remove(operation.Key)
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
