# tc DNAT for Bridge Traffic — Design

**Date:** 2026-10-19
**Status:** Proposed
**Phase:** M129 (Phase 2 of independent eBPF data plane)
**Depends on:** M128 (Socket LB), [ebpf-data-plane-v2.md](ebpf-data-plane-v2.md), [networking.md](networking.md)

---

## Motivation

M128 shipped socket-level VIP load balancing for host-originated traffic (cgroup/connect4, sendmsg4, getpeername4). But traffic originating inside containers — which traverses the `cca-net` bridge, not the host cgroup — still depends on nftables PREROUTING DNAT rules.

This document describes the tc BPF program that replaces nftables PREROUTING DNAT for container-to-VIP traffic. After M129, nftables is reduced to a single POSTROUTING masquerade rule (removed in Phase 3).

---

## Traffic Path

Container-to-VIP traffic follows this path on the bridge:

```
Container sends to VIP:port
    │ dst MAC = bridge MAC (VIP is off-subnet, ARP resolves to gateway)
    │
    ▼ veth host side → bridge port → bridge receives
    │ bridge sees dst MAC = own MAC → deliver to host IP stack
    │
    ▼ tc ingress on cca-net bridge fires (our BPF program)
    │ BPF creates conntrack DNAT entry via kfuncs
    │ returns TC_ACT_OK (packet unchanged)
    │
    ▼ IP stack: nf_conntrack PREROUTING (-200 priority)
    │ finds BPF-created conntrack entry
    │ nf_nat applies DNAT (rewrites dst IP:port to backend)
    │
    ▼ routing: forwards to backend
    │ local backend: bridge → container veth
    │ remote backend: cca-vxlan0 → VXLAN encap → remote node
    │
    ▼ return path: kernel conntrack reverse-NATs automatically
      (src=backend → src=VIP in reply packets)
```

**Key insight:** The BPF program does NOT rewrite packet headers. It creates a conntrack entry with DNAT info, and the kernel's nf_nat module applies the actual translation. This means:

- No packet parsing beyond IP + L4 headers for tuple extraction
- No checksum recomputation in BPF
- No BPF-side return path handling (kernel conntrack does it)
- Graceful coexistence with nftables during migration

---

## Why Conntrack Kfuncs (Not BPF Header Rewrite)

Three approaches were considered:

### Option A: BPF header rewrite + BPF conntrack map (Cilium approach)
- BPF rewrites packet headers directly
- Custom BPF conntrack map tracks NAT state
- Needs tc programs on every veth pair for return path
- **Rejected:** complex (per-veth tc programs), breaks bridge L2 forwarding model

### Option B: BPF header rewrite + kernel conntrack kfuncs
- BPF rewrites headers AND creates conntrack entries
- Kernel handles return path
- **Rejected:** redundant — if conntrack handles return path, it can also handle forward rewrite

### Option C: BPF conntrack kfuncs only (chosen)
- BPF creates conntrack DNAT entry, does NOT rewrite packet
- Kernel nf_nat applies DNAT (forward) and reverse-NAT (return) automatically
- **Chosen:** simplest BPF code, leverages kernel NAT infrastructure

### Why Option C works

When `bpf_ct_set_nat_info()` is called, it calls `nf_nat_setup_info()` internally, which sets the `IPS_DST_NAT_DONE` status flag on the conntrack entry. When the packet later reaches the nf_nat PREROUTING hook, `nf_nat_initialized()` returns true, so nftables DNAT rules are skipped and `nf_nat_packet()` applies the cached translation directly.

This means:
- BPF-handled connections bypass nftables rules entirely (no conflict)
- If BPF fails to create the entry, nftables DNAT rules still fire (safe fallback)
- Migration is incremental: both paths coexist without interference

---

## BPF Program: `cca_tc_dnat`

### Hook

`SEC("tc")` attached to `cca-net` bridge tc ingress via TCX link (kernel 6.6+).

### Flow

```
1. Parse Ethernet → check ETH_P_IP → parse IP header
2. Extract {src_ip, src_port, dst_ip, dst_port, protocol} tuple
3. bpf_skb_ct_lookup(): check if conntrack entry already exists
   → if yes: release + return TC_ACT_OK (established flow, nf_nat handles it)
4. Lookup cca_services map: {dst_ip, dst_port, protocol}
   → if no match: return TC_ACT_OK (not a VIP, passthrough)
5. select_backend(): pick backend from cca_backends map (random + affinity)
6. bpf_skb_ct_alloc(): allocate new conntrack entry for the original tuple
7. bpf_ct_set_nat_info(): set DNAT target to selected backend
8. bpf_ct_insert_entry(): commit entry to conntrack table
9. bpf_ct_release(): release reference
10. return TC_ACT_OK
```

