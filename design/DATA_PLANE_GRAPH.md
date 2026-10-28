# CCattler Data Plane Reference

Machine-readable reference for packet forwarding paths across all three data
plane providers (nftables, iptables, eBPF). Documents what IS, not what should be.

## Interface Topology (per node)

```
                                    EXTERNAL NETWORK
                                          │
                                          │
                                     ┌────┴────┐
                                     │  eth0   │  physical NIC
                                     │         │  (advertise address, e.g. 192.168.100.20)
                                     └────┬────┘
                                          │
                 ┌────────────────────────┼────────────────────────┐
                 │                        │                        │
           ┌─────┴──────┐          ┌──────┴──────┐          ┌─────┴──────┐
           │ cca-vxlan0 │          │  cca-vip0   │          │    cca0    │
           │ VXLAN tun  │          │ dummy iface │          │ CNI bridge │
           │ VNI=42     │          │ VIP /32 addrs│          │ 10.100.X.1 │
           │ MTU=1450   │          │             │          │ /24 subnet │
           │ UDP:4789   │          │             │          │            │
           └────────────┘          └─────────────┘          └─────┬──────┘
                                                                  │
                                                       ┌──────────┼──────────┐
                                                       │          │          │
                                                    ┌──┴──┐    ┌──┴──┐    ┌──┴──┐
                                                    │veth │    │veth │    │veth │
                                                    │pair │    │pair │    │pair │
                                                    └──┬──┘    └──┬──┘    └──┬──┘
                                                       │          │          │
                                                   container  container  container
                                                   10.100.X.2 10.100.X.3 10.100.X.4
```

### Interface Roles

| Interface | Type | Created By | Purpose |
|---|---|---|---|
| `eth0` | physical NIC | OS | Node-to-node traffic, VXLAN underlay |
| `cca0` | Linux bridge | CNI (containerd) | Container networking, gateway for overlay /24 |
| `cca-vip0` | dummy | Data plane provider | Holds VIP /32 addresses so kernel accepts VIP packets |
| `cca-vxlan0` | VXLAN | VXLANReconciler | Cross-node overlay tunnel (VNI=42, UDP 4789, MTU 1450) |
| `vethXXX` | veth pair | CNI (containerd) | Per-container bridge attachment |

### Interface Creation Order

```
1. Agent starts
   └── VXLANReconciler.ensureVXLANInterfaceExists
       ├── ip link add cca-vxlan0 type vxlan id 42 local <addr> dstport 4789 dev <nic>
       ├── ip link set cca-vxlan0 address <derived-MAC>
       ├── ip link set cca-vxlan0 mtu 1450
       └── ip link set cca-vxlan0 up

2. Data plane reconcile (every 1s)
   └── Provider.ensureDummyInterface
       ├── ip link add cca-vip0 type dummy
       └── ip link set cca-vip0 up

3. First container starts
   └── CNI bridge plugin creates cca0
       ├── cca0 bridge + first veth pair
       └── eBPF: RetryBridgeAttachment attaches tc DNAT to cca0

4. Each service VIP
   └── Provider.ensureVIPAddress
       └── ip addr add <VIP>/32 dev cca-vip0
```

## Provider 1: nftables (default)

### Kernel Objects

```
table ip cca {
    map vip_dispatch {
        type ipv4_addr . inet_proto . inet_service : verdict
        elements = {
            10.200.0.1 . tcp . 80 : goto svc_web,
            10.200.0.2 . tcp . 5432 : goto svc_metadata_db,
            10.200.0.3 . tcp . 8080 : goto rt_web_public     ← route override
        }
    }

    chain prerouting {
        type nat hook prerouting priority -100; policy accept;
        ip daddr . ip protocol . th dport vmap @vip_dispatch
    }

    chain output {
        type nat hook output priority -100; policy accept;
        ip daddr . ip protocol . th dport vmap @vip_dispatch
    }

    chain postrouting {
        type nat hook postrouting priority 100; policy accept;
        ct status dnat masquerade                              ← SNAT for DNAT'd packets
    }

    chain container_isolation {                                ← SECURITY: blocks container→etcd
        type filter hook input priority 0; policy accept;
        iifname "cca0" tcp dport { 2379, 2380 } limit rate 1/minute log prefix "cca:etcd-blocked: "
        iifname "cca0" tcp dport { 2379, 2380 } counter drop
    }

    chain svc_web {
        meta l4proto tcp dnat to numgen inc mod 3 map {        ← round-robin
            0 : 10.100.1.2 . 8080,
            1 : 10.100.2.3 . 8080,
            2 : 192.168.100.30 . 32100                        ← remote node host port
        }
    }

    chain svc_metadata_db {
        meta l4proto tcp dnat to 10.100.1.4:5432               ← single backend
    }

    chain rt_web_public {                                       ← source-ip sticky
        meta l4proto tcp dnat to jhash ip saddr mod 3 map {
            0 : 10.100.1.2 . 8080,
            1 : 10.100.2.3 . 8080,
            2 : 192.168.100.30 . 32100
        }
    }
}
```

