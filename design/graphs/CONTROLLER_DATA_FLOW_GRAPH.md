# CCattler Controller Data Flow Graph

Machine-readable function call index for every controller's Reconcile
function, its Watch prefixes, output keys, and the dependency graph that
orders them. Purpose: trace fact cascades through the control plane
without reading controller source files.

## 1. Runner Infrastructure (controllers/runner.go)

### Runner.Run → entry point
```
Runner.Run(ctx)                                              :206
├── IF eventLog != nil:
│   └── EventProjector.Run(ctx)                              [goroutine]
│
└── FOR EACH controller:
    └── runSingleController(ctx, controller)                 :248
        └── [retry loop with backoff: 100ms, 500ms, 1s, 2s, 5s, 30s]
            └── runControllerLoop(ctx, controller)           :284
                ├── setupControllerWatches(ctx, ctrl, ...)   :300
                │   └── FOR EACH prefix in ctrl.Watch():
                │       ├── store.Watch(ctx, prefix)
                │       └── forwardWatchEvents(ctx, ch, ...) [goroutine]  :313
                │
                ├── executeReconciliationCycle(ctx, ctrl, nil) [initial]  :419
                │
                └── runReconcileSelectLoop(ctx, ctrl, ...)   :352
                    └── SELECT:
                        ├── watchLost → restart controller
                        ├── resyncChannel (30s) → full reconcile
                        └── reconcileTrigger → debounce(50ms) → reconcile
```

### executeReconciliationCycle → the core loop
```
executeReconciliationCycle(ctx, controller, deltaEvents)     :419
└── FOR attempt 0..maxReconciliationAttempts(5):
    └── attemptSingleReconciliation(ctx, ctrl, events)       :479
        ├── scanFactsForController(ctx, ctrl)                :603
        │   └── FOR EACH prefix in ctrl.Watch():
        │       └── store.ScanWithRevision(ctx, prefix)
        │
        ├── store.SortFacts(allFacts)
        │
        ├── computeReconciliationChanges(ctx, ctrl, ...)     :528
        │   ├── invokeControllerReconcile(ctx, ctrl, ...)    :539
        │   │   ├── IF DeltaController + deltaEvents:
        │   │   │   └── ctrl.ReconcileDelta(ctx, facts, events)
        │   │   │       └── ErrDeltaFallback → fallback below
        │   │   └── ctrl.Reconcile(ctx, facts)
        │   └── metricsCollector.RecordReconciliation(...)   [if collector set]
        │
        ├── enforceWriteDomain(ctrlName, changes)            :695
        │   └── drop changes outside controllerOutputPrefixes
        │
        ├── validateAndCapChanges(ctrlName, changes)         :558
        │   ├── sortChangesByGroupAndKey(changes)            :731
        │   ├── findDuplicateChangeKey(changes)              :785
        │   └── IF > 60 changes:
        │       └── takeWholeGroups(changes, 60)             :747
        │
        ├── buildReconciliationTransaction(...)              :620
        │   ├── per-change revision guards (CAS)
        │   └── appendInputKeyGuards(...)                    :658
        │       └── guards input keys within write domain
        │
        ├── store.Transaction(ctx, compares, ops, nil)
        │   ├── succeeded → commit, break
        │   └── conflict → exponential backoff (10ms–500ms) + retry
        │
        └── executePendingPostCommitOperations(ctx, ctrl)    :589
```

### Constants
| Name | Value | File:Line |
|---|---|---|
| `defaultControllerDebounceInterval` | 50ms | runner.go:26 |
| `defaultControllerResyncInterval` | 30s | runner.go:30 |
| `reconcileRetryBaseDelayMs` | 10 | runner.go:34 |
| `reconcileRetryMaxDelayMs` | 500 | runner.go:38 |
| `MaxTransactionChanges` | 60 | runner.go:55 |
| `defaultMaxReconciliationAttempts` | 5 | runner.go:412 |

---

## 2. Controller Call Trees

### intent-resolver (controllers/intent.go)

