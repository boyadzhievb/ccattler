# CCattler Call Graph Reference

Machine-readable function call index. Purpose: avoid re-reading source files
to trace flow. Load this file instead.

## Agent Side (agent/agent.go, agent/init.go)

### Agent.Run → entry point
```
Agent.Run
├── nodeReporter.RegisterNode
├── nodeReporter.PublishAliveState
├── nodeReporter.WriteHeartbeat
├── nodeReporter.PublishAdvertiseAddress
├── vxlanReconciler.EnsureSubnetPublished
├── initializeDataPlaneIfSupported
├── dnsForwarder.Start                          [goroutine]
├── executeReconciliationCycle                  [initial]
├── probeScheduler.Run                          [goroutine]
├── heartbeatLoop                               [goroutine, 5s]
├── dataPlaneLoop                               [goroutine, 1s]
└── runWatchLoop                                [retry loop]
    └── executeReconciliationCycle              [on 1s ticker OR watch event]
```

### executeReconciliationCycle → the core loop
```
executeReconciliationCycle
├── findInstancesPlacedOnThisNode
│   ├── store.Scan(placement/node/{nodeID}/)
│   ├── isInstanceMarkedForSkip
│   │   ├── store.Get(derived/instance/{id}/node_failure)
│   │   └── store.Get(derived/instance/{id}/controller_stopped)
│   ├── store.Get(observed/instance/{id}/state)     [skip if stopped/failed]
│   ├── store.Get(derived/drain/evict/{nodeID}/{id}) [skip if drain]
│   ├── store.Get(observed/instance/{id}/service)    [primary: get service name]
│   └── derive service from instanceID              [fallback: stateful only]
│
├── runtime.List() → runningByID map
│
├── sort(desired): no-init-step services first
│
├── FOR EACH desired instance:
│   ├── IF not running:
│   │   ├── publishExitReasonFromStatus         [if exitCode != 0]
│   │   └── reconcileDesiredInstance             [full startup, see below]
│   │
│   └── IF running:
│       ├── detectConfigChange                   [if changed → stop+restart]
│       ├── reconcileResourceChanges
│       ├── republishHostPortIfNeeded
│       ├── observeInstanceState
│       ├── publishInstanceStateToStore
│       └── publishExitReasonIfFailed
│
├── cleanup: stop running instances not in desired set
│   └── cleanupUndesiredInstance
│
├── cleanup: imagePullBackoffs for unplaced
│   └── clearPullStatusFromStore
│
└── cleanup: initStepBackoffs for unplaced
```

### reconcileDesiredInstance → full instance startup sequence
```
reconcileDesiredInstance(instanceInfo)
├── lookupServiceImageFromStore                  [BAIL if empty]
├── resolveImagePullCredentials
├── ensureImageAvailable                         [BAIL if false]
│   ├── lookupServicePullPolicyFromStore
│   ├── IsImageCached                            [return true if cached]
│   ├── PullImage                                [5min timeout]
│   └── recordImagePullFailure                   [backoff 10s–5min]
│
├── IF secretAPIServer:
│   └── RegisterInstanceToken
│
├── IF hasInitSteps:
│   ├── executeInitializationSteps               [BAIL if false, revoke token]
│   │   ├── loadInitStepDefinitions
│   │   │   └── store.Scan(desired/service/{name}/init/)
│   │   ├── readCurrentInitPhase                 [skip if already complete]
│   │   ├── FOR EACH step (sequential):
│   │   │   ├── readInitStepState                [skip if succeeded]
│   │   │   └── executeInitStep
│   │   │       ├── check backoff (attemptCount, nextRetryAt)
│   │   │       ├── publishInitProgress
│   │   │       ├── IF connection type:
│   │   │       │   └── runConnectionProbe
│   │   │       │       ├── assembleConnectionURL
│   │   │       │       ├── parseConnectionJSON
│   │   │       │       ├── buildCCattlerResolver
│   │   │       │       │   ├── IF advertiseAddress set:
│   │   │       │       │   │   └── custom resolver → advertiseAddress:53
│   │   │       │       │   └── ELSE:
│   │   │       │       │       └── net.DefaultResolver (SYSTEM DNS!)
│   │   │       │       └── net.Dialer.DialContext("tcp", host:port)
│   │   │       └── IF exec type:
│   │   │           └── runInitCommand
│   │   │               └── runtime.ExecInit(image, initSpec)
│   │   └── clearInitProgress                    [all passed]
│
├── IF storageProvider:
│   ├── publishStatusReason("waiting for volumes")
│   └── ensureVolumesAttachedForInstance         [BAIL if false, revoke token]
│
├── publishStatusReason("starting")
├── buildRuntimeSpecForInstance
│   ├── resolveServiceConfigEnvVars
│   ├── resolveServiceConfigFiles
│   ├── lookupServiceExposedPortsFromStore
│   ├── lookupServiceResourcesFromStore
│   ├── lookupServiceCommandFromStore
│   └── networkProvider.AllocateIP
├── materializeSecretFiles
├── materializeConnectionFiles
├── IF secretAPIToken:
│   └── injectSecretAPIIntoSpec
└── startInstanceAndPublishState
    ├── runtime.Start(spec)                      [BAIL on error → publish failed]
    ├── retryBridgeAttachmentIfNeeded
    ├── executeLifecycleHook(post_start)
    ├── HostPortsForInstance → publishInstanceHostPorts
    ├── runtime.Status → get IP
    ├── publishInstanceStateToStore(running)
    └── store.Put(observed/instance/{id}/image)
```

