# Tasks: Resource requests are read with their units (M243)

Implementation goes to `lead-developer` (Opus, effort medium) via `openspec-apply-change`. Group 4 stays in the main
session. Read `design/graphs/SCHEDULING_GRAPH.md` and Flow 10 before changing code.

## 1. Failing tests first

- [x] 1.1 Parser tests in `types/resource_quantity_test.go`: `TestParseCPUMillicoresAcceptsKubernetesForms`,
  `TestParseMemoryBytesAcceptsKubernetesForms`, and a refusal table (`512MB`, `-1`, `0.0005`, `1e3`, ` 1`, `""`,
  overflow, `1.5` bytes). Stubs return an error, so the tests compile and fail. Record the red output here.
  - Red (stubs): `go test ./types/ -run 'TestParse|TestFormat'` →
    `--- FAIL: TestParseCPUMillicoresAcceptsKubernetesForms`, `--- FAIL: TestParseMemoryBytesAcceptsKubernetesForms`,
    `--- FAIL: TestParseResourceQuantitiesRefuseOtherForms` (stub errors do not name the value),
    `--- FAIL: TestFormatResourceQuantitiesWithUnits`, `--- FAIL: TestFormattedQuantitiesParseBack`; `FAIL types`.
  - The refusal table also has `1.1Gi` (1181116006.4 bytes, finer than 1 byte, so refused per D2/spec).
- [x] 1.2 Compiler tests: `TestCompileRefusesUnparseableResourceValue` (`512MB`, `cpu 0.0005`),
  `TestCompileRefusesUnparseableVerticalBounds`, `TestClassifyQoSComparesParsedQuantities`, and
  `TestApplyRefusesUnparseableTemplatedResourceValue` (asserts the error text and an unchanged store). Record red.
  - Red (`lang/resource_values_test.go`): `TestCompileRefusesUnparseableResourceValue`: want an error naming
    ["web" memory 512MB], got none; `TestCompileRefusesUnparseableVerticalBounds`: want an error naming
    ["web" cpu max lots], got none; `TestClassifyQoSComparesParsedQuantities`: qos_class = "Burstable", want
    Guaranteed; `TestApplyRefusesUnparseableTemplatedResourceValue`: want an error naming ["db" cpu half], got none.
- [x] 1.3 Scheduler tests:
  - `TestSchedulerLeavesUnitRequestLargerThanNodesUnplaced`
  - `TestSchedulerPacksUnitRequestsWithinNodeCapacity`
  - `TestSchedulerWatchesEffectiveResourceRequest`, through the runner's fact scan (`scanFactsForController`), not
    hand-built facts
  - `TestSchedulerRefusesUnparseableStoredRequest`: no preemption, no rebalance move, and the running instance keeps
    its node

  Record red.
  - Red (`scheduler/resource_request_units_test.go`; `types.UnplacedInvalidResourceRequest` and
    `KeyDerivedSchedulerInvalidResourceRequest` added first so the tests compile):
    `LeavesUnitRequestLargerThanNodesUnplaced`: a 64Gi request was placed on a node with at most 3072 MiB;
    `PacksUnitRequestsWithinNodeCapacity`: placed 3 instances of 750m on a 2000m node, want 2;
    `WatchesEffectiveResourceRequest`: web-1 placed on node-1 with effective cpu 1500m on a 1000m node;
    `RefusesUnparseableStoredRequest`: placement/instance/web-2="node-3" and a rebalance move of web-1 to node-3,
    unplaced reason "", want invalid_resource_request.
