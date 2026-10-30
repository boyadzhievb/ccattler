# CCattler eBPF Data Plane Graph

Machine-readable reference for the eBPF data plane provider. Purpose: understand
every BPF program, map, packet path, conntrack kfunc interaction, and Go-side
map reconciliation without reading source files. Load this file instead.

For the nftables and iptables providers, see DATA_PLANE_GRAPH.md.

## Provider Registration

```
network/ebpf_register.go:15
  init() → RegisterDataPlaneFactory("ebpf", func(...) { NewEbpfDataPlane() })

Build tag: //go:build ebpf
  Only compiled when: go build -tags ebpf
  Without tag: "ebpf" factory absent from registry, agent falls back to nftables/iptables
```

## Source Files

| File | Purpose |
|---|---|
| `network/ebpf_dataplane.go` | Go-side map reconciliation, initialization, cleanup |
| `network/ebpf_types.go` | Go struct types mirroring C structs, constants, byte-order helpers |
| `network/ebpf_embed.go` | `go:embed` of compiled ELF (`network/ebpf/cca_dataplane.o`) |
| `network/ebpf_gen.go` | `go:generate` directive for `bpf2go` (alternative build pipeline) |
| `network/ebpf_register.go` | Factory registration via `init()` under `ebpf` build tag |
| `network/dataplane.go` | `DataPlaneProvider` interface + extension interfaces |
| `network/dataplane_registry.go` | `RegisterDataPlaneFactory` / `LookupDataPlaneFactory` |
| `network/ebpf/cca_dataplane.c` | Unified BPF C source — all 5 programs |
| `network/ebpf/cca_maps.h` | BPF map definitions and C struct layouts |
| `network/ebpf/cca_helpers.h` | `select_backend()` helper — random slot + affinity |
| `network/ebpf/cca_ct_helpers.h` | Conntrack kfunc declarations for tc programs |
| `network/ebpf/cca_dataplane.o` | Pre-compiled ELF — committed for macOS cross-builds |
| `scripts/build-ebpf.sh` | Docker-based BPF compilation pipeline |

## Architecture Overview

```
┌────────────────────────────────────────────────────────────────────────┐
│                        Go Binary (cca agent)                          │
│                                                                       │
│  EbpfDataPlane                                                        │
│  ├── Initialize() ─── load embedded ELF ──→ ebpf.Collection          │
│  │                    pin maps to /sys/fs/bpf/cca/                    │
│  │                    attach cgroup programs                          │
│  │                    extract tc programs                             │
│  │                                                                    │
│  ├── RegisterNode() ─ discover bridge (cca0) ──→ attach tc DNAT      │
│  │                    discover phys NIC (eth0) ──→ attach tc SNAT     │
│  │                    populate cca_config map                         │
│  │                                                                    │
│  ├── ReconcileVIPDataPlane() ─ build desired state from facts         │
│  │                             apply to cca_services + cca_backends   │
│  │                                                                    │
│  ├── ReconcileRouteDataPlane() ─ enable sticky affinity flags         │
│  │                                                                    │
│  └── Cleanup() ─── detach all programs, unpin maps, close FDs        │
└────────────────────────────────────────────────────────────────────────┘
                            │
                            │  cilium/ebpf library
                            ▼
┌────────────────────────────────────────────────────────────────────────┐
│                         Linux Kernel (BPF subsystem)                  │
│                                                                       │
│  CGROUP PROGRAMS (root cgroup /sys/fs/cgroup)                         │
│  ├── cca_sock4_connect     (cgroup/connect4)   ─ TCP VIP rewrite     │
│  ├── cca_sock4_sendmsg     (cgroup/sendmsg4)   ─ UDP VIP rewrite     │
│  └── cca_sock4_getpeername (cgroup/getpeername4) ─ reverse VIP lookup │
│                                                                       │
│  TC PROGRAMS                                                          │
│  ├── cca_tc_dnat  (TCX ingress on cca0 bridge) ─ conntrack DNAT      │
│  └── cca_tc_snat  (TCX egress on eth0 NIC)     ─ overlay SNAT        │
│                                                                       │
│  BPF MAPS (shared by all 5 programs, single ELF compilation unit)     │
│  ├── cca_services    (HASH)     ─ VIP → service metadata             │
│  ├── cca_backends    (HASH)     ─ {svc_id, slot} → backend endpoint  │
│  ├── cca_affinity    (LRU_HASH) ─ {client, svc_id} → cached backend  │
│  ├── cca_reverse_nat (LRU_HASH) ─ {cookie, backend} → original VIP   │
│  └── cca_config      (ARRAY)    ─ node IP, overlay CIDR              │
└────────────────────────────────────────────────────────────────────────┘
```

### Comparison with Other Providers

