# CCattler Network Function Call Graph

Machine-readable function-level index for the networking subsystem. Purpose:
trace exact call chains, store key patterns, and data flows without reading
source files. Load this file instead.

---

## 1. NetworkController (controllers/network.go)

### NetworkController.Reconcile :88
```
NetworkController.Reconcile(ctx, facts)
├── collectServicesWithEndpointsFromFacts(facts)               :151
│   └── store.FactsWithPrefix(facts, ScanEndpoints)
│       └── returns map[serviceName]bool
├── extractServiceExposedPorts(facts)                          [shared helper]
│   └── returns map[serviceName][]int
├── collectDefinedServiceNamesFromFacts(facts)                 :210
│   └── store.FactsWithPrefix(facts, ScanDesiredServices)
│       └── returns map[serviceName]bool
├── collectExistingVIPAllocations(facts)                       :165
│   └── store.FactsWithPrefix(facts, ScanNetworkVIPs)
│       └── returns (existingVIPs map[svc]vipAddr, existingVIPPorts map[svc]port)
├── collectExistingDNSMappings(facts)                          :182
│   └── store.FactsWithPrefix(facts, ScanNetworkDNS)
│       └── returns map[serviceName]vipAddr
├── determineNextVIPFourthOctet(existingVIPs)                  :193
│   └── scans existing VIP addresses → returns next available fourth octet
│
├── FOR EACH service with endpoints (sorted):
│   ├── skip if no exposed ports
│   ├── IF no existing VIP:
│   │   └── allocate VIP → groupedChanges("vip-create/{svc}"):
│   │       ├── PUT network/vip/service/{name}       = "{vipAddress}"
│   │       ├── PUT network/vip/service/{name}/port  = "{port}"
│   │       └── PUT network/dns/{name}               = "{vipAddress}"
│   └── ELSE: skip (VIP persists through zero-endpoint windows)
│
└── buildStaleVIPAndDNSRemovalChanges(existingVIPs, existingDNS, definedServiceNames)  :226
    ├── FOR EACH existing VIP (sorted):
    │   └── IF service definition deleted:
    │       └── groupedChanges("vip-remove/{svc}"):
    │           ├── DELETE network/vip/service/{name}
    │           ├── DELETE network/vip/service/{name}/port
    │           └── DELETE network/dns/{name}
    └── FOR EACH existing DNS (sorted):
        └── IF service definition deleted AND no VIP:
            └── DELETE network/dns/{name}
```

### Store keys (NetworkController)

| Key Pattern | Op | Purpose |
|---|---|---|
| `network/vip/service/{name}` | Write | VIP address for service |
| `network/vip/service/{name}/port` | Write | Exposed port for VIP |
| `network/dns/{name}` | Write | DNS name → VIP mapping |
| `endpoint/service/{name}/*` | Read | Detect services with endpoints |
| `desired/service/{name}/*` | Read | Detect defined services, exposed ports |

---

## 2. EndpointController (controllers/endpoint.go)

### EndpointController.Reconcile :54
```
EndpointController.Reconcile(ctx, facts)
├── parseInstanceFieldsFromFacts(facts)                        [shared helper]
│   └── returns map[instanceID]map[field]value
├── extractServiceExposedPorts(facts)                          [shared helper]
├── detectServicesWithReadinessProbe(facts)                    :71
│   └── reads desired/service/{name}/probe/readiness/method
│       └── returns map[serviceName]bool
├── parseNodeAdvertiseAddresses(facts)                         :85
│   └── collectStringValuesBySuffix(facts, ScanObservedNodes, "address")
│       └── returns map[nodeID]advertiseAddress
├── parseExistingEndpoints(facts)                              :92
│   └── store.FactsWithPrefix(facts, ScanEndpoints)
│       └── returns map["{svc}/{id}/{port}"]value
│
├── buildDesiredEndpoints(instanceFields, servicePorts,        :108
│   serviceHasReadinessProbe, nodeAddresses)
│   ├── FOR EACH instance (sorted by ID):
│   │   ├── skip if state != running
│   │   ├── skip if no overlay IP AND no host port+node address
│   │   ├── skip if no exposed ports for service
│   │   ├── skip if drain_readiness = not_ready
│   │   ├── skip if service has readiness probe AND probe != ready
│   │   └── FOR EACH exposed port:
│   │       ├── IF hasOverlayAddress:
│   │       │   └── endpoint = "{containerIP}:{containerPort}"
│   │       └── ELSE (host port):
│   │           └── endpoint = "{nodeAddress}:{hostPort}"
│   └── returns map["{svc}/{id}/{port}"]endpoint
│
└── computeEndpointDiffChanges(desired, existing)              :174
    ├── PUT endpoint/service/{svc}/{id}/{port} for new/changed
    └── DELETE endpoint/service/{svc}/{id}/{port} for stale
```

