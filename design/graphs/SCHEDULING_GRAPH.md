# CCattler Scheduling & Placement Graph Reference

Machine-readable reference for the scheduler placement pipeline, rebalancing,
and per-node placement mode. Purpose: understand how instances land on nodes
without reading scheduler source files.

## Scheduling Pipeline Overview

```
                         RECONCILIATION CYCLE
                               │
                               ▼
                    ┌──────────────────────┐
                    │  extractFacts()      │
                    │  nodes, instances,   │
                    │  placements, rsrc,   │
                    │  constraints, groups │
                    └──────────┬───────────┘
                               │
                    ┌──────────▼───────────┐
                    │ findUnplacedPending   │  instances with state=pending
                    │ Instances()          │  and no placement key
                    └──────────┬───────────┘
                               │
               ┌───────────────┼───────────────┐
               │               │               │
     ┌─────────▼────────┐     │     ┌─────────▼────────┐
     │  Grace period?   │     │     │ partitionByGroup  │
     │ (first boot only)│     │     │ grouped vs ungrouped│
     └─────────┬────────┘     │     └─────────┬────────┘
               │ skip/proceed │               │
               └───────┬──────┘      ┌────────┴────────┐
                       │             │                  │
              ┌────────▼───────┐  ┌──▼──────────┐  ┌───▼─────────────┐
              │ placeGrouped   │  │ groupBy      │  │ placeBatched    │
              │ Instances()    │  │ Service()    │  │ Instances()     │
              │ (co-locate)    │  │              │  │ per service     │
              └────────┬───────┘  └──┬──────────┘  └───┬─────────────┘
                       │             │                  │
                       │         ┌───▼──────────────────▼───┐
                       │         │      FILTER STAGE        │
                       │         │  filterByConstraints()   │
                       │         │  (arch, zone, require,   │
                       │         │   accept, per-node)      │
                       │         └───────────┬──────────────┘
                       │                     │
                       │         ┌───────────▼──────────────┐
                       │         │      RANK STAGE          │
                       │         │  rankByPreferences()     │
                       │         │  (prefer labels → bonus) │
                       │         └───────────┬──────────────┘
                       │                     │
                       │         ┌───────────▼──────────────┐
                       │         │      HEAP STAGE          │
                       │         │  NodeCapacityCache       │
                       │         │  min-heap by loadScore   │
                       │         │  O(log n) per placement  │
                       │         └───────────┬──────────────┘
                       │                     │
                       │         ┌───────────▼──────────────┐
                       │         │     ASSIGN STAGE         │
                       │         │  BestNode() or           │
                       │         │  BestNodeInZones()       │
                       │         │  → write placement/      │
                       │         │    instance/{id}         │
                       │         └───────────┬──────────────┘
                       │                     │
              ┌────────▼─────────────────────▼───┐
              │      UNPLACED DEMAND FACTS       │
              │  emitUnplacedDemandFacts()        │
              │  → derived/scheduler/unplaced/    │
              └────────┬─────────────────────────┘
                       │
              ┌────────▼─────────────────────────┐
              │      REBALANCE STAGE             │
              │  rebalancePlacedInstances()      │
              │  detect imbalance → migrate      │
              │  up to 2 instances per cycle     │
              └────────┬─────────────────────────┘
                       │
              ┌────────▼─────────────────────────┐
              │      PRESSURE DETECTION          │
              │  detectAndLogNodePressure()      │
              │  warn if CPU/mem > 80%           │
              └──────────────────────────────────┘
```

## Fact Keys Read / Written

### Read (Watch prefixes)

| Prefix | Source | Data Extracted |
|--------|--------|----------------|
| `placement/instance/` | Scheduler (self) | existing placements: instanceID → nodeID |
| `placement/node/{id}/` | Scheduler (self) | reverse index: node → instances |
| `observed/node/{id}/` | Node agent | state, avail CPU/mem, arch, zone, labels, restrictions |
| `observed/instance/{id}/` | Node agent | service name, lifecycle state |
| `desired/service/{name}/` | Compiler | resources, placement constraints, groups |
| `desired/group/{name}/` | Compiler | service group memberships |
| `derived/scheduler/unplaced/` | Scheduler (self) | stale unplaced demand facts to clean up |

