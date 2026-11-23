# CCattler Scheduling & Placement Call Graph

Machine-readable function-level index for the scheduler placement pipeline,
rebalancing, preemption, and per-node mode. Purpose: trace placement decisions
without reading scheduler source files.

## 1. Reconcile Entry Point (scheduler/scheduler.go)

### Scheduler.Reconcile → top-level pipeline
```
Scheduler.Reconcile(ctx, facts)                                  :104
├── extractNodeInfoFromFacts(facts)                              :108/:1613  → map[nodeID]*schedulerNodeInfo
├── extractInstanceInfoFromFacts(facts)                          :109/:1579  → map[instanceID]*schedulerInstanceInfo
│   └── derived/instance/{id}/controller_stopped = true → state stopped: never placed, no
│       load, resources, spread or per-node slot (a surplus the instance controller marked;
│       without it the testbed per-node cpu 1500 rollout stalled, M241 2i.2)
├── extractPlacementsFromFacts(facts)                            :110  → map[instanceID]nodeID
├── extractServiceResourcesFromFacts(facts)                      :110 [resource_requests.go:28]
│   │   → map[svc]serviceResourceRequirements{cpu millicores, memory MiB rounded up},
│   │     map[svc]"{key}={value}" of an unreadable request (M243 D4/D5)
│   ├── types.CollectEffectiveFirstResourceValues(facts)         [types/resource_kind.go:120; shared with
│   │   controllers.collectResourceQuantities, 3f.3] → {svc, kind} → {FactKey, FactValue}
│   │   (an unreadable effective value does NOT fall back to desired: the agent uses it)
│   └── parseServiceRequest(svc, chosen)                         [resource_requests.go:51]
│       ├── FOR EACH types.ResourceKindSpecs() (CPU first): kindSpec.Parse; memory → types.MemoryBytesToMiBRoundedUp
│       └── parse error → types.WarnUnreadableResourceOnce("scheduler", key, value) → invalid,
│           no request entry (its placed instances count 0: the stated exception)
├── extractPlacementConstraints(facts)                           :112 → map[svc]*servicePlacementConstraint
├── extractServiceGroupMemberships(facts)                        :114 → map[svc]groupName
├── extractServicePriorities(facts)                              :115 → map[svc]int
├── extractStatefulServices(facts)                               :116 → map[svc]bool
│
├── findUnplacedPendingInstances(instances, placements)          :119 → []instanceID
├── excludeInvalidRequestInstances(unplaced, instances, invalid) :119 [resource_requests.go:75]
│   └── → placeableUnplaced (instances of invalid services are never placed nor preempt)
├── buildInvalidResourceRequestChanges(facts, invalid)           :120 [resource_requests.go:96]
│   ├── PUT derived/scheduler/invalid_resource_request/{svc} = "{key}={value}" (when changed)
│   └── DELETE it for services no longer invalid
│
├── shouldDeferInitialPlacement(placements, unplaced, nodes)     :123/:171
│   ├── existing placements > 0 → false (scale-up, not first boot)
│   ├── gracePeriod <= 0 → false (tests)
│   ├── first call → record firstPendingSeenAt, return true
│   ├── elapsed < gracePeriod → true (still waiting)
│   └── elapsed >= gracePeriod → false (proceed)
│
├── attemptPlacements(placeableUnplaced, nodes, ...)             :126/:856
│   [full tree in §2]
│
├── collectPlacedInstanceIDs(changes)                            :132 → map[instanceID]bool
│
├── attemptPreemptions(placeableUnplaced, placed, nodes, changes, inputs) :134/:251
│   [full tree in §5]
│
├── selectInstancesWaitingForPlacement(unplaced, placed)         :144/:918 → waiting (unplaced \ placed, sorted;
│   computed once, input of both unplaced-fact steps below, M262 D1)
│
├── emitUnplacedDemandFacts(waiting, nodes, ..., invalid, extractStoredUnplacedValues(facts),
│       maxUnplacedFactChangesPerCycle) :145/:932 → unplacedChanges (puts first in the shared budget, M262 D4)
│   [full tree in §6]
│
├── cleanupStaleUnplacedFacts(facts, waiting, maxUnplacedFactChangesPerCycle − len(unplacedChanges)) :151/:1117
│
├── rebalancePlacedInstances(nodes, rebalanceInputs{...})        :154/:565
│   [full tree in §4; pinned = extractRebalancePinnedServices(facts, stateful, invalid) :158;
│    nodesUnderPressure = extractNodesUnderPressure(facts, nodes) :159/:549 (M267 D1)]
│
└── detectAndLogNodePressure(nodes, placements, instances, ...)  :163/:829
    └── FOR EACH alive node:
        ├── cpuPercent = usedCPU * 100 / availCPU
        ├── memPercent = usedMemory * 100 / availMemory
        └── >= 80% → log WARN "node under allocation pressure"
```

