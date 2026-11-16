# Tasks

## 0. Failing tests first

- [ ] 0.1 Make the spec builders testable (D2): `Start` and `ExecInit` prepend `oci.WithImageConfig(image)`; `buildOCISpecOptions(spec, netnsPath)` and new `buildInitContainerSpecOptions(execSpec, initContainerName)` return the rest; no behaviour change
- [ ] 0.2 Write `TestContainerSpecKeepsImageRun` (spec with a granted secret: no mount on `/run`, `/run/cca-secrets/<name>` bind `ro,nosuid,nodev,noexec` from the secret directory) and `TestInitContainerSpecKeepsImageRun` in `runtime/`; record them failing on the `/run` assertion

## 1. Fix

- [ ] 1.1 Shared base options with `oci.WithoutRunMount` (D1), used by both builders; the two tests pass
- [ ] 1.2 Docs: `design/node-agent.md` (what a container sees under `/run`), Flow 27 (secret mount points are created in the container's writable layer, values stay on the node's memory filesystem), the spec-building calls in `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md`

## 2. WordPress on the testbed

- [ ] 2.1 Deploy whoami + WordPress by hand on the deploy testbed with the fixed binary (`--workload whoami,wordpress`, slow groups skipped); MariaDB stays running, the WordPress checks (Step 5.18) pass; record the time; `deploy-run:wordpress`
- [ ] 2.2 `PUSH_WORKLOADS="whoami,wordpress"` and the self-test case (D3); `design/TESTING.md` push profile

## 3. Crash-loop observations (D4)

- [ ] 3.1 Restart accounting: why the crash-looping instance stayed `running` with `Restarts: 0` and was restarted about every second; confirmed or not, with evidence
- [ ] 3.2 Probe target: why the readiness probe used `10.92.128.130` while the instance IP was `10.92.128.140`; confirmed or not, with evidence
- [ ] 3.3 Log retention: whether each restart replaces the container log so the exit cause is lost; confirmed or not, with evidence
- [ ] 3.4 Each confirmed observation gets a bug milestone row and phase in milestones.md

## 4. Close

- [ ] 4.1 gofmt, golangci-lint, `go test ./runtime/ ./agent/`
- [ ] 4.2 Independent review: `code-reviewer` and `security-audit` (secret mounts); record every finding as fixed or rejected with one line here
- [ ] 4.3 Full Deploy Testbed run (summary says full run), then a push run that includes WordPress
