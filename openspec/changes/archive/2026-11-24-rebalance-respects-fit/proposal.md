# Proposal

Milestone: **M267 — Rebalancing Moves Instances Onto Nodes They Don't Fit** (milestones.md Phase 267, bug).

## Why

The M262 closing full run 45211387656 failed because whoami looped on the deploy cluster every ~6 s, about 40
replacements in 4 minutes, still running after the run ended. Traced live (2026-11-23):

1. Placement puts a new whoami instance on `cca-deploy-ctrl` (9–10 instances; the only node with room for its
   requests).
2. The scheduler's rebalancing sees an instance-count imbalance (ctrl 9–10, worker-2 3–4) and moves it to
   `cca-deploy-worker-2`: `selectRebalanceTarget` picks by instance count only and never checks that the instance's
   CPU and memory requests fit, though worker-2's memory allocation is at 99–101%.
3. ctrl's agent removes its container, because the instance is no longer placed there, and writes
   `observed/instance/{id}/state = stopped` (`cleanupUndesiredInstance`), although the same instance ID is now placed
   on worker-2. Since M241 a stopped state is final, so worker-2's agent may not publish `running` ("instance stopped
   meanwhile"), and the instance controller creates a replacement, which placement puts on ctrl again.

So every rebalance "move" so far has been a stop plus a new instance, and with no fit check it never ends. Both
causes predate M262. The loop became reachable with M243, which made requests count (before, every request was 0).

Kubernetes does not move pods; its descheduler evicts a pod only if another node fits it (`NodeFit`), and the
scheduler places the replacement with the normal checks. CCattler moves the instance in place (same ID, new
placement). That is cheaper (no new identity, endpoint or secrets) and stays, with both rules repaired.

## What Changes

- **Scheduler:** a rebalance target must fit the instance's CPU and memory requests against the node's remaining
  capacity (the check placement uses, `node_heap.go`), and must not carry eviction's `derived/node/{id}/pressure`
  marker. The remaining capacity is updated after each move in a cycle, so two moves cannot fill the same room.
- **Agent:** when it removes a container whose instance is now placed on another node, the agent does not write
  `stopped` for that instance ID; it only removes its local container and local state. It still writes `stopped`
  when the instance has no placement or is placed on this node (scale-down, controller stop).
- **Deploy check:** the Deploy Testbed counts whoami instance IDs during the run and fails on churn (more distinct
  IDs than the test's own scale and node-failure steps explain), so a loop fails the run even when the final state
  looks fine.

Out of scope:
- Whether rebalancing should balance by resources instead of instance count (a design question noted for later).
- Making placement avoid nodes under memory pressure (Kubernetes' memory-pressure taint); noted as a follow-up.
- The agent's "etcd watch event dropped (channel buffer full)" warning seen on ctrl during the loop; recorded in
  tasks.md for a separate milestone if it persists without the loop.

## Capabilities

### New Capabilities
- `instance-rebalancing`: when and where the scheduler moves a running instance to another node, and what the
  source and target agents do with a moved instance.

### Modified Capabilities

## Impact

- `scheduler/scheduler.go` (`selectRebalanceTarget`, `executeRebalanceMoves`, `rebalancePlacedInstances`).
- `agent/agent.go` (`cleanupUndesiredInstance`, `cleanupStoppedStaleInstance`).
- `test/e2e/cluster_test.sh` (churn count).
- Graphs: SCHEDULING_GRAPH §4 (rebalance), INSTANCE_LIFECYCLE_GRAPH / CALL_GRAPH (agent cleanup);
  runtime-invariant-flows (rebalance move as a flow).
- No API, DSL or store-key change. No BREAKING change.
