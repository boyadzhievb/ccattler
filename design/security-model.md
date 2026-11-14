# Security Model

Five layers: Identity → Authentication → Authorization → Isolation → Audit.

**Zero-trust by default** — being inside the cluster grants nothing. Every component authenticates and is authorized independently. Note: zero-trust requires `--tls` mode; without it, `cca run` uses a demo admin authenticator for local development.

## Transport: mTLS Everywhere

```
CLI ──────── mTLS/OIDC ──────► API
API ──────── mTLS ────────────► Store
Controller ── mTLS ────────────► Store
Node ──────── mTLS ────────────► Store/API
```

No unauthenticated cluster communication.

## Certificate Authority Hierarchy

```
              Offline Root CA
                    │
        ┌───────────┼───────────┐
        ▼           ▼           ▼
  Control-plane   Node CA    Workload CA
      CA            │           │
      │        ┌────┴────┐    services
  ┌───┴───┐   node-1  node-2
API-1  API-2
```

- Root CA is offline (never on a running control-plane machine) — *planned; current implementation uses a single CA*
- Short-lived certificates (1hr) with automatic rotation — *planned; current certs are long-lived from enrollment*
- SPIFFE-style workload identities: `spiffe://cluster/node/node-1`, `spiffe://cluster/controller/scheduler`

## Node Enrollment

```
cca join <cluster> <bootstrap-token>
```

Bootstrap token is short-lived, single-use, scoped to enrollment only. Node generates its own private key, sends CSR, receives signed certificate, bootstrap token is destroyed. Supports hardware identity (TPM, cloud instance identity) for stronger enrollment.

## Human Authentication

Standard identity providers (OIDC, OAuth 2.0, LDAP, SAML):

```
Developer → Identity Provider → OIDC token → CCattler API
```

Token establishes: `subject=alice, groups=[developers, payments]`

### Authenticator Chain

The API server evaluates authenticators in order. The first one that returns
a result wins; if it returns nil/nil, the next authenticator tries.

```
mTLS → Webhook → ExternalOIDC → InternalJWT → LocalToken
```

### OIDC Browser Login (M153)

- `OIDCAuthenticator` validates JWTs with ES256 (`security/oidc.go`)
- `OIDCDiscoveryClient` fetches JWKS from external IdPs (`security/oidc_discovery.go`)
- `cca login <server>` runs OAuth2 PKCE browser flow (`security/oidc_browser_flow.go`)
- Localhost callback server on ephemeral port, CSRF state validation
- Auto-refresh when access token < 5min from expiry
- Works with Google, GitHub, and generic OIDC providers
- Server advertises external OIDC config at `/api/auth/oidc-config`

### Credential Storage & Contexts (M154)

Named server contexts in `~/.ccattler/config.json`. Each context stores server
address, auth method, tokens, TLS paths. Auto-migrates from legacy `credentials.json`.

```bash
cca context list              # show all contexts
cca context use prod          # switch active context
cca context current           # show active context
cca context delete staging    # remove a context
cca login https://prod.example.com   # creates/updates context from server name
```

Token resolution priority: `CCA_TOKEN` env → active context → legacy credentials → local-token file.

### Webhook Token Authentication (M155)

External webhook validates bearer tokens by receiving a POST with the token and
returning the principal identity, groups, and attributes. Fail-closed: any error
from the webhook rejects the request. Responses are cached (default 2 min TTL).

```bash
# Enable webhook authentication on the server:
cca server --authn-webhook-url https://auth.internal/validate \
           --tls --cert server.pem --key server-key.pem --ca ca.pem

# The webhook receives:
POST /validate
Content-Type: application/json
{"token": "bearer-token-value"}

# The webhook responds:
{
  "authenticated": true,
  "principal": "user:alice@example.com",
  "groups": ["developers", "team-platform"],
  "attributes": {"team": "platform", "role": "admin"}
}

# On failure (or authenticated: false), the request is rejected.
# The webhook is placed BEFORE OIDC in the authenticator chain,
# so it gets first crack at non-JWT bearer tokens.
```

Implementation: `security/authn_webhook.go` (`WebhookAuthenticator`)

## Authorization: Two-Layer Model

Authorization uses two layers. The store layer enforces prefix-based access
internally (controllers and agents), while the API layer enforces capability-based
access for users. Both are deny-by-default, fail-closed.

### Layer 1: Store-Layer Prefix RBAC (internal)

Every controller and agent has a `Principal` with read/write/delete/watch
permissions scoped to fact key prefixes. A compromised controller can only
touch its own write domain.

