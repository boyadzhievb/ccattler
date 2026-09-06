# etcd Key Layout & Consistency Model

The store schema for CCatler. Every key lives under a top-level prefix that separates desired state from observed state — this boundary is the foundation of the security model and the reconciliation loop.

## Key Hierarchy

```
/ccatler/
├── desired/                        # what the world should look like (written by users + policy controllers)
│   ├── service/{name}              # service definition
│   ├── service/{name}/image
│   ├── service/{name}/instances
│   ├── service/{name}/expose/{port}
│   ├── service/{name}/resources/cpu
│   ├── service/{name}/resources/memory
│   ├── service/{name}/health/method
│   ├── service/{name}/health/path
│   ├── service/{name}/health/interval
│   ├── service/{name}/autoscale/metric
│   ├── service/{name}/autoscale/threshold
│   ├── service/{name}/autoscale/min
│   ├── service/{name}/autoscale/max
│   ├── service/{name}/placement/architecture
│   ├── service/{name}/placement/zone
│   ├── service/{name}/update/max_unavailable
│   ├── service/{name}/update/max_extra
│   ├── volume/{name}               # volume definition
│   ├── volume/{name}/size
│   ├── volume/{name}/persistent
│   └── volume/{name}/access_mode
│
├── effective/                      # derived desired state (computed from intent layers)
│   └── service/{name}/instances    # the actual target after user + autoscaler + policy
│
├── observed/                       # what the world actually looks like (written by node agents)
│   ├── node/{node-id}              # node registration
│   ├── node/{node-id}/state        # alive | unreachable | draining
│   ├── node/{node-id}/capacity/cpu
│   ├── node/{node-id}/capacity/memory
│   ├── node/{node-id}/available/cpu
│   ├── node/{node-id}/available/memory
│   ├── node/{node-id}/architecture
│   ├── node/{node-id}/zone
│   ├── instance/{instance-id}                # instance existence
│   ├── instance/{instance-id}/service        # which service this belongs to
│   ├── instance/{instance-id}/node           # which node it runs on
│   ├── instance/{instance-id}/state          # pending | running | failed | stopped
│   ├── instance/{instance-id}/image          # actual image running
│   ├── instance/{instance-id}/ip             # assigned IP
│   ├── instance/{instance-id}/health         # healthy | unhealthy | unknown
│   ├── instance/{instance-id}/started_at
│   ├── volume/{name}/attached_node
│   └── volume/{name}/state                   # attached | detached | error
│
├── placement/                      # scheduling decisions (written by scheduler)
│   └── instance/{instance-id}      # value = node-id
│
├── endpoint/                       # derived network state (written by network controller)
│   └── service/{name}/{instance-id}  # value = {ip, port}
│
├── intent/                         # intent layers for multi-writer resolution
│   ├── user/service/{name}/instances
│   ├── autoscaler/service/{name}/instances
│   └── policy/service/{name}/instances
│
├── lease/                          # TTL leases for liveness
│   └── node/{node-id}             # etcd lease ID, expires if agent stops renewing
│
├── event/                          # append-only history (not authoritative)
│   └── {timestamp}-{sequence}     # value = {type, subject, detail}
│
├── auth/                           # permissions
│   └── {principal}/rules          # value = [{action, prefix_pattern}]
│
└── schema/                         # plugin fact type registrations
    └── {fact-type}                # value = {fields, types, constraints}
```

## Key Design Decisions

### 1. Desired / Observed Separation

The most important structural choice. These two trees are written by different actors with different trust levels:

| Tree | Writers | Trust |
|------|---------|-------|
| `desired/` | Users, policy controllers, autoscaler | Trusted — authenticated, authorized |
| `observed/` | Node agents | Partially trusted — can only report reality |
| `placement/` | Scheduler | Trusted — control plane component |
| `endpoint/` | Network controller | Trusted — derived from observed |
| `effective/` | Intent resolver | Trusted — computed from intent layers |

A compromised node agent can write to `observed/` but never to `desired/`.

### 2. Instance Identity

Instances get short random IDs (e.g., `a8f31`), not sequential numbers. They are internal bookkeeping, not user-facing objects.

```
/ccatler/observed/instance/a8f31/service  → "web"
/ccatler/observed/instance/a8f31/node     → "node-2"
/ccatler/observed/instance/a8f31/state    → "running"
```

### 3. Flat Facts, Not Nested Objects

Each fact is its own key. This enables:
- Fine-grained watches (`Watch("/ccatler/observed/instance/")` for all instance changes)
- Atomic updates to individual fields without read-modify-write on a blob
- Prefix scans to answer specific queries

### 4. Revisions & Optimistic Concurrency

etcd provides a global revision number. Every key mutation increments it. Transactions use revision-based compare-and-swap:

```
Transaction:
    IF   /ccatler/desired/service/web  mod_revision == 8172
    THEN PUT /ccatler/desired/service/web/instances = 5
    ELSE FAIL (re-read and retry)
```

This prevents two schedulers from double-placing an instance:

```
Transaction:
    IF   /ccatler/placement/instance/a8f31  create_revision == 0  (does not exist)
    THEN PUT /ccatler/placement/instance/a8f31 = "node-2"
    ELSE FAIL (already placed by another scheduler)
```

### 5. Leases for Node Liveness

Each node agent creates an etcd lease with a TTL (e.g., 30s) and continuously refreshes it.

```
Lease: id=abc123, ttl=30s
Key:   /ccatler/lease/node/node-1  →  lease_id=abc123
```

