# CCattler Extensibility Call Graph Reference

Machine-readable function call index for all pluggable extension points:
Runtime, CloudProvider, DataPlaneProvider, StateStore, Controller interfaces,
registration patterns, and the controller SDK. Load this file instead of
reading source.

## Runtime Interface (runtime/runtime.go)

### Interface methods (runtime/runtime.go:61–103)

| Method | Signature | Line |
|---|---|---|
| `Start` | `(ctx, spec Spec) error` | `:64` |
| `Stop` | `(ctx, id string) error` | `:68` |
| `Status` | `(ctx, id string) (Status, error)` | `:72` |
| `List` | `(ctx) ([]Status, error)` | `:75` |
| `Exec` | `(ctx, id string, execSpec ExecSpec) error` | `:81` |
| `ExecInit` | `(ctx, image string, execSpec ExecSpec) error` | `:87` |
| `Stats` | `(ctx, id string) (ResourceStats, error)` | `:92` |
| `Logs` | `(ctx, id string, options LogsOptions) (io.ReadCloser, error)` | `:96` |
| `Resize` | `(ctx, id string, cpuMillicores int64, memoryBytes int64) error` | `:102` |

### Optional interfaces

| Interface | Methods | File:Line | Implementors |
|---|---|---|---|
| `GracefulStopper` | `StopWithGracePeriod(ctx, id, duration) error` | `:108` | Containerd, Container, Process |
| `HostPortProvider` | `HostPortsForInstance(id) map[int]int` | `:115` | Containerd, Container |
| `ImageLister` | `ListImages(ctx) ([]ImageInfo, error)` | `:147` | Containerd, Container |
| `ImagePuller` | `PullImage(ctx, ref, creds) error` / `IsImageCached(ctx, ref) bool` | `:161` | Containerd, Container |
| `ExecCapturer` | `ExecCapture(ctx, id, ExecSpec) ([]byte, error)` | `:154` | Containerd, Container |

### Implementations

| Runtime | File | Flag | Backend |
|---|---|---|---|
| `ContainerdRuntime` | `runtime/containerd.go:76` | `--runtime containerd` (default) | containerd gRPC |
| `ContainerRuntime` | `runtime/container.go:43` | `--runtime container` | nerdctl/lima CLI |
| `ProcessRuntime` | `runtime/process.go:28` | `--runtime process` | OS exec |
| `SimulatorRuntime` | (in-memory, tests) | `cca run` / `cca run-container` | Simulated |

### Runtime selection → createAgentRuntimeAdapter
```
createAgentRuntimeAdapter(runtimeBackend, advertiseAddr, clusterCIDR)
                                               (cmd/cca/command_agent.go:387)
├── "containerd":
│   ├── NewContainerdRuntime("")                (runtime/containerd.go:76)
│   ├── .SetNetwork("cca-net")
│   └── .SetDNS(advertiseAddress)
├── "container":
│   ├── NewContainerRuntime()                   (runtime/container.go:43)
│   ├── .SetNetwork("cca-net")
│   └── .SetDNS(advertiseAddress)
└── "process":
    └── NewProcessRuntime()                     (runtime/process.go:28)
```

### Agent → Runtime call paths
```
Agent.executeReconciliationCycle
├── runtime.List() → runningByID map
│
├── FOR EACH desired instance (not running):
│   └── reconcileDesiredInstance
│       ├── ensureImageAvailable
│       │   ├── ImagePuller.IsImageCached(ctx, ref)     [if implements]
│       │   └── ImagePuller.PullImage(ctx, ref, creds)  [5min timeout]
│       ├── runtime.ExecInit(image, initSpec)            [init steps]
│       └── startInstanceAndPublishState
│           ├── runtime.Start(spec)                      [main start]
│           └── runtime.Status(ctx, id) → get IP
│
├── FOR EACH running instance:
│   ├── detectConfigChange → runtime.Stop + restart
│   ├── reconcileResourceChanges
│   │   └── runtime.Resize(ctx, id, cpu, mem)
│   │       └── IF ErrResizeUnsupported → stop+restart
│   └── observeInstanceState
│       └── runtime.Status(ctx, id)
│
├── cleanupUndesiredInstance
│   └── runtime.Stop(ctx, id)
│   └── GracefulStopper.StopWithGracePeriod      [if configured]
│
└── telemetry (1s tick):
    └── CollectAndReportTelemetry
        ├── runtime.List(ctx)
        └── runtime.Stats(ctx, id) → ResourceStats
```

