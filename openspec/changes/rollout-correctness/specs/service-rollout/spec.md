# Spec Delta

## Purpose
States how a change of a service's image reaches its instances: paced within `max_unavailable`, unhealthy old instances replaced first, failures of the new image counted, and a broken image rolled back automatically.

## ADDED Requirements

### Requirement: Old instances are replaced within max_unavailable
When a service's desired image differs from the image of some of its instances, the rollout controller SHALL stop old-image instances so that expected minus healthy instances never exceeds `max_unavailable` (unset: 25 % down; unset `max_extra`: 25 % up). It SHALL stop healthy old instances only while at least one new-image instance is healthy (see "A new instance counts only once it stays healthy"), or while none has been created yet, and SHALL pick them in instance-ID order.

#### Scenario: Paced and deterministic
- **GIVEN** a service with 4 running instances on image `a`, `max_unavailable 1`, whose desired image changes to `b`
- **WHEN** the rollout controller reconciles twice on the same facts
- **THEN** both times it proposes stopping exactly one instance, the one with the lowest instance ID
- Test: `TestRolloutStopsOldInstancesInIDOrder`

#### Scenario: Stopped and not-yet-healthy instances count against the budget
- **GIVEN** a service with 4 expected instances, `max_unavailable 1`, two running old instances, one healthy new instance and one pending new instance
- **WHEN** the rollout controller reconciles
- **THEN** it stops no old instance, because one of the 4 is already not healthy
- Test: `TestRolloutCountsUnavailableCapacityAgainstMaxUnavailable`

#### Scenario: Defaults surge a one-instance service
- **GIVEN** a 1-instance service with no `update` block whose desired image changes
- **WHEN** the rollout controller reconciles before the new instance is healthy
- **THEN** a surge of one instance is allowed and the old instance is not stopped; once the new instance is healthy the old one is stopped
- Test: `TestRolloutDefaultsSurgeSingleInstanceService`

#### Scenario: Unavailable instances use up the budget
- **GIVEN** a rollout with `max_unavailable 2` where one instance of the new image is in `crash-loop-backoff` and a healthy new-image instance is running
- **WHEN** the rollout controller reconciles
- **THEN** it stops one healthy old instance, not two
- Test: `TestRolloutCountsAllUnavailableInstancesAgainstMaxUnavailable`

### Requirement: Unhealthy old instances are replaced first
Old-image instances that are `failed`, `crash-loop-backoff`, `image-pull-backoff`, or on a lost node SHALL be stopped before any healthy old instance and SHALL NOT use up `max_unavailable`: they are already unavailable.

#### Scenario: Crash-looping old instance goes first
- **GIVEN** a rollout with `max_unavailable 1` where one old-image instance is in `crash-loop-backoff` and two old-image instances are running
- **WHEN** the rollout controller reconciles
- **THEN** it stops the crash-looping instance, and no healthy old instance in the same pass
- Test: `TestRolloutStopsUnhealthyOldInstancesFirst`

### Requirement: Failures of the new image are counted
The rollout controller SHALL count new-image failures as the sum of restarts of the new-image instances plus the new-image instances that are `failed`. Instances on a lost node SHALL NOT count. It SHALL publish the count as `derived/service/{svc}/rollout/failures` while a rollout is in progress.

#### Scenario: Restarts and failed instances count
- **GIVEN** a rollout to image `b` with one `b` instance restarted twice and one `b` instance `failed`
- **WHEN** the rollout controller reconciles
- **THEN** `rollout/failures` is 3
- Test: `TestRolloutCountsNewImageRestartsAndFailures`

#### Scenario: Restarts from before the rollout do not count
- **GIVEN** a rollout from `b` back to `a` (the stored previous image) whose `a` instances restarted 2 and 1 times before the rollout started
- **WHEN** the rollout controller reconciles
- **THEN** it does not roll back and `rollout/failures` is 0
- Test: `TestRolloutRevertToPreviousImageNeverRollsBack`

#### Scenario: Node loss is not a failure of the image
- **GIVEN** a rollout to image `b` whose `b` instances are all on a node marked failed (`node_failure`)
- **WHEN** the rollout controller reconciles
- **THEN** `rollout/failures` is 0 and the desired image is not reverted
- Test: `TestRolloutIgnoresNodeFailureForRollback`

### Requirement: A broken new image is rolled back
When new-image failures reach half the service's desired instance count (rounded up), kept between 3 and 200, the rollout controller SHALL write the previous image to `desired/service/{svc}/image`, set the rollout state to `rollback`, and record in `rollout/rollback_reason` the failure count, the threshold and the latest exit reason of a failing instance. It SHALL NOT start another rollback while one is in progress.

#### Scenario: One-instance service rolls back
- **GIVEN** a service with 1 desired instance rolling out from `a` to `b`, whose `b` instance has restarted 3 times
- **WHEN** the rollout controller reconciles
- **THEN** `desired/service/{svc}/image` is `a`, `rollout/state` is `rollback`, and `rollout/rollback_reason` names 3 failures, the threshold 3 and `exited with code 1`
- Test: `TestRolloutRollsBackSmallServiceOnRestarts`

