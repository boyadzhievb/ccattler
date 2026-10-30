# CCattler Observability Call Graph Reference

Machine-readable function call index for all observability subsystems:
Prometheus metrics, controller metrics, tracing, logging, probes, heartbeat,
failure detection, telemetry, and event projection. Load this file instead of
reading source.

## Prometheus Registry (metrics/metrics.go)

### Registry.Handler → HTTP exposition endpoint
```
Registry.Handler                                  (metrics/metrics.go:259)
├── responseWriter.Header.Set("Content-Type", "text/plain; version=0.0.4")
├── registry.mutex.RLock
├── FOR EACH registrationKey in registrationOrder:
│   ├── "counter:" → writeCounterText              (:288)
│   │   └── formatLabelString → Fprintf per key
│   ├── "gauge:" → writeGaugeText                  (:302)
│   │   └── formatLabelString → Fprintf per key
│   └── "histogram:" → writeHistogramText           (:316)
│       ├── cumulative bucketCounts per boundary
│       ├── +Inf bucket = totalCount
│       ├── _sum = totalSum / 1e6  (micros → seconds)
│       └── _count = totalCount
└── responseWriter.Write(builder.String)
```

### Counter hot path
```
Counter.Inc(labelValues...)                       (metrics/metrics.go:127)
└── Counter.Add(1, labelValues...)                 (:132)
    ├── joinLabelValues(labelValues)                (:342)  ["\x00" separator]
    ├── mutex.RLock → check values map [fast path]
    └── mutex.Lock → create atomic.Int64 [first-touch only]
        └── atomicValue.Add(delta)                 [lock-free after creation]
```

### Gauge hot path
```
Gauge.Set(value, labelValues...)                  (metrics/metrics.go:152)
├── joinLabelValues → labelKey
├── mutex.RLock → check values map [fast path]
└── atomicValue.Store(value)                       [lock-free after creation]

Gauge.Inc / Gauge.Dec                              (:172, :192)
└── atomicValue.Add(±1)
```

### Histogram hot path
```
Histogram.Observe(valueMicros, labelValues...)    (metrics/metrics.go:213)
├── joinLabelValues → labelKey
├── mutex.RLock → check series map [fast path]
├── series.totalSum.Add(valueMicros)
├── series.totalCount.Add(1)
└── FOR EACH bucket boundary:
    └── if valueSeconds ≤ boundary → bucketCounts[i].Add(1), break

Histogram.ObserveDuration(duration, labelValues...) (:243)
└── Observe(duration.Microseconds(), labelValues...)

Histogram.ObserveSince(startTime, labelValues...)   (:253)
└── ObserveDuration(time.Since(startTime), labelValues...)
```

### Registered API metrics (api/server.go:33–61)

| Metric | Type | Labels | Line |
|---|---|---|---|
| `ccattler_api_requests_total` | Counter | `endpoint`, `method`, `status` | `:33` |
| `ccattler_api_request_duration_seconds` | Histogram | `endpoint` | `:39` |
| `ccattler_active_watches` | Gauge | — | `:44` |
| `ccattler_instances` | Gauge | `state` | `:49` |
| `ccattler_services` | Gauge | — | `:54` |
| `ccattler_nodes` | Gauge | — | `:58` |

### Registered runner metrics (controllers/runner.go:71–101)

| Metric | Type | Labels | Line |
|---|---|---|---|
| `ccattler_reconciliation_duration_seconds` | Histogram | `controller` | `:71` |
| `ccattler_reconciliation_total` | Counter | `controller`, `result` | `:77` |
| `ccattler_reconciliation_conflicts_total` | Counter | `controller` | `:83` |
| `ccattler_reconciliation_changes_total` | Counter | `controller` | `:87` |
| `ccattler_write_domain_violations_total` | Counter | `controller` | `:92` |
| `ccattler_transaction_rejections_total` | Counter | `controller` | `:97` |

---

## MetricsCollector — Control Plane (controllers/metrics.go)

### Recording call chain
```
HARunner.Run → LeaderElection.Run                  (controllers/ha_runner.go:78)
  └── startControllers → Runner.Run                 (:101)
      └── runSingleController → runControllerLoop   (controllers/runner.go:238)
          └── executeReconciliationCycle             (:409)
              ├── attemptSingleReconciliation        (:469)
              │   └── controller.Reconcile
              └── metricsCollector.RecordReconciliation  [after each cycle]
                  (controllers/metrics.go:52)
                  ├── getOrCreateReconciliationMetrics   (:160) [double-check lock]
                  ├── TotalCycles.Add(1)
                  ├── TotalChanges.Add(changeCount)
                  ├── LastDuration.Store(durationMicros)
                  ├── TotalDuration.Add(durationMicros)
                  ├── MaxDuration.CompareAndSwap         [lock-free CAS loop]
                  └── if err → TotalErrors.Add(1)
```

