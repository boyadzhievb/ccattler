package store

import "context"

type Fact struct {
	Key            string
	Value          []byte
	Revision       int64
	CreateRevision int64
	LeaseID        int64
}

type Event struct {
	Type EventType
	Fact Fact
	Prev *Fact
}

type EventType int

const (
	EventPut EventType = iota
	EventDelete
)

type Compare struct {
	Key      string
	Revision int64
}

type Op struct {
	Type  OpType
	Key   string
	Value []byte
}

type OpType int

const (
	OpPut OpType = iota
	OpDelete
)

type WatchOption struct {
	Prefix bool
}

type StateStore interface {
	Get(ctx context.Context, key string) (*Fact, error)
	Put(ctx context.Context, key string, value []byte) (int64, error)
	Delete(ctx context.Context, key string) error

	Scan(ctx context.Context, prefix string) ([]Fact, error)
	Watch(ctx context.Context, key string, opts WatchOption) (<-chan Event, error)

	Transaction(ctx context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error)

	Revision(ctx context.Context) (int64, error)

	Close() error
}
