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
│   │   │       │       └── net.Dialer{Timeout: 10s}.DialContext("tcp", host:port)
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
├── IF storageProvider:
│   └── resolveVolumeMountsForSpec               [reads mount paths from store → Spec.VolumeMounts]
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
| `observed/instance/{id}/created-at` | publishInstanceStateToStore (write-once) |
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
| `resolveVolumeMountsForSpec` | `reconcileDesiredInstance` (if storageProvider, after buildRuntimeSpec) |
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

## Store Write Rate Limiter (store/write_rate_limiter.go)

Defense-in-depth: global token-bucket rate limiter on `EtcdStore.Put()`,
`Delete()`, `PutWithLease()`, and `Transaction()`. Protects etcd from
write floods caused by controller bugs, runaway reconciliation, or
compromised agents.

```
EtcdStore.Put / Delete / PutWithLease / Transaction
├── checkClosed
├── writeLimiter.allowWrite()        ← token bucket check
│   ├── refill tokens (elapsed × ratePerSecond, capped at burstSize)
│   ├── if tokens < 1 → ErrWriteRateLimited, log periodically
│   └── consume 1 token → proceed
└── (actual etcd operation)
```

| Parameter | Default | Rationale |
|---|---|---|
| `defaultWriteRatePerSecond` | 500 | Sustained write budget, well above normal (~50-100 writes/s) |
| `defaultWriteBurstSize` | 1000 | Absorbs reconciliation bursts without throttling normal operation |
| `writeRateLimitLogInterval` | 10s | Avoids journal flooding when limiter is actively dropping |

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

## Data Plane Side (agent/dataplane.go, network/*_dataplane.go)

### DataPlaneReconciler.Reconcile → called every 1s from dataPlaneLoop
```
DataPlaneReconciler.Reconcile
├── buildServiceVIPConfigs
│   ├── store.Scan(derived/vip/)              [VIP allocations]
│   ├── store.Scan(desired/service/*/ports)   [exposed ports]
│   ├── store.Scan(desired/service/*/expose/)
│   ├── resolveServiceBackends               [for each service]
│   │   ├── identifyStaleNodes
│   │   │   └── store.Scan(observed/node/*/heartbeat)
│   │   ├── store.Scan(observed/instance/)    [find instances for service]
│   │   ├── resolveLocalBackend              [if instance on this node]
│   │   │   └── resolveHostPort
│   │   │       └── store.Get(observed/instance/{id}/host_port/{port})
│   │   └── resolveRemoteBackend             [if instance on another node]
│   │       └── store.Get(observed/node/{nodeID}/address)
│   └── → []ServiceVIPConfig
│
├── DataPlaneProvider.ReconcileVIPDataPlane(serviceConfigs)
│   [dispatches to one of: nftables, iptables, eBPF, simulator]
│
├── buildRouteDataPlaneConfigs
│   ├── store.Scan(desired/route/)
│   ├── store.Scan(derived/route/)
│   ├── parseRouteFactsForDataPlane
│   ├── buildRouteConfigFromEntry
│   │   └── parseBackendAddressList
│   └── → []RouteDataPlaneConfig
│
└── DataPlaneProvider.ReconcileRouteDataPlane(routeConfigs)
```

### Agent startup: initializeDataPlaneIfSupported
```
initializeDataPlaneIfSupported
├── DataPlaneInitializer.Initialize           [if provider supports it]
│   ├── eBPF: loadEbpfPrograms, extractTCPrograms, ensurePinDirectory,
│   │         extractAndPinMaps, attachCgroupPrograms, scanExistingServiceIDs
│   └── iptables/nftables: no-op (don't implement DataPlaneInitializer)
│
└── DataPlaneNodeRegistrar.RegisterNode       [if provider supports it]
    └── eBPF: program node map, attach TC to bridge
```

### NftablesDataPlane.ReconcileVIPDataPlane → the nftables path
```
NftablesDataPlane.ReconcileVIPDataPlane(serviceConfigs)
├── ensureDummyInterface                      [creates cca-vip0 dummy]
│   └── ip link add cca-vip0 type dummy
│   └── ip link set cca-vip0 up
│
├── FOR EACH service VIP:
│   ├── ensureVIPAddress(virtualIP)
│   │   └── ip addr add {vip}/32 dev cca-vip0
│   └── track active VIPs
│
├── removeStaleVIPAddresses(desiredAddresses)
│   └── ip addr del {stale_vip}/32 dev cca-vip0
│
├── buildVIPRuleset(serviceConfigs)
│   ├── writePostRoutingChain                 [masquerade for DNAT]
│   ├── writeContainerIsolationChain          [blocks cca0→etcd 2379/2380]
│   │   ├── filter hook input priority 0
│   │   ├── rate-limited log (1/min)          [journal: "cca:etcd-blocked:"]
│   │   └── counter drop                     [visible via nft list table ip cca]
│   ├── writeVerdictMapWithElements           [dispatch VIP:port → chain]
│   └── writeBackendDNATRules                 [DNAT to backends]
│
└── applyNftablesRuleset(ruleset)
    └── nft -f (atomic replace)
```

