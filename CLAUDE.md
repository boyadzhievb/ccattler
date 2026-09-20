# CCattler — Fact-Based Container Orchestrator

## Current Status

**Completed through:** M39 — Phase 42 Store Correctness. M1–M39 complete. All architecture debt resolved.

### Architecture Debt (from external reviews, Sep 19 2026)

Items addressed by Phase 26 (P0 Correctness), Phase 35a (Correctness II), Phase 36 (Correctness III), and Phase 37 (Runtime Stats): runtime observation authoritative, 127.0.0.1 fallback removed, probe scheduling decoupled, init restart-safe, watch overflow resync, transaction snapshot CAS, derived/ prefix, etcd Put simplified, VIP allocation race fixed, init dual authority resolved, etcd WithPrevKV, EventProjector from committed state, init runtime isolation via ExecInit, multi-port endpoint model, agent sub-reconciler extraction, Runtime Stats() API.

**All architecture debt from external reviews resolved.**

### Performance Optimization Principle

Optimize the architecture first, the algorithms second, Go code third, and assembly only for demonstrated hot paths. Go is the right language for the control plane. Networking data plane is the one area where eBPF/XDP could eventually matter. Establish benchmarks (`cca benchmark`) before optimizing: reconciliation/sec, scheduling/sec, state transactions/sec, controller latency, agent reconciliation latency, startup-to-ready latency.

---

## Code Style Rules

- **Comment every function.** Every exported and unexported function must have a doc comment explaining what it does.
- **Use long descriptive variable names.** No single-letter or cryptic abbreviations. Examples: `factStore` not `s`, `instanceController` not `ic`, `nodeAgent` not `ag`, `simulatorRuntime` not `rt`, `serviceName` not `svc`, `factEntry` not `f`.
- **Document variables.** Struct fields must have inline comments explaining their purpose. Named constants and map variables should have comments when their role isn't obvious from the name alone.
- **Descriptive function names.** Prefer `executeReconciliationCycle` over `reconcileOnce`, `buildClusterStatusJSON` over `buildStatusJSON`, `findInstancesPlacedOnThisNode` over `desiredInstances`.
- **Receiver names match the type.** Use `nodeFailureController` not `ctrl`, `memStore` not `m`, `containerRuntime` not `c`.

---

**CCattler** (Container Cattler) — a Kubernetes-alternative container orchestrator built around **facts, rules, and reconciliation** instead of an object hierarchy. The name captures the metaphor: something that herds and manages containers, without implying that the containers themselves are the primary abstraction.

**Core premise**: given a description of desired behavior, continuously make a distributed machine satisfy that description.

**Central design principle**: CCattler manages containers by maintaining state and constraints, rather than exposing an object hierarchy to the user.

## Language

**Go** — the standard in the Kubernetes ecosystem. Familiar to K8s developers and operators, rich container tooling (containerd client, CNI, OCI), excellent concurrency primitives for controllers, and straightforward cross-compilation for node agents.

## Design Philosophy

- The cluster contains **facts**, not objects
- Users write **domain intent**, not serialized implementation objects
- No Pods, ReplicaSets, Deployments, or `apiVersion/kind` — these are implementation artifacts
- Extensibility via **typed facts + schemas** instead of CRDs
- Controllers are **rule engines** operating on facts, not object-method hierarchies
- The scheduler is a **pure function**: `schedule(requirements, nodes) → placement`

### Architectural Rules

1. **No component may assume another component performed an action.** Components only react to state. The scheduler writes a placement fact; the network controller notices it. They never call each other.
2. **Desired state and observed state are always separate.** Never overwrite desired with reality. The difference between them is the work.
3. **Idempotency is the fundamental primitive.** Every operation is `ensure_X()`, not `do_X()`. Safe to repeat. `ensure_running(container)`, not `start_container()`.
4. **Store facts and desired results, not commands.** `desired(instance, running)` not `start(instance)`. This makes the system naturally retryable — if an action fails, the gap between desired and actual persists and the agent tries again.
5. **Controllers return proposed changes, not arbitrary mutations.** `reconcile(facts) → []change`, validated and committed transactionally by the command layer.

### Intent Layers

Multiple writers (user, autoscaler, policy) can contribute facts without fighting over the same object:

```
user intent:        min=3, max=30
autoscaler decision: desired=5
→ effective_instances(web) = 5
```

