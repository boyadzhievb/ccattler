# Data Plane Scaling: nftables + Future eBPF

Issue: #12 — eBPF data plane provider

## Problem

The current iptables `DataPlaneProvider` scales linearly with service count. Each service adds one iptables chain with N rules (one per backend). On every new connection the kernel walks the chain sequentially — the same O(N) scaling as kube-proxy iptables mode. At 500+ services with short-lived connections this becomes a measurable bottleneck.

## Reviews

This design was shaped by three independent reviews:

1. **Internal code review** — recommended Rust for BPF language, Phase 0 PoC, expanded edge cases.
2. **External review round 1** ([chatgpt-review-ebpf-plan.md](chatgpt-review-ebpf-plan.md)) — identified critical correctness issue: BPF TC header rewrite alone does NOT establish kernel conntrack NAT state. Also flagged: host-originated traffic, fragmentation, kernel version conflicts.
3. **External review round 2** ([updated-review-ebpf-chatgpt.md](updated-review-ebpf-chatgpt.md)) — confirmed conntrack kfuncs need kernel 6.1+. Recommended benchmarking nftables before committing to BPF.

**Conclusion:** nftables sets provide the same O(1) hash lookup scaling with full kernel NAT integration, no BPF complexity, and wide kernel support. eBPF conntrack kfuncs require kernel 6.1+, which excludes RHEL 9, Rocky 9, AlmaLinux 9, and SLES 15 — common production distros. nftables is the right primary solution. eBPF becomes an optimization for kernel 6.1+ environments if benchmarks show nftables is insufficient.

## Strategy: two-phase approach

| Phase | Provider | Kernel | Lookup | NAT | Complexity |
|---|---|---|---|---|---|
| **M121 (now)** | nftables sets | 3.13+ (all modern distros) | O(1) hash set | Full kernel conntrack | ~300 lines Go |
| **Future** | eBPF + conntrack kfuncs | 6.1+ (Debian 12+, Ubuntu 24.04+, RHEL 10+, AL2023+) | O(1) BPF hash map | BPF kfunc conntrack | ~600 lines Rust + Go |
| **Current** | iptables chains | any | O(N) chain walk | iptables conntrack | ~570 lines Go (exists) |

## Current architecture

```
Agent reconciliation loop
        │
        ▼
DataPlaneProvider interface (network/dataplane.go)
        │
        ├── ReconcileVIPDataPlane(ctx, []ServiceVIPConfig)
        ├── ReconcileRouteDataPlane(ctx, []RouteDataPlaneConfig)
        └── Cleanup(ctx)
        │
        ▼
IptablesDataPlane (network/iptables_dataplane.go, ~570 lines)
        │
        ├── CCA_SERVICES chain (PREROUTING + OUTPUT)
        ├── CCA_SVC_{service} per-service chains (DNAT rules)
        ├── statistic module for round-robin
        ├── xt_recent module for source-IP sticky
        ├── POSTROUTING MASQUERADE for cross-host
        └── cca0 dummy interface for VIP addresses
```

The `DataPlaneProvider` interface was explicitly designed for pluggable backends. The agent wires the provider in `cmd/cca/command_agent.go` (lines 229-231).

---

# Part 1: nftables DataPlaneProvider (M121)

## Why nftables solves the scaling problem

nftables replaces iptables' linear chain walk with **hash set lookups**:

| | iptables (current) | nftables sets |
|---|---|---|
| **Service lookup** | Linear chain walk: O(N) | Hash set: O(1) |
| **Backend selection** | `statistic --mode nth`: walk N rules | Hash set with `numgen`: single lookup |
| **NAT mechanism** | `DNAT` target + conntrack | `dnat` statement + conntrack |
| **MASQUERADE** | `MASQUERADE` target in POSTROUTING | `masquerade` statement |
| **Host-originated** | OUTPUT chain | OUTPUT hook |
| **Conntrack** | Full kernel integration | Full kernel integration |
| **Fragmentation** | Handled by netfilter defrag | Handled by netfilter defrag |

nftables uses the same kernel netfilter infrastructure as iptables — conntrack, NAT, defragmentation all work identically. The only change is how rules are organized: sets with hash lookups instead of linear chain walks.

This eliminates every correctness concern from the external reviews:
- Conntrack NAT state is established by the kernel's `dnat` statement (not BPF header rewrite)
- Host-originated traffic is covered by the OUTPUT hook (same as iptables)
- Fragmentation is handled by netfilter defrag (runs before nftables hooks)
- MASQUERADE uses the kernel's `masquerade` statement with proper conntrack state

