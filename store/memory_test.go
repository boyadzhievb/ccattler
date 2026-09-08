package store

import (
	"context"
	"testing"
	"time"
)

var ctx = context.Background()

func TestPutAndGet(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	rev, err := memoryStore.Put(ctx, "/service/web", []byte("nginx"))
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Fatalf("expected revision 1, got %d", rev)
	}

	f, err := memoryStore.Get(ctx, "/service/web")
	if err != nil {
		t.Fatal(err)
	}
	if string(f.Value) != "nginx" {
		t.Fatalf("expected nginx, got %s", f.Value)
	}
	if f.Revision != 1 {
		t.Fatalf("expected revision 1, got %d", f.Revision)
	}
	if f.CreateRevision != 1 {
		t.Fatalf("expected create revision 1, got %d", f.CreateRevision)
	}
}

func TestGetNotFound(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	_, err := memoryStore.Get(ctx, "/missing")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestPutOverwrite(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(ctx, "/service/web", []byte("v1"))
	memoryStore.Put(ctx, "/service/web", []byte("v2"))

	f, _ := memoryStore.Get(ctx, "/service/web")
	if string(f.Value) != "v2" {
		t.Fatalf("expected v2, got %s", f.Value)
	}
	if f.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", f.Revision)
	}
	if f.CreateRevision != 1 {
		t.Fatalf("create revision should stay 1, got %d", f.CreateRevision)
	}
}