```
IntentResolverController.Reconcile(ctx, facts)               :53
├── extractIntentCounts(facts, intent/user/service/)         :258
├── extractIntentCounts(facts, intent/autoscaler/service/)   :258
├── extractPolicyBounds(facts)                               :271
│   └── reads desired/service/{svc}/scale/horizontal/{min,max}
├── extractQuotaCeilings(facts)                              :309
│   └── reads desired/service/{svc}/quota/instances
├── extractEffectiveCounts(facts)                            :304
│   └── reads effective/service/{svc}/instances
├── extractDesiredPlacementModes(facts)                      :360
├── extractEffectivePlacementModes(facts)                    :374
├── countAliveNodesFromFacts(facts)                          :388
│
├── resolvePerNodePlacementChanges(...)                      :403
│   └── FOR EACH per-node service:
│       └── PUT effective/service/{svc}/placement_mode
│
├── collectDeclaredServiceKeys(facts)                        :92   (M217)
│   └── reads desired/service/{svc} markers (exact key)
├── keepDeclaredServices(userIntents, declared)              :102  (M217)
├── resolveEffectiveInstanceCountChanges(...)                :149
│   └── FOR EACH declared service with a user intent:
│       ├── max(user, autoscaler) → raw count
│       ├── clamp to [min, max] bounds
│       ├── clamp to quota ceiling
│       └── IF changed: PUT effective/service/{svc}/instances
├── resolveUndeclaredServiceChanges(...)                     :117  (M217, Flow 31)
│   └── FOR EACH name (sorted) with an intent or effective count but no marker:
│       └── IF effective != 0: PUT effective/service/{svc}/instances = 0
│           (never delete: the instance controller only sees names with an
│            effective count, so 0 is what stops the orphan instances)
│
├── extractAutoscalerRecommendations(facts)                  :303  (by key suffix: team/web works, M243 B5)
│   └── collectServiceResourceValues(facts, intent/autoscaler/service/) :316
│       └── types.SplitServiceResourceKey(key, prefix)       [types/resource_kind.go:95]
├── extractEffectiveResources(facts)                         :309  → map[types.ServiceResourceKey]value
│
├── resolveEffectiveResourceChanges(recommendations, effective) :207
│   └── FOR EACH types.SortedServiceResourceKeys(recommendations) WITH a value:
│       └── PUT types.LookupResourceKindSpec(kind).EffectiveKey(svc) = recommendation (when it differs)
└── deleteEffectiveResourcesWithoutIntent(recommendations, effective) :230  (M243 D7, B3)
    └── FOR EACH effective {svc, kind} (sorted by service, then kind; a struct key, 3f.2) with
        no recommendation (also when desired has none):
        └── DELETE effective/service/{svc}/resources/{kind} (desired applies again)
```

**Watch:** `intent/user/service/`, `intent/autoscaler/service/`, `desired/service/`, `effective/service/`, `observed/node/`
**Writes:** `effective/service/{svc}/instances`, `effective/service/{svc}/resources/{cpu,memory}`, `effective/service/{svc}/placement_mode`

---

### instance (controllers/instance.go)

See INSTANCE_LIFECYCLE_GRAPH.md §1 for full call tree.

**Watch:** `effective/service/`, `desired/service/` (image to record, M241), `observed/instance/`, `derived/instance/`, `derived/service/`
**Writes:** `observed/instance/{id}/*` (DELETE `restarts` on re-creation, M241), `derived/instance/{id}/controller_stopped`, `derived/instance/{id}/image` (M241)

---

### scheduler (scheduler/scheduler.go)

See SCHEDULING_GRAPH.md for full call tree.

**Watch:** `placement/instance/`, `observed/node/`, `observed/instance/`, `desired/service/`, `desired/group/`, `derived/scheduler/unplaced/`, `observed/volume/`, `derived/instance/`, `effective/service/` (M243), `derived/scheduler/invalid_resource_request/` (M243)
**Writes:** `placement/instance/{id}`, `placement/node/{nodeID}/{id}`, `derived/scheduler/unplaced/{id}/*`, `derived/scheduler/invalid_resource_request/{svc}` (M243), `derived/instance/{id}/*`

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
│       ├── stateWithControllerStop(fields)                  [helpers.go:173]
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

**Watch:** `desired/service/` (the image a replacement records, M241 B1), `observed/instance/`, `derived/instance/`, `derived/crash-loop/`
**Writes:** `derived/instance/{id}/controller_stopped`, `derived/instance/{id}/drain_readiness`, `derived/instance/{id}/drain_since`, `derived/crash-loop/{svc}`, `observed/instance/{replacementID}/*`, `derived/instance/{replacementID}/image`

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

