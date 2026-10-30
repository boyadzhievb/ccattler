# CCattler Data Plane Call Graph

Machine-readable function-level index for the data plane reconciler and all
three providers (nftables, iptables, eBPF). Purpose: trace VIP fact → DNAT
rule flow without reading source files. Load this file instead.

For eBPF-specific BPF program details, see EBPF_GRAPH.md.

## Source File Index

| File | Line Count | Purpose |
|---|---|---|
| `agent/dataplane.go` | 524 | DataPlaneReconciler: reads facts, builds configs, calls provider |
| `network/dataplane.go` | 140 | DataPlaneProvider interface + extension interfaces |
| `network/dataplane_registry.go` | 31 | RegisterDataPlaneFactory / LookupDataPlaneFactory |
| `network/nftables_dataplane.go` | 554 | nftables provider: atomic table replace |
| `network/iptables_dataplane.go` | 605 | iptables provider: per-chain flush+rebuild |
| `network/ebpf_dataplane.go` | 840 | eBPF provider: BPF map operations (see EBPF_GRAPH) |
| `network/simulator_dataplane.go` | 121 | Test-only provider: records calls, no real rules |
| `network/shadow_dataplane.go` | 141 | Shadow mode: iptables active + nftables comparison |

## Interface Hierarchy

```
DataPlaneProvider                                    [network/dataplane.go:20]
├── ReconcileVIPDataPlane(ctx, []ServiceVIPConfig) error
├── ReconcileRouteDataPlane(ctx, []RouteDataPlaneConfig) error
└── Cleanup(ctx) error

DataPlaneInitializer                                 [network/dataplane.go:100]
└── Initialize(ctx) error                            [eBPF only]

DataPlaneNodeRegistrar                               [network/dataplane.go:106]
└── RegisterNode(ctx, nodeIP, podCIDR) error         [eBPF only]

DataPlaneBridgeRetrier                               [network/dataplane.go:112]
└── RetryBridgeAttachment(ctx, podCIDR) error        [eBPF only]

DataPlanePolicyReconciler                            [network/dataplane.go:125]
└── ReconcilePolicies(ctx) error                     [not yet implemented]

EndpointIdentityRegistrar                            [network/dataplane.go:118]
└── RegisterEndpointIdentity(ctx, ...) error         [not yet implemented]
```

## Provider Registry

```
network/dataplane_registry.go:15
  var dataPlaneFactories = map[string]DataPlaneFactory{}

RegisterDataPlaneFactory(name, factory)              [dataplane_registry.go:19]
└── panics on duplicate name

LookupDataPlaneFactory(name) → DataPlaneFactory      [dataplane_registry.go:28]
└── returns nil if not registered

Registered providers:
  "nftables" → NewNftablesDataPlane()                [built-in, no tag]
  "iptables" → NewIptablesDataPlane()                [built-in, no tag]
  "ebpf"     → NewEbpfDataPlane()                    [//go:build ebpf]
  "simulator" → NewSimulatorDataPlane()              [test code]
  "shadow"   → NewShadowDataPlane()                  [iptables active + nftables compare]
```

## DataPlaneReconciler (agent/dataplane.go)

### DataPlaneReconciler.Reconcile → entry point

```
DataPlaneReconciler.Reconcile(ctx)                   [agent/dataplane.go:55]
├── buildServiceVIPConfigs(ctx)                      [agent/dataplane.go:92]
│   └── → []ServiceVIPConfig (see below)
├── dataPlaneProvider.ReconcileVIPDataPlane(ctx, serviceVIPConfigs)
│   └── dispatches to nftables/iptables/eBPF provider
├── buildRouteDataPlaneConfigs(ctx, serviceVIPConfigs)
│                                                    [agent/dataplane.go:190]
│   └── → []RouteDataPlaneConfig (see below)
├── IF len(routeConfigs) > 0:
│   └── dataPlaneProvider.ReconcileRouteDataPlane(ctx, routeConfigs)
└── IF provider implements DataPlanePolicyReconciler:
    └── policyReconciler.ReconcilePolicies(ctx)
```

### buildServiceVIPConfigs → fact scan + backend resolution

