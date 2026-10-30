# CCattler Authentication & Authorization Call Graph

Machine-readable function-level index. Purpose: trace authentication, authorization,
and credential flows without re-reading security/ source files.

## Authentication Pipeline (security/authenticator.go)

### AuthenticatorChain.Authenticate → entry point
```
AuthenticatorChain.Authenticate(request)                       :39
├── MTLSAuthenticator.Authenticate(request)                    [1st, security/authn_mtls.go:29]
│   ├── request.TLS == nil → return nil, nil                   [no TLS = skip]
│   ├── request.TLS.PeerCertificates[0].Subject.CommonName     [extract CN]
│   └── return AuthenticationResult{Principal: "node:<CN>"}    [PrincipalKindNode]
│
├── BearerTokenAuthenticator.Authenticate(request)             [2nd, security/authn_bearer.go:45]
│   ├── request.Header.Get("Authorization")                    [no header → skip]
│   ├── strings.TrimPrefix(header, "Bearer ")
│   ├── looksLikeJWT(tokenString)                              [:23, count "." == 2]
│   │   └── false → return nil, nil                            [non-JWT → skip to next]
│   └── oidcAuthenticator.Authenticate(tokenString)            [delegates to OIDCAuthenticator]
│       ├── parseAndVerifyJWTSignature                         [security/oidc.go:92]
│       │   ├── split token into 3 parts (header.payload.signature)
│       │   ├── decode header → verify alg == "ES256"
│       │   ├── verifyECDSASignature(publicKey, hash, sig)     [:204, IEEE P1363 format]
│       │   └── return claimsJSON
│       ├── extractAndValidateJWTClaims                        [:142]
│       │   ├── verify iss == config.Issuer
│       │   ├── verify aud == config.Audience (if set)
│       │   └── check exp > now (default +1h if no exp)
│       └── constructAuthenticationResultFromClaims             [:173]
│           ├── principal = "user:" + claims[PrincipalClaim]   [default: "sub"]
│           └── attributes: team = claims[TeamClaim], role = claims[RoleClaim]
│
├── LocalTokenAuthenticator.Authenticate(request)              [3rd, security/authn_local_token.go:107]
│   ├── request.Header.Get("Authorization")                    [no header → skip]
│   ├── strings.TrimPrefix(header, "Bearer ")
│   ├── subtle.ConstantTimeCompare(expected, presented)        [constant-time compare]
│   │   └── mismatch → error "invalid token"                   [hard error, short-circuit]
│   └── return AuthenticationResult{Principal: "user:local-admin", Groups: ["platform"]}
│
├── WebhookAuthenticator.Authenticate(request)                 [4th, security/authn_webhook.go:71]
│   ├── request.Header.Get("Authorization")                    [no header → skip]
│   ├── lookupCache(tokenString)                               [:166, RWMutex.RLock]
│   │   └── hit + !expired → return cached result
│   ├── callWebhook(tokenString)                               [:112]
│   │   ├── POST JSON{token} to endpointURL                   [http.Client with timeout]
│   │   ├── decode webhookResponse{authenticated, principal, groups, attributes}
│   │   └── !authenticated → error (fail-closed)
│   └── storeCache(tokenString, result)                        [:181, RWMutex.Lock]
│
└── all nil → error "no authenticator accepted the request"    [:49]
```

### Chain Semantics
```
Result   Error    Action
≠nil     -        return result (first match wins)
-        ≠nil     return error (hard error = short-circuit deny)
nil      nil      try next authenticator
all nil  all nil  return error "no authenticator accepted"
```

## Authentication Context Propagation (security/authorized_store.go)

### WithPrincipal / PrincipalFromContext
```
HTTP handler (api/server.go)
├── WithPrincipal(ctx, "user:alice")                           [:34, string form for store layer]
├── WithPrincipalStruct(ctx, Principal{...})                   [security/principal.go:58, struct form for API layer]
└── WithAuthMethod(ctx, "oidc")                                [:46, for audit log entries]
```

## AuthenticationResult (security/oidc.go:62)

| Field         | Type          | Source                     |
|---------------|---------------|----------------------------|
| Principal     | string        | "kind:name" canonical form |
| PrincipalKind | PrincipalKind | user, node, service, system|
| Groups        | []string      | from IdP                   |
| Attributes    | []Attribute   | team, role from JWT claims |
| ExpiresAt     | time.Time     | token/cert expiry          |
| AuthMethod    | string        | mtls, oidc, local-token, webhook |