Each layer writes its own facts. Effective state is derived.

### Security Model

Five layers: Identity → Authentication → Authorization → Isolation → Audit.

**Zero-trust by default** — being inside the cluster grants nothing. Every component authenticates and is authorized independently.

#### Transport: mTLS Everywhere

```
CLI ──────── mTLS/OIDC ──────► API
API ──────── mTLS ────────────► Store
Controller ── mTLS ────────────► Store
Node ──────── mTLS ────────────► Store/API
```

No unauthenticated cluster communication.

#### Certificate Authority Hierarchy

```
              Offline Root CA
                    │
        ┌───────────┼───────────┐
        ▼           ▼           ▼
  Control-plane   Node CA    Workload CA
      CA            │           │
      │        ┌────┴────┐    services
  ┌───┴───┐   node-1  node-2
API-1  API-2
```

- Root CA is offline (never on a running control-plane machine)
- Short-lived certificates (1hr) with automatic rotation
- SPIFFE-style workload identities: `spiffe://cluster/node/node-1`, `spiffe://cluster/controller/scheduler`

#### Node Enrollment

```
cca join <cluster> <bootstrap-token>
```

Bootstrap token is short-lived, single-use, scoped to enrollment only. Node generates its own private key, sends CSR, receives signed certificate, bootstrap token is destroyed. Supports hardware identity (TPM, cloud instance identity) for stronger enrollment.

#### Human Authentication

Standard identity providers (OIDC, OAuth 2.0, LDAP, SAML):

```
Developer → Identity Provider → OIDC token → CCattler API
```

Token establishes: `subject=alice, groups=[developers, payments]`

#### Authorization: RBAC + ABAC

**RBAC** for broad authority — permissions over fact prefixes:

```
role developer {
    allow service.read
    allow service.update
}

role operator {
    allow read *
    allow modify service/*
    allow modify node/*
}

grant developer to group developers
```

**ABAC** for context — attribute-based conditions:

```
policy team-isolation {
    allow service.update
    when subject.team == resource.team
}

policy production-gate {
    allow service.deploy
    when subject.environment != "production"
    OR subject.role == "production-deployer"
}
```

Every API operation evaluates: `authorize(principal, action, resource, context) → ALLOW | DENY`

#### Per-Controller Least Privilege

Every controller gets its own identity and minimum permissions:

```
scheduler:          READ nodes, instances, requirements    WRITE placements
network:            READ instances, endpoints              WRITE routing
autoscaler:         READ health, metrics                   WRITE intent/autoscaler
node-agent:         READ desired/node-assignments          WRITE observed/node-X/*
```

A compromised autoscaler cannot modify user configuration. A compromised node cannot set `desired_instances(database) = 0` — it can only write to `observed/`.

#### Authorized Store

The fact store is the security boundary — every write is authenticated, authorized, and audited:

```
controllers → Authorized Store (identity + authN + authZ + txn + audit) → state
```

#### Secrets

Secrets live in an encrypted store, never in plain DSL config:

```
secret database.password

service database {
    secret database.password
}
```

Envelope encryption with KMS integration (AWS KMS, GCP KMS, Vault, HSM). Secrets scoped by service — the scheduler and network controller never see them.

#### Workload-to-Workload Network Policy

Identity-based, not IP-based:

```
allow frontend → api:443
allow api → database:5432
deny frontend → database:5432
```

Network controller translates identity policies → iptables/nftables/eBPF rules.

#### Audit Logging

Every security-sensitive operation produces an immutable record:

```
principal:   alice
auth:        OIDC
action:      service.update
target:      service/web
change:      image nginx:1.27 → nginx:1.28
decision:    ALLOW
policy:      production-deployer
request_id:  8f31...
```

#### Bootstrap Problem

Cluster creation generates a one-time bootstrap credential → creates admin identity → bootstrap credential destroyed. No permanent "magic password."

#### Cryptographic Trust Summary

```
        Root CA
           │
  ┌────────┼────────┐
  ▼        ▼        ▼
Users    Nodes   Controllers
 OIDC     mTLS      mTLS
  │        │        │
  └────────┼────────┘
           ▼
     Authorization
      RBAC + ABAC
           │
           ▼
      State changes
           │
           ▼
        Audit
```

### Multi-Tenancy

