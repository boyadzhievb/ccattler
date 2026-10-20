# CCattler Project Status

## Current Milestone

M121 — nftables Data Plane (Phase 121)

- Status: complete
- Release: v1.48.0-beta
- Milestones complete: M1–M121

## Capabilities by Area

### Core Control Plane (M1–M10, M35–M37, M43, M73, M107)
Fact store (etcd-backed + in-memory), instance/endpoint/failure controllers, scheduler with zone-spread bin packing, batch placement, NodeCapacityCache, deterministic iteration, optimistic concurrency transactions, runtime stats API.

### Node Agent (M11–M15, M96–M97, M102, M111)
Observer/reconciler/reporter, container runtime adapter (nerdctl/containerd), lease management, DNS integration (dnsmasq domain-scoped forwarding), cross-node VIP routing, hairpin NAT fix, host port re-publication on steady-state cycles.

### Networking & Data Plane (M102, M111, M118, M121)
nftables data plane with O(1) verdict map dispatch (preferred), iptables fallback. VIP allocation, cross-node DNAT, hairpin NAT, gate blocks with sticky sessions (cookie + source-IP), shadow mode migration, `cca benchmark dataplane`. Protocol: `--dataplane auto|nftables|iptables`.

### DSL & Developer Experience (M1–M5, M98, M104, M119–M120)
Human-oriented declarative language (no YAML), parser/compiler/emitter, service/volume/group/role/grant/policy/config/secret/tenant/network/gate/connection blocks, `cca fmt` + `cca lint`, service tags with cross-service filtering.

### Security & Auth (M55–M63, M80, M100–M101, M110)
mTLS everywhere, CA hierarchy, RBAC+ABAC authorization, per-controller least-privilege, user authentication (local token + OIDC), `cca login`, bearer auth chain, etcd mTLS deployment, CSR-based enrollment, store token hashing, cert permission hardening, audit logging.

### Multi-Tenancy (M46–M50, M109)
Hierarchical naming, ownership model, quotas, fair scheduling, scoped store access, node principal tenant visibility.

### Cloud Integration (M27–M33, M77–M78, M113)
Cloud provider adapters (AWS, GCP, Azure stub) for LB/routes/lifecycle, OIDC workload identity, credential broker, cloud identity DSL, cluster autoscale controller (SimulatorInfraProvider). AWS/GCP SDK gated behind build tag (slim binary ~25MB vs full ~62MB).

### Observability & Operations (M64–M69, M99, M106, M112, M114–M117)
`cca top`, `cca node-inspect`, `cca exec`, `cca images`, JSON API errors, `cca init` bootstrap, join token TLS chain, health check scripts, deploy script audit.

## Performance Benchmarks

### Load Test Results (current, from TestSyntheticCluster tests)

| Test | Nodes | Instances | Convergence | Recovery | Scale-up | Status |
|------|-------|-----------|-------------|----------|----------|--------|
| 50n/1000i | 50 | 1,000 | 8.9s | 4m0s (5 nodes killed) | 12.6s (→1,500) | **PASS** |
| 100n/2000i | 100 | 2,000 | 35s | 5m0s (10 nodes killed) | 1m34s (→3,000) | **PASS** |
| 200n/5000i | 200 | 5,000 | 5m41s | 10m7s (10 nodes killed) | 15m3s (→6,000) | **PASS** |

### Data Plane Benchmark (`cca benchmark dataplane`)

nftables generates a single atomic ruleset (O(1) lookup per connection) vs iptables sequential `iptables -A` commands (O(n) per service).

## Metrics

- Source files: 180
- Test files: 150
- Total Go lines: ~108K
- Test count: 1,803 across 20 packages
- Latest release: v1.48.0-beta
- Milestones complete: M1–M121

## Known Issues

- Azure cloud provider: stub implementation only (all methods return "not implemented").
- Cloud autoscaling: cluster autoscale controller works with SimulatorInfraProvider. Real cloud InfrastructureProvider implementations (AWS EC2, GCP Compute) are a future milestone.
- Load test: 200-node/5000-workload recovery takes 10m (vs 4m at 50-node scale). Scale-up to 6000 instances converges at 96.8% within 15m under single-process MemoryStore contention.

## CI & Testing

- **Required PR checks (10):** unit tests, integration tests, golangci-lint, CodeQL, Trivy, E2E (libvirt VMs on testbed), deploy testbed, pages, release build
- **E2E tests:** multi-node libvirt cluster (ctrl + 2 workers), deploys real services (whoami, airflow), validates VIP data plane (nftables/iptables), cross-node connectivity, DNS resolution, HTTP traffic through VIPs
- **Load tests:** synthetic in-memory clusters at 50/100/200 node scale (`//go:build loadtest`)
- **Branch protection:** required reviews, status checks, no force push to master
