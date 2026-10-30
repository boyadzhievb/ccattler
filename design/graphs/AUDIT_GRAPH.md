# CCattler Audit & Event Projection Call Graph

Machine-readable function-level index. Purpose: trace audit logging, event
projection, and query flows without re-reading source files.

## Subsystem 1: Authorization Audit Log (security/audit.go)

### StoreBackedAuditLog — persistent audit entries

#### NewStoreBackedAuditLog → initialization
```
NewStoreBackedAuditLog(stateStore, maxEntries)                 :112
├── entries = make([]AuditEntry, 0, maxEntries)
└── resumeSequenceFromStore()                                  :164
    ├── stateStore.Scan("audit/")
    ├── for each fact: parse key → int64 sequence
    ├── find highest sequence
    └── sequenceNum.Store(highestSequence)                     [resume after restart]
```

#### StoreBackedAuditLog.Log → record + persist
```
StoreBackedAuditLog.Log(entry)                                 :124
├── entry.Timestamp = time.Now()                               [set at log time]
├── mutex.Lock
│   ├── if len(entries) >= maxEntries:
│   │   └── copy(entries, entries[1:])                         [ring buffer shift]
│   └── entries = append(entries, entry)
├── mutex.Unlock
└── go persistEntry(entry)                                     :149 [async goroutine]
    ├── sequenceNum.Add(1)                                     [monotonic counter]
    ├── key = fmt.Sprintf("audit/%020d", sequenceNumber)
    ├── json.Marshal(entry) → entryJSON
    └── stateStore.Put(key, entryJSON)                         [async, never blocks auth]
```

#### StoreBackedAuditLog.Entries → query in-memory copy
```
StoreBackedAuditLog.Entries()                                  :139
├── mutex.Lock
└── copy(entriesCopy, entries) → return copy
```

### InMemoryAuditLog — test/single-node

```
NewInMemoryAuditLog(maxEntries)                                :61
InMemoryAuditLog.Log(entry)                                    :69
├── mutex.Lock
├── entry.Timestamp = time.Now()
├── ring buffer shift if full
└── append entry

InMemoryAuditLog.Entries()                                     :83
└── mutex.Lock → copy + return
```

### AuditLogger interface (security/audit.go:43)
```go
type AuditLogger interface {
    Log(entry AuditEntry)
    Entries() []AuditEntry
}
```

### AuditEntry schema (security/audit.go:32)
| Field      | Type      | Description                              |
|------------|-----------|------------------------------------------|
| Timestamp  | time.Time | set at Log() time, not caller time       |
| Principal  | string    | sanitized "kind:name"                    |
| Action     | string    | get, put, delete, scan, watch, transaction |
| Target     | string    | fact key or prefix (secrets redacted)    |
| Decision   | string    | "allow" or "deny"                        |
| AuthMethod | string    | mtls, oidc, local-token, webhook         |
| Policy     | string    | policy name on condition failures        |

---

## Audit Entry Production (security/authorized_store.go)

### AuthorizedStore → every operation produces audit entries
```
AuthorizedStore.Get(ctx, key)                                  :76
├── PrincipalFromContext(ctx)                                   :40
├── authorizer.Authorize(principal, PermissionRead, key)
│   ├── error → logDenied(ctx, principal, "get", key)          :220
│   └── nil → logAllowed(ctx, principal, "get", key)           :207
└── inner.Get(ctx, key)

AuthorizedStore.Put(ctx, key, value)                           :87
├── authorizer.Authorize(principal, PermissionWrite, key)
│   ├── error → logDenied(ctx, principal, "put", key)
│   └── nil → logAllowed(ctx, principal, "put", key)
└── inner.Put(ctx, key, value)

AuthorizedStore.Delete(ctx, key)                               :98
├── authorizer.Authorize(principal, PermissionDelete, key)
│   ├── error → logDenied(ctx, principal, "delete", key)
│   └── nil → logAllowed(ctx, principal, "delete", key)
└── inner.Delete(ctx, key)

AuthorizedStore.Scan(ctx, prefix)                              :109
├── authorizer.Authorize(principal, PermissionRead, prefix)
│   ├── error → logDenied(ctx, principal, "scan", prefix)
│   └── nil → logAllowed(ctx, principal, "scan", prefix)
└── inner.Scan(ctx, prefix)

AuthorizedStore.Watch(ctx, key, opts)                          :131
├── authorizer.Authorize(principal, PermissionWatch, key)
│   ├── error → logDenied(ctx, principal, "watch", key)
│   └── nil → logAllowed(ctx, principal, "watch", key)
└── inner.Watch(ctx, key, opts)

AuthorizedStore.Transaction(ctx, compares, onSuccess, onFailure) :143
├── for each compare: Authorize(PermissionRead, compare.Key)
│   └── error → logDenied(ctx, principal, "transaction-compare", key)
├── for each onSuccess op: Authorize(Write or Delete, op.Key)
│   └── error → logDenied(ctx, principal, "transaction-op", key)
├── for each onFailure op: Authorize(Write or Delete, op.Key)
│   └── error → logDenied(ctx, principal, "transaction-op", key)
├── logAllowed(ctx, principal, "transaction", "{N} ops")
└── inner.Transaction(ctx, compares, onSuccess, onFailure)
```

