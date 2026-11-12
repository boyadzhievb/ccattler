# Design

Full design: `design/config-secrets.md`, section "Tenant-scoped secret names" (decisions A, R2, R3, R8). This file records what the change adds. Revised after three code-reviewer + security-audit rounds on 2026-11-12 (findings in milestones.md Phase 179). Legacy name migration is M209 (`openspec/changes/secret-name-migration/`); the job/service confusion in delivery is fixed separately in M210.

## Context

- User secrets live at `secrets/{name}`; gate keys at `secrets/route/{gate}/tls/key`; the store key is the v2 envelope AAD (M177) — only once `keyring/legacy` is `none` or `retired` (v1 envelopes do not bind it).
- Grant facts are `desired/service/{svc}/secret/{name}` with the mount path as value (default `/run/secrets/{name}` from the parser) or `connection`. The agent mounts every grant at `/run/cca-secrets/{name}` and at the value path, and hashes `"secret:" + key + "=" + value` into the instance config hash stored at start (`agent/agent.go` `computeServiceConfigHash`, `detectConfigChange`).
- Pull secrets: `desired/service/{svc}/image_pull_secret` names the secret; delivery requires an explicit grant for it. Connections (`desired/connection/{name}`, one JSON fact) and routes/gates have no owner; only `ServiceDecl` has `Owner`.
- Apply commits compiled facts with sequential `Put`s and never deletes facts; every authorization uses `ScopeCluster`; the per-fact authorizer has no tenant. Conditional policies have no scope field and receive a nil resource context.
- Service names are not validated in `lang/`; a service named `x/secret/y` writes service `x`'s grant key; a route named `a/service` writes gate `a`'s target. The lexer allows uppercase, `_`, `.`, `:` in identifiers.
- `tenant/scoped_query.go` shows every non-`desired/service/` key to every team reader.

## Goals / Non-Goals

**Goals:** tenant and cluster ownership of secrets, connections and gates; explicit sharing; apply-time and delivery-time checks at the owning tenant's scope; existing clusters keep working, with no instance restarts from the upgrade.

**Non-Goals:** migrating legacy names (M209); per-tenant KEKs; tenant quotas; job pull secrets (no production reader; gap); scoped conditional policies (a scope field is later work); compromised-node isolation (M181).

## Build order (one release, four steps)

1. Names and apply authorization (validation, tenant resolution, stored-owner checks, decode table, conditional policies).
2. Secrets API and shares (v2 endpoints, CLI, guard, conflicts, visibility).
3. Resolution, delivery and agent (grant values, legacy pinning, fetch on start, gate keys).
4. Upgrade verification (rolling HA upgrade, deploy cluster under traffic), review.

## Decisions — names and authorization

| Decision | Choice | Why |
|---|---|---|
| Name validation | Services: every segment passes `ValidateResourceName`, and no segment equals a store attribute name. Routes/gates and connections: single segment, `ValidateResourceName`. The reserved attribute set is one exported list in `types`, and a test fails when a `KeyDesiredService*`, `KeyEffectiveService*`, `KeyIntent*Service*` or `KeyDesiredRoute*` helper adds a segment the list lacks; `tenant/scoped_query.go` uses the same list. | Forged keys (`x/secret/y`, `a/service`); one list instead of three drifting ones. |
| Existing invalid names | `cca lint` and the server startup log list every existing service, connection and route whose name fails the new rules or resolves to a reserved tenant; they keep running, re-apply fails until renamed. Upgrade note. | The lexer accepted names the new rules reject. |
| Tenant resolution | Service or connection: `owner`, else first segment of a hierarchical name, else `default`; a gate: its target service's tenant. Only "no owner fact" maps to `default`; any store error fails. A hierarchical name's first segment must equal `owner` unless the applier holds `cluster.admin`. Explicit `owner default` is allowed; `owner cluster/tenant/route` rejected. | Fail closed; no namespace squatting. |
| Existing objects | The resolution stage reads the stored owner of an existing service or connection and the stored target of an existing gate. Scope is required at the stored tenant and at the declared one; an omitted `owner` on an owned object means "unchanged". A tenant change needs `cluster.admin`; retargeting a gate needs `workload.create` at the old and the new target's tenant. A gate whose target is neither declared nor stored is rejected. | Apply never deletes facts, so re-applying `service api` without `owner` would otherwise replace another tenant's image while the stored owner (and its secrets) stays (round 3 SA H1). |
| Ownerless existing connections | Treated as legacy: changing one needs `cluster.admin` until an admin assigns an owner. | Otherwise any `team/default` user could repoint the host and receive a pinned legacy password. |
| Decode table | The stage classifies each compiled fact by the declaration that produced it (annotated by `Compile`, not by parsing keys): `desired/service/{svc}/…`, `effective/service/{svc}/…`, `intent/user/service/{svc}/…`, `derived/service/{svc}/…` → service tenant; `desired/connection/{c}` → connection tenant; `desired/route/{g}/…` → gate tenant. Everything else (tenants, volumes, jobs, cronjobs, groups, cloud identities, credential brokers, config, policy) and any unclassified fact keeps today's check at `ScopeCluster`; a test fails on an unclassified family. `desired/secret_share/` → `cluster.admin`. | Opening team scopes only where a tenant is defined (round 3 SA H3, CR M3). |
| Apply paths | One stage in `api/` used by `applyDirect` and `EvaluateAndCommit`, taking either principal form; `evaluateSecurityPolicyGate` keeps treating `ErrKeyNotFound` on tenant state as "not deleting" and fails on any other error. | Review N5, CR M3/L5. |
| Conditional policies | Grant only when `requiredScope` is `cluster` (fail closed below it); scoped checks pass `ResourceContext{Team: tenant}`. Upgrade note: principals whose rights come only from conditional policies keep cluster-scope access but no longer satisfy tenant-scoped checks unless they also hold an unconditional grant. | `ConditionalPolicy` has no scope field; a schema change is later work (round 3 H2/M4). |

