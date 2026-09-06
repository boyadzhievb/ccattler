# CCatler — Fact-Based Container Orchestrator

A Kubernetes-alternative container orchestrator built around **facts, rules, and reconciliation** instead of an object hierarchy. The core premise: given a description of desired behavior, continuously make a distributed machine satisfy that description.

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

Fact-based permissions with a clean desired/observed boundary:

```
alice:             can modify desired/service/*    cannot modify observed/*
node-agent:        can modify observed/*           cannot modify desired/user/*
```

A compromised node can report "database is stopped" but can never set `desired_instances(database) = 0`.

### Control Plane Failure Tolerance

Nodes cache last known desired state. If the control plane dies for 10 minutes, workloads keep running. When it returns, observed state + desired state → reconciliation → convergence.

## Architecture

```
                USER
                  │
                  ▼
          ┌──────────────┐
          │ DOMAIN LANG. │    .ccatler files
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
```

## Domain Language (DSL)

No YAML. A human-oriented declarative language:

```
service web {
    image nginx:1.27
    instances 3

    expose 8080

    health {
        http /health
        every 10s
    }

    resources {
        cpu 500m
        memory 512Mi
    }

    autoscale {
        cpu > 70%
        min 3
        max 30
    }

    placement {
        architecture amd64
        zone spread
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
```

The parser compiles DSL into facts:

```
service(name="web", image="nginx:1.27", instances=3)
exposes(service="web", port=8080)
requires(service="web", cpu=500m, memory=512Mi)
```

**Architectural boundary**: everything above the fact store is human-facing; everything below is machine-facing.

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

### Autoscale Controller
```
RULE autoscale(service):
    if cpu(service) > threshold:
        set desired_instances = min(current + scale_up, max)
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

Considerations: resource fit, anti-affinity (spread instances across nodes), existing allocation.

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
- **Reconciler** — compares desired state (from store) with observed state, executes `ensure_*()` operations via containerd/runc
- **Reporter** — publishes actual state, health, capacity, and events back to the store

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

## CLI

```
ctl apply <file>              # deploy config
ctl get services              # list services
ctl get instances             # list instances
ctl get nodes                 # list nodes
ctl scale web 20              # change desired count
ctl logs web                  # view logs
ctl status                    # cluster overview
```

---

## Implementation Phases

### Phase 0 — Foundation
- [x] Language: **Go**
- [x] Backing store: **etcd**
- [x] etcd key layout & consistency model → see [etcd-schema.md](etcd-schema.md)
- [ ] Set up repo: `lang/`, `store/`, `scheduler/`, `controllers/`, `agent/`, `api/`, `cli/`

### Phase 1 — Fact Store
- [ ] Define the store interface (Get/Put/Delete/Scan/Watch/Transaction)
- [ ] Implement in-memory store (for tests and local dev)
- [ ] Implement etcd (or Postgres) adapter
- [ ] Store integration tests — concurrency, watch ordering, transaction conflicts

### Phase 2 — Domain Language & Parser
- [ ] Design formal grammar for the DSL
- [ ] Write lexer/parser → AST
- [ ] AST → facts compiler (the human-to-machine boundary)
- [ ] Validation layer — image refs, port ranges, resource units, duplicates
- [ ] `apply` command — parse file → compile → transactionally write facts

### Phase 3 — Reconciliation Engine
- [ ] Controller framework — generic watch → reconcile → write loop
- [ ] Instance controller (desired vs actual instance count)
- [ ] Endpoint controller (running instances → endpoint facts)
- [ ] Failure controller (dead instances/nodes → replacement facts)
- [ ] Deterministic reconciliation tests

### Phase 4 — Scheduler
- [ ] Scoring function: `schedule(requirements, nodes) → placement`
- [ ] Integration with instance controller
- [ ] Anti-affinity / spread rules
- [ ] Resource accounting (allocated vs available per node)

### Phase 5 — Single Machine (M2 target)
- [ ] Node agent with observer/reconciler/reporter
- [ ] Container runtime interface (containerd) — pull, start, stop
- [ ] Health checking (HTTP, TCP, exec probes)
- [ ] Graceful shutdown (SIGTERM → grace period → SIGKILL)
- [ ] End-to-end: CLI → parser → store → reconciler → container on one machine

### Phase 6 — Three Machines (M3 target)
- [ ] Multi-node agent registration, leases, heartbeats
- [ ] Distributed scheduling across nodes
- [ ] Node failure detection (lease expiry → unreachable → reschedule)
- [ ] `service web { instances 10 }` actually distributes across nodes

### Phase 7 — Networking
- [ ] Instance IP allocation from pool
- [ ] Endpoint aggregation
- [ ] DNS / service discovery (CoreDNS integration or custom)
- [ ] Load balancing (per-node proxy or centralized)

### Phase 8 — Storage
- [ ] Volume facts and attach/mount lifecycle
- [ ] Constraint enforcement (exclusive attach, node compatibility)
- [ ] Storage driver interface (local, NFS, cloud block)

### Phase 9 — Failure & Chaos Testing
- [ ] Kill node — does the system reschedule?
- [ ] Kill agent — does the lease expire and trigger recovery?
- [ ] Kill scheduler/controller — does another instance take over?
- [ ] Network partition — do both sides stay safe?
- [ ] Restart etcd — does the cluster converge?
- [ ] The only question: **does the system eventually converge to desired state?**

### Phase 10 — Policies
- [ ] Autoscaling (cpu/memory threshold → adjust desired_instances)
- [ ] Intent layers (user, autoscaler, policy — derived effective state)
- [ ] Placement constraints (architecture, zone, spread, affinity)
- [ ] Rolling update controller with max_unavailable/max_extra
- [ ] Rollback on health check failure

### Phase 11 — API, CLI & Security
- [ ] Query API (GET/QUERY/APPLY/WATCH)
- [ ] CLI tool (apply, get, scale, logs, status)
- [ ] Fact-based RBAC: `permission(user, action, prefix)`
- [ ] Desired/observed security boundary enforcement

### Phase 12 — Extensibility & Hardening
- [ ] Typed fact schemas for plugins
- [ ] Custom controller SDK
- [ ] Append-only event log for audit trail
- [ ] Metrics (reconciliation latency, scheduling decisions, instance transitions)
- [ ] Multi-node control plane with leader election (3 or 5 control-plane nodes)
- [ ] Stateless controllers — multiple copies, shared state, automatic failover

### Milestones

| Milestone | Phases | Demo |
|-----------|--------|------|
| M1 — State machine | 0–4 | Apply config, see facts reconcile in the store |
| M2 — Single machine | 5 | CLI → parser → store → reconciler → running container |
| M3 — Distributed | 6 | 10 instances spread across 3 nodes |
| M4 — Networking | 7 | Services reachable by name, traffic balances |
| M5 — Storage | 8 | Persistent volumes survive node moves |
| M6 — Resilient | 9 | Kill anything, cluster converges |
| M7 — Smart | 10 | Autoscaling, rolling deploys, placement policies |
| M8 — Production | 11–12 | Auth, observability, HA control plane, extensibility |

**Start with M1.** If the reconciliation loop and fact store work correctly, everything else layers on top. If they don't, nothing else matters.
