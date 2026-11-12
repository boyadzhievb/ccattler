# Spec Delta

## Purpose

Secrets belong to a tenant or to the cluster. A service can consume a secret only if the secret is in its own tenant or the owner shared it with the service's tenant. Every check uses the scope of the owning tenant and fails closed. Existing (legacy) secrets keep working for the grants that already name them; moving them is M209.

Test names below are the tests this change adds; none exist yet.

## ADDED Requirements

### Requirement: Canonical secret names
The system SHALL store a user secret under `secrets/tenant/{tenant}/{name}` or `secrets/cluster/{name}` through `/api/secret/v2/…`, and SHALL use that store key as the encryption AAD.

#### Scenario: Tenant secret
- **WHEN** a user with `secret.write` at `team/payments` runs `cca secret set database.password --tenant payments`
- **THEN** the envelope exists at `secrets/tenant/payments/database.password` and no `secrets/database.password` key is written
- **Test:** `api/TestSecretSetWritesTenantCanonicalKey`

#### Scenario: No scope flag means the default tenant
- **WHEN** a user runs `cca secret set legacy-app.password` with neither `--tenant` nor `--cluster`
- **THEN** the envelope exists at `secrets/tenant/default/legacy-app.password`
- **Test:** `api/TestSecretSetWithoutScopeUsesDefaultTenant`

#### Scenario: Both scope flags
- **WHEN** a user passes `--tenant payments` together with `--cluster`
- **THEN** the command fails before any request is sent
- **Test:** `cmd/cca/TestSecretCommandRejectsTenantWithCluster`

#### Scenario: Unknown tenant
- **WHEN** a user runs `cca secret set x --tenant paymnets` and no tenant `paymnets` is declared
- **THEN** the API refuses it and nothing is written
- **Test:** `api/TestSecretSetRequiresDeclaredTenant`

#### Scenario: Value from stdin or file only
- **WHEN** `cca secret set x --tenant payments` runs with `--from-file v.txt`, and again with the value on stdin
- **THEN** both store the content; a value passed as a positional argument is rejected
- **Test:** `cmd/cca/TestSecretSetReadsValueFromStdinOrFile`

### Requirement: Name validation
The system SHALL validate every segment of service, connection and route names with `ValidateResourceName` and reject segments reserved for store attributes; SHALL reject tenant names `default` (declared), `cluster`, `tenant`, `route`; and SHALL reject secret names whose first segment is `tenant`, `cluster` or `route`.

#### Scenario: Forged grant key through a service name
- **WHEN** a configuration declares a service named `myapp/secret/cluster/registry-credentials`
- **THEN** apply fails with a reserved-segment error and no fact under `desired/service/myapp/secret/` is written
- **Test:** `lang/TestCompileRejectsReservedServiceNameSegments`

#### Scenario: Reserved attribute list is complete
- **WHEN** a key helper under `desired/service/{svc}/`, `effective/service/{svc}/`, `intent/*/service/{svc}/` or `desired/route/{gate}/` uses a segment missing from the reserved list
- **THEN** the test fails naming the segment
- **Test:** `types/TestReservedNameSegmentsCoverKeyHelpers`

#### Scenario: Route name with a slash
- **WHEN** a configuration declares route `a/service`
- **THEN** apply fails and gate `a`'s target is unchanged
- **Test:** `lang/TestCompileRejectsMultiSegmentRouteNames`

#### Scenario: Existing names that fail the new rules
- **WHEN** the store holds a service named `Billing_API` and `cca lint` runs on its configuration
- **THEN** lint reports it with the rule it breaks, and the running service is untouched
- **Test:** `lang/TestLintReportsNamesFailingNewValidation`

#### Scenario: Canonical-looking secret name
- **WHEN** a user sets `tenant/x/y` with `--tenant frontend`
- **THEN** the set fails with a reserved-name error
- **Test:** `api/TestSecretNameRejectsReservedFirstSegment`

