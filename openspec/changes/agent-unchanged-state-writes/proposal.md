# Proposal

Milestone: **M266 — Agents Rewrite Unchanged Instance State Every Second** (milestones.md Phase 266, bug).

## Why

On the deploy cluster, `cca watch observed/instance/` counted 20 puts of an unchanged
`observed/instance/{id}/state = running` per running instance in 20 seconds (M262 testbed check, 2026-11-23). Each
agent cycle (about once a second) runs `publishInstanceStateToStore` for every running instance, and its first step,
`publishInstanceStateUnlessStopped` (`agent/agent.go`, M241 2i.1), commits a compare-and-swap transaction that puts
the state even when it has not changed. etcd gives every committed put a new revision and a watch event, so 1000
running instances mean 1000 etcd writes per second, and every controller watching `observed/instance/` (scheduler,
rollout, endpoints, cluster scaling, instance GC and more) is woken for nothing.

The same class of bug has two more doors:
- **Controllers:** the runner commits a controller's changes in one transaction (`controllers/runner.go`), so a
  controller that proposes an unchanged put also writes and wakes everything watching that key, itself included.
  M243's testbed finding F1 (the scheduler re-putting unplaced facts, about 80 puts/s) was exactly that, fixed in the
  scheduler only.
- **Tests:** `EtcdStore.Put` skips unchanged values, but etcd transactions do not. `MemoryStore` skips unchanged puts
  in transactions too (`store/memory.go`, `executePutOperation`), so no unit test on the memory store can see this
  kind of write loop. M243 3g.1 needed a test-only `etcdLikeStore` wrapper to prove F1.

Kubernetes avoids this at both ends: the kubelet's status manager only sends a changed pod status, and the API server
treats an update that changes nothing as a no-op (no new resourceVersion, no watch event).

## What Changes

- **Agent:** `publishInstanceStateUnlessStopped` does not write when the stored state already equals the observed
  state. The rest of `publishInstanceStateToStore` already uses `Put`, which skips unchanged values.
- **Runner:** before capping and committing, the runner drops a proposed put whose value is byte-equal to the value
  of the same key in the facts it scanned for that reconcile, like the Kubernetes API server's no-op update. A put of a
  key that was not scanned is kept.
- **Memory store:** a put of an unchanged value inside `Transaction` gets a new revision and a watch event, as in etcd.
  `MemoryStore.Put` keeps skipping unchanged values, as `EtcdStore.Put` does. Test helpers that emulated etcd for this
  (the scheduler's `etcdLikeStore`) are removed.
- **Milestones (done with this proposal):** the M265 text is corrected: input-key guards exclude read-only prefixes
  such as `observed/instance/` by design (`appendInputKeyGuards`); they are not capped.

Out of scope:
- Reading all watched prefixes at one revision (M265).
- Write-rate metrics or alerts for etcd; the deploy check in tasks.md measures the rate by hand.
- Other agent writes that already skip unchanged values (`Put`, create-only transactions such as `created-at`,
  volume home node, VXLAN subnet) and `PublishAliveState`, which runs only at agent start.

## Capabilities

### New Capabilities
- `fact-store-writes`: when components write facts: an unchanged value is never rewritten by the agent or through
  the controller runner, and the test store treats unchanged puts in transactions as etcd does.

### Modified Capabilities

## Impact

- `agent/agent.go` (`publishInstanceStateUnlessStopped`), `controllers/runner.go` (new filter before
  `validateAndCapChanges`), `store/memory.go` (`executePutOperation`, `transactionWouldMutateState`).
- Tests across packages that use `MemoryStore` transactions may start to see events they did not see before; each
  such failure is either a real write loop (fixed in this change) or a test that assumed the old behaviour (adjusted).
- Graphs: CALL_GRAPH, INSTANCE_LIFECYCLE_GRAPH (agent), CONTROLLER_DATA_FLOW_GRAPH, HA_GRAPH, EXTENSIBILITY_GRAPH
  (runner).
- No API, DSL or store-key change. No BREAKING change.