| Property | nftables | iptables | eBPF |
|---|---|---|---|
| Hook point (host → VIP) | PREROUTING/OUTPUT nat | PREROUTING/OUTPUT nat | cgroup connect4/sendmsg4 |
| Hook point (container → VIP) | PREROUTING nat | PREROUTING nat | tc ingress on bridge |
| Hook point (SNAT egress) | POSTROUTING masquerade | POSTROUTING masquerade | tc egress on physical NIC |
| LB algorithm | `numgen inc mod N` | statistic `--mode nth` | `bpf_get_prandom_u32() % N` |
| Session affinity | `jhash ip saddr` | `xt_recent` | LRU map + timestamp TTL |
| Getpeername transparency | No | No | Yes (reverse_nat map) |
| Packet rewrite (host) | Kernel conntrack | Kernel conntrack | Socket-level (no packet touch) |
| Packet rewrite (container) | Kernel conntrack | Kernel conntrack | Conntrack kfunc DNAT entry |
| Update atomicity | Full table replace | Per-chain flush+rebuild | Per-map-entry put/delete |
| Build requirement | nft userspace tool | iptables userspace tool | Embedded ELF (zero deps) |
| Kernel version | 4.10+ (nftables) | 2.6+ | 6.1+ (conntrack kfuncs) |
| Build tag | (default) | (default) | `-tags ebpf` |

## Program Inventory

### Program 1: cca_sock4_connect

```
Source:    network/ebpf/cca_dataplane.c:39
Section:  SEC("cgroup/connect4")
Type:     BPF_PROG_TYPE_CGROUP_SOCK_ADDR
Attach:   AttachCGroupInet4Connect → /sys/fs/cgroup
Hook:     Intercepts connect() syscalls on IPv4 TCP sockets
Purpose:  Rewrite VIP destination to selected backend at socket level
```

**Logic:**
```
1. Build service_key from socket context: {user_ip4, user_port, protocol}
2. Lookup cca_services → if miss: return 1 (passthrough)
3. client_identity = (u32)bpf_get_socket_cookie()  ← NOTE: msg_src_ip4 not readable in connect4
4. select_backend(service, client_identity) → chosen backend
5. Rewrite: socket_ctx->user_ip4 = backend.addr
            socket_ctx->user_port = backend.port
6. Record reverse NAT: {cookie, backend} → {original VIP}
7. Return 1 (allow)
```

### Program 2: cca_sock4_sendmsg

```
Source:    network/ebpf/cca_dataplane.c:80
Section:  SEC("cgroup/sendmsg4")
Type:     BPF_PROG_TYPE_CGROUP_SOCK_ADDR
Attach:   AttachCGroupUDP4Sendmsg → /sys/fs/cgroup
Hook:     Intercepts sendto()/sendmsg() on IPv4 UDP sockets
Purpose:  Same as connect4 but for connectionless UDP datagrams
```

**Logic:**
Same as connect4 except:
- Uses `socket_ctx->msg_src_ip4` for affinity (readable in sendmsg4, unlike connect4)
- Per-datagram translation (no persistent socket rewrite)

### Program 3: cca_sock4_getpeername

```
Source:    network/ebpf/cca_dataplane.c:120
Section:  SEC("cgroup/getpeername4")
Type:     BPF_PROG_TYPE_CGROUP_SOCK_ADDR
Attach:   AttachCgroupInet4GetPeername → /sys/fs/cgroup
Hook:     Intercepts getpeername() syscall
Purpose:  Return original VIP address instead of translated backend
```

**Logic:**
```
1. Build reverse_nat_key: {bpf_get_socket_cookie(), user_ip4, user_port}
2. Lookup cca_reverse_nat → if miss: return 1 (passthrough)
3. Restore: socket_ctx->user_ip4 = vip_addr
            socket_ctx->user_port = vip_port
4. Return 1
```

### Program 4: cca_tc_dnat

```
Source:    network/ebpf/cca_dataplane.c:181
Section:  SEC("tc")
Type:     BPF_PROG_TYPE_SCHED_CLS
Attach:   link.AttachTCX → cca0 bridge, TCXIngress
Hook:     tc ingress on container bridge — catches container→VIP traffic
Purpose:  Create conntrack DNAT entries for container-originated flows
```

**Logic:**
```
1. Parse Ethernet → IPv4 → TCP/UDP headers (bounds checks for verifier)
2. Build bpf_sock_tuple from packet headers
3. bpf_skb_ct_lookup(tuple)
   └── IF existing conntrack entry → bpf_ct_release(), return TC_ACT_OK
4. Build service_key from {dst_ip, dst_port, protocol}
5. Lookup cca_services → if miss: return TC_ACT_OK
6. select_backend(service, src_ip) → chosen backend
7. bpf_skb_ct_alloc(tuple) → new conntrack entry
8. bpf_ct_set_nat_info(entry, backend_addr, backend_port, NF_NAT_MANIP_DST)
9. bpf_ct_insert_entry(entry) → committed to conntrack table
   └── Kernel nf_nat handles actual packet rewrite + return path
10. Return TC_ACT_OK (program does NOT modify packets directly)
```