- [x] 1.4 Controller and agent tests:
  - `TestClusterScaleCountsUnitRequestShortage`
  - `TestClusterScaleIgnoresInvalidResourceRequest`, with zero alive nodes
  - `TestVerticalAutoscalerWritesQuantitiesWithUnits`
  - `TestVerticalAutoscalerRewritesBareRecommendation`
  - `TestRemovingVerticalScalingRestoresDesiredRequest`
  - `TestAgentReadsEffectiveResourceQuantities`

  Record red. Some may already pass; say which and why.
  - Red: `integration/resource_request_units_test.go`: `TestClusterScaleCountsUnitRequestShortage`: timed out
    waiting for the 64Gi instance to be unplaced with insufficient_capacity (it was placed);
    `TestClusterScaleIgnoresInvalidResourceRequest`: unplaced reason = "no_nodes", want invalid_resource_request;
    `TestRemovingVerticalScalingRestoresDesiredRequest`: timed out waiting for recommendation and effective memory
    deleted. `controllers/resource_request_units_test.go`: `TestVerticalAutoscalerWritesQuantitiesWithUnits`: intent
    cpu = "4", want 750m (the `4` core max read by `Atoi` as 4); `TestVerticalAutoscalerRewritesBareRecommendation`:
    intent cpu = "4", want 750m; extra `TestVerticalAutoscalerDeletesRecommendationWithoutPolicy` (D7): no delete
    proposed. `agent/resource_request_units_test.go`: extra `TestLookupServiceResourcesReadsKubernetesForms`: memory
    `1G` and `1.5Gi` read as 0.
  - Already green: `TestAgentReadsEffectiveResourceQuantities`. The agent already read effective-first, and its old
    parser handled `750m` and `768Mi`. The test pins the behavior through the shared parser.

## 2. Shared parser and apply validation

- [x] 2.1 `types/resource_quantity.go` (D1, D2), using integer arithmetic and named constants for the suffix
  multipliers. Verify: 1.1 is green.
  - `go test ./types/ -run 'TestParse|TestFormat'` → `ok`. Exact arithmetic with `math/big` (no `float64`). Also
    `WarnUnreadableResourceOnce`, the shared log-once helper every reader uses (D5, D6).
- [x] 2.2 `lang/compiler.go` (D3): `validateServiceResourceValues` in `compileServiceDeclaration`, and `classifyQoS`
  compares parsed values. Verify: 1.2 is green, and `go test ./lang/... ./cmd/cca/...` passes.
  - 1.2 green; `go test ./lang/... ./cmd/cca/...` → `ok lang`, `ok cmd/cca`. Error text, e.g.:
    `service "web" memory "512MB" cannot be read: memory "512MB": not a number with a known unit (CPU: 250m, 0.5 or 2;
    memory: 512Mi, 1Gi, 1G or bytes)`, positioned at the service's line.
- [x] 2.3 Update the bare-value configs and fixtures listed in proposal.md (BREAKING item) to unit form, keeping each
  test's intended meaning (`cpu 1000` that meant millicores becomes `1000m`; `memory 2048` that meant MiB becomes
  `2048Mi`). Verify: every touched package's tests pass; list the files here.
  - Changed: `integration/priority_preemption_test.go`, `integration/phase10_test.go` (store puts, the vertical DSL
    block and its compiled-fact assertions, recommendation checks now parse units), `integration/rollout_kinds_test.go`,
    `scheduler/benchmark_test.go` (memory `536870912`, which the old scheduler read as MiB, is now `512Mi`),
    `scheduler/fuzz_test.go`, `controllers/autoscale_test.go`, and beyond the proposal's list, with the same bare
    millicores/MiB: `scheduler/scheduler_test.go`, `scheduler/per_node_rollout_test.go`,
    `scheduler/per_node_resource_rollout_test.go`, `tenant/tenant_test.go` (`100` → `100m`),
    `controllers/eviction_test.go` (node memory facts written as `1Gi`/`50Mi` are now plain MiB, as agents write them),
    `integration/autoscale_test.go` (writes the autoscaler intent instead of effective values, which D7 now deletes
    when no intent exists). Docs: `design/storage.md` (2.4).
  - Unchanged: `lang/benchmark_test.go` (`cpu 1` with `memory 1Gi` reads as 1 core, a sensible meaning; it only
    compiles) and `integration/slow_runtime_test.go` (already `512Mi`; its bare numbers are node facts, plain MiB).
  - Verified after 3.1–3.4 (the scheduler fixtures need the new reader): `go test ./scheduler/... ./controllers/...
    ./lang/... ./tenant/... ./agent/... ./api/... ./cmd/cca/... ./types/... ./integration/...` all `ok`.
- [x] 2.4 Docs: a units table in `design/dsl-reference.md` (forms, what is refused, bare CPU = cores, bare memory =
  bytes); `design/storage.md` example in unit form.
  - New `## Resource Units` section in `design/dsl-reference.md`; `design/storage.md` now `cpu 200m`, `memory 128Mi`.

