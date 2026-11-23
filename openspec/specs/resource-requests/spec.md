# resource-requests Specification

## Purpose
States how a service's CPU and memory requests and limits are written, which values an apply refuses, and how every
component reads them, so placement checks real node capacity and a capacity shortage is reported.

## Requirements

### Requirement: Resource values use one notation
CPU values in `resources` and `scale vertical` SHALL be millicores with the suffix `m`, or cores (fractions allowed).
Memory values SHALL be a number with a binary suffix (`Ki`…`Ei`), a decimal suffix (`k`…`E`), or no suffix (bytes).
Every component that reads these values SHALL give the same value the same meaning.

#### Scenario: CPU forms
- **WHEN** a service declares `cpu 250m`, `cpu 0.5` or `cpu 2`
- **THEN** its request is 250, 500 or 2000 millicores, respectively, for placement, and the agent uses it as the
  container's CPU limit (a declared `cpu_limit` is not applied until M254)
- Test: `TestParseCPUMillicoresAcceptsKubernetesForms`

#### Scenario: Memory forms
- **WHEN** a service declares `memory 512Mi`, `memory 1.5Gi`, `memory 1G` or `memory 1048576`
- **THEN** its request is 536870912, 1610612736, 1000000000 or 1048576 bytes, respectively
- Test: `TestParseMemoryBytesAcceptsKubernetesForms`

#### Scenario: Leading dot outside a configuration file
- **WHEN** a stored value, or one read by any component, is `.5` (CPU) or `.5Gi` (memory)
- **THEN** it reads as 500 millicores or 536870912 bytes; `5.` is refused. A `.cca` file cannot write a number with a
  leading dot (the DSL lexer refuses `.`), so a configuration writes `0.5`
- Test: `TestParseCPUMillicoresAcceptsKubernetesForms`, `TestParseMemoryBytesAcceptsKubernetesForms`

#### Scenario: Converted Kubernetes manifest
- **WHEN** `k8s-to-cca` converts a container with `cpu: .5` and `memory: .5Gi` requests
- **THEN** the output declares `cpu 0.5` and `memory 0.5Gi`, and `cca lint` accepts it
- Test: `TestConvertWritesLeadingDotQuantitiesWithZero`

#### Scenario: Equal values written differently are one QoS class
- **WHEN** a service declares `cpu 1`, `cpu_limit 1000m`, `memory 1Gi` and `memory_limit 1024Mi`
- **THEN** `derived/service/{svc}/qos_class` is `Guaranteed`
- Test: `TestClassifyQoSComparesParsedQuantities`

#### Scenario: Requests without limits
- **WHEN** a service declares `cpu 250m` and `memory 256Mi` and no limits
- **THEN** `derived/service/{svc}/qos_class` is `Guaranteed`, because the agent uses the request as the container's
  limit (Kubernetes would class this Burstable)
- Test: `TestClassifyQoSRequestsWithoutLimitsIsGuaranteed`

### Requirement: An apply refuses resource values it cannot read
An apply SHALL be refused, with nothing written, when a resource value does not follow the notation. The error SHALL
name the service, the field and the value. The check SHALL apply to the value after template substitution. A value
finer than 1 millicore (CPU) or 1 byte (memory) SHALL be accepted and rounded up to the next millicore or byte. A CPU
value written without `m` that is 64 cores or more SHALL be accepted with a warning that it is read as cores.

#### Scenario: Unknown suffix
- **WHEN** the user applies a service with `memory 512MB`
- **THEN** the apply fails with an error naming the service, `memory` and `512MB`, and no `desired/service/{svc}/` fact
  is written or changed
- Test: `TestCompileRefusesUnparseableResourceValue`

#### Scenario: Finer than the unit rounds up
- **WHEN** the user applies a service with `cpu 0.0005` and `memory 1.1Gi`
- **THEN** the apply succeeds, and every reader reads 1 millicore and 1181116007 bytes
- Test: `TestParseQuantitiesRoundFinerValuesUp`, `TestCompileAcceptsFinerResourceValues`

#### Scenario: Bare CPU of 64 or more
- **WHEN** the user applies a service with `cpu 500` or `scale { vertical { cpu { max 4000 } } }`
- **THEN** the apply succeeds and prints a warning naming the service and the value, saying it is read as cores and
  `500m` was probably meant; `cpu 2` gives no warning
- Test: `TestDeprecationWarningsFlagBareCPUOfSixtyFourOrMore`

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
- **THEN** a new instance of the service is not placed on that node, and once the effective value changes to `500m`
  the scheduler reconciles again and places it