### Key types (runtime/runtime.go)

| Type | Line | Purpose |
|---|---|---|
| `Spec` | `:33` | ID, ServiceName, Image, Command, Env, ConfigFiles, Ports, CPUm, MemoryB, IP, LogMaxSize |
| `Status` | `:49` | ID, Running, PID, ExitCode, Error, IP |
| `ResourceStats` | `:129` | CPUMillicores, MemoryBytes, MemoryLimitBytes, OOMKillCount |
| `ExecSpec` | `:182` | Command, Env, ConfigFiles |
| `LogsOptions` | `:120` | Follow, TailLines, Since, Timestamps |
| `ImageInfo` | `:137` | Repository, Tag, ImageID, SizeBytes |

### Sentinel errors

| Error | Meaning |
|---|---|
| `ErrNotFound` | Workload never started by this runtime |
| `ErrResizeUnsupported` | Runtime cannot resize in place → stop+restart |

---

## Cloud Provider Interface (cloud/cloud.go)

### Interface methods (cloud/cloud.go:15–56)

| Method | Signature | Line |
|---|---|---|
| `ProviderName` | `() string` | `:17` |
| `ListInstances` | `(ctx) ([]CloudInstance, error)` | `:22` |
| `CreateInstance` | `(ctx, InstanceConfig) (string, error)` | `:26` |
| `TerminateInstance` | `(ctx, providerInstanceID string) error` | `:30` |
| `EnsureLoadBalancer` | `(ctx, LoadBalancerConfig) (string, error)` | `:37` |
| `DeleteLoadBalancer` | `(ctx, serviceName string) error` | `:40` |
| `ListLoadBalancers` | `(ctx) ([]LoadBalancerStatus, error)` | `:43` |
| `EnsureRoute` | `(ctx, RouteConfig) error` | `:49` |
| `DeleteRoute` | `(ctx, destinationCIDR string) error` | `:52` |
| `ListRoutes` | `(ctx) ([]RouteEntry, error)` | `:55` |

### Registry pattern (cloud/registry.go)
```
CloudProviderFactory = func(ctx, projectID, region string) (CloudProvider, error)
                                                    (cloud/registry.go:12)

cloudProviderFactories = map[string]CloudProviderFactory{}  (:16)

RegisterCloudProviderFactory(name, factory)         (:20)  [called from init()]
└── panic on duplicate name

LookupCloudProviderFactory(name) → factory          (:29)  [called at startup]
```

### Implementations

| Provider | File | Build Tag | Status |
|---|---|---|---|
| `AWSCloudProvider` | `cloud/aws.go:50` | `cloudprovider` | Production |
| `GCPCloudProvider` | `cloud/gcp.go:45` | `cloudprovider` | Production |
| `AzureCloudProvider` | `cloud/azure.go:15` | (none) | Stub |
| `SimulatorCloudProvider` | `cloud/simulator.go:16` | (none) | Test |

### AWS implementation (cloud/aws.go)
```
AWSCloudProvider
├── ec2Client        → EC2 API
│   ├── RunInstances       [CreateInstance]
│   ├── TerminateInstances [TerminateInstance]
│   └── DescribeInstances  [ListInstances]
├── elbv2Client      → ELBv2 API
│   ├── CreateLoadBalancer + CreateTargetGroup + CreateListener  [EnsureLoadBalancer]
│   ├── DeleteLoadBalancer + DeleteTargetGroup                   [DeleteLoadBalancer]
│   └── DescribeLoadBalancers                                    [ListLoadBalancers]
└── vpcRouteTableID  → VPC route table
    ├── CreateRoute / ReplaceRoute  [EnsureRoute]
    ├── DeleteRoute                  [DeleteRoute]
    └── DescribeRouteTables          [ListRoutes]

Tag scheme:
    ccattler:managed = "true"
    ccattler:service = <serviceName>
    ccattler:node-id = <nodeID>
Resource name: "cca-<sanitized>" (max 32 chars)
Credentials: default AWS credential chain
```

