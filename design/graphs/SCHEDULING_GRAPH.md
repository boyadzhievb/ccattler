# CCattler Scheduling & Placement Call Graph

Machine-readable function-level index for the scheduler placement pipeline,
rebalancing, preemption, and per-node mode. Purpose: trace placement decisions
without reading scheduler source files.

## 1. Reconcile Entry Point (scheduler/scheduler.go)

### Scheduler.Reconcile → top-level pipeline
```
Scheduler.Reconcile(ctx, facts)                                  :123
├── extractNodeInfoFromFacts(facts)                              :127/:1703  → map[nodeID]*schedulerNodeInfo
├── extractInstanceInfoFromFacts(facts)                          :128/:1669  → map[instanceID]*schedulerInstanceInfo
│   └── derived/instance/{id}/controller_stopped = true → state stopped: never placed, no
│       load, resources, spread or per-node slot (a surplus the instance controller marked;
│       without it the testbed per-node cpu 1500 rollout stalled, M241 2i.2)
├── extractPlacementsFromFacts(facts)                            :129/:1761  → map[instanceID]nodeID
├── extractServiceResourcesFromFacts(facts)                      :130 [resource_requests.go:28]
│   │   → map[svc]serviceResourceRequirements{cpu millicores, memory MiB rounded up},
│   │     map[svc]"{key}={value}" of an unreadable request (M243 D4/D5)
│   ├── types.CollectEffectiveFirstResourceValues(facts)         [types/resource_kind.go:120; shared with
│   │   controllers.collectResourceQuantities, 3f.3] → {svc, kind} → {FactKey, FactValue}
│   │   (an unreadable effective value does NOT fall back to desired: the agent uses it)
│   └── parseServiceRequest(svc, chosen)                         [resource_requests.go:51]
│       ├── FOR EACH types.ResourceKindSpecs() (CPU first): kindSpec.Parse; memory → types.MemoryBytesToMiBRoundedUp
│       └── parse error → types.WarnUnreadableResourceOnce("scheduler", key, value) → invalid,
│           no request entry (its placed instances count 0: the stated exception)
├── extractPlacementConstraints(facts)                           :131/:1801 → map[svc]*servicePlacementConstraint
├── extractServiceGroupMemberships(facts)                        :133/:2157 → map[svc]groupName
├── extractServicePriorities(facts)                              :134/:233 → map[svc]int
├── extractStatefulServices(facts)                               :135/:470 → map[svc]bool
│
├── findUnplacedPendingInstances(instances, placements)          :138/:1429 → []instanceID
├── excludeInvalidRequestInstances(unplaced, instances, invalid) :139 [resource_requests.go:75]
│   └── → placeableUnplaced (instances of invalid services are never placed nor preempt)
├── buildInvalidResourceRequestChanges(facts, invalid)           :140 [resource_requests.go:96]
│   ├── PUT derived/scheduler/invalid_resource_request/{svc} = "{key}={value}" (when changed)
│   └── DELETE it for services no longer invalid
│
├── shouldDeferInitialPlacement(placements, unplaced, nodes)     :142/:197
│   ├── existing placements > 0 → false (scale-up, not first boot)
│   ├── gracePeriod <= 0 → false (tests)
│   ├── first call → record firstPendingSeenAt, return true
│   ├── elapsed < gracePeriod → true (still waiting)
│   └── elapsed >= gracePeriod → false (proceed)
│
├── attemptPlacements(placeableUnplaced, nodes, ...)             :145/:946
│   [full tree in §2]
│
├── collectPlacedInstanceIDs(changes)                            :151/:992 → map[instanceID]bool
│
├── attemptPreemptions(placeableUnplaced, placed, nodes, changes, inputs) :153/:278
│   → (preemptionChanges, preemptions: the reservations, nil without a requester; M267 B1)
│   [full tree in §5]
│
├── selectInstancesWaitingForPlacement(unplaced, placed)         :163/:1008 → waiting (unplaced \ placed, sorted;
│   computed once, input of both unplaced-fact steps below, M262 D1)
│
├── emitUnplacedDemandFacts(waiting, nodes, ..., invalid, extractStoredUnplacedValues(facts),
│       maxUnplacedFactChangesPerCycle) :164/:1022 → unplacedChanges (puts first in the shared budget, M262 D4)
│   [full tree in §6]
│
├── cleanupStaleUnplacedFacts(facts, waiting, maxUnplacedFactChangesPerCycle − len(unplacedChanges)) :170/:1207
│   → unplacedCleanupChanges
│
├── rebalanceMoveTimes = extractRebalanceMoveTimes(facts)        :174/:586  (M267 D4)
├── rebalanceTime = currentRebalanceTime()                       :175/:105  (injected clock, else time.Now)
├── rebalancePlacedInstances(nodes, rebalanceInputs{...})        :176/:648
│   [full tree in §4; pinned = extractRebalancePinnedServices(facts, stateful, invalid) :180;
│    nodesUnderPressure = extractNodesUnderPressure(facts, nodes) :181/:632 (M267 D1);
│    recentlyMoved = findInstancesInRebalanceCooldown(moveTimes, now) :182/:597 (M267 D4);
│    thisCycleChanges = changes, preemptions (M267 B1)]
├── cleanupGoneInstanceRebalanceFacts(moveTimes, instances, budget) :186/:611  (M267 D4, R2-2)
│   └── DELETE derived/scheduler/rebalanced/{id} of instances no longer known, sorted,
│       within budget = maxUnplacedFactChangesPerCycle − unplaced puts − unplaced deletes (the
│       shared bookkeeping budget, so a cycle without placements stays ≤ 51); a live instance's fact stays
│
└── detectAndLogNodePressure(nodes, placements, instances, ...)  :189/:919
    └── FOR EACH alive node:
        ├── cpuPercent = usedCPU * 100 / availCPU
        ├── memPercent = usedMemory * 100 / availMemory
        └── >= 80% → log WARN "node under allocation pressure"
```

