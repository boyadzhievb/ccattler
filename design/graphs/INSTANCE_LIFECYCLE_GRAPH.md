# Instance Lifecycle Graph Reference

Machine-readable state machine reference for a single CCattler instance.
Purpose: trace why an instance is stuck in any state without reading source.

## Complete State Machine

```
                    ┌──────────────────────────────────────────┐
                    │          USER: cca apply                 │
                    └────────────────┬─────────────────────────┘
                                     │
                                     ▼
                          ┌─────────────────────┐
                          │  InstanceController  │
                          │  writes state=pending│
                          └──────────┬──────────┘
                                     │ observed/instance/{id}/state = "pending"
                                     ▼
                          ┌─────────────────────┐
                          │     Scheduler        │
                          │  writes placement    │
                          └──────────┬──────────┘
                                     │ placement/node/{nodeID}/{id}
                                     ▼
                    ┌────────────────────────────────────┐
                    │  AGENT: executeReconciliationCycle  │
                    │  findInstancesPlacedOnThisNode      │
                    │  → instance not in runningByID      │
                    │  → reconcileDesiredInstance          │
                    └────────────────┬───────────────────┘
                                     │
              ┌──────────────────────┼──────────────────────┐
              │                      │                      │
              ▼                      ▼                      ▼
     lookupServiceImage      ensureImageAvailable    (image empty?)
     returns ""?             ───────┬──────────     → silent return
     → BAIL (silent)               │                  (no state change)
                                   │
                    ┌──────────────┤
                    │              │
                    ▼              ▼
              IMAGE CACHED    IMAGE PULL
              (skip pull)     state="pulling"
                    │         ────────┬──────
                    │                │
                    │         ┌──────┴──────┐
                    │         │             │
                    │         ▼             ▼
                    │    PULL OK       PULL FAILED
                    │         │        state="image-pull-backoff"
                    │         │        → recordImagePullFailure
                    │         │        → BAIL (retry next cycle)
                    │         │
                    ▼         ▼
              ┌─────────────────────────┐
              │  hasInitSteps?          │
              │  ─────────┬─────────── │
              │  NO       │  YES       │
              └───┬───────┴────┬───────┘
                  │            │
                  │            ▼
                  │   executeInitializationSteps
                  │   ─────────┬───────────────
                  │            │ FOR EACH step (sequential):
                  │            │
                  │            ├── readInitStepState
                  │            │   "succeeded" → skip
                  │            │   "running" → mark failed (agent restart)
                  │            │
                  │            ├── check backoff
                  │            │   attemptCount >= maxAttempts → PERMANENT FAIL
                  │            │   time.Now < nextRetryAt → BAIL
                  │            │
                  │            ├── publishInitProgress
                  │            ├── write step state = "running"
                  │            │
                  │            ├── IF connection:
                  │            │   └── runConnectionProbe
                  │            │       ├── assembleConnectionURL
                  │            │       ├── resolveConnectionAddress (CCattler DNS)
                  │            │       └── net.Dialer.DialContext("tcp")
                  │            │
                  │            ├── IF exec:
                  │            │   └── runInitCommand
                  │            │       └── runtime.ExecInit(image, spec)
                  │            │
                  │            ├── SUCCESS → step state = "succeeded"
                  │            │             delete backoff entry
                  │            │             continue to next step
                  │            │
                  │            └── FAILURE → increment backoff
                  │                         publishInitProgress (with error)
                  │                         → BAIL (retry next cycle)
                  │
                  │ (all init steps passed → clearInitProgress)
                  │            │
                  ▼            ▼
              ┌─────────────────────────┐
              │  storageProvider?        │
              │  ─────────┬─────────── │
              │  NO       │  YES       │
              └───┬───────┴────┬───────┘
                  │            │
                  │            ▼
                  │   publishStatusReason("waiting for volumes")
                  │   ensureVolumesAttachedForInstance
                  │   ─────────┬───────────────
                  │            │
                  │   ┌────────┴────────┐
                  │   │                 │
                  │   ▼                 ▼
                  │  READY          NOT READY
                  │   │            → BAIL (revoke token)
                  │   │
                  ▼   ▼
              ┌─────────────────────────────────┐
              │  publishStatusReason("starting") │
              │  buildRuntimeSpecForInstance      │
              │  materializeSecretFiles           │
              │  materializeConnectionFiles       │
              │  runtime.Start(spec)              │
              └──────────────┬──────────────────┘
                             │
                    ┌────────┴────────┐
                    │                 │
                    ▼                 ▼
             START OK           START FAILED
                    │           state="failed"
                    │           publishStatusReason("start failed: {err}")
                    │           → revoke token
                    │           → return
                    │
                    ▼
              ┌─────────────────────────────────────┐
              │  retryBridgeAttachmentIfNeeded       │
              │  executeLifecycleHook(post_start)    │
              │  publishInstanceHostPorts            │
              │  runtime.Status → get IP             │
              │  observeInstanceState                │
              │  publishInstanceStateToStore          │
              │    state="running" (if Status OK)    │
              │    state="starting" (if Status err)  │
              │    state="failed" (if exit code != 0)│
              │  publishStatusReason("running")      │
              │  write observed image, config hash   │
              └──────────────────┬──────────────────┘
                                 │
              ┌──────────────────┤
              │                  │
              ▼                  ▼
         STATE=RUNNING      STATE=STARTING
              │             (runtime.Status returned error)
              │             (next reconcile cycle retries observation)
              │
              ▼
         ═══════════════════════════════════
         STEADY STATE (reconcile cycle, ~1s)
         ═══════════════════════════════════
              │
              ├── detectConfigChange
              │   → if changed: stop & re-enter reconcileDesiredInstance
              │
              ├── reconcileResourceChanges
              │   → live resize CPU/memory if supported
              │
              ├── republishHostPortIfNeeded
              │
              ├── observeInstanceState(runtime.Status)
              │   ├── Running: true  → InstanceRunning
              │   ├── Status error   → InstanceStarting
              │   └── ExitCode != 0  → InstanceFailed
              │
              └── publishInstanceStateToStore
                  publishExitReasonIfFailed
         ═══════════════════════════════════
         PROBE SCHEDULER (independent goroutine, ~5s tick)
         ═══════════════════════════════════
              │
              ├── performHealthCheckAndReportResult
              │   → write observed/instance/{id}/health
              │
              └── executeProbesForInstance
                  │
                  ├── STARTUP PROBE (gates liveness + readiness)
                  │   ├── pending → checking → succeeded | failed
                  │   └── must pass successThreshold times
                  │
                  ├── LIVENESS PROBE (only after startup succeeds)
                  │   ├── healthy | unhealthy
                  │   └── unhealthy → FailureController drains then replaces
                  │
                  ├── READINESS PROBE (only after startup succeeds)
                  │   ├── ready | not-ready
                  │   └── not-ready → removed from endpoints (no restart)
                  │
                  └── publishProbeProgress
                      updateSummaryHealthFromProbes
```

