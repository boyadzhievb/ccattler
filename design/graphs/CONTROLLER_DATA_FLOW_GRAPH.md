# CCattler Controller Data Flow Graph

Machine-readable function call index for every controller's Reconcile
function, its Watch prefixes, output keys, and the dependency graph that
orders them. Purpose: trace fact cascades through the control plane
without reading controller source files.

## 1. Runner Infrastructure (controllers/runner.go)

### Runner.Run → entry point
```
Runner.Run(ctx)                                              :196
├── IF eventLog != nil:
│   └── EventProjector.Run(ctx)                              [goroutine]
│
└── FOR EACH controller:
    └── runSingleController(ctx, controller)                 :238
        └── [retry loop with backoff: 100ms, 500ms, 1s, 2s, 5s, 30s]
            └── runControllerLoop(ctx, controller)           :274
                ├── setupControllerWatches(ctx, ctrl, ...)   :300
                │   └── FOR EACH prefix in ctrl.Watch():
                │       ├── store.Watch(ctx, prefix)
                │       └── forwardWatchEvents(ctx, ch, ...) [goroutine]  :313
                │
                ├── executeReconciliationCycle(ctx, ctrl, nil) [initial]  :409
                │
                └── runReconcileSelectLoop(ctx, ctrl, ...)   :352
                    └── SELECT:
                        ├── watchLost → restart controller
                        ├── resyncChannel (30s) → full reconcile
                        └── reconcileTrigger → debounce(50ms) → reconcile
```

### executeReconciliationCycle → the core loop
```
executeReconciliationCycle(ctx, controller, deltaEvents)     :409
└── FOR attempt 0..maxReconciliationAttempts(5):
    └── attemptSingleReconciliation(ctx, ctrl, events)       :469
        ├── scanFactsForController(ctx, ctrl)                :582
        │   └── FOR EACH prefix in ctrl.Watch():
        │       └── store.ScanWithRevision(ctx, prefix)
        │
        ├── store.SortFacts(allFacts)
        │
        ├── computeReconciliationChanges(ctx, ctrl, ...)     :518
        │   ├── IF DeltaController + deltaEvents:
        │   │   └── ctrl.ReconcileDelta(ctx, facts, events)
        │   │       └── ErrDeltaFallback → fallback below
        │   └── ctrl.Reconcile(ctx, facts)
        │
        ├── enforceWriteDomain(ctrlName, changes)            :674
        │   └── drop changes outside controllerOutputPrefixes
        │
        ├── validateAndCapChanges(ctrlName, changes)         :537
        │   ├── sortChangesByGroupAndKey(changes)            :710
        │   ├── findDuplicateChangeKey(changes)              :764
        │   └── IF > 60 changes:
        │       └── takeWholeGroups(changes, 60)             :726
        │
        ├── buildReconciliationTransaction(...)              :599
        │   ├── per-change revision guards (CAS)
        │   └── appendInputKeyGuards(...)                    :637
        │       └── guards input keys within write domain
        │
        ├── store.Transaction(ctx, compares, ops, nil)
        │   ├── succeeded → commit, break
        │   └── conflict → exponential backoff (10ms–500ms) + retry
        │
        └── executePendingPostCommitOperations(ctx, ctrl)    :568
```

### Constants
| Name | Value | File:Line |
|---|---|---|
| `defaultControllerDebounceInterval` | 50ms | runner.go:26 |
| `defaultControllerResyncInterval` | 30s | runner.go:30 |
| `reconcileRetryBaseDelayMs` | 10 | runner.go:34 |
| `reconcileRetryMaxDelayMs` | 500 | runner.go:38 |
| `maxTransactionChanges` | 60 | runner.go:55 |
| `defaultMaxReconciliationAttempts` | 5 | runner.go:402 |

---

## 2. Controller Call Trees

### intent-resolver (controllers/intent.go)

