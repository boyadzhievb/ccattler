# CCattler Audit & Event Projection Graph

Machine-readable reference for the audit logging and event projection pipeline.
Purpose: trace how authorization decisions and cluster state transitions become
queryable audit history and event streams without reading source files.

## System Overview

CCattler has two parallel audit subsystems that share the fact store but serve
different purposes:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          AUDIT PIPELINE OVERVIEW                            │
│                                                                             │
│  ┌─────────────────┐    ┌──────────────────┐    ┌───────────────────────┐  │
│  │  Authorization   │    │  Store Watches    │    │  Controller Changes   │  │
│  │  Decisions       │    │  (committed state)│    │  (state transitions)  │  │
│  └────────┬─────────┘    └────────┬──────────┘    └───────────┬───────────┘  │
│           │                       │                            │              │
│           ▼                       ▼                            │              │
│  ┌────────────────────┐  ┌────────────────────┐               │              │
│  │  StoreBackedAudit  │  │  EventProjector    │               │              │
│  │  Log               │  │  (watch-based)     │◄──────────────┘              │
│  │                    │  │                    │                               │
│  │  security/audit.go │  │  controllers/      │                              │
│  │                    │  │  event_projector.go │                              │
│  └────────┬───────────┘  └────────┬───────────┘                              │
│           │                       │                                           │
│           ▼                       ▼                                           │
│  ┌────────────────────┐  ┌────────────────────┐                              │
│  │  audit/{seq}       │  │  event/{id}        │                              │
│  │  (fact store)      │  │  (fact store)      │                              │
│  └────────┬───────────┘  └────────┬───────────┘                              │
│           │                       │                                           │
│           ▼                       ▼                                           │
│  ┌────────────────────┐  ┌────────────────────┐                              │
│  │  AuditLogGC        │  │  EventLog.trim     │                              │
│  │  Controller        │  │  OldEvents()       │                              │
│  │  (7-day retention) │  │  (count-based)     │                              │
│  └────────────────────┘  └────────────────────┘                              │
│                                                                              │
│  ┌───────────────────────────────────────────────────────────────────────┐   │
│  │                       QUERY LAYER                                     │   │
│  │                                                                       │   │
│  │  CLI: cca audit [--principal] [--action] [--since] [--limit] [-f]     │   │
│  │  API: GET /api/audit?principal=&action=&since=&limit=&follow=true     │   │
│  │  API: GET /api/events?kind=&target=&limit=&since=                     │   │
│  └───────────────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────────────┘
```

---

## Subsystem 1: Authorization Audit Log

Records every RBAC authorization decision (allow/deny) on store operations.

### Data Flow

```
API Request
    │
    ▼
AuthorizedStore.Get/Put/Delete/Scan/Watch/Transaction
    │
    ├─ Authorize(principal, permission, key) ──► allow/deny
    │
    ▼
logAllowed() / logDenied()                   (security/authorized_store.go:207,220)
    │
    ▼
StoreBackedAuditLog.Log(AuditEntry)          (security/audit.go:124)
    │
    ├─ Append to in-memory ring buffer (mutex-protected)
    │
    └─ go persistEntry(entry)                (security/audit.go:149)
         │
         ▼
       sequenceNum.Add(1) → monotonic counter
         │
         ▼
       store.Put("audit/{020d seq}", JSON)   (security/audit.go:151)
```

### AuditEntry Schema

```go
type AuditEntry struct {                     // security/audit.go:32
    Timestamp  time.Time  // set at Log() time, not caller time
    Principal  string     // sanitized (newlines escaped, secrets redacted)
    Action     string     // "get", "put", "delete", "scan", "watch", "transaction"
    Target     string     // fact key or prefix (secrets redacted to /secret/[REDACTED])
    Decision   string     // "allow" or "deny"
    AuthMethod string     // "bearer", "mtls", "oidc", "local-token", "webhook"
    Policy     string     // policy that granted/denied (optional)
}
```

### Store Key Format

```
audit/00000000000000000001  →  JSON(AuditEntry)
audit/00000000000000000002  →  JSON(AuditEntry)
...
audit/{zero-padded 20-digit sequence}
```

Key properties:
- 20-digit zero-padded sequence ensures lexicographic = chronological order
- Monotonic counter resumes from highest existing key on restart (security/audit.go:164)
- Persistence is async (goroutine per entry) — audit never blocks auth decisions

### Implementations

| Type | File | Backing | Use Case |
|------|------|---------|----------|
| `InMemoryAuditLog` | security/audit.go:53 | `[]AuditEntry` ring buffer | Tests, single-node |
| `StoreBackedAuditLog` | security/audit.go:101 | etcd + in-memory buffer | Production |

Both implement `AuditLogger` interface (security/audit.go:43):
```go
type AuditLogger interface {
    Log(entry AuditEntry)
    Entries() []AuditEntry
}
```

### Wiring (server startup)

```
command_server.go:356  initializeAuthorizationLayer(factStore)
    │
    ├─ NewRBACAuthorizer(factStore)
    ├─ NewStoreBackedAuditLog(factStore, 1000)       ← DefaultEventLogMaxEvents
    └─ NewAuthorizedStore(factStore, rbacAuthorizer, auditLog)
         │
         └─ Every Get/Put/Delete/Scan/Watch/Transaction → logAllowed/logDenied