- Test: `TestSchedulerWatchesEffectiveResourceRequest`

#### Scenario: Capacity shortage reaches cluster scaling and the rollout
- **GIVEN** a running service whose new configuration requests more memory than any node has
- **WHEN** the new configuration is applied and the progress deadline passes
- **THEN** the cluster-scale controller counts the instance as demand for a node, `cca describe service` shows a rollout
  reason naming `insufficient_capacity`, and `rollout/rollback_reason` names `insufficient_capacity`
- Test: `TestClusterScaleCountsUnitRequestShortage`
- Check: `deploy-run:resource-capacity` (by hand)

### Requirement: A stored value that cannot be read blocks new work for its service
When a stored request cannot be parsed, the scheduler SHALL NOT place, preempt for, preempt or rebalance that
service's instances, and SHALL give its unplaced instances reason `invalid_resource_request`, which cluster scaling
ignores. Placed instances SHALL keep running and count as zero in node capacity, and `cca status` SHALL name the
service.

#### Scenario: Legacy bad value
- **GIVEN** `desired/service/web/resources/memory` is `512MB` in the store, `web` has one placed, running instance and
  one unplaced instance, and `web` has a higher priority than the other services
- **WHEN** the scheduler reconciles
- **THEN** the unplaced instance stays unplaced with reason `invalid_resource_request`, no instance of another service
  is preempted, the running instance keeps its node, and `cca status` names `web` and the value
- Test: `TestSchedulerRefusesUnparseableStoredRequest`, `TestSchedulerRebalanceLeavesInvalidRequestServiceInPlace`,
  `TestSchedulerWritesInvalidResourceRequestShownByStatus`

#### Scenario: Not a preemption victim
- **GIVEN** a running instance of a low-priority service whose stored request cannot be read, and a pending
  higher-priority instance that fits nowhere
- **WHEN** the scheduler reconciles
- **THEN** that running instance is not preempted
- Test: `TestSchedulerNeverPreemptsInvalidRequestService`

#### Scenario: Still evictable
- **GIVEN** a node over the eviction threshold whose only running instance belongs to a service whose stored
  `memory` is `512MB`
- **WHEN** the eviction controller reconciles
- **THEN** that instance is evicted, counting 0 MiB freed
- Test: `TestEvictionReadsResourceRequestsWithUnits`

### Requirement: Preemption only stops a victim whose resources let the requester fit
The scheduler SHALL preempt for an unplaced instance only a single lower-priority victim on a node where the node's
remaining CPU and memory plus the victim's requests cover the requester's requests. Among such victims it SHALL choose
the lowest priority, then a fixed order by node and instance ID. When no single victim makes room, it SHALL preempt
nothing. The existing victim rules (strictly lower priority, no per-node, stateful or invalid-request services)
SHALL still hold.

#### Scenario: No victim makes room
- **GIVEN** a pending `big` instance (priority 900, `memory 64Gi`) and a running `small` instance (priority 10,
  `memory 128Mi`) on a node with 3072 MiB
- **WHEN** the scheduler reconciles
- **THEN** `small` is not stopped, and `big` stays unplaced with reason `insufficient_capacity`
- Test: `TestSchedulerSkipsPreemptionThatCannotMakeRoom`

#### Scenario: The victim that makes room
- **GIVEN** a full node running `tiny` (priority 5, `memory 128Mi`) and `medium` (priority 10, `memory 1Gi`), and a
  pending instance (priority 900, `memory 768Mi`)
- **WHEN** the scheduler reconciles
- **THEN** `medium` is stopped and `tiny` keeps running, because stopping `tiny` alone would not make room
- Test: `TestSchedulerPreemptsOnlyAVictimThatMakesRoom`

#### Scenario: No node asked for
- **GIVEN** the same stored value and no alive node
- **WHEN** the scheduler and the cluster-scale controller reconcile
- **THEN** the reason is `invalid_resource_request` and no capacity request is written
- Test: `TestClusterScaleIgnoresInvalidResourceRequest`

### Requirement: Effective requests follow the vertical autoscaler
The vertical autoscaler SHALL write its CPU recommendation in millicores with the `m` suffix and its memory
recommendation in whole `Mi` (minimum bound rounded up, maximum bound rounded down, recommendation rounded up, and
compared with the current value in MiB, so an unchanged recommendation is not written again), and SHALL make no
recommendation for a resource whose bounds cannot be read.