### Written

| Key Pattern | When | Value |
|-------------|------|-------|
| `placement/instance/{id}` | Instance placed | nodeID |
| `placement/node/{nodeID}/{instanceID}` | Instance placed | "" |
| `derived/scheduler/unplaced/{id}/reason` | Instance unplaceable | no_nodes / insufficient_capacity / unsatisfiable_constraint |
| `derived/scheduler/unplaced/{id}/requirements` | Instance unplaceable | JSON: {service, cpu, memory, architecture} |

## Placement Constraints

### servicePlacementConstraint struct (scheduler/scheduler.go:1128)

```
┌──────────────────────────────────────────────────────────────┐
│  servicePlacementConstraint                                   │
│                                                               │
│  mode         string            "" or "per-node"              │
│  architecture string            "" = any, "amd64", "arm64"    │
│  zonePolicy   string            "" = none, "spread", "us-1a"  │
│  require      map[string]string hard label match (all must)   │
│  prefer       map[string]string soft label match (bonus)      │
│  accept       map[string]bool   tolerate restricted nodes     │
└──────────────────────────────────────────────────────────────┘
```

### DSL Syntax → Fact Keys → Constraint Fields

| DSL Declaration | Fact Key | Constraint Field |
|----------------|----------|------------------|
| `placement { architecture amd64 }` | `desired/service/{s}/placement/architecture` | `.architecture` |
| `placement { zone spread }` | `desired/service/{s}/placement/zone` | `.zonePolicy` |
| `placement { zone us-east-1a }` | `desired/service/{s}/placement/zone` | `.zonePolicy` |
| `placement { require gpu=nvidia }` | `desired/service/{s}/placement/require/gpu` | `.require["gpu"]` |
| `placement { prefer tier=fast }` | `desired/service/{s}/placement/prefer/tier` | `.prefer["tier"]` |
| `placement { accept spot }` | `desired/service/{s}/placement/accept/spot` | `.accept["spot"]` |
| `placement { mode per-node }` | `desired/service/{s}/placement/mode` | `.mode` |

### Filter Evaluation Order (filterByConstraints, :1218)

```
  for each candidate:
    1. architecture: skip if constraint.arch != "" && node.arch != constraint.arch
    2. zone:         skip if zonePolicy != "spread" && node.zone != zonePolicy
    3. require:      skip if node missing any required label key=value
    4. accept:       skip if node has restriction label not in service's accept set

  fallback: if all candidates filtered out → return original unfiltered list
```

### Node Labels and Restrictions

Nodes report labels and restrictions via facts:

```
observed/node/{id}/label/{key}     = value     ← for require/prefer matching
observed/node/{id}/restrict/{key}  = value     ← blocks scheduling unless service accepts
```

Restrictions act as inverse taints: a restricted node rejects all services
that don't explicitly `accept` the restriction label.

## Per-Node Placement (M160)

### Data Flow

```
desired/service/{s}/placement/mode = "per-node"
        │
        ▼
┌───────────────────┐    observed/node/*/state = "alive"
│  IntentResolver   │◄────────────────────────────────
│  (intent.go)      │
│                   │    effective count = alive node count
│                   │    (capped by policy_max)
└───────┬───────────┘
        │ writes:
        │  effective/service/{s}/instances = N
        │  effective/service/{s}/placement_mode = "per-node"
        ▼
┌───────────────────┐
│  InstanceController│    creates N pending instances
│  (instance.go)    │    scale-down: stops dead-node instances first
└───────┬───────────┘
        │
        ▼
┌───────────────────┐
│  Scheduler        │    per-node mode triggers:
│  (scheduler.go)   │    1. filterOutNodesAlreadyHostingService()
│                   │    2. cache.RemoveNode() after each placement
│                   │    3. findMovableInstances() skips per-node
└───────────────────┘
```