## Principal (security/principal.go)

### Principal struct and methods
```
Principal.String()                                             [:37]
└── return Kind + ":" + Name                                   ["user:alice@example.com"]

Principal.HasGroup(groupName)                                  [:42]
└── linear scan Groups slice

PrincipalFromKindAndName(kindColonName)                        [:72]
└── split at first ':' → Principal{Kind, Name}                 [fallback: PrincipalKindUser]
```

---

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
| node-agent       | observed/instance/, observed/node/, observed/volume/, lease/node/, desired/, effective/, placement/, credentials/, observed/credential/, derived/instance/, derived/credential/ | mixed |
| scheduler        | placement/, observed/, desired/, effective/ | mixed |
| controller       | desired/, effective/, observed/, derived/, intent/, placement/, endpoint/, network/ | mixed |
| credential-broker| desired/cloud_identity/, desired/service/, desired/credential_broker/, observed/instance/, observed/credential/, derived/credential/, credentials/ | mixed |
| cloud-controller | desired/cloud/, desired/service/, observed/node/, observed/cloud/, observed/instance/, endpoint/, network/node/ | mixed |
| api-reader       | "" (everything)          | read, watch              |
| api-writer       | desired/, intent/user/, observed/metric/, "" | mixed |

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
APIAuthorizer.ReplaceGrants(newGrants)                         [:69, atomic swap]
APIAuthorizer.ReplacePolicies(newPolicies)                     [:77, atomic swap]
APIAuthorizer.GrantRole(principal, roleName)                   [:85]
└── builtinRoleCapabilities(roleName)                          [:169]
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
│       └── no scope → default to ScopeCluster
├── apiAuthorizer.ReplaceGrants(newGrants)                     [atomic swap]
├── parseConditionalPoliciesFromFacts(facts)                   [:192]
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

## Token Lifecycle

### Bootstrap Token (security/bootstrap.go)

```
GenerateBootstrapToken(ctx, factStore, tokenTTL)               [:37]
├── factStore.Get(BootstrapUsedKey) → if "true" → error        [already completed]
├── rand.Read(32 bytes) → hex encode
├── factStore.Put("bootstrap/token", hex)
├── factStore.Put("bootstrap/expiry", RFC3339)
└── return BootstrapResult{Token, Expiry}

ValidateBootstrapToken(ctx, factStore, token)                  [:64]
├── factStore.Get("bootstrap/used") → if "true" → error        [already used]
├── factStore.Get("bootstrap/token") → constant-time compare
├── factStore.Get("bootstrap/expiry") → check not expired
├── factStore.Put("bootstrap/used", "true")                    [mark consumed]
├── factStore.Delete("bootstrap/token")                        [destroy token]
├── factStore.Delete("bootstrap/expiry")                       [destroy expiry]
└── return "admin"

IsBootstrapComplete(ctx, factStore)                            [:107]
└── factStore.Get("bootstrap/used") == "true"
```

### Local Token (security/authn_local_token.go)

```
NewLocalTokenAuthenticator(tokenDirectory)                     [:58]
├── rand.Read(32 bytes) → hex encode (64 chars)
├── os.MkdirAll(tokenDirectory, 0700)
├── os.WriteFile(path, tokenHex, 0600)
└── return LocalTokenAuthenticator{expectedTokenHex, tokenFilePath}

LoadLocalTokenAuthenticator(tokenFilePath)                     [:87]
└── os.ReadFile(tokenFilePath) → trimmed hex string

ReadLocalToken()                                               [:144]
└── os.ReadFile("/etc/ccattler/local-token")                   [CLI reads for bearer auth]
```

### User Token (security/user_token.go)

```
NewUserTokenIssuer(workloadIssuer)                             [:33]
└── shares signingKey, issuerURL, keyID from WorkloadTokenIssuer

UserTokenIssuer.MintUserToken(username, team, groups, TTL)     [:54]
├── claims: iss, sub=username, aud="ccattler", iat, exp
│   + optional: team, groups
├── signJWT(header, claims)                                    [:88]
│   └── ES256 sign with shared ECDSA P-256 key
└── default TTL: 8 hours
```

### Workload Token (security/workload_token.go)

