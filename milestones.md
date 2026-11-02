# Milestones — Active & Upcoming

Open phase checklists and the upcoming milestone table. Completed phases and
milestones (M1–M169) live in [milestones-archive.md](milestones-archive.md).
When a milestone is released, move its checklist and row to the archive.

## Open Phase Checklists

### Phase 136 — HA Etcd Cluster Validation (M136)

First end-to-end validation of HA control plane on vmherd: 3-node etcd raft cluster, leader-elected cca-server, control plane failover, and etcd member failure recovery.

- [ ] vmherd Herdfile: 3 ctrl (4GB) + 1 worker (2GB) on 192.168.125.0/24
- [ ] Ansible inventory mapping vmherd VMs to HA groups (etcd, controlplane, workers)
- [ ] Test workload: whoami (3 instances) + whoami-multiport (2 instances)
- [ ] HA validation script: 9 checks (reachability, etcd health, API, leader election, nodes, workload, VIP, failover, etcd failure)
- [ ] Deploy cluster via `ha-deploy.yml` with existing HA Ansible roles
- [ ] Verify etcd 3-member quorum, leader election, workload placement
- [ ] Control plane failover: stop leader cca-server, verify new leader within 30s
- [ ] etcd member failure: stop one member, verify quorum holds (2/3), API works
- [ ] Recovery: restart failed components, verify they rejoin

### Phase 141 — Airflow Triggerer & Observability (M141)

Future placeholder: additional Airflow services for full Helm chart parity.

- [ ] Triggerer service (deferrable operators, exec-based probes)
- [ ] StatsD metrics exporter sidecar
- [ ] Log groomer via existing logging DSL keyword

### Phase 151 — eBPF Data Plane Correctness (M151)

Harden the eBPF data plane (M128–M134) to the point where it can fully replace nftables for VIP, SNAT, and basic network policy — running a single data plane instead of two in parallel. Discovered during VXLAN CI investigation: tc SNAT was stateless and broke TCP reply packets; agent restart race deleted BPF pin directory; no TCP-level integration tests existed; packet flow matrix was undocumented.

**tc SNAT conntrack validation:**
- [x] Make `cca_tc_snat` conntrack-aware via `bpf_skb_ct_lookup` — skip SNAT for reply-direction packets
- [x] Add `IP_CT_DIR_ORIGINAL`/`IP_CT_DIR_REPLY` defines to `cca_ct_helpers.h`
- [x] Document packet flow matrix in `NETWORK_GRAPH.md` (all source→dest combinations)
- [x] Update `EBPF_GRAPH.md` — SNAT call tree with conntrack, kfuncs table
- [ ] Validate tc SNAT conntrack fix passes 10+ CI runs without init probe regression
- [ ] Verify container-initiated egress (container → external) still works with SNAT

**Agent restart BPF pin race:**
- [ ] Fix `removePinnedMaps()` to only remove pins created by THIS instance (PID-namespaced pin dir or flock)
- [ ] Add integration test: restart agent under load, verify BPF maps survive and service map entries persist
- [ ] Add startup log line: "reusing existing BPF pins" vs "creating fresh BPF pins"

**eBPF TCP integration tests:**
- [ ] Add e2e test: host-ns → remote container TCP via VIP (the init probe path)
- [ ] Add e2e test: container → container TCP cross-node via VIP
- [ ] Add e2e test: container → external host TCP (egress SNAT path)
- [ ] Add e2e test: verify `nc` TCP connectivity, not just ICMP ping, in cluster health check
- [ ] Each test row from NETWORK_GRAPH.md packet flow matrix has a corresponding e2e assertion

Policy and nftables elimination items moved to Phase 190.

### Phase 190 — eBPF Policy & nftables Elimination (M190)

Second half of the original Phase 151 checklist. Depends: M151.

**eBPF network policy (basic):**
- [ ] tc ingress program on cca0 for ingress allow/deny by source CIDR (replaces nftables filter FORWARD)
- [ ] BPF map populated from `derived/network/rule/` facts by agent reconciler
- [ ] Default-deny when at least one policy applies to the service
- [ ] Integration test: policy blocks cross-service traffic, allows permitted

**nftables elimination gate:**
- [ ] M151 complete and all items above done
- [ ] Remove nftables VIP chains when `--dataplane ebpf` (currently kept as fallback)
- [ ] Remove nftables POSTROUTING masquerade per-container rules when eBPF SNAT active
- [ ] `--dataplane ebpf` tested on all three targets: bare-metal, Vagrant, Lima
- [ ] Document kernel version requirements (6.6+ for TCX, 5.18+ for conntrack kfuncs)

## Milestones

Ordered by priority (reviewed 2026-11-02). Numbers are identifiers, not order.

