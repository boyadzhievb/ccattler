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
- [x] 2.5 Deploy check `deploy-run:user-token-survives-restart`:
  - the deploy test creates an `e2e-admin` user with a direct `--capability cluster.admin` grant and mints a
    token;
  - it calls `cca status` with `CCA_TOKEN` before and after the existing `cca-server` restart of gate-tls T12
    (test/e2e/gate_tls_checks.sh, `gate_tls_check_restart_stability`).

  The HA check `ha:user-token-cross-replica` goes in vm-ha/ha_test.sh: a token from replica 1 is accepted by replica
  2. It is verified on the next E2E HA run (per the user's rule).
  - **Changed 2026-11-26 (main session):** the reference was cluster_test.sh:2157. That restart is in the `--upgrade`
    Step 7, which deploy-testbed.yml never runs, so the check could not appear in the full run (3.3). The check moved
    to T12's restart, which the default gate-tls group runs.
  — 2026-11-26: done in gate_tls_checks.sh. The token is minted into a root-only file on the control node, 0600, and
    read into CCA_TOKEN by the remote shell, so it is never printed or in an argv. Cleanup removes the file and the
    user. shellcheck: no new findings. Stub harness (scratchpad m283-harness.txt): PASS when the token works; FAIL
    with cleanup when it is refused after the restart. The HA check is ha_test.sh Check 6b, which also compares
    /oidc/jwks.
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

**Main session (2026-11-26), on the implementation report:**
- **Accepted, the agent's choices:**
  - `--max-user-token-ttl` cannot be below the 8 h default;
  - only API replicas load the identity (`--controllers-only` skips it);
  - the external OIDC authenticator keeps one key and ignores `kid`, as before.
- **Recorded, out of scope:**
  - `/oidc/jwks` and `/.well-known/openid-configuration` require authentication (api/server.go:313, :335: the
    exempt paths are `/healthz`, `/metrics` and `/api/enroll`), although OIDC expects them public. Nothing outside
    the cluster uses them today, since `CredentialBrokerController` has no production caller. To be fixed with the
    credential-broker or federation follow-up.
  - `WorkloadTokenIssuer.PublicKey()` now has only test callers (minor dead code); `KeyID()` and
    `UserTokenIssuer.PublicKey()` were test-only already. Same follow-up.

### 3b. Review round 1 (2026-11-26, on c017fe7..75646c4)

**security-audit:** no CRITICAL or HIGH findings.
- **What holds, checked in the code and in runs:** bootstrap, fail-closed loading, selection by kid, the issuer
  refusal, M216 revocation, the keyring operations and the TTL limit.
- **Red:** the new tests fail to build on c017fe7; with the D4/D5 changes removed, the 6 keyring and TTL tests fail.
- **Mutations refused:** `alg` none or HS256, a foreign key under the active kid, an unknown, missing or non-string
  kid. Deleting the marker, the marker and `active`, or `active` alone refuses startup. A lost race always loads the
  winner's key.
- **Evidence:** the evidence file (scratchpad m283-security-audit-r1.txt) was lost in a scratchpad reset; the
  findings below are copied from the audit's report.

**code-reviewer** (rerun 2026-11-27; m283-code-review-r1.txt; Codex job review-mwysipfk-6e2lvt): request changes, for
SA1-M1 only.
- **No new HIGH or MEDIUM.** What holds: bootstrap, the lost-race re-read, fail-closed, kid selection, the start order,
  HA, and the rotation and migration watches and write domains.
- **Tests:** the M283 tests pass on HEAD; the `etcd_integration` tier passes on a downloaded, SHA-checked etcd
  v3.5.36.
- **The deploy check** runs in the default full run (deploy-testbed.yml:13 → cluster_test.sh:1946 →
  gate_tls_checks.sh:610) and can pass.
- **Codex's 2 findings** both equal SA1-L5.

More findings (all LOW):

| ID | Finding | Proposed handling |
|---|---|---|
| SA1-M1, more evidence | On a legacy-key cluster, an API replica without secret-master.key can neither create nor open the v1 signing key, so the API refuses to start (server_secret_keys.go:60, envelope_codec.go:116-117). Before M283 only secret operations failed there | The same fix: always seal v2 |
| CR1-L1 (CONFIRMED by mutation) | The cmd/cca tests rebuild the start sequence (server_token_signing_test.go:84-105). Removing `attachTokenSigningIdentity` (command_server.go:655) passes every unit test; removing `SetMaxUserTokenTTL` (server_token_signing.go:81) is caught by nothing, so a custom `--max-user-token-ttl` could be silently ignored | Fix: test through the production start path |
| CR1-L2 (CONFIRMED by mutation) | No test for an unreadable non-active key; skipping its open error (token_signing_key.go:182-186) passes | Fix, with SA1-L2 |
| CR1-L3 (CONFIRMED) | The proposal promises a real-etcd restart case; only concurrent start exists (token_signing_key_etcd_test.go:105) | Fix: add the restart case |
| CR1-L4 (CONFIRMED, pre-existing) | `parseUserTokenTTL` accepts "-1h" and "0s", which `MintUserToken` turns into 8 h | Record |
| CR1 style (CONFIRMED) | Receivers `identity`, `signingArea` and `singleKey` don't match their types (a CLAUDE.md rule); the same internal-authenticator config is built by hand in 3 places instead of `BuildInternalTokenAuthenticator` | Fix: rule violations, in the same rework |

| ID | Finding | Proposed handling (waits for the user's go) |
|---|---|---|
| SA1-M1 (MEDIUM, CONFIRMED) | On a cluster still on the legacy secret key, the signing key is sealed as a v1 envelope with the legacy key (token_signing_key.go:131 → `SealEnvelopeForKey`, envelope_codec.go:111-120). A probe decrypted the private key with the legacy key alone. Legacy clusters install secret-master.key on nodes, and nodes have full etcd access until M181, so a compromised node can take the signing key. v1 envelopes are also not bound to their store key | Fix: always seal the signing key as v2 with the active KEK, which exists in legacy mode too. Agents never read it. A test proves a legacy-mode cluster stores a v2 envelope |
| SA1-L1 (CONFIRMED by mutation) | No test covers "key/* present, marker missing": disabling `refuseWithoutMarker` (:109) leaves every test green | Fix: the test for this failure-table row |
| SA1-L2 (CONFIRMED by mutation) | Removing the kid-matches-key check (:212) or the P-256 check, or falling back to the active key for an unknown kid (:235), leaves the tests green. The code is correct today | Fix: three small tests |
| SA1-L3 (CONFIRMED, by design) | Deleting all of `keyring/token-signing/` makes the next start create a new identity (D6 cannot tell it apart from a first start after an upgrade); no audit record of creation | Record. Possibly an audit record or log line on creation |
| SA1-L4 (CONFIRMED) | `--max-user-token-ttl` has no upper bound (server_token_signing.go:34): 87600h is accepted | Record, or a cap |
| SA1-L5 (CONFIRMED) | Test scripts: gate_tls_checks.sh:524 exits without cleanup on a failed restart; ha_test.sh:261 puts the token in an ssh argv; ha_test.sh:257 aborts under `set -e` before cleanup | Fix: they are this milestone's own check scripts, and the argv leak breaks its own rule |

**User decision (2026-11-27, "a"):** wait for the weekly usage reset (Nov 29, 20:00 Europe/Sofia), then run the
round-1 rework as proposed above (every Fix row; the Record rows stay recorded) and review round 2 (the last)
back to back.

**By-hand check on a real host (main session, 2026-11-27; iteration evidence, not the closing run).**
- **Host and build:** a fresh Debian 13 VM (192.168.100.103), set up by the guide with a `cca` build of master
  230cb72 (code as at 75646c4).
- **Identity:** the server logged "Token signing identity loaded (kid BbZCiiub84s, issuer https://debian1:9770)".
- **The guide's step 3:**
  - `cca user create tester`, a direct `--capability cluster.admin` grant, then
    `export CCA_TOKEN=$(sudo cca user token tester --ttl 1h)`;
  - `cca status` without sudo succeeded (exit 0);
  - after `systemctl restart cca-server`, the same token succeeded again (exit 0), and the server loaded the same kid.
- **TTL limit:** `cca user token tester --ttl 25h` was refused: "ttl 25h0m0s is above the maximum user token
  lifetime 24h0m0s (cca server --max-user-token-ttl)".

## Workflow follow-up

- Release with `scripts/release.sh M283 <version>`, then archive (sync `api-token-signing`).