### Packet Path (nftables)

```
INGRESS (external or container → VIP):
  packet dst=10.200.0.1:80
    → PREROUTING hook (priority -100)
      → vip_dispatch verdict map lookup: {10.200.0.1, tcp, 80}
        → O(1) hash hit → goto svc_web
          → numgen inc mod 3 → slot 1 → DNAT to 10.100.2.3:8080
    → conntrack creates DNAT entry
    → POSTROUTING hook
      → ct status dnat → masquerade (rewrite src to node IP)
    → packet forwarded to backend

RETURN PATH:
  reply from 10.100.2.3:8080
    → conntrack reverse NAT
      → src rewritten from 10.100.2.3:8080 → 10.200.0.1:80
    → delivered to original client

LOCAL PROCESS → VIP:
  connect(10.200.0.1:80) from host process
    → OUTPUT hook (priority -100)
      → same vip_dispatch lookup → DNAT
    → POSTROUTING → masquerade
```

### Rule Generation Flow (nftables)

```
buildServiceVIPConfigs (agent/dataplane.go)
  ├── factStore.Scan(derived/vip/)              → VIP allocations
  ├── factStore.Scan(derived/vip/*/port)        → service ports
  ├── resolveServiceBackends                    → local (container IP) or remote (host port)
  └── → []ServiceVIPConfig

NftablesDataPlane.ReconcileVIPDataPlane (network/nftables_dataplane.go)
  ├── ensureDummyInterface                      [ip link add cca-vip0]
  ├── buildVIPRuleset(serviceConfigs)
  │   ├── table ip cca {}\n delete table ip cca    ← atomic replace
  │   ├── map vip_dispatch { ... }                 ← empty declaration
  │   ├── chain prerouting { vmap @vip_dispatch }
  │   ├── chain output { vmap @vip_dispatch }
  │   ├── writePostRoutingChain                    ← masquerade for DNAT
  │   ├── FOR EACH service (sorted by name):
  │   │   ├── dispatchEntries += "VIP . proto . port : goto svc_NAME"
  │   │   │   (or "goto rt_NAME" if routeDispatchOverrides has override)
  │   │   └── writeBackendDNATRules → chain svc_NAME { numgen ... }
  │   └── writeVerdictMapWithElements              ← populated dispatch map
  ├── appendRouteChains(&ruleset, lastRouteConfigs)
  │   ├── source-ip: writeSourceIPStickyChain      ← jhash ip saddr
  │   └── cookie: writeCookieProxyChain            ← redirect to :proxyPort
  ├── applyNftablesRuleset
  │   ├── write ruleset to temp file
  │   └── nft -f <temp>                            ← ATOMIC APPLY
  ├── ensureVIPAddress per service                 [ip addr add VIP/32 dev cca-vip0]
  └── removeStaleVIPAddresses                      [ip addr del stale/32]
```

### Key Property: Atomic Replacement

The entire nftables table is deleted and recreated in one `nft -f` transaction.
No partial state. Either the whole ruleset applies or nothing changes.

## Provider 2: iptables

### Kernel Objects