#### Scenario: Threshold scales with the service
- **GIVEN** a service with 10 desired instances rolling out to `b`, with 4 new-image failures
- **WHEN** the rollout controller reconciles
- **THEN** it does not roll back; with 5 failures it does
- Test: `TestRolloutRollbackThresholdScalesWithDesiredCount`

#### Scenario: Broken image rolled back on a real cluster
- **GIVEN** a service running image `a` on the deploy testbed
- **WHEN** its configuration is applied with an image whose container exits with code 1 at once
- **THEN** within a few minutes `cca describe service` shows state `rolled-back`, its instances run `a` again, and the old instances that were running `a` are still running or have been replaced by `a` instances
- Check: `deploy-run:rollout-rollback` (by hand)

### Requirement: A new instance counts only once it stays healthy
For stopping old instances, progress and finishing, a new-image instance SHALL count as healthy only when it is `running`, has run since its last start (`started_at`, written by the agent at every start) for at least `min_healthy_time` (default 10 seconds), and is ready if the service has a readiness probe. Without `started_at` a running instance SHALL count as healthy.

#### Scenario: Image that exits after a moment does not replace the old instance
- **GIVEN** a 1-instance service rolling out from `a` to `b`, whose `b` instance is `running` with `started_at` 2 seconds ago and the default `min_healthy_time`
- **WHEN** the rollout controller reconciles
- **THEN** it stops no old instance, the state stays `rolling`, and the reason says it waits for the `b` instance to stay healthy
- Test: `TestRolloutDoesNotStopOldBeforeMinHealthyTime`

#### Scenario: Rollout does not finish before the window
- **GIVEN** a rollout whose only old instance is gone and whose new instance started 5 seconds ago, `min_healthy_time 10s`
- **WHEN** the rollout controller reconciles, and again 6 seconds later
- **THEN** the first pass leaves the state `rolling`; the second sets it to `complete`
- Test: `TestRolloutDoesNotFinishBeforeMinHealthyTime`

#### Scenario: Readiness probe must pass
- **GIVEN** a service with a readiness probe whose new instance has run for longer than `min_healthy_time` but is `not-ready`
- **WHEN** the rollout controller reconciles
- **THEN** it counts the instance as not healthy and stops no healthy old instance
- Test: `TestRolloutRequiresReadinessWhenProbed`

#### Scenario: Instance from an older agent
- **GIVEN** a running new instance with no `started_at`
- **WHEN** the rollout controller reconciles
- **THEN** it counts the instance as healthy
- Test: `TestRolloutCountsInstanceWithoutStartedAtAsHealthy`

#### Scenario: Agent records every start
- **WHEN** the agent starts an instance, and later restarts it in place
- **THEN** `observed/instance/{id}/started_at` is set at each start and the second value is later than the first
- Test: `TestAgentRecordsStartedAtOnEveryStart`

#### Scenario: DSL
- **WHEN** a service declares `update { min_healthy_time 30s }`
- **THEN** it compiles to `desired/service/{svc}/update/min_healthy_time` = `30s`; `min_healthy_time -1s` or `min_healthy_time soon` is refused by the parser
- Test: `TestCompileUpdateMinHealthyTime`

### Requirement: Rollout state ends and is reset
When no old-image instance is left and the new-image instances are healthy (at least the expected count), the rollout controller SHALL set the state to `complete` after a rollout and to `rolled-back` after a rollback (also from `stalled`), and SHALL delete `previous_image`, `failures`, `reason` and the surge allowance; `rollback_reason` stays until the next rollout. The next image change SHALL record its own previous image, revision and history entry.

#### Scenario: Second rollout is tracked
- **GIVEN** a service that completed a rollout from `a` to `b`
- **WHEN** its desired image changes to `c`
- **THEN** `rollout/state` is `rolling`, `rollout/previous_image` is `b`, the revision is one higher and the history records `b`
- Test: `TestRolloutTracksSecondRollout`

#### Scenario: Rollback finishes
- **GIVEN** a rollback from `b` to `a`
- **WHEN** no instance runs `b` any more
- **THEN** `rollout/state` is `rolled-back` and `rollout/previous_image` is gone
- Test: `TestRolloutRollbackMovesToRolledBack`

### Requirement: A waiting rollout says why
While a rollout or rollback is in progress, the rollout controller SHALL publish `derived/service/{svc}/rollout/reason`, naming what it waits for: a new-image instance that is not running (its ID, state, and its status reason or scheduler unplaced reason), the `max_unavailable` budget in use (the unavailable instance IDs), a pause, or the previous image failing during a rollback. The reason SHALL be deleted when the rollout ends.

#### Scenario: New image cannot be pulled
- **GIVEN** a rollout whose only new-image instance `web-3` is in `image-pull-backoff` with status reason `pull failed: not found`
- **WHEN** the rollout controller reconciles
- **THEN** `rollout/reason` names `web-3`, `image-pull-backoff` and `pull failed: not found`
- Test: `TestRolloutReasonNamesWaitingInstance`

#### Scenario: Paused and budget-bound rollouts
- **WHEN** a rollout is paused, or cannot stop an old instance because `max_unavailable` is used up
- **THEN** `rollout/reason` says it is paused, or names the unavailable instances
- Test: `TestRolloutReasonForPauseAndBudget`