## 3. Readers use the parser

- [x] 3.1 Scheduler (D4, D5):
  - `Watch()` adds `ScanEffectiveServices`.
  - Effective-first reads; MiB rounded up.
  - `UnplacedInvalidResourceRequest`, checked before the `aliveCount == 0` return.
  - Skipped in placement and `attemptPreemptions`; pinned in `extractRebalancePinnedServices`.
  - A derived fact listing the service and value, shown by `cca status`.
  - The warning logged once per value.

  Verify: 1.3 is green, and `go test ./scheduler/... ./cmd/cca/...` passes.
  - New `scheduler/resource_requests.go` (reading, invalid filter, derived fact). The derived fact is
    `derived/scheduler/invalid_resource_request/{svc}` = `{key}={value}`: not under `derived/service/`, which apply
    lets `workload.create` write. The scheduler watches it (a put to an unscanned key would be a create-only CAS), it
    is in the scheduler's write domain (`controllers/topological_sort.go`), and it is in the `/api/status` snapshot
    prefixes. `cca status` prints `Resources:   {svc}: unreadable request {key}={value}; ...` (API `ServiceStatus`
    and CLI `serviceStatusEntry` field `invalid_resource_request`); test
    `TestStatusTextNamesServiceWithUnreadableResourceRequest` (through the API and in process).
  - An unreadable service gets no request entry at all (CPU and memory count 0 for its running instances).
  - 1.3 green; `go test ./scheduler/... ./cmd/cca/...` → `ok`, `ok`. `TestControllerInterface` now expects 10 watch
    prefixes.
- [x] 3.2 Controllers (D6, D7):
  - Eviction: effective-first; MiB; node facts as plain integers; skips and logs an unreadable service.
  - Split `collectResourceValuesBySuffix` into CPU and memory readers.
  - Autoscaler: shared-parser bounds; writes units; rewrites bare recommendations every cycle; deletes intents for
    services without vertical facts.
  - `resolveEffectiveResourceChanges` deletes effective values without an intent.
  - Delete `parseResourceQuantity`.

  Verify: 1.4 (controller tests) is green, and `go test ./controllers/...` passes.
  - `collectCPUMillicoresBySuffix` / `collectMemoryBytesBySuffix` (shared `collectResourceQuantitiesEffectiveFirst`)
    in `controllers/helpers.go`; `TestParseResourceQuantity` replaced by
    `TestCollectResourceQuantitiesReadEffectiveFirst`. The autoscaler now watches `effective/service/`.
  - Bare recommendation: rewritten in unit form (legacy CPU = millicores, legacy memory = bytes, as the old autoscaler
    wrote them) and that resource gets no new recommendation in the same cycle, since its current (effective) value
    may still be the bare one, which now reads as cores/bytes.
  - "No intent" for the intent resolver means no CPU and no memory recommendation (an entry that only holds
    `instances` counts as none). Such services are skipped by `resolveEffectiveResourceChanges` (before, it copied
    the desired value into effective) and their effective values are deleted by the new
    `deleteEffectiveResourcesWithoutIntent`; the meaning is the same, as readers fall back to desired.
  - 1.4 controller tests green (`TestVerticalAutoscalerWritesQuantitiesWithUnits`,
    `TestVerticalAutoscalerRewritesBareRecommendation`, `TestVerticalAutoscalerDeletesRecommendationWithoutPolicy`,
    and in `integration/` `TestRemovingVerticalScalingRestoresDesiredRequest`, `TestClusterScaleCountsUnitRequestShortage`,
    `TestClusterScaleIgnoresInvalidResourceRequest`); `go test ./controllers/...` → `ok`.
- [x] 3.3 Agent (D6): `lookupServiceResourcesFromStore` uses the shared parser and logs once on error. Delete
  `parseMillicores`/`parseMemoryBytes` and move their tests. Verify: `TestAgentReadsEffectiveResourceQuantities` is
  green, and `go test ./agent/...` passes.
  - `parseMillicores`/`parseMemoryBytes` had no tests in `agent/`; their forms are covered by the parser tests in
    `types/`, plus `TestLookupServiceResourcesReadsKubernetesForms` (agent). `go test ./agent/...` → `ok`.