```
IntentResolverController.Reconcile(ctx, facts)               :53
├── extractIntentCounts(facts, intent/user/service/)         :198
├── extractIntentCounts(facts, intent/autoscaler/service/)   :198
├── extractPolicyBounds(facts)                               :211
│   └── reads desired/service/{svc}/scale/horizontal/{min,max}
├── extractQuotaCeilings(facts)                              :249
│   └── reads desired/service/{svc}/quota/instances
├── extractEffectiveCounts(facts)                            :244
│   └── reads effective/service/{svc}/instances
├── extractDesiredPlacementModes(facts)                      :300
├── extractEffectivePlacementModes(facts)                    :314
├── countAliveNodesFromFacts(facts)                          :328
│
├── resolveEffectiveInstanceCountChanges(...)                :89
│   └── FOR EACH service:
│       ├── max(user, autoscaler) → raw count
│       ├── clamp to [min, max] bounds
│       ├── clamp to quota ceiling
│       └── IF changed: PUT effective/service/{svc}/instances
│
├── resolvePerNodePlacementChanges(...)                      :343
│   └── FOR EACH per-node service:
│       └── PUT effective/service/{svc}/placement_mode
│
├── resolveEffectiveResourceChanges(...)                     :142
│   └── FOR EACH service with autoscaler resource intents:
│       └── PUT effective/service/{svc}/resources/{cpu,memory}
│
└── extractUserResources(facts)                              :383
    └── reads intent/user/service/{svc}/resources/{cpu,memory}
```

**Watch:** `intent/user/service/`, `intent/autoscaler/service/`, `desired/service/`, `effective/service/`, `observed/node/`
**Writes:** `effective/service/{svc}/instances`, `effective/service/{svc}/resources/{cpu,memory}`, `effective/service/{svc}/placement_mode`

---

### instance (controllers/instance.go)

See INSTANCE_LIFECYCLE_GRAPH.md §1 for full call tree.

**Watch:** `effective/service/`, `observed/instance/`, `derived/instance/`, `derived/service/`
**Writes:** `observed/instance/{id}/*`, `derived/instance/{id}/controller_stopped`

---

### scheduler (scheduler/scheduler.go)

See SCHEDULING_GRAPH.md for full call tree.

**Watch:** `placement/instance/`, `observed/node/`, `observed/instance/`, `desired/service/`, `desired/group/`, `derived/scheduler/unplaced/`
**Writes:** `placement/instance/{id}`, `placement/node/{nodeID}/{id}`, `derived/scheduler/unplaced/{id}/*`, `derived/instance/{id}/*`

---

### endpoint (controllers/endpoint.go)

```
EndpointController.Reconcile(ctx, facts)                     :54
├── parseInstanceFieldsFromFacts(facts)                      [helpers.go:160]
├── extractServiceExposedPorts(facts)                        [helpers.go:211]
│   └── reads desired/service/{svc}/expose/{port}
├── detectServicesWithReadinessProbe(facts)                  :71
│   └── reads desired/service/{svc}/probe/readiness/method
├── parseNodeAdvertiseAddresses(facts)                       :85
│   └── collectStringValuesBySuffix(observed/node/, "address")
├── parseExistingEndpoints(facts)                            :92
│   └── reads endpoint/service/{svc}/{id}/{port}
│
├── buildDesiredEndpoints(fields, ports, probes, addrs)      :108
│   └── FOR EACH running instance with exposed port:
│       ├── endpointEligibleState(fields)                    [helpers.go:188]
│       │   └── ignores node_failure (data plane handles it)
│       ├── skip if drain_readiness == "not-ready"
│       ├── skip if readiness probe configured AND not "ready"
│       ├── IF overlay IP → endpoint = ip:port
│       └── IF host port + node address → endpoint = nodeAddr:hostPort
│
└── computeEndpointDiffChanges(desired, existing)            :174
    ├── PUT endpoint/service/{svc}/{id}/{port} = "ip:port"   [create/update]
    └── DELETE endpoint/service/{svc}/{id}/{port}             [stale]
```

