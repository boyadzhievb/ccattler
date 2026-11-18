# instance-restart Specification

## Purpose
States how the node agent restarts an instance whose container exits: in place, with exponential back-off, counted and visible to the user.

## Requirements

### Requirement: An exited container is restarted in place with back-off
When an instance's container exits while the instance is desired on the node, the agent SHALL restart it on the same node under the same instance ID, waiting before each restart: 10 s after the first exit, doubling on each further exit, at most 5 min. An instance that has run for 10 min without exiting SHALL start again from the shortest wait.

#### Scenario: Repeated exits wait longer each time
- **GIVEN** an instance whose container exits right after every start
- **WHEN** it has exited three times
- **THEN** the agent waited the base delay before the second start and twice the base delay before the third, and did not start it earlier
- Test: `TestAgentBacksOffCrashLoopingInstance`

#### Scenario: Back-off resets after a clean run
- **GIVEN** an instance that exited twice and then ran longer than the reset period
- **WHEN** it exits again
- **THEN** the agent waits the base delay again, not four times it
- Test: `TestAgentResetsCrashLoopBackoffAfterStableRun`

### Requirement: Restarts are counted and the wait is visible
Every in-place restart SHALL increment `observed/instance/{id}/restarts`. While the agent waits to restart, the instance's observed state SHALL be `crash-loop-backoff` and its status reason SHALL name the exit and the time until the next attempt.

#### Scenario: Count and state during a crash loop
- **GIVEN** an instance whose container exited with code 1 twice and was restarted once in between
- **WHEN** the user runs `cca describe instance` while the agent waits after the second exit
- **THEN** it shows restarts 1, state `crash-loop-backoff` and a reason like `exited with code 1; restarting in 20s`; after the next restart it shows restarts 2
- Test: `TestAgentCountsRestartsAndPublishesCrashLoopState`

### Requirement: A crash-looping instance is not replaced
An instance in `crash-loop-backoff` SHALL NOT be treated as failed: the failure controller SHALL NOT replace it or count it toward its crash-loop circuit breaker.

#### Scenario: Failure controller leaves it alone
- **GIVEN** an instance with observed state `crash-loop-backoff`
- **WHEN** the failure controller reconciles
- **THEN** it proposes no replacement for that instance
- Test: `TestFailureControllerIgnoresCrashLoopBackoff`

#### Scenario: Crash loop on a real cluster
- **GIVEN** a service whose image exits with code 1 at once, deployed on the deploy testbed
- **WHEN** it has been running for a few minutes
- **THEN** its instance shows a growing restart count and `crash-loop-backoff`, restarts are minutes apart, the node's CNI address allocations do not grow with each restart, and the service's VIP backends and eBPF identity entries list only the current IP
- Check: `deploy-run:crash-loop` (by hand)

### Requirement: An instant exit is restarted in place
A container that has already exited when the agent first observes it after a successful start SHALL be handled like any other exit: restarted in place under the same instance ID with back-off, counted, shown as `crash-loop-backoff`. The agent SHALL NOT publish `failed` for it.

#### Scenario: Exit before the first look
- **GIVEN** a service whose container exits with code 1 before the agent observes it after the start
- **WHEN** the agent has started it twice
- **THEN** the instance keeps its ID, its state is `crash-loop-backoff`, its restart count is 1, and `failed` was never published for it
- Test: `TestAgentBacksOffInstantExitInPlace`

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