## InstanceState Values (types/state.go)

| State                | Value                | Written By | Meaning |
|---|---|---|---|
| `InstancePending`    | `"pending"`          | InstanceController | Created, awaiting scheduling |
| `InstancePulling`    | `"pulling"`          | Agent (ensureImageAvailable) | Image download in progress |
| `InstanceImagePullBackoff` | `"image-pull-backoff"` | Agent (recordImagePullFailure) | Pull failed, waiting for retry |
| `InstanceStarting`   | `"starting"`         | Agent (observeInstanceState) | runtime.Status returned error |
| `InstanceRunning`    | `"running"`          | Agent (observeInstanceState) | runtime.Status confirms alive |
| `InstanceFailed`     | `"failed"`           | Agent (observeInstanceState / startInstanceAndPublishState) | Non-zero exit or start error |
| `InstanceStopped`    | `"stopped"`          | Derived (controller_stopped marker) | Intentionally replaced |

### Effective State Resolution (types/codec.go:234)

```
IF controller_stopped == "true" → InstanceStopped
IF node_failure == "true"       → InstanceFailed
ELSE                            → observed/instance/{id}/state value
```

## Status Reason Timeline

Each `publishStatusReason` call writes to `observed/instance/{id}/status-reason`.
These are the exact strings and where they are produced:

