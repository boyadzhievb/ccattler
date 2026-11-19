# Spec Delta

## Purpose
States how a service's CPU and memory requests and limits are written, which values an apply refuses, and how every
component reads them, so placement checks real node capacity and a capacity shortage is reported.

## ADDED Requirements

### Requirement: Resource values use one notation
CPU values in `resources` and `scale vertical` SHALL be millicores with the suffix `m`, or cores (fractions allowed).
Memory values SHALL be a number with a binary suffix (`Ki`…`Ei`), a decimal suffix (`k`…`E`), or no suffix (bytes).
Every component that reads these values SHALL give the same value the same meaning.

#### Scenario: CPU forms
- **WHEN** a service declares `cpu 250m`, `cpu 0.5` or `cpu 2`
- **THEN** its request is 250, 500 or 2000 millicores, respectively, for placement and for the container's CPU limit
- Test: `TestParseCPUMillicoresAcceptsKubernetesForms`

#### Scenario: Memory forms
- **WHEN** a service declares `memory 512Mi`, `memory 1.5Gi`, `memory 1G` or `memory 1048576`
- **THEN** its request is 536870912, 1610612736, 1000000000 or 1048576 bytes, respectively
- Test: `TestParseMemoryBytesAcceptsKubernetesForms`

#### Scenario: Equal values written differently are one QoS class
- **WHEN** a service declares `cpu 1`, `cpu_limit 1000m`, `memory 1Gi` and `memory_limit 1024Mi`
- **THEN** `derived/service/{svc}/qos_class` is `Guaranteed`
- Test: `TestClassifyQoSComparesParsedQuantities`

### Requirement: An apply refuses resource values it cannot read
An apply SHALL be refused, with nothing written, when a resource value does not follow the notation or is finer than
1 millicore (CPU) or 1 byte (memory). The error SHALL name the service, the field and the value. The check SHALL apply
to the value after template substitution.

#### Scenario: Unknown suffix
- **WHEN** the user applies a service with `memory 512MB`
- **THEN** the apply fails with an error naming the service, `memory` and `512MB`, and no `desired/service/{svc}/` fact
  is written or changed
- Test: `TestCompileRefusesUnparseableResourceValue`

#### Scenario: Too fine
- **WHEN** the user applies a service with `cpu 0.0005`
- **THEN** the apply fails with an error naming `cpu` and `0.0005`
- Test: `TestCompileRefusesUnparseableResourceValue`

#### Scenario: Vertical scaling bounds
- **WHEN** the user applies a service with `scale { vertical { cpu { min 250m max lots } } }`
- **THEN** the apply fails with an error naming `cpu max` and `lots`
- Test: `TestCompileRefusesUnparseableVerticalBounds`

#### Scenario: Template value
- **WHEN** a configuration declares `cpu {{ .db.cpu }}` and the values file sets `db.cpu: half`
- **THEN** the apply fails with an error naming the service, `cpu` and `half`
- Test: `TestApplyRefusesUnparseableTemplatedResourceValue`

### Requirement: Placement respects requested resources
The scheduler SHALL place an instance only on a node whose remaining CPU (millicores) and memory (MiB) cover the
instance's requests, with memory requests rounded up to whole MiB. It SHALL use the service's effective request when
one exists and the desired request otherwise, and SHALL reconcile again when either changes.

#### Scenario: Request larger than every node
- **GIVEN** alive nodes with at most 3072 MiB of available memory
- **WHEN** a service with `memory 64Gi` is applied
- **THEN** its instance has no `observed/instance/{id}/node`, and the unplaced demand fact has reason
  `insufficient_capacity`
- Test: `TestSchedulerLeavesUnitRequestLargerThanNodesUnplaced`

#### Scenario: Requests fill a node
- **GIVEN** one alive node with 2000 millicores available and a service with `cpu 750m` and `instances 3`
- **WHEN** the scheduler reconciles
- **THEN** two instances are placed on the node and the third is unplaced with reason `insufficient_capacity`
- Test: `TestSchedulerPacksUnitRequestsWithinNodeCapacity`

