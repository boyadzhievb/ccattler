# CCattler Observability Graph Reference

Machine-readable reference for all observability subsystems: metrics, tracing,
structured logging, health probes, node heartbeats, failure detection, and
resource telemetry. Purpose: trace how observability data flows without reading
source files.

## Observability Architecture Overview

```
┌────────────────────────────────────────────────────────────────────────────────┐
│                           CONTROL PLANE (server)                               │
│                                                                                │
│  ┌──────────────┐    ┌───────────────────┐    ┌──────────────────────────────┐ │
│  │ API Server   │    │ MetricsCollector   │    │ Controllers (reconcile loop) │ │
│  │              │    │ (controllers/      │    │                              │ │
│  │ /metrics ────┼───▶│  metrics.go)       │◀───│ RecordReconciliation()       │ │
│  │ /healthz     │    │                    │    │ RecordSchedulingDecision()   │ │
│  │ /api/status  │    │ Per-controller:    │    │ RecordInstanceTransition()   │ │
│  │ /api/watch   │    │ cycles,errors,     │    │                              │ │
│  │ /api/logs    │    │ changes,duration   │    │ NodeFailureController        │ │
│  │ /api/events/ │    └───────────────────┘    │  ↓ reads lease/ timestamps   │ │
│  │   stream     │                              │  ↓ marks nodes unreachable   │ │
│  └──────┬───────┘    ┌───────────────────┐    └──────────────────────────────┘ │
│         │            │ Prometheus        │                                      │
│         │            │ Registry          │                                      │
│         └───────────▶│ (metrics/         │                                      │
│    on /metrics       │  metrics.go)      │                                      │
│    scrape: snapshot  │                   │                                      │
│    cluster state     │ Counters, Gauges, │                                      │
│    into gauges       │ Histograms        │                                      │
│                      └───────────────────┘                                      │
└────────────────────────────────────────────────────────────────────────────────┘
         ▲                                          ▲
         │ heartbeat (lease/node/{id})               │ probe results
         │ telemetry (observed/telemetry/...)         │ (observed/instance/{id}/probe/...)
         │ node state (observed/node/{id}/state)      │
         │                                           │
┌────────┴───────────────────────────────────────────┴──────────────────────────┐
│                            NODE AGENT (per node)                               │
│                                                                                │
│  ┌──────────────────┐  ┌──────────────────┐  ┌─────────────────────────────┐  │
│  │ HeartbeatLoop     │  │ NodeReporter     │  │ ProbeScheduler              │  │
│  │ (agent/agent.go   │  │ (agent/          │  │ (agent/probes.go)           │  │
│  │  :485)            │  │  telemetry.go)   │  │                             │  │
│  │                   │  │                  │  │ Three probe types:          │  │
│  │ Writes lease/     │  │ Writes:          │  │  ├─ startup  (gate)         │  │
│  │ node/{id} every   │  │  workload count  │  │  ├─ liveness (drain/replace)│  │
│  │ 5s independently  │  │  per-instance    │  │  └─ readiness (endpoints)   │  │
│  │ of reconcile loop │  │  CPU, memory,    │  │                             │  │
│  │                   │  │  OOM kills       │  │ Three probe methods:        │  │
│  │ Decoupled from    │  │                  │  │  ├─ HTTP GET (2xx-3xx)      │  │
│  │ reconciliation so │  │ Also: node       │  │  ├─ TCP dial                │  │
│  │ slow cycles don't │  │ registration,    │  │  └─ exec (in container)     │  │
│  │ cause false       │  │ alive state,     │  │                             │  │
│  │ failure detection │  │ advertise addr   │  │ Legacy HealthCheck also runs│  │
│  └──────────────────┘  └──────────────────┘  └─────────────────────────────┘  │
│                                                                                │
│  ┌──────────────────┐                                                          │
│  │ Structured Logger │                                                          │
│  │ (logging/         │                                                          │
│  │  logging.go)      │                                                          │
│  │ Human or JSON     │                                                          │
│  │ format to stderr  │                                                          │
│  └──────────────────┘                                                          │
└────────────────────────────────────────────────────────────────────────────────┘
```

