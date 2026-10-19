# Independent eBPF Data Plane — Design v2

**Date:** 2026-10-19
**Status:** Proposed
**Supersedes:** ebpf-data-plane.md "Part 2: eBPF DataPlaneProvider (Future)"
**Depends on:** [networking.md](networking.md), [ebpf-data-plane.md](ebpf-data-plane.md), [cilium-integration-v2.md](cilium-integration-v2.md)
**Informed by:** [cilium-ebpf-debug.md](cilium-ebpf-debug.md) (bare-metal testing, 2026-10-19)

---

## Motivation

CCattler's data plane needs to evolve beyond nftables DNAT. Three options exist:

1. **Cilium integration** — use Cilium agents in kvstore mode as the eBPF data plane
2. **Independent eBPF** — CCattler compiles and loads its own BPF programs
3. **Stay on nftables** — defer eBPF indefinitely

Bare-metal testing on 2026-10-19 ([cilium-ebpf-debug.md](cilium-ebpf-debug.md)) exposed fundamental problems with option 1: Cilium's service model is Kubernetes-centric, its kvstore service prefix is not consumed in standalone mode, its tunnel management conflicts with CCattler's VXLAN overlay, and Ubuntu 24.04 iptables incompatibility causes continuous error storms. The Cilium path requires maintaining a 60,000-line external agent, adapting to its API conventions, and debugging opaque eBPF program behavior through its abstractions.

This document proposes option 2: an independent eBPF data plane where CCattler owns every BPF program, every map structure, and every tunnel decision. The programs are small (estimated 400-600 lines of Rust total), the maps are simple hash tables, and the control plane is Go code that already exists (the nftables reconciler pattern, the VXLAN reconciler pattern).

---

## Architecture

```
CCattler Agent (Go)
        │
        ├── EbpfDataPlane (implements DataPlaneProvider)
        │       │
        │       ├── cilium/ebpf Go library
        │       │       ├── loads compiled .o ELF files
        │       │       ├── creates/updates BPF maps
        │       │       └── attaches programs to hooks
        │       │
        │       ├── BPF programs (Rust, aya-ebpf, compiled to ELF .o)
        │       │       ├── cgroup/connect4  — VIP translation for host-originated traffic
        │       │       ├── cgroup/sendmsg4  — UDP VIP translation (sendto/sendmsg)
        │       │       ├── cgroup/getpeername4 — reverse VIP translation for getpeername()
        │       │       ├── tc ingress       — VIP DNAT for bridged/forwarded traffic
        │       │       └── tc egress        — SNAT for cross-node reply packets
        │       │
        │       └── BPF maps (pinned in /sys/fs/bpf/cca/)
        │               ├── cca_services     — {VIP, port, proto} → service metadata
        │               ├── cca_backends     — {service_id, slot} → backend endpoint
        │               ├── cca_affinity     — {client_ip, service_id} → backend + timestamp
        │               ├── cca_conntrack    — {5-tuple} → NAT state (via kfuncs or custom)
        │               └── cca_tunnel_peers — {node_subnet_prefix} → {node_ip, vxlan_mac}
        │
        └── VXLAN Tunnel (unchanged: cca-vxlan0 kernel interface)
                │
                └── BPF manages FDB/ARP entries via maps instead of `ip route` / `bridge fdb`
                    (Phase 3 optimization — Phase 1 keeps kernel VXLAN as-is)
```

### Key design decisions

**Go control plane, Rust data plane.** The cilium/ebpf Go library loads ELF objects and manages maps. BPF programs are written in Rust using aya-ebpf for compile-time type safety on kernel struct layouts (conntrack kfuncs require exact struct alignment). This matches the existing plan from [ebpf-data-plane.md](ebpf-data-plane.md) Part 2.

**Kernel VXLAN stays.** The existing `cca-vxlan0` kernel VXLAN interface works correctly (M122). BPF-based tunnel encapsulation is a Phase 3 optimization, not a Phase 1 requirement. The immediate value is replacing nftables DNAT with BPF socket LB and tc programs, not reimplementing tunnel encapsulation.

**Socket LB for host namespace, tc for bridge traffic.** Cilium's testing confirmed that `cgroup/connect4` intercepts host-originated VIP connections cleanly. For traffic originating inside containers (which traverse the bridge, not the host cgroup), tc ingress on the bridge interface handles DNAT. This two-hook approach covers all traffic paths.

**Conntrack kfuncs for stateful NAT.** BPF header rewrite alone does not establish kernel conntrack state (identified in external review round 1, confirmed in [ebpf-data-plane.md](ebpf-data-plane.md)). The `bpf_skb_ct_alloc` + `bpf_ct_set_nat_info` + `bpf_ct_insert_entry` kfuncs (kernel 6.1+) create proper NAT entries from within BPF. Reply packets are reverse-NATed by the kernel's conntrack subsystem, not by BPF.