| Lifecycle Phase | Status Reason String | Source |
|---|---|---|
| Image pull active | *(no status-reason written during active pull)* | — |
| Image pull failed | `"pulling image {image} (attempt {N}): {error}"` | agent.go:842 recordImagePullFailure |
| Init step (running) | `"init:{step}/{total} {label}"` | init.go:192 publishInitProgress |
| Init step (failed) | `"init:{step} {label} attempt {N}/{max}: {error}"` | init.go:190 publishInitProgress |
| Volume wait | `"waiting for volumes"` | agent.go:734 |
| Starting container | `"starting"` | agent.go:747 |
| Start failed | `"start failed: {error}"` | agent.go:935 |
| Running | `"running"` | agent.go:964 |

### Status Reason Gaps (no reason written)

1. **image == ""** — `lookupServiceImageFromStore` returns empty, agent silently returns.
   Status reason stays whatever it was from a previous cycle (or empty).
2. **Image cached, no pull needed** — status reason remains from previous cycle.
3. **Init phase complete** — `clearInitProgress` deletes init-progress but doesn't
   update status-reason. It stays as `"init:{last}/{total} {label}"` until `"starting"`.

## Probe Lifecycle Detail

### Probe Types (types/state.go:212-227)

| Type | Gate | Effect of Failure | Effect of Success |
|---|---|---|---|
| **Startup** | Blocks liveness + readiness | `StartupProbeFailed` → FailureController replaces | `StartupProbeSucceeded` → unlocks other probes |
| **Liveness** | Requires startup succeeded | `LivenessProbeUnhealthy` → FailureController drain+replace | `LivenessProbeHealthy` → healthy |
| **Readiness** | Requires startup succeeded | `ReadinessProbeNotReady` → removed from endpoints | `ReadinessProbeReady` → included in endpoints |

### Probe State Machine

```
                          STARTUP PROBE
                  ┌───────────────────────────┐
                  │                           │
                  ▼                           │
            [initialDelay]                    │
                  │                           │
                  ▼                           │
            ┌─────────┐    pass ≥ threshold   │
            │ pending  ├──────────────────► succeeded
            └────┬────┘                       │
                 │ fail ≥ threshold            │
                 ▼                             │
              failed                           │
              (FailureController replaces)     │
                                               │
                  ┌────────────────────────────┘
                  │ startup succeeded unlocks:
                  ▼
     ┌────────────────────────────┐
     │    LIVENESS + READINESS    │
     │     (run in parallel)      │
     └────────────┬───────────────┘
                  │
     ┌────────────┼────────────┐
     ▼                         ▼
  LIVENESS                  READINESS
  ├── [initialDelay]        ├── [initialDelay]
  ├── healthy (passing)     ├── ready (passing)
  └── unhealthy             └── not-ready
      (fail ≥ threshold)        (fail ≥ threshold)
      → drain_since marker      → removed from endpoints
      → grace period            → NOT restarted
      → replace
```

### Probe Configuration Defaults (agent/probes.go:20-36)

| Parameter | Default | Store Key |
|---|---|---|
| interval | 10s | `desired/service/{svc}/probe/{type}/interval` |
| timeout | 2s | `desired/service/{svc}/probe/{type}/timeout` |
| failureThreshold | 3 | `desired/service/{svc}/probe/{type}/failure_threshold` |
| successThreshold | 1 | `desired/service/{svc}/probe/{type}/success_threshold` |
| initialDelay | 0s | `desired/service/{svc}/probe/{type}/initial_delay` |

### Probe IP Prerequisite

Probes only run when `observed/instance/{id}/ip` is non-empty. If the IP fact
is missing (e.g. NetworkProvider failed to allocate), no probes execute for
that instance.

### Probe Progress Fact

`observed/instance/{id}/probe-progress` — JSON published by `publishProbeProgress`
only when any probe has `consecutiveFailures > 0`:

```json
{
  "startup_failures": 5,
  "startup_threshold": 30,
  "startup_error": "HTTP GET 10.100.1.5:8080/health failed",
  "liveness_failures": 0,
  "readiness_failures": 2,
  "readiness_threshold": 3,
  "readiness_error": "TCP 10.100.1.5:5432 connection failed"
}
```

Deleted when all probes are passing (no failures).

### Health Summary Derivation (probes.go:178-198)