## Decisions — secrets API and shares

| Decision | Choice | Why |
|---|---|---|
| Endpoints | `/api/secret/v2/value` (GET/PUT/DELETE, query `scope=tenant\|cluster`, `tenant`, `name`; `scope=cluster` with `tenant` rejected), `/api/secret/v2/list`, `/api/secret/v2/share`. Authorization runs before the tenant-existence check. The CLI never falls back to `/api/secret` on 404. | Old replicas 404; no tenant-name oracle. |
| Legacy endpoint | `/api/secret` keeps get, list, delete and **update of existing** legacy keys at cluster scope; it refuses creating keys, reserved names and anything under `tenant/` or `cluster/`. CLI: `cca secret set|get|delete --legacy`. `cca secret set X` (default tenant) warns when a legacy `X` exists that pinned services still use. | Legacy values must stay rotatable until M209 (round 3 CR H4). |
| Tenant existence | `--tenant X` needs `desired/tenant/X` (or `default`) for set and share only; get, list, delete work on any existing key, and admins' list scans the `secrets/tenant/` prefix. | Secrets of a deleted tenant stay manageable; a re-declared tenant inherits them (threat model note). |
| Mixed-version guard | Replicas report `secret_name_format: tenant-v1` in the keyring replica report; agents in their node report. A separate helper refuses **set and share** with 409 naming live replicas and enrolled (from `enrollment/node/`), non-disabled nodes without it, and when no keyring reporter exists. Delete, unshare and delete-conflict are never blocked. Set and share also require `keyring/legacy` ∈ {none, retired}. A node forging another node's report (node-writable `observed/node/`) can block writes — accepted until M181. | Emergency revocation must not depend on every node (round 3 SA M2, CR M4/M7). |
| `keyring/name_format` and conflicts | Owner: `KeyringEnvelopeCodec` on the raw store (as the keyring bootstrap). Each replica's reporter loop, once the guard passes, tries a create-only transaction writing `keyring/name_format = tenant-v1` plus `keyring/name_conflict/{key}` for every key already under `secrets/tenant/` or `secrets/cluster/` (batches before the marker if large). Until the marker exists, v2 reads and writes, apply resolution of qualified references, and delivery of canonical keys refuse. `delete-conflict` requires the record for that exact key and deletes key and record in one transaction guarded on both revisions. The `secret-delivery` role reads `keyring/name_format` and `keyring/name_conflict/`. A legacy writer outside the guard (expired lease) between scan and marker is accepted (needs cluster `secret.write`). | Defined writer and trigger (round 3 CR H5, SA M5). |
| Shares | `desired/secret_share/{owner}/{consumer}/{name}`, `owner` = tenant or `cluster`; written only by the share handler; the `api-server` store role gets delete on `desired/secret_share/`. At most 100 shares per secret, so a delete cascades in one transaction; delete refuses grants of the owning tenant unless `--force`. Sharing to `default` warns. | 128-op limit (round 3 SA M11). |
| Visibility | `/api/state` and watch: `desired/secret_share/{owner}/{consumer}/…` visible to readers of the owner or consumer tenant; `desired/connection/{c}` to readers of the connection's tenant; cluster readers see all. | "Share recipient cannot enumerate" must hold beyond `secret list` (round 3 SA M6). |
| Listing | `secret.metadata.read` checked with `AuthorizeAPI` per tenant; metadata only; status and describe filter the same way. | |
| `route/` reservation | Permanent. `/api/gate-key` stays cluster-only (`secret.write` at cluster). | |
| Audit | New `request_id` field in `AuditEntry` and a request-ID middleware; records for set, delete, delete-conflict, share, unshare, list denials, 409 refusals, delivery refusals. | `AuditEntry` has no request ID today. |