### rollout (controllers/rollout.go, rollout_plan.go, rollout_facts.go)

Flow 34 (M241). Behaviour: `openspec/specs/service-rollout`.

```
RolloutController.Reconcile(ctx, facts)                      :82
├── extractRolloutClusterFacts(facts)                        [rollout_facts.go:160]
│   ├── extractDesiredImages                                 :218  desired/service/{svc}/image
│   ├── extractUpdatePolicies                                :239  update/{max_unavailable,max_extra,progress_deadline,min_healthy_time}
│   │   ├── newDefaultUpdatePolicy                           :232  10m deadline, 10s min_healthy_time, pace unset
│   │   └── parseUpdatePolicyInteger                         :277  explicit max_unavailable / max_extra (nil = unset)
│   ├── extractInstancesByService                            :290  sorted by ID
│   │   ├── observed/instance/{id}/{service,state,image,restarts,status-reason,exit-reason,
│   │   │   started_at,created-at,last_exit,exit-reason,probe/readiness}
│   │   │   derived/instance/{id}/{image (shadows observed image),node_failure,controller_stopped}
│   │   └── parseInstanceTimestamp                           :321  started_at, created-at; zero when missing or corrupt
│   ├── extractRolloutFields                                 :395  derived/service/{svc}/rollout/* (history excluded)
│   ├── extractRolloutDisruptionBudgets                      :356
│   ├── extractDesiredInstanceCounts                         :389  desired/service/{svc}/instances
│   ├── effective/service/{svc}/instances (expected count)
│   ├── extractRolloutPausedServices                         :432  desired/service/{svc}/rollout/paused
│   ├── extractReadinessProbedServices                       :335  desired/service/{svc}/probe/readiness/*
│   ├── extractWorkloadKinds                                 :418  effective/service/{svc}/{stateful,placement_mode}
│   │   └── types.WorkloadKindOf                             [types/rollout_pacing.go]  stateful > per-node > stateless
│   └── extractWaitingUnplacedDemand                         [clusterscale.go:226]  derived/scheduler/unplaced/{id}/{reason,requirements}
│                                                            (only instances still waiting for a placement, M262 D2)
│
└── FOR EACH service (sorted) with instances:
    buildServiceInput(svc, now)                              [rollout_facts.go:179]
    │   ├── expected = effective instances, else desired
    │   ├── types.ResolveRolloutPacing(kind, expected, explicit…)  [types/rollout_pacing.go]
    │   │   stateless unset: 25 % down (floor), 25 % up (ceil) (D12); stateful / per-node: 1 / 0,
    │   │   explicit max_extra ignored (D14); both 0 → max_unavailable 1 (forced)
    │   ├── RolloutPacing.CappedByDisruptionBudget           [types/rollout_pacing.go]
    │   └── healthRule = {min_healthy_time, readiness probe declared, now}
    reconcileServiceRollout(input)                           :100
    ├── classifyRolloutInstances(…, rollout/started_at)      [rollout_plan.go:81]  (again after tracking starts)
    │   ├── rolloutHealthRule.evaluate(instance)             [rollout_plan.go:47]
    │   │   → healthy = running ∧ now − started_at ≥ min_healthy_time (no started_at: ok)
    │   │     ∧ (no readiness probe ∨ probe/readiness = ready); else "until HH:MM:SS UTC" or readiness (D10)
    │   ├── old healthy = running (D11)
    │   └── isCreatedSince(instance, started_at)             [rollout_plan.go:117]
    │       → failures = Σ weight of new instances created since started_at − 30 s
    │         (no started_at or created-at: counted), node_failure skipped (D3, D13)
    ├── state none/complete/rolled-back + old exists:
    │   └── beginRolloutTowardDesiredImage(mostCommonImage(old))  :244
    ├── desired ≠ target_image (retarget): known good = previous_image, else the current
    │   target (after a revert, D14) → beginRolloutTowardDesiredImage
    │       └── PUT state=rolling, previous_image (DELETE when desired = known-good: a revert, D13),
    │           target_image, revision+1, history/{rev}, surge_allowance (only max_extra > 0) |
    │           DELETE rollback_reason, rolled_back_*, failures; progress_at=started_at=now, running_new
    ├── classification.isFinished(expected count)           [rollout_plan.go:127]
    │   │   no old left, every new healthy, ≥ expected count healthy
    │   └── finishRollout                                    :300
    │       └── PUT state=complete (rolling, or no rollback_reason: older server, L1) | rolled-back;
    │           DELETE previous_image, target_image, failures, surge_allowance, reason, progress_at,
    │           running_new, started_at
    ├── rolling: PUT failures; failures ≥ computeRolloutFailureThreshold :320 and previous_image set
    │   └── rollBackToPreviousImage                          :278
    │       ├── describeWorstFailingInstance(failing)        [rollout_plan.go:263]  last_exit first
    │       ├── countHealthyOldOnImage(previous)             [rollout_plan.go:136]
    │       └── PUT desired/service/{svc}/image = previous_image, state=rollback,
    │           target_image, rollback_reason, rolled_back_image, rolled_back_at,
    │           progress_at, started_at, running_new, reason | DELETE surge_allowance
    └── advanceRollout                                       :148
        ├── rollback / stalled: max_unavailable ≥ rolloutMinimumRollbackMaxUnavailable (1, D14)
        ├── planOldInstanceStops (not paused)                [rollout_plan.go:174]
        │   → unavailable old first (no budget; not node_failure: B2),
        │     then healthy old in ID order up to max_unavailable − (expected − healthy of any image),
        │     only while a new one is healthy or none exists (D11)
        ├── countOldInstancesLeftAfterStops == 0             :188  → DELETE surge_allowance (D14)
        ├── buildRolloutReason                               [rollout_plan.go:210]
        │   ├── running, not healthy: "waiting for {id} to stay healthy (until HH:MM:SS UTC | readiness …)"
        │   ├── describeWaitingInstance                      [rollout_plan.go:244]
        │   └── budget: "waiting for the first new-image instance (…)" | "max_unavailable 0: waiting
        │       for an extra instance to become healthy" | "max_unavailable N used up: U of E …"
        ├── isProgressDeadlineExceeded                       :215
        │   ├── rolling  → rollBackToPreviousImage("no progress for …")
        │   └── rollback → PUT state=stalled (progress → rollback)
        ├── recordRolloutProgress                            :201  progress_at, running_new (healthy new)
        ├── PUT reason (truncateRolloutReason [rollout_plan.go:312])
        └── buildInstanceStopChanges                         :330
            └── PUT observed/instance/{id}/state = "stopped"
```