### RecordSchedulingDecision
```
RecordSchedulingDecision                            (controllers/metrics.go:77)
└── schedulingDecisions.Add(1)                     [atomic, no lock]
```

### RecordInstanceTransition
```
RecordInstanceTransition(transition)                (controllers/metrics.go:83)
└── getOrCreateTransitionCounter(transition)        (:179) [double-check lock]
    └── counter.Add(1)
```

### Read API (used by cca status/top)
```
GetReconciliationMetrics(name) → *ReconciliationMetrics    (:90)
GetAllReconciliationStats() → map[string]ReconciliationSnapshot  (:125)
  └── FOR EACH controller: snapshot TotalCycles, TotalErrors, TotalChanges,
      LastDurationMicro, MaxDurationMicro, AvgDurationMicro
GetSchedulingDecisions() → int64                           (:97)
GetInstanceTransitions(transition) → int64                 (:102)
GetAllTransitions() → map[string]int64                     (:113)
```

### Wiring
```
cmd/cca/command_server.go:
  NewMetricsCollector()                             (:382)
  → NewHARunner(factStore, nodeID, metrics, ...)    (:396)
  → haRunner.Metrics() → *MetricsCollector          (ha_runner.go:95)
```

---

## Distributed Tracing (tracing/trace_context.go)

### Middleware → extract/create trace → inject response
```
Middleware(next)                                    (tracing/trace_context.go:128)
├── ExtractFromRequest(request)                     (:99)
│   ├── request.Header.Get("traceparent")
│   ├── IF empty/invalid → NewTraceContext()         (:32)
│   │   ├── generateTraceID()                        (:137) [crypto/rand 16 bytes]
│   │   ├── generateSpanID()                         (:143) [crypto/rand 8 bytes]
│   │   └── Sampled = true
│   └── IF valid → ParseTraceparent(header)          (:61)
│       ├── split on "-" → [version, traceID, spanID, flags]
│       ├── validate: version=="00", len(traceID)==32, len(spanID)==16
│       └── → traceContext.NewChildSpan()            (:41)
│           └── new SpanID, ParentID = old SpanID
├── ContextWithTrace(ctx, traceContext)              (:112)
│   └── context.WithValue(ctx, traceContextKey{}, traceContext)
├── responseWriter.Header.Set("traceparent", traceContext.Traceparent())
│   └── Traceparent()                                (:51)
│       └── "00-{traceID}-{spanID}-{flags}"
└── next.ServeHTTP(responseWriter, request.WithContext(ctx))
```

### Downstream propagation
```
Handler code
├── TraceFromContext(ctx)                            (:118)
│   └── ctx.Value(traceContextKey{}).(TraceContext)
├── traceContext.NewChildSpan()                     [for sub-operations]
└── traceContext.InjectHeaders(outboundRequest)      (:93)
    └── request.Header.Set("traceparent", Traceparent())
```

### W3C format
```
traceparent: 00-{traceID 32hex}-{spanID 16hex}-{flags 2hex}
                 └─ 16 bytes     └─ 8 bytes    └─ 01=sampled
```

---

## Structured Logging (logging/logging.go)

### Logger.log → format dispatch
```
Logger.Debug / .Info / .Warn / .Error               (logging/logging.go:120–136)
└── Logger.log(level, message, keyValues)            (:139)
    ├── if level < logger.level → return [fast skip]
    ├── timestamp = time.Now().UTC()
    ├── mutex.Lock
    ├── IF FormatJSON → writeJSON                    (:156)
    │   ├── jsonEntry{ts: RFC3339Nano, level, component, msg, fields}
    │   └── json.Marshal → output.Write
    └── IF FormatHuman → writeHuman                  (:179)
        └── "15:04:05.000 LEVEL [component] message key=value ..."
```

### Singleton
```
Default() → defaultLogger                           (:91)   [process-wide]
SetDefault(logger)                                  (:96)   [replace singleton]
New(output, level, format) → *Logger                (:101)
logger.WithComponent("scheduler") → *Logger         (:110)  [returns copy with tag]
```

### Level constants (logging/logging.go:17–24)

