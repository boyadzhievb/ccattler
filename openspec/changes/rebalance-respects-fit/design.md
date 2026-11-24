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

### D4. A move keeps its ID and accepts a short, safe backend gap (user decision on N1, 2026-11-23)

A move is stop-old-then-start-new for one instance ID: the source removes its container when its desired set no
longer holds the instance, the target starts it when it sees the placement. Between the two the service has one
backend fewer: a multi-instance service serves from its other instances, a single-instance service has a short
outage. Option A (chosen): keep this and make the gap safe and visible; zero-gap moves (a new instance on the
target, started and ready before the old one stops, as a rollout does) are a later milestone. Safeguards:

- **Source never leaves it looking running.** After removing a moved instance's container, the source agent writes
  `observed/instance/{id}/state = pending` and deletes `observed/instance/{id}/probe/readiness`, in one transaction
  that compares the revisions of `observed/instance/{id}/node` (read as naming the source) and of the state. The
  agent's state publish writes `node` in the same transaction as `state`, so once the target published, `node`
  changed and the source's transaction fails; the target's publish always wins. `pending` is not final (only
  `stopped` is), so the target can still publish. The endpoint controller builds endpoints only for `running`
  instances, so the source's backend leaves the VIP.
- **A source that is behind never overwrites the target (review round 2 R2-1).** A source still in a long cycle
  (pull, init) that read its desired set before the move would otherwise publish `running` + `node = source` after
  the target did, and its next cleanup would mark the instance `pending` while it runs on the target. Every agent
  state publish therefore also compares the revision of `placement/node/{self}/{id}` that its cycle's placement scan
  read (`placedInstanceInfo.placementRevision`); the move deletes that key, so the late publish fails. The IP is
  resolved before anything is written, and the marker, service, `ip` and `network/allocation/{id}` (only those
  whose stored value differs, so etcd sees no extra writes) go into that same guarded transaction: a move can
  never land between the state and the IP (3c.6). `created-at` stays a separate create-only write. The status
  reason, last exit and secret-wait status are written only after a committed publish, and the probe pass's probe,
  health and progress writes compare the same placement revision (`placementGuard`), so an in-flight source probe
  cannot write `ready` after the move (R3-S1, R3-S2).
- **The rule (review round 4, 3e): all per-instance observed writes are guarded by placement.** Every agent write
  or delete under `observed/instance/{id}/` goes through `instanceFactWriter.writeGuarded` (the scanned placement
  revision, no-op changes dropped) or the guarded publish transaction, which carries the facts of the same publish
  (status reason, start facts, last exit, exit reason, pull and secret-wait status). Telemetry reads the placement
  first. Exceptions: the `stopped` write (on the `placement/instance/{id}` revision the move check read, round 5 P2),
  the source's `pending` after a move (its own node and state guard), and cleanup of an instance that left the
  desired set (only while it is not placed on another node; instance GC deletes `network/allocation/{id}` with the
  tree, B3). Host ports go in the state publish; when some do not fit under the transaction limit, the agent
  publishes `starting` (node = self, with the marker, service, IP and allocation, readiness deleted, guarded) first,
  then writes them in guarded chunks, then publishes; a failure in either step skips the rest (round 6 R6-1, round 7
  R7-1, round 8 R8-1); start facts a store error
  kept out are carried by the next running publish (B2) and dropped once the instance is no longer placed on this node
  (R6-4). A `stopped` write that fails because the instance moved during the stop is replaced by the source's
  `pending` (R6-3). A closed-world scan of `agent/*.go` (`TestAgentInstanceWritesAreGuarded`, T1, R6-2) checks every
  store Transaction against a list keyed by receiver and name, with reasons and Transaction counts, and every
  Put/Delete key for a direct non-per-instance `types.Key*` call; it cannot see writes through other packages or
  reflection, nor what an allow-listed Transaction's ops write: the audit of real store operations in the agent tests
  (3g.0) covers that. Store errors
  in these transactions are logged as errors with the error, not as "changed meanwhile" (R2-3).