```

### Input Sanitization

```
authorized_store.go:21  sanitizeForAudit(input)
    │
    ├─ Escape \n → \\n, \r → \\r, \t → \\t    (log injection prevention)
    └─ Regex: /secret/.* → /secret/[REDACTED]   (sensitive data redaction)
```

---

## Subsystem 2: Event Projection (System Events)

Watches committed state transitions and emits semantic events.

### Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                     EventProjector.Run()                         │
│                     controllers/event_projector.go:49             │
│                                                                  │
│   ┌──────────────────────────────────────────────────────────┐   │
│   │  Watch goroutines (one per prefix, auto-reconnect)       │   │
│   │                                                          │   │
│   │  1. observed/instance/   (state transitions, probes)     │   │
│   │  2. derived/instance/    (node_failure, controller_stop) │   │
│   │  3. observed/node/       (node state changes)            │   │
│   │  4. placement/instance/  (scheduling decisions)          │   │
│   │  5. effective/service/   (scaling events)                │   │
│   └──────────────┬───────────────────────────────────────────┘   │
│                  │                                                │
│                  ▼                                                │
│   ┌──────────────────────────────────┐                           │
│   │  mergedChannel (buffered, 64)    │                           │
│   └──────────────┬───────────────────┘                           │
│                  │                                                │
│                  ▼                                                │
│   ┌──────────────────────────────────┐                           │
│   │  classifyWatchEventAsSemanticEvent()                         │
│   │  event_projector.go:226          │                           │
│   │                                  │                           │
│   │  Filters: only emits on actual   │                           │
│   │  value changes (prev != current) │                           │
│   └──────────────┬───────────────────┘                           │
│                  │                                                │
│                  ▼                                                │
│   ┌──────────────────────────────────┐                           │
│   │  enrichInstanceEventWithService  │                           │
│   │  Name()  (lookup observed/       │                           │
│   │  instance/{id}/service)          │                           │
│   │  event_projector.go:189          │                           │
│   └──────────────┬───────────────────┘                           │
│                  │                                                │
│                  ▼                                                │
│   ┌──────────────────────────────────┐                           │
│   │  EventLog.Emit(kind, target,     │                           │
│   │    detail, "event-projector")    │                           │
│   │  types/eventlog.go:64            │                           │
│   └──────────────────────────────────┘                           │
└──────────────────────────────────────────────────────────────────┘
```

### Watch Prefixes → Event Kinds