### Watch Prefixes
```
Scheduler.Watch()                                                :77
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
    ├── derived/scheduler/invalid_resource_request/ [own facts, deleted once fixed; a put to an unscanned key
    │                                 would be a create-only CAS, M243 D5]
    └── derived/node/                [eviction's derived/node/{id}/pressure: no rebalance move onto that node,
                                      M267 D1; read-only, so the runner adds no input guards for it]
```

## 2. Placement Pipeline (attemptPlacements)

### attemptPlacements → orchestrates grouped + ungrouped placement
```
attemptPlacements(placeableUnplaced, nodes, instances, ...)      :856
├── computeNodeLoadAndResourceUsage(placements, instances, res)  :821
│   └── scans placements × instances → loadPerNode, usedCPU (millicores), usedMemory (MiB)  :1336
│       (a service with an unreadable request has no entry: its instances add 0, M243 D5)
│       (stopped instances skipped: their requests are no longer counted from the
│        stop request on, though the container may still run its pre_stop and grace
│        period; otherwise a per-node replacement could not fit on its node until GC.
│        Accepted trade-off, M241 2g.1/2h; release on proof of termination: M246)
│
├── buildAliveCandidateNodes(nodes, usedCPU, usedMemory)         :822
│   └── FOR EACH node where state == "alive":                    :1357
│       └── → candidateNode{id, availCPU, availMemory, arch, zone, labels, restrictions}
│
├── buildNodeIndexMap(alive)                                     :827  → map[nodeID]int
├── buildNodeZoneMap(alive)                                      :828  → map[nodeID]zone
├── computeServiceZonePlacements(placements, instances, zoneMap) :829  → map[svc]map[zone]int
├── buildNodeServicesMap(placements, instances)                   :830  → map[nodeID]map[svc]bool
│
├── partitionUnplacedByGroup(unplaced, instances, svcToGroup)    :840
│   └── → groupedUnplaced map[groupName][]instanceID, ungrouped []instanceID
│
├── placeGroupedInstances(groupedUnplaced, svcToGroup, state)    :842
│   [full tree in §3]
│
└── placeUngroupedInstances(ungroupedUnplaced, state)            :843
    └── groupInstancesByService(unplaced, instances)             :1136
        └── FOR EACH service (sorted):
            └── placeBatchedInstances(svc, instanceIDs, state)   :1139
                [full tree below]
```

### placeBatchedInstances → filter → rank → heap → assign
```
placeBatchedInstances(serviceName, instanceIDs, state)           :1216
├── filterByConstraints(alive, serviceName, constraints)         :1177
│   └── FOR EACH candidate:                                      :1768
│       ├── architecture mismatch → skip                         :1769
│       ├── zone mismatch (non-spread) → skip                   :1772
│       ├── satisfiesRequireLabels(candidate, require) → false → skip  :1775/:1922
│       │   └── FOR EACH required k=v: node.labels[k] != v → false
│       ├── toleratesRestrictions(candidate, accept) → false → skip    :1778/:1936
│       │   └── FOR EACH node restriction: !accept[label] → false
│       └── all filtered out → return original list (fallback)   :1785
│
├── [per-node] filterOutNodesAlreadyHostingService(candidates, svc, ...) :1181/:1773
│   └── build occupiedNodes from placements of not stopped instances → remove from candidates
│       (a stopped instance keeps its placement until instance GC, which waits for its
│        replacement; counting it blocked every per-node rollout, M241 2e.2)
│
├── [prefer] rankByPreferences(candidates, prefer, load, cpu, mem)  :1185/:1964
│   └── FOR EACH candidate: count matching prefer labels → preferScore
│       └── stable sort: higher preferScore first
│
├── [colocate|separate] rankByAffinityHints(candidates, coloc, sep, nodeServicesMap) :1189/:2021
│   └── FOR EACH candidate:
│       ├── +1 per colocate target present on node
│       ├── -1 per separate target present on node
│       └── stable sort: higher affinityScore first
│
├── serviceInstancesPerNode = state.serviceInstancesPerNode[svc]  [from countServiceInstancesPerNode :1354; stopped instances skipped, M241 2h.1]
├── NewNodeCapacityCache(candidates, loadPerNode, svcPerNode, reqCPU, reqMem)
│   └── buildNodeHeap(candidates, load, svcPerNode, cpu, mem)    [node_heap.go:66]
│       └── filter by capacity → heap.Init (soft service spread first)
│
└── FOR EACH instanceID:                                         :1205
    ├── [zone-spread] findMinZones(candidates, svc, zoneCounts, maxSkew)  :1212/:1287
    │   ├── maxSkew from constraint (default 1): zone eligible if count < minCount + maxSkew
    │   └── cache.BestNodeInZones(acceptableZones)               [cache.go:49]
    ├── [default] cache.BestNode()                               [cache.go:39]
    ├── loadPerNode[bestNode]++                                  :1222
    ├── serviceInstancesPerNode[bestNode]++
    ├── cache.RecordPlacement(bestNode, reqCPU, reqMem)          :1224
    │   └── decrement resources, ++load, ++serviceInstanceCount, heap.Fix or heap.Remove [cache.go:72]
    ├── [per-node] cache.RemoveNode(bestNode)                    :1226/[cache.go:94]
    ├── updateNodeResourcesAfterPlacement(alive, bestNode, ...)  :1228/:1474
    └── emit:
        ├── PUT placement/instance/{id} = bestNode               :1232
        └── PUT placement/node/{bestNode}/{id} = ""              :1233
```

