# Spec Delta

## Purpose
States how the node agent restarts an instance whose container exits: in place, with exponential back-off, counted and visible to the user.

## ADDED Requirements

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
- **GIVEN** an instance whose container exited with code 1 twice
- **WHEN** the user runs `cca describe instance` while the agent waits
- **THEN** it shows restarts 2, state `crash-loop-backoff` and a reason like `exited with code 1; restarting in 20s`
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
