# CCattler Authentication & Authorization Graph

Machine-readable reference for the security subsystem. Purpose: trace how
identities, credentials, and authorization decisions flow through the system
without reading security/ source files.

## Authentication Pipeline

### Authenticator Chain Order

```
Request
  │
  ├─1→ MTLSAuthenticator        (security/authn_mtls.go)
  │      TLS peer cert → "node:<CN>"
  │      Returns nil if no TLS / no peer cert → fall through
  │
  ├─2→ BearerTokenAuthenticator  (security/authn_bearer.go)
  │      Authorization: Bearer <JWT> → delegates to OIDCAuthenticator
  │      Skips non-JWT tokens (no dots) → fall through
  │      Hard error on invalid JWT → short-circuit deny
  │
  ├─3→ LocalTokenAuthenticator   (security/authn_local_token.go)
  │      Authorization: Bearer <hex-token> → "user:local-admin"
  │      Constant-time comparison, 64-char hex token
  │      Returns nil if no Authorization header → fall through
  │
  ├─4→ WebhookAuthenticator      (security/authn_webhook.go)
  │      Authorization: Bearer <any> → POST to external endpoint
  │      Response cached for cacheTTL (default 2min)
  │      Fail-closed: any error → deny
  │
  └─ No match → "no authenticator accepted the request"
```

### Chain Semantics (security/authenticator.go)

```
AuthenticatorChain.Authenticate(request):
  for each authenticator:
    result, error = authenticator.Authenticate(request)
    if error != nil → return nil, error       (hard error = short-circuit)
    if result != nil → return result, nil     (first match wins)
  return nil, "no authenticator accepted"     (all returned nil)
```

### AuthenticationResult (security/oidc.go:62)

| Field         | Type          | Description                                     |
|---------------|---------------|-------------------------------------------------|
| Principal     | string        | Canonical "kind:name" (e.g. "user:alice")       |
| PrincipalKind | PrincipalKind | user, node, service, system                     |
| Groups        | []string      | Group memberships from IdP                      |
| Attributes    | []Attribute   | Key-value pairs for ABAC (team, role)           |
| ExpiresAt     | time.Time     | Token/cert expiry                               |
| AuthMethod    | string        | "mtls", "oidc", "local-token", "webhook"        |

---

## Principal Types (security/principal.go)

```
PrincipalKind = "user"    → human via OIDC, local-token, or webhook
PrincipalKind = "node"    → node agent via mTLS certificate (CN = node ID)
PrincipalKind = "service" → workload with SPIFFE identity
PrincipalKind = "system"  → internal controller/scheduler component
```

### Principal Struct

```go
type Principal struct {
    Kind       PrincipalKind   // user | node | service | system
    Name       string          // unique within kind
    Groups     []string        // from identity provider
    Attributes []Attribute     // key-value for ABAC
}
```

### Canonical String Form

```
"kind:name"  →  "user:alice@example.com"
                "node:node-1"
                "service:web-frontend"
                "system:scheduler"
```

### Context Propagation

```
HTTP handler
  │
  ├→ WithPrincipal(ctx, "user:alice")           ← string form (store layer)
  ├→ WithPrincipalStruct(ctx, Principal{...})   ← struct form (API layer)
  └→ WithAuthMethod(ctx, "oidc")                ← for audit log entries
```

---

## Two-Layer Authorization Model

```
┌──────────────────────────────────────────────────────────────────┐
│                         API Layer                                │
│  APIAuthorizer (security/api_authorizer.go)                      │
│  Capability-based: "workload.create" at "team/payments" scope    │
│  Checked by: API handlers (cca get, cca apply, cca scale, etc.) │
│  Principals: typed Principal struct                              │
└──────────────────┬───────────────────────────────────────────────┘
                   │ (user requests hit API layer)
                   │
┌──────────────────┴───────────────────────────────────────────────┐
│                        Store Layer                               │
│  RBACAuthorizer + ABACAuthorizer (security/rbac.go, abac.go)     │
│  Key-prefix permissions: read/write/delete/watch on "desired/"   │
│  Checked by: AuthorizedStore (security/authorized_store.go)      │
│  Principals: string "kind:name" form                             │
└──────────────────────────────────────────────────────────────────┘
```

### Store-Layer RBAC (security/rbac.go)

#### Permissions

