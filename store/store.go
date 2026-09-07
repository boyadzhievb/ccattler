package store

import "context"

// Fact represents a single key-value entry in the state store.
// Each fact tracks its current revision and the revision at which it was first created,
// enabling optimistic concurrency control via compare-and-swap transactions.
type Fact struct {
	// Key is the hierarchical path identifying this fact (e.g., "/service/web/image").
	Key string
	// Value is the raw byte payload of the fact.
	Value []byte
	// Revision is the store-global revision at which this fact was last modified.
	Revision int64
	// CreateRevision is the store-global revision at which this fact was first created.
	CreateRevision int64
	// LeaseID is the optional lease binding this fact to a time-to-live expiration.
	LeaseID int64
}

// Event represents a change notification emitted by the store's watch mechanism.
// Watchers receive events whenever facts matching their watch criteria are created,
// modified, or deleted.
type Event struct {
	// Type indicates whether the fact was put (created/updated) or deleted.
	Type EventType
	// Fact is the current state of the fact after the change.
	Fact Fact
	// Prev is the previous state of the fact before the change, or nil for new facts.
	Prev *Fact
}

// EventType distinguishes between put and delete operations in watch events.
type EventType int

const (
	// EventPut indicates a fact was created or updated.
	EventPut EventType = iota
	// EventDelete indicates a fact was removed from the store.
	EventDelete
)

// Compare represents a precondition for a transactional operation.
// A Revision of 0 asserts the key must not exist; any other value asserts the
// key must exist with exactly that revision.
type Compare struct {
	// Key is the fact key to check.
	Key string
	// Revision is the expected revision of the fact. Zero means the key must not exist.
	Revision int64
}

// Op represents a single operation within a transaction's success or failure branch.
type Op struct {
	// Type indicates whether this operation is a put or a delete.
	Type OpType
	// Key is the fact key to operate on.
	Key string
	// Value is the payload to store (only meaningful for OpPut).
	Value []byte
}

// OpType distinguishes between put and delete operations in transactions.
type OpType int

const (
	// OpPut writes a key-value pair into the store.
	OpPut OpType = iota
	// OpDelete removes a key from the store.
	OpDelete
)

// WatchOption configures the behavior of a watch subscription.
type WatchOption struct {
	// Prefix, when true, causes the watch to match all keys sharing the
	// watched key as a prefix rather than requiring an exact key match.
	Prefix bool
}

// StateStore defines the interface for CCattler's fact store. All components in the
// system communicate exclusively through the store -- controllers watch for changes,
// reconcilers read desired and observed state, and the scheduler writes placements.
// Implementations include an in-memory store for testing, SQLite for persistence,
// and etcd for distributed production use.
type StateStore interface {
	// Get retrieves a single fact by its exact key.
	// Returns ErrKeyNotFound if the key does not exist.
	Get(ctx context.Context, key string) (*Fact, error)
	// Put creates or updates the fact at the given key with the provided value,
	// returning the new store-global revision.
	Put(ctx context.Context, key string, value []byte) (int64, error)
	// Delete removes the fact at the given key.
	// Returns ErrKeyNotFound if the key does not exist.
	Delete(ctx context.Context, key string) error

	// Scan returns all facts whose keys begin with the given prefix, sorted by key.
	Scan(ctx context.Context, prefix string) ([]Fact, error)
	// Watch creates a subscription that receives events for changes to the specified
	// key (or key prefix, if opts.Prefix is true). The returned channel is closed
	// when the store is closed.
	Watch(ctx context.Context, key string, opts WatchOption) (<-chan Event, error)

	// Transaction atomically evaluates the compare preconditions and, if all pass,
	// executes the onSuccess operations; otherwise executes the onFailure operations.
	// Returns true if all preconditions were satisfied.
	Transaction(ctx context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error)

	// Revision returns the current store-global revision counter.
	Revision(ctx context.Context) (int64, error)

	// Close shuts down the store, closing all active watch channels and releasing resources.
	Close() error
}