```
buildServiceVIPConfigs(ctx)                          [agent/dataplane.go:92]
├── factStore.Scan(ScanNetworkVIPs)                  [derived/vip/]
│   └── parse: serviceName → VIP address
│   └── factStore.Get(KeyNetworkVIPServicePort(name))
│       └── parse: port number
├── identifyStaleNodes(ctx)                          [agent/dataplane.go:156]
│   ├── factStore.Scan(ScanLeaseNodes)               [lease/node/]
│   ├── skip self (nodeID == local)
│   └── IF now - heartbeat > 30s → staleNodeSet[nodeID] = true
├── sort serviceNames                                [deterministic order]
└── FOR EACH service:
    └── resolveServiceBackends(ctx, name, staleNodeSet)
                                                     [agent/dataplane.go:367]
        ├── factStore.Scan(PrefixEndpoint + "/service/" + name + "/")
        │   └── parse: instanceID, containerPort, endpointIP:port
        ├── FOR EACH endpoint:
        │   ├── factStore.Get(KeyObservedInstanceNode(id)) → nodeID
        │   ├── IF staleNodeSet[nodeID] → SKIP
        │   ├── IF nodeID == local:
        │   │   └── resolveLocalBackend(ctx, id, containerPort, ...)
        │   │                                        [agent/dataplane.go:449]
        │   │       ├── factStore.Get(KeyObservedInstanceIP(id))
        │   │       │   └── → container bridge IP
        │   │       └── → DataPlaneBackend{containerIP, containerPort}
        │   └── IF nodeID != local:
        │       └── resolveRemoteBackend(ctx, id, nodeID, containerPort)
        │                                            [agent/dataplane.go:472]
        │           ├── factStore.Get(KeyObservedInstanceIP(id))
        │           │   └── IF overlay IP available → use it (VXLAN routable)
        │           ├── factStore.Get(KeyObservedNodeAddress(nodeID))
        │           │   └── → node advertise address
        │           ├── resolveHostPort(ctx, factStore, id, containerPort)
        │           │                                [agent/dataplane.go:423]
        │           │   ├── factStore.Get(KeyObservedInstanceHostPortForPort(id, port))
        │           │   └── FALLBACK: factStore.Get(KeyObservedInstanceHostPort(id))
        │           └── → DataPlaneBackend{nodeIP, hostPort}
        └── → []DataPlaneBackend
```

### buildRouteDataPlaneConfigs → route fact parsing

```
buildRouteDataPlaneConfigs(ctx, serviceVIPConfigs)   [agent/dataplane.go:190]
├── factStore.Scan(ScanDesiredRoutes)                [desired/route/]
├── factStore.Scan(ScanDerivedRoutes)                [derived/route/]
├── build serviceVIPLookup map
├── parseRouteFactsForDataPlane(desiredFacts, derivedFacts)
│                                                    [agent/dataplane.go:290]
│   ├── FOR EACH desired fact:
│   │   ├── parse: routeName, fieldPath
│   │   └── switch fieldPath:
│   │       ├── "service" → entry.serviceName
│   │       ├── "port" → entry.servicePort
│   │       ├── "sticky" → entry.stickyMethod
│   │       ├── "cookie/name" → entry.cookieName
│   │       ├── "cookie/secure" → entry.cookieSecure
│   │       ├── "cookie/httponly" → entry.cookieHTTPOnly
│   │       └── "cookie/samesite" → entry.cookieSameSite
│   └── FOR EACH derived fact:
│       └── "backends" → split by comma → entry.backendAddresses
└── FOR EACH parsedRoute:
    └── buildRouteConfigFromEntry(parsedRoute, vipConfig)
                                                     [agent/dataplane.go:226]
        ├── resolve port (route port or VIP port fallback)
        ├── parseBackendAddressList(backendAddresses)
        │                                            [agent/dataplane.go:255]
        │   └── split "host:port" → DataPlaneBackend
        └── IF stickyMethod == "cookie":
            └── routeConfig.CookieConfig = {Name, Secure, HTTPOnly, SameSite}
```

### PublishAdvertiseAddress / publishInstanceHostPorts

```
PublishAdvertiseAddress(ctx)                          [agent/dataplane.go:500]
└── factStore.Put(KeyObservedNodeAddress(nodeID), advertiseAddress)

publishInstanceHostPorts(ctx, factStore, id, portMappings)
                                                     [agent/dataplane.go:512]
└── FOR EACH containerPort → hostPort:
    └── factStore.Put(KeyObservedInstanceHostPortForPort(id, port), hostPort)
```