## nftables rule structure

```nft
table ip cca {
    # VIP → backend mapping: hash set with DNAT
    set vip_backends {
        type ipv4_addr . inet_proto . inet_service : ipv4_addr . inet_service
        flags interval
    }

    # Per-service backend sets for round-robin
    set web_backends {
        type ipv4_addr . inet_service
    }

    chain prerouting {
        type nat hook prerouting priority dstnat; policy accept;
        ip daddr . ip protocol . th dport vmap @vip_dispatch
    }

    chain output {
        type nat hook output priority dstnat; policy accept;
        ip daddr . ip protocol . th dport vmap @vip_dispatch
    }

    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        ct status dnat masquerade
    }

    # Verdict map: VIP:proto:port → jump to per-service chain
    map vip_dispatch {
        type ipv4_addr . inet_proto . inet_service : verdict
    }

    # Per-service chains with numgen round-robin
    chain svc_web {
        dnat to numgen inc mod 3 map {
            0 : 10.100.1.5 . 8080,
            1 : 10.100.2.8 . 8080,
            2 : 10.100.3.2 . 8080
        }
    }
}
```

The `vip_dispatch` verdict map is the O(1) lookup: it hashes {VIP, protocol, port} and jumps directly to the per-service chain. No linear walk.

Within each service chain, `numgen inc mod N` provides round-robin across backends. For source-IP sticky, replace `numgen` with `jhash ip saddr mod N`.

## Go provider implementation

```go
type NftablesDataPlane struct {
    conn        *nftables.Conn    // go-nftables connection
    table       *nftables.Table   // "cca" table
    dispatchMap *nftables.Set     // vip_dispatch verdict map
}
```

### ReconcileVIPDataPlane flow

1. Build desired nftables state from `[]ServiceVIPConfig`
2. Read current nftables state (`nft list table ip cca`)
3. Diff: add new service chains/map entries, remove stale ones, update changed backends
4. Apply changes as a single nftables transaction (atomic — all-or-nothing)

nftables transactions are **atomic at the kernel level**. A batch of rule changes either all apply or none do. This eliminates the concurrent-update races that plagued the BPF map approach — no seqlock, no generation-based publication needed.

### ReconcileRouteDataPlane flow

Routes with source-IP sticky use `jhash ip saddr mod N` in place of `numgen`. Routes with cookie sticky redirect to the cookie proxy port using `redirect to :proxyPort`. Both use standard nftables statements with full conntrack integration.

### Cleanup

`nft delete table ip cca` removes all rules, chains, sets, and maps in one atomic operation. Clean teardown on agent shutdown.

## API changes

### ServiceVIPConfig: add Protocol field

Add `Protocol string` to `ServiceVIPConfig`. The nftables provider needs it for the verdict map key. Phase 1 scope: IPv4/TCP only. UDP added after TCP path is validated.

## Source-IP sticky sessions

nftables supports stateful set elements with timeouts:

```nft
set sticky_web {
    type ipv4_addr : ipv4_addr . inet_service
    timeout 5m
    flags dynamic
}

chain svc_web {
    ip saddr @sticky_web dnat to ip saddr map @sticky_web
    update @sticky_web { ip saddr : numgen inc mod 3 map { ... } }
    dnat to numgen inc mod 3 map { ... }
}
```

The `timeout` is natively enforced by the kernel — no Go-side cleanup needed. This is cleaner than both the iptables `xt_recent` module and the BPF LRU map approach.

## Cookie-based sticky sessions

Same approach as iptables: redirect to the userspace cookie proxy port. The nftables `redirect` statement establishes proper conntrack state for the proxy's return path.

```nft
chain route_web_public {
    redirect to :9801
}
```

## Build pipeline

```
network/
├── nftables_dataplane.go       # NftablesDataPlane provider
├── nftables_dataplane_test.go  # tests
├── nftables_ruleset.go         # nft rule generation from ServiceVIPConfig
└── nftables_ruleset_test.go    # rule generation tests
```

**Dependencies:** `github.com/google/nftables` (pure Go nftables library, uses netlink — no `nft` binary needed).

No Rust toolchain, no clang, no BPF verifier, no compiled bytecode. Pure Go.

## Testing strategy

### Unit tests

- Rule generation: `ServiceVIPConfig` → expected nftables ruleset
- Diff computation: current rules vs desired rules → correct add/remove/update operations
- Atomic transaction: verify all changes apply as one batch