### logAllowed / logDenied → sanitize + emit
```
logAllowed(ctx, principal, action, target)                     :207
├── sanitizeForAudit(principal)                                :21
│   ├── replace \n → \\n, \r → \\r, \t → \\t                 [log injection prevention]
│   └── regex: /secret/.* → /secret/[REDACTED]                [sensitive data redaction]
├── sanitizeForAudit(action)
├── sanitizeForAudit(target)
└── auditLog.Log(AuditEntry{
        Principal:  sanitized, Action: sanitized,
        Target:     sanitized, Decision: "allow",
        AuthMethod: AuthMethodFromContext(ctx)                  :52
    })

logDenied(ctx, principal, action, target)                      :220
└── auditLog.Log(AuditEntry{Decision: "deny", ...})           [same sanitization]
```

### APIAuthorizer condition failure audit
```
APIAuthorizer.logConditionFailure(principal, capability, scope, policy) :136
└── auditLog.Log(AuditEntry{
        Principal: principal.String(),
        Action:    string(capability),
        Target:    scope.String(),
        Decision:  "deny",
        Policy:    policyName + ":condition_failed"
    })
```

---

## Subsystem 2: Event Projection (controllers/event_projector.go)

### EventProjector.Run → entry point
```
EventProjector.Run(ctx)                                        :49
├── watchPrefixes = [
│   "observed/instance/",                                      [state transitions, probes]
│   "derived/instance/",                                       [node_failure, controller_stop]
│   "observed/node/",                                          [node state changes]
│   "placement/instance/",                                     [scheduling decisions]
│   "effective/service/"                                       [scaling events]
│   ]
├── mergedChannel = make(chan store.Event, 64)                  [defaultMergedEventBuffer]
├── for each prefix:
│   └── go watchPrefixWithReconnect(ctx, prefix, mergedChannel) [goroutine per prefix]
└── select loop:
    ├── <-ctx.Done → return
    └── watchEvent <-mergedChannel → projectEvent(ctx, watchEvent)
```

### watchPrefixWithReconnect → watch with auto-reconnect
```
watchPrefixWithReconnect(ctx, prefix, mergedChannel)           :80
└── for (retry loop):
    ├── factStore.Watch(ctx, prefix, {Prefix: true, StartRevision: lastRev+1})
    │   └── error → sleep watchReconnectBackoff (2s) → retry
    ├── for watchEvent in watchChannel:
    │   ├── channel closed:
    │   │   └── log warn → break → sleep 2s → retry
    │   ├── EventCompacted or EventOverflow:
    │   │   └── resyncFromScan(ctx, prefix, lastProcessedRevision)  :155
    │   │       ├── factStore.ScanWithRevision(ctx, prefix)
    │   │       ├── eventLog.Emit("projector.resync",
    │   │       │   "projector/{prefix}",
    │   │       │   "event history gap for prefix {prefix}: last processed revision {N}, resumed at {M}",
    │   │       │   "event-projector")
    │   │       └── return scanResult.Revision
    │   └── normal event:
    │       ├── update lastProcessedRevision
    │       └── mergedChannel <- watchEvent
    └── ctx cancelled → return
```

