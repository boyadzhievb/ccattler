# CCattler Extensibility Graph Reference

Machine-readable reference for all pluggable extension points. Purpose: understand
every interface boundary, its implementations, and how providers are selected —
without reading source files.

## Extension Points Overview

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                        CCattler Extension Architecture                          │
│                                                                                 │
│  ┌────────────────────────────────────────────────────────────────────────┐     │
│  │                           CONTROL PLANE                                │     │
│  │                                                                        │     │
│  │  ┌──────────────────┐    ┌──────────────────────────────────────────┐  │     │
│  │  │  StateStore       │    │  Controller Interface                    │  │     │
│  │  │  (store/store.go) │    │  (controllers/controller.go)            │  │     │
│  │  │                   │    │                                          │  │     │
│  │  │  • MemoryStore    │    │  • Controller (Name/Watch/Reconcile)     │  │     │
│  │  │  • EtcdStore      │    │  • DeltaController (incremental)        │  │     │
│  │  └──────────────────┘    │  • PostCommitController (side effects)   │  │     │
│  │                           │  • CustomController (SDK)                │  │     │
│  │                           └──────────────────────────────────────────┘  │     │
│  │                                                                        │     │
│  │  ┌──────────────────────────────────────────────────────────────────┐  │     │
│  │  │  CloudProvider                                                   │  │     │
│  │  │  (cloud/cloud.go)                                                │  │     │
│  │  │                                                                  │  │     │
│  │  │  • AWS   (EC2 + NLB + VPC routes)      [build tag: cloudprovider]│  │     │
│  │  │  • GCP   (GCE + forwarding rules + VPC routes)  [cloudprovider] │  │     │
│  │  │  • Azure (stub — not yet implemented)                            │  │     │
│  │  │  • Simulator (in-memory, for tests)                              │  │     │
│  │  └──────────────────────────────────────────────────────────────────┘  │     │
│  └────────────────────────────────────────────────────────────────────────┘     │
│                                                                                 │
│  ┌────────────────────────────────────────────────────────────────────────┐     │
│  │                              NODE AGENT                                │     │
│  │                                                                        │     │
│  │  ┌──────────────────┐    ┌──────────────────────────────────────────┐  │     │
│  │  │  Runtime          │    │  DataPlaneProvider                       │  │     │
│  │  │  (runtime/        │    │  (network/dataplane.go)                  │  │     │
│  │  │   runtime.go)     │    │                                          │  │     │
│  │  │                   │    │  • NftablesDataPlane (default)           │  │     │
│  │  │  • Containerd     │    │  • IptablesDataPlane                    │  │     │
│  │  │  • Container      │    │  • EbpfDataPlane     [build tag: ebpf]  │  │     │
│  │  │  • Process        │    │  • ShadowDataPlane   (canary/compare)   │  │     │
│  │  │  • Simulator      │    │  • SimulatorDataPlane (tests)           │  │     │
│  │  └──────────────────┘    └──────────────────────────────────────────┘  │     │
│  └────────────────────────────────────────────────────────────────────────┘     │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## 1. Runtime Interface

### Interface Definition (runtime/runtime.go:61)

```
Runtime interface {
    Start(ctx, spec Spec) error
    Stop(ctx, id string) error
    Status(ctx, id string) (Status, error)
    List(ctx) ([]Status, error)
    Exec(ctx, id string, execSpec ExecSpec) error
    ExecInit(ctx, image string, execSpec ExecSpec) error
    Stats(ctx, id string) (ResourceStats, error)
    Logs(ctx, id string, options LogsOptions) (io.ReadCloser, error)
    Resize(ctx, id string, cpuMillicores int64, memoryBytes int64) error
}
```

### Optional Interfaces

| Interface | Methods | Implementors |
|---|---|---|
| `GracefulStopper` (runtime/runtime.go:108) | `StopWithGracePeriod(ctx, id, duration)` | ContainerRuntime, ContainerdRuntime, ProcessRuntime |
| `HostPortProvider` (runtime/runtime.go:115) | `HostPortsForInstance(id) map[int]int` | ContainerRuntime, ContainerdRuntime |
| `ImageLister` (runtime/runtime.go:147) | `ListImages(ctx) ([]ImageInfo, error)` | ContainerRuntime, ContainerdRuntime |
| `ImagePuller` (runtime/runtime.go:161) | `PullImage(ctx, ref, creds) error` / `IsImageCached(ctx, ref) bool` | ContainerRuntime, ContainerdRuntime |
| `ExecCapturer` (runtime/runtime.go:154) | `ExecCapture(ctx, id, execSpec) ([]byte, error)` | ContainerRuntime, ContainerdRuntime |