**No Cilium agent, no Cilium operator.** Zero external daemons. The Go agent loads BPF programs directly. This eliminates the kvstore quorum issues, the iptables-nft compatibility issues, and the service registration API workaround documented in [cilium-integration-v2.md](cilium-integration-v2.md).

---

## BPF Program Inventory

### Program 1: `cca_connect4` (cgroup/connect4)

**Hook:** `BPF_CGROUP_INET4_CONNECT` on the root cgroup (or host-level cgroup).

**Purpose:** Intercept `connect()` syscalls where the destination is a VIP. Translate the destination address and port to a selected backend before the TCP handshake begins. This is the same mechanism Cilium uses for socket-level LB.

**Flow:**
1. Extract `{dst_ip, dst_port, protocol}` from the socket context
2. Lookup `cca_services` map: if no match, return `CGROUP_OK` (passthrough)
3. Select backend: round-robin via atomic counter in service entry, or affinity lookup in `cca_affinity` map
4. Rewrite `dst_ip` and `dst_port` in the socket context to the backend address
5. The kernel performs the `connect()` with the rewritten destination — full conntrack NAT state is established automatically by the kernel because the socket itself holds the translated address

**Why this works without conntrack kfuncs:** Socket-level translation happens before the kernel creates the socket. The kernel sees the backend IP as the original destination. No packet rewriting needed, no conntrack fixup needed. This is the cleanest NAT path in BPF.

**Limitation:** Only works for traffic originating in the cgroup where the program is attached (typically host namespace processes and containers in the same cgroup hierarchy). Does not intercept forwarded/bridged traffic.

**Estimated size:** ~80 lines of Rust.

### Program 2: `cca_sendmsg4` (cgroup/sendmsg4)

**Hook:** `BPF_CGROUP_UDP4_SENDMSG` on the root cgroup.

**Purpose:** Translate VIP destinations for UDP `sendto()` / `sendmsg()` calls. UDP is connectionless, so `connect()` interception alone is insufficient — each datagram send specifies its own destination.

**Flow:** Same as `cca_connect4` but operates on the `sendmsg` context. Rewrites destination address and port before the kernel sends the datagram.

**Estimated size:** ~50 lines of Rust (shares lookup logic with connect4).

### Program 3: `cca_getpeername4` (cgroup/getpeername4)

**Hook:** `BPF_CGROUP_INET4_GETPEERNAME` on the root cgroup.

**Purpose:** When an application calls `getpeername()` on a socket that was VIP-translated by `cca_connect4`, return the original VIP address instead of the backend address. This preserves application-level transparency — the app thinks it connected to the VIP.

**Flow:**
1. Check if the socket's actual peer address matches a known backend
2. If yes, look up the reverse mapping and rewrite the returned address to the VIP
3. If no, passthrough

**Estimated size:** ~40 lines of Rust.

### Program 4: `cca_tc_ingress` (tc/ingress on cca-net bridge)

**Hook:** TC ingress classifier on the `cca-net` bridge interface.

**Purpose:** DNAT for traffic entering the bridge from containers. When a container sends a packet to a VIP, this program translates the destination to a backend IP before the packet is forwarded. This covers container-to-VIP traffic that does not go through the host cgroup (containers connected directly to the bridge via veth pairs).

**Flow:**
1. Parse the packet: extract `{dst_ip, dst_port, protocol}`
2. Lookup `cca_services` map: if no match, return `TC_ACT_OK` (passthrough)
3. Select backend (round-robin or affinity)
4. Call `bpf_skb_ct_alloc()` to create a conntrack entry
5. Call `bpf_ct_set_nat_info()` to record the NAT translation
6. Call `bpf_ct_insert_entry()` to commit the conntrack entry
7. Rewrite L3/L4 headers: destination IP and port, recompute checksums
8. Return `TC_ACT_OK` to continue normal forwarding