```
nat table:
  PREROUTING ─→ CCA_SERVICES                       (jump, position 1)
  OUTPUT     ─→ CCA_SERVICES                       (jump, position 1)
  POSTROUTING: ! -o lo -m conntrack --ctstate DNAT -j MASQUERADE

  CCA_SERVICES:
    -d 10.200.0.1/32 -p tcp --dport 80 -j CCA_SVC_web
    -d 10.200.0.2/32 -p tcp --dport 5432 -j CCA_SVC_metadata_db
    -d 10.200.0.1/32 -p tcp --dport 80 -j CCA_RT_web_public  ← route override

  CCA_SVC_web:                                       ← round-robin via -m statistic
    -p tcp -m statistic --mode nth --every 3 --packet 0 -j DNAT --to 10.100.1.2:8080
    -p tcp -m statistic --mode nth --every 2 --packet 0 -j DNAT --to 10.100.2.3:8080
    -p tcp -j DNAT --to 192.168.100.30:32100          ← last backend: no statistic

  CCA_SVC_metadata_db:
    -p tcp -j DNAT --to 10.100.1.4:5432               ← single backend

  CCA_RT_web_public:                                   ← source-ip sticky via xt_recent
    -p tcp -m recent --name cca_rt_web_public_0 --rcheck --seconds 1800 -j DNAT --to 10.100.1.2:8080
    -p tcp -m recent --name cca_rt_web_public_1 --rcheck --seconds 1800 -j DNAT --to 10.100.2.3:8080
    -p tcp -m statistic --mode nth --every 2 --packet 0 -m recent --name cca_rt_web_public_0 --set -j DNAT --to 10.100.1.2:8080
    -p tcp -m recent --name cca_rt_web_public_1 --set -j DNAT --to 10.100.2.3:8080
```

### Packet Path (iptables)

```
INGRESS (external or container → VIP):
  packet dst=10.200.0.1:80
    → PREROUTING chain
      → -j CCA_SERVICES
        → -d 10.200.0.1/32 -p tcp --dport 80 -j CCA_SVC_web
          → statistic nth --every 3 --packet 0 → backend 0
          → statistic nth --every 2 --packet 0 → backend 1
          → (last rule, unconditional) → backend 2
        → DNAT to selected backend
    → conntrack creates DNAT entry
    → POSTROUTING: MASQUERADE if DNAT'd

LOCAL PROCESS → VIP:
  connect(10.200.0.1:80)
    → OUTPUT chain → -j CCA_SERVICES → same path
```

### Rule Generation Flow (iptables)

```
IptablesDataPlane.ReconcileVIPDataPlane (network/iptables_dataplane.go)
  ├── ensureGlobalChainAndJumpRules (once)
  │   ├── iptables -t nat -N CCA_SERVICES
  │   ├── iptables -t nat -I PREROUTING 1 -j CCA_SERVICES
  │   ├── iptables -t nat -I OUTPUT 1 -j CCA_SERVICES
  │   └── ensureMasqueradeForDNAT → POSTROUTING MASQUERADE rule
  ├── ensureDummyInterface                     [ip link add cca-vip0]
  ├── FOR EACH service:
  │   ├── ensureVIPAddress(VIP)                [ip addr add VIP/32]
  │   └── reconcileServiceChain(config, CCA_SVC_NAME)
  │       ├── iptables -t nat -N CCA_SVC_NAME (idempotent)
  │       ├── iptables -t nat -F CCA_SVC_NAME (flush = rebuild)
  │       ├── FOR EACH backend (except last):
  │       │   └── -A CCA_SVC_NAME -p tcp -m statistic --mode nth
  │       │       --every <remaining> --packet 0 -j DNAT --to <backend>
  │       ├── Last backend:
  │       │   └── -A CCA_SVC_NAME -p tcp -j DNAT --to <backend>
  │       └── ensureJumpFromMainToService
  │           └── -A CCA_SERVICES -d VIP/32 -p tcp --dport PORT -j CCA_SVC_NAME
  ├── removeStaleServiceChains
  │   ├── removeJumpRuleFromMainChain (up to 20 passes)
  │   └── flushAndDeleteChain
  └── removeStaleVIPAddresses                  [ip addr del]
```

### Key Property: Per-Chain Rebuild

Each service chain is flushed and rebuilt on every reconcile cycle.
The CCA_SERVICES jump rules are idempotent (check-then-add via -C).
Not atomic — transient partial state exists during flush+rebuild.

### Round-Robin Algorithm (iptables statistic module)

```
3 backends → rules in chain:
  Rule 1: --every 3 --packet 0   → match 1 in 3 (33%) → DNAT to backend[0]
  Rule 2: --every 2 --packet 0   → match 1 in 2 (50% of remaining) → backend[1]
  Rule 3: (unconditional)        → all remaining → backend[2]

Effective distribution: 33% / 33% / 33% (equal probability)
```

## Provider 3: eBPF

### Architecture