### Requirement: A rollout without progress is rolled back with its reason
When a rolling, unpaused rollout has made no progress (no old instance stopped and no new-image instance started running) for its progress deadline (`update { progress_deadline }`, default 10 minutes), the rollout controller SHALL roll back as for failures and record why in `rollout/rollback_reason`: the deadline and the reason the rollout was waiting for. A rollback that itself makes no progress for the deadline SHALL become `stalled` with its reason and SHALL NOT roll back again.

#### Scenario: No room for the new instance
- **GIVEN** a rollout from `a` to `b` whose new-image instance `web-3` has been unplaced with scheduler reason `insufficient_capacity` for longer than the progress deadline
- **WHEN** the rollout controller reconciles
- **THEN** `desired/service/{svc}/image` is `a`, `rollout/state` is `rollback`, and `rollout/rollback_reason` names the deadline, `web-3`, `insufficient_capacity` and the instance's resource requirements
- Test: `TestRolloutRollsBackAfterProgressDeadlineWithReason`

#### Scenario: Progress restarts the deadline
- **GIVEN** a rollout whose last progress was 9 minutes ago with a 10-minute deadline
- **WHEN** a new-image instance starts running
- **THEN** no rollback happens and the deadline counts from that moment
- Test: `TestRolloutProgressRestartsDeadline`

#### Scenario: Paused rollout does not time out
- **GIVEN** a paused rollout with no progress for longer than the deadline
- **WHEN** the rollout controller reconciles
- **THEN** it does not roll back
- Test: `TestRolloutPausedIgnoresProgressDeadline`

#### Scenario: Rollback that cannot progress
- **GIVEN** a rollback whose previous-image instance has been unplaced for longer than the deadline
- **WHEN** the rollout controller reconciles
- **THEN** `rollout/state` is `stalled`, `rollout/reason` names the instance and its scheduler reason, and the desired image is unchanged
- Test: `TestRolloutRollbackStallsWithReason`

#### Scenario: Shown to the user
- **WHEN** the user runs `cca describe service` after a rollback
- **THEN** it shows the state and `rollout/rollback_reason`; the rollback reason stays until the next rollout starts
- Test: `TestDescribeServiceShowsRolloutReason`

### Requirement: Stalled and rolled-back services are shown without asking
The status API SHALL include each service's rollout state, reason and rollback reason. The RUNTIME HEALTH summary of `cca status` SHALL list services whose rollout is `stalled` or `rolled-back`, with the reason. The e2e health check SHALL report them as warnings with the reason. A `rolled-back` service SHALL stay listed until its next rollout starts.

#### Scenario: Status lists them
- **GIVEN** one service in `stalled` with reason `web-3 unplaced: insufficient_capacity` and one `rolled-back` with rollback reason `3 failures ≥ 3 on app:v2: exited with code 1`
- **WHEN** the user runs `cca status`
- **THEN** RUNTIME HEALTH lists both services with their state and reason
- Test: `TestStatusTextListsStalledAndRolledBackServices`

#### Scenario: Health check warns
- **WHEN** the status JSON has a service whose rollout is `stalled` or `rolled-back`
- **THEN** test/e2e/health_check.sh prints a warning naming the service, the state and the reason
- Check: health_check.sh run on a sample status JSON (by hand), and `deploy-run:rollout-rollback`

### Requirement: Applying a rolled-back image warns
A rollback SHALL record the image it rolled back from and when, in `rollout/rolled_back_image` and `rollout/rolled_back_at`, kept with `rollout/rollback_reason` until the next rollout starts. When a configuration applied to the cluster sets a service's image to its `rolled_back_image`, the apply SHALL succeed and its response SHALL carry a warning naming the service, the image, how long ago it was rolled back, and the rollback reason; `cca apply` SHALL print it.

#### Scenario: Same broken image applied again
- **GIVEN** service `web` rolled back from `app:v2` 5 minutes ago with reason `3 failures ≥ 3 on app:v2: exited with code 1`
- **WHEN** the user applies a configuration with `web` on `app:v2`
- **THEN** the apply succeeds and prints a warning naming `web`, `app:v2`, about 5 minutes ago and the reason
- Test: `TestApplyWarnsForRolledBackImage`

#### Scenario: Other image applies without warning
- **GIVEN** the same rolled-back service
- **WHEN** the user applies `web` on `app:v3`
- **THEN** no rollback warning is printed
- Test: `TestApplyWarnsForRolledBackImage`

### Requirement: A new image during a rollout keeps the last known-good image
When the desired image changes while a rollout or rollback is in progress, the rollout controller SHALL start a new rollout to the new image whose previous image stays the image of the last completed rollout.

#### Scenario: Image changed mid-rollout
- **GIVEN** a rollout from `a` to `b` in progress
- **WHEN** the desired image changes to `c`
- **THEN** `rollout/state` is `rolling`, `rollout/previous_image` stays `a`, and a later rollback goes to `a`
- Test: `TestRolloutNewImageMidRolloutKeepsKnownGood`
