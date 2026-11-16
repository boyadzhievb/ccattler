# Spec Delta

## Purpose

Defines how a service's instance count is set and bounded: what the scale API accepts and answers, the warning threshold and the approval limit on the scale and apply paths, and the rule that only declared services ever get instances.

## ADDED Requirements

### Requirement: Scale only declared services
`POST /api/scale` SHALL answer 404 for a service name that has no `desired/service/{name}` declaration, and SHALL NOT write any fact for it.

#### Scenario: Scale a name nobody declared
- **WHEN** a principal with `scaling.write` sends `{"service":"nosuch","instances":2}` and no `desired/service/nosuch` exists
- **THEN** the server answers 404 naming the service, and no `desired/service/nosuch/instances` or `intent/user/service/nosuch/instances` fact exists afterwards
- Test: `api/TestScaleRefusesUndeclaredService`

### Requirement: A scale is stored completely or not at all
A successful scale SHALL write `desired/service/{name}/instances` and `intent/user/service/{name}/instances` together in one store transaction. The server SHALL answer success only after that transaction commits.

#### Scenario: Store write fails
- **WHEN** the store refuses the scale transaction
- **THEN** the server answers 500 with an error, never `ok: true`, and neither count fact changed
- Test: `api/TestScaleReportsStoreError`

#### Scenario: Service removed during the request
- **WHEN** the service's declaration is deleted or rewritten between the server's check and its write
- **THEN** the server answers 409 and neither count fact changed
- Test: `api/TestScaleConflictsWhenServiceChanges`

### Requirement: Warning threshold
A requested count of 500 or more and at most 600 SHALL be accepted, and the answer SHALL carry a warning that names the service, the count and the threshold. `cca scale` and `cca apply` SHALL print that warning.

#### Scenario: Scale to the warning threshold
- **WHEN** a principal with `scaling.write` scales a declared service to 500
- **THEN** the server answers 200, stores 500, and the body has a `warning` naming the threshold 500
- Test: `api/TestScaleWarnsFromThreshold`

#### Scenario: Apply a configuration at the threshold
- **WHEN** a configuration declares `instances 550` and is applied
- **THEN** the apply succeeds and its answer carries a warning for that service; `cca apply` prints it
- Test: `api/TestApplyWarnsFromThreshold`

### Requirement: Approval above the limit
A requested count above 600 SHALL be refused with 400, unless the request carries an approval equal to that exact count. An approved count SHALL be accepted only for a principal holding `cluster.admin`; any other principal SHALL get 403. There is no count above which an approved request is refused.

#### Scenario: Above the limit without approval
- **WHEN** a principal scales a declared service to 601 without `approved_instances`
- **THEN** the server answers 400 naming the limit 600 and how to approve, and nothing is written
- Test: `api/TestScaleAboveLimitNeedsApproval`

#### Scenario: Approval with a different count
- **WHEN** a `cluster.admin` principal sends `instances 100000` with `approved_instances 10000`
- **THEN** the server answers 400 and nothing is written
- Test: `api/TestScaleApprovalMustRepeatCount`

#### Scenario: Approval by a principal without cluster.admin
- **WHEN** a principal holding `scaling.write` but not `cluster.admin` sends `instances 700` with `approved_instances 700`
- **THEN** the server answers 403 naming `cluster.admin`, and nothing is written
- Test: `api/TestScaleApprovalNeedsClusterAdmin`

#### Scenario: Approved large count
- **WHEN** a `cluster.admin` principal sends `instances 100000` with `approved_instances 100000`
- **THEN** the server answers 200 with the warning, stores 100000, and the audit log records the principal, the service and the count
- Test: `api/TestScaleApprovedAboveLimit`

#### Scenario: Apply above the limit
- **WHEN** a configuration declares `instances 700`, or a `scale { min 700 }` or `scale { max 700 }`, and is applied without approval
- **THEN** the apply is refused with 400 naming the service and its count, and no fact of the configuration is written
- Test: `api/TestApplyAboveLimitNeedsApproval`

#### Scenario: Apply with approval
- **WHEN** a `cluster.admin` principal applies a configuration declaring `instances 700` with `--approve-above-limit 700`
- **THEN** the apply succeeds; another service in the same configuration declaring 800 is still refused, naming that service
- Test: `api/TestApplyApprovalCoversOnlyThatCount`

### Requirement: The CLI asks for the count again
`cca scale` SHALL, for a count above the limit on an interactive terminal, ask the user to type the count again and send it as the approval; without a terminal it SHALL send the approval only from `--approve-above-limit <count>`. `cca apply` SHALL take the same flag.

#### Scenario: Scripted scale above the limit
- **WHEN** `cca scale web 700` runs without a terminal and without `--approve-above-limit`
- **THEN** it exits non-zero and prints the server's refusal with the flag to use
- Test: `cmd/cca/TestScaleCommandSendsApproval`

#### Scenario: Retyped count differs
- **WHEN** `cca scale web 100000` runs on a terminal and the user types `10000`
- **THEN** nothing is sent and the command exits non-zero
- Test: `cmd/cca/TestScaleCommandSendsApproval`

### Requirement: Only declared services keep instances
The system SHALL run instances only for services that have a `desired/service/{name}` declaration. When a count exists for a name without a declaration, the system SHALL set that name's effective instance count to 0, whatever its user or autoscaler intent says.

#### Scenario: Orphan instances from an earlier scale
- **GIVEN** `intent/user/service/nosuch/instances = 2` and two running instances of `nosuch`, with no `desired/service/nosuch`
- **WHEN** the controllers reconcile
- **THEN** `effective/service/nosuch/instances` is 0 and both instances are stopped
- Test: `controllers/TestIntentResolverStopsUndeclaredServices`, `integration/TestOrphanScaleInstancesStopped`

#### Scenario: Declared service is untouched
- **GIVEN** a declared service `web` with a user intent of 3
- **WHEN** the controllers reconcile
- **THEN** `web` keeps an effective count of 3
- Test: `controllers/TestIntentResolverStopsUndeclaredServices`
