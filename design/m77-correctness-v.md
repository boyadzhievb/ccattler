# M77 — Correctness V: Storage Retry, Operation Isolation, Watch Continuity, Cloud Post-Commit

Source: [chat-09oct.md](chat-09oct.md) (ChatGPT review of M76)

## Findings

### 1. P1 — Failed storage operations remain stuck indefinitely

The runner calls `ExecutePostCommitOperations` only after a successful, non-empty
transaction. If Reconcile produces no changes (because observed state already
reflects the intent), the executor is skipped. Failed provider operations leave
a pending fact in the store that is never retried.

**Resize failure path:** Reconcile writes both the pending resize operation AND
the observed volume size update in the same transaction. Provider call fails.
Next reconcile sees desired == observed size → no changes → executor never runs.

**Migration failure path:** Reconcile transitions to VolumeMigrating and clears
bindings before the snapshot executes. If the snapshot fails, the volume is
already migrating with no bindings — reconcile won't re-trigger migration.

**Fix:** Two changes:
1. Runner calls `ExecutePostCommitOperations` on every reconciliation cycle for
   PostCommitControllers, regardless of whether Reconcile produced changes.
2. Remove observed size update from Reconcile resize path — only update observed
   size inside post-commit after provider succeeds.

### 2. P1 — Resize and migration duplicate the same pending key

Both `reconcileVolumeResize` and `reconcileVolumeMigration` write to
`derived/volume/<name>/pending_operation`. When both conditions are true
simultaneously, the runner rejects the change set for duplicate keys.

**Fix:** Use distinct keys per operation type:
- `derived/volume/<name>/pending_resize`
- `derived/volume/<name>/pending_snapshot`

### 3. P2 — Event projector watch reconnection loses events

The reconnect loop creates a fresh watch without `StartRevision`. Events between
watch closure and reconnection are missed. The existing test doesn't exercise
actual reconnection.

**Fix:** Track last processed revision per prefix. On reconnect, pass
`StartRevision = lastRevision + 1`. Handle `EventCompacted` by re-scanning
current state and restarting the watch from scan revision.

### 4. P2 — Heartbeat stress test not in standard CI

The test skips under `-short`. Phase 3 uses `KillNode` which directly writes
state rather than testing heartbeat-expiry detection.

**Fix:** Deferred to a CI infrastructure phase — not a code correctness issue.

### 5. P2 — Cloud provider calls inside Reconcile

Cloud LB and route controllers call `EnsureLoadBalancer`, `DeleteLoadBalancer`,
`EnsureRoute`, `DeleteRoute` inside Reconcile. CAS conflict after a provider call
means the call happens again on retry.

**Fix:** Adopt the durable-intent/post-commit pattern: Reconcile emits pending
operation facts, runner commits them, post-commit executor calls the provider.

## Checklist

- [ ] Runner calls ExecutePostCommitOperations on every cycle (not just non-empty txn)
- [ ] Resize: defer observed size update to post-commit
- [ ] Distinct pending keys: pending_resize, pending_snapshot
- [ ] Test: resize retry on provider failure
- [ ] Test: simultaneous resize + migration (no duplicate key)
- [ ] Event projector: revision-aware reconnect with StartRevision
- [ ] Event projector: handle EventCompacted with resync
- [ ] Test: event projector reconnection with actual channel closure
- [ ] Cloud LB controller: durable-intent/post-commit pattern
- [ ] Cloud route controller: durable-intent/post-commit pattern
- [ ] Test: cloud LB post-commit execution
- [ ] Test: cloud route post-commit execution
- [ ] All tests pass, lint clean
