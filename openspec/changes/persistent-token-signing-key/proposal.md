# Proposal

## Why

Milestone **M283 — API Tokens Survive Restarts and Work on Every Replica** (security bug, found in the M280 rework,
2026-11-25). The user approved the design direction in two reviews on 2026-11-25; it is summarized in milestones.md
Phase 283 and drawn in design/plans/m283-token-signing-key-design.md.

The API's JWT signing key lives only in memory (design/graphs/AUTHN_GRAPH.md "Workload Token" and "User Token"):
- **A new key at every start:** `cca server` calls `NewWorkloadTokenIssuer` (cmd/cca/command_server.go:658), which
  generates a new ECDSA P-256 key (security/workload_token.go:31). User tokens share that key
  (security/user_token.go:37-43), and the internal JWT authenticator verifies only against it.
- **The issuer URL is per host:** it comes from `resolveServerIssuerURL` (command_server.go:456), from the listen
  address or the host name. `BearerTokenAuthenticator` skips every token whose `iss` is not this replica's issuer
  (AUTHN_GRAPH "Authentication Pipeline"), so a token from another replica never even reaches signature checking.

As a result:
- **Every `cca user token` token dies at a server restart,** including the guide's step 3
  (website/installation.md:499), which tells users to `export CCA_TOKEN=$(sudo cca user token USERNAME --ttl 24h)`.
- **In HA, a token works only on the replica that minted it:** the same request gets 200 or 401 depending on which
  replica answers.
- **`/oidc/jwks` differs per replica and per restart.**
- **No maximum token lifetime:** `MintUserToken` only defaults to 8 h when none is given (user_token.go:62-63).

Not affected: the local admin token, external OIDC tokens, and mTLS identities.

**How others do it.**

| System | Signing key | Rotation and verification |
|---|---|---|
| Kubernetes | every kube-apiserver loads the same `--service-account-signing-key-file` | `--service-account-key-file` lists several public keys; tokens carry `kid`; `--service-account-max-token-expiration` caps lifetime |
| Vault | the token store and its keys live in Vault's encrypted storage, shared by every node | tokens survive restarts and fail-over |
| Consul | ACL tokens are replicated state, not signatures; the signing keys for its JWT auth come from configuration | tokens survive restarts |
| AWS IAM / STS | one regional signing identity, rotated by AWS; verifiers fetch the published keys | short-lived credentials with a maximum lifetime |

All four share one rule: the signing identity is cluster state, never per-process state. CCattler already has the
encrypted, replicated store for it: the keyring in etcd (design/graphs/SECRETS_GRAPH.md, Flow 29).

## What Changes

- **One signing key per cluster, kept in the keyring.**
  - The private key is sealed with `KeyringEnvelopeCodec.SealEnvelopeForKey`, so the envelope is bound to its store
    key.
  - The first replica creates it in a create-only transaction, together with an initialization marker and the
    cluster's issuer URL. Every other replica, and every restart, loads that key.
  - A replica that loses the creation race re-reads and uses the winner's key. It never keeps its own (the M284
    lesson).
- **One issuer URL per cluster.** It is stored next to the key at first start. It comes from the new
  `--token-issuer-url` flag, else from today's `resolveServerIssuerURL` on the first replica. Later starts use the
  stored value. A replica started with a different explicit `--token-issuer-url` refuses to start.
- **Fail closed:**
  - an initialized cluster whose signing-key record is missing, corrupt or unopenable refuses to start and never
    generates a new identity;
  - a replica with a different root key already refuses through the keyring root check.
- **Verification by key ID:**
  - one active private key signs;
  - the internal authenticator selects the public key by the token's `kid` from the shared key set;
  - `/oidc/jwks` publishes the whole set, identical on every replica.
- **Keyring operations carry the key:**
  - **KEK rotation** re-wraps the sealed signing key and counts it before it retires a KEK;
  - **legacy-key migration** moves it to v2 envelopes;
  - **root-key rotation** needs nothing, because it re-wraps the KEKs.
- **A maximum token lifetime:**
  - `POST /api/user/token` refuses a TTL above `--max-user-token-ttl` (default 24 h);
  - the default TTL stays 8 h.
- **Docs:**
  - the guide's step-3 note from M280 ("a user token does not survive a server restart until M283") is replaced;
  - the changelog says that tokens issued before this release must be re-created once.

## Capabilities

### New Capabilities
- `api-token-signing`: how the API's signing identity is created, shared, protected and used to sign and verify
  tokens, and the limits on token lifetime.

### Modified Capabilities
- (none)

## Impact

- **Code:** cmd/cca/command_server.go (the issuer setup and flags), security/workload_token.go,
  security/user_token.go, security/oidc.go and security/authn_bearer.go (selection by kid), security/keyring.go
  (`encryptedStorePrefixes` and bootstrap ordering), a new security/token_signing_key.go, api/server.go
  (`handleUserToken`, the max TTL), types/keys_secret.go (new store keys), controllers (the KEK-rotation write
  domain).
- **Store:** new keys under `keyring/token-signing/`.
- **Compatibility:** tokens minted before this release stop working once, at the upgrade. Their private keys were
  never persisted, and old replicas each had their own.
- **Graphs:** AUTHN_GRAPH (token lifecycle, authenticator), SECRETS_GRAPH (keyring prefixes, rotation write domain).
  A new flow, "Token signing key", goes in design/runtime-invariant-flows.md.

## Out of scope

- An operator command to rotate the signing key, and the removal of retired keys. This change defines and tests the
  multi-key contract and the retention rule only.
- `CredentialBrokerController` (dead code: no production caller). Recorded as a follow-up, not touched.
- Self-refreshing tokens for services and integrations: a later milestone, after this one.
- Per-identity etcd authorization: M181.

## Proof

- **Red-first unit and integration tests** named in the spec, among them:
  - concurrent first start converges on one key;
  - a token survives a restart;
  - a token from replica A is accepted by B;
  - JWKS is equal across replicas;
  - selection by kid with two keys;
  - a missing or corrupt key refuses startup;
  - a wrong root key refuses to serve;
  - a KEK rotation re-wraps the signing key;
  - the private key never appears in any API, status, log or audit output;
  - user deletion and recreation still revoke;
  - a TTL above the maximum is refused.
- **The real-etcd tier** (`-tags etcd_integration`) for the concurrent-start and restart cases.
- **The deploy cluster:** the full Deploy Testbed run (job summary `full run`) adds a check that an `e2e-admin`
  `CCA_TOKEN` works before and after the run's `cca-server` restart. The cross-replica check goes into the HA test,
  and runs on the next E2E HA run, per the user's rule: one dispatch, other CI scenarios verified as they occur.
- **Independent review:** `code-reviewer` plus a patterns review, and `security-audit`. At most two rounds; the main
  session stops after each round and waits for the user's go.
