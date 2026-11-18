# Tasks

Implementation (groups 0–3) by the `lead-developer` subagent (Opus, effort medium, `openspec-apply-change` skill; CLAUDE.md "Who does what"). The main session checks its work and does groups 4–5.

## 0. Failing tests first

- [ ] 0.1 Rollout controller tests (controllers/rollout_test.go) from the `service-rollout` spec: `TestRolloutStopsOldInstancesInIDOrder`, `TestRolloutCountsAllUnavailableInstancesAgainstMaxUnavailable`, `TestRolloutStopsUnhealthyOldInstancesFirst`, `TestRolloutCountsNewImageRestartsAndFailures`, `TestRolloutIgnoresNodeFailureForRollback`, `TestRolloutRollsBackSmallServiceOnRestarts`, `TestRolloutRollbackThresholdScalesWithDesiredCount`, `TestRolloutTracksSecondRollout`, `TestRolloutRollbackMovesToRolledBack`, `TestRolloutNewImageMidRolloutKeepsKnownGood`, `TestRolloutReasonNamesWaitingInstance`, `TestRolloutReasonForPauseAndBudget`, `TestRolloutRollsBackAfterProgressDeadlineWithReason`, `TestRolloutProgressRestartsDeadline`, `TestRolloutPausedIgnoresProgressDeadline`, `TestRolloutRollbackStallsWithReason`; plus `TestDescribeServiceShowsRolloutReason` (api). Run `go test ./controllers/ -run 'TestRollout' -count=1` and record each failure (or why a test passes already) here.
- [ ] 0.2 Instance controller tests `TestInstanceControllerRecordsImageOnCreate` and `TestInstanceControllerClearsRestartsOnRecreate` (stateless and stateful-ordinal paths). Agent tests `TestAgentRestartsInstanceWithItsOwnImage` and `TestAgentStartsInstanceWithRecordedImage`, plus the fallback case with no recorded image. Record the failures here.
- [ ] 0.3 Delete `TestRolloutStallsWithoutRollbackWhenNewImageCrashLoops`. Its interim M238 scenario in `instance-restart` now names `TestRolloutRollsBackSmallServiceOnRestarts` (OpenSpec cannot drop a scenario from a MODIFIED requirement, so the scenario stays and says the interim has ended). Note the deletion here.

## 1. Instances keep their image (design D1, D2)

- [ ] 1.1 `types.KeyDerivedInstanceImage(id)` → `derived/instance/{id}/image`, with a doc comment. The instance controller writes it, and deletes `observed/instance/{id}/restarts`, in the creation group of both creation paths (controllers/instance.go). Verify with the 0.2 instance controller tests.
- [ ] 1.2 The agent's start path (`reconcileDesiredInstance`) reads the instance's recorded image. It falls back to `lookupServiceImageFromStore` when the image is absent and logs that once per instance. Verify with the 0.2 agent tests and `go test ./agent/ -count=1`.
- [ ] 1.3 Docs: INSTANCE_LIFECYCLE_GRAPH.md (image lookup, new key), CALL_GRAPH.md where functions change, and design/etcd-schema.md (new key).

## 2. Rollout controller (design D3–D6)

- [ ] 2.1 Read `restarts` and `node_failure` per instance in `extractInstancesByService`, and sort services and instances. Classify instances and count new-image failures (D3), with named constants for the threshold bounds (3, 200) and the divisor. Verify with the counting and threshold tests.
- [ ] 2.2 Stop old instances: unavailable ones first without budget, then healthy ones up to `maxUnavailable − unavailable new-image instances`, in ID order (D4). Verify with the stop-order and budget tests and the existing disruption-budget, surge and paused tests.
- [ ] 2.3 State machine (D5): `types.KeyDerivedServiceRolloutTargetImage`. Start tracking from the state rather than from an empty `previous_image`. Handle an image change during a rollout, rollback with `target_image`, no rollback during `rollback`, `complete` and `rolled-back` with cleanup of `previous_image`, `failures` and surge, and write `rollout/failures` only when it changes. Typed constants for the rollout states. Verify with the bookkeeping tests. Keep every function under 80 lines.
- [ ] 2.4 Reason and progress deadline (D7): `rollout/reason`, `rollout/rollback_reason`, `rollout/progress_at` and `rollout/running_new` keys. The scheduler's unplaced reason and requirements go into the reason, with `derived/scheduler/` added to Watch. A `Now` field on the controller. Deadline → rollback with `rollback_reason`. A failure rollback also writes `rollback_reason`. A rollback without progress → `stalled`. Paused rollouts don't time out. Log one warning per transition. Verify with the reason and deadline tests.
- [ ] 2.5 DSL (lang/parser.go, lang/formatter.go, compiler) `update { progress_deadline <duration> }` → `desired/service/{svc}/update/progress_deadline`. The parser refuses an invalid or non-positive value. Verify with parser and compiler tests, and update design/dsl-reference.md.
- [ ] 2.6 `cca describe service` shows the state (`rolled-back`, `stalled`), the failure count, the reason and the rollback reason. Verify with `TestDescribeServiceShowsRolloutReason`.
- [ ] 2.6a Status visibility (D8): the status API service entries carry the rollout state, reason and rollback reason. `cca status` RUNTIME HEALTH lists `stalled` and `rolled-back` services, verified with `TestStatusTextListsStalledAndRolledBackServices`. health_check.sh Check 6 warns for them: run `shellcheck -S warning` and run the jq filter on a sample status JSON, then record the output.
- [ ] 2.7 Docs: Flow 34 "Rolling Update and Rollback" in design/runtime-invariant-flows.md, with links from Flow 18 and Flow 33. Update the rollout section of CONTROLLER_DATA_FLOW_GRAPH.md (functions, line numbers, keys) and design/etcd-schema.md (`target_image`, `failures`, states).

## 3. Checks before handing back

- [ ] 3.1 gofmt, `golangci-lint run ./controllers/... ./agent/... ./types/... ./api/...` (0 issues), go build and vet, `go test ./controllers/ ./agent/ ./types/ ./api/ ./integration/ -count=1`, touched agent tests with `-race -count=3`, and `openspec validate rollout-correctness --strict`. Paste the results here.

## 4. Testbed and review (main session)

- [ ] 4.1 Testbed by hand (`deploy-run:rollout-rollback`): deploy a service on a good image, apply it with an image whose container exits with code 1 at once, and watch `cca describe service` move from `rolling` to `rollback` to `rolled-back`, with instances back on the good image. Apply an image tag that does not exist, with `progress_deadline 2m`: the reason names the pull error, and after 2 minutes the rollout rolls back with that `rollback_reason`. Apply a service whose resource request no node can meet and see `insufficient_capacity` in the reason and in the rollback reason. Then apply a good second image and see `rolling` → `complete`, with `previous_image` gone. Record the commands and output.
- [ ] 4.2 Independent review: `code-reviewer` (Opus, effort high) on the milestone diff against this design and the specs. Record each finding as fixed or rejected, with one line of reasoning.
- [ ] 4.3 Closing full Deploy Testbed run (job summary says `full run`). Record the run ID.

## 5. Release

- [ ] 5.1 Update the milestones.md M241 row's changelog text to the final scope, then run `scripts/release.sh M241 <version>`.

## Workflow follow-up

- Archive the change after the release (syncs `service-rollout` into openspec/specs/ and the `instance-restart` delta).