#### Scenario: Effective request from the vertical autoscaler
- **GIVEN** a service declaring `cpu 250m` whose `effective/service/{svc}/resources/cpu` is `1500m`, and a node with
  1000 millicores available
- **WHEN** the effective value is written and the controller runner reconciles the scheduler
- **THEN** a new instance of the service is not placed on that node
- Test: `TestSchedulerWatchesEffectiveResourceRequest`

#### Scenario: Capacity shortage reaches cluster scaling and the rollout
- **GIVEN** a running service whose new configuration requests more memory than any node has
- **WHEN** the new configuration is applied and the progress deadline passes
- **THEN** the cluster-scale controller counts the instance as demand for a node, `cca describe service` shows a rollout
  reason naming `insufficient_capacity`, and `rollout/rollback_reason` names `insufficient_capacity`
- Test: `TestClusterScaleCountsUnitRequestShortage`
- Check: `deploy-run:resource-capacity` (by hand)

### Requirement: A stored value that cannot be read blocks new work for its service
When a stored request cannot be parsed, the scheduler SHALL NOT place, preempt for or rebalance that service's
instances, and SHALL give its unplaced instances reason `invalid_resource_request`, which cluster scaling ignores.
Placed instances SHALL keep running and count as zero in node capacity, and `cca status` SHALL name the service.

#### Scenario: Legacy bad value
- **GIVEN** `desired/service/web/resources/memory` is `512MB` in the store, `web` has one placed, running instance and
  one unplaced instance, and `web` has a higher priority than the other services
- **WHEN** the scheduler reconciles
- **THEN** the unplaced instance stays unplaced with reason `invalid_resource_request`, no instance of another service
  is preempted, the running instance keeps its node, and `cca status` names `web` and the value
- Test: `TestSchedulerRefusesUnparseableStoredRequest`

#### Scenario: No node asked for
- **GIVEN** the same stored value and no alive node
- **WHEN** the scheduler and the cluster-scale controller reconcile
- **THEN** the reason is `invalid_resource_request` and no capacity request is written
- Test: `TestClusterScaleIgnoresInvalidResourceRequest`

### Requirement: Effective requests follow the vertical autoscaler
The vertical autoscaler SHALL write its CPU recommendation in millicores with the `m` suffix and its memory
recommendation in `Mi`, rounded up. It SHALL rewrite a stored recommendation that has no unit, and SHALL delete its
recommendation when the service no longer declares vertical scaling. The effective request SHALL be deleted when no
recommendation exists, so the desired request applies again.

#### Scenario: CPU recommendation
- **WHEN** the vertical autoscaler recommends 750 millicores for `web`
- **THEN** `intent/autoscaler/service/web/resources/cpu` and `effective/service/web/resources/cpu` are `750m`
- Test: `TestVerticalAutoscalerWritesQuantitiesWithUnits`

#### Scenario: The agent applies the effective request
- **GIVEN** `effective/service/web/resources/cpu` is `750m` and `effective/service/web/resources/memory` is `768Mi`
- **WHEN** the agent starts a container of `web`
- **THEN** its CPU limit is 750 millicores and its memory limit 805306368 bytes
- Test: `TestAgentReadsEffectiveResourceQuantities`

#### Scenario: Legacy recommendation without a unit
- **GIVEN** `intent/autoscaler/service/web/resources/cpu` is `750`, written by an older version, and the autoscaler's
  next recommendation equals the current request
- **WHEN** the autoscaler reconciles
- **THEN** the recommendation is rewritten as `750m`
- Test: `TestVerticalAutoscalerRewritesBareRecommendation`

#### Scenario: Vertical scaling removed
- **GIVEN** `web` had `scale vertical`, effective memory `8Gi` and desired memory `256Mi`
- **WHEN** `web` no longer has any `desired/service/web/scale/vertical/` fact (until M244, an apply that drops the block
  leaves these facts in place, so they are removed by M244's apply or by hand)
- **THEN** the autoscaler's recommendation and the effective memory are deleted, and the scheduler places `web` with
  256 MiB
- Test: `TestRemovingVerticalScalingRestoresDesiredRequest`
