package security

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// AuditEntry records a single authorization decision.
type AuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Principal string    `json:"principal"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Decision  string    `json:"decision"`
	Policy    string    `json:"policy,omitempty"`
}

// AuditLogger is the interface for recording authorization decisions.
type AuditLogger interface {
	// Log records a single audit entry.
	Log(entry AuditEntry)

	// Entries returns all recorded entries. The returned slice is a copy.
	Entries() []AuditEntry
}

// InMemoryAuditLog stores audit entries in memory. Suitable for testing and
// single-node clusters with bounded retention.
type InMemoryAuditLog struct {
	entries    []AuditEntry
	maxEntries int
	mutex      sync.Mutex
}

// NewInMemoryAuditLog creates an audit log that retains up to maxEntries.
// When the limit is reached, the oldest entries are discarded.
func NewInMemoryAuditLog(maxEntries int) *InMemoryAuditLog {
	return &InMemoryAuditLog{
		entries:    make([]AuditEntry, 0, maxEntries),
		maxEntries: maxEntries,
	}
}

// Log appends an entry to the audit log with the current timestamp.
func (auditLog *InMemoryAuditLog) Log(entry AuditEntry) {
	auditLog.mutex.Lock()
	defer auditLog.mutex.Unlock()

	entry.Timestamp = time.Now()

	if len(auditLog.entries) >= auditLog.maxEntries {
		copy(auditLog.entries, auditLog.entries[1:])
		auditLog.entries = auditLog.entries[:len(auditLog.entries)-1]
	}
	auditLog.entries = append(auditLog.entries, entry)
}

// Entries returns a copy of all audit entries.
func (auditLog *InMemoryAuditLog) Entries() []AuditEntry {
	auditLog.mutex.Lock()
	defer auditLog.mutex.Unlock()

	entriesCopy := make([]AuditEntry, len(auditLog.entries))
	copy(entriesCopy, auditLog.entries)
	return entriesCopy
}

// MarshalJSON serializes all audit entries as a JSON array.
func (auditLog *InMemoryAuditLog) MarshalJSON() ([]byte, error) {
	return json.Marshal(auditLog.Entries())
}

// StoreBackedAuditLog persists audit entries to the fact store under the
// audit/ key prefix. Each entry is written as a JSON-encoded value with a
// monotonically increasing sequence number in the key. Writes happen in a
// background goroutine to avoid blocking authorization decisions.
type StoreBackedAuditLog struct {
	stateStore  store.StateStore // backing store for persisted entries
	sequenceNum atomic.Int64     // monotonic counter for unique keys
	entries     []AuditEntry     // in-memory copy for Entries() queries
	mutex       sync.Mutex       // protects entries slice
	maxEntries  int              // maximum in-memory entries to retain
}

// NewStoreBackedAuditLog creates an audit log that writes entries to both
// an in-memory buffer and the fact store under the audit/ prefix.
func NewStoreBackedAuditLog(stateStore store.StateStore, maxEntries int) *StoreBackedAuditLog {
	return &StoreBackedAuditLog{
		stateStore: stateStore,
		entries:    make([]AuditEntry, 0, maxEntries),
		maxEntries: maxEntries,
	}
}

// Log records an audit entry to the in-memory buffer and persists it to the
// store asynchronously.
func (auditLog *StoreBackedAuditLog) Log(entry AuditEntry) {
	entry.Timestamp = time.Now()

	auditLog.mutex.Lock()
	if len(auditLog.entries) >= auditLog.maxEntries {
		copy(auditLog.entries, auditLog.entries[1:])
		auditLog.entries = auditLog.entries[:len(auditLog.entries)-1]
	}
	auditLog.entries = append(auditLog.entries, entry)
	auditLog.mutex.Unlock()

	go auditLog.persistEntry(entry)
}

// Entries returns a copy of all in-memory audit entries.
func (auditLog *StoreBackedAuditLog) Entries() []AuditEntry {
	auditLog.mutex.Lock()
	defer auditLog.mutex.Unlock()
	entriesCopy := make([]AuditEntry, len(auditLog.entries))
	copy(entriesCopy, auditLog.entries)
	return entriesCopy
}

// persistEntry writes a single audit entry to the store under
// audit/{sequence} with JSON-encoded value.
func (auditLog *StoreBackedAuditLog) persistEntry(entry AuditEntry) {
	sequenceNumber := auditLog.sequenceNum.Add(1)
	key := fmt.Sprintf("%s/%020d", types.PrefixAudit, sequenceNumber)
	entryJSON, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		return
	}
	auditLog.stateStore.Put(context.Background(), key, entryJSON)
}
