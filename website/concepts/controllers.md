# Controllers

Controllers are the rule engines of CCattler. Each controller watches specific fact prefixes, compares desired state against observed state, and proposes changes to close the gap. Controllers never call each other — they communicate exclusively through the fact store.

## Instance controller

Maintains the correct number of running instances for each service.

```
desired=3, actual=1 → create 2 instances
desired=3, actual=5 → remove 2 instances
```

The controller doesn't know why the desired count is 3 — it could be a user declaration, an autoscaler recommendation, or a policy override.

## Endpoint controller

Derives network endpoints from running instances. When an instance transitions to `running` with a health state of `ready`, the endpoint controller creates an endpoint fact:

```
instance a8f31 (running, ready, ip=10.1.0.5, port=8080)
→ endpoint(web, a8f31, 10.1.0.5:8080)
```

Readiness-gated: only instances with `readiness=ready` (or no readiness probe configured) appear in endpoint sets.

## Failure controller

Detects failed instances and nodes, triggers replacements.

- **Instance failure** — if `instance.state = failed`, create a replacement
- **Node failure** — if `node.state = unreachable` (lease expired), all instances on that node need rescheduling

## Init controller

Manages initialization lifecycle for services with `init` blocks. Init steps run sequentially before the main workload starts:

```hcl
service api {
    init {
        exec "db-migrate --run"
        timeout 30s
        retry 3
    }
    init {
        exec "cache-warm"
        timeout 10s
    }
}
```

The controller tracks per-instance init phase: `pending → running → complete` (or `failed`). The main container starts only after all init steps succeed.

## Autoscaling engine

A unified engine with four input signals:

| Signal | Example | How it works |
|---|---|---|
| **CPU/memory** | `target cpu = 60%` | Scale when average exceeds threshold |
| **Custom metrics** | `target requests_per_second = 500` | Scale on application metrics |
| **Event-driven** | `event payments.pending = 20` | Queue depth / target = instances |
| **Scheduled** | `schedule { weekdays 08:00-18:00, minimum 10 }` | Time-based minimum |

Multiple metrics evaluated independently: `desired = max(cpu_rec, mem_rec, rps_rec, queue_rec, scheduled_min)`.

Stabilization windows prevent oscillation: scale-up stabilization = 60s, scale-down stabilization = 5m.

## Rolling update controller

When a service's image changes, the update controller orchestrates a gradual rollout:

```
desired: 10 × nginx:1.28
actual:  10 × nginx:1.27

update_strategy { max_unavailable 1, max_extra 1 }

→ start one 1.28 instance
→ wait until healthy
→ stop one 1.27 instance
→ repeat
```

If new instances fail health checks beyond a threshold, the controller reverts the desired image — automatic rollback.

## How controllers communicate

Controllers never import or call each other. Communication flows through state:

```
User applies config
  → desired/service/web/instances = 3
    → Instance controller creates 3 pending instances
      → Scheduler places them on nodes
        → Node agents start containers
          → Agents report observed state
            → Endpoint controller creates endpoints
              → DNS resolves service name to endpoints
```

Each step is a different controller reacting to state changes. If any controller crashes, the next resync picks up where it left off.