**Key insight:** The tc DNAT program does NOT rewrite packets. It programs the
kernel's conntrack table with DNAT entries, and the kernel's `nf_nat` module
performs the actual packet rewriting. This means:
- First packet: conntrack entry created by BPF, nf_nat rewrites
- Subsequent packets: conntrack hit, nf_nat rewrites (BPF returns immediately)
- Return path: conntrack reverse NAT automatically applied by kernel

### Program 5: cca_tc_snat

```
Source:    network/ebpf/cca_dataplane.c:315
Section:  SEC("tc")
Type:     BPF_PROG_TYPE_SCHED_CLS
Attach:   link.AttachTCX → eth0 (physical NIC), TCXEgress
Hook:     tc egress on physical NIC — catches overlay-sourced outbound traffic
Purpose:  Replace overlay source IPs with node's advertise address (masquerade)
```

**Logic:**
```
1. Parse Ethernet → IPv4 headers
2. is_overlay_source(saddr)?
   └── (saddr & cca_config[OVERLAY_MASK]) == cca_config[OVERLAY_PREFIX]
   └── If no: return TC_ACT_OK (not overlay traffic)
3. get_node_ip() from cca_config[NODE_IP]
   └── If 0: return TC_ACT_OK (config not populated)
4. If saddr == node_ip: return TC_ACT_OK (already correct)
5. bpf_l3_csum_replace(ip_checksum, old_src, node_ip)
6. bpf_skb_store_bytes(saddr ← node_ip)
7. bpf_l4_csum_replace(tcp/udp checksum, old_src, node_ip)
8. Return TC_ACT_OK
```

**Key difference from tc DNAT:** The SNAT program directly modifies packet bytes
using BPF helper functions, replacing the nftables `ct status dnat masquerade`
rule. It does not use conntrack kfuncs.

## Map Inventory

### cca_services

```
Source:       network/ebpf/cca_maps.h:72
Type:         BPF_MAP_TYPE_HASH
Max entries:  4096
Pin path:     /sys/fs/bpf/cca/cca_services
```

| Field | C Type | Go Type | Size | Description |
|---|---|---|---|---|
| **Key** | | `EbpfServiceKey` | 8 bytes | |
| `.vip_addr` | `__u32` | `uint32` | 4 | VIP IP (native endian, same as `user_ip4`) |
| `.port` | `__u16` | `uint16` | 2 | Service port (network byte order via `portToNetworkOrder`) |
| `.protocol` | `__u8` | `uint8` | 1 | IANA protocol (6=TCP, 17=UDP) |
| `.pad` | `__u8` | `uint8` | 1 | Alignment to 8 bytes |
| **Value** | | `EbpfServiceValue` | 12 bytes | |
| `.service_id` | `__u32` | `uint32` | 4 | Monotonic ID, starts at 1, survives restart |
| `.backend_count` | `__u16` | `uint16` | 2 | Number of active backend slots |
| `.flags` | `__u16` | `uint16` | 2 | `0x01` = STICKY_SOURCE_IP |
| `.affinity_timeout_seconds` | `__u32` | `uint32` | 4 | Affinity TTL (default 300s = 5min) |

### cca_backends

```
Source:       network/ebpf/cca_maps.h:80
Type:         BPF_MAP_TYPE_HASH
Max entries:  65536
Pin path:     /sys/fs/bpf/cca/cca_backends
```

| Field | C Type | Go Type | Size | Description |
|---|---|---|---|---|
| **Key** | | `EbpfBackendKey` | 8 bytes | |
| `.service_id` | `__u32` | `uint32` | 4 | Service identifier |
| `.slot` | `__u16` | `uint16` | 2 | Backend index (0 to backend_count-1) |
| `.pad` | `__u16` | `uint16` | 2 | Alignment |
| **Value** | | `EbpfBackendValue` | 8 bytes | |
| `.backend_addr` | `__u32` | `uint32` | 4 | Backend IP (native endian) |
| `.port` | `__u16` | `uint16` | 2 | Backend port (network byte order) |
| `.pad` | `__u16` | `uint16` | 2 | Alignment |

### cca_affinity

```
Source:       network/ebpf/cca_maps.h:89
Type:         BPF_MAP_TYPE_LRU_HASH
Max entries:  131072
Pin path:     /sys/fs/bpf/cca/cca_affinity
Not pinned:   LRU maps are eviction-based, read-only from Go side
```