- [x] 3.4 Tenant (D6): usage and fair share via `ParseCPUMillicores`; unreadable values skipped and logged. Verify: a
  test with `cpu 0.5` ×2 gives 1000, and `go test ./tenant/...` passes.
  - `TestTenantCPUUsageReadsCoresWithSharedParser` (quota usage and fair share both 1000; a `lots` service is left
    out); `go test ./tenant/...` → `ok`.
- [x] 3.5 Graphs and flows:
  - SCHEDULING_GRAPH.md: Watch list, effective keys, new reason, preemption and rebalance changes, line numbers.
  - Flow 10: effective-first readers, units, stale-effective deletion.
  - The controller and agent graphs: removed and split functions.

  Grep for every removed function name. Verify: no stale references remain.
  - Updated `design/graphs/SCHEDULING_GRAPH.md` (§1 tree and Watch, §2/§4/§5/§6, store keys, reverse index
    regenerated with current lines, constants, file index with `resource_requests.go`),
    `design/graphs/CONTROLLER_DATA_FLOW_GRAPH.md` (intent resolver, autoscale, eviction, scheduler watch/writes,
    write-domain table, reverse index), `design/graphs/OBSERVABILITY_GRAPH.md` (agent resource lookup),
    `design/runtime-invariant-flows.md` Flow 10, `design/etcd-schema.md` (scheduler watches).
  - `grep -rn "parseMillicores\|parseMemoryBytes\|parseResourceQuantity\|collectResourceValuesBySuffix"` over `*.go`
    and `*.md` (excluding this change and `milestones-archive.md`) → no matches.
- [x] 3.6 Checks: `gofmt -l .` is empty, `golangci-lint run ./...` is clean, and `go test ./...` passes. Paste the
  results here.
  - `gofmt -l .` → empty. `go vet ./...` → clean. `golangci-lint run ./...` → `0 issues.`
  - `go test ./...` → every package `ok` (no FAIL lines). `go test -race` over `types scheduler controllers lang
    tenant agent integration api cmd/cca` → all `ok`.
  - `compileServiceDeclaration` was already 120 lines (over the 80-line rule) before this change; the name, image
    and instance checks moved with the new resource check into `validateServiceDeclarationFields`, so it shrank
    instead of growing. It is still over 80; splitting it further is outside this change.

## 3b. Rework after review round 2 (design D9–D11, added 2026-11-20)

`code-reviewer` and Codex returned REQUEST CHANGES. The main session verified every finding in the code. The user's
decisions of 2026-11-20 are D-a (startup migration), D-b (round up instead of refusing) and D-c (warning for bare CPU).

- [x] 3b.0 Failing tests first for every item below. Record the red run.
  - The red run was made with `controllers.MigrateUnitlessResourceRecommendations` as a no-op stub and
    `types.isDecimalDigits` renamed to `IsBareInteger` (S3). Nothing else had changed yet.
  - `go test ./types/ -run 'TestParse|TestIsBare'`: `TestParseQuantitiesRoundFinerValuesUp` fails with
    `ParseCPUMillicores("0.0005") = 0, cpu "0.0005": finer than 1 millicore; want 1`. `"1.5m"`, `"0.0011"`,
    `"1.1Gi"`, `"0.1Gi"`, `"1.5"` and `"0.0001k"` fail the same way.
  - `go test ./lang/ -run '…'`:
    - `TestCompileAcceptsFinerResourceValues`: finer values refused (`cpu "0.0005" … finer than 1 millicore`).
    - `TestDeprecationWarningsFlagBareCPUOfSixtyFourOrMore`: warnings lack `"web" cpu 500`, `500m`,
      `"web" cpu_limit 64`, `"web" cpu max 4000` and `cores`.
  - `go test ./controllers/ -run '…'`:
    - `TestMigrateUnitlessResourceRecommendations`: `intent/…/web/resources/cpu = "750"` after the migration, want
      `"750m"`. The same failure for the effective value, both memory values, and team/api's `"4"`, want `"250m"`.
      Rewrote 0, want 6.
    - `TestVerticalAutoscalerSkipsResourceWithUnreadableBound`: CPU recommendation `"1429m"` with an unreadable
      min bound, want none.
    - `TestRemovingOnlyCPUVerticalScalingDeletesOnlyCPU`: for both web and team/web, the intent cpu change is `""`
      (no change), want a delete, and the same for the effective cpu.
  - `go test ./scheduler/ -run '…'`: `TestSchedulerNeverPreemptsInvalidRequestService`: preempted legacy-1, whose
    request cannot be read.
  - `go test ./integration/ -run '…'`: `TestLegacyBareRecommendationAfterMigrationKeepsItsSize`: round 0, intent
    cpu = `"750"`, want 750m.
  - Already green, because they pin behavior implemented in 3.1/3.2:
    - `TestEvictionReadsResourceRequestsWithUnits` (T5).
    - `TestSchedulerRebalanceLeavesInvalidRequestServiceInPlace` (T3; it also proves that a readable request does
      move).
    - The new second half of `TestSchedulerWatchesEffectiveResourceRequest` (T4).
    - `TestSchedulerWritesInvalidResourceRequestShownByStatus` (T2).
