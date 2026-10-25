# eBPF vs nftables Data Plane — Comparison and Roadmap

CCattler supports two data plane backends for service VIP routing and
container networking: **nftables** (default) and **eBPF** (opt-in via
`--dataplane ebpf`). This document covers what each provides, where eBPF
wins, what Cilium-level features are possible, and what is out of scope.

## Current State (M134)

| Component | nftables | eBPF |
|---|---|---|
| VIP → backend translation | DNAT rule per service | Socket LB (cgroup connect/sendmsg) |
| Reverse NAT for getpeername | conntrack | BPF reverse_nat map |
| Bridge DNAT (container → VIP) | nftables PREROUTING | TC program + conntrack alloc |
| Egress SNAT (overlay → physical) | nftables POSTROUTING masquerade | TC program on physical NIC |
| Kernel requirement | 5.x+ (nft) | 6.1+ (bpf_skb_ct_alloc) |
| Deploy | Binary only | Binary only (ELF embedded via go:embed) |
| Map sharing | N/A | Single collection — all programs share maps |

## What eBPF Provides Over nftables

### 1. Socket-Level VIP Interception (Biggest Win)

nftables DNAT rewrites packets **after** the TCP handshake starts — the
kernel creates a conntrack entry, evaluates the DNAT rule on every SYN,
and must reverse-NAT every return packet.

eBPF socket LB intercepts `connect()` **before** the TCP stack runs. The
kernel TCP stack sees the real backend IP from the start. No conntrack
entry, no per-packet NAT, no return-path rewriting.

**Impact**: Lower connection latency, lower CPU per connection, no
conntrack table pressure from VIP traffic.

### 2. Constant-Time Service Lookup

nftables evaluates rules linearly. With 100 services, every VIP-bound
packet walks 100 rules until it matches (or falls through).

eBPF uses a hash map (`cca_services`). Lookup is O(1) regardless of
service count.

**Impact**: Latency stays flat as services grow. Matters at 50+ services.

### 3. No Rule Management

nftables requires the agent to add/remove rules atomically when services
change. Rule ordering, chain management, and cleanup-on-crash are all
failure modes.

eBPF programs read from maps. The agent updates map entries. Programs
never change after load.

**Impact**: Simpler agent code, no partial-rule-set failure mode.

### 4. SNAT Without Masquerade

nftables masquerade creates a full conntrack entry per outgoing flow from
the overlay. The tc SNAT program rewrites source IP and checksum inline
— no conntrack entry.

**Impact**: Lower memory per cross-node flow, faster SNAT path.

### 5. Single Binary Deploy

nftables depends on the `nft` binary being installed on the host. eBPF
programs are embedded in the Go binary via `go:embed` — no external
dependencies beyond the kernel.

**Impact**: Simpler deploy, no "nft not found" failure mode.

## Cilium Feature Parity Matrix

| Cilium Feature | CCattler Status | Feasibility | Notes |
|---|---|---|---|
| Socket LB (cgroup) | **Done** (M134) | — | connect4, sendmsg4, getpeername4 |
| TC DNAT (per-endpoint) | **Done** (M134) | — | Bridge ingress conntrack DNAT |
| TC SNAT (masquerade) | **Done** (M134) | — | Physical NIC egress |
| Shared map collection | **Done** (M134) | — | Single ELF, single LoadCollectionSpec |
| Embedded in binary | **Done** (M134) | — | go:embed, no /usr/lib install |
| **Network Policy (L3/L4)** | Not started | Medium | New tc program + policy map. Drop/allow per endpoint pair. Most impactful missing feature. |
| **XDP Fast Path** | Not started | Medium | XDP program for known VIPs — skip full network stack on ingress. Up to 10x throughput for UDP. |
| **DSR (Direct Server Return)** | Not started | Hard | Backend replies directly to client. Needs IP-in-IP or GENEVE encap. Eliminates return-path bottleneck. |
| **L7 Policy** | Not started | Hard | HTTP/gRPC-aware filtering. Needs userspace proxy (Envoy) + BPF redirect. |
| **Bandwidth (EDT)** | Not started | Medium | Per-endpoint rate limiting via tc Earliest Departure Time. |
| **Transparent Encryption** | Not started | Hard | WireGuard or IPsec between nodes via BPF redirect. |
| **Host Firewall** | Not started | Medium | Replace ufw/iptables host rules with BPF programs. |
| **BPF-based Monitoring** | Not started | Easy | Perf ring buffer for connection events, drop counters. |

