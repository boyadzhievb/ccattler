# etcd Key Schema

Every key in the CCattler fact store lives under a top-level prefix that separates desired state from observed state. This boundary is the foundation of the security model and the reconciliation loop.

## Key hierarchy

```
/ccattler/
├── desired/                        # what the world should look like
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
│   ├── volume/{name}
│   ├── volume/{name}/size
│   ├── volume/{name}/persistent
│   └── volume/{name}/access_mode
│
├── effective/                      # derived desired state
│   └── service/{name}/instances    # target after user + autoscaler + policy
│
├── observed/                       # what the world actually looks like
│   ├── node/{node-id}
│   ├── node/{node-id}/state        # alive | unreachable | draining
│   ├── node/{node-id}/capacity/cpu
│   ├── node/{node-id}/capacity/memory
│   ├── node/{node-id}/available/cpu
│   ├── node/{node-id}/available/memory
│   ├── node/{node-id}/architecture
│   ├── node/{node-id}/zone
│   ├── instance/{instance-id}
│   ├── instance/{instance-id}/service
│   ├── instance/{instance-id}/node
│   ├── instance/{instance-id}/state  # pending | running | failed | stopped
│   ├── instance/{instance-id}/image
│   ├── instance/{instance-id}/ip
│   ├── instance/{instance-id}/health
│   ├── instance/{instance-id}/started_at
│   ├── volume/{name}/attached_node
│   └── volume/{name}/state           # attached | detached | error
│
├── placement/                      # scheduling decisions
│   └── instance/{instance-id}      # value = node-id
│
├── endpoint/                       # derived network state
│   └── service/{name}/{instance-id}  # value = {ip, port}
│
├── intent/                         # multi-writer resolution
│   ├── user/service/{name}/instances
│   ├── autoscaler/service/{name}/instances
│   └── policy/service/{name}/instances
│
├── lease/                          # TTL leases for liveness
│   └── node/{node-id}
│
├── event/                          # append-only history
│   └── {timestamp}-{sequence}
│
├── auth/                           # permissions
│   └── {principal}/rules
│
└── schema/                         # plugin fact type registrations
    └── {fact-type}
```

## Key design decisions

### Desired / observed separation

The most important structural choice. Different actors with different trust levels write to each tree:

| Tree | Writers | Trust level |
|---|---|---|
| `desired/` | Users, policy controllers, autoscaler | Trusted — authenticated, authorized |
| `observed/` | Node agents | Partially trusted — can only report reality |
| `placement/` | Scheduler | Trusted — control plane component |
| `endpoint/` | Network controller | Trusted — derived from observed |
| `effective/` | Intent resolver | Trusted — computed from intent layers |

A compromised node agent can write to `observed/` but never to `desired/`.

### Instance identity

Instances get short random IDs (e.g., `a8f31`), not sequential numbers:

```
observed/instance/a8f31/service  → "web"
observed/instance/a8f31/node     → "node-2"
observed/instance/a8f31/state    → "running"
```

### Flat facts, not nested objects

Each fact is its own key. This enables fine-grained watches, atomic field updates, and prefix scans.

### Revisions and optimistic concurrency

Every key mutation increments the global revision. Transactions use revision-based compare-and-swap to prevent conflicts.

### Leases for node liveness

Each node agent creates a lease with a TTL (30s) and continuously refreshes it. If the agent crashes, the lease expires and the failure controller detects it.

## Worked example: service lifecycle

### 1. User applies config

```
PUT desired/service/web              → {}
PUT desired/service/web/image        → "nginx:1.28"
PUT desired/service/web/instances    → 3
PUT desired/service/web/expose/8080  → {}
PUT intent/user/service/web/instances → 3
```

### 2. Intent resolver computes effective state

```
PUT effective/service/web/instances → 3
```

### 3. Instance controller: desired=3, actual=0

```
PUT observed/instance/a8f31/service → "web"
PUT observed/instance/a8f31/state   → "pending"
PUT observed/instance/b72c9/service → "web"
PUT observed/instance/b72c9/state   → "pending"
PUT observed/instance/c913d/service → "web"
PUT observed/instance/c913d/state   → "pending"
```

### 4. Scheduler places instances

```
PUT placement/instance/a8f31 → "node-1"
PUT placement/instance/b72c9 → "node-2"
PUT placement/instance/c913d → "node-3"
```

### 5. Node agents start containers and report

```
PUT observed/instance/a8f31/node       → "node-1"
PUT observed/instance/a8f31/state      → "running"
PUT observed/instance/a8f31/ip         → "10.1.0.5"
PUT observed/instance/a8f31/health     → "healthy"
```

### 6. Network controller derives endpoints

```
PUT endpoint/service/web/a8f31 → {"ip": "10.1.0.5", "port": 8080}
PUT endpoint/service/web/b72c9 → {"ip": "10.1.0.9", "port": 8080}
PUT endpoint/service/web/c913d → {"ip": "10.1.0.12", "port": 8080}
```

## Worked example: node failure

### Node-2 crashes

```
Lease lease/node/node-2 expires (no refresh for 30s)
```

### Failure controller detects

```
PUT observed/node/node-2/state → "unreachable"
```

### Instance controller creates replacement

```
PUT observed/instance/d41ab/service → "web"
PUT observed/instance/d41ab/state   → "pending"
```

### Scheduler places replacement

```
PUT placement/instance/d41ab → "node-3"
```

### Network controller updates endpoints

```
DELETE endpoint/service/web/b72c9
PUT    endpoint/service/web/d41ab → {"ip": "10.1.0.17", "port": 8080}
```

System converged. No component called another. All communication through state.

## Network partition behavior

### Nodes can't reach etcd

- Nodes keep running workloads (cached desired state)
- Leases expire → control plane marks nodes unreachable
- Control plane may create replacement instances on reachable nodes
- When partition heals: agents re-report, duplicates detected and reconciled

### etcd loses quorum

- etcd rejects all writes (safety)
- Controllers stall (no watches fire, no transactions succeed)
- Node agents keep running containers (last known state)
- When quorum restores: watches resume, controllers reconcile, system converges

**Design invariant:** temporary control-plane failure must never automatically destroy data-plane availability.
