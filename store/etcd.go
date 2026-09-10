package store

import (
	"bytes"
	"context"
	"log"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// EtcdStore is a distributed implementation of StateStore backed by an etcd cluster.
// It provides strongly consistent reads, prefix-based watches, optimistic-concurrency
// transactions, and global revision tracking — all mapped from the StateStore interface
// onto the etcd v3 client API. The optional keyPrefix is prepended to every key so
// multiple CCattler clusters can share a single etcd instance.
type EtcdStore struct {
	// etcdClient is the underlying etcd v3 client used for all operations.
	etcdClient *clientv3.Client
	// keyPrefix is prepended to all keys to namespace this store within a shared etcd cluster.
	keyPrefix string
	// isClosed tracks whether Close has been called, preventing operations on a closed store.
	isClosed bool
	// closeMutex guards the isClosed flag for concurrent access.
	closeMutex sync.RWMutex
}

// EtcdStoreConfig holds the configuration required to create a new EtcdStore connection.
type EtcdStoreConfig struct {
	// Endpoints is the list of etcd server addresses to connect to (e.g., ["localhost:2379"]).
	Endpoints []string
	// KeyPrefix is the optional string prepended to all keys to namespace the store.
	KeyPrefix string
	// DialTimeout is the maximum time to wait when establishing the initial connection.
	DialTimeout time.Duration
	// Username is the optional etcd authentication username.
	Username string
	// Password is the optional etcd authentication password.
	Password string
}

// NewEtcdStore creates a new EtcdStore connected to the specified etcd cluster. The
// keyPrefix is prepended to every key operation, allowing multiple CCattler instances
// to coexist within a single etcd cluster.
func NewEtcdStore(config EtcdStoreConfig) (*EtcdStore, error) {
	dialTimeout := config.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second
	}

	etcdClient, connectionError := clientv3.New(clientv3.Config{
		Endpoints:   config.Endpoints,
		DialTimeout: dialTimeout,
		Username:    config.Username,
		Password:    config.Password,
	})
	if connectionError != nil {
		return nil, connectionError
	}

	return &EtcdStore{
		etcdClient: etcdClient,
		keyPrefix:  config.KeyPrefix,
	}, nil
}

// NewEtcdStoreFromClient creates a new EtcdStore wrapping an existing etcd client.
// This is primarily useful for testing with embedded etcd servers.
func NewEtcdStoreFromClient(etcdClient *clientv3.Client, keyPrefix string) *EtcdStore {
	return &EtcdStore{
		etcdClient: etcdClient,
		keyPrefix:  keyPrefix,
	}
}

// prefixedKey returns the key with the store's namespace prefix prepended.
func (etcdStore *EtcdStore) prefixedKey(key string) string {
	return etcdStore.keyPrefix + key
}

// unprefixedKey strips the store's namespace prefix from a key returned by etcd.
func (etcdStore *EtcdStore) unprefixedKey(prefixedKey string) string {
	if len(etcdStore.keyPrefix) > 0 && len(prefixedKey) >= len(etcdStore.keyPrefix) {
		return prefixedKey[len(etcdStore.keyPrefix):]
	}
	return prefixedKey
}

// checkClosed returns ErrStoreClosed if the store has been shut down.
func (etcdStore *EtcdStore) checkClosed() error {
	etcdStore.closeMutex.RLock()
	defer etcdStore.closeMutex.RUnlock()
	if etcdStore.isClosed {
		return ErrStoreClosed
	}
	return nil
}

// Get retrieves a single fact by its exact key from etcd. Returns ErrKeyNotFound if
// the key does not exist. The returned fact includes revision metadata from etcd's
// MVCC storage.
func (etcdStore *EtcdStore) Get(ctx context.Context, key string) (*Fact, error) {
	if closedError := etcdStore.checkClosed(); closedError != nil {
		return nil, closedError
	}

	getResponse, getError := etcdStore.etcdClient.Get(ctx, etcdStore.prefixedKey(key))
	if getError != nil {
		return nil, getError
	}

	if len(getResponse.Kvs) == 0 {
		return nil, ErrKeyNotFound
	}

	keyValue := getResponse.Kvs[0]
	return &Fact{
		Key:            etcdStore.unprefixedKey(string(keyValue.Key)),
		Value:          keyValue.Value,
		Revision:       keyValue.ModRevision,
		CreateRevision: keyValue.CreateRevision,
		LeaseID:        int64(keyValue.Lease),
	}, nil
}