- [x] 3b.1 D-a / B1: a one-time migration at server start, before the controller runner starts. It runs in one CAS
  transaction and rewrites bare `intent/autoscaler/…/resources/{cpu,memory}` and
  `effective/service/…/resources/{cpu,memory}` (CPU: millicores → `{n}m`; memory: bytes → unit form). It is
  idempotent and never touches `desired/`. Remove the per-cycle rewrite from `computeVerticalDimensionChange`.
  Test: the legacy state (intent `750`, effective `750`, metric at target) makes no jump to the policy max and gives
  no `insufficient_capacity`.
  - `controllers.MigrateUnitlessResourceRecommendations` (controllers/resource_migration.go) is called first thing
    in `startControllersWithLeaderElection` (cmd/cca/command_server.go). A failure is logged and the server still
    starts. Each value has a revision compare; a lost CAS rescans, up to 5 attempts.
  - One transaction, except when the plan exceeds `etcdTransactionOperationLimit` (128). Then it uses several
    transactions, each holding whole services. With 4 values per service that means more than 32 vertically scaled
    services with legacy values.
  - `computeVerticalDimensionChange` and `isUnitlessRecommendation` are removed. design.md D7/D9 explains why the
    migration makes the per-cycle rewrite redundant: every server migrates before its runner starts, so after a rolling
    upgrade no old writer remains.
  - Green:
    - `TestMigrateUnitlessResourceRecommendations`: rewrites 6 values, leaves `desired/` untouched, and a second run
      keeps the store revision unchanged.
    - `TestLegacyBareRecommendationAfterMigrationKeepsItsSize` (integration): 5 rounds in the order instance,
      scheduler, autoscale, autoscale, intent. The intent stays `750m` throughout, there is never an
      `insufficient_capacity`, and web is placed.
- [x] 3b.2 B2: the migration clamps to the parsed [min, max] (`cpu { min 250m max 4 }` with a legacy `4` → `250m`).
  - `verticalResourceBounds.clamp`, with bounds from `extractVerticalBoundsByResource`, the same reader the
    autoscaler uses. If a bound is unreadable, no clamping happens. Covered by
    `TestMigrateUnitlessResourceRecommendations` (team/api `4` → `250m`).
- [x] 3b.3 D-b: a value finer than 1 byte or 1 millicore is rounded UP. Malformed values are still refused. Update
  the spec scenario, D2 and the tests.
  - `parseScaledDecimal` rounds a remainder up. A CPU value with `m` may now have a fraction (`1.5m` → 2).
  - Spec: the "Too fine" scenario became "Finer than the unit rounds up". D2 was rewritten and the "1.1Gi is exact"
    sentence dropped. The `dsl-reference.md` units section is updated.
  - Tests: `TestParseQuantitiesRoundFinerValuesUp` and `TestCompileAcceptsFinerResourceValues`. The refusal tables
    no longer contain rounded values (`0.0005`, `1.5m`, `1.5`, `1.1Gi`), and the compile refusal now uses
    `cpu 0.5k` and `memory_limit 1.5mi`.
