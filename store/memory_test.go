package store

import (
	"context"
	"testing"
	"time"
)

var testContext = context.Background()

func TestPutAndGet(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	rev, err := memoryStore.Put(testContext, "/service/web", []byte("nginx"))
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Fatalf("expected revision 1, got %d", rev)
	}

	retrievedFact, err := memoryStore.Get(testContext, "/service/web")
	if err != nil {
		t.Fatal(err)
	}
	if string(retrievedFact.Value) != "nginx" {
		t.Fatalf("expected nginx, got %s", retrievedFact.Value)
	}
	if retrievedFact.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", retrievedFact.Revision)
	}
	if retrievedFact.CreateRevision != 1 {
		t.Fatalf("expected create revision 1, got %d", retrievedFact.CreateRevision)
	}
}

func TestGetNotFound(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	_, err := memoryStore.Get(testContext, "/missing")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestPutOverwrite(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/service/web", []byte("v1"))
	memoryStore.Put(testContext, "/service/web", []byte("v2"))

	retrievedFact, _ := memoryStore.Get(testContext, "/service/web")
	if string(retrievedFact.Value) != "v2" {
		t.Fatalf("expected v2, got %s", retrievedFact.Value)
	}
	if retrievedFact.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", retrievedFact.Revision)
	}
	if retrievedFact.CreateRevision != 1 {
		t.Fatalf("create revision should stay 1, got %d", retrievedFact.CreateRevision)
	}
}

func TestDelete(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/service/web", []byte("nginx"))

	err := memoryStore.Delete(testContext, "/service/web")
	if err != nil {
		t.Fatal(err)
	}

	_, err = memoryStore.Get(testContext, "/service/web")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	err := memoryStore.Delete(testContext, "/missing")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestScan(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/service/web", []byte("nginx"))
	memoryStore.Put(testContext, "/service/api", []byte("go"))
	memoryStore.Put(testContext, "/node/node-1", []byte("alive"))

	facts, err := memoryStore.Scan(testContext, "/service/")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}
	if facts[0].Key != "/service/api" {
		t.Fatalf("expected /service/api first (sorted), got %s", facts[0].Key)
	}
	if facts[1].Key != "/service/web" {
		t.Fatalf("expected /service/web second, got %s", facts[1].Key)
	}
}

func TestScanEmpty(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	facts, err := memoryStore.Scan(testContext, "/nothing/")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(facts))
	}
}

func TestRevision(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	rev, _ := memoryStore.Revision(testContext)
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	memoryStore.Put(testContext, "/a", []byte("1"))
	memoryStore.Put(testContext, "/b", []byte("2"))

	rev, _ = memoryStore.Revision(testContext)
	if rev != 2 {
		t.Fatalf("expected revision 2, got %d", rev)
	}
}