### GCP implementation (cloud/gcp.go)
```
GCPCloudProvider
├── instancesClient       → GCE instances
│   ├── Insert                [CreateInstance]
│   ├── Delete                [TerminateInstance]
│   └── AggregatedList        [ListInstances]
├── forwardingRulesClient → forwarding rules (LB entry)
├── targetPoolsClient     → target pools (LB backends)
│   ├── Insert/Get/RemoveInstance  [EnsureLoadBalancer, DeleteLoadBalancer]
│   └── List                       [ListLoadBalancers]
└── routesClient          → VPC routes
    ├── Insert  [EnsureRoute]
    ├── Delete  [DeleteRoute]
    └── List    [ListRoutes]

Label scheme:
    ccattler-managed = "true"
    ccattler-service = <serviceName>
    ccattler-node-id = <nodeID>
Resource name: "cca-<sanitized>" (max 63 chars)
Credentials: Application Default Credentials
```

### Cloud controllers → CloudProvider call paths
```
cmd/cca/command_server.go (:384):
IF parsedConfig.cloudProviderName != "":
  ├── LookupCloudProviderFactory(name)
  ├── factory(ctx, projectID, region) → cloudProviderInstance
  └── controllerList = append(controllerList,
      ├── NewNodeLifecycleController(cloudProvider)
      ├── NewCloudLoadBalancerController(cloudProvider, factStore)
      └── NewCloudRouteController(cloudProvider, factStore)
      )
```

### NodeLifecycleController.Reconcile
```
NodeLifecycleController.Reconcile(ctx, facts)
                                (controllers/cloud_node_lifecycle.go)
├── cloudProvider.ListInstances(ctx)
├── build nodeID→providerID map from facts
├── FOR EACH cloud instance:
│   └── IF terminated AND node still alive:
│       └── PUT observed/node/{id}/state = "unreachable"
└── changes[]
```

### CloudLoadBalancerController.Reconcile
```
CloudLoadBalancerController.Reconcile(ctx, facts)
                                (controllers/cloud_loadbalancer.go)
├── scan desired/route/ for services needing external LBs
├── cloudProvider.ListLoadBalancers(ctx)
├── FOR EACH desired LB:
│   ├── resolve backends from endpoints
│   └── cloudProvider.EnsureLoadBalancer(ctx, config) → externalAddr
│       └── PUT derived/loadbalancer/{svc}/address = externalAddr
├── FOR EACH stale LB:
│   └── cloudProvider.DeleteLoadBalancer(ctx, serviceName)
└── changes[]
```

### CloudRouteController.Reconcile
```
CloudRouteController.Reconcile(ctx, facts)
                                (controllers/cloud_routes.go)
├── scan observed/node/ for node CIDRs
├── cloudProvider.ListRoutes(ctx)
├── FOR EACH node CIDR:
│   └── IF no matching route:
│       └── cloudProvider.EnsureRoute(ctx, RouteConfig{CIDR, nodeID, instanceID})
├── FOR EACH stale route:
│   └── cloudProvider.DeleteRoute(ctx, destinationCIDR)
└── changes[]
```

### Key types (cloud/cloud.go)

| Type | Line | Purpose |
|---|---|---|
| `CloudInstance` | `:59` | ProviderInstanceID, NodeID, State, Region, InstanceType |
| `InstanceState` | `:73` | running, pending, terminated, stopped |
| `InstanceConfig` | `:87` | InstanceType, Region, Zone, ImageID, Labels |
| `LoadBalancerConfig` | `:101` | ServiceName, Port, TargetPort, Protocol, Backends |
| `LoadBalancerBackend` | `:114` | NodeID, Address, Port |
| `LoadBalancerStatus` | `:124` | ServiceName, ExternalAddress, State |
| `LoadBalancerState` | `:135` | active, provisioning, deleting |
| `RouteConfig` | `:147` | DestinationCIDR, TargetNodeID, TargetInstanceID |
| `RouteEntry` | `:157` | DestinationCIDR, TargetNodeID, TargetInstanceID |

---

## Data Plane Provider Interface (network/dataplane.go)

### Interface methods (network/dataplane.go:20–36)

