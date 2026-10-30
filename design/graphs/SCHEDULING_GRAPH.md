# CCattler Scheduling & Placement Call Graph

Machine-readable function-level index for the scheduler placement pipeline,
rebalancing, preemption, and per-node mode. Purpose: trace placement decisions
without reading scheduler source files.

## 1. Reconcile Entry Point (scheduler/scheduler.go)

### Scheduler.Reconcile → top-level pipeline
```
Scheduler.Reconcile(ctx, facts)                                  :86
├── extractNodeInfoFromFacts(facts)                              :87  → map[nodeID]*schedulerNodeInfo
├── extractInstanceInfoFromFacts(facts)                          :88  → map[instanceID]*schedulerInstanceInfo
├── extractPlacementsFromFacts(facts)                            :89  → map[instanceID]nodeID
├── extractServiceResourcesFromFacts(facts)                      :90  → map[svc]serviceResourceRequirements
├── extractPlacementConstraints(facts)                           :91  → map[svc]*servicePlacementConstraint
├── extractServiceGroupMemberships(facts)                        :92  → map[svc]groupName
├── extractServicePriorities(facts)                              :93  → map[svc]int
├── extractStatefulServices(facts)                               :94  → map[svc]bool
│
├── findUnplacedPendingInstances(instances, placements)          :96  → []instanceID
│
├── shouldDeferInitialPlacement(placements, unplaced, nodes)     :99
│   ├── existing placements > 0 → false (scale-up, not first boot)
│   ├── gracePeriod <= 0 → false (tests)
│   ├── first call → record firstPendingSeenAt, return true
│   ├── elapsed < gracePeriod → true (still waiting)
│   └── elapsed >= gracePeriod → false (proceed)
│
├── attemptPlacements(unplaced, nodes, ...)                      :102
│   [full tree in §2]
│
├── collectPlacedInstanceIDs(changes)                            :108 → map[instanceID]bool
│
├── attemptPreemptions(unplaced, placedInThisCycle, ...)         :110
│   [full tree in §5]
│
├── emitUnplacedDemandFacts(unplaced, placed, nodes, ...)        :117
│   [full tree in §6]
│
├── cleanupStaleUnplacedFacts(facts, placements, placed)         :122
│
├── rebalancePlacedInstances(nodes, instances, ...)              :124
│   [full tree in §4]
│
└── detectAndLogNodePressure(nodes, placements, instances, ...)  :130
    └── FOR EACH alive node:                                     :594
        ├── cpuPercent = usedCPU * 100 / availCPU
        ├── memPercent = usedMemory * 100 / availMemory
        └── >= 80% → log WARN "node under allocation pressure"
```

### Watch Prefixes
```
Scheduler.Watch()                                                :64
└── returns:
    ├── placement/instance/          [own placements]
    ├── observed/node/               [node state, resources, labels]
    ├── observed/instance/           [instance state, service]
    ├── desired/service/             [resources, constraints, groups]
    ├── desired/group/               [service group memberships]
    └── derived/scheduler/unplaced/  [stale unplaced demand facts]
```

## 2. Placement Pipeline (attemptPlacements)

### attemptPlacements → orchestrates grouped + ungrouped placement
```
attemptPlacements(unplaced, nodes, instances, ...)               :621
├── computeNodeLoadAndResourceUsage(placements, instances, res)  :630
│   └── scans placements × instances → loadPerNode, usedCPU, usedMemory  :1047
│
├── buildAliveCandidateNodes(nodes, usedCPU, usedMemory)         :631
│   └── FOR EACH node where state == "alive":                    :1070
│       └── → candidateNode{id, availCPU, availMemory, arch, zone, labels, restrictions}
│
├── buildNodeIndexMap(alive)                                     :636  → map[nodeID]int
├── buildNodeZoneMap(alive)                                      :637  → map[nodeID]zone
├── computeServiceZonePlacements(placements, instances, zoneMap) :638  → map[svc]map[zone]int
├── buildNodeServicesMap(placements, instances)                   :639  → map[nodeID]map[svc]bool
│
├── partitionUnplacedByGroup(unplaced, instances, svcToGroup)    :648
│   └── → groupedUnplaced map[groupName][]instanceID, ungrouped []instanceID
│
├── placeGroupedInstances(groupedUnplaced, svcToGroup, state)    :650
│   [full tree in §3]
│
└── placeUngroupedInstances(ungroupedUnplaced, state)            :651
    └── groupInstancesByService(unplaced, instances)             :889
        └── FOR EACH service (sorted):
            └── placeBatchedInstances(svc, instanceIDs, state)   :893
                [full tree below]
```

