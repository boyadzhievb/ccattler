# CCattler eBPF Data Plane Call Graph

Machine-readable function-level index for the eBPF data plane provider.
Purpose: trace Go→BPF map operations and program attachment flows without
reading source files. Load this file instead.

For the nftables/iptables providers and the reconciler, see DATA_PLANE_GRAPH.md.

## Source File Index

| File | Line Count | Purpose |
|---|---|---|
| `network/ebpf_dataplane.go` | 840 | Go-side map reconciliation, initialization, cleanup |
| `network/ebpf_types.go` | 217 | Go struct types mirroring C structs, byte-order helpers |
| `network/ebpf_embed.go` | 17 | `go:embed` of compiled ELF (`network/ebpf/cca_dataplane.o`) |
| `network/ebpf_gen.go` | — | `go:generate` directive for `bpf2go` (alternative build) |
| `network/ebpf_register.go` | 18 | Factory registration via `init()` under `ebpf` build tag |
| `network/dataplane.go` | 140 | `DataPlaneProvider` interface + extension interfaces |
| `network/dataplane_registry.go` | 31 | `RegisterDataPlaneFactory` / `LookupDataPlaneFactory` |
| `network/ebpf/cca_dataplane.c` | — | Unified BPF C source — all 5 programs |
| `network/ebpf/cca_maps.h` | — | BPF map definitions and C struct layouts |
| `network/ebpf/cca_helpers.h` | — | `select_backend()` helper — random slot + affinity |
| `network/ebpf/cca_ct_helpers.h` | — | Conntrack kfunc declarations for tc programs |
| `network/ebpf/cca_dataplane.o` | — | Pre-compiled ELF — committed for macOS cross-builds |

Build tag: `//go:build ebpf` — only compiled with `go build -tags ebpf`.

## Provider Registration

```
network/ebpf_register.go:14
  init()
  └── RegisterDataPlaneFactory("ebpf", func(...) DataPlaneProvider)
      └── NewEbpfDataPlane()                         [ebpf_dataplane.go:125]
          ├── pinPath = "/sys/fs/bpf/cca"
          ├── nextServiceID = 1
          └── serviceNameToID = make(map[string]uint32)
```

## EbpfDataPlane.Initialize → load ELF + attach cgroup programs

```
EbpfDataPlane.Initialize(ctx)                        [ebpf_dataplane.go:136]
├── rlimit.RemoveMemlock()                           [cilium/ebpf/rlimit]
├── ensurePinDirectory()                             [ebpf_dataplane.go:217]
│   └── os.MkdirAll("/sys/fs/bpf/cca", 0700)
├── ebpf.LoadCollectionSpecFromReader(embeddedDataplaneELF)
│   └── embeddedDataplaneELF                         [ebpf_embed.go:16, go:embed]
├── ebpf.NewCollection(collectionSpec)               [loads 5 programs + 5 maps]
├── extractAndPinMaps()                              [ebpf_dataplane.go:223]
│   ├── FOR EACH {cca_services, cca_backends, cca_affinity, cca_reverse_nat}:
│   │   ├── bpfCollection.Maps[mapName]
│   │   ├── os.Remove(pinPath + "/" + mapName)       [idempotent re-pin]
│   │   └── bpfMap.Pin(pinPath + "/" + mapName)
│   └── assigns: serviceMap, backendMap, affinityMap, reverseNatMap
├── attachCgroupPrograms()                           [ebpf_dataplane.go:252]
│   └── FOR EACH ebpfCgroupPrograms:
│       ├── {cca_sock4_connect,   AttachCGroupInet4Connect}
│       ├── {cca_sock4_sendmsg,   AttachCGroupUDP4Sendmsg}
│       └── {cca_sock4_getpeername, AttachCgroupInet4GetPeername}
│       └── link.AttachCgroup({Path: "/sys/fs/cgroup", Attach: type, Program: prog})
│           └── append to attachedLinks
├── scanExistingServiceIDs()                         [ebpf_dataplane.go:275]
│   ├── serviceMap.Iterate() → scan all entries
│   ├── track max(ServiceID)
│   └── nextServiceID = max + 1                      [resume ID allocation]
└── extractTCPrograms()                              [ebpf_dataplane.go:189]
    ├── tcProgram = bpfCollection.Programs["cca_tc_dnat"]
    ├── snatProgram = bpfCollection.Programs["cca_tc_snat"]
    └── configMap = bpfCollection.Maps["cca_config"]
```

