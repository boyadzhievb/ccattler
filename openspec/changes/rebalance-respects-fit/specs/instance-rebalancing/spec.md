# Spec Delta

## Purpose
States when the scheduler may move a running instance to another node to even out load, which nodes may receive
it, and what the agents on the source and target nodes do, so a move never ends in a stop and a replacement.

## ADDED Requirements

### Requirement: A rebalance target fits the instance
The scheduler SHALL move an instance during rebalancing only to a node whose remaining CPU and memory, after the
moves already chosen in the same cycle, cover the instance's requests, and that has no
`derived/node/{id}/pressure` marker. When no node qualifies, the instance SHALL stay where it is.

#### Scenario: Count-imbalanced but memory-full target
- **GIVEN** node A with 10 running instances and node B with 3, where B's remaining memory is less than the
  instance's memory request
- **WHEN** the scheduler reconciles
- **THEN** no instance is moved to B
- Test: `TestRebalanceSkipsTargetWithoutRoom`

#### Scenario: Target under memory pressure
- **GIVEN** the same imbalance, B with room by requests, and `derived/node/B/pressure` stored
- **WHEN** the scheduler reconciles
- **THEN** no instance is moved to B
- Test: `TestRebalanceSkipsTargetUnderPressure`

#### Scenario: Two moves share one target's room
- **GIVEN** B with room for exactly one more instance's requests and an imbalance that would move two
- **WHEN** the scheduler reconciles
- **THEN** at most one instance is moved to B in that cycle
- Test: `TestRebalanceCountsRoomUsedByEarlierMoves`

#### Scenario: Room available
- **GIVEN** the imbalance and B with room and no pressure marker
- **WHEN** the scheduler reconciles
- **THEN** an instance is moved to B (its placement changes; its ID stays)
- Test: `TestRebalanceMovesToTargetWithRoom`

### Requirement: A moved instance keeps running under its ID
When an instance's placement moves to another node, the source node's agent SHALL remove its local container
without writing `stopped` for that instance ID, and the target node's agent SHALL start it and publish its state as
for any instance placed there. The source agent SHALL still write `stopped` when the instance has no placement or a
controller stopped it.

#### Scenario: Source agent after a move
- **GIVEN** instance `web-1` running on node A, then `placement/instance/web-1` changed to B
- **WHEN** A's agent reconciles
- **THEN** A's container for `web-1` is removed and `observed/instance/web-1/state` is not set to `stopped` by A
- Test: `TestAgentDoesNotMarkMovedInstanceStopped`

#### Scenario: Scale-down still stops
- **GIVEN** instance `web-1` running on A, then `derived/instance/web-1/controller_stopped = true` (placement still A)
- **WHEN** A's agent reconciles
- **THEN** the container is removed and `observed/instance/web-1/state` becomes `stopped`
- Test: `TestAgentDoesNotMarkMovedInstanceStopped` (sub-case `controller stop`)

#### Scenario: No churn on the deploy cluster
- **GIVEN** a full Deploy Testbed run
- **WHEN** the run ends
- **THEN** the number of distinct whoami instance IDs is within what the test's own scale and node-failure steps
  create, and the run fails otherwise
- Test: deploy check `whoami-churn` in `test/e2e/cluster_test.sh`
