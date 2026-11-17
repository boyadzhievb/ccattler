# Tasks

Implementation (groups 0–2) by the `lead-developer` subagent (Opus, effort medium; CLAUDE.md "Who does what"); the main session checks its work and does groups 3–4.

## 0. Failing tests first

- [ ] 0.1 Agent tests: `TestAgentBacksOffInstantExitInPlace` (a runtime whose container has exited with code 1 by the first Status after Start), `TestAgentBacksOffProcessStartFailureInPlace` (Start returns a `StartError` with `ProcessFailed`), `TestAgentPublishesFailedForInfrastructureStartError` (Start returns a `StartError` without it); assert instance ID kept, state, restart count, and that `failed` is never written for the first two (watch the state key). Record each failing
  - `TestAgentBacksOffInstantExitInPlace` (agent/instant_exit_test.go; `crashingRuntime.exitBeforeFirstLook`, `stateWriteRecordingStore` records every state write): failed before 1.1 — timed out waiting for the second exit in `crash-loop-backoff`: the start path published `failed`, the instance left the desired set and its container was cleaned up as stale. Passes after 1.1.
  - The other two tests are not written: blocked on the D2/D3 finding under 1.2.
- [ ] 0.2 Runtime test `TestStartErrorMarksProcessStartFailure` for the containerd classification (a function-level test of where `ProcessFailed` is set; no containerd daemon). Record failing

## 1. Fix

- [x] 1.1 D1: the first look after a successful start never publishes `failed` — `startInstanceAndPublishState` publishes `starting` for it and no exit reason; Flow 33 records the exit next cycle
- [ ] 1.2 D2 + D3: `StartError.ProcessFailed`, set by containerd for the task start failure, container kept (or the D3 fallback, recorded here); agent handles it through Flow 33
  - Blocked (lead-developer, 2026-11-17): D2 does not fit runc. Probed with the local runc 1.1.15 (rootless, empty rootfs): a missing binary (`exec: "/no/such/binary": stat …: no such file or directory`) and a non-executable file (`exec: "/notexec": permission denied`) fail in `runc create`, i.e. containerd `NewTask` → our `create task: …` error, not `start task`. An exec-format error passes `runc start` and the process exits (code 255) → already the D1 instant-exit case. So `start task` is almost never the workload's own failure, and classifying only it would leave the spec scenario "Entrypoint does not exist" `failed`. Also: on a `create task` failure no task exists, so a kept container's `Status` reports exited with code 0 and no error — Flow 33 could not name the start error from it. Needs a decision before D2/D3 are implemented.
- [ ] 1.3 Docs: Flow 33 (remove the known limit, describe both cases), INSTANCE_LIFECYCLE_GRAPH.md, OBSERVABILITY_GRAPH.md Container Logs (previous log kept for these cases)

## 2. Checks

- [ ] 2.1 gofmt, golangci-lint, `go test ./agent/ ./runtime/ ./controllers/ -count=1`, go build/vet, openspec validate --strict

## 3. Testbed

- [ ] 3.1 By hand (`deploy-run:instant-exit`): a service with `command "sh /no/such/script"` (exits at once) and one with `command "/no/such/binary"` (cannot start); both stay on their instance ID in `crash-loop-backoff` with growing restart counts; `cca logs --previous` works for the first

## 4. Close

- [ ] 4.1 Independent review: `code-reviewer` (Opus, high); record every finding as fixed or rejected with one line here
- [ ] 4.2 Closing full Deploy Testbed run
