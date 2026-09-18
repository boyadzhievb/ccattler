# Architecture

## System overview

```
        USER
          |
          v
  +---------------+
  | DOMAIN LANG.  |    .ccattler files
  +-------+-------+
          |
          v
  +---------------+
  |  FACT STORE   |    etcd
  +-------+-------+
          |
  +-------+--------+-------+
  |       |        |       |
  v       v        v       v
sched  network  storage  autoscale  (controllers — watch facts, derive actions)
  |       |        |       |
  +-------+--------+-------+
          |
          v
  +---------------+
  | NODE AGENTS   |    one per machine, runs containers via containerd
  +-------+-------+
          |
          v
      MACHINES
```

## Fact Store

The spine of the system. Every component talks through it.

### Interface

```go
Get(key) → (value, revision)
Put(key, value) → revision
Delete(key)
Scan(prefix) → []fact
Watch(prefix) → channel of events
Transaction(compares, mutations) → ok/fail
Revision() → global revision
```

### Backends

| Backend | Use case |
|---|---|
| `MemoryStore` | Tests, local development, single-process mode |
| `EtcdStore` | Distributed production clusters |

Controllers never know which backend they're using.

### etcd integration

- Key prefix namespacing — multiple CCattler clusters can share one etcd instance
- Idempotent Put — compares before writing to avoid unnecessary revision bumps
- Watch bridging — etcd watch channel forwarded to CCattler Event channel
- Transaction mapping — `Compare/Op` mapped to etcd `Txn`

## Controllers

Independent rule engines. Each watches specific fact prefixes, compares desired vs observed state, and proposes changes.

| Controller | Watches | Writes |
|---|---|---|
| Instance | `effective/service/*/instances`, `observed/instance/` | instance creation/deletion |
| Endpoint | `observed/instance/*/state`, `observed/instance/*/ip` | `endpoint/service/*` |
| Failure | `observed/node/*/state`, `lease/node/` | replacement instances |
| Init | `desired/service/*/init`, `observed/instance/*/init` | init phase derivation |
| Autoscaler | `observed/instance/*/health`, metrics | `intent/autoscaler/*` |
| Rolling update | `desired/service/*/image`, `observed/instance/*/image` | instance replacement |
| Network | identity policies | firewall rules |
| Storage | `desired/volume/`, `observed/volume/` | volume attachment |

Controllers communicate **only through state**. They never call each other.

## Scheduler

A pure function:

```
schedule(requirement, available_nodes) → placement
```

Considerations: resource fit, zone spread, existing allocation, placement constraints (require/prefer/restrict/accept).

The scheduler writes `placement/instance/{id} → node-id`. The node agent reads it and starts the container.

## Node Agent

A binary on each machine with three internal components:

```
          NODE AGENT
              │
   ┌──────────┼──────────┐
   ▼          ▼          ▼
observer   reconciler  reporter
```

- **Observer** — reads Linux state (`/proc`, `/sys`, cgroups, container runtime) to determine what's running
- **Reconciler** — compares desired vs observed, runs `ensure_*()` operations, materializes config and secrets
- **Reporter** — publishes actual state, health, capacity, and events back to the store
- **Telemetry** — collects per-node workload count and per-instance CPU/memory usage

### Runtime adapters

The runtime is pluggable:

| Adapter | Description |
|---|---|
| `SimulatorRuntime` | Pure simulation, no processes (testing) |
| `ProcessRuntime` | Linux processes (development) |
| `ContainerRuntime` | nerdctl/containerd (production) |

## API

Tiny surface:

| Endpoint | Method | Description |
|---|---|---|
| `/state` | GET | Read facts |
| `/state` | QUERY | Query with predicates |
| `/config` | APPLY | Submit DSL configuration |
| `/changes` | WATCH | Stream changes (SSE) |
| `/api/enroll` | POST | Node enrollment |

## Control plane failure tolerance

Nodes cache last known desired state. If the control plane dies:

- Workloads keep running (cached desired state)
- Node agents continue reporting observed state locally
- When control plane returns: observed + desired → reconciliation → convergence

The design invariant: **temporary control-plane failure must never automatically destroy data-plane availability.**

## Multi-node control plane

For high availability:

- 3 or 5 control-plane nodes with leader election
- Controllers are stateless — multiple copies share state, automatic failover
- etcd cluster provides distributed consensus
