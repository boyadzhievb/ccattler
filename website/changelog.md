# Changelog

All notable releases of CCattler are documented here.

## v1.14.1-beta <Badge type="tip" text="latest" /> {#v1-14-1-beta}

**2026-10-09** — Phases 72–78 complete. Five correctness review passes, durable capacity state machine.

- **Durable capacity request state machine** (M78) — autoscaler uses post-commit pattern with explicit `pending → launching → ready | failed` lifecycle; scheduler emits structured unplaced-demand facts distinguishing `insufficient_capacity` from `unsatisfiable_constraint`
- **Correctness V** (M77) — storage operation retry after provider failure, duplicate pending-operation key collision fix, event projector watch gap on reconnect, cloud controllers converted to post-commit
- **Correctness IV** (M76) — CAS liveness race fix, storage pre-commit side effects eliminated, heartbeat guard, projector reconnect resilience
- **Correctness III** (M75) — VIP deletion atomicity, agent node resurrection race, oversized group hard error, independent heartbeat goroutine
- **Atomic change groups** (M74) — budget truncation respects multi-key atomic operations, stale derived marker poisoning fixed for stateful ordinals
- **Derived failure markers** (M73) — failure-path writes moved to `derived/instance/*`, CAS decoupling eliminates controller/agent shared-key contention (root cause of 50+ node livelock)
- **Correctness II** (M72) — failure controller recovery cap, node failure controller single budget, HA fencing, agent resilience
- **Race condition fix** — `SimulatorRuntime.InjectStartFailure` for concurrent-safe test failure injection

### What's new since v1.8.3

| Version | Phase | Summary |
|---|---|---|
| v1.14.0 | 78 | Durable capacity request state machine, scheduler unplaced-demand signaling |
| v1.13.0 | 77 | Correctness V: storage retry, operation isolation, watch continuity |
| v1.12.0 | 76 | Correctness IV: atomic liveness, storage idempotency, projector resilience |
| v1.11.0 | 75 | Correctness III: VIP atomicity, agent resurrection, heartbeat decoupling |
| v1.10.1 | 74 | Atomic change groups, stateful marker lifecycle, API consistency |
| v1.10.0 | 73 | Derived failure markers, CAS decoupling, node recovery |
| v1.9.9 | 72 | Budget alignment, HA fencing, agent resilience |
| v1.9.7 | 71 | Post-review correctness fixes |
| v1.9.1 | 69 | Node runtime inspection: agent debug API, `cca node-inspect`, `cca exec` |
| v1.9.0 | 68–69 | Scheduler scale (NodeCapacityCache min-heap), agent debug API |

---

## v1.8.3-beta {#v1-8-3-beta}

**2026-10-04** — Phase 67 complete plus CI hardening.

- **Production readiness validation** (M67) — controller failure isolation, HA resilience, chaos recovery metrics, runtime evidence, observability audit trail
- **`cca readiness` CLI** — pre-flight cluster health check
- **DSL enhancements** — triple-quote strings, `from` file inclusion, `equals` syntax
- **CI hardening** — PR security checks, license headers, CodeQL alerts resolved, E2E Vagrant parallel race fix
- **Bugfixes** — container memory double-conversion, lease timeout, agent TLS loading regression

---

## v1.6.0-beta {#v1-6-0-beta}

**2026-10-03** — Phase 67c: chaos benchmark with recovery metrics.

- **Chaos benchmark CLI** — `cca benchmark --json` for structured recovery reporting with P50/P95/P99 convergence times
- **RecoveryReport** — structured metrics (mean/P50/P95/P99/max convergence, recovery success rate) at 100/1K/5K workload scales
- **Node recovery scenario** — new chaos injection type: restart previously killed nodes
- **Autoscaler oscillation test** — validates stabilization windows prevent rapid scale-up/down thrashing

---

## v1.5.0-beta {#v1-5-0-beta}

**2026-10-03** — Phase 67b: HA and control plane resilience tests.

