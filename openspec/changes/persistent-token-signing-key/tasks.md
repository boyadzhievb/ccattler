# Tasks

Milestone M283. Design: [design.md](design.md). Spec: [api-token-signing](specs/api-token-signing/spec.md).
Graphs to read before the code: design/graphs/AUTHN_GRAPH.md and design/graphs/SECRETS_GRAPH.md.

## 1. Tests first (each recorded failing before any implementation)

- [ ] 1.1 Bootstrap: `TestTokenSigningKeyConcurrentFirstStartConverges` (memory store, plus `etcd_integration` with
  the real-etcd harness) and `TestTokenSigningKeyLostRaceLoadsWinner`.
- [ ] 1.2 Restart and replicas: `TestUserTokenSurvivesServerRestart`, `TestUserTokenAcceptedByOtherReplica` and
  `TestJWKSIdenticalAcrossReplicas`. Two API servers on one store; the issuer and JWKS are compared.
- [ ] 1.3 Issuer and fail-closed:
  - `TestServerRefusesDifferentTokenIssuerURL`;
  - `TestServerRefusesStartWhenSigningKeyMissing`;
  - `TestServerRefusesCorruptSigningKey`;
  - `TestServerRefusesSigningKeyWithWrongRootKey`.

  Each also asserts that the store is unchanged.
- [ ] 1.4 Key ID: `TestInternalAuthenticatorSelectsKeyByKid`, `TestJWKSPublishesEveryKey` and
  `TestInternalAuthenticatorRefusesUnknownKid` (a two-key set written to the store directly).
- [ ] 1.5 Keyring operations: `TestKEKRotationRewrapsTokenSigningKey`, `TestKEKRetirementWaitsForTokenSigningKey` and
  `TestLegacyMigrationMovesTokenSigningKeyToV2`.
- [ ] 1.6 Confinement, lifetime and revocation: `TestTokenSigningKeyNeverExposed`,
  `TestUserTokenTTLAboveMaximumRefused` and `TestDeletedUserTokenRefusedAfterRestart`.

## 2. Implementation

- [ ] 2.1 Store keys (types/keys_secret.go) and `loadOrCreateTokenSigningIdentity` (new
  security/token_signing_key.go), following D1 and D2. It is called after the keyring is ready and before the
  controllers and the API start. 1.1 and 1.3 go green.
- [ ] 2.2 `TokenSigningIdentity`; the issuers built with `New*IssuerWithKey`; a key resolver in `OIDCAuthenticator`;
  JWKS and discovery from the key set and the stored issuer; the `--token-issuer-url` flag (D3). 1.2 and 1.4 go
  green.
- [ ] 2.3 `encryptedStorePrefixes`, the KEK-rotation and legacy-migration watches and write domains (D4). 1.5 goes
  green.
- [ ] 2.4 `--max-user-token-ttl` in `handleUserToken` (D5); key confinement. 1.6 goes green.
- [ ] 2.5 Deploy check `deploy-run:user-token-survives-restart`:
  - test/e2e/cluster_test.sh creates an `e2e-admin` user with a direct `--capability cluster.admin` grant and mints a
    token;
  - it calls `cca status` with `CCA_TOKEN` before and after the existing `cca-server` restart (cluster_test.sh:2157).

  The HA check `ha:user-token-cross-replica` goes in vm-ha/ha_test.sh: a token from replica 1 is accepted by replica
  2. It is verified on the next E2E HA run (per the user's rule).
- [ ] 2.6 Docs and graphs:
  - AUTHN_GRAPH and SECRETS_GRAPH;
  - Flow 37 in runtime-invariant-flows.md;
  - website/installation.md: replace the M280 note at step 3, and add `--max-user-token-ttl`;
  - design/security-model.md: no rotation command yet; a leaked key needs re-initialization until then;
  - the changelog text in the milestones.md row: tokens issued before this release must be re-created once.

## 3. Checks

- [ ] 3.1 gofmt, golangci-lint, `go test -count=1 ./...`, the `etcd_integration` tests, and -race on security, api and
  cmd/cca.
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