### Store keys (EndpointController)

| Key Pattern | Op | Purpose |
|---|---|---|
| `endpoint/service/{svc}/{id}/{port}` | Write | Backend endpoint address |
| `observed/instance/{id}/state` | Read | Instance must be running |
| `observed/instance/{id}/ip` | Read | Container overlay IP |
| `observed/instance/{id}/node` | Read | Node hosting instance |
| `observed/instance/{id}/hostport/{port}` | Read | Per-port host mapping |
| `observed/node/{nodeID}/address` | Read | Node advertise address |
| `desired/service/{name}/ports` | Read | Exposed ports |
| `desired/service/{name}/probe/readiness/method` | Read | Readiness probe config |
| `derived/instance/{id}/drain/readiness` | Read | Drain readiness state |

---

## 3. NetworkPolicyController (controllers/network_policy.go)

### NetworkPolicyController.Reconcile :78
```
NetworkPolicyController.Reconcile(ctx, facts)
├── parseIdentityPolicyRulesFromFacts(facts)                   :95
│   └── FOR EACH policy/network/{name} fact:
│       └── parseIdentityPolicyRuleValue(name, value)          :110
│           └── splits "source:target:port:action"
│           └── returns identityPolicyRule{name, sourceService, targetService, port, action}
│
├── buildServiceEndpointMapFromFacts(facts)                    :131
│   └── FOR EACH endpoint/service/{svc}/{id}/{port} fact:
│       └── parseEndpointFact(fact)                            :153
│           ├── extracts serviceName, instanceID
│           └── extractIPFromEndpointValue(value)              :167
│       └── returns map[serviceName][]serviceEndpointEntry{instanceID, ipAddress}
│
├── buildInstanceNodeMapFromPlacements(facts)                  :177
│   └── FOR EACH placement/instance/{id} fact:
│       └── returns map[instanceID]nodeID
│
├── compileIdentityRulesToIPRulesPerNode(rules, endpoints, nodeMap)  :190
│   ├── FOR EACH identity rule:
│   │   ├── resolveServiceToIPAddresses(sourceService, endpoints)    :210
│   │   │   └── returns []string of IPs (or ["*"] for wildcard)
│   │   ├── resolveServiceToTargetNodeEntries(targetService, endpoints, nodeMap)  :227
│   │   │   └── joins endpoints with placements → []targetNodeEntry{ipAddress, nodeID}
│   │   └── appendCompiledRulesForPolicyRule(rulesPerNode, sourceIPs, targets, rule)  :248
│   │       └── FOR EACH target × source:
│   │           └── formatCompiledFirewallRule → "sourceIP:targetIP:port:action"
│   │           └── appended to rulesPerNode[target.nodeID]
│   └── sortCompiledRulesPerNode(rulesPerNode)                 :272
│
├── collectExistingDerivedNetworkRules(facts)                  :280
│   └── parseDerivedNetworkRuleKey(factKey)                    :297
│       └── returns map[nodeID]map[ruleIndex]value
│
└── diffDerivedNetworkRules(desired, existing)                 :312
    ├── diffDesiredNodeRules(desired, existing)                :324
    │   ├── PUT derived/network/rule/{nodeID}/{index} for new/changed
    │   └── removeExcessRulesForNode(nodeID, count, existing)  :349
    │       └── DELETE derived/network/rule/{nodeID}/{index} for excess
    └── removeStaleNodeRules(desired, existing)                :366
        └── DELETE all rules for nodes not in desired set
```