## Provider 1: NftablesDataPlane (network/nftables_dataplane.go)

### ReconcileVIPDataPlane → atomic table replace

```
NftablesDataPlane.ReconcileVIPDataPlane(ctx, serviceConfigs)
                                                     [nftables_dataplane.go:92]
├── ensureDummyInterface()                           [nftables_dataplane.go:471]
│   ├── runCommandIdempotent("ip", "link", "add", "cca-vip0", "type", "dummy")
│   └── runCommandStrict("ip", "link", "set", "cca-vip0", "up")
├── buildRouteDispatchOverrides(lastRouteConfigs)    [nftables_dataplane.go:135]
│   └── FOR EACH route with StickyMethod + backends:
│       └── overrides[VIP:port] = "rt_" + sanitizedRouteName
├── buildVIPRuleset(configs, desiredChains, desiredVIPs, overrides)
│                                                    [nftables_dataplane.go:234]
│   ├── "table ip cca {}\n delete table ip cca\n table ip cca {"
│   ├── map vip_dispatch { type ipv4_addr . inet_proto . inet_service : verdict }
│   ├── chain prerouting { vmap @vip_dispatch }
│   ├── chain output { vmap @vip_dispatch }
│   ├── writePostRoutingChain(&builder)              [nftables_dataplane.go:318]
│   │   └── "ct status dnat masquerade"
│   ├── writeContainerIsolationChain(&builder)       [nftables_dataplane.go:305]
│   │   └── iifname "cca0" tcp dport {2379,2380} → log + drop
│   ├── FOR EACH service (sorted by name):
│   │   ├── dispatchEntry = "VIP . proto . port : goto svc_NAME"
│   │   │   └── OR "goto rt_NAME" if override exists
│   │   └── writeBackendDNATRules(&builder, backends, proto)
│   │                                                [nftables_dataplane.go:349]
│   │       ├── IF 1 backend → "dnat to addr:port"
│   │       └── IF N backends → "dnat to numgen inc mod N map { ... }"
│   └── writeVerdictMapWithElements(&builder, entries)
│                                                    [nftables_dataplane.go:327]
├── appendRouteChains(&ruleset, lastRouteConfigs, desiredRouteChains)
│                                                    [nftables_dataplane.go:150]
│   └── FOR EACH route:
│       ├── "source-ip" → writeSourceIPStickyChain   [nftables_dataplane.go:421]
│       │   └── "jhash ip saddr mod N map { ... }"
│       └── "cookie" → writeCookieProxyChain         [nftables_dataplane.go:446]
│           └── "redirect to :proxyPort"
├── applyNftablesRuleset(ruleset)                    [nftables_dataplane.go:508]
│   ├── os.CreateTemp() → write ruleset
│   └── exec.Command("nft", "-f", tempFile)          ← ATOMIC APPLY
├── FOR EACH service: ensureVIPAddress(VIP)          [nftables_dataplane.go:482]
│   └── runCommandIdempotent("ip", "addr", "add", VIP+"/32", "dev", "cca-vip0")
└── removeStaleVIPAddresses(desiredVIPs)             [nftables_dataplane.go:495]
    └── removeVIPAddressFromInterface(staleVIP)
```

### ReconcileRouteDataPlane

```
NftablesDataPlane.ReconcileRouteDataPlane(ctx, routeConfigs)
                                                     [nftables_dataplane.go:198]
├── cookieProxy.ReconcileRoutes(routeConfigs)
└── lastRouteConfigs = routeConfigs                  [stored for next VIP cycle]
```

### Cleanup

```
NftablesDataPlane.Cleanup(ctx)                       [nftables_dataplane.go:210]
├── runNft("delete", "table", "ip", "cca")
├── FOR EACH activeVIPAddress: removeVIPAddressFromInterface
├── deleteDummyInterface()                           [iptables_dataplane.go:535]
│   └── exec.Command("ip", "link", "del", "cca-vip0")
└── cookieProxy.Cleanup()
```

## Provider 2: IptablesDataPlane (network/iptables_dataplane.go)

### ReconcileVIPDataPlane → per-chain rebuild