### IptablesDataPlane.ReconcileVIPDataPlane → the iptables path
```
IptablesDataPlane.ReconcileVIPDataPlane(serviceConfigs)
├── ensureDummyInterface                      [creates cca-vip0 dummy]
├── ensureGlobalChainAndJumpRules
│   ├── ensureJumpToChain(PREROUTING, CCA_SERVICES)
│   ├── ensureJumpToChain(OUTPUT, CCA_SERVICES)
│   ├── ensureMasqueradeForDNAT
│   └── ensureContainerIsolationRules          [blocks cca0→etcd 2379/2380]
│       ├── INPUT: LOG --limit 1/min           [journal: "cca:etcd-blocked:"]
│       └── INPUT: DROP                        [unconditional]
│
├── FOR EACH service VIP:
│   ├── ensureVIPAddress(virtualIP)
│   ├── sanitizeChainName → CCA_SVC_{name}
│   ├── reconcileServiceChain                 [DNAT rules per backend]
│   └── ensureJumpFromMainToService           [CCA_SERVICES → chain]
│
├── removeStaleServiceChains(desiredChains)
│   ├── removeJumpRuleFromMainChain
│   └── flushAndDeleteChain
│
└── removeStaleVIPAddresses(desiredAddresses)
    └── removeVIPAddressFromInterface
```

### EbpfDataPlane.ReconcileVIPDataPlane → the eBPF path
```
EbpfDataPlane.ReconcileVIPDataPlane(serviceConfigs)
├── buildDesiredVIPState(serviceConfigs)
│   ├── buildServiceEntries
│   └── assignOrLookupServiceID
│
├── applyDesiredServiceEntries                [update BPF service map]
├── applyDesiredBackendEntries                [update BPF backend map]
├── removeStaleServiceEntries                 [delete old entries]
└── removeStaleBackendEntries                 [delete old entries]
```

### Known Bug: VIP dummy interface name collision (FIXED in M150)

`vipDummyInterface` was `"cca0"` in network/iptables_dataplane.go, same
name as `cniBridgeName` in runtime/containerd.go. Data plane creates dummy
first (agent startup), then CNI bridge plugin fails with:
`"cca0" already exists but is not a bridge`.

Fix: renamed to `"cca-vip0"`. CNI bridge stays `"cca0"`.

## VXLAN Overlay Side (agent/vxlan.go)

### VXLANReconciler.Reconcile → called every 1s from dataPlaneLoop
```
VXLANReconciler.Reconcile
├── EnsureSubnetPublished (if not yet)
│   ├── allocateNextFreeSubnet
│   │   ├── store.Scan(observed/node/*/subnet)
│   │   ├── parseClusterCIDRBaseOctets
│   │   ├── collectOccupiedThirdOctets
│   │   └── findNextFreeThirdOctetForNode
│   │       └── hashNodeIDToOctetOffset        [consistent hashing]
│   └── store.Put(observed/node/{nodeID}/subnet)
│
├── ensureVXLANInterfaceExists
│   ├── interfaceAlreadyExists("cca-vxlan0")
│   ├── detectPhysicalInterfaceForAddress
│   ├── deriveVXLANMACAddress(nodeID)
│   ├── executeVXLANCreate                    [ip link add cca-vxlan0 type vxlan]
│   ├── executeIPLinkSetAddress               [set MAC]
│   ├── executeIPLinkSetMTU(1450)
│   └── executeIPLinkSetUp("cca-vxlan0")
│
├── discoverPeerNodes
│   ├── store.Scan(observed/node/*/address)
│   ├── store.Scan(observed/node/*/subnet)
│   ├── buildNodeAddressMap
│   └── buildNodeSubnetMap
│
├── programPeerRoutes                         [for each peer node]
│   ├── bridge fdb append                     [VTEP → peer address]
│   └── ip route replace                      [peer subnet → vxlan0]
│
└── removeStaleRoutes                         [for peers that disappeared]
    ├── ip route delete
    └── bridge fdb del
```

### VXLANReconciler.EnsureSubnetPublished → called once at agent startup
```
EnsureSubnetPublished
├── AllocatedSubnet                           [check if already assigned]
├── allocateNextFreeSubnet                    [if not yet]
│   └── (see above)
└── store.Put(observed/node/{nodeID}/subnet)
```

## DNS Side (network/dns.go, network/dns_forwarder.go)