| Field | C Type | Go Type | Size | Description |
|---|---|---|---|---|
| **Key** | | `EbpfAffinityKey` | 8 bytes | |
| `.client_addr` | `__u32` | `uint32` | 4 | Client source IP |
| `.service_id` | `__u32` | `uint32` | 4 | Service identifier |
| **Value** | | `EbpfAffinityValue` | 16 bytes | |
| `.backend_addr` | `__u32` | `uint32` | 4 | Cached backend IP |
| `.port` | `__u16` | `uint16` | 2 | Cached backend port |
| `.pad` | `__u16` | `uint16` | 2 | Alignment |
| `.timestamp` | `__u64` | `uint64` | 8 | `bpf_ktime_get_ns()` when created |

**Eviction:** LRU — kernel evicts least-recently-used entries when map is full.
**Expiry:** BPF program checks `timestamp + (timeout * 1e9) > bpf_ktime_get_ns()`.
Go side does not manage this map — BPF programs read/write it autonomously.

### cca_reverse_nat

```
Source:       network/ebpf/cca_maps.h:99
Type:         BPF_MAP_TYPE_LRU_HASH
Max entries:  131072
Pin path:     /sys/fs/bpf/cca/cca_reverse_nat
```

| Field | C Type | Go Type | Size | Description |
|---|---|---|---|---|
| **Key** | | `EbpfReverseNatKey` | 16 bytes | |
| `.socket_cookie` | `__u64` | `uint64` | 8 | `bpf_get_socket_cookie()` — unique per socket |
| `.backend_addr` | `__u32` | `uint32` | 4 | Backend IP |
| `.backend_port` | `__u16` | `uint16` | 2 | Backend port |
| `.pad` | `__u16` | `uint16` | 2 | Alignment |
| **Value** | | `EbpfReverseNatValue` | 8 bytes | |
| `.vip_addr` | `__u32` | `uint32` | 4 | Original VIP address |
| `.vip_port` | `__u16` | `uint16` | 2 | Original VIP port |
| `.pad` | `__u16` | `uint16` | 2 | Alignment |

**Purpose:** Enables getpeername() transparency — applications see the VIP, not
the translated backend address. Keyed by socket cookie to avoid collisions when
different services share the same backend endpoint.

### cca_config

```
Source:       network/ebpf/cca_maps.h:108
Type:         BPF_MAP_TYPE_ARRAY
Max entries:  8
Not pinned:   Written by Go on RegisterNode, read by tc SNAT program
```

| Index | Constant | Go Constant | Content |
|---|---|---|---|
| 0 | `CCA_CONFIG_NODE_IP` | `ebpfConfigIndexNodeIP` | Node advertise address (NBO) |
| 1 | (reserved) | — | Unused |
| 2 | `CCA_CONFIG_OVERLAY_PREFIX` | `ebpfConfigIndexOverlayPrefix` | Overlay CIDR network (NBO) |
| 3 | `CCA_CONFIG_OVERLAY_MASK` | `ebpfConfigIndexOverlayMask` | Overlay CIDR netmask (NBO) |
| 4-7 | — | — | Reserved for future use |

## Packet Paths

### Path 1: Host Process → VIP (Socket LB)

```
Application on node calls connect(10.200.0.1, 80):

  ┌────────────┐
  │ Application │
  │ connect()  │
  └──────┬─────┘
         │ syscall
         ▼
  ┌──────────────────────────────────────────────────┐
  │ cca_sock4_connect (cgroup/connect4)              │
  │                                                  │
  │  key = {vip=10.200.0.1, port=80, proto=TCP}     │
  │  cca_services[key] → {svc_id=3, count=2}        │
  │  cookie = bpf_get_socket_cookie()                │
  │  slot = prandom % 2 → 1                         │
  │  cca_backends[{3,1}] → {10.100.2.3, 8080}       │
  │                                                  │
  │  socket.user_ip4  ← 10.100.2.3                  │
  │  socket.user_port ← 8080                        │
  │  cca_reverse_nat[{cookie, 10.100.2.3, 8080}]    │
  │    ← {10.200.0.1, 80}                           │
  └──────────────────────────────────────────────────┘
         │
         ▼
  TCP SYN to 10.100.2.3:8080 (VIP never hits the wire)
  → routed via overlay or physical network normally
```

**No packet rewrite.** The socket destination is rewritten before the kernel
creates TCP state. No conntrack, no NAT rule evaluation, no checksum fixups.
This is why socket LB is faster than DNAT — zero per-packet overhead.

### Path 2: Container → VIP (TC Bridge DNAT)