| Level | Int | String |
|---|---|---|
| `LevelDebug` | 0 | `DEBUG` |
| `LevelInfo` | 1 | `INFO` |
| `LevelWarn` | 2 | `WARN` |
| `LevelError` | 3 | `ERROR` |

---

## Health Probes (agent/probes.go, agent/health.go)

### ProbeScheduler.Run → main probe loop
```
ProbeScheduler.Run(ctx, findInstances)              (agent/probes.go:99)
└── ticker at probeScheduler.interval
    └── ExecuteProbePass(ctx, findInstances)         (:115)   [on each tick]
        ├── findInstances(ctx) → []placedInstanceInfo
        └── FOR EACH instance:
            ├── performHealthCheckAndReportResult    (:517)   [legacy health]
            └── executeProbesForInstance              (:131)   [three-probe system]
```

### executeProbesForInstance → full probe pass for one instance
```
executeProbesForInstance(ctx, instanceInfo)          (agent/probes.go:131)
├── loadProbeConfig(ctx, service, ProbeStartup)      (:406)
├── loadProbeConfig(ctx, service, ProbeLiveness)
├── loadProbeConfig(ctx, service, ProbeReadiness)
├── [all nil → return]
├── getOrCreateProbeState(instanceID)                (:483)
├── factStore.Get(observed/instance/{id}/ip)         [BAIL if no IP]
│
├── IF startupConfig:
│   └── executeStartupProbe(...)                     (:202)
│       ├── readProbeState(ctx, id, ProbeStartup)    (:473)
│       ├── [if "succeeded" → return true]
│       ├── [if "failed" → return false]
│       ├── check initialDelay
│       ├── check interval
│       ├── runProbeCheck(ctx, config, id, ip)       (:366)
│       │   ├── "http" → CheckHealth(ProbeHTTP)      (health.go:41)
│       │   │   └── performHTTPHealthCheck            (:59)   [GET, 2xx-3xx]
│       │   ├── "tcp" → CheckHealth(ProbeTCP)
│       │   │   └── performTCPHealthCheck             (:78)   [dial]
│       │   └── "exec" → runtime.Exec(id, ExecSpec)  [exit 0 = pass]
│       ├── IF passed: consecutiveSuccesses++
│       │   └── ≥ threshold → PUT probe state = "succeeded"
│       └── IF failed: consecutiveFailures++
│           └── ≥ threshold → PUT probe state = "failed"
│
├── IF startupComplete AND livenessConfig:
│   └── executeLivenessProbe(...)                    (:268)
│       ├── runProbeCheck → passed/failed
│       ├── ≥ successThreshold → PUT "healthy"
│       └── ≥ failureThreshold → PUT "unhealthy"
│
├── IF startupComplete AND readinessConfig:
│   └── executeReadinessProbe(...)                   (:316)
│       ├── runProbeCheck → passed/failed
│       ├── ≥ successThreshold → PUT "ready"
│       └── ≥ failureThreshold → PUT "not-ready"
│
├── publishProbeProgress(ctx, ...)                   (:605)
│   └── JSON {StartupFailures, LivenessFailures, ReadinessFailures}
│       → PUT observed/instance/{id}/probe_progress
│
└── updateSummaryHealthFromProbes(ctx, id, startupComplete) (:178)
    ├── liveness "unhealthy" → PUT health = "unhealthy"
    ├── readiness "not-ready" → PUT health = "unhealthy"
    └── either "healthy"/"ready" → PUT health = "healthy"
```

### performHealthCheckAndReportResult → legacy health check
```
performHealthCheckAndReportResult(ctx, instanceInfo) (agent/probes.go:517)
├── buildHealthProbeFromServiceConfig(ctx, service)   (:565)
│   ├── factStore.Get(desired/service/{svc}/health/method)
│   ├── "http" → ProbeHTTP, read health/path
│   ├── "tcp" → ProbeTCP
│   └── Scan(desired/service/{svc}/expose/) → derive port
├── lookupHealthIntervalFromStore(ctx, service)       (:549)
├── [check lastHealthCheck map, skip if < interval]
├── factStore.Get(observed/instance/{id}/ip)
├── CheckHealth(ctx, probe, instanceIP)               (health.go:41)
└── PUT observed/instance/{id}/health = healthy/unhealthy
```