### DNSServer (control plane, port 15353)
```
DNSServer.Start(ctx)
├── net.ListenPacket("udp", listenAddress)
└── FOR EACH incoming packet:
    └── handleDNSQuery
        ├── parseDNSQuestionName              [extract queried hostname]
        ├── StoreBackedResolver.ResolveEndpoints(serviceName)
        │   └── store.Scan(derived/endpoint/{serviceName}/)
        ├── IF found:
        │   └── buildDNSARecordResponse → reply with first IP
        └── ELSE:
            └── sendNXDomainResponse
```

### DNSForwarder (agent side, per-node, port 53)
```
DNSForwarder.Start(ctx)
├── net.ListenPacket("udp", listenAddress)
└── FOR EACH incoming packet:
    └── handleForwardedQuery
        ├── parseDNSQuestionName
        ├── dnsCache.get(queryName)           [return cached if hit]
        ├── IF name ends in ".cca.local":
        │   └── forwardToUpstream(ccaUpstream) [→ control plane :15353]
        └── ELSE:
            └── forwardToUpstream(systemUpstream) [→ /etc/resolv.conf]
        └── dnsCache.put(queryName, response, ttl)
```

### ParseSystemResolvers — called at agent startup
```
ParseSystemResolvers(resolvConfPath, excludeAddress)
└── reads /etc/resolv.conf, returns first nameserver != excludeAddress
```

## Network Provider Side (network/network.go)

### NetworkProvider interface
```
NetworkProvider
├── AllocateIP(ctx, nodeID, instanceID) → IP
│   [called by buildRuntimeSpecForInstance]
├── ReleaseIP(ctx, nodeID, instanceID)
│   [called by cleanupUndesiredInstance]
└── NodeSubnet(nodeID) → CIDR
    [called by VXLANReconciler, DataPlaneReconciler]
```

### EndpointController (controllers/endpoint.go)
```
EndpointController.Reconcile(ctx, facts)
├── parseInstanceFieldsFromFacts(facts)          [observed/instance/*]
├── extractServiceExposedPorts(facts)             [desired/service/*/expose/*]
├── detectServicesWithReadinessProbe(facts)        [desired/service/*/probe/readiness/method]
├── parseNodeAdvertiseAddresses(facts)             [observed/node/*/address]
├── parseExistingEndpoints(facts)                  [endpoint/service/*]
│
├── buildDesiredEndpoints(instanceFields, servicePorts, readiness, nodeAddresses)
│   ├── FOR each running instance with exposed port:
│   │   ├── skip if readiness probe exists and state != ready
│   │   ├── skip if drain_readiness == not-ready
│   │   ├── IF overlay IP exists → use containerIP:exposedPort     (preferred)
│   │   └── ELSE IF hostPort + nodeAddress → use nodeAddress:hostPort (fallback)
│   └── → map[endpointKey]address
│
└── computeEndpointDiffChanges(desired, existing)
    ├── PUT endpoint/service/{svc}/{id}/{port} for new/changed
    └── DELETE endpoint/service/{svc}/{id}/{port} for stale
```

### StoreBackedResolver (network/resolver.go)
```
StoreBackedResolver.ResolveEndpoints(ctx, serviceName)
├── store.Scan(endpoint/service/{serviceName}/)
└── → []Endpoint{IP, Port, InstanceID}

StoreBackedResolver.ResolveVIP(ctx, serviceName)
├── store.Get(derived/vip/{serviceName})
└── → VIP string
```

### UserSpaceProxy (network/userspace_proxy.go)
```
UserSpaceProxy.ServeHTTP(responseWriter, incomingRequest)
├── extractServiceName(incomingRequest.Host)
│   ├── strip port (":8080")
│   └── strip ".cca.local" suffix
│
├── types.ValidateResourceName(serviceName)
│   ├── IF valid → direct service name routing (e.g. "webserver.cca.local" → "webserver")
│   └── IF invalid → resolveRouteHostname(ctx, serviceName)
│       ├── check routeHostMap cache (5s TTL)
│       ├── IF cache miss/expired → refreshRouteHostMap(ctx)
│       │   ├── store.Scan(desired/route/)
│       │   ├── build hostname→service map from route host + service facts
│       │   │   (e.g. "airflow.home.arpa" → route "airflow" → service "webserver")
│       │   └── cache with 5s TTL
│       ├── IF hostname found → use resolved service name
│       └── IF hostname not found → 400 "invalid service name in Host header"
│
├── resolver.ResolveEndpoints(ctx, serviceName)
│   └── → []Endpoint (or error → 502)
│
├── IF no endpoints + warm-zero enabled → handleColdActivation
│   ├── cap concurrent waiters at 100/service (DoS protection)
│   ├── poll for endpoints every 500ms up to 30s
│   └── timeout → 503
│
├── recordLastRequestTime(ctx, serviceName)
│   └── store.Put(observed/service/{name}/last_request_time)
│
└── forwardToBackend(responseWriter, incomingRequest, serviceName, endpoints)
    ├── loadBalancer.Select(endpoints) → least-connections pick
    ├── circuitBreaker.Allow(backend) → check backend health
    └── httputil.ReverseProxy.ServeHTTP → forward to backend
```