### placeBatchedInstances → filter → rank → heap → assign
```
placeBatchedInstances(serviceName, instanceIDs, state)           :926
├── filterByConstraints(alive, serviceName, constraints)         :931
│   └── FOR EACH candidate:                                      :1439
│       ├── architecture mismatch → skip                         :1447
│       ├── zone mismatch (non-spread) → skip                   :1450
│       ├── satisfiesRequireLabels(candidate, require) → false → skip  :1453/:1470
│       │   └── FOR EACH required k=v: node.labels[k] != v → false
│       ├── toleratesRestrictions(candidate, accept) → false → skip    :1456/:1484
│       │   └── FOR EACH node restriction: !accept[label] → false
│       └── all filtered out → return original list (fallback)   :1462
│
├── [per-node] filterOutNodesAlreadyHostingService(candidates, svc, ...) :935/:1413
│   └── build occupiedNodes from placements → remove from candidates
│
├── [prefer] rankByPreferences(candidates, prefer, load, cpu, mem)  :939/:1512
│   └── FOR EACH candidate: count matching prefer labels → preferScore
│       └── stable sort: higher preferScore first
│
├── [colocate|separate] rankByAffinityHints(candidates, coloc, sep, nodeServicesMap) :943/:1569
│   └── FOR EACH candidate:
│       ├── +1 per colocate target present on node
│       ├── -1 per separate target present on node
│       └── stable sort: higher affinityScore first
│
├── NewNodeCapacityCache(candidates, loadPerNode, reqCPU, reqMem) :947
│   └── buildNodeHeap(candidates, load, cpu, mem)                [node_heap.go:60]
│       └── filter by capacity → heap.Init
│
└── FOR EACH instanceID:                                         :950
    ├── [zone-spread] findMinZones(candidates, svc, zoneCounts)  :957/:985
    │   └── min count across candidate zones → acceptableZones
    │   └── cache.BestNodeInZones(acceptableZones)               [cache.go:48]
    ├── [default] cache.BestNode()                               [cache.go:38]
    ├── loadPerNode[bestNode]++                                  :967
    ├── cache.RecordPlacement(bestNode, reqCPU, reqMem)          :968
    │   └── decrement resources, ++load, heap.Fix or heap.Remove [cache.go:71]
    ├── [per-node] cache.RemoveNode(bestNode)                    :970/[cache.go:93]
    ├── updateNodeResourcesAfterPlacement(alive, bestNode, ...)  :972/:1145
    └── emit:
        ├── PUT placement/instance/{id} = bestNode               :976
        └── PUT placement/node/{bestNode}/{id} = ""              :977
```

## 3. Service Group Placement