```
Container 10.100.1.2 connects to VIP 10.200.0.1:80:

  ┌───────────────┐
  │ Container      │
  │ 10.100.1.2     │
  │ connect(VIP)   │
  └───────┬───────┘
          │ veth pair
          ▼
  ┌─── cca0 bridge (tc ingress hook) ──────────────────────────┐
  │ cca_tc_dnat                                                │
  │                                                            │
  │  parse: ETH → IP(src=10.100.1.2 dst=10.200.0.1) → TCP:80  │
  │                                                            │
  │  bpf_skb_ct_lookup({src,dst,sport,dport})                  │
  │  └── miss (new connection)                                 │
  │                                                            │
  │  cca_services[{10.200.0.1, 80, TCP}]                       │
  │  → {svc_id=3, count=2}                                    │
  │                                                            │
  │  select_backend(svc, src_ip=10.100.1.2)                    │
  │  → {10.100.2.3, 8080}                                     │
  │                                                            │
  │  bpf_skb_ct_alloc(tuple)     ← allocate conntrack entry   │
  │  bpf_ct_set_nat_info(entry,                                │
  │      addr=10.100.2.3, port=8080, MANIP_DST)               │
  │  bpf_ct_insert_entry(entry)  ← commit to conntrack table  │
  │  bpf_ct_release(entry)                                     │
  │                                                            │
  │  return TC_ACT_OK  (packet unmodified by BPF)              │
  └────────────────────────────────────────────────────────────┘
          │
          ▼
  Kernel nf_nat sees new conntrack DNAT entry
  → rewrites dst: 10.200.0.1:80 → 10.100.2.3:8080
  → POSTROUTING: masquerade src to node IP
  → packet forwarded to backend

  SUBSEQUENT PACKETS (same TCP flow):
  → bpf_skb_ct_lookup() hits → BPF returns immediately
  → kernel nf_nat rewrites from conntrack state

  RETURN PATH:
  → kernel conntrack reverse NAT: src 10.100.2.3:8080 → 10.200.0.1:80
  → delivered back to container via bridge
```

### Path 3: Overlay SNAT (Physical NIC Egress)

```
Container 10.100.1.2 sends packet to external destination:

  ┌───────────────┐
  │ Container      │
  │ 10.100.1.2     │
  └───────┬───────┘
          │ routed to physical NIC
          ▼
  ┌─── eth0 (tc egress hook) ─────────────────────────────────┐
  │ cca_tc_snat                                               │
  │                                                           │
  │  parse: ETH → IP(src=10.100.1.2)                          │
  │                                                           │
  │  is_overlay_source(10.100.1.2)?                            │
  │  └── (10.100.1.2 & 255.255.0.0) == 10.100.0.0 → YES     │
  │                                                           │
  │  get_node_ip() → 192.168.100.20                           │
  │                                                           │
  │  10.100.1.2 ≠ 192.168.100.20 → rewrite:                   │
  │                                                           │
  │  bpf_l3_csum_replace(ip_check, 10.100.1.2, 192.168.100.20)│
  │  bpf_skb_store_bytes(ip.saddr ← 192.168.100.20)           │
  │  bpf_l4_csum_replace(l4_check, 10.100.1.2, 192.168.100.20)│
  │                                                           │
  │  return TC_ACT_OK                                         │
  └───────────────────────────────────────────────────────────┘
          │
          ▼
  Packet leaves NIC with src=192.168.100.20 (node IP)
  → remote host sees node IP, not overlay IP
```

## Conntrack Kfuncs (Kernel 6.1+)

The tc DNAT program uses kernel conntrack kfuncs instead of modifying packets
directly. This integrates with the kernel's existing NAT infrastructure.

```
Declared in: network/ebpf/cca_ct_helpers.h
```

| Kfunc | Kernel Version | Purpose |
|---|---|---|
| `bpf_skb_ct_lookup` | 5.18+ | Find existing conntrack entry for a tuple |
| `bpf_skb_ct_alloc` | 5.18+ | Allocate new conntrack entry (not yet committed) |
| `bpf_ct_set_nat_info` | 6.1+ | Record DNAT target on allocated entry |
| `bpf_ct_insert_entry` | 5.18+ | Commit entry to conntrack hash table |
| `bpf_ct_release` | 5.18+ | Release reference (must call once per non-NULL lookup/insert) |
| `bpf_ct_set_timeout` | 5.18+ | Optional custom timeout (not currently used) |

**Types required for kfunc resolution (must match kernel BTF):**

```c
struct nf_conn        { unsigned int __opaque; };     // opaque handle
union nf_inet_addr    { __u32 all[4]; __be32 ip; };   // IPv4 NAT target
struct bpf_ct_opts    { __s32 netns_id; __s32 error;   // 12 bytes
                        __u8 l4proto; __u8 dir;
                        __u8 reserved[2]; };
enum nf_nat_manip_type { NF_NAT_MANIP_SRC=0, NF_NAT_MANIP_DST=1 };
```

### Why Conntrack Kfuncs Instead of Direct Packet Rewrite?

```
Direct rewrite (what tc SNAT does):
  + Simple: bpf_skb_store_bytes + checksum fixup
  - No return path handling: must also catch reply packets and reverse
  - Stateless: every packet must be processed by BPF

Conntrack kfuncs (what tc DNAT does):
  + Return path handled automatically by kernel nf_nat
  + Subsequent packets handled by conntrack (BPF returns immediately)
  + Integrates with netfilter: iptables/nftables see the connection
  - Requires kernel 6.1+ for bpf_ct_set_nat_info
  - More complex BPF code (alloc → set_nat → insert → release)
```