- **9 resilience tests** — etcd unavailable (workloads survive), control plane restart (no data loss), leader election fencing, disaster recovery from snapshot, split-brain healing, endpoint staleness during partition, volume disappearance recovery, slow store latency, rolling control plane upgrade
- **slowStore wrapper** — configurable per-operation latency injection for store operations

---

## v1.4.0-beta {#v1-4-0-beta}

**2026-10-03** — Phase 67a: controller failure isolation tests.

- **5 isolation tests** — scheduler crash, network controller crash, instance controller restart, all controllers restart, concurrent controller recovery
- **Per-controller lifecycle** — independent kill/restart of individual controller groups via separate Runner instances

---

## v1.3.0-beta {#v1-3-0-beta}

**2026-10-03** — Phase 66: stateful workloads.

- **Stateful workloads** — ordinal instance IDs, ordered startup/teardown, per-ordinal persistent volumes, stable DNS names per ordinal
- **Disruption budgets** — `max_unavailable` enforcement during voluntary operations (drain, rolling update)
- **Security audit remediation** — 10 findings from external audit addressed: local token auth, default listen localhost, request body limits, SSE connection caps, secret capability split, cross-tenant tests, GitHub Actions SHA pinning
- **Node drain and disable/enable** — `cca drain` and `cca disable-node`/`enable-node` commands

### What's new since v1.2.0

| Milestone | Phase | Summary |
|---|---|---|
| M64 | 64 | Node drain and disable/enable |
| M65 | 65 | Disruption budgets + security audit remediation |
| M66 | 66 | Stateful workloads: ordinal IDs, ordered startup, per-ordinal volumes |

---

## v1.2.0-beta {#v1-2-0-beta}

**2026-10-02** — Phases 58–63 complete. Full authorization stack, cloud SDK integration, and production hardening.

- **ABAC condition engine** — attribute-based access control with `when` conditions
- **Multi-tenant visibility** — tenant-scoped filtering across all API endpoints
- **Network policy enforcement** — identity-based allow/deny with iptables rule generation
- **Service groups** — co-scheduled process groups sharing network and volumes
- **Vertical autoscaling** — P95 sliding window with asymmetric stabilization
- **Real cloud SDKs** — AWS EC2/ELB/VPC and GCP Compute/LB/Routes integration
- **Secret encryption** — envelope encryption with KMS (AWS KMS, GCP KMS)
- **3-VM E2E tests** — Vagrant/libvirt cluster tests with real container workloads

| Milestone | Phase | Summary |
|---|---|---|
| M56 | 58 | ABAC condition engine |
| M57 | 59 | Multi-tenant visibility filtering |
| M58 | 60 | Network policy enforcement |
| M59 | 61 | Service groups with co-scheduling |
| M60 | 62 | Vertical autoscaling controller |
| M61 | 63 | Cloud SDK wiring (AWS, GCP) + secret encryption |
| M62 | 64 | Production hardening: E2E tests, install scripts |
| M63 | 65 | Placement policy and tenant isolation tests |

---

## v1.1.0-beta {#v1-1-0-beta}

**2026-09-28** — Phases 48–57 complete. Authorization architecture, DSL templating, anti-pattern remediation.

- **Anti-pattern remediation** — dead code removal, magic number extraction, function length enforcement, typed enums
- **Authorization wiring** — capability-based permissions, per-controller least privilege, store prefix RBAC
- **Authentication** — local token auth, mTLS cert auth, OIDC infrastructure
- **DSL templating** — values files, `--set`, `--set-from-env`, `cca render` command
- **CLI acceptance tests** — end-to-end CLI command validation

| Milestone | Phase | Summary |
|---|---|---|
| M48 | 50a | Split god-object main.go into per-command files |
| M49 | 49b | CI fix: skip container tests in short mode |
| M52 | 50b–54 | Anti-pattern remediation, authorization, authentication |
| M55 | 55–57 | Auth DSL, templating engine, CLI acceptance tests |

