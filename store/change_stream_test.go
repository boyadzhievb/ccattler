package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestChangeStreamReceivesChanges(t *testing.T) {
	memoryStore := NewMemoryStore()
	changeStream := NewChangeStream(memoryStore, "desired/")

	receivedRecords := make([]ChangeRecord, 0)
	recordsMutex := sync.Mutex{}

	changeStream.Subscribe(func(record ChangeRecord) error {
		recordsMutex.Lock()
		receivedRecords = append(receivedRecords, record)
		recordsMutex.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamReady := make(chan struct{})
	go func() {
		close(streamReady)
		changeStream.Start(ctx)
	}()
	<-streamReady
	time.Sleep(50 * time.Millisecond)

	memoryStore.Put(ctx, "desired/service/web/image", []byte("nginx:latest"))
	memoryStore.Put(ctx, "desired/service/api/image", []byte("api:v2"))
	memoryStore.Put(ctx, "observed/instance/aaa/state", []byte("running"))

	time.Sleep(100 * time.Millisecond)

	recordsMutex.Lock()
	defer recordsMutex.Unlock()

	if len(receivedRecords) != 2 {
		t.Fatalf("expected 2 records (desired/ prefix only), got %d", len(receivedRecords))
	}

	if receivedRecords[0].Key != "desired/service/web/image" {
		t.Errorf("first record key: got %s", receivedRecords[0].Key)
	}
	if string(receivedRecords[0].Value) != "nginx:latest" {
		t.Errorf("first record value: got %s", string(receivedRecords[0].Value))
	}
}

func TestChangeStreamMultipleSubscribers(t *testing.T) {
	memoryStore := NewMemoryStore()
	changeStream := NewChangeStream(memoryStore, "desired/")

	subscriberACalls := 0
	subscriberBCalls := 0
	callsMutex := sync.Mutex{}

	changeStream.Subscribe(func(record ChangeRecord) error {
		callsMutex.Lock()
		subscriberACalls++
		callsMutex.Unlock()
		return nil
	})
	changeStream.Subscribe(func(record ChangeRecord) error {
		callsMutex.Lock()
		subscriberBCalls++
		callsMutex.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go changeStream.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	memoryStore.Put(ctx, "desired/service/web/image", []byte("nginx"))
	time.Sleep(100 * time.Millisecond)

	callsMutex.Lock()
	defer callsMutex.Unlock()

	if subscriberACalls != 1 {
		t.Errorf("subscriber A: expected 1 call, got %d", subscriberACalls)
	}
	if subscriberBCalls != 1 {
		t.Errorf("subscriber B: expected 1 call, got %d", subscriberBCalls)
	}
}

func TestChangeStreamCapturesPrevValue(t *testing.T) {
	memoryStore := NewMemoryStore()
	changeStream := NewChangeStream(memoryStore, "desired/")

	var capturedRecord ChangeRecord
	capturedMutex := sync.Mutex{}

	changeStream.Subscribe(func(record ChangeRecord) error {
		capturedMutex.Lock()
		capturedRecord = record
		capturedMutex.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	memoryStore.Put(ctx, "desired/service/web/image", []byte("v1"))

	go changeStream.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	memoryStore.Put(ctx, "desired/service/web/image", []byte("v2"))
	time.Sleep(100 * time.Millisecond)

	capturedMutex.Lock()
	defer capturedMutex.Unlock()

	if string(capturedRecord.Value) != "v2" {
		t.Errorf("expected value v2, got %s", string(capturedRecord.Value))
	}
	if capturedRecord.PrevValue != nil && string(capturedRecord.PrevValue) != "v1" {
		t.Errorf("expected prev value v1, got %s", string(capturedRecord.PrevValue))
	}
}

func TestChangeStreamStopsOnCancel(t *testing.T) {
	memoryStore := NewMemoryStore()
	changeStream := NewChangeStream(memoryStore, "desired/")

	ctx, cancel := context.WithCancel(context.Background())

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- changeStream.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-streamDone:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancel")
	}
}

func TestChangeStreamDeleteEvent(t *testing.T) {
	memoryStore := NewMemoryStore()
	changeStream := NewChangeStream(memoryStore, "desired/")

	var lastRecord ChangeRecord
	lastRecordMutex := sync.Mutex{}

	changeStream.Subscribe(func(record ChangeRecord) error {
		lastRecordMutex.Lock()
		lastRecord = record
		lastRecordMutex.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	memoryStore.Put(ctx, "desired/service/web/image", []byte("nginx"))

	go changeStream.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	memoryStore.Delete(ctx, "desired/service/web/image")
	time.Sleep(100 * time.Millisecond)

	lastRecordMutex.Lock()
	defer lastRecordMutex.Unlock()

	if lastRecord.Type != EventDelete {
		t.Errorf("expected delete event type, got %d", lastRecord.Type)
	}
}