---

## 1. Metrics Pipeline

### Prometheus Registry (`metrics/metrics.go`)

Custom, dependency-free Prometheus-compatible metrics library. Three metric
types, all lock-free hot path (atomic operations), locks only on first-touch
label creation.

| Type | Struct | Hot-path Ops | File:Line |
|------|--------|-------------|-----------|
| Counter | `Counter` | `Inc()`, `Add()` | `metrics/metrics.go:42` |
| Gauge | `Gauge` | `Set()`, `Inc()`, `Dec()` | `metrics/metrics.go:51` |
| Histogram | `Histogram` | `Observe()`, `ObserveDuration()`, `ObserveSince()` | `metrics/metrics.go:59` |

**Default registry**: `metrics.DefaultRegistry` (process-wide singleton, `metrics/metrics.go:39`)

**Duration buckets** (`DurationBuckets()`, `:400`):
```
0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0 seconds
```

**Exposition**: `Registry.Handler()` returns an `http.Handler` serving
Prometheus text exposition format (`text/plain; version=0.0.4`).

### Registered Prometheus Metrics (`api/server.go:32-61`)

| Metric Name | Type | Labels | Description |
|---|---|---|---|
| `ccattler_api_requests_total` | Counter | `endpoint`, `method`, `status` | Total API requests |
| `ccattler_api_request_duration_seconds` | Histogram | `endpoint` | API request latency (DurationBuckets) |
| `ccattler_active_watches` | Gauge | — | Active SSE watch/event stream connections |
| `ccattler_instances` | Gauge | `state` | Current instance count by state |
| `ccattler_services` | Gauge | — | Total registered services |
| `ccattler_nodes` | Gauge | — | Total registered nodes |

**Snapshot on scrape**: `handleMetrics()` (`api/server.go:398`) rebuilds
`instancesByState`, `serviceCount`, `nodeCount` from a live store scan
before each `/metrics` response, ensuring scrapers see current values.

### API Endpoint

```
GET /metrics
  Content-Type: text/plain; version=0.0.4
  Rate-limit exempt (api/rate_limiter.go:72)
```

---

## 2. MetricsCollector (Control Plane)

In-memory operational metrics for the controller runner. Not Prometheus — these
power `cca status` and internal monitoring.

**File**: `controllers/metrics.go`

### Struct: `MetricsCollector`

```
MetricsCollector
  ├── reconciliationMetrics  map[string]*ReconciliationMetrics  (per controller)
  ├── schedulingDecisions    atomic.Int64                       (global counter)
  └── instanceTransitions    map[string]*atomic.Int64           (by transition type)
```

### ReconciliationMetrics (per controller)

| Field | Type | Description |
|---|---|---|
| `TotalCycles` | `atomic.Int64` | Total reconciliation cycles completed |
| `TotalErrors` | `atomic.Int64` | Cycles that returned an error |
| `TotalChanges` | `atomic.Int64` | Cumulative changes produced |
| `LastDuration` | `atomic.Int64` | Wall-clock time of most recent cycle (μs) |
| `MaxDuration` | `atomic.Int64` | Longest reconciliation cycle observed (μs) |
| `TotalDuration` | `atomic.Int64` | Sum of all cycle durations (μs) |

**Recording API** (called by the HARunner after each reconciliation):

| Method | File:Line | Called By |
|---|---|---|
| `RecordReconciliation(name, duration, changeCount, err)` | `:52` | Controller runner after each cycle |
| `RecordSchedulingDecision()` | `:77` | Scheduler on each placement |
| `RecordInstanceTransition(transition)` | `:83` | Instance state changes |

**Read API** (called by status/top commands):

| Method | Returns |
|---|---|
| `GetReconciliationMetrics(name)` | Single controller's `ReconciliationMetrics` |
| `GetAllReconciliationStats()` | `map[string]ReconciliationSnapshot` |
| `GetSchedulingDecisions()` | `int64` total decisions |
| `GetInstanceTransitions(transition)` | `int64` for a specific transition |
| `GetAllTransitions()` | `map[string]int64` snapshot |

