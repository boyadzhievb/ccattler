# Service Dependency Graph Reference

Machine-readable function call index for how CCattler handles service-level
dependencies — implicit ordering, init connection probes, and DNS resolution.
Purpose: trace why a dependent service hasn't started without reading source.

## 1. Agent Startup Ordering (agent/agent.go)

### executeReconciliationCycle → sort by init steps
```
executeReconciliationCycle(ctx)                              :525
├── findInstancesPlacedOnThisNode(ctx)                       :661
│   └── store.Scan(placement/node/{nodeID}/)
│       → []placedInstanceInfo{id, service}
│
├── runtime.List(ctx) → runningByID map
│
├── sort.SliceStable(desired, ...)                           :561
│   └── FOR EACH pair:
│       ├── hasInitSteps(ctx, desired[left].service)         [init.go:402]
│       │   └── store.Scan(desired/service/{svc}/init/)
│       │       → true if any init step markers exist
│       ├── hasInitSteps(ctx, desired[right].service)
│       └── services WITHOUT init steps sort first
│           → infrastructure (databases, caches) starts before
│             services that need init connection probes
│
└── FOR EACH desired instance (in sorted order):
    ├── IF not running:
    │   └── reconcileDesiredInstance(ctx, instanceInfo)       :726
    └── IF running:
        └── observe + publish (see INSTANCE_LIFECYCLE_GRAPH)
```

### Ordering Guarantees and Limitations

| Guarantee | Explanation |
|---|---|
| No-init before init | `sort.SliceStable` puts services without init steps first |
| Same priority within group | Services with the same init status process in arbitrary order |
| **No cross-node ordering** | Agent on node-2 doesn't wait for agent on node-1 |
| **No multi-level ordering** | Only distinguishes "has init" vs "no init" — no A-before-B-before-C |
| **Sequential processing** | Instances process one at a time; if metadata-db init blocks, redis on the same node waits |

---

## 2. Init Connection Probes (agent/init.go)

### executeInitializationSteps → the blocking gate
```
executeInitializationSteps(ctx, instanceInfo, image)         :57
├── readCurrentInitPhase(ctx, instanceID)                    :289
│   └── store.Get(derived/instance/{id}/init/phase)
│       → "complete" → return true (skip all steps)
│
├── loadInitStepDefinitions(ctx, service)                    :232
│   └── store.Scan(desired/service/{svc}/init/)
│       → FOR EACH step index:
│           ├── store.Get(desired/service/{svc}/init/{n}/exec)
│           ├── store.Get(desired/service/{svc}/init/{n}/connection)
│           ├── store.Get(desired/service/{svc}/init/{n}/timeout)
│           └── store.Get(desired/service/{svc}/init/{n}/retry)
│               → connection default: 30 retries (connectionProbeDefaultRetry)
│
└── FOR EACH step (sequential, 0-indexed):
    └── executeInitStep(ctx, instanceInfo, step, image, N)   :96
        ├── readInitStepState(ctx, id, stepIndex)            :298
        │   ├── "succeeded" → skip to next step
        │   └── "running" → mark "failed" (agent restart recovery)
        │
        ├── check backoff:
        │   ├── attemptCount >= maxAttempts → PERMANENT FAIL
        │   │   → publishInitProgress with error
        │   │   → return false
        │   └── time.Now < nextRetryAt → BAIL (retry next cycle)
        │
        ├── publishInitProgress(ctx, id, step, total, ...)   :172
        │   ├── PUT observed/instance/{id}/init-progress = {JSON}
        │   └── PUT observed/instance/{id}/status-reason
        │
        ├── PUT observed/instance/{id}/init/step/{n}/state = "running"
        │
        ├── IF step.connection != "":
        │   └── runConnectionProbe(ctx, step, service)       :310
        │       [see §3 below]
        │
        ├── IF step.exec != "":
        │   └── runInitCommand(ctx, image, service, step)    :209
        │       ├── materializeInitConfigFiles(ctx, svc, spec) :419
        │       └── runtime.ExecInit(image, ExecSpec)
        │
        ├── SUCCESS:
        │   ├── PUT init/step/{n}/state = "succeeded"
        │   ├── delete backoff entry
        │   └── continue to next step
        │
        └── FAILURE:
            ├── increment backoff: 1s * 2^(attempt-1), cap 30s
            ├── publishInitProgress (with error)
            └── → return false (BAIL, retry next cycle)
```

### Init Step Store Keys
| Key Pattern | Written By | Purpose |
|---|---|---|
| `desired/service/{svc}/init/{n}` | cca apply | Step existence marker |
| `desired/service/{svc}/init/{n}/exec` | cca apply | Exec command |
| `desired/service/{svc}/init/{n}/connection` | cca apply | Connection name |
| `desired/service/{svc}/init/{n}/timeout` | cca apply | Step timeout |
| `desired/service/{svc}/init/{n}/retry` | cca apply | Max retry count |
| `observed/instance/{id}/init/step/{n}/state` | Agent | Step execution state |
| `observed/instance/{id}/init-progress` | Agent | JSON progress (step, attempt, error) |
| `derived/instance/{id}/init_phase` | InitController | Overall phase (pending/running/complete/failed) |

