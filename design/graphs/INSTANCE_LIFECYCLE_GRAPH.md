# Instance Lifecycle Graph Reference

Machine-readable function call index for the full lifecycle of a single
CCattler instance — from creation to garbage collection. Purpose: trace
why an instance is stuck in any state without reading source.

## 1. Instance Creation (controllers/instance.go)

### InstanceController.Reconcile → entry point
```
InstanceController.Reconcile(ctx, facts)                     :80
├── parseEffectiveServiceFacts(facts)                        :314
│   └── FOR EACH effective/service/{svc}/ fact:
│       ├── suffix "instances" → desiredCounts[svc]
│       ├── suffix "stateful"  → statefulServices[svc]
│       └── suffix "placement_mode" → perNodeServices[svc]
│
├── extractSurgeAllowances(facts)                            :344
│   └── reads derived/service/{svc}/rollout/surge_allowance
│       → desiredCounts[svc] += surgeCount
│
├── parseInstanceFieldsFromFacts(facts)                      [helpers.go:160]
│   └── scans observed/instance/ + derived/instance/
│       → map[instanceID]map[field]value
│
├── instanceCreationFacts{extractDesiredImages(facts), fields}  [M241 D1, D2]
│   └── reads desired/service/{svc}/image  [rollout_facts.go]
│
├── buildInstanceMapsFromFields(fieldsByInstanceID)           :363
│   ├── effectiveInstanceState(fields)                       [helpers.go:198]
│   │   ├── controller_stopped == "true" → InstanceStopped
│   │   ├── node_failure == "true"       → InstanceFailed
│   │   └── else                         → observed state
│   └── → stateByInstanceID, serviceByInstanceID
│
├── buildActiveInstanceIDsByService(...)                      :379
│   └── groups non-stopped IDs by service, sorted
│
├── reconcileScaleDowns(...)                                 :135
│   └── FOR EACH service where haveCount > wantCount:
│       ├── perNodeServices → markPerNodeExcessInstancesAsStopped  :562
│       │   └── priority: failedNode > pending > running
│       ├── statefulServices → stopHighestOrdinalInstances         :471
│       │   └── reverse ordinal order, writes controller_stopped
│       └── default → markExcessStatelessInstancesAsStopped        :535
│           └── selectInstancesForRemoval(pending first)           :604
│               → writes derived/instance/{id}/controller_stopped
│
├── computeServiceDeficits(...)                              :178
│   └── FOR EACH service where haveCount < wantCount:
│       ├── stateful → createNextStatefulInstance             :425
│       │   └── ordered startup: N+1 only after N is running
│       │       clears stale controller_stopped / node_failure / restarts
│       └── stateless → deficit = wantCount - haveCount
│
└── allocateCreationsFairly(deficits, ...)                   :213
    ├── baseShare = budget / serviceCount, cap at deficit
    ├── remainder slots → largest unmet demand first
    ├── stateful → createNextStatefulInstance                 :425
    │   ├── writes observed/instance/{svc}-{ord}/*
    │   └── buildImageAndRestartChanges                      :514  (same group)
    └── stateless → createPendingInstances                   :488
        └── FOR EACH count:
            └── writes grouped (instance-create/{id}):
                ├── observed/instance/{id}        = ""
                ├── observed/instance/{id}/service = serviceName
                ├── observed/instance/{id}/state   = "pending"
                └── buildImageAndRestartChanges               :514  [M241 D1, D2]
                    ├── derived/instance/{id}/image = desired/service/{svc}/image (when set)
                    └── DELETE observed/instance/{id}/restarts (when a stale count exists)
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
| `desired/service/{svc}/image` | Image recorded for a new instance (M241) |
| `observed/instance/{id}/restarts` | Stale restart count of a re-created ID (M241) |

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
| `derived/instance/{id}/image` | desired image | Every creation, when the service has an image (M241 D1) |
| DELETE `observed/instance/{id}/restarts` | — | Creation of an ID with a stale count (M241 D2) |

### Constants
| Name | Value | File:Line |
|---|---|---|
| `defaultMaxInstanceCreationsPerCycle` | 14 (4 ops per creation since M241) | instance.go:23 |
| `maxInstanceControllerChangesPerCycle` | 58 | instance.go:29 |

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
reconcileDesiredInstance(instanceInfo)                        :736
├── lookupInstanceImageFromStore(ctx, instanceInfo)          :1178  [M241 D1]
│   ├── store.Get(derived/instance/{id}/image)  → the image recorded at creation;
│   │   used for every start and in-place restart (Flow 33, Flow 34)
│   └── absent → lookupServiceImageFromStore(ctx, service)    :1205
│       ├── store.Get(desired/service/{svc}/image)  (instance created before M241)
│       └── logged once per instance (imageFallbackLogged; cleanupStaleImageFallbackLogs :1194)
│       → BAIL if empty (silent return, no state change)
│
├── computeServiceConfigHash(ctx, service)                   :744 (def :1490)  [configHashBeforeBuild, M235]
│   ├── Scan desired/service/{svc}/config/ + desired/service/{svc}/secret/
│   ├── writeSecretReferenceEntries                          :1455  [M236 D2]
│   │   ├── loadServiceSecretRequirements (connection refs + mount paths, desired/connection/{name}, pull secret)
│   │   └── Get desired/secret_generation/{secret} per used secret → line only if != InitialSecretGeneration
│   ├── taken BEFORE any input is read; stored after the start (Flow 16)
│   └── read error → BAIL this cycle ("cannot read config, retrying next cycle")
│
├── dropSecretMaterialIfConfigChanged(ctx, id, hash)         :1334  [M236 review]
│   └── stored observed/instance/{id}/config-hash != hash → secretMaterialStore.RemoveInstance (re-fetch)
│
├── prepareInstanceSecretMaterial(ctx, instanceInfo)          [agent/secret_delivery.go, M174]
│   ├── no grants / connection secrets / pull secret → proceed
│   ├── material in /run/ccattler/secrets/{id}/ with all grants → reuse (container restart)
│   ├── waiting and before retryAfter (and requirements unchanged) → BAIL
│   ├── POST /api/node/secrets {instance_id}  (node mTLS)
│   │   ├── OK → WriteInstanceSecrets (dir 0700, files 0444) → proceed
│   │   └── error → PUT observed/instance/{id}/state = "waiting-for-secrets"
│   │              PUT observed/instance/{id}/secret-wait = {reason, secrets, next_retry}
│   │              reasons: api_unreachable (2s…30s) | not_authorized | secret_missing (5min fallback)
│   │                       | delivery_not_configured → BAIL
│   └── never pulls or starts without material; not a failure (no replacement)
│
├── resolveImagePullCredentials(ctx, service, secretValues)
│   ├── lookupImagePullSecretFromStore(ctx, service)
│   │   └── store.Get(desired/service/{svc}/image_pull_secret)
│   └── secretValues[name] → "user:pass"                     [delivered material]
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
│   │           │       └── net.Dialer{Timeout: 10s}.DialContext("tcp", resolved:port)
│   │           │
│   │           ├── IF exec:
│   │           │   └── runInitCommand(ctx, image, svc, step) [init.go:209]
│   │           │       ├── materializeInitConfigFiles(ctx, svc, spec) [init.go:419]
│   │           │       └── runtime.ExecInit(image, ExecSpec)
│   │           │           └── [containerd] buildInitContainerSpecOptions → containerBaseSpecOptions (no /run tmpfs, M232)
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
│           (only an EXCLUSIVE volume attached to another node waits forever;
│            shared/NFS volumes attach on every node with a per-instance
│            attachment fact — Flow 14. State stays "pulling" with reason
│            "waiting for volumes" while blocked)
│
├── publishStatusReason(ctx, id, "starting")                 :876
├── buildRuntimeSpecForInstance(ctx, instanceInfo, image)     :920
│   ├── lookupServiceCommandFromStore(ctx, service)          :1152
│   ├── lookupServiceExposedPortsFromStore(ctx, service)     :1204
│   ├── resolveServiceConfigEnvVars(ctx, service)            :1222
│   └── resolveServiceConfigFiles(ctx, service)              :1243
│
├── buildInstanceSecretMounts(ctx, instanceInfo, secretValues)  [Spec.SecretMounts: read-only binds from tmpfs]
│   └── addConnectionFileMounts → WriteConnectionFile         [connection URLs as files only]
├── addSecretHelperIfMounted                                 [cca-secret helper, not secret]
│
└── startInstanceAndPublishState(ctx, instanceInfo, image, configHashBeforeBuild, spec) → attempted bool
    ├── runtime.Start(spec)
    │   ├── [containerd] rotateInstanceRunLog: <id>-json.log → <id>-json.log.previous (M234)
    │   ├── [containerd] WithNewSpec(WithImageConfig, buildOCISpecOptions(spec, netns)...)
    │   │   └── containerBaseSpecOptions: WithoutRunMount (no tmpfs on /run, M232)
    │   ├── [containerd] NewTask / task.Start failed → cleanup (task, CNI, container),
    │   │   newContainerdTaskStartError("create task"|"start task", err)
    │   │   └── ProcessFailed, ProcessError = extractRuncProcessExecFailure(err)
    │   │       [runc "unable to start container process: " + optional "error during container init: "
    │   │        + `exec: "`; ProcessError = the `exec: "…": …` part (runc 1.1 / 1.4), M238]
    │   ├── START FAILED → handleInstanceStartError(ctx, instanceInfo, image, err)
    │   │   ├── revoke token, publishInstanceImage
    │   │   ├── StartError.ProcessFailed (workload's process could not start, M238):
    │   │   │   ├── publishInstanceConfigHash(configHashBeforeBuild)   [compared during the wait]
    │   │   │   └── recordInstanceStartFailure(ProcessError, else Reason) [section 12b] → return true (attempt counted)
    │   │   └── otherwise (image, CNI, netns, create container, cgroup/seccomp/shim):
    │   │       ├── PUT observed/instance/{id}/state = "failed"
    │   │       ├── publishStatusReason("start failed: {err}")
    │   │       └── return false
    │   └── START OK: continue
    │
    ├── retryBridgeAttachmentIfNeeded(ctx)                   :438
    ├── executeLifecycleHook(ctx, id, svc, "post_start")     :1441
    ├── publishInstanceHostPorts(ctx, id, ...)
    ├── runtime.Status(id) → get IP
    ├── publishInstanceStartedAt(id)                 :1050
    │   └── PUT observed/instance/{id}/started_at = now (RFC 3339, fractions)
    │       [every start, in-place restarts included, before the state; rollout min_healthy_time, M241 D10]
    ├── observeInstanceState(id) → running | starting   [exited → starting (instant exit, M238)]
    ├── publishInstanceStateToStore(ctx, id, state, ip, node)
    │   ├── PUT observed/instance/{id}/state    = "running"|"starting"
    │   ├── PUT observed/instance/{id}/ip       = containerIP
    │   └── PUT observed/instance/{id}/node     = nodeID
    ├── publishStatusReason("running")              [running only]
    ├── publishInstanceImage → PUT observed/instance/{id}/image = image
    ├── publishInstanceConfigHash → PUT observed/instance/{id}/config-hash = configHashBeforeBuild
    └── recordInstanceStarted(id)                   [section 12b] → return true
```

### observeInstanceState Decision Table
| runtime.Status() | Status.Running | Result |
|---|---|---|
| error | — | `InstanceStarting` |
| nil | true | `InstanceRunning` |
| nil | false (any exit code, any error) | `InstanceStarting` |

The agent never publishes `failed` from observing a container (M238). An exited container is handled by the next reconcile cycle, which finds it not running in `List` and records the exit through Flow 33 (section 12b: in place, back-off, `crash-loop-backoff`). This covers an instant exit right after `Start` (D1) and a container that exits between `List` and `Status` on the running path (D4).

---

## 4. Steady State (agent/agent.go, agent/probes.go)

### executeReconciliationCycle → running instance path
```
executeReconciliationCycle                                   :535
└── FOR EACH running instance:
    ├── detectConfigChange(ctx, instanceInfo)                :1347
    │   ├── no stored hash → no compare (instance started by an older agent)
    │   ├── computeServiceConfigHash(ctx, service)           :1430  [empty input → hash of "", never ""; M236 inputs above]
    │   │   └── read error → no compare, instance keeps running (a blip is not a change)
    │   └── compare with observed/instance/{id}/config-hash
    │       → if changed: stopInstanceWithGracePeriod → restart
    │
    ├── reconcileResourceChanges(ctx, instanceInfo)
    │   └── live resize CPU/memory if supported
    │
    ├── republishHostPortIfNeeded(ctx, id)                   :1009
    │
    ├── observeInstanceState(id)
    │   ├── Running: true  → InstanceRunning
    │   └── otherwise      → InstanceStarting   [exited after List: next cycle records it, Flow 33, M238 D4]
    │
    ├── publishInstanceStateToStore(ctx, id, state, ip, node)
    └── deleteExitReason(ctx, id)               [DELETE observed/instance/{id}/exit-reason]
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
│   ├── FOR EACH running/pending/starting/crash-loop-backoff instance on unreachable node (isActiveInstanceState):
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
├── FOR EACH instance in stateByInstanceID:
│   ├── IF serviceName == "" (orphaned):
│   │   └── add to candidates unconditionally
│   ├── skip if state != stopped AND state != failed
│   ├── IF service has no desired count (deleted):
│   │   └── treat as desiredCount=0 → eligible
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
| `InstanceWaitingForSecrets` | `"waiting-for-secrets"` | Agent (recordSecretDeliveryWait, M174) |
| `InstanceCrashLoopBackoff` | `"crash-loop-backoff"` | Agent (beginCrashLoopWait via recordInstanceExit / recordInstanceStartFailure, M233/M238) |
| `InstanceStarting` | `"starting"` | Agent (observeInstanceState) |
| `InstanceRunning` | `"running"` | Agent (observeInstanceState) |
| `InstanceFailed` | `"failed"` | Agent (handleInstanceStartError, start error without ProcessFailed); node failure controller |
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
| Formula | `backoff.Delay(10s, 5m, attempts)` = `10s * 2^(attempts-1)`, capped at 5m |

---

## 12b. In-Place Restart Back-off (agent/crash_loop_backoff.go, M233)

Flow 33 in design/runtime-invariant-flows.md. Called from `executeReconciliationCycle` when a desired instance has no running container.

```
reconcileInstanceWithoutRunningContainer(instanceInfo, runtimeStatus, containerExited)
├── retryAfterStartFailure = entry exists && entry.startFailed   [last attempt's process could not start, M238]
├── containerExited || retryAfterStartFailure:
│   └── isExitedInstanceDueForRestart  [false → return]
│       ├── no crashLoopBackoffs entry → true (agent restarted: restart once)
│       ├── entry.waiting == false → recordInstanceExit → false
│       │   ├── publishExitReasonFromStatus   [exit code != 0]
│       │   └── beginCrashLoopWait("exited with code N")
│       ├── now < retryAfter → hasConfigChangedSinceStart ? (drop entry → true) : false
│       └── now >= retryAfter → true
├── otherwise (container missing): entry.waiting = false (the next exit is a new one)
├── reconcileDesiredInstance → attempted bool       [section 3; false on any bail-out or a start error published failed]
│   ├── started (startInstanceAndPublishState) → recordInstanceStarted   [entry.waiting = false, startFailed = false, lastStartedAt = now]
│   ├── process could not start (handleInstanceStartError, StartError.ProcessFailed, M238)
│   │   ├── publishInstanceConfigHash(configHashBeforeBuild)  [so hasConfigChangedSinceStart compares with this attempt]
│   │   └── recordInstanceStartFailure(processError)          [runc `exec: "…": …`]
│   │       ├── ensureCrashLoopBackoffEntry
│   │       ├── publishExitReason(processError)               [PUT observed/instance/{id}/exit-reason]
│   │       ├── beginCrashLoopWait(processError)              [reset decided from the previous lastStartedAt]
│   │       └── entry.startFailed = true ; lastStartedAt = now
│   └── false → return: no count, entry stays waiting with its past retryAfter, so the next
│       cycle retries without recording a new exit; the bail-out's own state and reason
│       (image-pull-backoff, waiting-for-secrets, init, volumes) stay visible
└── attempted && (containerExited || retryAfterStartFailure)
    → incrementRestartCountInStore   [observed/instance/{id}/restarts, read +1 write]

beginCrashLoopWait(instanceInfo, entry, exitDescription)
├── now - lastStartedAt >= crashLoopResetPeriod → attempts = 0
├── attempts++ ; retryAfter = now + backoff.Delay(base, max, attempts) ; waiting = true
├── publishInstanceStateToStore(crash-loop-backoff)
└── publishStatusReason("{exitDescription}; restarting in 40s")

cleanupStaleCrashLoopBackoffs(desiredByID)   [end of the cycle: drop entries of instances not placed here]
cleanupStaleImageFallbackLogs(desiredByID)   [same, for the once-per-instance image fallback log (M241)]
```

| Parameter | Value |
|---|---|
| Base delay | 10s (`crashLoopBaseDelay`, agent field `crashLoopBaseDelay`) |
| Max delay | 5m (`crashLoopMaxDelay`) |
| Reset after clean run | 10m (`crashLoopResetPeriod`) |
| Formula | `backoff.Delay(base, max, attempt)`, shared with image pull and `calculateCrashLoopBackoff` |

---

## 13. Token Revocation Points

| Location | Trigger | Source |
|---|---|---|
| Init failure | Before BAIL | agent.go:728 |
| Volume attach failure | Before BAIL | agent.go:741 |
| Start failure | In handleInstanceStartError (both outcomes) | agent.go |
| Cleanup | In cleanupUndesiredInstance | agent.go:996 |

Instance data removal (M234): `cleanupUndesiredInstance` and `cleanupStoppedStaleInstance` call `removeInstanceDataIfSupported` after the stop → `runtime.InstanceDataRemover.RemoveInstanceData` (containerd: deletes `/var/lib/ccattler/logs/<id>/`, current and previous run logs). `Stop` itself no longer deletes the log directory.

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
| `reconcileInstanceWithoutRunningContainer` | executeReconciliationCycle (no running container) |
| `isExitedInstanceDueForRestart` | reconcileInstanceWithoutRunningContainer |
| `recordInstanceExit` | isExitedInstanceDueForRestart |
| `recordInstanceStartFailure` | handleInstanceStartError (StartError.ProcessFailed, M238) |
| `beginCrashLoopWait` | recordInstanceExit, recordInstanceStartFailure |
| `recordInstanceStarted` | startInstanceAndPublishState |
| `ensureCrashLoopBackoffEntry` | recordInstanceStarted, recordInstanceStartFailure |
| `handleInstanceStartError` | startInstanceAndPublishState |
| `publishInstanceConfigHash` | startInstanceAndPublishState, handleInstanceStartError (ProcessFailed) |
| `publishInstanceStartedAt` | startInstanceAndPublishState (start OK, M241 D10) |
| `newContainerdTaskStartError` / `extractRuncProcessExecFailure` | ContainerdRuntime.Start (create task, start task) |
| `hasConfigChangedSinceStart` | isExitedInstanceDueForRestart, detectConfigChange |
| `incrementRestartCountInStore` | reconcileInstanceWithoutRunningContainer |
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

### Stuck in `waiting-for-secrets`
1. Check `observed/instance/{id}/secret-wait` JSON (reason, secrets, next_retry)
2. `api_unreachable` — agent cannot reach `--api-server` (retry 2s…30s)
3. `not_authorized` — missing grant for a mounted, connection or pull secret; wrong node; node disabled or not enrolled
4. `secret_missing` — granted secret has no value (`cca secret set`)
5. `delivery_not_configured` — agent has no node certificate or API address

### Stuck in `crash-loop-backoff`
1. Check `observed/instance/{id}/status-reason` (exit code and wait) and `exit-reason`
2. `observed/instance/{id}/restarts` counts the in-place restarts
3. The wait is in agent memory only (section 12b); a config/secret change ends it
4. A restart that bails out (pull failure, secrets, init, volumes) shows that state instead; it is not counted as a restart or a new exit

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