**Wiring**: Created in `cmd/cca/command_server.go:382`, passed to
`controllers.NewHARunner()` at `:396`, accessible via `haRunner.Metrics()`.

---

## 3. Distributed Tracing

### W3C Trace Context (`tracing/trace_context.go`)

Implements W3C Trace Context propagation (version `00`).

```
traceparent: 00-{traceID 32hex}-{spanID 16hex}-{flags 2hex}
                └── 16 bytes     └── 8 bytes    └── 01=sampled
```

| Type / Func | Description | File:Line |
|---|---|---|
| `TraceContext` struct | Carries TraceID, SpanID, ParentID, Sampled | `:24` |
| `NewTraceContext()` | New root trace (random IDs, sampled=true) | `:32` |
| `NewChildSpan()` | Creates child span under current trace | `:41` |
| `Traceparent()` | Formats as W3C traceparent header value | `:51` |
| `ParseTraceparent(header)` | Parses W3C traceparent string | `:60` |
| `InjectHeaders(request)` | Writes traceparent into HTTP request | `:93` |
| `ExtractFromRequest(request)` | Reads traceparent or creates new trace | `:99` |
| `ContextWithTrace(ctx, trace)` | Stores TraceContext in Go context | `:112` |
| `TraceFromContext(ctx)` | Retrieves TraceContext from Go context | `:118` |
| `Middleware(next)` | HTTP middleware: extract → store → propagate | `:128` |

**Header names** (constants at `:17`):
```
traceparent     (W3C standard, lowercase)
tracestate      (W3C standard, lowercase — parsed but not used yet)
```

**ID generation**: `crypto/rand` → 16 bytes for trace ID, 8 bytes for span ID.

### Trace Flow

```
Incoming HTTP request
  │
  ▼
Middleware(next)               (tracing/trace_context.go:128)
  │ Extract traceparent header
  │ If missing/invalid → NewTraceContext()
  │ If valid → NewChildSpan()
  │ Store in context
  │ Set traceparent on response
  │
  ▼
Handler code
  │ TraceFromContext(ctx)  →  read trace
  │ trace.NewChildSpan()  →  for sub-operations
  │ trace.InjectHeaders() →  propagate to outbound calls
```

---

## 4. Structured Logging

### Logger (`logging/logging.go`)

| Type | Description | File:Line |
|---|---|---|
| `Logger` | Structured logger with level, format, component | `:67` |
| `Level` | `DEBUG=0, INFO=1, WARN=2, ERROR=3` | `:18` |
| `Format` | `FormatHuman=0, FormatJSON=1` | `:58` |

**Creation**:
```go
logging.New(output, level, format)           // custom logger
logging.Default()                            // process-wide singleton
logging.SetDefault(logger)                   // replace singleton
logger.WithComponent("scheduler")            // tag all messages with component
```

**Log methods** (`Debug`, `Info`, `Warn`, `Error`): all accept `(message string, keyValues ...string)` where keyValues are alternating key-value pairs.

### Output Formats

**Human** (default):
```
15:04:05.000 INFO  [scheduler] scheduling instance  service=web node=node-2
```

**JSON** (`FormatJSON`):
```json
{"ts":"2026-10-30T12:00:00.000Z","level":"INFO","component":"scheduler","msg":"scheduling instance","fields":{"service":"web","node":"node-2"}}
```

### JSON Entry Structure (`jsonEntry`, `:76`)

| Field | JSON Key | Description |
|---|---|---|
| `Timestamp` | `ts` | RFC3339Nano timestamp (UTC) |
| `Level` | `level` | DEBUG/INFO/WARN/ERROR |
| `Component` | `component` | Component tag (omitted if empty) |
| `Message` | `msg` | Log message |
| `Fields` | `fields` | Key-value pairs (omitted if empty) |

---

## 5. Health Probes

