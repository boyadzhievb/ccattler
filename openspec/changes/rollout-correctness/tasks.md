# Tasks

Implementation (groups 0–3) by the `lead-developer` subagent (Opus, effort medium, `openspec-apply-change` skill; CLAUDE.md "Who does what"). The main session checks its work and does groups 4–5.

## 0. Failing tests first

- [x] 0.1 Rollout controller tests (controllers/rollout_test.go) from the `service-rollout` spec: `TestRolloutStopsOldInstancesInIDOrder`, `TestRolloutCountsAllUnavailableInstancesAgainstMaxUnavailable`, `TestRolloutStopsUnhealthyOldInstancesFirst`, `TestRolloutCountsNewImageRestartsAndFailures`, `TestRolloutIgnoresNodeFailureForRollback`, `TestRolloutRollsBackSmallServiceOnRestarts`, `TestRolloutRollbackThresholdScalesWithDesiredCount`, `TestRolloutTracksSecondRollout`, `TestRolloutRollbackMovesToRolledBack`, `TestRolloutNewImageMidRolloutKeepsKnownGood`, `TestRolloutReasonNamesWaitingInstance`, `TestRolloutReasonForPauseAndBudget`, `TestRolloutRollsBackAfterProgressDeadlineWithReason`, `TestRolloutProgressRestartsDeadline`, `TestRolloutPausedIgnoresProgressDeadline`, `TestRolloutRollbackStallsWithReason`; plus `TestDescribeServiceShowsRolloutReason` and `TestApplyWarnsForRolledBackImage` (api). Run `go test ./controllers/ -run 'TestRollout' -count=1` and record each failure (or why a test passes already) here.
  - Red run, 2026-11-18, before any implementation (only the new keys, `types.RolloutState` and the `Now` field were added so the tests compile). `go test ./controllers/ -run 'TestRollout' -count=1`:
    ```
    --- FAIL: TestRolloutStopsOldInstancesInIDOrder        rollout_test.go:429: attempt 0 stopped [web-c], want exactly [web-a]
    --- FAIL: TestRolloutCountsAllUnavailableInstancesAgainstMaxUnavailable  rollout_test.go:450: stopped [old-2 old-3], want exactly one healthy old instance (max_unavailable 2, one new instance unavailable)
    --- FAIL: TestRolloutStopsUnhealthyOldInstancesFirst    rollout_test.go:468: attempt 0 stopped [old-1], want exactly [old-2]
    --- FAIL: TestRolloutCountsNewImageRestartsAndFailures  rollout_test.go:486: rollout/failures = "" (written false), want 3
    --- FAIL: TestRolloutIgnoresNodeFailureForRollback      rollout_test.go:505: rollout/failures = "" (written false), want 0
                                                            rollout_test.go:507: desired image reverted to "app:a", want no rollback
    --- FAIL: TestRolloutRollsBackSmallServiceOnRestarts    rollout_test.go:523: desired image = "", want app:a
                                                            rollout_test.go:526: rollout/state = "complete", want rollback
                                                            (+ rollback_reason, rolled_back_image, rolled_back_at missing)
    --- FAIL: TestRolloutRollbackThresholdScalesWithDesiredCount/10_instances,_5_failures  rollout_test.go:565: rollback = false (image ""), want true
    --- FAIL: TestRolloutRollbackThresholdScalesWithDesiredCount/1000_instances,_200_failures_(upper_bound_200)  rollback = false, want true
    --- FAIL: TestRolloutTracksSecondRollout                rollout_test.go:597: derived/service/web/rollout/state = "", want "rolling" (previous_image, target_image, revision, history/2 likewise)
                                                            rollout_test.go:601: rollback_reason of the earlier rollback not deleted when the next rollout starts
    --- FAIL: TestRolloutRollbackMovesToRolledBack          rollout_test.go:617: rollout/state = "", want rolled-back (previous_image, failures not deleted)
    --- FAIL: TestRolloutNewImageMidRolloutKeepsKnownGood   rollout_test.go:641: rollout/state = "", want rolling; target_image = "", want app:c; revision = "", want 2
                                                            rollout_test.go:662: later rollback reverted to "", want app:a
    --- FAIL: TestRolloutReasonNamesWaitingInstance         rollout_test.go:675: rollout/reason "" does not contain "web-3" / "image-pull-backoff" / "pull failed: not found"
    --- FAIL: TestRolloutReasonForPauseAndBudget/paused     rollout_test.go:690: rollout/reason "" does not contain "paused"
    --- FAIL: TestRolloutReasonForPauseAndBudget/max_unavailable_used_up  rollout_test.go:705: stopped [web-1] with the budget used up, want none; reason lacks "max_unavailable", "web-3"
    --- FAIL: TestRolloutRollsBackAfterProgressDeadlineWithReason  rollout_test.go:731: desired image = "", want app:a; state = "", want rollback; rollback_reason lacks "no progress for 10m", "web-3", "insufficient_capacity", "cpu 2000m", "memory 1024Mi"
    --- FAIL: TestRolloutProgressRestartsDeadline           rollout_test.go:754: progress_at = "", want now (2026-11-18T12:00:00Z); running_new = "", want 1
    --- PASS: TestRolloutPausedIgnoresProgressDeadline      (passes already: the old controller has no deadline, so it never rolls back on time)
    --- FAIL: TestRolloutRollbackStallsWithReason           rollout_test.go:788: rollout/state = "", want stalled; reason lacks "web-3", "insufficient_capacity"
    FAIL	github.com/boyadzhievb/ccattler/controllers	0.006s
    ```
    `go test ./api/ -run 'TestDescribeServiceShowsRolloutReason|TestApplyWarnsForRolledBackImage' -count=1` (fields `Reason`/`RollbackReason` added to `DescribeRollout` first so it compiles):
    ```
    --- FAIL: TestApplyWarnsForRolledBackImage   apply_rollback_warning_test.go:41: policy-gate: apply answer {"ok":true,"facts_set":5} does not mention "app:v2" / "rolled back 5m" / the reason (same for direct)
    --- FAIL: TestDescribeServiceShowsRolloutReason  describe_rollout_test.go:49: rollout = {State:stalled PreviousImage: Failures:2 Reason: RollbackReason:}, want {... Reason:waiting for web-3 (...) RollbackReason:3 failures ≥ 3 on app:b: exited with code 1}
    FAIL	github.com/boyadzhievb/ccattler/api	0.010s
    ```
    Also written first (task 2.6a): `go test ./cmd/cca/ -run TestStatusTextListsStalledAndRolledBackServices -count=1`:
    ```
    --- FAIL: TestStatusTextListsStalledAndRolledBackServices
        command_status_render_test.go:154: through API: RUNTIME HEALTH lacks "Rollouts:    web stalled: web-3 unplaced: insufficient_capacity"
        command_status_render_test.go:154: in process: RUNTIME HEALTH lacks "Rollouts:    worker rolled-back: 3 failures ≥ 3 on app:v2: exited with code 1" (and the other two combinations)
    FAIL	github.com/boyadzhievb/ccattler/cmd/cca	1.587s
    ```
