package api

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

func TestWatchMultiplexerSharesWatchForSamePrefix(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	multiplexer := NewWatchMultiplexer(factStore)

	subscriberContext1, cancelSubscriber1 := context.WithCancel(context.Background())
	defer cancelSubscriber1()
	subscriberContext2, cancelSubscriber2 := context.WithCancel(context.Background())
	defer cancelSubscriber2()

	eventChannel1, subscribeError1 := multiplexer.Subscribe(subscriberContext1, "desired/service/")
	if subscribeError1 != nil {
		t.Fatalf("subscribe 1: %v", subscribeError1)
	}

	eventChannel2, subscribeError2 := multiplexer.Subscribe(subscriberContext2, "desired/service/")
	if subscribeError2 != nil {
		t.Fatalf("subscribe 2: %v", subscribeError2)
	}

	if multiplexer.ActiveWatchCount() != 1 {
		t.Fatalf("expected 1 active watch, got %d", multiplexer.ActiveWatchCount())
	}

	factStore.Put(context.Background(), "desired/service/web/image", []byte("nginx:1.28"))

	receiveWithTimeout := func(eventChannel <-chan store.Event, label string) store.Event {
		select {
		case event := <-eventChannel:
			return event
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no event received within timeout", label)
			return store.Event{}
		}
	}

	receivedEvent1 := receiveWithTimeout(eventChannel1, "subscriber-1")
	receivedEvent2 := receiveWithTimeout(eventChannel2, "subscriber-2")

	if string(receivedEvent1.Fact.Value) != "nginx:1.28" {
		t.Errorf("subscriber-1: expected nginx:1.28, got %s", string(receivedEvent1.Fact.Value))
	}
	if string(receivedEvent2.Fact.Value) != "nginx:1.28" {
		t.Errorf("subscriber-2: expected nginx:1.28, got %s", string(receivedEvent2.Fact.Value))
	}
}

func TestWatchMultiplexerDifferentPrefixesSeparateWatches(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	multiplexer := NewWatchMultiplexer(factStore)

	subscriberContext1, cancelSubscriber1 := context.WithCancel(context.Background())
	defer cancelSubscriber1()
	subscriberContext2, cancelSubscriber2 := context.WithCancel(context.Background())
	defer cancelSubscriber2()

	_, subscribeError1 := multiplexer.Subscribe(subscriberContext1, "desired/service/")
	if subscribeError1 != nil {
		t.Fatalf("subscribe 1: %v", subscribeError1)
	}

	_, subscribeError2 := multiplexer.Subscribe(subscriberContext2, "observed/instance/")
	if subscribeError2 != nil {
		t.Fatalf("subscribe 2: %v", subscribeError2)
	}

	if multiplexer.ActiveWatchCount() != 2 {
		t.Fatalf("expected 2 active watches, got %d", multiplexer.ActiveWatchCount())
	}
}

func TestWatchMultiplexerCleanupOnUnsubscribe(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	multiplexer := NewWatchMultiplexer(factStore)

	subscriberContext1, cancelSubscriber1 := context.WithCancel(context.Background())
	subscriberContext2, cancelSubscriber2 := context.WithCancel(context.Background())
	defer cancelSubscriber2()

	_, subscribeError1 := multiplexer.Subscribe(subscriberContext1, "desired/service/")
	if subscribeError1 != nil {
		t.Fatalf("subscribe 1: %v", subscribeError1)
	}

	_, subscribeError2 := multiplexer.Subscribe(subscriberContext2, "desired/service/")
	if subscribeError2 != nil {
		t.Fatalf("subscribe 2: %v", subscribeError2)
	}

	if multiplexer.ActiveWatchCount() != 1 {
		t.Fatalf("expected 1 active watch before unsubscribe, got %d", multiplexer.ActiveWatchCount())
	}

	cancelSubscriber1()
	time.Sleep(100 * time.Millisecond)

	if multiplexer.ActiveWatchCount() != 1 {
		t.Fatalf("expected 1 active watch after partial unsubscribe, got %d", multiplexer.ActiveWatchCount())
	}

	cancelSubscriber2()
	time.Sleep(100 * time.Millisecond)

	if multiplexer.ActiveWatchCount() != 0 {
		t.Fatalf("expected 0 active watches after full unsubscribe, got %d", multiplexer.ActiveWatchCount())
	}
}

func TestWatchMultiplexerSubscriberContextCancelClosesChannel(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	multiplexer := NewWatchMultiplexer(factStore)

	subscriberContext, cancelSubscriber := context.WithCancel(context.Background())

	eventChannel, subscribeError := multiplexer.Subscribe(subscriberContext, "desired/service/")
	if subscribeError != nil {
		t.Fatalf("subscribe: %v", subscribeError)
	}

	cancelSubscriber()

	select {
	case _, open := <-eventChannel:
		if open {
			t.Fatal("expected channel to be closed after context cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed within timeout")
	}
}