### Probe Types (`types/state.go:209-231`)

```
ProbeType = string
  ├── ProbeStartup    = "startup"    — gate: blocks liveness/readiness until pass
  ├── ProbeLiveness   = "liveness"   — ongoing: failures → drain → replace
  └── ProbeReadiness  = "readiness"  — ongoing: failures → remove from endpoints
```

`AllProbeTypes = []ProbeType{ProbeStartup, ProbeLiveness, ProbeReadiness}`

### Probe Methods (`agent/health.go`)

| Method | Check | Success Criteria | File:Line |
|---|---|---|---|
| `http` | HTTP GET to `host:port/path` | Status 2xx-3xx | `health.go:59` |
| `tcp` | TCP dial to `host:port` | Connection succeeds | `health.go:78` |
| `exec` | Execute command in container via `runtime.Exec` | Exit code 0 | `probes.go:390` |

### Probe State Machines

**Startup Probe**:
```
                              ┌─ success ≥ threshold ─▶ StartupProbeSucceeded
                              │                         (gates liveness/readiness)
StartupProbePending ──check──┤
                              │
                              └─ failure ≥ threshold ─▶ StartupProbeFailed
                                                        (triggers replacement)
```

**Liveness Probe** (only runs after startup succeeds):
```
                              ┌─ success ≥ threshold ─▶ LivenessProbeHealthy
LivenessProbeHealthy ─check──┤
                              └─ failure ≥ threshold ─▶ LivenessProbeUnhealthy
                                                        (triggers drain → replace)
```

**Readiness Probe** (only runs after startup succeeds):
```
                               ┌─ success ≥ threshold ─▶ ReadinessProbeReady
ReadinessProbeReady ──check───┤
                               └─ failure ≥ threshold ─▶ ReadinessProbeNotReady
                                                          (removed from endpoints)
```

### ProbeScheduler (`agent/probes.go`)

Runs independently of the main reconciliation loop.

```
ProbeScheduler.Run(ctx, findInstances)
  │
  └─ Every interval tick:
      └─ findInstances()  →  list of placedInstanceInfo
          └─ For each instance:
              ├── performHealthCheckAndReportResult()   legacy health check
              └── executeProbesForInstance()
                   ├── loadProbeConfig(startup)
                   ├── loadProbeConfig(liveness)
                   ├── loadProbeConfig(readiness)
                   │
                   ├── executeStartupProbe()   if configured, gates others
                   ├── executeLivenessProbe()   if startup complete
                   ├── executeReadinessProbe()  if startup complete
                   │
                   ├── publishProbeProgress()  write JSON progress to store
                   └── updateSummaryHealthFromProbes()  derive health fact
```

### Probe Configuration (from fact store)

| Fact Key | Default | Description |
|---|---|---|
| `desired/service/{svc}/probe/{type}/method` | — | `http`, `tcp`, or `exec` |
| `desired/service/{svc}/probe/{type}/path` | — | URL path or exec command |
| `desired/service/{svc}/probe/{type}/port` | first expose port | TCP port |
| `desired/service/{svc}/probe/{type}/interval` | `10s` | Time between checks |
| `desired/service/{svc}/probe/{type}/timeout` | `2s` | Max check duration |
| `desired/service/{svc}/probe/{type}/failure_threshold` | `3` | Consecutive failures to transition |
| `desired/service/{svc}/probe/{type}/success_threshold` | `1` | Consecutive successes to transition |
| `desired/service/{svc}/probe/{type}/initial_delay` | `0` | Delay before first check |

### Probe Constants (`agent/probes.go:21-36`)

| Constant | Value | File:Line |
|---|---|---|
| `defaultProbeInterval` | `10s` | `:23` |
| `defaultProbeTimeout` | `2s` | `:27` |
| `defaultProbeFailureThreshold` | `3` | `:31` |
| `defaultProbeSuccessThreshold` | `1` | `:35` |
| `defaultHealthProbeTimeout` | `2s` | `health.go:17` |

### Legacy Health Check