#### Scenario: Declaring a reserved tenant
- **WHEN** a configuration declares `tenant default`, `owner cluster`, or a service named `tenant/api`
- **THEN** apply fails with a reserved-tenant error
- **Test:** `lang/TestCompileRejectsReservedTenantNames`

#### Scenario: Gate key path stays unreachable
- **WHEN** a user requests `route/web/tls/key` on `/api/secret`, `/api/secret/v2/value`, the agent secret API, or as a service or gate `key secret` reference
- **THEN** every path refuses it and no gate key is returned
- **Test:** `api/TestRouteSecretNamesRefusedOnEveryPath`

### Requirement: Tenant resolution
The system SHALL resolve the tenant of a service or connection as explicit `owner`, else the first segment of a hierarchical name, else `default`, and the tenant of a gate as its target service's tenant. Only an absent owner SHALL map to `default`; a store error SHALL fail the request. A hierarchical name's first segment SHALL equal `owner` unless the applier holds `cluster.admin`.

#### Scenario: Unowned service is refused another tenant's secret (acceptance A)
- **WHEN** a flat-named service without `owner` references `secret database.password` and that secret exists only in tenant `payments`
- **THEN** apply fails with "secret database.password is not available to tenant default"
- **Test:** `integration/TestUnownedServiceResolvesToDefaultTenantSecret`

#### Scenario: Owner read error
- **WHEN** reading a service's owner fails with a store error, during apply and during delivery
- **THEN** both refuse; neither falls back to `default`
- **Test:** `security/TestTenantLookupErrorFailsClosed`

#### Scenario: Namespace squatting
- **WHEN** a user with scopes only at `team/frontend` applies a service `payments/evil` with `owner frontend`
- **THEN** apply fails
- **Test:** `integration/TestHierarchicalNameMustMatchOwner`

### Requirement: Scoped authorization
The system SHALL authorize secret set, delete and share with `secret.write`, get with `secret.read`, list with `secret.metadata.read`, and apply with `workload.create` at the owning tenant's scope plus `secret.use` at the consumer tenant per reference. Conditional policies SHALL NOT grant a scope narrower than `cluster` unless they name a containing scope.

#### Scenario: Writing into another tenant
- **WHEN** a user holding `secret.write` only at `team/frontend` sets a secret with `--tenant payments`
- **THEN** the API answers 403 and nothing is written
- **Test:** `api/TestSecretWriteRequiresOwningTenantScope`

#### Scenario: Reading another tenant's value
- **WHEN** a user holding `secret.read` only at `team/frontend` runs `cca secret get database.password --tenant payments`
- **THEN** the API answers 403 with no value
- **Test:** `api/TestSecretGetRequiresOwningTenantScope`

#### Scenario: Sharing without owner scope
- **WHEN** a user holding `secret.write` only at `team/frontend` shares `payments`' `database.password` with `frontend`
- **THEN** the API answers 403 and no share fact is written
- **Test:** `api/TestSecretShareRequiresOwnerScope`

#### Scenario: Claiming a tenant through owner
- **WHEN** a user holding `workload.create` and `secret.use` only at `team/frontend` applies `service x { owner payments  secret database.password }`, through the direct path and through the policy gate
- **THEN** both paths fail with an authorization error and no fact is written
- **Test:** `integration/TestApplyOwnerClaimRequiresTenantScope`

#### Scenario: Changing an owner
- **WHEN** a user without `cluster.admin` re-applies an existing service or connection with a different `owner`
- **THEN** apply fails and existing facts are unchanged
- **Test:** `integration/TestApplyOwnerChangeRequiresClusterAdmin`

#### Scenario: Takeover by omitting owner
- **WHEN** service `api` is stored with `owner payments` and a user with scopes only at `team/default` applies `service api { image evil }` without `owner`
- **THEN** apply fails and the stored image is unchanged
- **Test:** `integration/TestApplyOmittedOwnerChecksStoredTenant`