**Watch:** `observed/instance/`, `observed/node/`, `endpoint/service/`, `desired/service/`, `derived/instance/`
**Writes:** `endpoint/service/{svc}/{id}/{port}`

---

### failure (controllers/failure.go)

See INSTANCE_LIFECYCLE_GRAPH.md §5 for full call tree.

**Watch:** `observed/instance/`, `derived/instance/`, `derived/crash-loop/`
**Writes:** `derived/instance/{id}/controller_stopped`, `derived/instance/{id}/drain_readiness`, `derived/instance/{id}/drain_since`, `derived/crash-loop/{svc}`, `observed/instance/{replacementID}/*`

---

### node-failure (controllers/nodefailure.go)

See INSTANCE_LIFECYCLE_GRAPH.md §6 for full call tree.

**Watch:** `lease/node/`, `observed/node/`, `observed/instance/`, `derived/instance/`, `placement/instance/`
**Writes:** `observed/node/{id}/state`, `derived/instance/{id}/node_failure`

---

### init (controllers/init.go)

See INSTANCE_LIFECYCLE_GRAPH.md §7 for full call tree.

**Watch:** `desired/service/`, `observed/instance/`, `derived/instance/`
**Writes:** `derived/instance/{id}/init_phase`

---

### rollout (controllers/rollout.go)

```
RolloutController.Reconcile(ctx, facts)                      :46
├── extractDesiredImages(facts)                              :269
├── extractUpdatePolicies(facts)                             :280
│   └── reads desired/service/{svc}/update/{strategy,maxSurge,maxUnavailable}
├── extractInstancesByService(facts)                         :313
│   └── reads observed/instance/{id}/{state,image,service}
├── extractRolloutDisruptionBudgets(facts)                   :340
├── extractDesiredInstanceCounts(facts)                      :373
├── extractRolloutState(facts)                               :402
├── extractRolloutPausedServices(facts)                      :423
│
└── FOR EACH service with image mismatch:
    ├── classifyInstancesByImage(instances, desiredImage)     :113
    │   → old-image instances, new-image instances
    │
    ├── shouldTriggerRollback(newImageFailed, prevImage)      :178
    │   └── IF rollback needed:
    │       └── buildRollbackChanges(svc, prevImage)          :185
    │           └── PUT desired/service/{svc}/image = previousImage
    │
    ├── buildRolloutTrackingChanges(svc, oldInstances, ...)   :138
    │   ├── PUT derived/service/{svc}/rollout/state = "rolling"
    │   ├── PUT derived/service/{svc}/rollout/image = targetImage
    │   ├── PUT derived/service/{svc}/rollout/revision = N+1
    │   └── PUT derived/service/{svc}/rollout/surge_allowance = N
    │
    └── buildOldInstanceStopChanges(oldInstances, budget)     :209
        └── PUT derived/instance/{id}/controller_stopped = "true"
            (respects maxUnavailable and disruption budget)
```

**Watch:** `desired/service/`, `observed/instance/`, `derived/instance/`, `derived/service/`, `placement/instance/`
**Writes:** `derived/service/{svc}/rollout/*`, `derived/service/{svc}/rollout/surge_allowance`, `observed/instance/{id}/state`, `desired/service/{svc}/image`

---

### autoscale (controllers/autoscale.go)