### Store keys (NetworkPolicyController)

| Key Pattern | Op | Purpose |
|---|---|---|
| `derived/network/rule/{nodeID}/{index}` | Write | Compiled IP-based firewall rule |
| `policy/network/{name}` | Read | Identity-based policy rules |
| `endpoint/service/{svc}/{id}/{port}` | Read | Service → IP resolution |
| `placement/instance/{id}` | Read | Instance → node mapping |

---

## 4. DNS Server (network/dns.go)

### DNSServer.Start :56
```
DNSServer.Start(ctx)                                           [blocks, goroutine per query]
├── net.ResolveUDPAddr("udp", listenAddress)
├── net.ListenUDP("udp", udpAddress)
├── goroutine: <-ctx.Done() → close connection
└── loop: ReadFromUDP → go handleDNSQuery

DNSServer.handleDNSQuery(ctx, conn, addr, packet)             :104
├── parseDNSQuestionName(packet, 12)                           :156
│   └── extracts dotted domain name from DNS wire format
├── strip ".{domain}" suffix → serviceName
├── IF not in domain:
│   └── sendNXDomainResponse(conn, addr, txid, question)       :141
├── resolver.ResolveVIP(ctx, serviceName)
│   └── StoreBackedResolver.ResolveVIP                         [see §6]
├── IF VIP not found:
│   └── sendNXDomainResponse
└── buildDNSARecordResponse(txid, question, ipv4)              :179
    └── constructs A record: name pointer, type A, TTL=30s, 4-byte IP
    └── WriteToUDP(response, addr)
```

---

## 5. DNS Forwarder (network/dns_forwarder.go)

### DNSForwarder.Start :92
```
DNSForwarder.Start(ctx)                                        [blocks, goroutine per query]
├── net.ListenUDP("udp", listenAddress)
└── loop: ReadFromUDP → go handleForwardedQuery

DNSForwarder.handleForwardedQuery(ctx, conn, addr, packet)     :147
├── parseDNSQuestionName(packet, 12)
├── cache.get(queryName)                                       :272
│   └── IF hit: patch txid → WriteToUDP → return
├── determine upstream:
│   ├── IF *.cca.local → ccaUpstream (control plane :15353)
│   └── ELSE → systemUpstream (host DNS)
├── forwardToUpstream(ctx, upstream, packet)                    :193
│   └── DialUDP → Write → Read → return response
├── extractTTLFromDNSResponse(response)                        :225
│   └── parse answer section → TTL capped at dnsCacheMaxTTL (30s)
├── cache.put(queryName, response, ttl)                        :285
│   └── evicts expired entries when cache >= 1000 entries
└── patch txid → WriteToUDP(response, addr)

ParseSystemResolvers(resolvConfPath, excludeAddress)           :316
└── reads /etc/resolv.conf → first nameserver != excludeAddress
```

### Constants (DNS)

| Constant | Value | File | Purpose |
|---|---|---|---|
| `DefaultDNSDomain` | `"cca.local"` | `network/dns.go:18` | Service discovery DNS suffix |
| `dnsReadBufferSize` | `512` | `network/dns.go:23` | UDP read buffer (RFC 1035) |
| `dnsCacheMaxTTL` | `30s` | `network/dns_forwarder.go:22` | Max cache TTL |
| `dnsForwarderReadTimeout` | `3s` | `network/dns_forwarder.go:26` | Upstream read deadline |
| `dnsForwarderMaxCacheEntries` | `1000` | `network/dns_forwarder.go:30` | Cache size limit |

---

## 6. StoreBackedResolver (network/resolver.go)

### StoreBackedResolver.ResolveVIP :80
```
StoreBackedResolver.ResolveVIP(ctx, serviceName)
└── factStore.Get(ctx, KeyNetworkVIPService(serviceName))
    └── key: network/vip/service/{name}
    └── returns VIP address string
```

### StoreBackedResolver.ResolveEndpoints :46
```
StoreBackedResolver.ResolveEndpoints(ctx, serviceName)
├── factStore.Scan(ctx, "endpoint/service/{name}/")
└── FOR EACH endpoint fact:
    └── parse "ip:port" → types.Endpoint{Service, InstanceID, IP, Port}
```

