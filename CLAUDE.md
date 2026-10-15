# CCattler — Fact-Based Container Orchestrator

## Current Status

**Completed through:** M110 — Phase 110 Security Hardening (All Open Threats). M1–M110 complete. Gates A–I resolved.

### Architecture Debt (from external reviews, Sep 19 2026)

Items addressed by Phase 26 (P0 Correctness), Phase 35a (Correctness II), Phase 36 (Correctness III), and Phase 37 (Runtime Stats): runtime observation authoritative, 127.0.0.1 fallback removed, probe scheduling decoupled, init restart-safe, watch overflow resync, transaction snapshot CAS, derived/ prefix, etcd Put simplified, VIP allocation race fixed, init dual authority resolved, etcd WithPrevKV, EventProjector from committed state, init runtime isolation via ExecInit, multi-port endpoint model, agent sub-reconciler extraction, Runtime Stats() API.

**All architecture debt from external reviews resolved.**

### Performance Optimization Principle

Optimize the architecture first, the algorithms second, Go code third, and assembly only for demonstrated hot paths. Go is the right language for the control plane. Networking data plane is the one area where eBPF/XDP could eventually matter. Establish benchmarks (`cca benchmark`) before optimizing: reconciliation/sec, scheduling/sec, state transactions/sec, controller latency, agent reconciliation latency, startup-to-ready latency.

---

## Code Style Rules

- **Comment every function.** Every exported and unexported function must have a doc comment explaining what it does. Comments must be at most 4 lines — keep them concise. If more context is needed, add it to a design doc and reference it.
- **Use long descriptive variable names.** No single-letter or cryptic abbreviations. Examples: `factStore` not `s`, `instanceController` not `ic`, `nodeAgent` not `ag`, `simulatorRuntime` not `rt`, `serviceName` not `svc`, `factEntry` not `f`.
- **Document variables.** Struct fields must have inline comments explaining their purpose. Named constants and map variables should have comments when their role isn't obvious from the name alone.
- **Descriptive function names.** Prefer `executeReconciliationCycle` over `reconcileOnce`, `buildClusterStatusJSON` over `buildStatusJSON`, `findInstancesPlacedOnThisNode` over `desiredInstances`.
- **Receiver names match the type.** Use `nodeFailureController` not `ctrl`, `memStore` not `m`, `containerRuntime` not `c`.

### Anti-Pattern Rules (enforced from Phase 50)

- **No dead code.** Every exported type, function, and constant must have at least one production caller (non-test). Code that only exists in tests is a test helper, not library code — move it to `_test.go` or remove it. Do not add code "for later" without wiring it into a real path.
- **No magic numbers.** Timeouts, capacities, thresholds, retry counts, and interval durations must be package-level named constants (e.g., `defaultReconcileInterval`, `maxConcurrentReconciles`, `defaultLeaseTimeout`). Tests should reference the named constants, not duplicate the raw values.
- **Function length limit: 80 lines.** No function (excluding test functions) may exceed 80 lines. Break long functions into sub-functions with clear responsibilities. Compiler/builder functions should delegate per-block or per-section.
- **No copy-paste duplication.** If the same pattern appears in 3+ places, extract it into a shared helper. Near-duplicate blocks over 10 lines must be refactored.
- **Typed enums over raw strings.** Instance states, node states, probe types, and similar finite sets must use typed string constants (e.g., `type InstanceState string`). Use these typed constants in comparisons, not raw string literals. This catches typos at compile time.
- **Every exported type must be wired.** If you add a new exported type or interface, show where it gets instantiated in production (in `cmd/`, `api/`, or a controller). Interfaces need at least one production call site, not just test implementations.
- **Integration test assertions must be inside `waitFor`.** Never assert a fact written by a different goroutine immediately after a `waitFor` that checked a different fact. Multiple async writers (controllers, agents) commit facts at different times — a running instance does not guarantee its network allocation exists yet. Put every condition the test depends on inside the same `waitFor` predicate, or use a second `waitFor` before the assertion. A bare assertion after `waitFor` is only safe for facts produced in the same transaction as the waited-on fact.

### Data Structures & Algorithms

- **Maps for O(1) lookups.** Use `map[string]T` for key-based lookups (instances by ID, nodes by ID, services by name). Never do linear scans on slices when the lookup key is known.
- **Slices for ordered iteration.** Use slices when order matters (reconciliation plans, init steps, admission pipeline stages). Sort slices deterministically by key before iterating when the source is a map.
- **Deterministic iteration.** Controllers must produce the same output given the same input. When iterating maps, collect keys into a slice and sort before processing. This is already enforced (Phase 43) — never regress.
- **Prefix trees / sorted scan for fact store queries.** The fact store uses prefix-based key layout (`desired/service/`, `observed/node/`). Design keys so that related facts share a prefix, enabling efficient `Scan(prefix)` instead of full-store iteration + filter.
- **Optimistic concurrency for transactions.** Compare-and-swap via store transactions, never lock-and-hold. Read the current revision, compute changes, attempt transaction with revision guard. Retry on conflict with backoff.
- **Time-windowed averaging for autoscaling.** Never scale from instantaneous metrics. Use sliding window averages with asymmetric stabilization (fast scale-up, slow scale-down).
- **Topological sort for dependency ordering.** When execution order depends on dependencies (init steps, controller startup), use topological sort. Detect cycles and fail explicitly rather than deadlocking.
- **Consistent hashing / spread algorithms for placement.** Scheduler distributes instances across nodes/zones using spread-aware bin packing. Zone spread is a constraint, not a random assignment.

### Authorization Architecture (from design review, Sep 29 2026)