### caddy-sync (scripts/caddy-sync.sh)

External process on the testbed host that reconciles Caddyfile from CCattler route facts.

```
caddy-sync loop (every ~40s)
├── query_api /api/state?prefix=desired/route/
│   └── extracts per-route: host, service, port, sticky, cookie
│
├── query_api /api/state?prefix=derived/route/
│   └── extracts per-route: derived/route/{name}/backends
│       (comma-separated host:port list, e.g. "192.168.124.20:8080")
│
├── build_caddyfile
│   ├── FOR each route with host + service + backends:
│   │   ├── {host} { tls internal { on_demand } }
│   │   └── reverse_proxy {backend1} {backend2} ...
│   └── skip routes with no backends resolved
│
├── IF Caddyfile changed:
│   ├── write /etc/caddy/Caddyfile
│   └── caddy reload --config /etc/caddy/Caddyfile
│
└── NOTE: backends are host-mapped addresses (VM IP:hostPort),
    so traffic flows: Caddy → UserSpaceProxy → container
    The proxy resolves external hostnames via route host facts.
```

## CLI Side (cmd/cca/command_status.go)

### cca events → event stream display
```
executeEventsCommand / executeEventsFollowMode
├── fetch /api/events or SSE /api/events/stream
├── stripEventTargetPrefix(event.Target)
│   └── remove "instance/", "node/", "service/" prefix
│       → bare target for display (e.g. "scheduler-eb7509a28e")
└── Printf("%-24s  %-22s  %-30s  %s", timestamp, kind, target, detail)
```

Event target assembly (server side):
```
classifyWatchEventAsSemanticEvent(watchEvent)
├── parse fact key → instanceID + subPath via splitInstanceSubPath
│   e.g. "eb7509a28e/init/step/1" → id="eb7509a28e", sub="init/step/1"
├── build target: "instance/" + instanceID (bare ID only)
├── build detail: state message + " (subPath)" if sub-resource
└── enrichInstanceEventWithServiceName
    ├── lookup observed/instance/{id}/service
    └── prepend serviceName to instanceID in target
        → "instance/scheduler-eb7509a28e"
```

### cca logs → container log streaming
```
executeLogsCommand(config)
├── parseContainerName(arg) → serviceName, instanceID
│   e.g. "worker-43a2115efd" → service="worker", id="43a2115efd"
├── IF instanceID set:
│   └── streamContainerLogs(serviceName, instanceID, config)
├── ELSE:
│   ├── resolveServiceNamesForLogs (by name or --tag)
│   ├── fetchObservedInstanceFacts
│   ├── findInstanceIDsForService (running instances only)
│   └── streamContainerLogs for each instance
│
streamContainerLogs(serviceName, instanceID, config)
├── discoverInstancePlacement(instanceID)
│   └── GET /api/state?prefix=placement/instance/{id}
│       → nodeID
├── discoverNodeDebugAddress(nodeID)
│   └── GET /api/state?prefix=observed/node/{nodeID}/debug_address
│       → host:port
├── GET http://{debugAddress}/debug/logs?instance={id}&tail=...&follow=...
├── IF 404 (workload not found):
│   ├── lookupInstanceState(instanceID) → check if in init/pulling
│   └── print hint with current instance state
└── stream response body to stdout
```

### cca agent debug → local runtime inspection
```
executeAgentDebugCommand()
├── TRY: runtime.NewContainerdRuntime("")       [connect to containerd socket]
│   └── IF success → use containerd client API (no nerdctl needed)
├── FALLBACK: runtime.NewContainerRuntime()     [shells out to nerdctl/docker]
│
├── displayDiscoveredContainers(inspector)
│   └── inspector.DiscoverAllContainers(ctx)
│       ├── containerd: client.Containers() + task.Status()
│       └── nerdctl: "nerdctl ps -a --format ..."
│
├── displayDiscoveredStats(inspector)
│   └── inspector.DiscoverAllContainerStats(ctx)
│       ├── containerd: cgroup v2 metrics per task
│       └── nerdctl: "nerdctl stats --no-stream --format ..."
│
└── displayDiscoveredImages(inspector)
    └── inspector.ListImages(ctx)
        ├── containerd: client.ListImages()
        └── nerdctl: "nerdctl images --format ..."
```