---

## v1.0.0-beta {#v1-0-0-beta}

**2026-09-25** — Gate F complete. All correctness, security, and performance gates resolved.

### Highlights

- **Synthetic cluster load test** — 5-phase test exercising 50 nodes and 1,000-1,500 workloads: deploy convergence, placement verification, node failure recovery, scale-up, and store verification
- **Controller runner hardening** — exponential backoff with jitter for optimistic concurrency retries, configurable input-key guard limit to reduce transaction conflicts under high contention
- **Performance validated at scale** — 1,000 instances converge in ~24s, node failure recovery in ~90s, scale-up to 1,500 in ~2m40s

### What's new since v0.9.1

| Milestone | Phase | Summary |
|---|---|---|
| M44 | 47 | Synthetic cluster load test (50 nodes, 1K-1.5K workloads) |

---

## v0.9.1

**2026-09-24** — Controller algorithm complexity audit.

- Audited all 17 controllers + runner for O(n^2) or worse patterns
- Removed dead O(n^2) code in InstanceController
- Replaced 11 full fact-store scans with `FactsWithPrefix` binary search across 6 controllers + SDK
- Merged redundant prefix scans in credential broker and cloud controllers
- Runner early-exit on input-key guard loop when etcd 128-op transaction cap reached

---

## v0.9.0

**2026-09-22** — MCP server, container runtime default, auto-logging hook.

- Container runtime as default mode
- MCP server for remote host management with auth, read-only mode, and audit log
- Auto-logging hook for command history

---

## v0.8.0

**2026-09-21** — Store correctness, truth & reconciliation, agent decomposition, security audit.

| Milestone | Phase | Summary |
|---|---|---|
| M39 | 42 | Watch loss-tolerance, compaction resync, 12 conformance tests |
| M40 | 43 | Deterministic plans, write domain enforcement, key ownership matrix |
| M41 | 44 | Agent sub-component extraction, runtime conformance suite, failure matrix tests |
| M42 | 45 | Formal threat model (10 categories), command/secret/etcd audits with fixes |

---

## v0.7.0 — v0.7.1

**2026-09-20** — System design patterns and algorithm improvements.

- Circuit breaker, rate limiter, least-connections LB, bulkhead, tracing, response cache, DLQ, CDC stream
- Min-heap scheduler, binary search extraction, trie prefix scan, topological sort, cycle detection
- CI test workflow, fuzz tests, race detector, benchmarks

---

## v0.6.0 — v0.6.1

**2026-09-19** — Warm-zero scale-to-zero with HTTP activation proxy, project hardening (CI, benchmarks, structured errors).

---

## v0.5.0 — v0.5.5

**2026-09-18** — Correctness passes, cloud controller manager, API horizontal scalability, identity RBAC.

- Transaction snapshot CAS, derived/ prefix, init dual authority
- Cloud provider interface, node lifecycle, load balancers, VPC routes
- Credential broker, agent credential materialization, OIDC infrastructure

---

## v0.4.0

**2026-09-17** — Storage resilience, observability, CLI improvements.

- Storage migration, snapshots, monitoring, resize, replication
- Prometheus /metrics, structured JSON logging, event streaming, Grafana dashboards
- `cca logs`, `cca describe`, `cca diff`, `cca events`, shell completions

---

## v0.3.0

**2026-09-16** — Service networking, placement, multi-host deployment.

- DNS, proxy, VIP data plane with iptables DNAT
- Multi-host demo across physical servers
- Ansible deployment automation with Vagrant test clusters
- Node enrollment (`cca join` with bootstrap tokens)

---

## v0.2.0

**2026-09-15** — Foundation through single-machine runtime.

- Fact store (memory + etcd), domain language parser, reconciliation engine
- Instance, endpoint, failure, scheduler controllers
- Process and container runtime adapters
- mTLS, RBAC + ABAC, per-controller least privilege
- Unified autoscaling engine, multi-tenancy, rolling deployments