| Watch Prefix | Fact Suffix/Pattern | New Value | Event Kind | Target |
|---|---|---|---|---|
| `observed/instance/` | `{id}/state` | `running` | `instance.running` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/state` | `failed` | `instance.failed` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/state` | `pending` (prev="") | `instance.created` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/state` | `pending` (prev!="") | `instance.pending` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/state` | `starting` | `instance.starting` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/state` | `stopped` | `instance.stopped` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/probe/{type}` | `startup_failed` | `probe.failed` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/probe/{type}` | `startup_succeeded` | `probe.succeeded` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/probe/{type}` | `liveness_unhealthy` | `probe.unhealthy` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/probe/{type}` | `readiness_not_ready` | `probe.not_ready` | `instance/{svc}-{id}` |
| `observed/instance/` | `{id}/probe/{type}` | healthy (prev=unhealthy) | `probe.recovered` | `instance/{svc}-{id}` |
| `derived/instance/` | `{id}/node_failure` | any | `instance.failed` | `instance/{id}` |
| `derived/instance/` | `{id}/controller_stopped` | any | `instance.stopped` | `instance/{id}` |
| `placement/instance/` | `{id}` | PUT | `instance.placed` | `instance/{id}` |
| `observed/node/` | `{nodeID}/state` | any | `node.{state}` | `node/{nodeID}` |
| `effective/service/` | `{svc}/instances` | any | `service.scaled` | `service/{svc}` |

### SystemEvent Schema

```go
type SystemEvent struct {                    // types/eventlog.go:22
    ID        string     // "{unixnano}-{seq04d}-{kind}" — globally unique, time-ordered
    Timestamp time.Time  // set at Emit time
    Kind      string     // e.g. "instance.running", "node.alive", "service.scaled"
    Target    string     // e.g. "instance/web-abc123", "node/node-1", "service/web"
    Detail    string     // human-readable description
    Source    string     // always "event-projector" for projected events
}
```

### Event Store Key Format

```
event/{unixnano}-{0001}-{kind}  →  JSON(SystemEvent)
```

Key properties:
- Nanosecond timestamp + 4-digit sequence = unique even under burst
- Lexicographic order = chronological order
- atomic.Uint64 sequence counter (types/eventlog.go:49)

### EventLog Query Methods

| Method | File:Line | Parameters | Behavior |
|---|---|---|---|
| `Query` | types/eventlog.go:98 | `kind string, limit int` | Filter by kind, "" = all kinds. Returns newest `limit`. |
| `Since` | types/eventlog.go:125 | `after time.Time, limit int` | Events after timestamp, newest `limit`. |
| `ForTarget` | types/eventlog.go:150 | `target string, limit int` | Substring match on target field. |
| `Count` | types/eventlog.go:175 | none | Total event count (full scan). |
| `Emit` | types/eventlog.go:64 | `kind, target, detail, source` | Create + persist + trim. Returns `*SystemEvent`. |

### Event Enrichment

Instance events are enriched with service names (event_projector.go:189):

```
Raw target:     instance/eb7509a28e
Lookup:         observed/instance/eb7509a28e/service → "web"
Enriched:       instance/web-eb7509a28e
```

Non-instance events pass through unchanged.

---

## Subsystem 3: Audit Log Garbage Collection

### Controller: audit-log-gc

```
controllers/audit_gc.go
    │
    Watch: audit/                            (:59)
    │
    Reconcile:                               (:66)
    │
    ├─ For each fact under audit/:
    │   └─ Parse JSON → AuditEntry
    │   └─ If now - timestamp > RetentionPeriod → candidate
    │
    ├─ Sort candidates by key (oldest first)
    ├─ Cap at MaxDeletionsPerCycle
    │
    └─ Return []Change{Delete} for each expired entry
```

### Event Trimming (count-based)

```
types/eventlog.go:184  trimOldEvents()
    │
    ├─ Called on every Emit()
    ├─ Scan all event/ facts
    ├─ If count > maxEvents → delete oldest (count - maxEvents) entries
    └─ Deletion by individual store.Delete calls
```

---

## Constants

| Constant | Value | File:Line | Purpose |
|---|---|---|---|
| `DefaultAuditQueryLimit` | 100 | security/audit.go:29 | Max entries per CLI/API query |
| `DefaultEventLogMaxEvents` | 1000 | types/defaults.go:22 | Max events before trimming |
| `defaultAuditRetentionPeriod` | 7 days | controllers/audit_gc.go:22 | Audit entry TTL |
| `defaultMaxAuditGCDeletionsPerCycle` | 50 | controllers/audit_gc.go:26 | GC batch size per reconcile |
| `defaultMergedEventBuffer` | 64 | controllers/event_projector.go:20 | Watch merge channel capacity |
| `watchReconnectBackoff` | 2s | controllers/event_projector.go:43 | Backoff after watch close |

---

## Fact Store Key Prefixes

| Prefix | Type | Purpose |
|---|---|---|
| `audit/` | `PrefixAudit` (types/keys.go:63) | Persistent authorization audit entries |
| `event/` | `PrefixEvent` (types/keys.go:46) | Append-only system event history |

---

## CLI: `cca audit`

```
cmd/cca/command_audit.go:33  executeAuditCommand(args)

Usage:
  cca audit [--principal <p>] [--action <a>] [--since <time>] [--limit N] [--follow]

Flags:
  --principal   Filter by principal identity
  --action      Filter by action (get, put, delete, scan, watch, transaction)
  --since       RFC3339 timestamp or duration (1h, 24h, 7d)
  --limit       Max entries (default: 100)
  --follow, -f  Stream new entries via SSE
```

### Query mode (command_audit.go:128)

```
GET /api/audit?principal=&action=&since=&limit=&denied=true
    │
    ▼
api/server.go:1221  handleAudit()
    │
    ├─ Scan audit/ prefix from store
    ├─ Parse each JSON → AuditEntry
    ├─ Apply filters: principal, action, since, denied-only, tenant scope
    └─ Return JSON array
```

### Follow mode (command_audit.go:159)

```
GET /api/audit?follow=true&...
    │
    ▼
api/server.go:1287  handleAuditFollow()
    │
    ├─ Watch audit/ prefix via store.Watch
    ├─ For each new entry:
    │   ├─ Parse JSON → AuditEntry
    │   ├─ Apply filters
    │   └─ Write SSE: "data: {json}\n\n"
    └─ Client reads line-by-line, prints formatted rows