```
Store-layer permissions: read, write, delete, watch
Scoped to key prefixes: observed/instance/, derived/crash-loop/, etc.

Example (from security/rbac.go):
  scheduler:   READ  desired/, observed/   WRITE  derived/placement/
  failure:     READ  observed/instance/    WRITE  derived/instance/, derived/crash-loop/
  node-agent:  READ  desired/node/X/       WRITE  observed/node/X/
```

Implementation: `security/authorized_store.go` wraps the raw store. Every
`Put`/`Delete`/`Scan` checks `RBACAuthorizer.Authorize(principal, permission, key)`
against prefix rules before reaching etcd.

**Status (2026-11-03):** only the API server uses `AuthorizedStore`, as its own
principal `service:api-server` with the `api-server` role; users are not
checked at this layer (Flow 25). Agents and controllers use the raw etcd client,
so this layer is not enforced for them (see threat-model T5, fixed in M181).

### Layer 2: API-Layer Capability Auth (users)

Users authenticate via mTLS or JWT/OIDC and receive capability grants at
hierarchical scopes. Capabilities are NOT K8s-style API verbs — they describe
what the user can do, not which API endpoint they call.

```
Capabilities (from security/capability.go):
  workload.read      workload.create     workload.update     workload.delete
  workload.exec      (run commands in containers via the agent debug proxy)
  secret.metadata.read  secret.read      secret.use          secret.write
  node.read          node.manage
  placement.read     placement.write
  scaling.read       scaling.write
  cluster.admin      (grants all capabilities at all scopes)

Scopes: hierarchical paths
  cluster             — whole cluster
  team/payments       — payment team's resources
  node/node01         — single node
```

Example grants (via auth DSL):
```
auth {
    grant workload.read, workload.update to group developers scope team/web
    grant cluster.admin to user ops-lead scope cluster
    grant secret.use to group backend scope team/api
}
```

Implementation: `security/api_authorizer.go` evaluates
`AuthorizeAPI(principal, capability, scope, resourceContext) → ALLOW | DENY`.

### Design Direction: Unix-Style Groups, Not Hierarchical Roles (2026-11-14)

CCattler follows the Unix authorization model: **groups + direct capabilities**.
Named roles (`api-reader`, `api-writer`, `cluster-admin`) exist only as
convenience aliases for common capability bundles. They are not a hierarchy,
cannot inherit from each other, and are not required.

**Why not roles?** Role hierarchies (as in Active Directory, AWS IAM, or
Kubernetes RBAC with Role/ClusterRole/RoleBinding/ClusterRoleBinding) add
indirection that obscures who can do what. Unix survived decades with
user + group + permission, and that model scales to thousands of users.
The complexity of role hierarchies serves vendors selling identity management
products, not operators running systems.

**The CCattler way:**

```
# Groups define WHO (identity, team membership)
group deployers { member alice; member bob }
group observers { member carol }
group secret-ops { member dave }

# Direct capability grants define WHAT each group can do
auth {
    grant workload.create, workload.update to group deployers scope cluster
    grant workload.read, node.read to group observers scope cluster
    grant secret.read, secret.write to group secret-ops scope cluster
}
```

This is equivalent to three Unix groups with fine-grained capabilities instead
of rwx. No roles needed. `cca role grant group:deployers --capability
workload.create` works at runtime too.

**Builtin role aliases** (`api-reader`, `api-writer`, `cluster-admin`) expand
to fixed capability sets at grant time. They are shortcuts, not a separate
authorization layer. `cca role grant user:alice --role api-reader` is exactly
equivalent to granting `workload.read`, `node.read`, `placement.read`,
`scaling.read`, `secret.metadata.read` individually.

### Runtime Grant Management CLI (M156)

Capabilities can be granted to users and groups at runtime:

```bash
# Direct capability grants (preferred)
cca role grant user:bob --capability workload.read --scope team/payments
cca role grant group:deployers --capability workload.create

# Role aliases (convenience shortcuts)
cca role grant user:alice --role api-reader

# Inspection
cca role list
cca role list user:bob
cca role check user:bob --capability workload.read --scope team/payments

# Revocation
cca role revoke user:alice --role api-reader
```

Grants are persisted as `auth/grant/` and `auth/role/` facts in the store.
The auth controller watches these keys and rebuilds the in-memory authorizer
on each reconciliation cycle — no server restart required.

The built-in `cluster-admin` capability set is auto-bootstrapped to
`user:local-admin` and the server's own mTLS principal at startup, persisted
to the store so grants survive restarts.

Implementation: `api/handler_role.go` (API), `cmd/cca/command_role.go` (CLI)

### Auth Groups and User Deletion (M216)

Auth groups are declared in `.cca` configuration files and compiled into
`auth/group/{name}/member/{user}` facts. The auth controller resolves group
grants: a grant to `group developers` applies to every member listed in the
group.

