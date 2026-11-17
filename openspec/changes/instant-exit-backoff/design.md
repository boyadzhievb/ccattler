# Design

## Context

- Flow 33 (design/runtime-invariant-flows.md, M233) is the flow this change extends; its "known limit" note (an exit observed right after a start is published `failed`) goes away.
- `reconcileInstanceWithoutRunningContainer` (agent/crash_loop_backoff.go) → `reconcileDesiredInstance` → `startInstanceAndPublishState` (agent/agent.go). After `runtime.Start`, `observeInstanceState` returns `failed` for an exited container with a non-zero code or runtime error; a `runtime.Start` error is published `failed` with "start failed: …". `recordInstanceStarted` runs only after a real start (M233 rework).
- `runtime.StartError{ID, Reason}` (runtime/process.go) is returned by containerd for: mount validation, image not found, log dir, CNI init, netns, create container, CNI setup, create task, start task.
- The failure controller replaces `failed` instances (controllers/failure.go) with its own per-service back-off and circuit breaker.
- Graphs: INSTANCE_LIFECYCLE_GRAPH.md (start path, section 12b), OBSERVABILITY_GRAPH.md (Container Logs).

## Goals / Non-Goals

**Goals:** both instant cases go through Flow 33 in place; infrastructure start failures keep being replaced.

**Non-Goals:** rescheduling crash loops; retrying infrastructure failures in place.

## Decisions

### D1. The first look after a start never publishes `failed`
In `startInstanceAndPublishState`, an exited container observed right after a successful start is published as `starting` (not `failed`, no exit-reason fact from this path); the next cycle sees the exited container and Flow 33 records the exit (`recordInstanceExit`: back-off, `crash-loop-backoff`, exit reason). The instance was really started, so `recordInstanceStarted` runs as today and the restart count stays correct. Alternative (record the exit right away from the start path): saves one cycle but duplicates Flow 33's entry handling; rejected.

### D2. `StartError` says whether the workload's process failed (revised 2026-11-17)
Finding during apply (lead-developer, probed with runc 1.1.15): a missing or non-executable entrypoint fails in `runc create`, i.e. containerd `NewTask` → our `create task: …` error; an exec-format error passes create and start and exits at once (the D1 case). So the original "only `start task`" rule would miss the main case. User decision (option 1): `runtime.StartError` gets `ProcessFailed bool`; containerd sets it for a `create task` or `start task` failure whose error text contains runc's exec failure (`exec:` within `unable to start container process`), through one small classification function with a test against the real runc message texts. Every other start error (image, CNI, netns, create container, other task failures: cgroup, shim, seccomp) leaves it false and stays `failed`.

### D3. The agent records the exit from the start path (revised)
After a `create task` failure no task exists, so a kept container would report "exited, code 0, no error" and Flow 33 could not name the cause. Containerd therefore cleans up as today (container and network removed), and the agent, on a start error with `ProcessFailed`, records the exit itself through Flow 33's entry handling (`recordInstanceExit` with the start error as the reason): back-off, `crash-loop-backoff`, restart count on the next real attempt, never `failed`. No log file exists for such a run (the process never ran), so `cca logs --previous` shows the run before, if any.

### D4. The running path never publishes `failed` from a lost race (added)
Found by the lead-developer: in `executeReconciliationCycle`, a container that `List` reported running but that exits before `observeInstanceState` is published `failed` — another way around Flow 33. The running path publishes the exit as `starting`-equivalent no-op (no `failed`, no exit reason) and leaves it to the next cycle, where the container is seen exited and Flow 33 records it. Test: `TestAgentRunningPathRaceDoesNotPublishFailed`.

### D5. A crash-looping new image counts toward rollout auto-rollback (added, user decision 2026-11-17)
The rollout controller counted only `failed` new-image instances toward the rollback threshold, so a crash-looping new image stalled the rollout forever; `isInstanceFailedForRollout` (controllers/rollout.go) now also counts `crash-loop-backoff` with `observed/instance/{id}/restarts` ≥ `rolloutCrashLoopRestartThreshold` (3), for the rollback threshold and for old-image `currentUnavailable` in `buildOldInstanceStopChanges`. Tests: `TestRolloutRollsBackWhenNewImageCrashLoops`, `TestRolloutDoesNotRollBackForFewCrashLoopRestarts`, `TestRolloutCountsCrashLoopingOldInstancesAsUnavailable`.

### D6. Health summaries show crash-loop-backoff (added, user decision 2026-11-17)
The RUNTIME HEALTH summary of `cca status` (`renderRuntimeHealthSummary`, cmd/cca/command_server.go) and Check 6 of test/e2e/health_check.sh count `crash-loop-backoff` on its own as unhealthy, not merged into failed. Test: `TestStatusTextCountsCrashLoopBackoffInRuntimeHealth`.

## Risks / Trade-offs

Failure cases and what happens:
- **A start failure that looks like a process failure but is node-specific** (e.g. a missing kernel feature making exec fail): it now loops in place on that node instead of being replaced elsewhere. Same as Kubernetes; the node-failure controller and draining still move instances off a broken node.
- **Agent restart between the start and the next cycle**: the exited container is found by `List`; Flow 33 restarts it once at once (no entry), as for any exit after an agent restart.
- **The process runtime and nerdctl runtime** do not set `ProcessFailed`: their start failures stay `failed` (unchanged).
- **Exit code 0 right after start**: already `starting` today; unchanged, Flow 33 handles it on the next cycle.
