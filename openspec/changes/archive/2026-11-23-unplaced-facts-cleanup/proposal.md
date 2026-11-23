# Proposal

Milestone: **M262 — Unplaced-Demand Facts Outlive Their Instance** (milestones.md Phase 262, bug).

## Why

The scheduler writes `derived/scheduler/unplaced/{id}/reason` and `/requirements` for each pending instance it cannot
place. `cleanupStaleUnplacedFacts` (`scheduler/scheduler.go`) deletes them only when the instance gets a placement.
Nothing else deletes them, instance GC included. An unplaced instance that is stopped (scaled away, controller-stopped
by a rollout or a scale-down) or removed leaves both facts behind for good. Seen on the deploy cluster (M243 4.1
rerun, 2026-11-23): `cca scale m243big 0` stopped both unplaced instances and no delete followed.

The cluster-autoscale controller (`controllers/clusterscale.go`, `extractUnplacedDemand`) counts every
`insufficient_capacity` / `no_nodes` fact as demand without checking that its instance still waits. Today
`cca server` does not run that controller (M260), so the leftovers only clutter the store. Once M260 wires it in,
each leftover becomes a phantom capacity request: a node is asked for an instance that no longer exists. M262 must
land before or with M260.

Kubernetes has the same shape: a Pod's `PodScheduled=False, reason=Unschedulable` condition lives on the Pod object,
so it disappears with the Pod, and the cluster autoscaler only considers Pods that still exist and are still
unschedulable. CCattler keeps the condition in a separate fact, so its lifetime has to be tied to the instance
explicitly.

## What Changes

- **Scheduler:** each cycle deletes the unplaced facts of every instance that is not waiting for a placement in that
  cycle: placed (stored or this cycle, as today), stopped (including `controller_stopped`), in any other non-pending
  state, or gone from the store. Facts of an instance that still waits are kept and, since 3g.1 of M243, written
  only when they change.
- **Cluster autoscale controller:** counts an unplaced fact as demand only when its instance still exists, is pending,
  is not controller-stopped and has no placement. This closes the window between an instance stopping and the next
  scheduler cycle, and protects against any leftover written by an older version.
- **Upgrade:** no migration step. Leftovers already in a store are deleted by the first scheduler cycle after the
  upgrade.

Out of scope:
- Running the cluster-autoscale controller in `cca server` (M260).
- Showing the unplaced reason in `cca status`, `cca describe service` and `cca get instances` (M261).
- Cancelling capacity requests already created for instances that are gone (the controller's existing cleanup of
  ready requests is unchanged); noted in the design as a follow-up for M260.

## Capabilities

### New Capabilities

### Modified Capabilities
- `resource-requests`: adds a requirement that unplaced-demand facts exist exactly while their instance waits for a
  placement, and that cluster scaling counts only those instances.

## Impact

- `scheduler/scheduler.go`: `cleanupStaleUnplacedFacts` (keyed on this cycle's waiting set instead of placements).
- `controllers/clusterscale.go`: `extractUnplacedDemand` / `emitScaleUpRequests` filter by instance state.
- `design/graphs/SCHEDULING_GRAPH.md` and the cluster-scale graph entries; `design/runtime-invariant-flows.md`
  section 10 (scheduler bullet).
- No API, DSL or store-key changes. No BREAKING change.
