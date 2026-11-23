# Design

## Context

Call graph: [SCHEDULING_GRAPH.md](../../../design/graphs/SCHEDULING_GRAPH.md) §1 and §6 (Reconcile,
`emitUnplacedDemandFacts`, `cleanupStaleUnplacedFacts`), and
[CONTROLLER_DATA_FLOW_GRAPH.md](../../../design/graphs/CONTROLLER_DATA_FLOW_GRAPH.md) (cluster-autoscale).
Flow: [runtime-invariant-flows.md §10](../../../design/runtime-invariant-flows.md) (scheduler bullet: no fit → unplaced
`insufficient_capacity` → cluster scaling, rollout reason). This change adds the facts' lifetime to that bullet; no
flow is added.

Current state, verified in the code:
- `Scheduler.Reconcile` builds `unplaced = findUnplacedPendingInstances(instances, placements)`: state `pending` and no
  stored placement. `extractInstanceInfoFromFacts` already turns `derived/instance/{id}/controller_stopped = true` into
  `stopped` (M241), so a controller-stopped instance is never in `unplaced`.
- `emitUnplacedDemandFacts` writes facts for `unplaced \ placedInThisCycle`, skipping unchanged values (M243 3g.1).
- `cleanupStaleUnplacedFacts(facts, placements, placedInThisCycle)` deletes stored facts only for IDs with a stored
  placement or placed this cycle. Stopped, otherwise non-pending and removed instances are never cleaned.
- `ClusterAutoscaleController.Reconcile` → `emitScaleUpRequests` → `extractUnplacedDemand(facts)` reads every fact
  under the prefix; `filterInsufficientCapacityDemand` keeps `insufficient_capacity` and `no_nodes`. It already
  watches `observed/instance/`, `derived/instance/` and `placement/`, so it has the facts for an instance check.
- The runner does not split change sets: it commits at most `maxTransactionChanges = 60` per cycle (under etcd's
  128-operation limit), sorted by (Group, Key), and defers the rest; ungrouped changes such as these deletes sort
  ahead of the grouped `placement/instance/` puts. Its contract (`controllers/runner.go:44-55`) is that each
  controller caps its own output. (Corrected after review 3.3; the first version of this design said the runner
  splits.)

## Goals / Non-Goals

**Goals:**
- Unplaced-demand facts exist exactly while their instance waits for a placement (spec requirement 1).
- Cluster scaling never counts an instance that no longer waits, even inside the window before the scheduler's next
  cycle or with facts an older version left (spec requirement 2).

**Non-Goals:**
- Running cluster scaling in `cca server` (M260), showing the reason in the CLI (M261).
- Cancelling capacity requests already written for an instance that has since gone. Today a request becomes `ready`
  and is cleaned once a node joins; a request whose instances are all gone keeps going. Recorded for M260, which
  makes requests reachable in production.

## Decisions

### D1. The scheduler deletes facts for every instance outside this cycle's waiting set

`waiting = unplaced \ placedInThisCycle` is exactly the set `emitUnplacedDemandFacts` writes for. Cleanup deletes the
reason and requirements of every stored ID not in `waiting`. This covers placed (as today), stopped, controller-stopped,
any other state and removed instances with one rule, and it can never delete a fact the same cycle writes (the sets
are complementary), so `findDuplicateChangeKey` cannot fire. `cleanupStaleUnplacedFacts` takes the waiting set instead
of `placements` and `placedInThisCycle`; the waiting set is computed once in `Reconcile` and passed to both functions.

Alternatives:
- Instance GC deletes the facts when it removes an instance: covers removal only, not stop or scale-down (the instance
  stays until GC), and makes a second writer for a scheduler-owned key. Rejected.
- A TTL lease on the facts: etcd leases would expire facts of instances that still wait while the scheduler is not
  leader, and the memory store has no leases. Rejected.
