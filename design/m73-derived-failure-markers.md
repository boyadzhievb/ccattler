# M73 — Derived Failure Markers & CAS Decoupling

## Problem

At 50+ nodes, controller transactions fail repeatedly due to CAS (Compare-And-Swap) contention with agent writes. The runner builds transactions with ModRevision guards on all scanned facts. Since controllers scan `observed/instance/` and agents write there every cycle, the guards are always stale by commit time.

Two contention vectors:

1. **Shared-key writes**: NodeFailureController writes `observed/instance/{id}/state = failed` — same key agents write to.
2. **Input key guard invalidation**: The runner guards on all scanned `observed/instance/` facts. Any agent writing to *any* instance key between scan and commit invalidates the transaction, even if the controller only writes to different keys.

## Fix

### Step 5: Move failure writes to `derived/`

- NodeFailureController writes `derived/instance/{id}/node_failure = true` instead of `observed/instance/{id}/state = failed`
- FailureController writes `derived/instance/{id}/controller_stopped = true` instead of `observed/instance/{id}/state = stopped`
- Replacement instance creation stays in `observed/instance/{newID}/*` (new IDs don't contend)
- `effectiveInstanceState()` helper merges observed state with derived markers for all consumers

### Step 6: Scope input key guards to write domain

`appendInputKeyGuards` only guards on facts whose prefix matches the controller's declared write domain. Facts from read-only prefixes (like `observed/instance/` for failure controllers) are excluded. This means agents can write freely to `observed/instance/` without invalidating controller transactions that only write to `derived/`.

### Step 7: Marker priority order

`controller_stopped` takes priority over `node_failure` in `effectiveInstanceState`. Once the FailureController has replaced an instance (writing `controller_stopped`), it is permanently "stopped" — even if the NodeFailureController also marked it with `node_failure`. Without this priority, an instance with both markers evaluates as "failed", causing the FailureController to replace it again in an infinite cascade.

### Step 8: Node recovery via fresh heartbeat

`identifyUnreachableNodes` now checks the heartbeat for nodes in "unreachable" state. If the heartbeat is fresh (within lease timeout), the node has recovered and the controller emits a state change back to "alive". Previously, an "unreachable" node was unconditionally treated as unreachable regardless of heartbeat freshness, preventing recovery after partition healing.

### Why this is safe

Controllers are idempotent. If the scanned state was stale (an agent updated something between scan and commit), the controller's proposed changes might be redundant or slightly wrong — but the next cycle re-scans fresh state and corrects. The only risk would be conflicting concurrent writes to the *same* key, which step 5 eliminates by separating write domains.

## New keys

| Key | Writer | Reader |
|-----|--------|--------|
| `derived/instance/{id}/node_failure` | NodeFailureController | FailureController, InstanceController, all state consumers, Agent |
| `derived/instance/{id}/controller_stopped` | FailureController | InstanceController, all state consumers, Agent |

## Bugs discovered and fixed

1. **Replacement cascade livelock**: When both `node_failure` and `controller_stopped` markers existed on an instance, `effectiveInstanceState` returned "failed" (node_failure checked first), causing the FailureController to replace it again infinitely. Fixed by checking `controller_stopped` first.

2. **Unreachable node recovery failure**: `identifyUnreachableNodes` unconditionally added "unreachable" nodes to the unreachable set without checking heartbeat freshness. A node whose agent restarted and sent fresh heartbeats would remain stuck in "unreachable" state, causing all instances placed there to be perpetually failed. Fixed by checking heartbeat even for "unreachable" nodes.

## Acceptance criterion

`TestTransactionBudgetingConvergence` (50 nodes, 1000 workloads, 5 killed nodes) passes within 300s.
`TestChaosRunnerFullChaosConverges` (all 4 chaos scenarios, 3 nodes) converges at 80%+ rate.