- **Principal model**: `User`, `ServiceIdentity` (includes `NodeIdentity`). Groups are authentication-provided attributes, not authorization objects.
- **Permission vocabulary**: Capability-based (`workload.read`, `secret.use`, `placement.write`), not Kubernetes API verbs (`get`, `list`, `watch`).
- **Scopes**: Hierarchical paths (`cluster`, `team/payments`, `node/node01`). Parent scope includes children.
- **Two-layer authorization**: Store-layer prefix RBAC for internal components, API-layer capability-based auth for users.
- **Deny-by-default, fail-closed**: Authentication failure, authorization failure, policy engine unavailable, incomplete identity → all DENY.
- **Authorization always enforced**: Even without `--tls`, a local-user authenticator derives principal from OS user. No bypass path.
- **Do NOT implement**: Kubernetes Role/ClusterRole/RoleBinding/ClusterRoleBinding pattern. Use `group + permission + scope + condition` instead.

---

## Overview

**CCattler** (Container Cattler) — a Kubernetes-alternative container orchestrator built around **facts, rules, and reconciliation** instead of an object hierarchy.

**Core premise**: given a description of desired behavior, continuously make a distributed machine satisfy that description.

**Language**: Go. **Store**: etcd. **Runtime**: pluggable (Simulator → Process → Container).

## Design Philosophy

- The cluster contains **facts**, not objects
- Users write **domain intent**, not serialized implementation objects
- No Pods, ReplicaSets, Deployments, or `apiVersion/kind`
- Controllers are **rule engines** operating on facts
- The scheduler is a **pure function**: `schedule(requirements, nodes) → placement`

### Architectural Rules

1. **No component may assume another component performed an action.** Components only react to state.
2. **Desired state and observed state are always separate.** The difference between them is the work.
3. **Idempotency is the fundamental primitive.** Every operation is `ensure_X()`, not `do_X()`.
4. **Store facts and desired results, not commands.** `desired(instance, running)` not `start(instance)`.
5. **Controllers return proposed changes, not arbitrary mutations.** `reconcile(facts) → []change`.
6. **Init containers must match main containers.** Same network, DNS, env vars. See [design/runtime-invariant-flows.md](design/runtime-invariant-flows.md).

## Architecture

```
USER → DSL (.cca files) → FACT STORE (etcd) → Controllers → NODE AGENTS → MACHINES
```

Computational model: `facts → rules/queries → new facts → actions → real world → observed facts → repeat`

## Core Fact Types

```
service, instance, node, endpoint, volume, placement, config, secret, secret_grant,
init_step, init_phase, utilization, probe, probe_state, node_label, node_restrict
```

See [design/etcd-schema.md](design/etcd-schema.md) for the full key layout.

## Design Documents

Detailed design for each subsystem lives in `design/`:

- **[security-model.md](design/security-model.md)** — mTLS, CA hierarchy, RBAC+ABAC, per-controller least privilege, audit
- **[multi-tenancy.md](design/multi-tenancy.md)** — Ownership, hierarchical naming, quotas, fair scheduling, network isolation
- **[controllers.md](design/controllers.md)** — Instance/endpoint/failure/init controllers, autoscaling engine
- **[config-secrets.md](design/config-secrets.md)** — Config delivery, secret grants, materialization boundary, secret lifecycle
- **[node-agent.md](design/node-agent.md)** — Observer/reconciler/reporter, runtime adapters, lease management
- **[cloud-integration.md](design/cloud-integration.md)** — Cloud provider sub-controllers, external load balancers
- **[dsl-reference.md](design/dsl-reference.md)** — Full DSL syntax with examples
- **[runtime-invariant-flows.md](design/runtime-invariant-flows.md)** — Cross-component invariant chains (init, DNS, secrets, VIP, connections)
- **[threat-model.md](design/threat-model.md)** — Security threat model and mitigations
- **[etcd-schema.md](design/etcd-schema.md)** — Fact store key layout and prefix conventions

## CLI

```
cca apply <file>              # deploy config (simulated, prints status and exits)
cca run [--watch] <file>      # start real OS processes (--watch for live status)
cca run-container [--watch] <file>  # start real Docker containers (--watch for live status)
cca server [--listen h:p] [--tls] [--cert/--key/--ca] [--api-only] [--controllers-only] [--node-id <id>] [--cloud-provider <name>] [--cloud-region <region>]
cca agent --node-id <id> [--cert/--key/--ca] [--advertise-address <ip>] [--debug-listen <addr>]
cca token create [--node-id <id>] [--ttl 15m]
cca token list / cca token revoke <token>
cca join <server> <token> --node-id <id> [--ca-cert <path>]
cca get services / instances / nodes / secrets / config
cca scale <service> <count>
cca logs <service> [--follow] [--instance <id>]
cca status / cca watch [prefix]
cca node-inspect <node-id>
cca agent debug
cca exec <id> -- <command>
cca images list [--node <id>] / cca images pull <img> [--node]
cca top [nodes|workloads]
cca metric set <svc> <m> <v>
```

---

## Release Process

Use `scripts/release.sh` for all releases. Never update changelog, CLAUDE.md status, or tags manually.

```bash
scripts/release.sh M89 v1.17.0-beta
git push && git push origin v1.17.0-beta
```

The script validates milestone, updates CLAUDE.md status, generates changelog, moves badge, commits bookkeeping, creates annotated tag.

**Prerequisites:** clean working tree, milestone in `milestones.md`, tests passing.

**CI handles:** `release.yml` (binaries), `pages.yml` (website), `test.yml`, `codeql.yml`, `trivy.yml`.

---

## Integration Testing Infrastructure

E2E tests run on `testbed-100.43` using libvirt VMs. See [design/TESTING.md](design/TESTING.md) for server requirements, test flow, workloads, and tooling.

---

## Implementation Phases & Milestones
yes
See [milestones.md](milestones.md) for the full phase checklist and milestone table.
