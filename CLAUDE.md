# CCattler — Fact-Based Container Orchestrator

## Current Status

**Completed through:** M157 — Instance Fact Garbage Collection. M1–M157 complete. Gates A–I resolved.

**In progress:** None. See milestones.md for upcoming work.

### Performance Optimization Principle

Optimize the architecture first, the algorithms second, Go code third, and assembly only for demonstrated hot paths. Establish benchmarks (`cca benchmark`) before optimizing.

---

## Code Style Rules

- **Comment every function.** Every exported and unexported function must have a doc comment (max 4 lines).
- **Use long descriptive variable names.** `factStore` not `s`, `instanceController` not `ic`, `serviceName` not `svc`.
- **Document variables.** Struct fields must have inline comments. Named constants/maps should have comments when role isn't obvious.
- **Descriptive function names.** `executeReconciliationCycle` not `reconcileOnce`, `buildClusterStatusJSON` not `buildStatusJSON`.
- **Receiver names match the type.** `nodeFailureController` not `ctrl`, `memStore` not `m`.

### Anti-Pattern Rules (enforced from Phase 50)

- **No dead code.** Every exported type/function/constant must have a production caller.
- **No magic numbers.** Timeouts, thresholds, intervals → package-level named constants.
- **Function length limit: 80 lines.** Break long functions into sub-functions. Excludes test functions.
- **No copy-paste duplication.** Same pattern 3+ places → shared helper. Near-duplicate blocks >10 lines → refactor.
- **Typed enums over raw strings.** Instance/node states, probe types → typed string constants.
- **Every exported type must be wired.** Show where it's instantiated in production.
- **Integration test assertions must be inside `waitFor`.** Never assert async facts after a `waitFor` that checked a different fact.

### Data Structures & Algorithms

- **Maps for O(1) lookups.** `map[string]T` for key-based lookups, never linear scans.
- **Slices for ordered iteration.** Sort deterministically when source is a map.
- **Deterministic iteration.** Controllers must produce same output for same input. Sort map keys before processing.
- **Prefix scan for fact store queries.** Design keys so related facts share a prefix (`Scan(prefix)`).
- **Optimistic concurrency.** CAS via store transactions, never lock-and-hold.
- **Time-windowed averaging for autoscaling.** Sliding window, asymmetric stabilization.
- **Topological sort for dependency ordering.** Detect cycles, fail explicitly.
- **Spread algorithms for placement.** Zone-aware bin packing, not random assignment.

### Authorization Architecture

- **Principals**: `User`, `ServiceIdentity` (includes `NodeIdentity`). Groups are auth-provided attributes.
- **Permissions**: Capability-based (`workload.read`, `secret.use`), not K8s API verbs.
- **Scopes**: Hierarchical paths (`cluster`, `team/payments`, `node/node01`).
- **Two-layer**: Store-layer prefix RBAC (internal), API-layer capability auth (users).
- **Deny-by-default, fail-closed.** No bypass path, even without `--tls`.
- **Do NOT implement** K8s Role/ClusterRole/RoleBinding pattern.

---

## Overview

**CCattler** — Kubernetes-alternative container orchestrator built around **facts, rules, and reconciliation**.

**Core premise**: given desired behavior, continuously make a distributed machine satisfy it.

**Language**: Go. **Store**: etcd. **Runtime**: pluggable (Simulator → Process → Container).

### Architectural Rules

1. **No component may assume another performed an action.** React to state only.
2. **Desired state and observed state are always separate.** The diff is the work.
3. **Idempotency is the fundamental primitive.** `ensure_X()`, not `do_X()`.
4. **Store facts, not commands.** `desired(instance, running)` not `start(instance)`.
5. **Controllers return proposed changes.** `reconcile(facts) → []change`.
6. **Init containers must match main containers.** Same network, DNS, env vars.

### Architecture

```
USER → DSL (.cca files) → FACT STORE (etcd) → Controllers → NODE AGENTS → MACHINES
```

### Network CIDRs

- **`--cluster-cidr`** (default `10.100.0.0/16`): Container overlay IPs. Each node gets a /24 subnet.
- **`--vip-cidr`** (default `10.200.0.0/24`): Service VIP pool. nftables DNATs to container endpoints.

Both via `cca init`, `cca server`, `cca agent`. Must not overlap each other or physical network.

---

## Design Documents

See `design/` directory: security-model, multi-tenancy, controllers, config-secrets, node-agent, cloud-integration, dsl-reference, runtime-invariant-flows, threat-model, etcd-schema, networking, storage, TESTING.

## CLI

```
cca apply <file>              # deploy config
cca run [--watch] <file>      # start real OS processes
cca run-container [--watch] <file>  # start real Docker containers
cca server [flags]            # start control plane
cca agent --node-id <id>      # start node agent
cca token create/list/revoke  # bootstrap token management
cca join <server> <token>     # join node to cluster
cca init [flags]              # initialize cluster
cca get services/instances/nodes/secrets/config
cca scale <service> <count>
cca logs <service> [--follow]
cca status / cca watch [prefix]
cca node-inspect <node-id>
cca exec <id> -- <command>
cca images list/pull
cca top [nodes|workloads]
cca metric set <svc> <m> <v>
cca role grant <principal> [flags] # grant role or capability (--role, --capability, --scope)
cca role revoke <principal> [flags] # revoke role or capability
cca role list [principal]        # list all grants
cca role check <principal> [flags] # check if principal has capability
cca gc instances                 # garbage-collect stopped instance facts
cca fmt [--check] <file>
cca lint <file>
```

---

## Release Process

Use `scripts/release.sh` for all releases. Never update changelog, CLAUDE.md status, or tags manually.

```bash
scripts/release.sh M89 v1.17.0-beta
git push && git push origin v1.17.0-beta
```

**Prerequisites:** clean working tree, milestone in `milestones.md`, tests passing.
**CI handles:** `release.yml`, `pages.yml`, `test.yml`, `codeql.yml`, `trivy.yml`.

---

## Integration Testing Infrastructure

E2E tests run on `testbed-100.43` using libvirt VMs. Deploy cluster on bare-metal (100.215/30/31). See [design/TESTING.md](design/TESTING.md).

---

## Implementation Phases & Milestones

wSee [milestones.md](milestones.md) for the full phase checklist and milestone table.
