# Instance Lifecycle Graph Reference

Machine-readable function call index for the full lifecycle of a single
CCattler instance — from creation to garbage collection. Purpose: trace
why an instance is stuck in any state without reading source.

## 1. Instance Creation (controllers/instance.go)

### InstanceController.Reconcile → entry point
```
InstanceController.Reconcile(ctx, facts)                     :77
├── parseEffectiveServiceFacts(facts)                        :310
│   └── FOR EACH effective/service/{svc}/ fact:
│       ├── suffix "instances" → desiredCounts[svc]
│       ├── suffix "stateful"  → statefulServices[svc]
│       └── suffix "placement_mode" → perNodeServices[svc]
│
├── extractSurgeAllowances(facts)                            :340
│   └── reads derived/service/{svc}/rollout/surge_allowance
│       → desiredCounts[svc] += surgeCount
│
├── parseInstanceFieldsFromFacts(facts)                      [helpers.go:160]
│   └── scans observed/instance/ + derived/instance/
│       → map[instanceID]map[field]value
│
├── buildInstanceMapsFromFields(fieldsByInstanceID)           :359
│   ├── effectiveInstanceState(fields)                       [helpers.go:198]
│   │   ├── controller_stopped == "true" → InstanceStopped
│   │   ├── node_failure == "true"       → InstanceFailed
│   │   └── else                         → observed state
│   └── → stateByInstanceID, serviceByInstanceID
│
├── buildActiveInstanceIDsByService(...)                      :375
│   └── groups non-stopped IDs by service, sorted
│
├── reconcileScaleDowns(...)                                 :131
│   └── FOR EACH service where haveCount > wantCount:
│       ├── perNodeServices → markPerNodeExcessInstancesAsStopped  :528
│       │   └── priority: failedNode > pending > running
│       ├── statefulServices → stopHighestOrdinalInstances         :465
│       │   └── reverse ordinal order, writes controller_stopped
│       └── default → markExcessStatelessInstancesAsStopped        :501
│           └── selectInstancesForRemoval(pending first)           :570
│               → writes derived/instance/{id}/controller_stopped
│
├── computeServiceDeficits(...)                              :174
│   └── FOR EACH service where haveCount < wantCount:
│       ├── stateful → createNextStatefulInstance             :420
│       │   └── ordered startup: N+1 only after N is running
│       │       clears stale controller_stopped / node_failure
│       └── stateless → deficit = wantCount - haveCount
│
└── allocateCreationsFairly(deficits, ...)                   :209
    ├── baseShare = budget / serviceCount, cap at deficit
    ├── remainder slots → largest unmet demand first
    ├── stateful → createNextStatefulInstance                 :420
    │   └── writes observed/instance/{svc}-{ord}/*
    └── stateless → createPendingInstances                   :482
        └── FOR EACH count:
            └── writes grouped:
                ├── observed/instance/{id}        = ""
                ├── observed/instance/{id}/service = serviceName
                └── observed/instance/{id}/state   = "pending"
```

### Store Key Reads
| Key Pattern | Purpose |
|---|---|
| `effective/service/{svc}/instances` | Desired count |
| `effective/service/{svc}/stateful` | Ordinal ID mode |
| `effective/service/{svc}/placement_mode` | Per-node mode |
| `observed/instance/{id}/state` | Current instance state |
| `observed/instance/{id}/service` | Owning service |
| `derived/instance/{id}/controller_stopped` | Stop marker |
| `derived/instance/{id}/node_failure` | Node failure marker |
| `derived/service/{svc}/rollout/surge_allowance` | Rollout surge |

### Store Key Writes
| Key Pattern | Value | Condition |
|---|---|---|
| `observed/instance/{id}` | `""` | Scale up |
| `observed/instance/{id}/service` | `serviceName` | Scale up |
| `observed/instance/{id}/state` | `"pending"` | Scale up |
| `observed/instance/{id}/ordinal` | `"N"` | Stateful scale up |
| `derived/instance/{id}/controller_stopped` | `"true"` | Scale down |
| DELETE `derived/instance/{id}/controller_stopped` | — | Stateful re-create |
| DELETE `derived/instance/{id}/node_failure` | — | Stateful re-create |