---

## 7. VXLAN Overlay (agent/vxlan.go)

### VXLANReconciler.Reconcile :71
```
VXLANReconciler.Reconcile(ctx)                                 [called every 1s from dataPlaneLoop]
├── IF not initialized:
│   └── ensureVXLANInterfaceExists(ctx)                        :160
│       ├── interfaceAlreadyExists(ctx, "cca-vxlan0")          :476
│       ├── detectPhysicalInterfaceForAddress(advertiseAddress) :484
│       │   └── scans net.Interfaces() → finds NIC with agent's IP
│       ├── executeVXLANCreate(ctx, physicalInterface, localAddress)  :533
│       │   └── ip link add cca-vxlan0 type vxlan id 42 dstport 4789 dev {nic} local {ip}
│       ├── deriveVXLANMACAddress(nodeID)                      :510
│       │   └── FNV32a hash → 02:{4 bytes}:{xor byte}
│       ├── executeIPLinkSetAddress(ctx, "cca-vxlan0", mac)    :523
│       ├── executeIPLinkSetUp(ctx, "cca-vxlan0")              :548
│       ├── executeIPLinkSetMTU(ctx, "cca-vxlan0", 1450)       :557
│       └── disableReversePathFilter(ctx, "cca-vxlan0")        :640
│           └── sysctl net.ipv4.conf.{iface}.rp_filter=0 AND net.ipv4.conf.all.rp_filter=0
│
├── disableReversePathFilter(ctx, "cca-vxlan0")                [re-apply every pass]
│
├── discoverPeerNodes(ctx)                                     :211
│   ├── factStore.Scan(ctx, ScanObservedNodes)
│   │   └── buildNodeAddressMap(facts)                         :251
│   │       └── map[nodeID]advertiseAddress from observed/node/{id}/address
│   ├── factStore.Scan(ctx, ScanNetworkNodeSubnets)
│   │   └── buildNodeSubnetMap(facts)                          :265
│   │       └── map[nodeID]subnetCIDR from network/node/{id}/subnet
│   └── exclude self → sort → return []peerNodeInfo
│
├── programPeerRoutes(ctx, peerNodes)                          :283
│   └── FOR EACH peer:
│       ├── deriveVXLANMACAddress(peer.nodeID)
│       ├── executeIPNeighReplace(ctx, gateway, peerMAC)       :592
│       │   └── ip neigh replace {subnet.0} dev cca-vxlan0 lladdr {mac} nud permanent
│       ├── executeIPRouteReplaceOnlink(ctx, subnet, gateway)  :579
│       │   └── ip route replace {subnet} via {gateway} dev cca-vxlan0 onlink
│       ├── executeBridgeFDBAppend(ctx, peerAddress)           :655
│       │   └── bridge fdb append 00:00:00:00:00:00 dev cca-vxlan0 dst {peerIP}
│       └── executeBridgeFDBReplaceUnicast(ctx, peerMAC, peerAddress)  :671
│           └── bridge fdb replace {mac} dev cca-vxlan0 dst {peerIP}
│
└── removeStaleRoutes(ctx, peerNodes)                          :329
    ├── listVXLANRouteSubnets(ctx)                             :694
    │   └── ip route show dev cca-vxlan0 → []subnet
    └── FOR EACH installed subnet not in active peers:
        ├── executeIPRouteDelete(ctx, subnet)                  :624
        └── executeIPNeighDelete(ctx, subnetBaseAddress)       :601
```

### VXLANReconciler.EnsureSubnetPublished :131
```
VXLANReconciler.EnsureSubnetPublished(ctx)
├── factStore.Get(ctx, KeyNetworkNodeSubnet(nodeID))
│   └── IF exists: done
└── allocateNextFreeSubnet(ctx)                                :367
    ├── parseClusterCIDRBaseOctets(clusterCIDR)                :414
    └── RETRY up to 10 times:
        ├── factStore.ScanWithRevision(ctx, ScanNetworkNodeSubnets)
        ├── collectOccupiedThirdOctets(facts)                  :432
        ├── findNextFreeThirdOctetForNode(occupied, nodeID)    :452
        │   └── hashNodeIDToOctetOffset(nodeID, range)         :467
        │       └── FNV32a(nodeID) mod range → deterministic start
        └── factStore.Transaction(CAS: revision=0)
            └── PUT network/node/{nodeID}/subnet = "{cidr}"
```