### Implementations

| Runtime | File | Selection Flag | Backend | Networking |
|---|---|---|---|---|
| ContainerdRuntime | runtime/containerd.go:76 | `--runtime containerd` (default) | containerd gRPC API | cca-net overlay, DNS via advertise addr |
| ContainerRuntime | runtime/container.go:43 | `--runtime container` | nerdctl/lima CLI | cca-net bridge, DNS via advertise addr |
| ProcessRuntime | runtime/process.go:28 | `--runtime process` | OS exec | Host network (no overlay) |
| SimulatorRuntime | (in-memory, tests only) | `cca run` / `cca run-container` | Simulated | Simulated |

### Runtime Selection (cmd/cca/command_agent.go:387)

```
createAgentRuntimeAdapter(runtimeBackend, advertiseAddress, clusterCIDR):
    switch runtimeBackend:
        "containerd" → NewContainerdRuntime("") + SetNetwork + SetDNS
        "container"  → NewContainerRuntime()    + SetNetwork + SetDNS
        "process"    → NewProcessRuntime()
```

CLI flag: `cca agent --runtime <containerd|container|process>`

### Key Types

| Type | File | Purpose |
|---|---|---|
| `Spec` | runtime/runtime.go:33 | Workload spec: ID, Image, Env, ConfigFiles, Ports, CPU/Memory, IP |
| `Status` | runtime/runtime.go:49 | Observed state: Running, PID, ExitCode, IP, Error |
| `ResourceStats` | runtime/runtime.go:129 | CPU/Memory usage, OOM kill count |
| `ExecSpec` | runtime/runtime.go:182 | Exec command + env + config files |
| `LogsOptions` | runtime/runtime.go:120 | Follow, TailLines, Since, Timestamps |
| `ImageInfo` | runtime/runtime.go:137 | Repository, Tag, ImageID, SizeBytes |
| `StartError` | runtime/process.go:356 | ID + Reason for launch failures |

### Sentinel Errors

| Error | File | Meaning |
|---|---|---|
| `ErrNotFound` | (each runtime) | Workload never started by this runtime |
| `ErrResizeUnsupported` | (each runtime) | Runtime cannot resize in place (stop + restart needed) |

---

## 2. Cloud Provider Interface

### Interface Definition (cloud/cloud.go:15)

```
CloudProvider interface {
    ProviderName() string

    // Node Lifecycle
    ListInstances(ctx) ([]CloudInstance, error)
    CreateInstance(ctx, InstanceConfig) (string, error)
    TerminateInstance(ctx, providerInstanceID string) error

    // Load Balancer
    EnsureLoadBalancer(ctx, LoadBalancerConfig) (string, error)
    DeleteLoadBalancer(ctx, serviceName string) error
    ListLoadBalancers(ctx) ([]LoadBalancerStatus, error)

    // Routes
    EnsureRoute(ctx, RouteConfig) error
    DeleteRoute(ctx, destinationCIDR string) error
    ListRoutes(ctx) ([]RouteEntry, error)
}
```

### Registration Pattern (cloud/registry.go)

```
CloudProviderFactory = func(ctx, projectID, region string) (CloudProvider, error)

cloudProviderFactories = map[string]CloudProviderFactory{}     // package-level

RegisterCloudProviderFactory(name, factory)   // called from init() in build-tagged files
LookupCloudProviderFactory(name)              // called at startup
```

Build tag `cloudprovider` gates AWS and GCP — they are excluded from default builds.

### Implementations

| Provider | File | Build Tag | Status | Cloud Resources |
|---|---|---|---|---|
| AWS | cloud/aws.go:50 | `cloudprovider` | Production | EC2 instances, NLB + target groups, VPC route table |
| GCP | cloud/gcp.go:45 | `cloudprovider` | Production | GCE instances, forwarding rules + target pools, VPC routes |
| Azure | cloud/azure.go:15 | (none) | **Stub** | All methods return "not yet implemented" |
| Simulator | cloud/simulator.go:16 | (none) | Test only | In-memory maps, call recording |

### AWS Implementation Details (cloud/aws.go)

```
AWSCloudProvider:
    ec2Client        → EC2 API (RunInstances, TerminateInstances, DescribeInstances)
    elbv2Client      → ELBv2 API (NLBs, target groups, listeners)
    vpcRouteTableID  → VPC route table (CreateRoute, ReplaceRoute, DeleteRoute)

    Tag scheme:
        ccattler:managed = "true"        (filter for managed resources)
        ccattler:service = <serviceName> (on LBs)
        ccattler:node-id = <nodeID>      (on instances)

    Resource name format: "cca-<sanitized-name>" (max 32 chars)

    Credentials: default AWS credential chain (env, shared, IAM role)
```