### CheckHealth dispatch (agent/health.go:41)
```
CheckHealth(ctx, probe, host)
├── ProbeHTTP → performHTTPHealthCheck(ctx, host, port, path, timeout)  (:59)
│   └── http.Client{Timeout}.Do(GET http://host:port/path)
│       └── pass if status ∈ [200, 400)
└── ProbeTCP → performTCPHealthCheck(host, port, timeout)               (:78)
    └── net.DialTimeout("tcp", host:port, timeout)
        └── pass if connection succeeds
```

### Probe constants (agent/probes.go:20–36, agent/health.go:17)

| Constant | Value | File:Line |
|---|---|---|
| `defaultProbeInterval` | `10s` | `probes.go:23` |
| `defaultProbeTimeout` | `2s` | `probes.go:27` |
| `defaultProbeFailureThreshold` | `3` | `probes.go:31` |
| `defaultProbeSuccessThreshold` | `1` | `probes.go:35` |
| `defaultHealthProbeTimeout` | `2s` | `health.go:17` |

---

## Node Heartbeat (agent/agent.go, agent/telemetry.go)

### heartbeatLoop → independent goroutine
```
Agent.Run(ctx)                                      (agent/agent.go:291)
├── nodeReporter.RegisterNode(ctx)                   (telemetry.go:111)
│   └── PUT observed/node/{id} = ""                  [marker]
├── nodeReporter.PublishAliveState(ctx)               (telemetry.go:60)
│   ├── factStore.Get(observed/node/{id}/state)
│   ├── IF absent → Transaction(revision-0 guard, PUT "alive")
│   ├── IF "alive" → Transaction(CAS guard, PUT "alive")
│   └── IF draining/disabled/unreachable → skip       [controller owns]
├── nodeReporter.WriteHeartbeat(ctx)                  (telemetry.go:43)
│   └── PUT lease/node/{id} = Unix-millis timestamp
├── nodeReporter.PublishAdvertiseAddress(ctx)          (telemetry.go:101)
│   └── PUT observed/node/{id}/address = advertiseAddress
│
└── go heartbeatLoop(ctx)                             (agent/agent.go:485) [goroutine]
    └── ticker at 5s (heartbeatInterval)
        └── nodeReporter.WriteHeartbeat(ctx)
            └── PUT lease/node/{id} = Unix-millis timestamp
```

### Heartbeat timing

| Constant | Value | File:Line |
|---|---|---|
| `heartbeatInterval` | `5s` | `agent/agent.go:42` |
| `defaultNodeFailureLeaseTimeout` | `30s` | `controllers/nodefailure.go:22` |

Failure window: 30s / 5s = 6 missed heartbeats → unreachable.

---

## Resource Telemetry (agent/telemetry.go)

### CollectAndReportTelemetry → called on agent reconcile ticker (1s)
```
CollectAndReportTelemetry(ctx)                      (agent/telemetry.go:121)
├── runtimeAdapter.List(ctx) → []Status
├── count running workloads
│   └── PUT observed/node/{id}/workload_count = N
│
└── reportWorkloadTelemetry(ctx, workloads)          (:146)
    └── FOR EACH running workload:
        ├── runtimeAdapter.Stats(ctx, id) → ResourceStats
        ├── PUT observed/telemetry/instance/{id}/cpu = millicores
        ├── PUT observed/telemetry/instance/{id}/memory = bytes
        └── IF oomKillCount > 0:
            ├── PUT observed/telemetry/instance/{id}/oom_kills = count
            └── WARN "container OOM kill detected"
```

### Resource parsers (agent/telemetry.go:221–274)

| Function | Input | Output | Line |
|---|---|---|---|
| `parseMillicores(cpuString)` | `"500m"` or `"0.5"` | `500` int64 | `:221` |
| `parseMemoryBytes(memString)` | `"512Mi"`, `"1Gi"` | bytes int64 | `:239` |

Supported memory suffixes: `Ki` (1024), `Mi` (1024²), `Gi` (1024³), `Ti` (1024⁴).

### Telemetry prefix separation

`observed/telemetry/` is separated from `observed/instance/` so high-frequency
CPU/memory updates don't trigger controllers watching `observed/instance/`.
Defined at `types/keys.go:65`: `PrefixObservedTelemetry = PrefixObserved + "/telemetry"`.

---

## Failure Detection (controllers/nodefailure.go, controllers/failure.go)