All rollout bookkeeping of a service goes through `rolloutFactWriter` (rollout_facts.go:76–125): one change group `rollout/{svc}`, a put only when the value changes, a delete only when the fact exists.

The surge itself is the instance controller's (INSTANCE_LIFECYCLE_GRAPH.md §1): `InstanceController.Reconcile` (instance.go:80) adds `rollout/surge_allowance` to a service's count only when it is neither stateful nor per-node, and never more than `countOldImageInstances` (instance.go:349), the active instances still on another image (D14, review round 3 M1): new instances never outnumber the expected count once the old ones stop, so none is scaled down.

A new instance counts as healthy only once the agent started it on its recorded image: `rolloutHealthRule.evaluate` (rollout_plan.go:48) refuses a running instance whose `observed/instance/{id}/image` differs from `derived/instance/{id}/image` (`extractInstancesByService` keeps both, rollout_facts.go:293), with or without `started_at` (testbed T1, 2e.1). The older-agent rule (no `started_at` = healthy) applies only once the images match. The same check, `isStartedOnRecordedImage` (rollout_plan.go), keeps such an instance out of the failure count in `classifyRolloutInstances`: its restarts and state are still its previous image's (2f.2).

**Read side (API and CLI):**
```
api buildServiceDescribe → buildServiceUpdateStrategy        [api/describe.go:507]
├── store.ReadValueOrEmpty                                   [store/store.go:170]  desired/effective instances, disruption
├── readServiceWorkloadKind                                  [api/describe.go:534]  effective stateful, placement mode
├── readOptionalIntegerFact                                  [api/describe.go:545]  update/{max_unavailable,max_extra}
└── types.ResolveRolloutPacing(…).CappedByDisruptionBudget  → effective values, …IsDefault, …Forced, …Ignored
api buildServiceRollout                                      [api/describe.go:555]  rollout/{state,previous_image,failures,reason,rollback_reason}
cca describe service → describeMaxUnavailableNote / describeMaxExtraNote  [cmd/cca/command_status.go:2305, :2321]
cca status RUNTIME HEALTH ← collectServiceStatusEntries      [cmd/cca/command_server.go:1223]  ReadValueOrEmpty(rollout/*)
apply (applyInstanceLimitCheck.inspect, api/handler_scale.go:180)
├── collectRolledBackImageWarnings                           [api/apply_rollback_warning.go:22]  (D9)
└── collectIgnoredMaxExtraWarnings                           [api/apply_max_extra_warning.go:16]  stateful/per-node max_extra (D14)
```

