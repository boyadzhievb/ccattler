# M76 — Correctness IV: Atomic Liveness, Storage Idempotency, Projector Resilience

From ChatGPT review `chat-08oct-3.md` of M75.

## Problem Summary

Four remaining correctness/resilience issues:

1. **P1 — PublishAliveState CAS race**: GET+PUT is not atomic. Controller can write `unreachable` between the agent's GET and PUT, and the unconditional PUT resurrects the node.
2. **P1 — Storage pre-CAS side effects**: `StorageController.Reconcile()` calls `ResizeVolume`/`SnapshotVolume` before the runner commits the transaction. CAS failure → retry → duplicate external calls with different parameters (timestamp-based snapshot names).
3. **P2 — SetHeartbeatInterval panic**: `time.NewTicker(0)` panics if called with zero or negative duration. No guard.
4. **P2 — Event projector single-watch stall**: One compacted etcd watch in `mergeWatchChannels()` blocks consumption of that source forever without restarting the projector.

Plus a test gap: the 50-node loadtest disables input-key guards (`SetMaxInputKeyGuards(0)`) and injects failure directly via `KillNode()` instead of letting heartbeat expiry trigger it. The independent heartbeat goroutine isn't stress-tested under production settings.

## Fix 1: Atomic Node Liveness Transition

**Current** (`agent/telemetry.go:54-65`):
```go
func (nodeReporter *NodeReporter) PublishAliveState(ctx context.Context) {
    currentState, getError := nodeReporter.factStore.Get(ctx, ...)
    if getError == nil {
        if existingState == NodeDraining || NodeDisabled || NodeUnreachable {
            return
        }
    }
    nodeReporter.factStore.Put(ctx, ..., NodeAlive)  // unconditional!
}
```

**Race**:
```
Agent heartbeat goroutine         NodeFailureController
GET state == alive
                                  CAS → unreachable
PUT state = alive                 ← resurrects node!
```

**Approach (preferred by reviewer)**: Remove `PublishAliveState` from the heartbeat loop entirely. The heartbeat loop should only write the heartbeat timestamp. `NodeFailureController` already handles `unreachable→alive` recovery when it sees a fresh heartbeat (lines 169-171 of `nodefailure.go`). `PublishAliveState` becomes a startup-only operation.

**Changes**:
- `agent/agent.go`: Remove `PublishAliveState` from `heartbeatLoop`, keep only `WriteHeartbeat`
- `agent/agent.go`: Keep `PublishAliveState` in `Run()` startup (line 146) for initial registration
- `agent/telemetry.go`: Make `PublishAliveState` use CAS transaction for the startup path:
  - If key doesn't exist: create-only transaction (revision=0 guard)
  - If key exists and state is alive: CAS with revision guard
  - If key exists and state is draining/disabled/unreachable: skip

This is architecturally cleaner: the agent only *claims* alive at startup, the controller *owns* all state transitions after that, and the heartbeat timestamp is the sole liveness signal.

## Fix 2: Storage Intent/Executor Pattern

**Current** (`controllers/storage.go:204-210, 266-268`):
```go
// Inside Reconcile() — runs BEFORE runner commits transaction
storageController.storageProvider.ResizeVolume(ctx, volumeName, size)
storageController.storageProvider.SnapshotVolume(ctx, volumeName, snapshotName)
```

**Problem**: If the subsequent CAS transaction fails, the controller retries and calls `ResizeVolume`/`SnapshotVolume` again. Snapshot names use `time.Now().UnixMilli()`, so each retry creates a different snapshot.

**Approach**: Intent/executor pattern with stable operation IDs.

1. `reconcileVolumeResize` and `reconcileVolumeMigration` no longer call the provider directly. Instead, they write a `derived/volume/<name>/pending-operation` fact with a stable operation ID (deterministic from volume name + operation type + desired state hash).

2. A new `executeVolumeOperations` method (called by the runner *after* successful transaction commit, or by a separate post-commit hook) reads pending operations and executes them idempotently using the stable ID.

3. After successful execution, the executor writes the completion fact and clears the pending-operation key.