## EbpfDataPlane.RegisterNode → attach tc programs

```
EbpfDataPlane.RegisterNode(ctx, nodeIP, podCIDR)     [ebpf_dataplane.go:530]
├── discoverBridgeInterfaceBySubnet(podCIDR)         [ebpf_dataplane.go:725]
│   ├── net.ParseCIDR(podCIDR) → targetNetwork
│   ├── net.Interfaces() → scan all
│   ├── isBridgeInterface(name)                      [ebpf_dataplane.go:760]
│   │   └── os.Stat("/sys/class/net/" + name + "/bridge")
│   └── targetNetwork.Contains(interfaceIP) → match
├── attachTCProgramToBridge(bridgeName)              [ebpf_dataplane.go:702]
│   ├── net.InterfaceByName(bridgeName) → index
│   └── link.AttachTCX({Interface: index, Program: tcProgram, Attach: TCXIngress})
│       └── tcLink = result
├── attachSNATToPhysicalNIC(nodeIP, podCIDR)         [ebpf_dataplane.go:600]
│   ├── populateConfigMap(nodeIP, podCIDR)           [ebpf_dataplane.go:644]
│   │   ├── configMap.Put(0, ipToUint32(nodeIP))            [NODE_IP]
│   │   ├── configMap.Put(2, ipToUint32(overlayPrefix))     [OVERLAY_PREFIX]
│   │   └── configMap.Put(3, ipBytesToUint32(overlayMask))  [OVERLAY_MASK]
│   ├── detectPhysicalInterfaceForNodeIP(nodeIP)     [ebpf_dataplane.go:677]
│   │   └── net.Interfaces() → find matching address
│   └── link.AttachTCX({Interface: physIndex, Program: snatProgram, Attach: TCXEgress})
│       └── snatLink = result
```

## EbpfDataPlane.RetryBridgeAttachment → deferred bridge attach

```
EbpfDataPlane.RetryBridgeAttachment(ctx, podCIDR)   [ebpf_dataplane.go:570]
├── IF tcLink != nil → return (already attached)
├── IF tcProgram == nil → return (not loaded)
├── discoverBridgeInterfaceBySubnet(podCIDR)         [ebpf_dataplane.go:725]
└── attachTCProgramToBridge(bridgeName)              [ebpf_dataplane.go:702]
```

## EbpfDataPlane.ReconcileVIPDataPlane → map reconciliation

```
EbpfDataPlane.ReconcileVIPDataPlane(ctx, serviceConfigs)  [ebpf_dataplane.go:299]
├── buildDesiredVIPState(serviceConfigs)              [ebpf_dataplane.go:329]
│   ├── sort by ServiceName                           [deterministic]
│   ├── skip services with 0 backends
│   └── FOR EACH service:
│       └── buildServiceEntries(config, desiredSvcs, desiredBackends)
│                                                     [ebpf_dataplane.go:356]
│           ├── assignOrLookupServiceID(serviceName)  [ebpf_dataplane.go:386]
│           │   ├── IF in serviceNameToID → return existing
│           │   └── ELSE: allocate nextServiceID++, store in map
│           ├── serviceKey = {ipToUint32(VIP), portToNetworkOrder(port), proto}
│           ├── serviceValue = {svc_id, backendCount, flags=0, timeout=0}
│           └── FOR EACH backend[i]:
│               └── backendKey={svc_id, slot=i}, backendValue={addr, port}
│
├── applyDesiredBackendEntries(desiredBackends)       [ebpf_dataplane.go:398]
│   └── FOR EACH desired: backendMap.Put(key, value)  ← BACKENDS FIRST
│
├── applyDesiredServiceEntries(desiredServices)       [ebpf_dataplane.go:412]
│   └── FOR EACH desired: serviceMap.Put(key, value)  ← SERVICES SECOND
│
├── removeStaleServiceEntries(desiredServices)        [ebpf_dataplane.go:426]
│   ├── serviceMap.Iterate() → collect stale keys
│   └── serviceMap.Delete(staleKey)                   ← STALE SERVICES THIRD
│
└── removeStaleBackendEntries(desiredBackends)        [ebpf_dataplane.go:453]
    ├── backendMap.Iterate() → collect stale keys
    └── backendMap.Delete(staleKey)                   ← STALE BACKENDS LAST

ORDER INVARIANT:
  add backends → update services → remove stale services → remove stale backends
  Services always reference valid backends. No dangling references.
```

