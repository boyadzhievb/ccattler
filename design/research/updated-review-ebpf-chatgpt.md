# Review of the updated eBPF design

Verdict: substantially improved, but still not ready for implementation approval. The new Phase 0, the recognition of map-update races, and the split between DNAT and SNAT make this a more useful engineering plan. However, the central conntrack assumption remains incorrect, and the new TC egress design does not fix it.

I would approve a proof of concept to investigate the packet path, but I would not approve the proposed architecture or its seven-week estimate until the NAT model is settled.

## 1. Blocking issues

### P0 — The conntrack/NAT assumption is still wrong

The design still states:

> Both hooks run after kernel conntrack, so return-path NAT is handled automatically.

They do not. TC ingress runs before the ordinary netfilter `PREROUTING` and conntrack path. TC egress operates in the device-transmit path, after the normal routing and `POSTROUTING` stages. These hook positions matter because conntrack must know the NAT mapping, not merely observe a packet whose headers have changed. 【turn334050search0】【turn334050search4】

Consider a client connecting to a VIP:

1. The original TCP packet is addressed to the VIP.
2. TC ingress rewrites its destination to a backend.
3. Normal conntrack processing sees the rewritten packet.
4. The backend replies with its own address as the source.

A plain BPF header rewrite does not establish the equivalent of the original VIP-to-backend DNAT mapping. The reply therefore cannot be assumed to have its source rewritten back to the VIP. The TCP connection may fail even though the initial SYN reaches the backend.

The same problem applies to SNAT. Rewriting the source address at TC egress does not automatically register the corresponding connection-tracking SNAT state.

Recommended correction: Specify one of these architectures explicitly:

- Kernel conntrack/NAT integrated with BPF: Use the supported BPF conntrack and NAT mechanisms, including lookup/allocation, NAT metadata, insertion, and packet rewriting as appropriate.
- Netfilter-based NAT: Keep the stateful NAT operations in a supported netfilter path while using eBPF for an appropriate part of backend selection or packet processing.
- Fully custom BPF NAT: Track flows and implement both forward and reply translations in BPF. This is a much larger scope than the current design suggests.

The kernel does provide relevant BPF conntrack mechanisms: `bpf_skb_ct_alloc` is documented from Linux 6.0, and `bpf_ct_set_nat_info` from Linux 6.1. These demonstrate a possible direction, not a complete, automatically working DNAT implementation. 【turn344475view1】【turn344475view0】

The minimum supported kernel and capability detection must be derived from the chosen solution. A generic 5.10 baseline cannot be assumed sufficient for this particular approach.

### P0 — TC egress is not a drop-in replacement for POSTROUTING MASQUERADE

The proposal replaces the existing iptables MASQUERADE rule with a TC egress program that rewrites the packet's source IP. This changes the location of NAT processing without establishing how the matching flow state is created and reversed.

There is also an underspecified step:

> Check if the packet was DNAT'd by the ingress program (via a BPF map flag or conntrack mark).

A BPF map does not inherently attach metadata to a packet. A conntrack mark must also be set explicitly through an appropriate mechanism. You need to define how the ingress decision is communicated to egress, how the packet is associated with the correct flow, and what handles the return path.

Recommended correction: Do not replace MASQUERADE with TC egress source rewriting until the complete cross-host round trip passes integration tests. Either implement proper kernel NAT state or document and test the complete custom forward/reverse translation mechanism.

Do not assume XDP fixes this problem either. Moving a header rewrite to an earlier hook does not, by itself, establish stateful NAT.

### P1 — The fragmentation correction is still incorrect

The updated design says:

> TC ingress sees reassembled packets only if `nf_defrag_ipv4` is loaded.

Loading conntrack's IPv4 defragmentation support does not mean packets have already been reassembled when they reach TC ingress. TC ingress runs earlier in the receive path than the relevant netfilter processing.

Non-initial IPv4 fragments do not contain TCP or UDP port headers. Consequently, rewriting only the first fragment's destination can leave the remaining fragments addressed to the VIP, breaking packet reassembly or subsequent processing.

The statement that UDP fragmentation is handled by the application layer is also incorrect: IP fragmentation and reassembly happen below the application layer.

Recommended correction: Either move the relevant translation to a hook where the required defragmentation has already occurred, implement correct fragment-aware processing, or explicitly exclude fragmented traffic from the supported configuration with a safe handling strategy. Passing fragments through while DNATing the first fragment is not a safe fallback.

### P1 — The version-check mechanism is not a correct seqlock

The new concurrency section is a useful improvement, but the proposed version check does not guarantee a consistent backend set during removal.

For example, suppose the backend indices are:

| Index | Current backend |
| ----- | --------------- |
| 0     | A               |
| 1     | B               |
| 2     | C               |