### NodeFailureController.Reconcile → detect dead nodes
```
NodeFailureController.Reconcile(ctx, facts)          (controllers/nodefailure.go:92)
├── parseNodeLeaseTimestamps(facts)                   (:132)
│   └── FOR EACH lease/node/{id} → parse Unix-millis
├── parseCurrentNodeStates(facts)                     (:149)
│   └── collectStringValuesBySuffix(facts, observed/node/, "state")
│
├── identifyUnreachableNodes(now, heartbeats, states) (:156)
│   ├── FOR EACH node with heartbeat:
│   │   ├── IF unreachable AND heartbeat fresh → recoveredNodeIDs
│   │   ├── IF unreachable AND heartbeat stale → unreachableNodeSet
│   │   ├── IF not alive → skip
│   │   └── IF alive AND heartbeat stale → unreachableNodeSet
│   ├── FOR EACH recovered: PUT observed/node/{id}/state = "alive"
│   └── FOR EACH newly unreachable: PUT observed/node/{id}/state = "unreachable"
│
├── cap node state changes to MaxNodeStateChangesPerCycle (12)
│
├── markInstancesOnUnreachableNodesAsFailed(...)       (:219)
│   ├── build instancePlacementNode map from placement/instance/
│   ├── FOR EACH instance on unreachable node:
│   │   └── if running/pending/starting:
│   │       └── PUT derived/instance/{id}/node_failure = "true"
│   └── cap to remaining budget
│
└── cleanupStaleNodeFailureMarkers(...)                (:270)
    └── DELETE derived/instance/{id}/node_failure
        where instance's node is no longer unreachable
```

### FailureController.Reconcile → replace failed instances
```
FailureController.Reconcile(ctx, facts)              (controllers/failure.go:161)
├── scan instance states, probe states, drain states
├── FOR EACH instance:
│   ├── IF state="failed" OR startup probe="failed":
│   │   └── stopAndReplace(instanceID, serviceName)    (:403)
│   │       ├── PUT observed/instance/{old}/state = "stopped"  [→ controller_stopped]
│   │       └── PUT observed/instance/{new}/state = "pending"  [replacement]
│   │
│   ├── IF liveness probe="unhealthy" AND running:
│   │   └── handleUnhealthyLiveness(...)               (:318)
│   │       ├── IF no drain_since → begin drain:
│   │       │   ├── PUT derived/instance/{id}/drain_readiness = "not-ready"
│   │       │   └── PUT derived/instance/{id}/drain_since = now
│   │       └── IF drain_since + gracePeriod elapsed:
│   │           └── stopAndReplace(...)
│   │
│   └── IF was draining AND now healthy → cleanup drain markers
│
├── crash-loop backoff: derived/crashloop/{svc} JSON
│   ├── count + since timestamp
│   ├── delay = min(base × 2^(count-1), maxDelay)
│   └── IF count ≥ circuitBreakerThreshold → OPEN for cooldown
│
├── cap replacements to MaxReplacementsPerCycle (14)
├── cap drains to MaxDrainsPerCycle (10)
├── cap recoveries to MaxRecoveriesPerCycle (10)
└── final hard cap: maxFailureControllerChangesPerCycle (58)
```

### Failure controller constants (controllers/failure.go:22–71)

| Constant | Value | File:Line |
|---|---|---|
| `DefaultDrainGracePeriod` | `5s` | `:22` |
| `crashLoopBaseDelay` | `10s` | `:25` |
| `crashLoopMaxDelay` | `5m` | `:28` |
| `defaultCircuitBreakerThreshold` | `10` | `:33` |
| `defaultCircuitBreakerCooldown` | `10m` | `:38` |
| `defaultMaxReplacementsPerCycle` | `14` | `:51` |
| `defaultMaxDrainsPerCycle` | `10` | `:58` |
| `defaultMaxRecoveriesPerCycle` | `10` | `:65` |
| `maxFailureControllerChangesPerCycle` | `58` | `:71` |

### Node failure controller constants (controllers/nodefailure.go:22–35)

| Constant | Value | File:Line |
|---|---|---|
| `defaultNodeFailureLeaseTimeout` | `30s` | `:22` |
| `defaultMaxTotalChangesPerCycle` | `58` | `:29` |
| `defaultMaxNodeStateChangesPerCycle` | `12` | `:35` |

---

## Event Projector (controllers/event_projector.go)