## EbpfDataPlane.ReconcileRouteDataPlane → sticky affinity

```
EbpfDataPlane.ReconcileRouteDataPlane(ctx, routeConfigs) [ebpf_dataplane.go:480]
└── FOR EACH routeConfig with StickyMethod == "source-ip":
    └── enableStickyAffinityForService(routeConfig)  [ebpf_dataplane.go:500]
        ├── serviceKey = {ipToUint32(VIP), portToNetworkOrder(port), TCP}
        ├── serviceMap.Lookup(serviceKey) → serviceValue
        ├── serviceValue.Flags |= 0x01              [STICKY_SOURCE_IP]
        ├── serviceValue.AffinityTimeoutSeconds = 300
        └── serviceMap.Put(serviceKey, updatedValue)
```

## EbpfDataPlane.Cleanup → detach + unpin

```
EbpfDataPlane.Cleanup(ctx)                           [ebpf_dataplane.go:768]
├── closeAllResources()                              [ebpf_dataplane.go:785]
│   ├── snatLink.Close()                             [detach tc SNAT from NIC]
│   ├── tcLink.Close()                               [detach tc DNAT from bridge]
│   ├── FOR EACH attachedLink: link.Close()          [detach cgroup programs]
│   └── bpfCollection.Close()                        [close all program + map FDs]
├── removePinnedMaps()                               [ebpf_dataplane.go:827]
│   └── FOR EACH {cca_services, cca_backends, cca_affinity, cca_reverse_nat}:
│       └── os.Remove("/sys/fs/bpf/cca/" + mapName)
└── reset: serviceNameToID = empty, nextServiceID = 1, initialized = false
```

## Byte-Order Helpers (network/ebpf_types.go)

```
ipToUint32(ipAddress string) → uint32               [ebpf_types.go:175]
├── net.ParseIP(ipAddress) → parsedIP
├── parsedIP.To4() → ipv4Bytes
└── binary.NativeEndian.Uint32(ipv4Bytes)

uint32ToIP(bpfAddr uint32) → string                  [ebpf_types.go:189]
└── binary.NativeEndian.PutUint32 → dotted decimal

portToNetworkOrder(hostPort uint16) → uint16          [ebpf_types.go:17]
├── binary.BigEndian.PutUint16(networkBytes, hostPort)
└── binary.NativeEndian.Uint16(networkBytes)

ipBytesToUint32(ipBytes []byte) → uint32              [ebpf_types.go:198]
└── binary.NativeEndian.Uint32(ipBytes[:4])

protocolToNumber(protocolName string) → uint8         [ebpf_types.go:207]
├── "tcp" → 6
├── "udp" → 17
└── default → 6
```

## BPF Map Operation Table (Go → BPF)