| Permission | Value    | Operations           |
|------------|----------|----------------------|
| Read       | "read"   | Get, Scan            |
| Write      | "write"  | Put, PutWithLease    |
| Delete     | "delete" | Delete               |
| Watch      | "watch"  | Watch                |

#### Rule: KeyPrefix × []Permission

```go
type Rule struct {
    KeyPrefix  string        // fact store key prefix
    Operations []Permission  // operations allowed on matching keys
}
```

#### Resolution Order

```
RBACAuthorizer.Authorize(principal, operation, key):
  1. Collect role names:
     a. Exact match:   bindings["user:alice"]
     b. Wildcard match: bindings["user:*"] matches "user:alice"
  2. For each role name → look up Role → iterate Rules:
     if key hasPrefix rule.KeyPrefix AND operation in rule.Operations → ALLOW
  3. No match → DENY
```

#### Builtin Roles (security/rbac.go:141)

| Role               | Key Prefixes                                                  | Permissions              |
|--------------------|---------------------------------------------------------------|--------------------------|
| cluster-admin      | "" (everything)                                               | read, write, delete, watch |
| node-agent         | observed/instance/, observed/node/, observed/volume/, lease/node/, desired/, effective/, placement/, credentials/, observed/credential/, derived/instance/, derived/credential/ | mixed (see source) |
| scheduler          | placement/, observed/, desired/, effective/                    | mixed                    |
| controller         | desired/, effective/, observed/, derived/, intent/, placement/, endpoint/, network/ | mixed |
| credential-broker  | desired/cloud_identity/, desired/service/, desired/credential_broker/, observed/instance/, observed/credential/, derived/credential/, credentials/ | mixed |
| cloud-controller   | desired/cloud/, desired/service/, observed/node/, observed/cloud/, observed/instance/, endpoint/, network/node/ | mixed |
| api-reader         | "" (everything)                                               | read, watch              |
| api-writer         | desired/, intent/user/, observed/metric/, "" (read/watch)     | mixed                    |

### Store-Layer ABAC (security/abac.go)

```
ABACPolicy {
    Name               string
    RequiredAttributes []Attribute    // ALL must match (AND)
    TargetKeyPrefix    string
    AllowedOperations  []Permission
}

ABACAuthorizer.Authorize(principal, operation, key):
  For each policy:
    if key hasPrefix policy.TargetKeyPrefix
       AND principal's attributes match ALL required attributes
       AND operation in AllowedOperations → ALLOW
  No match → DENY
```

### Combined Authorizer (security/abac.go:124)

```
CombinedAuthorizer.Authorize(principal, operation, key):
  if RBAC allows → ALLOW
  if ABAC allows → ALLOW
  → DENY (both denied)
```

### AuthorizedStore (security/authorized_store.go)

Wraps every StateStore operation with authorization:

```
Get(ctx, key)          → Authorize(principal, PermissionRead,   key)
Put(ctx, key, value)   → Authorize(principal, PermissionWrite,  key)
Delete(ctx, key)       → Authorize(principal, PermissionDelete, key)
Scan(ctx, prefix)      → Authorize(principal, PermissionRead,   prefix)
Watch(ctx, key, opts)  → Authorize(principal, PermissionWatch,  key)
Transaction(ctx, ...)  → Authorize each compare key (read) + each op key (write/delete)
```

Every operation produces an audit log entry (allow or deny) with sanitized fields.

### API-Layer Authorization (security/api_authorizer.go)

#### Capabilities (security/capability.go)

| Capability           | Domain           |
|----------------------|------------------|
| workload.read        | Read services, instances, placements |
| workload.create      | Create services via apply |
| workload.update      | Modify services (image, config) |
| workload.delete      | Remove services |
| secret.metadata.read | List secret names and grants |
| secret.read          | Retrieve plaintext secret values |
| secret.use           | Mount secrets into workloads |
| secret.write         | Create, update, delete secrets |
| node.read            | Read node state |
| node.manage          | Drain, cordon, label nodes |
| placement.read       | Read placement decisions |
| placement.write      | Manual scheduling overrides |
| scaling.read         | Read autoscaler state |
| scaling.write        | Change instance counts / policies |
| cluster.admin        | Superuser — grants all capabilities |

#### Scopes (security/scope.go)

```
Scope hierarchy:
  "cluster"                ← root, contains everything
    "team/payments"        ← team subtree
      "team/payments/db"   ← child of payments
    "node/node-01"         ← specific node
    "service/web"          ← specific service

scope.Contains(child):
  cluster contains everything
  scope == child → yes
  child hasPrefix scope+"/" → yes
```