- [x] 0.2 Instance controller tests `TestInstanceControllerRecordsImageOnCreate` and `TestInstanceControllerClearsRestartsOnRecreate` (stateless and stateful-ordinal paths). Agent tests `TestAgentRestartsInstanceWithItsOwnImage` and `TestAgentStartsInstanceWithRecordedImage`, plus the fallback case with no recorded image. Record the failures here.
  - Red run, 2026-11-18 (only `types.KeyDerivedInstanceImage` added so they compile). `go test ./controllers/ -run 'TestInstanceControllerRecordsImageOnCreate|TestInstanceControllerClearsRestartsOnRecreate' -count=1 -v`:
    ```
    --- FAIL: TestInstanceControllerRecordsImageOnCreate
        --- FAIL: .../stateless         instance_image_test.go:71: creation group of inst-001 does not record image app:b
        --- FAIL: .../stateful_ordinal  instance_image_test.go:71: creation group of web-0 does not record image app:b
    --- FAIL: TestInstanceControllerClearsRestartsOnRecreate
        --- FAIL: .../stateful_ordinal  instance_image_test.go:117: creation group of db-0 does not delete the restart count
        --- FAIL: .../stateless         instance_image_test.go:117: creation group of inst-001 does not delete the restart count
    FAIL	github.com/boyadzhievb/ccattler/controllers	0.014s
    ```
    `go test ./agent/ -run 'TestAgentRestartsInstanceWithItsOwnImage|TestAgentStartsInstanceWithRecordedImage' -count=1 -v`:
    ```
    --- FAIL: TestAgentRestartsInstanceWithItsOwnImage (0.18s)
        instance_image_test.go:46: observed image after the in-place restart = "nginx:1.28", want "nginx:1.27"
        instance_image_test.go:49: agent pulled the service's desired image nginx:1.28, want only nginx:1.27
    --- FAIL: TestAgentStartsInstanceWithRecordedImage (5.02s)
        --- FAIL: .../recorded_image (5.01s)  instance_image_test.go:84: timed out waiting for: instance running with nginx:1.27
        --- PASS: .../no_recorded_image_falls_back_to_the_service_image (0.01s)  (passes already: today's behaviour is the fallback)
    FAIL	github.com/boyadzhievb/ccattler/agent	5.213s
    ```
- [x] 0.3 Delete `TestRolloutStallsWithoutRollbackWhenNewImageCrashLoops`. Its interim M238 scenario in `instance-restart` now names `TestRolloutRollsBackSmallServiceOnRestarts` (OpenSpec cannot drop a scenario from a MODIFIED requirement, so the scenario stays and says the interim has ended). Note the deletion here.
  - Deleted `TestRolloutStallsWithoutRollbackWhenNewImageCrashLoops` from controllers/rollout_test.go (it pinned the M238 interim: no rollback for a crash-looping new image). The `instance-restart` interim scenario names `TestRolloutRollsBackSmallServiceOnRestarts` instead.

## 1. Instances keep their image (design D1, D2)

- [x] 1.1 `types.KeyDerivedInstanceImage(id)` → `derived/instance/{id}/image`, with a doc comment. The instance controller writes it, and deletes `observed/instance/{id}/restarts`, in the creation group of both creation paths (controllers/instance.go). Verify with the 0.2 instance controller tests.
  - Done: `types.KeyDerivedInstanceImage` (types/keys.go); `instanceCreationFacts.buildImageAndRestartChanges` (controllers/instance.go) adds to the `instance-create/{id}` group of `createPendingInstances` and `createNextStatefulInstance` a put of the image (only when the service has a desired image) and a delete of `restarts` (only when a stale count exists for that ID). The instance controller now watches `desired/service/` for the image.
  - Green: `go test ./controllers/ -run 'TestInstanceControllerRecordsImageOnCreate|TestInstanceControllerClearsRestartsOnRecreate' -count=1 -v` → `--- PASS: TestInstanceControllerRecordsImageOnCreate`, `--- PASS: TestInstanceControllerClearsRestartsOnRecreate`, `ok github.com/boyadzhievb/ccattler/controllers`.
  - Finding (fits, but changes more than D1 says): `derived/instance/{id}/image` and `observed/instance/{id}/image` both become field `image` when observed and derived instance facts are merged (`parseInstanceFieldsFromFacts`, `types.ReadInstance`, `types.ListInstances`); the derived value wins. So merged reads show the recorded image, which is the image the agent starts, and a re-created ordinal shows its new image instead of the stale observed one. The rollout controller relies on this (see 2.1). Documented on the key's doc comment and in etcd-schema.md.
  - Finding: a creation is 4 store operations instead of 3, so `defaultMaxInstanceCreationsPerCycle` drops from 19 to 14 (56 ops, under the 58-op cap; otherwise `takeWholeGroups` defers and warns every cycle). Scale-up from zero needs more cycles; existing tests that set the count are unchanged. `TestControllerInterface` now expects 5 watch prefixes.
- [x] 1.2 The agent's start path (`reconcileDesiredInstance`) reads the instance's recorded image. It falls back to `lookupServiceImageFromStore` when the image is absent and logs that once per instance. Verify with the 0.2 agent tests and `go test ./agent/ -count=1`.
  - Done: `lookupInstanceImageFromStore` (agent/agent.go) reads `derived/instance/{id}/image`, else falls back to `lookupServiceImageFromStore` and logs `instance has no recorded image, using the service image` once per instance (`imageFallbackLogged`, cleared by `cleanupStaleImageFallbackLogs` when the instance leaves the node).
  - Green: `go test ./agent/ -run 'TestAgentRestartsInstanceWithItsOwnImage|TestAgentStartsInstanceWithRecordedImage' -count=1 -v` → `--- PASS: TestAgentRestartsInstanceWithItsOwnImage (0.18s)`, `--- PASS: TestAgentStartsInstanceWithRecordedImage/recorded_image`, `--- PASS: .../no_recorded_image_falls_back_to_the_service_image`; `go test ./agent/ -count=1` → `ok github.com/boyadzhievb/ccattler/agent 27.798s`.
  - Note: `executeReconciliationCycle` was already 113 lines (over the 80-line rule); the cleanup call makes it 114.
- [x] 1.3 Docs: INSTANCE_LIFECYCLE_GRAPH.md (image lookup, new key), CALL_GRAPH.md where functions change, and design/etcd-schema.md (new key).
  - Done: INSTANCE_LIFECYCLE_GRAPH.md §1 (creation group, new key, line numbers, constant 14) and §3 (`lookupInstanceImageFromStore`, fallback, cleanup); CALL_GRAPH.md (startup tree, key table, reverse index, error path); design/etcd-schema.md (`derived/instance/{id}/image`, `restarts` deletion, observed image shadowed).

## 2. Rollout controller (design D3–D6)

- [x] 2.1 Read `restarts` and `node_failure` per instance in `extractInstancesByService`, and sort services and instances. Classify instances and count new-image failures (D3), with named constants for the threshold bounds (3, 200) and the divisor. Verify with the counting and threshold tests.
  - Done: controllers/rollout_facts.go `extractInstancesByService` (sorted by ID; restarts, node_failure, status-reason, exit-reason) and services in sorted order in `Reconcile`; controllers/rollout_plan.go `classifyRolloutInstances`; constants `rolloutMinimumFailureThreshold` (3), `rolloutMaximumFailureThreshold` (200), `rolloutFailureThresholdDivisor` (2) in controllers/rollout.go, `computeRolloutFailureThreshold`.
  - Green: `--- PASS: TestRolloutCountsNewImageRestartsAndFailures`, `--- PASS: TestRolloutIgnoresNodeFailureForRollback`, `--- PASS: TestRolloutRollbackThresholdScalesWithDesiredCount` (4 cases incl. bounds 3 and 200).
  - Deviation from D3 wording: an instance's image is the recorded image when one exists, else the observed one ("" = new), because the merged fields carry the recorded image (1.1 finding). This classifies a new instance that has not started yet by the image it will run, and keeps a pending replacement created before a rollback (recorded `b`) from counting as a new instance of the reverted image `a`.