### Constants
| Name | Value | File:Line |
|---|---|---|
| `defaultMaxInstanceCreationsPerCycle` | 19 | instance.go:22 |
| `maxInstanceControllerChangesPerCycle` | 58 | instance.go:28 |

---

## 2. Scheduling (scheduler/scheduler.go)

See SCHEDULING_GRAPH.md for full call tree. Summary of placement-relevant functions:

```
Scheduler.Reconcile
├── extractPlacementConstraints        → per-service constraint map
├── buildNodeServicesMap               → nodeID → {running services}
├── attemptPlacements
│   └── placeBatchedInstances
│       ├── buildCandidateNodes        → heap sorted by loadScore
│       ├── rankByPreferences          → prefer/require/accept
│       ├── rankByAffinityHints        → colocate/separate tiebreaker
│       └── heap.Pop → best candidate
└── writes:
    ├── placement/instance/{id}         = nodeID
    ├── placement/node/{nodeID}/{id}    = ""
    └── derived/scheduler/unplaced/{id}/reason (if unplaceable)
```

---

## 3. Agent Instance Startup (agent/agent.go, agent/init.go)

### reconcileDesiredInstance → full startup sequence
```
reconcileDesiredInstance(instanceInfo)                        :726
├── lookupServiceImageFromStore(ctx, service)                :1085
│   └── store.Get(desired/service/{svc}/image)
│       → BAIL if empty (silent return, no state change)
│
├── resolveImagePullCredentials(ctx, service)                :1116
│   ├── lookupImagePullSecretFromStore(ctx, service)         :1105
│   │   └── store.Get(desired/service/{svc}/image_pull_secret)
│   └── secretProvider.ResolveSecretValue(ctx, secretName)
│
├── ensureImageAvailable(ctx, instanceInfo, image, creds)    :792
│   ├── lookupServicePullPolicyFromStore(ctx, service)       :1095
│   ├── runtime.IsImageCached(image)                         [return true → skip]
│   ├── publishStatusReason(ctx, id, "pulling")
│   ├── PUT observed/instance/{id}/state = "pulling"
│   ├── runtime.PullImage(image, creds) [5min timeout]
│   │   ├── SUCCESS → clearPullStatusFromStore               :883
│   │   └── FAILURE → recordImagePullFailure                 :835
│   │       ├── PUT observed/instance/{id}/state = "image-pull-backoff"
│   │       └── PUT observed/instance/{id}/pull-status = {JSON backoff}
│   └── → BAIL if false
│
├── IF secretAPIServer:
│   └── secretAPIServer.RegisterInstanceToken
│
├── IF hasInitSteps(ctx, service):                           [init.go:402]
│   ├── executeInitializationSteps(ctx, instanceInfo, image) [init.go:57]
│   │   ├── readCurrentInitPhase(ctx, instanceID)            [init.go:289]
│   │   │   └── store.Get(derived/instance/{id}/init/phase)
│   │   │       → "complete" → skip all steps
│   │   │
│   │   ├── loadInitStepDefinitions(ctx, service)            [init.go:232]
│   │   │   └── store.Scan(desired/service/{svc}/init/)
│   │   │       → ordered list of {exec|connection} steps
│   │   │
│   │   └── FOR EACH step (sequential, 0-indexed):
│   │       └── executeInitStep(ctx, ...)                    [init.go:96]
│   │           ├── readInitStepState(ctx, id, stepIndex)    [init.go:298]
│   │           │   ├── "succeeded" → skip to next
│   │           │   └── "running" → mark "failed" (agent restart)
│   │           │
│   │           ├── check backoff:
│   │           │   ├── attemptCount >= maxAttempts → PERMANENT FAIL
│   │           │   └── time.Now < nextRetryAt → BAIL
│   │           │
│   │           ├── publishInitProgress(ctx, ...)            [init.go:172]
│   │           │   └── PUT observed/instance/{id}/init-progress = {JSON}
│   │           │       PUT observed/instance/{id}/status-reason
│   │           │
│   │           ├── PUT observed/instance/{id}/init/step/{n}/state = "running"
│   │           │
│   │           ├── IF connection:
│   │           │   └── runConnectionProbe(ctx, step, svc)   [init.go:310]
│   │           │       ├── assembleConnectionURL(ctx, name)  [agent.go:1488]
│   │           │       ├── resolveConnectionAddress(ctx, host) [init.go:358]
│   │           │       │   ├── IF IP literal → use directly
│   │           │       │   └── ELSE:
│   │           │       │       ├── buildCCattlerResolver()   [init.go:388]
│   │           │       │       │   ├── IF advertiseAddress set:
│   │           │       │       │   │   └── custom resolver → UDP to addr:53
│   │           │       │       │   └── ELSE: net.DefaultResolver
│   │           │       │       └── resolver.LookupHost(hostname)
│   │           │       └── net.Dialer.DialContext("tcp", resolved:port)
│   │           │
│   │           ├── IF exec:
│   │           │   └── runInitCommand(ctx, image, svc, step) [init.go:209]
│   │           │       ├── materializeInitConfigFiles(ctx, svc, spec) [init.go:419]
│   │           │       └── runtime.ExecInit(image, ExecSpec)
│   │           │
│   │           ├── SUCCESS:
│   │           │   ├── PUT init/step/{n}/state = "succeeded"
│   │           │   ├── delete backoff entry
│   │           │   └── continue to next step
│   │           │
│   │           └── FAILURE:
│   │               ├── increment backoff counter
│   │               ├── publishInitProgress (with error)
│   │               └── → BAIL (retry next cycle)
│   │
│   └── on all steps passed → clearInitProgress(ctx, id)     [init.go:200]
│       └── DELETE observed/instance/{id}/init-progress
│
├── IF storageProvider:
│   ├── publishStatusReason(ctx, id, "waiting for volumes")
│   └── ensureVolumesAttachedForInstance(ctx, instanceInfo)
│       ├── READY → continue
│       └── NOT READY → revoke token, BAIL
│
├── publishStatusReason(ctx, id, "starting")                 :876
├── buildRuntimeSpecForInstance(ctx, instanceInfo, image)     :920
│   ├── lookupServiceCommandFromStore(ctx, service)          :1152
│   ├── lookupServiceExposedPortsFromStore(ctx, service)     :1204
│   ├── resolveServiceConfigEnvVars(ctx, service)            :1222
│   └── resolveServiceConfigFiles(ctx, service)              :1243
│
├── materializeSecretFiles(ctx, service, spec)               :1427
├── materializeConnectionFiles(ctx, service, spec)           :1463
│
└── startInstanceAndPublishState(ctx, instanceInfo, ...)     :956
    ├── runtime.Start(spec)
    │   ├── START FAILED:
    │   │   ├── PUT observed/instance/{id}/state = "failed"
    │   │   ├── publishStatusReason("start failed: {err}")
    │   │   └── revoke token, return
    │   └── START OK: continue
    │
    ├── retryBridgeAttachmentIfNeeded(ctx)                   :426
    ├── executeLifecycleHook(ctx, id, svc, "post_start")     :1288
    ├── publishInstanceHostPorts(ctx, id, ...)
    ├── runtime.Status(id) → get IP, running state
    ├── observeInstanceState(status) → state decision
    ├── publishInstanceStateToStore(ctx, id, state, ip, node)
    │   ├── PUT observed/instance/{id}/state    = "running"|"starting"|"failed"
    │   ├── PUT observed/instance/{id}/ip       = containerIP
    │   ├── PUT observed/instance/{id}/node     = nodeID
    │   └── PUT observed/instance/{id}/image    = image
    ├── publishStatusReason("running")
    └── PUT observed/instance/{id}/config-hash  = hash
```

