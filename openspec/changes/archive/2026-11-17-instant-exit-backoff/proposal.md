# Proposal

## Why

Milestone **M238 — Instant Exit Bypasses the Crash-Loop Back-off** (bug, milestones.md Phase 238), found in the M233 code review and verified in the code. M233 (Flow 33) restarts an exited container in place with back-off and shows `crash-loop-backoff`. Two ways around it remain:
1. **Exit before the first look.** `startInstanceAndPublishState` (agent/agent.go) calls `observeInstanceState` right after a successful `runtime.Start`; a container that has already exited with a non-zero code (`exit 1` within milliseconds, a script that fails at once) is published as `failed`. The failure controller then **replaces** the instance (new ID, new placement, fresh secret delivery) instead of the agent restarting it in place.
2. **The process cannot start.** A bad entrypoint or missing binary fails inside `runtime.Start` itself (containerd: `start task: …`, the task cannot exec); the agent publishes `failed` with "start failed", and the instance is replaced the same way.

In both cases the restart count never grows, the user sees `failed` and churning instance IDs instead of `crash-loop-backoff`, and `cca logs --previous` (M234) has nothing to show because each replacement is a new instance.

How others do it: Kubernetes treats both (a container that exits at once, and `RunContainerError` from a failed exec) as container restarts of the same pod with `CrashLoopBackOff`; only scheduling and node problems lead to a new pod.

## What Changes

- A container that has already exited when the agent first looks at it after a successful start is handled like any other exit (Flow 33): in place, with back-off, counted, `crash-loop-backoff`, never `failed` from that observation.
- A start that fails because the workload's process could not start (the runtime's task start failed: bad entrypoint, missing binary, exec format) is handled the same way, with the start error as the reason.
- Start failures that are not the workload's own (image not found, network setup, creating the container or task) stay `failed`, so the failure controller can still replace the instance, possibly on another node.

Out of scope: classifying start errors of the nerdctl and process runtimes beyond the same rule where they report it; rescheduling a crash-looping instance to another node.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `instance-restart`: instant exits and process start failures are restarted in place with back-off; infrastructure start failures still lead to replacement.

## Impact

- Code: `agent/agent.go` (`startInstanceAndPublishState`, the state observed right after a start), `agent/crash_loop_backoff.go` (record the exit from the start path), `runtime/process.go` (`StartError` says whether the workload's process failed), `runtime/containerd.go` (sets it for the task start failure).
- Tests: agent tests for both cases (same instance ID, `crash-loop-backoff`, restart count, no `failed`) and for an infrastructure start failure (still `failed`); runtime test for the classification.
- Docs: Flow 33 (remove the known limit), INSTANCE_LIFECYCLE_GRAPH.md, OBSERVABILITY_GRAPH.md Container Logs (previous log now kept for these cases).
- Testbed: a service whose command fails at once and one whose entrypoint does not exist.