#### APIAuthorizer Resolution (security/api_authorizer.go:96)

```
AuthorizeAPI(principal, capability, scope, resourceContext):
  1. Collect grants:
     a. Direct: grants["user:alice"]
     b. Wildcard: grants["user:*"] matches "user:alice"
  2. For each grant:
     if grant.Capability == cluster.admin AND grant.Scope.Contains(scope) → ALLOW
     if grant.Capability == capability AND grant.Scope.Contains(scope) → ALLOW
  3. Evaluate conditional ABAC policies:
     for each policy where policy.Capability matches:
       if EvaluateConditions(policy.Conditions, principal, resourceContext) → ALLOW
       else → audit log "condition_failed"
  4. No match → DENY
```

#### Builtin Role → Capability Mapping (security/api_authorizer.go:169)

| Role          | Capabilities                                                             | Scope   |
|---------------|--------------------------------------------------------------------------|---------|
| cluster-admin | cluster.admin                                                            | cluster |
| api-reader    | workload.read, node.read, placement.read, scaling.read, secret.metadata.read | cluster |
| api-writer    | workload.read/create/update, scaling.read/write, node.read, placement.read, secret.metadata.read | cluster |
| node-agent    | workload.read, node.read, placement.read                                | cluster |

---

## Token Lifecycle

### Bootstrap Token (security/bootstrap.go)

```
                   cca init
                      │
                      ▼
  GenerateBootstrapToken(store, TTL)
  ├─ Check bootstrap/used ≠ "true"
  ├─ Generate 32 random bytes → hex
  ├─ Store: bootstrap/token = hex
  │         bootstrap/expiry = RFC3339
  └─ Return token to admin

  ValidateBootstrapToken(store, token)
  ├─ Check bootstrap/used ≠ "true"
  ├─ Constant-time compare with stored token
  ├─ Check not expired
  ├─ Mark: bootstrap/used = "true"
  ├─ Delete: bootstrap/token, bootstrap/expiry
  └─ Return principal "admin"
```

One-time use. Destroyed after first validation.

### Local Token (security/authn_local_token.go)

```
Server startup:
  NewLocalTokenAuthenticator(tokenDirectory)
  ├─ Generate 32 random bytes → 64-char hex
  ├─ Write to /etc/ccattler/local-token (mode 0600)
  └─ Return authenticator

CLI authentication:
  ReadLocalToken() → reads /etc/ccattler/local-token
  Sends as: Authorization: Bearer <hex-token>
  Authenticates as: "user:local-admin" with group "platform"
  Expiry: 24h (re-authenticates every request)
```

### User Token (security/user_token.go)

```
UserTokenIssuer.MintUserToken(username, team, groups, TTL)
  ├─ JWT with claims: iss, sub=username, aud="ccattler", iat, exp
  │   + optional: team, groups
  ├─ Signed with ES256 (same key as WorkloadTokenIssuer)
  ├─ Default TTL: 8 hours
  └─ Validated by OIDCAuthenticator → "user:<sub>"
```

### Workload Token (security/workload_token.go)

```
WorkloadTokenIssuer.MintWorkloadToken(spiffeID, audience, TTL)
  ├─ JWT with claims: iss, sub=spiffeID, aud, iat, exp
  ├─ Signed with ES256 (ECDSA P-256)
  ├─ kid in header for key selection
  └─ Serves OIDC discovery + JWKS endpoints for cloud IdP trust

OIDC Discovery:
  GET /.well-known/openid-configuration → OIDCDiscoveryResponse
  GET /oidc/jwks → JWKSResponse (EC P-256 public key)
```

### Join Token / Node Enrollment (security/enrollment.go)

```
Admin:
  GenerateJoinToken(store, nodeID, TTL)
  ├─ Generate 32 random bytes → hex
  ├─ Hash(token) = SHA256 → key suffix
  ├─ Store: enrollment/token/<hash> = {prefix, nodeID, expiry, caFingerprint}
  │         Only first 8 chars stored (display prefix)
  └─ Return full token + CA fingerprint as composite string

Node:
  cca join <server> <token.fingerprint>
  ├─ Generate CSR with node's private key
  └─ POST EnrollmentRequest{Token, NodeID, IPAddresses, CSRPEM}

Server:
  EnrollNode(request)
  ├─ Validate token (not expired, node ID matches)
  ├─ Sign CSR with cluster CA
  ├─ Bind "node-agent" RBAC role to "node:<nodeID>"
  ├─ Record: enrollment/node/<nodeID> = {principal, enrolledAt, certExpiry}
  ├─ Delete join token (one-time use)
  └─ Return: CertificatePEM, CACertPEM, Principal
```