### observeInstanceState Decision Table (agent.go:1674-1690)
| runtime.Status() | Status.Running | Status.ExitCode | Result |
|---|---|---|---|
| error | — | — | `InstanceStarting` |
| nil | true | — | `InstanceRunning` |
| nil | false | != 0 | `InstanceFailed` |
| nil | false | 0 (non-empty error) | `InstanceFailed` |
| nil | false | 0 (empty error) | `InstanceStarting` |

---

## 4. Steady State (agent/agent.go, agent/probes.go)

### executeReconciliationCycle → running instance path
```
executeReconciliationCycle                                   :525
└── FOR EACH running instance:
    ├── detectConfigChange(ctx, instanceInfo)                :1264
    │   ├── computeServiceConfigHash(ctx, service)           :1349
    │   └── compare with observed/instance/{id}/config-hash
    │       → if changed: stopInstanceWithGracePeriod → restart
    │
    ├── reconcileResourceChanges(ctx, instanceInfo)
    │   └── live resize CPU/memory if supported
    │
    ├── republishHostPortIfNeeded(ctx, id)                   :1009
    │
    ├── observeInstanceState(runtime.Status)
    │   ├── Running: true  → InstanceRunning
    │   ├── Status error   → InstanceStarting
    │   └── ExitCode != 0  → InstanceFailed
    │
    ├── publishInstanceStateToStore(ctx, id, state, ip, node)
    └── publishExitReasonIfFailed(ctx, id, state, reason)    :905
```