No namespaces. Tenancy is built from **ownership, identity, and policy** as separate concerns.

#### Tenants & Ownership

```
tenant payments
tenant frontend
tenant platform
```

Every resource carries an owner: `owner(service:checkout, payments)`. Ownership drives authorization, quotas, and cleanup.

#### Hierarchical Naming

Filesystem-style paths instead of namespace prefixes:

```
/frontend/web
/payments/checkout
/payments/database
/platform/dns
```

Tenant owns its subtree — `/payments/*` is naturally isolated.

#### Resource Quotas

Attached to tenants, not namespaces:

```
tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
        volumes 50
        storage 10Ti
    }
}
```

Stored as facts (`quota_cpu(payments, 100)`), usage is observed (`usage_cpu(payments, 74)`). Admission is arithmetic: `74 + 30 > 100 → DENY`.

#### Fair Scheduling

Beyond quotas — weighted fairness prevents starvation:

| Tenant | Weight | Guaranteed CPU |
|--------|--------|----------------|
| platform | 5 | 50 |
| payments | 3 | 30 |
| frontend | 2 | 20 |

Unused guarantees become borrowable.

#### Network Isolation (Identity-Based)

No label selectors. Policies reference service identities:

```
network {
    allow frontend/web -> payments/checkout port 443
    allow payments/checkout -> payments/database port 5432
    deny frontend/web -> payments/database
}
```

When instances move, policies don't change — only derived firewall rules change.

#### Service Identity

Every instance gets a SPIFFE identity: `spiffe://ccattler/payments/database`. mTLS between services is automatic — no shared secrets.

#### Secret Isolation

Secrets belong to tenants. Only explicitly granted services receive them. Scheduler and network controller never see plaintext. Envelope encryption with master key rotation.

#### Shared Services

Cross-tenant infrastructure via exports:

```
export platform/dns {
    allow frontend
    allow payments
}
```

Consumers declare `uses platform/dns`.

#### Tenant Lifecycle

Creating a tenant automatically creates: identity scope, quota, network boundary, secret space, audit stream. Deleting a tenant triggers ownership-driven garbage collection of all resources.

#### Policy Gates (Admission)

Every change passes through a pipeline (borrowed from K8s admission controllers):

```
APPLY → syntax validation → schema validation → RBAC/ABAC → quota check → security policy → mutation → commit
```

#### Multi-Tenant Visibility

| Component | Tenant sees | Platform sees |
|-----------|-------------|---------------|
| Services | Own | All |
| Secrets | Own | Metadata only |
| Volumes | Own | All |
| Network policies | Own | All |
| Audit logs | Own | All |
| Node health | Aggregated | Full |

### Control Plane Failure Tolerance

Nodes cache last known desired state. If the control plane dies for 10 minutes, workloads keep running. When it returns, observed state + desired state → reconciliation → convergence.

## Architecture

```
                USER
                  │
                  ▼
          ┌──────────────┐
          │ DOMAIN LANG. │    .ccattler files
          └──────┬───────┘
                 │
                 ▼
          ┌──────────────┐
          │  FACT STORE  │    etcd
          └──────┬───────┘
                 │
      ┌──────────┼───────────┐
      │          │           │
      ▼          ▼           ▼
  scheduler   network     storage     (controllers — watch facts, derive actions)
      │          │           │
      └──────────┼───────────┘
                 │
                 ▼
          ┌──────────────┐
          │  NODE AGENTS │    one per machine, runs containers via containerd
          └──────┬───────┘
                 │
                 ▼
             MACHINES
```

### Computational Model (the reconciliation loop)

```
facts → rules/queries → new facts → actions → real world → observed facts → repeat
```

## Core Fact Types

```
service    (id, name, image, desired_instances, port)
instance   (id, service_id, node_id, state)
node       (id, cpu, memory, state)
endpoint   (service_id, instance_id, address, port)
volume     (id, name, size, persistent, access_mode)
placement  (instance_id, node_id)
config     (service, key, value, type)        — env var or file, declarative desired state
secret     (name)                              — secret exists (value in encrypted store, never here)
secret_grant (service, secret_name)            — service is authorized to access secret
init_step  (service, index, exec, timeout, retry) — initialization step before main workload
init_phase (instance, phase)                   — derived init lifecycle state (pending/running/complete/failed)
utilization (node, cpu, memory, workload_count) — observed resource telemetry
probe      (service, type, method, path, port, interval, timeout, thresholds) — startup/liveness/readiness config
probe_state (instance, type, state)    — observed probe result (gates endpoints and restarts)
node_label (node_id, label, value)     — key-value label on a node for placement matching
node_restrict (node_id, label)         — node restriction preventing scheduling without accept
```

