# Tasks

Milestone M283. Design: [design.md](design.md). Spec: [api-token-signing](specs/api-token-signing/spec.md).
Graphs to read before the code: design/graphs/AUTHN_GRAPH.md and design/graphs/SECRETS_GRAPH.md.

## 1. Tests first (each recorded failing before any implementation)

- [x] 1.1 Bootstrap: `TestTokenSigningKeyConcurrentFirstStartConverges` (memory store, plus `etcd_integration` with
  the real-etcd harness) and `TestTokenSigningKeyLostRaceLoadsWinner`.
  — 2026-11-26: security/token_signing_key_test.go, `…ConvergesOnEtcd` in token_signing_key_etcd_test.go (own etcd
  from ETCD_BIN); red on c017fe7: build failed (undefined LoadOrCreateTokenSigningIdentity).
- [x] 1.2 Restart and replicas: `TestUserTokenSurvivesServerRestart`, `TestUserTokenAcceptedByOtherReplica` and
  `TestJWKSIdenticalAcrossReplicas`. Two API servers on one store; the issuer and JWKS are compared.
  — cmd/cca/server_token_signing_test.go, through the production wiring; red on c017fe7: build failed.
- [x] 1.3 Issuer and fail-closed:
  - `TestServerRefusesDifferentTokenIssuerURL`;
  - `TestServerRefusesStartWhenSigningKeyMissing`;
  - `TestServerRefusesCorruptSigningKey`;
  - `TestServerRefusesSigningKeyWithWrongRootKey`.

  Each also asserts that the store is unchanged.
  — cmd/cca/server_token_signing_test.go (full-store snapshot before and after); red on c017fe7: build failed.
- [x] 1.4 Key ID: `TestInternalAuthenticatorSelectsKeyByKid`, `TestJWKSPublishesEveryKey` and
  `TestInternalAuthenticatorRefusesUnknownKid` (a two-key set written to the store directly).
  — security/token_signing_key_test.go (unknown kid, no kid, K1's kid signed by another key); red: build failed.
- [x] 1.5 Keyring operations: `TestKEKRotationRewrapsTokenSigningKey`, `TestKEKRetirementWaitsForTokenSigningKey` and
  `TestLegacyMigrationMovesTokenSigningKeyToV2`.
  — controllers/keyring_token_signing_test.go, plus `TestKeyringOperationsCoverTokenSigningKey` (watch and write
  domains) and security `TestLegacyRetirementGateCountsTokenSigningKey`; red: build failed, then behaviourally red
  after 2.2 and before 2.3 ("sealed with KEK 1", "KEK 1 retiring while … KEK 1", "still v1", gate `[]`).
- [x] 1.6 Confinement, lifetime and revocation: `TestTokenSigningKeyNeverExposed`,
  `TestUserTokenTTLAboveMaximumRefused` and `TestDeletedUserTokenRefusedAfterRestart`.
  — api/token_signing_confinement_test.go (every registered route, keyring reads, log, audit, store; 10 encodings)
  and cmd/cca; red: build failed; the TTL test also behaviourally red before 2.4 (25h minted, 200).

## 2. Implementation

- [x] 2.1 Store keys (types/keys_secret.go) and `loadOrCreateTokenSigningIdentity` (new
  security/token_signing_key.go), following D1 and D2. It is called after the keyring is ready and before the
  controllers and the API start. 1.1 and 1.3 go green.
  — exported `security.LoadOrCreateTokenSigningIdentity`; one Scan of keyring/token-signing/ per read, re-read after
  the create transaction (won or lost); called in executeServerCommand on API replicas only.
- [x] 2.2 `TokenSigningIdentity`; the issuers built with `New*IssuerWithKey`; a key resolver in `OIDCAuthenticator`;
  JWKS and discovery from the key set and the stored issuer; the `--token-issuer-url` flag (D3). 1.2 and 1.4 go
  green.
  — `JWTVerificationKeyResolver` (identity: kid map; external OIDC: one key, any kid); `NewWorkloadTokenIssuer` and
  `NewUserTokenIssuer` removed (no production caller left).
- [x] 2.3 `encryptedStorePrefixes`, the KEK-rotation and legacy-migration watches and write domains (D4). 1.5 goes
  green.
  — `storedEnvelopeArea` shared by both planners and countV1Envelopes; retirement scans encryptedStorePrefixes.
- [x] 2.4 `--max-user-token-ttl` in `handleUserToken` (D5); key confinement. 1.6 goes green.
  — `parseUserTokenTTL`; the flag refuses values below the 8h default, so the spec's 8h default always holds.
- [ ] 2.5 Deploy check `deploy-run:user-token-survives-restart`:
  - test/e2e/cluster_test.sh creates an `e2e-admin` user with a direct `--capability cluster.admin` grant and mints a
    token;
  - it calls `cca status` with `CCA_TOKEN` before and after the existing `cca-server` restart (cluster_test.sh:2157).

  The HA check `ha:user-token-cross-replica` goes in vm-ha/ha_test.sh: a token from replica 1 is accepted by replica
  2. It is verified on the next E2E HA run (per the user's rule).
  — open: the HA check is written (ha_test.sh Check 6b, also compares /oidc/jwks); the cluster_test.sh part is
  stopped: the only cca-server restarts are in the `--upgrade` Step 7, which deploy-testbed.yml never runs.
- [x] 2.6 Docs and graphs:
  - AUTHN_GRAPH and SECRETS_GRAPH;
  - Flow 37 in runtime-invariant-flows.md;
  - website/installation.md: replace the M280 note at step 3, and add `--max-user-token-ttl`;
  - design/security-model.md: no rotation command yet; a leaked key needs re-initialization until then;
  - the changelog text in the milestones.md row: tokens issued before this release must be re-created once.
  — done, plus design/cli-reference.md; the row already had the re-create sentence, the two flags were added.

## 3. Checks

- [x] 3.1 gofmt, golangci-lint, `go test -count=1 ./...`, the `etcd_integration` tests, and -race on security, api and
  cmd/cca.
  — 2026-11-26: gofmt clean; golangci-lint 0 issues (also with the etcd_integration tag); go test ./... all ok;
  etcd_integration security + cmd/cca ok; -race on security, api, cmd/cca ok; -race -count=30 on the race tests ok.
- [ ] 3.2 Independent review: `code-reviewer` with a patterns review against design/Anti-pattern.md, plus
  `security-audit`.
  - **At most two rounds.** After each round the main session stops, reports the verdict and its classification of
    each finding (fix, record or cut), and waits for the user's go.
  - **LOW findings are recorded** unless they are real defects in this milestone's goal.
  - **Stop-time hook findings are recorded only.**
- [ ] 3.3 One full Deploy Testbed run (job summary `full run`) whose log shows `deploy-run:user-token-survives-restart`
  passed; record the run ID.

## Workflow follow-up

- Release with `scripts/release.sh M283 <version>`, then archive (sync `api-token-signing`).