Separate from the probe system, configured via `desired/service/{svc}/health/method`.
Runs inside `performHealthCheckAndReportResult()`. Writes
`observed/instance/{id}/health` directly.

| Fact Key | Description |
|---|---|
| `desired/service/{svc}/health/method` | `http` or `tcp` |
| `desired/service/{svc}/health/path` | HTTP check path |
| `desired/service/{svc}/health/interval` | Check interval (default 10s) |
| `desired/service/{svc}/health/timeout` | Check timeout (default 2s) |

---

## 6. Node Heartbeat

### HeartbeatLoop (`agent/agent.go:485`)

Runs in a dedicated goroutine, decoupled from the main reconciliation ticker.
This prevents slow reconciliation cycles from delaying heartbeats and causing
false node-failure detection.

```
agent.Run(ctx)
  │
  ├── Register node + write initial heartbeat
  ├── PublishAliveState()     one-time CAS to observed/node/{id}/state = "alive"
  │
  └── go heartbeatLoop(ctx)   independent goroutine
       └── Every 5s:
           └── nodeReporter.WriteHeartbeat(ctx)
                 └── PUT lease/node/{id} = Unix-millis timestamp
```

### Node Registration (`agent/telemetry.go:111`)

On startup, the agent writes:

| Step | Method | Fact Key | Value |
|---|---|---|---|
| 1 | `RegisterNode()` | `observed/node/{id}` | `""` (marker) |
| 2 | `WriteHeartbeat()` | `lease/node/{id}` | Unix-millis timestamp |
| 3 | `PublishAliveState()` | `observed/node/{id}/state` | `"alive"` (CAS) |
| 4 | `PublishAdvertiseAddress()` | `observed/node/{id}/address` | IP address |

**CAS for alive state** (`telemetry.go:60`): Uses store transactions to avoid
race with NodeFailureController:
- Key absent → create-only (revision-0 guard)
- Key exists with `alive` → CAS with revision guard
- Key exists with `draining`/`disabled`/`unreachable` → skip (controller owns state)

### Heartbeat Timing

| Constant | Value | File:Line |
|---|---|---|
| `heartbeatInterval` | `5s` | `agent/agent.go:42` |
| `defaultNodeFailureLeaseTimeout` | `30s` | `controllers/nodefailure.go:22` |

**Failure window**: 30s timeout / 5s interval = 6 missed heartbeats before unreachable.

---

## 7. Failure Detection Flow

### NodeFailureController (`controllers/nodefailure.go`)

```
Watch:
  lease/node/                    ← heartbeat timestamps
  observed/node/                 ← current node states
  observed/instance/             ← instance facts
  derived/instance/              ← existing failure markers
  effective/placement/           ← instance-to-node placements

Writes:
  observed/node/{id}/state       ← "unreachable" when lease expires
  derived/instance/{id}/node_failure ← "true" for instances on dead nodes
```

### Failure Detection Timeline

```
t=0    Agent writes heartbeat (lease/node/worker-1 = 1730000000000)
t=5s   Agent writes heartbeat
t=10s  Agent writes heartbeat
t=15s  ❌ Agent crashes (no more heartbeats)
t=20s  NodeFailureController reads lease, age = 5s → OK
t=25s  NodeFailureController reads lease, age = 10s → OK
t=30s  NodeFailureController reads lease, age = 15s → OK
t=35s  NodeFailureController reads lease, age = 20s → OK
t=45s  NodeFailureController reads lease, age = 30s → EXPIRED
       │
       ├── PUT observed/node/worker-1/state = "unreachable"
       │
       └── For each instance placed on worker-1:
           └── PUT derived/instance/{id}/node_failure = "true"
               │
               ▼
           FailureController sees instance as "failed"
               │
               └── Stop + replace (with crash-loop backoff)
```

### FailureController (`controllers/failure.go`)

Three failure scenarios:

| Trigger | Action | Grace Period |
|---|---|---|
| Instance state `"failed"` (runtime crash) | Immediate stop + replace | Crash-loop backoff |
| Liveness probe `"unhealthy"` | Drain → wait → replace | `DefaultDrainGracePeriod` (5s) |
| Startup probe `"failed"` | Immediate stop + replace | Crash-loop backoff |

### Crash-Loop Protection

```
First failure:  immediate replacement
2nd failure:    10s delay (crashLoopBaseDelay)
3rd failure:    20s delay (2^1 × base)
4th failure:    40s delay (2^2 × base)
...
nth failure:    min(base × 2^(n-1), 300s)  (crashLoopMaxDelay = 5min)

After 10 consecutive failures (defaultCircuitBreakerThreshold):
  Circuit breaker OPEN → block all replacements for 10min
  After cooldown: half-open → allow 1 probe replacement
  If probe fails: re-open for another 10min cooldown
```

### Failure Controller Constants (`controllers/failure.go`)

| Constant | Value | File:Line |
|---|---|---|
| `DefaultDrainGracePeriod` | `5s` | `:22` |
| `crashLoopBaseDelay` | `10s` | `:25` |
| `crashLoopMaxDelay` | `5m` | `:28` |
| `defaultCircuitBreakerThreshold` | `10` | `:33` |
| `defaultCircuitBreakerCooldown` | `10m` | `:38` |
| `defaultMaxReplacementsPerCycle` | `14` | `:50` |
| `defaultMaxDrainsPerCycle` | `10` | `:56` |
| `defaultMaxRecoveriesPerCycle` | `10` | `:64` |
| `maxFailureControllerChangesPerCycle` | `58` | `:71` |

### NodeFailureController Constants (`controllers/nodefailure.go`)

| Constant | Value | File:Line |
|---|---|---|
| `defaultNodeFailureLeaseTimeout` | `30s` | `:22` |
| `defaultMaxNodeStateChangesPerCycle` | `12` | `:35` |

---

## 8. Resource Telemetry

### NodeReporter (`agent/telemetry.go`)

`CollectAndReportTelemetry()` runs on the agent's reconciliation ticker (default 1s).

```
CollectAndReportTelemetry(ctx)
  │
  ├── runtimeAdapter.List(ctx)                    list all workloads
  │    └── count running workloads
  │         └── PUT observed/node/{id}/workload_count = N
  │
  └── reportWorkloadTelemetry(ctx, workloads)
       └── For each running workload:
           ├── runtimeAdapter.Stats(ctx, id)      query cgroup/proc stats
           ├── PUT observed/telemetry/instance/{id}/cpu     = millicores
           ├── PUT observed/telemetry/instance/{id}/memory  = bytes
           └── if OOM kills > 0:
               └── PUT observed/telemetry/instance/{id}/oom_kills = count
                   + WARN log "container OOM kill detected"
```

### Resource Parsing Utilities (`agent/telemetry.go`)

| Function | Input | Output | File:Line |
|---|---|---|---|
| `parseMillicores()` | `"500m"` or `"0.5"` | `500` (int64 millicores) | `:221` |
| `parseMemoryBytes()` | `"512Mi"`, `"1Gi"` | bytes (int64) | `:239` |

**Supported memory suffixes**: `Ki` (1024), `Mi` (1024²), `Gi` (1024³), `Ti` (1024⁴)

---

## 9. CLI Observability Tools

### Commands (`cmd/cca/main.go`, `cmd/cca/command_status.go`)

| Command | API Endpoint | Description | File:Line |
|---|---|---|---|
| `cca status` | `GET /status` | Cluster overview (text) | `command_status.go:519` |
| `cca top nodes` | `GET /api/status` | Per-node CPU, memory, workloads | `:553` |
| `cca top workloads` | `GET /api/status` | Per-instance CPU, memory, health | `:578` |
| `cca top volumes` | `GET /api/status` | Per-volume state, usage | `:605` |
| `cca watch [prefix]` | `GET /api/watch?prefix=` | SSE stream of fact changes | `:1879` |
| `cca events [--follow] [--service]` | `GET /api/logs`, `GET /api/events/stream` | Event log query/stream | `:69` |
| `cca logs <svc> [--follow]` | `GET /api/logs` | Service log output | `main.go:111` |
| `cca node-inspect <id>` | `GET /api/describe` | Detailed node state dump | `main.go:220` |
| `cca metric set <svc> <m> <v>` | `POST /api/metric` | Inject simulated metric | `:538` |