## Domain Language (DSL)

No YAML. A human-oriented declarative language:

```
service web {
    image nginx:1.27
    instances 3

    expose 8080

    init {
        exec "db-migrate --run"
        timeout 30s
        retry 3
    }

    init {
        exec "cache-warm"
        timeout 10s
    }

    health {
        http /health
        every 10s
    }

    resources {
        cpu 500m
        memory 512Mi
    }

    scale {
        horizontal {
            min 3
            max 30
            target cpu = 60%
            target requests_per_second = 500
            event { source payments.pending, target 20 messages/instance }
            schedule { weekdays 08:00-18:00, minimum 10 }
        }
        vertical {
            cpu { min 250m, max 4 }
            memory { min 512Mi, max 8Gi }
        }
    }

    placement {
        architecture amd64
        zone spread
        require gpu = true
        prefer region = us-east
        accept dedicated-compute
    }
}

volume database {
    size 100Gi
    persistent true
}

group frontend {
    process proxy
    process web
    share network
    share volume cache
}

role developer {
    allow service.read
    allow service.update
}

grant developer to group developers

policy team-isolation {
    allow service.update
    when subject.team == resource.team
}

config api {
    env "LOG_LEVEL" = "info"
    env "PORT" = "8080"

    file "/etc/api/config.yaml" = "..."
}

secret database.password

service database {
    image "postgres:16"
    instances 1

    secret database.password {
        mount "/run/secrets/database-password"
    }
}

tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
    }
}

network {
    allow frontend/web -> payments/checkout port 443
    deny frontend/web -> payments/database
}

export platform/dns {
    allow frontend
    allow payments
}
```

The parser compiles DSL into facts:

```
service(name="web", image="nginx:1.27", instances=3)
exposes(service="web", port=8080)
requires(service="web", cpu=500m, memory=512Mi)
```

**Architectural boundary**: everything above the fact store is human-facing; everything below is machine-facing.

## Runtime Adapters

The runtime is pluggable — prove semantics first, add real infrastructure later:

```
Runtime
   │
   ├── SimulatorRuntime    ← pure simulation, no processes (Phase 0)
   ├── ProcessRuntime      ← Linux processes (Phase 1)
   └── ContainerRuntime    ← containerd/CRI-O (Phase 3+)
```

Similarly, the store is pluggable:

```
StateStore
   │
   ├── MemoryStore         ← laptop development, tests
   └── EtcdStore           ← distributed production
```

Controllers never know which backend they're using. **Build the semantic control plane first; make Linux/container/distributed infrastructure replaceable adapters underneath.**

Runtime interface includes `Exec(ctx, id, ExecSpec)` for running commands inside a workload — used by init steps and diagnostics.

### Logical Node Simulation

Test scheduling and failure on one laptop with fake nodes:

```
node(laptop-1, cpu=4, memory=8Gi, zone=local-a)
node(laptop-2, cpu=4, memory=8Gi, zone=local-b)
node(laptop-3, cpu=4, memory=8Gi, zone=local-c)
```

The scheduler doesn't know they're simulated. Kill `laptop-2` → lease expires → reconciler reschedules → system converges.

### Deterministic Testing

The entire control plane must be deterministic: given state A + observation B + policy C → desired state D. Tests don't need a cluster:

```
INPUT:  nodes=[n1:4cpu, n2:4cpu], service=web, desired=5, cpu=1
EXPECT: placement=[n1:3, n2:2]

INPUT:  n2=dead
EXPECT: placement=[n1:4, unsatisfied=1]

INPUT:  n2=alive
EXPECT: placement=[n1:3, n2:2]
```

### Chaos Mode

```
cca chaos
```

Randomly injects: node failures, network delays, process crashes, stale observations, controller restarts, duplicate events, lost messages, slow storage. Asserts: **eventually observed state converges to desired state.**

## Fact Store Interface

The store is the spine. Every component talks through it.

