package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

var ErrKeyNotFound = errors.New("key not found")

type watcher struct {
	key    string
	prefix bool
	ch     chan Event
}

type MemoryStore struct {
	mu       sync.RWMutex
	data     map[string]*Fact
	revision int64
	watchers []watcher
	closed   bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		data:     make(map[string]*Fact),
		revision: 0,
	}
}

func (m *MemoryStore) Get(_ context.Context, key string) (*Fact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	f, ok := m.data[key]
	if !ok {
		return nil, ErrKeyNotFound
	}
	cp := *f
	return &cp, nil
}

func (m *MemoryStore) Put(_ context.Context, key string, value []byte) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.revision++
	rev := m.revision

	var prev *Fact
	if existing, ok := m.data[key]; ok {
		cp := *existing
		prev = &cp
	}

	createRev := rev
	if prev != nil {
		createRev = prev.CreateRevision
	}

	val := make([]byte, len(value))
	copy(val, value)

	fact := &Fact{
		Key:            key,
		Value:          val,
		Revision:       rev,
		CreateRevision: createRev,
	}
	m.data[key] = fact

	cp := *fact
	m.notify(Event{Type: EventPut, Fact: cp, Prev: prev})

	return rev, nil
}

func (m *MemoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.data[key]
	if !ok {
		return ErrKeyNotFound
	}

	m.revision++
	prev := *existing
	delete(m.data, key)

	deleted := Fact{
		Key:      key,
		Revision: m.revision,
	}
	m.notify(Event{Type: EventDelete, Fact: deleted, Prev: &prev})

	return nil
}

func (m *MemoryStore) Scan(_ context.Context, prefix string) ([]Fact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []Fact
	for k, f := range m.data {
		if strings.HasPrefix(k, prefix) {
			results = append(results, *f)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Key < results[j].Key
	})
	return results, nil
}

func (m *MemoryStore) Watch(_ context.Context, key string, opts WatchOption) (<-chan Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan Event, 64)
	m.watchers = append(m.watchers, watcher{
		key:    key,
		prefix: opts.Prefix,
		ch:     ch,
	})
	return ch, nil
}

func (m *MemoryStore) Transaction(_ context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ok := true
	for _, c := range compares {
		f, exists := m.data[c.Key]
		if c.Revision == 0 {
			if exists {
				ok = false
				break
			}
		} else {
			if !exists || f.Revision != c.Revision {
				ok = false
				break
			}
		}
	}

	ops := onSuccess
	if !ok {
		ops = onFailure
	}

	for _, op := range ops {
		switch op.Type {
		case OpPut:
			m.revision++
			rev := m.revision

			var prev *Fact
			if existing, exists := m.data[op.Key]; exists {
				cp := *existing
				prev = &cp
			}

			createRev := rev
			if prev != nil {
				createRev = prev.CreateRevision
			}

			val := make([]byte, len(op.Value))
			copy(val, op.Value)

			fact := &Fact{
				Key:            op.Key,
				Value:          val,
				Revision:       rev,
				CreateRevision: createRev,
			}
			m.data[op.Key] = fact

			cp := *fact
			m.notify(Event{Type: EventPut, Fact: cp, Prev: prev})

		case OpDelete:
			if existing, exists := m.data[op.Key]; exists {
				m.revision++
				prev := *existing
				delete(m.data, op.Key)
				deleted := Fact{Key: op.Key, Revision: m.revision}
				m.notify(Event{Type: EventDelete, Fact: deleted, Prev: &prev})
			}
		}
	}

	return ok, nil
}

func (m *MemoryStore) Revision(_ context.Context) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.revision, nil
}

func (m *MemoryStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true
	for _, w := range m.watchers {
		close(w.ch)
	}
	m.watchers = nil
	return nil
}

// notify sends an event to matching watchers. Must be called with mu held.
func (m *MemoryStore) notify(e Event) {
	for _, w := range m.watchers {
		if w.prefix {
			if !strings.HasPrefix(e.Fact.Key, w.key) {
				continue
			}
		} else {
			if e.Fact.Key != w.key {
				continue
			}
		}
		select {
		case w.ch <- e:
		default:
		}
	}
}
