# Kubernetes Parity Gaps

Identified 2026-10-23. Feature gaps between CCattler and Kubernetes that affect real-workload readiness.

## Critical

### 1. Image Pull Lifecycle — DONE

**K8s**: Pull policies (Always/IfNotPresent/Never), ImagePullBackOff retry loop, image pull secrets.
**CCattler before**: `nerdctl run` pulled inline; failure → instant `failed` state → infinite replacement loop.
**Fix**: `pulling` and `image-pull-backoff` instance states, pre-pull with exponential backoff (10s→5min), `--pull=never` on container start, pull-status fact for visibility.
**Files**: `agent/agent.go`, `runtime/container.go`, `runtime/simulator.go`, `types/state.go`, `types/keys.go`.
**Remaining**: Pull policies (Always/IfNotPresent/Never), image pull secrets (registry auth).

### 2. Crash Loop Backoff

**K8s**: CrashLoopBackOff with exponential backoff (10s→5min), restart count tracking.
**CCattler**: Failed instance → immediate replacement by FailureController (`controllers/failure.go`). No backoff. A container that crashes on start gets replaced infinitely at full speed.
**Fix needed**: Exponential backoff on repeated failures per service+node, restart count, backoff cap. The failure controller should delay replacement when the same service has failed N times recently.

## Important

### 3. Resource Limits Enforcement — DONE

**K8s**: Requests vs limits, OOM kill handling, eviction under pressure, QoS classes.
**CCattler**: Resource limits already wired end-to-end: DSL → scheduler → agent → `--cpus`/`--memory` on nerdctl run. Container runtime `Status()` now parses ExitCode and OOMKilled from nerdctl inspect. Agent publishes `exit-reason` fact ("OOMKilled" or "exit(N)").
**Files**: `runtime/container.go`, `agent/agent.go`, `types/keys.go`.
**Remaining**: Node-pressure eviction, QoS classes (BestEffort/Burstable/Guaranteed).

### 4. Rolling Update MaxSurge — DONE

**K8s**: maxSurge, maxUnavailable, revision history, pause/resume.
**CCattler**: RolloutController with maxUnavailable, auto-rollback, and maxSurge (`controllers/rollout.go`). DSL `update { max_extra N }` emits a surge allowance fact during rolling updates. The instance controller reads it and temporarily increases effective desired count, pre-creating new-image instances before old ones are stopped. Surge is cleared on rollout completion or rollback.
**Files**: `controllers/rollout.go`, `controllers/instance.go`, `types/keys.go`.
**Remaining**: Revision history for multi-step rollback, pause/resume.

### 5. Config/Secret Reload — DONE

**K8s**: ConfigMap/Secret watch → pod restart or volume refresh, secret rotation.
**CCattler**: Agent computes SHA256 hash of all config inputs (env vars, config files, secrets) at start time, stores as `observed/instance/{id}/config-hash`. On each steady-state cycle, recomputes and compares — if different, stops and restarts the container with new config.
**Files**: `agent/agent.go`, `types/keys.go`.
**Remaining**: Secret rotation (automatic re-encryption with new master key).

### 6. Pod Disruption Budgets — DONE (pre-existing)

**K8s**: PDB minAvailable/maxUnavailable during voluntary disruption.
**CCattler**: Already implemented. DSL `disruption { min_available N }` or `max_unavailable N`. Drain controller enforces budget during node drain (`canEvictUnderDisruptionBudget`). Rollout controller constrains maxUnavailable via `applyDisruptionBudgetToUpdatePolicy`. 14 tests in `drain_test.go`.

## Nice-to-Have

### 7. Container Lifecycle Hooks

**K8s**: postStart, preStop hooks, SIGTERM+grace period.
**CCattler**: SIGTERM→SIGKILL grace period in process runtime. Init containers supported. No postStart/preStop hooks.

### 8. Job/Batch Workloads

**K8s**: Jobs (run-to-completion), CronJobs, parallelism, completions.
**CCattler**: None — all services are long-running.

### 9. Log Rotation

**K8s**: Container log rotation, `kubectl logs --previous`.
**CCattler**: `cca logs --follow` streams via nerdctl. No rotation config, no previous-container logs.

### 10. Events CLI — DONE (pre-existing)

**K8s**: Event objects with reason/message, visible via `kubectl describe`.
**CCattler**: Already implemented. `cca events [--follow] [--service <name>]` queries the EventLog via `/api/logs` and `/api/events/stream` SSE. EventProjector in `controllers/event_projector.go` maps store changes to semantic events.