```
AutoscaleController.Reconcile(ctx, facts)                    :123
├── extractScalePolicies(facts)                              :579
│   └── reads desired/service/{svc}/scale/horizontal/target/*
├── extractStabilizationWindows(facts)                       :538
├── extractEventTargets(facts)                               :630
├── extractScheduleRules(facts)                              :671
├── extractVerticalPolicies(facts)                           :743
│
├── buildHorizontalScalingChanges(facts, policies, ...)      :154
│   └── FOR EACH service with scale policy:
│       ├── computeRawHorizontalRecommendation(...)          :214
│       │   └── ceil(currentInstances * currentMetric / targetMetric)
│       ├── clamp to [min, max]
│       ├── applyStabilizationWindow(...)                    :464
│       │   └── windowed max for scale-down, windowed min for scale-up
│       ├── isWithinScheduleWindow(now, schedule)            :706
│       │   └── enforce minimum during window
│       └── PUT intent/autoscaler/service/{svc}/instances = N
│
└── buildVerticalScalingChanges(facts, policies, ...)        :263
    └── FOR EACH service with vertical policy:
        ├── recordVerticalMetricSamples(...)                  :300
        ├── computeVerticalResourceChange(...)                :319
        │   └── computeP95FromSamples(samples)               :413
        ├── applyVerticalStabilization(...)                   :362
        └── PUT intent/autoscaler/service/{svc}/resources/{cpu,memory}
```

**Watch:** `desired/service/`, `observed/metric/`, `observed/instance/`, `derived/instance/`, `intent/autoscaler/service/`, `derived/service/`
**Writes:** `intent/autoscaler/service/{svc}/instances`, `intent/autoscaler/service/{svc}/resources/{cpu,memory}`

---

### drain (controllers/drain.go)

```
DrainController.Reconcile(ctx, facts)                        :52
├── parseDrainingNodes(facts)                                :160
│   └── reads observed/node/{id}/state == "draining"
├── parsePlacementsByNode(facts)                             :175
├── parseInstanceStates(facts)                               :186
├── parseInstanceServices(facts)                             :197
├── parseDrainCompleteMarkers(facts)                         :203
├── parseDrainEvictionMarkers(facts)                         :313
├── parseDisruptionBudgets(facts)                            :224
│   └── reads desired/service/{svc}/disruption/{minAvailable,maxUnavailable}
├── countGlobalActiveInstancesByService(...)                  :257
├── parseDesiredInstanceCounts(facts)                        :279
│
└── FOR EACH draining node (not already complete):
    └── evictInstancesFromNode(nodeID, ...)                  :96
        └── FOR EACH instance on node (sorted):
            ├── canEvictUnderDisruptionBudget(...)            :286
            │   └── check minAvailable / maxUnavailable
            ├── PUT derived/node/{nodeID}/drain/evict/{id}
            └── IF all evicted:
                └── PUT derived/node/{nodeID}/drain/complete
```

**Watch:** `observed/node/`, `placement/instance/`, `observed/instance/`, `derived/instance/`, `desired/service/`, `derived/node/`
**Writes:** `derived/node/{nodeID}/drain/evict/{id}`, `derived/node/{nodeID}/drain/complete`

---

### eviction (controllers/eviction.go)

```
EvictionController.Reconcile(ctx, facts)                     :80
├── extractNodeMemoryCapacity(facts)                         :249
├── extractNodeMemoryAvailable(facts)                        :268
├── extractInstanceNodeMapping(facts)                        :287
├── extractInstanceServiceMapping(facts)                     :306
├── extractInstanceStates(facts)                             :325
├── extractServiceQoSClasses(facts)                          :349
├── extractServiceMemoryRequests(facts)                      :368
├── extractServicePrioritiesFromFacts(facts)                 :419
├── extractExistingNodePressure(facts)                       :387
│
├── reconcileNodePressure(capacity, available, existing)     :116
│   └── FOR EACH node:
│       ├── calculateMemoryUtilizationPercent(cap, avail)    :239
│       ├── IF util >= threshold AND NOT pressured:
│       │   └── PUT derived/node/{nodeID}/pressure = "true"
│       └── IF util < threshold AND pressured:
│           └── DELETE derived/node/{nodeID}/pressure
│
└── FOR EACH pressured node:
    ├── buildEvictionCandidates(nodeID, ...)                 :170
    │   └── priority: BestEffort > Burstable > Guaranteed
    │       within same QoS: lowest priority first
    └── buildEvictionChanges(candidates, ...)                :209
        └── PUT derived/instance/{id}/controller_stopped = "true"
            (enough to free memory below threshold)
```