#### Scenario: Gate retarget
- **WHEN** gate `web-public` targets a `payments` service and a user with scopes only at `team/frontend` re-applies it targeting a `frontend` service
- **THEN** apply fails and the stored gate is unchanged
- **Test:** `integration/TestGateRetargetNeedsBothTenants`

#### Scenario: Gate without a known target
- **WHEN** a gate's target service is neither in the configuration nor in the store
- **THEN** apply fails instead of resolving `default`
- **Test:** `api/TestGateWithUnknownTargetRejected`

#### Scenario: Ownerless existing connection
- **WHEN** connection `db` exists without an owner and a user without `cluster.admin` re-applies it with a different host
- **THEN** apply fails
- **Test:** `integration/TestOwnerlessConnectionChangeNeedsClusterAdmin`

#### Scenario: Facts without a tenant stay cluster-scoped
- **WHEN** a user with `workload.create` only at `team/payments` applies a configuration containing a volume, a tenant quota or a job
- **THEN** apply fails for those facts; and the stage's classification test fails for any compiled fact family it does not classify
- **Test:** `api/TestResolutionStageClassifiesEveryFactFamily`

#### Scenario: Grant facts only from declared references
- **WHEN** compiled facts contain a key under `desired/service/{svc}/secret/` that no declared reference produced, or a key under `desired/secret_share/`
- **THEN** apply rejects it
- **Test:** `api/TestApplyRejectsUndeclaredGrantAndShareFacts`

#### Scenario: Conditional policies grant at cluster scope only
- **WHEN** a principal's only `secret.read` policy is conditional and its conditions pass, and it requests a value at `team/payments` and then a cluster-scope operation
- **THEN** the first is denied and the second is allowed
- **Test:** `security/TestConditionalPolicyGrantsOnlyAtClusterScope`

### Requirement: Listing
`cca secret list` SHALL list, without values, every secret in the scopes where the caller holds `secret.metadata.read`; `--tenant` and `--cluster` SHALL narrow it. Status and describe SHALL apply the same filter.

#### Scenario: Unflagged list spans authorized tenants only
- **WHEN** a user with `secret.metadata.read` at `team/payments` and `team/default` and only `secret.use` at `team/frontend` runs `cca secret list`
- **THEN** the output contains the `payments` and `default` secrets with name, tenant, KEK version and shares, and no `frontend` tenant or secret name
- **Test:** `api/TestSecretListShowsOnlyReadableScopes`

#### Scenario: Share recipient cannot enumerate
- **WHEN** `payments` shares `database.password` with `frontend` and a user with `secret.metadata.read` only at `team/frontend` lists secrets
- **THEN** `tenant/payments/database.password` does not appear
- **Test:** `api/TestSecretListExcludesSharedSecretsOfOtherTenants`

#### Scenario: Built-in cluster reader
- **WHEN** a principal with the built-in `api-reader` role lists secrets
- **THEN** it sees every tenant, cluster and legacy secret's metadata
- **Test:** `api/TestSecretListClusterReaderSeesAllScopes`

#### Scenario: Status and describe
- **WHEN** a user with `secret.metadata.read` only at `team/frontend` runs `cca status` and `cca describe service payments/api`
- **THEN** no `payments` secret or grant name appears, and `frontend` services show their granted secrets
- **Test:** `api/TestStatusAndDescribeFilterSecretsByTenant`

#### Scenario: State and watch visibility of shares and connections
- **WHEN** a reader scoped to `team/marketing` reads `/api/state` and watches `desired/`, while `payments` shares a secret with `frontend` and owns connection `db`
- **THEN** neither the share fact nor connection `db` is returned or streamed
- **Test:** `api/TestStateHidesForeignSharesAndConnections`

