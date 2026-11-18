# Spec Delta

## ADDED Requirements

### Requirement: An in-place restart keeps the instance's image
An instance SHALL be started, and restarted in place, with the image recorded for it when it was created, not with the service's current desired image. Only replacing the instance moves it to another image. An instance created before images were recorded SHALL use the service's desired image.

#### Scenario: Old instance crashes during a rollout
- **GIVEN** an instance created with image `a`, whose service's desired image has since changed to `b`
- **WHEN** its container exits and the agent restarts it in place
- **THEN** it runs `a` again and `observed/instance/{id}/image` stays `a`
- Test: `TestAgentRestartsInstanceWithItsOwnImage`

#### Scenario: New instance gets the desired image
- **GIVEN** a service whose desired image is `b`
- **WHEN** the instance controller creates an instance for it
- **THEN** the instance's recorded image is `b` and the agent starts it with `b`
- Test: `TestInstanceControllerRecordsImageOnCreate`, `TestAgentStartsInstanceWithRecordedImage`

### Requirement: A created instance starts with no restarts
When the instance controller creates an instance, including a stateful ordinal recreated under an earlier instance ID, the instance's restart count SHALL start at zero.

#### Scenario: Stateful ordinal recreated
- **GIVEN** a stateful instance `db-0` with `observed/instance/db-0/restarts` 7 that was stopped
- **WHEN** the instance controller creates `db-0` again
- **THEN** `observed/instance/db-0/restarts` is gone
- Test: `TestInstanceControllerClearsRestartsOnRecreate`

## MODIFIED Requirements

### Requirement: A workload process that cannot start is restarted in place
When a start fails because the workload's own process could not be started (the runtime reports its task start failed: bad entrypoint, missing binary), the agent SHALL handle it like an exit with back-off, in place, with the start error as the reason. A start failure that is not the workload's own (image not found, network setup, creating the container or task) SHALL still be published as `failed`.

#### Scenario: Entrypoint does not exist
- **GIVEN** a service whose command is a path that does not exist in the image
- **WHEN** the agent tries to start it
- **THEN** the instance keeps its ID, shows `crash-loop-backoff` with a reason naming the start error, and is retried after the back-off
- Test: `TestAgentBacksOffProcessStartFailureInPlace`, Check: `deploy-run:instant-exit` (by hand)

#### Scenario: Infrastructure start failure
- **GIVEN** a start that fails while setting up the network
- **WHEN** the agent tries to start the instance
- **THEN** the instance is published `failed`, as before
- Test: `TestAgentPublishesFailedForInfrastructureStartError`

#### Scenario: Start errors are classified by the runtime
- **WHEN** the containerd runtime fails to create or start the task because runc could not exec the workload's process (missing or non-executable entrypoint)
- **THEN** its start error says the workload's process failed; other start errors (image, network, container creation, other task failures) do not
- Test: `TestStartErrorMarksProcessStartFailure`

#### Scenario: Exit between the runtime listing and the status check
- **GIVEN** a running instance whose container exits after the agent listed it as running but before it reads its status
- **WHEN** the agent observes the exited status on that cycle
- **THEN** it does not publish `failed`; the next cycle restarts it in place with back-off
- Test: `TestAgentRunningPathRaceDoesNotPublishFailed`

#### Scenario: A crash-looping new image stalls the rollout (interim, until M241)
- **GIVEN** a rolling update whose new-image instance is in `crash-loop-backoff`
- **WHEN** its restarts reach the rollback threshold
- **THEN** the rollout no longer stalls: it rolls back as `service-rollout` requires (M241 ended the interim)
- Test: `TestRolloutRollsBackSmallServiceOnRestarts`

#### Scenario: Logs of a crash-looping instance are read only for real instance IDs
- **WHEN** the agent debug logs endpoint (used by `cca logs`, `--previous` included) gets an `instance` that is not an instance ID (`../x`, `a/b`, `..`)
- **THEN** it answers 400 and the runtime builds no path from it; generated and test instance IDs are still served
- Test: `TestDebugLogsRefusesUnsafeInstanceID`, `TestContainerdLogsRefusesUnsafeInstanceID`

#### Scenario: Health summaries show crash-looping instances
- **WHEN** some instances are in `crash-loop-backoff`
- **THEN** the RUNTIME HEALTH summary of `cca status` and the e2e health check count them on their own as unhealthy, not as failed and not dropped
- Test: `TestStatusTextCountsCrashLoopBackoffInRuntimeHealth`
