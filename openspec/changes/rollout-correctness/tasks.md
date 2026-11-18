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

## 2d. Rework after review round 2 (design D14, added 2026-11-18)

- [x] 2d.0 Failing tests first, with the instance controller in the loop (controller + instance controller + fake agent/placement, as the reviewer's probes did): `TestRolloutStatefulAndPerNodeDefaultsReplaceInPlace`, `TestRolloutRollbackWithDefaultPacingCompletes`, `TestRolloutSurgeEndsWithLastOldInstance`, plus an 8-node per-node rollout that completes (not rolled back by the deadline). Record the red run.
  - Red run, 2026-11-18, before the fixes. To make the tests compile, only these were added: `types.WorkloadKind` with its constants, `types.PlacementModePerNode`, the `workloadKind` parameter of `ResolveRolloutPacing` (not yet used), and the `MaxUnavailableForced` / `MaxExtraIgnored` / `WorkloadKind` fields.
    - The loop tests are in controllers/rollout_loop_test.go: a `rolloutLoopCluster` runs the real InstanceController and RolloutController, plus a fake agent that starts pending instances within a placement capacity (per-node: the node count), with 15 s per pass. They follow the reviewer's probes.
    - Besides the three spec scenarios, there is `TestRolloutPerNodeEightNodesCompletes` (8 nodes, 60 passes = 15 min). Unit tests were added for each remaining fix: surge delete, the max_unavailable 0 reason, retarget after a revert, instance-controller surge by kind and old instances, describe kind pacing, the apply warning, and k8s-to-cca zeros.
    ```
    $ go test ./controllers/ -run 'TestRolloutStatefulAndPerNodeDefaultsReplaceInPlace|TestRolloutPerNodeEightNodesCompletes|TestRolloutRollbackWithDefaultPacingCompletes|TestRolloutSurgeEndsWithLastOldInstance|TestRolloutDeletesSurgeWhenLastOldInstanceStops|TestRolloutBudgetReasonWhenMaxUnavailableIsZero|TestRolloutRetargetAfterRevertKeepsTargetAsKnownGood|TestInstanceControllerSurgesOnlyStatelessWithOldInstances' -count=1
    --- FAIL: TestRolloutStatefulAndPerNodeDefaultsReplaceInPlace (0.00s)
        --- FAIL: TestRolloutStatefulAndPerNodeDefaultsReplaceInPlace/stateful,_1_instance (0.00s)
            rollout_loop_test.go:198: up to 2 active instances, want at most 1 (no extra ordinal)
            rollout_loop_test.go:201: extra ordinal db-1 created, want db-0 replaced in place
            rollout_loop_test.go:203: 2 active instances at the end, want 1
        --- FAIL: TestRolloutStatefulAndPerNodeDefaultsReplaceInPlace/per-node,_3_nodes (0.00s)
            rollout_loop_test.go:210: rollout/state = "rolling" after 20 passes, want complete
            rollout_loop_test.go:213: up to 4 active instances, want at most 3 (no surge instance)
            rollout_loop_test.go:215: 4 active instances at the end, want 3
            rollout_loop_test.go:215: instance agentd-old-1 is running on "app:a" at the end, want running on app:b
            rollout_loop_test.go:215: instance agentd-old-2 is running on "app:a" at the end, want running on app:b
            rollout_loop_test.go:215: instance agentd-new-00 is pending on "app:b" at the end, want running on app:b
            rollout_loop_test.go:215: instance agentd-old-0 is running on "app:a" at the end, want running on app:b
    --- FAIL: TestRolloutPerNodeEightNodesCompletes (0.02s)
        rollout_loop_test.go:227: rollout/state = "rolled-back", desired image "app:a", want complete on app:b
        rollout_loop_test.go:230: instance agentd-new-13 is running on "app:a" at the end, want running on app:b
        rollout_loop_test.go:230: instance agentd-new-11 is running on "app:a" at the end, want running on app:b
        rollout_loop_test.go:230: instance agentd-new-17 is running on "app:a" at the end, want running on app:b
        rollout_loop_test.go:230: instance agentd-new-14 is running on "app:a" at the end, want running on app:b
        rollout_loop_test.go:230: instance agentd-new-15 is running on "app:a" at the end, want running on app:b
        rollout_loop_test.go:230: instance agentd-new-16 is running on "app:a" at the end, want running on app:b
        rollout_loop_test.go:230: instance agentd-new-10 is running on "app:a" at the end, want running on app:b
        rollout_loop_test.go:230: instance agentd-new-12 is running on "app:a" at the end, want running on app:b
    --- FAIL: TestRolloutRollbackWithDefaultPacingCompletes (0.00s)
        rollout_loop_test.go:256: rollout/state = "rollback" after 20 passes, want rolled-back
        rollout_loop_test.go:258: instance web-b-1 is running on "app:b" at the end, want running on app:a
        rollout_loop_test.go:258: instance web-b-2 is running on "app:b" at the end, want running on app:a
    --- FAIL: TestRolloutSurgeEndsWithLastOldInstance (0.00s)
        rollout_loop_test.go:272: instance controller created [web-new-00 web-new-01], want exactly one new instance
        rollout_loop_test.go:276: new instance web-new-00 scaled down after the rollout
    --- FAIL: TestRolloutDeletesSurgeWhenLastOldInstanceStops (0.00s)
        rollout_round2_test.go:29: surge_allowance kept when the last old instance is stopped, want it deleted
    --- FAIL: TestRolloutBudgetReasonWhenMaxUnavailableIsZero (0.00s)
        rollout_round2_test.go:48: rollout/reason "max_unavailable 0 used up: 0 of 3 expected instances not healthy ()" does not contain "waiting for an extra instance to become healthy"
    --- FAIL: TestRolloutRetargetAfterRevertKeepsTargetAsKnownGood (0.00s)
        rollout_round2_test.go:66: previous_image = "app:b" after a retarget from the revert target app:a, want app:a
    --- FAIL: TestInstanceControllerSurgesOnlyStatelessWithOldInstances (0.00s)
        --- FAIL: TestInstanceControllerSurgesOnlyStatelessWithOldInstances/stateless_without_old_instances (0.00s)
            rollout_round2_test.go:96: surge instance created = true ([web-new-00]), want false
        --- FAIL: TestInstanceControllerSurgesOnlyStatelessWithOldInstances/per-node_never_surges (0.00s)
            rollout_round2_test.go:96: surge instance created = true ([web-new-00]), want false
        --- FAIL: TestInstanceControllerSurgesOnlyStatelessWithOldInstances/stateful_never_surges (0.00s)
            rollout_round2_test.go:96: surge instance created = true ([]), want false
    $ go test ./types/ -run TestResolveRolloutPacing -count=1
    --- FAIL: TestResolveRolloutPacing (0.00s)
        --- FAIL: TestResolveRolloutPacing/explicit_max_extra_0_and_default_max_unavailable_0_→_1 (0.00s)
            rollout_pacing_test.go:59: ResolveRolloutPacing(stateless, 2) = {MaxUnavailable:1 MaxExtra:0 MaxUnavailableIsDefault:true MaxExtraIsDefault:false MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnavai
        --- FAIL: TestResolveRolloutPacing/both_explicit_0_→_max_unavailable_1 (0.00s)
            rollout_pacing_test.go:59: ResolveRolloutPacing(stateless, 4) = {MaxUnavailable:1 MaxExtra:0 MaxUnavailableIsDefault:false MaxExtraIsDefault:false MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnava
        --- FAIL: TestResolveRolloutPacing/no_instances_yet (0.00s)
            rollout_pacing_test.go:59: ResolveRolloutPacing(stateless, 0) = {MaxUnavailable:1 MaxExtra:0 MaxUnavailableIsDefault:true MaxExtraIsDefault:true MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnavail
        --- FAIL: TestResolveRolloutPacing/stateful_defaults_replace_in_place (0.00s)
            rollout_pacing_test.go:59: ResolveRolloutPacing(stateful, 1) = {MaxUnavailable:0 MaxExtra:1 MaxUnavailableIsDefault:true MaxExtraIsDefault:true MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnavaila
        --- FAIL: TestResolveRolloutPacing/per-node_defaults_replace_in_place (0.00s)
            rollout_pacing_test.go:59: ResolveRolloutPacing(per-node, 8) = {MaxUnavailable:2 MaxExtra:2 MaxUnavailableIsDefault:true MaxExtraIsDefault:true MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnavaila
        --- FAIL: TestResolveRolloutPacing/stateful_ignores_an_explicit_max_extra (0.00s)
            rollout_pacing_test.go:59: ResolveRolloutPacing(stateful, 3) = {MaxUnavailable:0 MaxExtra:2 MaxUnavailableIsDefault:true MaxExtraIsDefault:false MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnavail
        --- FAIL: TestResolveRolloutPacing/per-node_explicit_max_unavailable_0_is_forced_to_1 (0.00s)
            rollout_pacing_test.go:59: ResolveRolloutPacing(per-node, 3) = {MaxUnavailable:0 MaxExtra:1 MaxUnavailableIsDefault:false MaxExtraIsDefault:true MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnavail
        --- FAIL: TestResolveRolloutPacing/per-node_explicit_max_unavailable_2_kept (0.00s)
            rollout_pacing_test.go:59: ResolveRolloutPacing(per-node, 8) = {MaxUnavailable:2 MaxExtra:2 MaxUnavailableIsDefault:false MaxExtraIsDefault:true MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnavail
    $ go test ./api/ -run 'TestDescribeServiceShowsKindPacing|TestApplyWarnsForIgnoredMaxExtra' -count=1
    --- FAIL: TestApplyWarnsForIgnoredMaxExtra (0.00s)
        apply_max_extra_warning_test.go:24: direct: apply answer {"ok":true,"facts_set":8}
        apply_max_extra_warning_test.go:24: direct: apply answer {"ok":true,"facts_set":8}
        apply_max_extra_warning_test.go:24: direct: apply answer {"ok":true,"facts_set":8}
        apply_max_extra_warning_test.go:24: direct: apply answer {"ok":true,"facts_set":8}
        apply_max_extra_warning_test.go:24: policy-gate: apply answer {"ok":true,"facts_set":8}
        apply_max_extra_warning_test.go:24: policy-gate: apply answer {"ok":true,"facts_set":8}
        apply_max_extra_warning_test.go:24: policy-gate: apply answer {"ok":true,"facts_set":8}
        apply_max_extra_warning_test.go:24: policy-gate: apply answer {"ok":true,"facts_set":8}
    --- FAIL: TestDescribeServiceShowsKindPacing (0.00s)
        describe_pacing_kind_test.go:40: update strategy = &{MaxUnavailable:0 MaxExtra:2 MaxUnavailableIsDefault:false MaxExtraIsDefault:false MaxUnavailableForced:false MaxExtraIgnored:false}, want {MaxUnavailable:1 MaxExtra:0 MaxUnavailableIsDefault:false MaxExt
    $ go test ./cmd/k8s-to-cca/ -run TestConvertRollingUpdateKeepsExplicitZero -count=1
    --- FAIL: TestConvertRollingUpdateKeepsExplicitZero (0.00s)
        rolling_update_zero_test.go:37: max_unavailable = &{MaxUnavailable:<nil> MaxExtra:0x11f7b03ddbf8 ProgressDeadline: MinHealthyTime:}, want an explicit 0
    ```
    The red run confirms each finding: H3 created ordinal `db-1`; H2 left a 4th instance pending forever, and with 8 nodes the deadline rolled the good image back; H1's rollback stayed in `rollback` with both b instances; M1 created a second instance and scaled the first down.
    Already passing before the fix, as expected: the instance-controller case "stateless with old instances surges" and the pre-existing stateless pacing cases.
- [x] 2d.1 H2/H3: pacing defaults by kind (stateful, per-node: 1/0, never surge; explicit `max_extra` ignored with an apply warning); `ResolveRolloutPacing` gets the kind; the instance controller adds no surge for these kinds.
  - `ResolveRolloutPacing(kind, …)`: for stateful and per-node services the defaults are 1/0. `max_extra` is always 0 for them, and `MaxExtraIgnored` is set for an explicit non-zero value. An explicit `max_unavailable` keeps its value. Both 0 → 1, now marked `MaxUnavailableForced`.
    - `types.WorkloadKindOf(stateful, placementMode)` decides the kind, with stateful winning when both are set. The rollout controller reads it with `extractWorkloadKinds`, from effective `stateful` and `placement_mode`. Describe reads it with `readServiceWorkloadKind`, using the effective placement mode, else the desired one.
    - The instance controller adds no surge for stateful or per-node services (see 2d.3 for the old-instance condition).
    - `cca apply` warns with `collectIgnoredMaxExtraWarnings` (api/apply_max_extra_warning.go, called from `applyInstanceLimitCheck.inspect`, on both apply paths): `service db: update { max_extra 2 } is ignored: stateful services never surge, they replace one instance at a time`.
- [x] 2d.2 H1: in `rollback`/`stalled` the effective max_unavailable is at least 1 and no surge is used.
  - `advanceRollout` raises the stop budget to `rolloutMinimumRollbackMaxUnavailable` (1) when the state is not `rolling`. The rollback already deletes `surge_allowance`, and the instance controller no longer adds one once no old instance remains.
- [x] 2d.3 M1: surge only while old-image instances remain; `surge_allowance` deleted when the last old instance is stopped.
  - Instance controller: the surge is added only when `hasOldImageInstances` (instance.go) finds an active instance whose merged image (recorded, else observed) differs from the desired image.
    - Rollout: `countOldInstancesLeftAfterStops == 0` deletes `surge_allowance` in the pass that stops the last old instance, and in any later pass where it is still stored. Old instances on a lost node do not count, since the failure controller replaces them.
    - `TestInstanceControllerRespectsSurgeAllowance` (existing): its instances had no image, so they are not old instances under D14. They now run `nginx:1.27` with the desired image `nginx:1.28`, deliberately.
- [x] 2d.4 L1 remove dead `stopWritten`; L5 budget-reason and describe wording; L6 retarget after revert uses the current target as known good; L3 k8s-to-cca keeps explicit zeros (percentages stay as they are, pre-existing, note it); L2 document in dsl-reference.md; L4 graphs (OBSERVABILITY_GRAPH.md `stopAndReplace`, INSTANCE_LIFECYCLE_GRAPH.md calls, `ReadValueOrEmpty`, `readOptionalIntegerFact`, `describePacingDefaultNote`); L7 move the agent test assertions into `waitFor`.
  - L1: `stopWritten` removed (field, extraction, stop-plan case).
    - L5: the reason is `max_unavailable 0: waiting for an extra instance to become healthy` when the budget is 0 by configuration. `cca describe service` says `(raised to 1: with max_extra 0 the rollout could not move)`, `(update { max_extra } ignored: stateful services never surge)`, or `(default for stateful services)` / `(default: 25% …)`, via `describeMaxUnavailableNote` and `describeMaxExtraNote`, which replace `describePacingDefaultNote`.
    - L6: a retarget without `previous_image` uses the stored `target_image` as known good.
    - L3: k8s-to-cca keeps any `maxUnavailable` / `maxSurge` present in the YAML, 0 included. Percentages still convert to their bare number (pre-existing bug, left as is, noted in a comment).
    - L2: documented in dsl-reference.md. **Deviation from D14's wording:** the code does not do what D14 says. `cca apply` (`WriteCompiledFacts`) writes facts and prunes only gate facts, so removing a field from `update {}` never restores its default on a later apply. The stored value stays until the service is deleted and applied again, and the same holds for the implicit 1/1 that older servers compiled for every `update` block. The doc says so; the design note should be corrected (main session).
    - L4: OBSERVABILITY_GRAPH.md `stopAndReplace` (:409, image argument) and INSTANCE_LIFECYCLE_GRAPH.md (both other `stopAndReplace` calls, all §1 instance-controller line numbers, the surge condition) are updated. The CONTROLLER_DATA_FLOW_GRAPH.md rollout section has a new "Read side (API and CLI)" block with `ReadValueOrEmpty`, `readServiceWorkloadKind`, `readOptionalIntegerFact`, the describe note functions and both apply warnings.
    - L7: the three agent tests assert inside `waitFor`. The no-start check fails at once inside the condition.
- [x] 2d.5 Docs: D14 in Flow 34, CONTROLLER_DATA_FLOW_GRAPH.md, dsl-reference.md defaults table by kind.
  - Flow 34: pacing by kind, rollback ≥ 1, the surge rule, retarget after a revert, the `max_unavailable 0` reason, and a "Kinds" bullet. CONTROLLER_DATA_FLOW_GRAPH.md: rollout section rewritten (kinds, rollback minimum, surge delete, line numbers, read side, the instance controller's surge condition). dsl-reference.md: defaults table by kind, rollback, surge lifetime, removed fields. etcd-schema.md: `surge_allowance` lifetime.
- [x] 2d.6 Checks as in 3.1; paste results.
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l (changed and new Go files)
    (no output)
    $ golangci-lint run ./...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ ./cmd/k8s-to-cca/ ./store/ -count=1
    ok  	github.com/boyadzhievb/ccattler/controllers	5.243s
    ok  	github.com/boyadzhievb/ccattler/agent	28.249s
    ok  	github.com/boyadzhievb/ccattler/types	0.061s
    ok  	github.com/boyadzhievb/ccattler/api	9.190s
    ok  	github.com/boyadzhievb/ccattler/integration	8.213s
    ok  	github.com/boyadzhievb/ccattler/lang	0.061s
    ok  	github.com/boyadzhievb/ccattler/tenant	0.074s
    ok  	github.com/boyadzhievb/ccattler/cmd/cca	6.804s
    ok  	github.com/boyadzhievb/ccattler/cmd/k8s-to-cca	0.010s
    ok  	github.com/boyadzhievb/ccattler/store	1.622s
    $ go test ./agent/ -race -count=3 -run "TestAgentSkipsStartWhenRecordedImageUnreadable|TestAgentResetsReadinessOnStart|TestAgentKeepsLastExitAfterRestart|TestAgentRecordsStartedAtOnEveryStart"
    ok  	github.com/boyadzhievb/ccattler/agent	2.165s
    $ go test ./controllers/ -race -count=3 -run "TestRollout|TestInstanceController|TestFailure"
    ok  	github.com/boyadzhievb/ccattler/controllers	1.244s
    $ go test ./integration/ -race -count=3 -run "TestRollingUpdateGraduallyReplacesInstances|TestRollbackOnHealthFailure"
    ok  	github.com/boyadzhievb/ccattler/integration	1.741s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    $ go test ./... -count=1
    exit 0 (20 packages ok, 0 failures)
    (gofmt and golangci-lint rerun after `gofmt -w controllers/rollout_loop_test.go`, a whitespace-only fix the first run found; the controller package tests were run again after it:)
    $ go test ./controllers/ -count=1
    ok  	github.com/boyadzhievb/ccattler/controllers	5.218s
    ```

## 2e. Rework after testbed 4.1 (stateful, per-node) and review round 3 (added 2026-11-18)

- [x] 2e.0 Failing tests first (loop tests where possible; the loop harness must model the agent seeing a stop only on a later pass, and "running but not yet healthy"). Record the red run.
  - Loop harness (controllers/rollout_loop_test.go): the fake agent now keeps one container per instance (`rolloutLoopContainer`, its image). Each pass runs instance controller → rollout controller → instance controller again → agent, so the agent sees a stop only after the instance controller reacted (a re-created ordinal overwrites `stopped` with `pending` before the agent sees it, as on the testbed). The agent removes the containers of stopped instances, reports a kept container as `running` without a new `started_at` (the real running path), and, with `agentEnsuresImage`, replaces a container on another image than the recorded one. `stepDuration` (default 15 s) is set to 5 s in the new tests, so a started instance is running but not yet healthy for a pass. `recordPrematureFinish` records any pass that ends the rollout while an active instance's container runs another image; `assertAllActiveOnImage` also checks the containers. Instance-controller scale-downs are recorded (`scaledDownIDs`).
  - New tests: controllers/rollout_round3_test.go `TestRolloutRecreatedOrdinalCountsOnlyOnceStartedOnNewImage` (loop, both agent models), `TestRolloutRecreatedInstanceWithoutStartedAtIsNotHealthy` (health rule table), `TestInstanceControllerSurgesOnlyForRemainingOldInstances` (loop: 5 on defaults, 3 with `max_extra 2`), `TestRolloutReasonNamesWaitingPerNodeInstance`; agent/recorded_image_ensure_test.go `TestAgentReplacesContainerOnAnotherImageThanRecorded`; scheduler/per_node_rollout_test.go `TestPerNodePlacesReplacementOnNodeOfStoppedInstance`; integration/rollout_kinds_test.go `TestPerNodeRolloutReplacesEveryNode`, `TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage` (real scheduler, simulator agents); api `TestDescribeReadsEffectivePlacementModeLikeTheControllers`; per-node case in `TestApplyWarnsForIgnoredMaxExtra`.
  - Red run, 2026-11-18, before any fix (only the `recordedImage` / `observedImage` fields of `rolloutInstanceInfo` added so the tests compile):
    ```
    $ go test ./controllers/ -run 'TestRolloutRecreated|TestInstanceControllerSurgesOnlyForRemainingOldInstances|TestRolloutStatefulAndPerNode|TestRolloutPerNodeEightNodes|TestRolloutRollbackWithDefaultPacing|TestRolloutSurgeEnds|TestInstanceControllerSurgesOnlyStateless' -count=1
    --- FAIL: TestRolloutRecreatedOrdinalCountsOnlyOnceStartedOnNewImage/agent_keeps_the_old_container
        rollout_round3_test.go:42: rollout complete while db-0's container runs "app:a", want it to keep waiting
        rollout_round3_test.go:45: pass 1: rollout complete while db-0 runs "app:a", want app:b
        rollout_round3_test.go:50: rollout/reason "" does not contain "db-0" (also "app:a", "app:b")
    --- FAIL: TestRolloutRecreatedInstanceWithoutStartedAtIsNotHealthy/recreated,_old_container_kept
        rollout_round3_test.go:84: healthy = true (""), want false
    --- FAIL: TestRolloutRecreatedInstanceWithoutStartedAtIsNotHealthy/recreated,_old_container_kept,_stale_started_at
        rollout_round3_test.go:84: healthy = true (""), want false
    --- FAIL: TestInstanceControllerSurgesOnlyForRemainingOldInstances/5_instances_on_defaults
        rollout_round3_test.go:113: instance controller created 6 instances [web-new-00 … web-new-05], want exactly 5
        rollout_round3_test.go:118: new instance web-new-00 scaled down
    --- FAIL: TestInstanceControllerSurgesOnlyForRemainingOldInstances/3_instances_with_max_extra_2
        rollout_round3_test.go:113: instance controller created 4 instances [web-new-00 … web-new-03], want exactly 3
        rollout_round3_test.go:118: new instance web-new-00 scaled down
    FAIL	github.com/boyadzhievb/ccattler/controllers	0.040s
    $ go test ./agent/ -run TestAgentReplacesContainerOnAnotherImageThanRecorded -count=1
    --- FAIL: TestAgentReplacesContainerOnAnotherImageThanRecorded (5.02s)
        recorded_image_ensure_test.go:32: timed out waiting for: db-0 restarted on nginx:1.28 with a new started_at and no restart counted
    $ go test ./scheduler/ -run TestPerNodePlacesReplacementOnNodeOfStoppedInstance -count=1
    --- FAIL: TestPerNodePlacesReplacementOnNodeOfStoppedInstance (0.00s)
        per_node_rollout_test.go:48: new-1 not placed, want node-1; changes: [derived/scheduler/unplaced/new-1/reason = insufficient_capacity, …/requirements]
    $ go test ./integration/ -run 'TestPerNodeRolloutReplacesEveryNode|TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage' -count=1
    --- FAIL: TestPerNodeRolloutReplacesEveryNode (10.05s)
        rollout_kinds_test.go:86: timed out waiting for: agentd rollout complete on monitor:2.0 on every node
    $ go test ./api/ -run TestDescribeReadsEffectivePlacementModeLikeTheControllers -count=1
    --- FAIL: TestDescribeReadsEffectivePlacementModeLikeTheControllers/desired_per-node_not_effective_yet
        describe_pacing_kind_test.go:82: update strategy = &{MaxUnavailable:1 MaxExtra:0 … WorkloadKind:per-node …}, want kind stateless
    ```
    Passing already: the existing loop tests on the new harness; `TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage` (with real agents at a 50 ms interval the agent usually sees the stop before the re-creation, so the T1 race does not occur there; kept as an end-to-end check); `TestRolloutReasonNamesWaitingPerNodeInstance` and the per-node case of `TestApplyWarnsForIgnoredMaxExtra` (see 2e.2, 2e.4).
- [x] 2e.1 Testbed T1 (stateful never rolled out): 1-instance stateful `m241db` whoami → `mariadb:11` logged `rollout started` 16:00:11.995 and `rollout finished state=complete` 16:00:12.972. The rollout stopped `m241db-0` and the instance controller recreated it under the same ID with the new recorded image before the agent saw the stop (no stop in the agent log), so the whoami container kept running (0 restarts) while describe shows `mariadb:11`; the recreation deleted `started_at`, and a missing `started_at` counts as healthy. Fix so that (a) the agent ensures the container of an instance runs its recorded image and replaces it when not (idempotent `ensure`, architectural rule 3), and (b) a recreated instance never counts as healthy before the agent has started it (e.g. a `started_at` older than the incarnation, or missing after a recreation, is not healthy; keep the older-agent fallback only where it is genuinely an older agent).
  - Root cause: the agent's running path (`executeReconciliationCycle`) never compared a running container with the instance's image. After the re-creation (`state` = `pending`, recorded image `b`, `started_at` deleted) it found the whoami container running, published `running` over `pending`, and wrote no `started_at` and no image (only a start does). The rollout's health rule then took the missing `started_at` for an older agent and counted db-0 healthy at once, so the rollout ended in the next pass.
  - (a) `replaceContainerOnAnotherImage` (agent/agent.go), called on the running path before `detectConfigChange`: when `observed/instance/{id}/image` (the image this agent last started; the re-creation keeps it) differs from `derived/instance/{id}/image`, it runs the pre_stop hook, stops the container and forgets the crash-loop back-off; the next cycle starts the recorded image like any missing container (new `started_at`, no restart counted). A missing fact or a read error changes nothing.
  - (b) `rolloutHealthRule.evaluate` (rollout_plan.go): a running instance with a recorded image is healthy only when its observed image equals it, with or without `started_at`; the reason is `waiting for db-0 to stay healthy (still runs "app:a", not yet started on app:b)`. `extractInstancesByService` keeps both images apart (`recordedImage`, `observedImage`). The older-agent rule (no `started_at` = healthy) stays for an instance on its recorded image: every agent since M7 publishes the observed image at each start, and an older agent starts a new instance on the service image, which is its recorded image. An instance without a recorded image (older server) is unchanged.
  - Decision (no new key): "started by the agent in this incarnation" is read from the existing observed image rather than a new incarnation marker. Limit: an ordinal re-created with the same image as its still-running container counts at once, which is correct (it runs the right image). A stateful rollout on an agent older than this change never becomes healthy and is rolled back by the progress deadline, which is what actually happens on such a node (the old container keeps running).
  - Green: `--- PASS: TestRolloutRecreatedOrdinalCountsOnlyOnceStartedOnNewImage` (both subtests), `--- PASS: TestRolloutRecreatedInstanceWithoutStartedAtIsNotHealthy` (5 cases), `--- PASS: TestAgentReplacesContainerOnAnotherImageThanRecorded (0.05s)`, `--- PASS: TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage`.
- [x] 2e.2 Testbed T2 (per-node replacement never placed): per-node `m241pn` on 3 nodes, whoami → `nginx:1.27`: old `233eb1b1f3` on worker-1 stopped at 16:00:12, new `6272b78d8a` created with no node and no events, still `pending` after 3.5 min; no rollout reason, no unplaced fact logged. Find why per-node placement does not place it (stopped instance still counted on the node? per-node placement owned by the intent controller?), fix it, and make the rollout reason name the waiting instance in this case too (D7: a waiting rollout always says why).
  - Root cause: the scheduler, not the intent controller. Per-node placement (`placeBatchedInstances` → `filterOutNodesAlreadyHostingService`, scheduler/scheduler.go) treated every node with a placement of the service as taken, stopped instances included. The rollout stops the old instance by writing its observed state `stopped`, but its placement stays until instance GC, and GC waits until the service has its expected running count, which needs the replacement. With one instance per node, every node was taken and the replacement stayed pending forever: a deadlock. The intent controller only sets the effective count to the alive node count (3), and the instance controller created the replacement correctly.
  - Fix: `filterOutNodesAlreadyHostingService` skips instances whose observed state is `stopped`. The replacement goes to the node its predecessor left. (Corrected in 2g.3: within the agent cycle that sees both, the desired loop pulls and starts the new instance before the stale loop stops the old one, so the two overlap briefly on the node. That is the decided order.)
  - Reason: not reproduced. With the bug, the integration run's rollout reason was `waiting for <id> unplaced: insufficient_capacity` (the scheduler wrote `derived/scheduler/unplaced/<id>/reason`). `TestRolloutReasonNamesWaitingPerNodeInstance` pins that a pending per-node replacement is named with its unplaced reason, or with `pending` before the scheduler has seen it, and it passed before any change. No code path for a rolling service skips the reason. The testbed's "no rollout reason" may have been read from output without the Rollout section; the main session should check the reason on the next testbed run.
  - Decided 2026-11-18 (user agreed): (1) → new bug milestone M245, verified in the code (`classifyUnplacedReason` falls through to `insufficient_capacity`). (2) → note only: a `controller_stopped` instance holds its per-node node only until its agent stops it (one agent pass); it blocks for good only when that agent never reports, which the node-failure path covers.
  - Not fixed, out of scope (flag): (1) the scheduler labels "every matching node already hosts this per-node service" as `insufficient_capacity`, and the cluster-scale controller requests a node for it (clusterscale.go:255). (2) The per-node filter reads only the observed state, so an instance stopped only by `controller_stopped` (failure-controller replacement on an alive node, scale-down) still takes its node until GC. That has the same shape, but the scheduler does not watch `derived/instance/`.
  - Green: `--- PASS: TestPerNodePlacesReplacementOnNodeOfStoppedInstance`, `--- PASS: TestPerNodeRolloutReplacesEveryNode`, `go test ./scheduler/ -count=1` → `ok`.
- [x] 2e.3 Review round 3 M1 remainder: the instance controller adds `min(surge, active old-image instances)` instead of the full surge (instance.go `hasOldImageInstances` → count). Loop tests: 5 instances on defaults, and 3 instances with `max_extra 2`: exactly N created, none scaled down.
  - `hasOldImageInstances` → `countOldImageInstances` (instance.go). `Reconcile` adds `min(surge_allowance, old-image instances)` for stateless services; stateful and per-node still get none.
  - Green: `--- PASS: TestInstanceControllerSurgesOnlyForRemainingOldInstances` (5 on defaults: 5 created; 3 with `max_extra 2`: 3 created; none scaled down; at most N + surge active), plus the existing `TestRolloutSurgeEndsWithLastOldInstance` and `TestInstanceControllerSurgesOnlyStatelessWithOldInstances`.
- [x] 2e.4 Round 3 lows: INSTANCE_LIFECYCLE_GRAPH.md call order (surge after the instance maps); `t *testing.T` field renamed; use `PlacementModePerNode` for the literal `"per-node"` (instance.go, intent.go); one loop over stateful and placement keys in rollout_facts.go; describe reads the effective placement mode like the controllers; per-node case in the max_extra warning test.
  - INSTANCE_LIFECYCLE_GRAPH.md §1 now shows the surge after `buildInstanceMapsFromFields` / `buildActiveInstanceIDsByService`, as the code runs it, with all §1 line numbers corrected. The loop cluster field `t` is now `testingReporter`. `types.PlacementModePerNode` replaces the four `"per-node"` literals in intent.go and the one in instance.go. `extractWorkloadKinds` uses one loop over both key maps. `readServiceWorkloadKind` (api/describe.go) reads only `effective/service/{svc}/placement_mode`, as `extractWorkloadKinds` and `parseEffectiveServiceFacts` do; `TestDescribeReadsEffectivePlacementModeLikeTheControllers` was red before the change.
  - The new per-node case of `TestApplyWarnsForIgnoredMaxExtra` (`placement { mode per-node }` with `max_extra 1`) passed already: per-node services compile an instance count, so `collectIgnoredMaxExtraWarnings` already covered them.
- [x] 2e.5 Docs and graphs for 2e.1–2e.3; checks as in 3.1, paste results.
  - Docs: Flow 34 (the agent's image check, the per-node scheduler rule, the healthy rule with "started on its recorded image", surge = min(allowance, old left), the new reason, an in-place-replacement bullet); INSTANCE_LIFECYCLE_GRAPH.md §1 (order, line numbers, surge count, re-created ordinal note), §2 (per-node filter), §4 (`replaceContainerOnAnotherImage`), reverse index; SCHEDULING_GRAPH.md (per-node filter, line numbers); CONTROLLER_DATA_FLOW_GRAPH.md (surge count, health rule); CALL_GRAPH.md (running path, reverse index); dsl-reference.md (surge never exceeds the old instances). Spec delta service-rollout: the healthy requirement names the recorded image; new requirement "The agent ensures each container runs its recorded image"; the scenarios name the new tests. No new store key.
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l (changed and new Go files)
    (no output)
    $ golangci-lint run ./...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ ./cmd/k8s-to-cca/ ./store/ ./scheduler/ -count=1
    ok  	github.com/boyadzhievb/ccattler/controllers	5.286s
    ok  	github.com/boyadzhievb/ccattler/agent	28.260s
    ok  	github.com/boyadzhievb/ccattler/types	0.039s
    ok  	github.com/boyadzhievb/ccattler/api	9.074s
    ok  	github.com/boyadzhievb/ccattler/integration	8.390s
    ok  	github.com/boyadzhievb/ccattler/lang	0.045s
    ok  	github.com/boyadzhievb/ccattler/tenant	0.034s
    ok  	github.com/boyadzhievb/ccattler/cmd/cca	6.610s
    ok  	github.com/boyadzhievb/ccattler/cmd/k8s-to-cca	0.008s
    ok  	github.com/boyadzhievb/ccattler/store	1.601s
    ok  	github.com/boyadzhievb/ccattler/scheduler	0.017s
    $ go test ./agent/ -race -count=3 -run "TestAgentReplacesContainerOnAnotherImageThanRecorded|TestAgentStartsInstanceWithRecordedImage|TestAgentRestartsInstanceWithItsOwnImage|TestAgentRecordsStartedAtOnEveryStart|TestAgentSkipsStartWhenRecordedImageUnreadable|TestAgentResetsReadinessOnStart|TestAgentKeepsLastExitAfterRestart"
    ok  	github.com/boyadzhievb/ccattler/agent	3.069s
    $ go test ./controllers/ -race -count=3 -run "TestRollout|TestInstanceController|TestFailure"
    ok  	github.com/boyadzhievb/ccattler/controllers	1.299s
    $ go test ./scheduler/ -race -count=3 -run PerNode
    ok  	github.com/boyadzhievb/ccattler/scheduler	1.022s
    $ go test ./integration/ -race -count=3 -run "TestPerNodeRolloutReplacesEveryNode|TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage|TestRollingUpdateGraduallyReplacesInstances|TestRollbackOnHealthFailure|TestPerNodePlacementLifecycle"
    ok  	github.com/boyadzhievb/ccattler/integration	2.136s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    (first run warned that the healthy requirement text was over 500 characters; the agent rule became its own requirement)
    $ go test ./... -count=1
    20 packages ok, 0 failures
    ```

## 2f. Rework after the 2e testbed rerun (added 2026-11-18)

Testbed on `ae47f36` (push run 44366640013): T1 stateful whoami → nginx PASSED (agent logged `container runs another image than recorded, replacing it`, nginx/1.27.5 served at the instance IP, `complete` after the 10 s healthy wait). T2 per-node whoami → nginx PASSED (one node at a time, reason `waiting for <id> to stay healthy` each time, `complete`; the 2e.2 reason gap did not reappear). Stateful nginx → `mariadb:11` (no password, exits 1) rolled back (`Rollback reason: 3 failures ≥ 3 on …/mariadb:11: exited with code 1`) and ended `rolled-back` on nginx, but:

- [x] 2f.1 Back-off of the old image delays the rollback. The rollback recreated `m241db-0` with recorded image nginx at 18:19:30 before the agent saw the stop; the mariadb container exited at 18:19:31, and the agent counted the exit (attempt 4, `wait=1m20s`) and started nginx only at 18:20:51. The exited-container path (`reconcileInstanceWithoutRunningContainer` → `isExitedInstanceDueForRestart`, agent/crash_loop_backoff.go) never compares the exited container's image with the recorded image, and `hasConfigChangedSinceStart` did not end the wait. Fix: when the exited (or start-failed) container's image differs from the recorded image, drop the back-off entry and start the recorded image at once (the same new-incarnation rule as `replaceContainerOnAnotherImage`). Up to 5 min (the back-off cap) per instance otherwise.
  - Root cause: only the running path (`replaceContainerOnAnotherImage`, 2e.1) compared images. On the exited path, `isExitedInstanceDueForRestart` recorded the mariadb exit against the re-created instance and started a new wait (attempt 4, 1m20s). `hasConfigChangedSinceStart` could not end that wait. The re-creation does not delete `observed/instance/{id}/config-hash`, and this was not the cause either: `computeServiceConfigHash` hashes only config env/files, secret grants, connections, the pull secret and secret generations. The image is not an input, so the stored hash matched and the wait ran on.
  - Fix: `findNewIncarnationOnAnotherImage` (agent/agent.go) is the shared rule: the observed image, which the agent last started or failed to start (`handleInstanceStartError` publishes it too), differs from the recorded image. `replaceContainerOnAnotherImage` uses it. The new `forgetOldIncarnationBackoff` (agent/crash_loop_backoff.go) is called first in `reconcileInstanceWithoutRunningContainer` for an exited container or a retry after a start failure. On a mismatch it drops the back-off entry and treats the container as missing: the recorded image starts at once (`runtime.Start` removes the exited container first), with no exit, last exit or back-off recorded and no restart counted.
- [x] 2f.2 The old image's exit is counted against the new image. After that exit, describe said `rolling back, but the previous image also fails: 1 failures on …/nginx:1.27` and `m241db-0` showed 1 restart, though nginx never exited. An exit, its exit reason and its restart count belong to the image the container ran, not the instance's recorded image; a new incarnation on another image starts with 0 restarts. Find where the per-image failure count reads it (B1 lifetime restarts per image) and fix both sides.
  - Root cause, agent side: after the re-creation (which deleted `restarts`, `last_exit` and `created-at`), the agent recorded the mariadb exit against the new incarnation. `beginCrashLoopWait` wrote `last_exit` and the crash-loop-backoff state, and `publishInstanceStateToStore` wrote a new `created-at`. When the wait ended, `incrementRestartCountInStore` wrote `restarts = 1` on the nginx incarnation.
  - Root cause, rollout side: `classifyRolloutInstances` counted every new instance's restarts and `failed` state. The instance was new because its recorded image is nginx, and it counted because its fresh `created-at` was after the rollback's `started_at` (D13). That gave `rolling back, but the previous image also fails: 1 failures on nginx`.
  - Fix, agent: the 2f.1 rule. No exit, back-off, last exit or restart is recorded for a new incarnation on another image, so its count starts at 0.
  - Fix, rollout: `isStartedOnRecordedImage` (rollout_plan.go) is now shared by the health rule (2e.1b) and the failure count. An instance whose observed image differs from its recorded image adds no failures: its restarts and state are still its previous image's. This also covers an agent older than this change and the window before the agent acts.
  - Fix, re-creation: `observed/instance/{id}/exit-reason` joins the stale incarnation facts deleted by `buildInstanceIncarnationChanges`, so an earlier incarnation's exit reason does not stay on the new one until its first running cycle.
  - No new store key. Lifetime restarts are still kept per instance, not per image. Because the agent never attributes another image's exit to an incarnation, the count is the recorded image's own.
- [x] 2f.3 Failing tests first (agent: exited container on another image than recorded starts at once without counting; loop/rollout: an exit of the old image during a rollback never counts as a failure of the target image). Record red, then green.
  - New tests: agent/incarnation_backoff_test.go `TestAgentStartsNewIncarnationWithoutOldBackoff` covers three cases, all with an hour-long base delay so only the fix can start the instance in time: an exit recorded before the re-creation, an exit right after it (re-creation and kill in one `RunInAgentLoop` hook, the testbed order), and a process start failure of the old image. Each asserts, inside `waitFor`, that the instance runs the new recorded image with no `restarts` and no `last_exit`. controllers/rollout_round3_test.go `TestRolloutNeverCountsOldImageExitsAgainstTarget` covers a rollback b→a and a rollout a→b. In both, the re-created ordinal still shows the previous image (observed ≠ recorded) in `crash-loop-backoff` with 3 restarts.
  - Red run, 2026-11-18, before the fixes:
    ```
    $ go test ./controllers/ -run TestRolloutNeverCountsOldImageExitsAgainstTarget -count=1
    --- FAIL: TestRolloutNeverCountsOldImageExitsAgainstTarget/rollback_from_b_to_a
        rollout_round3_test.go:207: rollout/reason "rolling back, but the previous image also fails: 3 failures on app:a" counts the old image's exit against app:a
    --- FAIL: TestRolloutNeverCountsOldImageExitsAgainstTarget/rolling_from_a_to_b
        rollout_round3_test.go:200: desired image reverted to "app:a", want no rollback for the old image's exits
        rollout_round3_test.go:203: rollout/failures = "3", want 0
    $ go test ./agent/ -run TestAgentStartsNewIncarnationWithoutOldBackoff -count=1
    --- FAIL: TestAgentStartsNewIncarnationWithoutOldBackoff/exit_recorded_before_the_re-creation (5.04s)
        incarnation_backoff_test.go:67: timed out waiting for: db-0 started at once on nginx:1.28, nothing of the old image counted
    --- FAIL: TestAgentStartsNewIncarnationWithoutOldBackoff/exit_right_after_the_re-creation (5.03s)
        incarnation_backoff_test.go:81: timed out waiting for: db-0 started at once on nginx:1.28, nothing of the old image counted
    --- FAIL: TestAgentStartsNewIncarnationWithoutOldBackoff/start_failure_of_the_old_image (5.03s)
        incarnation_backoff_test.go:98: timed out waiting for: db-0 started at once on nginx:1.28, nothing of the old image counted
    ```
  - Green: `--- PASS: TestRolloutNeverCountsOldImageExitsAgainstTarget` (both cases); `go test ./agent/ -run 'TestAgentStartsNewIncarnationWithoutOldBackoff|TestAgentReplacesContainerOnAnotherImageThanRecorded' -count=1` → `ok github.com/boyadzhievb/ccattler/agent 0.168s`.
  - No loop-harness test: the fake agent has no exit or back-off model. The rule is pinned on the rollout side by the unit test, and on the agent side by the real agent with its real back-off.
- [x] 2f.4 Docs and graphs (INSTANCE_LIFECYCLE_GRAPH.md exited path, runtime-invariant-flows Flow 34); checks as in 3.1, paste results.
  - Docs:
    - INSTANCE_LIFECYCLE_GRAPH.md: §12b exited path (`forgetOldIncarnationBackoff`, why the config hash never ended the wait), the reverse index, and `exit-reason` in the §1 stale-key lists.
    - Flow 34: the exited or start-failed mismatch, and failures counting only instances started on their recorded image.
    - CONTROLLER_DATA_FLOW_GRAPH.md: `isStartedOnRecordedImage` in the failure count, `exit-reason` in the incarnation deletes.
    - CALL_GRAPH.md: reverse index.
    - etcd-schema.md: `exit-reason` is deleted at re-creation.
    - Spec delta: scenarios "An exit of the previous image is not a failure of the target" and "Exited container of the previous image".
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l (changed and new Go files)
    (no output)
    $ golangci-lint run ./...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ ./cmd/k8s-to-cca/ ./store/ ./scheduler/ -count=1
    ok  	github.com/boyadzhievb/ccattler/controllers	5.280s
    ok  	github.com/boyadzhievb/ccattler/agent	28.376s
    ok  	github.com/boyadzhievb/ccattler/types	0.076s
    ok  	github.com/boyadzhievb/ccattler/api	9.013s
    ok  	github.com/boyadzhievb/ccattler/integration	8.174s
    ok  	github.com/boyadzhievb/ccattler/lang	0.131s
    ok  	github.com/boyadzhievb/ccattler/tenant	0.036s
    ok  	github.com/boyadzhievb/ccattler/cmd/cca	7.211s
    ok  	github.com/boyadzhievb/ccattler/cmd/k8s-to-cca	0.014s
    ok  	github.com/boyadzhievb/ccattler/store	1.607s
    ok  	github.com/boyadzhievb/ccattler/scheduler	0.022s
    $ go test ./agent/ -race -count=3 -run "TestAgentStartsNewIncarnationWithoutOldBackoff|TestAgentReplacesContainerOnAnotherImageThanRecorded|TestAgentBacksOff|TestAgentCountsRestarts|TestAgentKeepsLastExitAfterRestart|TestAgentRecordsStartedAtOnEveryStart"
    ok  	github.com/boyadzhievb/ccattler/agent	8.390s
    $ go test ./controllers/ -race -count=3 -run "TestRollout|TestInstanceController|TestFailure"
    ok  	github.com/boyadzhievb/ccattler/controllers	1.356s
    $ go test ./integration/ -race -count=3 -run "TestPerNodeRolloutReplacesEveryNode|TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage|TestRollingUpdateGraduallyReplacesInstances|TestRollbackOnHealthFailure"
    ok  	github.com/boyadzhievb/ccattler/integration	2.173s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    $ go test ./... -count=1
    20 packages ok, 0 failures
    ```

Testbed rerun after 2f (push run 44373404786 on `25c5088`, green): stateful nginx → `mariadb:11` rolled back (`3 failures ≥ 3 on …/mariadb:11`); the agent logged `exited container ran another image than recorded, starting the recorded image at once` (exited_image mariadb:11, recorded nginx:1.27), the healthy wait started at once, `rolled-back` with `m241db-0` running nginx and 0 restarts; no "previous image also fails". PASSED.

## 2g. Rework after review round 4 (added 2026-11-18)

- [x] 2g.1 (H) A stopped instance still holds node CPU and memory: `computeNodeLoadAndResourceUsage` (scheduler/scheduler.go) counts every placement, so a per-node replacement that requests resources is `insufficient_capacity` on its only node until GC, which waits for the replacement: the 2e.2 deadlock again (reviewer probe: `cpu 1500` on a 2000m node). Skip `InstanceStopped` there as the per-node filter and `computeServiceZonePlacements` do; per-node scheduler test and rollout test with resource requests.
  - `computeNodeLoadAndResourceUsage` (scheduler/scheduler.go) now skips placements of `stopped` instances for load, CPU and memory, as the per-node filter does. Its three callers (placement, rebalance resources, pressure log) all get the corrected usage.
  - Green: `TestPerNodeReplacementWithResourcesFitsBesideStoppedInstance` (scheduler: 1500m on a 2000m node, beside a stopped instance) and `TestPerNodeRolloutWithResourceRequestsReplacesEveryNode` (integration: 3000m per instance on 4000m nodes, rollout completes). The request is written as plain millicores, since unit suffixes read as 0 until M243.
- [x] 2g.2 (M, found in round 4 by reading, verified) The disruption budget gets `desiredCounts` (0 for per-node services without `instances`) instead of the expected count (controllers/rollout_facts.go `CappedByDisruptionBudget(…, clusterFacts.desiredCounts[…])`, api/describe.go:519), so `min_available` is ignored for per-node services. Pass the expected (effective) count in both; `cca describe service` shows the effective count for per-node services instead of `0 desired`.
  - The rollout controller (`buildServiceInput`) and describe (`buildServiceUpdateStrategy`) now pass the expected count to `CappedByDisruptionBudget`: effective, else desired. Its parameter is renamed `expectedCount`.
  - `readPerNodeAwareDesiredCount` (api/describe.go) makes describe show the effective count as desired for per-node services (kind from the effective placement mode); other services are unchanged.
  - Green: `TestRolloutDisruptionBudgetUsesExpectedCountForPerNode` (one stop, not two) and `TestDescribePerNodeServiceUsesExpectedCount` (3 desired, max_unavailable 1).
- [x] 2g.3 (L, decided 2026-11-18: keep the order) Per-node replacement starts before the old container on the same node is stopped: the agent runs the desired loop (pull + start the new instance) before the stale loop (pre_stop + graceful stop of the old). User: stopping the old first would leave the node (or a one-node cluster) without the service for the pull, start and grace period. Keep start-new-then-stop-old; the brief overlap is safe (per-instance IPs, no host networking or host-port clash). Only document it: correct the 2e.2 note, state the order in Flow 34 and the service-rollout spec, and add a test that pins it (new running before old stopped on the same node).
  - Documented as decided. The 2e.2 note is corrected, Flow 34 has an "Agent, per-node replacement" paragraph, and the service-rollout spec has the scenario "Per-node replacement starts before the old instance stops".
  - The pin is `TestAgentStartsPerNodeReplacementBeforeStoppingOldInstance` (agent/per_node_order_test.go). In one agent-loop hook the old instance is stopped and its replacement placed on the same node; the runtime's call order must show `start agentd-new` before `stop agentd-old`. It passes on the current code, as expected for a pin.
- [x] 2g.4 (L) `forgetOldIncarnationBackoff` logs at Info every cycle while the recorded image cannot start yet; log once per incarnation.
  - `isFirstSightOfIncarnation` (agent/crash_loop_backoff.go) remembers the recorded image last logged per instance (`incarnationLoggedImage`, cleared with the other per-instance logs when the instance leaves the node). `forgetOldIncarnationBackoff` logs only on a new incarnation. Green: `TestAgentLogsNewIncarnationOnce`.
- [x] 2g.5 (style) Doc comments over 4 lines (`replaceContainerOnAnotherImage`, `findNewIncarnationOnAnotherImage`, `filterOutNodesAlreadyHostingService`, `extractInstancesByService`); typed `PlacementMode` with `PlacementModePerNode` used for every `"per-node"` literal (scheduler.go 322/643/1045/1090, lang/parser.go 912, lang/compiler.go 293); INSTANCE_LIFECYCLE_GRAPH.md §4 line numbers.
  - Doc comments of the four functions cut to 4 lines.
  - `types.PlacementMode` is now a typed string, with `PlacementModePerNode PlacementMode`. `servicePlacementConstraint.mode` is typed, and every `"per-node"` literal in scheduler.go (322, 643, 1045, 1090), lang/parser.go:912 and lang/compiler.go:293 uses the constant. `WorkloadKindOf` takes a `PlacementMode`, and its callers convert the stored string. `lang.PlacementDecl.Mode` stays a string: the AST holds source text.
  - INSTANCE_LIFECYCLE_GRAPH.md §4 line numbers updated (`executeReconciliationCycle` :539, `replaceContainerOnAnotherImage` :1229, `findNewIncarnationOnAnotherImage` :1249, `detectConfigChange` :1479, `computeServiceConfigHash` :1562, `republishHostPortIfNeeded` :1083).
- [x] 2g.6 Failing tests first for 2g.1–2g.3 (record red); docs and graphs; checks as in 3.1, paste results.
  - Red run, 2026-11-18, before the fixes:
    ```
    --- FAIL: TestPerNodeReplacementWithResourcesFitsBesideStoppedInstance
        per_node_rollout_test.go:78: new-1 not placed on node-1 beside the stopped old-1; changes: [derived/scheduler/unplaced/new-1/reason = insufficient_capacity, …]
    --- FAIL: TestPerNodeRolloutWithResourceRequestsReplacesEveryNode (10.05s)
        rollout_kinds_test.go:138: timed out waiting for: agentd rollout complete on monitor:2.0 on every node
    --- FAIL: TestRolloutDisruptionBudgetUsesExpectedCountForPerNode
        rollout_round4_test.go:30: stopped [web-1 web-2], want exactly one (3 expected, min_available 2)
    --- FAIL: TestDescribePerNodeServiceUsesExpectedCount
        describe_pacing_kind_test.go:115: desired instances = 0, want the effective count 3
        describe_pacing_kind_test.go:118: update strategy = &{MaxUnavailable:2 … WorkloadKind:per-node …}, want max_unavailable 1
    ok  agent  TestAgentStartsPerNodeReplacementBeforeStoppingOldInstance (passes: pins the decided order)
    ```
  - Docs: Flow 34 (stopped instances use no node resources, the per-node start order, the budget against the expected count); SCHEDULING_GRAPH.md (`computeNodeLoadAndResourceUsage` skip note, line numbers); INSTANCE_LIFECYCLE_GRAPH.md §2 and §4; the service-rollout spec (three scenarios).
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l (changed and new Go files)
    (no output)
    $ golangci-lint run ./...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ ./cmd/k8s-to-cca/ ./store/ ./scheduler/ -count=1
    ok  controllers 5.267s | agent 28.412s | types 0.078s | api 9.349s | integration 8.190s | lang 0.048s
    ok  tenant 0.123s | cmd/cca 6.636s | cmd/k8s-to-cca 0.008s | store 1.604s | scheduler 0.017s
    $ go test ./agent/ -race -count=3 -run "TestAgentStartsPerNodeReplacementBeforeStoppingOldInstance|TestAgentLogsNewIncarnationOnce|TestAgentStartsNewIncarnationWithoutOldBackoff|TestAgentReplacesContainerOnAnotherImageThanRecorded|TestAgentBacksOff"
    ok  	github.com/boyadzhievb/ccattler/agent	6.240s
    $ go test ./controllers/ -race -count=3 -run "TestRollout|TestInstanceController|TestFailure"
    ok  	github.com/boyadzhievb/ccattler/controllers	1.291s
    $ go test ./scheduler/ -race -count=3 -run PerNode
    ok  	github.com/boyadzhievb/ccattler/scheduler	1.023s
    $ go test ./integration/ -race -count=3 -run "TestPerNodeRollout|TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage|TestRollingUpdateGraduallyReplacesInstances|TestRollbackOnHealthFailure|TestPerNodePlacementLifecycle"
    ok  	github.com/boyadzhievb/ccattler/integration	2.500s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    $ go test ./... -count=1
    20 packages ok, 0 failures
    ```

## 2h. Rework after review round 5 (added 2026-11-18)

Round 5 (on `25c5088..c843115`, APPROVE) found that 2g.1 frees a stopped instance's CPU and memory as soon as `stopped` is written, while the container may still run (pre_stop + grace); a decision review (code-reviewer, Opus high) and the user agreed: accept it in M241 as a deliberate accounting trade-off, never described as freed resources; releasing only on an agent observation that the container is gone is follow-up milestone M246 ("a stop request is not proof of termination"). Verified bound: pre_stop ≤ 30 s (`lifecycleHookTimeout`), then SIGTERM, the service's `termination_grace_period` (no upper limit), SIGKILL, ≤ 10 s wait (runtime/containerd.go `StopWithGracePeriod`, which never returns an error); only a process that survives SIGKILL outlives it.

- [x] 2h.1 Spread: `countServiceInstancesPerNode` (scheduler/scheduler.go ~:1172) skips stopped instances like the load count, so a replacement is not pushed off its predecessor's node (otherwise one node ends with two replicas and one with none until GC); fix its comment; test.
  - `countServiceInstancesPerNode` (scheduler/scheduler.go:1175) now skips stopped instances; its comment is fixed.
  - Red: `--- FAIL: TestSpreadIgnoresStoppedInstance  per_node_rollout_test.go:106: web-new placed on node-2, want node-1`. The test has a stopped web on node-1 and two cache instances adding load there; the stopped one counted for spread, so node-2 got a second web replica.
  - Green after the fix.
- [x] 2h.2 Document the trade-off in Flow 34 and the service-rollout spec (replace "a stopped instance uses none of the node's CPU or memory" with what is true): marking an instance stopped releases its requests in the scheduler before the container is gone; during pre_stop and the grace period the node's real usage can exceed its accounted requests; the bound above (incl. that `termination_grace_period` is user-set and uncapped, and a process surviving SIGKILL); every stop path opens it (rollouts, `cca stop`, `cca restart`, which marks every replica at once: up to 2x a node's requests); per-node start-new-first opens it on every node, and overlapping rollouts of several services add up per node. Link M246.
  - Flow 34: the scheduler paragraph now states the trade-off instead of "freed". Marking an instance stopped releases its requests before the container is gone, and the node's real usage can exceed its accounted requests. The bound: pre_stop ≤ 30 s, uncapped `termination_grace_period`, SIGKILL, ≤ 10 s. Every stop path opens it (rollouts, `cca stop`, `cca restart` up to 2x), per-node start-new-first opens it on every node, and overlapping rollouts add up per node. Links M246.
  - Spec: the per-node resources scenario no longer says the stopped instance uses nothing. New scenario "Requests released before the container is gone (accepted trade-off)".
  - The same wording is fixed in the SCHEDULING_GRAPH.md skip note, the `computeNodeLoadAndResourceUsage` comment ("accounted"), the inline comment, and the 2g.1 test comment.
- [x] 2h.3 Tests: keep the 2g.1 no-deadlock regression tests; add a scheduler test that makes the discrepancy visible (a stopped-but-not-gone instance's requests are not counted, so another instance is placed beside it), named in the spec scenario.
  - Kept: `TestPerNodeReplacementWithResourcesFitsBesideStoppedInstance` and `TestPerNodeRolloutWithResourceRequestsReplacesEveryNode`.
  - New: `TestStoppedInstanceRequestsReleasedBeforeContainerIsGone` (scheduler). A stopped `web-old` (1500m) is still placed on a 2000m node, and `batch-1` (1500m) is placed beside it. It passes on the current code: it documents the discrepancy, it is not a fix. It is named in the new spec scenario. `TestSpreadIgnoresStoppedInstance` is named in the per-node resources scenario.
- [x] 2h.4 Doc comment of `TestAgentStartsPerNodeReplacementBeforeStoppingOldInstance` to ≤ 4 lines; checks as in 3.1, paste results.
  - The doc comment is cut to 4 lines.
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l (changed and new Go files)
    (no output)
    $ golangci-lint run ./...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ ./cmd/k8s-to-cca/ ./store/ ./scheduler/ -count=1
    ok  controllers 5.243s | agent 28.319s | types 0.053s | api 8.536s | integration 8.815s | lang 0.116s
    ok  tenant 0.067s | cmd/cca 6.842s | cmd/k8s-to-cca 0.012s | store 1.597s | scheduler 0.030s
    $ go test ./scheduler/ -race -count=3
    ok  	github.com/boyadzhievb/ccattler/scheduler	1.084s
    $ go test ./integration/ -race -count=3 -run "TestPerNodeRollout|TestPerNodePlacementLifecycle|TestRollingUpdateGraduallyReplacesInstances"
    ok  	github.com/boyadzhievb/ccattler/integration	2.428s
    $ go test ./agent/ -race -count=3 -run TestAgentStartsPerNodeReplacementBeforeStoppingOldInstance
    ok  	github.com/boyadzhievb/ccattler/agent	1.132s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    $ go test ./... -count=1
    20 packages ok, 0 failures
    ```

## 2i. Rework after the per-node resource testbed run (added 2026-11-18)

Testbed on `c843115` (push run 44386083771): per-node `m241pr` with `cpu 1500` on 2000m nodes, whoami → nginx: ctrl and worker-1 replaced; worker-2 STALLED, replacement `e1ef91879b` `insufficient_capacity (cpu 1500m)` for good. Events (20:04:05): .603 rollout marks old `f1fce61172` stopped; .713 new `9769e395ab` created; .777 the agent overwrites `f1fce61172` with `running` (plain Put, round 4 "accepted" item); .806 the instance controller sees 3 active and marks `9769e395ab` surplus (`controller_stopped`); .823 rollout stops `f1fce61172` again; .875 `e1ef91879b` created; .888 the scheduler places `9769e395ab` on worker-2 anyway (it reads only the observed state, the 2e.2 flag (2) noted as harmless). `9769e395ab` now holds worker-2's 1500m and GC never removes it (running never reaches 3). Both earlier accepted items are real together.

- [x] 2i.1 The agent never overwrites a `stopped` written by another component: publish the observed state with a store transaction (CAS on the value/revision read in the cycle), and treat a state that became `stopped` mid-cycle as the stop it is (the instance leaves the desired set next cycle). Keep the agent's own `stopped` writes and the M233/M238 states working.
  - `publishInstanceStateUnlessStopped` (agent/agent.go) is called first in `publishInstanceStateToStore`, the one path for every non-stopped state the agent writes (running, starting, pulling, image-pull-backoff, waiting-for-secrets, failed, crash-loop-backoff). It reads the state key. If the value is `stopped` it writes nothing at all (no marker, node, IP or created-at). Otherwise it writes the state in a store transaction that compares the revision just read (0: the key must not exist). A lost CAS writes nothing, and the next cycle publishes again.
  - The agent's own `stopped` writes (`cleanupUndesiredInstance`, `cleanupStoppedStaleInstance`) stay plain Puts. A stopped instance is already final for the agent: `findInstancesPlacedOnThisNode` skips it, so next cycle its container is stopped by the stale loop.
  - Choice (small, noted): the CAS revision is read at publish time, not carried from the cycle's first read. Carrying it would also refuse legitimate changes made during the cycle, such as a re-created ordinal's `pending` (2e.1), and only `stopped` must never be overwritten. The read-to-transaction window is closed by the revision compare.
- [x] 2i.2 The scheduler treats `derived/instance/{id}/controller_stopped=true` as stopped: never places such an instance, and does not count it in load, resources, spread or the per-node slot. Watch `derived/instance/` (or the key) so it reacts; update SCHEDULING_GRAPH.md.
  - `extractInstanceInfoFromFacts` (scheduler/scheduler.go:1401) sets the state to stopped when `derived/instance/{id}/controller_stopped` = `true`. Every scheduler rule keyed on the state now treats it as stopped: never pending, so never placed; no load or resources (2g.1); no spread (2h.1); no per-node slot (2e.2); not a preemption or rebalance candidate. `derived/instance/` is added to `Scheduler.Watch` (8 prefixes; `TestControllerInterface` updated). SCHEDULING_GRAPH.md updated.
- [x] 2i.3 Failing tests first: a loop test that replays the testbed order (state overwritten mid-cycle, surplus stop, late placement) on a per-node service with resource requests and ends `complete` with exactly one instance per node; a scheduler test with a `controller_stopped` pending instance (not placed, no resources counted); an agent test that a concurrent `stopped` survives the agent's publish. Record red, then green.
  - Tests:
    - scheduler/per_node_resource_rollout_test.go `TestPerNodeResourceRolloutSurvivesStaleStateOverwrite` replays the testbed order with the real instance controller, rollout controller and scheduler (the scheduler package can import controllers; the controllers loop harness cannot), on 3 nodes of 2000m with `cpu 1500`. A fake agent reads the states at cycle start and publishes after: without CAS it overwrites the rollout's stop. Then instance → rollout → instance → scheduler → agent apply. A `controller_stopped` instance without a container keeps its observed `pending`, as the real agent does. Two subtests: agent without CAS (the pre-2i agent) and with CAS.
    - `TestSchedulerTreatsControllerStoppedAsStopped`: a placed and a pending `controller_stopped` surplus; the replacement must get the node's slot and CPU.
    - agent/state_cas_test.go `TestAgentNeverOverwritesConcurrentStop`: the runtime's `Status`, called between the running-path read and the publish, writes `stopped`; the stop must survive and the container be stopped.
  - Red run, 2026-11-18, before the fixes:
    ```
    --- FAIL: TestPerNodeResourceRolloutSurvivesStaleStateOverwrite/agent_CAS_false
        per_node_resource_rollout_test.go:223: rollout/state = "rolling" after 30 passes, want complete (reason "waiting for agentd-new-02 unplaced: insufficient_capacity (cpu 1500m, memory 0Mi)")
        per_node_resource_rollout_test.go:230: node-1 hosts active instances [], want exactly one
        per_node_resource_rollout_test.go:235: node-2: agentd-old-1 is running with container "app:a", want running app:b
        per_node_resource_rollout_test.go:235: node-3: agentd-old-2 is running with container "app:a", want running app:b
    --- FAIL: TestSchedulerTreatsControllerStoppedAsStopped
        per_node_rollout_test.go:186: replacement placed on "", want node-1 (the surplus holds no slot or CPU); changes: [derived/scheduler/unplaced/replacement/reason = insufficient_capacity …]
    --- FAIL: TestAgentNeverOverwritesConcurrentStop (5.03s)
        state_cas_test.go:62: timed out waiting for: the concurrent stop kept and web-1's container stopped
    ```
    The CAS subtest of the replay passed before the fixes: its fake agent models the fix, and the real agent's fix is covered by `TestAgentNeverOverwritesConcurrentStop`. The non-CAS subtest passing after the fixes shows the scheduler fix alone also ends the stall.
  - Green: all three pass; spec scenario "A stop survives the agent's publish and a surplus instance is never placed".
- [x] 2i.4 Docs and graphs; checks as in 3.1, paste results. Main session then reruns the testbed per-node `cpu 1500` case.
  - Docs: Flow 34 (the scheduler's "stopped" includes `controller_stopped`; the agent publishes state by CAS, with the testbed chain); SCHEDULING_GRAPH.md (Watch, `extractInstanceInfoFromFacts`, key table, line numbers); INSTANCE_LIFECYCLE_GRAPH.md §3 and §4 (`publishInstanceStateUnlessStopped`); CALL_GRAPH.md (reverse index, key table); the service-rollout spec scenario.
  - Run 2026-11-18 on the final working tree:
    ```
    $ gofmt -l (changed and new Go files)
    (no output)
    $ golangci-lint run ./...
    0 issues.
    $ go build ./... && go vet ./...
    exit 0
    $ go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ ./lang/ ./tenant/ ./cmd/cca/ ./cmd/k8s-to-cca/ ./store/ ./scheduler/ -count=1
    ok  controllers 5.272s | agent 28.411s | types 0.049s | api 9.144s | integration 8.207s | lang 0.041s
    ok  tenant 0.100s | cmd/cca 6.925s | cmd/k8s-to-cca 0.024s | store 1.608s | scheduler 0.038s
    $ go test ./agent/ -race -count=3 -run "TestAgentNeverOverwritesConcurrentStop|TestAgentStartsPerNodeReplacementBeforeStoppingOldInstance|TestAgentStartsNewIncarnationWithoutOldBackoff|TestAgentReplacesContainerOnAnotherImageThanRecorded|TestAgentBacksOff"
    ok  	github.com/boyadzhievb/ccattler/agent	6.316s
    $ go test ./scheduler/ -race -count=3
    ok  	github.com/boyadzhievb/ccattler/scheduler	1.108s
    $ go test ./controllers/ -race -count=3 -run "TestRollout|TestInstanceController"
    ok  	github.com/boyadzhievb/ccattler/controllers	1.272s
    $ go test ./integration/ -race -count=3 -run "TestPerNodeRollout|TestPerNodePlacementLifecycle|TestStatefulRolloutRestartsRecreatedOrdinalOnNewImage|TestRollingUpdateGraduallyReplacesInstances"
    ok  	github.com/boyadzhievb/ccattler/integration	2.368s
    $ openspec validate rollout-correctness --strict
    Change 'rollout-correctness' is valid
    $ go test ./... -count=1
    20 packages ok, 0 failures
    ```

## 4. Testbed and review (main session)

- [ ] 4.1 Testbed by hand (`deploy-run:rollout-rollback`): deploy a service on a good image, apply it with an image whose container exits with code 1 at once, and watch `cca describe service` move from `rolling` to `rollback` to `rolled-back`, with instances back on the good image. Apply an image tag that does not exist, with `progress_deadline 2m`: the reason names the pull error, and after 2 minutes the rollout rolls back with that `rollback_reason`. Apply a service whose resource request no node can meet and see `insufficient_capacity` in the reason and in the rollback reason. Then apply a good second image and see `rolling` → `complete`, with `previous_image` gone. Record the commands and output.
  - First attempt 2026-11-18 on `abe4d19` (deploy cluster, service `m241roll`, `max_unavailable 1`, `progress_deadline 2m`):
    - Crash-looping image (whoami → `mariadb:11`, no password): **FAILED** — `rollout started … revision=1` 10:15:29, `rollout finished state=complete image=…/mariadb:11` 10:15:52; the instance then crash-looped (41 restarts) under a `complete` rollout. Cause: a new instance that reports `running` for 1–2 s counted as done. Fixed by D10 (group 2b); rerun after it.
    - Image tag that does not exist (`whoami:nosuchtag`): **PASSED** — at t+30 s `rolling`, reason `waiting for 66dbc0be92 image-pull-backoff: pulling image …:nosuchtag (attempt 2): … not found`; at t+150 s `rolled-back`, rollback reason `no progress for 2m: waiting for 66dbc0be92 image-pull-backoff: … not found`, whoami running again.
    - Rerun 2026-11-18 on `9a60a15` after group 2b (fresh deploy, `cluster_test.sh --env deploy --skip-build --workload whoami --skip-checks gate-tls,node-failure,multiport,warmzero` → TEST PASSED; controller binary sha256 matches the local build). whoami → `mariadb:11`, `progress_deadline 5m`: t+30 s `rolling`, failures 2, reason `waiting for 208b5404da crash-loop-backoff: exited with code 1; restarting in 20s`; t+90 s `rolled-back`, whoami running again, stayed so through t+270 s. **PASSED.** Findings: (a) rollback reason was `3 failures ≥ 3 on …/mariadb:11: running` — `describeWorstFailingInstance` falls back to the state name when the failing instance has just restarted (status reason empty); should name `exited with code 1`. (b) The old whoami was stopped before any new instance existed (D4 "none created yet" with `max_unavailable 1` on 1 instance) — about 40 s with nothing running; as configured, not a D10 regression.
    - Good second image (whoami → `nginx:1.27`): +5 s `rolling`, reason `waiting for 2326b112ac to stay healthy (10s)`; +30 s `complete`, `Previous image` gone. **PASSED.** Finding (c): the remaining time in the reason is not refreshed (still `(10s)` at age 21 s).
    - Rerun 2026-11-18 on `3762eee` after rework 2c (deploy TEST PASSED, binary sha256 6eb8c8b7… matches), `m241roll` with no `update` block: describe shows `Max unavailable: 0 (default …)`, `Max extra: 1 (default …)`.
      - whoami → `mariadb:11`: sampled every 10 s, the whoami instance `ff98323e9b` ran the whole time (no outage); t+10…80 s `rolling` with reason `waiting for af10f4bdaa crash-loop-backoff: exited with code 1; restarting in …`; t+90 s `rolled-back`, rollback reason `3 failures ≥ 3 on …/mariadb:11: exited with code 1` (finding a fixed). **PASSED.**
      - → `nginx:1.27`: new instance beside whoami, t+24 s `complete`, whoami stopped only then, `Previous image` gone. **PASSED.**
      - mariadb again → `rolled-back`; `cca status` RUNTIME HEALTH: `Rollouts:    m241roll rolled-back: 3 failures ≥ 3 on …/mariadb:11: exited with code 1`; applying `mariadb:11` once more prints `warning: service m241roll: image …/mariadb:11 was rolled back 18s ago (…); applying it starts a new rollout`. **PASSED.** Service restored to nginx.
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
  - Round 2, 2026-11-18, on `9a60a15..3762eee` (REQUEST CHANGES). All round-1 findings confirmed fixed with tests. New findings, verified by the main session in the code (instance controller adds surge to every kind, `ResolveRolloutPacing` ignores kind, `rollBackToPreviousImage` removes surge):
    - H1 rollback with default pacing deadlocks (budget 0, no surge) — real → D14, 2d.2.
    - H2 per-node services cannot place the surge; rollout never completes — real → D14 (DaemonSet defaults 1/0), 2d.1.
    - H3 stateful 1-instance service surges an extra ordinal — real → D14 (StatefulSet defaults), 2d.1.
    - M1 surplus new instance at the end, then a scale-down — real (reviewer probe) → D14, 2d.3.
    - L1 dead `stopWritten`, L3 k8s-to-cca explicit zeros, L4 graphs, L5 wording, L7 assertions outside `waitFor` → 2d.4.
  - Round 4, 2026-11-18, on `152c2fd..59ad61a` (REQUEST CHANGES): H stopped instance holds node resources — real (reviewer probe, verified in the code) → 2g.1. Budget gets the configured count, 0 for per-node — real (verified) → 2g.2. Per-node new instance starts before the old one stops on the same node — real (reading) → 2g.3. Log noise → 2g.4. Style (comment length, `"per-node"` literals, graph lines) → 2g.5. Rejected/accepted: one-cycle races at the incarnation boundary (at most one spurious count against a threshold ≥ 3; the next cycle corrects state) — accepted; a persistently failing `publishInstanceImage` restarts a healthy container each cycle — accepted, needs a store that fails writes while reads work; plain-Put state overwrite of `stopped` — pre-existing, the rollout re-stops it. `0 desired` for per-node predates M241 (M160) but is fixed with 2g.2.
  - Round 3, 2026-11-18, on `3762eee..bbf3694` (REQUEST CHANGES): H1, H2, H3, L1, L3–L7 fixed. M1 fixed only for surge 1 — real (reviewer probe, instance.go adds the full surge while any old instance remains) → 2e.3. Lows (graph call order, field name, `PlacementModePerNode` literal, duplicate loop, describe placement source, per-node warning test) → 2e.4. The loop harness never models a late stop or the not-yet-healthy window, which is why testbed T1 was missed → 2e.0. L2 removed field keeps stored value → documented (2d.4). L6 retarget after revert may pick the bad image → fixed (2d.4); b→c→b older instances ignored by D13 — accepted, the deadline covers it.
- [ ] 4.3 Closing full Deploy Testbed run (job summary says `full run`). Record the run ID.

## 5. Release

- [ ] 5.1 Update the milestones.md M241 row's changelog text to the final scope, then run `scripts/release.sh M241 <version>`.

## Workflow follow-up

- Archive the change after the release (syncs `service-rollout` into openspec/specs/ and the `instance-restart` delta).
