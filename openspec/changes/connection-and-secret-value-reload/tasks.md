# Tasks

Implementation (groups 0–2) by the `lead-developer` subagent with **model Opus, effort low** passed on the call — third arm of the subagent trial (design/plans/subagent-implementation-trial.md); the main session checks its work and does groups 3–4.

## 0. Failing tests first

- [ ] 0.1 Agent tests (D4): `TestAgentRestartsWhenConnectionChanges`, `TestAgentRestartsWhenPullSecretChanges`, `TestAgentRestartsWhenSecretValueGenerationChanges`; extend `TestServiceConfigHashFormatUnchanged` with a granted secret at generation 1 (and without the generation fact) — hash bytes unchanged. Record each failing
- [ ] 0.2 `TestSecretGenerationRaisedOnlyOnValueChange` in `security/`: create → 1, same value → 1, new value → 2, delete removes it, re-encryption path leaves it. Record failing

## 1. Fix

- [ ] 1.1 D1: generation key in types/keys.go, `PutSecret`/`DeleteSecret` with the transaction guard; D3: node-agent read access to the key (check security/rbac.go)
- [ ] 1.2 D2: `computeServiceConfigHash` appends connection, pull secret and generation (> 1) lines; read errors return an error
- [ ] 1.3 Docs: Flow 16 (what the hash covers, rotation restarts consumers, all at once until M239), SECRETS_GRAPH.md (generation key and PutSecret), INSTANCE_LIFECYCLE_GRAPH.md, design/etcd-schema.md, website secrets page

## 2. Checks

- [ ] 2.1 gofmt, golangci-lint, `go test ./agent/ ./security/ ./api/ ./types/ -count=1`

## 3. Testbed

- [ ] 3.1 By hand (`deploy-run:secret-rotation`): a whoami service with a mounted secret; `cca secret set` a new value; the instance restarts and the mounted file shows the new value; setting the same value again restarts nothing

## 4. Close

- [ ] 4.1 Independent review: `code-reviewer` and `security-audit` (secret handling); record every finding as fixed or rejected with one line here
- [ ] 4.2 Trial numbers for M236; closing full Deploy Testbed run