### projectEvent → classify + enrich + emit
```
projectEvent(ctx, watchEvent)                                  :175
├── classifyWatchEventAsSemanticEvent(watchEvent)              :226
│   └── [see classification table below]
├── if eventKind == "" → return (not noteworthy)
├── enrichInstanceEventWithServiceName(ctx, target, detail)    :189
│   ├── if !strings.HasPrefix(target, "instance/") → pass through
│   ├── factStore.Get("observed/instance/{id}/service")
│   └── replace "instance/{id}" with "instance/{svc}-{id}"
└── eventLog.Emit(ctx, kind, target, detail, "event-projector")
```

### classifyWatchEventAsSemanticEvent → watch event → semantic event
```
classifyWatchEventAsSemanticEvent(watchEvent)                  :226
├── factValue == previousValue → return "", "", ""             [no actual change]
│
├── observed/instance/{id}/state:
│   ├── "running"  → "instance.running"
│   ├── "failed"   → "instance.failed"
│   ├── "pending" (prev="")  → "instance.created"
│   ├── "pending" (prev!="") → "instance.pending"
│   ├── "starting" → "instance.starting"
│   └── "stopped"  → "instance.stopped"
│
├── observed/instance/{id}/probe/{type}:
│   ├── "startup_failed"     → "probe.failed"
│   ├── "startup_succeeded"  → "probe.succeeded"
│   ├── "liveness_unhealthy" → "probe.unhealthy"
│   ├── "readiness_not_ready" → "probe.not_ready"
│   ├── healthy (prev=unhealthy) → "probe.recovered"
│   └── ready (prev=not_ready) → "probe.recovered"
│
├── derived/instance/{id}/node_failure:
│   └── any → "instance.failed" (detail: "node unreachable")
│
├── derived/instance/{id}/controller_stopped:
│   └── any → "instance.stopped" (detail: "stopped by controller")
│
├── placement/instance/{id} (EventPut):
│   └── → "instance.placed" (detail: "placed on {nodeID}")
│
├── observed/node/{nodeID}/state:
│   └── any → "node.{value}" (detail: "node {id} is now {value}")
│
└── effective/service/{svc}/instances:
    └── any → "service.scaled" (detail: "service {svc} scaled to {N} instances")
```

---

## Subsystem 3: EventLog (types/eventlog.go)

### EventLog.Emit → create + persist + trim
```
EventLog.Emit(ctx, kind, target, detail, source)              :64
├── now = TimeFunc()
├── sequence = sequence.Add(1)
├── eventID = "{unixnano}-{seq04d}-{kind}"                    [globally unique, time-ordered]
├── event = SystemEvent{ID, Timestamp, Kind, Target, Detail, Source}
├── json.Marshal(event) → eventJSON
├── factStore.Put("event/{eventID}", eventJSON)
└── if maxEvents > 0:
    └── trimOldEvents(ctx)                                     :184
        ├── factStore.Scan("event/")
        ├── excess = len(allFacts) - maxEvents
        └── for i := 0; i < excess: factStore.Delete(allFacts[i].Key) [oldest first]
```

### EventLog query methods
```
EventLog.Query(ctx, kind, limit)                               :98
├── factStore.Scan("event/")
├── filter by kind (empty = all)
└── return newest `limit` events

EventLog.Since(ctx, after, limit)                              :125
├── factStore.Scan("event/")
├── filter: event.Timestamp.After(after)
└── return newest `limit` events

EventLog.ForTarget(ctx, target, limit)                         :150
├── factStore.Scan("event/")
├── filter: strings.Contains(event.Target, target)             [substring match]
└── return newest `limit` events

EventLog.Count(ctx)                                            :175
└── factStore.Scan("event/") → len(allFacts)
```

### SystemEvent schema (types/eventlog.go:22)
| Field     | Type      | Description                              |
|-----------|-----------|------------------------------------------|
| ID        | string    | "{unixnano}-{seq04d}-{kind}" — unique    |
| Timestamp | time.Time | set at Emit time                         |
| Kind      | string    | e.g. "instance.running", "node.alive"    |
| Target    | string    | e.g. "instance/web-abc123", "node/node-1"|
| Detail    | string    | human-readable description               |
| Source    | string    | always "event-projector" for projected   |