### Requirement: Mixed-version guard
The system SHALL serve scoped secrets only on `/api/secret/v2/…`, and SHALL refuse set and share (default tenant included) with 409 while any live keyring replica report or any enrolled, non-disabled node lacks `secret_name_format: tenant-v1`, or `keyring/legacy` is not `none` or `retired`. Delete, unshare and delete-conflict SHALL NOT be blocked.

#### Scenario: Old replica or agent blocks writes
- **WHEN** one replica report, and in a second run one enrolled node without any report, lacks `secret_name_format` and a default-tenant `secret set` runs
- **THEN** it answers 409 naming that replica or node and nothing is written
- **Test:** `api/TestScopedWritesRefusedWhileReplicaOrNodeLacksNameFormat`

#### Scenario: Revocation is never blocked
- **WHEN** the guard fails and an owner runs `unshare` and `delete`
- **THEN** both succeed
- **Test:** `api/TestGuardDoesNotBlockUnshareOrDelete`

#### Scenario: Key migration not finished
- **WHEN** `keyring/legacy` is `migrating` and a tenant secret is set
- **THEN** it answers 409
- **Test:** `api/TestScopedWritesRequireFinishedKeyMigration`

#### Scenario: No reporter
- **WHEN** the server runs with a legacy-only codec (no keyring replica reporter)
- **THEN** v2 writes answer 409 instead of panicking or writing
- **Test:** `api/TestScopedWritesRefusedWithoutKeyringReporter`

#### Scenario: Canonical-looking legacy key
- **WHEN** a key `secrets/tenant/payments/db` exists before the upgrade
- **THEN** the first replica past the guard records it as a conflict together with `keyring/name_format`; v2 get, set and delivery of it refuse; after `cca secret delete-conflict secrets/tenant/payments/db` by a cluster admin, `set db --tenant payments` succeeds
- **Test:** `api/TestCanonicalLookingLegacyKeyIsConflictUntilDeleted`

#### Scenario: Before the name format marker
- **WHEN** `keyring/name_format` does not exist yet
- **THEN** v2 reads and writes, apply of a qualified reference, and delivery of a canonical key refuse
- **Test:** `api/TestCanonicalPathsWaitForNameFormatMarker`

#### Scenario: delete-conflict needs a record
- **WHEN** a cluster admin runs `delete-conflict` on a canonical key that has no conflict record
- **THEN** it is refused and the key is unchanged
- **Test:** `api/TestDeleteConflictRequiresRecord`

### Requirement: Reference resolution at apply
A resolution stage shared by both apply paths SHALL resolve each secret reference in its owner's tenant (service, connection, or a gate's target service), reject unresolvable and unshared references with one identical error, and write grant facts keyed by canonical name with value `{source, reference, mount_path}`.

#### Scenario: Own-tenant reference with explicit mount
- **WHEN** a service owned by `payments` references `secret database.password "/etc/db/pw"` and that tenant secret exists
- **THEN** the grant `desired/service/{svc}/secret/tenant/payments/database.password` has value `{"reference":"database.password","mount_path":"/etc/db/pw","roles":["explicit"]}` and the instance sees the file at `/etc/db/pw`
- **Test:** `integration/TestResolvedGrantKeepsExplicitMountPath`

#### Scenario: One secret in several roles
- **WHEN** a service declares `secret db.pw`, uses connection `db` whose secret is `db.pw` in the same tenant, and names `db.pw` as its pull secret
- **THEN** one grant has roles `explicit`, `connection:db` and `image_pull`, and the per-service connection and pull facts name the canonical secret
- **Test:** `api/TestResolvedGrantMergesRoles`

#### Scenario: Pull secret resolution
- **WHEN** a service names `image_pull_secret "tenant/platform/registry"` with a share from `platform`, and an explicit `secret` grant for it
- **THEN** the pull uses `platform`'s value; without the explicit grant delivery refuses as today
- **Test:** `integration/TestQualifiedPullSecretResolves`

#### Scenario: Default mount path unchanged
- **WHEN** a service references unqualified `secret database.password`
- **THEN** the instance sees `/run/cca-secrets/database.password`
- **Test:** `agent/TestSecretMountUsesWrittenReference`