---

## 3. Connection Probe DNS Resolution (agent/init.go)

### runConnectionProbe → the TCP-level gate
```
runConnectionProbe(ctx, stepDefinition, serviceName)         :310
├── assembleConnectionURL(ctx, connectionName, service)      [agent.go:1488]
│   ├── store.Get(desired/connection/{name})
│   │   → connectionDefinition{host, port, driver, ...}
│   ├── parseConnectionJSON(jsonValue)                       [agent.go:1529]
│   └── → "host:port" as the dial target
│
├── resolveConnectionAddress(ctx, hostname, address)         :358
│   ├── IF hostname is IP literal:
│   │   └── use directly (no DNS)
│   │
│   └── ELSE (hostname is a service name):
│       ├── buildCCattlerResolver()                          :388
│       │   ├── IF advertiseAddress set:
│       │   │   └── custom net.Resolver
│       │   │       └── DialContext: UDP to advertiseAddress:53
│       │   │           → CCattler control plane DNS server
│       │   └── ELSE:
│       │       └── net.DefaultResolver (system DNS)
│       │
│       └── resolver.LookupHost(ctx, hostname)
│           └── CCattler DNS resolves from network/dns/{svc}
│               → IP from NetworkController VIP allocation
│               → first resolved IP replaces hostname
│
└── net.Dialer.DialContext("tcp", resolvedIP:port)
    ├── SUCCESS → return nil
    └── FAILURE → return error (backoff + retry)
```

### DNS Resolution Chain
```
connection "airflow-db" { host "metadata-db"; port 5432 }
                │
                ▼
resolveConnectionAddress(ctx, "metadata-db", "metadata-db:5432")
                │
                ▼
buildCCattlerResolver() → custom resolver
                │
                ▼
UDP query "metadata-db" → advertiseAddress:53
                │
                ▼
Control plane DNS server reads network/dns/metadata-db
                │
                ▼
NetworkController wrote: network/dns/metadata-db = "10.200.0.5"
                │
                ▼
net.Dialer.DialContext("tcp", "10.200.0.5:5432")
```

### DNS Resolution Latency

The DNS record exists only after this sequence completes:
1. metadata-db instance starts and gets an IP (`observed/instance/{id}/ip`)
2. EndpointController creates `endpoint/service/metadata-db/{id}/{port}`
3. NetworkController allocates VIP and writes `network/dns/metadata-db`
4. DNS forwarder on worker nodes picks up the record

Typical latency: 2-3 reconcile cycles (150-300ms) from instance running to DNS available.

---

## 4. Connection File Mount (agent/agent.go)

### materializeConnectionFiles → file mount (no startup gate)
```
materializeConnectionFiles(ctx, serviceName, runtimeSpec)    :1463
├── store.Scan(desired/service/{svc}/connection/)
│   → {connectionName: mountPath} pairs
│
└── FOR EACH connection mount:
    ├── assembleConnectionURL(ctx, connectionName, service)  :1488
    │   └── builds full URL from connection definition
    │       e.g. "postgresql://user:pass@metadata-db:5432/airflow"
    │
    └── append to runtimeSpec.ConfigFiles:
        └── {path: mountPath, content: connectionURL}
```

This only mounts the connection URL into the container filesystem.
It does **not** gate startup — the container starts regardless of whether
the target service is reachable.

---

## 5. Implicit Dependency Mechanisms

### Ranked by enforcement strength

| Mechanism | Enforcement | Store Keys | Source |
|---|---|---|---|
| `init { connection X }` | **Hard runtime gate** — TCP dial must succeed | `desired/service/{svc}/init/{n}/connection` | init.go:310 |
| `init { exec "cmd" }` | **Hard runtime gate** — command must exit 0 | `desired/service/{svc}/init/{n}/exec` | init.go:209 |
| `connection X "/path"` | **No gate** — file mounted, container starts | `desired/service/{svc}/connection/{name}` | agent.go:1463 |
| env var hostname refs | **No gate** — application handles retries | `desired/service/{svc}/config/env/{key}` | agent.go:1222 |

### Example Dependency Graph (Airflow workload)
```
                    ┌──────────────┐
                    │  metadata-db │  (postgres:16)
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
        └───────────┘ └─────────┘ └───────────┘
              │            │            │
         env var       env var     env var
         (no gate)    (no gate)   (no gate)
              │            │            │
              ▼            ▼            ▼
        ┌─────────────────────────────────────┐
        │              redis                  │
        └─────────────────────────────────────┘

Enforced:      metadata-db → scheduler, metadata-db → worker
Not enforced:  redis → *, metadata-db → webserver
```

---

## 6. Controller-Side Topology (controllers/topological_sort.go)

