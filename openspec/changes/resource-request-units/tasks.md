# Tasks: Resource requests are read with their units (M243)

Implementation goes to `lead-developer` (Opus, effort medium) via `openspec-apply-change`. Group 4 stays in the main
session. Read `design/graphs/SCHEDULING_GRAPH.md` and Flow 10 before changing code.

## 1. Failing tests first

- [ ] 1.1 Parser tests in `types/resource_quantity_test.go`: `TestParseCPUMillicoresAcceptsKubernetesForms`,
  `TestParseMemoryBytesAcceptsKubernetesForms`, and a refusal table (`512MB`, `-1`, `0.0005`, `1e3`, ` 1`, `""`,
  overflow, `1.5` bytes). Stubs return an error, so the tests compile and fail. Record the red output here.
- [ ] 1.2 Compiler tests: `TestCompileRefusesUnparseableResourceValue` (`512MB`, `cpu 0.0005`),
  `TestCompileRefusesUnparseableVerticalBounds`, `TestClassifyQoSComparesParsedQuantities`, and
  `TestApplyRefusesUnparseableTemplatedResourceValue` (asserts the error text and an unchanged store). Record red.
- [ ] 1.3 Scheduler tests:
  - `TestSchedulerLeavesUnitRequestLargerThanNodesUnplaced`
  - `TestSchedulerPacksUnitRequestsWithinNodeCapacity`
  - `TestSchedulerWatchesEffectiveResourceRequest`, through the runner's fact scan (`scanFactsForController`), not
    hand-built facts
  - `TestSchedulerRefusesUnparseableStoredRequest`: no preemption, no rebalance move, and the running instance keeps
    its node

  Record red.
- [ ] 1.4 Controller and agent tests:
  - `TestClusterScaleCountsUnitRequestShortage`
  - `TestClusterScaleIgnoresInvalidResourceRequest`, with zero alive nodes
  - `TestVerticalAutoscalerWritesQuantitiesWithUnits`
  - `TestVerticalAutoscalerRewritesBareRecommendation`
  - `TestRemovingVerticalScalingRestoresDesiredRequest`
  - `TestAgentReadsEffectiveResourceQuantities`

  Record red. Some may already pass; say which and why.

## 2. Shared parser and apply validation

- [ ] 2.1 `types/resource_quantity.go` (D1, D2), using integer arithmetic and named constants for the suffix
  multipliers. Verify: 1.1 is green.
- [ ] 2.2 `lang/compiler.go` (D3): `validateServiceResourceValues` in `compileServiceDeclaration`, and `classifyQoS`
  compares parsed values. Verify: 1.2 is green, and `go test ./lang/... ./cmd/cca/...` passes.
- [ ] 2.3 Update the bare-value configs and fixtures listed in proposal.md (BREAKING item) to unit form, keeping each
  test's intended meaning (`cpu 1000` that meant millicores becomes `1000m`; `memory 2048` that meant MiB becomes
  `2048Mi`). Verify: every touched package's tests pass; list the files here.
- [ ] 2.4 Docs: a units table in `design/dsl-reference.md` (forms, what is refused, bare CPU = cores, bare memory =
  bytes); `design/storage.md` example in unit form.

## 3. Readers use the parser

- [ ] 3.1 Scheduler (D4, D5):
  - `Watch()` adds `ScanEffectiveServices`.
  - Effective-first reads; MiB rounded up.
  - `UnplacedInvalidResourceRequest`, checked before the `aliveCount == 0` return.
  - Skipped in placement and `attemptPreemptions`; pinned in `extractRebalancePinnedServices`.
  - A derived fact listing the service and value, shown by `cca status`.
  - The warning logged once per value.

  Verify: 1.3 is green, and `go test ./scheduler/... ./cmd/cca/...` passes.
- [ ] 3.2 Controllers (D6, D7):
  - Eviction: effective-first; MiB; node facts as plain integers; skips and logs an unreadable service.
  - Split `collectResourceValuesBySuffix` into CPU and memory readers.
  - Autoscaler: shared-parser bounds; writes units; rewrites bare recommendations every cycle; deletes intents for
    services without vertical facts.
  - `resolveEffectiveResourceChanges` deletes effective values without an intent.
  - Delete `parseResourceQuantity`.

  Verify: 1.4 (controller tests) is green, and `go test ./controllers/...` passes.