| Method | Signature | Line |
|---|---|---|
| `ReconcileVIPDataPlane` | `(ctx, serviceConfigs []ServiceVIPConfig) error` | `:25` |
| `ReconcileRouteDataPlane` | `(ctx, routeConfigs []RouteDataPlaneConfig) error` | `:30` |
| `Cleanup` | `(ctx) error` | `:35` |

### Optional interfaces (network/dataplane.go:100–127)

| Interface | Methods | Line | Purpose |
|---|---|---|---|
| `DataPlaneInitializer` | `Initialize(ctx) error` | `:100` | One-time init before first reconcile |
| `DataPlaneNodeRegistrar` | `RegisterNode(ctx, nodeIP, podCIDR) error` | `:106` | Register node metadata on startup |
| `DataPlaneBridgeRetrier` | `RetryBridgeAttachment(ctx, podCIDR) error` | `:112` | Defer bridge attach until CNI creates it |
| `EndpointIdentityRegistrar` | `RegisterEndpointIdentity(ctx, containerIP, hostIP, svcName, port) error` | `:118` | IP→identity for policy enforcement |
| `DataPlanePolicyReconciler` | `ReconcilePolicies(ctx) error` | `:125` | Translate network policy rules |

### Registry pattern (network/dataplane_registry.go)
```
DataPlaneFactory = func(factStore StateStore, clusterName, nodeID string) DataPlaneProvider
                                                    (network/dataplane_registry.go:11)

dataPlaneFactories = map[string]DataPlaneFactory{}   (:15)

RegisterDataPlaneFactory(name, factory)              (:19)  [called from init()]
└── panic on duplicate name

LookupDataPlaneFactory(name) → factory               (:28)  [called at agent startup]
```

### Implementations

| Data Plane | File | Build Tag | Mechanism |
|---|---|---|---|
| `NftablesDataPlane` | `network/nftables_dataplane.go:60` | (none) | `nft -f` atomic ruleset |
| `IptablesDataPlane` | `network/iptables_dataplane.go:49` | (none) | iptables nat chains |
| `EbpfDataPlane` | `network/ebpf_dataplane.go:83` | `ebpf` | XDP/TC BPF programs |
| `ShadowDataPlane` | `network/shadow_dataplane.go` | (none) | Dual-write canary compare |
| `SimulatorDataPlane` | `network/simulator_dataplane.go` | (none) | In-memory recording |

### Agent → DataPlaneProvider call paths
```
Agent.Run(ctx)                                       (agent/agent.go:291)
├── initializeDataPlaneIfSupported(ctx)              (:397)
│   ├── DataPlaneInitializer.Initialize(ctx)         [if supported]
│   │   └── eBPF: loadEbpfPrograms, extractTCPrograms,
│   │            ensurePinDirectory, extractAndPinMaps,
│   │            attachCgroupPrograms, scanExistingServiceIDs
│   └── DataPlaneNodeRegistrar.RegisterNode(ctx, nodeIP, podCIDR) [if supported]
│       └── eBPF: program node map, attach TC to bridge
│
└── go dataPlaneLoop(ctx)                            (:504) [goroutine, 1s]
    └── DataPlaneReconciler.Reconcile(ctx)
        ├── buildServiceVIPConfigs                   [from store facts]
        │   └── → []ServiceVIPConfig
        ├── DataPlaneProvider.ReconcileVIPDataPlane(serviceConfigs)
        ├── buildRouteDataPlaneConfigs               [from store facts]
        │   └── → []RouteDataPlaneConfig
        └── DataPlaneProvider.ReconcileRouteDataPlane(routeConfigs)
```

### Key types (network/dataplane.go)

| Type | Line | Purpose |
|---|---|---|
| `ServiceVIPConfig` | `:41` | ServiceName, VirtualIP, Port, Protocol, Backends |
| `DataPlaneBackend` | `:67` | Address, Port |
| `RouteDataPlaneConfig` | `:80` | RouteName, ServiceName, VirtualIP, Port, Backends, StickyMethod, CookieConfig |
| `RouteCookieConfig` | `:130` | Name, Secure, HTTPOnly, SameSite |

### nftables constants (network/nftables_dataplane.go)