No fact changes when an instance's `min_healthy_time` ends; the runner's periodic resync (`defaultControllerResyncInterval`, 30 s, runner.go) gives the pass that counts it, as it does for the progress deadline, so a rollout step may count an instance up to 30 s after its window ends.

**Watch:** `desired/service/`, `effective/service/`, `observed/instance/`, `derived/instance/`, `derived/service/`, `derived/scheduler/unplaced/`, `placement/instance/`
**Writes:** `derived/service/{svc}/rollout/{state,previous_image,target_image,revision,history/{rev},failures,surge_allowance,reason,rollback_reason,progress_at,started_at,running_new,rolled_back_image,rolled_back_at}`, `observed/instance/{id}/state`, `desired/service/{svc}/image`

---

### autoscale (controllers/autoscale.go)

```
AutoscaleController.Reconcile(ctx, facts)                    :123
├── extractScalePolicies(facts)                              :579
│   └── reads desired/service/{svc}/scale/horizontal/target/*
├── extractStabilizationWindows(facts)                       :538
├── extractEventTargets(facts)                               :630
├── extractScheduleRules(facts)                              :671
├── extractVerticalBoundsByResource(facts)                   :841
│   └── map[types.ServiceResourceKey]verticalResourceBounds{minimum, maximum, unreadable}, found by the
│       /scale/vertical/ segment (team/web works); kind via types.ParseResourceKind, parsed with
│       types.LookupResourceKindSpec(kind).Parse; unreadable → logged once, flagged (no recommendation, S4)
├── extractCurrentResources(facts, verticalBounds)           :876  → map[types.ServiceResourceKey]int64 (P-N5)
│   ├── types.CollectEffectiveFirstResourceValues(facts), once per cycle (R5-N5) [types/resource_kind.go:120]
│   └── FOR EACH kind in types.ResourceKindSpecs(): parseChosenResourceQuantities(chosen, kind, reader) [helpers.go:121]
│           (effective/service/ first, then desired/service/, no fallback from an unreadable effective
│            value; only services with a vertical policy for that kind (resourceQuantityReader.includeService,
│            review N4); unreadable → left out, logged once → no recommendation)
│
├── buildHorizontalScalingChanges(facts, policies, ...)      :154
│   └── FOR EACH service with scale policy:
│       ├── computeRawHorizontalRecommendation(...)          :214
│       │   └── ceil(currentInstances * currentMetric / targetMetric)
│       ├── clamp to [min, max]
│       ├── applyStabilizationWindow(...)                    :501
│       │   └── windowed max for scale-down, windowed min for scale-up
│       ├── isWithinScheduleWindow(now, schedule)            :743
│       │   └── enforce minimum during window
│       └── PUT intent/autoscaler/service/{svc}/instances = N
│
└── buildVerticalScalingChanges(bounds, current, metrics, recommendations, now) :312
    ├── recordVerticalMetricSamples(...)                      :361
    ├── FOR EACH types.SortedServiceResourceKeys(bounds) (service, then kind; struct key, 3f.2):
    │   ├── kindSpec = lookupVerticalResourceSpec(kind)       :282  (verticalResourceSpecs :273, one entry per
    │   │   kind: toVerticalUnit, boundsInVerticalUnit, formatVerticalUnit; memory = whole MiB, min up /
    │   │   max down via inWholeMebibytes :825, D7 / review finding 1; CPU = keepQuantity / keepBounds)
    │   └── computeVerticalResourceChange(svc, dimension, now) :381
    │       ├── current unknown, a bound unreadable (S4) or no max → no recommendation
    │       ├── computeP95FromSamples(samples)               :474
    │       ├── raw = ceil(current × p95 / target), clampToRange (int64) :513
    │       ├── applyVerticalStabilization(...)               :423  (int history, shared with its tests)
    │       ├── stabilized == current (same unit) → no change (no repeated put, no write loop)
    │       └── PUT kindSpec.IntentKey(svc) = kindSpec.formatVerticalUnit (types.FormatCPUMillicores / FormatMebibytes)
    └── buildRemovedVerticalPolicyDeletions(bounds, recommendations) :345  (per resource, B3)
        └── recommendation whose {svc, kind} has no bounds → DELETE types.LookupResourceKindSpec(kind).IntentKey(svc)
    (bare legacy recommendations are no longer rewritten here: startup migration, below)
```