```
IF startup NOT complete → no health update
IF liveness == unhealthy → health = unhealthy
IF readiness == not-ready → health = unhealthy
IF liveness == healthy OR readiness == ready → health = healthy
```

## Init Step Detail

### Init Step Types

| Type | Mechanism | Default Retries | Source |
|---|---|---|---|
| **exec** | `runtime.ExecInit(image, ExecSpec)` — runs in temp container from service image | 0 (no retries) | init.go:209 |
| **connection** | DNS resolve → TCP dial to host:port | 30 retries | init.go:280 (connectionProbeDefaultRetry) |

### Init Step Backoff (init.go:22-32)

| Parameter | Value |
|---|---|
| Base delay | 1s (`initStepBackoffBaseDelay`) |
| Max delay | 30s (`initStepMaxBackoffDelay`) |
| Formula | `1s * 2^(attemptCount-1)`, capped at 30s |
| Delays | 1s, 2s, 4s, 8s, 16s, 30s, 30s, 30s... |

### Init Step State Machine

```
            ┌──────────────────────────────────────┐
            │  readCurrentInitPhase                │
            │  phase == "complete" → skip all      │
            └───────────────┬──────────────────────┘
                            │
                            ▼
            FOR EACH step (sequential, 0-indexed):
            ┌──────────────────────────────────────┐
            │  readInitStepState                   │
            │  ├── "succeeded" → skip to next      │
            │  └── "running" → mark "failed"       │
            │      (agent restarted mid-step)       │
            └───────────────┬──────────────────────┘
                            │
                            ▼
            ┌──────────────────────────────────────┐
            │  Check backoff                       │
            │  ├── attemptCount >= maxAttempts      │
            │  │   → step state = "failed"         │
            │  │   → reason = "max retries exceeded"│
            │  │   → return false (PERMANENT FAIL)  │
            │  │                                    │
            │  └── time.Now < nextRetryAt           │
            │      → return false (BAIL, retry)    │
            └───────────────┬──────────────────────┘
                            │
                            ▼
            publishInitProgress (attempt N, no error)
            write step state = "running"
                            │
                 ┌──────────┴──────────┐
                 │                     │
                 ▼                     ▼
            connection            exec command
            runConnectionProbe    runInitCommand
                 │                     │
          ┌──────┴──────┐       ┌──────┴──────┐
          │             │       │             │
          ▼             ▼       ▼             ▼
       SUCCESS       FAILURE  SUCCESS      FAILURE
          │             │       │             │
          ▼             ▼       ▼             ▼
    state="succeeded"  attempt++  state="succeeded"  attempt++
    delete backoff     compute    delete backoff     compute
    → next step        backoff    → next step        backoff
                       delay                         delay
                       → BAIL                        → BAIL
```

### Init Phase Transitions (derived by InitController)

| Phase | Meaning | Key |
|---|---|---|
| `"pending"` | Not started yet | `derived/instance/{id}/init_phase` |
| `"running"` | At least one step in progress | `derived/instance/{id}/init_phase` |
| `"complete"` | All steps succeeded | `derived/instance/{id}/init_phase` |
| `"failed"` | A step permanently failed | `derived/instance/{id}/init_phase` |

### Init Progress Fact

`observed/instance/{id}/init-progress` — JSON published by `publishInitProgress`:

```json
{
  "step": 1,
  "total_steps": 3,
  "attempt": 5,
  "max_retries": 31,
  "step_label": "connection:airflow-db",
  "last_error": "connection \"airflow-db\": TCP dial to metadata-db:5432 failed: dial tcp: lookup metadata-db: no such host"
}
```

Deleted by `clearInitProgress` after all steps succeed.

### Connection Probe DNS Resolution (init.go:358-383)

```
resolveConnectionAddress(hostname)
├── IF hostname is IP literal → use directly
├── ELSE:
│   └── buildCCattlerResolver()
│       ├── IF advertiseAddress set:
│       │   └── custom resolver → UDP to advertiseAddress:53
│       └── ELSE:
│           └── net.DefaultResolver (SYSTEM DNS)
│   └── resolver.LookupHost(hostname)
│       → first resolved IP replaces hostname in dial address
└── net.Dialer.DialContext("tcp", resolvedIP:port)
```