### Scheduler Per-Node Behavior (placeBatchedInstances, :719)

1. `filterByConstraints()` — standard architecture/zone/label filtering
2. `filterOutNodesAlreadyHostingService()` — removes nodes with existing instances of this service
3. Heap selection — picks least-loaded among remaining candidates
4. After each placement: `cache.RemoveNode(bestNode)` — prevents batched duplicates
5. Rebalancing: `findMovableInstances()` skips per-node instances entirely (:344)

### Scale-Down Priority (instance.go)

Per-node scale-down uses `markPerNodeExcessInstancesAsStopped()`:
```
Priority order (stopped first):
  1. Instances on dead/unreachable nodes  (node_failure state)
  2. Instances in pending state
  3. Instances in running state            (least recent first)
```

## Filter Stage Detail

### All Filter Functions

| Function | File:Line | Purpose |
|----------|-----------|---------|
| `filterByConstraints` | scheduler.go:1218 | Main filter: arch + zone + require + accept |
| `filterByRestrictions` | scheduler.go:1274 | Restriction-only filter (no constraints) |
| `satisfiesRequireLabels` | scheduler.go:1249 | Hard label check: all required k=v present |
| `toleratesRestrictions` | scheduler.go:1263 | Accept check: service tolerates node restrictions |
| `filterOutNodesAlreadyHostingService` | scheduler.go:1192 | Per-node: exclude occupied nodes |

### Resource Capacity Filter (in buildNodeHeap, node_heap.go:60)

The heap build stage also filters: nodes with `availCPU < requiredCPU` or
`availMemory < requiredMemory` are excluded from the heap entirely.

## Rank Stage Detail

### rankByPreferences (scheduler.go:1291)

Soft ranking — all candidates survive, but preferred ones float to the top.

```
Algorithm:
  1. For each candidate, count matching prefer labels → preferScore
  2. Stable sort: higher preferScore first
  3. Equal preferScore preserves original order (feeds into heap)

Input:   candidates + preferLabels map + loadPerNode
Output:  reordered candidates slice (same elements, different order)
Effect:  preferRank in heap entry = position in reordered slice
         → lower preferRank wins ties in heap.Less()
```

## Node Heap

### Structure (scheduler/node_heap.go)

```
nodeSelectionHeap implements container/heap.Interface

┌─────────────────────────────────────┐
│  nodeHeapEntry                       │
│    node       candidateNode          │
│    loadScore  int        ← instance count on this node    │
│    preferRank int        ← position in preference order   │
│    heapIndex  int        ← maintained by Swap()           │
└─────────────────────────────────────┘

heap.Less ordering:
  1. loadScore ascending         (least-loaded wins)
  2. preferRank ascending        (prefer-label bonus)
  3. node.id ascending           (deterministic tie-break)
```

### Complexity

| Operation | Complexity |
|-----------|------------|
| Build heap | O(n) via heap.Init |
| BestNode | O(1) — peek heap[0] |
| BestNodeInZones | O(n) worst case — linear scan |
| RecordPlacement | O(log n) via heap.Fix or heap.Remove |
| RemoveNode | O(log n) via heap.Remove |

## NodeCapacityCache (scheduler/cache.go)

### Purpose

Wraps the min-heap with a `lookupMap` (node ID → entry) for O(1) access.
Reused across all instances in a single service batch — avoids rebuilding
the heap per instance.

### Lifecycle

```
1. NewNodeCapacityCache(candidates, load, reqCPU, reqMem)
   └─ buildNodeHeap → filter by capacity → heap.Init

2. Per instance in batch:
   a. BestNode() or BestNodeInZones()  → peek heap[0]
   b. RecordPlacement(nodeID, cpu, mem) → decrement resources, ++load
      └─ if exhausted → heap.Remove
      └─ else → heap.Fix (restore invariant)
   c. [per-node only] RemoveNode(nodeID)

3. Cache discarded at end of service batch
```