---

## Subsystem 4: Audit Log Garbage Collection (controllers/audit_gc.go)

### AuditLogGCController.Reconcile → time-based cleanup
```
AuditLogGCController.Reconcile(ctx, facts)                     :66
├── now = NowFunc()
├── for each fact under "audit/":
│   ├── json.Unmarshal(fact.Value, &entry)
│   ├── if entry.Timestamp.IsZero() → skip
│   └── if now - entry.Timestamp > RetentionPeriod → expiredKeys
├── sort.Strings(expiredKeys)                                  [oldest first]
├── if len > MaxDeletionsPerCycle:
│   └── expiredKeys = expiredKeys[:MaxDeletionsPerCycle]       [cap batch size]
└── return []Change{Delete} for each expired key

Watch: "audit/"                                                :60
Name: "audit-log-gc"                                           :56
```

---

## CLI: cca audit (cmd/cca/command_audit.go)

### Query mode
```
executeAuditCommand(args)                                      :33
├── parse flags: --principal, --action, --since, --limit, --follow
├── if --follow → follow mode
└── query mode:
    GET /api/audit?principal=&action=&since=&limit=&denied=true
    │
    ▼
    handleAudit()                                              api/server.go:1221
    ├── Scan "audit/" prefix from store
    ├── json.Unmarshal each → AuditEntry
    ├── apply filters:
    │   ├── principal match
    │   ├── action match
    │   ├── since (timestamp)
    │   ├── denied-only flag
    │   └── tenant scope (non-platform principals)             :1339
    │       ├── entry.Target starts with "{tenant}/"
    │       ├── entry.Target contains "/{tenant}/"
    │       └── entry.Principal contains "{tenant}"
    └── return JSON array
```

### Follow mode
```
GET /api/audit?follow=true&...
    │
    ▼
    handleAuditFollow()                                        api/server.go:1287
    ├── Watch "audit/" prefix via store.Watch
    ├── for each new entry:
    │   ├── json.Unmarshal → AuditEntry
    │   ├── apply filters
    │   └── write SSE: "data: {json}\n\n"
    └── client reads line-by-line → formatted rows
```

---

## Server Startup Wiring (cmd/cca/command_server.go)

### startCCattlerServer → audit + event subsystem initialization
```
startCCattlerServer()                                          :334
├── initializeAuthorizationLayer(factStore)                    :334
│   ├── NewRBACAuthorizer()
│   ├── NewStoreBackedAuditLog(factStore, 1000)                [DefaultEventLogMaxEvents]
│   │   └── resumeSequenceFromStore()                          [scan audit/ for max seq]
│   └── NewAuthorizedStore(factStore, rbacAuthorizer, auditLog)
│       └── every Get/Put/Delete/Scan/Watch/Transaction → logAllowed/logDenied
│
├── NewEventLog(factStore, 1000)                               :338
│
├── startControllersWithLeaderElection(factStore, eventLog, config) :341
│   └── HARunner.SetEventLog(eventLog)                         :398
│       └── on leader acquire:
│           └── Runner.SetEventLog(eventLog)
│               └── Runner.Run():
│                   └── go EventProjector.Run()                [runner.go:204]
│
├── coreControllers() includes:
│   └── NewAuditLogGCController()                              [helpers.go:303]
│
└── configureAndStartAPIServer(factStore, authorizedStore,
        rbacAuthorizer, auditLog, eventLog, config)            :345
    ├── server.SetEventLog(eventLog)                           :610
    └── server.SetAuditStore(factStore)                        :611
```

---

## Resync & Gap Handling (Best-Effort Contract)

### Gap causes
| Cause | Detection | Recovery |
|---|---|---|
| etcd compaction | EventCompacted on watch channel | resyncFromScan → gap marker |
| Watch overflow | EventOverflow on watch channel | resyncFromScan → gap marker |
| Watch channel close | channel closed by store | reconnect with lastRev + 1 |
| Projector restart | no watch active | start fresh watch (rev 0 = current) |