| Constant | Value | Line |
|---|---|---|
| `nftablesTableName` | `"cca"` | `:19` |
| `nftablesVIPDispatchMap` | `"vip_dispatch"` | `:23` |
| `nftablesServiceChainPrefix` | `"svc_"` | `:27` |
| `nftablesRouteChainPrefix` | `"rt_"` | `:31` |
| `containerBridgeInterface` | `"cca0"` | `:35` |
| `etcdClientPort` | `2379` | `:40` |

### iptables constants (network/iptables_dataplane.go)

| Constant | Value | Line |
|---|---|---|
| `iptablesMainChain` | `"CCA_SERVICES"` | `:18` |
| `iptablesServiceChainPrefix` | `"CCA_SVC_"` | `:22` |
| `iptablesRouteChainPrefix` | `"CCA_RT_"` | `:37` |
| `vipDummyInterface` | `"cca-vip0"` | `:26` |
| `maxJumpRuleRemovalAttempts` | `20` | `:33` |

---

## State Store Interface (store/store.go)

### Interface methods (store/store.go:117–165)

| Method | Signature | Line |
|---|---|---|
| `Get` | `(ctx, key string) (*Fact, error)` | `:120` |
| `Put` | `(ctx, key string, value []byte) (int64, error)` | `:123` |
| `Delete` | `(ctx, key string) error` | `:126` |
| `Scan` | `(ctx, prefix string) ([]Fact, error)` | `:129` |
| `ScanWithRevision` | `(ctx, prefix string) (*ScanResult, error)` | `:134` |
| `Watch` | `(ctx, key string, opts WatchOption) (<-chan Event, error)` | `:140` |
| `GrantLease` | `(ctx, ttlSeconds int64) (int64, error)` | `:145` |
| `PutWithLease` | `(ctx, key string, value []byte, leaseID int64) (int64, error)` | `:148` |
| `RenewLease` | `(ctx, leaseID int64) (int64, error)` | `:151` |
| `RevokeLease` | `(ctx, leaseID int64) error` | `:153` |
| `Transaction` | `(ctx, []Compare, []Op, []Op) (bool, error)` | `:158` |
| `Revision` | `(ctx) (int64, error)` | `:161` |
| `Close` | `() error` | `:164` |

### Implementations

| Store | File | Use Case | Watch | Leases |
|---|---|---|---|---|
| `MemoryStore` | `store/memory.go:131` | Tests, `cca run` | Ring buffer (4096 events) | Synthetic |
| `EtcdStore` | `store/etcd.go` | Production clusters | etcd watch API | Real TTL |

### Key abstractions (store/store.go)

| Type | Line | Purpose |
|---|---|---|
| `Fact` | `:11` | Key, Value, Revision, CreateRevision, LeaseID |
| `Event` | `:27` | Type (Put/Delete/Overflow/Compacted), Fact, Prev |
| `EventType` | `:37` | EventPut=0, EventDelete=1, EventOverflow=2, EventCompacted=3 |
| `Compare` | `:58` | Key, Revision (0 = must not exist) |
| `Op` | `:66` | Type (OpPut/OpDelete), Key, Value |
| `ScanResult` | `:105` | Facts, Revision |
| `WatchOption` | `:86` | Prefix, StartRevision |

### MemoryStore constants

| Constant | Value | File:Line |
|---|---|---|
| `defaultEventHistoryCapacity` | `4096` | `store/memory.go:57` |
| `defaultWatchChannelBufferSize` | `256` | `store/memory.go:157` |
| `memoryStoreTransactionOperationLimit` | `128` | `store/memory.go:35` |

---

## Controller Interface (controllers/controller.go)

### Core interface (controllers/controller.go:50–63)

| Method | Signature | Line |
|---|---|---|
| `Name` | `() string` | `:52` |
| `Watch` | `() []string` | `:57` |
| `Reconcile` | `(ctx, facts []store.Fact) ([]Change, error)` | `:62` |

### Extended interfaces

| Interface | Extra Method | Line | Purpose |
|---|---|---|---|
| `DeltaController` | `ReconcileDelta(ctx, allFacts, changedEvents) ([]Change, error)` | `:70` | Incremental reconciliation |
| `PostCommitController` | `ExecutePostCommitOperations(ctx) error` | `:88` | Side effects after CAS commit |

