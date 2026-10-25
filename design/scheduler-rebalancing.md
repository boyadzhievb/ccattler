# Scheduler Rebalancing, Grace Period, and Pressure Detection

## Problem Statement

When a CCattler cluster starts (or restarts all agents), the scheduler places
all pending instances on the first node to register. This creates a
thundering-herd problem: one node gets all workloads while others sit idle.
The overloaded node can become unresponsive (SSH timeout, etcd failures),
creating a deadlock where workers can't receive placements because the
server's etcd is unreachable.

**Observed incident (2026-10-25):** 3-node Airflow cluster restart placed all
6 containers on `cca-deploy-ctrl` (4 CPUs, 4GB RAM). The node became
unresponsive within 2 minutes, etcd connections from workers returned EOF,
and the cluster required manual intervention (virsh force-off + restart).

## Solution: Three Complementary Features

### 1. Initial Placement Grace Period

**What:** The scheduler waits `initialPlacementGracePeriod` (10 seconds)
after first seeing pending instances with no existing placements before
placing anything.

**Why:** On a fresh cluster, the server starts and agents register over
several seconds. Without the grace period, the scheduler sees the first
agent and immediately places everything there. Waiting 10 seconds allows
all agents to register so the least-loaded algorithm can spread evenly.

**When it activates:**
- Zero existing placements in the store AND pending instances exist
- Does NOT activate during scale-up (existing placements present)
- Does NOT activate during rolling restart (existing placements present)
- Resets after placement completes

**Implementation:**
- `Scheduler.firstPendingSeenAt` tracks when pending instances first appeared
- `Scheduler.placementGracePeriod` is configurable (default 10s, 0 in tests)
- Logs `WARN` when grace period starts and `INFO` when it expires

### 2. Reactive Rebalancing

**What:** After initial placement, the scheduler detects load imbalance and
migrates instances from overloaded to underloaded nodes.

**Algorithm:**
1. Count placed running/pending instances per alive node
2. Compute max and min load across alive nodes
3. If `maxLoad - minLoad <= rebalanceLoadImbalanceThreshold` (2), skip
4. Compute `idealLoad = totalInstances / aliveNodeCount`
5. Identify overloaded nodes: `load > idealLoad + 1`
6. For each overloaded node, find movable instances (see eligibility below)
7. Select target: least-loaded node that satisfies service constraints
8. Only move if `targetLoad < sourceLoad - 1` (prevents oscillation)
9. Cap at `maxRebalanceMovesPerCycle` (2) moves per reconciliation

**Instance eligibility for rebalancing:**
- State must be `running` (not pending, stopped, failed)
- Not part of a service group (co-scheduled instances stay together)
- Not placed in the current reconciliation cycle (no place-then-move)
- Service constraints must be satisfiable on the target node

**Constraint enforcement during rebalance:**
- Architecture: target node must match service's required architecture
- Zone policy: respected during target selection
- Require labels: target node must have all required label key-value pairs
- Restrict/accept: target node's restrictions must be accepted by the service

**Edge case — only valid target is restricted/tainted:**
If no constraint-compatible underloaded node exists, the instance stays put.
The rebalancer never forces placement onto an incompatible node.

**Move mechanics (store changes):**
```
PUT  placement/instance/{id}         = targetNode  (overwrites old value)
DEL  placement/node/{source}/{id}                  (remove reverse index)
PUT  placement/node/{target}/{id}    = ""          (create reverse index)
```
All three changes share a change group (`rebalance/{id}`) for atomic
application in the store transaction.

**Convergence:** With 2 moves per cycle and reconciliation running every
~1 second, a 6-instance imbalance resolves in 3 cycles (3 seconds). This
is fast enough to be invisible to users but slow enough to prevent
oscillation.

### 3. Node Allocation Pressure Detection

**What:** The scheduler computes each alive node's resource allocation ratio
and logs a `WARN` when it exceeds `nodeAllocationPressurePercent` (80%).

