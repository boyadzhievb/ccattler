# Facts & State

Everything in CCattler is a fact — a key-value pair stored in etcd. Facts are not mutable objects with methods. They are observations: "service web should have 3 instances", "instance a8f31 is running on node-2", "node-1 has 4 CPUs available".

## Three state namespaces

The most important structural choice in CCattler is the separation of desired and observed state:

| Namespace | Who writes | What it contains |
|---|---|---|
| `desired/` | Users, policy controllers, autoscaler | What the world should look like |
| `observed/` | Node agents | What the world actually looks like |
| `effective/` | Intent resolver | Computed desired state after policy |
| `placement/` | Scheduler | Where instances should run |
| `endpoint/` | Network controller | Derived network state |

A compromised node agent can write to `observed/` but never to `desired/`. This boundary is the foundation of the security model.

## Core fact types

| Fact | Fields | Description |
|---|---|---|
| `service` | name, image, instances, port | A workload definition |
| `instance` | id, service, node, state | One running unit of a service |
| `node` | id, cpu, memory, state | A machine in the cluster |
| `endpoint` | service, instance, address, port | Network reachability |
| `volume` | name, size, persistent | Storage |
| `placement` | instance, node | Scheduling decision |
| `config` | service, key, value, type | Environment variable or file |
| `secret` | name | Secret exists (value in encrypted store) |
| `secret_grant` | service, secret_name | Service authorized to access secret |
| `probe` | service, type, method, interval | Health check configuration |
| `probe_state` | instance, type, state | Observed health result |
| `node_label` | node, label, value | Key-value label for placement |

## Intent layers

Multiple writers can contribute facts without fighting over the same object. Each writes to its own intent prefix:

```
intent/user/service/web/instances        → 3      (user declared min=3)
intent/autoscaler/service/web/instances  → 5      (autoscaler recommends 5)
```

The intent resolver computes the effective value:

```
effective/service/web/instances → 5
```

The instance controller watches `effective/`, not `desired/` or `intent/`. It doesn't know or care *why* the desired count is 5.

## Flat facts, not nested objects

Each fact is its own key. This enables:

- **Fine-grained watches** — `Watch("observed/instance/")` for all instance changes
- **Atomic field updates** — change one field without read-modify-write on a blob
- **Prefix scans** — query `desired/service/web/` to get all facts about the web service

Example keys for a running instance:

```
observed/instance/a8f31/service    → "web"
observed/instance/a8f31/node       → "node-2"
observed/instance/a8f31/state      → "running"
observed/instance/a8f31/image      → "nginx:1.28"
observed/instance/a8f31/ip         → "10.1.0.5"
observed/instance/a8f31/health     → "healthy"
```

## Revisions and optimistic concurrency

etcd provides a global revision number. Every mutation increments it. Transactions use revision-based compare-and-swap:

```
Transaction:
    IF   desired/service/web  mod_revision == 8172
    THEN PUT desired/service/web/instances = 5
    ELSE FAIL (re-read and retry)
```

This prevents conflicts — two schedulers can't double-place an instance:

```
Transaction:
    IF   placement/instance/a8f31  create_revision == 0  (does not exist)
    THEN PUT placement/instance/a8f31 = "node-2"
    ELSE FAIL (already placed)
```

## Leases for node liveness

Each node agent creates an etcd lease (TTL 30s) and continuously refreshes it. If the agent crashes:

1. Lease expires
2. Failure controller detects it
3. Node marked as `unreachable`
4. Instances on that node get rescheduled

No explicit health checks between control plane and nodes — the lease mechanism handles liveness.