| BPF Map | Go Operation | Where Called |
|---|---|---|
| `cca_services` | `serviceMap.Put(key, value)` | `applyDesiredServiceEntries` |
| `cca_services` | `serviceMap.Iterate()` | `removeStaleServiceEntries`, `scanExistingServiceIDs` |
| `cca_services` | `serviceMap.Delete(key)` | `removeStaleServiceEntries` |
| `cca_services` | `serviceMap.Lookup(key, &value)` | `enableStickyAffinityForService` |
| `cca_backends` | `backendMap.Put(key, value)` | `applyDesiredBackendEntries` |
| `cca_backends` | `backendMap.Iterate()` | `removeStaleBackendEntries` |
| `cca_backends` | `backendMap.Delete(key)` | `removeStaleBackendEntries` |
| `cca_config` | `configMap.Put(index, value)` | `populateConfigMap` |
| `cca_affinity` | (not accessed from Go) | BPF programs read/write autonomously |
| `cca_reverse_nat` | (not accessed from Go) | BPF programs read/write autonomously |

## BPF Struct Types (Go ↔ C layout)

| Go Type | C Equivalent | Size | Fields |
|---|---|---|---|
| `EbpfServiceKey` | `struct service_key` | 8B | `VIPAddr:u32, Port:u16, Protocol:u8, Pad:u8` |
| `EbpfServiceValue` | `struct service_value` | 12B | `ServiceID:u32, BackendCount:u16, Flags:u16, AffinityTimeoutSeconds:u32` |
| `EbpfBackendKey` | `struct backend_key` | 8B | `ServiceID:u32, Slot:u16, Pad:u16` |
| `EbpfBackendValue` | `struct backend_value` | 8B | `BackendAddr:u32, Port:u16, Pad:u16` |
| `EbpfAffinityKey` | `struct affinity_key` | 8B | `ClientAddr:u32, ServiceID:u32` |
| `EbpfAffinityValue` | `struct affinity_value` | 16B | `BackendAddr:u32, Port:u16, Pad:u16, Timestamp:u64` |
| `EbpfReverseNatKey` | `struct reverse_nat_key` | 16B | `SocketCookie:u64, BackendAddr:u32, BackendPort:u16, Pad:u16` |
| `EbpfReverseNatValue` | `struct reverse_nat_value` | 8B | `VIPAddr:u32, VIPPort:u16, Pad:u16` |

All defined in `network/ebpf_types.go:70-169`.

## BPF Program Inventory

| # | Program Name | Section | Hook | Attach Point |
|---|---|---|---|---|
| 1 | `cca_sock4_connect` | `cgroup/connect4` | connect() TCP | `/sys/fs/cgroup` |
| 2 | `cca_sock4_sendmsg` | `cgroup/sendmsg4` | sendto() UDP | `/sys/fs/cgroup` |
| 3 | `cca_sock4_getpeername` | `cgroup/getpeername4` | getpeername() | `/sys/fs/cgroup` |
| 4 | `cca_tc_dnat` | `tc` | tc ingress | cca0 bridge (TCXIngress) |
| 5 | `cca_tc_snat` | `tc` | tc egress | eth0 NIC (TCXEgress) |

### cca_sock4_connect (cca_dataplane.c:39)

```
cca_sock4_connect(socket_ctx)
├── service_key = {user_ip4, user_port, protocol}
├── bpf_map_lookup_elem(&cca_services, &key)
│   └── IF miss → return 1 (passthrough)
├── client_identity = (u32)bpf_get_socket_cookie()
│   └── NOTE: msg_src_ip4 NOT readable in connect4
├── select_backend(service, client_identity)          [cca_helpers.h:18]
│   ├── IF STICKY_SOURCE_IP flag:
│   │   ├── bpf_map_lookup_elem(&cca_affinity, {client, svc_id})
│   │   └── IF cached AND not expired → return cached
│   ├── slot = bpf_get_prandom_u32() % backend_count
│   ├── bpf_map_lookup_elem(&cca_backends, {svc_id, slot})
│   │   └── IF miss → return -1
│   └── IF STICKY: bpf_map_update_elem(&cca_affinity, ..., ktime_ns)
├── socket_ctx->user_ip4 = backend.addr
├── socket_ctx->user_port = backend.port
├── bpf_map_update_elem(&cca_reverse_nat, {cookie, backend} → {vip})
└── return 1
```