### ProbeScheduler.Run → independent goroutine (5s tick)
```
ProbeScheduler.Run(ctx, findInstances)                       [probes.go:99]
├── ExecuteProbePass(ctx, findInstances)                     [probes.go:115]
│   └── FOR EACH instance:
│       ├── performHealthCheckAndReportResult(ctx, info)     [probes.go:517]
│       │   └── PUT observed/instance/{id}/health = "healthy"|"unhealthy"
│       │
│       └── executeProbesForInstance(ctx, info)              [probes.go:131]
│           ├── loadProbeConfig(ctx, service, type)          [probes.go:406]
│           ├── getOrCreateProbeState(instanceID)            [probes.go:483]
│           │
│           ├── executeStartupProbe(ctx, ...)                [probes.go:202]
│           │   ├── runProbeCheck(ctx, config, id, ip)       [probes.go:366]
│           │   ├── pass ≥ successThreshold → "succeeded"
│           │   └── fail ≥ failureThreshold → "failed"
│           │       → FailureController replaces
│           │
│           ├── IF startup succeeded:
│           │   ├── executeLivenessProbe(ctx, ...)           [probes.go:268]
│           │   │   ├── runProbeCheck(ctx, config, id, ip)
│           │   │   ├── pass → "healthy"
│           │   │   └── fail ≥ failureThreshold → "unhealthy"
│           │   │       → FailureController drain+replace
│           │   │
│           │   └── executeReadinessProbe(ctx, ...)          [probes.go:316]
│           │       ├── runProbeCheck(ctx, config, id, ip)
│           │       ├── pass → "ready" (included in endpoints)
│           │       └── fail ≥ failureThreshold → "not-ready"
│           │           → removed from endpoints (NOT restarted)
│           │
│           ├── updateSummaryHealthFromProbes(ctx, id, ...)  [probes.go:178]
│           │   ├── startup NOT complete → no update
│           │   ├── liveness unhealthy   → health = unhealthy
│           │   ├── readiness not-ready  → health = unhealthy
│           │   └── else                 → health = healthy
│           │
│           └── publishProbeProgress(ctx, id, ...)           [probes.go:605]
│               ├── IF any consecutiveFailures > 0:
│               │   └── PUT observed/instance/{id}/probe-progress = {JSON}
│               └── ELSE:
│                   └── DELETE observed/instance/{id}/probe-progress
```

---

## 5. Failure and Replacement (controllers/failure.go)

### FailureController.Reconcile → entry point
```
FailureController.Reconcile(ctx, facts)                      :161
├── parseInstanceFieldsFromFacts(facts)                      [helpers.go:160]
├── parseCrashLoopCounters(facts)                            :425
│   └── reads derived/crash-loop/{svc} → {count, since}
│
├── FOR EACH instance (sorted):
│   └── processInstanceForRecovery(id, fields, now, ...)     :232
│       │
│       ├── IF effectiveState == Failed:
│       │   ├── check crash-loop backoff:
│       │   │   ├── count < threshold (10) → exponential backoff
│       │   │   │   └── calculateCrashLoopBackoff(count)     :414
│       │   │   │       → 10s * 2^(count-1), cap 5min
│       │   │   ├── count >= threshold → circuit breaker
│       │   │   │   ├── now < since + cooldown → BLOCKED
│       │   │   │   └── now >= since + cooldown → HALF-OPEN
│       │   │   └── no backoff → proceed
│       │   └── stopAndReplace(instanceID, serviceName)      :403
│       │
│       ├── IF state == Running AND startup == "failed":
│       │   └── stopAndReplace(instanceID, serviceName)
│       │
│       ├── IF draining AND liveness recovered:
│       │   └── clearDrainState(instanceID)                  :394
│       │       └── DELETE drain_readiness, drain_since
│       │
│       └── IF liveness == "unhealthy":
│           └── handleUnhealthyLiveness(id, svc, ...)        :318
│               ├── IF no drain_since:
│               │   └── beginDrain(instanceID, now)          :377
│               │       ├── PUT drain_readiness = "not-ready"
│               │       └── PUT drain_since = now.UnixMilli
│               └── IF drain_since + grace period elapsed:
│                   └── stopAndReplace(instanceID, svc)
│
├── FOR EACH service with replacements this cycle:
│   └── PUT derived/crash-loop/{svc} = {count+N, since=now}
│
├── FOR EACH service with NO failed instances:
│   └── DELETE derived/crash-loop/{svc}
│
└── capOutputChanges(changes)                                :357
    └── takeWholeGroups(changes, 58)                         [runner.go:726]
```

