# Design

## Context

Call graphs: [INSTANCE_LIFECYCLE_GRAPH.md](../../../design/graphs/INSTANCE_LIFECYCLE_GRAPH.md) and
[CALL_GRAPH.md](../../../design/graphs/CALL_GRAPH.md) (`publishInstanceStateToStore` →
`publishInstanceStateUnlessStopped`), [CONTROLLER_DATA_FLOW_GRAPH.md](../../../design/graphs/CONTROLLER_DATA_FLOW_GRAPH.md)
and [HA_GRAPH.md](../../../design/graphs/HA_GRAPH.md) (`attemptSingleReconciliation` →
`buildReconciliationTransaction`). Flow: the agent's state publication in
[runtime-invariant-flows.md](../../../design/runtime-invariant-flows.md) Flow 34 (M241 2i.1, "agent publishes
instance state by CAS and never over a stop"); this change adds "and only when it changed".

Current state, verified in the code:
- `Agent` reconcile loop (`agent/agent.go`, "Reconcile each desired instance"): for every running instance, each
  cycle calls `observeInstanceState` and `publishInstanceStateToStore`. That function calls
  `publishInstanceStateUnlessStopped`, which reads the state, returns if it is `stopped`, and otherwise commits
  `Transaction(compare revision, put state)` unconditionally. The following writes (`Put` of the marker, service,
  node, IP; create-only `created-at`) skip unchanged values already.
- `EtcdStore.Put` reads first and skips an unchanged value (`store/etcd.go`). `EtcdStore.Transaction` sends the puts
  as they are, and etcd bumps the revision and emits an event for each.
- `MemoryStore.Transaction`: `transactionWouldMutateState` and `executePutOperation` skip unchanged puts (no revision,
  no event). So the memory store hides what etcd does.
- Runner: `attemptSingleReconciliation` → `enforceWriteDomain` → `validateAndCapChanges` (60 changes) →
  `buildReconciliationTransaction` (a revision guard per changed key that was scanned, create-only guard otherwise,
  plus input-key guards for scanned keys in the controller's write domain; read-only prefixes such as
  `observed/instance/` are excluded by design) → `Transaction`.
- `PublishAliveState` (node state) runs only at agent start (`agent.go` `Run`), so it is not a per-cycle write.

## Goals / Non-Goals

**Goals:**
- No per-cycle write of an unchanged instance state by the agent.
- No commit of an unchanged put proposed by any controller, whatever the controller does.
- Unit tests on the memory store see the writes etcd would see, so the next write loop fails a test.

**Non-Goals:**
- Reading watched prefixes at one revision (M265).
- An etcd write-rate metric or alert.
- Changing `EtcdStore.Transaction` to skip unchanged puts: it cannot know the current value atomically without a
  per-key value compare and an else branch, which changes its semantics for every caller.

## Decisions

### D1. The agent compares before it writes

`publishInstanceStateUnlessStopped` already reads the stored state. When it equals the observed state, it returns
true without a transaction ("published": the store holds that state), so `publishInstanceStateToStore` continues with
its own idempotent writes. When it differs, the compare-and-swap stays as in M241. This is the kubelet status
manager's rule (send only a changed status).

Alternative: a per-agent cache of the last published state. Rejected: another component may change the state in the
store (a controller stop), and the read is already there.

### D2. The runner drops unchanged puts

In `attemptSingleReconciliation`, after `enforceWriteDomain` and before `validateAndCapChanges`, a new
`dropUnchangedPuts(changes, allFacts)` removes every put whose key is in the scanned facts with a byte-equal value.
Deletes, puts of unscanned keys and changed values pass. If nothing is left, no transaction is sent (the existing
`len(changes) == 0` return). Dropped puts do not count against the 60-change budget.

Why it is safe without a guard for the dropped key: the key was scanned, so it is under one of the controller's
watched prefixes. If another writer changes it after the scan, that write wakes the controller, which proposes its
value again, now as a real change. A group (`Change.Group`) that loses an unchanged member still commits the rest
atomically; the dropped member already holds the wanted value.

Checked: no controller depends on a re-put to bump a revision (leases and heartbeats are written outside the runner:
`controllers/leader.go`, `agent/telemetry.go`). Task 2.2 repeats the check with a grep over every controller's
`Change{Type: store.OpPut` sites whose value is a timestamp or counter.

Alternative: rely on each controller to compare before it proposes (as the scheduler does since M243 3g.1). Rejected
as the only defence: it is the rule that failed in F1, and nothing enforces it. The controller-side checks stay; they
also save the work of building the change.

### D3. The memory store behaves like etcd for unchanged puts in transactions

`executePutOperation` writes an unchanged value like a changed one (new mod revision, same create revision, a put
event), and `transactionWouldMutateState` counts an unchanged put as a mutation. `MemoryStore.Put` keeps its skip.
The scheduler test's `etcdLikeStore` wrapper (M243 3g.1) becomes redundant and is removed (no dead code); its test
keeps working on the plain memory store.

Expected fallout: tests whose code under test commits unchanged puts through a transaction now see extra events or
revisions. Each such failure is classified: a real write loop is fixed in this change (bugs first); a test that only
assumed the old memory-store behaviour is adjusted, with one line in tasks.md per test. If the fallout exceeds about
ten tests or reaches into unrelated subsystems, stop and report before changing them.

## Failure cases

| Case | What happens |
|---|---|
| A controller stops the instance between the agent's read and its decision to skip | The agent skips a write it would have made with an old revision anyway; the stop stays; the next cycle sees `stopped` (M241 rule unchanged). |
| Stored state differs from the observed one | Compare-and-swap as before; on conflict the agent retries next cycle. |
| A key changes between the runner's scan and its commit, and the controller's put of the old value was dropped | The change wakes the controller; it proposes its value again as a real change and commits it. |
| A dropped put was the only change of an atomic group | Nothing to commit for that group; the stored value already equals it. |
| Code relied on the memory store hiding unchanged transactional puts | Its test now fails; classified per D3 (fix the loop, or adjust the test). |
| etcd compaction or watch restart | Unaffected: no change to watch handling. |

## Risks / Trade-offs

- D3 can surface many test failures at once. The stop-and-report threshold in D3 keeps this change from growing into
  unrelated fixes; anything large becomes its own milestone.
- D2 adds one map lookup per proposed change (the scanned revisions map exists already; values need one more map or
  the same map with values). O(n) in the scanned facts per reconcile, which the runner already pays.