- [x] 2.2 Stop old instances: unavailable ones first without budget, then healthy ones up to `maxUnavailable − unavailable new-image instances`, in ID order (D4). Verify with the stop-order and budget tests and the existing disruption-budget, surge and paused tests.
  - Done: `planOldInstanceStops` (rollout_plan.go): every unavailable old instance first without budget, then healthy old instances in ID order up to `max_unavailable − unavailable new-image instances` (`isUnavailableForRollout`: failed, crash-loop-backoff, image-pull-backoff, node_failure).
  - Green: `--- PASS: TestRolloutStopsOldInstancesInIDOrder` (20 passes, always `[web-a]`), `--- PASS: TestRolloutCountsAllUnavailableInstancesAgainstMaxUnavailable`, `--- PASS: TestRolloutStopsUnhealthyOldInstancesFirst`, and the existing `TestRolloutControllerRespectsDisruptionBudget`, `TestRolloutEmitsSurgeAllowanceOnStart`, `TestRolloutPausedSkipsStops` pass.
  - Interpretation (D4 vs the spec scenario "Crash-looping old instance goes first", which wants no healthy stop in the same pass): healthy old instances are stopped only while a new instance runs, or while no new instance exists and no unhealthy old one is being replaced in this pass (its replacement counts as a new instance that is not running yet). With D4's literal budget rule the scenario's pass would also stop a healthy instance.