### cca_sock4_sendmsg (cca_dataplane.c:80)

```
cca_sock4_sendmsg(socket_ctx)
└── same as connect4 except:
    ├── uses msg_src_ip4 for affinity (readable in sendmsg4)
    └── per-datagram translation (no persistent socket rewrite)
```

### cca_sock4_getpeername (cca_dataplane.c:120)

```
cca_sock4_getpeername(socket_ctx)
├── reverse_nat_key = {bpf_get_socket_cookie(), user_ip4, user_port}
├── bpf_map_lookup_elem(&cca_reverse_nat, &key)
│   └── IF miss → return 1 (passthrough)
├── socket_ctx->user_ip4 = vip_addr
├── socket_ctx->user_port = vip_port
└── return 1
```

### cca_tc_dnat (cca_dataplane.c:181)

```
cca_tc_dnat(skb)
├── parse Ethernet → IPv4 → TCP/UDP headers          [verifier bounds checks]
├── bpf_sock_tuple from packet headers
├── bpf_skb_ct_lookup(tuple)
│   └── IF existing conntrack → bpf_ct_release(), return TC_ACT_OK
├── service_key = {dst_ip, dst_port, protocol}
├── bpf_map_lookup_elem(&cca_services, &key)
│   └── IF miss → return TC_ACT_OK
├── select_backend(service, src_ip)
├── bpf_skb_ct_alloc(tuple)                          [allocate conntrack entry]
├── bpf_ct_set_nat_info(entry, backend_addr, backend_port, NF_NAT_MANIP_DST)
├── bpf_ct_insert_entry(entry)                       [commit to conntrack table]
├── bpf_ct_release(entry)
└── return TC_ACT_OK                                 [BPF does NOT modify packets]

NOTE: kernel nf_nat handles actual packet rewrite + return path via conntrack.
```

### cca_tc_snat (cca_dataplane.c:315)

```
cca_tc_snat(skb)
├── parse Ethernet → IPv4 headers
├── is_overlay_source(saddr)?
│   └── (saddr & cca_config[OVERLAY_MASK]) == cca_config[OVERLAY_PREFIX]
│       └── IF no → return TC_ACT_OK
├── get_node_ip() from cca_config[NODE_IP]
│   └── IF 0 → return TC_ACT_OK
├── IF saddr == node_ip → return TC_ACT_OK
├── bpf_l3_csum_replace(ip_checksum, old_src, node_ip)
├── bpf_skb_store_bytes(saddr ← node_ip)
├── bpf_l4_csum_replace(tcp/udp checksum, old_src, node_ip)
└── return TC_ACT_OK

NOTE: Direct packet modification (unlike tc_dnat which uses conntrack kfuncs).
```

## BPF Map Inventory

| Map | Type | Max Entries | Pin Path | Key | Value |
|---|---|---|---|---|---|
| `cca_services` | HASH | 4096 | `/sys/fs/bpf/cca/cca_services` | `EbpfServiceKey` (8B) | `EbpfServiceValue` (12B) |
| `cca_backends` | HASH | 65536 | `/sys/fs/bpf/cca/cca_backends` | `EbpfBackendKey` (8B) | `EbpfBackendValue` (8B) |
| `cca_affinity` | LRU_HASH | 131072 | `/sys/fs/bpf/cca/cca_affinity` | `EbpfAffinityKey` (8B) | `EbpfAffinityValue` (16B) |
| `cca_reverse_nat` | LRU_HASH | 131072 | `/sys/fs/bpf/cca/cca_reverse_nat` | `EbpfReverseNatKey` (16B) | `EbpfReverseNatValue` (8B) |
| `cca_config` | ARRAY | 8 | (not pinned) | `uint32` index | `uint32` value |

### cca_config Array Layout