- Kubernetes keeps `PodScheduled=False` as a condition on the Pod, so it dies with the Pod. CCattler's equivalent
  would be `observed/instance/{id}/unplaced_reason`, but `observed/` belongs to agents. Moving the key is a larger
  change for the same result; rejected.

### D2. Cluster scaling checks that the instance still waits

`extractUnplacedDemand` keeps an entry only when `observed/instance/{id}/state` is `pending`,
`derived/instance/{id}/controller_stopped` is not `true` and `placement/{id}` does not exist. The check reads facts
the controller already watches and scans. It is about 10 lines; the scheduler's `extractInstanceInfoFromFacts` does
the same work for its own types in another package, which is two places, below the 3+ duplication threshold. If a
third reader appears, a shared `types`-level helper is the follow-up.

Alternative: rely on D1 alone. Rejected: the scheduler and the cluster-autoscale controller run independently (rule 1,
no component may assume another acted), so between an instance stopping and the scheduler's next cycle the controller
would still see the fact.

### D3. No migration

A leftover has no instance or a non-pending one, so D1 deletes it on the first scheduler cycle after the upgrade and
D2 ignores it meanwhile. A separate migration would duplicate D1.

### D4. Cleanup is capped per cycle (added after review 3.3)

Without a cap, cleanup proposes 2 deletes for every instance that stopped waiting. Above about 30 instances (a large
`cca scale … 0`, or the first cycle after an upgrade), the ungrouped deletes fill the runner's 60-change budget and
every placement of that cycle is deferred, cycle after cycle, with a "deferred atomic change groups" warning each
time. Following the runner's contract, cleanup takes at most `maxUnplacedCleanupsPerCycle = 20` instances (40
deletes) per cycle, in sorted ID order, leaving at least 20 changes for placements. Kubernetes controllers bound
their work per sync the same way (for example the ReplicaSet controller's `burstReplicas`).

Naming (review suggestions taken): `extractUnplacedDemand` becomes `extractWaitingUnplacedDemand`, since it now
filters and the rollout controller calls it too; `endpointEligibleState` becomes a neutral
`stateWithControllerStop`, documented for both callers (endpoints and cluster scaling), so a later endpoint-only
change does not silently change cluster-scaling demand.

## Failure cases

| Case | What happens |
|---|---|
| Scheduler not running (no leader, restart) | Leftovers stay; cluster scaling ignores them (D2); the next leader's first cycle deletes them (D1). |
| Delete transaction fails (CAS conflict, etcd unavailable) | The runner retries with back-off; the next cycle recomputes the same deletes (idempotent). |
| Instance goes pending → stopped → pending (in-place restart keeps the ID) | Facts deleted, then written again on the cycle that sees it pending and unplaced; two writes, no loop. |
| Many instances stop waiting at once, or thousands of leftovers after an upgrade | Cleanup deletes at most `maxUnplacedCleanupsPerCycle` (20) instances' facts per cycle (40 changes), lowest IDs first, so placements keep room in the runner's 60-change budget (D4). Each delete wakes the scheduler, so the rest go over ceil(N/20) cycles; cluster scaling ignores them meanwhile (D2). |
| Instance stops after cluster scaling read it as pending in the same snapshot | One capacity request may still be written (pre-existing race); the next cycle ignores the instance. Cancelling such requests is the M260 follow-up. |
| Fact with an unknown suffix under the prefix | `extractExistingUnplacedInstanceIDs` still yields its ID; D1 deletes only `reason` and `requirements`, as today. Unchanged behaviour. |

## Risks / Trade-offs

- Existing cluster-autoscale tests build unplaced facts without instance facts; with D2 they would see no demand.
  Their fixtures gain a pending instance (task 2.3). That is a test change, not a behaviour change for real stores,
  where the scheduler only writes facts for pending instances.
- Deletes now happen for every stopped unplaced instance, which adds two watch events per instance stop under the
  prefix. The rollout and cluster-autoscale controllers watch it; each wakes once. Acceptable.
