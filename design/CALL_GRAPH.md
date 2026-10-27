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

## Known Bug: DNS Resolver Fallback

`buildCCattlerResolver()` at agent/agent.go:355-366:
- IF `advertiseAddress` is set AND `dnsForwarder` is not nil:
  → resolver queries `advertiseAddress:53` (CCattler DNS)
- ELSE:
  → falls back to `net.DefaultResolver` (system DNS, typically 127.0.0.1:53)
  → **This breaks init connection probes** because service names like
    `metadata-db` are only resolvable via CCattler DNS, not system DNS.

Root cause of deployment not converging: agents on worker nodes may have
empty `advertiseAddress` or nil `dnsForwarder`, causing init probes to fail.