### EventProjector.Run → watch committed state transitions
```
EventProjector.Run(ctx)                              (controllers/event_projector.go:49)
├── watchPrefixes:
│   ├── observed/instance/
│   ├── derived/instance/
│   ├── observed/node/
│   ├── placement/instance/
│   └── effective/service/
│
├── mergedChannel = make(chan store.Event, 64)
│
├── FOR EACH prefix:
│   └── go watchPrefixWithReconnect(ctx, prefix, mergedChannel) [goroutine]
│       ├── factStore.Watch(ctx, prefix, WatchOption{Prefix: true})
│       ├── forward events to mergedChannel
│       ├── IF EventCompacted/EventOverflow:
│       │   └── resyncFromScan(ctx, prefix, lastRevision) (:155)
│       │       ├── ScanWithRevision(ctx, prefix)
│       │       └── eventLog.Emit("projector.resync", ...)  [gap marker]
│       └── IF channel closed → sleep(2s) → reconnect
│
└── SELECT loop:
    └── projectEvent(ctx, watchEvent)                  (:175)
        ├── classifyWatchEventAsSemanticEvent(event)    (:226)
        │   ├── observed/instance/{id}/state transitions:
        │   │   ├── → "running" = "instance.running"
        │   │   ├── → "failed" = "instance.failed"
        │   │   ├── → "pending" (from "") = "instance.created"
        │   │   ├── → "pending" (from other) = "instance.pending"
        │   │   ├── → "starting" = "instance.starting"
        │   │   └── → "stopped" = "instance.stopped"
        │   ├── observed/instance/{id}/probe/{type} transitions:
        │   │   ├── startup "failed" = "probe.failed"
        │   │   ├── startup "succeeded" = "probe.succeeded"
        │   │   ├── liveness "unhealthy" = "probe.unhealthy"
        │   │   ├── readiness "not-ready" = "probe.not_ready"
        │   │   └── liveness/readiness recovered = "probe.recovered"
        │   ├── derived/instance/{id}/node_failure = "instance.failed"
        │   ├── derived/instance/{id}/controller_stopped = "instance.stopped"
        │   ├── placement/instance/{id} (PUT) = "instance.placed"
        │   ├── observed/node/{id}/state = "node.{value}"
        │   └── effective/service/{svc}/instances = "service.scaled"
        │
        ├── enrichInstanceEventWithServiceName(ctx, ...)  (:189)
        │   └── factStore.Get(observed/instance/{id}/service)
        │       → replace "instance/{id}" with "instance/{svc}-{id}"
        │
        └── eventLog.Emit(kind, target, detail, "event-projector")
```

### Event projector constants

| Constant | Value | File:Line |
|---|---|---|
| `defaultMergedEventBuffer` | `64` | `event_projector.go:20` |
| `watchReconnectBackoff` | `2s` | `event_projector.go:43` |

---

## API Endpoints (api/server.go)

### handleMetrics → Prometheus scrape
```
handleMetrics(w, r)                                  (api/server.go:398)
├── Scan observed/instance/ → count by state
│   → instancesByState gauge.Set(count, state)
├── Scan desired/service/ → service count
│   → serviceGauge.Set(count)
├── Scan observed/node/ → node count
│   → nodeGauge.Set(count)
└── metrics.DefaultRegistry.Handler().ServeHTTP(w, r)
```

### handleHealthz → cluster health
```
handleHealthz(w, r)                                  (api/server.go:426)
├── check "store": factStore.Revision(ctx)
│   └── pass if no error
├── check "controllers" (full mode only):
│   └── Scan("leader/") → pass if entries exist
├── IF any critical check fails → 503
└── JSON {"status":"healthy","mode":"full","checks":[...]}
```

### API routes (api/server.go:364–393)

| Endpoint | Method | Handler | Auth | Line |
|---|---|---|---|---|
| `/metrics` | GET | `handleMetrics` | No | `:391` |
| `/healthz` | GET | `handleHealthz` | No | `:391` |
| `/api/status` | GET | `handleStatus` | Yes | `:1123` |
| `/api/watch` | GET | `handleWatch` | Yes | `:682` |
| `/api/logs` | GET | `handleLogs` | Yes | `:1167` |
| `/api/events/stream` | GET | `handleEventsStream` | Yes | `:1464` |
| `/api/metric` | POST | `handleMetric` | Yes | `:1644` |
| `/api/describe` | GET | `handleDescribe` | Yes | `:1369` |

---

## Store Key Patterns

### Writes (agent → store)