### GCP Implementation Details (cloud/gcp.go)

```
GCPCloudProvider:
    instancesClient       → GCE instances (Insert, Delete, AggregatedList)
    forwardingRulesClient → forwarding rules (LB entry point)
    targetPoolsClient     → target pools (LB backend set)
    routesClient          → VPC routes (Insert, Delete, List)

    Label scheme:
        ccattler-managed = "true"
        ccattler-service = <serviceName>
        ccattler-node-id = <nodeID>

    Resource name format: "cca-<sanitized-name>" (max 63 chars)

    Credentials: Application Default Credentials
```

### Key Types

| Type | File | Purpose |
|---|---|---|
| `CloudInstance` | cloud/cloud.go:59 | ProviderInstanceID, NodeID, State, Region, InstanceType |
| `InstanceState` | cloud/cloud.go:75 | running, pending, terminated, stopped |
| `InstanceConfig` | cloud/cloud.go:87 | InstanceType, Region, Zone, ImageID, Labels |
| `LoadBalancerConfig` | cloud/cloud.go:100 | ServiceName, Port, TargetPort, Protocol, Backends |
| `LoadBalancerStatus` | cloud/cloud.go:124 | ServiceName, ExternalAddress, State |
| `LoadBalancerState` | cloud/cloud.go:136 | active, provisioning, deleting |
| `RouteConfig` | cloud/cloud.go:146 | DestinationCIDR, TargetNodeID, TargetInstanceID |
| `RouteEntry` | cloud/cloud.go:156 | DestinationCIDR, TargetNodeID, TargetInstanceID |

---

## 3. Cloud Controllers

Three controllers are conditionally registered when `--cloud-provider` is set
(cmd/cca/command_server.go:384):

```
if parsedConfig.cloudProviderName != "" {
    controllerList = append(controllerList,
        NewNodeLifecycleController(cloudProviderInstance),
        NewCloudLoadBalancerController(cloudProviderInstance, factStore),
        NewCloudRouteController(cloudProviderInstance, factStore),
    )
}
```

| Controller | File | Purpose |
|---|---|---|
| `NodeLifecycleController` | controllers/cloud_node_lifecycle.go | Detects terminated cloud instances → marks nodes dead |
| `CloudLoadBalancerController` | controllers/cloud_loadbalancer.go | Reconciles cloud LBs from route/service facts |
| `CloudRouteController` | controllers/cloud_routes.go | Programs VPC routes for node overlay CIDRs |

---

## 4. State Store Interface

### Interface Definition (store/store.go:117)

```
StateStore interface {
    Get(ctx, key string) (*Fact, error)
    Put(ctx, key string, value []byte) (int64, error)
    Delete(ctx, key string) error

    Scan(ctx, prefix string) ([]Fact, error)
    ScanWithRevision(ctx, prefix string) (*ScanResult, error)
    Watch(ctx, key string, opts WatchOption) (<-chan Event, error)

    GrantLease(ctx, ttlSeconds int64) (int64, error)
    PutWithLease(ctx, key string, value []byte, leaseID int64) (int64, error)
    RenewLease(ctx, leaseID int64) (int64, error)
    RevokeLease(ctx, leaseID int64) error

    Transaction(ctx, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error)

    Revision(ctx) (int64, error)
    Close() error
}
```

### Implementations

| Store | File | Use Case | Watch Mechanism | Lease Support |
|---|---|---|---|---|
| `MemoryStore` | store/memory.go:131 | Tests, `cca run` local mode | In-memory broadcast, ring buffer (4096 events) | Synthetic (no TTL enforcement) |
| `EtcdStore` | store/etcd.go | Production clusters | etcd watch API, revision-based replay | Real etcd leases with TTL |

### Key Abstractions

| Type | File | Purpose |
|---|---|---|
| `Fact` | store/store.go:11 | Key, Value, Revision, CreateRevision, LeaseID |
| `Event` | store/store.go:27 | Type (Put/Delete/Overflow/Compacted), Fact, Prev |
| `Compare` | store/store.go:58 | Precondition for CAS transactions |
| `Op` | store/store.go:66 | Put or Delete operation in a transaction |
| `ScanResult` | store/store.go:105 | Facts + snapshot Revision |
| `WatchOption` | store/store.go:86 | Prefix matching, StartRevision for gap-free replay |

### Constants

