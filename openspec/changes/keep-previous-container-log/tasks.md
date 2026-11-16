# Tasks

Implemented in the main session on Opus, as the second half of the subagent trial (design/plans/subagent-implementation-trial.md); compare with M233.

## 0. Failing tests first

- [ ] 0.1 Runtime tests for the log files without containerd (rotation and reading as functions over a log directory): `TestContainerdStartKeepsPreviousRunLog`, `TestContainerdLogsPreviousWithoutPreviousRun`; record them failing
- [ ] 0.2 `TestDebugLogsPassesPreviousOption` (agent debug API), `TestAgentRemovesLogsOfInstanceLeavingNode` (stale cleanup calls `RemoveInstanceData`); record them failing

## 1. Fix

- [ ] 1.1 D1 + D2: rotate at start, stop keeps the directory, `RemoveInstanceData` on the runtime interface (containerd deletes the directory, others no-op), agent cleanup paths call it
- [ ] 1.2 D3 + D4: `LogsOptions.Previous`, containerd `Logs` reads the previous log, other runtimes refuse, debug API parameter, `cca logs --previous` sends it; remove `findPreviousInstance` if unused
- [ ] 1.3 Docs: design/node-agent.md (log files), cli-reference and website CLI page (`--previous`), INSTANCE_LIFECYCLE_GRAPH.md, Flow 33 note; gofmt, golangci-lint, unit tests of runtime, agent, api, cmd

## 2. Testbed and close

- [ ] 2.1 By hand on the deploy testbed (`deploy-run:previous-log`): crash-looping MariaDB, `cca logs crashloop --previous` shows the missing-password error; current log does not contain the previous run
- [ ] 2.2 Independent review: `code-reviewer`; record every finding as fixed or rejected with one line here
- [ ] 2.3 Trial numbers for M234; closing full Deploy Testbed run