## Failure and Restart Flow

### Failed Instance → Replacement (FailureController)

```
observed/instance/{id}/state = "failed"
        │
        ▼
FailureController.Reconcile
├── check crash-loop backoff
│   ├── crashLoopCounter exists AND now < retryAfter
│   │   → SKIP (delay replacement)
│   └── no backoff or expired
│       → proceed
│
├── stopAndReplace(instanceID, serviceName)
│   ├── PUT derived/instance/{id}/controller_stopped = "true"
│   ├── PUT observed/instance/{replacementID}/ = ""
│   ├── PUT observed/instance/{replacementID}/service = serviceName
│   └── PUT observed/instance/{replacementID}/state = "pending"
│
└── increment crash-loop counter for service
    PUT derived/crash-loop/{serviceName} = {"count":N,"since":now}
```

### Stopped Instance → Fact GC (InstanceGCController)

```
derived/instance/{id}/controller_stopped = "true"
AND service running >= desired count
AND now - created-at > retention (10m default)
        │
        ▼
InstanceGCController.Reconcile
├── DELETE observed/instance/{id}/*          (all sub-keys)
├── DELETE derived/instance/{id}/*           (controller_stopped, etc.)
├── DELETE placement/instance/{id}
└── DELETE placement/node/{nodeID}/{id}

Budget: 4 instance trees per cycle (~60 deletes)
Remaining instances converge in subsequent cycles.
```

Manual GC: `cca gc instances` → POST /api/gc (no retention check, immediate)

### Crash-Loop Backoff (controllers/failure.go:24-28, 365-373)

| Parameter | Value |
|---|---|
| Base delay | 10s (`crashLoopBaseDelay`) |
| Max delay | 5m (`crashLoopMaxDelay`) |
| Formula | `10s * 2^(count-1)`, capped at 5m |
| Delays | 10s, 20s, 40s, 80s, 160s, 300s, 300s... |

### Circuit Breaker (controllers/failure.go)

When crash-loop count reaches the threshold, the circuit breaker trips open.
Replacements are blocked for the full cooldown instead of normal backoff.
After cooldown, one probe replacement is allowed (half-open). If the probe
succeeds (service recovers), the counter is deleted (circuit closes). If it
fails, the circuit re-opens for another cooldown period.

| Parameter | Default | Constant |
|---|---|---|
| Threshold | 10 failures | `defaultCircuitBreakerThreshold` |
| Cooldown | 10m | `defaultCircuitBreakerCooldown` |

```
derived/crash-loop/{service} = {"count":N,"since":ms}
        │
        ├── count < threshold (normal backoff)
        │   └── delay = 10s * 2^(count-1), cap 5m
        │
        └── count >= threshold (circuit breaker)
            ├── now < since + cooldown
            │   → BLOCKED (circuit open, no replacement)
            │
            └── now >= since + cooldown
                → HALF-OPEN: allow one probe replacement
                  ├── probe succeeds → counter deleted (circuit closed)
                  └── probe fails → count++, since=now (circuit re-opens)
```

**CLI visibility:** `cca status` services table shows STATUS column:
- `OK` — no crash-loop
- `CrashLoop(N)` — backoff active, N failures so far
- `CircuitBroken(N)` — circuit tripped after N failures

### Startup Probe Failed → Replacement

```
observed/instance/{id}/probe/startup = "failed"
AND state = "running"
        │
        ▼
FailureController.Reconcile
└── stopAndReplace(instanceID, serviceName)
    (same as above — immediate, no crash-loop check)
```

### Liveness Probe Failed → Drain → Replacement

```
observed/instance/{id}/probe/liveness = "unhealthy"
AND state = "running"
        │
        ▼
FailureController.handleUnhealthyLiveness
├── IF no drain_since marker:
│   └── PUT drain_since = now (begin grace period)
│
├── IF drain_since exists AND grace period elapsed:
│   └── stopAndReplace (same as failed-instance flow)
│
└── IF liveness recovers while draining:
    └── clearDrainState (remove drain_since marker)
```

### Image Pull Failure Backoff (agent/agent.go:50-57, 806-843)

