# Spec Delta

## ADDED Requirements

### Requirement: Unplaced-demand facts exist only while their instance waits
`derived/scheduler/unplaced/{id}/reason` and `/requirements` SHALL exist only while instance `{id}` exists, is
`pending`, is not controller-stopped and has no placement. Once any of these stops being true (placed, stopped, any
other state, or removed from the store), the scheduler SHALL delete both facts, for at most 20 instances per cycle
in instance-ID order so placements are not delayed, and the rest in the following cycles. Facts left by an earlier
version SHALL be deleted the same way, with no migration step.

#### Scenario: Unplaced instance scaled away
- **GIVEN** `web-1` is `pending` with no placement, and its unplaced reason `insufficient_capacity` and requirements
  are stored
- **WHEN** `web-1`'s state becomes `stopped`, or `derived/instance/web-1/controller_stopped` is `true`, and the
  scheduler reconciles
- **THEN** the scheduler deletes `derived/scheduler/unplaced/web-1/reason` and `/requirements`
- Test: `TestSchedulerDeletesUnplacedFactsOfStoppedInstance`

#### Scenario: Many instances stop waiting at once
- **GIVEN** stored unplaced facts for 50 instances that are all `stopped`, and one pending instance that fits a node
- **WHEN** the scheduler reconciles
- **THEN** it proposes deletes for the 20 lowest instance IDs only (40 changes) and also places the pending
  instance; reconciling again with those deleted removes the next 20
- Test: `TestSchedulerCapsUnplacedCleanupPerCycle`

#### Scenario: Unplaced instance removed from the store
- **GIVEN** unplaced reason and requirements are stored for `web-1`, and no `observed/instance/web-1/*` fact exists
- **WHEN** the scheduler reconciles
- **THEN** both facts are deleted
- Test: `TestSchedulerDeletesUnplacedFactsOfRemovedInstance`

#### Scenario: Waiting instance keeps its facts
- **GIVEN** `web-1` is `pending`, fits no node, and its unchanged unplaced facts are stored
- **WHEN** the scheduler reconciles
- **THEN** it proposes neither a put nor a delete for those facts
- Test: `TestSchedulerDoesNotRewriteUnchangedUnplacedFacts`

#### Scenario: Placed instance loses its facts
- **GIVEN** `web-1` has stored unplaced facts and gets a placement (stored, or in this cycle)
- **WHEN** the scheduler reconciles
- **THEN** both facts are deleted
- Test: `TestSchedulerDeletesUnplacedFactsOfPlacedInstance`

#### Scenario: Scale to zero on the deploy cluster
- **GIVEN** a service with `memory 64Gi` and two pending instances, watched with `cca watch derived/scheduler/unplaced/`
- **WHEN** the service is scaled to 0
- **THEN** a delete of each instance's reason and requirements appears within 10 seconds
- Test: deploy-cluster check by hand, `deploy-run:unplaced-cleanup` (recorded in tasks.md)

### Requirement: Cluster scaling counts only instances that still wait
The cluster-autoscale controller SHALL count an unplaced-demand fact as demand only when its instance exists, is
`pending`, is not controller-stopped and has no placement, whatever facts remain under
`derived/scheduler/unplaced/`.

#### Scenario: Leftover fact for a stopped instance
- **GIVEN** `derived/scheduler/unplaced/web-1/reason` is `insufficient_capacity` with requirements, and `web-1` is
  `stopped` (or controller-stopped, or has no instance facts)
- **WHEN** the cluster-autoscale controller reconciles
- **THEN** it writes no capacity request for `web-1`
- Test: `TestClusterAutoscaleIgnoresUnplacedFactsOfInstancesNotWaiting`

#### Scenario: Waiting instance still asks for a node
- **GIVEN** the same facts with `web-1` `pending` and no placement
- **WHEN** the cluster-autoscale controller reconciles
- **THEN** it writes a `pending` capacity request covering `web-1`
- Test: `TestClusterAutoscaleEmitsCapacityRequestForInsufficientCapacity`
