# CCattler Authorization Call Graph (AUTHZ)

What an authenticated principal may do: store-layer RBAC/ABAC, API capabilities
and scopes, the auth controller, network policy and audit.
Authentication is in [AUTHN_GRAPH.md](AUTHN_GRAPH.md); secret encryption and key
providers are in [SECRETS_GRAPH.md](SECRETS_GRAPH.md).

## Store-Layer Authorization

### RBACAuthorizer (security/rbac.go)

#### RBACAuthorizer.Authorize → main decision
```
RBACAuthorizer.Authorize(principal, operation, key)            [:107]
├── mutex.RLock
├── resolveRoleNames(principal)                                [:88]
│   ├── bindings[principal] → exact match roles
│   └── for boundPrincipal with "*" suffix:                    [wildcard]
│       └── if principal hasPrefix wildcardPrefix → add roles
├── for each roleName:
│   └── for each rule in role:
│       └── if key hasPrefix rule.KeyPrefix
│           AND operation in rule.Operations → return nil      [ALLOW]
└── return error "denied"                                      [DENY]
```

#### Role management
```
RBACAuthorizer.AddRole(role)                                   [:66]
RBACAuthorizer.BindRole(binding)                               [:73]
RBACAuthorizer.RemoveBinding(principal)                        [:80]
BuiltinRoles() → []Role                                       [:141]
```

#### Builtin Roles (security/rbac.go:141)

| Role             | Key Prefixes             | Permissions              |
|------------------|--------------------------|--------------------------|
| cluster-admin    | "" (everything)          | read, write, delete, watch |
| node-agent       | observed/instance/, observed/node/, observed/volume/, lease/node/, desired/, effective/, placement/, observed/credential/, derived/instance/, derived/credential/ | mixed |
| scheduler        | placement/, observed/, desired/, effective/ | mixed |
| controller       | desired/, effective/, observed/, derived/, intent/, placement/, endpoint/, network/ | mixed |
| credential-broker| desired/cloud_identity/, desired/service/, desired/credential_broker/, observed/instance/, observed/credential/, derived/credential/, credentials/ | mixed |
| cloud-controller | desired/cloud/, desired/service/, observed/node/, observed/cloud/, observed/instance/, endpoint/, network/node/ | mixed |
| api-reader       | "" (everything)          | read, watch              |
| api-writer       | desired/, intent/user/, observed/metric/, "" | mixed |
| api-server       | read/watch: orchestration roots, auth, leader, lease, policy; write: desired/, effective/, intent/, policy/network/, derived/service/, derived/node/, observed/node/, observed/metric/; write+delete: auth/, observed/instance/, derived/instance/, placement/; nothing on secrets/, credentials/, enrollment/, bootstrap/, audit/ | mixed (bound to `service:api-server`) |
| secret-delivery  | read only: secrets/, desired/service/, desired/connection/, placement/instance/, observed/instance/, observed/node/, enrollment/node/ | read (bound to `service:secret-delivery`, M174) |

### ABACAuthorizer (security/abac.go)

#### ABACAuthorizer.Authorize → attribute-based decision
```
ABACAuthorizer.Authorize(principal, operation, key)            [:78]
├── mutex.RLock
├── principalAttrs = principalAttributes[principal]
├── for each policy:
│   ├── if key NOT hasPrefix policy.TargetKeyPrefix → skip
│   ├── attributesMatch(principalAttrs, policy.RequiredAttributes) [:106]
│   │   └── for each required: ALL must match (AND)
│   └── if operation in AllowedOperations → return nil         [ALLOW]
└── return error "denied"                                      [DENY]
```

### CombinedAuthorizer (security/abac.go:138)
```
CombinedAuthorizer.Authorize(principal, operation, key)        [:138]
├── rbacAuthorizer.Authorize(...) → nil → ALLOW                [RBAC first]
├── abacAuthorizer.Authorize(...) → nil → ALLOW                [ABAC second]
└── both error → DENY (OR semantics: either allows)
```

### AuthorizedStore (security/authorized_store.go)

#### AuthorizedStore wraps every StateStore operation
Principal: `authorizingPrincipal(ctx)` — the service principal when `SetServicePrincipal` was called (the API server uses `service:api-server`, Flow 25), otherwise `PrincipalFromContext(ctx)`. Audit entries then carry the context principal in `on_behalf_of`.