### Store keys (VXLAN)

| Key Pattern | Op | Purpose |
|---|---|---|
| `network/node/{nodeID}/subnet` | Write/Read | Node's /24 container subnet |
| `observed/node/{nodeID}/address` | Read | Peer node advertise address |

### Constants (VXLAN)

| Constant | Value | File | Purpose |
|---|---|---|---|
| `vxlanInterfaceName` | `"cca-vxlan0"` | `agent/vxlan.go:21` | Tunnel interface name |
| `vxlanVNI` | `42` | `agent/vxlan.go:24` | VXLAN Network Identifier |
| `vxlanDestinationPort` | `4789` | `agent/vxlan.go:27` | UDP encapsulation port |
| `vxlanBridgeMTU` | `1450` | `agent/vxlan.go:31` | MTU (1500 - 50 overhead) |
| `maxSubnetAllocationAttempts` | `10` | `agent/vxlan.go:35` | CAS retry limit |

---

## 8. DataPlaneProvider Interface (network/dataplane.go)

### Interface hierarchy
```
DataPlaneProvider                                              :20
├── ReconcileVIPDataPlane(ctx, []ServiceVIPConfig) error        :25
├── ReconcileRouteDataPlane(ctx, []RouteDataPlaneConfig) error  :30
└── Cleanup(ctx) error                                         :35

DataPlaneInitializer                                           :100
└── Initialize(ctx) error

DataPlaneNodeRegistrar                                         :106
└── RegisterNode(ctx, nodeIP, podCIDR) error

DataPlaneBridgeRetrier                                         :112
└── RetryBridgeAttachment(ctx, podCIDR) error

EndpointIdentityRegistrar                                      :118
└── RegisterEndpointIdentity(ctx, containerIP, hostIP, service, port) error

DataPlanePolicyReconciler                                      :125
└── ReconcilePolicies(ctx) error
```

### Implementations

| Type | File | Mode |
|---|---|---|
| `NftablesDataPlane` | `network/nftables_dataplane.go:60` | Production: verdict map O(1) |
| `IptablesDataPlane` | `network/iptables_dataplane.go:49` | Production: linear chain walk |
| `EbpfDataPlane` | `network/ebpf_dataplane.go` | Production: BPF maps + TC programs |
| `ShadowDataPlane` | `network/shadow_dataplane.go:26` | Validation: iptables active + nftables shadow |
| `SimulatorDataPlane` | `network/simulator_dataplane.go:15` | Testing: records calls, no-op |

---

## 9. NftablesDataPlane (network/nftables_dataplane.go)

### NftablesDataPlane.ReconcileVIPDataPlane :92
```
NftablesDataPlane.ReconcileVIPDataPlane(ctx, serviceConfigs)
├── ensureDummyInterface()                                     :471
│   ├── ip link add cca-vip0 type dummy
│   └── ip link set cca-vip0 up
├── buildRouteDispatchOverrides(lastRouteConfigs)              :135
│   └── map["{vip}:{port}"] → route chain name
├── buildVIPRuleset(configs, desiredChains, desiredVIPs, overrides)  :234
│   ├── table ip cca { delete + recreate }
│   ├── map vip_dispatch { type verdict }
│   ├── chain prerouting { nat hook, vmap @vip_dispatch }
│   ├── chain output { nat hook, vmap @vip_dispatch }
│   ├── writePostRoutingChain(&builder)                        :318
│   │   └── chain postrouting { ct status dnat masquerade }
│   ├── writeContainerIsolationChain(&builder)                 :305
│   │   └── chain container_isolation { filter hook, drop cca0→etcd 2379/2380 }
│   ├── FOR EACH service (sorted):
│   │   └── chain svc_{name} { writeBackendDNATRules }         :349
│   │       ├── single backend: meta l4proto {proto} dnat to {ip}:{port}
│   │       └── multi-backend: dnat to numgen inc mod N map { 0: ip.port, ... }
│   └── writeVerdictMapWithElements(&builder, entries)          :327
│       └── map vip_dispatch { elements = { {vip} . {proto} . {port} : goto {chain} } }
├── appendRouteChains(&ruleset, routeConfigs, desiredRouteChains)  :150
│   ├── source-ip: writeSourceIPStickyChain(&builder, config, chain)  :421
│   │   └── jhash ip saddr mod N map { ... }
│   └── cookie: writeCookieProxyChain(&builder, chain, proxyPort)  :446
│       └── meta l4proto tcp redirect to :{proxyPort}
├── applyNftablesRuleset(ruleset)                              :508
│   └── write temp file → nft -f {tempFile} (atomic)
├── FOR EACH service: ensureVIPAddress(vip)                    :482
│   └── ip addr add {vip}/32 dev cca-vip0
└── removeStaleVIPAddresses(desired)                           :495
    └── ip addr del {staleVip}/32 dev cca-vip0
```

