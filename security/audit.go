package security

import (
	"encoding/json"
	"sync"
	"time"
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
