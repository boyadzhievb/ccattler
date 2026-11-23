# Spec Delta

## Purpose
States when a component may write a fact, so that a value that has not changed never produces a store write, a new
revision or a watch event, and the test store shows the same writes etcd would.

## ADDED Requirements

### Requirement: The agent writes an instance's state only when it changes
The agent SHALL NOT write `observed/instance/{id}/state` when the stored value already equals the state it observed.
It SHALL still write a changed state, and SHALL still never write over `stopped`.

#### Scenario: Running instance, nothing changes
- **GIVEN** an instance placed on the agent's node, running, with `observed/instance/{id}/state = running` stored
- **WHEN** the agent runs five reconcile cycles with nothing changing
- **THEN** no write to `observed/instance/{id}/state` happens and no watch event is delivered for it
- Test: `TestAgentDoesNotRewriteUnchangedInstanceState`

#### Scenario: State changes
- **GIVEN** the same instance with `observed/instance/{id}/state = starting` stored
- **WHEN** the agent observes it running
- **THEN** the state is written as `running` once
- Test: `TestAgentDoesNotRewriteUnchangedInstanceState` (sub-case `changed state is written`)

#### Scenario: Running instances on the deploy cluster
- **GIVEN** the deploy cluster after a push run, with its default workloads running
- **WHEN** `cca watch observed/instance/` runs for 20 seconds with nothing being applied
- **THEN** no `state` key is put more than once
- Test: deploy-cluster check by hand, `deploy-run:unchanged-state-writes` (recorded in tasks.md)

### Requirement: The runner does not commit unchanged puts
The controller runner SHALL drop a proposed put whose value is byte-equal to the value of the same key in the facts
it scanned for that reconcile, before counting changes against its transaction budget. A put of a key that was not
in the scanned facts, a changed value and every delete SHALL be committed as before. When every proposed change is
dropped, no transaction SHALL be sent.

#### Scenario: Controller proposes an unchanged value
- **GIVEN** a controller that watches a prefix containing `derived/x/a = 1` and proposes put `derived/x/a = 1` and put
  `derived/x/b = 2` (absent)
- **WHEN** the runner commits the reconcile
- **THEN** only `derived/x/b` is written, and no watch event is delivered for `derived/x/a`
- Test: `TestRunnerDropsUnchangedPuts`

#### Scenario: Only unchanged puts
- **GIVEN** the same controller proposing only put `derived/x/a = 1`
- **WHEN** the runner commits the reconcile
- **THEN** no transaction is sent and the controller is not woken again by its own proposal
- Test: `TestRunnerDropsUnchangedPuts` (sub-case `nothing left`)

### Requirement: The memory store treats unchanged puts in transactions as etcd does
A put inside `MemoryStore.Transaction` SHALL get a new revision and deliver a watch event even when the value is
unchanged, as etcd does. `MemoryStore.Put` SHALL keep skipping an unchanged value, as `EtcdStore.Put` does.

#### Scenario: Unchanged put in a transaction
- **GIVEN** a memory store with `k = v` at revision R and a watch on `k`
- **WHEN** a transaction puts `k = v`
- **THEN** `k`'s revision is greater than R and the watch receives a put event for `k`
- Test: `TestMemoryStoreTransactionUnchangedPutBumpsRevision`

#### Scenario: Unchanged plain put
- **GIVEN** the same store
- **WHEN** `Put(k, v)` is called
- **THEN** `k`'s revision stays R and no event is delivered
- Test: `TestMemoryStoreTransactionUnchangedPutBumpsRevision` (sub-case `plain put skips`)