- [x] 3b.4 D-c: apply warns, without refusing, for a bare CPU request or vertical CPU bound of 64 or more (read as
  cores; probably meant `{n}m`). Add the BREAKING text to the M243 row in milestones.md.
  - `lang.DeprecationWarnings` → `bareCPUValueWarnings` covers cpu, cpu_limit, and vertical cpu min/max, with the
    threshold in the named constant `bareCPUWarningMillicores`. Both apply paths and `cca lint` print it.
  - Example text: `service "web" cpu 500 is read as 500 cores (a CPU value without m means cores since M243); write
    500m if you meant millicores`.
  - Test: `TestDeprecationWarningsFlagBareCPUOfSixtyFourOrMore`. The M243 changelog row in milestones.md now has the
    BREAKING text.
- [x] 3b.5 B3: cleanup is per resource. The autoscaler deletes a dimension's intent when that dimension has no
  `scale/vertical/{cpu|memory}/` fact. The intent resolver deletes effective cpu or memory when THAT resource has no
  intent, also when desired has no value. Test: remove only `cpu {}` → the CPU intent and effective CPU are deleted
  and memory stays (assert the deletions).
  - Autoscaler: `extractVerticalBoundsByResource` is keyed `"{svc}/{kind}"`, and `buildRemovedVerticalPolicyDeletions`
    works per kind.
  - Intent resolver: `resolveEffectiveResourceChanges` only copies a recommendation per resource and no longer copies
    the desired value. `deleteEffectiveResourcesWithoutIntent` deletes per resource. `extractUserResources` is
    removed.
  - Test: `TestRemovingOnlyCPUVerticalScalingDeletesOnlyCPU` asserts the deletes and that memory is left unchanged.
- [x] 3b.6 B4: `findPreemptionVictim` skips instances of services with an unreadable request. Add a test.
  - `invalidResourceRequests` is passed through `attemptPreemptions` to `findPreemptionVictim`.
  - Test: `TestSchedulerNeverPreemptsInvalidRequestService`. It also asserts that a valid victim is preempted, so the
    skip is what is being proven.
- [x] 3b.7 B5: hierarchical names. Split "{svc}/{resource}" at the LAST slash, and check the other M243 code for the
  same first-segment split. Add a test with `team/web`.
  - `deleteEffectiveResourcesWithoutIntent` and `buildVerticalScalingChanges` use `strings.LastIndex`.
  - The same first-segment mistake was found and fixed in four places, which now match the key suffix or segment:
    `collectResourceQuantitiesEffectiveFirst` (autoscale current value, eviction), `extractAutoscalerResourceIntents`
    / `extractEffectiveResources` (through `collectServiceResourceValues` → `splitServiceResourceKey`), and the
    autoscaler's vertical bounds.
  - The scheduler's reader and the migration already match suffixes.
  - Tests: `team/web` in `TestRemovingOnlyCPUVerticalScalingDeletesOnlyCPU`, and `team/api` in
    `TestMigrateUnitlessResourceRecommendations`.
  - Not changed: the tenant usage sum (`computeTenantCPUUsage`) still splits at the first slash. That predates M243
    (only its CPU parse changed here).
- [x] 3b.8 S4: an unreadable vertical bound means no recommendation for that resource.
  - `verticalResourceBounds.unreadable` → `computeVerticalResourceChange` returns nil.
  - Test: `TestVerticalAutoscalerSkipsResourceWithUnreadableBound` (no CPU recommendation; memory gets `768Mi`).
- [x] 3b.9 Tests:
  - T1: move the async asserts at integration :129/:132 into `waitFor`.
  - T2: the scheduler writes the invalid-request fact, and `/api/status` shows it.
  - T3: rebalance leaves an invalid-request service where it is.
  - T4: a change of the effective value triggers a reconcile.
  - T5: eviction (effective-first, MiB credit, skips unreadable).
  - T1: in `TestClusterScaleCountsUnitRequestShortage`, the "no placement" and "capacity request" checks are now
    inside the first `waitFor`.
  - T2: `TestSchedulerWritesInvalidResourceRequestShownByStatus` (integration). The scheduler writes
    `{key}=512MB`, and GET `/api/status` returns it as `web.invalid_resource_request`.
  - T3: `TestSchedulerRebalanceLeavesInvalidRequestServiceInPlace`. With a readable request the same facts do move
    an instance, so the test has a control.
  - T4: `TestSchedulerWatchesEffectiveResourceRequest` turns resync off. After web-1 exists, effective cpu changes
    to `500m` and web-1 is placed.
  - T5: `TestEvictionReadsResourceRequestsWithUnits` (effective `512Mi` beats desired `64Mi`; `100Mi` each evicts
    both; an unreadable `512MB` is skipped).