#### Scenario: CPU recommendation
- **WHEN** the vertical autoscaler recommends 750 millicores for `web`
- **THEN** `intent/autoscaler/service/web/resources/cpu` and `effective/service/web/resources/cpu` are `750m`
- Test: `TestVerticalAutoscalerWritesQuantitiesWithUnits`

#### Scenario: The agent applies the effective request
- **GIVEN** `effective/service/web/resources/cpu` is `750m` and `effective/service/web/resources/memory` is `768Mi`
- **WHEN** the agent starts a container of `web`
- **THEN** its CPU limit is 750 millicores and its memory limit 805306368 bytes
- Test: `TestAgentReadsEffectiveResourceQuantities`

#### Scenario: Unreadable bound
- **WHEN** a stored vertical CPU bound cannot be parsed
- **THEN** the autoscaler makes no CPU recommendation for that service
- Test: `TestVerticalAutoscalerSkipsResourceWithUnreadableBound`

#### Scenario: Memory bounds that are not whole MiB
- **GIVEN** `memory { min 100M max 1G }`
- **WHEN** usage is low and the autoscaler reconciles twice
- **THEN** it writes `96Mi` once and nothing on the second reconcile; at high usage it never writes more than `953Mi`
- Test: `TestVerticalMemoryRecommendationSettlesOnWholeMiB`

### Requirement: Legacy recommendations are migrated at server start and on becoming leader
The server SHALL rewrite, at start and each time it becomes leader, before any controller runs, every autoscaler
recommendation stored without a unit, and every effective request stored without a unit for a resource that has a
recommendation, clamped to the service's vertical bounds. It SHALL never rewrite `desired/`.

#### Scenario: Legacy recommendation without a unit
- **GIVEN** `intent/autoscaler/service/web/resources/cpu` and `effective/service/web/resources/cpu` are `750`, written
  by an older version, and the CPU metric is at the target
- **WHEN** the server starts and the controllers reconcile
- **THEN** both are `750m` before any controller runs, the recommendation does not jump to the policy max, and `web` is
  not `insufficient_capacity`; running the migration again changes nothing, and `desired/` is not touched
- Test: `TestMigrateUnitlessResourceRecommendations`, `TestLegacyBareRecommendationAfterMigrationKeepsItsSize`

#### Scenario: Legacy value outside the bounds
- **GIVEN** `cpu { min 250m max 4 }` and a legacy recommendation `4` (4 millicores)
- **WHEN** the migration runs
- **THEN** the recommendation is `250m`
- Test: `TestMigrateUnitlessResourceRecommendations`

#### Scenario: Leader change in a rolling upgrade
- **GIVEN** a new-version server that started and migrated, after which an old-version leader wrote
  `intent/autoscaler/service/web/resources/cpu` = `750`
- **WHEN** the new-version server becomes leader
- **THEN** the value is `750m` before its controllers run
- Test: `TestHARunnerMigratesOnLeadershipAcquisition`

#### Scenario: Effective value without a recommendation
- **GIVEN** `effective/service/web/resources/cpu` is `2`, copied from the desired value by an older version, and no
  autoscaler recommendation for CPU
- **WHEN** the migration runs
- **THEN** the value stays `2` (2 cores, as desired now reads), and the intent controller later deletes it
- Test: `TestMigrateUnitlessResourceRecommendations`

### Requirement: Removed vertical scaling restores the desired request
Per resource, the autoscaler SHALL delete its recommendation when the service no longer declares vertical scaling for
that resource, and the effective request SHALL be deleted when that resource has no recommendation, so the desired
request applies again. Services with hierarchical names (`team/web`) SHALL behave the same.

#### Scenario: Vertical scaling removed
- **GIVEN** `web` had `scale vertical`, effective memory `8Gi` and desired memory `256Mi`
- **WHEN** `web` no longer has any `desired/service/web/scale/vertical/` fact (until M244, an apply that drops the block
  leaves these facts in place, so they are removed by M244's apply or by hand)
- **THEN** the autoscaler's recommendation and the effective memory are deleted, and the scheduler places `web` with
  256 MiB
- Test: `TestRemovingVerticalScalingRestoresDesiredRequest`

#### Scenario: One resource removed
- **GIVEN** `web` (or `team/web`) has cpu and memory recommendations and effective values
- **WHEN** only the `cpu {}` vertical block is gone
- **THEN** the CPU recommendation and the effective CPU are deleted, and memory's stay
- Test: `TestRemovingOnlyCPUVerticalScalingDeletesOnlyCPU`