| Key Pattern | Written By | Function | File:Line |
|---|---|---|---|
| `lease/node/{id}` | heartbeatLoop | `WriteHeartbeat` | `telemetry.go:43` |
| `observed/node/{id}` | agent startup | `RegisterNode` | `telemetry.go:111` |
| `observed/node/{id}/state` | agent startup | `PublishAliveState` | `telemetry.go:60` |
| `observed/node/{id}/address` | agent startup | `PublishAdvertiseAddress` | `telemetry.go:101` |
| `observed/node/{id}/workload_count` | telemetry tick | `CollectAndReportTelemetry` | `telemetry.go:135` |
| `observed/telemetry/instance/{id}/cpu` | telemetry tick | `reportWorkloadTelemetry` | `telemetry.go:157` |
| `observed/telemetry/instance/{id}/memory` | telemetry tick | `reportWorkloadTelemetry` | `telemetry.go:161` |
| `observed/telemetry/instance/{id}/oom_kills` | telemetry tick | `reportWorkloadTelemetry` | `telemetry.go:167` |
| `observed/instance/{id}/health` | probe pass | `performHealthCheckAndReportResult` | `probes.go:541` |
| `observed/instance/{id}/probe/{type}` | probe pass | `executeStartup/Liveness/ReadinessProbe` | `probes.go:230+` |
| `observed/instance/{id}/probe_progress` | probe pass | `publishProbeProgress` | `probes.go:637` |

### Writes (controllers → store)

| Key Pattern | Written By | Function | File:Line |
|---|---|---|---|
| `observed/node/{id}/state` = "unreachable" | NodeFailureController | `identifyUnreachableNodes` | `nodefailure.go:205` |
| `observed/node/{id}/state` = "alive" | NodeFailureController | `identifyUnreachableNodes` | `nodefailure.go:190` |
| `derived/instance/{id}/node_failure` | NodeFailureController | `markInstancesOnUnreachableNodesAsFailed` | `nodefailure.go:258` |
| `derived/instance/{id}/controller_stopped` | FailureController | `stopAndReplace` | `failure.go:403` |
| `derived/instance/{id}/drain_readiness` | FailureController | `handleUnhealthyLiveness` | `failure.go:318` |
| `derived/instance/{id}/drain_since` | FailureController | `handleUnhealthyLiveness` | `failure.go:318` |
| `derived/crashloop/{svc}` | FailureController | crash-loop backoff | `failure.go:161` |

### Reads (controllers ← store)

| Key Pattern | Read By | Function |
|---|---|---|
| `lease/node/{id}` | NodeFailureController | `parseNodeLeaseTimestamps` |
| `observed/node/{id}/state` | NodeFailureController | `parseCurrentNodeStates` |
| `observed/instance/{id}/state` | FailureController | `Reconcile` |
| `observed/instance/{id}/probe/{type}` | FailureController | `Reconcile` |
| `derived/instance/{id}/drain_since` | FailureController | `handleUnhealthyLiveness` |
| `effective/placement/instance/{id}` | NodeFailureController | `markInstancesOnUnreachableNodesAsFailed` |

---

## Reverse Index

| Function | Called By |
|---|---|
| `Registry.Handler` | `handleMetrics` (api/server.go:398) |
| `Counter.Inc` / `Counter.Add` | API middleware, runner reconcile loop |
| `Histogram.ObserveSince` | runner reconcile loop (reconciliationDuration) |
| `Gauge.Set` / `.Inc` / `.Dec` | `handleMetrics` (snapshot), `handleWatch` (activeWatches) |
| `RecordReconciliation` | `executeReconciliationCycle` (runner.go:409) |
| `RecordSchedulingDecision` | Scheduler.Reconcile |
| `RecordInstanceTransition` | InstanceController state changes |
| `Middleware` (tracing) | api/server.go route setup |
| `ExtractFromRequest` | `Middleware` |
| `TraceFromContext` | any handler needing trace context |
| `ProbeScheduler.Run` | `Agent.Run` (goroutine) |
| `ExecuteProbePass` | `ProbeScheduler.Run` (on tick) |
| `executeProbesForInstance` | `ExecuteProbePass` |
| `runProbeCheck` | `executeStartupProbe`, `executeLivenessProbe`, `executeReadinessProbe` |
| `CheckHealth` | `runProbeCheck`, `performHealthCheckAndReportResult` |
| `performHTTPHealthCheck` | `CheckHealth` (ProbeHTTP) |
| `performTCPHealthCheck` | `CheckHealth` (ProbeTCP) |
| `WriteHeartbeat` | `Agent.Run` (initial), `heartbeatLoop` (5s goroutine) |
| `PublishAliveState` | `Agent.Run` (once at startup) |
| `CollectAndReportTelemetry` | agent reconcile cycle (1s) |
| `reportWorkloadTelemetry` | `CollectAndReportTelemetry` |
| `parseMillicores` | `lookupServiceResourcesFromStore` |
| `parseMemoryBytes` | `lookupServiceResourcesFromStore` |
| `NodeFailureController.Reconcile` | Runner (on watch trigger) |
| `identifyUnreachableNodes` | `NodeFailureController.Reconcile` |
| `markInstancesOnUnreachableNodesAsFailed` | `NodeFailureController.Reconcile` |
| `cleanupStaleNodeFailureMarkers` | `NodeFailureController.Reconcile` |
| `FailureController.Reconcile` | Runner (on watch trigger) |
| `stopAndReplace` | `FailureController.Reconcile` |
| `handleUnhealthyLiveness` | `FailureController.Reconcile` |
| `EventProjector.Run` | `Runner.Run` (goroutine) |
| `watchPrefixWithReconnect` | `EventProjector.Run` (goroutine per prefix) |
| `projectEvent` | `EventProjector.Run` (select loop) |
| `classifyWatchEventAsSemanticEvent` | `projectEvent` |
| `enrichInstanceEventWithServiceName` | `projectEvent` |
| `resyncFromScan` | `watchPrefixWithReconnect` (on compaction/overflow) |