```
IptablesDataPlane.ReconcileVIPDataPlane(ctx, serviceConfigs)
                                                     [iptables_dataplane.go:80]
├── ensureGlobalChainAndJumpRules()                  [iptables_dataplane.go:297]
│   ├── iptables -t nat -N CCA_SERVICES              [create if not exists]
│   ├── ensureJumpToChain("PREROUTING", "CCA_SERVICES")
│   │                                                [iptables_dataplane.go:469]
│   │   ├── iptables -t nat -C PREROUTING -j CCA_SERVICES [check]
│   │   └── iptables -t nat -I PREROUTING 1 -j CCA_SERVICES [insert if absent]
│   ├── ensureJumpToChain("OUTPUT", "CCA_SERVICES")
│   ├── ensureMasqueradeForDNAT()                    [iptables_dataplane.go:446]
│   │   └── iptables -t nat -A POSTROUTING ! -o lo -m conntrack --ctstate DNAT -j MASQUERADE
│   └── ensureContainerIsolationRules()              [iptables_dataplane.go:326]
│       ├── iptables -I INPUT (LOG, rate 1/min)
│       └── iptables -A INPUT -i cca0 --dports 2379,2380 -j DROP
├── ensureDummyInterface()                           [iptables_dataplane.go:356]
│   ├── runCommandIdempotent("ip", "link", "add", "cca-vip0", "type", "dummy")
│   └── runCommandStrict("ip", "link", "set", "cca-vip0", "up")
├── FOR EACH service:
│   ├── ensureVIPAddress(VIP)                        [iptables_dataplane.go:369]
│   │   └── runCommandIdempotent("ip", "addr", "add", VIP+"/32", "dev", "cca-vip0")
│   └── reconcileServiceChain(config, chainName)     [iptables_dataplane.go:380]
│       ├── iptables -t nat -N CCA_SVC_<name>        [create if not exists]
│       ├── iptables -t nat -F CCA_SVC_<name>        [flush = rebuild]
│       ├── FOR EACH backend (except last):
│       │   └── iptables -t nat -A CCA_SVC_<name> -p tcp
│       │       -m statistic --mode nth --every <remaining> --packet 0
│       │       -j DNAT --to-destination <addr:port>
│       ├── last backend:
│       │   └── iptables -t nat -A CCA_SVC_<name> -p tcp
│       │       -j DNAT --to-destination <addr:port>
│       └── ensureJumpFromMainToService(VIP, port, proto, chain)
│                                                    [iptables_dataplane.go:480]
│           ├── iptables -t nat -C CCA_SERVICES -d VIP/32 -p tcp --dport PORT -j chain
│           └── iptables -t nat -A CCA_SERVICES ... [if absent]
├── removeStaleServiceChains(desiredChains)          [iptables_dataplane.go:418]
│   └── FOR EACH stale chain:
│       ├── removeJumpRuleFromMainChain(chain)       [iptables_dataplane.go:495]
│       │   └── up to 20 passes: iptables -S CCA_SERVICES → iptables -D
│       └── flushAndDeleteChain(chain)               [iptables_dataplane.go:524]
│           └── iptables -t nat -F chain; iptables -t nat -X chain
└── removeStaleVIPAddresses(desiredVIPs)             [iptables_dataplane.go:432]
    └── removeVIPAddressFromInterface(staleVIP)
```

### ReconcileRouteDataPlane

```
IptablesDataPlane.ReconcileRouteDataPlane(ctx, routeConfigs)
                                                     [iptables_dataplane.go:124]
├── ensureGlobalChainAndJumpRules()
├── FOR EACH route:
│   ├── "source-ip" → reconcileSourceIPStickyChain(config, chain)
│   │                                                [iptables_dataplane.go:166]
│   │   ├── iptables -t nat -N CCA_RT_<name>
│   │   ├── iptables -t nat -F CCA_RT_<name>
│   │   ├── FOR EACH backend: --rcheck rules (xt_recent)
│   │   ├── FOR EACH backend: --set + statistic rules
│   │   └── ensureJumpFromMainToService
│   └── "cookie" → reconcileCookieStickyChain(config, chain)
│                                                    [iptables_dataplane.go:221]
│       ├── iptables -t nat -N CCA_RT_<name>
│       ├── iptables -t nat -F CCA_RT_<name>
│       ├── iptables -t nat -A CCA_RT_<name> -j DNAT --to 127.0.0.1:proxyPort
│       └── ensureJumpFromMainToService
├── cookieProxy.ReconcileRoutes(routeConfigs)
└── removeStaleRouteChains(desiredChains)            [iptables_dataplane.go:249]
```

