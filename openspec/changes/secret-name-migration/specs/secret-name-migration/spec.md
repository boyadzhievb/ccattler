# Spec Delta

## Purpose

Legacy secrets (`secrets/{name}` outside `tenant/`, `cluster/` and gate keys) move to tenant or cluster ownership by re-encryption, without restarting instances.

Test names below are the tests this change adds; none exist yet.

## ADDED Requirements

### Requirement: Plan
`cca secret migrate-names --plan` SHALL list every legacy secret with its revision, consumer services and their tenants, and unreachable pre-M176 `route/` keys, and SHALL write nothing.

#### Scenario: Plan requires cluster metadata read
- **WHEN** a user with `secret.metadata.read` only at `team/payments` runs `--plan`
- **THEN** the API answers 403
- **Test:** `api/TestMigrateNamesPlanRequiresClusterMetadataRead`

### Requirement: Apply writes intent only
`--apply` SHALL validate the mapping and write `intent/secret_names/…` facts, refusing while any legacy secret is unmapped or a precondition fails.

#### Scenario: Preconditions
- **WHEN** `--apply` runs with `keyring/legacy` = `migrating`, a KEK rotation intent, or a `keyring/name_conflict/` record
- **THEN** it answers 409 and nothing changes
- **Test:** `api/TestMigrateNamesRequiresPreconditions`

#### Scenario: Unmapped secret
- **WHEN** a mapping omits one legacy secret
- **THEN** apply fails and no intent is written
- **Test:** `api/TestMigrateNamesRefusesUnmappedSecret`

#### Scenario: Invalid mapping
- **WHEN** a mapping has a duplicate target, a reserved name, `tenant//x`, an existing target key, or a stale plan revision
- **THEN** apply refuses it before writing
- **Test:** `api/TestMigrateNamesValidatesMapping`

#### Scenario: Foreign target tenant
- **WHEN** a secret used only by `payments` services is mapped to `tenant/frontend/…` without `--allow-foreign-tenant`
- **THEN** apply refuses that mapping
- **Test:** `api/TestMigrateNamesRefusesForeignTargetTenant`

#### Scenario: Intent not writable through apply
- **WHEN** a configuration compiles to a fact under `intent/secret_names/`
- **THEN** `RequiredCapabilityForFactWrite` requires `cluster.admin`
- **Test:** `security/TestSecretNameIntentRequiresClusterAdmin`

### Requirement: Controller moves secrets
The `secret-name-migration` controller SHALL move each mapped secret in idempotent phases, create shares for consumers in other tenants, and leave running instances untouched.

#### Scenario: Consumers in two tenants
- **WHEN** a legacy secret used by `payments` and `frontend` services is mapped to `tenant/payments/database.password`
- **THEN** the canonical envelope decrypts to the same value under the new AAD, both services hold canonical grants with unchanged `reference` and `mount_path`, a share to `frontend` exists, and the legacy key and old grants are gone
- **Test:** `controllers/TestSecretNameMigrationReencryptsAndCreatesShares`

#### Scenario: No restarts and no secret waits
- **WHEN** migration runs on a cluster whose instances use legacy secrets
- **THEN** no instance enters `waiting-for-secrets` and instance IDs and start times are unchanged
- **Test:** `integration/TestSecretNameMigrationKeepsInstancesRunning`

#### Scenario: Resume after crash
- **WHEN** the controller stops after phase 1 for some secrets and after 2a for others, and a new leader runs
- **THEN** migration completes without duplicate or lost writes
- **Test:** `controllers/TestSecretNameMigrationResumes`

#### Scenario: Concurrent set on a legacy key
- **WHEN** `secret set` changes a legacy secret between phase 1 and phase 2b
- **THEN** 2b fails its guard and the canonical envelope ends with the new value
- **Test:** `controllers/TestSecretNameMigrationDoesNotLoseConcurrentSet`

#### Scenario: Many consumers
- **WHEN** a legacy secret is granted to 200 services
- **THEN** migration completes in batches below the transaction limit
- **Test:** `controllers/TestSecretNameMigrationBatchesLargeGrantSets`

#### Scenario: Dual grants deliver once
- **WHEN** a service holds the legacy and the canonical grant for one reference
- **THEN** the agent mounts one file, from the canonical secret
- **Test:** `agent/TestDualGrantsDeduplicatedByReference`

#### Scenario: Tampered intent
- **WHEN** an intent's target is changed after apply so its hash no longer matches
- **THEN** the controller does not act on it and reports an observed error
- **Test:** `controllers/TestSecretNameMigrationIgnoresTamperedIntent`