// Put creates or updates the fact at the given key. For idempotency, it first reads
// the current value and skips the write if the value is unchanged — matching the
// MemoryStore behavior that prevents unnecessary revision bumps and watch events.
// Returns the store-global revision after the operation.
func (etcdStore *EtcdStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	if closedError := etcdStore.checkClosed(); closedError != nil {
		return 0, closedError
	}

	prefixedKey := etcdStore.prefixedKey(key)

	// Idempotent check: read current value and skip if unchanged.
	getResponse, getError := etcdStore.etcdClient.Get(ctx, prefixedKey)
	if getError != nil {
		return 0, getError
	}
	if len(getResponse.Kvs) > 0 && bytes.Equal(getResponse.Kvs[0].Value, value) {
		return getResponse.Kvs[0].ModRevision, nil
	}

	// Use a transaction to guard against concurrent writes between the Get and Put.
	// If the key's ModRevision changed since we read it, the transaction fails and
	// we fall back to an unconditional Put (the value we're writing is still correct).
	if len(getResponse.Kvs) > 0 {
		txnResponse, txnError := etcdStore.etcdClient.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(prefixedKey), "=", getResponse.Kvs[0].ModRevision)).
			Then(clientv3.OpPut(prefixedKey, string(value))).
			Commit()
		if txnError != nil {
			return 0, txnError
		}
		if txnResponse.Succeeded {
			return txnResponse.Header.Revision, nil
		}
	}

	putResponse, putError := etcdStore.etcdClient.Put(ctx, prefixedKey, string(value))
	if putError != nil {
		return 0, putError
	}

	return putResponse.Header.Revision, nil
}

// Delete removes the fact at the given key from etcd. Returns ErrKeyNotFound if no
// key was deleted (i.e., the key did not exist).
func (etcdStore *EtcdStore) Delete(ctx context.Context, key string) error {
	if closedError := etcdStore.checkClosed(); closedError != nil {
		return closedError
	}

	deleteResponse, deleteError := etcdStore.etcdClient.Delete(ctx, etcdStore.prefixedKey(key))
	if deleteError != nil {
		return deleteError
	}

	if deleteResponse.Deleted == 0 {
		return ErrKeyNotFound
	}

	return nil
}

// Scan returns all facts whose keys begin with the given prefix, sorted by key.
// Maps directly to etcd's range query with prefix option and ascending sort.
func (etcdStore *EtcdStore) Scan(ctx context.Context, prefix string) ([]Fact, error) {
	if closedError := etcdStore.checkClosed(); closedError != nil {
		return nil, closedError
	}

	getResponse, getError := etcdStore.etcdClient.Get(ctx, etcdStore.prefixedKey(prefix),
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
	)
	if getError != nil {
		return nil, getError
	}

	matchingFacts := make([]Fact, 0, len(getResponse.Kvs))
	for _, keyValue := range getResponse.Kvs {
		matchingFacts = append(matchingFacts, Fact{
			Key:            etcdStore.unprefixedKey(string(keyValue.Key)),
			Value:          keyValue.Value,
			Revision:       keyValue.ModRevision,
			CreateRevision: keyValue.CreateRevision,
			LeaseID:        int64(keyValue.Lease),
		})
	}

	return matchingFacts, nil
}

// ScanWithRevision returns all facts matching the prefix along with the
// cluster-global revision from the etcd response header, guaranteeing the
// revision is consistent with the returned facts.
func (etcdStore *EtcdStore) ScanWithRevision(ctx context.Context, prefix string) (*ScanResult, error) {
	if closedError := etcdStore.checkClosed(); closedError != nil {
		return nil, closedError
	}

	getResponse, getError := etcdStore.etcdClient.Get(ctx, etcdStore.prefixedKey(prefix),
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
	)
	if getError != nil {
		return nil, getError
	}

	matchingFacts := make([]Fact, 0, len(getResponse.Kvs))
	for _, keyValue := range getResponse.Kvs {
		matchingFacts = append(matchingFacts, Fact{
			Key:            etcdStore.unprefixedKey(string(keyValue.Key)),
			Value:          keyValue.Value,
			Revision:       keyValue.ModRevision,
			CreateRevision: keyValue.CreateRevision,
			LeaseID:        int64(keyValue.Lease),
		})
	}

	return &ScanResult{
		Facts:    matchingFacts,
		Revision: getResponse.Header.Revision,
	}, nil
}

// Watch creates a subscription that receives events for changes to the specified key
// (or key prefix, if opts.Prefix is true). Events from etcd's watch stream are
// translated to CCattler Event types and forwarded to the returned channel. The
// channel is closed when the context is cancelled or the store is closed.
func (etcdStore *EtcdStore) Watch(ctx context.Context, key string, opts WatchOption) (<-chan Event, error) {
	if closedError := etcdStore.checkClosed(); closedError != nil {
		return nil, closedError
	}

	eventChannel := make(chan Event, 64)

	var watchOptions []clientv3.OpOption
	if opts.Prefix {
		watchOptions = append(watchOptions, clientv3.WithPrefix())
	}

	etcdWatchChannel := etcdStore.etcdClient.Watch(ctx, etcdStore.prefixedKey(key), watchOptions...)

	go etcdStore.forwardEtcdWatchEvents(etcdWatchChannel, eventChannel)

	return eventChannel, nil
}