## Backend Selection Algorithm

```
Source: network/ebpf/cca_helpers.h:18  (select_backend)
```

```
select_backend(service, client_addr):
  │
  ├── IF service.flags & STICKY_SOURCE_IP:
  │   ├── affinity_key = {client_addr, service_id}
  │   ├── cached = cca_affinity[affinity_key]
  │   ├── IF cached AND (ktime_ns < cached.timestamp + timeout*1e9):
  │   │   └── RETURN cached backend  (affinity HIT)
  │   └── (expired or miss → fall through to random)
  │
  ├── IF backend_count == 0: RETURN -1 (error)
  │
  ├── slot = bpf_get_prandom_u32() % backend_count
  │   └── (uniform random, not round-robin — avoids shared counter)
  │
  ├── backend = cca_backends[{service_id, slot}]
  │   └── IF miss: RETURN -1 (error — map inconsistency)
  │
  ├── IF STICKY_SOURCE_IP:
  │   └── cca_affinity[{client_addr, service_id}] ← {backend, ktime_ns}
  │
  └── RETURN backend

NOTE on connect4 affinity:
  msg_src_ip4 is NOT readable in cgroup/connect4 (kernel restriction).
  connect4 uses (u32)socket_cookie as client_addr instead.
  sendmsg4 uses msg_src_ip4 (readable in sendmsg4).
```

## Session Affinity (Sticky Sessions)

```
Enabled by:  route with sticky-method "source-ip"
Mechanism:   ReconcileRouteDataPlane sets STICKY_SOURCE_IP flag + timeout

Go path (network/ebpf_dataplane.go:480):
  ReconcileRouteDataPlane
  └── FOR EACH route with StickyMethod == "source-ip":
      └── enableStickyAffinityForService
          ├── serviceMap.Lookup(service_key) → value
          ├── value.Flags |= 0x01 (STICKY_SOURCE_IP)
          ├── value.AffinityTimeoutSeconds = 300 (5 minutes)
          └── serviceMap.Put(service_key, updated_value)

BPF path:
  select_backend checks affinity map BEFORE random selection
  └── Cache hit + not expired → return cached backend
  └── Miss or expired → random slot → update cache
```

### Constants

| Constant | Value | File | Description |
|---|---|---|---|
| `ebpfServiceFlagStickySourceIP` | `0x01` | `ebpf_types.go:37` | Sticky source-IP flag |
| `ebpfDefaultAffinityTimeoutSeconds` | `300` | `ebpf_types.go:41` | 5-minute affinity TTL |
| `NANOSECONDS_PER_SECOND` | `1000000000` | `cca_helpers.h:12` | Timeout conversion factor |

## Map Reconciliation (Go → BPF)

### VIP Reconciliation Order

```
ReconcileVIPDataPlane (network/ebpf_dataplane.go:299)
  │
  ├── 1. buildDesiredVIPState(serviceConfigs)
  │      ├── Sort configs by ServiceName (deterministic)
  │      ├── Skip services with 0 backends
  │      └── FOR EACH service:
  │          ├── assignOrLookupServiceID(name) → monotonic u32
  │          ├── serviceKey = {ipToUint32(VIP), portToNetworkOrder(port), proto}
  │          ├── serviceValue = {svc_id, count, flags=0, timeout=0}
  │          └── FOR EACH backend[i]:
  │              └── backendKey={svc_id, slot=i}, backendValue={addr, port}
  │
  ├── 2. applyDesiredBackendEntries         ← BACKENDS FIRST
  │      └── backendMap.Put(key, value) for each desired
  │
  ├── 3. applyDesiredServiceEntries         ← SERVICES SECOND
  │      └── serviceMap.Put(key, value) for each desired
  │
  ├── 4. removeStaleServiceEntries          ← STALE SERVICES THIRD
  │      ├── serviceMap.Iterate() → find keys not in desired set
  │      └── serviceMap.Delete(staleKey) for each stale
  │
  └── 5. removeStaleBackendEntries          ← STALE BACKENDS LAST
         ├── backendMap.Iterate() → find keys not in desired set
         └── backendMap.Delete(staleKey) for each stale

ORDER INVARIANT:
  Add backends → update services → remove stale services → remove stale backends.
  Services always reference valid backends. No dangling references during
  concurrent BPF program execution.
```

### Service ID Allocation