---

## mTLS & Certificate Authority (security/ca.go)

### CA Hierarchy

```
CCattler Root CA (self-signed, ECDSA P-256)
├── Organization: "CCattler"
├── CommonName: "CCattler Root CA"
├── Validity: configurable (default long-lived)
├── MaxPathLen: 1
│
├── Server certificate (leaf)
│   ├── CN: server identity
│   ├── SANs: DNS names + IPs
│   ├── ExtKeyUsage: ServerAuth, ClientAuth
│   └── Validity: per request TTL
│
├── Node certificates (leaf, issued on enrollment)
│   ├── CN: node-<id>
│   ├── SANs: <nodeID>, <nodeID>.cca.local, node IPs
│   ├── ExtKeyUsage: ServerAuth, ClientAuth
│   └── Validity: certificateTTL from EnrollmentService
│
└── Controller certificates (leaf)
    ├── CN: controller identity
    └── ExtKeyUsage: ServerAuth, ClientAuth
```

### Certificate Issuance Flow

```
IssueCertificate(request):
  ├─ Generate ECDSA P-256 leaf key pair
  ├─ Create x509 template with CN, SANs, TTL
  ├─ Sign with CA private key
  └─ Return PEM cert + PEM key + NotAfter

SignCSR(signRequest):
  ├─ Verify CSR signature
  ├─ Merge SANs from CSR + server-side
  ├─ Sign with CA private key using CSR's public key
  └─ Return PEM cert (no private key — stays on node)
```

### TLS Configuration

```
Server: RequireAndVerifyClientCert, MinVersion TLS 1.3
Client: present client cert, verify server against CA
```

### Certificate Rotation (security/rotator.go)

```
CertificateRotator:
  ├─ renewalThreshold: fraction of lifetime (e.g. 0.7)
  ├─ Background goroutine:
  │    sleep until (TTL × threshold) elapsed
  │    call CA.IssueCertificate(same request)
  │    atomic swap currentCertificate + currentTLSCert
  ├─ minimumCertRenewalSleep: 10s (floor)
  └─ GetCertificate/GetClientCertificate: tls.Config callbacks

Full chain PEM: leaf cert + CA cert in the TLS handshake
  → clients can verify via CA fingerprint without --ca-cert
```

---

## OIDC Integration

### External IdP Authentication (security/oidc.go)

```
OIDCAuthenticator.Authenticate(tokenString):
  1. parseAndVerifyJWTSignature:
     ├─ Split into 3 parts (header.payload.signature)
     ├─ Decode header → verify alg == "ES256"
     ├─ ECDSA signature verification with configured public key
     └─ Return raw claims JSON

  2. extractAndValidateJWTClaims:
     ├─ Verify iss == config.Issuer
     ├─ Verify aud == config.Audience (if set)
     ├─ Check exp > now (default: +1h if no exp claim)
     └─ Return claims map + expiresAt

  3. constructAuthenticationResultFromClaims:
     ├─ principal = "user:" + claims[PrincipalClaim]  (default: "sub")
     ├─ attributes: team = claims[TeamClaim], role = claims[RoleClaim]
     └─ Return AuthenticationResult
```

### OIDC Discovery (security/oidc_discovery.go)

```
OIDCDiscoveryClient:
  ├─ FetchAndCacheKeys()
  │   GET {issuerURL}/.well-known/openid-configuration → jwks_uri
  │   GET {jwks_uri} → parse EC P-256 keys into cache (kid → *ecdsa.PublicKey)
  ├─ PublicKeyByID(kid) → cached key (auto-refresh if stale, TTL: 1h)
  └─ FirstPublicKey() → first cached key (single-key IdPs)
```

### Browser Flow (security/oidc_browser_flow.go)

