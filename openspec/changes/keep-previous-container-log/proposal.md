# Proposal

## Why

Milestone **M234 — A Restart Deletes the Previous Container Log** (bug, milestones.md Phase 234), found in M232, verified in the code: restarting an exited container runs `ContainerdRuntime.Start` → `Stop` → `removeContainerResources`, which removes `/var/lib/ccattler/logs/<id>/` with the log of the run that just failed. `cca logs` then shows only the new run, so the error that caused the exit is never visible: MariaDB's `/run/mysqld` error in M231 was found only by running the image by hand. Since M233 crash-looping instances restart in place by design, so this hits every crash loop.

Also verified: `cca logs --previous` exists but means "an earlier instance of the service" (`findPreviousInstance`); that instance's log directory was deleted when it was removed, so the flag cannot show anything useful today.

How others do it: Kubernetes keeps the log of the previous container of the same pod and shows it with `kubectl logs --previous`; a third run drops the oldest. Docker keeps one log per container until it is removed.

## What Changes

- When an instance's container starts again on the same node (crash-loop restart, config-change restart), the log of the run before is kept as the instance's previous log; the new run writes a fresh log. Only one previous log is kept.
- Stopping a container for a restart no longer deletes the instance's log directory; it is removed when the instance leaves the node (removed, moved, replaced).
- `cca logs --previous` shows the previous run of the instance (Kubernetes' meaning). With no previous run it says so. The old meaning, an earlier instance of the service, is dropped: its logs never survived.
- The containerd runtime serves the previous log; the nerdctl runtime (`ContainerRuntime`) answers that a previous log is not available.

Out of scope: log size limits and rotation by size (the current run's log is unbounded today, unchanged); keeping logs of removed instances; the process and simulator runtimes beyond "not available".

## Capabilities

### New Capabilities
- `instance-logs`: which container logs of an instance the node keeps and how a user reads the current and the previous run.

### Modified Capabilities
(none)

## Impact

- Code: `runtime/containerd.go` (start rotates the log, stop keeps the directory, removal deletes it, `Logs` reads the previous log), `runtime/runtime.go` (`LogsOptions.Previous`, a removal hook for instance data), `runtime/container.go`, the agent's stale-instance cleanup paths, `agent/debug_api.go` (`previous` query parameter), `cmd/cca/command_status.go` (`--previous`).
- Tests: runtime unit tests for rotation and for reading the previous log; agent debug API passes the option; CLI request; testbed: `cca logs --previous` on a crash-looping MariaDB shows its entrypoint error.
- Docs: `design/node-agent.md` (log files), `design/cli-reference.md` and the website CLI reference for `--previous`, `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md`.
- Behaviour change: `--previous` now means the previous run of the same instance.