```

### Tenant Scoping (api/server.go:1339)

Non-platform principals see only audit entries where:
- `entry.Target` starts with `{tenant}/`
- `entry.Target` contains `/{tenant}/`
- `entry.Principal` contains `{tenant}`

Platform principals see all entries.

---

## Wiring: Server Startup Sequence

```
command_server.go:334  startCCattlerServer()
    │
    ├─ initializeAuthorizationLayer(factStore)           (:334)
    │   ├─ NewRBACAuthorizer(factStore)
    │   ├─ NewStoreBackedAuditLog(factStore, 1000)
    │   │       └─ resumeSequenceFromStore()             (scan audit/ for max seq)
    │   └─ NewAuthorizedStore(factStore, rbac, auditLog)
    │
    ├─ NewEventLog(factStore, 1000)                      (:338)
    │
    ├─ startControllersWithLeaderElection(factStore, eventLog, config)  (:341)
    │   │
    │   └─ HARunner.SetEventLog(eventLog)                (:398)
    │       │
    │       └─ On leader acquire:
    │           └─ Runner.SetEventLog(eventLog)
    │               └─ Runner.Run():
    │                   └─ go EventProjector.Run()       (runner.go:204)
    │
    ├─ coreControllers() includes:
    │   └─ NewAuditLogGCController()                     (helpers.go:303)
    │
    └─ configureAndStartAPIServer(factStore, authorizedStore,
            rbacAuthorizer, auditLog, eventLog, config)  (:345)
        ├─ server.SetEventLog(eventLog)                  (:610)
        └─ server.SetAuditStore(factStore)               (:611)
```

---

## Resync & Gap Handling (Best-Effort Contract)

The EventProjector provides best-effort event history. Gaps occur under:

### Gap Causes

| Cause | Detection | Recovery |
|---|---|---|
| etcd compaction | `EventCompacted` on watch channel | `resyncFromScan()` → gap marker event |
| Watch overflow | `EventOverflow` on watch channel | `resyncFromScan()` → gap marker event |
| Watch channel close | Channel closed by store | Reconnect with `lastProcessedRevision + 1` |
| Projector restart | No watch active | Start fresh watch (revision 0 = from current) |

### Resync Flow (event_projector.go:155)

```
EventCompacted or EventOverflow detected
    │
    ▼
resyncFromScan(ctx, prefix, lastProcessedRevision)
    │
    ├─ ScanWithRevision(ctx, prefix) → get current state + revision
    │
    ├─ Emit gap marker event:
    │   kind:   "projector.resync"
    │   target: "projector/{prefix}"
    │   detail: "event history gap for prefix {prefix}: last processed revision {N}, resumed at {M}"
    │   source: "event-projector"
    │
    └─ Return scan revision → next watch starts from here
```

### Watch Reconnect Flow (event_projector.go:80)

```
Watch channel closed
    │
    ├─ Log warning with prefix + last_revision
    ├─ Wait watchReconnectBackoff (2s)
    │
    └─ Re-establish watch:
        ├─ If lastProcessedRevision > 0:
        │   └─ StartRevision = lastProcessedRevision + 1   (no gap)
        └─ If lastProcessedRevision == 0:
            └─ StartRevision = 0   (from current, may miss events)
```

### Guarantees

| Property | Guarantee |
|---|---|
| Ordering | Events within a prefix are ordered by etcd revision |
| Completeness | Best-effort — gaps signaled with `projector.resync` marker |
| Deduplication | Not guaranteed — same transition may produce duplicate events on reconnect |
| Latency | Near-real-time (watch-based, not polling) |
| Persistence | Events survive restarts (stored in etcd under event/ prefix) |
| Audit persistence | Auth decisions survive restarts (stored in etcd under audit/ prefix) |

---

## File Reference

| File | Purpose |
|---|---|
| `security/audit.go` | AuditEntry type, InMemoryAuditLog, StoreBackedAuditLog |
| `security/authorized_store.go` | AuthorizedStore wrapper, logAllowed/logDenied, sanitization |
| `controllers/event_projector.go` | EventProjector: watch-based state-to-event projection |
| `controllers/audit_gc.go` | AuditLogGCController: time-based audit entry cleanup |
| `types/eventlog.go` | EventLog: append-only event stream, query, trim |
| `types/defaults.go:22` | DefaultEventLogMaxEvents (1000) |
| `types/keys.go:46,63` | PrefixEvent, PrefixAudit |
| `cmd/cca/command_audit.go` | CLI: cca audit query/follow |
| `cmd/cca/command_server.go:334-345` | Wiring: audit log + event log + projector + API |
| `cmd/cca/helpers.go:303` | AuditLogGCController registration in coreControllers |
| `api/server.go:1218-1364` | HTTP handlers: handleAudit, handleAuditFollow, filters |