### Established flow fast path (step 3)

For packets belonging to an already-established connection (subsequent TCP segments, UDP datagrams on a "connected" flow), `bpf_skb_ct_lookup()` finds the existing conntrack entry. The BPF program returns immediately — no service lookup, no backend selection. The kernel's nf_nat applies the cached translation in PREROUTING.

This fast path handles the common case (data transfer) with one conntrack hash lookup instead of two BPF map lookups.

### Port and IP byte order

The `cca_services` map stores ports in the same format as `bpf_sock_addr.user_port`: big-endian bytes loaded as a native uint16. TCP/UDP header ports (`tcph->dest`, `udph->dest`) are `__be16`, which when loaded as a native `__u16` on little-endian produces the same value. No byte order conversion needed for map lookup.

IP addresses in the map match `iph->daddr` format (both are `__be32` bytes loaded natively). Direct comparison works.

For `bpf_ct_set_nat_info()`, the port parameter is in host byte order — use `bpf_ntohs(chosen_backend.port)` to convert.

---

## Conntrack Kfunc Declarations

Kfuncs are kernel functions callable from BPF programs. They require:
- Forward declaration with `__ksym` attribute
- Matching BTF type definitions (verified by the BPF loader)
- `CONFIG_NF_CONNTRACK=y` in kernel config
- Kernel 6.1+ for `bpf_ct_set_nat_info`

The required kfuncs:

| Kfunc | Kernel | Purpose |
|---|---|---|
| `bpf_skb_ct_lookup` | 5.18 | Look up existing conntrack entry |
| `bpf_skb_ct_alloc` | 5.18 | Allocate new conntrack entry |
| `bpf_ct_set_nat_info` | 6.1 | Set DNAT/SNAT on allocated entry |
| `bpf_ct_insert_entry` | 5.18 | Commit entry to conntrack table |
| `bpf_ct_release` | 5.18 | Release conntrack reference |
| `bpf_ct_set_timeout` | 5.18 | Set custom timeout (optional) |

Types needed (not in UAPI headers, defined in `cca_ct_helpers.h`):
- `struct nf_conn` — opaque pointer (forward declaration only)
- `struct bpf_ct_opts` — `{netns_id: s32, error: s32, l4proto: u8, dir: u8, reserved: u8[2]}`
- `union nf_inet_addr` — `{ip: u32}` (IPv4 only)

---

## Map Sharing

The tc program reuses the same BPF maps as the socket LB programs:
- `cca_services` — VIP lookup
- `cca_backends` — backend selection
- `cca_affinity` — session affinity (source-IP sticky)

The tc program is compiled to a separate ELF (`cca_tc_dnat.o`) that includes `cca_maps.h` for identical map definitions. At load time, the Go control plane uses `ebpf.CollectionOptions.MapReplacements` to substitute the tc ELF's maps with the already-loaded maps from the socket LB collection:

```go
tcCollection, _ := ebpf.NewCollectionWithOptions(tcSpec, ebpf.CollectionOptions{
    MapReplacements: map[string]*ebpf.Map{
        "cca_services": ebpfDataPlane.serviceMap,
        "cca_backends": ebpfDataPlane.backendMap,
        "cca_affinity": ebpfDataPlane.affinityMap,
    },
})
```

---

## Bridge Interface Discovery

The tc program attaches to the Linux bridge device that serves the container overlay network. This bridge is created by nerdctl/containerd and has a name like `br-<hash>`.

Discovery algorithm:
1. List all bridge interfaces via netlink (`ip link show type bridge`)
2. For each bridge, check if any assigned IP address falls within the node's overlay subnet (the `/24` allocated from `--cluster-cidr`)
3. Return the first match

The overlay subnet is already known to the agent (`--cluster-cidr` + allocated subnet from VXLAN reconciler). If no bridge is found, tc attachment is skipped and nftables continues to handle bridge traffic (safe degradation).

An optional `--bridge-interface` agent flag allows explicit override.

---

## Go Control Plane Changes

### EbpfDataPlane extensions