```
Service IDs are monotonically increasing uint32 values.
  ebpfInitialServiceID = 1     (ebpf_dataplane.go:59)
  Zero is reserved as invalid sentinel.

Allocation:
  assignOrLookupServiceID (ebpf_dataplane.go:386)
  ├── IF name in serviceNameToID map → return existing ID
  └── ELSE: allocate nextServiceID++, store in map

Restart recovery:
  scanExistingServiceIDs (ebpf_dataplane.go:275)
  ├── Iterate entire serviceMap (pinned, survives restart)
  ├── Find max(service_id) across all entries
  └── Set nextServiceID = max + 1
  Purpose: prevent ID collisions after agent restart
```

## Initialization Flow

```
Agent startup with --data-plane=ebpf:

  1. EbpfDataPlane.Initialize (ebpf_dataplane.go:136)
     ├── rlimit.RemoveMemlock()
     │   └── Removes BPF memory lock limit (required on older kernels)
     ├── os.MkdirAll("/sys/fs/bpf/cca", 0700)
     │   └── BPF filesystem directory for pinned maps
     ├── ebpf.LoadCollectionSpecFromReader(embeddedDataplaneELF)
     │   └── Parse embedded ELF (go:embed cca_dataplane.o)
     ├── ebpf.NewCollection(spec)
     │   └── Load ALL 5 programs + ALL 5 maps in one call
     ├── extractAndPinMaps()
     │   └── Pin: cca_services, cca_backends, cca_affinity, cca_reverse_nat
     │       └── /sys/fs/bpf/cca/<name> (os.Remove first for idempotent re-pin)
     ├── attachCgroupPrograms()
     │   └── Attach 3 programs to /sys/fs/cgroup:
     │       ├── cca_sock4_connect   (AttachCGroupInet4Connect)
     │       ├── cca_sock4_sendmsg   (AttachCGroupUDP4Sendmsg)
     │       └── cca_sock4_getpeername (AttachCgroupInet4GetPeername)
     ├── scanExistingServiceIDs()
     │   └── Iterate pinned serviceMap → resume ID allocation
     └── extractTCPrograms()
         ├── tcProgram = collection.Programs["cca_tc_dnat"]
         ├── snatProgram = collection.Programs["cca_tc_snat"]
         └── configMap = collection.Maps["cca_config"]

  2. EbpfDataPlane.RegisterNode (after node IP + pod CIDR known)
     ├── discoverBridgeInterfaceBySubnet(podCIDR)
     │   └── Find bridge with IP in overlay subnet (e.g. cca0)
     │       └── Check /sys/class/net/<name>/bridge exists
     ├── attachTCProgramToBridge(bridgeName)
     │   └── link.AttachTCX({Interface: cca0, Attach: TCXIngress})
     ├── populateConfigMap(nodeIP, podCIDR)
     │   ├── configMap.Put(0, nodeIP)            ← node address
     │   ├── configMap.Put(2, overlayPrefix)     ← CIDR network
     │   └── configMap.Put(3, overlayMask)       ← CIDR netmask
     ├── detectPhysicalInterfaceForNodeIP(nodeIP)
     │   └── Scan net.Interfaces() for matching address
     └── attachSNATToPhysicalNIC
         └── link.AttachTCX({Interface: eth0, Attach: TCXEgress})

  3. EbpfDataPlane.RetryBridgeAttachment (after first container starts)
     └── IF tcLink == nil AND tcProgram != nil:
         ├── discoverBridgeInterfaceBySubnet(podCIDR)
         └── attachTCProgramToBridge(cca0)
     Purpose: Bridge doesn't exist until CNI creates it for first container.
              RegisterNode logs a warning and defers; agent retries here.
```

## Cleanup Flow

```
EbpfDataPlane.Cleanup (agent shutdown, ebpf_dataplane.go:768)
  ├── closeAllResources()
  │   ├── snatLink.Close()                    ← detach tc SNAT from eth0
  │   ├── tcLink.Close()                      ← detach tc DNAT from cca0
  │   ├── FOR EACH attachedLink: link.Close() ← detach cgroup programs
  │   └── bpfCollection.Close()               ← close all program + map FDs
  ├── removePinnedMaps()
  │   └── os.Remove(/sys/fs/bpf/cca/<name>) for each map
  └── Reset state:
      ├── serviceNameToID = empty map
      ├── nextServiceID = 1
      └── initialized = false
```

## Build Pipeline

```
scripts/build-ebpf.sh
  ├── docker build -t cca-bpf-builder -f scripts/Dockerfile.bpf .
  ├── docker run --platform linux/amd64
  │   -v network/ebpf:/src
  │   cca-bpf-builder
  │   -c /src/cca_dataplane.c -o /src/cca_dataplane.o
  └── Commit cca_dataplane.o to repository

Alternative (bpf2go, manual):
  go generate -run CcaDataplane ./network/
  └── Requires: clang, BPF headers, Linux host
  └── Defined in: network/ebpf_gen.go:19

Go binary:
  //go:embed ebpf/cca_dataplane.o
  var embeddedDataplaneELF []byte    (network/ebpf_embed.go:16)
  └── ELF is self-contained: 5 programs + 5 maps in one object
```

