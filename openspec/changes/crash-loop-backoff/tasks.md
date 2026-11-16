# Tasks

Implementation (groups 0–2) by a `lead-developer` subagent on Sonnet, as the first half of the subagent trial (design/plans/subagent-implementation-trial.md); the main session checks its work and does groups 3–4.

## 0. Failing tests first

- [ ] 0.1 `backoff/` package test for `Delay` (attempt 1 → base, doubling, cap, attempt < 1), and a test that the failure controller's and the image pull's delays are unchanged for attempts 1–7
- [ ] 0.2 Agent tests with short delays (D6): `TestAgentBacksOffCrashLoopingInstance`, `TestAgentResetsCrashLoopBackoffAfterStableRun`, `TestAgentCountsRestartsAndPublishesCrashLoopState`, `TestAgentRestartsAtOnceWhenConfigChangesDuringBackoff`; controller test `TestFailureControllerIgnoresCrashLoopBackoff`. Record each failing (or not compiling) before the fix

## 1. Fix

- [ ] 1.1 D4: `backoff.Delay`; image pull back-off and `calculateCrashLoopBackoff` use it
- [ ] 1.2 D3: `types.InstanceCrashLoopBackoff`; check every place that switches on or compares instance states and record the list here
- [ ] 1.3 D1, D2, D5: crash-loop back-off state, restart count, waiting state and reason, config change skips the wait, entry removed with stale-instance cleanup; all tests from group 0 pass

## 2. Docs and checks

- [ ] 2.1 Flow 33 in design/runtime-invariant-flows.md; `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md` (exited-container branch, new state); instance state lists in design/ and website docs where they appear
- [ ] 2.2 gofmt, golangci-lint, `go test ./agent/ ./controllers/ ./api/ ./backoff/ ./types/`

## 3. Testbed

- [ ] 3.1 By hand on the deploy testbed (`deploy-run:crash-loop`): a service whose image exits with code 1 at once; after a few minutes `cca describe` shows a growing restart count, `crash-loop-backoff`, restarts minutes apart
- [ ] 3.2 Same run: the node's CNI `host-local` allocation directory does not grow with restarts; the service's VIP backends and eBPF identity entries list only the current IP. A failure becomes its own bug milestone

## 4. Close

- [ ] 4.1 Independent review: `code-reviewer` (and `security-audit` only if the review touches auth/secrets); record every finding as fixed or rejected with one line here
- [ ] 4.2 Trial numbers for M233 in design/plans/subagent-implementation-trial.md
- [ ] 4.3 Closing full Deploy Testbed run
