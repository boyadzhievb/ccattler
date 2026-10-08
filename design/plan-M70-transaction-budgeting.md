# Fix Controller Transaction Overflow (P0)

## Context

The failure controller and node failure controller generate transactions that exceed etcd's 128-operation limit, causing reconciliation to silently fail at 50+ node scale. Discovered during chaos benchmarks; confirmed by external analysis in `chat-06oct.md`. The MemoryStore does not enforce the 128-op limit, so tests pass while production etcd would reject the transactions. This is a P0 blocking production use at scale.

Two independent bugs:
1. **Transaction overflow** — FailureController emits 4 ops per failed instance (400 ops for 100 failures). NodeFailureController emits N+1 ops per failed node. Neither is capped.
2. **No test enforcement** — MemoryStore.Transaction() accepts any size, hiding the overflow.

## Implementation

### Phase 1: Runner safety net — cap changes per transaction

**`controllers/runner.go`**
- Add `maxTransactionChanges = 60` constant (each change = 1 op + 1 compare = 2 items; 60 changes = 120 items, leaving room for a few input-key guards under the 128 cap)
- Add `truncateChangesToTransactionLimit(changes []Change) []Change` — returns `changes[:maxTransactionChanges]` if over limit, logs a warning with original count
- Call it in `attemptSingleReconciliation` after `sortChangesByKey` and before `buildReconciliationTransaction`
- Add post-build assertion in `buildReconciliationTransaction`: if total compares + ops > 128, log error and drop excess input-key guards

**`controllers/runner_test.go`**
- Add `TestRunnerTruncatesOversizedChangeSets` — mock controller returns 200 changes, assert only 60 reach the transaction

### Phase 2: FailureController rate-limiting

**`controllers/failure.go`**
- Add `maxReplacementsPerCycle = 10` (10 replacements = 40 ops, well under 128)
- In `Reconcile()`, add replacement counter before the `stopAndReplace` loop. Break when counter reaches the cap. Keep `beginDrain` uncapped (2 ops each, naturally limited)
- Follows DrainController pattern: cap per cycle, watch events re-trigger for remaining work

**`controllers/failure_test.go`**
- Add `TestFailureControllerCapsReplacementsPerCycle` — 20 failed instances, assert exactly `maxReplacementsPerCycle * 4` changes
- Existing `TestFailureMultipleFailed` (2 replacements = 8 changes) stays under cap, no change needed

### Phase 3: NodeFailureController rate-limiting

**`controllers/nodefailure.go`**
- Add `maxInstanceStateChangesPerCycle = 50` (50 instance changes + a few node state changes = ~55 ops, under 128)
- In `markInstancesOnUnreachableNodesAsFailed()`, truncate output to the cap
- Remaining instances converge in subsequent cycles

**`controllers/nodefailure_test.go`**
- Add `TestNodeFailureControllerCapsInstanceChangesPerCycle` — 100 instances across 5 failed nodes, assert at most 50 instance state changes

### Phase 4: MemoryStore enforces 128-op limit

**`store/memory.go`**
- Add `ErrTransactionTooLarge` error and `memoryStoreTransactionOperationLimit = 128` constant
- At top of `Transaction()`, check `len(compares) + len(onSuccess) + len(onFailure) > 128` and return the error
- This turns silent production failures into test-visible errors

**`store/memory_test.go`**
- Add `TestTransactionRejectsOversizedOperation` (129 ops, expect error)
- Add `TestTransactionAcceptsExactlyAtLimit` (128 ops, expect success)

### Phase 5: Convergence regression test

**`controllers/failure_convergence_test.go`** (new)
- Full integration: MemoryStore + Runner + FailureController + NodeFailureController
- 50 nodes, 1000 instances, 5 failed nodes (~100 affected instances)
- Assert: all replacements eventually land (convergence within bounded cycles)
- Assert: no transaction exceeds 128 ops (MemoryStore would error if so)
- Assert: 0 abandoned reconciliations

## Dependency ordering

Phase 4 (MemoryStore limit) must land first or simultaneously with Phases 1-3 — it makes overflow test-visible. Phases 1-3 are independent of each other. Phase 5 depends on all prior phases.

## Constants summary

| Constant | Value | File | Rationale |
|---|---|---|---|
| `maxTransactionChanges` | 60 | runner.go | 60 changes = 120 items, safety margin under 128 |
| `maxReplacementsPerCycle` | 10 | failure.go | 10 × 4 ops = 40, follows DrainController pattern |
| `maxInstanceStateChangesPerCycle` | 50 | nodefailure.go | 50 + node states < 60 runner cap |
| `memoryStoreTransactionOperationLimit` | 128 | memory.go | Mirrors etcd default |

## Verification

1. `go test ./controllers/...` — all existing tests pass, new tests pass
2. `go test ./store/...` — MemoryStore limit tests pass
3. `go test -run TestChaosBenchmark_100Workloads ./loadtest/` — still passes (small scale)
4. `go test -run TestChaosBenchmark_1000Workloads ./loadtest/ -timeout 15m` — should now converge (failure controller batches work across cycles)
5. `golangci-lint run ./controllers/ ./store/`

## Future work (not in this plan)

- Separate controller intent from `observed/instance/*` via `derived/` markers (M64 drain pattern)
- Relax CAS on exclusive output domains after ownership is separated
- Optimize retry scanning (avoid full rescan of watch domain on every retry)