---

## 10. IptablesDataPlane (network/iptables_dataplane.go)

### IptablesDataPlane.ReconcileVIPDataPlane :80
```
IptablesDataPlane.ReconcileVIPDataPlane(ctx, serviceConfigs)
├── ensureGlobalChainAndJumpRules()                            :297
│   ├── iptables -t nat -N CCA_SERVICES
│   ├── ensureJumpToChain("PREROUTING", "CCA_SERVICES")       :469
│   ├── ensureJumpToChain("OUTPUT", "CCA_SERVICES")
│   ├── ensureMasqueradeForDNAT()                              :446
│   │   └── iptables -t nat -A POSTROUTING ! -o lo -m conntrack --ctstate DNAT -j MASQUERADE
│   └── ensureContainerIsolationRules()                        :326
│       └── iptables -I INPUT -i cca0 -p tcp --dports 2379,2380 -j LOG/DROP
├── ensureDummyInterface()                                     :356
├── FOR EACH service:
│   ├── ensureVIPAddress(vip)                                  :369
│   └── reconcileServiceChain(config, chainName)               :380
│       ├── iptables -t nat -N CCA_SVC_{name}
│       ├── iptables -t nat -F CCA_SVC_{name}
│       ├── FOR EACH backend: -A chain -p proto -m statistic --mode nth --every N -j DNAT
│       └── ensureJumpFromMainToService(vip, port, proto, chain)  :480
│           └── iptables -t nat -A CCA_SERVICES -d {vip}/32 -p {proto} --dport {port} -j {chain}
├── removeStaleServiceChains(desired)                          :418
│   └── removeJumpRuleFromMainChain(chain)                     :495
│   └── flushAndDeleteChain(chain)                             :524
└── removeStaleVIPAddresses(desired)                           :432
```

### IptablesDataPlane.ReconcileRouteDataPlane :124
```
IptablesDataPlane.ReconcileRouteDataPlane(ctx, routeConfigs)
├── ensureGlobalChainAndJumpRules()
├── FOR EACH route:
│   ├── "source-ip": reconcileSourceIPStickyChain(config, chain)  :166
│   │   ├── -A chain -m recent --name {list} --rcheck --seconds 1800 -j DNAT
│   │   └── -A chain -m statistic --mode nth -m recent --set -j DNAT
│   └── "cookie": reconcileCookieStickyChain(config, chain)    :221
│       └── -A chain -p tcp -j DNAT --to-destination 127.0.0.1:{proxyPort}
├── cookieProxy.ReconcileRoutes(routeConfigs)                  [see §12]
└── removeStaleRouteChains(desired)                            :249
```

---

## 11. ShadowDataPlane (network/shadow_dataplane.go)

### ShadowDataPlane.ReconcileVIPDataPlane :50
```
ShadowDataPlane.ReconcileVIPDataPlane(ctx, serviceConfigs)
├── activeProvider.ReconcileVIPDataPlane(ctx, serviceConfigs)   [iptables — real traffic]
└── compareVIPRulesets(serviceConfigs)                          [nftables — shadow only, log divergences]
```

