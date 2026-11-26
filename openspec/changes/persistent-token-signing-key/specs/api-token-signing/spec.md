# Spec Delta

## Purpose
States how the API's token signing identity is created once per cluster, shared by every control-plane replica,
protected in the keyring, and used to sign and verify internal JWTs, so that a valid token keeps working across
restarts and replicas until it expires or its user is revoked.

## ADDED Requirements

### Requirement: One signing identity per cluster, created once
The first `cca server` of a cluster SHALL create the signing identity: an ECDSA P-256 private key, sealed with the
keyring, plus an initialization marker and the cluster issuer URL. It SHALL use one create-only transaction. Every
other start SHALL load the stored identity. A replica whose creation transaction loses SHALL discard its own key,
re-read the store and use the stored key.

#### Scenario: Concurrent first start
- **GIVEN** an empty store with a ready keyring
- **WHEN** four replicas start at the same time
- **THEN** exactly one signing key and one marker exist in the store, and all four replicas sign with the same `kid`
- Test: `TestTokenSigningKeyConcurrentFirstStartConverges` (memory store and `etcd_integration`)

#### Scenario: Losing the creation race
- **GIVEN** a replica whose creation transaction fails because another replica committed first
- **WHEN** it continues its start
- **THEN** it uses the committed key, never its own, and never reports the signing state as incomplete
- Test: `TestTokenSigningKeyLostRaceLoadsWinner`

### Requirement: Tokens survive restarts and work on every replica
A token minted by any replica SHALL be accepted by every replica, and after any restart, until it expires or its user
is revoked. Every replica SHALL use the stored issuer URL and SHALL publish the same key set at `/oidc/jwks`.

#### Scenario: Restart
- **GIVEN** a token minted with `cca user token`
- **WHEN** `cca-server` restarts
- **THEN** the same token is still accepted
- Test: `TestUserTokenSurvivesServerRestart`; check `deploy-run:user-token-survives-restart`

#### Scenario: Another replica
- **GIVEN** two replicas on one store, and a token minted on replica A
- **WHEN** the token is presented to replica B
- **THEN** B accepts it, with the same principal as A
- **AND** both replicas serve the same `/oidc/jwks` and the same OIDC discovery `issuer`
- Test: `TestUserTokenAcceptedByOtherReplica`, `TestJWKSIdenticalAcrossReplicas`; check `ha:user-token-cross-replica`

### Requirement: The issuer URL is cluster state
The issuer URL SHALL be stored at first start. It SHALL come from `--token-issuer-url`, or else from the first
replica's listen address. Later starts SHALL use the stored value. A start with an explicit `--token-issuer-url` that
differs from the stored one SHALL fail with an error naming both values.

#### Scenario: A replica with a different explicit issuer
- **GIVEN** a cluster whose stored issuer is `https://ctrl-1:9770`
- **WHEN** a replica starts with `--token-issuer-url https://other:9770`
- **THEN** it exits before serving, naming both URLs, and the store is unchanged
- Test: `TestServerRefusesDifferentTokenIssuerURL`

### Requirement: A lost or unreadable signing identity fails closed
On a cluster whose marker exists, a missing, corrupt or unopenable signing-key record SHALL stop `cca server` before
it serves, with an error that names the store key. It SHALL never generate a replacement identity.

#### Scenario: Key record deleted
- **GIVEN** an initialized cluster whose signing-key record was deleted
- **WHEN** `cca server` starts
- **THEN** it exits with an error naming the store key, and no new key appears in the store
- Test: `TestServerRefusesStartWhenSigningKeyMissing`

#### Scenario: Corrupt envelope or a different root key
- **GIVEN** an initialized cluster whose key envelope is corrupt, or a replica with a different root key
- **WHEN** `cca server` starts
- **THEN** it exits before serving, and the store is unchanged
- Test: `TestServerRefusesCorruptSigningKey`, `TestServerRefusesSigningKeyWithWrongRootKey`

### Requirement: Verification selects the key by its ID
One active key SHALL sign new tokens. The verifier SHALL select the public key by the token's `kid` from the stored
key set, and SHALL refuse a token whose `kid` is unknown. `/oidc/jwks` SHALL publish every key in the set. Keys that
no longer sign SHALL keep verifying their tokens. No release before the rotation milestone removes a key.

#### Scenario: Two keys
- **GIVEN** a key set with an older key K1 and an active key K2
- **WHEN** a token signed by K1 and a new token are presented
- **THEN** both are accepted, the new token carries K2's `kid`, and `/oidc/jwks` lists K1 and K2
- Test: `TestInternalAuthenticatorSelectsKeyByKid`, `TestJWKSPublishesEveryKey`

#### Scenario: Unknown key ID
- **GIVEN** a token whose `kid` is not in the key set, or whose signature does not match its `kid`
- **WHEN** it is presented
- **THEN** it is refused with 401
- Test: `TestInternalAuthenticatorRefusesUnknownKid`

### Requirement: Keyring operations carry the signing key
KEK rotation SHALL re-wrap the sealed signing key and count it before retiring a KEK. Legacy-key migration SHALL move
it to a v2 envelope. After either operation, every replica SHALL still sign and verify with the same `kid`.

#### Scenario: KEK rotation
- **GIVEN** a cluster whose signing key is sealed with KEK version 1
- **WHEN** `cca secret rotate` completes and version 1 is retired
- **THEN** the signing key is sealed with version 2, every replica still verifies earlier tokens, and a restart
  succeeds
- Test: `TestKEKRotationRewrapsTokenSigningKey`, `TestKEKRetirementWaitsForTokenSigningKey`

#### Scenario: Legacy-key migration
- **GIVEN** a cluster still on the legacy secret key whose signing key is a v1 envelope
- **WHEN** the legacy migration completes
- **THEN** the signing key is a v2 envelope, and retiring the legacy key leaves tokens valid
- Test: `TestLegacyMigrationMovesTokenSigningKeyToV2`

### Requirement: The private key stays inside the control-plane process
The plaintext private key SHALL never appear in any API response, store fact, CLI output, log line or audit record.
In the store it SHALL exist only as a keyring envelope.

#### Scenario: Looking for the key
- **GIVEN** a running cluster
- **WHEN** every API route reachable by a cluster admin, the server log and the audit records are read
- **THEN** none of them contains the private key in any encoding, and the store fact is a v2 envelope
- Test: `TestTokenSigningKeyNeverExposed`

### Requirement: Token lifetime is bounded
`POST /api/user/token` SHALL refuse a TTL above `--max-user-token-ttl` (default 24 h) with 400 and a message naming the
maximum. Without a TTL it SHALL keep the 8-hour default.

#### Scenario: Too long
- **GIVEN** the default maximum
- **WHEN** `cca user token alice --ttl 25h` runs
- **THEN** it fails with a message naming 24h, and no token is minted
- Test: `TestUserTokenTTLAboveMaximumRefused`

### Requirement: User revocation still applies
Deleting or recreating a user SHALL keep refusing that user's earlier tokens (M216), also after a restart and on
another replica, although their signing key stays trusted.

#### Scenario: Deleted user across a restart
- **GIVEN** a token for alice, and alice deleted
- **WHEN** the server restarts and the token is presented
- **THEN** it is refused
- Test: `TestDeletedUserTokenRefusedAfterRestart`; the replay cases `deleted-user-token-refused` and
  `recreated-user-old-token-refused`