| Constant | Value | Location |
|---|---|---|
| `defaultEventHistoryCapacity` | 4096 | store/memory.go:57 |
| `defaultWatchChannelBufferSize` | 256 | store/memory.go:157 |
| `memoryStoreTransactionOperationLimit` | 128 | store/memory.go:35 |

---

## 5. Data Plane Provider Interface

### Interface Definition (network/dataplane.go:20)

```
DataPlaneProvider interface {
    ReconcileVIPDataPlane(ctx, serviceConfigs []ServiceVIPConfig) error
    ReconcileRouteDataPlane(ctx, routeConfigs []RouteDataPlaneConfig) error
    Cleanup(ctx) error
}
```

### Optional Data Plane Interfaces

| Interface | File | Purpose |
|---|---|---|
| `DataPlaneInitializer` | network/dataplane.go:100 | One-time init before first reconciliation |
| `DataPlaneNodeRegistrar` | network/dataplane.go:106 | Register node metadata on agent startup |
| `DataPlaneBridgeRetrier` | network/dataplane.go:112 | Defer bridge attachment until CNI creates bridge |
| `EndpointIdentityRegistrar` | network/dataplane.go:118 | Register container IP→identity mappings |
| `DataPlanePolicyReconciler` | network/dataplane.go:125 | Translate network policy rules to native format |

### Registration Pattern (network/dataplane_registry.go)

```
DataPlaneFactory = func(factStore StateStore, clusterName, nodeID string) DataPlaneProvider

dataPlaneFactories = map[string]DataPlaneFactory{}

RegisterDataPlaneFactory(name, factory)   // called from init()
LookupDataPlaneFactory(name)              // called at agent startup
```

### Implementations

| Data Plane | File | Build Tag | Mechanism | LB Strategy |
|---|---|---|---|---|
| `NftablesDataPlane` | network/nftables_dataplane.go:60 | (none) | `nft -f` atomic ruleset load | Verdict map dispatch + numgen round-robin |
| `IptablesDataPlane` | network/iptables_dataplane.go:49 | (none) | iptables nat-table chains | `-m statistic --mode nth` round-robin |
| `EbpfDataPlane` | network/ebpf_dataplane.go:83 | `ebpf` | XDP/TC BPF programs + BPF maps | BPF map lookup + hash |
| `ShadowDataPlane` | network/shadow_dataplane.go | (none) | Dual-write: primary + shadow, compare | Logs divergences between two providers |
| `SimulatorDataPlane` | network/simulator_dataplane.go | (none) | In-memory recording | No actual forwarding |

### Data Plane Selection

Default: `NftablesDataPlane`. Agent selects based on `--dataplane` flag or auto-detection.

`ShadowDataPlane` enables canary deployment of a new data plane by running it alongside
the production one and logging divergences.

### Key Types

| Type | File | Purpose |
|---|---|---|
| `ServiceVIPConfig` | network/dataplane.go:41 | VIP address + port + backends for one service |
| `DataPlaneBackend` | network/dataplane.go:67 | Address + Port of a real backend endpoint |
| `RouteDataPlaneConfig` | network/dataplane.go:80 | Route-level config with sticky session support |
| `RouteCookieConfig` | network/dataplane.go:130 | Cookie name, Secure, HttpOnly, SameSite |

### nftables Constants

| Constant | Value | Location |
|---|---|---|
| `nftablesTableName` | `"cca"` | network/nftables_dataplane.go:19 |
| `nftablesVIPDispatchMap` | `"vip_dispatch"` | network/nftables_dataplane.go:23 |
| `nftablesServiceChainPrefix` | `"svc_"` | network/nftables_dataplane.go:27 |
| `nftablesRouteChainPrefix` | `"rt_"` | network/nftables_dataplane.go:31 |
| `containerBridgeInterface` | `"cca0"` | network/nftables_dataplane.go:35 |
| `etcdClientPort` | `2379` | network/nftables_dataplane.go:40 |

### iptables Constants

| Constant | Value | Location |
|---|---|---|
| `iptablesMainChain` | `"CCA_SERVICES"` | network/iptables_dataplane.go:18 |
| `iptablesServiceChainPrefix` | `"CCA_SVC_"` | network/iptables_dataplane.go:22 |
| `iptablesRouteChainPrefix` | `"CCA_RT_"` | network/iptables_dataplane.go:37 |
| `vipDummyInterface` | `"cca-vip0"` | network/iptables_dataplane.go:26 |
| `maxJumpRuleRemovalAttempts` | `20` | network/iptables_dataplane.go:33 |

---

## 6. Controller Interface

### Interface Definition (controllers/controller.go:50)