New fields:
- `tcELFPath string` — path to compiled tc DNAT ELF (default: `/usr/lib/cca/cca_tc_dnat.o`)
- `tcCollection *ebpf.Collection` — loaded tc BPF program collection
- `tcLink link.Link` — tc ingress attachment to bridge
- `bridgeInterfaceName string` — discovered or configured bridge device name
- `overlaySubnetCIDR string` — node's overlay subnet for bridge discovery

New methods:
- `loadAndAttachTCProgram()` — loads tc ELF with map replacements, attaches to bridge
- `discoverBridgeInterface()` — finds the container bridge by overlay CIDR match
- `detachTCProgram()` — cleanup for tc attachment

### Initialization flow

```
Initialize()
    ├── [existing] load socket LB ELF, pin maps, attach cgroup programs
    └── [new] loadAndAttachTCProgram()
              ├── discoverBridgeInterface()
              ├── LoadCollectionSpec(tcELFPath)
              ├── NewCollectionWithOptions(spec, MapReplacements)
              └── link.AttachTCX(bridge, program, TCXIngress)
```

### Cleanup

`Cleanup()` detaches the tc program (via `tcLink.Close()`), closes the tc collection, then proceeds with existing socket LB cleanup.

---

## Build Pipeline

### New file: `network/ebpf/cca_tc_dnat.c`

Compiled separately from `cca_socket_lb.c`:

```bash
clang -O2 -g -Wall -Werror -target bpf -D__TARGET_ARCH_x86 \
    -I/usr/include -I/usr/include/x86_64-linux-gnu \
    -c network/ebpf/cca_tc_dnat.c -o /usr/lib/cca/cca_tc_dnat.o
```

### Updated `scripts/build-ebpf.sh`

Compiles both ELFs:
1. `cca_socket_lb.c` → `cca_ebpf.o` (existing)
2. `cca_tc_dnat.c` → `cca_tc_dnat.o` (new)

### Updated Makefile

`bpf` target compiles both. `deploy-ebpf` copies both ELFs to testbed.

---

## Testing

### Unit test (Go)

Mock the bridge discovery and verify that `loadAndAttachTCProgram()` attempts to load the tc ELF and attach it. Verify `MapReplacements` includes the correct map handles.

### Integration test (testbed)

1. Deploy updated binary + both ELFs to node-1
2. Start agent with `--dataplane ebpf`
3. From a container: `curl <VIP>:<port>` → should reach backend
4. Verify with conntrack: `conntrack -L | grep <VIP>` shows DNAT entry
5. Verify return path: container sees `src=VIP` not `src=backend`
6. Remove nftables DNAT rules: `nft delete chain ip cca prerouting` → container traffic still works (BPF handles it)
7. Cross-node: container on node-1 → VIP → backend on node-2 (via VXLAN)

### Verification commands

```bash
# Check tc program attached to bridge
tc filter show dev <bridge> ingress

# Check conntrack entries created by BPF
conntrack -L -p tcp --dport <vip_port>

# Verify nf_nat applied DNAT (not BPF header rewrite)
tcpdump -i <bridge> -n 'dst <backend_ip>'
```

---

## Exit Criteria

- Container `curl <VIP>:<port>` reaches backend on any node
- Reply packets show `src=VIP` (conntrack reverse-NAT working)
- `conntrack -L` shows DNAT entries for VIP connections
- Established flow fast path: `bpf_skb_ct_lookup` hits on subsequent packets
- Coexistence: nftables rules still fire for non-BPF connections (if any)
- Cross-node traffic works (VXLAN unaffected)
- No nftables DNAT rules needed (only POSTROUTING masquerade remains)

---

## Kernel Requirements

- Kernel 6.1+ for `bpf_ct_set_nat_info` kfunc
- Kernel 6.6+ for TCX attachment (`link.AttachTCX`)
- `CONFIG_NF_CONNTRACK=y` (standard on all distros)
- `CONFIG_DEBUG_INFO_BTF=y` (required for kfunc resolution)
- Testbed: Ubuntu 24.04 with kernel 6.8 (all requirements met)

---

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| BPF verifier rejects kfunc usage | Medium | High | Test minimal kfunc program first on testbed kernel 6.8 |
| Conntrack entry lifetime/GC issues | Low | Medium | LRU + kernel GC handles cleanup; verify with conntrack -L |
| Bridge interface not found | Low | Low | Graceful degradation: skip tc, log warning, nftables handles it |
| TCX attachment fails (kernel too old) | Very low | Medium | Testbed has 6.8; for older kernels, fall back to classic tc |
| nf_nat race with BPF-created entry | Very low | Medium | Kernel processes hooks sequentially per-packet; no race |