## Server Side (controllers/runner.go)

### Runner.Run → entry point
```
Runner.Run
├── EventProjector.Run                           [goroutine]
└── FOR EACH controller:
    └── runSingleController                      [goroutine, retry with exp backoff]
        └── runControllerLoop
            ├── setupControllerWatches
            │   └── FOR EACH prefix in controller.Watch():
            │       ├── store.Watch(prefix)
            │       └── forwardWatchEvents       [goroutine]
            ├── executeReconciliationCycle        [initial]
            └── runReconcileSelectLoop
                └── select:
                    ├── reconcileTrigger → debounce(50ms) → executeReconciliationCycle
                    ├── resyncTicker(30s) → executeReconciliationCycle
                    └── watchLost → return error → restart controller
```

### executeReconciliationCycle → single pass with optimistic concurrency
```
executeReconciliationCycle(controller, deltaEvents)
└── FOR attempt 0..4:
    └── attemptSingleReconciliation
        ├── store.Scan(controller prefixes) → facts[]
        ├── controller.Reconcile(ctx, facts) → changes[]
        │   (OR controller.ReconcileDelta on first attempt if delta)
        └── store.Transaction
            ├── Compare: input key revisions (guard)
            ├── Then: put/delete change keys
            └── Else: nil → retry on conflict
```

## Store Key Patterns (what the agent reads/writes)

### Reads (agent)
| Key Pattern | Where Read |
|---|---|
| `placement/node/{nodeID}/{instanceID}` | findInstancesPlacedOnThisNode |
| `derived/instance/{id}/node_failure` | isInstanceMarkedForSkip |
| `derived/instance/{id}/controller_stopped` | isInstanceMarkedForSkip |
| `observed/instance/{id}/state` | findInstancesPlacedOnThisNode |
| `observed/instance/{id}/service` | findInstancesPlacedOnThisNode |
| `derived/drain/{nodeID}/{instanceID}` | findInstancesPlacedOnThisNode |
| `desired/service/{name}/image` | lookupServiceImageFromStore |
| `desired/service/{name}/pull_policy` | lookupServicePullPolicyFromStore |
| `desired/service/{name}/pull_credentials` | resolveImagePullCredentials |
| `desired/service/{name}/init/{step}/*` | loadInitStepDefinitions |
| `desired/connection/{name}` | runConnectionProbe |
| `derived/instance/{id}/init_phase` | readCurrentInitPhase |
| `observed/instance/{id}/init/{step}/state` | readInitStepState |
| `desired/service/{name}/config/*` | resolveServiceConfigEnvVars |
| `desired/service/{name}/ports` | lookupServiceExposedPortsFromStore |
| `desired/service/{name}/resources/*` | lookupServiceResourcesFromStore |
| `desired/service/{name}/command` | lookupServiceCommandFromStore |