### placeGroupedInstances → co-schedule group members on one node
```
placeGroupedInstances(groupedUnplaced, svcToGroup, state)        :1661
└── FOR EACH groupName (sorted):
    └── placeOneGroup(groupName, instanceIDs, state)             :1675/:1685
        ├── findExistingGroupNode(groupName, state)              :1690/:1724
        │   └── scan placements for instances whose service is in this group
        │       └── first node in sorted order (deterministic)
        │
        ├── [no existing node] computeGroupResourceTotal(instanceIDs, state) :1693/:1744
        │   └── sum CPU + memory across all unplaced members
        │   └── selectLeastLoadedNode(alive, load, totalCPU, totalMem)  :1694/:1193
        │       └── linear scan: least-loaded node with sufficient capacity
        │
        └── FOR EACH instanceID:                                 :1701
            ├── resolveInstanceResourceNeeds(info, resources)    :1703/:1017
            ├── loadPerNode[targetNode]++
            ├── updateNodeResourcesAfterPlacement(...)           :1709
            └── emit:
                ├── PUT placement/instance/{id} = targetNode
                └── PUT placement/node/{targetNode}/{id} = ""
```

## 4. Rebalancing

### rebalancePlacedInstances → detect imbalance → migrate
```
rebalancePlacedInstances(nodes, instances, placements, ...)      :358
├── collectAliveNodeIDs(nodes)                                   :367/:444
│   └── sorted list of alive node IDs
│
├── computeRunningLoadPerNode(placements, instances, alive)      :372/:457
│   └── count running|pending instances per alive node
│
├── computeMaxMinLoad(nodeLoad, alive)                           :373/:478
│   └── maxLoad - minLoad <= 2 → return nil (no imbalance)
│
├── sumNodeLoad(nodeLoad)                                        :378/:494
│   └── idealLoad = totalLoad / aliveNodeCount
│
├── findOverloadedNodes(nodeLoad, alive, idealLoad)              :387/:504
│   └── nodes where load > idealLoad + 1, sorted by load desc
│
└── executeRebalanceMoves(overloaded, nodeLoad, alive, ...)      :389/:398
    └── FOR EACH overloaded node:                                :411
        ├── findMovableInstances(sourceNode, placements, ...)    :415/:519
        │   └── filter: running, not in group, not placedInThisCycle,
        │              not per-node, no colocate/separate hints  :542-548
        │
        └── FOR EACH movable instance:                           :416
            ├── selectRebalanceTarget(svc, source, alive, load, constraints)  :424/:558
            │   └── filterByConstraints → least-loaded non-source node
            │
            ├── GUARD: nodeLoad[target] >= nodeLoad[source] - 1 → skip  :428
            │   [anti-oscillation: move only if genuinely improves balance]
            │
            └── buildRebalanceMoveChanges(instanceID, source, target)  :434/:583
                ├── PUT placement/instance/{id} = targetNode
                ├── DELETE placement/node/{sourceNode}/{id}
                └── PUT placement/node/{targetNode}/{id} = ""
            [cap: maxRebalanceMovesPerCycle (2)]
```

## 5. Priority & Preemption (M161)

### attemptPreemptions → stop low-priority victims for high-priority requesters
```
attemptPreemptions(unplaced, placedInThisCycle, nodes, ...)      :194
├── stillUnplaced = unplaced \ placedInThisCycle                 :206
│
├── sort stillUnplaced: highest priority first, then instanceID  :214
│
└── FOR EACH stillUnplaced with priority > 0:                    :227
    ├── findPreemptionVictim(svc, priority, nodes, ...)          :238/:275
    │   └── FOR EACH placed running instance:                    :293
    │       ├── skip if already preempted
    │       ├── skip if not running
    │       ├── skip if node not alive
    │       ├── skip if same service as requester
    │       ├── skip if victimPriority >= requesterPriority      :310 [strictly less]
    │       ├── skip if per-node mode                            :314
    │       ├── skip if stateful                                 :317
    │       └── collect → sort by priority asc, then instanceID  :330
    │   └── return first candidate (lowest priority victim)
    │
    └── emit:
        └── PUT derived/instance/{victimID}/controller_stopped = "true"  :265
    [cap: maxPreemptionsPerCycle (3)]
```

### Preemption Immunity