```
                        ┌───────────────────────────────────┐
                        │         Embedded ELF              │
                        │  (go:embed cca_dataplane.o)       │
                        │                                   │
                        │  5 programs, sharing 5 maps       │
                        └──────┬──────────┬─────────────────┘
                               │          │
                ┌──────────────┘          └──────────────┐
                │                                        │
     ┌──────────┴──────────┐              ┌──────────────┴──────────┐
     │ SOCKET LB (cgroup)  │              │   TC PROGRAMS           │
     │                     │              │                         │
     │ cca_sock4_connect   │              │ cca_tc_dnat             │
     │   (cgroup/connect4) │              │   (tc ingress on cca0)  │
     │ cca_sock4_sendmsg   │              │                         │
     │   (cgroup/sendmsg4) │              │ cca_tc_snat             │
     │ cca_sock4_getpeername│              │   (tc egress on eth0)   │
     │   (cgroup/getpeername4)│            │                         │
     └──────────────────────┘              └─────────────────────────┘
```

### BPF Maps

| Map Name | Type | Max Entries | Key → Value | Purpose |
|---|---|---|---|---|
| `cca_services` | HASH | 4096 | `{vip, port, proto}` → `{svc_id, backend_count, flags, affinity_timeout}` | VIP → service lookup |
| `cca_backends` | HASH | 65536 | `{svc_id, slot}` → `{backend_addr, port}` | Service → backend slot lookup |
| `cca_affinity` | LRU_HASH | 131072 | `{client_addr, svc_id}` → `{backend_addr, port, timestamp}` | Source-IP session affinity cache |
| `cca_reverse_nat` | LRU_HASH | 131072 | `{socket_cookie, backend_addr, backend_port}` → `{vip_addr, vip_port}` | getpeername() VIP transparency |
| `cca_config` | ARRAY | 8 | `index` → `u32` | Node IP, overlay prefix, overlay mask |

Maps are pinned to `/sys/fs/bpf/cca/` for persistence across restarts and
inspection via `bpftool map dump pinned /sys/fs/bpf/cca/cca_services`.

### Packet Path (eBPF — socket LB, host processes)

```
HOST PROCESS → VIP:
  connect(10.200.0.1, 80)
    → cgroup/connect4 hook
      → cca_sock4_connect
        ├── service_key = {vip=10.200.0.1, port=80, proto=TCP}
        ├── bpf_map_lookup_elem(&cca_services, &key) → matched_service
        ├── select_backend(matched_service, socket_cookie)
        │   ├── IF sticky-source-ip flag set:
        │   │   └── bpf_map_lookup_elem(&cca_affinity, {client, svc_id})
        │   │       └── IF cached && not expired → return cached backend
        │   ├── slot = bpf_get_prandom_u32() % backend_count
        │   ├── bpf_map_lookup_elem(&cca_backends, {svc_id, slot}) → backend
        │   └── IF sticky: bpf_map_update_elem(&cca_affinity, ...) → cache
        ├── socket_ctx->user_ip4 = backend.addr
        ├── socket_ctx->user_port = backend.port
        └── bpf_map_update_elem(&cca_reverse_nat, {cookie, backend} → {vip})

  getpeername() on same socket:
    → cgroup/getpeername4 hook
      → cca_sock4_getpeername
        ├── bpf_map_lookup_elem(&cca_reverse_nat, {cookie, current_addr})
        └── IF found: restore socket_ctx to original VIP

  sendto(10.200.0.1, 80) (UDP):
    → cgroup/sendmsg4 hook
      → cca_sock4_sendmsg
        └── same logic as connect4, uses msg_src_ip4 for affinity
```

### Packet Path (eBPF — tc DNAT, container-originated)

```
CONTAINER → VIP (via bridge):
  packet from 10.100.1.2 dst=10.200.0.1:80
    → arrives on cca0 bridge ingress
    → tc ingress hook (TCX)
      → cca_tc_dnat
        ├── parse Ethernet + IP + L4 headers
        ├── bpf_skb_ct_lookup(connection_tuple)
        │   └── IF existing conntrack entry → return TC_ACT_OK (already tracked)
        ├── service_key = {vip=dst_ip, port=dst_port, proto}
        ├── bpf_map_lookup_elem(&cca_services, &key) → matched_service
        ├── select_backend(matched_service, src_ip)
        ├── bpf_skb_ct_alloc(connection_tuple)
        ├── bpf_ct_set_nat_info(entry, backend_addr, backend_port, MANIP_DST)
        └── bpf_ct_insert_entry(entry)
            → kernel nf_nat handles actual packet rewrite + return path

  SUBSEQUENT PACKETS on same flow:
    → ct_lookup hits existing entry → kernel nf_nat rewrites transparently
    → cca_tc_dnat returns TC_ACT_OK immediately (conntrack handles it)
```