### Change type (controllers/controller.go:25–36)

| Field | Type | Purpose |
|---|---|---|
| `Type` | `store.OpType` | Put or Delete |
| `Key` | `string` | Fact store key |
| `Value` | `[]byte` | New value (puts only) |
| `Group` | `string` | Atomic group (non-empty = must not split) |

---

## Controller SDK (controllers/sdk.go)

### CustomController → user-provided reconciliation function
```
NewCustomController(name, prefixes, reconcileFn)     (controllers/sdk.go:121)
├── controllerName = name
├── watchPrefixes = prefixes
├── reconcileFunc = reconcileFn
└── primaryPrefix = prefixes[0]

CustomController.Reconcile(ctx, facts)               (:152)
├── NewFactMap(facts, primaryPrefix)                  (:27)
│   └── FOR EACH fact with prefix:
│       └── split key → entityName + fieldName
│           → entities[entityName][fieldName] = value
└── reconcileFunc(ctx, factMap) → []Change
```

### FactMap API (controllers/sdk.go:16–94)

| Method | Signature | Line | Purpose |
|---|---|---|---|
| `NewFactMap` | `(facts, prefix) *FactMap` | `:27` | Organize flat facts into entities |
| `Entities` | `() []string` | `:53` | List all entity names |
| `GetField` | `(entity, field) string` | `:63` | Get entity field value |
| `HasEntity` | `(entity) bool` | `:72` | Check entity existence |
| `EntityFields` | `(entity) map[string]string` | `:79` | Get all fields for entity |
| `Raw` | `() []store.Fact` | `:92` | Get original fact list |

### Convenience constructors (controllers/sdk.go:158–165)

| Function | Line | Purpose |
|---|---|---|
| `PutChange(key, value)` | `:158` | Create a put Change |
| `DeleteChange(key)` | `:163` | Create a delete Change |

---

## Controller Runner (controllers/runner.go)

### Runner.Run → start controllers
```
Runner.Run(ctx)                                      (controllers/runner.go:196)
├── EventProjector.Run(ctx)                          [goroutine, if eventLog set]
└── FOR EACH controller:
    └── go runSingleController(ctx, controller)      (:238) [goroutine]
        └── retry loop with backoff [100ms → 500ms → 1s → 2s → 5s → 30s]
            └── runControllerLoop(ctx, controller)   (:274)
                ├── setupControllerWatches           (:300)
                │   └── FOR EACH prefix in Watch():
                │       ├── store.Watch(prefix, {Prefix: true})
                │       └── go forwardWatchEvents    (:313) [goroutine]
                ├── executeReconciliationCycle        [initial]
                └── runReconcileSelectLoop            (:352)
                    └── select:
                        ├── reconcileTrigger → debounce(50ms) → executeReconciliationCycle
                        ├── resyncTicker(30s) → executeReconciliationCycle
                        └── watchLost → return error → restart controller
```

### executeReconciliationCycle → single reconcile pass
```
executeReconciliationCycle(ctx, controller, deltaEvents)
                                                     (controllers/runner.go:409)
└── FOR attempt 0..maxAttempts:
    └── attemptSingleReconciliation                  (:469)
        ├── scanFactsForController(ctx, controller)  (:582)
        │   └── FOR EACH prefix: store.Scan(prefix) → merge all facts
        ├── computeReconciliationChanges             (:518)
        │   ├── IF deltaEvents AND controller is DeltaController:
        │   │   └── controller.ReconcileDelta(ctx, allFacts, deltaEvents)
        │   │       └── IF ErrDeltaFallback → fall through
        │   └── controller.Reconcile(ctx, allFacts) → changes[]
        ├── validateAndCapChanges(name, changes)     (:537)
        │   └── cap to maxTransactionChanges (60)
        ├── buildReconciliationTransaction           (:599)
        │   ├── Compare: input key revisions [guard]
        │   ├── Then: put/delete change keys
        │   └── appendInputKeyGuards                 (:637)
        ├── store.Transaction(ctx, compares, onSuccess, nil)
        │   └── IF false → conflictDetected, retry
        ├── metricsCollector.RecordReconciliation(...)
        ├── reconciliationDuration.ObserveSince(...)
        ├── reconciliationTotal.Inc(controller, "success"/"error")
        └── executePendingPostCommitOperations       (:568)
            └── IF controller is PostCommitController:
                └── controller.ExecutePostCommitOperations(ctx)
```