| Index | Go Constant | C Constant | Content |
|---|---|---|---|
| 0 | `ebpfConfigIndexNodeIP` | `CCA_CONFIG_NODE_IP` | Node advertise address (NBO) |
| 2 | `ebpfConfigIndexOverlayPrefix` | `CCA_CONFIG_OVERLAY_PREFIX` | Overlay CIDR network (NBO) |
| 3 | `ebpfConfigIndexOverlayMask` | `CCA_CONFIG_OVERLAY_MASK` | Overlay CIDR netmask (NBO) |

## Conntrack Kfuncs (kernel 6.1+)

Used by `cca_tc_dnat` to program conntrack entries instead of modifying packets.

| Kfunc | Kernel | Called In | Purpose |
|---|---|---|---|
| `bpf_skb_ct_lookup` | 5.18+ | `cca_tc_dnat` | Find existing conntrack entry |
| `bpf_skb_ct_alloc` | 5.18+ | `cca_tc_dnat` | Allocate new conntrack entry |
| `bpf_ct_set_nat_info` | 6.1+ | `cca_tc_dnat` | Set DNAT target on entry |
| `bpf_ct_insert_entry` | 5.18+ | `cca_tc_dnat` | Commit entry to conntrack |
| `bpf_ct_release` | 5.18+ | `cca_tc_dnat` | Release conntrack reference |

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `NewEbpfDataPlane` | `init()` [ebpf_register.go:15] via `RegisterDataPlaneFactory` |
| `Initialize` | Agent startup via `DataPlaneInitializer` interface |
| `RegisterNode` | Agent startup via `DataPlaneNodeRegistrar` interface |
| `RetryBridgeAttachment` | Agent reconcile via `DataPlaneBridgeRetrier` interface |
| `ReconcileVIPDataPlane` | `DataPlaneReconciler.Reconcile` [agent/dataplane.go:66] |
| `ReconcileRouteDataPlane` | `DataPlaneReconciler.Reconcile` [agent/dataplane.go:77] |
| `Cleanup` | Agent shutdown |
| `extractAndPinMaps` | `Initialize` |
| `attachCgroupPrograms` | `Initialize` |
| `scanExistingServiceIDs` | `Initialize` |
| `extractTCPrograms` | `Initialize` |
| `ensurePinDirectory` | `Initialize` |
| `buildDesiredVIPState` | `ReconcileVIPDataPlane` |
| `buildServiceEntries` | `buildDesiredVIPState` (for each service) |
| `assignOrLookupServiceID` | `buildServiceEntries` |
| `applyDesiredBackendEntries` | `ReconcileVIPDataPlane` |
| `applyDesiredServiceEntries` | `ReconcileVIPDataPlane` |
| `removeStaleServiceEntries` | `ReconcileVIPDataPlane` |
| `removeStaleBackendEntries` | `ReconcileVIPDataPlane` |
| `enableStickyAffinityForService` | `ReconcileRouteDataPlane` (for source-ip routes) |
| `discoverBridgeInterfaceBySubnet` | `RegisterNode`, `RetryBridgeAttachment` |
| `isBridgeInterface` | `discoverBridgeInterfaceBySubnet` |
| `attachTCProgramToBridge` | `RegisterNode`, `RetryBridgeAttachment` |
| `attachSNATToPhysicalNIC` | `RegisterNode` |
| `populateConfigMap` | `attachSNATToPhysicalNIC` |
| `detectPhysicalInterfaceForNodeIP` | `attachSNATToPhysicalNIC` |
| `closeAllResources` | `Cleanup`, `Initialize` (on attach error) |
| `removePinnedMaps` | `Cleanup` |
| `ipToUint32` | `buildServiceEntries`, `enableStickyAffinityForService`, `populateConfigMap` |
| `portToNetworkOrder` | `buildServiceEntries`, `enableStickyAffinityForService` |
| `protocolToNumber` | `buildServiceEntries` |
| `select_backend` (BPF) | `cca_sock4_connect`, `cca_sock4_sendmsg`, `cca_tc_dnat` |

