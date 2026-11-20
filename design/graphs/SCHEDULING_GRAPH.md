# CCattler Scheduling & Placement Call Graph

Machine-readable function-level index for the scheduler placement pipeline,
rebalancing, preemption, and per-node mode. Purpose: trace placement decisions
without reading scheduler source files.

## 1. Reconcile Entry Point (scheduler/scheduler.go)

### Scheduler.Reconcile → top-level pipeline
```
Scheduler.Reconcile(ctx, facts)                                  :93
├── extractNodeInfoFromFacts(facts)                              :97/:1453  → map[nodeID]*schedulerNodeInfo
├── extractInstanceInfoFromFacts(facts)                          :98/:1419  → map[instanceID]*schedulerInstanceInfo
│   └── derived/instance/{id}/controller_stopped = true → state stopped: never placed, no
│       load, resources, spread or per-node slot (a surplus the instance controller marked;
│       without it the testbed per-node cpu 1500 rollout stalled, M241 2i.2)
├── extractPlacementsFromFacts(facts)                            :99  → map[instanceID]nodeID
├── extractServiceResourcesFromFacts(facts)                      :100 [resource_requests.go:82]
│   │   → map[svc]serviceResourceRequirements{cpu millicores, memory MiB rounded up},
│   │     map[svc]"{key}={value}" of an unreadable request (M243 D4/D5)
│   ├── collectStoredResourceRequests(facts, desired/service/)   [resource_requests.go:46]
│   ├── collectStoredResourceRequests(facts, effective/service/) (keys ending /resources/cpu|memory)
│   ├── effectiveFirst(effective, desired) per resource          [resource_requests.go:71]
│   │   (an unreadable effective value does NOT fall back to desired: the agent uses it)
│   └── parseServiceRequest(cpu, memory)                         [resource_requests.go:119]
│       ├── types.ParseCPUMillicores / types.ParseMemoryBytes → types.MemoryBytesToMiBRoundedUp
│       └── parse error → types.WarnUnreadableResourceOnce("scheduler", key, value) → invalid,
│           no request entry (its placed instances count 0: the stated exception)
├── extractPlacementConstraints(facts)                           :101 → map[svc]*servicePlacementConstraint
├── extractServiceGroupMemberships(facts)                        :103 → map[svc]groupName
├── extractServicePriorities(facts)                              :104 → map[svc]int
├── extractStatefulServices(facts)                               :105 → map[svc]bool
│
├── findUnplacedPendingInstances(instances, placements)          :108 → []instanceID
├── excludeInvalidRequestInstances(unplaced, instances, invalid) :109 [resource_requests.go:142]
│   └── → placeableUnplaced (instances of invalid services are never placed nor preempt)
├── buildInvalidResourceRequestChanges(facts, invalid)           :110 [resource_requests.go:163]
│   ├── PUT derived/scheduler/invalid_resource_request/{svc} = "{key}={value}" (when changed)
│   └── DELETE it for services no longer invalid
│
├── shouldDeferInitialPlacement(placements, unplaced, nodes)     :112/:152
│   ├── existing placements > 0 → false (scale-up, not first boot)
│   ├── gracePeriod <= 0 → false (tests)
│   ├── first call → record firstPendingSeenAt, return true
│   ├── elapsed < gracePeriod → true (still waiting)
│   └── elapsed >= gracePeriod → false (proceed)
│
├── attemptPlacements(placeableUnplaced, nodes, ...)             :115/:735
│   [full tree in §2]
│
├── collectPlacedInstanceIDs(changes)                            :121 → map[instanceID]bool
│
├── attemptPreemptions(placeableUnplaced, placedInThisCycle, ...) :123/:208
│   [full tree in §5]
│
├── emitUnplacedDemandFacts(unplaced, placed, nodes, ..., invalid) :130/:796
│   [full tree in §6]
│
├── cleanupStaleUnplacedFacts(facts, placements, placed)         :135
│
├── rebalancePlacedInstances(nodes, instances, ..., pinned)      :137/:455
│   [full tree in §4; pinned = extractRebalancePinnedServices(facts, stateful, invalid) :140]
│
└── detectAndLogNodePressure(nodes, placements, instances, ...)  :144/:708
    └── FOR EACH alive node:
        ├── cpuPercent = usedCPU * 100 / availCPU
        ├── memPercent = usedMemory * 100 / availMemory
        └── >= 80% → log WARN "node under allocation pressure"
```