func TestTransactionSuccess(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/service/web", []byte("v1"))

	retrievedFact, _ := memoryStore.Get(testContext, "/service/web")

	ok, err := memoryStore.Transaction(testContext,
		[]Compare{{Key: "/service/web", Revision: retrievedFact.Revision}},
		[]Op{{Type: OpPut, Key: "/service/web", Value: []byte("v2")}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("transaction should have succeeded")
	}

	retrievedFact, _ = memoryStore.Get(testContext, "/service/web")
	if string(retrievedFact.Value) != "v2" {
		t.Fatalf("expected v2, got %s", retrievedFact.Value)
	}
}

func TestTransactionConflict(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/service/web", []byte("v1"))

	ok, err := memoryStore.Transaction(testContext,
		[]Compare{{Key: "/service/web", Revision: 999}},
		[]Op{{Type: OpPut, Key: "/service/web", Value: []byte("v2")}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("transaction should have failed due to revision mismatch")
	}

	retrievedFact, _ := memoryStore.Get(testContext, "/service/web")
	if string(retrievedFact.Value) != "v1" {
		t.Fatalf("value should remain v1, got %s", retrievedFact.Value)
	}
}

func TestTransactionCreateIfNotExists(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ok, err := memoryStore.Transaction(testContext,
		[]Compare{{Key: "/placement/a8retrievedFact31", Revision: 0}},
		[]Op{{Type: OpPut, Key: "/placement/a8retrievedFact31", Value: []byte("node-2")}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("transaction should succeed when key doesn't exist and revision=0")
	}

	// Second attempt should fail — key now exists
	ok, err = memoryStore.Transaction(testContext,
		[]Compare{{Key: "/placement/a8retrievedFact31", Revision: 0}},
		[]Op{{Type: OpPut, Key: "/placement/a8retrievedFact31", Value: []byte("node-3")}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("transaction should fail when key already exists and revision=0")
	}

	retrievedFact, _ := memoryStore.Get(testContext, "/placement/a8retrievedFact31")
	if string(retrievedFact.Value) != "node-2" {
		t.Fatalf("value should remain node-2, got %s", retrievedFact.Value)
	}
}

func TestTransactionOnFailureOps(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/service/web", []byte("v1"))

	ok, _ := memoryStore.Transaction(testContext,
		[]Compare{{Key: "/service/web", Revision: 999}},
		[]Op{{Type: OpPut, Key: "/result", Value: []byte("success")}},
		[]Op{{Type: OpPut, Key: "/result", Value: []byte("failure")}},
	)
	if ok {
		t.Fatal("transaction should have failed")
	}

	retrievedFact, _ := memoryStore.Get(testContext, "/result")
	if string(retrievedFact.Value) != "failure" {
		t.Fatalf("expected onFailure op to run, got %s", retrievedFact.Value)
	}
}

func TestTransactionDelete(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/endpoint/web/a8retrievedFact31", []byte("10.0.1.4"))
	retrievedFact, _ := memoryStore.Get(testContext, "/endpoint/web/a8retrievedFact31")

	ok, _ := memoryStore.Transaction(testContext,
		[]Compare{{Key: "/endpoint/web/a8retrievedFact31", Revision: retrievedFact.Revision}},
		[]Op{{Type: OpDelete, Key: "/endpoint/web/a8retrievedFact31"}},
		nil,
	)
	if !ok {
		t.Fatal("transaction should have succeeded")
	}

	_, err := memoryStore.Get(testContext, "/endpoint/web/a8retrievedFact31")
	if err != ErrKeyNotFound {
		t.Fatal("key should be deleted")
	}
}

func TestWatchExactKey(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	eventChannel, err := memoryStore.Watch(testContext, "/service/web", WatchOption{Prefix: false})
	if err != nil {
		t.Fatal(err)
	}

	memoryStore.Put(testContext, "/service/web", []byte("nginx"))
	memoryStore.Put(testContext, "/service/api", []byte("go")) // should not trigger

	select {
	case receivedEvent := <-eventChannel:
		if receivedEvent.Type != EventPut {
			t.Fatalf("expected EventPut, got %d", receivedEvent.Type)
		}
		if string(receivedEvent.Fact.Value) != "nginx" {
			t.Fatalf("expected nginx, got %s", receivedEvent.Fact.Value)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}

	select {
	case receivedEvent := <-eventChannel:
		t.Fatalf("should not receive event for /service/api, got %+v", receivedEvent)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchPrefix(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	eventChannel, err := memoryStore.Watch(testContext, "/service/", WatchOption{Prefix: true})
	if err != nil {
		t.Fatal(err)
	}

	memoryStore.Put(testContext, "/service/web", []byte("nginx"))
	memoryStore.Put(testContext, "/service/api", []byte("go"))
	memoryStore.Put(testContext, "/node/node-1", []byte("alive")) // should not trigger

	received := 0
	for iteration := 0; iteration < 2; iteration++ {
		select {
		case <-eventChannel:
			received++
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timed out waiting for event")
		}
	}
	if received != 2 {
		t.Fatalf("expected 2 events, got %d", received)
	}

	select {
	case receivedEvent := <-eventChannel:
		t.Fatalf("should not receive event for /node/, got %+v", receivedEvent)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchPutWithPrev(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/service/web", []byte("v1"))

	eventChannel, _ := memoryStore.Watch(testContext, "/service/web", WatchOption{})

	memoryStore.Put(testContext, "/service/web", []byte("v2"))

	select {
	case receivedEvent := <-eventChannel:
		if receivedEvent.Prev == nil {
			t.Fatal("expected Prev to be set on overwrite")
		}
		if string(receivedEvent.Prev.Value) != "v1" {
			t.Fatalf("expected prev value v1, got %s", receivedEvent.Prev.Value)
		}
		if string(receivedEvent.Fact.Value) != "v2" {
			t.Fatalf("expected new value v2, got %s", receivedEvent.Fact.Value)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out")
	}
}

func TestWatchDelete(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/service/web", []byte("nginx"))

	eventChannel, _ := memoryStore.Watch(testContext, "/service/web", WatchOption{})

	memoryStore.Delete(testContext, "/service/web")

	select {
	case receivedEvent := <-eventChannel:
		if receivedEvent.Type != EventDelete {
			t.Fatalf("expected EventDelete, got %d", receivedEvent.Type)
		}
		if receivedEvent.Prev == nil || string(receivedEvent.Prev.Value) != "nginx" {
			t.Fatal("expected prev value on delete")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out")
	}
}

func TestCloseStopsWatchers(t *testing.T) {
	memoryStore := NewMemoryStore()

	eventChannel, _ := memoryStore.Watch(testContext, "/", WatchOption{Prefix: true})

	memoryStore.Close()

	_, open := <-eventChannel
	if open {
		t.Fatal("channel should be closed after store.Close()")
	}
}

func TestValueIsolation(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	originalValue := []byte("original")
	memoryStore.Put(testContext, "/key", originalValue)

	// Mutate the original slice — should not affect stored value
	originalValue[0] = 'X'

	retrievedFact, _ := memoryStore.Get(testContext, "/key")
	if string(retrievedFact.Value) != "original" {
		t.Fatalf("stored value should be isolated from caller, got %s", retrievedFact.Value)
	}
}

func TestWatchContextCancellationUnregisters(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	watchCtx, watchCancel := context.WithCancel(context.Background())
	eventChannel, err := memoryStore.Watch(watchCtx, "/", WatchOption{Prefix: true})
	if err != nil {
		t.Fatal(err)
	}

	// Verify watcher count is 1.
	memoryStore.mutex.RLock()
	watcherCount := len(memoryStore.activeWatchers)
	memoryStore.mutex.RUnlock()
	if watcherCount != 1 {
		t.Fatalf("expected 1 watcher, got %d", watcherCount)
	}

	// Cancel context and wait briefly for cleanup goroutine.
	watchCancel()
	time.Sleep(50 * time.Millisecond)

	// Channel should be closed.
	_, open := <-eventChannel
	if open {
		t.Fatal("channel should be closed after context cancellation")
	}

	// Watcher should be unregistered.
	memoryStore.mutex.RLock()
	watcherCount = len(memoryStore.activeWatchers)
	memoryStore.mutex.RUnlock()
	if watcherCount != 0 {
		t.Fatalf("expected 0 watchers after cancel, got %d", watcherCount)
	}
}

func TestGetValueDeepCopy(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/key", []byte("original"))

	// Get and mutate the returned value — should not corrupt store.
	firstRead, _ := memoryStore.Get(testContext, "/key")
	firstRead.Value[0] = 'X'

	secondRead, _ := memoryStore.Get(testContext, "/key")
	if string(secondRead.Value) != "original" {
		t.Fatalf("Get must deep-copy values; mutating returned slice corrupted store: got %s", secondRead.Value)
	}
}

func TestTransactionSingleRevision(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	revBefore, _ := memoryStore.Revision(testContext)

	ok, err := memoryStore.Transaction(testContext,
		nil,
		[]Op{
			{Type: OpPut, Key: "/a", Value: []byte("1")},
			{Type: OpPut, Key: "/b", Value: []byte("2")},
			{Type: OpPut, Key: "/c", Value: []byte("3")},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("transaction should succeed")
	}

	revAfter, _ := memoryStore.Revision(testContext)
	if revAfter != revBefore+1 {
		t.Fatalf("transaction with 3 puts should produce exactly one revision bump: before=%d, after=%d", revBefore, revAfter)
	}

	// All three facts should share the same revision.
	factA, _ := memoryStore.Get(testContext, "/a")
	factB, _ := memoryStore.Get(testContext, "/b")
	factC, _ := memoryStore.Get(testContext, "/c")
	if factA.Revision != factB.Revision || factB.Revision != factC.Revision {
		t.Fatalf("all facts in a transaction should share one revision: a=%d, b=%d, c=%d", factA.Revision, factB.Revision, factC.Revision)
	}
}

func TestIdempotentPut(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	rev1, _ := memoryStore.Put(testContext, "/key", []byte("value"))
	rev2, _ := memoryStore.Put(testContext, "/key", []byte("value"))

	if rev1 != rev2 {
		t.Fatalf("idempotent Put should return same revision: got %d then %d", rev1, rev2)
	}

	globalRev, _ := memoryStore.Revision(testContext)
	if globalRev != 1 {
		t.Fatalf("idempotent Put should not increment global revision: expected 1, got %d", globalRev)
	}

	// Verify no watch event for the duplicate put.
	eventChannel, _ := memoryStore.Watch(testContext, "/key", WatchOption{})
	memoryStore.Put(testContext, "/key", []byte("value"))

	select {
	case unexpectedEvent := <-eventChannel:
		t.Fatalf("idempotent Put should not emit watch event, got %+v", unexpectedEvent)
	case <-time.After(50 * time.Millisecond):
	}

	// A different value should still produce a new revision.
	rev3, _ := memoryStore.Put(testContext, "/key", []byte("changed"))
	if rev3 == rev1 {
		t.Fatal("Put with different value should produce new revision")
	}
}

func TestOperationsAfterCloseReturnError(t *testing.T) {
	memoryStore := NewMemoryStore()
	memoryStore.Put(testContext, "/key", []byte("value"))
	memoryStore.Close()

	if _, err := memoryStore.Get(testContext, "/key"); err != ErrStoreClosed {
		t.Fatalf("Get after Close: expected ErrStoreClosed, got %v", err)
	}
	if _, err := memoryStore.Put(testContext, "/key", []byte("v2")); err != ErrStoreClosed {
		t.Fatalf("Put after Close: expected ErrStoreClosed, got %v", err)
	}
	if err := memoryStore.Delete(testContext, "/key"); err != ErrStoreClosed {
		t.Fatalf("Delete after Close: expected ErrStoreClosed, got %v", err)
	}
	if _, err := memoryStore.Scan(testContext, "/"); err != ErrStoreClosed {
		t.Fatalf("Scan after Close: expected ErrStoreClosed, got %v", err)
	}
	if _, err := memoryStore.Watch(testContext, "/", WatchOption{Prefix: true}); err != ErrStoreClosed {
		t.Fatalf("Watch after Close: expected ErrStoreClosed, got %v", err)
	}
	if _, err := memoryStore.Transaction(testContext, nil, nil, nil); err != ErrStoreClosed {
		t.Fatalf("Transaction after Close: expected ErrStoreClosed, got %v", err)
	}
	if _, err := memoryStore.Revision(testContext); err != ErrStoreClosed {
		t.Fatalf("Revision after Close: expected ErrStoreClosed, got %v", err)
	}
}

func TestScanValueDeepCopy(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(testContext, "/prefix/a", []byte("hello"))

	// Scan and mutate the returned value — should not corrupt store.
	facts, _ := memoryStore.Scan(testContext, "/prefix/")
	facts[0].Value[0] = 'X'

	factsAgain, _ := memoryStore.Scan(testContext, "/prefix/")
	if string(factsAgain[0].Value) != "hello" {
		t.Fatalf("Scan must deep-copy values; mutating returned slice corrupted store: got %s", factsAgain[0].Value)
	}
}
