# Changelog

All notable releases of CCattler are documented here.

## v1.0.0-beta <Badge type="tip" text="latest" /> {#v1-0-0-beta}

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