| Milestone | Phases | Demo |
|-----------|--------|------|
| **Tier 0 — Finish & verify what shipped** | | |
| M188 — Gate TLS End-to-End Validation | 188 | Deploy `tls auto` and `tls { cert key }` gates on the deploy cluster (192.168.124.x). Verify HTTPS with cluster-CA trust, cert renewal (short TTL override in test), manual cert rotation via re-apply, sticky + TLS together, `cca get gates` / `cca describe gate` output. Depends: M169. |
| M151 — eBPF Data Plane Correctness | 151 | Correctness half of the original M151 (see checklist): tc SNAT conntrack fix validated over 10+ CI runs, egress SNAT verified, agent-restart BPF pin race fixed, TCP e2e tests for every packet-flow-matrix row. eBPF policy and nftables removal moved to M190. Depends: M128–M134. |
| M136 — HA Control Plane Validation | 136 | First end-to-end HA validation on vmherd (see checklist): 3-node etcd quorum, leader-elected cca-server (`controllers/leader.go`, `ha_runner.go` already exist), control plane failover <30s, etcd member failure, recovery. Absorbs M185: also adds `cca server --join <existing>` if validation shows it is needed. |
| M184 — Etcd Backup & Restore | 184 | `cca backup create/restore`. Scheduled automatic backups. Retention policy. Point-in-time restore. |
| **Tier 1 — High value for effort** | | |
| M178 — Debug & Explain CLI | 178 | Existing: `cca describe` (service/node/instance/gate), `cca diff`, `cca events`. Remaining: `cca debug <service>` (one-shot health report: instances, probes, recent events, placement failures) and `cca explain <service>` (scheduler placement reasoning per node). |
| M171 — Network Policy Egress Rules | 171 | `network_policy "name" { egress { to_cidr "10.0.0.0/8" port 443 } }`. nftables output chain. Default-deny egress when policy applies. DNS-based egress (`to_fqdn`). Depends: M168. |
| M176 — Prometheus Metrics Gaps | 176 | Existing: `/metrics` in Prometheus format (`metrics/metrics.go`) with API, reconciliation (duration, changes, conflicts), activation, and node/service/instance gauges. Remaining: per-controller labels on reconciliation metrics, autoscaler decision metrics, instance counts by state, scrape config docs. |
| M177 — Quota CLI | 177 | Existing: tenant `quota {}` DSL and `QuotaAdmission` (`tenant/quota.go`) blocking instance/volume admission, usage facts. Remaining: `cca quota list` / `cca quota describe <tenant>` showing limits vs usage. |
| M173 — Persistent Volume Gaps | 173 | Existing: `volume` DSL (size, persistent, type, replicas), service volume mounts, per-ordinal stateful volumes (`controllers/stateful_volume.go`), LocalPath/NFS providers (M164). Remaining: scheduler volume locality (place instances on nodes holding their local volume), size enforcement/quota on local provisioner. |
| **Tier 2 — Substantial features** | | |
| M170 — Service Mesh via Groups | 170 | `mesh { mtls "strict" }` — compiler auto-adds proxy to service group. Proxy uses existing SPIFFE identity. Traffic metrics as derived facts. Uses group network sharing, not sidecar injection. Depends: M169. |
| M189 — DSL Admission Policy Rules | 189 | `policy "no-latest" { on apply match { service where image =~ ":latest$" } deny "not allowed" }`. Mutating policies. Compiled into PolicyGate — no webhooks. Escape hatch: `validate_url` for external validation. |
| M174 — Volume Snapshots | 174 | `cca volume snapshot create <service>`. Filesystem copy for local provisioner. Restore to new volume. Retention policy (count/age). Depends: M173. |
| M179 — Distributed Tracing | 179 | OpenTelemetry trace propagation through controllers. OTLP export. Trace correlation: API request → controller → agent. `cca trace <request-id>`. Depends: M176. |
| M182 — DSL Templates & Imports | 182 | `template "web-app" (image, port) { ... }`. `import "lib/common.cca"`. Pure substitution. Standard library at `stdlib/`. Reconsider first: Go templates + `.ccv` values already cover parameterization. |
| M190 — eBPF Policy & nftables Elimination | 190 | Second half of the original M151 checklist: tc ingress policy program on cca0 from `derived/network/rule/` facts, default-deny, then remove nftables VIP/masquerade chains under `--dataplane ebpf`; test on bare-metal, Vagrant, Lima; document kernel requirements. Depends: M151. |
| **Tier 3 — Later / optional** | | |
| M172 — Multi-Cluster Service Discovery | 172 | `cca cluster register <remote>` links clusters. Services visible as `<svc>.<cluster>.svc.ccattler.global`. Cross-cluster health checking. Depends: M168, M154. |
| M175 — Storage Plugin Interface | 175 | CSI-compatible plugin model. Driver registration and capability discovery. External provisioners (NFS, cloud block). Attach/detach lifecycle. Depends: M173. |
| M180 — Custom Fact Types with Schema Validation | 180 | Schema at `meta/fact-type/{name}`. PolicyGate validates writes to `custom/{type}/`. `cca get <type>` auto-discovers. `cca describe fact-type`. No API group versioning — facts with optional schema. |
| M181 — Controller SDK gRPC Transport | 181 | gRPC streaming for out-of-process controllers. `cca controller register --name X --address host:port`. Health check + auto-deregister. Builds on existing CustomController interface (M81). |
| M183 — Controller Scaffolding CLI | 183 | `cca controller init <name>` scaffolds Go/Python project with gRPC client, reconciler stub, test harness. Working example included. Not an operator lifecycle manager. Depends: M181. |
| M186 — Cluster Upgrade Controller | 186 | `cca upgrade plan/apply`. Rolling control-plane + agent upgrade. Drain → upgrade → re-enable node. Rollback on failure. Depends: M136, M184. |
| M187 — Conformance Test Suite | 187 | 100+ e2e test cases across all phases. `cca conformance run`. Test matrix: single/multi-node, HA, multi-tenant. Depends: all previous. Prefer growing it with each milestone over one big phase at the end. |
| M141 — Airflow Triggerer & Observability | 141 | Placeholder: triggerer service, StatsD exporter, log groomer |
| **Future (unscheduled)** | | |
| *(future)* API TLS Rate Limiting | — | Rate-limit failed TLS handshakes per source IP on the `cca server` API listener. After N failed handshakes from the same IP within a sliding window (e.g. 10 failures in 60s), temporarily block that IP for a cooldown period. Prevents a misconfigured client (wrong certs, broken TLS) from flooding the server with handshake errors, which starves goroutines and causes etcd operation timeouts — leading to leadership loss and controller stall. Implementation: wrap `tls.Listener` with a rate-limiting accept loop that tracks per-IP failure counts. Log blocked IPs at WARN level. No configuration needed beyond sensible defaults. Discovered during M130 validation when a caddy-sync daemon with wrong certificates caused leadership loss on the deploy cluster. |
| *(future)* Gate Traffic Splitting | — | Weighted backend routing for `gate` blocks. DSL: `gate canary { host app.example.com; backend api-v1 weight 90; backend api-v2 weight 10 }`. Compiler emits `desired/route/{name}/backend/{service}/weight` facts. Route controller distributes backends proportionally in `derived/route/{name}/backends` (e.g. 9 v1 entries + 1 v2 entry for round-robin). Data plane round-robin naturally approximates weights. For exact splitting, BPF `numgen` mod N with weighted slot assignment. Inspired by Kubernetes Gateway API HTTPRoute `backendRefs[].weight`. Enables canary deployments and A/B testing without a separate proxy. |
| *(future)* WireGuard Node Encryption | — | Transparent encryption of all inter-node traffic using WireGuard tunnels. Agent creates `cca-wg0` WireGuard interface on startup, generates key pair, publishes public key as `observed/node/{id}/wireguard/pubkey` fact. WireGuardReconciler watches node facts, programs peer entries for every other node (`wg set cca-wg0 peer <pubkey> endpoint <ip>:51820 allowed-ips <subnet>`). VXLAN traffic routed over WireGuard by adjusting FDB entries to use `cca-wg0` as the outgoing device. DSL opt-in: `cluster { encryption wireguard }` in the cluster config. No third-party dependencies — uses standard Linux WireGuard kernel module (`ip link add cca-wg0 type wireguard`). Key rotation via periodic regeneration and fact update — peers converge within one reconciliation cycle. |
| *(future)* Gate L7 Metrics | — | Request-level observability for `gate` blocks. The agent HTTP proxy (`network/userspace_proxy.go`) already sees every L7 request via Host header routing. Add per-gate metrics: request count, latency histogram (p50/p95/p99), status code distribution, error rate. Emit as `derived/metric/gate/{name}/{metric}` facts on each proxy reconciliation cycle. `cca top gates` CLI command reads and displays metrics in real-time. `cca metric export` outputs Prometheus exposition format for external scraping. No kernel instrumentation needed — measured at the userspace proxy layer. |
| *(future)* BPF L7 Policy | — | eBPF-based L7 request filtering for HTTP traffic. tc BPF program inspects HTTP request headers (method, path) on `cca-net` bridge and enforces allow/deny rules compiled from DSL `firewall` blocks with L7 matchers: `firewall web-api { allow from backend method GET path "/api/*" }`. Compiler emits `derived/network/rule/{name}/l7/{index}` facts with method/path patterns. BPF program parses TCP payload for HTTP/1.1 request line, matches against BPF hash map of compiled rules, drops non-matching packets. Kernel 6.1+ (BPF packet parsing helpers). Complements existing L3/L4 identity-based policy with application-layer granularity. |
