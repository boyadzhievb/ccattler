# Authorization redesign: groups and direct capability grants

Design proposal for the brief in [design/security_redesign_noroles.md](../security_redesign_noroles.md) (revised version, 2026-11-14).
Design only: nothing here is implemented. Every statement about current behaviour was read in the code on 2026-11-14; none was executed as a test.

## Summary

- The target model is sound and smaller than the current one: principals, groups, capabilities, scopes, direct grants. Policies only restrict.
- The change is less disruptive than it looks, because most of the role machinery has no production caller (section 1.3).
- The investigation found one privilege-widening bug in today's direct grants (B1 below). It should be fixed now, before and independently of this redesign.
- Six decisions are needed from you before implementation (section 12).

## 1. Current authorization architecture

### 1.1 Permission paths that exist today

| # | Path | Where | Used in production |
|---|---|---|---|
| P1 | Principal → role grant → role capabilities × role scopes | `auth/grant/{kind}/{name}/role/{role}`, `auth/role/{role}/capability/…`, `…/scope/…`; `controllers/auth.go` `buildCapabilityGrants` | yes (DSL `role` + `grant`) |
| P2 | Principal → role grant → built-in bundle | `BuiltinRoleCapabilities` (`cluster-admin`, `api-reader`, `api-writer`, `node-agent`); used when the role has no capability facts | yes (`cca role grant --role`) |
| P3 | Principal → hidden inline role → capabilities × scopes | `auth/role/inline-{kind}-{name}/…`; `api/handler_role.go` | yes (`cca role grant --capability`) |
| P4 | Group → role grant, expanded to each member as `user:{member}` | `auth/group/{g}/member/{user}`; `parsePrincipalRolesFromFacts` | yes, users only |
| P5 | Bootstrap grants held in memory, not revocable | `APIAuthorizer.GrantRole` for `user:local-admin` and the server's `node:<cn>` | yes |
| P6 | Conditional policy grants a capability by itself when its conditions pass, at any scope | `AuthorizeAPI` second loop; `auth/policy/…` | yes if a policy is applied; none in e2e workloads |
| P7 | Group named `platform` sees every tenant's audit entries | `tenant.IsPlatformPrincipal`, called only from `resolveAuditTenantFilter` | yes (local admin token, webhook users) |
| P8 | Wildcard principal grants (`user:*`) | `appendMatchingGrants` | no caller creates one |
| P9 | Store layer: principal → role binding → key-prefix rules | `security/rbac.go` | only for 2 in-process principals (1.3) |
| P10 | Node identity: mTLS `node:` principal, enrolled, instance placed on it | `/api/node/secrets`, `/api/node/gate-keys` (Flows 27, 28) | yes; no capability involved |
| P11 | Agent debug API admits the client whose `node:<cn>` holds the fact `auth/grant/node/<cn>/role/cluster-admin` | `agent/debug_api.go` `NewClusterAdminGrantChecker` | yes (the API server's certificate) |

### 1.2 Group sources today (three, not connected)

| Source | Stored as | Feeds |
|---|---|---|
| DSL `group { member … }` | `auth/group/{g}/member/{user}` | grant expansion (P4) only |
| `cca user create --group` | `groups` in the user record, copied into the user token's `groups` claim | **nothing**: no authenticator reads that claim back |
| Webhook authenticator response, local admin token (hard-coded `platform`) | `Principal.Groups` for one request | the `platform` check (P7) and `subject.groups` in policy conditions |

The OIDC authenticator reads no group claim at all (`constructAuthenticationResultFromClaims` maps only principal, `team` and `role` attributes). So no group taken from a login token can grant a capability today; token groups only affect P6 and P7.

### 1.3 Role machinery without a production caller

- Store-layer roles `cluster-admin`, `node-agent`, `scheduler`, `controller`, `credential-broker`, `cloud-controller`, `api-reader`, `api-writer`. The store layer only ever authorizes `service:api-server` and `service:secret-delivery` (`AuthorizedStore.authorizingPrincipal`); controllers and agents use the raw store.
- Store-layer bindings for `user:local-admin`, the server principal and enrolled nodes (`bindRBACRoleForNode`). They are in memory on one replica and never checked.
- `ABACAuthorizer`, `CombinedAuthorizer` (store layer), wildcard bindings, `RemoveBinding`.
- API-layer `node-agent` bundle: nothing grants it.

### 1.4 Defects found while tracing (independent of the redesign)

| # | Defect | Evidence | Severity |
|---|---|---|---|
| B1 | Direct grants at different scopes multiply. `cca role grant user:alice --capability workload.read --scope team/web` followed by `--capability secret.read --scope cluster` gives alice `workload.read` at `cluster` and `secret.read` at `team/web` too: one inline role holds all capabilities and all scopes, and `buildCapabilityGrants` takes the cross product. | `api/handler_role.go:92-95`, `controllers/auth.go:164-175` | privilege widening, present now |
| B2 | `cca role revoke --capability` deletes the capability for every scope and leaves the scope facts, which later multiply with new capabilities. The three writes of a grant are not one transaction. | `api/handler_role.go:97-108`, `:145-153` | wrong revocation |
| B3 | Unknown names are accepted silently: a DSL role may list a capability that does not exist (`design/dsl-reference.md` uses `service.read`), and a grant may name a role that does not exist. Both grant nothing, with no error. | `lang/compiler.go:1468-1528` has no validation | silent no-op |
| B4 | `cca user create --group` has no effect on authorization (1.2). | `security/oidc.go:173-204` | dead feature |
| B5 | A group can only contain users; a service or node listed as a member is treated as `user:{name}`. | `controllers/auth.go:123-128` | wrong principal |
| B6 | Conditional policies are never given a resource (`AuthorizeAPI` is always called with `nil`), so `resource.*` conditions can never pass, while `subject.*` conditions grant cluster-wide. | every `requireCapability` call | policy semantics |
| B7 | `cca apply` never deletes auth facts, so removing a grant or member from a configuration does not revoke it. | apply prunes only gates (M170) | revocation gap |

## 2. Target model

```
Principal ── direct grant ───────────────┐
                                         ├─ capability + scope match ─ restrictions pass ─ allow
Principal ── member of ── Group ── grant ┘
```

- **Principal**: `user`, `service`, `node` (the `system` kind is removed with its only use, P7).
- **Group**: a set of principals of any kind. No group name has a built-in meaning.
- **Grant**: subject (principal or group) + one capability + one scope. `cluster.admin` keeps its meaning (implies every capability inside its scope); it is a capability, not a role.
- **Restriction** (was "policy"): a condition that must hold for a capability; it can only turn an allow into a deny.
- Removed: roles, role bindings, built-in bundles, inline roles, wildcard principals, the `platform` rule, policies that grant.

## 3. Fact model

### 3.1 Grants

```
auth/grant/{kind}/{subject}/{capability}/{scope}
    kind        user | service | node | group
    subject     name with "/" written as %2F (as secret delivery does on disk, M174)
    capability  e.g. workload.read (never contains "/")
    scope       cluster | team/web | node/node01 … (last, may contain "/")
value: {"source":"config|cli|bootstrap|enrollment|migration","by":"user:ops-admin","at":"2026-11-14T12:00:00Z"}
```

- One key per (subject, capability, scope): adding is idempotent, revoking deletes exactly one key (fixes B1, B2).
- Parsing is unambiguous: split into four parts, the fourth is the scope.
- `Scan("auth/grant/{kind}/{subject}/")` lists one subject's grants; user deletion and the enrollment check keep their existing prefix scans.
- Old keys have the literal third segment `role`, which is not a capability name, so both layouts can coexist during migration. The new code ignores `…/role/…` keys; the old code ignores the others.
- Writing a grant: one fact, validated against `security.AllCapabilities` and the scope syntax, `cluster.admin` required (unchanged: `RequiredCapabilityForFactWrite` for `auth/`).

### 3.2 Group membership

```
auth/group/{group}/member/{kind}/{member}      member with "/" as %2F
value: {"source":"config|cli|external:<provider>","by":"…","at":"…"}
```

Old keys `auth/group/{group}/member/{user}` are migrated to `…/member/user/{user}` (fixes B5). The `groups` field of the user record and the `groups` token claim are removed (B4): `cca user create --group g` writes the membership fact in the same transaction as the user record.

### 3.3 Restrictions

`auth/restriction/{name}/capability`, `auth/restriction/{name}/condition/{i}/field|operator|value` (same shape as today's `auth/policy/`, new prefix so that old granting policies can never be read as restrictions by mistake).

## 4. DSL grammar

```
group web-deployers {
    member alice                      # a bare member is a user (existing syntax)
    member service payments/checkout
    member node node01
}

grant workload.read, workload.create, workload.update to group web-deployers scope team/web
grant node.read, node.manage to group production-operators scope cluster
grant cluster.admin to user ops-admin scope cluster

restrict workload.update {
    name team-isolation
    when subject.team == resource.team
}
```

- `grant <capability>[, <capability>…] to <user|service|node|group> <name> scope <scope>`. `scope` is mandatory: no grant becomes cluster-wide by omission.
- The compiler rejects unknown capability names (fixes B3).
- `role { … }`, `grant <role> to …` and `policy { allow … }` become compile errors that print the equivalent `grant` lines. `cca fmt` does not rewrite them (a formatter must not change authorization).
- The service co-scheduling block also uses the keyword `group` (`lang/parser.go:2093` tells them apart by content). Not changed here; worth a separate rename.

## 5. Evaluation algorithm

```
authorize(principal, capability, scope, resource):
  1. candidates = grants[principal] ∪ grants[group] for every group in groupsOf[principal]   (in-memory indexes)
  2. match = first candidate, in sorted key order, with
             (candidate.capability == capability or candidate.capability == cluster.admin)
             and candidate.scope.Contains(scope)
  3. no match → deny("no grant")
  4. for every restriction on `capability`, in name order:
         conditions false or not resolvable → deny("restriction <name>")
  5. allow, returning {grant key, group or "direct", restrictions checked}
```

- Group membership is resolved from the membership facts at decision time, never from the token. `subject.groups` in a restriction reads the same index.
- The result of step 5 is what `cca access explain` prints and what the audit entry records (`policy` field: grant key).
- Deterministic: indexes are rebuilt from sorted facts; same facts give the same decision and the same explanation.
- Restrictions apply to `cluster.admin` holders too, with one exception: the bootstrap grant of `user:local-admin` (break-glass, so a bad restriction cannot lock the cluster).
- Handlers pass a resource (`team`, `name`) wherever they know it; M179 task 1.9 already plans this. A restriction that needs a resource the handler does not provide denies (fail closed), and `cca lint` warns about it.

## 6. Internal service identity

| Today | Kind of data | Target |
|---|---|---|
| `service:api-server`, `service:secret-delivery` bound to store roles of the same name | In-process identity plus a key-prefix access list | Keep the two principals. Replace `Role`/`RoleBinding` with one table: principal → access rules, set at startup (`storeAccessForAPIServer()`, `storeAccessForSecretDelivery()`). No role name, no binding step. |
| 8 other store roles, wildcard bindings, store ABAC | Dead code (1.3) | Delete. |
| `bindRBACRoleForNode` at enrollment | Dead: never checked, lost on restart | Delete. Enrollment keeps writing `enrollment/node/{id}` (identity). |
| Node access to delivery endpoints (P10) | Identity kind + placement | Enrollment writes an explicit grant `auth/grant/node/{id}/node.agent/node/{id}` (`source: enrollment`) in its transaction; the two delivery endpoints require it in addition to the placement checks. Removing the grant cuts a node off without touching its certificate. See decision D2. |
| Server certificate `node:<cn>` holding `cluster-admin` (P5, P11) | Authorization | Becomes the grant `auth/grant/node/{cn}/cluster.admin/cluster` (`source: bootstrap`). The agent debug checker reads that key. |

The store-layer access lists stay in code, not in facts, on purpose: `service:api-server` may write `auth/`, so a fact-defined list would let it widen itself. See decision D1.

The enrollment rule "a node ID that holds grants cannot be enrolled" (M171) becomes "…holds any grant whose source is not `enrollment`".

## 7. Platform group and audit visibility

- Delete `IsPlatformPrincipal` and the hard-coded `platform` group on the local admin and demo authenticators.
- Audit visibility follows the same rule as `/api/status` (decision of 2026-11-03, M173): `workload.read` at `cluster` sees every tenant, at `team/<name>` sees that tenant, no grant is 403. Team membership adds nothing. This also removes the audit endpoint's dependence on the token's `team` attribute.
- No new capability is needed: the existing capability plus scope expresses it. A separate `audit.read` is decision D3.
- Effect: `user:local-admin` is unchanged (holds `cluster.admin`). A webhook-authenticated user in a group called `platform` without a cluster-scope `workload.read` grant loses cross-tenant audit access; the migration report cannot list such users (they exist only in the webhook), so the release notes must say it.

## 8. Restrictions instead of granting policies

- Today a policy is `allow <capability> when <conditions>` and grants by itself (P6, B6).
- Target: a restriction never grants (section 5, step 4).
- Migration cannot translate a granting policy automatically: its subjects are "whoever has attribute X", not a list of principals. For each existing policy the migration report prints the policy and two choices: (a) create a group, grant the capability to it at a stated scope, keep the condition as a restriction; (b) delete it. The migration refuses to finish while an unconverted policy exists (never silently drops, never silently widens).
- Attributes in conditions (`subject.team`, and the OIDC `role` claim mapped to `subject.role`) come from the token and are as old as the token. Since they can now only restrict, a stale attribute can at worst keep a restriction passing until the token expires. The `role` claim mapping is renamed to a generic attribute mapping; it never had authorization meaning.

## 9. Group membership from login tokens

- Finding: no token-borne group grants anything today (1.2). The target keeps it that way explicitly: authentication yields the principal only; groups in tokens and webhook responses are ignored for authorization.
- Local and configuration-managed memberships are facts and take effect on every replica as soon as its watch delivers the change.
- External memberships are **not synchronized in this change** (no synchronization exists, and the brief excludes new identity-provider integrations). Defined behaviour for when one is added:
  - a sync component writes `source: external:<provider>` membership facts with a lease; a membership that is not refreshed expires;
  - maximum staleness = lease duration; provider unreachable = memberships expire after the lease (fail closed);
  - removal at the provider takes effect at the next sync, at the latest when the lease expires.
- Until then: an OIDC or webhook user gets permissions only through direct grants or memberships written in CCattler. See decision D4.

## 10. CLI and API

| Today | Target |
|---|---|
| `cca role grant <p> --role r` | removed |
| `cca role grant <p> --capability c [--scope s]` | `cca grant add <kind:name> <capability>[,…] --scope <scope>` |
| `cca role revoke …` | `cca grant remove <kind:name> <capability> --scope <scope>` |
| `cca role list` | `cca grant list [<kind:name>]` (shows source) |
| `cca role check` | `cca access explain <kind:name> <capability> --scope <scope>` → allow/deny, the grant, the group path, restrictions |
| — | `cca access show [<kind:name>]`: effective permissions; without an argument, the caller's own (no `cluster.admin` needed for oneself) |
| `cca user create --group g` (no effect) | writes the membership fact |
| — | `cca group list`, `cca group add-member <g> <kind:name>`, `cca group remove-member …`, `cca group delete <g>` (removes memberships and the group's grants in one transaction) |

API: `/api/grant` (GET, POST, DELETE), `/api/group` (GET, POST, DELETE), `/api/access` (GET; `explain` query). `/api/role` and `/api/role/check` are removed. All writes require `cluster.admin` and are audited with the grant key. The route table in `AUTHZ_GRAPH.md` and the M219 sweep cover the new routes.

`--role api-reader` users: the e2e script (`cluster_test.sh`, 5 places) and the adversarial replay (5 places) switch to a group defined in their setup:

```
group api-readers { member adv-reader }
grant workload.read, node.read, placement.read, scaling.read, secret.metadata.read to group api-readers scope cluster
```

Ansible does not use `--role` (checked: no match under `deploy/`).

## 11. Ownership and lifecycle

| Event | Behaviour |
|---|---|
| Grant or membership declared in a configuration | `source: config`. On `cca apply`, each **declared group** is authoritative for its `config` members, and each **subject that has grant lines** in the applied files is authoritative for its `config` grants: missing ones are deleted, and `cca diff` lists them (same pattern as gates, `collectRemovedGateFacts`). Fixes B7 for what is declared; a group or subject removed from the files entirely is not pruned until M206. Decision D5. |
| Grant or membership added by CLI | `source: cli`; never touched by apply. |
| User deleted | One transaction: user record, `auth/grant/user/{name}/…`, and that user's `cli` memberships. `config` memberships stay (decision of 2026-11-12); a re-created user of the same name regains those groups, as documented. |
| Group deleted by CLI | Memberships and `auth/grant/group/{g}/…` in one transaction, so a later group of the same name starts empty. |
| Grant to a principal that has no record | Allowed (external users have no record). `cca grant list` marks it. |
| Bootstrap | `user:local-admin` and the server certificate get in-memory grants with `source: bootstrap` plus the fact; listed, not removable, re-created at start. |
| Consistency | Every API replica rebuilds its indexes from a watch on `auth/` and swaps them atomically. A change is effective on a replica when its watch delivers it; no revision is shared across replicas. A replica reports the last applied `auth/` revision in `observed/auth/replica/{id}`. Open streams keep running after revocation (existing open item from M216). |

## 12. Decisions needed

| # | Question | Recommendation |
|---|---|---|
| D1 | The two in-process store principals: access lists in code (per principal, no role) or as grant facts? | In code. Facts would let `service:api-server` change its own limits. This is the one place the proposal departs from "everything is a grant fact". |
| D2 | Node agents: explicit `node.agent` grant written at enrollment, or keep identity plus placement only? | Explicit grant: every allow then traces to a grant, and a node can be cut off by removing it. Costs one new capability and a changed M171 rule. |
| D3 | Audit: reuse `workload.read` plus scope, or add `audit.read`? | Reuse. Add `audit.read` only if tenant users should read workloads but not their tenant's audit log. |
| D4 | External group memberships: drop webhook groups now and define sync for later, or build sync in this change? | Drop now. Nothing synchronizes today, and no token group grants anything today. |
| D5 | Pruning of configuration-owned grants and memberships before M206: per declared group and subject, or wait for M206? | Per declared group and subject. Otherwise "remove the line" silently keeps the permission. |
| D6 | Fix B1 and B2 now as their own bug milestone, or only through this redesign? | Now. B1 widens privileges today, and the fix (one key per capability and scope) is the same fact layout as 3.1, so no work is lost. |

## 13. Migration

Command `cca auth migrate-grants` (cluster admin, same shape as `cca secret migrate-keys`), dry run unless `--apply --confirm-backup`.

1. **Report** (always). For every old fact:
   - role grant to a principal or group → grants = the role's capabilities × scopes, exactly as evaluated today; a role with no capability facts → its built-in bundle; a role with a built-in name **and** capability facts → the facts (reported);
   - inline roles with more than one scope → listed as *ambiguous* with today's effective cross product (B1). The operator either accepts it (`--accept-effective inline-user-alice`) or revokes and re-grants first. Nothing is narrowed or widened without that choice;
   - grant to a role that does not exist, or an unknown capability → reported as "grants nothing today";
   - group members → `…/member/user/{name}`;
   - granting policies → section 8, must be resolved by hand;
   - user records with `groups` → reported (no effect today); written as memberships only with `--adopt-user-groups`.
2. **Write** the new facts (`source: migration`, idempotent, one transaction per subject).
3. **Verify**: compute the effective permission table (principal × capability × scope) from the old facts with the old algorithm and from the new facts with the new one, for every principal that appears anywhere. Any difference stops the migration. Result in `observed/auth/migration`.
4. **Cut over**: `auth/model` goes `roles → grants`, forward only, guarded by: verification passed, every API replica and every agent reports a version that understands grants (agents because of P11). Replicas switch algorithm when they see the fact.
5. **Clean up** (`cca auth retire-roles`, separate, after a grace period): delete `auth/role/…`, `…/role/…` grant keys, old membership keys and `auth/policy/…`.

- Fresh clusters start with `auth/model = grants`.
- Rollback: before step 4, nothing has changed for the running system (old code ignores the new keys). After step 4, restore the etcd snapshot taken for `--confirm-backup`; there is no downgrade path, and no compatibility layer stays.
- Idempotent: re-running reports the same and writes nothing new. Audited: each written grant carries its origin (`from: auth/grant/user/alice/role/developer`).

## 14. Security implications and failure cases

| Case | Behaviour |
|---|---|
| Replica cannot read `auth/` at start | Only bootstrap grants exist; everything else is denied (fail closed), as today. |
| Watch on `auth/` breaks | The runner re-lists and re-subscribes (existing behaviour); until then the replica uses its last state. Bound to be measured and documented in Flow 25. |
| Unparseable grant or membership key | Ignored and logged with the key; never treated as a wider grant. |
| Grant fact with an unknown capability (written around the API) | Matches nothing; `cca grant list` shows it as invalid. |
| Restriction with a bad condition | Denies the capability it covers for everyone except the local admin bootstrap grant; `cca access explain` names it. |
| Group deleted while a member is mid-request | The request already authorized completes; the next request is denied. |
| Same name re-created (user, group, node) | User: direct grants are gone, `config` memberships return (documented). Group via CLI: starts empty. Node: only `enrollment` grants may pre-exist. |
| Mixed versions during upgrade | Old replicas and agents keep the role model until `auth/model` flips; the flip is refused while any of them is old. |
| Someone with etcd write access forges grants | Out of scope here, unchanged; store-level isolation is M181. |
| Privilege changes from the redesign itself | Narrower: B1 cross products (only if the operator does not accept them), granting policies, the `platform` rule, wildcard grants. Wider: none by design; step 3 of the migration proves it per cluster. |

## 15. Tests

- **Unit, authorizer**: direct grant; group grant; union of both; scope hierarchy; `cluster.admin` inside and outside its scope; no grant; removed grant; removed membership; group of services and nodes; restriction denies; restriction cannot allow; local admin bypasses a restriction; explanation names the grant and the group; deterministic output for shuffled facts.
- **Unit, facts**: key round trip with `/` in subject and scope; old and new keys in one scan; idempotent add; exact revoke leaves the same capability at another scope (regression for B1 and B2).
- **Unit, DSL**: grammar; unknown capability rejected; missing scope rejected; `role` and old `grant` produce the error with the suggested lines.
- **API**: grant, group and access routes need `cluster.admin` (the M219 sweep); `access show` for oneself; a user named `platform` or in a group `platform` gets no extra audit entries; audit visibility by grant scope.
- **Integration**: user lifecycle (delete, re-create, config membership returns, direct grants do not); group delete then re-create; apply removes a grant line and the permission goes; enrollment writes the node grant and delivery is refused without it; two replicas converge after a revoke.
- **Migration**: every old construct of section 13; the cross-product case is reported and blocks; verification catches an injected difference; re-run is a no-op; cut-over refused with an old replica or agent.
- **e2e, Deploy Testbed**: grant then revoke takes effect without restart (replaces the `--role api-reader` check); adversarial replay with groups; `cca exec` and `cca logs` still work after the cut-over (P11); upgrade run `--from-release` through migrate, verify, cut over, retire.

## 16. Before and after

| Path | Before | After |
|---|---|---|
| P1 role grant | role facts × scopes | direct grants (migrated) |
| P2 built-in bundle | resolved at each rebuild | explicit grants; a group where several principals share them |
| P3 inline role | hidden role, cross product (B1) | one fact per capability and scope |
| P4 group | users only, expanded per member | any principal kind, resolved at decision time, path kept for explanation |
| P5 bootstrap | in-memory bundle | in-memory grants with `source: bootstrap`, listed |
| P6 policy | grants by itself | restriction only |
| P7 `platform` | implicit cross-tenant audit | removed; grant scope decides |
| P8 wildcard | supported, unused | removed |
| P9 store roles | 10 roles, 2 used | per-principal access lists for the 2 in-process principals |
| P10 node delivery | identity and placement | plus explicit enrollment grant (D2) |
| P11 agent debug API | role-grant key | `cluster.admin` grant key |

Behaviour that cannot be preserved without a decision: B1 cross products, granting policies, the `platform` rule for webhook users, `--role` on the command line, DSL `role` blocks.

## 17. What breaks for users, and what does not

**Breaks (all in the control plane's management surface):**

- `cca role …` commands and `/api/role` routes; scripts using `--role` (`cluster_test.sh`, adversarial replay).
- Configurations with `role`, `grant <role> …` or `policy` blocks (in this repository: `examples/auth.cca`, `examples/reference.cca`; no e2e workload).
- Stored `auth/` facts need the one-time migration.
- Old agents cannot be reached by `cca exec`, `cca logs` after the cut-over (hence the version gate).
- Webhook users relying on a group named `platform`.

**Does not change:** capability names, scope semantics, every `requireCapability` call site, authentication, tokens, secrets and their delivery checks, scheduling, networking, running workloads.

## 18. Documents and flows to update

`design/security-model.md` (roles, groups, built-in roles, platform), `design/dsl-reference.md` (auth blocks; also the non-existent `service.read` examples), `design/cli-reference.md`, `design/runtime-invariant-flows.md` (Flow 11 enrollment, Flow 24 agent debug API, Flow 25 authorization, Flows 27 and 28 delivery; new flow for the migration), `design/threat-model.md`, `design/etcd-schema.md`, `design/graphs/AUTHZ_GRAPH.md` and `AUTHN_GRAPH.md`, `design/multi-tenancy.md` (audit visibility), `design/TESTING.md` (migration runbook), `design/adversarial-testing.md`, `examples/auth.cca`, `examples/reference.cca`, website security pages.

Related open work: M179 (tenant-scoped secrets) task 1.9 changes conditional policies and passes a resource context; it should be re-based on section 8. M219 (CLI/API alignment) adds the route sweep that the new routes must pass.
