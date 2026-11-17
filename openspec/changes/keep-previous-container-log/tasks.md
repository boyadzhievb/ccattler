# Tasks

Implemented in the main session on Opus, as the second half of the subagent trial (design/plans/subagent-implementation-trial.md); compare with M233.

## 0. Failing tests first

- [x] 0.1 Runtime tests for the log files without containerd (rotation and reading as functions over a log directory): `TestContainerdStartKeepsPreviousRunLog`, `TestContainerdLogsPreviousWithoutPreviousRun`; record them failing — runtime/container_log_test.go, plus `TestRemoveInstanceLogDirectoryStaysUnderBase` (removal refuses `..`, `a/b`, empty). Before the fix: did not compile (`undefined: instanceRunLogPath`)
- [x] 0.2 `TestDebugLogsPassesPreviousOption` (agent debug API), `TestAgentRemovesLogsOfInstanceLeavingNode` (stale cleanup calls `RemoveInstanceData`); record them failing — agent/previous_log_test.go. Before: did not compile (`LogsOptions has no field Previous`); the removal test also failed with the runtime part in place but without the agent change (`timed out waiting for: runtime data removed …`)

## 1. Fix

- [x] 1.1 D1 + D2: rotate at start, stop keeps the directory, `RemoveInstanceData` on the runtime interface (containerd deletes the directory, others no-op), agent cleanup paths call it — deviation from D2: `RemoveInstanceData` is an optional interface (`runtime.InstanceDataRemover`, checked by `removeInstanceDataIfSupported` like `initializeDataPlaneIfSupported`) instead of a `Runtime` method, so the three other runtimes and three test fakes need no no-op. runtime/container_log.go holds the file logic (`rotateInstanceRunLog`, `openPreviousRunLog`, `removeInstanceLogDirectory`); a failed rotation is logged and the start continues
- [x] 1.2 D3 + D4: `LogsOptions.Previous`, containerd `Logs` reads the previous log, other runtimes refuse, debug API parameter, `cca logs --previous` sends it; remove `findPreviousInstance` if unused — containerd reads the previous log without loading the container; nerdctl, process and simulator return `ErrPreviousLogNotAvailable`; `findPreviousInstance` removed (no other caller), `streamContainerLogs` lost its unused service name parameter
- [x] 1.3 Docs: design/node-agent.md (log files), cli-reference and website CLI page (`--previous`), INSTANCE_LIFECYCLE_GRAPH.md, Flow 33 note; gofmt, golangci-lint, unit tests of runtime, agent, api, cmd — node-agent.md, cli-reference.md, website reference/cli.md and troubleshooting, k8s-parity-gaps.md, lifecycle graph, Flow 33; gofmt clean, golangci-lint 0 issues, runtime/agent/api/cmd pass

## 2. Testbed and close

- [ ] 2.1 By hand on the deploy testbed (`deploy-run:previous-log`): crash-looping MariaDB, `cca logs crashloop --previous` shows the missing-password error; current log does not contain the previous run
- [ ] 2.2 Independent review: `code-reviewer`; record every finding as fixed or rejected with one line here
- [ ] 2.3 Trial numbers for M234; closing full Deploy Testbed run