### Watch Prefixes
```
Scheduler.Watch()                                                :67
└── returns:
    ├── placement/instance/          [own placements]
    ├── observed/node/               [node state, resources, labels]
    ├── observed/instance/           [instance state, service]
    ├── desired/service/             [resources, constraints, groups, volumes]
    ├── desired/group/               [service group memberships]
    ├── derived/scheduler/unplaced/  [stale unplaced demand facts]
    ├── observed/volume/             [volume access mode, rebalance pinning]
    ├── derived/instance/            [controller_stopped → the instance counts as stopped, M241 2i.2]
    ├── effective/service/           [effective resources/cpu|memory from the vertical autoscaler, read first, M243 D4;
    │                                 without it the runner never passes them to Reconcile]
    └── derived/scheduler/invalid_resource_request/ [own facts, deleted once fixed; a put to an unscanned key
                                      would be a create-only CAS, M243 D5]
```

## 2. Placement Pipeline (attemptPlacements)

### attemptPlacements → orchestrates grouped + ungrouped placement
```
attemptPlacements(placeableUnplaced, nodes, instances, ...)      :735
├── computeNodeLoadAndResourceUsage(placements, instances, res)  :737
│   └── scans placements × instances → loadPerNode, usedCPU (millicores), usedMemory (MiB)  :1212
│       (a service with an unreadable request has no entry: its instances add 0, M243 D5)
│       (stopped instances skipped: their requests are no longer counted from the
│        stop request on, though the container may still run its pre_stop and grace
│        period; otherwise a per-node replacement could not fit on its node until GC.
│        Accepted trade-off, M241 2g.1/2h; release on proof of termination: M246)
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
├── [per-node] filterOutNodesAlreadyHostingService(candidates, svc, ...) :1046/:1586
│   └── build occupiedNodes from placements of not stopped instances → remove from candidates
│       (a stopped instance keeps its placement until instance GC, which waits for its
│        replacement; counting it blocked every per-node rollout, M241 2e.2)
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
├── serviceInstancesPerNode = state.serviceInstancesPerNode[svc]  [from countServiceInstancesPerNode :1175; stopped instances skipped, M241 2h.1]
├── NewNodeCapacityCache(candidates, loadPerNode, svcPerNode, reqCPU, reqMem)
│   └── buildNodeHeap(candidates, load, svcPerNode, cpu, mem)    [node_heap.go:60]
│       └── filter by capacity → heap.Init (soft service spread first)
│
└── FOR EACH instanceID:                                         :950
    ├── [zone-spread] findMinZones(candidates, svc, zoneCounts, maxSkew)  :965/:995
    │   ├── maxSkew from constraint (default 1): zone eligible if count < minCount + maxSkew
    │   └── cache.BestNodeInZones(acceptableZones)               [cache.go:48]
    ├── [default] cache.BestNode()                               [cache.go:38]
    ├── loadPerNode[bestNode]++                                  :967
    ├── serviceInstancesPerNode[bestNode]++
    ├── cache.RecordPlacement(bestNode, reqCPU, reqMem)          :968
    │   └── decrement resources, ++load, ++serviceInstanceCount, heap.Fix or heap.Remove [cache.go:71]
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
Reconcile → extractRebalancePinnedServices(facts, stateful, invalid) :140/:414
├── stateful services (extractStatefulServices :357)
├── services with an unreadable resource request (size unknown, M243 D5)
├── extractSharedVolumeNames(facts)                              :434
│   └── observed/volume/{name}/access == "shared"
└── + services with desired/service/{svc}/volume/{vol} where vol not shared
    [absent access mode = exclusive → pinned]

rebalancePlacedInstances(nodes, instances, placements, ..., pinned)  :130/:409
├── collectAliveNodeIDs(nodes)                                   :500
│   └── sorted list of alive node IDs
│
├── computeRunningLoadPerNode(placements, instances, alive)      :513
│   └── count running|pending instances per alive node
│
├── computeMaxMinLoad(nodeLoad, alive)                           :534
│   └── maxLoad - minLoad <= 2 → return nil (no imbalance)
│
├── sumNodeLoad(nodeLoad)                                        :550
│   └── idealLoad = totalLoad / aliveNodeCount
│
├── findOverloadedNodes(nodeLoad, alive, idealLoad)              :560
│   └── nodes where load > idealLoad + 1, sorted by load desc
│
└── executeRebalanceMoves(overloaded, nodeLoad, alive, ..., pinned)  :441/:450
    ├── countServiceInstancesPerNode(placements, instances)      :1175  [skips stopped]
    └── FOR EACH overloaded node:
        ├── findMovableInstances(sourceNode, placements, ..., pinned)  :469/:575
        │   └── filter: running, service opted in (placement/rebalance = "true"),
        │              not in group, not placedInThisCycle,
        │              not pinned (stateful / non-shared volume),
        │              not stillStarting (markInstancesStillStarting:
        │              service declares desired/service/{svc}/probe/startup/method
        │              and observed/instance/{id}/probe/startup != succeeded),
        │              not per-node, no colocate/separate hints
        │
        └── FOR EACH movable instance:
            ├── selectRebalanceTarget(svc, source, alive, load, svcPerNode, constraints)  :478/:620
            │   └── filterByConstraints → least-loaded non-source node
            │       hosting fewer instances of svc than the source (never worsens spread)
            │
            ├── GUARD: nodeLoad[target] >= nodeLoad[source] - 1 → skip
            │   [anti-oscillation: move only if genuinely improves balance]
            │
            └── buildRebalanceMoveChanges(instanceID, source, target)  :488/:649
                ├── PUT placement/instance/{id} = targetNode
                ├── DELETE placement/node/{sourceNode}/{id}
                └── PUT placement/node/{targetNode}/{id} = ""
            [cap: maxRebalanceMovesPerCycle (2)]
```

