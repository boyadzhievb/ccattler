# Spec Delta

## Purpose
States which container logs of an instance a node keeps and how a user reads the current and the previous run, so the output of a container that exited is not lost when it restarts.

## ADDED Requirements

### Requirement: The previous run's log survives a restart
When an instance's container is started again on the same node, the log of the run before SHALL be kept as the instance's previous log and the new run SHALL write a new, empty log. At most one previous log SHALL be kept per instance. Restarting a container SHALL NOT delete either log.

#### Scenario: Restart keeps the failed run's output
- **GIVEN** an instance whose container wrote "boom" to its log and exited
- **WHEN** the container is started again on the same node
- **THEN** the previous log contains "boom" and the current log does not
- Test: `TestContainerdStartKeepsPreviousRunLog`

#### Scenario: Only one previous log
- **GIVEN** an instance restarted twice, with runs writing "first", "second" and "third"
- **WHEN** the third run is running
- **THEN** the previous log contains "second" and not "first", and the current log contains "third"
- Test: `TestContainerdStartKeepsPreviousRunLog`

### Requirement: An instance's logs are removed when it leaves the node
When an instance is no longer placed on a node (removed, moved, replaced), the node SHALL delete its log directory, current and previous logs included.

#### Scenario: Removed instance
- **GIVEN** an instance with a current and a previous log
- **WHEN** it is removed from the node
- **THEN** its log directory no longer exists
- Test: `TestAgentRemovesLogsOfInstanceLeavingNode`

### Requirement: The user can read the previous run
`cca logs --previous` SHALL show the previous run's log of the selected instance. When the instance has no previous run the command SHALL say so and exit with an error. A runtime that does not keep previous logs SHALL answer that the previous log is not available.

#### Scenario: Crash loop on a real cluster
- **GIVEN** a service whose MariaDB image exits at once because no password variable is set, crash-looping on the deploy testbed
- **WHEN** the user runs `cca logs crashloop --previous`
- **THEN** the output contains the entrypoint's error about the missing password option
- Check: `deploy-run:previous-log` (by hand)

#### Scenario: Previous option reaches the runtime
- **WHEN** the agent's debug API receives a logs request with `previous=true`
- **THEN** it asks the runtime for the previous log
- Test: `TestDebugLogsPassesPreviousOption`

#### Scenario: No previous run
- **GIVEN** an instance started once
- **WHEN** the previous log is requested
- **THEN** the answer is an error that names the instance and says it has no previous run
- Test: `TestContainerdLogsPreviousWithoutPreviousRun`