| Category | Immune? | Reason |
|----------|---------|--------|
| Per-node services | Yes | Pinned one-per-node |
| Stateful services | Yes | Volume affinity, ordinal stability |
| Same priority | Yes | Only strictly lower priority preempted |
| Priority 0 requesters | Yes | Default/unset never preempts |
| Same service | Yes | Don't cannibalize own instances |

## 6. Unplaced Demand Facts

### emitUnplacedDemandFacts → signal autoscaler
```
emitUnplacedDemandFacts(unplaced, placed, nodes, instances, ...) :681
├── countAliveNodes(nodes)                                       :689/:703
│
└── FOR EACH unplaced instance not in placedInThisCycle:
    ├── classifyUnplacedReason(svc, alive, constraints, resources) :714
    │   ├── aliveCount == 0 → "no_nodes"
    │   ├── countConstraintMatchingNodes() == 0 → "unsatisfiable_constraint"  :740
    │   └── else → "insufficient_capacity"
    │
    └── buildUnplacedFactChanges(instanceID, reason, requirements) :787
        ├── PUT derived/scheduler/unplaced/{id}/reason = reason
        └── PUT derived/scheduler/unplaced/{id}/requirements = JSON
```

### cleanupStaleUnplacedFacts
```
cleanupStaleUnplacedFacts(facts, placements, placedInThisCycle)  :830
├── extractExistingUnplacedInstanceIDs(facts)                    :852
└── FOR EACH existing unplaced ID:
    └── if now placed → DELETE derived/scheduler/unplaced/{id}/*
```

## 7. Node Heap (scheduler/node_heap.go)

### nodeSelectionHeap — container/heap.Interface
```
nodeSelectionHeap type                                           [node_heap.go:4]

nodeHeapEntry:
    node       candidateNode
    loadScore  int              ← instance count on node
    preferRank int              ← position in ranked candidate order
    heapIndex  int              ← maintained by Swap()

heap.Less ordering:                                              [node_heap.go:25]
    1. loadScore ascending      (least-loaded wins)
    2. preferRank ascending     (prefer-label/affinity bonus)
    3. node.id ascending        (deterministic tie-break)
```

### buildNodeHeap
```
buildNodeHeap(candidates, loadPerNode, reqCPU, reqMem)           [node_heap.go:60]
├── FOR EACH candidate:
│   ├── skip if availCPU < reqCPU (capacity filter)
│   ├── skip if availMemory < reqMem (capacity filter)
│   └── add nodeHeapEntry{loadScore: loadPerNode[id], preferRank: index}
├── heap.Init(&heapSlice)
└── → heapSlice, lookupMap
```

### Complexity

| Operation | Complexity | Function |
|-----------|------------|----------|
| Build heap | O(n) | `buildNodeHeap` via `heap.Init` |
| BestNode | O(1) | peek `heap[0]` |
| BestNodeInZones | O(n) worst case | linear scan of heap entries |
| RecordPlacement | O(log n) | `heap.Fix` or `heap.Remove` |
| RemoveNode | O(log n) | `heap.Remove` |

## 8. NodeCapacityCache (scheduler/cache.go)

### NodeCapacityCache — persistent heap with O(1) lookup
```
NewNodeCapacityCache(candidates, loadPerNode, reqCPU, reqMem)    [cache.go:26]
└── buildNodeHeap(candidates, load, cpu, mem)                    [node_heap.go:60]

BestNode() string                                                [cache.go:38]
└── selectFromHeap(&cache.heapData)                              [node_heap.go:86]

BestNodeInZones(acceptableZones) string                          [cache.go:48]
└── linear scan: first heap entry with zone in acceptableZones

RecordPlacement(nodeID, cpuCost, memCost)                        [cache.go:71]
├── lookupMap[nodeID] → entry
├── entry.node.availCPU -= cpuCost
├── entry.node.availMemory -= memCost
├── entry.loadScore++
├── exhausted? → heap.Remove
└── else → heap.Fix (restore invariant)

RemoveNode(nodeID)                                               [cache.go:93]
└── lookupMap[nodeID] → heap.Remove
```