Workload visibility (state, watch, status): `requireWorkloadVisibility` → `resolveWorkloadVisibility` → `APIAuthorizer.GrantedScopes(principal, workload.read)`: `cluster` → all teams; exact `team/<name>` → that team; then `tenant.ScanVisibleToTeams` / `tenant.IsFactVisibleToTeams` / `tenant.FilterServiceNamesByTeams` (api/visibility.go, tenant/scoped_query.go).

Apply authorization: `PolicyGate.evaluateAuthorizationGate` uses `CapabilityFactAuthorizer.Authorize` (security/fact_authorizer.go) → `RequiredCapabilityForFactWrite(key)` → `APIAuthorizer.AuthorizeAPI`; the direct apply path uses `Server.authorizeCompiledFacts` with the same mapping.
```
AuthorizedStore.Get(ctx, key)                                  [:76]
├── PrincipalFromContext(ctx)
├── authorizer.Authorize(principal, PermissionRead, key)
│   ├── error → logDenied(ctx, principal, "get", key)          [:220]
│   └── nil → logAllowed(ctx, principal, "get", key)           [:207]
└── inner.Get(ctx, key)

AuthorizedStore.Put(ctx, key, value)                           [:87]
├── authorizer.Authorize(principal, PermissionWrite, key)
└── inner.Put(ctx, key, value)

AuthorizedStore.Delete(ctx, key)                               [:98]
├── authorizer.Authorize(principal, PermissionDelete, key)
└── inner.Delete(ctx, key)

AuthorizedStore.Scan(ctx, prefix)                              [:109]
├── authorizer.Authorize(principal, PermissionRead, prefix)
└── inner.Scan(ctx, prefix)

AuthorizedStore.Watch(ctx, key, opts)                          [:131]
├── authorizer.Authorize(principal, PermissionWatch, key)
└── inner.Watch(ctx, key, opts)

AuthorizedStore.Transaction(ctx, compares, onSuccess, onFailure) [:143]
├── for each compare: Authorize(PermissionRead, compare.Key)
├── for each onSuccess op: Authorize(Write or Delete)
├── for each onFailure op: Authorize(Write or Delete)
├── logAllowed(ctx, principal, "transaction", "{N} ops")
└── inner.Transaction(ctx, compares, onSuccess, onFailure)
```

#### logAllowed / logDenied
```
logAllowed(ctx, principal, action, target)                     [:207]
├── sanitizeForAudit(principal)                                [:21]
│   ├── replace \n → \\n, \r → \\r, \t → \\t
│   └── regex: /secret/.* → /secret/[REDACTED]
└── auditLog.Log(AuditEntry{Decision: "allow"})

logDenied(ctx, principal, action, target)                      [:220]
└── auditLog.Log(AuditEntry{Decision: "deny"})
```

---

## API-Layer Authorization

### APIAuthorizer (security/api_authorizer.go)

#### APIAuthorizer.AuthorizeAPI → main API decision
```
APIAuthorizer.AuthorizeAPI(principal, capability, scope, resourceContext) [:96]
├── principal.Name == "" → error "incomplete principal"
├── mutex.RLock
├── resolveGrants(principalKey)                                [:151]
│   ├── grants[principal] → direct grants
│   └── for boundPrincipal with '*' suffix:                    [wildcard]
│       └── if principal hasPrefix wildcardPrefix → add grants
├── for each grant:
│   ├── if grant.Capability == CapabilityClusterAdmin
│   │   AND grant.Scope.Contains(requiredScope) → ALLOW
│   └── if grant.Capability == requiredCapability
│       AND grant.Scope.Contains(requiredScope) → ALLOW
├── for each conditionalPolicy:                                [ABAC policies]
│   ├── if policy.Capability matches capability
│   ├── EvaluateConditions(conditions, principal, resourceContext) [security/condition.go:48]
│   │   └── evaluateSingleCondition for each (AND semantics)   [:59]
│   │       ├── resolveField(field, principal, resourceContext) [:142]
│   │       │   ├── "subject.name" → principal.Name            [:164]
│   │       │   ├── "subject.groups" → principal.Groups        [multi-value]
│   │       │   ├── "subject.<key>" → principal.Attributes     [ABAC]
│   │       │   ├── "resource.team" → resourceContext.Team     [:187]
│   │       │   └── "resource.name" → resourceContext.Name
│   │       ├── isFieldReference(value)                        [:214]
│   │       └── operator dispatch: ==, !=, in, not_in
│   ├── true → ALLOW
│   └── false → logConditionFailure(...)                       [:136]
│       └── auditLog.Log({Decision: "deny", Policy: "name:condition_failed"})
└── return error "denied"                                      [DENY]
```

