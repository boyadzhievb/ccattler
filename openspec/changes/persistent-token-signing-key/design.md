# Design

## Context

Read from the graphs first (AUTHN_GRAPH "Token Lifecycle", "Authentication Pipeline"; SECRETS_GRAPH "Secret keyring
and envelope codec"), then the code on 2026-11-26.

**Today's token path:**
- **Signing:** `configureAndStartAPIServer` creates `NewWorkloadTokenIssuer(resolveServerIssuerURL(cfg))`
  (cmd/cca/command_server.go:657-663). It holds a fresh in-memory ECDSA key (security/workload_token.go:31) and a
  `kid` derived from the public key (`computeKeyID`, :181). `NewUserTokenIssuer` shares that key and `kid`
  (security/user_token.go:37-43); user tokens default to 8 h with no maximum (:62-63).
- **Verification:** `appendBearerTokenAuthenticators` builds the internal authenticator from
  `NewOIDCAuthenticator(config, workloadTokenIssuer.PublicKey())`, which holds one verifying key
  (security/oidc.go:42-56). It wraps that in `NewLocalUserRecordAuthenticator` for the M216 user-revision check.
  `BearerTokenAuthenticator` passes a token on only when its `iss` equals this replica's issuer (AUTHN_GRAPH).
- **Publishing:** `/oidc/jwks` and `/.well-known/openid-configuration` serve that one key and the per-host issuer
  (api/server.go:167-169, 1816).

**The keyring** (SECRETS_GRAPH):
- `startSecretEnvelopeCodec` builds `KeyringEnvelopeCodec` before the controllers and the API start.
- `SealEnvelopeForKey`/`OpenEnvelopeForKey` bind an envelope to its store key (security/envelope_codec.go:111-125).
- `encryptedStorePrefixes` (security/keyring.go:77) is `secrets/` and `credentials/`. KEK rotation
  (`PlanKEKRewrapBatch`), legacy migration and retirement (`countV1Envelopes`) work only on what these prefixes and
  the controllers' watches cover.
- `ErrKeyringRootCheckFailed` stops a replica with a different root key.

## Goals / Non-Goals

**Goals:**
- One signing identity per cluster: created once, shared by every replica, kept across restarts.
- One issuer URL per cluster.
- Fail closed when the identity is lost.
- Verification by `kid` over a key set, so that rotation can come later without a format change.
- A bounded token lifetime.

**Non-Goals:**
- An operator rotation command, and removing retired keys.
- The credential broker (dead code; recorded as a follow-up).
- Self-refreshing service tokens.
- etcd per-identity access (M181).

## Decisions

### D1. Store keys (all control-plane only, under the keyring area)

| Key | Value | Writers | Fencing |
|---|---|---|---|
| `keyring/token-signing/initialized` | JSON `{issuer_url, created}`, the marker | the first `cca server` only | create-only (revision 0) in one transaction with the next two |
| `keyring/token-signing/active` | the `kid` that signs | the first `cca server`; later only the rotation milestone | created with the marker; any later writer compares revisions |
| `keyring/token-signing/key/{kid}` | a keyring envelope of the PKCS#8 private key, AAD = this store key | the first `cca server` (create); the KEK-rotation and legacy-migration controllers (re-wrap) | create-only at bootstrap; re-wraps go through the runner's revision guard per key, as for `secrets/` today |

- **Bootstrap writes three facts in one transaction,** far below the etcd transaction limit.
- **The KEK-rotation re-wrap** adds one envelope to its existing batch (`PlanKEKRewrapBatch`, 40 per cycle), so the
  worst case per cycle is unchanged.
- **Placement under `keyring/`:** the area is never served by the secret APIs. It is already watched by every
  replica (`RunKeyringWatch`), and `refuseKeyringRebootstrap` already refuses a keyring/ area whose `keyring/active`
  is missing, which matches fail-closed.

### D2. Bootstrap: create once, re-read after a lost race
`loadOrCreateTokenSigningIdentity(ctx, factStore, keyringCodec, explicitIssuerURL, defaultIssuerURL)` runs after the
keyring is ready and before the API or any controller starts.
1. **Read the marker.**
2. **Marker present:**
   - load `active` and every `key/*`, opening each with `OpenEnvelopeForKey`;
   - fail if `active` or its key is missing, or if any envelope fails to open or parse;
   - if `explicitIssuerURL` is set and differs from the marker's, fail, naming both.
3. **Marker absent:**
   - refuse if any `key/*` or `active` exists, since that is a lost marker, not a fresh cluster;
   - otherwise generate the key, seal it, and commit the marker, `active` and `key/{kid}` with revision-0 compares;
   - if the transaction fails on a compare, discard the local key and go back to step 1, at most once more (a second
     loss is an error). It never infers "incomplete" from a partial read: the M284 lesson, a single re-read after the
     committed transaction.

The issuer URL at creation is `explicitIssuerURL`, else `defaultIssuerURL` (today's `resolveServerIssuerURL`).

### D3. Signer and verifier
- **One identity object** (`TokenSigningIdentity`) holds the active private key and `kid`, and a
  `map[kid]*ecdsa.PublicKey` of every key.
- **Signing:** `WorkloadTokenIssuer` and `UserTokenIssuer` are built from it (`NewWorkloadTokenIssuerWithKey` and
  `NewUserTokenIssuerWithKey` already exist; they get production callers). JWKS and discovery serve the whole key
  set and the stored issuer.
- **Verification:** `OIDCAuthenticator` gets a key resolver, `kid → public key`, instead of one key. An unknown
  `kid` is refused. The external-OIDC authenticator keeps its single key, wrapped as a one-entry resolver.
- **No watch in this milestone.** Keys do not change after bootstrap until the rotation milestone, which adds a
  watch on `keyring/token-signing/`. KEK re-wraps change only the envelope, never the key material held in memory.

### D4. Keyring operations
- `encryptedStorePrefixes` gains `keyring/token-signing/key/`.
- The KEK-rotation and legacy-migration controllers watch it, and their write domains gain it
  (controllers/topological_sort.go).
- Retirement and `countV1Envelopes` cover it through the same list. Tests prove each of these.

### D5. Maximum TTL
- New flag `--max-user-token-ttl`, default 24 h, the guide's own example (website/installation.md:499).
- `handleUserToken` refuses a TTL above it with 400 and the maximum in the message.
- The 8 h default stays.

### D6. Upgrade
- **The first start of this release** finds no marker and creates the identity; that is creation, not loss.
- **Tokens minted before the upgrade stop working once.** Their keys were ephemeral, and each old replica had its
  own; the changelog says so.
- **During a rolling HA upgrade,** a replica still on the old release keeps its own key and its per-host issuer
  until it is upgraded. The overlap is documented (start-new-then-stop-old, per the no-outage rule).

## Failure cases

| Case | Behaviour |
|---|---|
| Four replicas start at once on an empty store | One wins the transaction, the others re-read and use its key (D2) |
| Marker present, `key/{active}` deleted | `cca server` exits naming the key; nothing is written |
| Envelope corrupt, or KEK missing | exit naming the key; nothing is written |
| A replica with a different root key | exits at the keyring root check, before any signing-key read |
| `key/*` present but marker missing | exit ("signing state without marker; restore keyring/ from an etcd backup"); never re-create |
| Explicit `--token-issuer-url` differs from the stored one | exit naming both |
| Token with an unknown `kid` | 401 |
| KEK rotation while replicas run | re-wrap only; replicas keep their in-memory key; restarts open the new envelope |
| TTL above the maximum | 400 naming the maximum; no token minted |
| etcd unavailable at start | the start fails as the keyring start does today (exit, systemd restarts) |

## Flow

A new **Flow 37, "Token signing key"**, goes in design/runtime-invariant-flows.md: bootstrap, signing, verification
by `kid`, and the keyring operations. It links Flow 29 (the keyring) and AUTHN_GRAPH.

## Risks / Trade-offs

- **[One-time token invalidation at upgrade]** Unavoidable, and documented.
- **[The keyring's availability is now also token availability]** That is already true for secrets, so it adds no
  new dependency.
- **[No rotation command yet]** A leaked key is replaced only by re-initializing until the rotation milestone. This
  is stated in the security model.

## Migration Plan

None beyond D6. Rollback to an earlier release ignores the `keyring/token-signing/` keys; tokens are invalidated once
more.