### stopAndReplace (failure.go:403)
```
stopAndReplace(instanceID, serviceName)
└── grouped changes "replace/{id}":
    ├── PUT derived/instance/{id}/controller_stopped = "true"
    ├── PUT observed/instance/{replacementID}         = ""
    ├── PUT observed/instance/{replacementID}/service  = serviceName
    └── PUT observed/instance/{replacementID}/state    = "pending"
```

### Constants
| Name | Value | File:Line |
|---|---|---|
| `DefaultDrainGracePeriod` | 5s | failure.go:22 |
| `crashLoopBaseDelay` | 10s | failure.go:25 |
| `crashLoopMaxDelay` | 5min | failure.go:28 |
| `defaultCircuitBreakerThreshold` | 10 | failure.go:33 |
| `defaultCircuitBreakerCooldown` | 10min | failure.go:38 |
| `defaultMaxReplacementsPerCycle` | 14 | failure.go:51 |
| `defaultMaxDrainsPerCycle` | 10 | failure.go:58 |
| `defaultMaxRecoveriesPerCycle` | 10 | failure.go:65 |
| `maxFailureControllerChangesPerCycle` | 58 | failure.go:71 |

---

## 6. Node Failure (controllers/nodefailure.go)

### NodeFailureController.Reconcile → entry point
```
NodeFailureController.Reconcile(ctx, facts)                  :92
├── parseNodeLeaseTimestamps(facts)                          :132
│   └── reads lease/node/{nodeID} → lastHeartbeatMillis
│
├── parseCurrentNodeStates(facts)                            :149
│   └── collectStringValuesBySuffix(facts, observed/node/, "state")
│
├── identifyUnreachableNodes(now, heartbeats, states)        :156
│   ├── FOR EACH node:
│   │   ├── unreachable + fresh heartbeat → RECOVERED
│   │   │   └── PUT observed/node/{id}/state = "alive"
│   │   ├── alive + stale heartbeat → UNREACHABLE
│   │   │   └── PUT observed/node/{id}/state = "unreachable"
│   │   └── already unreachable → add to set
│   └── cap node state changes to MaxNodeStateChangesPerCycle (12)
│
├── markInstancesOnUnreachableNodesAsFailed(facts, set, budget) :219
│   ├── reads placement/instance/{id} → nodeID
│   ├── FOR EACH running/pending/starting on unreachable node:
│   │   └── PUT derived/instance/{id}/node_failure = "true"
│   └── capped by remaining budget
│
└── cleanupStaleNodeFailureMarkers(facts, set, budget)       :270
    └── FOR EACH node_failure marker where node is alive:
        └── DELETE derived/instance/{id}/node_failure
```

### Constants
| Name | Value | File:Line |
|---|---|---|
| `defaultNodeFailureLeaseTimeout` | 30s | nodefailure.go:22 |
| `defaultMaxTotalChangesPerCycle` | 58 | nodefailure.go:29 |
| `defaultMaxNodeStateChangesPerCycle` | 12 | nodefailure.go:35 |

---

## 7. Init Phase Derivation (controllers/init.go)

