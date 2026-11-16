# Tasks

## 0. Failing tests first

- [ ] 0.1 `TestAgentRestartsWhenConfigArrivesDuringStart` in `agent/`: a test runtime wraps the simulator and writes `DB_HOST=db.new.local` into the store inside its first `Start`; expect a second start and a changed stored hash. Record it failing
- [ ] 0.2 `TestAgentRestartsWhenFirstConfigArrivesAfterStart`: service with only an image, instance running; write the first env var; expect a restart. Record it failing

## 1. Fix

- [ ] 1.1 D1: `reconcileDesiredInstance` computes the config hash before any other read and passes it to `startInstanceAndPublishState`, which stores it instead of recomputing
- [ ] 1.2 D2: `computeServiceConfigHash` returns the hash of no inputs instead of `""`; check every caller; the two tests pass with the existing config/secret reload tests
- [ ] 1.3 Docs: Flow 16 (hash taken before the build, stored after the start; empty-input hash), `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md` start path and `detectConfigChange`

## 2. Close

- [ ] 2.1 gofmt, golangci-lint, `go test ./agent/ ./lang/ ./api/`
- [ ] 2.2 Independent review: `code-reviewer` and `security-audit` (secret delivery path); record every finding as fixed or rejected with one line here
- [ ] 2.3 A push run with WordPress passes (`deploy-run:push-fast`), then the closing full Deploy Testbed run