### SortControllersByDependency (different scope from service deps)
```
SortControllersByDependency(controllers)                     :164
├── buildControllerDependencyGraph(controllers)              :116
│   ├── controllerOutputPrefixes()                           :86
│   └── FOR EACH (consumer, producer) pair:
│       └── hasOverlappingPrefix(watched, outputs)           :142
│
└── topologicalSort(names, edges)                            :19
    └── Kahn's algorithm
        → orders CONTROLLER execution, NOT service startup
```

This orders which controllers run first within a single runner cycle.
It has **no relation** to service dependencies — it ensures
InstanceController runs before Scheduler, not that metadata-db starts
before scheduler.

---

## 7. Fresh Deploy Convergence Timeline

For the Airflow workload across 3 nodes:

```
TIME    EVENT                                     NODE
─────   ─────────────────────────────────────────  ────
 t0     cca apply airflow.cca                      user
 t1     InstanceController creates instances        server
 t2     Scheduler places instances across nodes     server

─── All agents see their placements simultaneously ───

 t3     ctrl agent: sort → metadata-db first       ctrl
        (no init steps → processed before scheduler)
 t3     worker-1: sort → webserver first           worker-1
        (no init steps on webserver)
 t3     worker-2: sort → worker first              worker-2
        (has init, but only service on this node)

 t4     ctrl: metadata-db starts (no init)         ctrl
        ctrl: redis starts (no init)
 t4     worker-1: webserver starts immediately     worker-1
        worker-1: scheduler init → connection probe
        → DNS lookup "metadata-db" → FAIL (not registered yet)
        → backoff 1s
 t4     worker-2: worker init → connection probe   worker-2
        → FAIL → backoff 1s

 t5     ctrl: metadata-db state=running            ctrl
        → observed/instance/{id}/ip published

 t6     EndpointController → endpoint created       server
        NetworkController → network/dns/metadata-db server

 t7     DNS forwarder on worker-1/2 resolves       worker-1,2

 t8     scheduler init retry → TCP SUCCESS         worker-1
        exec "airflow db check" → runs migration
 t8     worker init retry → TCP SUCCESS            worker-2
        exec "airflow db check" → runs check

 t9     webserver already running but crash-loops  worker-1
        (no init gate for DB → application-level failure)

 t10    scheduler init complete → starts           worker-1
 t10    worker init complete → starts              worker-2

 t11    startup + readiness probes pass            all
        endpoints created, data plane configured

─── CONVERGED: ~5-10 minutes total ───
```

---

## 8. Init Step Backoff Detail

### Per-Step Backoff (agent/init.go:22-32)
| Parameter | Value | Constant |
|---|---|---|
| Base delay | 1s | `initStepBackoffBaseDelay` |
| Max delay | 30s | `initStepMaxBackoffDelay` |
| Formula | `1s * 2^(attemptCount-1)`, capped at 30s |
| Delays | 1s, 2s, 4s, 8s, 16s, 30s, 30s... |

### Connection Probe Defaults
| Parameter | Value | Constant |
|---|---|---|
| Default retries | 30 | `connectionProbeDefaultRetry` |
| Max convergence time | ~15min (30 retries × 30s cap) |

### Exec Step Defaults
| Parameter | Value |
|---|---|
| Default retries | 0 (no retries) |
| Behavior on failure | Step state = "failed", init phase = "failed" |

---

## 9. Gap Analysis: What's Missing

| Gap | Impact | Severity |
|---|---|---|
| No `depends_on` in DSL | Dependencies only via init connection probes | High |
| No dependency-aware scheduling | Scheduler ignores service relationships | Medium |
| No "waiting for dependency X" status | Status shows raw TCP error, not service dependency | High |
| No cycle detection at service level | A depends on B depends on A → both fail after max retries | Medium |
| No dependency-aware restart ordering | Dependents discover outage via probe failure (minutes) | Low |
| Agent sort is single-level | Only has-init vs no-init, no multi-level ordering | Low |
| Init backoff is per-instance | No shared awareness that a target is globally down | Low |

---

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `hasInitSteps` | executeReconciliationCycle (sort comparator) |
| `loadInitStepDefinitions` | executeInitializationSteps |
| `executeInitializationSteps` | reconcileDesiredInstance (if hasInitSteps) |
| `executeInitStep` | executeInitializationSteps (per step loop) |
| `runConnectionProbe` | executeInitStep (connection type) |
| `runInitCommand` | executeInitStep (exec type) |
| `resolveConnectionAddress` | runConnectionProbe |
| `buildCCattlerResolver` | resolveConnectionAddress |
| `assembleConnectionURL` | runConnectionProbe, materializeConnectionFiles |
| `parseConnectionJSON` | assembleConnectionURL |
| `publishInitProgress` | executeInitStep (on attempt and on failure) |
| `clearInitProgress` | executeInitializationSteps (on all steps passed) |
| `readCurrentInitPhase` | executeInitializationSteps |
| `readInitStepState` | executeInitStep |
| `materializeConnectionFiles` | reconcileDesiredInstance (after init, before start) |
| `materializeInitConfigFiles` | runInitCommand |
| `SortControllersByDependency` | Runner.Run (via startControllersWithLeaderElection) |
| `topologicalSort` | SortControllersByDependency |