| Parameter | Value |
|---|---|
| Base delay | 10s (`imagePullBaseDelay`) |
| Max delay | 5m (`imagePullMaxDelay`) |
| Pull timeout | 5m per attempt (`imagePullTimeout`) |
| Formula | `10s * 2^(attempts-1)`, capped at 5m |
| State published | `InstanceImagePullBackoff` during backoff |

### Node Failure → Rescheduling

```
Node heartbeat expires (NodeFailureController)
        │
        ▼
PUT derived/instance/{id}/node_failure = "true"
(for every instance on the unreachable node)
        │
        ▼
effectiveInstanceState → InstanceFailed
        │
        ▼
FailureController → stopAndReplace
(new instance scheduled to different node)
```

### Token Revocation

Secret API tokens are revoked in three places:
1. **Init failure** — agent.go:728 (before BAIL)
2. **Volume attach failure** — agent.go:741 (before BAIL)
3. **Start failure** — agent.go:932 (in startInstanceAndPublishState)
4. **Cleanup** — agent.go:996 (cleanupUndesiredInstance)

## Store Key Timeline (Fresh Deploy of One Instance)

Ordered sequence of store key writes from `cca apply` to fully running instance.
Controller writes are in **bold**, agent writes are in `code`.

### Phase 1: Control Plane

| # | Key | Value | Writer |
|---|---|---|---|
| 1 | **desired/service/{svc}** | `""` | cca apply |
| 2 | **desired/service/{svc}/image** | `"my-app:1.0"` | cca apply |
| 3 | **desired/service/{svc}/instances** | `"3"` | cca apply |
| 4 | **desired/service/{svc}/init/0** | `"0"` | cca apply (if init steps) |
| 5 | **desired/service/{svc}/init/0/connection** | `"my-db"` | cca apply (if connection init) |
| 6 | **observed/instance/{id}** | `""` | InstanceController |
| 7 | **observed/instance/{id}/service** | `"{svc}"` | InstanceController |
| 8 | **observed/instance/{id}/state** | `"pending"` | InstanceController |
| 9 | **placement/instance/{id}** | `"{nodeID}"` | Scheduler |
| 10 | **placement/node/{nodeID}/{id}** | `""` | Scheduler |
| 11 | **derived/instance/{id}/init_phase** | `"pending"` | InitController (if init steps) |

### Phase 2: Agent (reconcileDesiredInstance)

| # | Key | Value | Writer |
|---|---|---|---|
| 12 | `observed/instance/{id}/state` | `"pulling"` | ensureImageAvailable |
| 13 | `observed/instance/{id}/init/{step}/state` | `"running"` | executeInitStep |
| 14 | `observed/instance/{id}/init-progress` | `{JSON}` | publishInitProgress |
| 15 | `observed/instance/{id}/status-reason` | `"init:0/2 connection:my-db"` | publishInitProgress |
| 16 | `observed/instance/{id}/init/{step}/state` | `"succeeded"` | executeInitStep |
| 17 | `observed/instance/{id}/status-reason` | `"starting"` | publishStatusReason |
| 18 | `observed/instance/{id}` | `""` | publishInstanceStateToStore |
| 19 | `observed/instance/{id}/service` | `"{svc}"` | publishInstanceStateToStore |
| 20 | `observed/instance/{id}/state` | `"running"` | publishInstanceStateToStore |
| 21 | `observed/instance/{id}/node` | `"{nodeID}"` | publishInstanceStateToStore |
| 22 | `observed/instance/{id}/ip` | `"10.100.1.5"` | publishInstanceStateToStore |
| 23 | `observed/instance/{id}/status-reason` | `"running"` | publishStatusReason |
| 24 | `observed/instance/{id}/image` | `"my-app:1.0"` | startInstanceAndPublishState |
| 25 | `observed/instance/{id}/config-hash` | `"a1b2c3..."` | startInstanceAndPublishState |

### Phase 3: Probes (ProbeScheduler, async)