// forwardEtcdWatchEvents reads from the etcd watch channel and translates each event
// into a CCattler Event, forwarding it to the output channel. The output channel is
// closed when the etcd watch channel closes (e.g., on context cancellation or store close).
func (etcdStore *EtcdStore) forwardEtcdWatchEvents(etcdWatchChannel clientv3.WatchChan, outputChannel chan Event) {
	defer close(outputChannel)
	eventsDropped := false

	for watchResponse := range etcdWatchChannel {
		if watchResponse.Canceled {
			return
		}

		for _, etcdEvent := range watchResponse.Events {
			var eventType EventType
			if etcdEvent.IsCreate() || etcdEvent.IsModify() {
				eventType = EventPut
			} else {
				eventType = EventDelete
			}

			var currentFact Fact
			if etcdEvent.Kv != nil {
				currentFact = Fact{
					Key:            etcdStore.unprefixedKey(string(etcdEvent.Kv.Key)),
					Value:          etcdEvent.Kv.Value,
					Revision:       etcdEvent.Kv.ModRevision,
					CreateRevision: etcdEvent.Kv.CreateRevision,
					LeaseID:        int64(etcdEvent.Kv.Lease),
				}
			}

			var previousFact *Fact
			if etcdEvent.PrevKv != nil {
				previousFact = &Fact{
					Key:            etcdStore.unprefixedKey(string(etcdEvent.PrevKv.Key)),
					Value:          etcdEvent.PrevKv.Value,
					Revision:       etcdEvent.PrevKv.ModRevision,
					CreateRevision: etcdEvent.PrevKv.CreateRevision,
					LeaseID:        int64(etcdEvent.PrevKv.Lease),
				}
			}

			event := Event{
				Type: eventType,
				Fact: currentFact,
				Prev: previousFact,
			}

			if eventsDropped {
				select {
				case outputChannel <- Event{Type: EventOverflow}:
					eventsDropped = false
				default:
				}
			}

			select {
			case outputChannel <- event:
			default:
				if !eventsDropped {
					log.Printf("WARNING: etcd watch event dropped for key %s (channel buffer full)", currentFact.Key)
				}
				eventsDropped = true
			}
		}
	}
}

// Transaction atomically evaluates the compare preconditions and executes the
// corresponding operations. CCattler's Compare with Revision == 0 maps to an etcd
// CreateRevision == 0 check (key must not exist). Any other revision maps to a
// ModRevision equality check. Returns true if all preconditions were satisfied.
func (etcdStore *EtcdStore) Transaction(ctx context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error) {
	if closedError := etcdStore.checkClosed(); closedError != nil {
		return false, closedError
	}

	etcdCompares := make([]clientv3.Cmp, 0, len(compares))
	for _, comparison := range compares {
		prefixedKey := etcdStore.prefixedKey(comparison.Key)
		if comparison.Revision == 0 {
			etcdCompares = append(etcdCompares, clientv3.Compare(clientv3.CreateRevision(prefixedKey), "=", 0))
		} else {
			etcdCompares = append(etcdCompares, clientv3.Compare(clientv3.ModRevision(prefixedKey), "=", comparison.Revision))
		}
	}

	etcdSuccessOps := etcdStore.translateOperationsToEtcdOps(onSuccess)
	etcdFailureOps := etcdStore.translateOperationsToEtcdOps(onFailure)

	transactionResponse, transactionError := etcdStore.etcdClient.Txn(ctx).
		If(etcdCompares...).
		Then(etcdSuccessOps...).
		Else(etcdFailureOps...).
		Commit()

	if transactionError != nil {
		return false, transactionError
	}

	return transactionResponse.Succeeded, nil
}

// translateOperationsToEtcdOps converts CCattler transaction operations into etcd Op types.
func (etcdStore *EtcdStore) translateOperationsToEtcdOps(operations []Op) []clientv3.Op {
	etcdOps := make([]clientv3.Op, 0, len(operations))
	for _, operation := range operations {
		switch operation.Type {
		case OpPut:
			etcdOps = append(etcdOps, clientv3.OpPut(etcdStore.prefixedKey(operation.Key), string(operation.Value)))
		case OpDelete:
			etcdOps = append(etcdOps, clientv3.OpDelete(etcdStore.prefixedKey(operation.Key)))
		}
	}
	return etcdOps
}

// Revision returns the current store-global revision from etcd. This is obtained
// from the response header of a lightweight status query, which gives the cluster's
// current revision without transferring key data.
func (etcdStore *EtcdStore) Revision(ctx context.Context) (int64, error) {
	if closedError := etcdStore.checkClosed(); closedError != nil {
		return 0, closedError
	}

	// Use a Get with limit 0 to obtain the cluster revision from the response header
	// without transferring any key-value data.
	getResponse, getError := etcdStore.etcdClient.Get(ctx, etcdStore.prefixedKey(""),
		clientv3.WithPrefix(),
		clientv3.WithLimit(1),
		clientv3.WithKeysOnly(),
	)
	if getError != nil {
		return 0, getError
	}

	return getResponse.Header.Revision, nil
}

// Close shuts down the etcd client connection and marks the store as closed.
// Subsequent operations will return ErrStoreClosed.
func (etcdStore *EtcdStore) Close() error {
	etcdStore.closeMutex.Lock()
	defer etcdStore.closeMutex.Unlock()

	if etcdStore.isClosed {
		return nil
	}
	etcdStore.isClosed = true

	return etcdStore.etcdClient.Close()
}
