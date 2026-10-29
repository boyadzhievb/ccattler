# Nerdctl Removal Plan — Direct Containerd API Migration

## Goal

Replace all `nerdctl` CLI shelling in `runtime/container.go` with direct
containerd gRPC calls via `github.com/containerd/containerd/v2/client`. This
eliminates a binary dependency, removes process-spawn overhead on every
operation, and gives us typed Go APIs instead of parsing text output.

---

## 1. Current State — Nerdctl Invocations in runtime/container.go

### Container Lifecycle
| Operation | nerdctl command | Lines |
|---|---|---|
| Start (run) | `nerdctl run -d --pull never --name ...` | 242 (via buildRunArguments) |
| Stop | `nerdctl stop -t 10 <name>` | 348 |
| Remove | `nerdctl rm -f <name>` | 351, 383, 405 |
| Inspect (running?) | `nerdctl inspect --format {{.State.Running}}` | 369, 423 |
| Inspect (OOM?) | `nerdctl inspect --format {{.State.OOMKilled}}` | 454 |

### Exec
| Operation | nerdctl command | Lines |
|---|---|---|
| Exec | `nerdctl exec <name> sh -c <cmd>` | 499 |
| ExecCapture | `nerdctl exec <name> sh -c <cmd>` | 522 |
| ExecInit | `nerdctl run --rm --entrypoint bash <image> -c <cmd>` | 561 |

### Logs & Stats
| Operation | nerdctl command | Lines |
|---|---|---|
| Logs | `nerdctl logs [--follow] [--tail] [--since] [--timestamps] <name>` | 691-706 |
| Stats (single) | `nerdctl stats --no-stream --format {{.CPUPerc}} {{.MemUsage}}` | 586 |
| Stats (all) | `nerdctl stats --no-stream --format {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}` | 873 |

### Images
| Operation | nerdctl command | Lines |
|---|---|---|
| ListImages | `nerdctl images --format {{.Repository}}\t{{.Tag}}\t{{.ID}}\t{{.Size}}` | 770 |
| PullImage | `nerdctl pull [--creds user:pass] <ref>` | 931 |
| IsImageCached | `nerdctl image inspect <ref>` | 942 |

### Network
| Operation | nerdctl command | Lines |
|---|---|---|
| Network inspect | `nerdctl network inspect <name>` | 129 |
| Network create | `nerdctl network create --driver bridge --subnet <cidr> <name>` | 136 |
| Network remove | `nerdctl network rm <name>` | 762 |

### Discovery (not in Runtime interface, used by CLI debug)
| Operation | nerdctl command | Lines |
|---|---|---|
| DiscoverAllContainers | `nerdctl ps -a --format {{.Names}}\t{{.ID}}\t{{.Image}}\t{{.Status}}` | 829 |

**Total: 18 distinct nerdctl invocations across 14 operations.**

---

## 2. Replacement Approach

Use `github.com/containerd/containerd/v2/client` which provides:

- `client.NewContainer()` / `container.NewTask()` — lifecycle
- `container.Task().Status()` — state inspection
- `task.Exec()` — in-container exec
- `client.GetImage()` / `client.Pull()` — image management
- `task.Metrics()` — cgroup v2 stats via `/sys/fs/cgroup`
- Log access via containerd's `cio` package (stdout/stderr fifos)
- CNI for networking (containerd uses CNI natively, nerdctl wraps it)

Key design decisions:
- New file: `runtime/containerd.go` — implements the same `Runtime` interface
- Keep `runtime/container.go` (nerdctl) as fallback during migration
- Factory function picks implementation based on config flag (`--runtime=containerd|nerdctl`)
- Container naming, label scheme, and network config stay identical

---

## 3. Implementation Phases

### Phase A — Scaffolding (no behavior change)
1. Add `github.com/containerd/containerd/v2` dependency
2. Create `runtime/containerd.go` with `ContainerdRuntime` struct
3. Implement `NewContainerdRuntime(socketPath string)` connecting to containerd socket
4. Wire `--runtime=containerd` flag in agent and run-container commands
5. Stub all Runtime interface methods returning `ErrNotImplemented`

