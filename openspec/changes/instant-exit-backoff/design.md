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

### D2. `StartError` says whether the workload's process failed
`runtime.StartError` gets `ProcessFailed bool` (field comment: the runtime created the workload but its process could not start). Containerd sets it only for the task start failure; others leave it false. In the agent, a start error with `ProcessFailed` is treated as an exit of a real start attempt: the container stays tracked as exited (no `failed`), the next cycle's Flow 33 path records it with the start error as the reason. Without it, the current `failed` path stays.

### D3. Containerd keeps the failed task's container
For a task start failure containerd must leave the container (and its log) in place so `Status` reports it exited and Flow 33 sees it on the next cycle (today the error path deletes the container and tears down the network). If keeping it is not possible without leaking the netns, the agent records the exit directly from the start path for this case instead (fallback; decide during apply and record it).

## Risks / Trade-offs

Failure cases and what happens:
- **A start failure that looks like a process failure but is node-specific** (e.g. a missing kernel feature making exec fail): it now loops in place on that node instead of being replaced elsewhere. Same as Kubernetes; the node-failure controller and draining still move instances off a broken node.
- **Agent restart between the start and the next cycle**: the exited container is found by `List`; Flow 33 restarts it once at once (no entry), as for any exit after an agent restart.
- **The process runtime and nerdctl runtime** do not set `ProcessFailed`: their start failures stay `failed` (unchanged).
- **Exit code 0 right after start**: already `starting` today; unchanged, Flow 33 handles it on the next cycle.
