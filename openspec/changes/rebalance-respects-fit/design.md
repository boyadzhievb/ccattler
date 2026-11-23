# Design

## Context

Call graph: [SCHEDULING_GRAPH.md §4](../../../design/graphs/SCHEDULING_GRAPH.md) (`rebalancePlacedInstances` →
`executeRebalanceMoves` → `selectRebalanceTarget` → `buildRebalanceMoveChanges`), and the agent cleanup in
[INSTANCE_LIFECYCLE_GRAPH.md](../../../design/graphs/INSTANCE_LIFECYCLE_GRAPH.md) (`cleanupUndesiredInstance`,
`cleanupStoppedStaleInstance`). Flow: [runtime-invariant-flows.md §10](../../../design/runtime-invariant-flows.md)
(scheduler bullet) and Flow 34's rule "the agent never publishes over a stop" (M241 2i.1). This change adds the
rebalance move as its own flow entry: scheduler moves → source agent removes without `stopped` → target agent starts.

Current state, verified in the code and live (deploy cluster, 2026-11-23):
- `rebalancePlacedInstances` runs when the instance-count spread exceeds `rebalanceLoadImbalanceThreshold`. It
  already builds `aliveCandidates` with each node's remaining `availCPU`/`availMemory` (after placed requests), but
  `selectRebalanceTarget` only filters by constraints, the per-service spread and the count; it never compares the
  instance's requests with the remaining capacity, and `executeRebalanceMoves` updates only the counts after a move.
- Placement checks fit in `node_heap.go` (`requiredCPU > 0 && candidate.availCPU < requiredCPU`, same for memory).
- The eviction controller writes `derived/node/{id}/pressure` above 90% real memory use and deletes it below 80%.
  The scheduler watches `observed/node/` but not `derived/node/`.
- The agent's `cleanupUndesiredInstance` (and `cleanupStoppedStaleInstance` for an exited container) write
  `observed/instance/{id}/state = stopped` for every container whose instance is no longer in this node's desired
  set, including one whose placement moved to another node. Since M241, `stopped` is final for agents
  (`publishInstanceStateUnlessStopped`), so the target agent cannot publish `running`, and the instance controller
  replaces the instance. Seen live: `f918761b34` rebalanced ctrl → worker-2 at 16:13:34, started on worker-2 at
  16:13:36, replacement `1685da89c2` created at ~16:13:40.

## Goals / Non-Goals

**Goals:**
- A rebalance move goes only to a node with room for the instance's requests and without memory pressure.
- A moved instance keeps running under its ID; the source agent never declares it stopped.
- The deploy run detects instance churn even when its final state looks healthy.

**Non-Goals:**
- Balancing by resources instead of by count; placement avoiding pressured nodes; the agent's dropped watch events.
- Changing how rebalance chooses which instance to move (pinned, stateful and volume services stay excluded).

## Decisions

### D1. The rebalance target needs the placement fit and no pressure marker

`selectRebalanceTarget` gets the instance's requests (`serviceResources[service]`, millicores and MiB as placement
uses) and skips a candidate whose `availCPU`/`availMemory` is below them, using the same comparison as
`node_heap.go` (extracted into one shared `candidateFitsRequests` helper so placement and rebalance cannot drift; the
existing check is replaced by the helper, not duplicated). The scheduler adds `derived/node/` to its `Watch()` and
skips candidates with a pressure marker (`types.KeyDerivedNodePressure`). After each move, `executeRebalanceMoves`
subtracts the requests from the target's remaining capacity and adds them back to the source, so later moves in
the same cycle see the room already taken.

Alternative: rebalance by resources (least-allocated). Rejected for this bug fix: it changes what "balanced" means
for every cluster; recorded as a follow-up question.

### D2. The source agent does not stop a moved instance

At the start of `cleanupUndesiredInstance` and `cleanupStoppedStaleInstance`, before it removes anything, the agent
reads `placement/instance/{id}` (on a read error it does nothing this cycle and retries next cycle). If it names another node and no `derived/instance/{id}/controller_stopped = true` exists,
the agent removes its container, releases its local IP and local state as today, and skips the `stopped` write. In
every other case (no placement, placement on this node, controller stop) it writes `stopped` as today.

Alternative: make rebalance an eviction (controller stop + replacement placed by the scheduler), as Kubernetes'
descheduler does. Rejected: it creates a new instance ID, endpoint and secret delivery for every move, and the
scheduler could put the replacement back on the same node; the move semantics already exist and are cheaper once
the source agent stops sabotaging them.

### D3. The deploy run counts instance churn

`cluster_test.sh` records the set of whoami instance IDs seen in `/api/status` at each step it already polls, and
at the end compares the number of distinct IDs with the IDs the test's own steps explain (initial instances, the
2→1→2 scale step, the node-failure replacement, plus a small named margin). Above that, it fails with the list of
IDs, so a loop fails the run even if the final state is healthy. The bound is a named constant with a comment.

## Failure cases

| Case | What happens |
|---|---|
| No node has room for any movable instance | No move; the imbalance stays and is logged as today (warning once per cycle). |
| Target gains pressure after the move | Eviction may evict there as for any instance; the next rebalance skips that target (D1). |
| Source agent cannot read the placement (store error) | It decides before it removes anything: on a read error it leaves the container and the store as they are, logs, and retries the whole cleanup next cycle (the container is still in its stale set). Corrected after implementation: the first version removed the container first and then skipped the `stopped` write, which left a really undesired instance `running` with no container and nothing to retry. |
| Placement moves back to the source before its agent reconciles | The instance is in the source's desired set again; nothing is removed. |
| Instance is moved, then scaled away before the target starts it | The controller stop marker exists, so whichever agent removes it writes `stopped` (D2). |
| Target agent is down after the move | The instance is not started there; node-failure handling replaces it as for any instance on a dead node. |

## Risks / Trade-offs

- D2 adds one store read per removed container on the source agent (not per cycle per instance): negligible.
- D3's bound must not make normal runs flaky; it is derived from the test's own steps with a margin, and the first
  runs record the observed counts in tasks.md before the bound is tightened.
