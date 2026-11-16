# Tasks

## 0. Failing tests first

- [x] 0.1 `TestAgentRestartsWhenConfigArrivesDuringStart` in `agent/`: a test runtime wraps the simulator and writes `DB_HOST=db.new.local` into the store inside its first `Start`; expect a second start and a changed stored hash. Record it failing — `agent/config_hash_test.go` (`lateFactRuntime`); asserts a second start. Failed before the fix: `timed out waiting for: instance restarted after the env var written during its start`
- [x] 0.2 `TestAgentRestartsWhenFirstConfigArrivesAfterStart`: service with only an image, instance running; write the first env var; expect a restart. Record it failing — failed before the fix: `timed out waiting for: instance restarted after the first env var`

## 1. Fix

- [x] 1.1 D1: `reconcileDesiredInstance` computes the config hash before any other read and passes it to `startInstanceAndPublishState`, which stores it instead of recomputing — `configHashBeforeBuild`, taken right after the image lookup (the image is not a hash input)
- [x] 1.2 D2: `computeServiceConfigHash` returns the hash of no inputs instead of `""`; check every caller; the two tests pass with the existing config/secret reload tests — two callers (start path, `detectConfigChange`); its `currentHash == ""` guard became dead and was removed; both new tests and `TestAgentRestartsOnConfigChange`, `TestConfigChangeRestartDropsMaterialAndMissingSecretRetriesSooner` pass
- [x] 1.3 Docs: Flow 16 (hash taken before the build, stored after the start; empty-input hash), `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md` start path and `detectConfigChange`

## 2. Close

- [x] 2.1 gofmt, golangci-lint, `go test ./agent/ ./lang/ ./api/` — gofmt clean, golangci-lint `./agent/` 0 issues, all three pass
- [x] 2.2 Independent review: `code-reviewer` and `security-audit` (secret delivery path); record every finding as fixed or rejected with one line here — 2026-11-16 on 6197f28..f56e635. `code-reviewer`: request changes, one medium (core fix confirmed: every hashed input is read after the hash; early returns store nothing). `security-audit`: no defect in the change (grant add/remove during a start now always restarts and re-fetches). Outcome:
  - fixed (medium, code-reviewer) — a store read error at compare time hashed as "no config" and stopped every running instance with config (the old `== ""` guard had hidden it): `computeServiceConfigHash` now scans `config/` and `secret/` itself and returns an error; `detectConfigChange` compares nothing on error, the start path skips the cycle. Failing-first test `TestAgentKeepsInstanceWhenConfigReadFails` (instance was gone after one blip); `TestServiceConfigHashFormatUnchanged` pins the hash bytes so older agents' hashes still match after an upgrade; resolvers share `configFactsUnderPrefix`
  - opened M236 (security-audit, pre-existing) — connection facts and the pull secret are not in the hash, so a late or retargeted connection never reaches a running instance; secret values are not in it either (open question in Phase 236). Flow 16 now states what the hash covers
  - rejected for M235 (low, security-audit) — a node agent can forge another instance's `config-hash` and force a restart: agents can write all of `observed/instance/` today, the scope of M181 (Store Access Isolation)
  - noted (security-audit, pre-existing) — the hash of env values is unsalted and readable with `observed/` read; every role that reads it also reads `desired/`
  - fixed (nit, code-reviewer) — graph line numbers and call-site vs definition
  - noted (nit, code-reviewer) — tests count starts rather than checking the new value reaches the restarted container; removing all of a service's config now restarts it (intended, spec "first config" mirror)
- [ ] 2.3 A push run with WordPress passes (`deploy-run:push-fast`), then the closing full Deploy Testbed run — by hand first, 2026-11-16 with the db3ebbc binary (push profile: whoami + WordPress, slow groups skipped):
  - run 1 failed: both WordPress containers had all nine secret files (the fix delivered them), but MariaDB answered `Access denied … (using password: YES)`: its data on the NFS volume was initialised by an earlier run, and `wordpress.workload` generated new random passwords every run. Test-setup flaw from M231, not CCattler → ff8cd5e: secrets created once per deploy host (`~/.cca-test/wordpress-secrets.env`, 0600) and reused; the user emptied `/srv/exports/cca-volumes/wordpress-db/` once. Also fixed: an empty redirect shifted the parsed fields in `wordpress_checks.sh`
  - run 2 passed: `TEST PASSED`, 193 s, WordPress answers 302 to `wp-admin/install.php` through the nginx VIP and the gate. No `config changed, restarting` in the agents' logs, so the race did not occur in this run: the race itself is proven by `TestAgentRestartsWhenConfigArrivesDuringStart`, the run shows no regression
  - push run 43947682844 on c2e85a2 passed (`deploy-run:push-fast`): `workload=whoami,wordpress`, WordPress answers 200 through the VIP and the gate (the site installed by the user between runs survived the redeploy: data on NFS, stable secrets). Closing full run still to come