If the agent crashes or the network partitions, the lease expires. A watcher on `/ccatler/lease/node/` detects the expiry and sets:

```
/ccatler/observed/node/node-1/state → "unreachable"
```

This triggers the failure controller.

### 6. Watches by Prefix

Each controller watches only the prefixes it cares about:

| Controller | Watches |
|------------|---------|
| Instance controller | `desired/service/*/instances`, `observed/instance/` |
| Scheduler | `placement/`, `observed/node/`, `observed/instance/*/state` |
| Network controller | `observed/instance/*/state`, `observed/instance/*/ip` |
| Storage controller | `desired/volume/`, `observed/volume/`, `placement/` |
| Autoscaler | `observed/instance/*/health`, `desired/service/*/autoscale/` |
| Failure controller | `observed/node/*/state`, `lease/node/` |
| Intent resolver | `intent/*/service/` |

### 7. Intent Layer Resolution

When multiple writers can set `desired_instances`, each writes to its own intent prefix:

```
/ccatler/intent/user/service/web/instances        → 3
/ccatler/intent/autoscaler/service/web/instances   → 5
/ccatler/intent/policy/service/web/instances       → (not set)
```

The intent resolver watches all `/ccatler/intent/` changes and computes:

```
effective = clamp(autoscaler_value, user_min, user_max)
```

Writes the result to:

```
/ccatler/effective/service/web/instances → 5
```

The instance controller watches `effective/`, not `desired/` or `intent/`.

## Example: Full Lifecycle of `service web { instances 3 }`

### 1. User applies config

```
PUT /ccatler/desired/service/web              → {}
PUT /ccatler/desired/service/web/image        → "nginx:1.28"
PUT /ccatler/desired/service/web/instances    → 3
PUT /ccatler/desired/service/web/expose/8080  → {}
PUT /ccatler/intent/user/service/web/instances → 3
```

### 2. Intent resolver computes effective state

```
PUT /ccatler/effective/service/web/instances → 3
```

### 3. Instance controller sees desired=3, actual=0

Creates 3 instance placeholders:

```
TXN: IF effective/service/web mod_revision == current
PUT /ccatler/observed/instance/a8f31/service → "web"
PUT /ccatler/observed/instance/a8f31/state   → "pending"
PUT /ccatler/observed/instance/b72c9/service → "web"
PUT /ccatler/observed/instance/b72c9/state   → "pending"
PUT /ccatler/observed/instance/c913d/service → "web"
PUT /ccatler/observed/instance/c913d/state   → "pending"
```

### 4. Scheduler places instances

```
TXN: IF placement/instance/a8f31 create_revision == 0
PUT /ccatler/placement/instance/a8f31 → "node-1"

TXN: IF placement/instance/b72c9 create_revision == 0
PUT /ccatler/placement/instance/b72c9 → "node-2"

TXN: IF placement/instance/c913d create_revision == 0
PUT /ccatler/placement/instance/c913d → "node-3"
```

### 5. Node agents observe placements, start containers

Node-1 sees `/ccatler/placement/instance/a8f31 → "node-1"`, pulls nginx:1.28, starts container.

Reports back:

```
PUT /ccatler/observed/instance/a8f31/node       → "node-1"
PUT /ccatler/observed/instance/a8f31/state      → "running"
PUT /ccatler/observed/instance/a8f31/image      → "nginx:1.28"
PUT /ccatler/observed/instance/a8f31/ip         → "10.1.0.5"
PUT /ccatler/observed/instance/a8f31/health     → "healthy"
PUT /ccatler/observed/instance/a8f31/started_at → "2026-09-06T18:42:00Z"
```

### 6. Network controller derives endpoints

```
PUT /ccatler/endpoint/service/web/a8f31 → {"ip": "10.1.0.5", "port": 8080}
PUT /ccatler/endpoint/service/web/b72c9 → {"ip": "10.1.0.9", "port": 8080}
PUT /ccatler/endpoint/service/web/c913d → {"ip": "10.1.0.12", "port": 8080}
```

### 7. Service is live

DNS resolves `web` → `{10.1.0.5, 10.1.0.9, 10.1.0.12}`.

## Example: Node Failure

### Node-2 crashes

```
Lease /ccatler/lease/node/node-2 expires (TTL=30s, no refresh)
```

### Failure controller detects

```
PUT /ccatler/observed/node/node-2/state → "unreachable"
```

### Instance controller sees instance on dead node

```
b72c9 was on node-2, now unreachable
desired=3, healthy=2
→ create replacement instance d41ab, state=pending
```

### Scheduler places replacement

```
TXN: PUT /ccatler/placement/instance/d41ab → "node-3"
```

### Network controller updates endpoints

```
DELETE /ccatler/endpoint/service/web/b72c9
PUT    /ccatler/endpoint/service/web/d41ab → {"ip": "10.1.0.17", "port": 8080}
```

System converged. No component called another. All communication through state.

## Network Partition Behavior

### Partition: nodes can't reach etcd

- Nodes keep running workloads (cached desired state)
- Leases expire in etcd → control plane marks nodes unreachable
- Control plane may create replacement instances on reachable nodes
- When partition heals: agents re-report observed state, duplicates detected and reconciled
- Reconciliation must handle "instance running on two nodes" by preferring the newer placement and stopping the stale one

### Partition: etcd loses quorum

- etcd rejects all writes (safety)
- Controllers stall (no watches fire, no transactions succeed)
- Node agents keep running containers (last known state)
- When quorum restores: watches resume, controllers reconcile, system converges

### Design invariant

Temporary control-plane failure must never automatically destroy data-plane availability.