### Startup migration (controllers/resource_migration.go, M243 D9)

```
cca server → startControllersWithLeaderElection (cmd/cca/command_server.go) builds the HA runner with
NewHARunner(…, ResourceMigrationPrecondition(store) [resource_migration.go:211], …) (required, R5-I1), then
└── go RunControllersAfterResourceMigration(ctx, store, start)  :222  (returns at once to the server: API comes up)
    └── runControllersAfterResourceMigration(ctx, store, backoff, start) :228
        └── runAfterPreconditionSucceeds(ctx, precondition, backoff, start)  [precondition_gate.go:64; generic gate,
            │   P-N7; ControllerPrecondition{Name "resource unit migration", Check} :21, the name in its log, R5-N7]
            ├── LOOP attempt = 1, 2, …:
            │   ├── precondition.Check = MigrateUnitlessResourceRecommendations(ctx, store) :41  (up to
            │   │   │   resourceMigrationMaxAttempts = 5; count = total over batches and attempts, N3)
            │   │   ├── planUnitlessResourceRewrites(ctx, store)     :68  (two explicit passes, P-N8)
            │   │   │   ├── scanMigrationLayer × 3 (desired/ for bounds only, never written; intent; effective) :104
            │   │   │   ├── pass 1: collectRecommendedResources(intentFacts) :92  → set of {svc, kind} with an intent
            │   │   │   └── pass 2: planLayerRewrites(intent, all) + planLayerRewrites(effective, recommended only) :115
            │   │   │       ├── types.SplitServiceResourceKey(key, prefix)  (suffix /resources/{cpu|memory})
            │   │   │       ├── skip unless types.IsBareInteger(value) (idempotent: unit values untouched)
            │   │   │       ├── effective: skip unless its {svc, kind} has an intent (D9, review N6)
            │   │   │       └── formatLegacyQuantity(lookupVerticalResourceSpec(kind), n, bounds) :146
            │   │   │           (a legacy 0 under a declared max becomes the max, CPU and memory, R5-N1; clamp in
            │   │   │            the sizing unit: "{n}m" / whole MiB; a max of 0 is no max; when
            │   │   │            boundsInVerticalUnit(bounds).maximum == 0 (memory < 1 MiB, R5-N2) clamp exactly and
            │   │   │            write types.FormatMemoryBytesExactly ("100k", "512Ki"; 3e.7, 3e.8); B2, D7)
            │   │   └── commitResourceRewrites(ctx, store, rewrites) :162  → (committed count, all committed, error)
            │   │       ├── batchRewritesByService(rewrites)         :189  (whole services; 2 items per rewrite
            │   │       │                                                   (compare + put) ≤ etcdTransactionOperationLimit, C1)
            │   │       └── store.Transaction(Compare{key, scanned revision}, Put{key, unit value}); lost CAS → scan again
            │   ├── success → start() = haControllerRunner.Run(ctx); RETURN true   (controllers start only now, C2)
            │   ├── ctx cancelled → RETURN false, no error log (leadership loss or shutdown, round 2 N-b)
            │   └── failure → log precondition.Name + attempt + next delay; wait
            │                 preconditionRetryBackoff.delayAfterAttempt(attempt) [precondition_gate.go:52] (1s doubling to 1min) or ctx.Done() → RETURN false

Each leadership acquisition (HA_GRAPH: HARunner.startControllers, ha_runner.go:121) runs the leadership
precondition (the same migration, a required NewHARunner argument from cmd/cca) through runAfterPreconditionSucceeds in a goroutine with
controllerCtx, before Runner.Run; leadership loss cancels the retry (D9, review finding 3). HARunner never
names the migration (P-N7).
```