### Writes (agent)
| Key Pattern | Where Written |
|---|---|
| `observed/instance/{id}/state` | publishInstanceStateToStore |
| `observed/instance/{id}/service` | publishInstanceStateToStore |
| `observed/instance/{id}/ip` | publishInstanceStateToStore |
| `observed/instance/{id}/node` | publishInstanceStateToStore |
| `observed/instance/{id}/image` | startInstanceAndPublishState |
| `observed/instance/{id}/exit_reason` | publishExitReasonIfFailed |
| `observed/instance/{id}/status_reason` | publishStatusReason |
| `observed/instance/{id}/pull_status` | recordImagePullFailure |
| `observed/instance/{id}/init/{step}/state` | executeInitStep |
| `observed/instance/{id}/init/{step}/reason` | executeInitStep |
| `observed/instance/{id}/init_progress` | publishInitProgress |
| `observed/node/{nodeID}/heartbeat` | WriteHeartbeat |
| `observed/node/{nodeID}/state` | PublishAliveState |

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `findInstancesPlacedOnThisNode` | `executeReconciliationCycle` (agent) |
| `isInstanceMarkedForSkip` | `findInstancesPlacedOnThisNode` |
| `reconcileDesiredInstance` | `executeReconciliationCycle` (agent, if instance not running) |
| `ensureImageAvailable` | `reconcileDesiredInstance` |
| `executeInitializationSteps` | `reconcileDesiredInstance` (if hasInitSteps) |
| `executeInitStep` | `executeInitializationSteps` (for each step, sequential) |
| `runConnectionProbe` | `executeInitStep` (if connection type) |
| `resolveConnectionAddress` | `runConnectionProbe` |
| `buildCCattlerResolver` | `resolveConnectionAddress` |
| `runInitCommand` | `executeInitStep` (if exec type) |
| `buildRuntimeSpecForInstance` | `reconcileDesiredInstance` |
| `startInstanceAndPublishState` | `reconcileDesiredInstance` |
| `publishInstanceStateToStore` | `executeReconciliationCycle` (if running), `startInstanceAndPublishState` |
| `publishExitReasonIfFailed` | `executeReconciliationCycle` (if running) |
| `publishExitReasonFromStatus` | `executeReconciliationCycle` (if not running, exitCode != 0) |
| `detectConfigChange` | `executeReconciliationCycle` (if running) |
| `cleanupUndesiredInstance` | `executeReconciliationCycle` (stale instances) |
| `clearPullStatusFromStore` | `executeReconciliationCycle` (unplaced backoffs) |
| `materializeSecretFiles` | `reconcileDesiredInstance` |
| `materializeConnectionFiles` | `reconcileDesiredInstance` |
| `lookupServiceImageFromStore` | `reconcileDesiredInstance` |
| `resolveImagePullCredentials` | `reconcileDesiredInstance` |
| `hasInitSteps` | `reconcileDesiredInstance` |
| `publishStatusReason` | `reconcileDesiredInstance` (multiple points) |
| `ensureVolumesAttachedForInstance` | `reconcileDesiredInstance` (if storageProvider) |
| `runSingleController` | `Runner.Run` (goroutine per controller) |
| `runControllerLoop` | `runSingleController` |
| `setupControllerWatches` | `runControllerLoop` |
| `runReconcileSelectLoop` | `runControllerLoop` |
| `executeReconciliationCycle` (server) | `runControllerLoop` (initial), `runReconcileSelectLoop` (on trigger) |
| `attemptSingleReconciliation` | `executeReconciliationCycle` (server, up to 5 retries) |

## Store Key Data Flow (controller writes → agent reads)

Shows which controller writes a key that the agent later reads. This is the
implicit coupling between server-side controllers and node agents.

| Key Pattern | Written By (Controller) | Read By (Agent) |
|---|---|---|
| `placement/node/{nodeID}/{id}` | **Scheduler**.Reconcile | `findInstancesPlacedOnThisNode` |
| `placement/instance/{id}` | **Scheduler**.Reconcile | (not directly, but placement/node is the agent view) |
| `observed/instance/{id}/state` | **InstanceController**.Reconcile (→ pending) | `findInstancesPlacedOnThisNode` (skip if stopped/failed) |
| `observed/instance/{id}/service` | **InstanceController**.Reconcile | `findInstancesPlacedOnThisNode` (get service name) |
| `derived/instance/{id}/node_failure` | **NodeFailureController**.Reconcile | `isInstanceMarkedForSkip` |
| `derived/instance/{id}/controller_stopped` | **FailureController**.Reconcile | `isInstanceMarkedForSkip` |
| `derived/node/{nodeID}/drain/evict/{id}` | **DrainController**.Reconcile | `findInstancesPlacedOnThisNode` (skip if drain) |
| `derived/instance/{id}/init_phase` | **InitController**.Reconcile | `readCurrentInitPhase` |
| `desired/service/{name}/*` | User via `cca apply` | multiple agent functions |
| `desired/connection/{name}` | User via `cca apply` | `runConnectionProbe` |

### Data flow chain (fresh deploy)
```
User (cca apply)
  → desired/service/{name}/* written
    → InstanceController reads desired/, writes observed/instance/{id}/state=pending
      → Scheduler reads observed/instance/, writes placement/node/{nodeID}/{id}
        → Agent reads placement/node/, starts reconcile
          → Agent reads observed/instance/{id}/service to find service name
          → Agent reads desired/service/{name}/init/* for init steps
          → Agent reads desired/connection/{name} for connection probes
          → Agent writes observed/instance/{id}/state=running when done
```

## Error Propagation Paths

Shows how errors propagate through the agent call chain. Critical for debugging
why deployment isn't converging: find where the error is generated, trace up to
see what happens (BAIL = stops instance startup, BACKOFF = retries later,
LOG = swallowed and continues).

