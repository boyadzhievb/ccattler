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

### Phase 151 — eBPF Data Plane Stabilization (M151)

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

**eBPF network policy (basic):**
- [ ] tc ingress program on cca0 for ingress allow/deny by source CIDR (replaces nftables filter FORWARD)
- [ ] BPF map populated from `derived/network/rule/` facts by agent reconciler
- [ ] Default-deny when at least one policy applies to the service
- [ ] Integration test: policy blocks cross-service traffic, allows permitted

**nftables elimination gate:**
- [ ] All above items complete
- [ ] Remove nftables VIP chains when `--dataplane ebpf` (currently kept as fallback)
- [ ] Remove nftables POSTROUTING masquerade per-container rules when eBPF SNAT active
- [ ] `--dataplane ebpf` tested on all three targets: bare-metal, Vagrant, Lima
- [ ] Document kernel version requirements (6.6+ for TCX, 5.18+ for conntrack kfuncs)

## Milestones

| Milestone | Phases | Demo |
|-----------|--------|------|
| **Phase C — Networking & Service Discovery** | | |
| M168 — Cluster DNS | 168 | CoreDNS as CCattler service with `placement { mode per-node }`. Auto-generate `<service>.svc.ccattler.local` records from service/endpoint facts. Headless services: A records per instance. Depends: M160. |
| M169 — Gate TLS Termination | 169 | `tls { mode auto }` or `tls { cert "…" key "…" }` inside `gate` block. Cluster CA issues certs for `mode auto`, auto-renews. Agent embedded proxy serves HTTPS when TLS facts present. `cca get gates` shows TLS status. Depends: M168. |
| M170 — Service Mesh via Groups | 170 | `mesh { mtls "strict" }` — compiler auto-adds proxy to service group. Proxy uses existing SPIFFE identity. Traffic metrics as derived facts. Uses group network sharing, not sidecar injection. Depends: M169. |
| M171 — Network Policy Egress Rules | 171 | `network_policy "name" { egress { to_cidr "10.0.0.0/8" port 443 } }`. nftables output chain. Default-deny egress when policy applies. DNS-based egress (`to_fqdn`). Depends: M168. |
| M172 — Multi-Cluster Service Discovery | 172 | `cca cluster register <remote>` links clusters. Services visible as `<svc>.<cluster>.svc.ccattler.global`. Cross-cluster health checking. Depends: M168, M154. |
| **Phase D — Storage & State** | | |
| M173 — Persistent Volumes | 173 | `volume "data" { size "10Gi" mount "/data" }` as service block. Volume controller manages lifecycle. Local provisioner with quota. Scheduler considers volume locality. Per-instance volumes for stateful services. |
| M174 — Volume Snapshots | 174 | `cca volume snapshot create <service>`. Filesystem copy for local provisioner. Restore to new volume. Retention policy (count/age). Depends: M173. |
| M175 — Storage Plugin Interface | 175 | CSI-compatible plugin model. Driver registration and capability discovery. External provisioners (NFS, cloud block). Attach/detach lifecycle. Depends: M173. |
| **Phase E — Observability & Operations** | | |
| M176 — Prometheus Metrics Endpoint | 176 | `/metrics` in Prometheus exposition format. Controller metrics (reconcile duration, queue depth, error rate). Cluster metrics (node/instance/service counts by state). Autoscaler metrics. |
| M177 — Resource Quota Controller | 177 | `quota "team-a" { cpu "8" memory "16Gi" services 50 }`. QuotaController blocks apply when exceeded. `cca quota list/describe`. Scoped to teams. |
| M178 — Debug & Troubleshooting CLI | 178 | `cca debug <service>` (health report), `cca describe <resource>` (detailed view + events), `cca diff <file>` (dry-run), `cca explain <service>` (placement reasoning). |
| M179 — Distributed Tracing | 179 | OpenTelemetry trace propagation through controllers. OTLP export. Trace correlation: API request → controller → agent. `cca trace <request-id>`. Depends: M176. |
| **Phase F — Extensibility** | | |
| M180 — Custom Fact Types with Schema Validation | 180 | Schema at `meta/fact-type/{name}`. PolicyGate validates writes to `custom/{type}/`. `cca get <type>` auto-discovers. `cca describe fact-type`. No API group versioning — facts with optional schema. |
| M180 — DSL Admission Policy Rules | 180 | `policy "no-latest" { on apply match { service where image =~ ":latest$" } deny "not allowed" }`. Mutating policies. Compiled into PolicyGate — no webhooks. Escape hatch: `validate_url` for external validation. |
| M181 — Controller SDK gRPC Transport | 181 | gRPC streaming for out-of-process controllers. `cca controller register --name X --address host:port`. Health check + auto-deregister. Builds on existing CustomController interface (M81). |
| M182 — DSL Templates & Imports | 182 | `template "web-app" (image, port) { ... }`. `import "lib/common.cca"`. Pure substitution. Standard library at `stdlib/`. |
| M183 — Controller Scaffolding CLI | 183 | `cca controller init <name>` scaffolds Go/Python project with gRPC client, reconciler stub, test harness. Working example included. Not an operator lifecycle manager. Depends: M181. |
| **Phase G — Production Hardening** | | |
| M184 — Etcd Backup & Restore | 184 | `cca backup create/restore`. Scheduled automatic backups. Retention policy. Point-in-time restore. |
| M185 — Control Plane HA | 185 | Multi-server with leader election. `cca server --join <existing>`. Auto-failover. Split-brain via etcd quorum. Depends: M184. |
| M186 — Cluster Upgrade Controller | 186 | `cca upgrade plan/apply`. Rolling control-plane + agent upgrade. Drain → upgrade → uncordon. Rollback on failure. Depends: M185. |
| M187 — Conformance Test Suite | 187 | 100+ e2e test cases across all phases. `cca conformance run`. Test matrix: single/multi-node, HA, multi-tenant. Depends: all previous. |
| M151 — eBPF Data Plane Stabilization | 151 | Harden eBPF data plane to fully replace nftables. Fix tc SNAT (conntrack-aware reply skip), agent restart BPF pin race, add TCP integration tests for every packet flow matrix row, basic eBPF network policy, then remove nftables fallback under `--dataplane ebpf`. Depends: M128–M134. |
| *(future)* API TLS Rate Limiting | — | Rate-limit failed TLS handshakes per source IP on the `cca server` API listener. After N failed handshakes from the same IP within a sliding window (e.g. 10 failures in 60s), temporarily block that IP for a cooldown period. Prevents a misconfigured client (wrong certs, broken TLS) from flooding the server with handshake errors, which starves goroutines and causes etcd operation timeouts — leading to leadership loss and controller stall. Implementation: wrap `tls.Listener` with a rate-limiting accept loop that tracks per-IP failure counts. Log blocked IPs at WARN level. No configuration needed beyond sensible defaults. Discovered during M130 validation when a caddy-sync daemon with wrong certificates caused leadership loss on the deploy cluster. |
| *(future)* Gate Traffic Splitting | — | Weighted backend routing for `gate` blocks. DSL: `gate canary { host app.example.com; backend api-v1 weight 90; backend api-v2 weight 10 }`. Compiler emits `desired/route/{name}/backend/{service}/weight` facts. Route controller distributes backends proportionally in `derived/route/{name}/backends` (e.g. 9 v1 entries + 1 v2 entry for round-robin). Data plane round-robin naturally approximates weights. For exact splitting, BPF `numgen` mod N with weighted slot assignment. Inspired by Kubernetes Gateway API HTTPRoute `backendRefs[].weight`. Enables canary deployments and A/B testing without a separate proxy. |
| *(future)* WireGuard Node Encryption | — | Transparent encryption of all inter-node traffic using WireGuard tunnels. Agent creates `cca-wg0` WireGuard interface on startup, generates key pair, publishes public key as `observed/node/{id}/wireguard/pubkey` fact. WireGuardReconciler watches node facts, programs peer entries for every other node (`wg set cca-wg0 peer <pubkey> endpoint <ip>:51820 allowed-ips <subnet>`). VXLAN traffic routed over WireGuard by adjusting FDB entries to use `cca-wg0` as the outgoing device. DSL opt-in: `cluster { encryption wireguard }` in the cluster config. No third-party dependencies — uses standard Linux WireGuard kernel module (`ip link add cca-wg0 type wireguard`). Key rotation via periodic regeneration and fact update — peers converge within one reconciliation cycle. |
| *(future)* Gate L7 Metrics | — | Request-level observability for `gate` blocks. The agent HTTP proxy (`network/userspace_proxy.go`) already sees every L7 request via Host header routing. Add per-gate metrics: request count, latency histogram (p50/p95/p99), status code distribution, error rate. Emit as `derived/metric/gate/{name}/{metric}` facts on each proxy reconciliation cycle. `cca top gates` CLI command reads and displays metrics in real-time. `cca metric export` outputs Prometheus exposition format for external scraping. No kernel instrumentation needed — measured at the userspace proxy layer. |
| *(future)* BPF L7 Policy | — | eBPF-based L7 request filtering for HTTP traffic. tc BPF program inspects HTTP request headers (method, path) on `cca-net` bridge and enforces allow/deny rules compiled from DSL `firewall` blocks with L7 matchers: `firewall web-api { allow from backend method GET path "/api/*" }`. Compiler emits `derived/network/rule/{name}/l7/{index}` facts with method/path patterns. BPF program parses TCP payload for HTTP/1.1 request line, matches against BPF hash map of compiled rules, drops non-matching packets. Kernel 6.1+ (BPF packet parsing helpers). Complements existing L3/L4 identity-based policy with application-layer granularity. |
