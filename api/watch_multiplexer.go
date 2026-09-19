// Package api implements the CCattler HTTP API server.
package api

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
)

// WatchMultiplexer shares store watch connections across multiple API
// subscribers watching the same prefix. Instead of each SSE client opening
// its own store.Watch (which maps 1:1 to an etcd watch), the multiplexer
// opens one watch per unique prefix and fans events out to all subscribers.
// This reduces etcd watch load as API replicas scale horizontally.
type WatchMultiplexer struct {
	// factStore is the backing state store for creating watches.
	factStore store.StateStore
	// sharedWatchers maps watch prefix → shared watcher instance.
	sharedWatchers map[string]*sharedPrefixWatcher
	mutex          sync.Mutex
}

// NewWatchMultiplexer creates a multiplexer backed by the given store.
func NewWatchMultiplexer(factStore store.StateStore) *WatchMultiplexer {
	return &WatchMultiplexer{
		factStore:      factStore,
		sharedWatchers: make(map[string]*sharedPrefixWatcher),
	}
}

// Subscribe returns an event channel for the given prefix. If another
// subscriber is already watching the same prefix, events are shared from
// a single underlying store watch. The returned channel is closed when
// the provided context is cancelled. Callers must consume the channel
// or events will be dropped for that subscriber.
func (multiplexer *WatchMultiplexer) Subscribe(ctx context.Context, prefix string) (<-chan store.Event, error) {
	multiplexer.mutex.Lock()

	shared, exists := multiplexer.sharedWatchers[prefix]
	if !exists {
		var startError error
		shared, startError = multiplexer.createSharedWatcher(prefix)
		if startError != nil {
			multiplexer.mutex.Unlock()
			return nil, startError
		}
		multiplexer.sharedWatchers[prefix] = shared
	}

	subscriberChannel := make(chan store.Event, 64)
	subscriptionID := shared.addSubscriber(subscriberChannel)
	multiplexer.mutex.Unlock()

	go func() {
		<-ctx.Done()
		shared.removeSubscriber(subscriptionID)
		close(subscriberChannel)

		multiplexer.mutex.Lock()
		if shared.subscriberCount() == 0 {
			shared.stop()
			delete(multiplexer.sharedWatchers, prefix)
		}
		multiplexer.mutex.Unlock()
	}()

	return subscriberChannel, nil
}

// ActiveWatchCount returns the number of unique prefixes with active shared watches.
func (multiplexer *WatchMultiplexer) ActiveWatchCount() int {
	multiplexer.mutex.Lock()
	defer multiplexer.mutex.Unlock()
	return len(multiplexer.sharedWatchers)
}

// createSharedWatcher opens a store watch for the prefix and starts a
// fan-out goroutine. Must be called with multiplexer.mutex held.
func (multiplexer *WatchMultiplexer) createSharedWatcher(prefix string) (*sharedPrefixWatcher, error) {
	watchContext, cancelWatch := context.WithCancel(context.Background())

	storeChannel, watchError := multiplexer.factStore.Watch(watchContext, prefix, store.WatchOption{Prefix: true})
	if watchError != nil {
		cancelWatch()
		return nil, watchError
	}

	shared := &sharedPrefixWatcher{
		prefix:      prefix,
		subscribers: make(map[int64]chan store.Event),
		cancelWatch: cancelWatch,
	}

	go shared.fanOutEvents(storeChannel)

	return shared, nil
}

// sharedPrefixWatcher holds a single store watch and fans events to multiple
// subscriber channels. When the last subscriber disconnects, the underlying
// store watch is cancelled by the multiplexer.
type sharedPrefixWatcher struct {
	// prefix is the watched key prefix.
	prefix string
	// subscribers maps subscription ID → event channel.
	subscribers map[int64]chan store.Event
	// nextSubscriberID is the monotonically increasing subscription counter.
	nextSubscriberID atomic.Int64
	// cancelWatch cancels the underlying store watch context.
	cancelWatch context.CancelFunc
	mutex       sync.RWMutex
}

// addSubscriber registers a new subscriber channel and returns its ID.
func (shared *sharedPrefixWatcher) addSubscriber(eventChannel chan store.Event) int64 {
	subscriberID := shared.nextSubscriberID.Add(1)
	shared.mutex.Lock()
	shared.subscribers[subscriberID] = eventChannel
	shared.mutex.Unlock()
	return subscriberID
}

// removeSubscriber removes a subscriber by ID.
func (shared *sharedPrefixWatcher) removeSubscriber(subscriberID int64) {
	shared.mutex.Lock()
	delete(shared.subscribers, subscriberID)
	shared.mutex.Unlock()
}

// subscriberCount returns the number of active subscribers.
func (shared *sharedPrefixWatcher) subscriberCount() int {
	shared.mutex.RLock()
	defer shared.mutex.RUnlock()
	return len(shared.subscribers)
}

// stop cancels the underlying store watch.
func (shared *sharedPrefixWatcher) stop() {
	shared.cancelWatch()
}

// fanOutEvents reads from the store watch channel and distributes each event
// to all subscriber channels. If a subscriber's channel is full, the event
// is dropped for that subscriber (it should resync if needed).
func (shared *sharedPrefixWatcher) fanOutEvents(storeChannel <-chan store.Event) {
	for event := range storeChannel {
		shared.mutex.RLock()
		for _, subscriberChannel := range shared.subscribers {
			select {
			case subscriberChannel <- event:
			default:
				logging.Default().Warn("watch subscriber channel full, dropping event",
					"prefix", shared.prefix,
					"key", event.Fact.Key)
			}
		}
		shared.mutex.RUnlock()
	}
}
