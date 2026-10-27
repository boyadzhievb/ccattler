# Service Dependency Graph Reference

How CCattler currently handles service-level dependencies (implicit), what's
missing, and how explicit `depends_on` would fit the fact-based architecture.

---

## 1. Current Implicit Dependency Mechanisms

CCattler has no `depends_on` keyword. Service dependencies are expressed
indirectly through three mechanisms, listed from strongest to weakest:

### 1a. Connection init probes (strongest signal)

An init step with `connection <name>` causes the agent to TCP-dial the
connection's host:port before starting the main container. This creates a
hard runtime dependency: the service cannot start until the target is
reachable.

```
# From airflow.cca — scheduler depends on metadata-db
service scheduler {
    init {
        connection airflow-db        ← blocks until metadata-db:5432 reachable
    }
    init {
        exec "airflow db check"      ← blocks until db schema accessible
    }
}

connection airflow-db {
    host metadata-db                 ← DNS name of the metadata-db service
    port 5432
}
```

**Store keys involved:**

```
desired/service/scheduler/init/0/connection  = "airflow-db"
desired/connection/airflow-db                = {"driver":"...","host":"metadata-db","port":5432,...}
```

**Runtime behavior** (`agent/init.go`):

1. `loadInitStepDefinitions` reads `desired/service/{svc}/init/{n}/*`
2. `executeInitStep` calls `runConnectionProbe` for connection-type steps
3. `runConnectionProbe` → `resolveConnectionAddress` → `buildCCattlerResolver`
   → DNS lookup via CCattler DNS → TCP dial to host:port
4. On failure: backoff (1s→30s exponential), retry up to 30 times (default)
5. On permanent failure: writes `InitStepFailed`, instance never starts

**Implicit graph from airflow.cca:**

```
metadata-db ←── airflow-db connection ──→ scheduler
metadata-db ←── airflow-db connection ──→ worker
redis       ←── (env var, no init)    ──→ webserver
redis       ←── (env var, no init)    ──→ scheduler
redis       ←── (env var, no init)    ──→ worker
metadata-db ←── (env var, no init)    ──→ webserver
```

Only the `init { connection }` arrows are enforced at runtime. The env var
references (`AIRFLOW__CELERY__BROKER_URL "redis://redis:6379/0"`) are not
checked — the container starts and fails at application level.

### 1b. Service-level connection references (file mount)

```
service webserver {
    connection airflow-db "/run/connections/airflow-db"
}
```

This mounts the assembled connection URL into the container at the specified
path but does **not** gate startup. The webserver starts regardless of whether
metadata-db is reachable. The dependency is only enforced if there is also an
`init { connection airflow-db }` block.

### 1c. Environment variable hostname references (no enforcement)

```
config {
    env DB_SERVER_HOST postgres
}
```

The Zabbix workload (`test/e2e/workloads/zabbix.cca`) uses this pattern —
env vars reference other service names, but nothing in CCattler validates
or gates on these. The service starts immediately and the application itself
handles connection retries.

---

## 2. Current Startup Ordering

### Agent-side sort (`agent/agent.go:551-563`)

```go
sort.SliceStable(desired, func(leftIndex, rightIndex int) bool {
    leftHasInit := nodeAgent.hasInitSteps(ctx, desired[leftIndex].service)
    rightHasInit := nodeAgent.hasInitSteps(ctx, desired[rightIndex].service)
    if leftHasInit != rightHasInit {
        return !leftHasInit   // services WITHOUT init steps go first
    }
    return false
})
```

This is the **only ordering mechanism**. It guarantees:

- Services without init steps (typically infrastructure: databases, caches)
  start before services with init steps (typically application services)

It does **not** guarantee:

- Any ordering among init-less services (metadata-db vs redis — arbitrary)
- Any ordering among init-having services (scheduler vs worker — arbitrary)
- Cross-node ordering (if metadata-db is on node-1, worker on node-2, the
  worker agent doesn't wait for metadata-db to be running on node-1)

### Controller-side topological sort (different scope)

`controllers/topological_sort.go` uses Kahn's algorithm to order **controller
execution** based on watch/output prefix overlap. This has no relation to
service dependencies — it orders which controllers run in which sequence
on the server side (e.g. InstanceController before Scheduler before
EndpointController).

---

## 3. Fresh Deploy Convergence Timeline

For the Airflow workload across 3 nodes, with the current implicit mechanism:

```
TIME    EVENT                                     NODE
─────   ─────────────────────────────────────────  ────
 t0     cca apply airflow.cca                      user
 t1     InstanceController creates instances        server
 t2     Scheduler places instances across nodes     server
        placement/node/ctrl/metadata-db-xxx
        placement/node/ctrl/redis-xxx
        placement/node/worker-1/scheduler-xxx
        placement/node/worker-1/webserver-xxx
        placement/node/worker-2/worker-xxx-1
        placement/node/worker-2/worker-xxx-2

─── All agents see their placements simultaneously ───

 t3     ctrl agent: sort → metadata-db first       ctrl
        ctrl agent: pull postgres:16 image
 t3     worker-1 agent: sort → webserver first     worker-1
        (no init steps on webserver)
        worker-1 agent: pull airflow:2.10.5
 t3     worker-2 agent: sort → worker first        worker-2
        worker-2 agent: pull airflow:2.10.5

 t4     ctrl: metadata-db starts (no init steps)   ctrl
        ctrl: redis starts (no init steps)
 t4     worker-1: webserver starts                 worker-1
        worker-1: scheduler init → connection      worker-1
        probe airflow-db (metadata-db:5432)
        → DNS lookup "metadata-db"
        → FAIL: DNS not registered yet!
        → backoff 1s
 t4     worker-2: worker init → connection         worker-2
        probe airflow-db
        → FAIL: DNS not registered yet!
        → backoff 1s

 t5     ctrl: metadata-db state → running          ctrl
        ctrl agent writes observed/instance/{id}/ip

 t6     StatefulDNSController sees IP              server
        writes derived/dns/metadata-db → IP

 t7     DNS forwarder on worker-1/2 can now        worker-1,2
        resolve "metadata-db"

 t8     worker-1: scheduler init retry → TCP       worker-1
        dial metadata-db:5432 → SUCCESS
        scheduler init step 2: "airflow db check"
        → runs migration, takes ~60s

 t8     worker-2: worker init retry → TCP          worker-2
        dial metadata-db:5432 → SUCCESS
        worker init step 2: "airflow db check"
        → runs check, takes ~10s

 t9     webserver already running but can't         worker-1
        connect to DB (no init gate!)
        → application-level crash-loop until
        metadata-db and scheduler complete migration

 t10    scheduler init complete → starts            worker-1
        scheduler startup probe begins (60s delay)

 t10    worker init complete → starts               worker-2
        worker startup probes begin (60s delay)

 t11    startup probes pass                         all
        readiness probes pass
        endpoints created
        data plane configured

─── CONVERGED: ~5-10 minutes total ───
```

### Key observations

1. **DNS registration latency** (t5→t7): 2-3 reconcile cycles for the IP to
   propagate through StatefulDNSController to the DNS forwarder. Connection
   probes fail during this window.

2. **No cross-node coordination**: worker-2's agent doesn't know metadata-db
   is on ctrl. It just retries the connection probe until DNS works.

3. **Unprotected services**: webserver has no `init { connection }` block,
   so it starts immediately and crash-loops until the database is ready.
   This is a deployment authoring bug, not a CCattler bug, but an explicit
   dependency graph would catch it.

4. **Parallel init on same node**: If scheduler and webserver are both on
   worker-1, scheduler's init blocks the reconcile loop (sequential per
   instance in `executeReconciliationCycle`), delaying webserver's
   steady-state observation.

---

## 4. Gap Analysis

### What's missing vs. explicit dependency graph support

| Gap | Impact | Severity |
|-----|--------|----------|
| **No `depends_on` in DSL** | Dependencies only expressible via init connection probes. Services that reference hostnames via env vars have no enforcement. | High |
| **No dependency-aware scheduling** | Scheduler places instances without knowing service B needs service A. Can't co-locate or sequence across nodes. | Medium |
| **No "waiting for dependency X" status reason** | When a connection probe fails, the status shows `init:0 connection:airflow-db attempt 6/31: TCP dial failed`. It doesn't say "waiting for service metadata-db to become ready". | High (M150) |
| **No cycle detection at service level** | If service A depends on B and B depends on A, both will retry connection probes until max retries, then permanently fail. No up-front validation. | Medium |
| **No dependency-aware restart ordering** | If metadata-db restarts, dependent services aren't notified. They discover the outage via liveness probe failure (minutes later), not dependency awareness (immediate). | Low |
| **No dependency graph visualization** | `cca get services` shows a flat list. No way to see "scheduler → metadata-db → postgres" chain. | Medium |
| **Agent sort is single-level** | Sort only distinguishes "has init" vs "no init". Can't express "service A must start before service B, which must start before service C". | Low |
| **Init backoff is per-instance, not dependency-aware** | If metadata-db is known to be down, all dependent services independently retry instead of pausing. Wastes probes and log noise. | Low |

### What connection probes get right