### Integration tests (network namespaces)

```
[client ns] ──veth──> [node ns with nftables] ──veth──> [backend ns]
```

- TCP connection through DNAT + reverse-NAT (reply has VIP source)
- Host-originated VIP connection (OUTPUT hook)
- Cross-host: MASQUERADE with correct conntrack state
- Hairpin: same-node client and backend
- Source-IP sticky: same client → same backend
- Cookie proxy redirect: full HTTP request/response
- Backend removal mid-connection: existing connections unaffected
- Concurrent reconciliation: atomic transaction, no partial state

### Benchmark (iptables vs nftables)

`cca benchmark dataplane` comparing:
- New connection latency (p50, p99) at 10, 100, 500, 1000 services
- Established flow packet cost
- Throughput (connections/sec)
- Rule update latency (adding/removing a backend)
- Memory usage

## Implementation phases (M121)

### Phase 1: Core provider (1 week)

- `NftablesDataPlane` implementing `DataPlaneProvider`
- nftables table/chain/set/map creation
- `ReconcileVIPDataPlane`: verdict map + per-service chains with `numgen` round-robin
- PREROUTING + OUTPUT hooks (ingress + host-originated)
- POSTROUTING masquerade for cross-host
- Atomic transactions via `go-nftables`
- Add `Protocol` field to `ServiceVIPConfig`
- Unit tests for rule generation

### Phase 2: Sticky sessions + routes (1 week)

- Source-IP sticky via `jhash` + dynamic sets with kernel-enforced timeout
- `ReconcileRouteDataPlane`: cookie proxy redirect
- Integration tests with netns + veth
- Full TCP round-trip validation (forward DNAT + reply reverse-NAT)

### Phase 3: Agent integration + CLI (0.5 week)

- Wire `NftablesDataPlane` into `cmd/cca/command_agent.go`
- `--dataplane auto|nftables|iptables` flag
- `auto` mode: prefer nftables if available, fall back to iptables
- Explicit `--dataplane nftables` fails if nftables unavailable
- `cca benchmark dataplane` command

### Phase 4: Hardening + benchmark (1 week)

- Edge cases: UDP, fragmentation, ICMP, health probes hitting VIP
- Shadow mode: run nftables alongside iptables, log decision divergence
- Multi-node test on testbed-100.43
- Benchmark iptables vs nftables at 10/100/500/1000 services
- Graceful migration: drain iptables after nftables confirmed working
- Publish benchmark results in website networking docs

**Total estimate: ~3.5 weeks**

## Kernel detection

```go
func DetectDataPlaneProvider(explicit string) DataPlaneProvider {
    // --dataplane nftables: probe for nftables support (netlink).
    //   FAIL with clear error if unavailable.
    //
    // --dataplane iptables: current behavior.
    //
    // --dataplane auto (default):
    //   try nftables → fall back to iptables.
    //   Log which provider was selected and why.
}
```

The `go-nftables` library uses netlink directly — if the kernel supports nftables (3.13+), it works. No external binaries needed.

## Migration from iptables

### Shadow mode

Before switching live traffic, run nftables alongside iptables in shadow mode:
- nftables rules are installed but with a higher priority (lower precedence) than iptables
- The agent logs what nftables would do for each reconciliation cycle
- Compare backend selections accounting for different algorithms (round-robin vs round-robin is deterministic, so decisions should match)

Only enable nftables as the active provider after shadow validation passes.

### Cutover

1. Install nftables rules (atomic transaction)
2. Verify nftables is handling traffic (check counters)
3. Remove iptables rules
4. If nftables fails, re-install iptables within one reconciliation cycle

### Coexistence

During migration, both `CCA_SERVICES` iptables chains and the `cca` nftables table may exist. nftables and iptables use separate kernel subsystems (`nf_tables` vs `xt_tables`) — they don't conflict unless both try to NAT the same packet. The shadow mode priority ordering prevents this.

---

# Part 2: eBPF DataPlaneProvider (Future)

Deferred until benchmarks show nftables is insufficient, or kernel 6.1+ is ubiquitous.

## When to revisit

- nftables benchmarks show bottlenecks at 1000+ services with short-lived connections
- Kernel 6.1+ is the minimum supported kernel for CCattler (RHEL 10 adoption)
- Per-packet custom logic is needed beyond DNAT (e.g., in-kernel cookie parsing, custom load balancing algorithms)

