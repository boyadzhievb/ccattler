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

- [x] 4.1 gofmt, golangci-lint, `go test ./runtime/ ./agent/` — gofmt clean, golangci-lint `./runtime/` 0 issues, both packages pass
- [x] 4.2 Independent review: `code-reviewer` and `security-audit` (secret mounts); record every finding as fixed or rejected with one line here — 2026-11-16 on 1c0191e..01248c1. `code-reviewer`: approve, no defects (ExecInit extraction behaviour-identical, `WithoutRunMount` drops only the `/run` mount, no other runtime builds OCI specs, M233/M234 claims verified in the code). `security-audit`: no defects (secret values stay on the node tmpfs, only empty mount points in the writable layer; snapshots removed on stop; destinations resolved inside the rootfs). Outcome:
  - fixed (low, security-audit) — docs did not say that a secret an application copies to `/run` (or any writable path) is now on the node's disk: one sentence in `design/node-agent.md`
  - fixed (nit, code-reviewer) — milestones.md Phase 232 still listed the three observations as "to verify": now states each outcome
  - rejected (low, security-audit) — `buildSecretOCIMounts` returns nil on invalid paths: unreachable, `Start` and `ExecInit` validate the same mounts with `sortedSecretMountBindings` first and fail the start
  - rejected (low, both) — runc's handling of the new mount points is not executed by the unit test, and the wiring of `Start`/`ExecInit` to the builders is not unit-tested: the WordPress testbed run (2.1) executed both with secrets mounted (MariaDB and WordPress authenticated)
  - noted (nit, code-reviewer) — the `container-filesystem` spec link in `design/node-agent.md` resolves once the change is archived at release
  - noted, pre-existing — no disk quota on the container writable layer (`/tmp`, `/var` were already unbounded; `/run` joins them); `os.WriteFile`/`MkdirAll` errors for config files are ignored in both builders (as before the extraction)
- [x] 4.3 Full Deploy Testbed run (summary says full run), then a push run that includes WordPress — closing full run 43913935555 on b3b2f76 (manual, form unchanged): `run_kind=full`, `skipped check groups: none`, deploy `TEST PASSED`, replay `32 passed, 0 failed, 0 skipped`, 627 s, all steps success. The WordPress push run is not green until M235 (user decision 2026-11-16: close M232 on the full run, M235 next); the WordPress scenarios of this change passed by hand (2.1). First push run 43906605190 (d48e41b, whoami + WordPress) **failed**: WordPress HTTP 500 through the VIP for 300 s. Cause verified on the testbed: both WordPress containers started without their secret mounts (MariaDB had its own, `/run` content from the image visible as intended), because the agent started them while `cca apply` was still writing facts one by one, and the config hash stored after the start already included the secrets, so no restart followed. Pre-existing, not caused by M232 → M235