#### APIAuthorizer state management
```
APIAuthorizer.ReplaceGrants(newGrants)                         [atomic swap of store-derived grants]
APIAuthorizer.ReplacePolicies(newPolicies)                     [atomic swap]
APIAuthorizer.GrantRole(principal, roleName)                   [bootstrap grant: kept across ReplaceGrants]
└── BuiltinRoleCapabilities(roleName)
    ├── "cluster-admin" → [{cluster.admin, cluster}]
    ├── "api-reader" → [workload.read, node.read, placement.read, scaling.read, secret.metadata.read]
    ├── "api-writer" → [workload.read/create/update, scaling.read/write, ...]
    └── "node-agent" → [workload.read, node.read, placement.read]
```

### Scope (security/scope.go)

```
Scope.Contains(child)                                          [:42]
├── scope == ScopeCluster → true                               [root contains everything]
├── scope == child → true                                      [self-match]
└── child hasPrefix scope+"/" → true                           [hierarchical containment]

TeamScope(teamName) → "team/" + teamName                       [:26]
NodeScope(nodeID) → "node/" + nodeID                           [:30]
ServiceScope(serviceName) → "service/" + serviceName           [:35]
```

### Capabilities (security/capability.go)

| Capability           | Domain                                    |
|----------------------|-------------------------------------------|
| workload.read        | Read services, instances, placements      |
| workload.create      | Create services via apply                 |
| workload.update      | Modify services (image, config)           |
| workload.delete      | Remove services                           |
| workload.exec        | Run commands in containers (debug proxy)  |
| secret.metadata.read | List secret names and grants              |
| secret.read          | Retrieve plaintext secret values          |
| secret.use           | Mount secrets into workloads              |
| secret.write         | Create, update, delete secrets            |
| node.read            | Read node state                           |
| node.manage          | Drain, cordon, label nodes                |
| placement.read       | Read placement decisions                  |
| placement.write      | Manual scheduling overrides               |
| scaling.read         | Read autoscaler state                     |
| scaling.write        | Change instance counts / policies         |
| cluster.admin        | Superuser — grants all capabilities       |

---

## API Route Authorization (M219)

Every route registered on `api.Server` (api/server.go). The CLI reaches cluster state only through these routes;
the root key commands are the one local exception (Flow 30). "visibility" = `requireWorkloadVisibility`:
`workload.read` at `cluster` sees every team, at `team/<name>` only that team. Public routes are the reviewed
allowlist of `api/TestEveryRouteRefusesPrincipalWithoutGrants`. Built from the handlers on 2026-11-14.