### Recommended Next Steps (Priority Order)

1. **Network Policy (L3/L4)** — Highest value. A tc ingress program per
   endpoint that checks a policy map before allowing traffic. Matches
   Cilium's `CiliumNetworkPolicy` equivalent.

2. **BPF Monitoring** — Low effort, high value. Perf ring buffer emitting
   connection events (new VIP hit, backend selected, SNAT applied). Gives
   visibility into the data plane without tcpdump.

3. **XDP Fast Path** — For UDP-heavy workloads (DNS, metrics). XDP program
   at the NIC driver level for known VIP destinations.

## When to Use Which

| Scenario | Recommended | Reason |
|---|---|---|
| Production cluster, 50+ services | eBPF | O(1) lookup, no conntrack pressure |
| Legacy kernel (< 6.1) | nftables | BPF needs bpf_skb_ct_alloc (6.1+) |
| Cloud VMs (restricted BPF) | nftables | Some clouds block BPF_PROG_LOAD |
| Dev/test with few services | Either | Difference is negligible under 10 services |
| Cross-node Airflow/Celery workers | eBPF | SNAT path is faster, VIP routing is socket-level |
| Debugging networking issues | nftables | `nft list ruleset` + `conntrack -L` are familiar |

## How to Measure the Difference

1. **VIP connection latency**: `curl -w '%{time_connect}'` to a service VIP.
   Socket LB should show lower connect time (no DNAT chain traversal).

2. **Connection rate**: `ab -n 10000 -c 100` through a VIP. eBPF should
   sustain higher connections/second.

3. **Conntrack table size**: `conntrack -C` under load. eBPF socket LB
   connections should not appear in conntrack at all.

4. **CPU overhead**: `perf top` during load. Look for nf_nat, nf_conntrack
   entries — absent with eBPF socket LB.

5. **Service scaling**: Add 100 dummy services, measure VIP latency.
   nftables degrades linearly, eBPF stays constant.

## Architecture

```
                    eBPF Data Plane
                    ===============

  User Process
  ┌──────────┐
  │ connect() │ ←── cgroup/connect4: VIP → backend rewrite
  │ sendmsg() │ ←── cgroup/sendmsg4: UDP VIP rewrite
  │ getpeer() │ ←── cgroup/getpeername4: reverse VIP lookup
  └────┬─────┘
       │
  ┌────▼──────────────────────────────────┐
  │ Kernel TCP/UDP Stack                  │
  │ (sees real backend IP from the start) │
  └────┬──────────────────────────────────┘
       │
  ┌────▼──────────┐     ┌───────────────────┐
  │ Bridge (cca0) │     │ Physical NIC       │
  │ tc ingress:   │     │ tc egress:         │
  │ cca_tc_dnat   │     │ cca_tc_snat        │
  │ (conntrack    │     │ (overlay src IP →  │
  │  DNAT alloc)  │     │  node IP rewrite)  │
  └───────────────┘     └───────────────────┘

  Shared BPF Maps (one collection):
  ┌─────────────────┐  ┌──────────────────┐
  │ cca_services    │  │ cca_backends     │
  │ VIP → svc info  │  │ svc+idx → backend│
  └─────────────────┘  └──────────────────┘
  ┌─────────────────┐  ┌──────────────────┐
  │ cca_reverse_nat │  │ cca_conntrack    │
  │ cookie → VIP    │  │ flow tracking    │
  └─────────────────┘  └──────────────────┘
  ┌─────────────────┐
  │ cca_config      │
  │ node_ip, CIDR   │
  └─────────────────┘
```

## Files

| File | Purpose |
|---|---|
| `network/ebpf/cca_dataplane.c` | Unified BPF source (5 programs) |
| `network/ebpf/cca_maps.h` | Shared map definitions |
| `network/ebpf/cca_helpers.h` | Backend selection, affinity |
| `network/ebpf/cca_ct_helpers.h` | Conntrack helpers |
| `network/ebpf_embed.go` | `go:embed` directive |
| `network/ebpf_dataplane.go` | Go loader + attach logic |
| `network/ebpf_types.go` | Constants and program names |
| `scripts/build-ebpf.sh` | Docker-based BPF compilation |
| `scripts/Dockerfile.bpf` | BPF cross-compiler image |