## Constants Reference

| Constant | Value | File:Line | Used By |
|---|---|---|---|
| `ebpfPinPath` | `/sys/fs/bpf/cca` | `ebpf_dataplane.go:27` | `ensurePinDirectory`, `extractAndPinMaps`, `removePinnedMaps` |
| `ebpfCgroupPath` | `/sys/fs/cgroup` | `ebpf_dataplane.go:30` | `attachCgroupPrograms` |
| `ebpfInitialServiceID` | `1` | `ebpf_dataplane.go:59` | `NewEbpfDataPlane`, `Cleanup` |
| `ebpfServiceFlagStickySourceIP` | `0x01` | `ebpf_types.go:37` | `enableStickyAffinityForService` |
| `ebpfDefaultAffinityTimeoutSeconds` | `300` | `ebpf_types.go:41` | `enableStickyAffinityForService` |
| `ebpfProtocolTCP` | `6` | `ebpf_types.go:44` | `enableStickyAffinityForService`, `protocolToNumber` |
| `ebpfProtocolUDP` | `17` | `ebpf_types.go:47` | `protocolToNumber` |
| `ebpfTCDNATProgName` | `"cca_tc_dnat"` | `ebpf_types.go:50` | `extractTCPrograms` |
| `ebpfTCSNATProgName` | `"cca_tc_snat"` | `ebpf_types.go:53` | `extractTCPrograms` |
| `ebpfConfigMapName` | `"cca_config"` | `ebpf_types.go:56` | `extractTCPrograms` |
| `ebpfConfigIndexNodeIP` | `0` | `ebpf_types.go:60` | `populateConfigMap` |
| `ebpfConfigIndexOverlayPrefix` | `2` | `ebpf_types.go:64` | `populateConfigMap` |
| `ebpfConfigIndexOverlayMask` | `3` | `ebpf_types.go:68` | `populateConfigMap` |

## Fallback Behavior

```
eBPF unavailable paths:
  1. No "-tags ebpf" → ebpf_register.go not compiled
     → "ebpf" factory never registered
     → agent uses nftables (default) or iptables

  2. Kernel < 6.1 → ebpf.NewCollection() fails at program load
     → Initialize() returns error
     → agent must use different --data-plane

  3. Bridge not found on RegisterNode
     → logs warning, socket LB still works (host processes)
     → container traffic falls through to nftables/iptables
     → RetryBridgeAttachment called after first container starts

  4. SNAT attachment fails
     → logs warning, VIP LB still works
     → overlay SNAT falls back to nftables POSTROUTING masquerade

  TC programs degrade gracefully — each failure logged, not aborted.
  Socket LB (cgroup programs) provides host-level VIP translation
  even if tc programs fail to attach.
```

## Build Pipeline

```
scripts/build-ebpf.sh
├── docker build -t cca-bpf-builder -f scripts/Dockerfile.bpf .
├── docker run --platform linux/amd64
│   -v network/ebpf:/src
│   cca-bpf-builder -c /src/cca_dataplane.c -o /src/cca_dataplane.o
└── commit cca_dataplane.o to repository

Alternative (bpf2go):
  go generate -run CcaDataplane ./network/
  └── defined in: network/ebpf_gen.go
  └── requires: clang, BPF headers, Linux host

Go binary:
  //go:embed ebpf/cca_dataplane.o                    [ebpf_embed.go:16]
  var embeddedDataplaneELF []byte
```

## Inspection Commands

```
bpftool map list pinned /sys/fs/bpf/cca/
bpftool map dump pinned /sys/fs/bpf/cca/cca_services
bpftool map dump pinned /sys/fs/bpf/cca/cca_backends
bpftool prog list | grep cca
bpftool prog show name cca_sock4_connect
bpftool cgroup tree /sys/fs/cgroup
tc filter show dev cca0 ingress
tc filter show dev eth0 egress
```