---

## Agent Timing Constants

| Constant | Value | File:Line | Description |
|---|---|---|---|
| `defaultAgentReconcileInterval` | `1s` | `agent/agent.go:32` | Main reconciliation ticker |
| `heartbeatInterval` | `5s` | `agent/agent.go:42` | Independent heartbeat writes |
| `dataPlaneReconcileInterval` | `1s` | `agent/agent.go:48` | Data plane reconciliation |
| `defaultProbeInterval` | `10s` | `agent/probes.go:23` | Default health probe interval |
| `defaultNodeFailureLeaseTimeout` | `30s` | `controllers/nodefailure.go:22` | Lease expiry threshold |
| `defaultControllerDebounceInterval` | `50ms` | `controllers/runner.go:26` | Watch debounce |
| `defaultControllerResyncInterval` | `30s` | `controllers/runner.go:30` | Forced full reconcile |

### Timing relationships
```
Agent Reconcile (1s)  ──────────────────────────────────────────▶
Heartbeat (5s)        ─────5s─────10s────15s────20s────25s──────▶
Probes (10s)          ──────────10s──────────────20s────────────▶
Failure Detection     ────────────────────────────────30s───────▶
                      0    5    10    15    20    25    30 seconds
```

---

## Data Flow Chains

### Deploy → probe → failure → replace
```
cca apply → desired/service/{svc}/probe/startup/method = "http"
  → Agent reads probe config in loadProbeConfig
    → ProbeScheduler ticks, executeStartupProbe runs
      → runProbeCheck(http, host:port/path) → fails
        → consecutiveFailures++ → ≥ threshold
          → PUT observed/instance/{id}/probe/startup = "failed"
            → FailureController reads probe state in Reconcile
              → stopAndReplace: stop old, create new pending
                → Scheduler places new instance → Agent starts it
```

### Node crash → heartbeat expires → replace
```
Agent crashes → no more heartbeats
  → NodeFailureController reads lease/node/{id}
    → currentTime - heartbeatTime > 30s
      → PUT observed/node/{id}/state = "unreachable"
      → PUT derived/instance/{id}/node_failure = "true"
        → Agent on another node skips instance (isInstanceMarkedForSkip)
        → FailureController sees failed instances → replaces them
```

### Telemetry → cca top
```
Agent.CollectAndReportTelemetry (every 1s)
  → runtime.List → count running → PUT workload_count
  → runtime.Stats → CPU/memory → PUT telemetry/instance/{id}/cpu,memory
    → cca top → GET /api/status → Scan observed/telemetry/ → display
```

---

## Node State Machine

```
Agent startup
  PublishAliveState() with CAS
         │
         ▼
     ┌────────┐
  ┌──│  alive  │◀──────────── heartbeat resumes
  │  └──┬───┬──┘              (NodeFailureController
  │     │   │                   marks recovered → alive)
  │ lease│   │ cca drain
  │ 30s  │   │
  │     ▼   ▼
  │  ┌──────────┐
  │  │ draining  │ (graceful workload migration)
  │  └────┬─────┘
  │       │
  ▼       ▼
┌──────────────┐
│ unreachable   │ NodeFailureController owns this state
│ instances get  │ derived/instance/{id}/node_failure = "true"
│ node_failure   │ → FailureController replaces them
└──────────────┘

┌──────────────┐
│  disabled     │ cca node disable
│  alive but no │ excluded from scheduler
│  new placements│
└──────────────┘
```