Now remove B and compact the list.

The provider updates the service count and version, then deletes B and moves C into index 1. Between these operations, the BPF program can still see B at index 1, or find no entry at an index it selects. Because the version is not necessarily changing throughout the mutation, reading it before and after the lookup may return the same value.

That is not the consistency guarantee the design claims. A true sequence-counter pattern needs to bracket mutations, and even that is awkward when modifying several maps in place.

Recommended correction: Use generation-based publication:

1. Keep the currently active backend generation untouched.
2. Populate the new generation completely.
3. Validate the new service configuration and backend entries.
4. Atomically switch the service's active-generation reference.
5. Retire the old generation only after it is safe to do so.

Include the active generation in the relevant map keys. This avoids relying on an in-place, multi-map update behaving atomically.

There is a related issue with `sticky_affinity`: storing only `backend_index` means an existing affinity entry can point to a different backend after index compaction. Store a stable backend identity or otherwise validate and migrate affinity entries when a generation changes.

## 2. Remaining important issues

### P1 — Host-originated traffic and health probes still conflict with the design

The existing iptables implementation installs jumps in both `PREROUTING` and `OUTPUT`. TC ingress on physical or virtual interfaces does not intercept packets originating from local host processes through the `OUTPUT` path.

That means this statement is not generally correct:

> Health probes from the agent to a service VIP will be DNAT'd by the BPF program like any other packet.

A probe originating in the host network namespace will not ordinarily enter through TC ingress. The same consideration applies to other local processes connecting to a VIP and to cookie-proxy redirection targeting localhost.

Recommended correction: Define whether host-originated traffic is supported. If it is, implement an appropriate local-output path and test it independently. Otherwise document the limitation and avoid claiming parity with the existing provider.

### P1 — Canary mode cannot simply run both data planes and compare their decisions

The design proposes keeping iptables rules active while eBPF routes traffic and logs divergence.

For a packet that eBPF successfully DNATs, iptables subsequently sees a packet addressed to the backend rather than the original VIP. The old VIP rule will not select its backend in the same way. Both mechanisms cannot independently route that same packet and produce two comparable outcomes.

There is also a semantic mismatch in the proposed algorithms: the existing iptables implementation uses round-robin-style selection, while the eBPF implementation hashes the source address and port. Different backend choices are expected even if both implementations are correct.

Recommended correction: Implement an initial shadow mode. In that mode, eBPF parses the packet and records which backend it would select, but does not rewrite the packet. Leave iptables responsible for forwarding. Make the comparison account for the two providers' different selection policies, or configure them to use an equivalent deterministic policy for the experiment.

Only enable active eBPF forwarding after the shadow-mode checks and end-to-end tests pass.

### P1 — The Rust safety benefits are overstated

Choosing Rust remains reasonable, but I would tone down several claims in the language decision.

Rust can provide compile-time type checking and memory-safety guarantees within its rules. It does not automatically catch all byte-order errors, protocol mismatches, incorrect packet offsets, or struct-layout mistakes. These bugs can still occur in eBPF code that uses raw pointers and explicit conversions.

The documented BPF Rust target also requires more than installing an ordinary Rust target: current Rust documentation specifies `rust-src`, `bpf-linker`, and a BPF target such as `bpfel-unknown-none`; the documented build path uses nightly Rust with `-Z build-std=core`. `bpf-linker` itself is an LLVM bitcode linker. 【turn263780view1】

I'd make these edits:

- Use the current Aya kernel-side crate name, `aya-ebpf`, rather than starting a new project with the older `aya-bpf` naming. 【turn334050search3】
- Replace “Rust catches endianness, struct alignment and off-by-one bugs at compile time” with “Rust's types and APIs help prevent some classes of mistakes; explicit byte-order conversions, layout assertions and packet-boundary tests remain necessary.”
- Make the toolchain requirements accurate and reproducible. Pin Rust, Aya and `bpf-linker` versions in CI.
- Retain the PoC requirement to prove that the Rust-produced ELF loads using `cilium/ebpf`. Compatibility should be a demonstrated result of the specific object format, map definitions and program sections—not an assumption that every BPF ELF is interchangeable.

I would also remove the claim that Datadog uses this exact Rust-BPF-to-Go-loader stack unless you can point to a specific public implementation proving it. A production eBPF agent is not, on its own, evidence for a particular language and loader combination.

### P1 — The affinity timeout should not be coupled blindly to UDP conntrack timeouts

The design proposes removing affinity entries based on `/proc/sys/net/netfilter/nf_conntrack_udp_timeout`. These represent different kinds of state:

- Conntrack state supports packet-flow tracking and NAT translation.
- Source-IP affinity represents a load-balancing policy that can span multiple flows.

