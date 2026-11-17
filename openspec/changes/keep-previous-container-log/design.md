# Design

## Context

- `ContainerdRuntime.Start` (`runtime/containerd.go`): when the instance's container is still tracked but not running, it calls `Stop` to delete the old container and snapshot before creating the new one. `Stop` and `StopWithGracePeriod` end in `removeContainerResources`, which forgets tracking state and `os.RemoveAll(ContainerdLogBaseDir/<id>)`.
- The log directory `/var/lib/ccattler/logs/<id>/` holds `<id>-json.log` (the container's stdout/stderr via `cio.LogFile`) and `config/` (hosts, resolv.conf, config files, rewritten on every start). The containerd shim opens the log file with `O_APPEND`, so keeping the file across a restart without rotation would mix two runs.
- The agent stops containers for: crash-loop restart (M233, via `Start`), config change (`detectConfigChange` → `Stop`, next cycle starts), removal (`cleanupUndesiredInstance`, `cleanupStoppedStaleInstance`).
- `cca logs` → API debug proxy → agent `handleDebugLogs` → `runtime.Logs(ctx, id, LogsOptions)`. `--previous` today resolves an earlier instance ID of the service (`findPreviousInstance`).
- Flow: M233's Flow 33 (in-place restart) is where the previous log is kept; documented in design/node-agent.md (log files) and the lifecycle graph.

## Goals / Non-Goals

**Goals:** the failed run's log survives any restart on the node; one previous log; `cca logs --previous` reads it; logs leave with the instance.

**Non-Goals:** size-based rotation or limits; logs of removed instances; previous logs for the nerdctl, process and simulator runtimes.

## Decisions

### D1. Rotate at start, not at stop
`Start` renames an existing `<id>-json.log` to `<id>-json.log.previous` (replacing an older previous) before it creates the task, so every way of restarting (crash loop, config change, a stop by hand) keeps the run before, and the new run starts with an empty file. Rotating at stop would miss stops that do not go through the runtime's restart path. A failed rename is logged and the start continues (losing the previous log is better than not starting).

### D2. Stop keeps the directory; removal deletes it
`removeContainerResources` keeps forgetting tracking state but no longer deletes the log directory. A new runtime method `RemoveInstanceData(ctx, instanceID)` deletes it; the agent calls it in `cleanupUndesiredInstance` and `cleanupStoppedStaleInstance` after the stop. Other runtimes implement it as a no-op (nerdctl removes its own container logs; process and simulator keep none).

### D3. `LogsOptions.Previous`
`runtime.LogsOptions` gets `Previous bool`. `ContainerdRuntime.Logs` reads `<id>-json.log.previous` when set (tail applies — the static reader ignores since and timestamps, for the current log too, pre-existing; follow is refused with Previous: a finished run does not grow; the instance ID must be a plain name). No previous file → error "instance <id> has no previous run". Other runtimes return "previous log not available on this runtime". `handleDebugLogs` maps the query parameter `previous=true`.

### D4. `cca logs --previous` means the previous run
The CLI sends `previous=true` for the selected instance (same instance selection as without the flag) instead of looking up an earlier instance ID; `findPreviousInstance` goes if nothing else uses it.

## Risks / Trade-offs

Failure cases and what happens:
- **Directories left behind** — a graceful agent stop (`StopAll` deletes containers but no longer the logs), a removal while the agent is down, an agent crash between removal and `RemoveInstanceData`: the agent's `sweepInstanceData` runs every reconcile cycle and removes every log directory whose instance is neither desired on the node nor known to the runtime (added after the review found the `StopAll` leak; skips `cca-init-*` directories of running init steps).
- **Disk use**: up to two runs of output per instance instead of one, each unbounded as today.
- **Rename fails** (permissions, disk full): logged, start continues, previous log missing for that restart.
- **Instance moved to another node**: its previous log stays on the old node until cleanup there; the new node has none (same as Kubernetes after rescheduling).
- **Behaviour change of `--previous`**: anyone relying on the old meaning got empty output anyway (the logs were deleted with the instance).
