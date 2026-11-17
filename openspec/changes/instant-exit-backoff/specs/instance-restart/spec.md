# Spec Delta

## ADDED Requirements

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