### Packet Path (eBPF — tc SNAT, cross-node egress)

```
OVERLAY-SOURCED PACKET → PHYSICAL NIC:
  packet from 10.100.1.2 (overlay) → external destination
    → arrives on eth0 tc egress hook (TCX)
    → cca_tc_snat
      ├── parse Ethernet + IP headers
      ├── is_overlay_source(ip_header->saddr)?
      │   └── (saddr & overlay_mask) == overlay_prefix → yes
      ├── get_node_ip() from cca_config[0]
      ├── IF saddr == node_ip → skip (already correct)
      ├── bpf_l3_csum_replace(check, old_src, node_ip)
      ├── bpf_skb_store_bytes(saddr ← node_ip)
      └── bpf_l4_csum_replace(tcp/udp check, old_src, node_ip)
          → packet leaves with node IP as source
```

### Map Update Flow (eBPF)

```
EbpfDataPlane.ReconcileVIPDataPlane (network/ebpf_dataplane.go)
  ├── buildDesiredVIPState(serviceConfigs)
  │   ├── FOR EACH service (sorted by name):
  │   │   ├── assignOrLookupServiceID(name) → monotonic u32 ID
  │   │   ├── serviceKey = {ipToUint32(VIP), portToNetworkOrder(port), proto}
  │   │   ├── serviceValue = {svc_id, backend_count, flags=0, timeout=0}
  │   │   ├── FOR EACH backend:
  │   │   │   └── backendKey = {svc_id, slot=index}
  │   │   │       backendValue = {ipToUint32(addr), portToNetworkOrder(port)}
  │   │   └── collect into desiredServices, desiredBackends
  │   └── → (map[ServiceKey]ServiceValue, map[BackendKey]BackendValue)
  │
  ├── applyDesiredBackendEntries               ← backends FIRST
  │   └── FOR EACH desired: backendMap.Put(key, value)
  ├── applyDesiredServiceEntries               ← services SECOND
  │   └── FOR EACH desired: serviceMap.Put(key, value)
  ├── removeStaleServiceEntries                ← stale services THIRD
  │   ├── serviceMap.Iterate() → find keys not in desired
  │   └── serviceMap.Delete(staleKey)
  └── removeStaleBackendEntries                ← stale backends LAST
      ├── backendMap.Iterate() → find keys not in desired
      └── backendMap.Delete(staleKey)

ORDER INVARIANT:
  Add backends → update services → remove stale services → remove stale backends
  Services always reference valid backends. No dangling references.
```

### eBPF Initialization Flow

```
EbpfDataPlane.Initialize (agent startup)
  ├── rlimit.RemoveMemlock()                    [BPF memlock limit]
  ├── os.MkdirAll(/sys/fs/bpf/cca)             [pin directory]
  ├── ebpf.LoadCollectionSpecFromReader(ELF)    [parse embedded ELF]
  ├── ebpf.NewCollection(spec)                  [load all programs + maps]
  ├── extractAndPinMaps()
  │   └── FOR EACH {cca_services, cca_backends, cca_affinity, cca_reverse_nat}:
  │       └── map.Pin(/sys/fs/bpf/cca/<name>)
  ├── attachCgroupPrograms()
  │   └── FOR EACH {connect4, sendmsg4, getpeername4}:
  │       └── link.AttachCgroup({Path: /sys/fs/cgroup, Program: prog})
  ├── scanExistingServiceIDs()                  [resume ID allocation after restart]
  │   └── serviceMap.Iterate() → find max service_id
  └── extractTCPrograms()
      ├── tcProgram = collection.Programs["cca_tc_dnat"]
      └── snatProgram = collection.Programs["cca_tc_snat"]

EbpfDataPlane.RegisterNode (after agent discovers node IP + pod CIDR)
  ├── discoverBridgeInterfaceBySubnet(podCIDR)  [find cca0 by subnet IP]
  ├── attachTCProgramToBridge(cca0)
  │   └── link.AttachTCX({Interface: cca0, Attach: TCXIngress})
  ├── populateConfigMap(nodeIP, podCIDR)
  │   ├── configMap.Put(INDEX_NODE_IP, nodeIP)
  │   ├── configMap.Put(INDEX_OVERLAY_PREFIX, prefix)
  │   └── configMap.Put(INDEX_OVERLAY_MASK, mask)
  ├── detectPhysicalInterfaceForNodeIP(nodeIP)  [find eth0/ens3/bond0]
  └── attachSNATToPhysicalNIC
      └── link.AttachTCX({Interface: eth0, Attach: TCXEgress})

EbpfDataPlane.RetryBridgeAttachment (called after first container starts)
  └── IF tcLink == nil AND tcProgram != nil:
      ├── discoverBridgeInterfaceBySubnet(podCIDR)
      └── attachTCProgramToBridge(cca0)
```