func TestDelete(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(ctx, "/service/web", []byte("nginx"))

	err := memoryStore.Delete(ctx, "/service/web")
	if err != nil {
		t.Fatal(err)
	}

	_, err = memoryStore.Get(ctx, "/service/web")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	err := memoryStore.Delete(ctx, "/missing")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestScan(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(ctx, "/service/web", []byte("nginx"))
	memoryStore.Put(ctx, "/service/api", []byte("go"))
	memoryStore.Put(ctx, "/node/node-1", []byte("alive"))

	facts, err := memoryStore.Scan(ctx, "/service/")
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

	facts, err := memoryStore.Scan(ctx, "/nothing/")
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

	rev, _ := memoryStore.Revision(ctx)
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	memoryStore.Put(ctx, "/a", []byte("1"))
	memoryStore.Put(ctx, "/b", []byte("2"))

	rev, _ = memoryStore.Revision(ctx)
	if rev != 2 {
		t.Fatalf("expected revision 2, got %d", rev)
	}
}

func TestTransactionSuccess(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(ctx, "/service/web", []byte("v1"))

	f, _ := memoryStore.Get(ctx, "/service/web")

	ok, err := memoryStore.Transaction(ctx,
		[]Compare{{Key: "/service/web", Revision: f.Revision}},
		[]Op{{Type: OpPut, Key: "/service/web", Value: []byte("v2")}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("transaction should have succeeded")
	}

	f, _ = memoryStore.Get(ctx, "/service/web")
	if string(f.Value) != "v2" {
		t.Fatalf("expected v2, got %s", f.Value)
	}
}

func TestTransactionConflict(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(ctx, "/service/web", []byte("v1"))

	ok, err := memoryStore.Transaction(ctx,
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

	f, _ := memoryStore.Get(ctx, "/service/web")
	if string(f.Value) != "v1" {
		t.Fatalf("value should remain v1, got %s", f.Value)
	}
}

func TestTransactionCreateIfNotExists(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ok, err := memoryStore.Transaction(ctx,
		[]Compare{{Key: "/placement/a8f31", Revision: 0}},
		[]Op{{Type: OpPut, Key: "/placement/a8f31", Value: []byte("node-2")}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("transaction should succeed when key doesn't exist and revision=0")
	}

	// Second attempt should fail — key now exists
	ok, err = memoryStore.Transaction(ctx,
		[]Compare{{Key: "/placement/a8f31", Revision: 0}},
		[]Op{{Type: OpPut, Key: "/placement/a8f31", Value: []byte("node-3")}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("transaction should fail when key already exists and revision=0")
	}

	f, _ := memoryStore.Get(ctx, "/placement/a8f31")
	if string(f.Value) != "node-2" {
		t.Fatalf("value should remain node-2, got %s", f.Value)
	}
}

func TestTransactionOnFailureOps(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(ctx, "/service/web", []byte("v1"))

	ok, _ := memoryStore.Transaction(ctx,
		[]Compare{{Key: "/service/web", Revision: 999}},
		[]Op{{Type: OpPut, Key: "/result", Value: []byte("success")}},
		[]Op{{Type: OpPut, Key: "/result", Value: []byte("failure")}},
	)
	if ok {
		t.Fatal("transaction should have failed")
	}

	f, _ := memoryStore.Get(ctx, "/result")
	if string(f.Value) != "failure" {
		t.Fatalf("expected onFailure op to run, got %s", f.Value)
	}
}

func TestTransactionDelete(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(ctx, "/endpoint/web/a8f31", []byte("10.0.1.4"))
	f, _ := memoryStore.Get(ctx, "/endpoint/web/a8f31")

	ok, _ := memoryStore.Transaction(ctx,
		[]Compare{{Key: "/endpoint/web/a8f31", Revision: f.Revision}},
		[]Op{{Type: OpDelete, Key: "/endpoint/web/a8f31"}},
		nil,
	)
	if !ok {
		t.Fatal("transaction should have succeeded")
	}

	_, err := memoryStore.Get(ctx, "/endpoint/web/a8f31")
	if err != ErrKeyNotFound {
		t.Fatal("key should be deleted")
	}
}

func TestWatchExactKey(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ch, err := memoryStore.Watch(ctx, "/service/web", WatchOption{Prefix: false})
	if err != nil {
		t.Fatal(err)
	}

	memoryStore.Put(ctx, "/service/web", []byte("nginx"))
	memoryStore.Put(ctx, "/service/api", []byte("go")) // should not trigger

	select {
	case receivedEvent := <-ch:
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
	case receivedEvent := <-ch:
		t.Fatalf("should not receive event for /service/api, got %+v", receivedEvent)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchPrefix(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	ch, err := memoryStore.Watch(ctx, "/service/", WatchOption{Prefix: true})
	if err != nil {
		t.Fatal(err)
	}

	memoryStore.Put(ctx, "/service/web", []byte("nginx"))
	memoryStore.Put(ctx, "/service/api", []byte("go"))
	memoryStore.Put(ctx, "/node/node-1", []byte("alive")) // should not trigger

	received := 0
	for iteration := 0; iteration < 2; iteration++ {
		select {
		case <-ch:
			received++
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timed out waiting for event")
		}
	}
	if received != 2 {
		t.Fatalf("expected 2 events, got %d", received)
	}

	select {
	case receivedEvent := <-ch:
		t.Fatalf("should not receive event for /node/, got %+v", receivedEvent)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchPutWithPrev(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	memoryStore.Put(ctx, "/service/web", []byte("v1"))

	ch, _ := memoryStore.Watch(ctx, "/service/web", WatchOption{})

	memoryStore.Put(ctx, "/service/web", []byte("v2"))

	select {
	case receivedEvent := <-ch:
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

	memoryStore.Put(ctx, "/service/web", []byte("nginx"))

	ch, _ := memoryStore.Watch(ctx, "/service/web", WatchOption{})

	memoryStore.Delete(ctx, "/service/web")

	select {
	case receivedEvent := <-ch:
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

	ch, _ := memoryStore.Watch(ctx, "/", WatchOption{Prefix: true})

	memoryStore.Close()

	_, open := <-ch
	if open {
		t.Fatal("channel should be closed after store.Close()")
	}
}

func TestValueIsolation(t *testing.T) {
	memoryStore := NewMemoryStore()
	defer memoryStore.Close()

	val := []byte("original")
	memoryStore.Put(ctx, "/key", val)

	// Mutate the original slice — should not affect stored value
	val[0] = 'X'

	f, _ := memoryStore.Get(ctx, "/key")
	if string(f.Value) != "original" {
		t.Fatalf("stored value should be isolated from caller, got %s", f.Value)
	}
}