#### Scenario: Qualified reference
- **WHEN** a `frontend` service references `secret tenant/payments/database.password` and a share exists
- **THEN** parsing accepts it and the grant names `tenant/payments/database.password`
- **Test:** `lang/TestParseQualifiedSecretReference`

#### Scenario: Connection secret belongs to the connection's tenant
- **WHEN** connection `db { owner payments  secret database.password }` is used by a `frontend` service and `payments` has not shared the secret with `frontend`
- **THEN** apply fails; after the share, the `frontend` instance receives `payments`' value
- **Test:** `integration/TestConnectionSecretResolvesInConnectionTenant`

#### Scenario: Redefining another tenant's connection
- **WHEN** a user with scopes only at `team/frontend` re-applies connection `db` owned by `payments` with a different host
- **THEN** apply fails and the connection is unchanged
- **Test:** `integration/TestConnectionChangeRequiresOwnerScope`

#### Scenario: Cross-tenant reference without a share
- **WHEN** a `frontend` service references `secret tenant/payments/database.password` without a share, through the direct path and the policy gate
- **THEN** both fail with "not available to tenant frontend", the same text as for a missing secret
- **Test:** `integration/TestApplyRejectsUnsharedCrossTenantSecret`

#### Scenario: Cluster secret needs a share
- **WHEN** a `frontend` service references `secret cluster/registry-credentials` without a cluster share to `frontend`, and again after a cluster admin shares it
- **THEN** the first apply fails; the second succeeds for a user with `secret.use` at `team/frontend` and delivery checks the share
- **Test:** `integration/TestClusterSecretRequiresShareToTenant`

#### Scenario: Gate key from another tenant
- **WHEN** a gate targeting a `frontend` service declares `tls { key secret "tenant/payments/web-key" }` without a share
- **THEN** apply fails and gate key delivery refuses the key
- **Test:** `integration/TestGateKeySecretResolvesInTargetServiceTenant`

#### Scenario: Mount collision
- **WHEN** a service references `tenant/payments/db.pw` and `cluster/db.pw` without explicit mounts, or a secret mount path equals a connection file path
- **THEN** apply fails naming the collision, and an agent given such grants refuses the instance
- **Test:** `api/TestResolveRejectsMountPathCollision`

### Requirement: Legacy secrets keep working
The system SHALL keep an existing grant on a legacy secret pinned to it while that secret exists, SHALL refuse new grants naming a legacy secret, SHALL keep legacy values updatable, and SHALL NOT restart instances because of the upgrade.

#### Scenario: Re-apply before migration
- **WHEN** a new service references a legacy secret, an existing service holding a grant for it is re-applied with a new image, and a `default` secret of the same name is created afterwards
- **THEN** the new service is refused with the standard "not available" text; the existing service applies, keeps receiving the legacy value, and is not switched to the `default` secret
- **Test:** `integration/TestLegacyGrantStaysPinned`

#### Scenario: Legacy value rotation
- **WHEN** a cluster admin runs `cca secret set db.pw --legacy` for an existing legacy secret, and `--legacy` for a name that does not exist
- **THEN** the first updates `secrets/db.pw`; the second is refused
- **Test:** `api/TestLegacyEndpointUpdatesExistingKeysOnly`

#### Scenario: Agent upgrade alone restarts nothing
- **WHEN** the config hash of a service with legacy grants is computed by the new agent
- **THEN** it equals the hash the v1.77.0-beta agent stored (golden value)
- **Test:** `agent/TestConfigHashUnchangedForLegacyGrants`

#### Scenario: Upgrade under traffic
- **WHEN** replicas and agents are upgraded from v1.77.0-beta on a running cluster and services are re-applied unchanged
- **THEN** no running instance restarts (instance IDs and start times unchanged)
- **Test:** `integration/TestUpgradeKeepsInstancesRunning`

