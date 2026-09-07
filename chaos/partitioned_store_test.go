package chaos

import (
	"context"
	"sync"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
)

// TestPartitionedStorePassesThroughWhenHealed verifies that all store
// operations delegate to the underlying store when not partitioned.
func TestPartitionedStorePassesThroughWhenHealed(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	partitionedStore := NewPartitionedStore(memoryStore)

	ctx := context.Background()

	revision, err := partitionedStore.Put(ctx, "/test/key", []byte("value"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if revision == 0 {
		t.Fatal("expected non-zero revision from Put")
	}

	fact, err := partitionedStore.Get(ctx, "/test/key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(fact.Value) != "value" {
		t.Fatalf("expected value 'value', got %q", string(fact.Value))
	}

	facts, err := partitionedStore.Scan(ctx, "/test/")
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact from Scan, got %d", len(facts))
	}

	err = partitionedStore.Delete(ctx, "/test/key")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	storeRevision, err := partitionedStore.Revision(ctx)
	if err != nil {
		t.Fatalf("Revision failed: %v", err)
	}
	if storeRevision == 0 {
		t.Fatal("expected non-zero revision")
	}
}

// TestPartitionedStoreReturnsErrorWhenPartitioned verifies that all operations
// return ErrPartitioned when the partition is active.
func TestPartitionedStoreReturnsErrorWhenPartitioned(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	partitionedStore := NewPartitionedStore(memoryStore)
	partitionedStore.Partition()

	ctx := context.Background()

	_, err := partitionedStore.Get(ctx, "/test/key")
	if err != ErrPartitioned {
		t.Fatalf("Get: expected ErrPartitioned, got %v", err)
	}

	_, err = partitionedStore.Put(ctx, "/test/key", []byte("value"))
	if err != ErrPartitioned {
		t.Fatalf("Put: expected ErrPartitioned, got %v", err)
	}

	err = partitionedStore.Delete(ctx, "/test/key")
	if err != ErrPartitioned {
		t.Fatalf("Delete: expected ErrPartitioned, got %v", err)
	}

	_, err = partitionedStore.Scan(ctx, "/test/")
	if err != ErrPartitioned {
		t.Fatalf("Scan: expected ErrPartitioned, got %v", err)
	}

	_, err = partitionedStore.Revision(ctx)
	if err != ErrPartitioned {
		t.Fatalf("Revision: expected ErrPartitioned, got %v", err)
	}
}

// TestPartitionedStoreWatchReturnsErrorWhenPartitioned verifies that Watch
// returns ErrPartitioned when the store is partitioned at call time.
func TestPartitionedStoreWatchReturnsErrorWhenPartitioned(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	partitionedStore := NewPartitionedStore(memoryStore)
	partitionedStore.Partition()

	ctx := context.Background()
	_, err := partitionedStore.Watch(ctx, "/test/", store.WatchOption{Prefix: true})
	if err != ErrPartitioned {
		t.Fatalf("Watch: expected ErrPartitioned, got %v", err)
	}
}

// TestPartitionedStoreTransactionReturnsErrorWhenPartitioned verifies that
// Transaction returns ErrPartitioned when the store is partitioned.
func TestPartitionedStoreTransactionReturnsErrorWhenPartitioned(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	partitionedStore := NewPartitionedStore(memoryStore)
	partitionedStore.Partition()

	ctx := context.Background()
	_, err := partitionedStore.Transaction(ctx, nil, []store.Op{
		{Type: store.OpPut, Key: "/test/key", Value: []byte("value")},
	}, nil)
	if err != ErrPartitioned {
		t.Fatalf("Transaction: expected ErrPartitioned, got %v", err)
	}
}

// TestPartitionedStorePartitionAndHealCycle verifies that operations fail
// during partition and succeed after healing.
func TestPartitionedStorePartitionAndHealCycle(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	partitionedStore := NewPartitionedStore(memoryStore)

	ctx := context.Background()

	_, err := partitionedStore.Put(ctx, "/test/key", []byte("before"))
	if err != nil {
		t.Fatalf("Put before partition failed: %v", err)
	}

	partitionedStore.Partition()

	_, err = partitionedStore.Put(ctx, "/test/key", []byte("during"))
	if err != ErrPartitioned {
		t.Fatalf("Put during partition: expected ErrPartitioned, got %v", err)
	}

	partitionedStore.Heal()

	_, err = partitionedStore.Put(ctx, "/test/key", []byte("after"))
	if err != nil {
		t.Fatalf("Put after heal failed: %v", err)
	}

	fact, err := partitionedStore.Get(ctx, "/test/key")
	if err != nil {
		t.Fatalf("Get after heal failed: %v", err)
	}
	if string(fact.Value) != "after" {
		t.Fatalf("expected value 'after', got %q", string(fact.Value))
	}
}

// TestPartitionedStoreConcurrentPartitionSafety verifies that concurrent
// Partition/Heal calls and store operations don't race.
func TestPartitionedStoreConcurrentPartitionSafety(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	partitionedStore := NewPartitionedStore(memoryStore)

	ctx := context.Background()
	var waitGroup sync.WaitGroup

	waitGroup.Add(3)

	go func() {
		defer waitGroup.Done()
		for range 100 {
			partitionedStore.Partition()
			partitionedStore.Heal()
		}
	}()

	go func() {
		defer waitGroup.Done()
		for range 100 {
			partitionedStore.Put(ctx, "/test/concurrent", []byte("value"))
		}
	}()

	go func() {
		defer waitGroup.Done()
		for range 100 {
			partitionedStore.Get(ctx, "/test/concurrent")
		}
	}()

	waitGroup.Wait()
}

// TestPartitionedStoreClosePassesThrough verifies that Close always delegates
// to the underlying store regardless of partition state.
func TestPartitionedStoreClosePassesThrough(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	partitionedStore := NewPartitionedStore(memoryStore)
	partitionedStore.Partition()

	err := partitionedStore.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestPartitionedStoreIsPartitionedReflectsState verifies that IsPartitioned
// accurately reports the current partition state.
func TestPartitionedStoreIsPartitionedReflectsState(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	partitionedStore := NewPartitionedStore(memoryStore)

	if partitionedStore.IsPartitioned() {
		t.Fatal("expected not partitioned initially")
	}

	partitionedStore.Partition()
	if !partitionedStore.IsPartitioned() {
		t.Fatal("expected partitioned after Partition()")
	}

	partitionedStore.Heal()
	if partitionedStore.IsPartitioned() {
		t.Fatal("expected not partitioned after Heal()")
	}
}