## Store Key Patterns

### Reads
| Key Pattern | Source | Extracted By |
|---|---|---|
| `placement/instance/{id}` | self | `extractPlacementsFromFacts` :1327 |
| `observed/node/{id}/state` | agent | `extractNodeInfoFromFacts` :1269 |
| `observed/node/{id}/available_cpu` | agent | `extractNodeInfoFromFacts` :1269 |
| `observed/node/{id}/available_memory` | agent | `extractNodeInfoFromFacts` :1269 |
| `observed/node/{id}/architecture` | agent | `extractNodeInfoFromFacts` :1269 |
| `observed/node/{id}/zone` | agent | `extractNodeInfoFromFacts` :1269 |
| `observed/node/{id}/label/{key}` | agent | `extractNodeInfoFromFacts` :1269 |
| `observed/node/{id}/restrict/{key}` | agent | `extractNodeInfoFromFacts` :1269 |
| `observed/instance/{id}/state` | agent | `extractInstanceInfoFromFacts` :1243 |
| `observed/instance/{id}/service` | agent | `extractInstanceInfoFromFacts` :1243 |
| `desired/service/{svc}/resources/cpu` | compiler | `extractServiceResourcesFromFacts` :90 |
| `desired/service/{svc}/resources/memory` | compiler | `extractServiceResourcesFromFacts` :90 |
| `desired/service/{svc}/placement/*` | compiler | `extractPlacementConstraints` :1359 |
| `desired/service/{svc}/priority` | compiler | `extractServicePriorities` :174 |
| `desired/service/{svc}/stateful` | compiler | `extractStatefulServices` :341 |
| `desired/group/{grp}/process/{svc}` | compiler | `extractServiceGroupMemberships` :1615 |
| `derived/scheduler/unplaced/{id}/*` | self | `extractExistingUnplacedInstanceIDs` :852 |

### Writes
| Key Pattern | Value | Written By |
|---|---|---|
| `placement/instance/{id}` | nodeID | `placeBatchedInstances` :976, `placeOneGroup` :1712, `buildRebalanceMoveChanges` :586 |
| `placement/node/{nodeID}/{id}` | "" | `placeBatchedInstances` :977, `placeOneGroup` :1713, `buildRebalanceMoveChanges` :588 |
| `derived/scheduler/unplaced/{id}/reason` | reason string | `buildUnplacedFactChanges` :787 |
| `derived/scheduler/unplaced/{id}/requirements` | JSON | `buildUnplacedFactChanges` :787 |
| `derived/instance/{id}/controller_stopped` | "true" | `attemptPreemptions` :265 |

### Deletes
| Key Pattern | Deleted By |
|---|---|
| `placement/node/{oldNode}/{id}` | `buildRebalanceMoveChanges` :587 |
| `derived/scheduler/unplaced/{id}/*` | `cleanupStaleUnplacedFacts` :830 |

## DSL Syntax → Fact Keys → Constraint Fields

| DSL Declaration | Fact Key | Constraint Field |
|---|---|---|
| `placement { architecture amd64 }` | `desired/service/{s}/placement/architecture` | `.architecture` |
| `placement { zone spread }` | `desired/service/{s}/placement/zone` | `.zonePolicy` |
| `placement { zone us-east-1a }` | `desired/service/{s}/placement/zone` | `.zonePolicy` |
| `placement { require gpu=nvidia }` | `desired/service/{s}/placement/require/gpu` | `.require["gpu"]` |
| `placement { prefer tier=fast }` | `desired/service/{s}/placement/prefer/tier` | `.prefer["tier"]` |
| `placement { accept spot }` | `desired/service/{s}/placement/accept/spot` | `.accept["spot"]` |
| `placement { mode per-node }` | `desired/service/{s}/placement/mode` | `.mode` |
| `placement { colocate "redis" }` | `desired/service/{s}/placement/colocate/redis` | `.colocate` |
| `placement { separate "logging" }` | `desired/service/{s}/placement/separate/logging` | `.separate` |