## Kernel 6.1+ distribution support (as of 2026)

| Distribution | Kernel | Has 6.1+? |
|---|---|---|
| Debian 12 "Bookworm" | 6.1 | Yes |
| Debian 13 "Trixie" | 6.12 | Yes |
| Ubuntu 24.04 LTS | 6.8 | Yes |
| RHEL 10.0 | 6.12 | Yes |
| Amazon Linux 2023 | 6.1 | Yes |
| Fedora 39+ | 6.5+ | Yes |
| **RHEL 9.x** | **5.14** | **No** |
| **Rocky/Alma 9.x** | **5.14** | **No** |
| **SLES 15 SP4+** | **5.14** | **No** |
| **Ubuntu 22.04 LTS** | **5.15** | **No** |
| **Oracle Linux UEK 7** | **5.15** | **No** |

## eBPF design summary (preserved for future reference)

### The central problem: stateful NAT

BPF TC header rewrite alone does NOT establish kernel conntrack NAT state. The kernel sees the rewritten tuple, not the original VIP→backend mapping. Reply packets cannot be reverse-NATed.

**Solution:** BPF conntrack kfuncs (`bpf_skb_ct_alloc`, `bpf_ct_set_nat_info`, `bpf_ct_insert_entry`) — available from kernel 6.1+. These create proper kernel NAT entries from within the BPF program.

### Architecture

```
EbpfDataPlaneProvider (Go)
        │
        ├── cilium/ebpf library (load BPF ELF, manage maps)
        │       └── loads .o from aya-ebpf (Rust)
        │
        ├── TC ingress: hash map lookup → backend selection → conntrack kfunc NAT → header rewrite
        ├── TC egress: MASQUERADE for cross-host (or keep iptables rule during transition)
        ├── cgroup/connect4: host-originated VIP traffic
        │
        └── BPF maps: vip_services, vip_backends, sticky_affinity
```

### Key design decisions (from reviews)

- **Rust (aya-ebpf)** for BPF programs — compile-time type safety for conntrack kfunc struct layout. Use current `aya-ebpf` crate. Pin Rust + aya-ebpf + bpf-linker versions. Requires nightly Rust with `-Z build-std=core`.
- **Generation-based map publication** — seqlock insufficient for backend removal with index compaction. Populate new generation completely, switch atomically, retire old generation.
- **Shadow mode** for canary — log BPF's backend selection without rewriting packets. Do not run both BPF and iptables rewriting the same packet.
- **Affinity TTL independent of conntrack timeout** — they represent different state. BPF-side expiry check against configurable TTL, not `/proc/sys/net/netfilter/nf_conntrack_udp_timeout`.
- **Fragmentation** — TC ingress runs before `nf_defrag_ipv4`. Cannot safely DNAT first fragment and pass subsequent fragments. Must either exclude fragmented traffic or implement fragment-aware processing.
- **Established flow fast path** — `bpf_skb_ct_lookup` to skip backend selection for flows with existing conntrack NAT state.
- **Phase 0 exit criteria** — bidirectional TCP with correct conntrack NAT state (verify reply source is VIP, not backend), cross-host MASQUERADE, Rust ELF → cilium/ebpf compatibility. All demonstrated in netns test.
- **.o files not committed** — CI generates from Rust source. Connect Cargo output to Go embed via build script.

### Estimated effort

~7 weeks if pursued. See previous revisions of this document for the detailed phase breakdown.

## Reference material

- [cilium/ebpf library](https://github.com/cilium/ebpf) — Go BPF loader, map management
- [aya-rs](https://github.com/aya-rs/aya) — Rust eBPF library (current kernel crate: `aya-ebpf`)
- [google/nftables](https://github.com/google/nftables) — Pure Go nftables library via netlink
- [eunomia-bpf tutorial lesson 42](https://github.com/eunomia-bpf/bpf-developer-tutorial) — XDP load balancer patterns
- [Sysdig eBPF primer](https://www.sysdig.com/blog/the-art-of-writing-ebpf-programs-a-primer) — verifier constraints
- [Cilium kube-proxy-free docs](https://docs.cilium.io/en/stable/network/kubernetes/kubeproxy-free/) — production eBPF service LB
- [BPF conntrack kfuncs](https://docs.ebpf.io) — `bpf_skb_ct_alloc` (6.0+), `bpf_ct_set_nat_info` (6.1+)
- [nftables wiki](https://wiki.nftables.org/) — nftables sets, maps, verdicts, transactions