**Watch:** `observed/node/`, `observed/instance/`, `desired/service/`, `derived/service/`
**Writes:** `derived/node/{nodeID}/pressure`, `derived/instance/{id}/controller_stopped`

---

### network (controllers/network.go)

```
NetworkController.Reconcile(ctx, facts)                      :88
├── collectServicesWithEndpointsFromFacts(facts)             :151
│   └── scans endpoint/service/ → set of service names
├── collectExistingVIPAllocations(facts)                     :165
│   └── reads network/vip/service/{svc}, network/vip/service/{svc}/port
├── collectExistingDNSMappings(facts)                        :182
│   └── reads network/dns/{svc}
├── collectDefinedServiceNamesFromFacts(facts)               :210
│   └── reads desired/service/ root markers
├── determineNextVIPFourthOctet(existingVIPs)                :193
│
├── FOR EACH service with endpoints but no VIP:
│   ├── PUT network/vip/service/{svc} = "10.200.0.{N}"
│   ├── PUT network/vip/service/{svc}/port = portNumber
│   └── PUT network/dns/{svc} = "10.200.0.{N}"
│
└── buildStaleVIPAndDNSRemovalChanges(vips, dns, defined)    :226
    └── DELETE stale VIPs and DNS for removed services
```

**Watch:** `endpoint/service/`, `desired/service/`, `network/vip/service/`, `network/dns/`
**Writes:** `network/vip/service/{svc}`, `network/vip/service/{svc}/port`, `network/dns/{svc}`

---

### instance-gc (controllers/instance_gc.go)

See INSTANCE_LIFECYCLE_GRAPH.md §8 for full call tree.

**Watch:** `observed/instance/`, `derived/instance/`, `effective/service/`, `placement/instance/`
**Writes:** DELETE `observed/instance/{id}/*`, DELETE `derived/instance/{id}/*`, DELETE `placement/instance/{id}`, DELETE `placement/node/{nodeID}/{id}`

---

### Non-controller components

#### EventProjector (controllers/event_projector.go)

```
EventProjector.Run(ctx)                                      :49
├── FOR EACH watched prefix:
│   └── watchPrefixWithReconnect(ctx, prefix)                :80
│       └── [goroutine, auto-reconnect on close]
│           ├── store.Watch(ctx, prefix)
│           └── FOR EACH event:
│               └── projectEvent(ctx, event)                 :175
│                   ├── classifyWatchEventAsSemanticEvent(ev) :226
│                   │   ├── splitInstanceSubPath(fullPath)    :215
│                   │   └── → kind, target, detail
│                   ├── enrichInstanceEventWithServiceName    :189
│                   │   └── "instance/eb7509a28e" → "instance/scheduler-eb7509a28e"
│                   └── eventLog.Emit(kind, target, detail)
│
├── Watched prefixes:
│   observed/instance/, observed/node/, placement/,
│   endpoint/, network/vip/, network/dns/,
│   derived/instance/, derived/service/
│
└── Events available via: cca events, /events, /events/stream (SSE)
```

---

## 3. Topological Sort (controllers/topological_sort.go)

### SortControllersByDependency → entry point
```
SortControllersByDependency(controllers)                     :164
├── buildControllerDependencyGraph(controllers)              :116
│   ├── controllerOutputPrefixes()                           :86
│   │   → map[controllerName][]outputPrefix
│   └── FOR EACH (consumer, producer) pair:
│       └── hasOverlappingPrefix(watched, outputs)           :142
│           └── prefixOverlaps(a, b)                         :154
│               → true if either is prefix of other
│
└── topologicalSort(names, edges)                            :19
    └── Kahn's algorithm:
        ├── compute incomingDegree per controller
        ├── seed queue with degree-0 controllers
        ├── BFS: dequeue, reduce dependents' degree
        └── detect cyclic controllers (not fully sorted)
```