```
Get(key) → (value, revision)
Put(key, value) → revision
Delete(key)
Scan(prefix) → []fact
Watch(prefix) → channel of events
Transaction(compares, mutations) → ok/fail
Revision() → global revision
```

Transactions use optimistic concurrency:
```
transaction:
    if service/web revision == 1827
    then set desired_instances(web) = 5
```

### Backend: etcd

**etcd** — proven for Kubernetes-style control planes, familiar to K8s developers and operators. Provides distributed consensus, strongly consistent reads/writes, transactions, watches, revisions, and leases out of the box. The store interface maps directly onto etcd's API without an adapter layer.

## Controllers (Rule Engine)

Controllers watch fact prefixes, compare desired vs actual state, and write new facts/actions.

### Instance Controller
```
RULE maintain_instances(service):
    desired = service.instances
    actual  = count(instances where instance.service = service)
    if actual < desired: create(desired - actual)
    if actual > desired: remove(actual - desired)
```

### Endpoint Controller
```
RULE expose_running(service):
    for instance where instance.service = service AND instance.state = running:
        create endpoint(service, instance)
```

### Failure Controller
```
RULE replace_failed_instance(instance):
    if instance.state = failed:
        create replacement
```

### Init Controller
```
RULE derive_init_phase(instance):
    steps = desired_init_steps(instance.service)
    if all steps succeeded: phase = complete
    if any step failed:    phase = failed
    if any step running:   phase = running
    else:                  phase = pending
```

### Autoscaling (Unified Scaling Engine)

**Architectural rule**: autoscalers recommend and modify desired capacity — they never directly manipulate runtime instances.

One generic engine with three concepts: **Signal**, **Policy**, **Dimension**.

#### Horizontal Scaling
```
autoscale checkout {
    dimension instances
    min 2, max 30
    target cpu = 60%
    target memory = 70%
    target requests_per_second = 500
}
```
Multiple metrics evaluated independently → `desired = max(cpu_rec, mem_rec, rps_rec)`.

#### Vertical Scaling
Changes resource requirements instead of instance count:
```
scale vertically {
    cpu { min 250m, max 4 }
    memory { min 512Mi, max 8Gi }
}
```
Reconciler decides: resize in-place (live cgroup update) or replace instance.

#### Event-Driven Scaling
External signals become observations, not imperative triggers:
```
scale horizontally {
    event { source payments.pending, target 20 messages/instance }
}
→ queue_depth(800) / 20 = 40 instances
```

#### Scheduled Scaling
Time as an input signal:
```
schedule { weekdays 08:00-18:00, minimum 10 }
```

#### Stabilization
Never scale from instantaneous metrics. Use time-windowed averages with asymmetric stabilization:
```
scale-up stabilization = 60s
scale-down stabilization = 5m
```

#### Multi-Policy Resolution
Single scaling decision function — no competing writers:
```
desired = max(cpu_rec, queue_rec, scheduled_min)
    subject to: min, max, tenant quota, cluster capacity
```

#### Capacity Chain
```
autoscaler recommendation → tenant quota → scheduler capacity → running
```
Each constraint is visible: "scaling constrained by tenant quota" vs silent capping.

#### Cluster Autoscaling
Unsatisfied scheduling demand triggers node scaling:
```
application autoscaler → desired workload → scheduler → unsatisfied capacity
    → cluster autoscaler → desired nodes → infrastructure provider
```

#### Complete Flow
```
Metrics/Events → Scaling Engine (policies, prediction, stabilization)
    → Policy/Limits (quota, min/max, capacity) → Desired State → Reconciliation → Reality
```

The instance controller doesn't care *why* desired changed — it just sees `desired=5, actual=3` and creates two more.

### Controller Interface
```go
type Controller interface {
    Name() string
    Watch() []string          // fact prefixes to observe
    Reconcile(facts) []Change // proposed changes, not arbitrary mutations
}
```

When a node dies, the fact `node(node-a).state = dead` invalidates all dependent facts. Controllers converge the system without explicit notification to every object.

## Scheduler

Pure function — no methods on objects:

```
schedule(requirement, available_nodes) → placement

Input:
    cpu = 500m, memory = 512Mi
    nodes: [{node-a, 4cpu avail}, {node-b, 12cpu avail}]

Output:
    place on node-b
```

Considerations: resource fit, spread (distribute across nodes/zones), existing allocation, placement constraints (require/prefer/restrict/accept).