```
ExecuteOIDCBrowserFlow(config, openBrowserFunc):
  1. Generate PKCE code pair (32-byte verifier → S256 challenge)
  2. Listen on 127.0.0.1:<random-port>/callback
  3. Generate random state token (16 bytes)
  4. Build authorize URL:
     response_type=code, client_id, redirect_uri, scope,
     state, code_challenge, code_challenge_method=S256
  5. Open browser → user authenticates at IdP
  6. /callback handler:
     ├─ Verify state matches
     ├─ Extract authorization code
     └─ Send to channel
  7. Exchange code for tokens:
     POST tokenURL with grant_type=authorization_code,
     code, redirect_uri, client_id, code_verifier
  8. Return: AccessToken, IDToken, RefreshToken, ExpiresIn

  Timeout: 5 minutes

RefreshOIDCToken(tokenURL, clientID, refreshToken):
  POST tokenURL with grant_type=refresh_token
```

---

## Auth Controller (controllers/auth.go)

### Watch Prefixes

```
auth/role/        ← role definitions (capabilities, scopes)
auth/grant/       ← principal → role bindings
auth/group/       ← group → member mappings
auth/policy/      ← conditional ABAC policies
```

### Reconciliation Flow

```
AuthController.Reconcile(facts):
  1. parseRoleCapabilitiesFromFacts
     auth/role/{name}/capability/{cap} → map[roleName][]Capability

  2. parseRoleScopesFromFacts
     auth/role/{name}/scope/{path} → map[roleName][]Scope

  3. parseGroupMembersFromFacts
     auth/group/{name}/member/{id} → map[groupName][]string

  4. parsePrincipalRolesFromFacts
     auth/grant/{kind}/{name}/role/{role} → map[principal][]roleName
     Group grants expanded: "group/devs" → each member as "user:{member}"

  5. buildCapabilityGrants
     For each principal → each role → each capability × each scope
     (no scope → default to ScopeCluster)
     → map[principal][]CapabilityGrant

  6. apiAuthorizer.ReplaceGrants(newGrants)    ← atomic swap

  7. parseConditionalPoliciesFromFacts
     auth/policy/{name}/capability → Capability
     auth/policy/{name}/condition/{index}/field → "subject.team"
     auth/policy/{name}/condition/{index}/operator → "=="
     auth/policy/{name}/condition/{index}/value → "resource.team"

  8. apiAuthorizer.ReplacePolicies(policies)   ← atomic swap

  Returns: nil changes (side effect only — rebuilds in-memory authorizer)
```

---

## Policy Conditions & Cycle Detection

### Condition Evaluation (security/condition.go)

```
Condition {
    Field    string             // "subject.team", "resource.name"
    Operator ConditionOperator  // ==, !=, in, not_in
    Value    string             // literal "prod" or reference "resource.team"
}

EvaluateConditions(conditions, principal, resourceContext):
  AND semantics: all conditions must pass
  Fail-closed: missing attribute → false

Field resolution:
  subject.name    → principal.Name
  subject.groups  → principal.Groups (multi-value)
  subject.<key>   → principal.Attributes[key]
  resource.team   → resourceContext.Team
  resource.name   → resourceContext.Name
  resource.type   → resourceContext.Type

Multi-value (groups):
  == / in   → ANY left value matches → true
  != / not_in → NO left value matches → true
```

### Network Policy Cycle Detection (security/policy_cycle_detection.go)

```
NetworkPolicyEngine.DetectPolicyCycles():
  1. Build directed graph from allow rules (source → target)
     Skip wildcard ("*") sources/targets
  2. DFS with three-color marking (white/gray/black)
  3. Back edge (gray → gray) = cycle
  4. Return []PolicyCycle{Services: [A, B, C]}
```

---

## Network Policy (security/network_policy.go)

### Identity-Based Rules

```
NetworkPolicyRule {
    Name          string       // unique rule name
    SourceService string       // source identity or "*"
    TargetService string       // target identity
    Port          int          // target port (0 = any)
    Action        PolicyAction // "allow" or "deny"
}

Stored as: policy/network/{name} = "source:target:port:action"

Evaluation order:
  1. Check all deny rules first — any match → DENY
  2. Check all allow rules — any match → ALLOW
  3. Default → DENY (deny-all unless explicitly allowed)
```

---

## KMS & Secrets

### Envelope Encryption (security/kms.go)