**Why conntrack kfuncs are needed here:** Unlike socket-level translation, tc programs operate on in-flight packets. The kernel sees the original VIP destination in the packet that triggered the conntrack entry. Without kfuncs, the kernel creates a conntrack entry for the VIP tuple, and reply packets from the backend are not reverse-NATed (the kernel doesn't know the backend IP maps back to the VIP).

**Estimated size:** ~120 lines of Rust.

### Program 5: `cca_tc_egress` (tc/egress on physical interface)

**Hook:** TC egress classifier on the physical NIC (e.g. `eth0`).

**Purpose:** SNAT (masquerade) for packets leaving the node toward backends on remote nodes. When a VIP DNAT rewrites the destination to a remote container IP, the source address must be changed to this node's address so that reply packets return to this node (where the conntrack entry exists for reverse-NAT).

**Flow:**
1. Check if the packet's destination is in a remote node's subnet (lookup `cca_tunnel_peers`)
2. Check if conntrack shows this packet was DNATed (established flow with NAT state)
3. If both conditions match, rewrite source IP to this node's advertise address
4. Recompute checksums
5. Return `TC_ACT_OK`

**Note:** In Phase 1, SNAT can remain in nftables (`ct status dnat masquerade` — one rule) while the DNAT path is migrated to BPF. This simplifies the initial implementation.

**Estimated size:** ~60 lines of Rust.

### Total BPF code: ~350 lines of Rust

Plus shared helper functions (map lookups, checksum computation, backend selection): ~100 lines. Grand total: ~450 lines of Rust.

---

## BPF Map Structures

All maps are pinned at `/sys/fs/bpf/cca/` for persistence across agent restarts and for debugging via `bpftool`.

### `cca_services` — Service lookup map

```
Type:     BPF_MAP_TYPE_HASH
Key:      { vip_addr: u32, port: u16, protocol: u8, _pad: u8 }  (8 bytes)
Value:    { service_id: u32, backend_count: u16, flags: u16, affinity_ttl: u32 }  (12 bytes)
Max entries: 4096 (supports ~4000 services)
```

**Flags:** `STICKY_SOURCE_IP = 0x01`, `STICKY_COOKIE = 0x02`, `UDP = 0x04`.

### `cca_backends` — Backend endpoint map

```
Type:     BPF_MAP_TYPE_HASH
Key:      { service_id: u32, slot: u16, _pad: u16 }  (8 bytes)
Value:    { backend_addr: u32, port: u16, _pad: u16 }  (8 bytes)
Max entries: 65536 (supports ~16 backends per service at 4096 services)
```

Backend selection: `slot = atomic_add(&service.counter, 1) % service.backend_count`. The counter is stored in a per-CPU array map for lock-free round-robin.

### `cca_affinity` — Session affinity map

```
Type:     BPF_MAP_TYPE_LRU_HASH
Key:      { client_addr: u32, service_id: u32 }  (8 bytes)
Value:    { backend_addr: u32, port: u16, _pad: u16, timestamp: u64 }  (16 bytes)
Max entries: 131072
```

LRU eviction handles cleanup. The BPF program checks `timestamp + affinity_ttl > now` before using a cached backend. Expired entries are overwritten with the newly selected backend.

### `cca_reverse_nat` — Reverse VIP mapping for getpeername

```
Type:     BPF_MAP_TYPE_LRU_HASH
Key:      { backend_addr: u32, backend_port: u16, _pad: u16 }  (8 bytes)
Value:    { vip_addr: u32, vip_port: u16, _pad: u16 }  (8 bytes)
Max entries: 65536
```

Populated by the control plane when programming backends. Used by `cca_getpeername4` to return the original VIP address.

### `cca_tunnel_peers` — Node subnet to physical IP mapping

```
Type:     BPF_MAP_TYPE_LPM_TRIE
Key:      { prefixlen: u32, addr: u32 }  (8 bytes, standard LPM key)
Value:    { node_addr: u32, node_mac: [6]u8, _pad: [2]u8 }  (12 bytes)
Max entries: 256
```

Used by `cca_tc_egress` to determine whether a destination is remote (needs SNAT). In Phase 3, also used for BPF-based tunnel encapsulation.

### `cca_config` — Runtime configuration

```
Type:     BPF_MAP_TYPE_ARRAY
Key:      u32 (index)
Value:    u32 (config value)
Max entries: 8
```

Indices: `0 = node_ip`, `1 = vxlan_vni`, `2 = vip_cidr_prefix`, `3 = vip_cidr_mask`. Allows the BPF programs to reference node-specific values without recompilation.

---

## Cross-Node Tunnel Approach

### Phase 1-2: Keep kernel VXLAN (cca-vxlan0)

The existing VXLAN overlay from M122 works correctly for cross-node traffic. BPF programs perform VIP DNAT, the kernel routing table delivers packets via `cca-vxlan0` to remote nodes, and the existing VXLAN reconciler manages routes and FDB entries.

This means Phase 1 BPF only replaces the nftables DNAT rules. Cross-node encapsulation is unchanged.

```
Container → VIP packet
    │
    ▼ BPF tc ingress on cca-net: DNAT to backend IP (conntrack kfuncs)
    │
    ▼ kernel routing: 10.100.2.0/24 via cca-vxlan0
    │
    ▼ kernel VXLAN encap → UDP 4789 → remote node → VXLAN decap → bridge → container
```

### Phase 3: BPF tunnel encapsulation (optional optimization)

Replace `cca-vxlan0` kernel interface with BPF-based encapsulation on the tc egress hook of the bridge. The BPF program:

1. Looks up `cca_tunnel_peers` (LPM trie) for the destination IP
2. Prepends VXLAN + UDP + IP + Ethernet headers
3. Redirects the packet to the physical NIC via `bpf_redirect`

This eliminates the kernel VXLAN interface, FDB entries, and ARP resolution for tunnel endpoints. The BPF program knows exactly which physical node IP to send to (from the map), so there is no ARP needed.

**Benefits:**
- Eliminates the VXLAN reconciler's `ip route` and `bridge fdb` commands
- Faster encap/decap (no netfilter traversal for tunnel packets)
- Single BPF program handles both DNAT and encapsulation

**Cost:**
- ~150 additional lines of Rust for header construction
- Must handle MTU clamping (TCP MSS via tc, ICMP frag-needed for UDP)
- Must handle VXLAN decapsulation on the receive side (tc ingress on physical NIC)

This is an optimization, not a requirement. The kernel VXLAN interface is production-proven and sufficient. BPF tunneling is worth pursuing only if benchmarks show per-packet overhead from the kernel VXLAN path matters.

---

## Go Control Plane Implementation

### `EbpfDataPlane` (network/ebpf_dataplane.go)

```go
type EbpfDataPlane struct {
    // bpfObjects holds loaded BPF programs and maps from the compiled ELF.
    bpfObjects      *ccaBpfObjects
    // serviceMap is a handle to the cca_services BPF hash map.
    serviceMap      *ebpf.Map
    // backendMap is a handle to the cca_backends BPF hash map.
    backendMap      *ebpf.Map
    // affinityMap is a handle to the cca_affinity BPF LRU hash map.
    affinityMap     *ebpf.Map
    // reverseNATMap is a handle to the cca_reverse_nat BPF LRU hash map.
    reverseNATMap   *ebpf.Map
    // tunnelPeerMap is a handle to the cca_tunnel_peers BPF LPM trie.
    tunnelPeerMap   *ebpf.Map
    // configMap is a handle to the cca_config BPF array map.
    configMap       *ebpf.Map
    // nextServiceID is the next service ID to allocate.
    nextServiceID   uint32
    // serviceIDMap tracks allocated service IDs by service name.
    serviceIDMap    map[string]uint32
    // attachedLinks tracks BPF program attachments for cleanup.
    attachedLinks   []link.Link
}
```

### ReconcileVIPDataPlane flow

1. **Build desired map state** from `[]ServiceVIPConfig`:
   - For each service: compute `cca_services` key `{vip, port, proto}` and value `{service_id, backend_count, flags, affinity_ttl}`
   - For each backend: compute `cca_backends` key `{service_id, slot}` and value `{backend_ip, port}`
   - For each backend: compute `cca_reverse_nat` entry `{backend_ip, port} → {vip, port}`

2. **Diff against current map state** (read from BPF maps via `Map.Iterate()`):
   - New services: insert into `cca_services` + `cca_backends`
   - Changed backends: update `cca_backends` entries, update backend count in `cca_services`
   - Removed services: delete from `cca_services` + `cca_backends` + `cca_reverse_nat`

3. **Apply as a batch**: BPF map operations are per-entry (not transactional like nftables). To avoid a brief window where a service exists with zero backends:
   - Add new backends first
   - Update the service entry (with new backend count)
   - Remove old backends last
   - This ordering ensures the service always has at least the old backends or the new backends

### Generation-based backend publication (from ebpf-data-plane.md)

For backend changes that involve both additions and removals (e.g., scaling from 3 backends to 2 different backends), a seqlock is insufficient. Instead:

1. Write new backends with `slot` values starting at `backend_count` (appending)
2. Update `backend_count` atomically to include old + new backends
3. Wait one conntrack timeout (configurable, default 120s) for existing connections to drain
4. Remove old backends and update `backend_count` to final value

In practice, most backend changes are single additions or removals (scaling by 1, instance restart). The full generation swap is only needed when all backends change simultaneously.

### Build pipeline

```
ebpf/
├── Cargo.toml                  # Rust workspace: aya-ebpf programs
├── src/
│   ├── connect4.rs             # cgroup/connect4 VIP translation
│   ├── sendmsg4.rs             # cgroup/sendmsg4 UDP VIP translation
│   ├── getpeername4.rs         # cgroup/getpeername4 reverse VIP
│   ├── tc_ingress.rs           # tc ingress DNAT with conntrack kfuncs
│   ├── tc_egress.rs            # tc egress SNAT / masquerade
│   └── common.rs               # shared types, map definitions, helpers
├── build.rs                    # cargo build script (sets target, linker)
└── target/
    └── bpfel-unknown-none/
        └── release/
            └── *.o             # compiled BPF ELF objects

network/
├── ebpf_dataplane.go           # EbpfDataPlane provider
├── ebpf_dataplane_test.go      # unit tests (map state transitions)
├── ebpf_maps.go                # Go struct definitions matching BPF map layouts
├── ebpf_loader.go              # ELF loading, map pinning, program attachment
└── ebpf_objects.go             # generated by bpf2go or manual embed

scripts/
├── build-ebpf.sh               # nightly Rust + aya-ebpf + bpf-linker → .o files
└── ci-ebpf.sh                  # CI: build .o, embed in Go binary, run tests
```

**Build dependency:** nightly Rust with `aya-ebpf` crate and `bpf-linker`. The `.o` files are committed to the repo (unlike the original plan) so that contributors can build the Go binary without a Rust toolchain. CI rebuilds them and fails if the committed `.o` differs from the source.

**Alternative considered:** Not committing `.o` files (original plan in ebpf-data-plane.md). Rejected because: (a) Go `embed` is the simplest loading path, (b) contributors who only change Go code should not need Rust, (c) CI guards against stale binaries.

---

## Comparison: Independent eBPF vs Cilium Integration

### Effort

| Dimension | Independent eBPF | Cilium Integration |
|---|---|---|
| **BPF programs** | ~450 lines Rust (we write them) | 0 lines (Cilium's, opaque) |
| **Go control plane** | ~600 lines (map management, loader) | ~800 lines (written in M126-M127, plus API reconciler from v2 design) |
| **Tunnel management** | 0 new lines (keep cca-vxlan0) | Complex: disable cca-vxlan0, configure cilium_vxlan, debug bridge/ARP issues |
| **External dependencies** | cilium/ebpf Go library (well-maintained, MIT) | Cilium agent binary + cilium-operator + etcd schema + REST API client |
| **Kernel requirements** | 6.1+ (conntrack kfuncs) | 5.10+ for base, but 6.1+ for full feature set; Ubuntu 24.04 iptables-nft workaround |
| **Build toolchain** | nightly Rust + bpf-linker (CI only, .o committed) | None extra, but Cilium binary is 200MB+ |
| **Debugging** | bpftool, our maps, our code | Cilium CLI, Hubble, opaque BPF internals |
| **New code to write** | ~1050 lines (Rust + Go) | ~400 lines new Go (CiliumServiceReconciler) + integration testing |
| **New code to maintain** | ~1050 lines | ~400 lines Go + Cilium agent upgrades + Ansible role |

### Features

| Feature | Independent eBPF | Cilium Integration |
|---|---|---|
| **VIP load balancing** | Socket LB + tc DNAT, same as Cilium | Socket LB via Cilium agent |
| **Cross-node routing** | Keep cca-vxlan0 (works now) | cilium_vxlan (new, debug needed) |
| **Session affinity** | BPF LRU map with TTL | Cilium native affinity (socket cookie) |
| **Network policy** | Future: tc ingress drop rules | Cilium identity-based policy (eBPF) |
| **L7 policy** | Not planned (not needed now) | Cilium Envoy (if enabled) |
| **Observability** | bpftool map dump, custom metrics | Hubble flow visibility |
| **Transparent encryption** | Not planned | WireGuard/IPsec (Cilium feature) |
| **Bandwidth manager** | Not planned | Cilium EDT-based rate limiting |

### Maintenance burden

| Concern | Independent eBPF | Cilium Integration |
|---|---|---|
| **Kernel upgrades** | Retest BPF verifier on new kernels, CO-RE handles most changes | Same + Cilium compatibility matrix |
| **Cilium upgrades** | N/A | Must track Cilium releases, API changes, deprecations |
| **Ubuntu/RHEL compat** | CO-RE BPF programs adapt to kernel structs via BTF | Cilium compat + iptables-nft workaround + operator version matching |
| **Debugging production issues** | Full visibility: our code, our maps, bpftool | Must understand Cilium internals, read Cilium source, file upstream issues |
| **Bus factor** | CCattler team owns all code | Dependent on Cilium project direction and support |
| **Security updates** | ~450 lines Rust to audit | 60,000+ lines in Cilium agent to track for CVEs |

### Operational complexity

| Concern | Independent eBPF | Cilium Integration |
|---|---|---|
| **Daemons per node** | 1 (cca-agent) | 2 (cca-agent + cilium-agent), plus cilium-operator on control plane |
| **etcd usage** | CCattler facts only | CCattler facts + Cilium state prefixes + Cilium leases + Cilium heartbeats |
| **Ansible roles** | Unchanged | cilium role: install binary, etcd config, iptables-nft symlink, operator |
| **Upgrade procedure** | Upgrade cca-agent (includes BPF .o) | Upgrade cca-agent + upgrade cilium-agent + upgrade cilium-operator, in coordinated order |
| **Failure domains** | Agent crash: BPF programs stay loaded (pinned maps), traffic continues | Cilium agent crash: same for BPF, but kvstore quorum checks fail, identity GC stalls |

---

## Lessons from Cilium Testing (2026-10-19)

The following findings from [cilium-ebpf-debug.md](cilium-ebpf-debug.md) directly inform this design.

### 1. Socket-based LB works well for VIP translation

Cilium's `cgroup/connect4` hook successfully intercepted host-namespace VIP connections and translated them to backend IPs. Local backends worked immediately. This validates the socket LB approach for host-originated traffic.

**Implication for independent eBPF:** The `cca_connect4` program is the highest-value, lowest-risk BPF program. It replaces nftables OUTPUT hook DNAT for all host-namespace traffic with zero conntrack complexity (the kernel handles conntrack naturally for socket-level translation).

### 2. Cross-node routing is the hard part, and we already solved it

Cilium's native routing mode failed because `cca-vxlan0` does not bridge host-namespace ARP to the container bridge. Cilium's tunnel mode would work but requires disabling CCattler's VXLAN, re-deploying Cilium with different flags, and accepting Cilium-managed tunnels.

**Implication for independent eBPF:** Keep `cca-vxlan0`. It works. BPF programs perform DNAT, the kernel routes to the VXLAN interface, the kernel encapsulates. No new tunnel code needed in Phase 1.

### 3. Cilium's kvstore service model does not work standalone

Writing to `cilium/state/services/v1/` has no effect in non-Kubernetes mode. Services must be pushed via the REST API to each node's local Cilium agent. This breaks the fact-based model (CCattler must make imperative API calls, not just write facts).

**Implication for independent eBPF:** With our own BPF maps, the control plane writes map entries directly. No API translation layer, no REST client, no per-node API calls. The pattern is identical to nftables reconciliation: read desired state from facts, compute map entries, apply diff.

### 4. External daemon management is a liability

Cilium's iptables error storm on Ubuntu 24.04, kvstore quorum check failures, and the need for `iptables-nft` symlinks demonstrate that managing an external agent with its own opinions about the system is fragile. Every Cilium release may introduce new incompatibilities.

**Implication for independent eBPF:** One binary (cca-agent) owns the entire data plane. No coordination between daemons, no version compatibility matrix, no surprise iptables reconciliation loops.

---

## Traffic Path Coverage

### All traffic paths and which BPF program handles each

| Source | Destination | Path | BPF hook |
|---|---|---|---|
| Host process | VIP (TCP) | connect() intercepted | `cca_connect4` |
| Host process | VIP (UDP) | sendmsg() intercepted | `cca_sendmsg4` |
| Host process | Container IP (local) | Direct routing via bridge | None (passthrough) |
| Host process | Container IP (remote) | Route via cca-vxlan0 | None (kernel VXLAN) |
| Container | VIP (TCP/UDP) | Bridge ingress | `cca_tc_ingress` on cca-net |
| Container | Container IP (local) | Bridge forwarding | None (passthrough) |
| Container | Container IP (remote) | Bridge → route → VXLAN | None (kernel VXLAN) |
| Remote node | Local container | VXLAN decap → bridge | None (kernel handles) |
| Any DNATed packet | Remote backend | Needs SNAT for return path | `cca_tc_egress` on eth0 (or nftables masquerade in Phase 1) |

### Established flow fast path

For packets belonging to an already-established connection (e.g., subsequent TCP segments after the SYN/SYN-ACK handshake), the tc ingress program can skip backend selection entirely:

1. Call `bpf_skb_ct_lookup()` on the incoming packet
2. If conntrack returns a NAT entry, apply the cached translation
3. If no entry, perform full service lookup + backend selection + conntrack kfunc insertion

This fast path handles the common case (data transfer on established connections) with a single hash lookup instead of two (service lookup + backend selection).

---

## Phased Implementation Plan

### Phase 1: Socket LB for host namespace (3 weeks)

**Goal:** Replace nftables OUTPUT hook DNAT with BPF `cgroup/connect4` for host-originated VIP traffic. Keep nftables PREROUTING for bridge traffic. Keep nftables POSTROUTING masquerade. Keep kernel VXLAN.

**Deliverables:**
- Rust BPF programs: `cca_connect4`, `cca_sendmsg4`, `cca_getpeername4`
- Go: `EbpfDataPlane` implementing `DataPlaneProvider`, map management, program loading
- Go: `--dataplane ebpf` flag in agent, registered via `RegisterDataPlaneFactory`
- Build: `scripts/build-ebpf.sh`, committed `.o` files, CI verification
- Tests: unit tests for map state transitions, netns integration test for host-originated VIP

**What changes for production traffic:**
- Host processes (including cca-agent health checks, cca-server API calls) use BPF socket LB for VIP resolution
- Container-to-VIP traffic still uses nftables (bridge path)
- Cross-node traffic still uses kernel VXLAN
- SNAT still uses nftables masquerade

**Effort:** 3 weeks (1 week Rust programs + build pipeline, 1 week Go control plane, 1 week testing + integration)

**Exit criteria:**
- `curl <vip>:<port>` from host namespace → reaches backend (verified with tcpdump: no nftables DNAT, socket connects directly to backend IP)
- Round-robin distribution across backends
- Source-IP affinity holds for configured TTL
- `getpeername()` returns VIP, not backend
- UDP `dig @<vip> ...` → reaches DNS backend
- Existing nftables path still works for bridge traffic (no regression)

### Phase 2: TC ingress DNAT for bridge traffic (3 weeks)

**Goal:** Replace nftables PREROUTING hook DNAT with BPF tc ingress on the `cca-net` bridge. After this phase, nftables is only used for POSTROUTING masquerade.

**Deliverables:**
- Rust BPF program: `cca_tc_ingress` with conntrack kfuncs
- Go: attach tc ingress program to bridge interface, extend map reconciliation
- Tests: netns integration test with veth pair simulating container → VIP → backend
- Benchmark: compare tc BPF DNAT latency vs nftables DNAT at 100/500/1000 services

**What changes for production traffic:**
- Container-to-VIP traffic uses BPF tc ingress DNAT instead of nftables
- Conntrack NAT state is established via kfuncs (kernel handles reply reverse-NAT)
- nftables `ip cca` table is reduced to postrouting masquerade only

**Effort:** 3 weeks (1 week Rust program + conntrack kfunc integration, 1 week Go integration, 1 week testing + verification)

**Exit criteria:**
- Container `curl <vip>:<port>` → reaches backend on any node
- Reply packets have correct source (VIP, not backend IP) — verifies conntrack kfunc NAT
- Established flow fast path: second request on same TCP connection skips backend selection
- No nftables DNAT rules exist (only masquerade)
- Cross-node traffic works (kernel VXLAN unaffected)
- Benchmark shows latency at parity or better than nftables at 100+ services

### Phase 3: BPF SNAT + nftables removal (2 weeks)

**Goal:** Replace nftables POSTROUTING masquerade with BPF tc egress. Remove the `ip cca` nftables table entirely. The data plane is 100% BPF.

**Deliverables:**
- Rust BPF program: `cca_tc_egress` for SNAT
- Go: remove nftables dependency when `--dataplane ebpf`
- Tests: cross-node DNAT + SNAT end-to-end with BPF only

**What changes for production traffic:**
- Zero nftables rules for the eBPF data plane mode
- SNAT is handled by BPF on the physical NIC egress path
- `nft list tables` shows no `ip cca` table

**Effort:** 2 weeks (1 week Rust program, 1 week testing)

### Phase 4: BPF tunnel encapsulation (3 weeks, optional)

**Goal:** Replace kernel VXLAN interface with BPF-based VXLAN encapsulation. Eliminate `cca-vxlan0`, FDB entries, and VXLAN reconciler.

**Deliverables:**
- Rust BPF: tunnel encap on tc egress of bridge, tunnel decap on tc ingress of physical NIC
- Go: populate `cca_tunnel_peers` map from subnet/advertise-address facts, disable VXLAN reconciler
- Tests: cross-node container-to-container connectivity with BPF-only tunneling

**What changes for production traffic:**
- No `cca-vxlan0` interface, no kernel VXLAN module
- BPF handles encap/decap, routing, and MAC resolution via maps
- VXLAN reconciler is replaced by tunnel peer map reconciliation

**Effort:** 3 weeks (1 week Rust encap/decap, 1 week Go map management, 1 week testing)

**Decision criteria:** Only pursue if Phase 2 benchmarks show kernel VXLAN overhead is material, or if the simplified operational model (no kernel VXLAN interface) justifies the effort.

### Phase 5: Health-aware backend selection + metrics (2 weeks, optional)

**Goal:** BPF programs skip backends marked unhealthy. Expose per-service, per-backend connection counts and byte counters.

**Deliverables:**
- Add `health` field to `cca_backends` map value (set by Go control plane from probe facts)
- BPF programs skip backends where `health == 0`
- Per-CPU counters map for connection/byte metrics
- `cca top services` reads BPF counters

**Effort:** 2 weeks (1 week BPF changes + Go integration, 1 week metrics + CLI)

---

## Effort Summary

| Phase | Scope | Weeks | Cumulative |
|---|---|---|---|
| Phase 1 | Socket LB (host namespace) | 3 | 3 |
| Phase 2 | TC ingress DNAT (bridge traffic) | 3 | 6 |
| Phase 3 | BPF SNAT, remove nftables | 2 | 8 |
| Phase 4 | BPF tunnel encapsulation (optional) | 3 | 11 |
| Phase 5 | Health-aware selection + metrics (optional) | 2 | 13 |

**Phases 1-3 deliver a complete eBPF data plane:** 8 weeks total, ~1050 lines of Rust, ~600 lines of Go. After Phase 3, the `--dataplane ebpf` mode has zero nftables dependency and full VIP load balancing via BPF.

**Phases 4-5 are optimizations.** They improve performance and observability but are not required for functional parity with nftables.

---

## Recommendation: Build Independent eBPF

**Build it. Do not integrate Cilium.**

### Rationale

1. **Cilium integration is harder than it looks.** M126-M127 invested ~1 week building the kvstore translation layer. Bare-metal testing revealed that the kvstore service path does not work, tunnel mode requires re-architecture, and Ubuntu 24.04 iptables compatibility is broken. The estimated remaining work for a working Cilium integration (tunnel mode + API service reconciler + debugging) is 4-6 weeks — nearly as much as Phases 1-3 of the independent path.

2. **Independent eBPF is less code.** ~1050 lines (Rust + Go) vs ~800 lines of Go already written for Cilium + ~400 lines for the API reconciler + an external 200MB binary per node + an operator on the control plane. The independent path has fewer moving parts.

3. **Independent eBPF fits the architecture.** CCattler's design philosophy is "no component may assume another component performed an action" and "store facts and desired results, not commands." The independent eBPF path writes BPF map entries from facts — same pattern as nftables reconciliation. The Cilium path requires imperative REST API calls to an external agent — an architectural misfit.

4. **Debugging is transparent.** When VIP load balancing breaks with independent eBPF: `bpftool map dump pinned /sys/fs/bpf/cca/cca_services` shows exactly what the BPF program sees. When it breaks with Cilium: read Cilium source code, check Cilium's internal BPF map format, debug Cilium's kvstore client, check if the API service registration was accepted.

5. **No external dependency lifecycle.** Cilium releases every 3 months. Each release may change kvstore schema, API endpoints, BPF map formats, or iptables behavior. Independent eBPF has no external release cycle to track.

6. **Features we don't need.** Cilium provides L7 policy, Hubble observability, bandwidth management, transparent encryption, multi-cluster mesh, and BGP peering. CCattler needs VIP DNAT and SNAT. The independent path builds exactly what is needed, nothing more.

### When to reconsider Cilium

Revisit Cilium integration if:
- CCattler needs L7 network policy enforcement (HTTP method/path matching in the kernel)
- CCattler needs transparent encryption (WireGuard/IPsec) between nodes
- The Cilium project ships a non-Kubernetes service registration API
- The independent eBPF maintenance burden exceeds 1 week per quarter

### What happens to M126-M127 code

The Cilium kvstore translation layer (`network/cilium_*.go`, build tag `cilium`) remains in the codebase behind the build tag. It is not deleted — it represents working code for identity and IP mapping that could be useful if Cilium integration is revisited. The `--dataplane cilium` flag continues to work (for testing), but `--dataplane ebpf` becomes the recommended eBPF path.

---

## Kernel Requirements

| Feature | Minimum kernel | Distribution support (2026) |
|---|---|---|
| BPF socket LB (cgroup/connect4) | 4.17 | All modern distros |
| BPF tc classifier | 4.1 | All modern distros |
| BPF hash maps, LRU maps | 4.10 | All modern distros |
| BPF LPM trie | 4.11 | All modern distros |
| CO-RE (BTF, bpf_core_read) | 5.5 | Debian 12+, Ubuntu 22.04+, RHEL 9+ |
| Conntrack kfuncs (bpf_skb_ct_alloc) | 6.0 | Debian 12+, Ubuntu 24.04+, RHEL 10+ |
| Conntrack NAT kfuncs (bpf_ct_set_nat_info) | 6.1 | Debian 12+, Ubuntu 24.04+, RHEL 10+ |
| TCX (tc attach via BPF link) | 6.6 | Ubuntu 24.04+, RHEL 10+ |

**CCattler eBPF data plane minimum: kernel 6.1+.** This is the same requirement as the original ebpf-data-plane.md plan. Distribution support is wider than 6 months ago: Debian 12 (6.1), Ubuntu 24.04 (6.8), RHEL 10.0 (6.12), Amazon Linux 2023 (6.1), Fedora 39+ (6.5+).

Distros excluded: RHEL 9.x (5.14), Rocky/Alma 9.x (5.14), Ubuntu 22.04 (5.15), SLES 15 (5.14). These use the nftables data plane.

---

## Risk Analysis

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| BPF verifier rejects conntrack kfunc usage | Medium | High (blocks Phase 2) | Phase 1 PoC: write minimal tc program that calls `bpf_skb_ct_alloc` + `bpf_ct_set_nat_info`. Verify in netns on kernel 6.8. |
| aya-ebpf crate breaks on nightly Rust update | Low | Medium | Pin nightly version in `rust-toolchain.toml`. CI builds with pinned version. |
| BPF program performance worse than nftables | Very low | Low | Socket LB is faster than netfilter hook traversal. TC DNAT with hash map is faster than nftables verdict map. Benchmark in Phase 2. |
| Fragmented packets bypass tc DNAT | Medium | Medium | Phase 2: tc ingress runs before nf_defrag. Mitigation: clamp TCP MSS to 1400, handle first-fragment DNAT, pass subsequent fragments. |
| Map update races between agent reconciliation and BPF program reads | Low | Medium | BPF map reads are atomic per-entry. Backend ordering (add new before removing old) ensures the service always has valid backends. |

---

## References

- [ebpf-data-plane.md](ebpf-data-plane.md) — Original eBPF plan (nftables first, eBPF deferred)
- [cilium-integration-v2.md](cilium-integration-v2.md) — Cilium integration attempt (tunnel mode + API)
- [cilium-ebpf-debug.md](cilium-ebpf-debug.md) — Bare-metal Cilium testing debug log
- [networking.md](networking.md) — CCattler network architecture
- [cilium/ebpf library](https://github.com/cilium/ebpf) — Go BPF loader (MIT license)
- [aya-rs/aya](https://github.com/aya-rs/aya) — Rust eBPF framework (MIT/Apache dual license)
- [BPF conntrack kfuncs](https://docs.ebpf.io) — `bpf_skb_ct_alloc` (6.0+), `bpf_ct_set_nat_info` (6.1+)
- [Cilium socket LB design](https://docs.cilium.io/en/stable/network/kubernetes/kubeproxy-free/) — Reference for cgroup/connect4 approach