### resyncFromScan flow
```
resyncFromScan(ctx, prefix, lastProcessedRevision)             :155
├── factStore.ScanWithRevision(ctx, prefix) → scanResult
├── eventLog.Emit("projector.resync",
│   "projector/{prefix}",
│   "event history gap for prefix {prefix}: last processed revision {N}, resumed at {M}",
│   "event-projector")
└── return scanResult.Revision → next watch starts from here
```

### Watch reconnect flow
```
watchPrefixWithReconnect (reconnect path)                      :80
├── watch channel closed
├── log warn: prefix + last_revision
├── sleep watchReconnectBackoff (2s)
└── re-establish watch:
    ├── if lastProcessedRevision > 0:
    │   └── StartRevision = lastProcessedRevision + 1          [no gap]
    └── if lastProcessedRevision == 0:
        └── StartRevision = 0                                  [from current, may miss events]
```

### Guarantees
| Property | Guarantee |
|---|---|
| Ordering | Events within a prefix ordered by etcd revision |
| Completeness | Best-effort — gaps signaled with projector.resync marker |
| Deduplication | Not guaranteed — duplicates possible on reconnect |
| Latency | Near-real-time (watch-based, not polling) |
| Audit persistence | Auth decisions survive restarts (etcd audit/ prefix) |
| Event persistence | System events survive restarts (etcd event/ prefix) |

---

## Store Key Patterns

### Reads
| Key Pattern | Where Read |
|---|---|
| `audit/{sequence}` | resumeSequenceFromStore, handleAudit, handleAuditFollow, AuditLogGCController.Reconcile |
| `event/{id}` | EventLog.Query, EventLog.Since, EventLog.ForTarget, EventLog.Count, trimOldEvents |
| `observed/instance/{id}/service` | enrichInstanceEventWithServiceName |
| `observed/instance/{id}/state` | classifyWatchEventAsSemanticEvent (via watch) |
| `observed/instance/{id}/probe/{type}` | classifyWatchEventAsSemanticEvent (via watch) |
| `derived/instance/{id}/node_failure` | classifyWatchEventAsSemanticEvent (via watch) |
| `derived/instance/{id}/controller_stopped` | classifyWatchEventAsSemanticEvent (via watch) |
| `placement/instance/{id}` | classifyWatchEventAsSemanticEvent (via watch) |
| `observed/node/{nodeID}/state` | classifyWatchEventAsSemanticEvent (via watch) |
| `effective/service/{svc}/instances` | classifyWatchEventAsSemanticEvent (via watch) |

### Writes
| Key Pattern | Where Written |
|---|---|
| `audit/{020d sequence}` | StoreBackedAuditLog.persistEntry |
| `event/{unixnano}-{seq04d}-{kind}` | EventLog.Emit |

### Deletes
| Key Pattern | Where Deleted |
|---|---|
| `audit/{sequence}` | AuditLogGCController.Reconcile (expired entries) |
| `event/{id}` | EventLog.trimOldEvents (excess entries) |