### Requirement: Sharing and delivery
The owner SHALL share a secret through `desired/secret_share/{owner}/{consumer}/{name}`. Delivery SHALL re-check tenant, grant and share (exact key) on every request, for cross-tenant and cluster secrets. Every container start SHALL fetch through delivery. Deleting a secret SHALL delete its shares in the same transaction.

#### Scenario: Shared secret is delivered
- **WHEN** `payments` shares `database.password` with `frontend` and a `frontend` service referencing it starts
- **THEN** the agent receives the secret material
- **Test:** `integration/TestSharedSecretDeliveredToConsumerTenant`

#### Scenario: Unshare stops crash restarts
- **WHEN** the share is removed and the consuming container crashes
- **THEN** the restart fetches, delivery refuses, and the container does not restart with the secret
- **Test:** `integration/TestUnshareBlocksCrashRestart`

#### Scenario: Stale delivery response after revocation
- **WHEN** a delivery response arrives after a grant, share or owner fact of the instance's service changed
- **THEN** the agent discards it and fetches again
- **Test:** `agent/TestDeliveryResponseDiscardedAfterRevocation`

#### Scenario: No material survives a stop
- **WHEN** an instance stops, and later an init step of a new start runs before delivery succeeds
- **THEN** the tmpfs material was wiped at stop and the init step does not see the old values
- **Test:** `agent/TestSecretMaterialWipedAtStop`

#### Scenario: Share prefix ambiguity
- **WHEN** `payments` shares `a` with `frontend` but not `a/b`
- **THEN** a `frontend` service referencing `tenant/payments/a/b` is refused
- **Test:** `security/TestShareCheckIsExactKey`

#### Scenario: Delete with grants
- **WHEN** the owner deletes a secret granted to a service of its own tenant, then with `--force`; and separately a secret granted only to a service of a consumer tenant
- **THEN** the first is refused listing the service; `--force` and the consumer-only case delete the secret and its shares, and delivery to the remaining grants refuses
- **Test:** `api/TestSecretDeleteGrantRules`

#### Scenario: Share cap
- **WHEN** a secret already has 100 shares and the owner adds another
- **THEN** the share is refused
- **Test:** `api/TestSecretShareCap`

#### Scenario: Re-created secret does not inherit shares
- **WHEN** a shared secret is deleted and created again under the same name
- **THEN** it has no shares
- **Test:** `api/TestSecretDeleteRemovesShares`

#### Scenario: Invalid shares
- **WHEN** a user shares a secret that does not exist, shares with its own tenant, or names an undeclared consumer tenant
- **THEN** each is rejected and no share fact is written
- **Test:** `api/TestSecretShareRejectsInvalidTargets`

### Requirement: Optional default-tenant admission policy
The system SHALL offer an admission policy, off by default, that rejects services resolving to `default`.

#### Scenario: Policy off by default
- **WHEN** no policy is configured and an unowned flat-named service is applied
- **THEN** apply succeeds
- **Test:** `integration/TestAdmissionPolicyOffByDefault`

#### Scenario: Policy enabled
- **WHEN** the policy is enabled and an unowned flat-named service is applied
- **THEN** apply fails with an error asking for an owner
- **Test:** `integration/TestAdmissionPolicyRejectsDefaultTenantService`

### Requirement: Audit
The system SHALL audit secret set, delete, delete-conflict, share, unshare, list denials, 409 guard refusals and delivery refusals with principal, action, canonical key, decision and request ID.

#### Scenario: Share audited
- **WHEN** a user shares and unshares a secret, and another user's share attempt is denied
- **THEN** three audit records name the principal, the share key, the decision and the request ID
- **Test:** `api/TestSecretShareIsAudited`

#### Scenario: Refusals audited
- **WHEN** a list is denied, a set is refused by the guard, and a delivery is refused
- **THEN** each produces an audit record with decision deny
- **Test:** `api/TestSecretRefusalsAreAudited`