**Watch:** `desired/service/`, `observed/metric/`, `observed/instance/`, `derived/instance/`, `intent/autoscaler/service/`, `derived/service/`, `effective/service/` (current request, M243)
**Writes:** `intent/autoscaler/service/{svc}/instances`, `intent/autoscaler/service/{svc}/resources/{cpu,memory}` (put and delete)

---

### cluster-autoscale (controllers/clusterscale.go)

Not run by `cca server` yet (M260); covered by unit tests.

```
ClusterAutoscaleController.Reconcile(ctx, facts)             :64
├── extractClusterAutoscaleConfig / extractCapacityRequests / extractClusterNodeStates
├── emitScaleUpRequests(facts, requests, config, alive, inFlight) :194
│   ├── extractWaitingUnplacedDemand(facts)                  :226  (also read by rollout, waiting reason)
│   │   ├── parseUnplacedDemandFacts(facts)                  :251  derived/scheduler/unplaced/{id}/{reason,requirements}
│   │   ├── none → return (no instance index built)
│   │   ├── parseInstanceFieldsFromFacts(facts)              [helpers.go:146]  observed/ + derived/instance/{id}/*
│   │   ├── parsePlacementsByNode(facts)                     [drain.go:175]  placement/instance/{id}
│   │   └── drop the entry unless isInstanceWaitingForPlacement :245 (map lookups, O(1) per entry)
│   │       = stateWithControllerStop [helpers.go:173] == pending (observed state, stopped under
│   │         controller_stopped) ∧ no placement: a leftover of a stopped, controller-stopped, placed or
│   │         removed instance is never demand, even before the scheduler deletes it (M262 D2)
│   ├── filterInsufficientCapacityDemand                     :279  insufficient_capacity | no_nodes
│   └── buildNewCapacityRequests (sorted IDs, uncovered, up to free slots) :304 → PUT derived/capacity/request/{id}/*
├── emitScaleDownRequests                                    :349
└── cleanupReadyCapacityRequests                             :416
```

**Watch:** `observed/instance/`, `derived/instance/`, `observed/node/`, `placement/instance/`, `desired/service/`, `derived/scheduler/unplaced/`, `derived/capacity/request/`
**Writes:** `derived/capacity/request/{id}/*`

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
EvictionController.Reconcile(ctx, facts)                     :81
├── extractNodeMemoryCapacity(facts)                         :250
├── extractNodeMemoryAvailable(facts)                        :256
│   └── extractNodeMemoryMiB(facts, suffix)                  :263  (plain MiB integers via Atoi, M243 D6;
│       corrupt → node skipped, warnCorruptNodeMemoryOnce :290 logs it once per key/value via types.OccurrenceSet, review N2, P-N6)
├── extractInstanceNodeMapping(facts)                        :299
├── extractServiceMemoryRequests(facts)                      :385
│   └── collectResourceQuantities(facts, types.ResourceKindMemory, reader) (effective-first) → MiB rounded up; an unreadable request is
│       left out and logged once, so its instances stay candidates credited 0 MiB (D6, decision A:
│       never immune; other instances may then also be evicted in the cycle, up to the cap)
├── extractInstanceServiceMapping(facts)                     :318  (no filtering: every service ranks)
├── extractInstanceStates(facts)                             :337
├── extractServiceQoSClasses(facts)                          :361
├── extractServicePrioritiesFromFacts(facts)                 :429
├── extractExistingNodePressure(facts)                       :397
│
├── reconcileNodePressure(capacity, available, existing)     :117
│   └── FOR EACH node (all in MiB):
│       ├── calculateMemoryUtilizationPercent(cap, avail)    :240
│       ├── IF util >= threshold AND NOT pressured:
│       │   └── PUT derived/node/{nodeID}/pressure = "true"
│       └── IF util < threshold AND pressured:
│           └── DELETE derived/node/{nodeID}/pressure
│
└── FOR EACH pressured node:
    ├── buildEvictionCandidates(nodeID, ...)                 :171
    │   └── priority: BestEffort > Burstable > Guaranteed
    │       within same QoS: lowest priority first
    └── buildEvictionChanges(candidates, ...)                :210
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
├── collectExistingDNSMappings(facts)                        :183
│   └── reads network/dns/{svc} (not hostname/ or instance/ sub-namespaces)
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
| `scheduler` | `placement/`, `derived/scheduler/unplaced/`, `derived/scheduler/invalid_resource_request/`, `derived/instance/` |
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
failure              → instance, init, node-failure, rollout
node-failure         → instance, scheduler
drain                → instance, scheduler, node-failure, cloud-node-lifecycle
network              → endpoint
stateful-dns         → intent-resolver, instance
init                 → instance
rollout              → intent-resolver, instance, scheduler
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