## Networking

A service's endpoints are derived facts, not managed objects:

```
service web, instances 3, expose 8080
  →
endpoint(web) = {10.0.1.4:8080, 10.0.2.8:8080, 10.0.3.2:8080}
```

Instance disappears → endpoint set updates automatically via the endpoint controller.

Service discovery via DNS: `<service-name>` resolves to current endpoints.

## Rolling Deployments

Image change triggers state reconciliation, not object mutation:

```
desired: 10 × nginx:1.28
actual:  10 × nginx:1.27

update_strategy { max_unavailable 1, max_extra 1 }

→ start 1.28, wait healthy, stop 1.27, repeat
```

Rollback: if new instances fail health checks beyond threshold, revert desired image fact.

## Storage

```
volume(database, 100Gi, persistent)
attached(database, node-b)
mounted(database, instance-db-1)
```

Constraints (e.g., persistent volume cannot simultaneously attach to incompatible nodes) are enforced by storage controllers as rules, not object methods.

## Config & Secrets

Config and secrets are two separate mechanisms. Controllers reason about references and desired state. The node agent is the only component that materializes config and secrets into workloads.

### Config

Config is declarative desired state, not imperative "set this environment variable":

```
config api {
    env "LOG_LEVEL" = "info"
    env "PORT" = "8080"

    file "/etc/api/config.yaml" = "..."
}

service api {
    image "my-api:v3"
    config api
}
```

The fact store holds config facts (`desired/service/api/config/...`), not rendered environment variables. The scheduler and controllers don't care how config becomes runtime input — that's the node agent's job.

Config supports two delivery modes:
- **Environment variables** — `env "KEY" = "value"`
- **Config files** — `file "/path" = "content"`

### Secrets

Secrets are references, never values. The fact store holds grants, not plaintext:

```
secret database.password

service api {
    secret database.password {
        mount "/run/secrets/database-password"
    }
}
```

Stored as:
```
secret(database.password)              — secret exists
secret_grant(api, database.password)   — api may access it
```

Never:
```
secret(database.password, "super-secret-password")   — WRONG
```

Actual secret values live in the encrypted secret subsystem (envelope encryption with KMS). The scheduler and network controller never see plaintext secrets.

**File-mounted secrets are preferred over environment variables:**
- Avoids exposing secrets through environment inspection
- Avoids accidental logging of environment variables
- Makes rotation easier (overwrite file, signal process)
- Works with applications that already consume secret files
- Gives the agent control over permissions (`0400`, memory-backed, ephemeral)

### Materialization Boundary

```
             CONTROL PLANE
                   │
      ┌────────────┴────────────┐
      │                         │
 desired config           secret reference
      │                         │
      └────────────┬────────────┘
                   │
             Fact Store
                   │
                   ▼
              Node Agent
                   │
         ┌─────────┴─────────┐
         │                   │
    config resolver     secret resolver
         │                   │
         └─────────┬─────────┘
                   ▼
              Container
```

Don't put rendered config into the fact store. Store `desired(instance, running)` + `config(api, ...)` + `secret_grant(api, database.password)`. The agent resolves effective configuration at reconciliation time.

### Secret Lifecycle

Secret access is tied to container lifecycle:

```
instance api-7 assigned to node-3
  → node-3 obtains authorized secret copy
  → secret materialized locally
  → container starts
  → container stops
  → secret material removed
```

If the container moves from node-3 to node-5, node-5 gets a fresh authorized copy. The old node removes its copy. This fits the zero-trust + short-lived credentials model.

## API

Tiny surface — not hundreds of REST endpoints:

```
GET    /state                    # read facts
QUERY  /state                    # query with predicates
APPLY  /config                   # submit DSL
WATCH  /changes                  # stream changes
```

Examples:
```
QUERY instances WHERE service="web" AND state="running"

APPLY
service web {
    image nginx:1.28
    instances 5
}
```

## Events

Events are history, not authoritative state. Append-only stream for operators:

```
2026-09-06 18:42 service.web.changed
2026-09-06 18:42 instance.a8f31.created
2026-09-06 18:45 node.node-2.unreachable
2026-09-06 18:45 instance.a8f31.failed
```

`STATE = truth` / `EVENTS = history`

## Node Agent

Small binary on each machine with three internal components:

```
          NODE AGENT
              │
   ┌──────────┼──────────┐
   ▼          ▼          ▼
observer   reconciler  reporter
```

- **Observer** — reads Linux state (`/proc`, `/sys`, cgroups, container runtime) to determine what is actually running
- **Reconciler** — compares desired state (from store) with observed state, executes `ensure_*()` operations via containerd/runc. Runs init steps before main workload start (sequential, with retry/backoff). Resolves config and secrets at reconciliation time: reads config facts, obtains authorized secret copies, materializes both into the container as environment variables, config files, or secret files
- **Reporter** — publishes actual state, health, capacity, and events back to the store
- **Telemetry** — collects per-node workload count and per-instance CPU/memory usage, writes as observed facts (powers `cca top`)

The node agent is the materialization boundary for config and secrets:

```
CCattler configuration
        │
        ├── environment variables
        ├── config files
        └── secret files
                │
                ▼
           Node Agent
                │
                ▼
       Runtime Adapter
                │
        ┌───────┼────────┐
        ▼       ▼        ▼
   Process   Container  Simulator
```

This keeps all three runtimes interchangeable — the agent translates declarative config/secret facts into whatever the runtime needs.

Node registration: publishes `node(id)`, `capacity_cpu`, `capacity_memory`, `available_cpu`, `available_memory`, `architecture`, `zone`.

Lease: maintains a TTL lease in etcd. If the agent disappears, lease expires → control plane derives `node_state(node, unreachable)`.

Container runtime interface (pluggable — containerd, CRI-O, Podman, systemd-nspawn):
```
ensure_container(id, image, resources, environment, mounts, network, desired_state)
```

## Extensibility

No CRDs. Plugins introduce **typed facts + schemas**:

```
fact firewall_rule {
    source      network
    destination network
    port        integer
    action      enum(allow, deny)
}
```

Extensibility = new facts + new constraints + new transformations.

Controller SDK: subscribe to fact prefixes, run reconciliation logic, write facts back.

## Cloud Provider Integration

Cloud providers manage node lifecycle, load balancers, and VPC routes:

```
cloud {
    provider aws
    region "us-east-1"
    instance_type "m5.large"
    credentials infra_identity
    route_table "rtb-abc123"
}

service web {
    image nginx:1.27
    instances 3
    expose 8080
    expose external 443 http
}
```

The `cloud` block configures the provider. `expose external` marks a port for cloud load balancer creation. The cloud controller manager runs three sub-controllers:
- **NodeLifecycleController** — detects terminated cloud instances, cordons and drains nodes
- **CloudLoadBalancerController** — creates/updates/deletes cloud LBs for `expose external` services
- **CloudRouteController** — programs VPC routes from node subnet assignments

Enabled via: `cca server --cloud-provider aws --cloud-region us-east-1`

## CLI

```
cca apply <file>              # deploy config (simulated, prints status and exits)
cca run [--watch] <file>      # start real OS processes (--watch for live status)
cca run-container [--watch] <file>  # start real Docker containers (--watch for live status)
cca server [--listen h:p] [--tls] [--cert/--key/--ca] [--api-only] [--controllers-only] [--node-id <id>] [--cloud-provider <name>] [--cloud-region <region>]  # control plane (--cloud-provider enables cloud controllers)
cca agent --node-id <id> [--cert/--key/--ca] [--advertise-address <ip>]  # node agent (mTLS, VIP data plane)
cca token create [--node-id <id>] [--ttl 15m]  # generate join token
cca token list                # list active join tokens
cca token revoke <token>      # revoke a join token
cca join <server> <token> --node-id <id> [--ca-cert <path>]  # enroll node
cca get services              # list services
cca get instances             # list instances
cca get nodes                 # list nodes
cca get secrets               # list secrets and their grants
cca get config                # list config entries (env vars and files)
cca scale web 20              # change desired count
cca logs <service> [--follow] [--instance <id>]  # aggregate container stdout/stderr logs
cca status                    # cluster overview
cca watch [prefix]            # stream fact store changes
cca top [nodes|workloads]     # resource utilization (CPU, memory, instances)
cca metric set <svc> <m> <v>  # inject simulated metric
```

---

## Implementation Phases & Milestones

See [milestones.md](milestones.md) for the full phase checklist (Phases 0–39a) and milestone table (M1–M36a).