### Cleanup

```
IptablesDataPlane.Cleanup(ctx)                       [iptables_dataplane.go:262]
├── cookieProxy.Cleanup()
├── FOR EACH activeRouteChain: remove jump + flush + delete
├── FOR EACH activeServiceChain: remove jump + flush + delete
├── removeJumpFromBuiltinChain("PREROUTING", "CCA_SERVICES")
├── removeJumpFromBuiltinChain("OUTPUT", "CCA_SERVICES")
├── flushAndDeleteChain("CCA_SERVICES")
├── removeMasqueradeForDNAT()                        [iptables_dataplane.go:461]
├── FOR EACH activeVIPAddress: removeVIPAddressFromInterface
└── deleteDummyInterface()
```

## Provider 3: SimulatorDataPlane (network/simulator_dataplane.go)

```
SimulatorDataPlane.ReconcileVIPDataPlane(ctx, configs)
                                                     [simulator_dataplane.go:40]
├── copy(configsCopy, configs)
├── lastReconciled = configsCopy
└── reconcileCallCount++

SimulatorDataPlane.ReconcileRouteDataPlane(ctx, configs)
                                                     [simulator_dataplane.go:53]
├── copy(configsCopy, configs)
├── lastReconciledRoutes = configsCopy
└── routeReconcileCallCount++

Test accessors:
  LastReconciled() → []ServiceVIPConfig              [simulator_dataplane.go:75]
  LastReconciledRoutes() → []RouteDataPlaneConfig    [simulator_dataplane.go:96]
  ReconcileCallCount() → int                         [simulator_dataplane.go:88]
  RouteReconcileCallCount() → int                    [simulator_dataplane.go:109]
  IsCleanedUp() → bool                              [simulator_dataplane.go:116]
```

## Provider 4: ShadowDataPlane (network/shadow_dataplane.go)

```
ShadowDataPlane.ReconcileVIPDataPlane(ctx, configs)  [shadow_dataplane.go:50]
├── activeProvider.ReconcileVIPDataPlane(ctx, configs) [iptables — real]
├── totalReconciliations++
└── compareVIPRulesets(configs)                       [shadow_dataplane.go:64]
    ├── shadowProvider.GenerateVIPRuleset(configs)    [nftables — shadow]
    └── compareShadowServices(configs, ruleset)       [shadow_dataplane.go:89]
        └── check each service has chain + correct numgen mod N
```

## Store Key Patterns (what the reconciler reads)

### Reads (DataPlaneReconciler)

| Key Pattern | Where Read |
|---|---|
| `derived/vip/{service}` | `buildServiceVIPConfigs` (VIP address) |
| `derived/vip/{service}/port` | `buildServiceVIPConfigs` (VIP port) |
| `derived/endpoint/service/{service}/{instance}/{port}` | `resolveServiceBackends` (endpoint IP:port) |
| `observed/instance/{id}/node` | `resolveServiceBackends` (which node) |
| `observed/instance/{id}/ip` | `resolveLocalBackend`, `resolveRemoteBackend` (container IP) |
| `observed/instance/{id}/host_port/{port}` | `resolveHostPort` (per-port mapping) |
| `observed/instance/{id}/host_port` | `resolveHostPort` (legacy fallback) |
| `observed/node/{nodeID}/address` | `resolveRemoteBackend` (node advertise IP) |
| `lease/node/{nodeID}` | `identifyStaleNodes` (heartbeat timestamp) |
| `desired/route/{name}/*` | `parseRouteFactsForDataPlane` (route definitions) |
| `derived/route/{name}/backends` | `parseRouteFactsForDataPlane` (route backends) |

### Writes (DataPlaneReconciler)

| Key Pattern | Where Written |
|---|---|
| `observed/node/{nodeID}/address` | `PublishAdvertiseAddress` (node LAN IP) |
| `observed/instance/{id}/host_port/{port}` | `publishInstanceHostPorts` (port mapping) |

## Store Key Data Flow (controller/user writes → reconciler reads)