- [ ] 3.3 Agent (D6): `lookupServiceResourcesFromStore` uses the shared parser and logs once on error. Delete
  `parseMillicores`/`parseMemoryBytes` and move their tests. Verify: `TestAgentReadsEffectiveResourceQuantities` is
  green, and `go test ./agent/...` passes.
- [ ] 3.4 Tenant (D6): usage and fair share via `ParseCPUMillicores`; unreadable values skipped and logged. Verify: a
  test with `cpu 0.5` ×2 gives 1000, and `go test ./tenant/...` passes.
- [ ] 3.5 Graphs and flows:
  - SCHEDULING_GRAPH.md: Watch list, effective keys, new reason, preemption and rebalance changes, line numbers.
  - Flow 10: effective-first readers, units, stale-effective deletion.
  - The controller and agent graphs: removed and split functions.

  Grep for every removed function name. Verify: no stale references remain.
- [ ] 3.6 Checks: `gofmt -l .` is empty, `golangci-lint run ./...` is clean, and `go test ./...` passes. Paste the
  results here.

## 4. Testbed, review, release (main session)

- [ ] 4.1 Testbed by hand (`deploy-run:resource-capacity`, on the cluster a push run leaves). Record commands and
  output here:
  - **Refused apply:** apply `memory 512MB`; the apply is refused with field and value, and the store is unchanged.
  - **Request no node can meet:** apply a new service with `memory 64Gi`. The instance is unplaced, `cca describe
    service` shows `insufficient_capacity`, and the cluster-scale controller writes a capacity request.
  - **M241 4.1 capacity case:** roll a running service to a config that requests more memory than any node, with
    `progress_deadline 2m`. The rollout reason names `insufficient_capacity`; after 2 minutes `rollback_reason` names
    it too.
  - **Filling a worker:** `cpu 750m` with one instance more than a worker fits. The extra instance is unplaced with
    `insufficient_capacity`, and no worker hosts more requests than its capacity (`cca get instances`, per-node count ×
    request). `cca describe node` still shows detected capacity, the stated non-goal.
  - **No vertical scaling:** confirm no deploy workload declares `scale vertical` (D8).
- [ ] 4.2 Independent review: `code-reviewer` (Opus, effort high) reviews the milestone diff against this design and
  the spec. Record each finding as fixed or rejected, with one line of reasoning.
- [ ] 4.3 Closing full Deploy Testbed run (job summary says `full run`); every default workload still places. Record
  the run ID.
- [ ] 4.4 Update the milestones.md M243 row's changelog text to the final scope, with the BREAKING items (bare
  numbers, apply refusals, vertical scaling active), then run `scripts/release.sh M243 <version>`.

## Workflow follow-up

- Archive with `openspec-archive-change`, syncing `specs/resource-requests` into `openspec/specs/`.
- Link the change from milestones.md Phase 243.
- Propose the follow-up milestone for allocated requests in `cca describe node` / `cca status`.

## Review findings on the proposal (2026-11-19)

Three `/code-review` runs and one Codex run, before implementation. Each finding was checked in the code:
- **Fixed in the plan:**
  - Scheduler `Watch()` lacks the effective prefix (D4).
  - Stale intent and effective values are never deleted (D7).
  - The legacy bare intent is never rewritten (D7).
  - `cpu -1` is caught by the lexer (D3, spec).
  - The zero-count contradiction (D5, stated exception).
  - Eviction reads desired only and parses node facts the same way (D6).
  - Invalid check after the `no_nodes` return (D5).
  - The by-hand check ID notation.
  - Bare memory is also BREAKING (proposal).
  - Preemption and rebalance for invalid services (D5).
  - Per-reader parse-error rules (D6).
  - Vertical scaling becomes active (D8).
  - `collectResourceValuesBySuffix` mixes CPU and memory (D6).
  - The agent scenario had no agent test (spec).
  - `cca describe node` cannot show allocation (task 4.1; non-goal).
  - The tenant quota unit is millicores (proposal).
- **Codex:** no findings (it reviews code diffs only).