## 3. Service Group Placement

### placeGroupedInstances → co-schedule group members on one node
```
placeGroupedInstances(groupedUnplaced, svcToGroup, state)        :2113
└── FOR EACH groupName (sorted):
    └── placeOneGroup(groupName, instanceIDs, state)             :2082/:2136
        ├── findExistingGroupNode(groupName, state)              :2097/:2179
        │   └── scan placements for instances whose service is in this group
        │       └── first node in sorted order (deterministic)
        │
        ├── [no existing node] computeGroupResourceTotal(instanceIDs, state) :2104/:2199
        │   └── sum CPU + memory across all unplaced members
        │   └── selectLeastLoadedNode(alive, load, totalCPU, totalMem)  :2105/:1522
        │       └── linear scan: least-loaded node with sufficient capacity
        │
        └── FOR EACH instanceID:                                 :2112
            ├── resolveInstanceResourceNeeds(info, resources)    :2114/:1323
            ├── loadPerNode[targetNode]++
            ├── updateNodeResourcesAfterPlacement(...)           :2120
            └── emit:
                ├── PUT placement/instance/{id} = targetNode
                └── PUT placement/node/{targetNode}/{id} = ""
```

## 4. Rebalancing

### rebalancePlacedInstances → detect imbalance → migrate
```
Reconcile → extractRebalancePinnedServices(facts, stateful, invalid) :158/:494
├── stateful services (extractStatefulServices :437)
├── services with an unreadable resource request (size unknown, M243 D5)
├── extractSharedVolumeNames(facts)                              :518
│   └── observed/volume/{name}/access == "shared"
└── + services with desired/service/{svc}/volume/{vol} where vol not shared
    [absent access mode = exclusive → pinned]

Reconcile → extractNodesUnderPressure(facts, nodes)              :159/:549  (M267 D1)
└── nodes whose key types.KeyDerivedNodePressure(id) is among the scanned derived/node/ facts
    (exact key match, never parsed) → nodesUnderPressure

rebalanceInputs{placements, instances, serviceResources, placementConstraints, serviceToGroup,
                placedInThisCycle, pinnedServices, nodesUnderPressure}  :535  (read-only for the pass)

rebalancePlacedInstances(nodes, inputs)                          :154/:565
├── collectAliveNodeIDs(nodes)                                   :663
│   └── sorted list of alive node IDs
│
├── computeRunningLoadPerNode(placements, instances, alive)      :676
│   └── count running|pending instances per alive node
│
├── computeMaxMinLoad(nodeLoad, alive)                           :697
│   └── maxLoad - minLoad <= 2 → return nil (no imbalance)
│
├── sumNodeLoad(nodeLoad)                                        :713
│   └── idealLoad = totalLoad / aliveNodeCount
│
├── findOverloadedNodes(nodeLoad, alive, idealLoad)              :723
│   └── nodes where load > idealLoad + 1, sorted by load desc
│
├── aliveCandidates = buildAliveCandidateNodes(nodes, usedCPU, usedMemory)  :585/:1399
│   └── each alive node's remaining CPU/memory after the requests of its placed, not stopped instances
│
└── executeRebalanceMoves(overloaded, nodeLoad, aliveCandidates, inputs)  :588/:595
    ├── countServiceInstancesPerNode(placements, instances)      :603/:1354  [skips stopped]
    ├── candidateIndexByNode = buildNodeIndexMap(aliveCandidates) :604/:1423
    └── FOR EACH overloaded node:
        ├── findMovableInstances(sourceNode, placements, ..., pinned)  :610/:738
        │   └── filter: running, service opted in (placement/rebalance = "true"),
        │              not in group, not placedInThisCycle,
        │              not pinned (stateful / non-shared volume),
        │              not stillStarting (markInstancesStillStarting:
        │              service declares desired/service/{svc}/probe/startup/method
        │              and observed/instance/{id}/probe/startup != succeeded),
        │              not per-node, no colocate/separate hints
        │
        └── FOR EACH movable instance:
            ├── selectRebalanceTarget(svc, source, aliveCandidates, load, svcPerNode, inputs)  :620/:785
            │   ├── filterByConstraints(aliveCandidates, svc, constraints)  :793/:1799
            │   ├── skip the source and nodesUnderPressure (M267 D1)
            │   ├── skip nodes hosting as many instances of svc as the source (never worsens spread)
            │   ├── skip unless candidateFitsRequests(candidate, serviceResources[svc])  :804/[node_heap.go:92]
            │   │   [the placement fit check, shared with buildNodeHeap, stopMakesRoom, RecordPlacement]
            │   └── → least-loaded remaining node, "" if none (the instance stays; no move)
            │
            ├── GUARD: nodeLoad[target] >= nodeLoad[source] - 1 → skip
            │   [anti-oscillation: move only if genuinely improves balance]
            │
            ├── buildRebalanceMoveChanges(instanceID, source, target)  :630/:818
            │   ├── PUT placement/instance/{id} = targetNode   (same instance ID: a move, not a replacement)
            │   ├── DELETE placement/node/{sourceNode}/{id}
            │   └── PUT placement/node/{targetNode}/{id} = ""
            │
            └── transferRemainingCapacity(aliveCandidates, index, source, target, requests)  :635/:646
                └── target's remaining CPU/memory −= requests, source's += requests, so later
                    moves in the cycle see the room taken (M267 D1)
            [cap: maxRebalanceMovesPerCycle (2)]

The source agent then removes its container without writing stopped (the instance is placed
on another node, no controller stop) and the target agent starts it: INSTANCE_LIFECYCLE_GRAPH,
checkStaleInstanceMove, publishRemovedInstanceState (M267 D2).
```

