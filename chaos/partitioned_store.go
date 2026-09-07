package chaos

import (
	"context"
	"errors"
	"sync"

	"github.com/boyadzhievb/ccattler/store"
)

// ErrPartitioned is returned by all store operations when the partition is active.
var ErrPartitioned = errors.New("store partitioned: network unreachable")

// PartitionedStore wraps a StateStore to simulate network partitions.
// When partitioned, all operations return ErrPartitioned. When healed,
// operations pass through to the underlying store transparently.
type PartitionedStore struct {
	// underlyingStore is the real store that operations delegate to when not partitioned.
	underlyingStore store.StateStore
	// partitionMutex guards the isPartitioned flag for concurrent access.
	partitionMutex sync.RWMutex
	// isPartitioned is true when this store view is cut off from the backing store.
	isPartitioned bool
}

// NewPartitionedStore creates a PartitionedStore wrapping the given backing store.
// The partition starts in the healed (connected) state.
func NewPartitionedStore(underlyingStore store.StateStore) *PartitionedStore {
	return &PartitionedStore{
		underlyingStore: underlyingStore,
	}
}

// Partition activates the network partition. All subsequent store operations
// will return ErrPartitioned until Heal is called.
func (partitionedStore *PartitionedStore) Partition() {
	partitionedStore.partitionMutex.Lock()
	defer partitionedStore.partitionMutex.Unlock()
	partitionedStore.isPartitioned = true
}

// Heal deactivates the network partition. Subsequent store operations will
// pass through to the underlying store.
func (partitionedStore *PartitionedStore) Heal() {
	partitionedStore.partitionMutex.Lock()
	defer partitionedStore.partitionMutex.Unlock()
	partitionedStore.isPartitioned = false
}

// IsPartitioned returns whether the store is currently partitioned.
func (partitionedStore *PartitionedStore) IsPartitioned() bool {
	partitionedStore.partitionMutex.RLock()
	defer partitionedStore.partitionMutex.RUnlock()
	return partitionedStore.isPartitioned
}

// checkPartition returns ErrPartitioned if the store is currently partitioned.
func (partitionedStore *PartitionedStore) checkPartition() error {
	partitionedStore.partitionMutex.RLock()
	defer partitionedStore.partitionMutex.RUnlock()
	if partitionedStore.isPartitioned {
		return ErrPartitioned
	}
	return nil
}

// Get retrieves a single fact by its exact key. Returns ErrPartitioned if partitioned.
func (partitionedStore *PartitionedStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	if err := partitionedStore.checkPartition(); err != nil {
		return nil, err
	}
	return partitionedStore.underlyingStore.Get(ctx, key)
}

// Put creates or updates the fact at the given key. Returns ErrPartitioned if partitioned.
func (partitionedStore *PartitionedStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	if err := partitionedStore.checkPartition(); err != nil {
		return 0, err
	}
	return partitionedStore.underlyingStore.Put(ctx, key, value)
}

// Delete removes the fact at the given key. Returns ErrPartitioned if partitioned.
func (partitionedStore *PartitionedStore) Delete(ctx context.Context, key string) error {
	if err := partitionedStore.checkPartition(); err != nil {
		return err
	}
	return partitionedStore.underlyingStore.Delete(ctx, key)
}

// Scan returns all facts whose keys begin with the given prefix. Returns ErrPartitioned if partitioned.
func (partitionedStore *PartitionedStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	if err := partitionedStore.checkPartition(); err != nil {
		return nil, err
	}
	return partitionedStore.underlyingStore.Scan(ctx, prefix)
}

// Watch creates a subscription for changes to the specified key or prefix.
// Returns ErrPartitioned if the store is partitioned at call time.
// Pre-partition watches continue delivering events from the underlying store.
func (partitionedStore *PartitionedStore) Watch(ctx context.Context, key string, opts store.WatchOption) (<-chan store.Event, error) {
	if err := partitionedStore.checkPartition(); err != nil {
		return nil, err
	}
	return partitionedStore.underlyingStore.Watch(ctx, key, opts)
}

// Transaction atomically evaluates compares and executes operations.
// Returns ErrPartitioned if partitioned.
func (partitionedStore *PartitionedStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	if err := partitionedStore.checkPartition(); err != nil {
		return false, err
	}
	return partitionedStore.underlyingStore.Transaction(ctx, compares, onSuccess, onFailure)
}

// Revision returns the current store-global revision counter.
// Returns ErrPartitioned if partitioned.
func (partitionedStore *PartitionedStore) Revision(ctx context.Context) (int64, error) {
	if err := partitionedStore.checkPartition(); err != nil {
		return 0, err
	}
	return partitionedStore.underlyingStore.Revision(ctx)
}

// Close shuts down the store, delegating to the underlying store.
// Close always passes through regardless of partition state.
func (partitionedStore *PartitionedStore) Close() error {
	return partitionedStore.underlyingStore.Close()
}
