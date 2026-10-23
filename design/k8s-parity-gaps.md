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

### 3. Resource Limits Enforcement

**K8s**: Requests vs limits, OOM kill handling, eviction under pressure, QoS classes.
**CCattler**: Resource-aware scheduling (`scheduler/scheduler.go`) but containers run unbounded. No `--memory`/`--cpus` on nerdctl run. No OOM detection or eviction.
**Fix needed**: Pass resource limits to nerdctl run args, detect OOM kills from runtime status, implement node-pressure eviction.

### 4. Rolling Update MaxSurge

**K8s**: maxSurge, maxUnavailable, revision history, pause/resume.
**CCattler**: RolloutController with maxUnavailable and auto-rollback (`controllers/rollout.go`). No maxSurge, no revision history, no pause.
**Fix needed**: maxSurge (over-provision during update), revision history for multi-step rollback.

### 5. Config/Secret Reload

**K8s**: ConfigMap/Secret watch → pod restart or volume refresh, secret rotation.
**CCattler**: Config files bind-mounted at start (`runtime/container.go`). No watch, no reload, no rotation. Change requires new deployment.
**Fix needed**: Watch config facts, trigger instance restart or file refresh when config changes.

### 6. Pod Disruption Budgets

**K8s**: PDB minAvailable/maxUnavailable during voluntary disruption.
**CCattler**: Drain controller evicts during node drain, rollout has maxUnavailable. No explicit PDB for maintenance.
**Fix needed**: Explicit min-available constraint during maintenance/upgrades.

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

### 10. Events CLI

**K8s**: Event objects with reason/message, visible via `kubectl describe`.
**CCattler**: EventLog exists internally (`types/eventlog.go`) with decision logging. Not surfaced via CLI.
**Fix needed**: `cca events` command to surface decision log entries.