```
Controller interface {
    Name() string                                              // Human-readable identifier
    Watch() []string                                           // Fact prefix subscriptions
    Reconcile(ctx, facts []store.Fact) ([]Change, error)       // Produce desired state changes
}
```

### Extended Controller Interfaces

| Interface | File | Purpose |
|---|---|---|
| `DeltaController` | controllers/controller.go:70 | Incremental reconciliation from watch events |
| `PostCommitController` | controllers/controller.go:88 | Execute side effects after successful CAS commit |
| `CustomController` | controllers/sdk.go:102 | SDK helper: controller from a `ReconcileFunc` closure |

### Controller Registration (cmd/cca/helpers.go:286)

#### Core Controllers (all nodes, always registered)

```
coreControllers():
  1.  instance           controllers/instance.go
  2.  scheduler          scheduler/scheduler.go
  3.  endpoint           controllers/endpoint.go
  4.  failure            controllers/failure.go
  5.  autoscale          controllers/autoscale.go
  6.  intent-resolver    controllers/intent.go
  7.  rollout            controllers/rollout.go
  8.  init               controllers/init.go
  9.  stateful-dns       controllers/stateful_dns.go
  10. stateful-volume    controllers/stateful_volume.go
  11. route              controllers/route.go
  12. eviction           controllers/eviction.go
  13. job                controllers/job.go
  14. cron               controllers/cronjob.go
  15. instance-gc        controllers/instance_gc.go
  16. audit-log-gc       controllers/audit_gc.go
```

#### Leader-Election Controllers (cmd/cca/command_server.go:370)

Added via `startControllersWithLeaderElection()`:

```
  17. node-failure       controllers/nodefailure.go
  18. drain              controllers/drain.go
  19. network            controllers/network.go
  20. network-policy     controllers/network_policy.go
  21. warm-zero          controllers/warmzero.go
```

#### Conditional Cloud Controllers (cmd/cca/command_server.go:384)

Only when `--cloud-provider` is set:

```
  22. cloud-node-lifecycle   controllers/cloud_node_lifecycle.go
  23. cloud-loadbalancer     controllers/cloud_loadbalancer.go
  24. cloud-routes           controllers/cloud_routes.go
```

#### Non-Controller Components (same runner)

```
  - EventProjector         controllers/event_projector.go
  - AuthController         controllers/auth.go
  - MetricsCollector       controllers/metrics.go
  - Bulkhead               controllers/bulkhead.go
```

### Runner Types

| Runner | File | Purpose |
|---|---|---|
| `Runner` | controllers/runner.go | Standard runner for `cca run` / single-node mode |
| `HARunner` | controllers/ha_runner.go | Leader-election-aware runner for multi-node clusters |

---

## 7. Build Tags

Build tags gate optional components to keep the default binary small:

| Tag | Effect | Files |
|---|---|---|
| `cloudprovider` | Includes AWS + GCP cloud providers, links AWS SDK + GCP SDK | cloud/aws.go, cloud/gcp.go |
| `ebpf` | Includes eBPF data plane, links BPF libraries | network/ebpf_*.go |
| (none) | Default build: containerd + nftables + iptables + simulator | All other files |

---

## 8. Configuration Flags

### Server (`cca server`)

| Flag | Purpose | Default |
|---|---|---|
| `--cloud-provider` | Cloud provider name (aws/gcp/azure/simulator) | (empty = no cloud) |
| `--cloud-region` | Cloud region | (required if cloud provider set) |
| `--vip-cidr` | VIP address pool CIDR | `10.200.0.0/24` |

### Agent (`cca agent`)

| Flag | Purpose | Default |
|---|---|---|
| `--runtime` | Runtime backend: containerd, container, process | `containerd` |
| `--dataplane` | Data plane provider: nftables, iptables, ebpf | (auto-detect) |
| `--cluster-cidr` | Container overlay CIDR | `10.100.0.0/16` |
| `--advertise-address` | Node address for VXLAN + DNS | (auto-detect) |

---

## 9. Extension Pattern Summary

All pluggable subsystems follow the same pattern:

```
1. Interface defined in a standalone file (cloud.go, dataplane.go, runtime.go, store.go)
2. Factory function type (CloudProviderFactory, DataPlaneFactory)
3. Package-level registry map (cloudProviderFactories, dataPlaneFactories)
4. Registration via init() in build-tagged files
5. Lookup at startup via CLI flag → LookupFactory(name)
6. Simulator implementation for testing (records calls, no real side effects)
```

This pattern ensures:
- Default builds are lightweight (no AWS/GCP/eBPF dependencies)
- Tests use simulators with full call recording
- New providers are added by implementing the interface + registering in init()
- No component hard-codes which provider is active