- **A failed target startup never looks healthy.** The target publishes `image_pull_backoff` (pull failure) or
  `failed` (start failure, replaced by the failure controller) with `node` = target. A dead target agent publishes
  nothing: the instance stays `pending` with its placement on the dead node, which node-failure handling marks
  (`markInstancesOnUnreachableNodesAsFailed` reads placements and counts `pending` as active).
- **No flapping.** A moved instance is not moved again within `rebalanceMoveCooldown` (10 min). The scheduler
  records the move time in `derived/scheduler/rebalanced/{id}` (RFC3339, in the move's change group), reads it
  back from the facts (the clock is injectable, so a reconcile is deterministic given facts and time), and deletes
  it once the instance is gone, within what the unplaced facts left of their shared 40-change budget (M262 D4), so a
  cycle without placements writes at most 51 changes and the runner's 60 leave room for placements (R2-2). A move
  still needs a strict count improvement (`nodeLoad[target] < nodeLoad[source]-1`). Clock skew between HA leaders
  shifts the cooldown by at most the skew (a move time in the future lengthens it); accepted, no code (R2-4).
- **Removal is never evidence of the new placement running.** Only the target's own publish (`running` with
  `node` = target, then readiness) brings the instance back into endpoints.

Change during apply, recorded 2026-11-24 (main session): in rework 3f, the "all per-instance observed writes are
guarded" bullet was updated to describe the round-5 fixes (B1 chunked host ports, B2 carried start facts, B3
allocation GC, T1 closed-world scan, P2 `stopped` on the placement revision), as 3e.3 did for round 4. It describes
fixes already decided in the tasks.md round-5 table. No requirement or spec scenario changed.

Change during apply, recorded 2026-11-24 (lead-developer subagent, rework 3g): the same bullet now describes the
round-6 fixes decided in the tasks.md round-6 table (R6-1 host ports in the state publish with only the overflow
written first, R6-2 scan keyed by receiver and resolved imports plus the store-operation audit, R6-3 pending after a
move during the stop, R6-4 start facts dropped when the instance leaves). Residual of the R6-1 design, as the main
session specified it: with more host ports than fit in one transaction, overflow ports can commit and the publish
then fail, so a move target's overflow ports sit next to the source's `running` and `node` until the next publish. No
requirement or spec scenario changed.

Decision recorded 2026-11-24 (user, round 7 R7-1; implemented in rework 3h): the residual of the 3g note is not
accepted. The user's words: "make instance pending on 90 ports instances". Implemented as `starting` rather than
`pending`: both are non-serving (endpoints are built only for `running`), but scale-down removes `pending` instances
first (`controllers/instance.go:544`), so marking a starting target `pending` would make it the first victim of a
scale-down. With an overflow the agent publishes `starting` with node = self through
`publishInstanceStateUnlessStopped` (state-revision compare, placement guard, stopped check), then writes the overflow,
then does the final publish. The residual above no longer exists; the sweep's exemption (`isOverflowHostPortFact`)
is replaced by the invariant "`running` on node N ⇒ N's host ports". No requirement or spec scenario changed.

Change during apply, recorded 2026-11-24 (user decision on round 8, option a: fix now; implemented by the
lead-developer subagent in rework 3i): the intermediate `starting` publish carries the facts already resolved for
the publish (marker, service, `ip`, `network/allocation/{id}`) and deletes readiness, in the same guarded
transaction (at most 5 changes beside state and node, no host ports), so "a move can never land between the state
and the IP" (3c.6) holds on the overflow path too (R8-1). No requirement or spec scenario changed.

Change during apply, recorded 2026-11-24 (user decision on round 9, option b; implemented by the lead-developer
subagent in rework 3j): on the overflow path the final publish filters its facts again after the `starting` publish,
so it does not re-put what `starting` committed and "etcd sees no extra writes" above holds there too (S1). An empty
or unresolved IP (no `ip` put, the source's IP stays) is its own bug milestone, M278. No requirement or spec
scenario changed.

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