**Key layout**:
```
derived/volume/<name>/pending-operation  →  {"id":"resize-db-100Gi","kind":"resize","target":"100Gi"}
derived/volume/<name>/last-operation     →  {"id":"resize-db-100Gi","status":"complete"}
```

**Changes**:
- `controllers/storage.go`: Remove direct provider calls from `reconcileVolumeResize` and `reconcileVolumeMigration`. Emit pending-operation facts instead.
- `controllers/storage.go`: Add `executeVolumeOperations` method that reads pending ops, calls provider idempotently, writes completion.
- `controllers/runner.go`: Add post-commit callback hook for controllers that need it (or run it as a separate reconciliation pass).
- `types/keys.go`: Add `KeyDerivedVolumePendingOperation`, `KeyDerivedVolumeLastOperation`.
- Snapshot names: use deterministic ID (`<volume>-pre-migration-<operation-id>`) instead of `time.Now().UnixMilli()`.

## Fix 3: SetHeartbeatInterval Guard

**Current** (`agent/agent.go:134-136`):
```go
func (nodeAgent *Agent) SetHeartbeatInterval(heartbeatFrequency time.Duration) {
    nodeAgent.heartbeatFrequency = heartbeatFrequency
}
```

`time.NewTicker(0)` panics.

**Fix**: Guard against zero/negative:
```go
func (nodeAgent *Agent) SetHeartbeatInterval(d time.Duration) {
    if d <= 0 {
        d = heartbeatInterval
    }
    nodeAgent.heartbeatFrequency = d
}
```

Also guard `SetInterval` the same way for consistency.

## Fix 4: Event Projector Per-Prefix Reconnect

**Current** (`controllers/event_projector.go:168-201`):
`mergeWatchChannels` starts one goroutine per watch channel. If one channel closes (etcd compaction), that goroutine exits and sends to `pendingCount`. The merged output channel only closes when *all* goroutines exit. But a closed source never gets re-established — that prefix is silently lost.

**Fix**: Give the projector its own per-prefix reconnect loop, analogous to the agent's `runWatchLoop` pattern.

**Changes**:
- `controllers/event_projector.go`: Replace `mergeWatchChannels` with `watchPrefixWithReconnect` — each prefix gets its own goroutine that re-establishes the watch on channel closure (with a short backoff delay).
- Each reconnect goroutine forwards events to the shared merged channel.
- The projector only exits when the context is cancelled, not when a single watch dies.

## Fix 5: Production-Config Heartbeat Stress Test

Add a second loadtest that validates the independent heartbeat under production-like settings:

```
50 agents
default input-key guards (production config)
heartbeat interval = 5s (production default)
lease timeout = 30s
injected slow reconciliation (200ms+ per cycle)
assertion: zero false node failures
```

**Changes**:
- `loadtest/heartbeat_stress_test.go`: New test `TestHeartbeatIndependenceUnderLoad`
- Uses chaos cluster with production guard settings
- Injects artificial reconciliation latency via `SlowSimulatorRuntime`
- Asserts no node ever transitions to unreachable during normal operation
- Kills nodes by cancelling agent context (not `KillNode`) to exercise the full heartbeat→lease-expiry→failure pipeline

## Implementation Order

1. Fix 3 (SetHeartbeatInterval guard) — 5 minutes, zero risk
2. Fix 1 (atomic liveness) — 30 minutes, moderate; simplifies heartbeat loop
3. Fix 4 (projector reconnect) — 30 minutes, moderate
4. Fix 2 (storage intent/executor) — 1-2 hours, largest change
5. Fix 5 (heartbeat stress test) — 30 minutes, depends on Fix 1

## Files Changed

| File | Change |
|------|--------|
| `agent/agent.go` | Guard SetHeartbeatInterval, remove PublishAliveState from heartbeatLoop |
| `agent/telemetry.go` | CAS transaction in PublishAliveState |
| `controllers/storage.go` | Intent/executor pattern, remove pre-CAS provider calls |
| `controllers/runner.go` | Post-commit callback hook (if needed) |
| `controllers/event_projector.go` | Per-prefix reconnect loop |
| `types/keys.go` | Volume operation keys |
| `loadtest/heartbeat_stress_test.go` | New production-config heartbeat test |