| Key Pattern | Written By | Read By |
|---|---|---|
| `derived/vip/{service}` | **VIPController**.Reconcile | `buildServiceVIPConfigs` |
| `derived/vip/{service}/port` | **VIPController**.Reconcile | `buildServiceVIPConfigs` |
| `derived/endpoint/service/{svc}/{id}/{port}` | **EndpointController**.Reconcile | `resolveServiceBackends` |
| `observed/instance/{id}/node` | Agent `publishInstanceStateToStore` | `resolveServiceBackends` |
| `observed/instance/{id}/ip` | Agent `publishInstanceStateToStore` | `resolveLocalBackend`, `resolveRemoteBackend` |
| `observed/node/{nodeID}/address` | Agent `PublishAdvertiseAddress` | `resolveRemoteBackend` (other nodes) |
| `lease/node/{nodeID}` | Agent heartbeat loop | `identifyStaleNodes` |
| `desired/route/{name}/*` | User via `cca apply` | `parseRouteFactsForDataPlane` |
| `derived/route/{name}/backends` | **RouteController**.Reconcile | `parseRouteFactsForDataPlane` |

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `DataPlaneReconciler.Reconcile` | Agent `dataPlaneLoop` (every 1s) |
| `NewDataPlaneReconciler` | Agent startup [agent/agent.go] |
| `buildServiceVIPConfigs` | `Reconcile` |
| `identifyStaleNodes` | `buildServiceVIPConfigs` |
| `resolveServiceBackends` | `buildServiceVIPConfigs` (for each service) |
| `resolveLocalBackend` | `resolveServiceBackends` (if nodeID == local) |
| `resolveRemoteBackend` | `resolveServiceBackends` (if nodeID != local) |
| `resolveHostPort` | `resolveRemoteBackend` |
| `buildRouteDataPlaneConfigs` | `Reconcile` |
| `parseRouteFactsForDataPlane` | `buildRouteDataPlaneConfigs` |
| `buildRouteConfigFromEntry` | `buildRouteDataPlaneConfigs` (for each route) |
| `parseBackendAddressList` | `buildRouteConfigFromEntry` |
| `PublishAdvertiseAddress` | Agent startup [agent/agent.go] |
| `publishInstanceHostPorts` | Agent reconcile (after container start) |
| `RegisterDataPlaneFactory` | `init()` in each provider's registration file |
| `LookupDataPlaneFactory` | Agent startup (select provider) |
| `NewNftablesDataPlane` | Factory via `RegisterDataPlaneFactory` |
| `NewIptablesDataPlane` | Factory via `RegisterDataPlaneFactory` |
| `NewEbpfDataPlane` | Factory via `RegisterDataPlaneFactory` [ebpf_register.go:15] |
| `NewSimulatorDataPlane` | Test setup |
| `NewShadowDataPlane` | Factory via `RegisterDataPlaneFactory` |
| `buildVIPRuleset` | `NftablesDataPlane.ReconcileVIPDataPlane` |
| `buildRouteDispatchOverrides` | `NftablesDataPlane.ReconcileVIPDataPlane` |
| `appendRouteChains` | `NftablesDataPlane.ReconcileVIPDataPlane` |
| `applyNftablesRuleset` | `NftablesDataPlane.ReconcileVIPDataPlane` |
| `writeBackendDNATRules` | `buildVIPRuleset` (for each service) |
| `writePostRoutingChain` | `buildVIPRuleset` |
| `writeContainerIsolationChain` | `buildVIPRuleset` |
| `writeVerdictMapWithElements` | `buildVIPRuleset` |
| `writeSourceIPStickyChain` | `appendRouteChains`, `buildRouteRuleset` |
| `writeCookieProxyChain` | `appendRouteChains`, `buildRouteRuleset` |
| `sanitizeNftablesChainName` | `buildVIPRuleset`, `appendRouteChains` |
| `CheckNftablesAvailable` | Agent startup (provider selection) |
| `ensureGlobalChainAndJumpRules` | `IptablesDataPlane.ReconcileVIPDataPlane`, `ReconcileRouteDataPlane` |
| `ensureJumpToChain` | `ensureGlobalChainAndJumpRules` |
| `ensureMasqueradeForDNAT` | `ensureGlobalChainAndJumpRules` |
| `ensureContainerIsolationRules` | `ensureGlobalChainAndJumpRules` |
| `reconcileServiceChain` | `IptablesDataPlane.ReconcileVIPDataPlane` (for each service) |
| `ensureJumpFromMainToService` | `reconcileServiceChain`, `reconcileSourceIPStickyChain`, `reconcileCookieStickyChain` |
| `reconcileSourceIPStickyChain` | `IptablesDataPlane.ReconcileRouteDataPlane` |
| `reconcileCookieStickyChain` | `IptablesDataPlane.ReconcileRouteDataPlane` |
| `removeStaleServiceChains` | `IptablesDataPlane.ReconcileVIPDataPlane` |
| `removeStaleRouteChains` | `IptablesDataPlane.ReconcileRouteDataPlane` |
| `removeStaleVIPAddresses` | `ReconcileVIPDataPlane` (both nftables and iptables) |
| `removeJumpRuleFromMainChain` | `removeStaleServiceChains`, `removeStaleRouteChains`, `Cleanup` |
| `flushAndDeleteChain` | `removeStaleServiceChains`, `removeStaleRouteChains`, `Cleanup` |
| `removeVIPAddressFromInterface` | `removeStaleVIPAddresses`, `Cleanup` |
| `deleteDummyInterface` | `Cleanup` (nftables, iptables) |
| `sanitizeChainName` | `reconcileServiceChain`, route chain creation (iptables) |
| `compareShadowServices` | `compareVIPRulesets` (shadow mode) |

