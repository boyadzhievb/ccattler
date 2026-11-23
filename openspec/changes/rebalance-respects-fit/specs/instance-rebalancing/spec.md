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

#### Scenario: A placement and a move in one cycle share the target's room
- **GIVEN** B with room for exactly one more instance's requests, a pending instance the scheduler places on B in
  this cycle, and an imbalance that would move an instance to B
- **WHEN** the scheduler reconciles
- **THEN** the pending instance is placed on B and no instance is moved to B in that cycle; likewise, room a
  preemption in this cycle reserves for its requester is not given to a move (the victim's requests count as freed,
  the requester's as taken)
- Test: `TestRebalanceCountsRoomTakenByThisCyclePlacements`, `TestRebalanceDoesNotMovePreemptedVictim` (a victim of
  this cycle is not moved)

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

A move is stop-old-then-start-new for one ID: a multi-instance service has one backend fewer, a single-instance
service a short outage, until the target runs it. During this gap the instance SHALL NOT look running or ready, the
source removal SHALL never count as evidence that the new placement runs, and a moved instance SHALL NOT move again
within `rebalanceMoveCooldown` (10 minutes); see the scenarios below.

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

#### Scenario: The source backend leaves the VIP, the target's publish wins
- **GIVEN** instance `web-1` running on A with an endpoint, then `placement/instance/web-1` changed to B
- **WHEN** A's agent removes the container, then B's agent publishes `web-1` running
- **THEN** after the removal `web-1` is `pending` without readiness and has no endpoint; after B's publish it is
  `running` on B with an endpoint; and when B published before A's removal, A changes nothing
- Test: `TestSourceAgentMarksMovedInstancePending`

#### Scenario: A failed target startup does not look healthy
- **GIVEN** `web-1` moved to B and left `pending` by A
- **WHEN** B cannot pull the image, cannot start it, or B's agent is dead
- **THEN** `web-1` is `image_pull_backoff` or `failed` on B, or stays `pending` with its placement on the dead B and
  node-failure handling marks it
- Test: `TestTargetStartupFailureAfterMoveIsNotRunning`, `TestNodeFailureMarksMovedInstanceOnDeadTarget`

#### Scenario: The gap per service size
- **GIVEN** a service with two instances, one of them moved, or a service with one instance that is moved
- **WHEN** the source removed the moved container and the target has not published yet
- **THEN** the two-instance service keeps one endpoint; the single-instance service has none and its instance is
  `pending`, not `running`; both are back to full once the target publishes `running`
- Test: `TestMoveGapMultiInstanceKeepsReadyBackend`, `TestMoveGapSingleInstanceShowsNotReady`

#### Scenario: A moved instance is not moved again within the cooldown
- **GIVEN** an imbalance and `derived/scheduler/rebalanced/web-00` written 1 minute ago (or 11 minutes ago)
- **WHEN** the scheduler reconciles
- **THEN** `web-00` is not moved (or may be moved again), a move writes `derived/scheduler/rebalanced/{id}` in its
  change group, and the fact of an instance that is gone is deleted
- Test: `TestRebalanceCooldownKeepsMovedInstanceInPlace`, `TestRebalanceCooldownFactDeletedWithInstance`

#### Scenario: No churn on the deploy cluster
- **GIVEN** a full Deploy Testbed run
- **WHEN** the run ends
- **THEN** the number of distinct whoami instance IDs is within what the test's own scale and node-failure steps
  create, and the run fails otherwise
- Test: deploy check `whoami-churn` in `test/e2e/cluster_test.sh`