```
NewWorkloadTokenIssuer(issuerURL)                              [:30]
├── ecdsa.GenerateKey(P-256)
└── computeKeyID(&publicKey)                                   [:181, SHA256 of X||Y → base64url[:8]]

WorkloadTokenIssuer.MintWorkloadToken(spiffeID, audience, TTL) [:58]
├── claims: iss, sub=spiffeID, aud, iat, exp
├── header: alg=ES256, typ=JWT, kid=keyID
└── ecdsa.Sign(hash) → IEEE P1363 signature

WorkloadTokenIssuer.OIDCDiscoveryDocument()                    [:123]
└── returns OIDCDiscoveryResponse{issuer, jwks_uri, ...}

WorkloadTokenIssuer.JWKSDocument()                             [:145]
└── returns JWKSResponse{Keys: [{EC, P-256, X, Y, kid}]}
```

---

## OIDC Integration

### OIDCAuthenticator (security/oidc.go)

```
OIDCAuthenticator.Authenticate(tokenString)                    [:74]
├── parseAndVerifyJWTSignature(tokenString)                    [:92]
│   ├── split 3 parts, decode header, verify alg=="ES256"
│   ├── verifyECDSASignature(ecdsaKey, hash, sig)             [:204]
│   │   └── r,s from IEEE P1363 → ecdsa.Verify
│   └── decode + return claimsJSON
├── extractAndValidateJWTClaims(claimsJSON)                    [:142]
│   ├── verify iss, aud, exp
│   └── return claims, expiresAt
└── constructAuthenticationResultFromClaims(claims, expiresAt) [:173]
    ├── "user:" + claims[PrincipalClaim]
    └── team/role attributes from claim mapping
```

### OIDC Discovery Client (security/oidc_discovery.go)

```
OIDCDiscoveryClient.FetchAndCacheKeys()                        [:52]
├── fetchJSON(issuerURL + "/.well-known/openid-configuration") [:135]
├── extract jwks_uri from discovery doc
├── fetchJSON(jwks_uri)
├── for each key in keys[]:
│   └── parseJWKEntry(keyMap) → ecdsa.PublicKey, kid           [:161]
└── mutex.Lock → update cachedKeys, lastRefresh

OIDCDiscoveryClient.PublicKeyByID(keyID)                       [:100]
├── mutex.RLock → cachedKeys[keyID]
├── if nil AND stale (>1h):
│   └── FetchAndCacheKeys() → retry lookup
└── return publicKey or nil

OIDCDiscoveryClient.FirstPublicKey()                           [:118]
└── return first cached key (single-key IdPs)
```

### Browser Flow (security/oidc_browser_flow.go)

```
ExecuteOIDCBrowserFlow(ctx, config, openBrowserFunc)           [:53]
├── generatePKCECodePair()                                     [:263, 32-byte verifier → S256 challenge]
├── net.Listen("tcp", "127.0.0.1:0") → random port
├── generateRandomState()                                      [:275, 16 random bytes]
├── build authorizeURL with: response_type=code, client_id,
│   redirect_uri, scope, state, code_challenge, code_challenge_method=S256
├── openBrowserFunc(authorizeURL)
├── /callback handler:
│   ├── verify state matches
│   ├── extract authorization code
│   └── send to channel
├── select: code | error | timeout(5min)
└── exchangeAuthorizationCode(config, code, redirectURI, codeVerifier) [:154]
    └── POST tokenURL: grant_type=authorization_code, code, redirect_uri,
        client_id, code_verifier → AccessToken, IDToken, RefreshToken

RefreshOIDCToken(tokenURL, clientID, refreshToken)             [:209]
└── POST tokenURL: grant_type=refresh_token

DiscoverOIDCEndpoints(issuerURL)                               [:285]
└── GET issuerURL/.well-known/openid-configuration → authorization_endpoint, token_endpoint
```

---

## Certificate Authority (security/ca.go)

### CA lifecycle
```
NewCertificateAuthority(validityDuration)                      [:41]
├── ecdsa.GenerateKey(P-256)
├── x509.CreateCertificate(self-signed)
│   ├── Subject: O=CCattler, CN=CCattler Root CA
│   ├── KeyUsage: CertSign | CRLSign
│   ├── IsCA: true, MaxPathLen: 1
│   └── NotBefore: now - 1min (clock skew)
└── pem.EncodeToMemory → caCertificatePEM

LoadCertificateAuthorityFromFiles(certPath, keyPath)           [:91]
├── os.ReadFile(certPath) → pem.Decode → x509.ParseCertificate
└── os.ReadFile(keyPath) → parseECPrivateKeyBlock              [:135]
    ├── x509.ParseECPrivateKey (SEC 1)
    └── x509.ParsePKCS8PrivateKey (PKCS#8 fallback)
```