---

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `StoreBackedAuditLog.Log` | AuthorizedStore.logAllowed, AuthorizedStore.logDenied, APIAuthorizer.logConditionFailure |
| `StoreBackedAuditLog.Entries` | API handler (handleAudit query) |
| `persistEntry` | StoreBackedAuditLog.Log (goroutine) |
| `resumeSequenceFromStore` | NewStoreBackedAuditLog |
| `logAllowed` | AuthorizedStore.Get, Put, Delete, Scan, ScanWithRevision, Watch, Transaction |
| `logDenied` | AuthorizedStore.Get, Put, Delete, Scan, ScanWithRevision, Watch, Transaction |
| `sanitizeForAudit` | logAllowed, logDenied |
| `PrincipalFromContext` | AuthorizedStore.Get, Put, Delete, Scan, ScanWithRevision, Watch, Transaction |
| `AuthMethodFromContext` | logAllowed, logDenied |
| `EventProjector.Run` | Runner.Run (goroutine, runner.go:204) |
| `watchPrefixWithReconnect` | EventProjector.Run (goroutine per prefix) |
| `resyncFromScan` | watchPrefixWithReconnect (on compaction/overflow) |
| `projectEvent` | EventProjector.Run (select loop) |
| `classifyWatchEventAsSemanticEvent` | projectEvent |
| `enrichInstanceEventWithServiceName` | projectEvent |
| `splitInstanceSubPath` | classifyWatchEventAsSemanticEvent |
| `EventLog.Emit` | projectEvent, resyncFromScan |
| `EventLog.Query` | API handler (GET /api/events) |
| `EventLog.Since` | API handler (GET /api/events?since=) |
| `EventLog.ForTarget` | API handler (GET /api/events?target=) |
| `EventLog.Count` | API handler |
| `trimOldEvents` | EventLog.Emit (if maxEvents > 0) |
| `AuditLogGCController.Reconcile` | Runner.runControllerLoop (on audit/ watch) |
| `NewAuditLogGCController` | coreControllers (helpers.go:303) |
| `handleAudit` | API server (GET /api/audit) |
| `handleAuditFollow` | API server (GET /api/audit?follow=true) |

---

## Constants

| Constant | Value | File:Line |
|---|---|---|
| DefaultAuditQueryLimit | 100 | security/audit.go:29 |
| AuditDecisionAllow | "allow" | security/audit.go:22 |
| AuditDecisionDeny | "deny" | security/audit.go:25 |
| DefaultEventLogMaxEvents | 1000 | types/defaults.go:22 |
| defaultAuditRetentionPeriod | 7 days | controllers/audit_gc.go:22 |
| defaultMaxAuditGCDeletionsPerCycle | 50 | controllers/audit_gc.go:26 |
| defaultMergedEventBuffer | 64 | controllers/event_projector.go:20 |
| watchReconnectBackoff | 2s | controllers/event_projector.go:43 |

---

## File Index

| File | Purpose |
|---|---|
| security/audit.go:112 | StoreBackedAuditLog — persistent audit with async store writes |
| security/audit.go:53 | InMemoryAuditLog — ring buffer for tests |
| security/audit.go:32 | AuditEntry struct |
| security/audit.go:43 | AuditLogger interface |
| security/authorized_store.go:67 | AuthorizedStore — per-op auth + audit emission |
| security/authorized_store.go:21 | sanitizeForAudit — log injection + secret redaction |
| security/authorized_store.go:34 | WithPrincipal — context propagation |
| controllers/event_projector.go:49 | EventProjector.Run — watch-based state-to-event projection |
| controllers/event_projector.go:80 | watchPrefixWithReconnect — auto-reconnect + resync |
| controllers/event_projector.go:226 | classifyWatchEventAsSemanticEvent — event classification |
| controllers/event_projector.go:189 | enrichInstanceEventWithServiceName — service name lookup |
| controllers/event_projector.go:155 | resyncFromScan — gap handling |
| controllers/audit_gc.go:46 | NewAuditLogGCController — 7-day retention, 50/cycle |
| controllers/audit_gc.go:66 | AuditLogGCController.Reconcile — time-based cleanup |
| types/eventlog.go:54 | NewEventLog — event stream with count-based trim |
| types/eventlog.go:64 | EventLog.Emit — create + persist + trim |
| types/eventlog.go:98 | EventLog.Query — filter by kind |
| types/eventlog.go:125 | EventLog.Since — filter by timestamp |
| types/eventlog.go:150 | EventLog.ForTarget — filter by target substring |
| types/eventlog.go:184 | trimOldEvents — delete oldest beyond maxEvents |
| types/keys.go:46 | PrefixEvent ("event") |
| types/keys.go:63 | PrefixAudit ("audit") |
| cmd/cca/command_audit.go:33 | CLI: cca audit query/follow |
| cmd/cca/command_server.go:334 | Wiring: audit log + event log + projector + API |
| cmd/cca/helpers.go:303 | AuditLogGCController registration in coreControllers |
| api/server.go:1221 | handleAudit — query endpoint |
| api/server.go:1287 | handleAuditFollow — SSE streaming endpoint |
| api/server.go:1339 | Tenant scoping for non-platform principals |