### `cca top nodes` Output

```
NODE            STATUS        CPU               MEMORY            WORKLOADS
node-1          alive         1200m/4000m       2048Mi/8192Mi     5
node-2          alive          800m/4000m       1024Mi/8192Mi     3
```

### `cca top workloads` Output

```
WORKLOAD                  NODE            STATUS      CPU         MEMORY      HEALTH
web/inst-abc123           node-1          running     200m        128Mi       healthy
worker/inst-def456        node-2          running     400m        256Mi       healthy
```

---

## 10. API Endpoints

### Observability-Related Routes (`api/server.go:364-393`)

| Endpoint | Method | Auth Required | Description |
|---|---|---|---|
| `/metrics` | GET | No (rate-limit exempt) | Prometheus text format |
| `/healthz` | GET | No (rate-limit exempt) | Health check (JSON) |
| `/api/status` | GET | Yes | Full cluster status (JSON) |
| `/api/watch` | GET | Yes | SSE stream of fact changes |
| `/api/logs` | GET | Yes | Event/log query |
| `/api/events/stream` | GET | Yes | SSE stream of events |
| `/api/metric` | POST | Yes | Inject custom metric |
| `/api/describe` | GET | Yes | Detailed resource description |

### `/healthz` Response (`api/server.go:422`)

```json
{
  "status": "healthy",
  "mode": "full",
  "checks": [
    {"name": "store", "status": "healthy"},
    {"name": "controllers", "status": "healthy"}
  ]
}
```

Checks:
- **store**: `factStore.Revision(ctx)` — etcd reachable?
- **controllers** (full mode only): `leader/` prefix has entries — leader elected?

Returns 200 when healthy, 503 when any critical check fails.

---

## 11. Fact Store Keys

### Observability Fact Prefixes

| Prefix | Written By | Consumed By |
|---|---|---|
| `lease/node/{id}` | Agent heartbeatLoop | NodeFailureController |
| `observed/node/{id}` | Agent RegisterNode | Scheduler, controllers |
| `observed/node/{id}/state` | Agent (initial), NodeFailureController | All controllers |
| `observed/node/{id}/address` | Agent | Data plane, networking |
| `observed/node/{id}/capacity/cpu` | Agent | Scheduler, `cca top` |
| `observed/node/{id}/capacity/memory` | Agent | Scheduler, `cca top` |
| `observed/node/{id}/available/cpu` | Agent | Scheduler |
| `observed/node/{id}/available/memory` | Agent | Scheduler |
| `observed/node/{id}/architecture` | Agent | Scheduler (placement filter) |
| `observed/node/{id}/zone` | Agent | Scheduler (zone spread) |
| `observed/node/{id}/workload_count` | Agent telemetry | `cca top nodes` |
| `observed/instance/{id}/state` | Agent | FailureController |
| `observed/instance/{id}/health` | ProbeScheduler | FailureController, `cca top` |
| `observed/instance/{id}/ip` | Agent | ProbeScheduler, endpoints |
| `observed/instance/{id}/probe/{type}` | ProbeScheduler | FailureController |
| `observed/instance/{id}/probe_progress` | ProbeScheduler | CLI/API status |
| `observed/telemetry/instance/{id}/cpu` | Agent telemetry | `cca top workloads` |
| `observed/telemetry/instance/{id}/memory` | Agent telemetry | `cca top workloads` |
| `observed/telemetry/instance/{id}/oom_kills` | Agent telemetry | OOM alerting |
| `observed/metric/service/{svc}/{metric}` | `cca metric set` | Autoscaler |
| `derived/instance/{id}/node_failure` | NodeFailureController | InstanceController |
| `derived/instance/{id}/drain_readiness` | FailureController | EndpointController |
| `derived/instance/{id}/drain_since` | FailureController | FailureController |
| `derived/crashloop/{svc}` | FailureController | FailureController (backoff) |