## Byte Order Conventions

```
Kernel representation:
  bpf_sock_addr.user_ip4   → __be32 loaded as native u32
  bpf_sock_addr.user_port  → sin_port (__be16) loaded as native u16

Go conversion helpers (network/ebpf_types.go):
  ipToUint32(string) → uint32        NativeEndian from net.IP.To4()
  uint32ToIP(uint32) → string        NativeEndian → dotted decimal
  portToNetworkOrder(uint16) → uint16 BigEndian bytes → NativeEndian read
  ipBytesToUint32([]byte) → uint32   NativeEndian (for netmask)
  protocolToNumber(string) → uint8   "tcp"→6, "udp"→17, default→6
```

## Fallback Behavior

```
eBPF unavailable (no build tag, old kernel, or initialization failure):

  1. No "-tags ebpf" at build time:
     → ebpf_register.go not compiled
     → "ebpf" factory never registered
     → Agent uses nftables (default) or iptables

  2. Kernel too old (< 6.1 for conntrack kfuncs):
     → ebpf.NewCollection() fails at program load
     → Initialize() returns error
     → Agent must be configured with a different --data-plane

  3. Bridge not found on RegisterNode:
     → Logs warning, socket LB still works (host processes)
     → Container traffic falls through to kernel netfilter (nftables/iptables)
     → RetryBridgeAttachment called after first container starts

  4. SNAT attachment fails:
     → Logs warning, VIP LB still works
     → Overlay SNAT falls back to nftables POSTROUTING masquerade rule

  TC programs degrade gracefully — each attachment failure is logged but
  does not abort. Socket LB (cgroup programs) provides host-level VIP
  translation even if tc programs fail to attach.
```

## Network Policy (Future)

```
Interfaces defined but not yet implemented:

  DataPlanePolicyReconciler (network/dataplane.go:125)
    ReconcilePolicies(ctx context.Context) error

  EndpointIdentityRegistrar (network/dataplane.go:118)
    RegisterEndpointIdentity(ctx, containerIP, hostIP, serviceName, containerPort)

Current state: nftables container_isolation chain handles etcd port blocking.
eBPF provider does NOT implement network policy enforcement yet.

Future direction: per-endpoint identity maps keyed by container IP, with
TC ingress/egress programs enforcing allow/deny by {src_identity, dst_identity,
port, protocol} tuples. Similar to Cilium's identity-based policy model.
```

## Inspection Commands

```
# List pinned maps
bpftool map list pinned /sys/fs/bpf/cca/

# Dump service map
bpftool map dump pinned /sys/fs/bpf/cca/cca_services

# Dump backend map
bpftool map dump pinned /sys/fs/bpf/cca/cca_backends

# List loaded programs
bpftool prog list | grep cca

# Show program info
bpftool prog show name cca_sock4_connect

# Dump program bytecode
bpftool prog dump xlated name cca_tc_dnat

# Check cgroup attachments
bpftool cgroup tree /sys/fs/cgroup

# Check tc attachments on bridge
tc filter show dev cca0 ingress

# Check tc attachments on physical NIC
tc filter show dev eth0 egress
```

## Constants Reference

| Constant | Value | File:Line | Description |
|---|---|---|---|
| `ebpfPinPath` | `/sys/fs/bpf/cca` | `ebpf_dataplane.go:27` | BPF filesystem pin directory |
| `ebpfCgroupPath` | `/sys/fs/cgroup` | `ebpf_dataplane.go:30` | Root cgroup mount point |
| `ebpfInitialServiceID` | `1` | `ebpf_dataplane.go:59` | Starting service ID (0 = invalid) |
| `ebpfServiceFlagStickySourceIP` | `0x01` | `ebpf_types.go:37` | Sticky source-IP flag |
| `ebpfDefaultAffinityTimeoutSeconds` | `300` | `ebpf_types.go:41` | 5-minute default TTL |
| `ebpfProtocolTCP` | `6` | `ebpf_types.go:44` | IANA TCP protocol number |
| `ebpfProtocolUDP` | `17` | `ebpf_types.go:47` | IANA UDP protocol number |
| `CCA_SVC_FLAG_STICKY_SOURCE_IP` | `0x01` | `cca_maps.h:10` | C-side sticky flag |
| `cca_services max_entries` | `4096` | `cca_maps.h:74` | Max VIP services |
| `cca_backends max_entries` | `65536` | `cca_maps.h:81` | Max backend slots |
| `cca_affinity max_entries` | `131072` | `cca_maps.h:90` | Max affinity cache entries |
| `cca_reverse_nat max_entries` | `131072` | `cca_maps.h:100` | Max reverse NAT entries |
| `cca_config max_entries` | `8` | `cca_maps.h:110` | Config array slots |