**Why:** Early warning that a node is approaching its limits, before it
becomes unresponsive. Operators monitoring logs can intervene (add nodes,
scale down) before an incident occurs.

**Calculation:**
```
cpuPercent    = (usedCPU[nodeID] * 100) / node.availCPU
memoryPercent = (usedMemory[nodeID] * 100) / node.availMemory
```
Where `usedCPU` / `usedMemory` are summed from placed instance resource
requirements, and `availCPU` / `availMemory` are the node's reported total
capacity.

**Log output example:**
```
WARN  scheduler: node under allocation pressure node=ctrl cpu_allocation_percent=87 memory_allocation_percent=92 threshold_percent=80
```

## Configuration

All thresholds are compile-time constants in `scheduler/scheduler.go`:

| Constant | Default | Description |
|----------|---------|-------------|
| `initialPlacementGracePeriod` | 10s | Wait time before initial placement |
| `maxRebalanceMovesPerCycle` | 2 | Max instance migrations per reconcile |
| `rebalanceLoadImbalanceThreshold` | 2 | Min max-min difference to trigger rebalance |
| `nodeAllocationPressurePercent` | 80 | Allocation % that triggers pressure warning |

## Test Coverage

| Test | Feature | Scenario |
|------|---------|----------|
| `TestGracePeriodDefersInitialPlacement` | Grace | First call with 0 placements blocks |
| `TestGracePeriodNotAppliedWithExistingPlacements` | Grace | Scale-up is not deferred |
| `TestGracePeriodExpiresAndPlaces` | Grace | Placement proceeds after expiry |
| `TestRebalanceSpreadsFromOverloadedNode` | Rebalance | 4 on 1 node, 2 empty → moves 2 |
| `TestRebalanceSkipsBalancedCluster` | Rebalance | 1 per node → no moves |
| `TestRebalanceSkipsGroupedInstances` | Rebalance | Co-scheduled → not moved |
| `TestRebalanceRespectsConstraints` | Rebalance | Architecture mismatch → no move |
| `TestRebalanceMaxMovesPerCycle` | Rebalance | 8 on 1 node → exactly 2 moves |
| `TestRebalanceMoveTargetsLeastLoaded` | Rebalance | First move targets empty node |

## Architecture Diagram

```
Reconcile()
  ├─ Extract facts (nodes, instances, placements, constraints)
  ├─ Find unplaced pending instances
  │
  ├─ Grace Period Check ──────────────────┐
  │  Is this the first batch?             │
  │  Are we within the grace window?      │
  │  YES → skip placement, emit unplaced  │
  │  NO  → proceed                        │
  │                                       ▼
  ├─ attemptPlacements() ← existing logic (least-loaded, constraint-aware)
  │
  ├─ Emit unplaced demand facts
  ├─ Cleanup stale unplaced facts
  │
  ├─ Rebalancing ─────────────────────────┐
  │  Count load per alive node            │
  │  max - min > threshold?               │
  │  YES → find overloaded nodes          │
  │      → find movable instances         │
  │      → select constraint-aware target │
  │      → emit move changes (max 2)      │
  │                                       ▼
  ├─ Pressure Detection ──────────────────┐
  │  For each alive node:                 │
  │  allocation > 80%? → log WARN         │
  │                                       ▼
  └─ Return all changes
```

## Future Improvements

- **Proactive rebalancing on node join:** Instead of waiting for the next
  reconcile cycle, trigger immediate rebalancing when a new node transitions
  to alive state.
- **Resource-weighted rebalancing:** Currently rebalancing counts instances.
  A future version could balance by actual resource usage (CPU millicores,
  memory MiB) for heterogeneous workloads.
- **Configurable grace period per cluster:** Allow operators to tune the
  grace period based on their cluster size and agent startup time.
- **Agent-reported system pressure:** Add actual system metrics (cgroup
  memory.pressure, /proc/meminfo) as store facts for real-time pressure
  detection beyond allocation ratios.