### HARunner → leader-election wrapper
```
HARunner.Run(ctx)                                    (controllers/ha_runner.go:78)
└── haRunner.election.Run(ctx)
    ├── OnAcquired → startControllers               (:101)
    │   └── NewRunner(factStore, controllers...)
    │       └── runner.Run(ctx)                      [goroutine]
    └── OnLost → stopControllers                     (:140)
        └── cancelRunner() → wait for runner done
```

### Runner constants (controllers/runner.go:23–56)

| Constant | Value | Line |
|---|---|---|
| `defaultControllerDebounceInterval` | `50ms` | `:26` |
| `defaultControllerResyncInterval` | `30s` | `:30` |
| `reconcileRetryBaseDelayMs` | `10` | `:34` |
| `reconcileRetryMaxDelayMs` | `500` | `:38` |
| `etcdTransactionOperationLimit` | `128` | `:42` |
| `maxTransactionChanges` | `60` | `:55` |

---

## Controller Registration (cmd/cca/helpers.go, cmd/cca/command_server.go)

### Core controllers — always registered (cmd/cca/helpers.go:286)
```
coreControllers():
  1.  instance              controllers/instance.go
  2.  scheduler             scheduler/scheduler.go
  3.  endpoint              controllers/endpoint.go
  4.  failure               controllers/failure.go
  5.  autoscale             controllers/autoscale.go
  6.  intent-resolver        controllers/intent.go
  7.  rollout               controllers/rollout.go
  8.  init                   controllers/init.go
  9.  stateful-dns           controllers/stateful_dns.go
  10. stateful-volume        controllers/stateful_volume.go
  11. route                  controllers/route.go
  12. eviction               controllers/eviction.go
  13. job                    controllers/job.go
  14. cron                   controllers/cronjob.go
  15. instance-gc            controllers/instance_gc.go
  16. audit-log-gc           controllers/audit_gc.go
```

### Leader-election controllers (cmd/cca/command_server.go:370)
```
startControllersWithLeaderElection():
  17. node-failure           controllers/nodefailure.go
  18. drain                  controllers/drain.go
  19. network                controllers/network.go
  20. network-policy         controllers/network_policy.go
  21. warm-zero              controllers/warmzero.go
```

### Conditional cloud controllers (cmd/cca/command_server.go:384)
```
IF --cloud-provider set:
  22. cloud-node-lifecycle   controllers/cloud_node_lifecycle.go
  23. cloud-loadbalancer     controllers/cloud_loadbalancer.go
  24. cloud-routes           controllers/cloud_routes.go
```

### Non-controller components on same runner
```
  - EventProjector           controllers/event_projector.go
  - AuthController           controllers/auth.go
  - MetricsCollector         controllers/metrics.go
  - Bulkhead                 controllers/bulkhead.go
```

---

## Build Tags

| Tag | Effect | Files |
|---|---|---|
| `cloudprovider` | AWS + GCP providers, links AWS/GCP SDKs | `cloud/aws.go`, `cloud/gcp.go` |
| `ebpf` | eBPF data plane, links BPF libraries | `network/ebpf_*.go` |
| (none) | Default: containerd + nftables + iptables + simulator | all other files |

---

## Reverse Index

