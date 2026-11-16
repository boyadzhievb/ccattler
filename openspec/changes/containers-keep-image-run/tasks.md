# Tasks

## 0. Failing tests first

- [x] 0.1 Make the spec builders testable (D2): `Start` and `ExecInit` prepend `oci.WithImageConfig(image)`; `buildOCISpecOptions(spec, netnsPath)` and new `buildInitContainerSpecOptions(execSpec, initContainerName)` return the rest; no behaviour change — the init options moved out of `ExecInit` unchanged (same order: args, host network, env, hostname, resolv.conf, secrets, config files)
- [x] 0.2 Write `TestContainerSpecKeepsImageRun` (spec with a granted secret: no mount on `/run`, `/run/cca-secrets/<name>` bind `ro,nosuid,nodev,noexec` from the secret directory) and `TestInitContainerSpecKeepsImageRun` in `runtime/`; record them failing on the `/run` assertion — `runtime/container_spec_test.go`, applies the options with `oci.GenerateSpec` (containerd's default spec, no daemon, no image). Before the fix both failed only on `spec mounts tmpfs on /run ([nosuid strictatime mode=755 size=65536k])`; the secret assertions passed

## 1. Fix

- [x] 1.1 Shared base options with `oci.WithoutRunMount` (D1), used by both builders; the two tests pass — `containerBaseSpecOptions()` in `runtime/containerd.go`; both tests pass
- [x] 1.2 Docs: `design/node-agent.md` (what a container sees under `/run`), Flow 27 (secret mount points are created in the container's writable layer, values stay on the node's memory filesystem), the spec-building calls in `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md`

## 2. WordPress on the testbed

- [x] 2.1 Deploy whoami + WordPress by hand on the deploy testbed with the fixed binary (`--workload whoami,wordpress`, slow groups skipped); MariaDB stays running, the WordPress checks (Step 5.18) pass; record the time; `deploy-run:wordpress` — 2026-11-16, f676b0c binary on the a267019 scripts, as bojan: `TEST PASSED`, 210 s (whoami alone was 149 s). MariaDB started once (`instance.running` 16:06:47, readiness recovered 16:07:20 after the first database setup), **0 `instance.exit` events**; all five WordPress instances running and healthy; `WordPress answers 302 …/wp-admin/install.php` through the nginx VIP `10.156.0.4:80` and through the gate `wordpress.home.arpa` on cca-deploy-ctrl; `WordPress checks PASSED`
- [x] 2.2 `PUSH_WORKLOADS="whoami,wordpress"` and the self-test case (D3); `design/TESTING.md` push profile — self-test passes

## 3. Crash-loop observations (D4)

- [x] 3.1 Restart accounting: why the crash-looping instance stayed `running` with `Restarts: 0` and was restarted about every second; confirmed or not, with evidence — **confirmed**: the agent's reconcile loop restarts an exited container in place on every cycle (`agent/agent.go`, `!runtimeStatus.Running` → `publishExitReasonFromStatus` + `reconcileDesiredInstance`) and never publishes `failed`, so the failure controller's crash-loop back-off (counts `failed` only) never starts; `observed/instance/{id}/restarts` is read by `cca describe` and `/api/status` but written nowhere → M233
- [x] 3.2 Probe target: why the readiness probe used `10.92.128.130` while the instance IP was `10.92.128.140`; confirmed or not, with evidence — **not a separate bug**: the probe reads `observed/instance/{id}/ip` each cycle (`agent/probes.go:143`); the deploy agent takes IPs from CNI `host-local` (`buildCNIConflist`), and every in-place restart creates a new namespace and gets the next IP, so the status reason came from a probe of an earlier attempt. Recorded as part of M233 (whether an in-place restart should keep its IP)
- [x] 3.3 Log retention: whether each restart replaces the container log so the exit cause is lost; confirmed or not, with evidence — **confirmed**: restarting an exited container runs `Start` → `Stop` → `removeContainerResources`, which removes `/var/lib/ccattler/logs/<id>/` with the failed run's log → M234
- [x] 3.4 Each confirmed observation gets a bug milestone row and phase in milestones.md — M233 (restart without back-off or count, includes 3.2) and M234 (restart deletes the previous log)

## 4. Close

- [ ] 4.1 gofmt, golangci-lint, `go test ./runtime/ ./agent/`
- [ ] 4.2 Independent review: `code-reviewer` and `security-audit` (secret mounts); record every finding as fixed or rejected with one line here
- [ ] 4.3 Full Deploy Testbed run (summary says full run), then a push run that includes WordPress