| Route | Methods | Capability | Scope | CLI command / caller |
|---|---|---|---|---|
| `/api/enroll` | POST | none (one-time join token) | — | `cca join` |
| `/.well-known/openid-configuration` | GET | none (public) | — | OIDC clients |
| `/oidc/jwks` | GET | none (public) | — | token verifiers |
| `/api/state` | GET | `workload.read` (visibility) | cluster or team | `cca exec`, `cca images`, `cca get instance` lookups |
| `/api/apply` | POST | `workload.create` + per-fact `RequiredCapabilityForFactWrite` | cluster | none yet (M219 4.2: `cca apply`) |
| `/api/watch` | GET (stream) | `workload.read` (visibility) | cluster or team | `cca watch` |
| `/api/scale` | POST | `scaling.write` | cluster | `cca scale` |
| `/api/restart` | POST | `workload.update` | cluster | `cca restart` |
| `/api/rollout/restart` | POST | `workload.update` | cluster | `cca rollout restart` |
| `/api/status` | GET | `workload.read` (visibility) | cluster or team | `cca get`, `cca top` (M219 3.2: `cca status`) |
| `/api/logs` | GET | `workload.read` | cluster | `cca events` |
| `/api/describe` | GET | `workload.read` | cluster | `cca describe` |
| `/api/events/stream` | GET (stream) | `workload.read` | cluster | `cca events --follow` |
| `/api/diff` | POST | `workload.read` | cluster | `cca diff` (remote path) |
| `/api/metric` | POST | `workload.update` | cluster | `cca metric set` |
| `/api/activate` | POST | `workload.update` | cluster | none (no caller in the code) |
| `/api/secret/rotate` | POST | `secret.write` | cluster | `cca secret rotate` |
| `/api/secret/migrate-keys` | POST | `cluster.admin` | cluster | `cca secret migrate-keys` |
| `/api/secret/retire-legacy-key` | POST | `cluster.admin` | cluster | `cca secret retire-legacy-key` |
| `/api/secret` | GET, POST, DELETE | GET: `secret.metadata.read` (list) / `secret.read` (value); POST, DELETE: `secret.write` | cluster | `cca secret list/get/set/delete` |
| `/api/node/drain` | POST | `node.manage` | cluster | `cca drain` |
| `/api/node/disable` | POST | `node.manage` | cluster | `cca disable-node` |
| `/api/node/enable` | POST | `node.manage` | cluster | `cca enable-node` |
| `/api/node/secrets` | POST | mTLS `node:` principal; placement and grants checked (Flow 27) | node | agent `secret_delivery_client` |
| `/api/node/gate-keys` | POST | mTLS `node:` principal reporting `gate_proxy = enabled` (Flow 28) | node | agent `gate_key_syncer` |
| `/api/gate-key` | PUT | `secret.write` | cluster | `cca apply` inline gate key (`gate_key_writer`) |
| `/api/node/join-token` | POST, GET, DELETE | `cluster.admin`; audited `join-token.{create,list,revoke}` with the 8-char id, never the value | cluster | `cca token create/list/revoke` |
| `/api/node/debug/` | per endpoint | `node.read` (containers, images, stats), `workload.read` (logs), `workload.exec` (exec), `node.manage` (images/pull) | cluster | `cca node-inspect`, `cca logs`, `cca exec`, `cca images` |
| `/api/user` | GET, POST, DELETE | `cluster.admin` | cluster | `cca user list/create/delete` |
| `/api/user/token` | POST | `cluster.admin` | cluster | `cca user token` |
| `/api/role` | GET, POST, DELETE | `cluster.admin` | cluster | `cca role list/grant/revoke` |
| `/api/role/check` | POST | `cluster.admin` | cluster | `cca role check` |
| `/api/gc` | POST | `cluster.admin` | cluster | `cca gc instances` |
| `/api/audit` | GET (+ follow stream) | `workload.read`; tenant filter, teamless non-admin 403 | cluster | `cca audit` |
| `/api/auth/whoami` | GET | none (any authenticated caller) | — | `cca login` |
| `/api/auth/oidc-config` | GET | none (public) | — | `cca login` |
| `/healthz` | any | none (public) | — | `cca health`, load balancers |
| `/metrics` | any | none (open M182 item) | — | Prometheus |

CLI paths that do not use these routes: none (M219). `cca token` uses `/api/node/join-token` (5.2). `cca status`
moved to `/api/status` (3.2); `cca apply` / `cca diff` use `/api/apply` and `/api/diff` only (4.2),
except `--simulate`, which never leaves the process.

---

## Auth Controller (controllers/auth.go)