---

## 12. CookieStickyProxy (network/cookie_proxy.go)

### CookieStickyProxy.ReconcileRoutes :64
```
CookieStickyProxy.ReconcileRoutes(routeConfigs)
├── filter: only routes with StickyMethod="cookie" + CookieConfig
├── FOR EACH desired cookie route:
│   ├── IF already running: updateBackends(backends, cookieConf)  :166
│   └── IF new: startRouteProxy(routeName, routeConfig)        :118
│       ├── net.Listen("tcp", "127.0.0.1:0") → allocate port
│       ├── create cookieRouteProxy{routeName, port, backends, cookieConf}
│       └── goroutine: server.Serve(listener)
│           └── cookieRouteProxy.ServeHTTP(w, r)               :180
│               ├── selectBackendFromCookie(r, backends, cookieName)  :218
│               │   ├── read cookie → find matching backend
│               │   └── fallback: round-robin (nextIndex % len)
│               └── httputil.ReverseProxy.ServeHTTP
│                   └── ModifyResponse: buildStickyCookie(conf, value)  :247
└── stop proxies for routes no longer desired
```

---

## 13. NetworkProvider Interface (network/network.go)

### Interface
```
NetworkProvider                                                :26
├── AllocateIP(ctx, nodeID, instanceID) (string, error)
├── ReleaseIP(ctx, nodeID, instanceID) error
└── NodeSubnet(nodeID) string
```

### SimulatorNetworkProvider (network/simulator.go)

```
SimulatorNetworkProvider.AllocateIP(ctx, nodeID, instanceID)   :60
├── IF already allocated: return existing IP
├── ensureNodeSubnetAssigned(nodeID)                           :116
│   └── assign next third-octet, init fourthOctet=2
└── allocate {first}.{second}.{thirdOctet}.{fourthOctet}
    └── increment fourthOctet for node

SimulatorNetworkProvider.ReleaseIP(ctx, nodeID, instanceID)    :95
└── delete(instanceAllocations, instanceID)

SimulatorNetworkProvider.NodeSubnet(nodeID)                    :105
└── ensureNodeSubnetAssigned → format "{first}.{second}.{thirdOctet}.0/24"
```

---

## 14. Load Balancer (network/load_balancer.go)

### LeastConnectionsBalancer
```
LeastConnectionsBalancer.SelectBackend(candidates)             :68
├── ensure all candidates in heap (lazy init)
├── scan heap for candidate with fewest active connections
├── increment activeConnections
└── heap.Fix → re-heapify

LeastConnectionsBalancer.ReleaseBackend(address)               :110
└── decrement activeConnections → heap.Fix
```

---

## 15. CircuitBreaker (network/circuit_breaker.go)

### CircuitBreaker
```
CircuitBreaker.AllowRequest(backendAddress)                    :84
├── IF closed: allow
├── IF open: check if cooldown expired → half-open (allow one)
└── IF half-open: reject (one probe in flight)

CircuitBreaker.RecordSuccess(backendAddress)                   :117
└── reset to closed, clear failure count

CircuitBreaker.RecordFailure(backendAddress)                   :144
├── increment failure count
└── IF failures >= threshold: transition to open, set cooldown timer
```

---

## 16. ServiceProxy (network/proxy.go)

### SimulatorProxy.RouteRequest :58
```
SimulatorProxy.RouteRequest(ctx, serviceName)
├── resolver.ResolveEndpoints(ctx, serviceName)
├── getOrCreateCounter(serviceName)                            :93
│   └── lazy init atomic.Uint64 per service
├── round-robin: endpoints[counter % len]
└── record in routingDecisions[serviceName]
```

### UserSpaceProxy (network/userspace_proxy.go)
```
UserSpaceProxy — HTTP reverse proxy
├── resolves service from Host header (strip .cca.local)
├── resolver.ResolveEndpoints → backends
├── warm-zero: if min=0 + idle_timeout, triggers activation on cold request
└── httputil.ReverseProxy → forward to selected backend
```

---

## 17. DataPlane Registry (network/dataplane_registry.go)