## 5. Priority & Preemption (M161)

### attemptPreemptions → stop low-priority victims for high-priority requesters
```
attemptPreemptions(placeableUnplaced, placedInThisCycle, nodes, ...) :208
│   (placeableUnplaced excludes services with an unreadable request: they never preempt, M243 D5)
├── stillUnplaced = unplaced \ placedInThisCycle
│
├── sort stillUnplaced: highest priority first, then instanceID  :214
│
└── FOR EACH stillUnplaced with priority > 0:                    :243
    ├── findPreemptionVictim(svc, priority, nodes, ..., invalid)  :256/:290
    │   └── FOR EACH placed running instance:                    :309
    │       ├── skip if already preempted
    │       ├── skip if not running
    │       ├── skip if node not alive
    │       ├── skip if same service as requester
    │       ├── skip if victimPriority >= requesterPriority      :326 [strictly less]
    │       ├── skip if per-node mode                            :330
    │       ├── skip if stateful, or its request is unreadable :333 (size unknown, M243 B4)
    │       └── collect → sort by priority asc, then instanceID  :346
    │   └── return first candidate (lowest priority victim)
    │
    └── emit:
        └── PUT derived/instance/{victimID}/controller_stopped = "true"  :279
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
| Unreadable resource request | Yes | Size unknown, so stopping it frees an unknown amount (M243 B4) |

## 6. Unplaced Demand Facts

### emitUnplacedDemandFacts → signal autoscaler
```
emitUnplacedDemandFacts(unplaced, placed, nodes, instances, ..., invalid) :796
├── countAliveNodes(nodes)                                       :819
│
└── FOR EACH unplaced instance not in placedInThisCycle:
    ├── classifyUnplacedReason(info, alive, nodes, constraints, invalid) :832
    │   ├── service request unreadable → "invalid_resource_request" (checked FIRST, so it is never
    │   │   "no_nodes", which cluster scaling counts as demand; filterInsufficientCapacityDemand
    │   │   is an allow-list and ignores it, M243 D5)
    │   ├── aliveCount == 0 → "no_nodes"
    │   ├── countConstraintMatchingNodes() == 0 → "volume_home_node_unavailable" | "unsatisfiable_constraint" :865
    │   └── else → "insufficient_capacity" (a request no node's remaining cpu/MiB covers)
    │
    └── buildUnplacedFactChanges(instanceID, reason, requirements) :915
        ├── PUT derived/scheduler/unplaced/{id}/reason = reason
        └── PUT derived/scheduler/unplaced/{id}/requirements = JSON {cpu millicores, memory MiB}
```

### cleanupStaleUnplacedFacts
```
cleanupStaleUnplacedFacts(facts, placements, placedInThisCycle)  :958
├── extractExistingUnplacedInstanceIDs(facts)                    :980
└── FOR EACH existing unplaced ID:
    └── if now placed → DELETE derived/scheduler/unplaced/{id}/*
```

## 7. Node Heap (scheduler/node_heap.go)

### nodeSelectionHeap — container/heap.Interface
```
nodeSelectionHeap type                                           [node_heap.go:4]

nodeHeapEntry:
    node                 candidateNode
    serviceInstanceCount int    ← instances of the service being placed on node
    loadScore            int    ← instance count on node
    preferRank int              ← position in ranked candidate order
    heapIndex  int              ← maintained by Swap()

heap.Less ordering:                                              [node_heap.go:25]
    1. serviceInstanceCount asc (soft spread of one service across nodes)
    2. loadScore ascending      (least-loaded wins)
    3. preferRank ascending     (prefer-label/affinity bonus)
    4. node.id ascending        (deterministic tie-break)
```

### buildNodeHeap
```
buildNodeHeap(candidates, loadPerNode, svcPerNode, reqCPU, reqMem)  [node_heap.go:60]
├── FOR EACH candidate:
│   ├── skip if availCPU < reqCPU (capacity filter)
│   ├── skip if availMemory < reqMem (capacity filter)
│   └── add nodeHeapEntry{serviceInstanceCount: svcPerNode[id], loadScore: loadPerNode[id], preferRank: index}
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
NewNodeCapacityCache(candidates, loadPerNode, svcPerNode, reqCPU, reqMem)  [cache.go:27]
└── buildNodeHeap(candidates, load, svcPerNode, cpu, mem)        [node_heap.go:60]

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
| `effective/service/{svc}/resources/cpu` | intent resolver (from the vertical autoscaler) | `extractServiceResourcesFromFacts` resource_requests.go:82, read first (M243 D4) |
| `effective/service/{svc}/resources/memory` | intent resolver | `extractServiceResourcesFromFacts`, read first |
| `desired/service/{svc}/resources/cpu` | compiler | `extractServiceResourcesFromFacts` (when no effective value); `types.ParseCPUMillicores` |
| `desired/service/{svc}/resources/memory` | compiler | `extractServiceResourcesFromFacts`; `types.ParseMemoryBytes`, then MiB rounded up |
| `derived/scheduler/invalid_resource_request/{svc}` | self | `buildInvalidResourceRequestChanges` (stale ones deleted) |
| `desired/service/{svc}/placement/*` | compiler | `extractPlacementConstraints` :1359 |
| `desired/service/{svc}/priority` | compiler | `extractServicePriorities` :174 |
| `desired/service/{svc}/stateful` | compiler | `extractStatefulServices` :347 |
| `desired/service/{svc}/placement/rebalance` | compiler | `extractPlacementConstraints` → `constraint.rebalance` (opt-in for `findMovableInstances`) |
| `desired/service/{svc}/volume/{vol}` | compiler | `extractRebalancePinnedServices` :371 |
| `observed/volume/{vol}/access` | node agent | `extractSharedVolumeNames` :392 |
| `observed/volume/{vol}/home_node` | node agent | `applyVolumeHomeNodeConstraints` (Flow 26: hard placement pin; `filterByConstraints`, `countConstraintMatchingNodes`, `placeOneGroup`) |
| `desired/service/{svc}/probe/startup/method` | compiler | `extractServicesWithStartupProbe` |
| `observed/instance/{id}/probe/startup` | node agent | `extractInstanceInfoFromFacts` |
| `derived/instance/{id}/controller_stopped` | instance, failure, scheduler (preemption) | `extractInstanceInfoFromFacts` (counts as stopped, M241 2i.2) |
| `desired/group/{grp}/process/{svc}` | compiler | `extractServiceGroupMemberships` :1615 |
| `derived/scheduler/unplaced/{id}/*` | self | `extractExistingUnplacedInstanceIDs` :852 |

### Writes
| Key Pattern | Value | Written By |
|---|---|---|
| `placement/instance/{id}` | nodeID | `placeBatchedInstances` :976, `placeOneGroup` :1712, `buildRebalanceMoveChanges` :586 |
| `placement/node/{nodeID}/{id}` | "" | `placeBatchedInstances` :977, `placeOneGroup` :1713, `buildRebalanceMoveChanges` :588 |
| `derived/scheduler/unplaced/{id}/reason` | reason string (incl. `invalid_resource_request`) | `buildUnplacedFactChanges` :915 |
| `derived/scheduler/unplaced/{id}/requirements` | JSON | `buildUnplacedFactChanges` :915 |
| `derived/scheduler/invalid_resource_request/{svc}` | `{key}={value}` of the unreadable request | `buildInvalidResourceRequestChanges` resource_requests.go:163; read by `/api/status` → `cca status` "Resources:" line (M243 D5) |
| `derived/instance/{id}/controller_stopped` | "true" | `attemptPreemptions` :208 |

### Deletes
| Key Pattern | Deleted By |
|---|---|
| `placement/node/{oldNode}/{id}` | `buildRebalanceMoveChanges` :697 |
| `derived/scheduler/unplaced/{id}/*` | `cleanupStaleUnplacedFacts` :958 |
| `derived/scheduler/invalid_resource_request/{svc}` | `buildInvalidResourceRequestChanges` (request readable again) |

## DSL Syntax → Fact Keys → Constraint Fields

| DSL Declaration | Fact Key | Constraint Field |
|---|---|---|
| `placement { architecture amd64 }` | `desired/service/{s}/placement/architecture` | `.architecture` |
| `placement { zone spread }` | `desired/service/{s}/placement/zone` | `.zonePolicy` |
| `placement { zone spread maxSkew 2 }` | `desired/service/{s}/placement/zone/maxSkew` | `.zoneMaxSkew` |
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
| `Scheduler.Reconcile` | scheduler.go:93 | Runner.executeReconciliationCycle |
| `Scheduler.Watch` | scheduler.go:67 | Runner.setupControllerWatches |
| `shouldDeferInitialPlacement` | scheduler.go:152 | Reconcile |
| `extractNodeInfoFromFacts` | scheduler.go:1453 | Reconcile |
| `extractInstanceInfoFromFacts` | scheduler.go:1419 | Reconcile |
| `extractPlacementsFromFacts` | scheduler.go:1511 | Reconcile |
| `extractServiceResourcesFromFacts` | resource_requests.go:82 | Reconcile (M243 D4: effective-first, MiB rounded up) |
| `extractPlacementConstraints` | scheduler.go:1551 | Reconcile |
| `extractServiceGroupMemberships` | scheduler.go:1907 | Reconcile |
| `extractServicePriorities` | scheduler.go:188 | Reconcile |
| `extractStatefulServices` | scheduler.go:357 | Reconcile |
| `findUnplacedPendingInstances` | scheduler.go:1179 | Reconcile |
| `attemptPlacements` | scheduler.go:735 | Reconcile |
| `collectPlacedInstanceIDs` | scheduler.go:781 | Reconcile |
| `attemptPreemptions` | scheduler.go:208 | Reconcile |
| `findPreemptionVictim` | scheduler.go:290 | attemptPreemptions |
| `emitUnplacedDemandFacts` | scheduler.go:796 | Reconcile |
| `classifyUnplacedReason` | scheduler.go:832 | emitUnplacedDemandFacts (invalid request checked first) |
| `countConstraintMatchingNodes` | scheduler.go:865 | classifyUnplacedReason |
| `buildUnplacedFactChanges` | scheduler.go:915 | emitUnplacedDemandFacts |
| `buildUnplacedRequirements` | scheduler.go:936 | buildUnplacedFactChanges |
| `cleanupStaleUnplacedFacts` | scheduler.go:958 | Reconcile |
| `extractExistingUnplacedInstanceIDs` | scheduler.go:980 | cleanupStaleUnplacedFacts |
| `rebalancePlacedInstances` | scheduler.go:455 | Reconcile |
| `executeRebalanceMoves` | scheduler.go:496 | rebalancePlacedInstances |
| `collectAliveNodeIDs` | scheduler.go:546 | rebalancePlacedInstances |
| `computeRunningLoadPerNode` | scheduler.go:559 | rebalancePlacedInstances |
| `computeMaxMinLoad` | scheduler.go:580 | rebalancePlacedInstances |
| `sumNodeLoad` | scheduler.go:596 | rebalancePlacedInstances |
| `findOverloadedNodes` | scheduler.go:606 | rebalancePlacedInstances |
| `findMovableInstances` | scheduler.go:621 | executeRebalanceMoves |
| `selectRebalanceTarget` | scheduler.go:668 | executeRebalanceMoves |
| `countServiceInstancesPerNode` | scheduler.go:1194 | attemptPlacements, executeRebalanceMoves |
| `buildRebalanceMoveChanges` | scheduler.go:697 | executeRebalanceMoves |
| `detectAndLogNodePressure` | scheduler.go:708 | Reconcile |
| `computeNodeLoadAndResourceUsage` | scheduler.go:1212 | attemptPlacements, rebalancePlacedInstances, detectAndLogNodePressure |
| `buildAliveCandidateNodes` | scheduler.go:1239 | attemptPlacements, rebalancePlacedInstances |
| `buildNodeIndexMap` | scheduler.go:1263 | attemptPlacements |
| `buildNodeZoneMap` | scheduler.go:1273 | attemptPlacements |
| `computeServiceZonePlacements` | scheduler.go:1287 | attemptPlacements |
| `buildNodeServicesMap` | scheduler.go:1839 | attemptPlacements |
| `partitionUnplacedByGroup` | scheduler.go:1926 | attemptPlacements |
| `placeGroupedInstances` | scheduler.go:1953 | attemptPlacements |
| `placeOneGroup` | scheduler.go:1976 | placeGroupedInstances |
| `findExistingGroupNode` | scheduler.go:2019 | placeOneGroup |
| `computeGroupResourceTotal` | scheduler.go:2039 | placeOneGroup |
| `selectLeastLoadedNode` | scheduler.go:1362 | placeOneGroup |
| `placeUngroupedInstances` | scheduler.go:1019 | attemptPlacements |
| `groupInstancesByService` | scheduler.go:1030 | placeUngroupedInstances |
| `sortedServiceNames` | scheduler.go:1043 | placeUngroupedInstances |
| `placeBatchedInstances` | scheduler.go:1056 | placeUngroupedInstances |
| `filterByConstraints` | scheduler.go:1639 | placeBatchedInstances, selectRebalanceTarget |
| `filterByRestrictions` | scheduler.go:1787 | filterByConstraints (no constraints path) |
| `satisfiesRequireLabels` | scheduler.go:1762 | filterByConstraints |
| `toleratesRestrictions` | scheduler.go:1776 | filterByConstraints, filterByRestrictions |
| `filterOutNodesAlreadyHostingService` | scheduler.go:1613 | placeBatchedInstances (per-node) |
| `rankByPreferences` | scheduler.go:1804 | placeBatchedInstances |
| `rankByAffinityHints` | scheduler.go:1861 | placeBatchedInstances |
| `findMinZones` | scheduler.go:1127 | placeBatchedInstances (zone-spread, maxSkew param) |
| `resolveInstanceResourceNeeds` | scheduler.go:1163 | placeOneGroup, computeGroupResourceTotal |
| `updateNodeResourcesAfterPlacement` | scheduler.go:1314 | placeBatchedInstances, placeOneGroup |
| `NewNodeCapacityCache` | cache.go:27 | placeBatchedInstances |
| `BestNode` | cache.go:39 | placeBatchedInstances |
| `BestNodeInZones` | cache.go:49 | placeBatchedInstances (zone-spread) |
| `RecordPlacement` | cache.go:72 | placeBatchedInstances |
| `RemoveNode` | cache.go:95 | placeBatchedInstances (per-node) |
| `buildNodeHeap` | node_heap.go:66 | NewNodeCapacityCache |
| `selectFromHeap` | node_heap.go:93 | BestNode |
| `countAliveNodes` | scheduler.go:819 | shouldDeferInitialPlacement, emitUnplacedDemandFacts |
| `satisfiesRequireLabelsForNode` | scheduler.go:892 | countConstraintMatchingNodes |
| `toleratesRestrictionsForNode` | scheduler.go:904 | countConstraintMatchingNodes |
| `collectStoredResourceRequests` | resource_requests.go:46 | extractServiceResourcesFromFacts |
| `effectiveFirst` | resource_requests.go:71 | extractServiceResourcesFromFacts |
| `parseServiceRequest` | resource_requests.go:119 | extractServiceResourcesFromFacts |
| `excludeInvalidRequestInstances` | resource_requests.go:142 | Reconcile |
| `buildInvalidResourceRequestChanges` | resource_requests.go:163 | Reconcile |
| `sortedStringMapKeys` | resource_requests.go:189 | buildInvalidResourceRequestChanges |
| `extractRebalancePinnedServices` | scheduler.go:414 | Reconcile |

## Constants

| Constant | Value | File:Line | Used By |
|---|---|---|---|
| `initialPlacementGracePeriod` | 10s | scheduler.go:28 | shouldDeferInitialPlacement |
| `maxRebalanceMovesPerCycle` | 2 | scheduler.go:31 | executeRebalanceMoves |
| `rebalanceLoadImbalanceThreshold` | 2 | scheduler.go:34 | rebalancePlacedInstances |
| `nodeAllocationPressurePercent` | 80 | scheduler.go:37 | detectAndLogNodePressure |
| `maxPreemptionsPerCycle` | 3 | scheduler.go:184 | attemptPreemptions |
| `cpuRequestKeySuffix` / `memoryRequestKeySuffix` | `/resources/cpu`, `/resources/memory` | resource_requests.go:21 | collectStoredResourceRequests |

## File Index

| File | Lines | Purpose |
|---|---|---|
| `scheduler/scheduler.go` | ~2048 | Pipeline, filters, rank, rebalance, preemption, pressure |
| `scheduler/resource_requests.go` | ~195 | Effective-first request reading via the shared parser (`types/resource_quantity.go`), invalid-request handling (M243) |
| `scheduler/cache.go` | ~105 | NodeCapacityCache: persistent heap for batch placement |
| `scheduler/node_heap.go` | ~92 | Min-heap (container/heap.Interface) |
| `scheduler/benchmark_test.go` | — | Placement throughput benchmarks |
| `scheduler/fuzz_test.go` | — | Fuzz testing for placement determinism |