### Exhaustion Check

A node is exhausted when:
```
(minimumCPU > 0  AND  node.availCPU < minimumCPU) OR
(minimumMemory > 0  AND  node.availMemory < minimumMemory)
```

## Batch Placement (placeBatchedInstances, :719)

### Algorithm

Unplaced instances are grouped by service. Per service:

```
1. filterByConstraints(alive, serviceName, constraints)
2. [per-node] filterOutNodesAlreadyHostingService(candidates, svc, placements, instances)
3. [prefer]   rankByPreferences(candidates, prefer, load, cpu, mem)
4. NewNodeCapacityCache(candidates, loadPerNode, reqCPU, reqMem)
5. For each instance:
   a. [zone-spread] BestNodeInZones(findMinZones(candidates, svc, zoneCounts))
      [default]     BestNode()
   b. loadPerNode[bestNode]++
   c. cache.RecordPlacement(bestNode, reqCPU, reqMem)
   d. [per-node] cache.RemoveNode(bestNode)
   e. updateNodeResourcesAfterPlacement() → O(1) via nodeIndexMap
   f. emit placement/instance/{id} = bestNode
         placement/node/{bestNode}/{id} = ""
```

### Zone Spread (findMinZones, :774)

When `zonePolicy == "spread"`:
```
1. Count existing placements per zone for this service
2. Find minimum count among candidate zones
3. Return zones matching minimum count → acceptableZones
4. BestNodeInZones(acceptableZones) → least-loaded in acceptable zone
```

## Service Groups (Co-Scheduling)

### Data Model

```
desired/group/{groupName}/process/{serviceName}
```

Services in a group are co-located on the same node.

### Placement Logic (placeOneGroup, :1397)

```
1. Check if any existing group member is already placed
   └─ findExistingGroupNode() → reuse that node
2. If no existing placement:
   └─ sum CPU/memory across all unplaced members
   └─ selectLeastLoadedNode(alive, load, totalCPU, totalMem)
3. Place all members on chosen node
```

## Rebalancing

### Trigger Condition

```
maxLoad - minLoad > rebalanceLoadImbalanceThreshold (2)
```

Where load = count of running/pending instances per alive node.

### Algorithm (rebalancePlacedInstances, :160)

```
1. Collect alive node IDs (sorted)
2. Count running/pending instances per node → nodeLoad
3. Compute max, min load
4. If max - min <= 2 → skip
5. idealLoad = totalInstances / aliveNodeCount
6. overloaded = nodes where load > idealLoad + 1 (sorted by load desc)
7. For each overloaded node:
   a. findMovableInstances() → running, non-grouped, not freshly placed,
                                 not per-node
   b. For each movable instance:
      - selectRebalanceTarget() → least-loaded constraint-compatible node
      - Only move if targetLoad < sourceLoad - 1 (anti-oscillation)
      - Write rebalance changes:
          placement/instance/{id} = newNode
          DELETE placement/node/{oldNode}/{id}
          PUT placement/node/{newNode}/{id}
      - Update nodeLoad counters
   c. Stop at maxRebalanceMovesPerCycle (2) total moves
```

### Instance Eligibility (findMovableInstances, :321)

| Check | Reason |
|-------|--------|
| `state == running` | Don't move pending/stopped/failed |
| `not in serviceToGroup` | Co-scheduled instances must stay together |
| `not placedInThisCycle` | Avoid place-then-move oscillation |
| `not per-node mode` | Per-node placement is fixed to specific nodes |

### Anti-Oscillation Guard

```
targetLoad < sourceLoad - 1
```

Without this, the scheduler could bounce an instance between two nodes:
A has 5, B has 4 → move to B → B has 5, A has 4 → move to A → loop.
The `-1` ensures a move only happens if it genuinely improves balance.

## Initial Placement Grace Period

### Purpose