### Certificate issuance
```
CertificateAuthority.IssueCertificate(request)                 [:183]
├── ecdsa.GenerateKey(P-256) → leaf key pair
├── generateSerialNumber()                                     [:396, random 128-bit]
├── x509.CreateCertificate(leafTemplate, caCert, leafPub, caKey)
│   ├── ExtKeyUsage: ServerAuth, ClientAuth
│   └── NotBefore: now - 1min
├── pem.EncodeToMemory → CertificatePEM, PrivateKeyPEM
└── return IssuedCertificate{CertificatePEM, PrivateKeyPEM, NotAfter}

CertificateAuthority.SignCSR(signRequest)                      [:300]
├── signRequest.CSR.CheckSignature()                           [verify CSR integrity]
├── mergeUniqueStrings(CSR.DNSNames, request.DNSNames)         [:356]
├── mergeUniqueIPs(CSR.IPAddresses, request.IPAddresses)       [:375]
├── buildCSRLeafTemplate(serial, subject, dns, ips, notAfter)  [:339]
├── x509.CreateCertificate(template, caCert, CSR.PublicKey, caKey)
└── return SignCSRResponse{CertificatePEM, NotAfter}

CertificateAuthority.ServerTLSConfig(serverCert)               [:246]
└── tls.Config{ClientAuth: RequireAndVerifyClientCert, MinVersion: TLS13}

CertificateAuthority.ClientTLSConfig(clientCert)               [:265]
└── tls.Config{RootCAs: caCertPool, MinVersion: TLS13}
```

### Certificate Rotation (security/rotator.go)

```
NewCertificateRotator(ca, request, renewalThreshold)           [:36]
└── renewCertificate()                                         [:119, initial issuance]
    ├── ca.IssueCertificate(request)
    ├── fullChainPEM = leafCert + caCert                       [for fingerprint verification]
    ├── tls.X509KeyPair(fullChainPEM, privateKeyPEM)
    └── atomic swap: currentCertificate, currentTLSCert

CertificateRotator.Start(ctx)                                  [:78]
└── go renewalLoop(ctx)                                        [:89]
    └── sleep until (TTL × threshold) elapsed
        ├── floor: minimumCertRenewalSleep (10s)
        └── renewCertificate() on wake

CertificateRotator.GetCertificate(hello) → *tls.Certificate   [:56, for tls.Config callback]
CertificateRotator.GetClientCertificate(info) → *tls.Certificate [:63]
```

---

## Node Enrollment (security/enrollment.go)

### GenerateJoinToken
```
EnrollmentService.GenerateJoinToken(ctx, nodeID, tokenTTL)     [:96]
├── rand.Read(32 bytes) → hex encode (64 chars)
├── computeCAFingerprint(caCertPEM)                            [:361]
│   └── SHA256(DER bytes) → hex
├── hashTokenValue(rawToken)                                   [:44]
│   └── SHA256(token) → hex (key suffix)
├── store JoinToken{prefix[8], nodeID, expiry, fingerprint}
│   key = "enrollment/token/<hash>"
└── return JoinToken with full token value (for display)
```

### EnrollNode
```
EnrollmentService.EnrollNode(ctx, request)                     [:173]
├── hashTokenValue(request.Token) → tokenKey
├── validateJoinToken(ctx, tokenKey, request)                  [:212]
│   ├── factStore.Get(tokenKey) → unmarshal JoinToken
│   ├── check not expired (delete if expired)
│   └── check nodeID matches (if token is node-specific)
├── issueOrSignCertificate(request, dnsNames)                  [:239]
│   └── signCSRFromRequest(request, dnsNames)                  [:251]
│       ├── pem.Decode(CSRPEM) → x509.ParseCertificateRequest
│       └── ca.SignCSR(SignCSRRequest{CSR, TTL, DNSNames, IPs})
├── bindRBACRoleForNode(principal)                             [:278]
│   └── rbacAuthorizer.BindRole({principal, "node-agent"})
├── recordEnrolledNode(ctx, nodeID, principal, certExpiry)     [:288]
│   └── factStore.Put("enrollment/node/<nodeID>", JSON)
├── factStore.Delete(tokenKey)                                 [one-time use, destroy token]
└── return EnrollmentResponse{CertPEM, PrivateKeyPEM, CACertPEM, Principal}
```