- [x] 2.3 State machine (D5): `types.KeyDerivedServiceRolloutTargetImage`. Start tracking from the state rather than from an empty `previous_image`. Handle an image change during a rollout, rollback with `target_image`, no rollback during `rollback`, `complete` and `rolled-back` with cleanup of `previous_image`, `failures` and surge, and write `rollout/failures` only when it changes. Typed constants for the rollout states. Verify with the bookkeeping tests. Keep every function under 80 lines.
  - Done: `types.RolloutState` (rolling, complete, rollback, rolled-back, stalled) in types/state.go; `types.KeyDerivedServiceRolloutTargetImage`; `reconcileServiceRollout`, `beginRolloutTowardDesiredImage`, `rollBackToPreviousImage`, `finishRollout` (rollout.go). All bookkeeping goes through `rolloutFactWriter` (rollout_facts.go): one group `rollout/{svc}`, put only on a changed value (so `failures` only when it changes), delete only of existing facts, one change per key. Longest function: `reconcileServiceRollout` 41 lines.
  - Green: `--- PASS: TestRolloutTracksSecondRollout`, `--- PASS: TestRolloutRollbackMovesToRolledBack`, `--- PASS: TestRolloutNewImageMidRolloutKeepsKnownGood`, `--- PASS: TestRolloutRollsBackSmallServiceOnRestarts`, `--- PASS: TestRolloutDeletesSurgeOnCompletion`, `--- PASS: TestRolloutStartWhilePausedProposesEachKeyOnce` (added: before `propose` replaced earlier changes of a key it failed with `key derived/service/web/rollout/progress_at proposed twice in one pass` and `.../target_image`, `.../running_new` proposed twice).
  - Deviation from D5 rows 4–5 (needs the main session's confirmation): `complete` / `rolled-back` require no old instance **and** every new instance running **and** at least the expected count running (`effective/service/{svc}/instances`, else `desired/.../instances`; `effective/service/` added to Watch). D5's condition "no active instance runs a different image" is already true for a 1-instance service once the old instance is stopped, so the rollout ended before the crash loop could be counted: the red run of `TestRolloutRollsBackSmallServiceOnRestarts` showed `rollout/state = "complete", want rollback`. The same rule is what lets a rollback become `stalled` (spec "Rollback that cannot progress") instead of `rolled-back` while its replacement is unplaced. This follows D5's own failure-case table (row 1) and the spec scenarios.
  - Also: `finishRollout` deletes `target_image`, `reason`, `progress_at` and `running_new` besides `previous_image`, `failures` and surge; a stored state with no `target_image` (older server) adopts the desired image without a new revision.
- [x] 2.4 Reason and progress deadline (D7): `rollout/reason`, `rollout/rollback_reason`, `rollout/progress_at` and `rollout/running_new` keys. The scheduler's unplaced reason and requirements go into the reason, with `derived/scheduler/` added to Watch. A `Now` field on the controller. Deadline → rollback with `rollback_reason`. A failure rollback also writes `rollback_reason`. A rollback without progress → `stalled`. Paused rollouts don't time out. Log one warning per transition. Verify with the reason and deadline tests.
  - Done: keys `KeyDerivedServiceRolloutReason`, `...RollbackReason`, `...ProgressAt`, `...RunningNew` (types/keys.go); `Now` field set by `NewRolloutController`; `advanceRollout`, `recordRolloutProgress`, `isProgressDeadlineExceeded` (rollout.go); `buildRolloutReason`, `describeWaitingInstance` (scheduler unplaced reason + `cpu Nm, memory NMi`), `describeWorstFailingInstance`, `truncateRolloutReason` (`rolloutReasonMaxLength` 300) in rollout_plan.go. Warnings logged on rollback (`rollout rolled back`) and stall (`rollback stalled`); info on start/finish/un-stall.
  - Green: `--- PASS: TestRolloutReasonNamesWaitingInstance`, `--- PASS: TestRolloutReasonForPauseAndBudget`, `--- PASS: TestRolloutRollsBackAfterProgressDeadlineWithReason`, `--- PASS: TestRolloutProgressRestartsDeadline`, `--- PASS: TestRolloutPausedIgnoresProgressDeadline`, `--- PASS: TestRolloutRollbackStallsWithReason`.
  - Choices: Watch adds `derived/scheduler/unplaced/` (`types.ScanDerivedSchedulerUnplaced`, the only scheduler facts read) rather than all of `derived/scheduler/`. A paused rollout has no `progress_at` (deleted once when paused, set to now on resume), so pausing never writes on every pass. When none of the design's four reasons applies the reason is `replacing N old-image instances`. The "waiting for" reason skips new instances on a lost node so the budget reason can name them. The failure rollback reason is `N failures ≥ T on <image>: <exit reason>`, the exit reason being the status reason without its `; restarting in …` part, else `exit-reason`.
- [x] 2.5 DSL (lang/parser.go, lang/formatter.go, compiler) `update { progress_deadline <duration> }` → `desired/service/{svc}/update/progress_deadline`. The parser refuses an invalid or non-positive value. Verify with parser and compiler tests, and update design/dsl-reference.md.
  - Done: `UpdateDecl.ProgressDeadline` (lang/ast.go), `expectPositiveDuration` (lang/parser.go: `time.ParseDuration`, must be > 0), compiler fact `desired/service/{svc}/update/progress_deadline` only when set, formatter line; `extractUpdatePolicies` reads it (invalid stored value → warning + default). design/dsl-reference.md: `update` block in the complete example and a "Rolling Updates" section. Also added `/update/progress_deadline` to the known suffixes in tenant/fair_scheduler.go and `progress_deadline` to the VS Code grammar keywords.
  - Red (tests written first, lang/progress_deadline_test.go): `--- FAIL: TestCompileUpdateProgressDeadline  progress_deadline_test.go:25: 6:27: unknown update field "progress_deadline"`; `TestParseRefusesInvalidProgressDeadline` and `TestCompileUpdateWithoutProgressDeadline` passed already (the unknown field error also names it; no fact without the field).
  - Green: `go test ./lang/ -run 'ProgressDeadline|TestFormatServiceUpdate' -count=1 -v` → all 4 PASS; `go test ./lang/ -count=1` → `ok`.
- [x] 2.6 `cca describe service` shows the state (`rolled-back`, `stalled`), the failure count, the reason and the rollback reason. Verify with `TestDescribeServiceShowsRolloutReason`.
  - Done: `DescribeRollout.Reason`, `.RollbackReason` filled by `buildServiceRollout` (api/describe.go); `cca describe service` prints `Reason:` and `Rollback reason:` (cmd/cca/command_status.go `printServiceDescribeScalingAndRollout`).
  - Green: `--- PASS: TestDescribeServiceShowsRolloutReason`.
- [x] 2.6a Status visibility (D8): the status API service entries carry the rollout state, reason and rollback reason. `cca status` RUNTIME HEALTH lists `stalled` and `rolled-back` services, verified with `TestStatusTextListsStalledAndRolledBackServices`. health_check.sh Check 6 warns for them: run `shellcheck -S warning` and run the jq filter on a sample status JSON, then record the output.
  - Done: `ServiceStatus.RolloutState`, `.RolloutReason`, `.RolloutRollbackReason` (api/status.go, JSON `rollout_state`, `rollout_reason`, `rollout_rollback_reason`); the status snapshot scans `derived/service/` (one more store read per status build); the in-process status entries read the same keys; `renderRolloutHealthLines` (cmd/cca/command_server.go) adds `Rollouts:    <svc> <state>: <reason>` to RUNTIME HEALTH, reason cut at `rolloutHealthReasonMaxLength` (100). health_check.sh Check 6 warns for them.
  - Green: `go test ./cmd/cca/ -run TestStatusText -count=1 -v` → `--- PASS: TestStatusTextListsStalledAndRolledBackServices` (through the API and in process), `--- PASS: TestStatusTextThroughAPIMatchesInProcessText`.
  - `shellcheck -S warning test/e2e/health_check.sh`: only the two existing `SC2034` warnings (`REPO_DIR` line 20, `VM_IP_MAP` line 37; the same two in HEAD's version), none in Check 6.
  - Check 6 run as written (lines from `# ---- Check 6` to before `# ---- Check 7`, with stub `log`/`warn`) on a sample status JSON with one stalled, one rolled-back, one complete and one plain service:
    ```
    [log] Check 6: Instance health
    [log]   Total: 0, Running: 0, Failed: 0, Crash-loop-backoff: 0
    [WARN] 2 service rollout(s) stalled or rolled back
        web stalled: waiting for web-3 unplaced: insufficient_capacity (cpu 2000m, memory 1024Mi)
        worker rolled-back: 3 failures ≥ 3 on app:v2: exited with code 1
    ```
- [x] 2.6b Apply warning (D9): every rollback group also writes `rollout/rolled_back_image` and `rollout/rolled_back_at`; both are deleted with `rollback_reason` when the next rollout starts. Both apply paths in api/server.go append a warning to `applyResponse.Warnings` when a compiled `desired/service/{svc}/image` equals the stored `rolled_back_image` (warn only, never refuse). Verify with `TestApplyWarnsForRolledBackImage`.
  - Done: `rollBackToPreviousImage` writes `rolled_back_image` and `rolled_back_at` in the rollback group; `beginRolloutTowardDesiredImage` deletes them with `rollback_reason`. `collectRolledBackImageWarnings` (api/apply_rollback_warning.go) is called from `applyInstanceLimitCheck.inspect` (api/handler_scale.go), which both apply paths already run on the compiled facts (`applyDirect` and the policy gate's `EvaluateAndCommit` callback); the warning is `service web: image app:v2 was rolled back 5m ago (<reason>); applying it starts a new rollout`. `cca apply` prints it with the existing warning output.
  - Green: `--- PASS: TestApplyWarnsForRolledBackImage` (policy-gate and direct; `app:v3` gives no warning); rolled_back_image/at asserted in `TestRolloutRollsBackSmallServiceOnRestarts`, their deletion in `TestRolloutTracksSecondRollout` (via `rollback_reason`) and `beginRolloutTowardDesiredImage`.
- [x] 2.7 Docs: Flow 34 "Rolling Update and Rollback" in design/runtime-invariant-flows.md, with links from Flow 18 and Flow 33. Update the rollout section of CONTROLLER_DATA_FLOW_GRAPH.md (functions, line numbers, keys) and design/etcd-schema.md (`target_image`, `failures`, `rolled_back_image`, `rolled_back_at`, states).
  - Done: Flow 34 appended to design/runtime-invariant-flows.md; Flow 18 links to it; Flow 33's rollout bullet now describes M241 (the M238 interim and the deleted test are gone). CONTROLLER_DATA_FLOW_GRAPH.md: rollout section rewritten (three files, line numbers, keys, Watch/Writes), instance section Watch/Writes, adjacency `rollout → intent-resolver, instance, scheduler`. design/etcd-schema.md: `update/progress_deadline`, `rollout/paused`, all rollout keys (`target_image`, `failures`, `reason`, `rollback_reason`, `progress_at`, `running_new`, `rolled_back_image`, `rolled_back_at`, history, surge), the five states, and the rollout controller's watch row.

## 3. Checks before handing back

- [x] 3.1 gofmt, `golangci-lint run ./controllers/... ./agent/... ./types/... ./api/...` (0 issues), go build and vet, `go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ -count=1`, touched agent tests with `-race -count=3`, and `openspec validate rollout-correctness --strict`. Paste the results here.
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l $(git diff --name-only -- "*.go") $(git ls-files --others --exclude-standard -- "*.go")
    (no output)
    $ golangci-lint run ./controllers/... ./agent/... ./types/... ./api/... ./lang/... ./cmd/... ./tenant/...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ -count=1
    ok  	github.com/boyadzhievb/ccattler/controllers	5.302s
    ok  	github.com/boyadzhievb/ccattler/agent	27.775s
    ok  	github.com/boyadzhievb/ccattler/types	0.048s
    ok  	github.com/boyadzhievb/ccattler/api	9.223s
    ok  	github.com/boyadzhievb/ccattler/integration	9.363s
    ok  	github.com/boyadzhievb/ccattler/lang	0.040s
    ok  	github.com/boyadzhievb/ccattler/tenant	0.039s
    ok  	github.com/boyadzhievb/ccattler/cmd/cca	7.069s
    $ go test ./agent/ -race -count=3 -run "TestAgentRestartsInstanceWithItsOwnImage|TestAgentStartsInstanceWithRecordedImage|TestAgentDoesNotCountBailedOutStartAsCrash|TestAgentBacksOff|TestAgentCountsRestarts"
    ok  	github.com/boyadzhievb/ccattler/agent	11.186s
    $ go test ./controllers/ -race -count=3 -run "TestRollout|TestInstanceController"
    ok  	github.com/boyadzhievb/ccattler/controllers	1.166s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    $ go test ./... -count=1
    exit 0 (20 packages ok, no failures)
    ```

## 2b. A new instance counts once it stays healthy (design D10, added 2026-11-18 after testbed 4.1)

- [x] 2b.0 Failing tests first: `TestRolloutDoesNotStopOldBeforeMinHealthyTime`, `TestRolloutDoesNotFinishBeforeMinHealthyTime`, `TestRolloutRequiresReadinessWhenProbed`, `TestRolloutCountsInstanceWithoutStartedAtAsHealthy` (controllers), `TestAgentRecordsStartedAtOnEveryStart` (agent), `TestCompileUpdateMinHealthyTime` (lang). Existing rollout tests that expect a new instance to count at once get a `started_at` older than the window. Record the red run here.
  - Red run, 2026-11-18, before any implementation (only `types.KeyObservedInstanceStartedAt` and `types.KeyDesiredServiceUpdateMinHealthyTime` were added so the tests compile):
    ```
    $ go test ./controllers/ -run 'TestRollout' -count=1
    --- FAIL: TestRolloutDoesNotStopOldBeforeMinHealthyTime   rollout_min_healthy_test.go:34: stopped [old-1] while the new instance has run only 2s, want none
                                                              rollout_min_healthy_test.go:38: rollout/reason "replacing 1 old-image instances" does not contain "waiting for new-1 to stay healthy"
    --- FAIL: TestRolloutDoesNotFinishBeforeMinHealthyTime    rollout_min_healthy_test.go:53: rollout/state = "complete", want the rollout to stay rolling
    --- FAIL: TestRolloutRequiresReadinessWhenProbed/not_ready  rollout_min_healthy_test.go:86: stopped [old-1] with readiness not-ready, want 0
    $ go test ./agent/ -run TestAgentRecordsStartedAtOnEveryStart -count=1
    --- FAIL: TestAgentRecordsStartedAtOnEveryStart           started_at_test.go:34: timed out waiting for: first start recorded, instance waiting in crash-loop-backoff
    $ go test ./lang/ -run MinHealthyTime -count=1
    --- FAIL: TestCompileUpdateMinHealthyTime                 min_healthy_time_test.go:27: 6:26: unknown update field "min_healthy_time"
    ```
    `TestRolloutCountsInstanceWithoutStartedAtAsHealthy` passed already: before D10 every running instance counted at once, which is the behaviour it pins for agents without `started_at`. The ready case of `TestRolloutRequiresReadinessWhenProbed` passed for the same reason. Existing rollout tests: `buildRolloutTestFacts` now gives every running test instance a `started_at` one minute before the test clock (`rolloutTestSettledAge`), as the agent writes it, so they still count at once; `startedAgo` / `noStartedAt` / `readiness` set the new cases. The older raw-fact tests at the top of rollout_test.go keep no `started_at` (counted at once, the old-agent rule). `integration/phase10_test.go` `TestRollingUpdateGraduallyReplacesInstances` (real simulator agents, 10 s wait) timed out under load in the full run once D10 was in; its configuration now sets `min_healthy_time 0s`, since it checks pacing, not the window.
- [x] 2b.1 Agent: `types.KeyObservedInstanceStartedAt(id)` → `observed/instance/{id}/started_at` (RFC 3339), written at every container start, in-place restarts included, with the `running` state. instance-gc already deletes `observed/instance/{id}/*`; verify it.
  - `publishInstanceStartedAt` (agent/agent.go) runs in `startInstanceAndPublishState` after a successful `runtime.Start`, before `publishInstanceStateToStore`. A controller that sees `running` then also sees the `started_at` of that start. Every start, in-place restarts included, goes through this function. The value is RFC 3339 with fractional seconds (`time.RFC3339Nano`), which `time.Parse(time.RFC3339, …)` reads, so two starts within one second still differ. Instance GC: `buildInstanceDeletionChanges` (controllers/instance_gc.go) deletes every key under `observed/instance/{id}/`, `started_at` included. Verified in the code.
- [x] 2b.2 Rollout controller: healthy rule (running, `started_at` + `min_healthy_time` ≤ now, readiness `ready` when the service has a readiness probe, missing `started_at` = healthy). Use it in the stop rule, progress (`running_new` counts healthy instances) and `isFinished`. Reason `waiting for <id> to stay healthy (<remaining>)`. Constant `rolloutDefaultMinHealthyTime` = 10 s. Make sure a pass runs after the window ends without a new fact. Keep functions under 80 lines.
  - `rolloutHealthRule.evaluate` (rollout_plan.go) applies the rule. `classifyRolloutInstances` marks every instance (`healthy`, `healthWait`) and counts `healthyNewCount`, which replaces `runningNewCount` in `planOldInstanceStops`, progress and `running_new`, and `isFinished`. `countHealthyOldOnImage` (was `countRunningOldOnImage`) sets `running_new` when a rollback starts, so the next pass compares like with like. The reason is `waiting for <id> to stay healthy (8s)`, with the time left rounded up to the second. When only readiness is missing it is `(readiness not-ready)`, or `(readiness unknown)` before the first probe result. A corrupt `started_at` is logged and treated as missing. A corrupt or negative `update/min_healthy_time` fact is logged and the default is used, as with `progress_deadline`. `newDefaultUpdatePolicy` gives a service without update facts the defaults: 0 is a valid `min_healthy_time`, so a zero value cannot mean "unset". Periodic pass: no fact changes when the window ends. The runner's resync (`defaultControllerResyncInterval` = 30 s, controllers/runner.go, never overridden in production) runs every controller's full Reconcile, just as the progress deadline relies on. So a pass comes at most 30 s after the window ends, and no change was needed. A rollout step can therefore take up to min_healthy_time + 30 s longer than before when no other fact changes.
- [x] 2b.3 DSL: `update { min_healthy_time <duration> }` → `desired/service/{svc}/update/min_healthy_time`; parser refuses invalid or negative values (0 allowed); formatter; tenant/fair_scheduler.go known suffixes; VS Code grammar; design/dsl-reference.md.
  - `expectNonNegativeDuration` and `expectPositiveDuration` (lang/parser.go) now share `expectDurationText`. `UpdateDecl.MinHealthyTime`, the compiler, the formatter, `tenant/fair_scheduler.go` (`/update/min_healthy_time`), the VS Code grammar and dsl-reference.md (example and Rolling Updates paragraph) are updated. `-1s`, `soon` and `10` are refused, with the field named in the error.
- [x] 2b.4 Docs: Flow 34 (healthy rule), CONTROLLER_DATA_FLOW_GRAPH.md and INSTANCE_LIFECYCLE_GRAPH.md (new key, functions, line numbers), design/etcd-schema.md (`started_at`, `update/min_healthy_time`).
  - Flow 34: invariant, agent `started_at`, healthy rule, finish/stop/progress on healthy, the new reason, and a bullet on the resync pass. CONTROLLER_DATA_FLOW_GRAPH.md § rollout: rewritten with new functions and line numbers, plus a note on the resync pass. INSTANCE_LIFECYCLE_GRAPH.md: `publishInstanceStartedAt` in the start chain and the reverse index, with two stale line numbers in that block corrected. CALL_GRAPH.md: start chain and store-key table. etcd-schema.md: `update/min_healthy_time`, `observed/instance/{id}/started_at`.
- [x] 2b.5 Checks as in 3.1 (gofmt, golangci-lint, build, vet, tests incl. `-race` on touched agent and controller tests, `openspec validate rollout-correctness --strict`). Paste the results here.
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l (changed and new Go files)
    (no output)
    $ golangci-lint run ./controllers/... ./agent/... ./types/... ./api/... ./lang/... ./cmd/... ./tenant/... ./integration/...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ -count=1
    ok  	github.com/boyadzhievb/ccattler/controllers	5.210s
    ok  	github.com/boyadzhievb/ccattler/agent	27.858s
    ok  	github.com/boyadzhievb/ccattler/types	0.033s
    ok  	github.com/boyadzhievb/ccattler/api	8.852s
    ok  	github.com/boyadzhievb/ccattler/integration	9.393s
    ok  	github.com/boyadzhievb/ccattler/lang	0.039s
    ok  	github.com/boyadzhievb/ccattler/tenant	0.037s
    ok  	github.com/boyadzhievb/ccattler/cmd/cca	6.973s
    $ go test ./agent/ -race -count=3 -run "TestAgentRecordsStartedAtOnEveryStart|TestAgentRestartsInstanceWithItsOwnImage|TestAgentStartsInstanceWithRecordedImage|TestAgentBacksOff|TestAgentCountsRestarts"
    ok  	github.com/boyadzhievb/ccattler/agent	7.790s
    $ go test ./controllers/ -race -count=3 -run "TestRollout|TestInstanceController"
    ok  	github.com/boyadzhievb/ccattler/controllers	1.149s
    $ go test ./integration/ -race -count=3 -run TestRollingUpdateGraduallyReplacesInstances
    ok  	github.com/boyadzhievb/ccattler/integration	1.375s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    $ go test ./... -count=1
    exit 0 (20 packages ok, 0 failures)
    ```

## 2c. Rework after review 4.2 and testbed 4.1 (design D11–D13, added 2026-11-18)

- [x] 2c.0 Failing tests first (red run recorded here): `TestRolloutCountsUnavailableCapacityAgainstMaxUnavailable`, `TestRolloutDefaultsSurgeSingleInstanceService`, `TestRolloutRevertToPreviousImageNeverRollsBack`, plus a test per fix below.
  - Red run, 2026-11-18, before the fixes. To make the tests compile, only these were added: `types.KeyDerivedServiceRolloutStartedAt`, `types.KeyObservedInstanceLastExit`, a zero-returning `types.ResolveRolloutPacing` stub and the two `…IsDefault` fields of `api.DescribeUpdateStrategy`. `TestRolloutDoesNotStopOldBeforeMinHealthyTime` now expects the reason format from (c).
    ```
    $ go test ./controllers/ -run 'TestRollout|TestInstanceControllerClearsStale|TestFailureControllerReplacementRecordsImage' -count=1
    --- FAIL: TestInstanceControllerClearsStaleIncarnationFactsOnRecreate (0.00s)
        instance_recreate_test.go:45: creation group of db-0 does not delete observed/instance/db-0/probe/readiness
        instance_recreate_test.go:45: creation group of db-0 does not delete observed/instance/db-0/probe/liveness
        instance_recreate_test.go:45: creation group of db-0 does not delete observed/instance/db-0/created-at
        instance_recreate_test.go:45: creation group of db-0 does not delete observed/instance/db-0/started_at
        instance_recreate_test.go:45: creation group of db-0 does not delete observed/instance/db-0/last_exit
    --- FAIL: TestFailureControllerReplacementRecordsImage (0.00s)
        instance_recreate_test.go:57: failure controller does not watch desired/service/ for the image it records
        instance_recreate_test.go:85: replacement inst-001 has no recorded image
    --- FAIL: TestRolloutDoesNotStopOldBeforeMinHealthyTime (0.00s)
        rollout_min_healthy_test.go:38: rollout/reason "waiting for new-1 to stay healthy (8s)" does not contain "(until 12:00:08 UTC)"
    --- FAIL: TestRolloutCountsUnavailableCapacityAgainstMaxUnavailable (0.00s)
        rollout_rework_test.go:34: stopped [old-3] with 1 of 4 expected instances not healthy and max_unavailable 1, want none
    --- FAIL: TestRolloutDefaultsSurgeSingleInstanceService (0.00s)
        rollout_rework_test.go:47: stopped [old-1] before any new instance exists, want none (default max_unavailable 0)
        rollout_rework_test.go:50: surge_allowance = "", want 1 (default max_extra 25% rounded up)
    --- FAIL: TestRolloutRevertToPreviousImageNeverRollsBack (0.00s)
        rollout_rework_test.go:91: desired image reverted to "app:a", want no rollback
        rollout_rework_test.go:93: rollout/failures = "3", want 0: restarts from before the rollout do not count
        rollout_rework_test.go:96: previous_image kept on a revert to it, want it deleted (nothing to roll back to)
        rollout_rework_test.go:99: rollout/started_at = "", want 2026-11-18T12:00:00Z
    --- FAIL: TestRolloutLeavesNodeFailedOldInstanceToFailureController (0.00s)
        rollout_rework_test.go:133: old-1 on a lost node proposed as stopped again, want it left to the failure controller
        rollout_rework_test.go:137: desired image = "", want app:a (deadline passed without progress)
    --- FAIL: TestRolloutRollbackReasonNamesLastExit (0.00s)
        rollout_rework_test.go:155: rollback_reason "3 failures ≥ 3 on app:b: running" does not contain "exited with code 1"
    --- FAIL: TestRolloutLegacyRollbackWithoutTargetFinishesComplete (0.00s)
        rollout_rework_test.go:172: rollout/state = "rolled-back", want complete
    $ go test ./agent/ -run 'TestAgentSkipsStartWhenRecordedImageUnreadable|TestAgentResetsReadinessOnStart|TestAgentKeepsLastExitAfterRestart' -count=1
    --- FAIL: TestAgentSkipsStartWhenRecordedImageUnreadable (0.01s)
        rollout_rework_test.go:47: agent started "nginx:1.28" while the recorded image could not be read, want no start
    --- FAIL: TestAgentResetsReadinessOnStart (0.01s)
        rollout_rework_test.go:70: readiness after the start = "ready", want none until the probe passes again
    --- FAIL: TestAgentKeepsLastExitAfterRestart (0.19s)
        rollout_rework_test.go:85: last_exit after the restart = "", want "exited with code 1"
    $ go test ./lang/ -run TestCompileUpdateWritesOnlySetPacing -count=1
    --- FAIL: TestCompileUpdateWritesOnlySetPacing (0.00s)
        update_defaults_test.go:28: desired/service/web/update/max_unavailable = "1" without the field in the update block, want no fact
        update_defaults_test.go:28: desired/service/web/update/max_extra = "1" without the field in the update block, want no fact
        update_defaults_test.go:45: formatted configuration lost explicit pacing:
        update_defaults_test.go:47: format is not idempotent:
    $ go test ./api/ -run TestDescribeServiceShowsEffectivePacing -count=1
    --- FAIL: TestDescribeServiceShowsEffectivePacing (0.00s)
        describe_pacing_test.go:40: update strategy = {MaxUnavailable:3 MaxExtra: MaxUnavailableIsDefault:false MaxExtraIsDefault:false}, want {MaxUnavailable:3 MaxExtra:2 MaxUnavailableIsDefault:false MaxExtraIsDefault:true}
    $ go test ./types/ -run TestResolveRolloutPacing -count=1
    --- FAIL: TestResolveRolloutPacing (0.00s)
            rollout_pacing_test.go:42: ResolveRolloutPacing(1) = {MaxUnavailable:0 MaxExtra:0 MaxUnavailableIsDefault:false MaxExtraIsDefault:false}, want {MaxUnavailable:0 MaxExtra:1 MaxUnavailableIsDefault:true MaxExtraIsDefault:true}
        … (every case: the stub returned the zero pace)
    ```
    Spec deviation, `TestRolloutCountsUnavailableCapacityAgainstMaxUnavailable`: the scenario as written ("two old stopped, two new pending") has no running old instance to stop. With no healthy new instance, the old code stopped none either, so a literal test would not fail before the fix. The test therefore keeps 4 expected, 2 old stopped and `max_unavailable 1`, and adds two running old instances, one healthy new and one pending new. That is 3 of 4 healthy: the old code stopped `old-3`, D11 stops none. The scenario text could be updated to match (main session).
    The second half of `TestRolloutRevertToPreviousImageNeverRollsBack` (a crash-looping instance created during the revert counts 5 failures and still does not roll back) passed before the fix: the old code also needed a `previous_image`. The node-failure part of `TestRolloutLeavesNodeFailedOldInstanceToFailureController` sets the old instance's observed state to `stopped` with `node_failure`, which is exactly the loop B2 describes.
- [x] 2c.1 B1: the failure controller's replacement group (`stopAndReplace`, controllers/failure.go) records `derived/instance/{id}/image` and deletes stale restarts like the instance controller (shared helper; watch `desired/service/`).
  - `buildInstanceIncarnationChanges` (controllers/instance_incarnation.go) is shared by the instance controller (`instanceCreationFacts.buildIncarnationChanges`, both creation paths) and `FailureController.stopAndReplace`. It writes the recorded image, then the sorted deletes of the stale incarnation's facts. The failure controller watches `desired/service/` and carries the desired images in `failureCycleCounters`. A replacement is now 5 changes, so `defaultMaxReplacementsPerCycle` 14 → 11 (55 ops ≤ 58). The new watch adds failure → rollout to the controller dependency graph (another cycle beside instance ↔ rollout, noted in CONTROLLER_DATA_FLOW_GRAPH.md §4).
- [x] 2c.2 B2 + D11: availability-based budget (expected − healthy of any image); old instances on a lost node are left to the failure controller; an instance already `stopped` is not proposed again and is not progress. Test: an 11-min-old `progress_at` with a node-failed old instance still rolls back on the deadline.
  - `planOldInstanceStops(classification, maxUnavailable, expectedCount)`: `unavailable = expected − healthy new − running old`. Healthy old instances are stopped up to `max(maxUnavailable − unavailable, 0)`. The value is not clamped below, as in Kubernetes: surplus healthy instances from a surge allow stops, which `max_unavailable 0` needs. Without an expected count (facts missing), the active instance count is used. Old instances with `node_failure`, or whose observed state already says `stopped` (`rolloutInstanceInfo.stopWritten`), are skipped: not stopped, not progress. The budget reason now reads `max_unavailable N used up: U of E expected instances not healthy (ids)`; before any new instance exists it reads `waiting for the first new-image instance (max_unavailable N, max_extra M)`. `unavailableNewIDs` is replaced by `notHealthyIDs` (any image).
- [x] 2c.3 D12: defaults 25 % / 25 % (down / up), both 0 → max_unavailable 1; replaces `rolloutDefaultMaxUnavailable`; describe shows effective values and that they are defaults. Check existing tests and `integration/` that assume max_unavailable 1, and test/e2e scripts that time rollouts.
  - `types.ResolveRolloutPacing` and `RolloutPacing.CappedByDisruptionBudget` (types/rollout_pacing.go) are used by the rollout controller and by describe. `rolloutDefaultMaxUnavailable` and `applyDisruptionBudgetToUpdatePolicy` are removed. Its three unit tests became `TestRolloutPacingCappedByDisruptionBudget` (types), which adds the case "the budget never raises a default 0": the old function raised an unset (0) value to the budget's limit.
    - The DSL now compiles only the pacing fields written in the block. Before, `update {}` always wrote `max_unavailable 1`, `max_extra 1` from parser defaults. `UpdateDecl.MaxUnavailable` and `MaxExtra` are `*int`, so an explicit 0 formats and compiles. cmd/k8s-to-cca and two tests follow the pointer change.
    - Behaviour change to note: an `update` block that sets only one of the two now gets the 25 % default for the other, instead of 1.
    - Describe always shows the effective values. `cca describe service` adds `(default: 25% of instances, rounded down|up)`.
    - Existing tests checked: the controller rollout tests without `update` facts now run on the defaults and pass unchanged. The integration rollouts (`TestRollingUpdateGraduallyReplacesInstances`, `TestRollbackOnHealthFailure`) set both values explicitly. The examples/*.cca update blocks set both. test/e2e has no rollout scenario (only `health_check.sh` Check 6 reads the state), so no script assumes `max_unavailable 1`.
- [x] 2c.4 B3 + D13: `rollout/started_at`; failures count only new instances created at or after it; a rollout to the stored `previous_image` has no previous image and cannot roll back.
  - `rollout/started_at` is written with `progress_at` when tracking starts, and again at a rollback, so "the previous image also fails" counts only the rollback's replacements. It is deleted at finish. Failures count new instances whose `created-at` is not earlier than `started_at − rolloutCreatedAtClockSkewAllowance` (30 s). Instances without `created-at` (not yet seen by an agent) count, and so does everything when no `started_at` is stored (a rollout begun by an older server). Instances are classified again after tracking starts in a pass.
    - Deviation: the 30 s allowance. `created-at` is stamped by the agent and `started_at` by the controller, so a few seconds of skew could otherwise exclude the rollout's first new instance and leave only the deadline to catch a crash loop.
    - A recreated stateful ordinal drops its stale `created-at` (2c.1 helper), otherwise it would never count.
    - A revert to the stored `previous_image` deletes `previous_image`; its history entry records the image it reverts from.
- [x] 2c.5 B4: stateful recreation deletes `observed/instance/{id}/probe/*`; the agent resets readiness at every start.
  - The recreation side is in the shared helper (2c.1): it deletes `probe/*`, which covers both creation paths. The agent's `resetInstanceReadiness` deletes `observed/instance/{id}/probe/readiness` at every start, after `started_at` and before the state. The probe scheduler's in-memory counters are not reset: that map is not locked and is used from the probe goroutine. After a restart, one passing check writes `ready` again.
- [x] 2c.6 B5: the agent uses the service image only on `store.ErrKeyNotFound`; any other read error skips the start this cycle.
  - `lookupInstanceImageFromStore` falls back only on `store.ErrKeyNotFound` (or an empty value). Any other error is logged and returns "", and `reconcileDesiredInstance` then skips the start this cycle.
- [x] 2c.7 Testbed (a): the rollback reason names the last exit (`exited with code 1`), not the state, when the failing instance has just restarted — keep/prefer the exit reason. Testbed (c): the waiting reason's remaining time is current (or drop the number and show the time the instance becomes healthy).
  - (a): the agent writes `observed/instance/{id}/last_exit` in `beginCrashLoopWait` (exits and process start failures), and it is not deleted on restart. `describeWorstFailingInstance` takes the failing instances of the count (D13) and prefers `last_exit`. Next it uses the status reason, but not for a running instance, whose status reason is "running". Then the exit reason, then the state. (c): the reason shows when the instance becomes healthy, `waiting for <id> to stay healthy (until 12:00:08 UTC)`, which needs no refresh.
- [x] 2c.8 L1: a stored `rollback` with no `target_image` (older server) finishes as `complete` without a rollback reason. L3: INSTANCE_LIFECYCLE_GRAPH.md §3 line numbers; graphs and etcd-schema.md say both creation paths record the image. L4: one shared "get fact value or empty" helper for the three copies (api/apply_rollback_warning.go, api/describe.go, cmd/cca/command_server.go). Docs for D11–D13 (Flow 34, CONTROLLER_DATA_FLOW_GRAPH.md, etcd-schema.md, dsl-reference.md defaults).
  - L1: `finishRollout` ends as `rolled-back` only when the state is not `rolling` and a `rollback_reason` is stored; otherwise `complete`. L3: INSTANCE_LIFECYCLE_GRAPH.md §1 (both paths through `buildInstanceIncarnationChanges`), §3 (agent line numbers, `resetInstanceReadiness`, B5 path, `last_exit`) and §5 (failure controller line numbers, replacement image, cap 11) are updated, as are CALL_GRAPH.md and etcd-schema.md (the image is recorded at creation and at replacement). L4: `store.ReadValueOrEmpty` (store/store.go) replaces the copies in api/apply_rollback_warning.go, cmd/cca/command_server.go and the closure in api/describe.go. Docs for D11–D13 and the fixes: Flow 34, the CONTROLLER_DATA_FLOW_GRAPH.md rollout and failure sections and dependency list, etcd-schema.md (`update/max_*` defaults, `rollout/started_at`, `last_exit`), dsl-reference.md (defaults, revert), and design/k8s-parity-gaps.md (the renamed budget function).
- [x] 2c.9 Checks as in 3.1; paste results.
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l (changed and new Go files)
    (no output)
    $ golangci-lint run ./...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ ./cmd/k8s-to-cca/ ./store/ -count=1
    ok  	github.com/boyadzhievb/ccattler/controllers	5.230s
    ok  	github.com/boyadzhievb/ccattler/agent	28.078s
    ok  	github.com/boyadzhievb/ccattler/types	0.029s
    ok  	github.com/boyadzhievb/ccattler/api	9.284s
    ok  	github.com/boyadzhievb/ccattler/integration	10.412s
    ok  	github.com/boyadzhievb/ccattler/lang	0.043s
    ok  	github.com/boyadzhievb/ccattler/tenant	0.032s
    ok  	github.com/boyadzhievb/ccattler/cmd/cca	6.422s
    ok  	github.com/boyadzhievb/ccattler/cmd/k8s-to-cca	0.008s
    ok  	github.com/boyadzhievb/ccattler/store	1.596s
    $ go test ./agent/ -race -count=3 -run "TestAgentSkipsStartWhenRecordedImageUnreadable|TestAgentResetsReadinessOnStart|TestAgentKeepsLastExitAfterRestart|TestAgentRecordsStartedAtOnEveryStart|TestAgentRestartsInstanceWithItsOwnImage|TestAgentStartsInstanceWithRecordedImage|TestAgentBacksOff|TestAgentCountsRestarts"
    ok  	github.com/boyadzhievb/ccattler/agent	8.861s
    $ go test ./controllers/ -race -count=3 -run "TestRollout|TestInstanceController|TestFailure"
    ok  	github.com/boyadzhievb/ccattler/controllers	1.170s
    $ go test ./integration/ -race -count=3 -run "TestRollingUpdateGraduallyReplacesInstances|TestRollbackOnHealthFailure"
    ok  	github.com/boyadzhievb/ccattler/integration	1.668s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    $ go test ./... -count=1
    exit 0 (20 packages ok, 0 failures)
    ```

## 4. Testbed and review (main session)

- [ ] 4.1 Testbed by hand (`deploy-run:rollout-rollback`): deploy a service on a good image, apply it with an image whose container exits with code 1 at once, and watch `cca describe service` move from `rolling` to `rollback` to `rolled-back`, with instances back on the good image. Apply an image tag that does not exist, with `progress_deadline 2m`: the reason names the pull error, and after 2 minutes the rollout rolls back with that `rollback_reason`. Apply a service whose resource request no node can meet and see `insufficient_capacity` in the reason and in the rollback reason. Then apply a good second image and see `rolling` → `complete`, with `previous_image` gone. Record the commands and output.
  - First attempt 2026-11-18 on `abe4d19` (deploy cluster, service `m241roll`, `max_unavailable 1`, `progress_deadline 2m`):
    - Crash-looping image (whoami → `mariadb:11`, no password): **FAILED** — `rollout started … revision=1` 10:15:29, `rollout finished state=complete image=…/mariadb:11` 10:15:52; the instance then crash-looped (41 restarts) under a `complete` rollout. Cause: a new instance that reports `running` for 1–2 s counted as done. Fixed by D10 (group 2b); rerun after it.
    - Image tag that does not exist (`whoami:nosuchtag`): **PASSED** — at t+30 s `rolling`, reason `waiting for 66dbc0be92 image-pull-backoff: pulling image …:nosuchtag (attempt 2): … not found`; at t+150 s `rolled-back`, rollback reason `no progress for 2m: waiting for 66dbc0be92 image-pull-backoff: … not found`, whoami running again.
    - Rerun 2026-11-18 on `9a60a15` after group 2b (fresh deploy, `cluster_test.sh --env deploy --skip-build --workload whoami --skip-checks gate-tls,node-failure,multiport,warmzero` → TEST PASSED; controller binary sha256 matches the local build). whoami → `mariadb:11`, `progress_deadline 5m`: t+30 s `rolling`, failures 2, reason `waiting for 208b5404da crash-loop-backoff: exited with code 1; restarting in 20s`; t+90 s `rolled-back`, whoami running again, stayed so through t+270 s. **PASSED.** Findings: (a) rollback reason was `3 failures ≥ 3 on …/mariadb:11: running` — `describeWorstFailingInstance` falls back to the state name when the failing instance has just restarted (status reason empty); should name `exited with code 1`. (b) The old whoami was stopped before any new instance existed (D4 "none created yet" with `max_unavailable 1` on 1 instance) — about 40 s with nothing running; as configured, not a D10 regression.
    - Good second image (whoami → `nginx:1.27`): +5 s `rolling`, reason `waiting for 2326b112ac to stay healthy (10s)`; +30 s `complete`, `Previous image` gone. **PASSED.** Finding (c): the remaining time in the reason is not refreshed (still `(10s)` at age 21 s).
    - Capacity (`cpu 8`, then `memory 64Gi`): **BLOCKED by M243** — both placed on nodes with 2 CPU / 3 GiB because the scheduler reads unit-suffixed requests as 0 (milestones.md Phase 243). Checked after M243.
- [ ] 4.2 Independent review: `code-reviewer` (Opus, effort high) on the milestone diff against this design and the specs. Record each finding as fixed or rejected, with one line of reasoning.
  - Round 1, 2026-11-18, on `a15384c..9a60a15` (REQUEST CHANGES). Verified by the main session in the code; all accepted → group 2c:
    - B1 failure-controller replacements unpinned — real (`stopAndReplace` group has no image put) → 2c.1.
    - B2 node-failed old instance stopped every pass, write loop, deadline renewed — real (`planOldInstanceStops` stops every unavailable old one; stop never changes effective state) → 2c.2.
    - B3 pre-rollout restarts counted; manual revert "rolls back" a→a — real (reviewer probe; D2 resets only on create) → 2c.4, D13.
    - B4 stale readiness result counts — real (no `probe/*` delete on recreation) → 2c.5.
    - B5 fallback on any read error — real (`lookupInstanceImageFromStore` ignores error kind) → 2c.6.
    - D-a "no outage" row wrong for defaults — real; user chose Kubernetes defaults (D12) → 2c.3, row corrected.
    - D-b max_unavailable not enforced once one new instance is healthy — real, pre-existing; user chose to fix in M241 (D11) → 2c.2.
    - 2b.1 `started_at` put separate from state put — accepted as is: low risk, a failed put is retried with the next start.
    - Integration test sets `min_healthy_time 0s` — accepted: D10 covered by unit tests and testbed 4.1.
    - L1 → 2c.8; L2 per-cycle cap ordering — rejected: converges next cycle, no wrong state; L3, L4 → 2c.8; L5 `executeReconciliationCycle` 114 lines — pre-existing, not in this milestone's scope.
- [ ] 4.3 Closing full Deploy Testbed run (job summary says `full run`). Record the run ID.

## 5. Release

- [ ] 5.1 Update the milestones.md M241 row's changelog text to the final scope, then run `scripts/release.sh M241 <version>`.

## Workflow follow-up

- Archive the change after the release (syncs `service-rollout` into openspec/specs/ and the `instance-restart` delta).