### Telemetry Prefix Separation

`observed/telemetry/` is intentionally separated from `observed/instance/` so
that high-frequency CPU/memory updates don't trigger controllers watching
`observed/instance/` (which would cause unnecessary reconciliation cycles).

Defined at `types/keys.go:65`:
```
PrefixObservedTelemetry = PrefixObserved + "/telemetry"
```

---

## 12. Agent Timing Constants

| Constant | Value | File:Line | Description |
|---|---|---|---|
| `defaultAgentReconcileInterval` | `1s` | `agent/agent.go:32` | Main reconciliation ticker |
| `heartbeatInterval` | `5s` | `agent/agent.go:42` | Independent heartbeat writes |
| `dataPlaneReconcileInterval` | `1s` | `agent/agent.go:48` | Data plane reconciliation |
| `defaultProbeInterval` | `10s` | `agent/probes.go:23` | Default health probe interval |
| `defaultNodeFailureLeaseTimeout` | `30s` | `controllers/nodefailure.go:22` | Lease expiry threshold |

### Timing Relationships

```
Agent Reconcile (1s)  ──────────────────────────────────────────▶
Heartbeat (5s)        ─────5s─────10s────15s────20s────25s──────▶
Probes (10s)          ──────────10s──────────────20s────────────▶
Failure Detection     ────────────────────────────────30s───────▶
                      0    5    10    15    20    25    30 seconds
```

Node failure detection requires ~30s after last successful heartbeat:
6 missed heartbeats (5s each) = 30s timeout → NodeFailureController marks
unreachable → FailureController replaces instances.

---

## 13. Node State Machine

```
                    ┌──────────────────────────────────┐
                    │          Agent startup            │
                    │  PublishAliveState() with CAS     │
                    └──────────────┬───────────────────┘
                                   │
                                   ▼
                    ┌──────────────────────────────────┐
          ┌────────│            alive                   │◀────────────┐
          │        └──────────────┬────────┬───────────┘             │
          │                       │        │                         │
          │   lease timeout       │        │  cca drain <node>       │
          │   (30s missed)        │        │                         │
          │                       │        ▼                         │
          │        ┌──────────────────────────────────┐              │
          │        │          draining                 │              │
          │        │  (graceful workload migration)    │              │
          │        └──────────────┬───────────────────┘              │
          │                       │                                  │
          ▼                       ▼                                  │
┌──────────────────────────────────────────────────┐                 │
│              unreachable                          │                 │
│  NodeFailureController owns this state.           │─── heartbeat ──┘
│  Instances marked with node_failure.              │    resumes
│  FailureController replaces them.                 │
└──────────────────────────────────────────────────┘

                    ┌──────────────────────────────────┐
                    │           disabled                │
                    │  cca node disable <node>          │
                    │  Alive but excluded from          │
                    │  new placements                   │
                    └──────────────────────────────────┘
```

### Node States (`types/state.go:52-68`)

| State | Constant | Description |
|---|---|---|
| `alive` | `NodeAlive` | Healthy, maintaining lease, accepts placements |
| `unreachable` | `NodeUnreachable` | Lease expired, instances being replaced |
| `draining` | `NodeDraining` | Graceful offline, workloads migrating |
| `disabled` | `NodeDisabled` | Alive but excluded from new placements |

### Health States (`types/state.go:150-155`)

| State | Constant | Description |
|---|---|---|
| `healthy` | `HealthHealthy` | Passed last health check |
| `unhealthy` | `HealthUnhealthy` | Failed last health check |

### Probe States (`types/state.go:162-203`)

| Probe Type | States |
|---|---|
| Startup | `pending`, `succeeded`, `failed` |
| Liveness | `healthy`, `unhealthy` |
| Readiness | `ready`, `not-ready` |