### Enrollment queries
```
EnrollmentService.IsNodeEnrolled(ctx, nodeID)                  [:320]
└── factStore.Get("enrollment/node/<nodeID>") → err == nil

EnrollmentService.ListEnrolledNodes(ctx)                       [:302]
└── factStore.Scan("enrollment/node/") → []EnrolledNode

EnrollmentService.RemoveNode(ctx, nodeID)                      [:326]
├── rbacAuthorizer.RemoveBinding("node:<nodeID>")
└── factStore.Delete("enrollment/node/<nodeID>")

EnrollmentService.ListJoinTokens(ctx)                          [:336]
└── factStore.Scan("enrollment/token/") → []JoinToken (non-expired, prefix + asterisks)
```

---

## KMS & Secrets

### Envelope Encryption (security/kms.go)

```
SealEnvelope(ctx, keyProvider, plaintext)                      [:97]
├── GenerateDEK() → 32-byte random AES-256 key                [:82]
├── encryptWithDEK(dek, plaintext)                             [:185]
│   └── AES-256-GCM: nonce + Seal(plaintext)
├── keyProvider.WrapKey(ctx, dek) → wrappedDEK
└── packEnvelope(wrappedDEK, ciphertext)                       [:162]
    └── [4-byte len (big-endian)][wrappedDEK][ciphertext]

OpenEnvelope(ctx, keyProvider, envelope)                       [:118]
├── unpackEnvelope(envelope) → wrappedDEK, ciphertext          [:171]
├── keyProvider.UnwrapKey(ctx, wrappedDEK) → dek
└── decryptWithDEK(dek, ciphertext)                            [:202]
    └── AES-256-GCM: Open(nonce, ciphertextBody)

RewrapEnvelope(ctx, oldProvider, newProvider, envelope)        [:141]
├── unpackEnvelope → wrappedDEK, ciphertext
├── oldProvider.UnwrapKey(wrappedDEK) → dek
├── newProvider.WrapKey(dek) → newWrappedDEK
└── packEnvelope(newWrappedDEK, ciphertext)                    [secret never decrypted]
```

### KeyProvider implementations

| Provider          | File                    | Build Tag     | Key Material             |
|-------------------|-------------------------|---------------|--------------------------|
| LocalKeyProvider  | security/kms.go:46      | (default)     | 32-byte AES-256 on disk  |
| AWSKMSKeyProvider | security/kms_cloud.go   | cloudprovider | AWS KMS key ARN/alias    |
| GCPKMSKeyProvider | security/kms_cloud.go   | cloudprovider | GCP KMS resource name    |

### SecretStore (security/secrets.go)