```
Seal:                                       Open:
  plaintext                                   envelope blob
      │                                            │
  [Generate random DEK]                    [Unpack: wrappedDEK + ciphertext]
      │                                            │
  [AES-256-GCM encrypt with DEK]          [KeyProvider.UnwrapKey(wrappedDEK)]
      │                                            │
  [KeyProvider.WrapKey(DEK)]                [AES-256-GCM decrypt with DEK]
      │                                            │
  [Pack: 4-byte len + wrappedDEK + ct]         plaintext
      │
  envelope blob

Key rotation (RewrapEnvelope):
  1. Unpack → unwrap DEK with old provider
  2. Re-wrap DEK with new provider
  3. Repack with same ciphertext (secret never decrypted)
```

### Envelope Binary Format

```
[4 bytes: wrappedDEK length (big-endian)] [wrappedDEK] [ciphertext]
                                                          └── nonce + AES-256-GCM sealed data
```

### KeyProvider Interface (security/kms.go:26)

| Provider         | File                      | Build Tag      | Key Material             |
|------------------|---------------------------|----------------|--------------------------|
| LocalKeyProvider | security/kms.go           | (default)      | 32-byte AES-256 on disk  |
| AWSKMSKeyProvider| security/kms_cloud.go     | cloudprovider  | AWS KMS key ARN/alias    |
| GCPKMSKeyProvider| security/kms_cloud.go     | cloudprovider  | GCP KMS resource name    |

### KMS Registry (security/kms_registry.go)

```
kmsKeyProviderFactories map[string]KMSKeyProviderFactory
  "aws-kms" → NewAWSKMSKeyProvider (registered via init())
  "gcp-kms" → NewGCPKMSKeyProvider (registered via init())

LookupKMSKeyProviderFactory("aws-kms") → factory
factory(ctx, keyARN, region) → KeyProvider
```

### Secret Store (security/secrets.go)

```
SecretStore operations:
  PutSecret(name, plaintext)    → SealEnvelope → base64 → store secrets/{name}
  GetSecret(name)               → store → base64 decode → OpenEnvelope → plaintext
  DeleteSecret(name)            → delete secrets/{name}
  ListSecrets()                 → scan secrets/ prefix → return names
  IsAuthorized(svc, secret)     → check desired/service/{svc}/secret/{name} exists
  GetSecretForService(svc, sec) → check grant → GetSecret → return plaintext + mountPath
  RotateKeyProvider(newProvider) → re-wrap all DEKs without re-encrypting secrets
```

---

## Audit Logging (security/audit.go)

### AuditEntry Fields

| Field      | Description                              |
|------------|------------------------------------------|
| Timestamp  | When the decision was made               |
| Principal  | Sanitized "kind:name" string             |
| Action     | Operation: "get", "put", "delete", etc.  |
| Target     | Key or prefix (secrets redacted)         |
| Decision   | "allow" or "deny"                        |
| AuthMethod | "mtls", "oidc", "local-token", "webhook" |
| Policy     | Policy name on condition failures        |

### Implementations

| Type                 | File               | Persistence        | Retention    |
|----------------------|--------------------|--------------------|--------------|
| InMemoryAuditLog     | security/audit.go  | Memory only        | maxEntries ring buffer |
| StoreBackedAuditLog  | security/audit.go  | etcd (audit/ prefix) + memory | maxEntries in-memory, persistent in store |

### StoreBackedAuditLog

```
Log(entry):
  1. Append to in-memory ring buffer (shift if full)
  2. Async goroutine: persistEntry
     ├─ sequenceNum.Add(1) → monotonic
     ├─ key = "audit/{020d sequence}"
     └─ PUT JSON to store

resumeSequenceFromStore():
  Scan audit/ prefix → find highest sequence → sequenceNum.Store(max)
  → no duplicate keys after restart
```

### Sanitization

```
sanitizeForAudit(input):
  1. Replace \n, \r, \t with escaped forms
  2. Redact /secret/.* → /secret/[REDACTED]
```

---

## Fact Store Key Reference

### Auth Keys