### controllerOutputPrefixes (topological_sort.go:86)

| Controller | Output Prefixes |
|---|---|
| `intent-resolver` | `effective/service/` |
| `instance` | `observed/instance/`, `derived/instance/` |
| `scheduler` | `placement/`, `derived/scheduler/unplaced/`, `derived/instance/` |
| `endpoint` | `endpoint/` |
| `failure` | `observed/instance/`, `derived/instance/`, `derived/crash-loop/` |
| `node-failure` | `observed/node/`, `derived/instance/` |
| `network` | `network/vip/`, `network/dns/` |
| `autoscale` | `intent/autoscaler/` |
| `rollout` | `observed/instance/`, `derived/service/`, `desired/service/` |
| `storage` | `observed/volume/`, `derived/volume/` |
| `warm-zero` | `derived/service/` |
| `init` | `derived/instance/` |
| `eviction` | `derived/instance/`, `derived/node/` |
| `drain` | `derived/node/` |
| `cluster-autoscale` | `derived/capacity/request/` |
| `route` | `derived/route/` |
| `auth` | (none) |

### Undeclared writers (not in controllerOutputPrefixes)

| Controller | Actually Writes |
|---|---|
| `network-policy` | `derived/network/rule/` |
| `job` | `observed/instance/`, `derived/job/` |
| `cron` | `desired/job/`, `derived/cronjob/` |
| `stateful-dns` | `network/dns/instance/` |
| `stateful-volume` | `desired/volume/` |
| `instance-gc` | DELETE `observed/instance/`, `derived/instance/`, `placement/` |
| `credential-broker` | `derived/credential/` |

---

## 4. Controller Dependency Graph

Derived from `buildControllerDependencyGraph()`: if B watches a prefix that A declares as output, B depends on A.

### Adjacency List (controller → depends on)
```
intent-resolver      → autoscale, node-failure, cloud-node-lifecycle
instance             → intent-resolver, failure, node-failure, init, rollout
scheduler            → instance, node-failure, cloud-node-lifecycle
endpoint             → instance, failure, node-failure, cloud-node-lifecycle
failure              → instance, init, node-failure
node-failure         → instance, scheduler
drain                → instance, scheduler, node-failure, cloud-node-lifecycle
network              → endpoint
stateful-dns         → intent-resolver, instance
init                 → instance
rollout              → instance, scheduler
autoscale            → instance, failure, node-failure, init, warm-zero, rollout
warm-zero            → instance, endpoint, rollout
eviction             → instance, node-failure, cloud-node-lifecycle, rollout
route                → endpoint
network-policy       → endpoint, scheduler
cluster-autoscale    → instance, failure, node-failure, init, scheduler, cloud-node-lifecycle
job                  → instance, failure, node-failure, init
cron                 → (no declared dependency)
credential-broker    → instance
cloud-loadbalancer   → endpoint, node-failure, cloud-node-lifecycle
cloud-routes         → node-failure, cloud-node-lifecycle
```

### Computed Tiers (Kahn's algorithm output)

```
Tier 0 (no dependencies):
  auth, cron, credential-broker

Tier 1 (depends on desired/* only):
  autoscale

Tier 2:
  intent-resolver  (← autoscale, node-failure, cloud-node-lifecycle)

Tier 3:
  instance         (← intent-resolver, failure, node-failure, init, rollout)
  stateful-volume  (← intent-resolver)

Tier 4:
  scheduler, init, rollout, failure, node-failure,
  stateful-dns, job

Tier 5:
  endpoint, drain, eviction, cluster-autoscale

Tier 6:
  network, route, network-policy, warm-zero, cloud-loadbalancer

Tier 7:
  Agent (reads all derived keys, programs the node)
```

**Cyclic controllers:** instance ↔ failure ↔ node-failure. Kahn's algorithm
processes what it can, then reports cyclic nodes. These converge through
multiple cycles: failure marks → instance sees marks → scheduler replaces.

---