## Decisions — resolution, delivery and agent

| Decision | Choice | Why |
|---|---|---|
| Resolution | Service secrets and pull secrets in the service's tenant; connection secrets in the connection's tenant; gate `key secret` in the target service's tenant. Consumers in another tenant (and every consumer of a cluster secret) need a share. One error text for missing and unshared. | Round 2 SA HIGH-2. |
| Legacy pinning | Order for an unqualified reference: (1) an existing grant for the legacy name while that legacy secret exists → stays legacy; (2) the canonical secret in the owner's tenant; (3) refuse. No new grant may name a legacy secret (same error text). A pin ends when the legacy key is deleted or M209 moves it; grants are never deleted by apply (documented). Connections pin through their consuming services' grants. | Round 3 CR H4. |
| Grant format | Legacy grants keep key and plain-string value byte-identical. Canonical grants: key `desired/service/{svc}/secret/{canonical}`, value JSON `{reference, mount_path, roles}` with `roles` ⊆ {`explicit`, `connection:{name}`, `image_pull`}. The resolved canonical name of a connection or pull reference is stored per service (`desired/service/{svc}/connection/{c}` value gains `secret`, `desired/service/{svc}/image_pull_secret` holds the canonical name) only when canonical. Pull secrets still need an explicit grant. The same reference text resolving to two canonical secrets in one service is a collision. | One `source` cannot express several roles; old agents never see JSON because canonical secrets need the guard (round 3 CR H1/H3). |
| Config hash | For legacy grants the agent emits exactly today's bytes; for canonical grants `"secret:" + KeyDesiredServiceSecret(svc, reference) + "=" + mount_path-or-connection`, sorted by reference. A golden test compares with a hash computed by the v1.77.0-beta code. A tenant change of a running service therefore takes effect at the next restart (documented). | Agent upgrade alone must restart nothing (round 3 CR H1). |
| Mount paths | Unchanged for legacy grants (`/run/cca-secrets/{name}` and the value path). Canonical: `/run/cca-secrets/{reference}` and the explicit path; a qualified reference defaults to `/run/secrets/{last segment}`. Collisions (including connection files and `a` vs `a/b`) fail apply, and the agent refuses the instance. The agent validates `reference` with `ValidateSecretName` and `mount_path` with `resolveExplicitSecretMountPath`. | Round 3 CR M8, SA L4. |
| Fetch on every start | Remove the tmpfs reuse branch (`agent/secret_delivery.go` `materialMatchesRequirements` path); wipe material when an instance stops or exits; init steps use only material from the current start's delivery. A per-instance generation counter is bumped when a grant, share or owner fact of the instance's service changes; a delivery response with an older generation is discarded. The legacy agent secret API checks grant and share by canonical name. API unreachable → `waiting-for-secrets`. A running container keeps its mounted copy until it stops. | Round 3 SA M8, CR M6. |
| Gate key delivery | Reads `desired/route/{gate}/service`, resolves the target's tenant with the shared helper, checks the share for a foreign-tenant or cluster key. | Round 3 CR M2. |
| Compromised node | Delivery trusts `observed/instance/{id}/service`, which nodes can write: a node can obtain secrets of any service for instances placed on it. Accepted until M181 (threat-model.md wording corrected). | Round 3 SA M7. |

## Risks / Trade-offs

| Failure case | Behaviour |
|---|---|
| Old replica receives a v2 request | 404; nothing written. |
| Old agent while canonical grants appear | Impossible: set/share need every live replica and enrolled node on `tenant-v1`. A node enrolled later with an old agent cannot use canonical secrets until upgraded (refused delivery, no crash). |
| Owner lookup fails | Apply and delivery fail; never `default`. |
| API down when a container crashes | It waits in `waiting-for-secrets` (accepted 2026-11-12). |
| Unshare while an instance runs | The running container keeps its copy until it stops. |
| Forged node report blocks set/share | Accepted until M181; delete, unshare, delete-conflict stay available. |
| Delivery refusal reasons in observed state | Distinguish missing from unauthorized for `observed/` readers — accepted (low). |

Open: none.
