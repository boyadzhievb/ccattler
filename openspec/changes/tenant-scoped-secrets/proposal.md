# Proposal

Implements M179 (Phase 179) from `milestones.md`. Legacy name migration is a separate change, `secret-name-migration` (M209), split off on 2026-11-12 after two review rounds.

## Why

All user secrets share one flat namespace (`secrets/{name}`), so a secret has no owning tenant and any service with a grant fact can consume it. Every authorization check runs at cluster scope, so a configuration can claim any tenant with `owner`. Tenants cannot keep secrets private from each other, and user secrets collide with system keys (the `route/` reservation of M176).

## What Changes

- Canonical store keys `secrets/tenant/{tenant}/{name}` and `secrets/cluster/{name}`, served by new `/api/secret/v2/…` endpoints; `cca secret set/get/list/delete/share/unshare --tenant/--cluster` (neither means `default`).
- Tenants for services and connections (`owner`, hierarchical name, `default`) and gates (target service's tenant); name validation closes forged grant keys; reserved tenant names and secret first segments.
- Apply authorizes at the owning tenant's scope through one resolution stage on both apply paths; conditional policies no longer widen scope.
- Shares `desired/secret_share/{owner}/{consumer}/{name}`, also for cluster secrets; delivery re-checks tenant, grant and share; every container start fetches.
- Grant values carry `{source, reference, mount_path}`; the agent's config hash ignores the format change.
- Existing legacy secrets keep working for their existing grants; no new grants on them.
- Mixed-version guard over API replicas and agents; conflict handling for canonical-looking legacy keys.
- Optional admission policy rejecting services that fall into `default`.
- The `route/` reservation stays permanently.

Out of scope: legacy name migration (M209), per-tenant KEKs, secret versioning, keyring changes, tenant quotas, job pull secrets, compromised-node isolation (M181).

## Capabilities

### New Capabilities
- `tenant-scoped-secrets`: ownership, naming, sharing, resolution and delivery checks of tenant and cluster secrets.

### Modified Capabilities

None (no specs exist yet in `openspec/specs/`).

## Impact

- `lang/` (name validation, qualified references, connection `owner`), `api/` (resolution stage, v2 handlers, status/describe), `security/` (authorizer scope, conditional policies, delivery, gate key delivery), `agent/` (grant values, config hash, fetch on start), `types/`, `tenant/`, `cmd/cca`.
- Store keys: `secrets/tenant/…`, `secrets/cluster/…`, `desired/secret_share/…`, `keyring/name_format`, `keyring/name_conflict/…`, `desired/service/{svc}/secret/{canonical name}`.
- Behaviour change: container restarts always fetch secrets (a crash while the API is down waits for it).
- Docs: `config-secrets.md`, `etcd-schema.md`, `cli-reference.md`, `dsl-reference.md`, `runtime-invariant-flows.md`, `threat-model.md`, call graphs.
- Upgrade rule: every API replica and agent upgraded before the first scoped write.