- [x] 3b.10 Structure:
  - S1: `compileServiceDeclaration` ≤ 80 lines.
  - S2: OBSERVABILITY_GRAPH status prefixes and round-trip count; SCHEDULING_GRAPH reverse index re-checked; the
    migration in a graph.
  - S3: one helper for bare integers and one MiB constant.
  - S1: `compileServiceDeclaration` is now 37 lines. The identity facts moved to `compileServiceIdentityFacts` and
    the optional single-line fields to `compileServiceOptionalFacts`. No changed or new non-test function is over
    80 lines.
  - S2:
    - OBSERVABILITY_GRAPH lists all 16 prefixes (adds `derived/service/` and
      `derived/scheduler/invalid_resource_request/`) and gives the round-trip count as 16, with the line numbers.
    - SCHEDULING_GRAPH's reverse index was regenerated from the code (63 lines moved after B4). The §1/§2/§4/§5/§6
      line numbers and the preemption immunity table (new row) are updated.
    - CONTROLLER_DATA_FLOW_GRAPH has a new "Startup migration" section and updated intent/autoscale trees and reverse
      index. Flow 10 is updated too.
  - S3: `types.IsBareInteger` (exported, replacing `isDecimalDigits` and `isUnitlessRecommendation`). The
    `bytesPerMebibyte` constant is removed in favor of `mebibyteMultiplier`.
- [x] 3b.11 Checks: gofmt, golangci-lint, `go test ./...`, `-race` on the affected packages.
  - `gofmt -l .` → empty. `golangci-lint run ./...` → `0 issues.` `go test -count=1 ./...` → 20 packages `ok`, no
    FAIL.
  - `go test -count=1 -race ./types/ ./scheduler/ ./controllers/ ./lang/ ./tenant/ ./agent/ ./integration/ ./api/
    ./cmd/cca/` → all `ok`.

## 3c. Rework after review round 3 (Codex re-review of 3b, design D9, added 2026-11-20)

The Codex re-review confirmed that all 3b fixes work. It found two bugs in the startup migration, and the main session
verified both.

- [x] 3c.0 Failing tests first. Record the red run.
  - The red run used stubs that kept the old behaviour: `transactionItemsPerRewrite = 1`, a `delayAfterAttempt`
    returning 0, and a gate that migrates once and starts the controllers either way.
  - Command: `go test ./controllers/ -run
    'TestMigrationBatches|TestControllersStartOnly|TestControllersNeverStart|TestResourceMigrationBackoff'`.
  - `TestMigrationBatchesStayWithinTransactionLimit`: migration of 33 services failed:
    `transaction: etcdserver: too many operations in txn request (256)`.
  - `TestControllersStartOnlyAfterResourceMigrationSucceeds`: controllers started before the migration committed;
    effective cpu = "750" after the gate, want 750m.
  - `TestControllersNeverStartWhileResourceMigrationFails`: controllers started while the migration kept failing.
  - `TestResourceMigrationBackoffDoublesUpToMaximum`: delay after attempt 1 = 0s, want 1s.
- [x] 3c.1 C1: `batchRewritesByService` counted rewrites against `etcdTransactionOperationLimit`. The limit covers
  compares plus success and failure ops, and each rewrite is one compare and one put. 33 services × 4 values gives a
  264-op transaction, which fails. Fix: count 2 per rewrite and never split a service across batches. Test: a plan
  that needs several batches.
  - The new constant `transactionItemsPerRewrite = 2` is used in the batch condition. A service is never split.
  - Green: `TestMigrationBatchesStayWithinTransactionLimit`. It uses a store wrapper that refuses any transaction over
    the limit, as etcd does. All 132 values are migrated in more than one transaction, each within the limit, and no
    service is split across batches.