```
SecretStore.PutSecret(ctx, name, plaintext)                    [:58]
├── SealEnvelope(ctx, keyProvider, plaintext)
├── base64.StdEncoding.EncodeToString(envelope)
└── factStore.Put("secrets/<name>", encoded)

SecretStore.GetSecret(ctx, name)                               [:77]
├── factStore.Get("secrets/<name>")
├── base64.StdEncoding.DecodeString(value)
└── OpenEnvelope(ctx, keyProvider, envelope)

SecretStore.DeleteSecret(ctx, name)                            [:101]
└── factStore.Delete("secrets/<name>")

SecretStore.ListSecrets(ctx)                                   [:107]
└── factStore.Scan("secrets/") → trim prefix → names

SecretStore.IsAuthorized(ctx, serviceName, secretName)         [:123]
└── factStore.Get("desired/service/{svc}/secret/{name}")       [grant check]

SecretStore.GetSecretForService(ctx, svc, secret)              [:134]
├── IsAuthorized → check grant
├── factStore.Get(grantKey) → mountPath
└── GetSecret → plaintext

SecretStore.RotateKeyProvider(ctx, newKeyProvider)             [:162]
├── factStore.Scan("secrets/")
├── for each: RewrapEnvelope(oldProvider, newProvider, blob)   [DEK re-wrap only]
└── secretStore.keyProvider = newKeyProvider
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
| `bootstrap/token` | ValidateBootstrapToken |
| `bootstrap/expiry` | ValidateBootstrapToken |
| `bootstrap/used` | GenerateBootstrapToken, ValidateBootstrapToken |
| `enrollment/token/{hash}` | EnrollNode (validateJoinToken) |
| `enrollment/node/{id}` | IsNodeEnrolled, ListEnrolledNodes |
| `secrets/{name}` | SecretStore.GetSecret |
| `desired/service/{svc}/secret/{name}` | SecretStore.IsAuthorized |
| `policy/network/{name}` | NetworkPolicyEngine.ListRules |
| `audit/{sequence}` | resumeSequenceFromStore |

### Writes (security layer)
| Key Pattern | Where Written |
|---|---|
| `bootstrap/token` | GenerateBootstrapToken |
| `bootstrap/expiry` | GenerateBootstrapToken |
| `bootstrap/used` | ValidateBootstrapToken |
| `enrollment/token/{hash}` | GenerateJoinToken |
| `enrollment/node/{id}` | recordEnrolledNode |
| `secrets/{name}` | SecretStore.PutSecret, RotateKeyProvider |
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
| `AuthenticatorChain.Authenticate` | HTTP middleware (api/server.go) |
| `MTLSAuthenticator.Authenticate` | AuthenticatorChain (1st in chain) |
| `BearerTokenAuthenticator.Authenticate` | AuthenticatorChain (2nd in chain) |
| `OIDCAuthenticator.Authenticate` | BearerTokenAuthenticator.Authenticate |
| `parseAndVerifyJWTSignature` | OIDCAuthenticator.Authenticate |
| `extractAndValidateJWTClaims` | OIDCAuthenticator.Authenticate |
| `constructAuthenticationResultFromClaims` | OIDCAuthenticator.Authenticate |
| `verifyECDSASignature` | parseAndVerifyJWTSignature |
| `looksLikeJWT` | BearerTokenAuthenticator.Authenticate |
| `LocalTokenAuthenticator.Authenticate` | AuthenticatorChain (3rd in chain) |
| `WebhookAuthenticator.Authenticate` | AuthenticatorChain (4th in chain) |
| `callWebhook` | WebhookAuthenticator.Authenticate |
| `lookupCache` | WebhookAuthenticator.Authenticate |
| `storeCache` | WebhookAuthenticator.Authenticate |
| `RBACAuthorizer.Authorize` | AuthorizedStore (all ops), CombinedAuthorizer |
| `ABACAuthorizer.Authorize` | CombinedAuthorizer |
| `resolveRoleNames` | RBACAuthorizer.Authorize |
| `attributesMatch` | ABACAuthorizer.Authorize |
| `CombinedAuthorizer.Authorize` | AuthorizedStore (when combined auth) |
| `APIAuthorizer.AuthorizeAPI` | API handlers (cca get, apply, scale, etc.) |
| `resolveGrants` | APIAuthorizer.AuthorizeAPI |
| `builtinRoleCapabilities` | APIAuthorizer.GrantRole |
| `EvaluateConditions` | APIAuthorizer.AuthorizeAPI (ABAC policies) |
| `evaluateSingleCondition` | EvaluateConditions |
| `resolveField` | evaluateSingleCondition |
| `resolveSubjectField` | resolveField (objectName=="subject") |
| `resolveResourceField` | resolveField (objectName=="resource") |
| `AuthController.Reconcile` | Runner.runControllerLoop (on auth/ watch) |
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
| `SealEnvelope` | SecretStore.PutSecret |
| `OpenEnvelope` | SecretStore.GetSecret |
| `RewrapEnvelope` | SecretStore.RotateKeyProvider |
| `GenerateDEK` | SealEnvelope |
| `encryptWithDEK` | SealEnvelope, LocalKeyProvider.WrapKey |
| `decryptWithDEK` | OpenEnvelope, LocalKeyProvider.UnwrapKey |
| `packEnvelope` | SealEnvelope, RewrapEnvelope |
| `unpackEnvelope` | OpenEnvelope, RewrapEnvelope |
| `GenerateBootstrapToken` | cca init |
| `ValidateBootstrapToken` | enrollment endpoint handler |
| `GenerateJoinToken` | cca token create |
| `EnrollNode` | cca join handler |
| `validateJoinToken` | EnrollNode |
| `issueOrSignCertificate` | EnrollNode |
| `signCSRFromRequest` | issueOrSignCertificate |
| `bindRBACRoleForNode` | EnrollNode |
| `recordEnrolledNode` | EnrollNode |
| `CertificateAuthority.IssueCertificate` | renewCertificate, EnrollNode (legacy) |
| `CertificateAuthority.SignCSR` | signCSRFromRequest |
| `renewCertificate` | NewCertificateRotator, renewalLoop |
| `NetworkPolicyEngine.Evaluate` | data plane nftables rules |
| `DetectPolicyCycles` | lint / validation |
| `matchesRule` | Evaluate (deny loop, allow loop) |
| `FetchAndCacheKeys` | startup, PublicKeyByID (on stale) |
| `ExecuteOIDCBrowserFlow` | cca login |
| `exchangeAuthorizationCode` | ExecuteOIDCBrowserFlow |
| `RefreshOIDCToken` | cca token refresh |

---

## Timing Constants

| Constant | Value | File:Line |
|---|---|---|
| defaultMTLSCertificateExpiry | 1h | security/authn_mtls.go:13 |
| defaultOIDCTokenExpiration | 1h | security/oidc.go:22 |
| defaultWebhookTimeout | 5s | security/authn_webhook.go:17 |
| defaultWebhookCacheTTL | 2min | security/authn_webhook.go:21 |
| localTokenExpiry | 24h | security/authn_local_token.go:40 |
| defaultUserTokenTTL | 8h | security/user_token.go:17 |
| defaultBrowserFlowTimeout | 5min | security/oidc_browser_flow.go:23 |
| defaultJWKSRefreshInterval | 1h | security/oidc_discovery.go:21 |
| defaultJWKSFetchTimeout | 10s | security/oidc_discovery.go:25 |
| minimumCertRenewalSleep | 10s | security/rotator.go:18 |
| certificateNotBeforeClockSkew | -1min | security/ca.go:28 |
| localTokenLength | 32 bytes | security/authn_local_token.go:21 |
| codeVerifierLength | 32 bytes | security/oidc_browser_flow.go:27 |
| dekLength | 32 bytes | security/kms.go:40 |

---

## File Index

| File | Purpose |
|---|---|
| security/authenticator.go:39 | AuthenticatorChain.Authenticate — chain evaluation |
| security/authn_mtls.go:29 | MTLSAuthenticator — TLS peer cert → "node:<CN>" |
| security/authn_bearer.go:45 | BearerTokenAuthenticator — JWT → OIDC delegation |
| security/authn_local_token.go:107 | LocalTokenAuthenticator — hex token, constant-time compare |
| security/authn_webhook.go:71 | WebhookAuthenticator — external endpoint with response cache |
| security/oidc.go:74 | OIDCAuthenticator — JWT ES256 verification + claim mapping |
| security/oidc_browser_flow.go:53 | ExecuteOIDCBrowserFlow — OAuth2 PKCE flow |
| security/oidc_discovery.go:52 | OIDCDiscoveryClient — JWKS fetch + cache |
| security/principal.go:28 | Principal struct, context propagation |
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
| security/ca.go:183 | CertificateAuthority.IssueCertificate |
| security/ca.go:300 | CertificateAuthority.SignCSR |
| security/rotator.go:36 | CertificateRotator — auto-renewal |
| security/enrollment.go:173 | EnrollmentService.EnrollNode |
| security/enrollment.go:96 | EnrollmentService.GenerateJoinToken |
| security/bootstrap.go:37 | GenerateBootstrapToken |
| security/bootstrap.go:64 | ValidateBootstrapToken |
| security/user_token.go:54 | UserTokenIssuer.MintUserToken |
| security/workload_token.go:58 | WorkloadTokenIssuer.MintWorkloadToken |
| security/kms.go:97 | SealEnvelope — envelope encrypt |
| security/kms.go:118 | OpenEnvelope — envelope decrypt |
| security/kms.go:141 | RewrapEnvelope — key rotation |
| security/secrets.go:58 | SecretStore.PutSecret |
| security/secrets.go:77 | SecretStore.GetSecret |
| security/secrets.go:162 | SecretStore.RotateKeyProvider |
| security/network_policy.go:83 | NetworkPolicyEngine.Evaluate |
| security/policy_cycle_detection.go:18 | DetectPolicyCycles — DFS cycle detection |
| controllers/auth.go:49 | AuthController.Reconcile — fact → APIAuthorizer rebuild |