| # | Key | Value | Writer |
|---|---|---|---|
| 26 | `observed/instance/{id}/probe/startup` | `"pending"` | executeStartupProbe |
| 27 | `observed/instance/{id}/probe-progress` | `{JSON}` | publishProbeProgress (if failures) |
| 28 | `observed/instance/{id}/probe/startup` | `"succeeded"` | executeStartupProbe |
| 29 | `observed/instance/{id}/health` | `"healthy"` | performHealthCheckAndReportResult |
| 30 | `observed/instance/{id}/probe/liveness` | `"healthy"` | executeLivenessProbe |
| 31 | `observed/instance/{id}/probe/readiness` | `"ready"` | executeReadinessProbe |

### Phase 4: Endpoint Creation (EndpointController, async)

| # | Key | Value | Writer |
|---|---|---|---|
| 32 | **derived/endpoint/{svc}/{id}** | `"{ip}:{port}"` | EndpointController |

## Diagnostic: "Why Is My Instance Stuck?"

### Stuck in `pending`

1. **InstanceController hasn't run** — check `observed/instance/{id}/state` exists
2. **Scheduler can't place** — check `derived/unplaced/{id}` for reason:
   - `insufficient_capacity` — no node has enough CPU/memory
   - `unsatisfiable_constraint` — no node matches placement labels
   - `no_nodes` — no alive nodes in cluster
3. **Stuck in scheduler queue** — scheduler watches `observed/instance/`, may be slow

### Stuck in `pulling`

1. **Image pull in progress** — check status-reason for the image name
2. **Registry unreachable** — node can't reach container registry
3. **Authentication failure** — check image_pull_secret configuration

### Stuck in `image-pull-backoff`

1. **Check pull-status fact** — `observed/instance/{id}/pull-status`
   ```json
   {"error":"...","attempts":5,"retryAfter":"2026-10-27T14:00:00Z","image":"..."}
   ```
2. **Bad image reference** — typo in image name, tag doesn't exist
3. **Registry auth expired** — secret needs rotation

### Stuck in `starting`

1. **runtime.Status() returning error** — the container started but
   `observeInstanceState` at agent.go:1674-1677 silently returns
   `InstanceStarting` on any Status error. The container may actually be running.
2. **Container exited immediately** — Started then crashed before next observe.
   Status sees no running task → returns error → stays `starting`.
3. **etcd timeout** — `publishInstanceStateToStore` PUT fails silently.
   State in etcd still shows previous value.

**Debug checklist:**
```
ctr -n ccattler task ls          # is container actually running?
ctr -n ccattler container ls     # does container exist?
cca watch observed/instance/{id} # what does etcd say?
```

### Stuck in init step (status-reason shows `init:N/M`)

1. **Connection probe failing** — target service not running or DNS not resolving
   - Check if target service has `observed/instance/{id}/ip` set
   - Check if DNS forwarder is running on the node
   - Check `init-progress` fact for specific error
2. **Exec step failing** — init command returning non-zero
   - Check `observed/instance/{id}/init/{step}/reason` for error
3. **Backoff** — waiting between retries
   - Init backoff: 1s, 2s, 4s, 8s, 16s, 30s cap
   - Connection default: 30 retries = max ~15 min
4. **Max retries exceeded** — step state = "failed", init aborted permanently

### Running but not `ready`

1. **Startup probe pending** — waiting for startup probe to succeed
   - Check `observed/instance/{id}/probe/startup`
   - Check `observed/instance/{id}/probe-progress` for failure count
2. **Readiness probe failing** — service running but not ready for traffic
   - Check probe-progress for readiness errors
   - Instance stays running, just removed from endpoints

### Running but `unhealthy`

1. **Liveness probe failing** — check probe-progress
   - After failureThreshold consecutive failures → drain → replace
2. **Legacy health check** — `health { tcp/http }` block in DSL
   - Separate from probes, simpler (no threshold, just pass/fail)

## observeInstanceState Decision Table (agent.go:1674-1690)

| runtime.Status() | Status.Running | Status.ExitCode | Status.Error | Result |
|---|---|---|---|---|
| error | — | — | — | `InstanceStarting` |
| nil | true | — | — | `InstanceRunning` |
| nil | false | != 0 | any | `InstanceFailed` (exit reason from error or `exit(N)`) |
| nil | false | != 0 | "" | `InstanceFailed` (exit reason `exit(N)`) |
| nil | false | 0 | non-empty | `InstanceFailed` (exit reason from error) |
| nil | false | 0 | "" | `InstanceStarting` |