### Watch Prefixes
```
Scheduler.Watch()                                                :86
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
    ├── derived/node/                [eviction's derived/node/{id}/pressure: no rebalance move onto that node,
    │                                 M267 D1; read-only, so the runner adds no input guards for it]
    └── derived/scheduler/rebalanced/ [own move times, the anti-flap cooldown; deleted with the instance,
                                      M267 D4; in the scheduler's write domain (topological_sort.go)]
```

## 2. Placement Pipeline (attemptPlacements)

### attemptPlacements → orchestrates grouped + ungrouped placement
```
attemptPlacements(placeableUnplaced, nodes, instances, ...)      :946
├── computeNodeLoadAndResourceUsage(placements, instances, res)  :955/:1462
│   └── scans placements × instances → loadPerNode, usedCPU (millicores), usedMemory (MiB)  :1470
│       (a service with an unreadable request has no entry: its instances add 0, M243 D5)
│       (stopped instances skipped: their requests are no longer counted from the
│        stop request on, though the container may still run its pre_stop and grace
│        period; otherwise a per-node replacement could not fit on its node until GC.
│        Accepted trade-off, M241 2g.1/2h; release on proof of termination: M246)
│
├── buildAliveCandidateNodes(nodes, usedCPU, usedMemory)         :956/:1489
│   └── FOR EACH node where state == "alive":                    :1491
│       └── → candidateNode{id, availCPU, availMemory, arch, zone, labels, restrictions}
│
├── buildNodeIndexMap(alive)                                     :961/:1513  → map[nodeID]int
├── buildNodeZoneMap(alive)                                      :962/:1523  → map[nodeID]zone
├── computeServiceZonePlacements(placements, instances, zoneMap) :963/:1537  → map[svc]map[zone]int
├── buildNodeServicesMap(placements, instances)                   :964/:2089  → map[nodeID]map[svc]bool
│
├── partitionUnplacedByGroup(unplaced, instances, svcToGroup)    :974/:2176
│   └── → groupedUnplaced map[groupName][]instanceID, ungrouped []instanceID
│
├── placeGroupedInstances(groupedUnplaced, svcToGroup, state)    :976/:2203
│   [full tree in §3]
│
└── placeUngroupedInstances(ungroupedUnplaced, state)            :977/:1269
    └── groupInstancesByService(unplaced, instances)             :1270/:1280
        └── FOR EACH service (sorted):
            └── placeBatchedInstances(svc, instanceIDs, state)   :1273/:1306
                [full tree below]
```

### placeBatchedInstances → filter → rank → heap → assign
```
placeBatchedInstances(serviceName, instanceIDs, state)           :1306
├── filterByConstraints(alive, serviceName, constraints)         :1311/:1889
│   └── FOR EACH candidate:                                      :1902
│       ├── architecture mismatch → skip                         :1903
│       ├── zone mismatch (non-spread) → skip                   :1906
│       ├── satisfiesRequireLabels(candidate, require) → false → skip  :1909/:2012
│       │   └── FOR EACH required k=v: node.labels[k] != v → false
│       ├── toleratesRestrictions(candidate, accept) → false → skip    :1912/:2026
│       │   └── FOR EACH node restriction: !accept[label] → false
│       └── all filtered out → return original list (fallback)   :1918
│
├── [per-node] filterOutNodesAlreadyHostingService(candidates, svc, ...) :1315/:1863
│   └── build occupiedNodes from placements of not stopped instances → remove from candidates
│       (a stopped instance keeps its placement until instance GC, which waits for its
│        replacement; counting it blocked every per-node rollout, M241 2e.2)
│
├── [prefer] rankByPreferences(candidates, prefer, load, cpu, mem)  :1319/:2054
│   └── FOR EACH candidate: count matching prefer labels → preferScore
│       └── stable sort: higher preferScore first
│
├── [colocate|separate] rankByAffinityHints(candidates, coloc, sep, nodeServicesMap) :1323/:2111
│   └── FOR EACH candidate:
│       ├── +1 per colocate target present on node
│       ├── -1 per separate target present on node
│       └── stable sort: higher affinityScore first
│
├── serviceInstancesPerNode = state.serviceInstancesPerNode[svc]  [from countServiceInstancesPerNode :1444; stopped instances skipped, M241 2h.1]
├── NewNodeCapacityCache(candidates, loadPerNode, svcPerNode, reqCPU, reqMem)  :1336
│   └── buildNodeHeap(candidates, load, svcPerNode, cpu, mem)    [node_heap.go:66]
│       └── filter by capacity → heap.Init (soft service spread first)
│
└── FOR EACH instanceID:                                         :1339
    ├── [zone-spread] findMinZones(candidates, svc, zoneCounts, maxSkew)  :1346/:1377
    │   ├── maxSkew from constraint (default 1): zone eligible if count < minCount + maxSkew
    │   └── cache.BestNodeInZones(acceptableZones)               [cache.go:49]
    ├── [default] cache.BestNode()                               [cache.go:39]
    ├── loadPerNode[bestNode]++                                  :1356
    ├── serviceInstancesPerNode[bestNode]++
    ├── cache.RecordPlacement(bestNode, reqCPU, reqMem)          :1358
    │   └── decrement resources, ++load, ++serviceInstanceCount, heap.Fix or heap.Remove [cache.go:72]
    ├── [per-node] cache.RemoveNode(bestNode)                    :1360/[cache.go:94]
    ├── updateNodeResourcesAfterPlacement(alive, bestNode, ...)  :1362/:1564
    └── emit:
        ├── PUT placement/instance/{id} = bestNode               :1366
        └── PUT placement/node/{bestNode}/{id} = ""              :1367
```