## Reverse Index

| Function | File:Line | Called By |
|---|---|---|
| `Scheduler.Reconcile` | scheduler.go:86 | Runner.executeReconciliationCycle |
| `Scheduler.Watch` | scheduler.go:64 | Runner.setupControllerWatches |
| `shouldDeferInitialPlacement` | scheduler.go:138 | Reconcile |
| `extractNodeInfoFromFacts` | scheduler.go:1269 | Reconcile |
| `extractInstanceInfoFromFacts` | scheduler.go:1243 | Reconcile |
| `extractPlacementsFromFacts` | scheduler.go:1327 | Reconcile |
| `extractServiceResourcesFromFacts` | scheduler.go:90 | Reconcile |
| `extractPlacementConstraints` | scheduler.go:1359 | Reconcile |
| `extractServiceGroupMemberships` | scheduler.go:1615 | Reconcile |
| `extractServicePriorities` | scheduler.go:174 | Reconcile |
| `extractStatefulServices` | scheduler.go:341 | Reconcile |
| `findUnplacedPendingInstances` | scheduler.go:1033 | Reconcile |
| `attemptPlacements` | scheduler.go:621 | Reconcile |
| `collectPlacedInstanceIDs` | scheduler.go:666 | Reconcile |
| `attemptPreemptions` | scheduler.go:194 | Reconcile |
| `findPreemptionVictim` | scheduler.go:275 | attemptPreemptions |
| `emitUnplacedDemandFacts` | scheduler.go:681 | Reconcile |
| `classifyUnplacedReason` | scheduler.go:714 | emitUnplacedDemandFacts |
| `countConstraintMatchingNodes` | scheduler.go:740 | classifyUnplacedReason |
| `buildUnplacedFactChanges` | scheduler.go:787 | emitUnplacedDemandFacts |
| `buildUnplacedRequirements` | scheduler.go:808 | buildUnplacedFactChanges |
| `cleanupStaleUnplacedFacts` | scheduler.go:830 | Reconcile |
| `extractExistingUnplacedInstanceIDs` | scheduler.go:852 | cleanupStaleUnplacedFacts |
| `rebalancePlacedInstances` | scheduler.go:358 | Reconcile |
| `executeRebalanceMoves` | scheduler.go:398 | rebalancePlacedInstances |
| `collectAliveNodeIDs` | scheduler.go:444 | rebalancePlacedInstances |
| `computeRunningLoadPerNode` | scheduler.go:457 | rebalancePlacedInstances |
| `computeMaxMinLoad` | scheduler.go:478 | rebalancePlacedInstances |
| `sumNodeLoad` | scheduler.go:494 | rebalancePlacedInstances |
| `findOverloadedNodes` | scheduler.go:504 | rebalancePlacedInstances |
| `findMovableInstances` | scheduler.go:519 | executeRebalanceMoves |
| `selectRebalanceTarget` | scheduler.go:558 | executeRebalanceMoves |
| `buildRebalanceMoveChanges` | scheduler.go:583 | executeRebalanceMoves |
| `detectAndLogNodePressure` | scheduler.go:594 | Reconcile |
| `computeNodeLoadAndResourceUsage` | scheduler.go:1047 | attemptPlacements, rebalancePlacedInstances, detectAndLogNodePressure |
| `buildAliveCandidateNodes` | scheduler.go:1070 | attemptPlacements, rebalancePlacedInstances |
| `buildNodeIndexMap` | scheduler.go:1094 | attemptPlacements |
| `buildNodeZoneMap` | scheduler.go:1104 | attemptPlacements |
| `computeServiceZonePlacements` | scheduler.go:1118 | attemptPlacements |
| `buildNodeServicesMap` | scheduler.go:1547 | attemptPlacements |
| `partitionUnplacedByGroup` | scheduler.go:1634 | attemptPlacements |
| `placeGroupedInstances` | scheduler.go:1661 | attemptPlacements |
| `placeOneGroup` | scheduler.go:1685 | placeGroupedInstances |
| `findExistingGroupNode` | scheduler.go:1724 | placeOneGroup |
| `computeGroupResourceTotal` | scheduler.go:1744 | placeOneGroup |
| `selectLeastLoadedNode` | scheduler.go:1193 | placeOneGroup |
| `placeUngroupedInstances` | scheduler.go:889 | attemptPlacements |
| `groupInstancesByService` | scheduler.go:900 | placeUngroupedInstances |
| `sortedServiceNames` | scheduler.go:913 | placeUngroupedInstances |
| `placeBatchedInstances` | scheduler.go:926 | placeUngroupedInstances |
| `filterByConstraints` | scheduler.go:1439 | placeBatchedInstances, selectRebalanceTarget |
| `filterByRestrictions` | scheduler.go:1495 | filterByConstraints (no constraints path) |
| `satisfiesRequireLabels` | scheduler.go:1470 | filterByConstraints |
| `toleratesRestrictions` | scheduler.go:1484 | filterByConstraints, filterByRestrictions |
| `filterOutNodesAlreadyHostingService` | scheduler.go:1413 | placeBatchedInstances (per-node) |
| `rankByPreferences` | scheduler.go:1512 | placeBatchedInstances |
| `rankByAffinityHints` | scheduler.go:1569 | placeBatchedInstances |
| `findMinZones` | scheduler.go:985 | placeBatchedInstances (zone-spread) |
| `resolveInstanceResourceNeeds` | scheduler.go:1017 | placeOneGroup, computeGroupResourceTotal |
| `updateNodeResourcesAfterPlacement` | scheduler.go:1145 | placeBatchedInstances, placeOneGroup |
| `NewNodeCapacityCache` | cache.go:26 | placeBatchedInstances |
| `BestNode` | cache.go:38 | placeBatchedInstances |
| `BestNodeInZones` | cache.go:48 | placeBatchedInstances (zone-spread) |
| `RecordPlacement` | cache.go:71 | placeBatchedInstances |
| `RemoveNode` | cache.go:93 | placeBatchedInstances (per-node) |
| `buildNodeHeap` | node_heap.go:60 | NewNodeCapacityCache |
| `selectFromHeap` | node_heap.go:86 | BestNode |
| `countAliveNodes` | scheduler.go:703 | shouldDeferInitialPlacement, emitUnplacedDemandFacts |
| `satisfiesRequireLabelsForNode` | scheduler.go:764 | countConstraintMatchingNodes |
| `toleratesRestrictionsForNode` | scheduler.go:776 | countConstraintMatchingNodes |

## Constants

| Constant | Value | File:Line | Used By |
|---|---|---|---|
| `initialPlacementGracePeriod` | 10s | scheduler.go:28 | shouldDeferInitialPlacement |
| `maxRebalanceMovesPerCycle` | 2 | scheduler.go:31 | executeRebalanceMoves |
| `rebalanceLoadImbalanceThreshold` | 2 | scheduler.go:34 | rebalancePlacedInstances |
| `nodeAllocationPressurePercent` | 80 | scheduler.go:37 | detectAndLogNodePressure |
| `maxPreemptionsPerCycle` | 3 | scheduler.go:170 | attemptPreemptions |

## File Index

| File | Lines | Purpose |
|---|---|---|
| `scheduler/scheduler.go` | ~1754 | Pipeline, filters, rank, rebalance, preemption, pressure |
| `scheduler/cache.go` | ~105 | NodeCapacityCache: persistent heap for batch placement |
| `scheduler/node_heap.go` | ~92 | Min-heap (container/heap.Interface) |
| `scheduler/benchmark_test.go` | — | Placement throughput benchmarks |
| `scheduler/fuzz_test.go` | — | Fuzz testing for placement determinism |