```
RegisterDataPlaneFactory(name, factory)                        :19
└── dataPlaneFactories[name] = factory  (called from init() in build-tagged files)

LookupDataPlaneFactory(name)                                   :28
└── return dataPlaneFactories[name]
```

---

## Reverse Index

| Function | Called By |
|---|---|
| `NetworkController.Reconcile` | `controllers.Runner` via watch loop |
| `EndpointController.Reconcile` | `controllers.Runner` via watch loop |
| `NetworkPolicyController.Reconcile` | `controllers.Runner` via watch loop |
| `DNSServer.Start` | `cmd/server.go` (control plane startup) |
| `DNSForwarder.Start` | `Agent.Run` (goroutine) |
| `StoreBackedResolver.ResolveVIP` | `DNSServer.handleDNSQuery` |
| `StoreBackedResolver.ResolveEndpoints` | `SimulatorProxy.RouteRequest`, `UserSpaceProxy` |
| `VXLANReconciler.Reconcile` | `Agent.dataPlaneLoop` (1s ticker) |
| `VXLANReconciler.EnsureSubnetPublished` | `Agent.Run` (startup) |
| `NftablesDataPlane.ReconcileVIPDataPlane` | `Agent.dataPlaneLoop` via `DataPlaneProvider` |
| `IptablesDataPlane.ReconcileVIPDataPlane` | `Agent.dataPlaneLoop` via `DataPlaneProvider` |
| `ShadowDataPlane.ReconcileVIPDataPlane` | `Agent.dataPlaneLoop` via `DataPlaneProvider` |
| `CookieStickyProxy.ReconcileRoutes` | `IptablesDataPlane.ReconcileRouteDataPlane`, `NftablesDataPlane.ReconcileRouteDataPlane` |
| `SimulatorNetworkProvider.AllocateIP` | `Agent.reconcileDesiredInstance` → `networkProvider.AllocateIP` |
| `LeastConnectionsBalancer.SelectBackend` | `UserSpaceProxy` request handler |
| `CircuitBreaker.AllowRequest` | `UserSpaceProxy` request handler |
| `collectServicesWithEndpointsFromFacts` | `NetworkController.Reconcile` |
| `buildDesiredEndpoints` | `EndpointController.Reconcile` |
| `compileIdentityRulesToIPRulesPerNode` | `NetworkPolicyController.Reconcile` |
| `deriveVXLANMACAddress` | `ensureVXLANInterfaceExists`, `programPeerRoutes` |
| `buildStickyCookie` | `cookieRouteProxy.ServeHTTP` |
| `ParseSystemResolvers` | `Agent.Run` (DNS forwarder setup) |

---

## Store Key Summary (all network-related)

| Prefix | Writer | Reader | Purpose |
|---|---|---|---|
| `network/vip/service/{name}` | NetworkController | DataPlaneReconciler, DNSServer, StoreBackedResolver | Service VIP address |
| `network/vip/service/{name}/port` | NetworkController | DataPlaneReconciler | VIP exposed port |
| `network/dns/{name}` | NetworkController | DNS subsystem | Service DNS → VIP |
| `network/dns/instance/{id}` | StatefulDNSController | DNS Forwarder | Per-instance DNS |
| `network/node/{nodeID}/subnet` | VXLANReconciler | VXLANReconciler (peers) | Node /24 subnet |
| `endpoint/service/{svc}/{id}/{port}` | EndpointController | NetworkController, DataPlaneReconciler, NetworkPolicyController, StoreBackedResolver | Backend endpoint |
| `policy/network/{name}` | Compiler | NetworkPolicyController | Identity policy rules |
| `derived/network/rule/{nodeID}/{index}` | NetworkPolicyController | DataPlanePolicyReconciler (eBPF) | Compiled IP firewall rules |
| `observed/instance/{id}/ip` | Agent | EndpointController, StatefulDNSController | Container IP |
| `observed/instance/{id}/host_port/{port}` | Agent | EndpointController, DataPlaneReconciler | Host port mapping |
| `observed/node/{nodeID}/address` | Agent | EndpointController, VXLANReconciler | Node LAN address |