```
group developers {
    member alice
    member bob
}

auth {
    grant workload.read, workload.create to group developers scope team/web
}
```

**Group memberships are configuration-owned.** Deleting a user (`cca user
delete alice`) removes the user record, all grants under
`auth/grant/user/alice/`, and the inline role `auth/role/inline-user-alice/`
in a single transaction. It does **not** remove `auth/group/*/member/alice`
facts because those belong to the applied configuration, not the user
record. The deleted user's tokens are refused immediately (the token
authenticator checks the user record exists and was created no later than
the token's `iat`).

Re-creating a user with the same name (`cca user create alice`) reattaches
the configured group memberships — the new user inherits whatever rights the
configuration grants to that name. **Do not reuse user names** unless the
intent is to grant the same group rights. To remove a name from a group,
update the `.cca` configuration and re-apply.

Implementation: `api/server.go` (`collectUserDeleteOperations`),
`security/authn_local_user.go` (`LocalUserRecordAuthenticator`),
`lang/compiler.go` (`compileGroupDeclaration`). Covered by
`integration/TestUserLifecycleWithConfigOwnedGroupMembership`.

### ABAC: Attribute-Based Conditions

ABAC adds context-sensitive rules on top of capability grants:

```
policy team-isolation {
    capability workload.update
    when subject.team == resource.team
}

policy production-gate {
    capability workload.create
    when subject.environment != "production"
    OR subject.groups contains "production-deployers"
}
```

Implementation: `security/abac.go` + `security/condition.go`.

**Status (2026-11-03):** since M173 the `AuthController` loads `auth/policy/`
facts into the API authorizer in every API process, and `AuthorizeAPI`
evaluates them after grants. API handlers pass no resource context, so only
conditions on the subject (`subject.team`, `subject.groups`, ...) can pass;
conditions on `resource.*` always fail closed. Policies without conditions are
ignored. The apply policy gate checks compiled facts by capability
(`CapabilityFactAuthorizer`) and does not evaluate ABAC conditions.

Every API operation evaluates: `authorize(principal, capability, scope, context) → ALLOW | DENY`

## Per-Controller Least Privilege

Every controller gets its own identity and minimum permissions:

```
scheduler:          READ nodes, instances, requirements    WRITE placements
network:            READ instances, endpoints              WRITE routing
autoscaler:         READ health, metrics                   WRITE intent/autoscaler
node-agent:         READ desired/node-assignments          WRITE observed/node-X/*
```

A compromised autoscaler cannot modify user configuration. A compromised node cannot set `desired_instances(database) = 0` — it can only write to `observed/`.

## Authorized Store

The fact store is the security boundary — every write is authenticated, authorized, and audited:

```
controllers → Authorized Store (identity + authN + authZ + txn + audit) → state
```

## Secrets

Secrets live in an encrypted store, never in plain DSL config:

```
secret database.password

service database {
    secret database.password
}
```

Envelope encryption with KMS integration (AWS KMS, GCP KMS, Vault, HSM). Secrets scoped by service — the scheduler and network controller never see them. The agent materializes secrets to the host filesystem and bind-mounts them read-only (`-v`) into containers at the declared path (e.g., `/run/secrets/db-password`).

## Workload-to-Workload Network Policy

Identity-based, not IP-based:

```
allow frontend → api:443
allow api → database:5432
deny frontend → database:5432
```

Network controller translates identity policies → iptables/nftables rules.

## Audit Logging

Every security-sensitive operation produces an immutable record:

```
principal:   alice
auth:        OIDC
action:      workload.update
target:      service/web
change:      image nginx:1.27 → nginx:1.28
decision:    ALLOW
policy:      production-deployer
request_id:  8f31...
```

**Current status (M158):** `StoreBackedAuditLog` persists entries under `audit/{sequence}`
in etcd. `AuditLogGCController` rotates entries older than 7 days. `cca audit` CLI
queries and streams entries with filters (--principal, --action, --since, --follow).
Auth method is tracked per entry. Per-resource audit verbosity and external audit
webhook are not yet implemented.

## Bootstrap Problem

Cluster creation generates a one-time bootstrap credential → creates admin identity → bootstrap credential destroyed. No permanent "magic password."

## Cryptographic Trust Summary

```
        Root CA
           │
  ┌────────┼────────┐
  ▼        ▼        ▼
Users    Nodes   Controllers
 OIDC     mTLS      mTLS
  │        │        │
  └────────┼────────┘
           ▼
     Authorization
  ┌────────┴────────┐
  ▼                 ▼
Store-layer      API-layer
prefix RBAC    capability auth
(internal)      (users) + ABAC
  │                 │
  └────────┬────────┘
           ▼
      State changes
           │
           ▼
        Audit
```