### reconcileDesiredInstance error paths
```
reconcileDesiredInstance
├── lookupServiceImageFromStore → returns ""
│   └── BAIL: silent return (no image yet, controller hasn't written it)
│
├── ensureImageAvailable → returns false
│   ├── pull policy check → LOG error, return false
│   ├── PullImage fails → recordImagePullFailure (BACKOFF 10s–5min), return false
│   └── BAIL: skips this instance, next reconcile retries
│
├── executeInitializationSteps → returns false
│   ├── executeInitStep fails:
│   │   ├── runConnectionProbe → error
│   │   │   ├── resolveConnectionAddress → DNS lookup failed
│   │   │   │   └── BACKOFF: increment attempt, schedule retry (10s–5min)
│   │   │   └── DialContext → TCP dial failed
│   │   │       └── BACKOFF: increment attempt, schedule retry (10s–5min)
│   │   ├── runInitCommand → error
│   │   │   └── BACKOFF: increment attempt, schedule retry (10s–5min)
│   │   └── max retries exceeded
│   │       └── PERMANENT FAIL: writes InitStepFailed, returns false
│   └── BAIL: revokes secret token, skips startup
│
├── ensureVolumesAttachedForInstance → returns false
│   └── BAIL: revokes secret token, skips startup
│
└── startInstanceAndPublishState
    └── runtime.Start → error
        └── BAIL: publishes state=failed, returns
```

### executeReconciliationCycle error paths (running instances)
```
executeReconciliationCycle (for running instance)
├── detectConfigChange → changed
│   └── ACTION: stop instance, restart via reconcileDesiredInstance
│
├── reconcileResourceChanges → error
│   └── LOG: continues (non-fatal)
│
├── observeInstanceState → exited
│   ├── exitCode == 0 → publishInstanceStateToStore(stopped)
│   └── exitCode != 0 → publishExitReasonIfFailed, publishInstanceStateToStore(failed)
│       └── next reconcile: publishExitReasonFromStatus, then reconcileDesiredInstance (restart)
│
└── cleanupUndesiredInstance → error
    └── LOG: continues (best-effort cleanup)
```

## Implicit Ordering Constraints

These are ordering dependencies that are NOT enforced by explicit code, but
are required for convergence. The agent sort (`no-init-step services first`)
is the only enforcement mechanism.

### 1. Infrastructure-before-dependents (sort order)
```
Agent sort: services WITHOUT init steps start FIRST
  → metadata-db (no init steps) starts → gets IP → DNS registers it
  → redis (no init steps) starts → gets IP → DNS registers it
  → THEN worker (has init steps: connection:airflow-db) tries probe
    → needs metadata-db to be running and DNS-resolvable
```
**Why it matters:** If the sort were reversed, workers would try connection
probes before metadata-db exists, hitting backoff loops unnecessarily.

**Where enforced:** `executeReconciliationCycle` sorts `desired` slice at
agent/agent.go — `sort.SliceStable` with `hasInitSteps(a) && !hasInitSteps(b)`.

### 2. DNS registration before connection probes
```
Container starts → runtime.Start returns IP
  → publishInstanceStateToStore(running) writes observed/instance/{id}/ip
  → StatefulDNSController reads observed/instance/{id}/ip
  → StatefulDNSController writes derived/dns/{name} record
  → DNS forwarder serves the record
  → Connection probe on ANOTHER node can now resolve the hostname
```
**Latency:** This chain crosses two reconcile cycles (agent → controller → DNS).
Connection probes may fail during this window even if the target service is
running. The backoff retry handles this naturally.

### 3. InstanceController before Scheduler before Agent
```
InstanceController must write observed/instance/{id}/state=pending
  BEFORE Scheduler can see and place the instance
    BEFORE Agent can see it in placement/node/{nodeID}/
```
**Where enforced:** Controller watch prefixes. Scheduler watches
`observed/instance/`, Agent watches `placement/node/{nodeID}/`. Each reacts
only to keys written by the previous stage.

### 4. Image pull before init steps before start
```
ensureImageAvailable MUST succeed
  BEFORE executeInitializationSteps can run
    BEFORE startInstanceAndPublishState can run
```
**Where enforced:** Sequential returns in `reconcileDesiredInstance` —
each step returns false/nil to BAIL before the next step runs.

## Known Bug: DNS Resolver Fallback (FIXED in M150)

`resolveConnectionAddress()` at agent/init.go now resolves hostnames
explicitly via `LookupHost` using `buildCCattlerResolver()` before dialing,
bypassing Go's `net.Dialer.Resolver` which CGo can silently override with
system DNS (127.0.0.1:53).

Previous behavior: `net.Dialer{Resolver: buildCCattlerResolver()}` passed
the resolver to Go's dialer, but CGo-linked binaries ignore `PreferGo: true`
and use the system resolver instead.