## 3. Service Group Placement

### placeGroupedInstances → co-schedule group members on one node
```
placeGroupedInstances(groupedUnplaced, svcToGroup, state)        :2203
└── FOR EACH groupName (sorted):
    └── placeOneGroup(groupName, instanceIDs, state)             :2216/:2226
        ├── findExistingGroupNode(groupName, state)              :2231/:2269
        │   └── scan placements for instances whose service is in this group
        │       └── first node in sorted order (deterministic)
        │
        ├── [no existing node] computeGroupResourceTotal(instanceIDs, state) :2238/:2289
        │   └── sum CPU + memory across all unplaced members
        │   └── selectLeastLoadedNode(alive, load, totalCPU, totalMem)  :2239/:1612
        │       └── linear scan: least-loaded node with sufficient capacity
        │
        └── FOR EACH instanceID:                                 :2246
            ├── resolveInstanceResourceNeeds(info, resources)    :2248/:1413
            ├── loadPerNode[targetNode]++
            ├── updateNodeResourcesAfterPlacement(...)           :2254
            └── emit:
                ├── PUT placement/instance/{id} = targetNode
                └── PUT placement/node/{targetNode}/{id} = ""
```

## 4. Rebalancing

### rebalancePlacedInstances → detect imbalance → migrate
```
Reconcile → extractRebalancePinnedServices(facts, stateful, invalid) :180/:527
├── stateful services (extractStatefulServices :470)
├── services with an unreadable resource request (size unknown, M243 D5)
├── extractSharedVolumeNames(facts)                              :528/:551
│   └── observed/volume/{name}/access == "shared"
└── + services with desired/service/{svc}/volume/{vol} where vol not shared
    [absent access mode = exclusive → pinned]

Reconcile → extractNodesUnderPressure(facts, nodes)              :181/:632  (M267 D1)
└── nodes whose key types.KeyDerivedNodePressure(id) is among the scanned derived/node/ facts
    (exact key match, never parsed) → nodesUnderPressure

Reconcile → extractRebalanceMoveTimes(facts)                     :174/:586  (M267 D4)
└── derived/scheduler/rebalanced/{id} → RFC3339 time (unreadable → zero time: out of the cooldown)
Reconcile → findInstancesInRebalanceCooldown(moveTimes, now)     :182/:597  (M267 D4)
└── now − movedAt < rebalanceMoveCooldown (10 min) → recentlyMoved

rebalanceInputs{placements, instances, serviceResources, placementConstraints, serviceToGroup,
                placedInThisCycle, thisCycleChanges, preemptions, pinnedServices,
                nodesUnderPressure, recentlyMoved, moveTimestamp}  :568  (read-only for the pass)

rebalancePlacedInstances(nodes, inputs)                          :176/:648
├── collectAliveNodeIDs(nodes)                                   :649/:757
│   └── sorted list of alive node IDs
│
├── computeRunningLoadPerNode(placements, instances, alive)      :654/:770
│   └── count running|pending instances per alive node
│
├── computeMaxMinLoad(nodeLoad, alive)                           :655/:791
│   └── maxLoad - minLoad <= 2 → return nil (no imbalance)
│
├── sumNodeLoad(nodeLoad)                                        :660/:807
│   └── idealLoad = totalLoad / aliveNodeCount
│
├── findOverloadedNodes(nodeLoad, alive, idealLoad)              :668/:817
│   └── nodes where load > idealLoad + 1, sorted by load desc
│
├── aliveCandidates = buildRebalanceCandidateNodes(nodes, inputs)  :667/:677  (M267 B1)
│   ├── preemptions != nil → slices.Clone(preemptions.aliveNodes): remaining capacity after
│   │   this cycle's placements and preemptions (victims freed, requesters taken), a copy
│   └── else computeNodeLoadAndResourceUsage(mergeThisCyclePlacements(placements,
│       thisCycleChanges) :681/:452) :682/:1462 → buildAliveCandidateNodes :683/:1489:
│       each alive node's remaining CPU/memory after stored and this cycle's placements
│       of not stopped instances
│
└── executeRebalanceMoves(overloaded, nodeLoad, aliveCandidates, inputs)  :670/:690
    ├── countServiceInstancesPerNode(placements, instances)      :698/:1444  [skips stopped]
    ├── candidateIndexByNode = buildNodeIndexMap(aliveCandidates) :699/:1513
    └── FOR EACH overloaded node:
        ├── findMovableInstances(sourceNode, inputs)             :705/:834
        │   └── filter: running, service opted in (placement/rebalance = "true"),
        │              not in group, not placedInThisCycle, not preempted in this cycle
        │              (preemptions.isPreempted :338, M267 B1), not recentlyMoved (M267 D4),
        │              not pinned (stateful / non-shared volume),
        │              not stillStarting (markInstancesStillStarting:
        │              service declares desired/service/{svc}/probe/startup/method
        │              and observed/instance/{id}/probe/startup != succeeded),
        │              not per-node, no colocate/separate hints
        │
        └── FOR EACH movable instance:
            ├── selectRebalanceTarget(svc, source, aliveCandidates, load, svcPerNode, inputs)  :714/:873
            │   ├── filterByConstraints(aliveCandidates, svc, constraints)  :881/:1889
            │   ├── skip the source and nodesUnderPressure (M267 D1)
            │   ├── skip nodes hosting as many instances of svc as the source (never worsens spread)
            │   ├── skip unless candidateFitsRequests(candidate, serviceResources[svc])  :892/[node_heap.go:92]
            │   │   [the placement fit check, shared with buildNodeHeap, stopMakesRoom, RecordPlacement]
            │   └── → least-loaded remaining node, "" if none (the instance stays; no move)
            │
            ├── GUARD: nodeLoad[target] >= nodeLoad[source] - 1 → skip
            │   [anti-oscillation: move only if genuinely improves balance]
            │
            ├── buildRebalanceMoveChanges(instanceID, source, target, moveTimestamp)  :724/:907
            │   ├── PUT derived/scheduler/rebalanced/{id} = moveTimestamp (RFC3339; cooldown, M267 D4)
            │   ├── PUT placement/instance/{id} = targetNode   (same instance ID: a move, not a replacement)
            │   ├── DELETE placement/node/{sourceNode}/{id}
            │   └── PUT placement/node/{targetNode}/{id} = ""
            │
            └── transferRemainingCapacity(aliveCandidates, index, source, target, requests)  :729/:740
                └── target's remaining CPU/memory −= requests, source's += requests, so later
                    moves in the cycle see the room taken (M267 D1)
            [cap: maxRebalanceMovesPerCycle (2)]

            [all four changes in group rebalance/{id}]

The source agent then removes its container without writing stopped (the instance is placed
on another node, no controller stop), marks it pending until the target publishes it, and the
target agent starts it: INSTANCE_LIFECYCLE_GRAPH § agent cleanup, teardownStaleInstance,
markMovedInstancePending (M267 D2, D4).
```