| Prefix                                | Written By      | Read By         |
|---------------------------------------|-----------------|-----------------|
| auth/role/{name}/capability/{cap}     | DSL compiler    | AuthController  |
| auth/role/{name}/scope/{path}         | DSL compiler    | AuthController  |
| auth/grant/{kind}/{name}/role/{role}  | cca role grant  | AuthController  |
| auth/group/{name}/member/{member}     | cca role grant  | AuthController  |
| auth/policy/{name}/capability         | DSL compiler    | AuthController  |
| auth/policy/{name}/condition/{i}/*    | DSL compiler    | AuthController  |

### Security Keys

| Prefix                  | Written By          | Read By             |
|-------------------------|---------------------|---------------------|
| bootstrap/token         | cca init            | ValidateBootstrapToken |
| bootstrap/expiry        | cca init            | ValidateBootstrapToken |
| bootstrap/used          | ValidateBootstrapToken | GenerateBootstrapToken |
| enrollment/token/{hash} | GenerateJoinToken   | EnrollNode          |
| enrollment/node/{id}    | EnrollNode          | IsNodeEnrolled      |
| secrets/{name}          | SecretStore.Put     | SecretStore.Get     |
| policy/network/{name}   | NetworkPolicyEngine | NetworkPolicyEngine |
| audit/{sequence}        | StoreBackedAuditLog | cca audit           |

---

## File Index

| File                                | Purpose                                  |
|-------------------------------------|------------------------------------------|
| security/principal.go               | Principal types, context propagation      |
| security/capability.go              | Capability constants                      |
| security/scope.go                   | Hierarchical scope type                   |
| security/authenticator.go           | Authenticator interface + chain           |
| security/authn_mtls.go              | mTLS authenticator (cert CN → node)       |
| security/authn_bearer.go            | Bearer token → OIDC delegation            |
| security/authn_local_token.go       | Local file-based token (hex, 0600)        |
| security/authn_webhook.go           | External webhook with response caching    |
| security/oidc.go                    | JWT verification, ES256, claim mapping    |
| security/oidc_browser_flow.go       | OAuth2 PKCE browser flow                  |
| security/oidc_discovery.go          | JWKS fetch + cache                        |
| security/rbac.go                    | Store-layer RBAC + builtin roles          |
| security/abac.go                    | Store-layer ABAC + CombinedAuthorizer     |
| security/authorizer.go              | Authorizer interface                      |
| security/api_authorizer.go          | API-layer capability authorizer           |
| security/condition.go               | ABAC condition evaluation engine          |
| security/policy_cycle_detection.go  | DFS cycle detection in network policies   |
| security/authorized_store.go        | Store wrapper with per-op authorization   |
| security/audit.go                   | AuditLogger, InMemory + StoreBacked       |
| security/ca.go                      | Certificate authority, issuance, signing  |
| security/rotator.go                 | Background certificate rotation           |
| security/enrollment.go              | Node enrollment (join tokens, CSR signing)|
| security/bootstrap.go               | One-time bootstrap token                  |
| security/user_token.go              | User JWT issuer (shares signing key)      |
| security/workload_token.go          | Workload JWT issuer + OIDC discovery/JWKS |
| security/kms.go                     | Envelope encryption, local KMS            |
| security/kms_cloud.go               | AWS + GCP KMS providers (build-tagged)    |
| security/kms_registry.go            | KMS provider factory registry             |
| security/secrets.go                 | Secret store (envelope + grant checking)  |
| security/network_policy.go          | Identity-based network policy engine      |
| security/cloud_credentials.go       | Cloud credential broker interface         |
| security/cloud_credentials_aws.go   | AWS STS credential provider               |
| controllers/auth.go                 | Auth controller (fact → APIAuthorizer)    |

---

## Timing Constants

| Constant                       | Value   | File                              |
|--------------------------------|---------|------------------------------------|
| defaultMTLSCertificateExpiry   | 1h      | security/authn_mtls.go             |
| defaultOIDCTokenExpiration     | 1h      | security/oidc.go                   |
| defaultWebhookTimeout          | 5s      | security/authn_webhook.go          |
| defaultWebhookCacheTTL         | 2min    | security/authn_webhook.go          |
| localTokenExpiry               | 24h     | security/authn_local_token.go      |
| defaultUserTokenTTL            | 8h      | security/user_token.go             |
| defaultBrowserFlowTimeout      | 5min    | security/oidc_browser_flow.go      |
| defaultJWKSRefreshInterval     | 1h      | security/oidc_discovery.go         |
| defaultJWKSFetchTimeout        | 10s     | security/oidc_discovery.go         |
| minimumCertRenewalSleep        | 10s     | security/rotator.go                |
| certificateNotBeforeClockSkew  | -1min   | security/ca.go                     |
| localTokenLength               | 32 bytes| security/authn_local_token.go      |
| codeVerifierLength             | 32 bytes| security/oidc_browser_flow.go      |
| dekLength                      | 32 bytes| security/kms.go                    |