### InitController.Reconcile → entry point
```
InitController.Reconcile(ctx, facts)                         :46
├── countDesiredInitSteps(facts)                             :80
│   └── scans desired/service/{svc}/init/{n}
│       → map[service]stepCount
│
├── mapInstancesToServices(facts)                            :103
│   └── reads observed/instance/{id}/service
│       → map[instanceID]serviceName
│
├── collectObservedInitStepStates(facts)                     :120
│   └── reads observed/instance/{id}/init/step/{n}/state
│       → map["instanceID/stepIndex"]state
│
├── collectCurrentInitPhases(facts)                          :142
│   └── reads derived/instance/{id}/init/phase
│
└── FOR EACH instance with init steps:
    ├── deriveInitPhase(id, stepCount, stepStates)           :159
    │   ├── all "succeeded"  → InitPhaseComplete
    │   ├── any "failed"     → InitPhaseFailed
    │   ├── any "running"    → InitPhaseRunning
    │   └── no observations  → InitPhasePending
    │
    └── IF derivedPhase != currentPhase:
        └── PUT derived/instance/{id}/init_phase = phase
```

---

## 8. Garbage Collection (controllers/instance_gc.go)

### InstanceGCController.Reconcile → entry point
```
InstanceGCController.Reconcile(ctx, facts)                   :74
├── parseInstanceFieldsFromFacts(facts)
├── buildInstanceMapsFromFields(fieldsByInstanceID)
├── parseEffectiveServiceFacts(facts)
├── countRunningInstancesByService(serviceByID, stateByID)   :146
├── parsePlacementFacts(facts)                               :161
│
├── FOR EACH stopped/failed instance:
│   ├── skip if running < desired for service
│   ├── skip if created-at < retention (10min)
│   └── add to candidates
│
├── sort candidates by instanceID
│
└── FOR EACH candidate (max 4 per cycle):
    └── buildInstanceDeletionChanges(id, facts, placements)  :175
        ├── DELETE observed/instance/{id}           (marker)
        ├── DELETE observed/instance/{id}/*         (all sub-keys)
        ├── DELETE derived/instance/{id}/*          (all markers)
        ├── DELETE placement/instance/{id}
        └── DELETE placement/node/{nodeID}/{id}
```

### Constants
| Name | Value | File:Line |
|---|---|---|
| `defaultGCRetentionPeriod` | 10min | instance_gc.go:21 |
| `defaultMaxGCDeletionsPerCycle` | 4 | instance_gc.go:26 |

---

## 9. Instance State Values (types/state.go)

| State | Value | Written By |
|---|---|---|
| `InstancePending` | `"pending"` | InstanceController |
| `InstancePulling` | `"pulling"` | Agent (ensureImageAvailable) |
| `InstanceImagePullBackoff` | `"image-pull-backoff"` | Agent (recordImagePullFailure) |
| `InstanceStarting` | `"starting"` | Agent (observeInstanceState) |
| `InstanceRunning` | `"running"` | Agent (observeInstanceState) |
| `InstanceFailed` | `"failed"` | Agent (observeInstanceState / start error) |
| `InstanceStopped` | `"stopped"` | Derived (controller_stopped marker) |

### Effective State Resolution (helpers.go:198)
```
IF controller_stopped == "true" → InstanceStopped
IF node_failure == "true"       → InstanceFailed
ELSE                            → observed/instance/{id}/state value
```

---

## 10. Init Step Detail

### Init Step Types
| Type | Mechanism | Default Retries | Source |
|---|---|---|---|
| **exec** | `runtime.ExecInit(image, ExecSpec)` | 0 (no retries) | init.go:209 |
| **connection** | DNS resolve → TCP dial to host:port | 30 retries | init.go:310 |

### Init Step Backoff (init.go:22-32)
| Parameter | Value |
|---|---|
| Base delay | 1s (`initStepBackoffBaseDelay`) |
| Max delay | 30s (`initStepMaxBackoffDelay`) |
| Formula | `1s * 2^(attemptCount-1)`, capped at 30s |

### Init Phase Values (types/state.go)
| Phase | Meaning | Key |
|---|---|---|
| `"pending"` | Not started yet | `derived/instance/{id}/init_phase` |
| `"running"` | At least one step in progress | `derived/instance/{id}/init_phase` |
| `"complete"` | All steps succeeded | `derived/instance/{id}/init_phase` |
| `"failed"` | A step permanently failed | `derived/instance/{id}/init_phase` |

---

## 11. Probe Configuration Defaults (agent/probes.go:20-36)

