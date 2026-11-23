# Tasks

Milestone M266. Design: [design.md](design.md). Spec delta: [specs/fact-store-writes/spec.md](specs/fact-store-writes/spec.md).

## 1. Tests first (record each failing before section 2)

- [ ] 1.1 `TestMemoryStoreTransactionUnchangedPutBumpsRevision` (store): unchanged put in a transaction → new
  revision and a put event; sub-case `plain put skips` → `Put` of the same value keeps the revision, no event.
  Record the failure of the first part.
- [ ] 1.2 `TestAgentDoesNotRewriteUnchangedInstanceState` (agent): count puts/events on
  `observed/instance/{id}/state` over five reconcile cycles of a running instance with nothing changing → 0; sub-case
  `changed state is written` (stored `starting`, observed running → one write). Use a store that shows unchanged
  transactional puts (the plain memory store after 2.3; before it, record the failure with a counting wrapper on
  `Transaction`).
- [ ] 1.3 `TestRunnerDropsUnchangedPuts` (controllers): a test controller proposes an unchanged and a new put → only
  the new key is written and no event for the unchanged key; sub-case `nothing left` → no transaction is sent (count
  `Transaction` calls on a wrapper). Record the failures.

## 2. Implementation

- [ ] 2.1 D1: `publishInstanceStateUnlessStopped` returns true without a transaction when the stored state equals the
  observed state; doc comment updated (≤ 4 lines).
- [ ] 2.2 D2: `dropUnchangedPuts(changes, allFacts)` in `controllers/runner.go`, called after `enforceWriteDomain` and
  before `validateAndCapChanges`; scanned values looked up in a map. Before wiring it, grep every controller's
  `store.OpPut` changes for values that are timestamps or counters meant to bump a revision; record the result here.
- [ ] 2.3 D3: `MemoryStore` transactions treat an unchanged put as a write (new mod revision, same create revision, a
  put event; `transactionWouldMutateState` true). `MemoryStore.Put` unchanged. Remove `etcdLikeStore` from
  `scheduler/unplaced_facts_test.go` and run its test on the plain memory store.
- [ ] 2.4 Run `go test -count=1 ./...`. For every test that now fails, record here: test, cause, **loop fixed** or
  **test adjusted**, one line each. Stop and report to the main session if more than about ten tests fail or the
  causes reach unrelated subsystems.
- [ ] 2.5 Docs: graphs (CALL_GRAPH and INSTANCE_LIFECYCLE_GRAPH for the agent, CONTROLLER_DATA_FLOW_GRAPH, HA_GRAPH
  and EXTENSIBILITY_GRAPH for the runner), runtime-invariant-flows Flow 34 "Agent state publish" line (only when
  changed).

## 3. Checks

- [ ] 3.1 gofmt, golangci-lint, `go test -count=1 ./...`, `-race` on `store`, `agent`, `controllers`, `scheduler`.
- [ ] 3.2 Deploy cluster by hand, `deploy-run:unchanged-state-writes`, on the cluster a push run leaves:
  `cca watch observed/instance/` for 20 s with nothing applied → no `state` key put more than once; then
  `cca watch derived/` for 20 s → no key put more than once without a visible cause. Record commands and output here.
- [ ] 3.3 Independent review (CLAUDE.md): `code-reviewer` (Opus, effort high) on the milestone diff against this
  design and the spec, including the patterns review against `design/Anti-pattern.md`. Record each finding as fixed
  or rejected, with one line of reasoning.
- [ ] 3.4 Closing full Deploy Testbed run (job summary says `full run`). Record the run ID.
- [ ] 3.5 Update the milestones.md M266 row to the final scope, link this change from Phase 266, archive (syncing
  `fact-store-writes`), release with `scripts/release.sh`.