## 5. Priority & Preemption (M161)

### attemptPreemptions → stop low-priority victims for high-priority requesters
```
Reconcile :134 → attemptPreemptions(placeableUnplaced, placedInThisCycle, nodes, changes,
    preemptionInputs{instances, placements, serviceResources, constraints, priorities,
                     stateful, invalid})                       :224  (read-only; M243 D12: the request map placement uses)

attemptPreemptions(unplaced, placed, nodes, thisCycleChanges, inputs) :251
│   (placeableUnplaced excludes services with an unreadable request: they never preempt, M243 D5)
├── requesters = unplaced \ placedInThisCycle, priority > 0 only; none → return (no copy, no usage, P-N1)
├── sort requesters: highest priority first, then instanceID    :265
├── reservations = newPreemptionReservations(nodes,              :312  (the only mutable state, N-d)
│       mergeThisCyclePlacements(placements, thisCycleChanges) :419, inputs)
│   ├── computeNodeLoadAndResourceUsage(cyclePlacements, ...) → usedCPU, usedMemory
│   └── {aliveNodes: buildAliveCandidateNodes(...) → remaining cpu (millicores) / memory (MiB),
│        aliveNodeIndex: buildNodeIndexMap(aliveNodes), cyclePlacements, preemptedInstances}
│
└── FOR EACH requester, while preemptionCount < maxPreemptionsPerCycle (explicit counter, P-N4): :277
    ├── findPreemptionVictim(svc, inputs, reservations)          :340  (single victim, D12; only finds, P-N3)
    │   ├── allowedPreemptionNodes(requester, inputs, reservations) :388  (as placement does):
    │   │   ├── filterByConstraints(aliveNodes, requester, constraints)
    │   │   └── per-node requester → filterOutNodesAlreadyHostingService(…, reservations.cyclePlacements, …)
    │   │       (stored + this cycle's + reserved placements; review round 2 I1)
    │   └── FOR EACH stored placement (instance → node):         :350
    │       ├── skip if already preempted, not running, or its node is not alive and allowed
    │       ├── skip if same service, or victimPriority >= requesterPriority [strictly less]  :358
    │       ├── skip unless isPreemptibleService(victim)         :399  (not per-node, stateful or
    │       │                                                           unreadable request, M243 B4)
    │       ├── skip unless stopMakesRoom(node, victim requests, requester requests) :361/:409
    │       │   (candidateFitsRequests on node remaining + victim's cpu/MiB; a zero request always fits)
    │       └── collect → sort by priority asc, then node ID, then instanceID  :369
    │   └── first candidate, or "" (nothing preempted; the requester stays insufficient_capacity)
    ├── reservations.reserve(requester, victim, node, inputs)    :326  (the only writer, P-N3):
    │   node remaining += victim − requester; victim marked preempted; cyclePlacements[requester] = node
    │   (a later per-node requester of the service skips it)
    │
    └── emit:
        └── PUT derived/instance/{victimID}/controller_stopped = "true"  :297
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
| Stop would not make room | Yes | Node remaining + its requests do not cover the requester's (M243 D12); no victim sets |
| Node the requester may not use | Yes | Only nodes placement would use: `filterByConstraints`, and for a per-node requester none already hosting it (M243 D12, round 2 I1) |

## 6. Unplaced Demand Facts

### emitUnplacedDemandFacts → signal autoscaler
```
emitUnplacedDemandFacts(waiting, nodes, instances, ..., invalid, stored, changeBudget) :932
│   changeBudget = maxUnplacedFactChangesPerCycle (40, scheduler.go:44), shared with cleanup (M262 D4)
│   waiting = selectInstancesWaitingForPlacement(unplaced, placedInThisCycle) :918
│   stored = extractStoredUnplacedValues(facts) :965  (derived/scheduler/unplaced/ is a watched prefix,
│   so the scheduler's own last writes are in its input)
├── countAliveNodes(nodes)                                       :974
│
└── FOR EACH waiting instance (sorted):
    ├── classifyUnplacedReason(info, alive, nodes, constraints, invalid) :987
    │   ├── service request unreadable → "invalid_resource_request" (checked FIRST, so it is never
    │   │   "no_nodes", which cluster scaling counts as demand; filterInsufficientCapacityDemand
    │   │   is an allow-list and ignores it, M243 D5)
    │   ├── aliveCount == 0 → "no_nodes"
    │   ├── countConstraintMatchingNodes() == 0 → "volume_home_node_unavailable" | "unsatisfiable_constraint" :1020
    │   └── else → "insufficient_capacity" (a request no node's remaining cpu/MiB covers)
    │
    └── buildUnplacedFactChanges(instanceID, reason, requirements) :1072
        ├── PUT derived/scheduler/unplaced/{id}/reason = reason
        ├── PUT derived/scheduler/unplaced/{id}/requirements = JSON {cpu millicores, memory MiB}
        │   (encoding/json on a struct: deterministic bytes)
        └── a put whose stored value is byte-equal is dropped (compare before update, 3g.1): a re-put
            of an unchanged value bumps the etcd revision, and through the scheduler's own watch it
            woke the scheduler every cycle (testbed F1: ≈80 puts/s for 3 unplaced instances)
    └── the instance's changed puts would exceed changeBudget → stop (whole instances only, sorted ID
        order; the rest are written in later cycles, each commit wakes the scheduler, D4)
```

### cleanupStaleUnplacedFacts
```
cleanupStaleUnplacedFacts(facts, waiting, changeBudget)         :1117
│   changeBudget = what emitUnplacedDemandFacts left of maxUnplacedFactChangesPerCycle (D4)
├── waiting → set (O(1) membership)
├── extractExistingUnplacedInstanceIDs(facts)                    :1140 (sorted)
└── FOR EACH existing unplaced ID not in waiting (placed, stored or this cycle; stopped, controller_stopped
    included; any other state; gone from the store, M262 D1):
    ├── 2 more deletes (unplacedFactsPerInstance) would exceed changeBudget → stop; the rest go in later
    │   cycles, lowest IDs first, so placements keep room in the runner's 60-change budget (D4)
    └── DELETE derived/scheduler/unplaced/{id}/reason and /requirements
        (waiting and the deleted IDs are complementary: never a put and a delete of one key in a cycle;
        leftovers of an older version are deleted from the first cycle on, no migration, D3)
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
buildNodeHeap(candidates, loadPerNode, svcPerNode, reqCPU, reqMem)  [node_heap.go:66]
├── FOR EACH candidate:
│   ├── skip unless candidateFitsRequests(candidate, {reqCPU, reqMem})  [node_heap.go:92]
│   │   (availCPU ≥ reqCPU and availMemory ≥ reqMem; a zero request always fits)
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
└── buildNodeHeap(candidates, load, svcPerNode, cpu, mem)        [node_heap.go:66]

BestNode() string                                                [cache.go:39]
└── selectFromHeap(&cache.heapData)                              [node_heap.go:101]

BestNodeInZones(acceptableZones) string                          [cache.go:49]
└── linear scan: first heap entry with zone in acceptableZones

RecordPlacement(nodeID, cpuCost, memCost)                        [cache.go:72]
├── lookupMap[nodeID] → entry
├── entry.node.availCPU -= cpuCost
├── entry.node.availMemory -= memCost
├── entry.loadScore++
├── exhausted = !candidateFitsRequests(entry.node, {minimumCPU, minimumMemory}) → heap.Remove
└── else → heap.Fix (restore invariant)

RemoveNode(nodeID)                                               [cache.go:94]
└── lookupMap[nodeID] → heap.Remove
```

## Store Key Patterns

### Reads
| Key Pattern | Source | Extracted By |
|---|---|---|
| `placement/instance/{id}` | self | `extractPlacementsFromFacts` :1671 |
| `observed/node/{id}/state` | agent | `extractNodeInfoFromFacts` :1613 |
| `observed/node/{id}/available_cpu` | agent | `extractNodeInfoFromFacts` :1613 |
| `observed/node/{id}/available_memory` | agent | `extractNodeInfoFromFacts` :1613 |
| `observed/node/{id}/architecture` | agent | `extractNodeInfoFromFacts` :1613 |
| `observed/node/{id}/zone` | agent | `extractNodeInfoFromFacts` :1613 |
| `observed/node/{id}/label/{key}` | agent | `extractNodeInfoFromFacts` :1613 |
| `observed/node/{id}/restrict/{key}` | agent | `extractNodeInfoFromFacts` :1613 |
| `observed/instance/{id}/state` | agent | `extractInstanceInfoFromFacts` :1579 |
| `observed/instance/{id}/service` | agent | `extractInstanceInfoFromFacts` :1579 |
| `effective/service/{svc}/resources/cpu` | intent resolver (from the vertical autoscaler) | `extractServiceResourcesFromFacts` resource_requests.go:28, read first (M243 D4) |
| `effective/service/{svc}/resources/memory` | intent resolver | `extractServiceResourcesFromFacts`, read first |
| `desired/service/{svc}/resources/cpu` | compiler | `extractServiceResourcesFromFacts` (when no effective value); `types.ParseCPUMillicores` |
| `desired/service/{svc}/resources/memory` | compiler | `extractServiceResourcesFromFacts`; `types.ParseMemoryBytes`, then MiB rounded up |
| `derived/scheduler/invalid_resource_request/{svc}` | self | `buildInvalidResourceRequestChanges` (stale ones deleted) |
| `desired/service/{svc}/placement/*` | compiler | `extractPlacementConstraints` :1711 |
| `desired/service/{svc}/priority` | compiler | `extractServicePriorities` :207 |
| `desired/service/{svc}/stateful` | compiler | `extractStatefulServices` :437 |
| `desired/service/{svc}/placement/rebalance` | compiler | `extractPlacementConstraints` → `constraint.rebalance` (opt-in for `findMovableInstances`) |
| `desired/service/{svc}/volume/{vol}` | compiler | `extractRebalancePinnedServices` :494 |
| `observed/volume/{vol}/access` | node agent | `extractSharedVolumeNames` :518 |
| `observed/volume/{vol}/home_node` | node agent | `applyVolumeHomeNodeConstraints` (Flow 26: hard placement pin; `filterByConstraints`, `countConstraintMatchingNodes`, `placeOneGroup`) |
| `desired/service/{svc}/probe/startup/method` | compiler | `extractServicesWithStartupProbe` |
| `observed/instance/{id}/probe/startup` | node agent | `extractInstanceInfoFromFacts` |
| `derived/instance/{id}/controller_stopped` | instance, failure, scheduler (preemption) | `extractInstanceInfoFromFacts` (counts as stopped, M241 2i.2) |
| `desired/group/{grp}/process/{svc}` | compiler | `extractServiceGroupMemberships` :2067 |
| `derived/scheduler/unplaced/{id}/*` | self | `extractStoredUnplacedValues` (compare before put, 3g.1), `extractExistingUnplacedInstanceIDs` :1140 |
| `derived/node/{id}/pressure` | eviction controller | `extractNodesUnderPressure` :549 (no rebalance move onto the node, M267 D1) |

### Writes
| Key Pattern | Value | Written By |
|---|---|---|
| `placement/instance/{id}` | nodeID | `placeBatchedInstances` :1232, `placeOneGroup` :2123, `buildRebalanceMoveChanges` :821 |
| `placement/node/{nodeID}/{id}` | "" | `placeBatchedInstances` :1233, `placeOneGroup` :2124, `buildRebalanceMoveChanges` :823 |
| `derived/scheduler/unplaced/{id}/reason` | reason string (incl. `invalid_resource_request`) | `buildUnplacedFactChanges` :1072 (put only while the instance waits and the stored value differs; the filter is `emitUnplacedDemandFacts` :932 over `selectInstancesWaitingForPlacement` :918, 3g.1, M262; at most 40 unplaced changes per cycle with the deletes, D4) |
| `derived/scheduler/unplaced/{id}/requirements` | JSON | `buildUnplacedFactChanges` :1072 (put only while the instance waits and the stored value differs; the filter is `emitUnplacedDemandFacts` :932 over `selectInstancesWaitingForPlacement` :918, 3g.1, M262; at most 40 unplaced changes per cycle with the deletes, D4) |
| `derived/scheduler/invalid_resource_request/{svc}` | `{key}={value}` of the unreadable request | `buildInvalidResourceRequestChanges` resource_requests.go:96; read by `/api/status` → `cca status` "Resources:" line (M243 D5) |
| `derived/instance/{id}/controller_stopped` | "true" | `attemptPreemptions` :251 (only a victim whose stop makes room, D12) |

### Deletes
| Key Pattern | Deleted By |
|---|---|
| `placement/node/{oldNode}/{id}` | `buildRebalanceMoveChanges` :822 |
| `derived/scheduler/unplaced/{id}/{reason,requirements}` | `cleanupStaleUnplacedFacts` :1117 (every stored ID not in this cycle's waiting set: placed, stopped, controller-stopped, other state, removed; M262 D1; within what the puts left of the 40-change budget, D4) |
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
| `Scheduler.Reconcile` | scheduler.go:104 | Runner.executeReconciliationCycle |
| `Scheduler.Watch` | scheduler.go:77 | Runner.setupControllerWatches |
| `shouldDeferInitialPlacement` | scheduler.go:171 | Reconcile |
| `extractNodeInfoFromFacts` | scheduler.go:1613 | Reconcile |
| `extractInstanceInfoFromFacts` | scheduler.go:1579 | Reconcile |
| `extractPlacementsFromFacts` | scheduler.go:1671 | Reconcile |
| `extractServiceResourcesFromFacts` | resource_requests.go:28 | Reconcile (M243 D4: effective-first, MiB rounded up) |
| `extractPlacementConstraints` | scheduler.go:1711 | Reconcile |
| `extractServiceGroupMemberships` | scheduler.go:2067 | Reconcile |
| `extractServicePriorities` | scheduler.go:207 | Reconcile |
| `extractStatefulServices` | scheduler.go:437 | Reconcile |
| `findUnplacedPendingInstances` | scheduler.go:1339 | Reconcile |
| `attemptPlacements` | scheduler.go:856 | Reconcile |
| `collectPlacedInstanceIDs` | scheduler.go:902 | Reconcile |
| `attemptPreemptions` | scheduler.go:251 | Reconcile |
| `findPreemptionVictim` | scheduler.go:340 | attemptPreemptions |
| `isPreemptibleService` | scheduler.go:399 | findPreemptionVictim |
| `stopMakesRoom` | scheduler.go:409 | findPreemptionVictim (M243 D12) |
| `mergeThisCyclePlacements` | scheduler.go:419 | attemptPreemptions (only when a priority > 0 requester exists, P-N1) |
| `newPreemptionReservations` | scheduler.go:312 | attemptPreemptions |
| `preemptionReservations.reserve` | scheduler.go:326 | attemptPreemptions (the only writer of reservations, P-N3) |
| `allowedPreemptionNodes` | scheduler.go:388 | findPreemptionVictim (same node set as placement, review round 2 I1) |
| `selectInstancesWaitingForPlacement` | scheduler.go:918 | Reconcile (waiting set, input of emitUnplacedDemandFacts and cleanupStaleUnplacedFacts, M262 D1) |
| `emitUnplacedDemandFacts` | scheduler.go:932 | Reconcile |
| `extractStoredUnplacedValues` | scheduler.go:965 | Reconcile (input of emitUnplacedDemandFacts: unchanged unplaced facts are not re-put, 3g.1) |
| `classifyUnplacedReason` | scheduler.go:987 | emitUnplacedDemandFacts (invalid request checked first) |
| `countConstraintMatchingNodes` | scheduler.go:1020 | classifyUnplacedReason |
| `buildUnplacedFactChanges` | scheduler.go:1072 | emitUnplacedDemandFacts |
| `buildUnplacedRequirements` | scheduler.go:1093 | buildUnplacedFactChanges |
| `cleanupStaleUnplacedFacts` | scheduler.go:1117 | Reconcile |
| `extractExistingUnplacedInstanceIDs` | scheduler.go:1140 | cleanupStaleUnplacedFacts |
| `rebalancePlacedInstances` | scheduler.go:565 | Reconcile |
| `executeRebalanceMoves` | scheduler.go:595 | rebalancePlacedInstances |
| `collectAliveNodeIDs` | scheduler.go:663 | rebalancePlacedInstances |
| `computeRunningLoadPerNode` | scheduler.go:676 | rebalancePlacedInstances |
| `computeMaxMinLoad` | scheduler.go:697 | rebalancePlacedInstances |
| `sumNodeLoad` | scheduler.go:713 | rebalancePlacedInstances |
| `findOverloadedNodes` | scheduler.go:723 | rebalancePlacedInstances |
| `findMovableInstances` | scheduler.go:738 | executeRebalanceMoves |
| `selectRebalanceTarget` | scheduler.go:785 | executeRebalanceMoves |
| `countServiceInstancesPerNode` | scheduler.go:1354 | attemptPlacements, executeRebalanceMoves |
| `buildRebalanceMoveChanges` | scheduler.go:818 | executeRebalanceMoves |
| `transferRemainingCapacity` | scheduler.go:646 | executeRebalanceMoves (M267 D1) |
| `extractNodesUnderPressure` | scheduler.go:549 | Reconcile (→ rebalanceInputs.nodesUnderPressure, M267 D1) |
| `candidateFitsRequests` | node_heap.go:92 | buildNodeHeap, selectRebalanceTarget, stopMakesRoom, NodeCapacityCache.RecordPlacement (M267 D1) |
| `detectAndLogNodePressure` | scheduler.go:829 | Reconcile |
| `computeNodeLoadAndResourceUsage` | scheduler.go:1372 | attemptPlacements, rebalancePlacedInstances, detectAndLogNodePressure, newPreemptionReservations |
| `buildAliveCandidateNodes` | scheduler.go:1399 | attemptPlacements, rebalancePlacedInstances, newPreemptionReservations |
| `buildNodeIndexMap` | scheduler.go:1423 | attemptPlacements, newPreemptionReservations, executeRebalanceMoves |
| `buildNodeZoneMap` | scheduler.go:1433 | attemptPlacements |
| `computeServiceZonePlacements` | scheduler.go:1447 | attemptPlacements |
| `buildNodeServicesMap` | scheduler.go:1999 | attemptPlacements |
| `partitionUnplacedByGroup` | scheduler.go:2086 | attemptPlacements |
| `placeGroupedInstances` | scheduler.go:2113 | attemptPlacements |
| `placeOneGroup` | scheduler.go:2136 | placeGroupedInstances |
| `findExistingGroupNode` | scheduler.go:2179 | placeOneGroup |
| `computeGroupResourceTotal` | scheduler.go:2199 | placeOneGroup |
| `selectLeastLoadedNode` | scheduler.go:1522 | placeOneGroup |
| `placeUngroupedInstances` | scheduler.go:1179 | attemptPlacements |
| `groupInstancesByService` | scheduler.go:1190 | placeUngroupedInstances |
| `sortedServiceNames` | scheduler.go:1203 | placeUngroupedInstances |
| `placeBatchedInstances` | scheduler.go:1216 | placeUngroupedInstances |
| `filterByConstraints` | scheduler.go:1799 | placeBatchedInstances, selectRebalanceTarget, allowedPreemptionNodes |
| `filterByRestrictions` | scheduler.go:1947 | filterByConstraints (no constraints path) |
| `satisfiesRequireLabels` | scheduler.go:1922 | filterByConstraints |
| `toleratesRestrictions` | scheduler.go:1936 | filterByConstraints, filterByRestrictions |
| `filterOutNodesAlreadyHostingService` | scheduler.go:1773 | placeBatchedInstances (per-node), allowedPreemptionNodes (per-node requester, I1) |
| `rankByPreferences` | scheduler.go:1964 | placeBatchedInstances |
| `rankByAffinityHints` | scheduler.go:2021 | placeBatchedInstances |
| `findMinZones` | scheduler.go:1287 | placeBatchedInstances (zone-spread, maxSkew param) |
| `resolveInstanceResourceNeeds` | scheduler.go:1323 | placeOneGroup, computeGroupResourceTotal |
| `updateNodeResourcesAfterPlacement` | scheduler.go:1474 | placeBatchedInstances, placeOneGroup |
| `NewNodeCapacityCache` | cache.go:27 | placeBatchedInstances |
| `BestNode` | cache.go:39 | placeBatchedInstances |
| `BestNodeInZones` | cache.go:49 | placeBatchedInstances (zone-spread) |
| `RecordPlacement` | cache.go:72 | placeBatchedInstances |
| `RemoveNode` | cache.go:94 | placeBatchedInstances (per-node) |
| `buildNodeHeap` | node_heap.go:66 | NewNodeCapacityCache |
| `selectFromHeap` | node_heap.go:101 | BestNode |
| `countAliveNodes` | scheduler.go:974 | shouldDeferInitialPlacement, emitUnplacedDemandFacts |
| `satisfiesRequireLabelsForNode` | scheduler.go:1047 | countConstraintMatchingNodes |
| `toleratesRestrictionsForNode` | scheduler.go:1059 | countConstraintMatchingNodes |
| `types.CollectEffectiveFirstResourceValues` | types/resource_kind.go:120 | extractServiceResourcesFromFacts (and controllers.collectResourceQuantities) |
| `parseServiceRequest` | resource_requests.go:51 | extractServiceResourcesFromFacts |
| `excludeInvalidRequestInstances` | resource_requests.go:75 | Reconcile |
| `buildInvalidResourceRequestChanges` | resource_requests.go:96 | Reconcile |
| `extractRebalancePinnedServices` | scheduler.go:494 | Reconcile |

## Constants

| Constant | Value | File:Line | Used By |
|---|---|---|---|
| `initialPlacementGracePeriod` | 10s | scheduler.go:29 | shouldDeferInitialPlacement |
| `maxRebalanceMovesPerCycle` | 2 | scheduler.go:32 | executeRebalanceMoves |
| `rebalanceLoadImbalanceThreshold` | 2 | scheduler.go:35 | rebalancePlacedInstances |
| `nodeAllocationPressurePercent` | 80 | scheduler.go:38 | detectAndLogNodePressure |
| `maxUnplacedFactChangesPerCycle` | 40 | scheduler.go:44 | Reconcile → emitUnplacedDemandFacts, cleanupStaleUnplacedFacts (M262 D4) |
| `unplacedFactsPerInstance` | 2 | scheduler.go:48 | cleanupStaleUnplacedFacts |
| `maxPreemptionsPerCycle` | 3 | scheduler.go:200 | attemptPreemptions |
| `types.ResourceKindSpec.RequestKeySuffix` | `/resources/cpu`, `/resources/memory` | types/resource_kind.go:37 | types.SplitServiceResourceKey |

## File Index

| File | Lines | Purpose |
|---|---|---|
| `scheduler/scheduler.go` | ~2208 | Pipeline, filters, rank, rebalance, preemption, pressure |
| `scheduler/resource_requests.go` | ~195 | Effective-first request reading via the shared parser (`types/resource_quantity.go`), invalid-request handling (M243) |
| `scheduler/cache.go` | ~105 | NodeCapacityCache: persistent heap for batch placement |
| `scheduler/node_heap.go` | ~106 | Min-heap (container/heap.Interface), shared fit check `candidateFitsRequests` |
| `scheduler/benchmark_test.go` | — | Placement throughput benchmarks |
| `scheduler/fuzz_test.go` | — | Fuzz testing for placement determinism |