**Cyclic controllers:** instance ↔ failure ↔ node-failure, and rollout ↔ instance, failure (both watch `desired/service/`, which a rollback writes, M241). Kahn's algorithm
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
| `parseInstanceFieldsFromFacts` | InstanceController, FailureController, EndpointController, InstanceGCController, EvictionController, NodeFailureController, extractWaitingUnplacedDemand (M262) |
| `effectiveInstanceState` | buildInstanceMapsFromFields, processInstanceForRecovery, markInstancesOnUnreachableNodesAsFailed |
| `stateWithControllerStop` | buildDesiredEndpoints, isInstanceWaitingForPlacement (M262) |
| `parsePlacementsByNode` | DrainController, extractWaitingUnplacedDemand (M262) |
| `extractWaitingUnplacedDemand` | emitScaleUpRequests (cluster-autoscale), extractRolloutClusterFacts (rollout) |
| `splitFactKeyIntoEntityAndSuffix` | collectStringValuesBySuffix, collectIntValuesBySuffix |
| `types.SplitServiceResourceKey` | planLayerRewrites, collectRecommendedResources, collectServiceResourceValues, types.CollectEffectiveFirstResourceValues |
| `types.CollectEffectiveFirstResourceValues` | collectResourceQuantities (controllers), extractServiceResourcesFromFacts (scheduler); the agent follows the same rule per key |
| `types.LookupResourceKindSpec` / `types.ResourceKindSpecs` | collectResourceQuantities, extractVerticalBoundsByResource, extractCurrentResources, resolveEffectiveResourceChanges, deleteEffectiveResourcesWithoutIntent, buildRemovedVerticalPolicyDeletions, verticalResourceSpecs, scheduler parseServiceRequest, lang validateServiceResourceValues |
| `types.SortedServiceResourceKeys` | buildVerticalScalingChanges, buildRemovedVerticalPolicyDeletions, resolveEffectiveResourceChanges, deleteEffectiveResourcesWithoutIntent |
| `types.OccurrenceSet.IsFirstOccurrence` | types.WarnUnreadableResourceOnce, warnCorruptNodeMemoryOnce (P-N6) |
| `lookupVerticalResourceSpec` | buildVerticalScalingChanges, planLayerRewrites |
| `extractVerticalBoundsByResource` | AutoscaleController.Reconcile, planUnitlessResourceRewrites |
| `extractAutoscalerRecommendations` | IntentResolverController.Reconcile, AutoscaleController.Reconcile |
| `MigrateUnitlessResourceRecommendations` | ResourceMigrationPrecondition (retried until it succeeds) |
| `ResourceMigrationPrecondition` | runControllersAfterResourceMigration, startControllersWithLeaderElection → NewHARunner (required argument, R5-I1) |
| `parseChosenResourceQuantities` | collectResourceQuantities, extractCurrentResources (effective-first map built once per cycle, R5-N5) |
| `RunControllersAfterResourceMigration` | startControllersWithLeaderElection (cmd/cca/command_server.go), in a goroutine; starts the HA runner |
| `runControllersAfterResourceMigration` | RunControllersAfterResourceMigration (server start) |
| `runAfterPreconditionSucceeds` | runControllersAfterResourceMigration (server start), HARunner.startControllers goroutine (each leadership acquisition, D9, P-N7) |
| `planLayerRewrites` / `collectRecommendedResources` / `scanMigrationLayer` | planUnitlessResourceRewrites (P-N8) |
| `verticalResourceBounds.inWholeMebibytes` | verticalResourceSpecs (memory boundsInVerticalUnit: buildVerticalScalingChanges, formatLegacyQuantity; D7) |
| `warnCorruptNodeMemoryOnce` | extractNodeMemoryMiB (eviction, review N2) |
| `collectResourceQuantities` | extractCurrentResources (autoscale), extractServiceMemoryRequests (eviction) |
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
