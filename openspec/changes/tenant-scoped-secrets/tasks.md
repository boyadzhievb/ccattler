# Tasks

One release, four steps (design.md "Build order"). Each step: failing tests first, then code, then `gofmt`, `golangci-lint`, `go test ./...`.

## 0. Before step 1

- [ ] 0.1 Update Flow 27 (canonical grants, tenant and share re-check, fetch on every start, generation counter) and Flow 28 (gate `key secret` in the target service's tenant); fix the "Flow 9" reference
- [x] 0.2 Resolve the open questions in `design.md` (three review rounds, 2026-11-12)
- [ ] 0.3 Targeted re-check of the round-3 rows by `code-reviewer` + `security-audit` (no full round)

## 1. Names and apply authorization

- [ ] 1.1 Reserved attribute segment list in `types` + completeness test over the key helpers; `tenant/scoped_query.go` uses it
- [ ] 1.2 Name validation: services per segment, routes and connections single segment; reserved tenant names; reserved secret first segments; canonical length
- [ ] 1.3 Lint and startup listing of existing names that fail the new rules or resolve to a reserved tenant
- [ ] 1.4 Tenant resolution helper in `types` (fail closed; first segment = owner; gates via target); remove the duplicate in `lang/compiler.go`
- [ ] 1.5 Connection `owner` (DSL, compiler, JSON value, agent `parseConnectionJSON`, delivery `connectionSecretField`, status, describe, `dsl-reference.md`)
- [ ] 1.6 `Compile` annotates each fact with its declaration; resolution stage in `api/` shared by `applyDirect` and `EvaluateAndCommit`: decode table, stored-owner and stored-target checks, omitted owner = unchanged, gate retarget needs both tenants, unknown gate target rejected, ownerless connections need `cluster.admin`, unclassified families at `ScopeCluster`, classification test
- [ ] 1.7 `RequiredCapabilityForFactWrite`: `desired/secret_share/` → `cluster.admin`
- [ ] 1.8 `evaluateSecurityPolicyGate`: `ErrKeyNotFound` = not deleting, other errors fail
- [ ] 1.9 `AuthorizeAPI`: conditional policies grant only at `cluster`; scoped checks pass `ResourceContext{Team}`; upgrade note (moves the M182 item)

## 2. Secrets API and shares

- [ ] 2.1 Key helpers: `secrets/tenant/…`, `secrets/cluster/…`, `desired/secret_share/{owner}/{consumer}/{name}`, `keyring/name_format`, `keyring/name_conflict/`; legacy-key predicate
- [ ] 2.2 `/api/secret/v2/value|list|share`; scope/tenant validation; authorize before tenant-existence; tenant declaration for set and share only; admin list scans `secrets/tenant/`
- [ ] 2.3 Legacy `/api/secret`: get, list, delete, update of existing keys; refuse create, reserved and canonical names; `cca secret … --legacy`; warning on default-tenant set when a pinned legacy name exists
- [ ] 2.4 Guard: `secret_name_format` in the keyring replica report and the node report; nodes from `enrollment/node/`; set and share only; `keyring/legacy` ∈ {none, retired}; 409 without a reporter
- [ ] 2.5 `keyring/name_format` + conflict records by `KeyringEnvelopeCodec` (reporter loop, create-only); canonical paths wait for the marker; `delete-conflict` with record and revision guards
- [ ] 2.6 Store roles: `secret-delivery` reads `desired/secret_share/`, `keyring/name_format`, `keyring/name_conflict/`; `api-server` deletes `desired/secret_share/`
- [ ] 2.7 Shares: cap 100, delete cascade in one transaction, own-tenant grants refuse unless `--force`, warning for `default`
- [ ] 2.8 Visibility of `desired/secret_share/` and `desired/connection/` in `/api/state` and watch; status and describe filter
- [ ] 2.9 CLI: `cca secret set/get/list/delete/share/unshare/delete-conflict --tenant/--cluster/--legacy`, stdin or `--from-file`, no fallback on 404; `cli-reference.md` (share key, flags)
- [ ] 2.10 Audit: `request_id` in `AuditEntry` + middleware; records for every listed action and refusal
- [ ] 2.11 Delete `tenant/secret_isolation.go`

## 3. Resolution, delivery, agent

- [ ] 3.1 Parser: qualified references for service, connection and pull secrets; default mount for qualified references
- [ ] 3.2 Resolution: tenants per owner kind, shares, one error text, legacy pinning order, no new legacy grants, role merge, collisions (incl. connection files), per-service canonical connection and pull facts
- [ ] 3.3 Grant format: legacy byte-identical; canonical JSON `{reference, mount_path, roles}`
- [ ] 3.4 Delivery: tenant, grant, share (exact Get, cluster secrets too), marker wait, conflict refusal, missing secret refusal, invalid names audited
- [ ] 3.5 Gate key delivery via `desired/route/{gate}/service` and the shared tenant helper; `/api/gate-key` stays cluster-only
- [ ] 3.6 Agent: parse both grant formats; canonical lookups with role mapping; validate reference and mount path; refuse colliding mounts; legacy secret API share check by canonical name
- [ ] 3.7 Agent config hash: legacy bytes unchanged; golden test against v1.77.0-beta
- [ ] 3.8 Agent: fetch on every start, wipe at stop, init steps use current material only, generation counter on grant/share/owner changes
- [ ] 3.9 Optional admission policy rejecting services that resolve to `default`

## 4. Verification and docs

- [ ] 4.1 Docs: `config-secrets.md`, `etcd-schema.md`, `key-ownership-matrix.md`, `cli-reference.md`, `dsl-reference.md`, `threat-model.md` (compromised node via `observed/instance/{id}/service`, forged node reports, re-declared tenants), `security/capability.go`, call graphs; upgrade notes
- [ ] 4.2 Rolling upgrade on the HA set from v1.77.0-beta: v2 404 on old replicas; set/share 409 until all replicas and agents report; no restarts
- [ ] 4.3 Deploy cluster under traffic: legacy grants keep working, 0 restarts; shared secret delivered; unshare blocks a crash restart
- [ ] 4.4 Independent review of the implementation; findings recorded in the M179 checklist