### AuthController.Reconcile → rebuild APIAuthorizer from facts
```
AuthController.Reconcile(ctx, facts)                           [:49]
├── parseRoleCapabilitiesFromFacts(facts)                      [:65]
│   └── auth/role/{name}/capability/{cap} → map[roleName][]Capability
├── parseRoleScopesFromFacts(facts)                            [:80]
│   └── auth/role/{name}/scope/{path} → map[roleName][]Scope
├── parseGroupMembersFromFacts(facts)                          [:95]
│   └── auth/group/{name}/member/{id} → map[groupName][]string
├── parsePrincipalRolesFromFacts(facts, groupMembers)          [:112]
│   └── auth/grant/{kind}/{name}/role/{role} → map[principal][]roleName
│       └── kind=="group" → expand each member as "user:{member}"
├── buildCapabilityGrants(principalRoles, roleCapabilities, roleScopes) [:140]
│   └── for principal → role → capability × scope → CapabilityGrant
│       ├── no scope → default to ScopeCluster
│       └── no capability facts → security.BuiltinRoleCapabilities(role)
├── apiAuthorizer.ReplaceGrants(newGrants)                     [atomic swap]
├── parseConditionalPoliciesFromFacts(facts)                   [:192]   [policies without conditions skipped, fail closed]
│   ├── collectPolicyConditionComponentsFromFacts(facts)       [:218]
│   │   └── auth/policy/{name}/capability → capability string
│   │       auth/policy/{name}/condition/{i}/field → field
│   │       auth/policy/{name}/condition/{i}/operator → operator
│   │       auth/policy/{name}/condition/{i}/value → value
│   └── assembleConditionsForPolicy(policyName, components)    [:279]
└── apiAuthorizer.ReplacePolicies(conditionalPolicies)         [atomic swap]

Watch prefixes: auth/role/, auth/grant/, auth/group/, auth/policy/
Returns: nil changes (side effect only — rebuilds in-memory authorizer)
```

---


## Network Policy (security/network_policy.go)

```
NetworkPolicyEngine.Evaluate(ctx, source, target, port)        [:83]
├── ListRules(ctx)                                             [:62]
│   └── factStore.Scan("policy/network/")
│       └── parseNetworkPolicyRule(name, value)                [:119]
│           └── split "source:target:port:action"
├── for each deny rule: matchesRule → DENY                     [:105]
├── for each allow rule: matchesRule → ALLOW
└── default → DENY

NetworkPolicyEngine.DetectPolicyCycles(ctx)                    [security/policy_cycle_detection.go:18]
├── ListRules(ctx) → filter allow rules (skip wildcards)
├── build adjacency list (source → target)
└── DFS with three-color marking:                              [:53]
    ├── colorWhite → unvisited
    ├── colorGray → in current path (back edge = cycle)
    ├── colorBlack → fully explored
    └── extractCyclePath(parentPath, end, start)               [:83]
```

---

## Audit Logging (security/audit.go)

### StoreBackedAuditLog
```
NewStoreBackedAuditLog(stateStore, maxEntries)                 [:112]
└── resumeSequenceFromStore()                                  [:164]
    └── scan audit/ → find highest sequence → sequenceNum.Store

StoreBackedAuditLog.Log(entry)                                 [:124]
├── entry.Timestamp = time.Now()
├── mutex.Lock → ring buffer append (shift if full)
└── go persistEntry(entry)                                     [:149, async goroutine]
    ├── sequenceNum.Add(1) → monotonic
    ├── key = "audit/{020d sequence}"
    └── stateStore.Put(key, JSON(entry))

StoreBackedAuditLog.Entries()                                  [:139]
└── mutex.Lock → copy entries slice
```

### InMemoryAuditLog
```
NewInMemoryAuditLog(maxEntries)                                [:61]
InMemoryAuditLog.Log(entry)                                    [:69]
└── mutex.Lock → ring buffer append (shift if full)
InMemoryAuditLog.Entries()                                     [:83]
```

---

## Store Key Patterns

### Reads (security layer)
| Key Pattern | Where Read |
|---|---|
| `policy/network/{name}` | NetworkPolicyEngine.ListRules |
| `audit/{sequence}` | resumeSequenceFromStore |

### Writes (security layer)
| Key Pattern | Where Written |
|---|---|
| `policy/network/{name}` | NetworkPolicyEngine.AddRule |
| `audit/{sequence}` | StoreBackedAuditLog.persistEntry |

