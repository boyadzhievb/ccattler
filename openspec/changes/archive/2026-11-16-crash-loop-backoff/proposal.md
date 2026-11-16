# Proposal

## Why

Milestone **M233 — Crash-Looping Containers Restart Without Back-off or Count** (bug, milestones.md Phase 233), found in M232 from the MariaDB crash loop. Verified in the code:
- When a container exits, the agent's reconcile loop (`agent/agent.go`, the `!runtimeStatus.Running` branch) publishes the exit reason and calls `reconcileDesiredInstance` on the same cycle: the instance is restarted in place about every second, forever.
- Its state stays `running`; it never becomes `failed`, so the failure controller's crash-loop back-off and circuit breaker (`controllers/failure.go`, counts `failed` instances only) never apply.
- `observed/instance/{id}/restarts` is read by `cca describe` and `/api/status` but written nowhere, so every instance shows 0 restarts.

How others do it: Kubernetes' kubelet restarts a crashed container in place with exponential back-off (10 s doubling to 5 min, reset after 10 min of clean running), shows the pod as `CrashLoopBackOff` and counts `restartCount`. Nomad restarts in place a set number of times, then reschedules. The user chose the kubelet model (2026-11-16, option 1): a crash loop is almost always the image or its configuration, not the node, and node failures have their own controller.

## What Changes

- The agent restarts an exited container in place with exponential back-off per instance: 10 s, 20 s, 40 s … capped at 5 min; the back-off resets after the instance has run without exiting for 10 min.
- While it waits, the instance's observed state is the new `crash-loop-backoff`, with a status reason naming the exit and the next attempt ("exited with code 1; restarting in 40s"). It is not `failed`, so the failure controller does not replace it.
- Every in-place restart increments `observed/instance/{id}/restarts`, so `cca describe` and status show it.
- The exponential formula, which would now exist in three places (image pull back-off, the failure controller's crash-loop back-off, this one), becomes one shared helper.
- Verification only: a crash-looping container on the testbed releases its old IP on every restart (CNI `host-local` allocations do not grow) and leaves no stale VIP backend or eBPF identity entry. A failure becomes its own bug milestone.

Out of scope: keeping an instance's IP across in-place restarts — the user wants it kept, as its own milestone M237 right after M234 (decision 2026-11-16, option 2); until then each restart gets a new IP and this change verifies old IPs are released; rescheduling a crash-looping instance to another node; the previous run's log (M234).

## Capabilities

### New Capabilities
- `instance-restart`: how the agent restarts an instance whose container exits, how restarts are counted and shown, and when it backs off.

### Modified Capabilities
(none)

## Impact

- Code: `agent/agent.go` (exited-container branch, back-off state per instance, restart count), `types/state.go` (new state), `controllers/failure.go` and the image pull back-off (shared helper), any code that switches on instance states (checked during apply).
- Tests: agent tests with a short base delay (back-off, count, reset, state); a failure controller test that `crash-loop-backoff` is not replaced; testbed crash-loop check.
- Docs: a flow in design/runtime-invariant-flows.md (in-place restart with back-off), `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md`, the instance state list wherever it is documented.
- Behaviour change: a crashing container now waits up to 5 min between attempts instead of about 1 s, and shows `crash-loop-backoff` instead of `running`.
