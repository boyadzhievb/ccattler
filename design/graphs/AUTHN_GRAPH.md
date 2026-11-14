# CCattler Authentication Call Graph (AUTHN)

Who is calling: credentials, tokens, OIDC, certificates and node enrollment.
Authorization (what a principal may do) is in [AUTHZ_GRAPH.md](AUTHZ_GRAPH.md);
secret encryption and key providers are in [SECRETS_GRAPH.md](SECRETS_GRAPH.md).

## Authentication Pipeline (security/authenticator.go)

### AuthenticatorChain.Authenticate → entry point
Chain built by `buildServerAuthenticatorAndBindings` (cmd/cca/command_server.go).
Each authenticator claims only its own credential shape or issuer (M173), so a
token is validated by exactly one authenticator and the webhook (last) never
sees local or internal tokens.
```
AuthenticatorChain.Authenticate(request)                       :39
├── MTLSAuthenticator.Authenticate(request)                    [1st, TLS mode only, security/authn_mtls.go]
│   ├── request.TLS == nil → return nil, nil                   [no TLS = skip]
│   ├── request.TLS.PeerCertificates[0].Subject.CommonName     [extract CN; CA sets CN for enrolled nodes, Flow 11]
│   └── return AuthenticationResult{Principal: "node:<CN>"}    [PrincipalKindNode]
│
├── LocalTokenAuthenticator.Authenticate(request)              [2nd, security/authn_local_token.go]
│   ├── no "Bearer " header → nil, nil
│   ├── looksLikeLocalToken(token)                             [64 hex chars; else nil, nil → next]
│   ├── subtle.ConstantTimeCompare(expected, presented)
│   │   └── mismatch → error "invalid token"                   [claimed: hard deny]
│   └── return {Principal: "user:local-admin", Groups: ["platform"]}
│
├── BearerTokenAuthenticator.Authenticate(request)             [3rd internal JWT, 4th external OIDC; security/authn_bearer.go]
│   ├── looksLikeJWT(token) false → nil, nil
│   ├── unverifiedJWTIssuer(token) != config.Issuer → nil, nil [not this issuer → next]
│   └── oidcAuthenticator.Authenticate(token)                  [claimed: error on failure]
│       ├── parseAndVerifyJWTSignature                         [security/oidc.go:92, ES256]
│       ├── extractAndValidateJWTClaims                        [iss, aud, exp]
│       └── constructAuthenticationResultFromClaims            [principal = "user:" + sub]
│
├── WebhookAuthenticator.Authenticate(request)                 [5th, only with --authn-webhook-url; security/authn_webhook.go:71]
│   ├── lookupCache(tokenString)
│   ├── callWebhook(tokenString)                               [POST {token}; !authenticated → error]
│   └── storeCache(tokenString, result)
│
└── all nil → error "no authenticator accepted the request"    [:49]

api/server.go authenticationMiddleware: chain error → log reason, respond 401 "authentication failed" (no detail)
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

CertificateAuthority.SignCSR(signRequest)                      [:301]
├── signRequest.CSR.CheckSignature()                           [verify CSR integrity]
├── require signRequest.CommonName                             [CA chooses identity; CSR subject/SANs ignored]
├── buildCSRLeafTemplate(serial, CN, dns, ips, notAfter)       [:341]
│   └── O=CCattler, ExtKeyUsage = ClientAuth only
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
EnrollmentService.EnrollNode(ctx, request)                     [:180]
├── types.ValidateNodeID(request.NodeID)                       [lowercase DNS name]
├── hashTokenValue(request.Token) → tokenKey
├── validateJoinToken(ctx, tokenKey, request)                  [:216]
│   ├── factStore.Get(tokenKey) → unmarshal JoinToken, revision
│   ├── check not expired (delete if expired)
│   └── check nodeID matches (if token is node-specific)
├── checkNodeIdentityAvailable(ctx, nodeID, tokenBound)        [:245]
│   ├── reservedNodeIDs["ccattler-server"] → reject
│   ├── factStore.Scan("auth/grant/node/<nodeID>/") non-empty → reject
│   └── factStore.Get("enrollment/node/<nodeID>") exists && !tokenBound → reject
├── signNodeCSR(request)                                       [:309]
│   ├── pem.Decode(CSRPEM) → x509.ParseCertificateRequest
│   └── ca.SignCSR(SignCSRRequest{CSR, TTL, CN=nodeID, DNSNames, IPs})
├── commitEnrollment(ctx, tokenKey, tokenRev, ..., enrolledRev) [:271]
│   └── factStore.Transaction(
│         compare tokenKey@tokenRev, enrollment/node/<nodeID>@enrolledRev,
│         delete tokenKey, put "enrollment/node/<nodeID>")      [one-time use, atomic]
├── bindRBACRoleForNode(principal)                             [:339]
│   └── rbacAuthorizer.BindRole({principal, "node-agent"})
└── return EnrollmentResponse{CertPEM, CACertPEM, Principal}
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


## Store Key Patterns

### Reads (security layer)
| Key Pattern | Where Read |
|---|---|
| `bootstrap/token` | ValidateBootstrapToken |
| `bootstrap/expiry` | ValidateBootstrapToken |
| `bootstrap/used` | GenerateBootstrapToken, ValidateBootstrapToken |
| `enrollment/token/{hash}` | EnrollNode (validateJoinToken) |
| `enrollment/node/{id}` | IsNodeEnrolled, ListEnrolledNodes |

### Writes (security layer)
| Key Pattern | Where Written |
|---|---|
| `bootstrap/token` | GenerateBootstrapToken |
| `bootstrap/expiry` | GenerateBootstrapToken |
| `bootstrap/used` | ValidateBootstrapToken |
| `enrollment/token/{hash}` | GenerateJoinToken |
| `enrollment/node/{id}` | commitEnrollment |

---

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `AuthenticatorChain.Authenticate` | HTTP middleware (api/server.go) |
| `MTLSAuthenticator.Authenticate` | AuthenticatorChain (1st, TLS mode) |
| `BearerTokenAuthenticator.Authenticate` | AuthenticatorChain (3rd internal JWT, 4th external OIDC) |
| `OIDCAuthenticator.Authenticate` | BearerTokenAuthenticator.Authenticate |
| `parseAndVerifyJWTSignature` | OIDCAuthenticator.Authenticate |
| `extractAndValidateJWTClaims` | OIDCAuthenticator.Authenticate |
| `constructAuthenticationResultFromClaims` | OIDCAuthenticator.Authenticate |
| `verifyECDSASignature` | parseAndVerifyJWTSignature |
| `looksLikeJWT` | BearerTokenAuthenticator.Authenticate |
| `LocalTokenAuthenticator.Authenticate` | AuthenticatorChain (2nd) |
| `WebhookAuthenticator.Authenticate` | AuthenticatorChain (5th, last) |
| `callWebhook` | WebhookAuthenticator.Authenticate |
| `lookupCache` | WebhookAuthenticator.Authenticate |
| `storeCache` | WebhookAuthenticator.Authenticate |
| `GenerateBootstrapToken` | cca init |
| `ValidateBootstrapToken` | enrollment endpoint handler |
| `GenerateJoinToken` | cca token create |
| `EnrollNode` | cca join handler |
| `validateJoinToken` | EnrollNode |
| `checkNodeIdentityAvailable` | EnrollNode |
| `signNodeCSR` | EnrollNode |
| `bindRBACRoleForNode` | EnrollNode |
| `commitEnrollment` | EnrollNode |
| `CertificateAuthority.IssueCertificate` | renewCertificate, EnrollNode (legacy) |
| `CertificateAuthority.SignCSR` | signNodeCSR |
| `renewCertificate` | NewCertificateRotator, renewalLoop |
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
| security/ca.go:183 | CertificateAuthority.IssueCertificate |
| security/ca.go:301 | CertificateAuthority.SignCSR |
| security/rotator.go:36 | CertificateRotator — auto-renewal |
| security/enrollment.go:180 | EnrollmentService.EnrollNode |
| security/enrollment.go:103 | EnrollmentService.GenerateJoinToken |
| security/bootstrap.go:37 | GenerateBootstrapToken |
| security/bootstrap.go:64 | ValidateBootstrapToken |
| security/user_token.go:54 | UserTokenIssuer.MintUserToken |
| security/workload_token.go:58 | WorkloadTokenIssuer.MintWorkloadToken |
