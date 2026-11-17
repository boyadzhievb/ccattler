# Tasks

Implementation (groups 0–2) by the `lead-developer` subagent with **model Opus, effort low** passed on the call — third arm of the subagent trial (design/plans/subagent-implementation-trial.md); the main session checks its work and does groups 3–4.

## 0. Failing tests first

- [x] 0.1 Agent tests (D4): `TestAgentRestartsWhenConnectionChanges`, `TestAgentRestartsWhenPullSecretChanges`, `TestAgentRestartsWhenSecretValueGenerationChanges`; extend `TestServiceConfigHashFormatUnchanged` with a granted secret at generation 1 (and without the generation fact) — hash bytes unchanged. Record each failing — the three restart tests failed before the fix ("timed out waiting for: instance restarted after the change", one start only); the extended format test passed before and after (bytes unchanged), as intended
- [x] 0.2 `TestSecretGenerationRaisedOnlyOnValueChange` in `security/`: create → 1, same value → 1, new value → 2, delete removes it, re-encryption path leaves it. Record failing — failed at "create: generation \"\", want \"1\""; added `TestSecretGenerationMissingFactTreatedAsOne` (pre-M236 secret → 2 on first change), failed with "generation \"\", want 2"

## 1. Fix

- [x] 1.1 D1: generation key in types/keys.go, `PutSecret`/`DeleteSecret` with the transaction guard; D3: node-agent read access to the key (check security/rbac.go) — `KeyDesiredSecretGeneration`; PutSecret/DeleteSecret use `Transaction` guarded on the `secrets/<name>` revision; rotation/migration write envelopes directly (never PutSecret). D3: node-agent role already has read+watch on `desired/` (rbac.go BuiltinRoles), no change; SecretStore uses the raw store
- [x] 1.2 D2: `computeServiceConfigHash` appends connection, pull secret and generation (> 1) lines; read errors return an error — `writeSecretReferenceEntries` reuses `loadServiceSecretRequirements` (now also records connection definitions and the pull secret name)
- [x] 1.3 Docs: Flow 16 (what the hash covers, rotation restarts consumers, all at once until M239), SECRETS_GRAPH.md (generation key and PutSecret), INSTANCE_LIFECYCLE_GRAPH.md, design/etcd-schema.md, website secrets page — website page is website/concepts/security.md ("Changing a secret value")

## 2. Checks

- [x] 2.1 gofmt, golangci-lint, `go test ./agent/ ./security/ ./api/ ./types/ -count=1` — clean, 0 lint issues, all pass (also with -race and ./tenant/); go build/vet clean

## 3. Testbed

- [ ] 3.1 By hand (`deploy-run:secret-rotation`): a whoami service with a mounted secret; `cca secret set` a new value; the instance restarts and the mounted file shows the new value; setting the same value again restarts nothing

## 4. Close

- [ ] 4.1 Independent review: `code-reviewer` and `security-audit` (secret handling); record every finding as fixed or rejected with one line here
- [ ] 4.2 Trial numbers for M236; closing full Deploy Testbed run
