# Tasks

Implementation (groups 0–2) by the `lead-developer` subagent with **model Opus, effort low** passed on the call — third arm of the subagent trial (design/plans/subagent-implementation-trial.md); the main session checks its work and does groups 3–4.

## 0. Failing tests first

- [x] 0.1 Agent tests (D4): `TestAgentRestartsWhenConnectionChanges`, `TestAgentRestartsWhenPullSecretChanges`, `TestAgentRestartsWhenSecretValueGenerationChanges`; extend `TestServiceConfigHashFormatUnchanged` with a granted secret at generation 1 (and without the generation fact) — hash bytes unchanged. Record each failing — the three restart tests failed before the fix ("timed out waiting for: instance restarted after the change", one start only); the extended format test passed before and after (bytes unchanged), as intended
- [x] 0.2 `TestSecretGenerationRaisedOnlyOnValueChange` in `security/`: create → 1, same value → 1, new value → 2, delete removes it, re-encryption path leaves it. Record failing — failed at "create: generation \"\", want \"1\""; added `TestSecretGenerationMissingFactTreatedAsOne` (pre-M236 secret → 2 on first change), failed with "generation \"\", want 2"

## 1. Fix

- [x] 1.1 D1: generation key in types/keys.go, `PutSecret`/`DeleteSecret` with the transaction guard; D3: node-agent read access to the key (check security/rbac.go) — `KeyDesiredSecretGeneration`; PutSecret/DeleteSecret use `Transaction` guarded on the `secrets/<name>` revision; rotation/migration write envelopes directly (never PutSecret). D3: node-agent role already has read+watch on `desired/` (rbac.go BuiltinRoles), no change; SecretStore uses the raw store
- [x] 1.2 D2: `computeServiceConfigHash` appends connection, pull secret and generation (> 1) lines; read errors return an error — `writeSecretReferenceEntries` reuses `loadServiceSecretRequirements` (now also records connection definitions and the pull secret name)
- [x] 1.3 Docs: Flow 16 (what the hash covers, rotation restarts consumers, all at once until M239), SECRETS_GRAPH.md (generation key and PutSecret), INSTANCE_LIFECYCLE_GRAPH.md, design/etcd-schema.md, website secrets page — website page is website/concepts/security.md ("Changing a secret value")

- [x] 1.4 Review rework (code review + security audit, verified by the main session); tests written first, first failures:
  - R1 stale material on restart: `TestAgentRefetchesSecretsWhenRestartingAfterValueChange` timed out ("instance started with the new secret value", old material reused) → `dropSecretMaterialIfConfigChanged` in `reconcileDesiredInstance`
  - R2 generation tombstone: `TestSecretGenerationRaisedOnlyOnValueChange` failed "after delete: generation \"\", want 3 (kept)"; `TestSecretDeleteKeepsConsumerConfigHash` failed (hash changed on delete) → DeleteSecret keeps the fact, create continues from it
  - R3 API hiding: `TestStateAPIHidesSecretGenerations` failed (scans returned the fact, single get HTTP 200) → `unreadableKeyPrefixes` in api/state_access.go
  - R4 admin-only write: `TestRequiredCapabilityForFactWrite` failed ("workload.create", want "cluster.admin") → added to `clusterAdminFactPrefixes`
  - R5 409: `TestSecretPutReturnsConflictWhenChangedConcurrently` failed (HTTP 500) → `ErrSecretChangedConcurrently` exported, mapped to 409
  - R6 unreadable value: `TestSecretPutFailsWhenCurrentValueUnreadable` failed ("put over an unreadable value succeeded") → returns an error
  - R7 connection mount path: `TestAgentRestartsWhenConnectionMountPathChanges` timed out → `connection:<name>@<mount>=` ; shared `types.InitialSecretGeneration`; graph line numbers fixed
  - Docs: design.md (D1 tombstone/409/unreadable, D1a material drop, D1b API hiding, D2 mount path), Flow 16, SECRETS_GRAPH.md, INSTANCE_LIFECYCLE_GRAPH.md, website security page

## 2. Checks