## 5. Reconcile Trigger Chain (fresh deploy)

```
T+0ms     User writes desired/service/*, desired/connection/*
          ↓ watch events fire

T+50ms    intent-resolver: reads desired/ + intent/
          → writes effective/service/{name}/instances

T+100ms   instance: reads effective/ + observed/instance/
          → writes observed/instance/{id}/state=pending (N new instances)

T+150ms   scheduler: reads observed/instance/ + observed/node/ + placement/
          → writes placement/node/{nodeID}/{id}

T+150ms   init: reads observed/instance/ + desired/service/{name}/init/
          → writes derived/instance/{id}/init_phase=pending

T+200ms   Agent (node): reads placement/node/{nodeID}/, finds new instances
          → reconcileDesiredInstance: pull, init, start
          → writes observed/instance/{id}/state=running
          → writes observed/instance/{id}/ip

T+250ms   endpoint: reads observed/instance/ (running + IP)
          → writes endpoint/service/{svc}/{id}/{port}

T+300ms   network: reads endpoint/ + desired/service/
          → writes network/vip/service/{name}, network/dns/{name}

T+300ms   route: reads endpoint/ + desired/route/
          → writes derived/route/{name}/backends

T+1000ms  Data plane (agent, 1s loop):
          → reads endpoint/, network/vip/, derived/route/
          → programs nftables/eBPF rules

T+1000ms  DNS forwarder (agent):
          → resolves .cca.local via control plane DNS server
```

---

## 6. Agent Reads and Writes

The node agent is NOT a controller — it runs its own loop (1s ticker + watch events).

### Agent reads (from controllers)
| From Controller | Key Pattern |
|---|---|
| scheduler | `placement/node/{nodeID}/{id}` |
| instance | `observed/instance/{id}/state`, `/service` |
| init | `derived/instance/{id}/init_phase` |
| node-failure | `derived/instance/{id}/node_failure` |
| failure | `derived/instance/{id}/controller_stopped` |
| drain | `derived/node/{nodeID}/drain/evict/{id}` |
| user (cca apply) | `desired/service/{name}/*`, `desired/connection/{name}` |

### Agent writes (read by controllers)
| To Controller | Key Pattern |
|---|---|
| instance, endpoint, etc. | `observed/instance/{id}/state` |
| endpoint | `observed/instance/{id}/ip` |
| node-failure | `observed/node/{nodeID}/heartbeat` (via lease) |
| init | `observed/instance/{id}/init/step/{n}/state` |
| autoscale | `observed/metric/{service}/{metric}` |

---

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `parseInstanceFieldsFromFacts` | InstanceController, FailureController, EndpointController, InstanceGCController, EvictionController, NodeFailureController |
| `effectiveInstanceState` | buildInstanceMapsFromFields, processInstanceForRecovery, markInstancesOnUnreachableNodesAsFailed |
| `endpointEligibleState` | buildDesiredEndpoints |
| `splitFactKeyIntoEntityAndSuffix` | collectStringValuesBySuffix, collectIntValuesBySuffix, collectResourceValuesBySuffix |
| `collectStringValuesBySuffix` | parseCurrentNodeStates, parseNodeAdvertiseAddresses |
| `extractServiceExposedPorts` | EndpointController.Reconcile |
| `controllerOutputPrefixes` | buildControllerDependencyGraph, enforceWriteDomain, appendInputKeyGuards |
| `topologicalSort` | SortControllersByDependency |
| `buildControllerDependencyGraph` | SortControllersByDependency |
| `takeWholeGroups` | validateAndCapChanges, InstanceController.Reconcile, FailureController.capOutputChanges |
| `sortChangesByGroupAndKey` | validateAndCapChanges |
| `forwardWatchEvents` | setupControllerWatches (goroutine per prefix) |
| `executeReconciliationCycle` | runControllerLoop (initial), runReconcileSelectLoop (watch/resync) |
| `scanFactsForController` | attemptSingleReconciliation |
