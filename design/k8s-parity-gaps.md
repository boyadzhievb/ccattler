# Kubernetes Parity Gaps

Identified 2026-10-23. Feature gaps between CCattler and Kubernetes that affect real-workload readiness.

## Critical

### 1. Image Pull Lifecycle — DONE

**K8s**: Pull policies (Always/IfNotPresent/Never), ImagePullBackOff retry loop, image pull secrets.
**CCattler**: `pulling` and `image-pull-backoff` instance states, pre-pull with exponential backoff (10s→5min), `--pull=never` on container start, pull-status fact for visibility. Pull policies supported via DSL `pull_policy always|if_not_present|never` — agent skips pull for "never", bypasses cache for "always", default "if_not_present".
**Files**: `agent/agent.go`, `runtime/container.go`, `runtime/simulator.go`, `types/state.go`, `types/keys.go`.
**Remaining**: Image pull secrets (registry auth).

### 2. Crash Loop Backoff — DONE

**K8s**: CrashLoopBackOff with exponential backoff (10s→5min), restart count tracking.
**CCattler**: Failure controller tracks per-service crash-loop counters (`derived/crash-loop/{service}`). Exponential backoff (10s→5min) delays replacement when the same service fails repeatedly. Circuit breaker trips after 10 consecutive failures — blocks replacements for 10m cooldown, then allows one probe replacement (half-open). Counter resets when the service recovers to running state. `cca status` shows CrashLoop(N)/CircuitBroken(N) per service.
**Files**: `controllers/failure.go`, `controllers/topological_sort.go`, `types/keys.go`.

## Important

### 3. Resource Limits Enforcement — DONE

**K8s**: Requests vs limits, OOM kill handling, eviction under pressure, QoS classes.
**CCattler**: Resource limits wired end-to-end: DSL `resources { cpu "500m" memory "512Mi" cpu_limit "1000m" memory_limit "1Gi" }` → scheduler → agent → `--cpus`/`--memory` on nerdctl run. Separate request/limit fields. QoS classes derived at compile time: Guaranteed (request==limit), Burstable (partial resources), BestEffort (none). OOM detection via nerdctl inspect ExitCode/OOMKilled.
**Files**: `runtime/container.go`, `agent/agent.go`, `lang/ast.go`, `lang/compiler.go`, `types/keys.go`.
Node-pressure eviction controller (`controllers/eviction.go`) monitors per-node memory utilization and evicts instances in QoS priority order (BestEffort → Burstable → Guaranteed) when usage exceeds the hard 90% threshold.
**Files**: `runtime/container.go`, `agent/agent.go`, `lang/ast.go`, `lang/compiler.go`, `controllers/eviction.go`, `types/keys.go`.

### 4. Rolling Update MaxSurge — DONE

**K8s**: maxSurge, maxUnavailable, revision history, pause/resume.
**CCattler**: RolloutController with maxUnavailable, auto-rollback, maxSurge, revision history, and pause/resume. DSL `update { max_extra N }` emits surge allowance. Revision history stores previous images at `derived/service/{name}/rollout/history/{revision}` for multi-step rollback. Pause via `desired/service/{name}/rollout/paused` stops old instance replacement while keeping surge instances running.
**Files**: `controllers/rollout.go`, `controllers/instance.go`, `types/keys.go`.

### 5. Config/Secret Reload — DONE

**K8s**: ConfigMap/Secret watch → pod restart or volume refresh, secret rotation.
**CCattler**: Agent computes SHA256 hash of all config inputs (env vars, config files, secrets) at start time. On steady-state cycle, recomputes and compares — if different, stops and restarts with new config. Secret rotation library exists (`security/secrets.go:RotateKeyProvider`).
**Files**: `agent/agent.go`, `security/secrets.go`, `types/keys.go`.
`cca secret rotate` CLI command generates a new master key and re-wraps all DEKs via the `/api/secret/rotate` API endpoint. New nodes receive the rotated key at enrollment.
**Files**: `agent/agent.go`, `security/secrets.go`, `api/server.go`, `cmd/cca/command_status.go`, `types/keys.go`.

### 6. Pod Disruption Budgets — DONE (pre-existing)

**K8s**: PDB minAvailable/maxUnavailable during voluntary disruption.
**CCattler**: Already implemented. DSL `disruption { min_available N }` or `max_unavailable N`. Drain controller enforces budget during node drain (`canEvictUnderDisruptionBudget`). Rollout controller constrains maxUnavailable via `applyDisruptionBudgetToUpdatePolicy`. 14 tests in `drain_test.go`.

## Nice-to-Have

### 7. Container Lifecycle Hooks — DONE

**K8s**: postStart, preStop hooks, SIGTERM+grace period.
**CCattler**: DSL `lifecycle { pre_stop "cmd" post_start "cmd" }`. Agent executes pre_stop via `runtime.Exec` before every `runtime.Stop` call (scale-down, config change, resize). Post-start executed after successful `runtime.Start`. Hooks are best-effort with 30s timeout — failures logged but don't block the operation.
**Files**: `agent/agent.go`, `lang/ast.go`, `lang/parser.go`, `lang/compiler.go`, `types/keys.go`.

### 8. Job/Batch Workloads — DONE

**K8s**: Jobs (run-to-completion), CronJobs, parallelism, completions.
**CCattler**: DSL `job "name" { image "img" command "cmd" completions N parallelism N backoff_limit N }`. JobController creates instances that run to completion, tracks succeeded/failed counts, marks job "complete" when completions met or "failed" when backoff limit exceeded. Failure controller skips job-owned instances.
**Files**: `controllers/job.go`, `lang/ast.go`, `lang/parser.go`, `lang/compiler.go`, `lang/formatter.go`, `types/keys.go`.
**Remaining**: CronJobs (scheduled recurring jobs).

### 9. Log Rotation — DONE

**K8s**: Container log rotation, `kubectl logs --previous`.
**CCattler**: DSL `logging { max_size "10m" max_files 3 }`. Agent passes `--log-driver json-file --log-opt max-size=X --log-opt max-file=Y` to nerdctl run when configured.
**Files**: `runtime/container.go`, `runtime/runtime.go`, `lang/ast.go`, `lang/parser.go`, `lang/compiler.go`, `types/keys.go`.
**Remaining**: `cca logs --previous` (requires tracking previous container ID).

### 10. Events CLI — DONE (pre-existing)

**K8s**: Event objects with reason/message, visible via `kubectl describe`.
**CCattler**: Already implemented. `cca events [--follow] [--service <name>]` queries the EventLog via `/api/logs` and `/api/events/stream` SSE. EventProjector in `controllers/event_projector.go` maps store changes to semantic events.