## Provider Comparison

| Aspect | nftables | iptables | eBPF |
|---|---|---|---|
| VIP lookup | O(1) verdict map | O(N) chain walk | O(1) hash map |
| Backend selection | `numgen inc mod N` | `statistic --mode nth` | `bpf_get_prandom_u32() % N` |
| Atomicity | Full table replace (`nft -f`) | Per-chain flush+rebuild | Per-entry map put/delete |
| Session affinity | `jhash ip saddr` | `xt_recent` module | `cca_affinity` LRU map |
| SNAT | `ct status dnat masquerade` | `--ctstate DNAT -j MASQUERADE` | `cca_tc_snat` on NIC egress |
| Hook point | netfilter (prerouting, output, postrouting) | same | cgroup + tc |
| Kernel requirement | nftables 5.x+ | iptables 2.6+ | 6.6+ (TCX, ct kfuncs) |
| Cleanup | `delete table ip cca` | flush+delete each chain | detach programs, unpin maps |
| Build tag | (default) | (default) | `-tags ebpf` |

## Constants Reference

| Constant | Value | File:Line | Used By |
|---|---|---|---|
| `defaultNodeStalenessThreshold` | `30s` | `agent/dataplane.go:24` | `identifyStaleNodes` |
| `nftablesTableName` | `"cca"` | `nftables_dataplane.go:19` | all nftables functions |
| `nftablesVIPDispatchMap` | `"vip_dispatch"` | `nftables_dataplane.go:23` | `buildVIPRuleset` |
| `nftablesServiceChainPrefix` | `"svc_"` | `nftables_dataplane.go:27` | `buildVIPRuleset` |
| `nftablesRouteChainPrefix` | `"rt_"` | `nftables_dataplane.go:31` | `appendRouteChains` |
| `containerBridgeInterface` | `"cca0"` | `nftables_dataplane.go:36` | `writeContainerIsolationChain` |
| `etcdClientPort` | `2379` | `nftables_dataplane.go:39` | container isolation |
| `etcdPeerPort` | `2380` | `nftables_dataplane.go:42` | container isolation |
| `iptablesMainChain` | `"CCA_SERVICES"` | `iptables_dataplane.go:18` | all iptables VIP functions |
| `iptablesServiceChainPrefix` | `"CCA_SVC_"` | `iptables_dataplane.go:22` | `reconcileServiceChain` |
| `iptablesRouteChainPrefix` | `"CCA_RT_"` | `iptables_dataplane.go:36` | route chain creation |
| `vipDummyInterface` | `"cca-vip0"` | `iptables_dataplane.go:26` | VIP address management |
| `maxJumpRuleRemovalAttempts` | `20` | `iptables_dataplane.go:32` | `removeJumpRuleFromMainChain` |
| `shadowDataPlaneMaxLoggedDivergences` | `10` | `shadow_dataplane.go:19` | `compareVIPRulesets` |