- [x] 2.1 gofmt, golangci-lint, `go test ./agent/ ./security/ ./api/ ./types/ -count=1` — clean, 0 lint issues, all pass (also with -race and ./tenant/); go build/vet clean

## 3. Testbed

- [x] 3.1 By hand (`deploy-run:secret-rotation`): a whoami service with a mounted secret; `cca secret set` a new value; the instance restarts and the mounted file shows the new value; setting the same value again restarts nothing — first run 2026-11-17 with the 549db73 binary (nginx:1.27, secret `m236.token`, running instance): `value-one` → set `value-two` → instance restarted (one `config changed, restarting` in the agent log), file reads `value-two`; same value again → no restart. Passed for a RUNNING instance; the review then found the stale-material case for non-running instances (rework round 1) — repeat after the rework, including a crash-looping consumer. Repeated with the a3053f5 binary on a fresh cluster: (A) `rotation` running with `value-one`, `fixme` (nginx, `command "sh /run/secrets/script"`, secret `m236.script` = `exit 1`) in `crash-loop-backoff`; (B) set `value-two` and script `exec sleep 3600` → `rotation` reads `value-two`, **`fixme` running with the new script** (`Restarts: 2`) — the stale-material defect would have kept it crash-looping; (C) `cca secret delete m236.token` → `rotation` stays running; (D) set `value-three` after the delete → `rotation` restarted and reads `value-three` (generation continued past the delete). The state API hiding was not checked on the cluster (unauthenticated curl, empty answer); `TestStateAPIHidesSecretGenerations` covers it

## 4. Close

- [x] 4.1 Independent review: `code-reviewer` and `security-audit` (secret handling); record every finding as fixed or rejected with one line here — 2026-11-17 on 0c78680..549db73 (both Sonnet, effort high). Security posture adequate: no secret value, digest, length or cross-secret equality in the generation, hash or logs; no new write path. All fixes in rework round 1 (task 1.4, implementing subagent), verified by the main session:
  - fixed (high, code-reviewer) — a restart of a placed but not-running instance (crash loop, agent restart) reused the cached secret material (names compared, never values) and stored the new hash: stuck on the old value. `dropSecretMaterialIfConfigChanged`; proven on the testbed (3.1 B, `fixme`)
  - fixed (medium, both; from the change's own design) — delete + set reset the generation to 1, so a rotation by delete and re-create restarted nothing; deleting a secret at generation > 1 restarted consumers into waiting: `DeleteSecret` keeps the generation as a name-only tombstone, create continues from it (testbed 3.1 C, D)
  - fixed (low-medium, security-audit; design) — generation keys readable through the raw state API by any team-scoped user (all secret names, rotation timing): hidden from scans, gets and watches
  - fixed (low, security-audit) — the generation prefix needs `cluster.admin` to write through apply (deny-by-default for future paths)
  - fixed (low, code-reviewer) — a concurrent change (e.g. keyring rotation) during `cca secret set` answered 500: now 409 "secret changed concurrently, retry"
  - fixed (low, code-reviewer) — an unreadable current value counted as changed (a transient keyring problem would restart every consumer): now an error
  - fixed (low, code-reviewer) — a connection's mount path was not in the hash: `connection:<name>@<mount>=`; shared `types.InitialSecretGeneration`; graph line numbers
  - noted (low, security-audit) — orphaned generation facts for gate keys deleted outside `DeleteSecret`: now the intended tombstone, never read for them
  - noted (low, security-audit) — `PutSecret` lets a write-only caller learn whether a set matched (no restart): not a practical oracle, every wrong guess overwrites the value
  - noted (implementer) — `DeleteSecret` on a concurrent change still answers 500 (only set was asked for 409); tombstones are never removed (hidden from the API)
  - noted (code-reviewer) — the hash now reads more facts per running instance per cycle; caching per service per cycle if it shows in benchmarks
- [x] 4.2 Trial numbers for M236; closing full Deploy Testbed run — closing full run 44074905065 on b646480: `run_kind=full`, `skipped check groups: none`, deploy `TEST PASSED`, replay `32 passed, 0 failed, 0 skipped`, 595 s. Trial numbers in design/plans/subagent-implementation-trial.md (context figure after release from the user)