| Parameter | Default | Store Key |
|---|---|---|
| interval | 10s | `desired/service/{svc}/probe/{type}/interval` |
| timeout | 2s | `desired/service/{svc}/probe/{type}/timeout` |
| failureThreshold | 3 | `desired/service/{svc}/probe/{type}/failure_threshold` |
| successThreshold | 1 | `desired/service/{svc}/probe/{type}/success_threshold` |
| initialDelay | 0s | `desired/service/{svc}/probe/{type}/initial_delay` |

### Probe Types
| Type | Gate | Failure Effect | Success Effect |
|---|---|---|---|
| **Startup** | Blocks liveness + readiness | FailureController replaces | Unlocks other probes |
| **Liveness** | Requires startup succeeded | Drain → replace | Healthy |
| **Readiness** | Requires startup succeeded | Removed from endpoints | Included in endpoints |

---

## 12. Image Pull Failure Backoff (agent/agent.go:50-57)

| Parameter | Value |
|---|---|
| Base delay | 10s (`imagePullBaseDelay`) |
| Max delay | 5m (`imagePullMaxDelay`) |
| Pull timeout | 5m per attempt (`imagePullTimeout`) |
| Formula | `10s * 2^(attempts-1)`, capped at 5m |

---

## 13. Token Revocation Points

| Location | Trigger | Source |
|---|---|---|
| Init failure | Before BAIL | agent.go:728 |
| Volume attach failure | Before BAIL | agent.go:741 |
| Start failure | In startInstanceAndPublishState | agent.go:932 |
| Cleanup | In cleanupUndesiredInstance | agent.go:996 |

---

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `parseEffectiveServiceFacts` | InstanceController.Reconcile, InstanceGCController.Reconcile |
| `parseInstanceFieldsFromFacts` | InstanceController.Reconcile, FailureController.Reconcile, EndpointController.Reconcile, InstanceGCController.Reconcile, EvictionController.Reconcile, NodeFailureController (via markInstances) |
| `buildInstanceMapsFromFields` | InstanceController.Reconcile, InstanceGCController.Reconcile |
| `effectiveInstanceState` | buildInstanceMapsFromFields, processInstanceForRecovery, markInstancesOnUnreachableNodesAsFailed |
| `endpointEligibleState` | buildDesiredEndpoints |
| `stopAndReplace` | processInstanceForRecovery (failed, startup-probe-failed, drain-elapsed) |
| `calculateCrashLoopBackoff` | processInstanceForRecovery |
| `buildInstanceDeletionChanges` | InstanceGCController.Reconcile |
| `reconcileDesiredInstance` | executeReconciliationCycle (agent) |
| `executeInitializationSteps` | reconcileDesiredInstance |
| `runConnectionProbe` | executeInitStep (connection type) |
| `runInitCommand` | executeInitStep (exec type) |
| `resolveConnectionAddress` | runConnectionProbe |
| `observeInstanceState` | executeReconciliationCycle (running path), startInstanceAndPublishState |
| `publishInstanceStateToStore` | executeReconciliationCycle (running path), startInstanceAndPublishState |

---

## Diagnostic: "Why Is My Instance Stuck?"

### Stuck in `pending`
1. InstanceController hasn't run — check `observed/instance/{id}/state` exists
2. Scheduler can't place — check `derived/scheduler/unplaced/{id}/reason`
3. Stuck in scheduler queue — scheduler watches observed/instance/

### Stuck in `pulling`
1. Image pull in progress — check status-reason
2. Registry unreachable — node can't reach registry
3. Auth failure — check image_pull_secret

### Stuck in `image-pull-backoff`
1. Check `observed/instance/{id}/pull-status` JSON
2. Bad image reference — typo or missing tag
3. Expired registry auth

### Stuck in `starting`
1. runtime.Status() returning error — container may be running
2. Container exited immediately — status sees no task
3. etcd timeout — publishInstanceStateToStore PUT failed silently

### Stuck in init step (status-reason shows `init:N/M`)
1. Connection probe failing — target not running or DNS not resolving
2. Exec step failing — init command returning non-zero
3. Backoff — 1s, 2s, 4s, 8s, 16s, 30s cap
4. Max retries exceeded — permanent failure

### Running but not `ready`
1. Startup probe pending — check probe/startup
2. Readiness probe failing — check probe-progress

### Running but `unhealthy`
1. Liveness probe failing — drain → replace after threshold
