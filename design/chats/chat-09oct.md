I reviewed the current `master` branch, including [M76 — Correctness IV](https://github.com/boyadzhievb/ccattler/commit/6dd780a14143b66348496175c05c37ff8ee7a7e5) and the subsequent [Go toolchain update](https://github.com/boyadzhievb/ccattler/commit/2de7be0e1e708566a0c11eb1a7bc87c24601a397).

Verdict: substantial progress, but I found two remaining storage correctness issues I would fix before relying on automated volume recovery. The liveness changes are sound in principle; the new storage intent/executor path is not yet reliable when external operations fail or multiple operations are needed for the same volume.

## What M76 fixed

* Node liveness race: `PublishAliveState` now uses a revision-guarded transaction, and the heartbeat goroutine no longer writes node lifecycle state. This addresses the previous GET-then-PUT race.

* Heartbeat configuration: zero or negative heartbeat and reconciliation intervals are normalized, avoiding `time.NewTicker` panics.

* Storage side effects: resize and snapshot requests are now represented as store facts before the external provider is called. This is a better foundation than making provider calls inside `Reconcile`.

* Event projector: each watched prefix can reconnect independently, avoiding the previous permanent stall when one watch closes.

The remaining problems are below.

## Findings

### 1. P1 — Failed storage operations can remain stuck indefinitely

Files: [`controllers/runner.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/runner.go) · [`controllers/storage.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/storage.go)

The runner invokes `ExecutePostCommitOperations` only after a successful, non-empty transaction. It returns early when reconciliation produces no changes, before reaching that hook.

That is unsafe with the current storage logic:

Resize failure

1. `Reconcile` writes a pending resize operation and updates the observed volume size to the desired size in the same transaction.

2. The post-commit provider call fails, so the pending operation remains.

3. The next reconciliation sees the observed size already matching the desired size and emits no resize changes.

4. With no changes, the runner skips the executor. Periodic resync does not fix this because it takes the same no-change path.

The volume can therefore appear resized in the store even though the provider resize failed, with no guaranteed retry.

Snapshot failure during migration

The transaction moves the volume to `VolumeMigrating` and deletes its node/instance/mount bindings before executing the snapshot. If the snapshot fails, the subsequent reconciliation skips migration because the volume is no longer attached. The pending snapshot can remain unprocessed indefinitely, and the expected pre-migration snapshot may never be created.

Recommended fix: make the durable-operation executor run independently of whether reconciliation produced changes. Treat the pending operation as an actual state-machine phase: don't publish the final observed resize or complete migration until the provider operation succeeds. Commit completion and pending-operation cleanup with revision guards, and add tests where the provider fails once and succeeds on retry.

### 2. P1 — Resize and migration can generate duplicate writes to one key

Files: [`controllers/storage.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/storage.go) · [`controllers/runner.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/runner.go)

Both `reconcileVolumeResize` and `reconcileVolumeMigration` write to:

`types.KeyDerivedVolumePendingOperation(volumeName)`

If a volume needs resizing at the same time its attached node becomes unreachable, both helpers can emit a write to that same key. The runner's duplicate-key validation rejects the complete change set before the transaction commits.

That can prevent both operations from progressing while the conflicting conditions persist.

Recommended fix: give operations distinct durable keys, such as `derived/volume/<name>/operations/<operation-id>`, or explicitly serialize operations per volume so one reconciliation cannot emit two incompatible intents for the same key. Add a regression test for a volume requiring resize and migration simultaneously.

### 3. P2 — Event projector reconnection still leaves a gap in the event history

Files: [`controllers/event_projector.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/event_projector.go) · [`store/store.go`](https://github.com/boyadzhievb/ccattler/blob/master/store/store.go)

The new reconnect loop creates a fresh watch without specifying `StartRevision`. Events committed between watch closure and reconnection can be missed. The projector also doesn't recover from `EventOverflow` or perform a resync after `EventCompacted`.

This matters because the store already supports revision-aware watches and documents that consumers must resync when events are lost. Reconnecting prevents a permanent stall, but it does not guarantee a complete event history.

The added [`TestWatchPrefixWithReconnect`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/event_projector_test.go) writes an event to an open MemoryStore watch; it doesn't force a watch closure or compaction, so it doesn't exercise the recovery behavior it claims to test.

Recommended fix: track the last processed revision, recover using a consistent scan and `StartRevision`, and explicitly test compaction and overflow. If the event log must preserve every transition, define how to recover transitions that cannot be replayed.

### 4. P2 — The new heartbeat stress test isn't actually run by standard CI

Files: [`loadtest/heartbeat_stress_test.go`](https://github.com/boyadzhievb/ccattler/blob/master/loadtest/heartbeat_stress_test.go) · [`.github/workflows/test.yml`](https://github.com/boyadzhievb/ccattler/blob/master/.github/workflows/test.yml) · [`chaos/chaos_cluster.go`](https://github.com/boyadzhievb/ccattler/blob/master/chaos/chaos_cluster.go)

The standard test workflow runs `go test -race -short ...`. The heartbeat stress test explicitly skips when `testing.Short()` is true, so the green standard test run does not establish that the new 50-node stress test passes.

There is another limitation in phase 3 of that test: it calls `cluster.KillNode`, which directly writes the node's state to `unreachable` and marks its instances failed. That contradicts the test's comment claiming phase 3 exercises the full heartbeat-expiry-to-failure pipeline. Phase 2 does provide useful coverage of false node failures under slow reconciliation, but neither phase is currently a standard CI gate.

Recommended fix: add a dedicated long-running CI or scheduled job. For the lease-expiry phase, stop an agent without directly modifying node or instance facts, then verify the controller detects the missing heartbeat within the configured timeout.

### 5. P2 — Cloud provider calls remain inside `Reconcile`

Files: [`controllers/cloud_loadbalancer.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/cloud_loadbalancer.go) · [`controllers/cloud_routes.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/cloud_routes.go)

M76 moves storage operations out of the pre-commit path, but the cloud load-balancer and route controllers still call `EnsureLoadBalancer`, `DeleteLoadBalancer`, `EnsureRoute`, and `DeleteRoute` while computing changes.

A subsequent CAS conflict can cause reconciliation to run again after one of those provider calls has already happened. The `Ensure` operations may be idempotent in their implementations, but that behavior needs to be guaranteed; otherwise this is the same general class of retry hazard M76 addressed for storage.

Recommended fix: adopt the durable-intent/post-commit pattern for external mutations throughout the controllers, with explicit idempotency and retry semantics.

## CI status

Latest master Test workflow

Build, race-enabled short tests, lint, and vulnerability audit passed after the Go toolchain update.

[View run](https://github.com/boyadzhievb/ccattler/actions/runs/37902115183)

Latest E2E Cluster Test

Passed on the same current master commit.

[View run](https://github.com/boyadzhievb/ccattler/actions/runs/37902115154)

Scheduled cluster health

The most recent health-check runs I found failed on an older commit, before M76 was pushed. I don't have a post-M76 scheduled result confirming deployed cluster health.

[View health-check run](https://github.com/boyadzhievb/ccattler/actions/runs/37896298574)

## Recommendation

I would retain the liveness and telemetry improvements, but fix the storage retry/state-ordering problem and the duplicate pending-operation key before treating M76 as complete. Then add failure-injection tests for resize and snapshot retries, a simultaneous resize-plus-migration test, and a non-short stress workflow that actually exercises heartbeat expiry.

The green CI result is encouraging, but it does not cover the long-running stress scenarios, and it does not invalidate the storage failure paths identified above.