### Reads (auth controller)
| Key Pattern | Where Read |
|---|---|
| `auth/role/{name}/capability/{cap}` | parseRoleCapabilitiesFromFacts |
| `auth/role/{name}/scope/{path}` | parseRoleScopesFromFacts |
| `auth/group/{name}/member/{id}` | parseGroupMembersFromFacts |
| `auth/grant/{kind}/{name}/role/{role}` | parsePrincipalRolesFromFacts |
| `auth/policy/{name}/capability` | collectPolicyConditionComponentsFromFacts |
| `auth/policy/{name}/condition/{i}/*` | collectPolicyConditionComponentsFromFacts |

---

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `RBACAuthorizer.Authorize` | AuthorizedStore (all ops), CombinedAuthorizer |
| `ABACAuthorizer.Authorize` | CombinedAuthorizer |
| `resolveRoleNames` | RBACAuthorizer.Authorize |
| `attributesMatch` | ABACAuthorizer.Authorize |
| `CombinedAuthorizer.Authorize` | AuthorizedStore (when combined auth) |
| `APIAuthorizer.AuthorizeAPI` | API handlers (cca get, apply, scale, etc.) |
| `resolveGrants` | APIAuthorizer.AuthorizeAPI |
| `BuiltinRoleCapabilities` | APIAuthorizer.GrantRole, buildCapabilityGrants |
| `EvaluateConditions` | APIAuthorizer.AuthorizeAPI (ABAC policies) |
| `evaluateSingleCondition` | EvaluateConditions |
| `resolveField` | evaluateSingleCondition |
| `resolveSubjectField` | resolveField (objectName=="subject") |
| `resolveResourceField` | resolveField (objectName=="resource") |
| `AuthController.Reconcile` | Runner started by `startAPIAuthorizationSync` in every API process (cmd/cca/command_server.go, M173) |
| `parseRoleCapabilitiesFromFacts` | AuthController.Reconcile |
| `parseRoleScopesFromFacts` | AuthController.Reconcile |
| `parseGroupMembersFromFacts` | AuthController.Reconcile |
| `parsePrincipalRolesFromFacts` | AuthController.Reconcile |
| `buildCapabilityGrants` | AuthController.Reconcile |
| `parseConditionalPoliciesFromFacts` | AuthController.Reconcile |
| `collectPolicyConditionComponentsFromFacts` | parseConditionalPoliciesFromFacts |
| `assembleConditionsForPolicy` | parseConditionalPoliciesFromFacts |
| `logAllowed` | AuthorizedStore.Get/Put/Delete/Scan/Watch/Transaction |
| `logDenied` | AuthorizedStore.Get/Put/Delete/Scan/Watch/Transaction |
| `sanitizeForAudit` | logAllowed, logDenied |
| `StoreBackedAuditLog.Log` | logAllowed, logDenied, APIAuthorizer.logConditionFailure |
| `persistEntry` | StoreBackedAuditLog.Log (goroutine) |
| `resumeSequenceFromStore` | NewStoreBackedAuditLog |
| `NetworkPolicyEngine.Evaluate` | data plane nftables rules |
| `DetectPolicyCycles` | lint / validation |
| `matchesRule` | Evaluate (deny loop, allow loop) |
| `FetchAndCacheKeys` | startup, PublicKeyByID (on stale) |

---

## File Index

| File | Purpose |
|---|---|
| security/capability.go:12 | Capability constants (workload.read, etc.) |
| security/scope.go:17 | Hierarchical scope type + Contains |
| security/rbac.go:107 | RBACAuthorizer.Authorize — key-prefix RBAC |
| security/abac.go:78 | ABACAuthorizer.Authorize — attribute-based ABAC |
| security/abac.go:138 | CombinedAuthorizer — RBAC OR ABAC |
| security/api_authorizer.go:96 | APIAuthorizer.AuthorizeAPI — capability + scope + ABAC |
| security/condition.go:48 | EvaluateConditions — ABAC condition engine |
| security/authorizer.go:9 | Authorizer interface |
| security/authorized_store.go:67 | AuthorizedStore — per-op authorization wrapper |
| security/audit.go:112 | StoreBackedAuditLog — persistent audit entries |
| security/network_policy.go:83 | NetworkPolicyEngine.Evaluate |
| security/policy_cycle_detection.go:18 | DetectPolicyCycles — DFS cycle detection |
| controllers/auth.go:49 | AuthController.Reconcile — fact → APIAuthorizer rebuild |