Prevents thundering herd on cluster startup — waits for agents to register
before placing anything.

### Activation Conditions

```
activated when ALL of:
  - zero existing placements in store
  - at least one pending instance exists
  - placementGracePeriod > 0

NOT activated when:
  - any existing placements (scale-up, rolling restart)
  - no pending instances
  - gracePeriod is 0 (unit tests)
```

### Timing

```
Default:  initialPlacementGracePeriod = 10s   (scheduler.go:28)
Tests:    SetPlacementGracePeriod(0)           (disables grace)
```

After grace period expires, `firstPendingSeenAt` resets and normal
placement proceeds.

## Unplaced Demand Facts

### Purpose

Signal to the cluster autoscaler that instances need nodes but can't be placed.

### Classification (classifyUnplacedReason, :509)

| Reason | Condition |
|--------|-----------|
| `no_nodes` | Zero alive nodes in cluster |
| `unsatisfiable_constraint` | No alive node matches the service's constraints |
| `insufficient_capacity` | Nodes exist but lack CPU/memory |

### Fact Structure

```
derived/scheduler/unplaced/{instanceID}/reason       = "no_nodes" | "insufficient_capacity" | ...
derived/scheduler/unplaced/{instanceID}/requirements = {"service":"web","cpu":500,"memory":512,"architecture":"amd64"}
```

Stale facts are cleaned up when the instance receives a placement
(`cleanupStaleUnplacedFacts`, :625).

## Pressure Detection

### Threshold

```
nodeAllocationPressurePercent = 80    (scheduler.go:37)
```

### Check (detectAndLogNodePressure, :391)

```
for each alive node with availCPU > 0:
  cpuPercent = (usedCPU * 100) / node.availCPU
  memPercent = (usedMemory * 100) / node.availMemory
  if cpuPercent >= 80 OR memPercent >= 80:
    log WARN "node under allocation pressure"
```

## Constants

| Constant | Value | File:Line | Purpose |
|----------|-------|-----------|---------|
| `initialPlacementGracePeriod` | 10s | scheduler.go:28 | Wait for agents before first placement |
| `maxRebalanceMovesPerCycle` | 2 | scheduler.go:31 | Cap rebalance migrations per cycle |
| `rebalanceLoadImbalanceThreshold` | 2 | scheduler.go:34 | Min max-min diff to trigger rebalance |
| `nodeAllocationPressurePercent` | 80 | scheduler.go:37 | Warn threshold for allocation pressure |

## Stateful Considerations

### Volume Affinity (stateful_volume.go)

Stateful services with volumes have placement affinity — the
`StatefulVolumeController` does not directly constrain the scheduler, but
the instance will fail init if placed on a node without the required volume.
The scheduler is volume-unaware; volume affinity is enforced at runtime.

### DNS Stability (stateful_dns.go)

Stateful DNS entries (`{service}-{ordinal}.{service}`) are maintained by the
`StatefulDNSController`. Rebalancing a stateful instance changes its node,
which updates the DNS A record. Service groups prevent rebalancing of
co-located members. Individual stateful instances can be rebalanced (DNS
updates within the same reconciliation cycle).

## File Reference

| File | Lines | Purpose |
|------|-------|---------|
| `scheduler/scheduler.go` | 1492 | Main scheduler: pipeline, filters, rebalance, pressure |
| `scheduler/cache.go` | 105 | NodeCapacityCache: persistent heap for batch placement |
| `scheduler/node_heap.go` | 92 | Min-heap implementation (container/heap interface) |
| `scheduler/benchmark_test.go` | — | Placement throughput benchmarks |
| `scheduler/fuzz_test.go` | — | Fuzz testing for placement determinism |
| `controllers/intent.go` | 399 | IntentResolver: feeds effective counts to scheduler |
| `types/keys.go` | — | Key functions: placement, unplaced demand, node facts |
| `design/scheduler-rebalancing.md` | — | Design doc for rebalancing and grace period |