| Function | Called By |
|---|---|
| `Runtime.Start` | `startInstanceAndPublishState` (agent) |
| `Runtime.Stop` | `cleanupUndesiredInstance`, `detectConfigChange` (agent) |
| `Runtime.Status` | `startInstanceAndPublishState`, `observeInstanceState` (agent) |
| `Runtime.List` | `executeReconciliationCycle` (agent), `CollectAndReportTelemetry` |
| `Runtime.Exec` | `runProbeCheck` (exec probes) |
| `Runtime.ExecInit` | `runInitCommand` (init steps) |
| `Runtime.Stats` | `reportWorkloadTelemetry` |
| `Runtime.Resize` | `reconcileResourceChanges` |
| `Runtime.Logs` | `handleLogs` (api/server.go) |
| `GracefulStopper.StopWithGracePeriod` | `cleanupUndesiredInstance` (if grace period configured) |
| `HostPortProvider.HostPortsForInstance` | `startInstanceAndPublishState` → `publishInstanceHostPorts` |
| `ImagePuller.PullImage` | `ensureImageAvailable` |
| `ImagePuller.IsImageCached` | `ensureImageAvailable` |
| `ImageLister.ListImages` | `handleImagesList` (api) |
| `ExecCapturer.ExecCapture` | `handleExec` (api) |
| `createAgentRuntimeAdapter` | `cmd/cca/command_agent.go` (agent startup) |
| `CloudProvider.ListInstances` | `NodeLifecycleController.Reconcile` |
| `CloudProvider.EnsureLoadBalancer` | `CloudLoadBalancerController.Reconcile` |
| `CloudProvider.DeleteLoadBalancer` | `CloudLoadBalancerController.Reconcile` |
| `CloudProvider.ListLoadBalancers` | `CloudLoadBalancerController.Reconcile` |
| `CloudProvider.EnsureRoute` | `CloudRouteController.Reconcile` |
| `CloudProvider.DeleteRoute` | `CloudRouteController.Reconcile` |
| `CloudProvider.ListRoutes` | `CloudRouteController.Reconcile` |
| `RegisterCloudProviderFactory` | `init()` in `cloud/aws.go`, `cloud/gcp.go` |
| `LookupCloudProviderFactory` | `cmd/cca/command_server.go` (server startup) |
| `DataPlaneProvider.ReconcileVIPDataPlane` | `DataPlaneReconciler.Reconcile` |
| `DataPlaneProvider.ReconcileRouteDataPlane` | `DataPlaneReconciler.Reconcile` |
| `DataPlaneProvider.Cleanup` | Agent shutdown |
| `DataPlaneInitializer.Initialize` | `initializeDataPlaneIfSupported` |
| `DataPlaneNodeRegistrar.RegisterNode` | `initializeDataPlaneIfSupported` |
| `DataPlaneBridgeRetrier.RetryBridgeAttachment` | `retryBridgeAttachmentIfNeeded` |
| `EndpointIdentityRegistrar.RegisterEndpointIdentity` | `startInstanceAndPublishState` |
| `DataPlanePolicyReconciler.ReconcilePolicies` | `dataPlaneLoop` |
| `RegisterDataPlaneFactory` | `init()` in `network/ebpf_dataplane.go` |
| `LookupDataPlaneFactory` | `cmd/cca/command_agent.go` (agent startup) |
| `StateStore.Get` | Agent (many), controllers (via facts), API handlers |
| `StateStore.Put` | Agent (state writes), runner (transaction path) |
| `StateStore.Scan` | Runner (`scanFactsForController`), API handlers |
| `StateStore.Watch` | Runner (`setupControllerWatches`), EventProjector |
| `StateStore.Transaction` | Runner (`attemptSingleReconciliation`) |
| `Controller.Reconcile` | Runner (`computeReconciliationChanges`) |
| `DeltaController.ReconcileDelta` | Runner (first attempt if delta events) |
| `PostCommitController.ExecutePostCommitOperations` | Runner (after successful commit) |
| `NewCustomController` | Plugin authors via SDK |
| `NewFactMap` | `CustomController.Reconcile` |
| `Runner.Run` | `HARunner.startControllers`, `cca run` (single-node) |
| `HARunner.Run` | `cmd/cca/command_server.go` (server startup) |

---

## Extension Pattern Summary

All pluggable subsystems follow the same four-step pattern:

```
1. Interface in standalone file     (cloud.go, dataplane.go, runtime.go, store.go)
2. Factory function type            (CloudProviderFactory, DataPlaneFactory)
3. Package-level registry map       (cloudProviderFactories, dataPlaneFactories)
4. Registration via init()          (build-tagged source files)
   └── Lookup at startup via CLI flag → LookupFactory(name)
```

New providers: implement the interface, register in `init()`, optionally
gate with a build tag. No component hard-codes which provider is active.
Simulators record calls for test assertions.