## Cross-Node Packet Path (VXLAN)

### Setup: VXLAN Mesh

```
Node-A (192.168.100.20, subnet 10.100.1.0/24):
  ip link add cca-vxlan0 type vxlan id 42 local 192.168.100.20 dstport 4789 dev eth0
  ip link set cca-vxlan0 mtu 1450
  ip link set cca-vxlan0 up

  FOR EACH peer node-B (192.168.100.30, subnet 10.100.2.0/24):
    bridge fdb append 00:00:00:00:00:00 dev cca-vxlan0 dst 192.168.100.30
    ip route replace 10.100.2.0/24 dev cca-vxlan0
```

### Cross-Node VIP → Backend Flow

```
Container on Node-A connects to VIP 10.200.0.1:80
  Backend selected: 10.100.2.3:8080 (on Node-B)

Node-A data plane has:
  LOCAL backends  → container IP:containerPort (e.g. 10.100.1.2:8080)
  REMOTE backends → node advertise address:hostPort (e.g. 192.168.100.30:32100)

STEP 1: DNAT on Node-A
  VIP 10.200.0.1:80 → DNAT to 192.168.100.30:32100 (remote host port)
  Source: 10.100.1.2 (container) → MASQUERADE to 192.168.100.20 (node IP)

STEP 2: Node-A → Node-B (physical LAN)
  packet: src=192.168.100.20:ephemeral dst=192.168.100.30:32100
  → routed via physical network (NOT VXLAN — uses host port, not overlay IP)

STEP 3: Node-B receives on host port
  containerd host port mapping: 192.168.100.30:32100 → 10.100.2.3:8080
  → packet delivered to container

STEP 4: Return path
  reply: src=10.100.2.3:8080 → containerd un-NATs to 192.168.100.30:32100
  → physical network back to 192.168.100.20
  → Node-A conntrack un-NATs to 10.200.0.1:80
  → container on Node-A receives reply
```

### Cross-Node Overlay Flow (container-to-container via VXLAN)

```
Container 10.100.1.2 on Node-A talks to container 10.100.2.3 on Node-B:

STEP 1: Packet from container
  src=10.100.1.2 dst=10.100.2.3
  → hits cca0 bridge → route lookup
  → ip route: 10.100.2.0/24 dev cca-vxlan0
  → bridge fdb: 00:00:00:00:00:00 dev cca-vxlan0 dst 192.168.100.30

STEP 2: VXLAN encapsulation
  outer: src=192.168.100.20:random dst=192.168.100.30:4789 UDP
  VXLAN header: VNI=42
  inner: src=10.100.1.2 dst=10.100.2.3

STEP 3: Node-B receives UDP 4789
  → VXLAN decapsulation (kernel)
  → inner packet: src=10.100.1.2 dst=10.100.2.3
  → cca0 bridge → veth → container 10.100.2.3
```

## VIP Lifecycle

```
1. ALLOCATE
   VIPController (server-side) scans desired services, allocates VIP from --vip-cidr pool
   → writes derived/vip/{service} = <VIP address>
   → writes derived/vip/{service}/port = <port>

2. DISCOVER
   DataPlaneReconciler.buildServiceVIPConfigs (agent-side, every 1s)
   → factStore.Scan(derived/vip/) → finds VIP + port
   → resolveServiceBackends → finds healthy backends

3. PROGRAM
   Provider.ReconcileVIPDataPlane(serviceConfigs)
   ├── nftables: atomic table replace (nft -f)
   ├── iptables: per-chain flush+rebuild
   └── eBPF: map put/delete operations

4. INTERFACE
   Provider.ensureVIPAddress(VIP)
   → ip addr add <VIP>/32 dev cca-vip0
   → kernel now accepts packets to this VIP

5. STALE CLEANUP
   Provider.removeStaleVIPAddresses(desired)
   → ip addr del <stale_VIP>/32 dev cca-vip0
   Provider removes stale chains/entries for deleted services

6. AGENT SHUTDOWN
   Provider.Cleanup()
   ├── nftables: delete table ip cca
   ├── iptables: remove all CCA_* chains + jump rules + MASQUERADE
   ├── eBPF: detach all programs, unpin maps, close collection
   └── all: ip link del cca-vip0
```