- [x] 3c.2 C2: a failed migration was only logged and the controllers started anyway, so the B1 window was back.
  User decision: retry with bounded exponential back-off (named constants). The controller runner does not start
  until the migration succeeds. The server does not exit, and the retry continues until the context is cancelled.
  Each failure is logged with the attempt number and the next delay. The API server and the rest of the server come
  up meanwhile. Test: a store that fails the first N transactions.
  - `controllers.RunControllersAfterResourceMigration` retries the migration with back-off: 1 s, doubling up to 1 min
    (`resourceMigrationInitialRetryDelay`, `resourceMigrationMaximumRetryDelay`,
    `resourceMigrationRetryGrowthFactor`). It waits on a timer or `ctx.Done()` and logs `attempt` and
    `next_retry_in`. Only after success does it call the start function, which runs `HARunner.Run`.
  - `startControllersWithLeaderElection` builds the runner and runs the gate with `go`, then returns. The API server
    (`configureAndStartAPIServer`, called next) comes up while the gate retries.
  - Green:
    - `TestControllersStartOnlyAfterResourceMigrationSucceeds`: 3 injected failures; the controllers start only after
      the migration has committed.
    - `TestControllersNeverStartWhileResourceMigrationFails`: the controllers never start, and cancelling the context
      ends the gate.
    - `TestResourceMigrationBackoffDoublesUpToMaximum`.
- [x] 3c.3 User decisions recorded, no change: per-service batching above the transaction limit is accepted, with
  each service atomic. The bare-CPU warning also covers `cpu_limit`.
  - Both are recorded in design D9 (batching) and in 3b.4 (`cpu_limit`).
- [x] 3c.4 Optional (Codex style note): make `resourceMigrationScanPrefixes` / `resourceKindSuffixes`
  non-mutable if that fits the project's conventions.
  - Both are now fixed-size arrays (`[...]`). They cannot grow, and `resourceKindSuffixes` (a pair struct instead of a
    map) iterates in a fixed order.
  - Go has no constant composite values, and the project uses package-level tables elsewhere (`qosPriority`,
    `statusScanPrefixes`), so they are not turned into functions.
- [x] 3c.6 Follow-up of the Codex check of 3c, item 1: `design/graphs/HA_GRAPH.md` showed `startControllersWithLeaderElection`
  launching `haRunner.Run` directly. The edge is now: goroutine → `RunControllersAfterResourceMigration` →
  `runControllersAfterResourceMigration` (resource_migration.go:214) → start callback → `haRunner.Run`
  (command_server.go:385). It uses the current lines (:341, :375, :377, :383), and the reverse-index row is updated.
- [x] 3c.7 Follow-up of the Codex check of 3c, item 2: regression test for a CAS loss in a later batch,
  `TestMigrationRecoversFromCASLossInLaterBatch`.
  - Setup: 33 legacy services make 3 batches. The wrapper deletes and re-puts a guarded key of batch 2, because a
    same-value put does not move the memory store revision, so batch 2 loses its CAS once.
  - Asserts: a CAS was lost; the migration completes; every value is `750m` / `768Mi`; each key is written by
    exactly one committed transaction, so batch 1 is not rewritten.
  - Not red first: the code already rescans, and unit values are skipped. Verified by mutation instead. Making the
    planner also re-read `750m` as bare fails with `intent/autoscaler/service/svc00/resources/cpu written by 2
    committed transactions, want exactly 1`. The mutation was reverted, and the file matches its copy.
  - Codex also confirmed, with no change needed:
    - the API starts while the gate retries;
    - the timer stops on cancel;
    - migrating before leader election is safe;
    - `--api-only` is not a gap;
    - a one-service batch cannot exceed the limit (at most 4 values).
  - Checks: `gofmt -l .` → empty; `golangci-lint run ./...` → `0 issues.`; `go test -count=1 ./controllers/` → `ok`;
    `go test -count=1 -race ./controllers/` → `ok`.
- [x] 3c.5 Checks: gofmt, golangci-lint, `go test ./...`, `-race` on the affected packages; graphs updated.
  - `gofmt -l .` → empty; `go vet ./...` clean; `golangci-lint run ./...` → `0 issues.`; `go test -count=1 ./...` → 20
    packages `ok`, no FAIL; `go test -count=1 -race ./controllers/ ./cmd/cca/ ./integration/ ./scheduler/ ./types/` →
    all `ok`.
  - Graphs: CONTROLLER_DATA_FLOW_GRAPH "Startup migration" (gate, back-off, batching, line numbers) and its reverse index,
    AUDIT_GRAPH and EXTENSIBILITY_GRAPH (HARunner.Run starts through the gate), Flow 10.

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
