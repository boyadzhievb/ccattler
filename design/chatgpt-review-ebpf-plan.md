# Review: eBPF Data Plane Design

Verdict: promising direction, but not ready for implementation as currently written. The Go provider interface and the choice of C with `cilium/ebpf` are reasonable. The major risk is the assumption that rewriting packet headers at TC ingress automatically integrates with Linux conntrack and stateful NAT. That assumption undermines the return path, cross-host routing, and the proposed migration strategy.

I checked the linked [GitHub issue #12](https://github.com/boyadzhievb/ccattler/issues/12?utm_source=chatgpt.com) and the current `DataPlaneProvider` and `IptablesDataPlane` implementations. The existing provider abstraction is real, and the implementation is TCP-oriented today, so the design has a useful foundation—but several details need to be resolved before committing to its packet-processing architecture.

## 1. Critical correctness issues

P0 — TC DNAT does not automatically establish conntrack NAT state

The design says TC ingress rewrites the destination, then kernel conntrack creates the NAT entry and reverse-NATs replies. That is not how this works automatically.

TC ingress runs before the normal IP `PREROUTING` path. If BPF changes the VIP destination to the backend address first, subsequent conntrack processing sees the rewritten tuple; it has not necessarily been given the original VIP-to-backend NAT mapping that iptables DNAT establishes. Reply packets therefore cannot be assumed to be reverse-NATed back to the VIP.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://www.kernel.org\&sz=32)

The Linux Kernel documentation

+2



Required change: Specify the actual stateful NAT mechanism. One option is to use the kernel's dedicated BPF conntrack/NAT facilities, including the required connection allocation, NAT metadata setup, and insertion operations, with a compatible kernel baseline. Another is to retain a netfilter-based NAT path. Do not proceed on the assumption that a header rewrite alone is sufficient.

P0 — Cross-host MASQUERADE will not necessarily work

The existing POSTROUTING rule matches `--ctstate DNAT`. If eBPF performs the destination rewrite without establishing the corresponding conntrack NAT state, that rule may not match the packet. The assertion that existing MASQUERADE can remain unchanged is therefore unsupported.

Required change: Design and test the complete forward and reply paths for local and remote backends, including source NAT, conntrack state, return routing, and hairpin traffic. Verify the packet's conntrack state, not just whether the initial SYN reaches the backend.

P1 — TC ingress alone misses host-originated VIP traffic

The current iptables provider installs rules in both `PREROUTING` and `OUTPUT`. A TC-ingress program on host interfaces does not cover packets generated locally by host processes, because those packets use the local output path instead.

Required change: Explicitly define support for host-originated VIP connections. Add a suitable interception mechanism for that traffic, or document and enforce that the eBPF provider only supports ingress-originated connections.

P1 — Per-entry updates do not make a multi-map update atomic

The design says the BPF program always sees a consistent entry. That is true only at the individual map-value level. If `backend_count` changes before all corresponding backend entries are published, a packet may select an index whose value is stale or missing.

There is a second issue: hashing each packet by source IP and port does not preserve the selected backend when the backend set changes. Existing TCP connections can be sent to a different backend mid-stream.

Required change: Publish backend sets using a generation-based or double-buffered design, switch the active generation only after the new set is complete, and define how established connections retain their original backend. True stateful NAT or an explicit flow-affinity map can address the latter.

## 2. More issues to fix before implementation

### P1 — Fragmentation handling is incorrect in the comparison table

The design says TC ingress benefits from kernel reassembly before the program runs. That is incorrect: TC ingress precedes the normal IP `PREROUTING` processing where defragmentation normally occurs. Non-initial IPv4 fragments do not contain TCP/UDP ports, so the program cannot perform the same VIP-and-port lookup on them.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://www.kernel.org\&sz=32)

The Linux Kernel documentation

+1



The dangerous case is a fragmented datagram whose first fragment gets DNATed while subsequent fragments retain the VIP destination.

Recommendation: Make fragmentation support an explicit acceptance criterion. Until it is implemented, preserve a safe handling path for fragmented VIP traffic; do not simply pass those packets through after removing the old DNAT rules.

### P1 — The 5.10 kernel baseline conflicts with the proposed conntrack fix

Kernel BPF has specific facilities for creating conntrack entries and configuring NAT. For example, `bpf_skb_ct_alloc` is documented from Linux 6.0, and `bpf_ct_set_nat_info` from 6.1. These are specialized operations, not something ordinary packet-header writes trigger automatically. The kernel also documents kfunc interfaces as potentially unstable.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://docs.ebpf.io\&sz=32)

eBPF Docs

+2



That creates a direct conflict between the proposed minimum kernel version and one plausible implementation of correct stateful DNAT.

Recommendation: Decide the NAT/conntrack design first, then derive the minimum supported kernel and capability probes from that choice. Do not select the minimum based only on hash maps, TC attachment, and BTF availability.

### P1 — The performance comparison may compare different workloads

The current problem statement correctly identifies the iptables rule-chain walk as a new-connection cost. Stateful NAT rules establish a mapping on the first packet; follow-up packets use the established NAT binding instead of repeating the NAT-rule lookup.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://linux.die.net\&sz=32)

Linux man page

+1



The proposed BPF program, by contrast, is attached to TC ingress and parses packets and consults maps whenever it runs. Unless established flows have an appropriate fast path, that means additional BPF work on every packet, not just the first packet of a connection.

This does not invalidate eBPF as an approach, but it changes what must be proven.

Recommendation: Benchmark both new-flow cost and established-flow packet-processing cost. Measure CPU utilization, packets per second, throughput, connection establishment rate, and latency distributions. The claimed roughly 10% TC-versus-XDP throughput gap should also be treated as a hypothesis to measure, not a general property of these implementations. TC and XDP have different processing costs, but the observed difference depends on the workload and program.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://docs.cilium.io\&sz=32)

Cilium 1.20.3 documentation



### P1 — Cookie-proxy redirection needs a working packet-path design

Cookie-based affinity remains a userspace HTTP responsibility, which is the right separation of concerns. However, the proposed TC rewrite to `127.0.0.1:proxyPort` is not automatically equivalent to the current iptables rule that DNATs to the proxy.

The design needs to establish that packets rewritten at ingress are routed to the local proxy correctly, including the relevant loopback-routing restrictions and reply path. The current iptables implementation is a better starting point for defining the required behavior than the phrase “same as iptables REDIRECT.”

Recommendation: Treat cookie-proxy redirection as a separate integration test. Verify a complete HTTP request and response through the proxy before including route support in the production rollout.

### P2 — Sticky-session expiry is underspecified

`BPF_MAP_TYPE_LRU_HASH` provides capacity-based eviction; it does not expire entries according to the `timestamp_ns` field.

The design says expiry is checked in Go, but packets consume the affinity map directly from BPF. An expired mapping could therefore still be used between cleanup passes, unless the BPF program itself checks expiry.

There is also a key-design question: the affinity key contains the client IP and VIP tuple, but no route identifier. That is appropriate only if all routes sharing that tuple intentionally share the same source-IP affinity policy and backend set.

Recommendation: Define whether expiry is enforced in BPF or by a guaranteed cleanup mechanism, and make the affinity key match the intended scope of the routing policy.

### P2 — The API and stated protocol support do not match

The actual [`ServiceVIPConfig`](https://github.com/boyadzhievb/ccattler/blob/master/network/dataplane.go?utm_source=chatgpt.com)[ interface](https://github.com/boyadzhievb/ccattler/blob/master/network/dataplane.go?utm_source=chatgpt.com) has VIP address, port, and backends, but no protocol field. The current iptables implementation installs TCP-specific DNAT rules. The proposal introduces a protocol field in the BPF map key and discusses UDP support, without explaining how protocol selection enters the provider API.

Recommendation: For Phase 1, explicitly scope support to IPv4/TCP if that is all the existing interface guarantees. Add protocol configuration and tests before claiming UDP support. State IPv6 support or non-support explicitly as well.

## 3. Architectural and operational improvements

The remaining changes are less fundamental than the NAT issue, but they will make the implementation safer and easier to maintain.

| Area                        | Recommendation                                                                                                                                                                                                                                                                                          |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Interface attachment        | Document the actual network topology and attach at the interfaces where relevant packets enter the host's L3 path. Attaching TC ingress to `cca0` does not capture host-originated packets, and attaching indiscriminately to every interface risks needless work and complicated lifecycle management. |
| Capability detection        | Probe the required program type, attach mode, map types, BTF/kfunc availability, and required kernel configuration instead of relying only on `uname`.                                                                                                                                                  |
| Explicit provider selection | If the operator explicitly selects `--dataplane ebpf`, fail clearly when required capabilities are unavailable. Reserve silent fallback for an explicit `auto` policy; otherwise the selected data plane can differ from the operator's intent without being obvious.                                   |
| ABI correctness             | Add tests for Go/C struct sizes, padding, and network byte order. Generate shared types where practical and verify the generated object for every supported build architecture.                                                                                                                         |
| Observability               | Report the active provider, attachment failures, map update errors, fallback decisions, selected backends, and BPF processing counters. A data plane that silently falls back or misses an attachment is difficult to troubleshoot.                                                                     |
| Cleanup and migration       | Define rollback if attachment partially succeeds, and specify ownership of links, maps, VIP addresses, and iptables rules. Make transitions safe while old connections are active.                                                                                                                      |

## 4. What I would keep

Several decisions are good and should remain part of the design:

- C + `cilium/ebpf` for the initial implementation. This is a sensible fit for a Go application with a small, narrowly scoped BPF program.
- The existing `DataPlaneProvider` interface. It gives you a natural integration point without requiring changes to the reconciliation controller's public design.
- Userspace control of BPF maps. Keeping desired-state reconciliation in Go is consistent with the existing architecture.
- A staged test strategy. BPF program tests are useful for parsing and rewriting logic; network-namespace tests are essential for routing, conntrack, and real request/response behavior.
- Keeping HTTP cookie handling in userspace. Parsing HTTP framing and cookies inside a TC program would significantly increase complexity.
- Keeping iptables as a supported backend. An established fallback is valuable, provided the selection behavior is explicit and the two implementations do not conflict during transitions.

One thing I would change is the use of “O(1) per connection” as a blanket description. Hash-map lookup gives expected constant-time lookup behavior, but the whole request path includes packet parsing, potentially multiple map lookups, backend selection, packet rewriting, and whatever conntrack operations are needed. The performance benefit has to be demonstrated for the full path.

## 5. Recommended revised plan

I would add a short Phase 0: packet-path feasibility before the existing four phases. It should be a deliberately small technical spike, not the start of the full provider implementation.

### Phase 0 — Validate stateful DNAT

1. Implement a minimal TC BPF program for one IPv4/TCP VIP and one backend.
2. Demonstrate that a complete TCP connection works in both directions, with correct conntrack/NAT state and checksums.
3. Test external ingress, a host-originated VIP connection, same-node traffic, and cross-host traffic.
4. Test backend replacement while a connection is active, plus packet fragmentation and cookie-proxy redirection.
5. Confirm the actual kernel capability baseline and measure the path against the existing iptables implementation.

Exit criterion: a demonstrated packet path with correct return traffic and a validated supported-kernel policy. Do not proceed to full reconciliation until this is satisfied.

After that, I would keep Phase 1 focused on the smallest correct MVP: one supported address family, an explicit protocol scope, working stateful NAT, safe map publication, lifecycle management, and integration tests. Add source-IP affinity once backend selection and established-connection behavior are correct. Add cookie routes and broader protocol support only after the baseline is stable.

I would also benchmark an appropriate nftables-based alternative before committing the team to a custom eBPF NAT implementation. That provides a useful baseline for determining whether the deployment really needs BPF-level packet processing, rather than simply needing a more scalable rule representation.

## Final assessment

The design has a good architectural starting point, but the current draft treats conntrack integration as a solved detail when it is actually the central engineering problem. The stated 5.10 minimum, automatic reverse NAT, retained MASQUERADE rule, fragmentation assumptions, and atomic-update claims need to be revised or demonstrated.

My recommendation is to approve the direction for investigation, but not approve the proposed implementation plan as written. Resolve the stateful NAT model first, validate it in a network namespace, and then lock down the minimum kernel requirements and scope for Phase 1. That will substantially reduce the risk of spending several weeks on a fast lookup mechanism that cannot safely replace the existing packet path.