## Backend Resolution (agent/dataplane.go)

```
resolveServiceBackends(serviceName, staleNodeSet)
  ├── factStore.Scan(derived/endpoint/{service}/)
  │   → endpoint facts: key=.../instanceID/port value=IP:port
  │
  ├── FOR EACH endpoint:
  │   ├── factStore.Get(observed/instance/{id}/node) → nodeID
  │   ├── IF staleNodeSet[nodeID] → SKIP (node heartbeat > 30s stale)
  │   │
  │   ├── IF nodeID == thisNode:
  │   │   └── resolveLocalBackend
  │   │       ├── factStore.Get(observed/instance/{id}/ip) → container IP
  │   │       └── → DataPlaneBackend{containerIP, containerPort}
  │   │
  │   └── IF nodeID != thisNode:
  │       └── resolveRemoteBackend
  │           ├── factStore.Get(observed/node/{nodeID}/address) → node LAN IP
  │           ├── resolveHostPort(instanceID, containerPort)
  │           │   ├── TRY: observed/instance/{id}/host_port/{containerPort}
  │           │   └── FALLBACK: observed/instance/{id}/host_port (legacy)
  │           └── → DataPlaneBackend{nodeIP, hostPort}
  │
  └── → []DataPlaneBackend

LOCAL backend:  direct bridge path,    no NAT overhead
REMOTE backend: host port mapping,     DNAT to nodeIP:hostPort on LAN
```

## Provider Comparison

| Aspect | nftables | iptables | eBPF |
|---|---|---|---|
| **VIP lookup** | O(1) verdict map | O(N) chain walk | O(1) hash map |
| **Backend selection** | `numgen inc mod N` | `-m statistic --mode nth` | `bpf_get_prandom_u32() % N` |
| **Atomicity** | Full table replace (`nft -f`) | Per-chain flush+rebuild | Per-entry map put/delete |
| **Session affinity** | `jhash ip saddr` | `xt_recent` module | `cca_affinity` LRU map |
| **SNAT** | `ct status dnat masquerade` | `--ctstate DNAT -j MASQUERADE` | `cca_tc_snat` on NIC egress |
| **Hook point** | netfilter hooks (prerouting, output, postrouting) | same | cgroup syscall hooks + tc |
| **Kernel requirement** | nftables support (5.x+) | iptables-legacy or nft backend | kernel 6.6+ (TCX, ct helpers) |
| **Persistence** | stateless (reconstructed) | stateless (reconstructed) | maps pinned at `/sys/fs/bpf/cca/` |
| **Cleanup** | `delete table ip cca` | flush+delete each chain | detach programs, unpin maps |
| **Cookie sticky** | redirect to cookie proxy | redirect to cookie proxy | NOT SUPPORTED (uses cookie proxy) |
| **Selection flag** | `--dataplane nftables` | `--dataplane iptables` | `--dataplane ebpf` (build tag) |

## Store Keys Read by Data Plane Reconciler

| Key Pattern | Read In | Purpose |
|---|---|---|
| `derived/vip/{service}` | buildServiceVIPConfigs | VIP address for service |
| `derived/vip/{service}/port` | buildServiceVIPConfigs | VIP port for service |
| `derived/endpoint/{service}/{instance}/{port}` | resolveServiceBackends | Endpoint IP:port |
| `observed/instance/{id}/node` | resolveServiceBackends | Which node runs instance |
| `observed/instance/{id}/ip` | resolveLocalBackend | Container bridge IP |
| `observed/instance/{id}/host_port/{port}` | resolveHostPort | Host port mapping |
| `observed/node/{nodeID}/address` | resolveRemoteBackend | Node advertise address |
| `lease/node/{nodeID}` | identifyStaleNodes | Node heartbeat timestamp |
| `desired/route/{name}/*` | buildRouteDataPlaneConfigs | Route definitions |
| `derived/route/{name}/backends` | buildRouteDataPlaneConfigs | Route backend list |