- **Fact-based**: Probe results are stored as facts, controllers react
- **Non-blocking reconcile**: Backoff tracked across cycles, loop never blocks
- **Self-healing**: Probes eventually succeed when dependency comes up
- **Observable**: `publishInitProgress` writes JSON progress to store

---

## 5. Proposed `depends_on` Design (Future M151+)

Reference: `.claude/agents/initialisation-startupdependencies.md`

### DSL syntax

```
service webserver {
    depends_on metadata-db {
        condition ready          # wait until metadata-db has readiness=ready
    }
    depends_on redis {
        condition running        # wait until redis state=running (weaker)
    }
}
```

### Fact store keys

```
# Desired (written by cca apply)
desired/service/webserver/depends_on/metadata-db  = "ready"
desired/service/webserver/depends_on/redis         = "running"

# Derived (written by DependencyController)
derived/service/webserver/dependency/metadata-db   = "satisfied"    # or "waiting"
derived/service/webserver/dependency/redis          = "satisfied"
derived/service/webserver/dependencies_met          = "true"        # all satisfied
```

### New controller: DependencyController

Watches: `desired/service/*/depends_on/`, `observed/instance/*/state`,
`observed/instance/*/health`, `observed/instance/*/probe/readiness`

Reconcile logic:

```
for each service with depends_on:
    for each dependency:
        check if target service has at least one instance matching condition
        write derived/service/{svc}/dependency/{target} = satisfied|waiting
    if all satisfied:
        write derived/service/{svc}/dependencies_met = true
    else:
        write derived/service/{svc}/dependencies_met = false
```

### Agent changes

Before calling `reconcileDesiredInstance`, the agent checks:

```go
dependenciesMet := nodeAgent.readDependenciesMet(ctx, instanceInfo.service)
if dependenciesMet == "false" {
    nodeAgent.publishStatusReason(ctx, instanceInfo.id,
        "waiting for dependency: " + firstUnmetDependency)
    return
}
```

### CLI changes

```
$ cca get services
NAME          INSTANCES   READY   DEPENDENCIES
metadata-db   1/1         1/1     (none)
redis         1/1         1/1     (none)
webserver     0/1         0/1     metadata-db:waiting, redis:satisfied
scheduler     1/1         0/1     metadata-db:satisfied
worker        2/2         0/2     metadata-db:satisfied

$ cca describe service webserver
...
Dependencies:
  metadata-db   waiting    (0/1 instances ready)
  redis         satisfied  (1/1 instances running)
```

### Cycle detection

At `cca apply` time (DSL validation), build a directed graph from
`depends_on` edges and run Kahn's algorithm (reuse existing
`topologicalSort` from `controllers/topological_sort.go`). Reject
the apply if cycles are detected:

```
$ cca apply broken.cca
Error: service dependency cycle detected: webserver → scheduler → webserver
```

### Interaction with init connection probes

`depends_on` and `init { connection }` serve different purposes:

- `depends_on` gates at the **scheduling/placement level** — don't even
  start reconciling the service until dependencies are met
- `init { connection }` gates at the **runtime level** — service is being
  reconciled but the main container waits for TCP reachability

Both can coexist. `depends_on` prevents unnecessary init retries, while
init connection probes provide the final TCP-level verification.

---

## Appendix: Implicit Dependency Graph for Airflow Workload

```
                    ┌──────────────┐
                    │  metadata-db │  (postgres:16)
                    │  expose:5432 │
                    └──────┬───────┘
                           │
              ┌────────────┼────────────┐
              │            │            │
         connection   connection    env var
         init probe   init probe    (no gate)
              │            │            │
              ▼            ▼            ▼
        ┌───────────┐ ┌─────────┐ ┌───────────┐
        │ scheduler │ │ worker  │ │ webserver  │
        │ init:     │ │ init:   │ │ (no init)  │
        │  0:conn   │ │  0:conn │ │            │
        │  1:exec   │ │  1:exec │ │            │
        └───────────┘ └─────────┘ └───────────┘
              │            │            │
              │            │            │
         env var       env var     env var
         (no gate)    (no gate)   (no gate)
              │            │            │
              ▼            ▼            ▼
        ┌─────────────────────────────────────┐
        │              redis                  │
        │           expose:6379               │
        └─────────────────────────────────────┘

Enforced (init connection):   metadata-db → scheduler, metadata-db → worker
Not enforced (env var only):  redis → scheduler, redis → worker, redis → webserver
Not enforced (env var only):  metadata-db → webserver
```

Legend:
- Solid arrows from connection init probes: **enforced** — target must be
  TCP-reachable before service starts
- Env var references: **not enforced** — application handles its own retries