## 5. Priority & Preemption (M161)

### attemptPreemptions → stop low-priority victims for high-priority requesters
```
Reconcile :153 → attemptPreemptions(placeableUnplaced, placedInThisCycle, nodes, changes,
    preemptionInputs{instances, placements, serviceResources, constraints, priorities,
                     stateful, invalid})                       :253  (read-only; M243 D12: the request map placement uses)

attemptPreemptions(unplaced, placed, nodes, thisCycleChanges, inputs) :278
│   → (changes, reservations; nil reservations without a requester; rebalancing reads them, M267 B1)
│   (placeableUnplaced excludes services with an unreadable request: they never preempt, M243 D5)
├── requesters = unplaced \ placedInThisCycle, priority > 0 only; none → return (no copy, no usage, P-N1)
├── sort requesters: highest priority first, then instanceID    :295
├── reservations = newPreemptionReservations(nodes,              :303/:345  (the only mutable state, N-d)
│       mergeThisCyclePlacements(placements, thisCycleChanges) :452, inputs)
│   ├── computeNodeLoadAndResourceUsage(cyclePlacements, ...) → usedCPU, usedMemory
│   └── {aliveNodes: buildAliveCandidateNodes(...) → remaining cpu (millicores) / memory (MiB),
│        aliveNodeIndex: buildNodeIndexMap(aliveNodes), cyclePlacements, preemptedInstances}
│
└── FOR EACH requester, while preemptionCount < maxPreemptionsPerCycle (explicit counter, P-N4): :307
    ├── findPreemptionVictim(svc, inputs, reservations)          :312/:373  (single victim, D12; only finds, P-N3)
    │   ├── allowedPreemptionNodes(requester, inputs, reservations) :381/:421  (as placement does):
    │   │   ├── filterByConstraints(aliveNodes, requester, constraints)
    │   │   └── per-node requester → filterOutNodesAlreadyHostingService(…, reservations.cyclePlacements, …)
    │   │       (stored + this cycle's + reserved placements; review round 2 I1)
    │   └── FOR EACH stored placement (instance → node):         :386
    │       ├── skip if already preempted, not running, or its node is not alive and allowed
    │       ├── skip if same service, or victimPriority >= requesterPriority [strictly less]  :394
    │       ├── skip unless isPreemptibleService(victim)         :394/:432  (not per-node, stateful or
    │       │                                                           unreadable request, M243 B4)
    │       ├── skip unless stopMakesRoom(node, victim requests, requester requests) :397/:442
    │       │   (candidateFitsRequests on node remaining + victim's cpu/MiB; a zero request always fits)
    │       └── collect → sort by priority asc, then node ID, then instanceID  :405
    │   └── first candidate, or "" (nothing preempted; the requester stays insufficient_capacity)
    ├── reservations.reserve(requester, victim, node, inputs)    :316/:359  (the only writer, P-N3):
    │   node remaining += victim − requester; victim marked preempted; cyclePlacements[requester] = node
    │   (a later per-node requester of the service skips it)
    │
    └── emit:
        └── PUT derived/instance/{victimID}/controller_stopped = "true"  :329
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
emitUnplacedDemandFacts(waiting, nodes, instances, ..., invalid, stored, changeBudget) :1022
│   changeBudget = maxUnplacedFactChangesPerCycle (40, scheduler.go:50), shared with cleanup (M262 D4)
│   waiting = selectInstancesWaitingForPlacement(unplaced, placedInThisCycle) :1008
│   stored = extractStoredUnplacedValues(facts) :1055  (derived/scheduler/unplaced/ is a watched prefix,
│   so the scheduler's own last writes are in its input)
├── countAliveNodes(nodes)                                       :1032/:1064
│
└── FOR EACH waiting instance (sorted):
    ├── classifyUnplacedReason(info, alive, nodes, constraints, invalid) :1036/:1077
    │   ├── service request unreadable → "invalid_resource_request" (checked FIRST, so it is never
    │   │   "no_nodes", which cluster scaling counts as demand; filterInsufficientCapacityDemand
    │   │   is an allow-list and ignores it, M243 D5)
    │   ├── aliveCount == 0 → "no_nodes"
    │   ├── countConstraintMatchingNodes() == 0 → "volume_home_node_unavailable" | "unsatisfiable_constraint" :1098/:1110
    │   └── else → "insufficient_capacity" (a request no node's remaining cpu/MiB covers)
    │
    └── buildUnplacedFactChanges(instanceID, reason, requirements) :1038/:1162
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
cleanupStaleUnplacedFacts(facts, waiting, changeBudget)         :1207
│   changeBudget = what emitUnplacedDemandFacts left of maxUnplacedFactChangesPerCycle (D4)
├── waiting → set (O(1) membership)
├── extractExistingUnplacedInstanceIDs(facts)                    :1230 (sorted)
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
| `placement/instance/{id}` | self | `extractPlacementsFromFacts` :1761 |
| `observed/node/{id}/state` | agent | `extractNodeInfoFromFacts` :1703 |
| `observed/node/{id}/available_cpu` | agent | `extractNodeInfoFromFacts` :1703 |
| `observed/node/{id}/available_memory` | agent | `extractNodeInfoFromFacts` :1703 |
| `observed/node/{id}/architecture` | agent | `extractNodeInfoFromFacts` :1703 |
| `observed/node/{id}/zone` | agent | `extractNodeInfoFromFacts` :1703 |
| `observed/node/{id}/label/{key}` | agent | `extractNodeInfoFromFacts` :1703 |
| `observed/node/{id}/restrict/{key}` | agent | `extractNodeInfoFromFacts` :1703 |
| `observed/instance/{id}/state` | agent | `extractInstanceInfoFromFacts` :1669 |
| `observed/instance/{id}/service` | agent | `extractInstanceInfoFromFacts` :1669 |
| `effective/service/{svc}/resources/cpu` | intent resolver (from the vertical autoscaler) | `extractServiceResourcesFromFacts` resource_requests.go:28, read first (M243 D4) |
| `effective/service/{svc}/resources/memory` | intent resolver | `extractServiceResourcesFromFacts`, read first |
| `desired/service/{svc}/resources/cpu` | compiler | `extractServiceResourcesFromFacts` (when no effective value); `types.ParseCPUMillicores` |
| `desired/service/{svc}/resources/memory` | compiler | `extractServiceResourcesFromFacts`; `types.ParseMemoryBytes`, then MiB rounded up |
| `derived/scheduler/invalid_resource_request/{svc}` | self | `buildInvalidResourceRequestChanges` (stale ones deleted) |
| `desired/service/{svc}/placement/*` | compiler | `extractPlacementConstraints` :1801 |
| `desired/service/{svc}/priority` | compiler | `extractServicePriorities` :233 |
| `desired/service/{svc}/stateful` | compiler | `extractStatefulServices` :470 |
| `desired/service/{svc}/placement/rebalance` | compiler | `extractPlacementConstraints` → `constraint.rebalance` (opt-in for `findMovableInstances`) |
| `desired/service/{svc}/volume/{vol}` | compiler | `extractRebalancePinnedServices` :527 |
| `observed/volume/{vol}/access` | node agent | `extractSharedVolumeNames` :551 |
| `observed/volume/{vol}/home_node` | node agent | `applyVolumeHomeNodeConstraints` (Flow 26: hard placement pin; `filterByConstraints`, `countConstraintMatchingNodes`, `placeOneGroup`) |
| `desired/service/{svc}/probe/startup/method` | compiler | `extractServicesWithStartupProbe` |
| `observed/instance/{id}/probe/startup` | node agent | `extractInstanceInfoFromFacts` |
| `derived/instance/{id}/controller_stopped` | instance, failure, scheduler (preemption) | `extractInstanceInfoFromFacts` (counts as stopped, M241 2i.2) |
| `desired/group/{grp}/process/{svc}` | compiler | `extractServiceGroupMemberships` :2157 |
| `derived/scheduler/unplaced/{id}/*` | self | `extractStoredUnplacedValues` (compare before put, 3g.1), `extractExistingUnplacedInstanceIDs` :1230 |
| `derived/node/{id}/pressure` | eviction controller | `extractNodesUnderPressure` :632 (no rebalance move onto the node, M267 D1) |
| `derived/scheduler/rebalanced/{id}` | self | `extractRebalanceMoveTimes` :586 (move cooldown, M267 D4) |

### Writes
| Key Pattern | Value | Written By |
|---|---|---|
| `placement/instance/{id}` | nodeID | `placeBatchedInstances` :1366, `placeOneGroup` :2257, `buildRebalanceMoveChanges` :911 |
| `placement/node/{nodeID}/{id}` | "" | `placeBatchedInstances` :1367, `placeOneGroup` :2258, `buildRebalanceMoveChanges` :913 |
| `derived/scheduler/unplaced/{id}/reason` | reason string (incl. `invalid_resource_request`) | `buildUnplacedFactChanges` :1162 (put only while the instance waits and the stored value differs; the filter is `emitUnplacedDemandFacts` :1022 over `selectInstancesWaitingForPlacement` :1008, 3g.1, M262; at most 40 unplaced changes per cycle with the deletes, D4) |
| `derived/scheduler/unplaced/{id}/requirements` | JSON | `buildUnplacedFactChanges` :1162 (put only while the instance waits and the stored value differs; the filter is `emitUnplacedDemandFacts` :1022 over `selectInstancesWaitingForPlacement` :1008, 3g.1, M262; at most 40 unplaced changes per cycle with the deletes, D4) |
| `derived/scheduler/invalid_resource_request/{svc}` | `{key}={value}` of the unreadable request | `buildInvalidResourceRequestChanges` resource_requests.go:96; read by `/api/status` → `cca status` "Resources:" line (M243 D5) |
| `derived/instance/{id}/controller_stopped` | "true" | `attemptPreemptions` :329 (only a victim whose stop makes room, D12) |
| `derived/scheduler/rebalanced/{id}` | RFC3339 move time | `buildRebalanceMoveChanges` :910 (in the move's group; cooldown, M267 D4) |

### Deletes
| Key Pattern | Deleted By |
|---|---|
| `placement/node/{oldNode}/{id}` | `buildRebalanceMoveChanges` :912 |
| `derived/scheduler/unplaced/{id}/{reason,requirements}` | `cleanupStaleUnplacedFacts` :1207 (every stored ID not in this cycle's waiting set: placed, stopped, controller-stopped, other state, removed; M262 D1; within what the puts left of the 40-change budget, D4) |
| `derived/scheduler/invalid_resource_request/{svc}` | `buildInvalidResourceRequestChanges` (request readable again) |
| `derived/scheduler/rebalanced/{id}` | `cleanupGoneInstanceRebalanceFacts` :624 (instance no longer known; within what the unplaced facts left of their 40-change budget, M267 D4, R2-2) |

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
| `Scheduler.Reconcile` | scheduler.go:123 | Runner.executeReconciliationCycle |
| `Scheduler.Watch` | scheduler.go:86 | Runner.setupControllerWatches |
| `shouldDeferInitialPlacement` | scheduler.go:197 | Reconcile |
| `extractNodeInfoFromFacts` | scheduler.go:1703 | Reconcile |
| `extractInstanceInfoFromFacts` | scheduler.go:1669 | Reconcile |
| `extractPlacementsFromFacts` | scheduler.go:1761 | Reconcile |
| `extractServiceResourcesFromFacts` | resource_requests.go:28 | Reconcile (M243 D4: effective-first, MiB rounded up) |
| `extractPlacementConstraints` | scheduler.go:1801 | Reconcile |
| `extractServiceGroupMemberships` | scheduler.go:2157 | Reconcile |
| `extractServicePriorities` | scheduler.go:233 | Reconcile |
| `extractStatefulServices` | scheduler.go:470 | Reconcile |
| `findUnplacedPendingInstances` | scheduler.go:1429 | Reconcile |
| `attemptPlacements` | scheduler.go:946 | Reconcile |
| `collectPlacedInstanceIDs` | scheduler.go:992 | Reconcile |
| `attemptPreemptions` | scheduler.go:278 | Reconcile |
| `findPreemptionVictim` | scheduler.go:373 | attemptPreemptions |
| `isPreemptibleService` | scheduler.go:432 | findPreemptionVictim |
| `stopMakesRoom` | scheduler.go:442 | findPreemptionVictim (M243 D12) |
| `mergeThisCyclePlacements` | scheduler.go:452 | attemptPreemptions (only when a priority > 0 requester exists, P-N1), buildRebalanceCandidateNodes (no preemption this cycle, M267 B1) |
| `preemptionReservations.isPreempted` | scheduler.go:338 | findMovableInstances (nil-safe; M267 B1) |
| `newPreemptionReservations` | scheduler.go:345 | attemptPreemptions |
| `preemptionReservations.reserve` | scheduler.go:359 | attemptPreemptions (the only writer of reservations, P-N3) |
| `allowedPreemptionNodes` | scheduler.go:421 | findPreemptionVictim (same node set as placement, review round 2 I1) |
| `selectInstancesWaitingForPlacement` | scheduler.go:1008 | Reconcile (waiting set, input of emitUnplacedDemandFacts and cleanupStaleUnplacedFacts, M262 D1) |
| `emitUnplacedDemandFacts` | scheduler.go:1022 | Reconcile |
| `extractStoredUnplacedValues` | scheduler.go:1055 | Reconcile (input of emitUnplacedDemandFacts: unchanged unplaced facts are not re-put, 3g.1) |
| `classifyUnplacedReason` | scheduler.go:1077 | emitUnplacedDemandFacts (invalid request checked first) |
| `countConstraintMatchingNodes` | scheduler.go:1110 | classifyUnplacedReason |
| `buildUnplacedFactChanges` | scheduler.go:1162 | emitUnplacedDemandFacts |
| `buildUnplacedRequirements` | scheduler.go:1183 | buildUnplacedFactChanges |
| `cleanupStaleUnplacedFacts` | scheduler.go:1207 | Reconcile |
| `extractExistingUnplacedInstanceIDs` | scheduler.go:1230 | cleanupStaleUnplacedFacts |
| `rebalancePlacedInstances` | scheduler.go:648 | Reconcile |
| `executeRebalanceMoves` | scheduler.go:690 | rebalancePlacedInstances |
| `collectAliveNodeIDs` | scheduler.go:757 | rebalancePlacedInstances |
| `computeRunningLoadPerNode` | scheduler.go:770 | rebalancePlacedInstances |
| `computeMaxMinLoad` | scheduler.go:791 | rebalancePlacedInstances |
| `sumNodeLoad` | scheduler.go:807 | rebalancePlacedInstances |
| `findOverloadedNodes` | scheduler.go:817 | rebalancePlacedInstances |
| `findMovableInstances` | scheduler.go:834 | executeRebalanceMoves |
| `selectRebalanceTarget` | scheduler.go:873 | executeRebalanceMoves |
| `countServiceInstancesPerNode` | scheduler.go:1444 | attemptPlacements, executeRebalanceMoves |
| `buildRebalanceMoveChanges` | scheduler.go:907 | executeRebalanceMoves |
| `transferRemainingCapacity` | scheduler.go:740 | executeRebalanceMoves (M267 D1) |
| `extractNodesUnderPressure` | scheduler.go:632 | Reconcile (→ rebalanceInputs.nodesUnderPressure, M267 D1) |
| `buildRebalanceCandidateNodes` | scheduler.go:677 | rebalancePlacedInstances (M267 B1) |
| `Scheduler.currentRebalanceTime` | scheduler.go:105 | Reconcile (M267 D4) |
| `extractRebalanceMoveTimes` | scheduler.go:586 | Reconcile (M267 D4) |
| `findInstancesInRebalanceCooldown` | scheduler.go:597 | Reconcile (→ rebalanceInputs.recentlyMoved, M267 D4) |
| `cleanupGoneInstanceRebalanceFacts` | scheduler.go:611 | Reconcile (M267 D4; budget shared with the unplaced facts, R2-2) |
| `candidateFitsRequests` | node_heap.go:92 | buildNodeHeap, selectRebalanceTarget, stopMakesRoom, NodeCapacityCache.RecordPlacement (M267 D1) |
| `detectAndLogNodePressure` | scheduler.go:919 | Reconcile |
| `computeNodeLoadAndResourceUsage` | scheduler.go:1462 | attemptPlacements, buildRebalanceCandidateNodes, detectAndLogNodePressure, newPreemptionReservations |
| `buildAliveCandidateNodes` | scheduler.go:1489 | attemptPlacements, buildRebalanceCandidateNodes, newPreemptionReservations |
| `buildNodeIndexMap` | scheduler.go:1513 | attemptPlacements, newPreemptionReservations, executeRebalanceMoves |
| `buildNodeZoneMap` | scheduler.go:1523 | attemptPlacements |
| `computeServiceZonePlacements` | scheduler.go:1537 | attemptPlacements |
| `buildNodeServicesMap` | scheduler.go:2089 | attemptPlacements |
| `partitionUnplacedByGroup` | scheduler.go:2176 | attemptPlacements |
| `placeGroupedInstances` | scheduler.go:2203 | attemptPlacements |
| `placeOneGroup` | scheduler.go:2226 | placeGroupedInstances |
| `findExistingGroupNode` | scheduler.go:2269 | placeOneGroup |
| `computeGroupResourceTotal` | scheduler.go:2289 | placeOneGroup |
| `selectLeastLoadedNode` | scheduler.go:1612 | placeOneGroup |
| `placeUngroupedInstances` | scheduler.go:1269 | attemptPlacements |
| `groupInstancesByService` | scheduler.go:1280 | placeUngroupedInstances |
| `sortedServiceNames` | scheduler.go:1293 | placeUngroupedInstances |
| `placeBatchedInstances` | scheduler.go:1306 | placeUngroupedInstances |
| `filterByConstraints` | scheduler.go:1889 | placeBatchedInstances, selectRebalanceTarget, allowedPreemptionNodes |
| `filterByRestrictions` | scheduler.go:2037 | filterByConstraints (no constraints path) |
| `satisfiesRequireLabels` | scheduler.go:2012 | filterByConstraints |
| `toleratesRestrictions` | scheduler.go:2026 | filterByConstraints, filterByRestrictions |
| `filterOutNodesAlreadyHostingService` | scheduler.go:1863 | placeBatchedInstances (per-node), allowedPreemptionNodes (per-node requester, I1) |
| `rankByPreferences` | scheduler.go:2054 | placeBatchedInstances |
| `rankByAffinityHints` | scheduler.go:2111 | placeBatchedInstances |
| `findMinZones` | scheduler.go:1377 | placeBatchedInstances (zone-spread, maxSkew param) |
| `resolveInstanceResourceNeeds` | scheduler.go:1413 | placeOneGroup, computeGroupResourceTotal |
| `updateNodeResourcesAfterPlacement` | scheduler.go:1564 | placeBatchedInstances, placeOneGroup |
| `NewNodeCapacityCache` | cache.go:27 | placeBatchedInstances |
| `BestNode` | cache.go:39 | placeBatchedInstances |
| `BestNodeInZones` | cache.go:49 | placeBatchedInstances (zone-spread) |
| `RecordPlacement` | cache.go:72 | placeBatchedInstances |
| `RemoveNode` | cache.go:94 | placeBatchedInstances (per-node) |
| `buildNodeHeap` | node_heap.go:66 | NewNodeCapacityCache |
| `selectFromHeap` | node_heap.go:101 | BestNode |
| `countAliveNodes` | scheduler.go:1064 | shouldDeferInitialPlacement, emitUnplacedDemandFacts |
| `satisfiesRequireLabelsForNode` | scheduler.go:1137 | countConstraintMatchingNodes |
| `toleratesRestrictionsForNode` | scheduler.go:1149 | countConstraintMatchingNodes |
| `types.CollectEffectiveFirstResourceValues` | types/resource_kind.go:120 | extractServiceResourcesFromFacts (and controllers.collectResourceQuantities) |
| `parseServiceRequest` | resource_requests.go:51 | extractServiceResourcesFromFacts |
| `excludeInvalidRequestInstances` | resource_requests.go:75 | Reconcile |
| `buildInvalidResourceRequestChanges` | resource_requests.go:96 | Reconcile |
| `extractRebalancePinnedServices` | scheduler.go:527 | Reconcile |

## Constants

| Constant | Value | File:Line | Used By |
|---|---|---|---|
| `initialPlacementGracePeriod` | 10s | scheduler.go:30 | shouldDeferInitialPlacement |
| `maxRebalanceMovesPerCycle` | 2 | scheduler.go:33 | executeRebalanceMoves |
| `rebalanceLoadImbalanceThreshold` | 2 | scheduler.go:36 | rebalancePlacedInstances |
| `rebalanceMoveCooldown` | 10m | scheduler.go:40 | findInstancesInRebalanceCooldown (M267 D4) |
| `nodeAllocationPressurePercent` | 80 | scheduler.go:43 | detectAndLogNodePressure |
| `maxUnplacedFactChangesPerCycle` | 40 | scheduler.go:50 | Reconcile → emitUnplacedDemandFacts, cleanupStaleUnplacedFacts (M262 D4), cleanupGoneInstanceRebalanceFacts (M267 R2-2) |
| `unplacedFactsPerInstance` | 2 | scheduler.go:54 | cleanupStaleUnplacedFacts |
| `maxPreemptionsPerCycle` | 3 | scheduler.go:229 | attemptPreemptions |
| `types.ResourceKindSpec.RequestKeySuffix` | `/resources/cpu`, `/resources/memory` | types/resource_kind.go:37 | types.SplitServiceResourceKey |

## File Index

| File | Lines | Purpose |
|---|---|---|
| `scheduler/scheduler.go` | ~2299 | Pipeline, filters, rank, rebalance, preemption, pressure |
| `scheduler/resource_requests.go` | ~195 | Effective-first request reading via the shared parser (`types/resource_quantity.go`), invalid-request handling (M243) |
| `scheduler/cache.go` | ~105 | NodeCapacityCache: persistent heap for batch placement |
| `scheduler/node_heap.go` | ~106 | Min-heap (container/heap.Interface), shared fit check `candidateFitsRequests` |
| `scheduler/benchmark_test.go` | — | Placement throughput benchmarks |
| `scheduler/fuzz_test.go` | — | Fuzz testing for placement determinism |
