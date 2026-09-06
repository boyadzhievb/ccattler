package store

import (
	"context"
	"testing"
	"time"
)

var ctx = context.Background()

func TestPutAndGet(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	rev, err := s.Put(ctx, "/service/web", []byte("nginx"))
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Fatalf("expected revision 1, got %d", rev)
	}

	f, err := s.Get(ctx, "/service/web")
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
	s := NewMemoryStore()
	defer s.Close()

	_, err := s.Get(ctx, "/missing")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestPutOverwrite(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/service/web", []byte("v1"))
	s.Put(ctx, "/service/web", []byte("v2"))

	f, _ := s.Get(ctx, "/service/web")
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
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/service/web", []byte("nginx"))

	err := s.Delete(ctx, "/service/web")
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Get(ctx, "/service/web")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	err := s.Delete(ctx, "/missing")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestScan(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/service/web", []byte("nginx"))
	s.Put(ctx, "/service/api", []byte("go"))
	s.Put(ctx, "/node/node-1", []byte("alive"))

	facts, err := s.Scan(ctx, "/service/")
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
	s := NewMemoryStore()
	defer s.Close()

	facts, err := s.Scan(ctx, "/nothing/")
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(facts))
	}
}

func TestRevision(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	rev, _ := s.Revision(ctx)
	if rev != 0 {
		t.Fatalf("expected revision 0, got %d", rev)
	}

	s.Put(ctx, "/a", []byte("1"))
	s.Put(ctx, "/b", []byte("2"))

	rev, _ = s.Revision(ctx)
	if rev != 2 {
		t.Fatalf("expected revision 2, got %d", rev)
	}
}

func TestTransactionSuccess(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/service/web", []byte("v1"))

	f, _ := s.Get(ctx, "/service/web")

	ok, err := s.Transaction(ctx,
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

	f, _ = s.Get(ctx, "/service/web")
	if string(f.Value) != "v2" {
		t.Fatalf("expected v2, got %s", f.Value)
	}
}

func TestTransactionConflict(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/service/web", []byte("v1"))

	ok, err := s.Transaction(ctx,
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

	f, _ := s.Get(ctx, "/service/web")
	if string(f.Value) != "v1" {
		t.Fatalf("value should remain v1, got %s", f.Value)
	}
}

func TestTransactionCreateIfNotExists(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	ok, err := s.Transaction(ctx,
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
	ok, err = s.Transaction(ctx,
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

	f, _ := s.Get(ctx, "/placement/a8f31")
	if string(f.Value) != "node-2" {
		t.Fatalf("value should remain node-2, got %s", f.Value)
	}
}

func TestTransactionOnFailureOps(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/service/web", []byte("v1"))

	ok, _ := s.Transaction(ctx,
		[]Compare{{Key: "/service/web", Revision: 999}},
		[]Op{{Type: OpPut, Key: "/result", Value: []byte("success")}},
		[]Op{{Type: OpPut, Key: "/result", Value: []byte("failure")}},
	)
	if ok {
		t.Fatal("transaction should have failed")
	}

	f, _ := s.Get(ctx, "/result")
	if string(f.Value) != "failure" {
		t.Fatalf("expected onFailure op to run, got %s", f.Value)
	}
}

func TestTransactionDelete(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/endpoint/web/a8f31", []byte("10.0.1.4"))
	f, _ := s.Get(ctx, "/endpoint/web/a8f31")

	ok, _ := s.Transaction(ctx,
		[]Compare{{Key: "/endpoint/web/a8f31", Revision: f.Revision}},
		[]Op{{Type: OpDelete, Key: "/endpoint/web/a8f31"}},
		nil,
	)
	if !ok {
		t.Fatal("transaction should have succeeded")
	}

	_, err := s.Get(ctx, "/endpoint/web/a8f31")
	if err != ErrKeyNotFound {
		t.Fatal("key should be deleted")
	}
}

func TestWatchExactKey(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	ch, err := s.Watch(ctx, "/service/web", WatchOption{Prefix: false})
	if err != nil {
		t.Fatal(err)
	}

	s.Put(ctx, "/service/web", []byte("nginx"))
	s.Put(ctx, "/service/api", []byte("go")) // should not trigger

	select {
	case e := <-ch:
		if e.Type != EventPut {
			t.Fatalf("expected EventPut, got %d", e.Type)
		}
		if string(e.Fact.Value) != "nginx" {
			t.Fatalf("expected nginx, got %s", e.Fact.Value)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}

	select {
	case e := <-ch:
		t.Fatalf("should not receive event for /service/api, got %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchPrefix(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	ch, err := s.Watch(ctx, "/service/", WatchOption{Prefix: true})
	if err != nil {
		t.Fatal(err)
	}

	s.Put(ctx, "/service/web", []byte("nginx"))
	s.Put(ctx, "/service/api", []byte("go"))
	s.Put(ctx, "/node/node-1", []byte("alive")) // should not trigger

	received := 0
	for i := 0; i < 2; i++ {
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
	case e := <-ch:
		t.Fatalf("should not receive event for /node/, got %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchPutWithPrev(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/service/web", []byte("v1"))

	ch, _ := s.Watch(ctx, "/service/web", WatchOption{})

	s.Put(ctx, "/service/web", []byte("v2"))

	select {
	case e := <-ch:
		if e.Prev == nil {
			t.Fatal("expected Prev to be set on overwrite")
		}
		if string(e.Prev.Value) != "v1" {
			t.Fatalf("expected prev value v1, got %s", e.Prev.Value)
		}
		if string(e.Fact.Value) != "v2" {
			t.Fatalf("expected new value v2, got %s", e.Fact.Value)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out")
	}
}

func TestWatchDelete(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	s.Put(ctx, "/service/web", []byte("nginx"))

	ch, _ := s.Watch(ctx, "/service/web", WatchOption{})

	s.Delete(ctx, "/service/web")

	select {
	case e := <-ch:
		if e.Type != EventDelete {
			t.Fatalf("expected EventDelete, got %d", e.Type)
		}
		if e.Prev == nil || string(e.Prev.Value) != "nginx" {
			t.Fatal("expected prev value on delete")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out")
	}
}

func TestCloseStopsWatchers(t *testing.T) {
	s := NewMemoryStore()

	ch, _ := s.Watch(ctx, "/", WatchOption{Prefix: true})

	s.Close()

	_, open := <-ch
	if open {
		t.Fatal("channel should be closed after store.Close()")
	}
}

func TestValueIsolation(t *testing.T) {
	s := NewMemoryStore()
	defer s.Close()

	val := []byte("original")
	s.Put(ctx, "/key", val)

	// Mutate the original slice — should not affect stored value
	val[0] = 'X'

	f, _ := s.Get(ctx, "/key")
	if string(f.Value) != "original" {
		t.Fatalf("stored value should be isolated from caller, got %s", f.Value)
	}
}