When UDP conntrack expires, it does not necessarily mean a client should be assigned a different backend. Conversely, an affinity entry can outlive the flow for which it was first created.

Recommended correction: Give affinity its own configurable expiry policy. Expire it based on its own timestamp, independently of the conntrack timeout. Validate that an affinity entry still references a valid backend when used.

Also remember that an LRU map provides capacity-based eviction, not time-based TTL. The BPF code or userspace reconciliation must enforce the selected expiry policy.

### P2 — The kernel feature table needs to match the actual implementation

The combined 5.10 minimum is not justified yet. The design relies on kernel conntrack/NAT behavior without specifying how that state is installed. Meanwhile, the newer BPF conntrack/NAT facilities described above have higher minimum kernel versions. 【turn344475view1】【turn344475view0】

Build a requirements table around the chosen implementation, distinguishing mandatory and optional features. Probe the actual capabilities needed, including the required program type, attach mode, map types, BTF/kfunc availability, and conntrack support. Do not treat kernel version alone as sufficient.

Two further configuration details should be resolved:

- `ServiceVIPConfig` currently lacks a protocol field, even though the new map key includes one. Decide whether Phase 1 is explicitly TCP-only or update the configuration API before promising UDP support.
- An explicit `--dataplane ebpf` selection should normally produce a clear startup error if required capabilities are missing. Reserve automatic fallback for a documented `auto` selection or equivalent policy, rather than silently contradicting the operator's choice.

## 3. Implementation plan: what I would change

The phases are more realistic than the original plan, but the sequencing still puts too much of the work behind the wrong acceptance criteria.

| Phase                                | Assessment                            | Recommended adjustment                                                                                                                                      |
| ------------------------------------ | ------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Phase 0 — PoC                        | Good idea, incorrect success criteria | Prove stateful DNAT and reverse translation, not just that the SYN reaches the backend. Include conntrack inspection and the reply packet's source address. |
| Phase 1a — BPF programs              | Too early to commit                   | Finalize the conntrack/NAT design and supported kernel baseline first.                                                                                      |
| Phase 1b — Provider                  | Good boundary                         | Implement generation-safe map publication, cleanup, attachment lifecycle and failure handling.                                                              |
| Phase 2 — Sticky sessions and routes | Reasonable separation                 | Define stable backend identity and independent affinity TTL before implementing stickiness.                                                                 |
| Phase 3 — Integration and CLI        | Reasonable                            | Add explicit provider-selection and fallback semantics.                                                                                                     |
| Phase 4 — Hardening                  | Necessary                             | Include IPv4 fragments, host-originated traffic, cookie proxy, rollback and realistic multi-node testing as acceptance criteria, not just follow-up tasks.  |

Two additional corrections are worth making before starting implementation.

First, remove the hard-coded performance claims. The estimated 10–200 ns map-lookup costs and 90%-of-XDP throughput figures are not universal values. Measure them on the intended kernel, hardware and packet workload. Also describe the complexity claim as expected constant-time service-map lookup, rather than promising O(1) end-to-end connection processing regardless of workload.

Second, make the build pipeline reproducible. If the Rust output files are not committed and the Go binary embeds them, CI must produce those exact ELF files before the Go build. The current `go generate ./network/...` step is insufficient unless a real generation directive or build script connects it to the Cargo output. Pin the toolchain and specify how each BPF artifact is generated and embedded.

## 4. What has improved

The following changes are worth keeping:

- The dedicated PoC phase before full development.
- Explicit backend map capacity limits and a stated consistency strategy.
- Separate ingress and egress program responsibilities, provided the actual NAT state model is corrected.
- Network-namespace tests for cross-host traffic, backend changes and return traffic.
- Observability counters, controlled rollout and rollback planning.
- Keeping HTTP cookie parsing in the userspace proxy.
- Keeping the existing provider interface as the integration boundary.

The central architectural issue remains much more important than the choice between C and Rust. Correct stateful NAT will determine the required hooks, minimum kernel version, map structure, return-path handling and overall project scope.

## Final recommendation

Approve the investigation, not the seven-week implementation commitment yet.

Before moving on to Phase 1, I would require three concrete results from Phase 0:

1. Bidirectional TCP works through the selected NAT mechanism, and the reply reaches the client with the expected VIP source address.
2. Cross-host traffic works with correct SNAT, routing and return-path state.
3. The supported kernel baseline and Rust-ELF-to-Go-loader compatibility are demonstrated on a reproducible test environment.

If the first two cannot be demonstrated with the proposed TC design, revise the architecture rather than trying XDP as a substitute for conntrack integration.

Once those criteria pass, the rest of the design becomes a much more tractable engineering project.