### Phase B — Read-Only Operations (lowest risk)
1. `Status()` — `task.Status()` returns Running/Stopped/ExitCode directly
2. `List()` — `client.Containers()` with label filter
3. `Stats()` — `task.Metrics()` reads cgroup v2 cpu.stat and memory.current
4. `ListImages()` — `client.ListImages()`
5. `IsImageCached()` — `client.GetImage()` existence check

### Phase C — Image Operations
1. `PullImage()` — `client.Pull()` with registry credentials via `remotes.NewDockerAuthorizer`
2. Validate pulled image content type matches expectations

### Phase D — Container Lifecycle
1. `Start()` — `client.NewContainer()` + `container.NewTask()` + `task.Start()`
   - Container labels: `ccattler.instance.id`, `ccattler.service.name`
   - Network: use CNI plugin configuration matching nerdctl bridge network
   - DNS: write resolv.conf into container rootfs
   - Resource limits: set via OCI spec cgroup resources
   - Config files: bind-mount via OCI spec mounts (same temp-dir approach)
2. `Stop()` — `task.Kill(SIGTERM)` + wait + `task.Kill(SIGKILL)` + `container.Delete()`
3. `ensureNetworkExists()` — CNI config file generation
4. `StopAll()` — iterate and stop

### Phase E — Exec & Logs
1. `Exec()` — `task.Exec()` with `cio.NewCreator()`
2. `ExecCapture()` — same but capture stdout/stderr to buffer
3. `ExecInit()` — create temp container, exec, remove
4. `Logs()` — read from containerd log path (json-file or fifos)

### Phase F — Discovery & Cleanup
1. `DiscoverAllContainers()` — `client.Containers()` with ccattler labels
2. `DiscoverAllContainerStats()` — iterate containers, read cgroup metrics
3. Remove nerdctl fallback, delete `runtime/container.go`
4. Remove `detectContainerCommand()`, `buildExecCommand()`, all nerdctl parsing
5. Update CLI checks that look for nerdctl in PATH

---

## 4. Risk Assessment

| Risk | Impact | Mitigation |
|---|---|---|
| CNI bridge config differs from nerdctl's | Containers get wrong IPs or can't reach each other | Test network connectivity in Phase D before removing nerdctl |
| containerd socket permissions | Agent can't connect | Document socket path and permissions; default `/run/containerd/containerd.sock` |
| Log format differs | Existing log consumers break | Phase E: verify log output matches nerdctl format |
| cgroup v2 stats parsing differs | Metrics are wrong | Phase B: cross-validate stats output against nerdctl |
| DNS resolution changes | Service discovery breaks | Phase D: verify resolv.conf injection matches nerdctl behavior |
| Lima/macOS support breaks | Dev workflow on macOS stops working | Keep nerdctl backend behind `--runtime=nerdctl` for macOS |

**Rollback strategy**: `--runtime=nerdctl` flag preserved throughout. If containerd backend fails in production, switch back with a flag change, no code revert needed. Remove nerdctl backend only after containerd has run stable for one release cycle.

---

## 5. Testing Strategy

### Per-Phase Validation
- **Phase B**: Run `cca agent` with `--runtime=containerd`, compare Status/List/Stats output against nerdctl backend for same containers
- **Phase C**: Pull images, verify `IsImageCached` matches
- **Phase D**: Deploy a full `.cca` file, verify containers start with correct IPs, DNS resolution, env vars, config mounts, resource limits
- **Phase E**: Run `cca exec` and `cca logs` against containerd-managed containers, compare output
- **Phase F**: Run full integration test suite (`test/e2e/`) on containerd backend

### Cross-Validation Harness
Add `runtime/containerd_test.go` that runs the same operation sequence on both backends and diffs results. This catches behavioral divergence before the nerdctl backend is removed.

### Testbed Deployment
1. Deploy containerd backend on one worker node, nerdctl on the other
2. Run mixed workload, verify cross-node service discovery works
3. Promote to all nodes after 48h stable operation
