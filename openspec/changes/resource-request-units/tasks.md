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

## 3d. Rework after the milestone review (task 4.2 findings, design D2/D6/D7/D9/D12, added 2026-11-23)

The code-reviewer (Opus, high) review of 329e0fa and the Codex check of the decisions; user decisions A, B (single
victim) and C on 2026-11-23. The main session verified every finding in the code.

- [x] 3d.0 Failing tests first, one per item below (names in the spec). Record the red run.
  - The red run had only one stub: an unused `HARunner.migrationBackoff` field, so that the new HA test compiles.
    Nothing else had changed yet.
  - `go test -count=1 ./types/ -run TestParse`: `TestParseCPUMillicoresAcceptsKubernetesForms` fails with
    `ParseCPUMillicores(".5"): unexpected error cpu ".5": not a number with a known unit`, and the same for `.25` and
    `.5m`. `TestParseMemoryBytesAcceptsKubernetesForms` fails the same way for `.5Gi`, `.5Ki` and `.5`.
  - `go test -count=1 ./controllers/ -run '…'`:
    - `TestHARunnerMigratesOnLeadershipAcquisition`: the first reconcile saw
      `intent/autoscaler/service/web/resources/cpu = "750"`, want 750m.
    - `TestHARunnerLeadershipLossCancelsMigrationRetry`: the controllers ran (saw `"750"`) although the migration
      never succeeded.
    - `TestMigrationRecoversFromCASLossInLaterBatch`: reported 68 rewritten values, want the total 132.
    - `TestMigrateUnitlessResourceRecommendations`: `effective/service/batch/resources/cpu = "2m"`, want `"2"`;
      `effective/service/solo/resources/memory = "1Mi"`, want `"2048"`; rewrote 8 values, want 6.
    - `TestVerticalMemoryRecommendationSettlesOnWholeMiB`: the second reconcile proposed `"96Mi"` again; high usage
      gave `"954Mi"`, want 953Mi; the 1Mi request at 60% proposed `"1Mi"`, want no write.
    - `TestEvictionReadsResourceRequestsWithUnits` (case 3 inverted): evicted `map[inst-b:true]`, want
      `map[inst-a:true inst-b:true]`.
  - `go test -count=1 ./scheduler/ -run '…'`:
    - `TestSchedulerSkipsPreemptionThatCannotMakeRoom`: preempted small-1, although stopping it cannot make room for
      64Gi.
    - `TestSchedulerPreemptsOnlyAVictimThatMakesRoom`: medium-1 not preempted, and cheap-1 (node-2, which the
      requester's `require tier=gold` excludes) was preempted instead.
  - Already green, as expected: `TestClassifyQoSRequestsWithoutLimitsIsGuaranteed`, which pins current behaviour.
- [x] 3d.1 Finding 1 (D7): compute vertical memory in whole MiB: min bound rounded up, max rounded down, raw
  recommendation rounded up, current value converted the same way before the comparison. Test
  `TestVerticalMemoryRecommendationSettlesOnWholeMiB` (`min 100M max 1G`: `96Mi` once, no write on the second
  reconcile; never above `953Mi`). Also cover a small request whose raw recommendation lands in the same MiB.
  - `buildVerticalScalingChanges` puts the memory dimension into whole MiB:
    - the current value via `types.MemoryBytesToMiBRoundedUp`;
    - the bounds via the new `verticalResourceBounds.inWholeMebibytes` (min up, max down; when both fall inside one
      MiB, the max wins);
    - the format via the new `types.FormatMebibytes`.
  - `computeVerticalResourceChange` is unchanged. Its `ceil(current × p95 / target)` is now in MiB, so the raw value is
    rounded up, and the comparison with the current value is in the same unit.
  - New in `types`: `FormatMebibytes` and `MemoryBytesToMiBRoundedDown` (`FormatMemoryMiB` now calls
    `FormatMebibytes`). Both are covered in `TestFormatResourceQuantitiesWithUnits`.
  - Also applied to the migration, so it cannot write above a non-MiB max either: `formatLegacyQuantity` clamps memory
    in whole MiB with the same bounds.
  - Green: `TestVerticalMemoryRecommendationSettlesOnWholeMiB`:
    - `96Mi` once, then no write when 96Mi is read back;
    - `953Mi` at 200% of 900Mi;
    - a `1Mi` request at 60% gives no write.
- [x] 3d.2 Finding 3 (D9): `HARunner.startControllers` runs the migration, with the same back-off and cancelled when
  leadership is lost, before it starts the runner. Test `TestHARunnerMigratesOnLeadershipAcquisition` (a bare value
  written after start is `750m` before the controllers run).
  - Under the mutex, `startControllers` only creates `controllerCtx`, the runner and `runnerDone`. It then starts a
    goroutine that runs `runControllersAfterResourceMigration(controllerCtx, …, haRunner.migrationBackoff, start)`,
    where `start` calls `Runner.Run`, and returns at once.
  - The election loop is not blocked, because the OnAcquired callback is synchronous in `tryAcquireOrRenew`. The mutex
    is never held across the retry.
  - `stopControllers` cancels `controllerCtx`, which ends a pending retry or the runner, and waits on `runnerDone` as
    before.
  - The back-off defaults are set in `NewHARunner` (1 s doubling to 1 min).
  - The goroutine captures the runner and done channel locally instead of reading `haRunner.runner` or
    `haRunner.runnerDone`. Before, it read those fields, which `stopControllers` resets after its timeout.
  - The migration at server start (`RunControllersAfterResourceMigration`) is kept, as D9 states. On a fresh start the
    leadership run is then a no-op scan.
  - Green:
    - `TestHARunnerMigratesOnLeadershipAcquisition`: the first reconcile sees `750m`.
    - `TestHARunnerLeadershipLossCancelsMigrationRetry` (added): with a store that always fails, `startControllers`
      returns at once, `stopControllers` returns well within `stopControllersTimeout`, and no controller ever ran.
- [x] 3d.3 D9 nits: rewrite an effective value only when the same resource has an autoscaler recommendation
  (scenario "Effective value without a recommendation"); the logged and returned count is the total over all batches
  and attempts.
  - `planUnitlessResourceRewrites` records each `"{svc}/{kind}"` that has an intent; the intent layer is scanned first.
    It plans an effective rewrite only for those.
  - `commitResourceRewrites` now returns the committed count. `MigrateUnitlessResourceRecommendations` adds it up over
    batches and attempts, and logs and returns that total. It logs only when the total is above 0.
  - Green:
    - `TestMigrateUnitlessResourceRecommendations`: batch `2` stays `2`, and solo's memory `2048` stays (solo has
      only a CPU intent); 6 rewritten.
    - `TestMigrationRecoversFromCASLossInLaterBatch`: 132 reported after a lost CAS.
- [x] 3d.4 Finding 2 (D6, decision A): eviction keeps instances of services with an unreadable request as candidates
  in normal QoS/priority order, credited 0 MiB, logged once. Remove `excludeUnreadableServiceInstances`. Invert case 3
  of `TestEvictionReadsResourceRequestsWithUnits` (the unreadable service is evicted; scenario "Still evictable") and
  assert the extra eviction of the next candidate in the same cycle.
  - `excludeUnreadableServiceInstances` is removed. `extractServiceMemoryRequests` returns only the request map; an
    unreadable service has no entry, so it is credited 0. The consequence logged once now says the instances stay
    candidates credited 0 MiB.
  - With no production reader of the unreadable-services set left, `collectCPUMillicoresBySuffix`,
    `collectMemoryBytesBySuffix` and `collectResourceQuantitiesEffectiveFirst` return one map and take a
    `resourceQuantityReader` (name, consequence, optional service filter; see 3d.7). `helpers_test.go` is updated.
  - Green: `TestEvictionReadsResourceRequestsWithUnits`, case 3: inst-a (unreadable `512MB`) and then inst-b are
    evicted.
- [x] 3d.5 Finding 4 (D12, decision B, single victim): `findPreemptionVictim` takes the request map and node remaining
  capacity and returns only a victim whose node's remaining CPU and memory plus its requests cover the requester's,
  on a node the requester's constraints allow; lowest priority, then node ID, then instance ID; none → no preemption.
  Tests `TestSchedulerSkipsPreemptionThatCannotMakeRoom`, `TestSchedulerPreemptsOnlyAVictimThatMakesRoom`; existing
  preemption tests (`integration/priority_preemption_test.go`) still pass.
  - Inputs:
    - The new `preemptionInputs` struct holds the instances, stored placements, `serviceResources`, constraints,
      priorities, stateful and invalid services.
    - `attemptPreemptions` computes the remaining capacity as placement does:
      `computeNodeLoadAndResourceUsage` → `buildAliveCandidateNodes`. It uses `mergeThisCyclePlacements(placements,
      changes)`, so placements made earlier in the same cycle count.
  - Victim choice in `findPreemptionVictim`:
    - Only nodes in `filterByConstraints(aliveNodes, requester, …)` are allowed (the same filter and fallback as
      placement).
    - The existing exclusions moved to `isPreemptibleService` (per-node, stateful, unreadable request).
    - The new `stopMakesRoom` requires node remaining + victim requests ≥ requester requests, per resource. A zero
      request always fits, as in `buildNodeHeap`.
    - Sort: priority, then node ID, then instance ID.
    - The chosen victim's room is reserved (remaining += victim − requester), so a later requester in the same cycle
      does not count it again.
  - Green: both new tests, every existing `TestPreemption*` scheduler test, and the three tests in
    `integration/priority_preemption_test.go` (`go test -count=1 -v ./integration/ -run 'Priority|Preempt'` →
    `TestPriorityPreemptionLifecycle`, `TestPriorityPreemptionRespectsEqual` and `TestPriorityEvictionOrder` PASS).
- [x] 3d.6 D2: the parser accepts a leading dot (`.5`, `.5Gi`); `5.` stays refused. Extend the two parser form tests.
  - `parseScaledDecimal` accepts an empty integer part when a fraction follows (`hasLeadingDot`). `.`, `.m`, `.Gi`,
    `5.`, `5.m` and `1.Gi` stay refused, and are in the refusal table.
  - Forms tests: `.5` = 500m, `.25` = 250m, `.5m` = 1m (rounded up); `.5Gi` = 536870912, `.5Ki` = 512, `.5` = 1 byte.
  - **Open, for the main session:** the DSL lexer refuses a `.` that starts a token. `cca lint` on
    `resources { cpu .5  memory .5Gi }` gives `parse error: 5:13: unexpected character '.'`, and `cpu ".5"` is refused
    the same way. Templates render before lexing. So the parser change reaches only values that bypass the lexer,
    such as store values. A `.cca` file, including one written by `k8s-to-cca`, still cannot declare `.5`, which the
    "CPU forms" scenario (`cpu .5`) and the D2 rationale assume.
  - Making the scenario hold needs a lexer change: `scanNextToken` must start a numeric literal at `.` followed by a
    digit. That is outside 3d.6 as written, so it was not done. `design/dsl-reference.md` is not changed, so it does not
    claim `.5` works in a configuration.
  - Main session (2026-11-23, after the Codex stop review flagged it): spec "CPU forms" / "Memory forms" no longer
    list `.5` / `.5Gi`; a new scenario "Leading dot outside a configuration file" states what holds; D2 says the DSL
    refuses it. User decision 2026-11-23: option 2, `k8s-to-cca` writes `0.5` (3d.10).
- [x] 3d.10 `k8s-to-cca` writes a leading-dot request or limit (`.5`, `.5Gi`) with a leading zero (`0.5`, `0.5Gi`).
  Test `TestConvertWritesLeadingDotQuantitiesWithZero`: the converted output declares `cpu 0.5` / `memory 0.5Gi`
  and compiles. Red first.
  - The converter copies quantities in one place: `extractResourceRequests` (cmd/k8s-to-cca/main.go). It reads
    `resources.requests`, or `resources.limits` when there are no requests, and no other field copies cpu or memory.
    The new `addLeadingZeroToQuantity` puts `0` before a value that starts with `.`. An unquoted YAML `.5` is already
    decoded as the float 0.5 and written `0.5`; the quoted form is the one that kept the dot.
  - Test: `TestConvertWritesLeadingDotQuantitiesWithZero` (cmd/k8s-to-cca/main_test.go). It runs for `requests` and
    for `limits`. It formats the result with `lang.Format`, then runs `lang.Parse` and `lang.Compile` on it.
  - Red: `go test -count=1 ./cmd/k8s-to-cca/ -run TestConvertWritesLeadingDotQuantitiesWithZero` →
    `requests: output does not declare cpu 0.5 and memory 0.5Gi` (the output had `cpu .5` / `memory .5Gi`), then
    `requests: converted output does not parse: 5:13: unexpected character '.'`; `FAIL`.
  - Green: the same command → `--- PASS: TestConvertWritesLeadingDotQuantitiesWithZero`.
  - Checks: `gofmt -l .` is empty; `golangci-lint run ./...` → `0 issues.`; `go test -count=1 ./cmd/... ./lang/` →
    `cmd/cca`, `cmd/k8s-to-cca` and `lang` all `ok`.
- [x] 3d.7 Spec C and nits: `TestClassifyQoSRequestsWithoutLimitsIsGuaranteed` (pins current behaviour, no code
  change expected); `eviction.go` logs a corrupt node memory fact once (`WarnUnreadableResourceOnce` or equivalent);
  `extractCurrentResources` logs "no vertical recommendation" only for a service with a vertical policy for that
  resource.
  - `TestClassifyQoSRequestsWithoutLimitsIsGuaranteed` (lang/resource_values_test.go) is green with no code change.
  - N2: `warnCorruptNodeMemoryOnce`, a `sync.Map` keyed by key and value, in eviction.go. It is an equivalent, not
    `WarnUnreadableResourceOnce`, whose text ("re-apply the configuration to fix it") does not fit an agent-written
    node fact.
  - N4: `extractCurrentResources(facts, verticalBounds)` reads, and so warns about, a service's current value only
    when `"{svc}/{kind}"` has vertical bounds (`resourceQuantityReader.includeService`). The filter is covered in
    `TestCollectResourceQuantitiesReadEffectiveFirst`.
  - The once-only logging itself has no test. Capturing the logger would mean swapping the process-wide
    `logging.SetDefault` while runner goroutines from other tests still log, which races under `-race`.
- [x] 3d.8 Graphs and flows: SCHEDULING_GRAPH (preemption inputs), HA_GRAPH (migration in `startControllers`),
  CONTROLLER_DATA_FLOW_GRAPH (fix `splitServiceResourceKey` and `computeP95FromSamples` line numbers; eviction no
  longer filters), Flow 10 where it mentions the migration or eviction.
  - SCHEDULING_GRAPH:
    - §5 is rewritten: inputs, the allowed nodes, `isPreemptibleService`, `stopMakesRoom`, the sort and the
      reservation.
    - The immunity table has two new rows.
    - The §1 Reconcile line numbers are updated.
    - The reverse index is regenerated from the code: 62 scheduler.go rows moved by +3 or +26, there are 3 new rows,
      and the callers of `computeNodeLoadAndResourceUsage`, `buildAliveCandidateNodes`, `buildNodeIndexMap` and
      `filterByConstraints` now include preemption.
    - The §6, writes/deletes and constants line numbers are updated.
    - §2–§4 statement-level line numbers were already stale before this change and are not part of 3d.
  - HA_GRAPH: `NewHARunner`, `Run`, `startControllers` (goroutine → migration → `Runner.Run`) and `stopControllers`;
    the server-start note; the reverse index (`runControllersAfterResourceMigration`, `stopControllers`,
    `Runner.Run`); ordering constraint 1.
  - CONTROLLER_DATA_FLOW_GRAPH:
    - `splitServiceResourceKey` is at :122 in both places, and `computeP95FromSamples` at :450.
    - The autoscale vertical tree shows whole MiB, and the migration tree shows the intent-first effective rule, the
      total count and the leadership note.
    - The eviction tree has no filtering, has the once-only node warning and current line numbers.
    - There are 3 new reverse-index rows.
  - Flow 10 (design/runtime-invariant-flows.md): a preemption bullet (D12); eviction credits 0 MiB; the autoscaler log
    rule; whole-MiB memory; the migration at start and on becoming leader, with the effective-only-with-recommendation
    rule.
- [x] 3d.9 Checks: gofmt, golangci-lint, `go test -count=1 ./...`, `-race` on the affected packages.
  - `gofmt -l .` → empty; `go vet ./...` clean; `golangci-lint run ./...` → `0 issues.`; `go test -count=1 ./...` → 20
    packages `ok`, no FAIL.
  - `go test -count=1 -race ./types/ ./scheduler/ ./controllers/ ./lang/ ./integration/ ./cmd/cca/` → all `ok` except
    one `integration` failure: `TestClusterAutoscaleProvisionesNodesForUnplacedInstances`, 25 s, in the run where all
    6 packages ran in parallel.
    - That test sets no priorities, so the preemption change cannot act in it.
    - It passed alone under `-race`, and `go test -count=1 -race ./integration/` then passed 3 times in a row (15.8 s,
      16.6 s, 18.6 s). It is recorded as a load flake, not fixed.
  - No changed or new non-test function is over 80 lines.

## 3e. Rework after review round 2 (task 4.2 round 2, added 2026-11-23)

- [x] 3e.1 I1 (D12): for a per-node requester, `findPreemptionVictim` also drops nodes already hosting the requester's
  service (`filterOutNodesAlreadyHostingService`, using stored plus this-cycle placements), as placement does. Red
  first: `TestSchedulerPerNodeRequesterPreemptsOnlyOnNodesItCanUse` (the reviewer's probe: per-node `agent` on
  node-1, `cheap` on node-1, `low` filling node-2 → `low` is the victim, `cheap` keeps running), plus the zero-request
  per-node case.
  - Red: `go test -count=1 ./scheduler/ -run TestSchedulerPerNodeRequesterPreemptsOnlyOnNodesItCanUse` →
    `preempted cheap-1 on node-1, which already hosts agent-1, so agent-2 can never use it`; `low-1 not preempted`;
    `zero request: preempted cheap-1, although agent-2 cannot use node-1, the only node`; `FAIL`.
  - Fix: the new `allowedPreemptionNodes` in scheduler.go makes the same choice as `placeBatchedInstances`.
    - It runs `filterByConstraints`.
    - For a per-node requester, it also runs `filterOutNodesAlreadyHostingService` over `reservations.cyclePlacements`,
      which holds the stored placements, this cycle's placements (`mergeThisCyclePlacements`) and the earlier
      preemptions of the cycle.
    - After a preemption, `attemptPreemptions` records requester → victim node in `cyclePlacements`. A second pending
      per-node instance of the same service in the cycle therefore does not pick the same node.
  - Green: the new test (both cases), and all scheduler tests.
- [x] 3e.2 I2: delete `types.FormatMemoryMiB`; its tests use `FormatMebibytes(MemoryBytesToMiBRoundedUp(x))`. Update
  design D1, D7, D9 and any graph that names it.
  - The function is removed from types/resource_quantity.go. `TestFormatResourceQuantitiesWithUnits` uses
    `FormatMebibytes(MemoryBytesToMiBRoundedUp(x))`, and `TestFormattedQuantitiesParseBack` uses
    `FormatMebibytes(768)`.
  - `grep -rn FormatMemoryMiB --include=*.go --include=*.md .` now finds only this file: no code, design doc or graph
    names it. The main session had already updated D1, D7 and D9.
  - No red run: this is a deletion of dead code, with only test callers.
- [x] 3e.3 N-a: in the migration clamp, a max that rounds down to 0 MiB is enforced, not read as unset (test: legacy
  `200000000` with `memory { max 512Ki }`). N-b: `runControllersAfterResourceMigration` does not log an error when
  the context is cancelled.
  - N-a red: `TestMigrationEnforcesMaximumBelowOneMebibyte` (controllers/resource_migration_test.go) →
    `intent/autoscaler/service/web/resources/memory = "191Mi" after the migration, want 0Mi`.
  - N-a fix: `verticalResourceBounds.hasMaximum` is set when a max fact parses. `clamp` and `inWholeMebibytes` use it
    instead of `maximum > 0`, so a declared max of 0 (or one that rounds down to 0 MiB) is enforced.
    `computeVerticalResourceChange` is unchanged: a max ≤ 0 still means no recommendation. Green: the test gives
    `0Mi`.
  - N-b: after a failed attempt, `runControllersAfterResourceMigration` returns false without logging when
    `ctx.Err() != nil`. This covers leadership loss and shutdown.
  - The N-b log line has no test, for the same reason as in 3d.7: capturing it needs the process-wide
    `logging.SetDefault`, which races under `-race`. The existing gate tests (`TestControllersNeverStart…`,
    `TestHARunnerLeadershipLossCancelsMigrationRetry`) still pass.
- [x] 3e.4 N-d: inline comments on `victimCandidate` fields; `preemptionInputs` documents which fields it mutates (or
  splits the mutable state out if that stays small).
  - Split: `preemptionInputs` is now read-only: instances, placements, requests, constraints, priorities, stateful
    and invalid services.
  - The new `preemptionReservations` holds the mutable state: `aliveNodes` (remaining capacity), `aliveNodeIndex`,
    `cyclePlacements` and `preemptedInstances`. Each field's comment says how a preemption changes it.
  - `findPreemptionVictim(requester, priority, inputs, reservations)` takes both. The `victimCandidate` fields have
    inline comments.
- [x] 3e.5 Checks: gofmt, golangci-lint, `go test -count=1 ./...`, `-race` on scheduler and controllers; graphs
  (SCHEDULING_GRAPH §5 "as placement does" holds again).
  - `gofmt -l .` → empty; `golangci-lint run ./...` → `0 issues.`; `go test -count=1 ./...` → 20 packages `ok`, no
    FAIL; `go test -count=1 -race ./scheduler/ ./controllers/` → both `ok`.
  - SCHEDULING_GRAPH:
    - §5 shows `preemptionReservations` and `allowedPreemptionNodes` (constraints + per-node filter over the
      stored, cycle and reserved placements) and the per-node reservation.
    - The immunity row "Node the requester may not use" names the per-node rule.
    - The reverse index is regenerated from the code again, with a new `allowedPreemptionNodes` row and updated callers
      of `filterByConstraints` and `filterOutNodesAlreadyHostingService`. The §1, §4 header, §6 and table line numbers
      are shifted.
  - CONTROLLER_DATA_FLOW_GRAPH: the autoscale line numbers moved by 1 for the new field, and the migration tree notes
    `hasMaximum` (N-a) and the quiet return on cancel (N-b).
  - Not changed (M258): placement order by priority (P1) and group preemption (P2).

- [x] 3e.6 R3-N1: when a max is declared and the migration's clamped value is 0, write the smallest unit (`1m` CPU,
  `1Mi` memory), never 0, which the runtimes read as no limit. Red first: `max 0` CPU and `max 512Ki` memory with a
  legacy value give `1m` / `1Mi` (update `TestMigrationEnforcesMaximumBelowOneMebibyte`'s `0Mi` expectation).
  - Test: `TestMigrationEnforcesMaximumBelowOneMebibyte` now also covers `cpu { max 0 }` with a legacy `750`, and
    expects `1Mi` / `1m`.
  - Red: `go test -count=1 ./controllers/ -run TestMigrationEnforcesMaximumBelowOneMebibyte` →
    `…/resources/memory = "0Mi" after the migration, want 1Mi` and `…/resources/cpu = "0m" after the migration, want
    1m`; `FAIL`.
  - Fix: the named constant `smallestLimitedQuantity = 1` in controllers/autoscale.go. Under a declared max
    (`hasMaximum`), `verticalResourceBounds.clamp` raises a result below it to 1 millicore or 1 MiB, the unit clamp
    works in.
  - `clamp` has no caller other than `formatLegacyQuantity`, so the autoscaler is unchanged: a max ≤ 0 still means no
    recommendation in `computeVerticalResourceChange`.
  - Green: the test passes, `go test -count=1 ./controllers/` → `ok`, and `go test -count=1 -race ./controllers/` →
    `ok`. `gofmt -l .` is empty; `golangci-lint run ./...` → `0 issues.`
  - CONTROLLER_DATA_FLOW_GRAPH: the autoscale line numbers moved (:825, :863, :808), and the migration note names the
    floor.
  - **Superseded by 3e.7.** The Codex stop review found that the floor exceeds a declared max (`max 512Ki` → `1Mi`).
    `smallestLimitedQuantity` and `hasMaximum` were removed again. The test now expects the 3e.7 values.

- [x] 3e.7 Codex stop review: the 3e.6 floor exceeds a declared max (`max 512Ki` → `1Mi`; `cpu max 0` → `1m`).
  Replace it per design D9: a max of 0 is no max (no floor, no 0 written); a memory max in (0, 1 MiB) gives exactly
  the max in unit form (`512Ki`, or decimal `k` such as `524.288k`), parsed back to the same bytes and never bare.
  Remove `smallestLimitedQuantity` if unused. Red first: `TestMigrationEnforcesMaximumBelowOneMebibyte` expects
  `512Ki` for `max 512Ki`, `524.289k` for `max 524289` (not whole KiB), and `750m` for `cpu { max 0 }` with legacy
  `750`; each written value parses to at most the max.
  - Tests:
    - `TestMigrationEnforcesMaximumBelowOneMebibyte` is rewritten. web has `memory max 512Ki` and `cpu max 0` with
      legacy `200000000` / `750`; odd has `memory max 524289`. It runs the migration twice and checks the values and
      that each memory value parses to at most its max. On the second run the store revision must not move.
    - `TestFormatMemoryBytesExactly` (types) is new. It checks Ki for whole KiB, decimal k otherwise, that the result is
      never bare digits, and that `ParseMemoryBytes` reads it back to the same bytes. Cases: 524288, 1024, 524289,
      524000, 524200, 500, 1, 1048575.
  - Red (the formatter a stub returning `""`, the 3e.6 code in place):
    - `go test -count=1 ./controllers/ -run TestMigrationEnforcesMaximumBelowOneMebibyte`:
      - odd memory = `"1Mi"`, want `"524.289k"`;
      - web cpu = `"1m"`, want `"750m"`;
      - web memory = `"1Mi"`, want `"512Ki"`;
      - both memory values parse to 1048576, above their max;
      - the same failures on the second run; `FAIL`.
    - `go test -count=1 ./types/ -run TestFormatMemoryBytesExactly`: `FormatMemoryBytesExactly(524200) = "", want
      "524.2k"` and similar; `FAIL`.
  - Fix:
    - `types.FormatMemoryBytesExactly` (new constants `kibibyteSuffix` and `kilobyteSuffix`). Its caller is the
      migration.
    - `formatLegacyQuantity` writes `FormatMemoryBytesExactly(max)` when a readable memory max is above 0 but under
      1 MiB. Otherwise it writes whole MiB, as before.
    - `verticalResourceBounds.clamp` and `inWholeMebibytes` treat a max of 0 as no max again (`maximum > 0`), as the
      autoscaler does. `hasMaximum` and `smallestLimitedQuantity` are removed, and nothing names them.
    - The autoscaler is unchanged: a max ≤ 0 still means no recommendation.
  - Green:
    - `go test -count=1 ./controllers/ ./types/` → both `ok`.
    - `go test -count=1 -race ./controllers/` → `ok`.
    - `gofmt -l .` is empty; `golangci-lint run ./...` → `0 issues.`
  - Graphs: the CONTROLLER_DATA_FLOW_GRAPH migration note describes the new rule. Line numbers are updated in
    autoscale (:816, :854, :800) and in resource_migration (:136, :150, :177, :216, :228, :236). HA_GRAPH's
    `resource_migration.go:232` becomes :236.

- [x] 3e.8 Codex stop review: with a memory max under 1 MiB, `formatLegacyQuantity` (resource_migration.go:140)
  writes the max whatever the legacy value, so a smaller value is raised (`100000` with `max 512Ki` → `512Ki`). Per D9:
  clamp the legacy bytes to [min, max] exactly and write `FormatMemoryBytesExactly` of that; a legacy `0` becomes the
  max. Red first: `100000` + `max 512Ki` → `100k`; `0` + `max 512Ki` → `512Ki`; `600000` + `min 524289 max 900000` stays
  `600k`; each parses to at most the max and at least the min; second run changes nothing.
  - Test: `TestMigrationClampsLegacyMemoryExactlyUnderSubMebibyteMax` (controllers/resource_migration_test.go) has the
    three cases. Each value must parse within [min, max], and on the second run the store revision must not move.
  - Red: `go test -count=1 ./controllers/ -run TestMigrationClampsLegacyMemoryExactlyUnderSubMebibyteMax` →
    `small/resources/memory = "512Ki", want "100k"` and `ranged/resources/memory = "900k", want "600k"`, on both runs;
    `FAIL`. The legacy `0` case already gave `512Ki`.
  - Fix: in the sub-MiB branch, `formatLegacyQuantity` now writes `FormatMemoryBytesExactly(bounds.clamp(legacy))`
    (exact bytes, [min, max]). A legacy `0` (no limit) writes the max. The function comment is updated.
  - Green:
    - `go test -count=1 ./controllers/ ./types/` → both `ok`.
    - `go test -count=1 -race ./controllers/` → `ok`.
    - `gofmt -l .` is empty; `golangci-lint run ./...` → `0 issues.`
    - `TestMigrationEnforcesMaximumBelowOneMebibyte` (3e.7) still passes.
  - Graphs: the CONTROLLER_DATA_FLOW_GRAPH migration note describes the clamp, with line numbers :153, :180, :219,
    :231, :239. HA_GRAPH's `resource_migration.go:236` becomes :239.

## 3f. Refactor after the patterns / anti-patterns review (added 2026-11-23, user approved)

Review by `code-reviewer` (Opus, high) of `git diff 9159e19 -- '*.go'` against design/Anti-pattern.md, CLAUDE.md,
the freeCodeCamp anti-patterns, design-patterns-for-humans (GoF) and the DSA checklist repo. No correctness bug and no
architecture-rule violation; three structural findings break mandatory CLAUDE.md rules. Main session verified I1–I3
(17 raw `"cpu"`/`"memory"` literals in autoscale/intent/migration; `LastIndex` splits at autoscale.go:293,
intent.go:239, :321; effective-first in scheduler/resource_requests.go:46-138, controllers/helpers.go:128,
agent/telemetry.go:207). Behaviour must not change: every existing test stays green unmodified except for renames.

- [x] 3f.1 P-I1 (typed enums, duplication, Strategy): `type resourceKind string` with `resourceKindCPU` /
  `resourceKindMemory`, and one table of per-kind specs (key suffix, parser, intent key, effective key, formatter,
  MiB conversion for memory). Replace the literals and if-pairs in autoscale.go, intent.go, resource_migration.go,
  helpers.go and eviction.go; replace `collectCPUMillicoresBySuffix` / `collectMemoryBytesBySuffix` (middlemen) with
  one collector taking the kind, so a reader cannot pass the wrong suffix.
  - Where the types live: the kind type is exported as `types.ResourceKind` (`ResourceKindCPU` /
    `ResourceKindMemory`) in the new types/resource_kind.go, not as an unexported `resourceKind` in controllers. The
    3f.3 helper and the scheduler need it, and the scheduler imports controllers, so types is the only package both
    reach without a cycle.
  - The spec table (Strategy as function values) is in two layers, because two packages need it:
    - `types.ResourceKindSpec`: kind, `RequestKeySuffix`, `Parse`, `IntentKey`, `EffectiveKey`. Accessors are
      `ResourceKindSpecs()` (CPU first), `LookupResourceKindSpec` and `ParseResourceKind`. Its users are controllers,
      the scheduler and lang.
    - `controllers.verticalResourceSpecs`: it embeds the types spec and adds the sizing unit (`toVerticalUnit`,
      `maximumToVerticalUnit`, `boundsInVerticalUnit`, `formatVerticalUnit`, `formatExact`). Memory is whole MiB via
      `inWholeMebibytes`, and CPU is `keepQuantity` / `keepBounds`.
    - CPU keeps its own bounds function instead of a generic min-up/max-down conversion. A generic one would change
      the result when CPU min > max (clamp gives min today).
  - Literals and if-pairs: autoscale.go, intent.go, resource_migration.go, helpers.go and eviction.go no longer
    compare `"cpu"` / `"memory"` or build `/resources/{kind}` keys. `grep -n '"cpu"\|"memory"\|resources/cpu\|resources/memory\|LastIndex'`
    over them leaves only the eviction pressure value `"memory"`, which is not a resource-kind lookup.
  - Collectors: `collectCPUMillicoresBySuffix`, `collectMemoryBytesBySuffix` and
    `collectResourceQuantitiesEffectiveFirst` are replaced by one `collectResourceQuantities(facts, kind, reader)`.
    The kind picks the suffix and the parser together. helpers_test.go call sites were renamed.
  - Migration: `formatLegacyQuantity(kindSpec, …)` is now one generic branch. For CPU, a max below one sizing unit is
    impossible, so the CPU result is unchanged. `resourceKindSuffixes` and `resourceMigrationScanPrefixes` are
    removed.
- [x] 3f.2 P-I2 (map key choice): a struct key `serviceResourceKey{serviceName, kind}` (or per-service structs) instead
  of building and re-splitting `"{svc}/{kind}"`; sorted deterministically.
  - `types.ServiceResourceKey{ServiceName, Kind}` is the key of vertical bounds, current requests, recommendations
    and effective values (map[ServiceResourceKey]…). `types.SortedServiceResourceKeys` (generic) orders by service,
    then kind.
  - All three `strings.LastIndex` splits and every `serviceName+"/"+kind` join are gone. intent.go no longer imports
    `strings`.
  - `autoscalerResourceIntent{cpu, memory}` is removed: the struct key makes it redundant. So P-N13 (inline
    comments on its fields) has nothing left to comment.
  - Order: the deterministic order of the emitted changes is now (service, kind) instead of the "{svc}/{kind}"
    string order. The two differ for names with `-` or `.` (e.g. `web-api` sorts after `web` here, before it in the
    string order), corrected after review R5. The set of changes is the same.
- [x] 3f.3 P-I3 (3+ duplication): one shared split-service-resource-key and effective-first-without-fallback helper,
  used by the scheduler and controllers; it returns the chosen key and value so the scheduler still builds its
  `invalid_resource_request` text. The agent (one key at a time) shares the rule by a doc reference.
  - `types.SplitServiceResourceKey(key, prefix)` returns (ServiceResourceKey, ok) and matches the key suffix (B5).
  - `types.CollectEffectiveFirstResourceValues(facts)` returns `map[ServiceResourceKey]StoredResourceValue{FactKey,
    FactValue}`: effective when stored, otherwise desired, with no fallback from an unreadable effective value.
  - Users:
    - controllers `collectResourceQuantities`;
    - scheduler `extractServiceResourcesFromFacts` / `parseServiceRequest`, which still builds the
      `invalid_resource_request` "{key}={value}" text from FactKey and FactValue;
    - the migration and intent.go, for the per-layer split.
  - The scheduler's `collectStoredResourceRequests`, `effectiveFirst`, `storedResourceRequest`,
    `serviceStoredRequests` and its key-suffix constants are removed.
  - The agent's `lookupResourceWithFallback` doc comment points at the helper; the agent reads one key at a time.
- [x] 3f.4 Scheduler nits: P-N1 do not copy placements or compute usage when no unplaced requester has priority > 0
  (filter first, merge inside); P-N3 `findPreemptionVictim` only finds, one `reserve` method does all reservation
  writes; P-N4 explicit preemption counter instead of `len(changes)`; P-N10 `slices.Sorted(maps.Keys(...))` instead
  of `sortedStringMapKeys` (remove only the M243 copy).
  - P-N1: `attemptPreemptions` first keeps only the unplaced requesters with priority > 0 and returns when there are
    none. Only then does `newPreemptionReservations` merge this cycle's placement changes (`mergeThisCyclePlacements`
    moved inside) and compute usage. Reconcile now passes `changes`.
  - P-N3: `findPreemptionVictim(requester, inputs, reservations)` only finds. `preemptionReservations.reserve` makes
    every reservation write: node capacity, `preemptedInstances` and `cyclePlacements`.
  - P-N4: an explicit `preemptionCount`.
  - P-N10: `slices.Sorted(maps.Keys(…))` in `buildInvalidResourceRequestChanges`. The M243 `sortedStringMapKeys`
    is removed.
  - All existing scheduler and integration preemption tests pass unmodified.
- [x] 3f.5 Controller nits: P-N5 vertical sizes in int64 end to end, delete `convertQuantitiesToInt`; P-N6 one small
  `firstOccurrence` / once-set used by both log-once caches, with a unit test of the dedup; P-N7 `HARunner` takes a
  generic `func(ctx) error` run on becoming leader (wired in command_server.go) instead of knowing the migration;
  P-N8 migration plans in two explicit passes (recommended set, then both layers), not a loop relying on array order;
  P-N13 inline comments on `autoscalerResourceIntent` fields.
  - P-N5: `extractCurrentResources` returns `map[ServiceResourceKey]int64`. The dimension's `currentValue`, the raw
    recommendation and `clampToRange` are int64, and `convertQuantitiesToInt` is deleted.
    - Not done: `applyVerticalStabilization` and its `timestampedRecommendation` history keep `int`. The existing
      tests `TestVerticalStabilizationBlocks*` call it with `int` variables, and changing them would modify tests
      beyond renames. `computeVerticalResourceChange` converts at that one call, which is exact on 64-bit.
  - P-N6: `types.OccurrenceSet.IsFirstOccurrence(parts...)` (a sync.Map; parts joined with NUL). Both
    `WarnUnreadableResourceOnce` and eviction's `warnCorruptNodeMemoryOnce` use it. New test:
    `TestOccurrenceSetReportsOnlyTheFirstOccurrence` (first once, no joined-text collision, 32 concurrent callers see
    one first occurrence).
  - P-N7: the generic gate `runAfterPreconditionSucceeds(ctx, name, precondition, backoff, start)` is in the new
    controllers/precondition_gate.go.
    - The back-off moved there with it: `preconditionRetryBackoff`, the `precondition*RetryDelay` /
      `preconditionRetryGrowthFactor` constants and `defaultPreconditionRetryBackoff`.
    - `HARunner` has `SetLeadershipPrecondition(func(ctx) error)` and a `preconditionBackoff`, and runs the
      precondition (a no-op when unset) through the gate on becoming leader. ha_runner.go does not import or name
      the migration.
    - cmd/cca/command_server.go wires it: `SetLeadershipPrecondition(controllers.ResourceMigrationPrecondition(factStore))`.
      `RunControllersAfterResourceMigration` / `runControllersAfterResourceMigration` wrap the same gate.
    - Test changes: renames (`resourceMigrationBackoff` → `preconditionRetryBackoff`, `migrationBackoff` →
      `preconditionBackoff`), plus one line in each of the two leadership tests that wires the precondition as
      cmd/cca does. The cancel-on-leadership-loss behaviour and both tests are unchanged and pass.
  - P-N8: `planUnitlessResourceRewrites` makes two explicit passes. `collectRecommendedResources(intentFacts)` builds
    the set (any intent key, as before), then `planLayerRewrites` plans the intent layer (all resources) and the
    effective layer (recommended resources only). `scanMigrationLayer` keeps the old early return per scan.
  - P-N13: obsolete, the struct was removed (3f.2).
- [x] 3f.6 lang nits (P-N9): one enumerator of declared resource values (with kind) used by compiler and linter;
  named constant for millicores per core; rename `DeprecationWarnings` to `ApplyTimeWarnings` (all callers).
  - `declaredResourceValues(serviceDecl)` (compiler.go) enumerates field name, text and `types.ResourceKind`. Both
    `validateServiceResourceValues` and the linter's `bareCPUValueWarnings` (CPU kind only, in the same order as
    before) use it.
  - `bareCPUWarningMillicores = 64 * types.MillicoresPerCore`; the types constant is now exported.
  - `DeprecationWarnings` → `ApplyTimeWarnings`, with all callers: linter.go, cmd/cca/command_apply.go (and
    `printDeprecationWarnings` → `printApplyTimeWarnings`), cmd/cca/configuration_api_client.go, and the two lang test
    files.
  - Not renamed: the test `TestDeprecationWarningsFlagBareCPUOfSixtyFourOrMore`. The spec names it, and the spec
    must not be edited here.
- [x] 3f.7 Graphs, Flow 10 and design references updated for renamed/moved functions; gofmt, golangci-lint,
  `go test -count=1 ./...`, `-race` on scheduler, controllers, integration.
  - CONTROLLER_DATA_FLOW_GRAPH:
    - intent, autoscale (vertical spec table, struct keys, int64), migration (precondition gate, two passes) and
      eviction trees;
    - reverse index: 17 rows replaced or added;
    - all line numbers re-read from the code.
  - SCHEDULING_GRAPH:
    - §1 request reading through `types.CollectEffectiveFirstResourceValues`;
    - §5 rewritten (filter first, `newPreemptionReservations`, find-only, `reserve`, counter);
    - the reverse index regenerated from the code (64 rows moved), plus rows for `newPreemptionReservations` and
      `reserve`, with the removed helpers dropped;
    - tables and constants.
  - HA_GRAPH: `SetLeadershipPrecondition`, `startControllers` through `runAfterPreconditionSucceeds`, the server
    wiring, the reverse index and ordering constraint 1. OBSERVABILITY_GRAPH and EXTENSIBILITY_GRAPH:
    `HARunner.Run` and `Metrics` line numbers.
  - Flow 10: names the shared rule and the kind table, and the leadership precondition.
  - design.md: only renamed identifiers (`collectResourceQuantities`, the `precondition*` constants,
    `lang.ApplyTimeWarnings`).
  - Checks:
    - `gofmt -l .` → empty; `go vet ./...` clean; `golangci-lint run ./...` → `0 issues.`
    - `go test -count=1 ./...` → 20 packages `ok`.
    - `go test -count=1 -race ./scheduler/ ./controllers/ ./types/` → `ok`.
    - No changed or new non-test function is over 80 lines.
  - `-race ./integration/`: one failure, `TestRollingControlPlaneUpgrade`, with `rolling upgrade caused 6
    availability dips (>5 tolerated)`.
    - It is a latency-injected timing tolerance test with no priorities, resource requests or HARunner, so the
      refactor's code paths do not act in it.
    - It passed 3/3 alone under `-race`. In three more full-package `-race` runs it failed once and passed twice.
    - Recorded as a timing flake under `-race` load, not fixed.
- Rejected or deferred (recorded, no task):
  - P-N2 cache "no victim for service S" / min-scan instead of sort: optimization without a benchmark (CLAUDE.md:
    benchmark before optimizing).
  - P-N11 integer fast path in the parser: same; add `BenchmarkParseCPUMillicores` first, later.
  - P-N12 unexport `MigrateUnitlessResourceRecommendations`: acceptable under the rule (production caller exists).
  - P-N14 move preemption to `scheduler/preemption.go`: optional; fits M258, which reworks preemption.

- [x] 3f.8 Review of 3f (same patterns reviewer, 2026-11-23): REQUEST CHANGES for one item; no behaviour change
  found, every 3f finding fixed, departures judged sound. Main session verified R5-I1 (ha_runner.go:143-146). Fix:
  - R5-I1 **fix**: the leadership precondition is a required argument of `NewHARunner` (no nil default, no silent
    no-op); every caller passes one (tests pass an explicit no-op); command_server.go passes
    `ResourceMigrationPrecondition`. A test fails if a nil precondition is passed. `SetLeadershipPrecondition` removed
    if it has no remaining caller.
  - R5-N1 **fix**: legacy `0` under a declared max becomes the max for CPU and memory in both branches (design D9);
    test cases for `cpu { max 4 }` + `0` → `4000m` and `memory { max 2Gi }` + `0` → `2048Mi`.
  - R5-N2 **fix**: drop `maximumToVerticalUnit` (and CPU-only placeholder fields); test
    `boundsInVerticalUnit(bounds).maximum == 0`.
  - R5-N3 **fix**: test that every `types.ResourceKindSpecs()` kind has a vertical spec.
  - R5-N4 **fix**: `types.ResourceKindSpecs()` returns a copy.
  - R5-N5 **fix**: the autoscaler builds the effective-first map once per cycle.
  - R5-N6 **fix**: Flow 10 links to D9 for the migrated forms instead of restating `{n}Mi`.
  - R5-N7 **fix**: the precondition carries a name used in its failure log ("resource unit migration").
  - R5-N8 **rejected**: a third kind is hypothetical; the table test (N3) catches a missing spec.
  - R5-N9 **rejected**: test-only forwarder kept so existing tests stay unchanged.
  - Also correct the 3f.2 note: the (service, kind) order differs from the old string order for names with `-` or
    `.` (e.g. `web-api`); the set of changes is the same.
  - R5-I1 done: `NewHARunner(factStore, nodeID, metrics, leadershipPrecondition, controllers...)`.
    - The precondition is a required `ControllerPrecondition{Name, Check}`. A nil `Check` panics at construction;
      `LookupResourceKindSpec` uses the same panic-on-wiring-error style.
    - `SetLeadershipPrecondition` and the silent no-op are removed.
    - Callers:
      - cmd/cca/command_server.go passes `controllers.ResourceMigrationPrecondition(factStore)`;
      - the 5 non-migration HA tests pass the explicit `noOpLeadershipPrecondition()` (test helper);
      - the 2 leadership-migration tests pass `ResourceMigrationPrecondition(store)`.
    - New test: `TestNewHARunnerRefusesNilLeadershipPrecondition`, which recovers the panic.
  - R5-N1 done:
    - Red: `go test -count=1 ./controllers/ -run TestMigrationRaisesLegacyZeroToTheDeclaredMax` →
      `…/web/resources/cpu = "0m" after the migration, want "4000m"`, `…/web/resources/memory = "0Mi"…, want
      "2048Mi"`; `FAIL`.
    - Fix: `formatLegacyQuantity` replaces a legacy 0 by a readable declared max before both branches. Without a
      max, 0 stays `0m`.
    - Green: the new test, plus the 3e.7/3e.8 tests unchanged.
  - R5-N2 done:
    - `maximumToVerticalUnit` and the CPU placeholder `formatExact` fields are removed.
    - The exact branch keys on `boundsInVerticalUnit(bounds).maximum == 0` (only memory can reach it) and writes
      `types.FormatMemoryBytesExactly`.
    - Test: `TestBoundsInVerticalUnitMaximumBelowOneUnit` (512Ki → 0 MiB, 2Gi → 2048, CPU 1m → 1).
  - R5-N3 done: `TestVerticalResourceSpecsCoverEveryKind`. One vertical spec per `types.ResourceKindSpecs()` kind,
    carrying the kind's storage spec, with no unset function.
  - R5-N4 done: `types.ResourceKindSpecs()` returns `slices.Clone` of the table.
  - R5-N5 done: `extractCurrentResources` builds `types.CollectEffectiveFirstResourceValues` once per cycle. It parses
    each kind with the new `parseChosenResourceQuantities`, which `collectResourceQuantities` now wraps for one-kind
    readers such as eviction.
  - R5-N6 done: Flow 10 links to design D9 for the migrated forms instead of restating `{n}Mi`. The link is relative
    to the change folder, so it needs updating when the change is archived.
  - R5-N7 done: `ControllerPrecondition.Name` ("resource unit migration") is used in the gate's failure log. The
    separate `resourceMigrationName` / `leadershipPreconditionName` constants are removed.
  - Docs:
    - HA_GRAPH: constructor (required precondition and panic), `startControllers` through the named gate, server
      wiring, reverse index.
    - CONTROLLER_DATA_FLOW_GRAPH: migration tree (required argument, `ControllerPrecondition`, R5-N1/N2 rule) and
      autoscale tree (map built once), reverse index, and line numbers re-read from the code.
    - OBSERVABILITY_GRAPH and EXTENSIBILITY_GRAPH: `HARunner.Run` and `Metrics` lines.
    - Flow 10.
  - Checks:
    - `gofmt -l .` → empty; `go vet ./...` clean; `golangci-lint run ./...` → `0 issues.`
    - `go test -count=1 ./...` → 20 packages `ok`.
    - `go test -count=1 -race ./controllers/ ./scheduler/ ./types/` → all `ok`.

## 3g. Fixes from the testbed run (task 4.1 findings, user approved 2026-11-23)

- [x] 3g.1 F1 write loop: the scheduler writes `derived/scheduler/unplaced/{id}/reason` and `/requirements` only when
  the stored value differs (compare with the facts it already reads); unchanged → no change. Red first: a scheduler
  test that reconciles twice with the same unplaced instance and asserts the second cycle proposes no put for those
  keys; and a runner-level test (resync off, real watch) showing the scheduler does not re-trigger itself with one
  unplaced instance (bounded reconcile count over a short window). Kubernetes controllers compare before update.
  - Red without the scheduler.go change: second cycle re-puts `requirements`; runner test counts 28 cycles in 300ms
    (limit 3). Green with it (`scheduler/unplaced_facts_test.go`).
- [x] 3g.2 F4 lint: `cca lint` refuses an unreadable resource value as apply does (same compiler check and message);
  test with `memory 512MB` (`TestLintRefusesUnreadableResourceValue`).
- [x] 3g.3 Checks: gofmt and golangci-lint clean; `go test -count=1 ./...` all `ok`; `-race` on scheduler and lang
  `ok`; SCHEDULING_GRAPH updated (2026-11-23).
- [x] 3g.4 Review of 4d42565 (code-reviewer Opus high + Codex: no correctness bugs). Findings:
  - fix: receiver names `etcdStore`/`counting` in `scheduler/unplaced_facts_test.go` → match the type (mandatory rule);
    local `scheduler` → `countingPlacementScheduler`.
  - fix: SCHEDULING_GRAPH `extractExistingUnplacedInstanceIDs` line number; the "put only when the stored value
    differs" row names `emitUnplacedDemandFacts` (the filter), not `buildUnplacedFactChanges`.
  - fix (optional, taken): runner test asserts the runner is still running after the quiet window.
  - rejected: `errors.As` in `lintResourceValues`: the fallback is harmless; style only.
  - rejected here: `Severity: "error"` raw string, ~20 existing call sites → M259.
  - deferred → M262: unplaced facts are never deleted when the instance stops or is removed (pre-existing since M78,
    confirmed on the testbed, see 4.1 rerun).
  - checks after the fixes: gofmt -l scheduler clean; golangci-lint ./scheduler/... 0 issues; go test -race
    ./scheduler/ ok; both TestSchedulerDoesNot* pass -count=10 -race.
- Deferred to milestones (recorded in milestones.md): F2 → M239 (resource changes roll out progressively; a rollback
  restores the whole service spec, not only the image); F3 → M260 (`cca server` never runs the cluster-autoscale
  controller); F4 rest → M261 (HTTP 403 for schema errors; unplaced reason not shown in `cca describe`).

## 4. Testbed, review, release (main session)

- [ ] 4.1 Testbed by hand (`deploy-run:resource-capacity`, on the cluster a push run leaves). Record commands and
  output here:
  - Run 2026-11-23 on the cluster push run 45179887458 (89362c3, partial, success) left; configs in
    `/tmp/{bad,big,roll1,roll2,fill}.cca` on cca-deploy-ctrl; free CPU before: ctrl 1150m, worker-1 1400m,
    worker-2 1250m.
    - **Refused apply: pass.** `cca apply /tmp/bad.cca` → exit 1, `service "m243bad" memory "512MB" cannot be read …`;
      `cca describe service m243bad` → not found. (Notes: `cca lint` accepts the file; the API answers HTTP 403 for
      this schema error.)
    - **Request no node can meet: pass, except the capacity request.** `m243big` (priority 900, `memory 64Gi`) stays
      pending; `cca watch derived/scheduler/unplaced/` → `reason=insufficient_capacity`, requirements memory 65536;
      no `controller_stopped` written, the 7 existing instances keep running (D12). No capacity request: `cca server`
      never creates the cluster-autoscale controller (only `cca run` / `--simulate` paths do), pre-existing.
      The reason is not shown by `cca describe service` for a new service (REASON column empty).
    - **M241 capacity case: pass.** `m243roll` whoami → nginx with `memory 64Gi`, `progress_deadline 2m`; rollout
      reason `waiting for 32078f9704 unplaced: insufficient_capacity (cpu 100m, memory 65536Mi)`; at +2m `State:
      rolled-back`, `Rollback reason: no progress for 2m: …insufficient_capacity…`, also in `cca status`.
    - **Filling a worker: pass on capacity, with a finding.** `m243fill` 4 × `cpu 750m`: one each on ctrl and
      worker-2, two unplaced `insufficient_capacity`; no node above capacity. worker-1 took none, because the
      rolled-back `m243roll` still declares `memory 64Gi` (rollback restores only the image) and its running instance
      counts 64 GiB there.
    - **No vertical scaling:** `grep -rn vertical test/e2e/workloads` → none (D8).
    - **Findings (verified in logs/code):**
      - F1 **write loop**: every scheduler cycle re-puts unchanged `derived/scheduler/unplaced/{id}/{reason,
        requirements}` (scheduler.go:992-994) and the scheduler watches that prefix (:74); `cca watch` counted 135
        puts per key in 10 s (≈80 etcd puts/s for 3 unplaced instances). Pre-existing since M78, made common by
        M243. Stopped after scaling the test services to 0.
      - F2 a resource change restarts running old instances at once, outside the rollout (agent: `resource change
        detected … live resize unsupported, stopping instance for restart`), and a rollback restores only the image,
        so the new resources stay (pre-existing in M241; matters since M243).
      - F3 `cca server` never runs the cluster-autoscale controller (pre-existing).
      - F4 small: `cca lint` does not refuse an unreadable resource value; HTTP 403 for a schema error; the unplaced
        reason is not shown in `cca describe` outside a rollout.
  - Rerun 2026-11-23 on the cluster push run 45187338094 (4d42565, success) left, after 3g:
    - **F1 fixed:** `cca apply /tmp/big.cca`, then `cca watch derived/scheduler/unplaced/` for 10 s → no events (was
      135 puts per key). `cca scale m243big 2` → exactly one put of reason (`insufficient_capacity`) and requirements
      for the new instance, no repeats in 40 s.
    - **F4 lint fixed:** `cca lint /tmp/bad.cca` → `line 1: error: service "m243bad" memory "512MB" cannot be read …`,
      exit 1.
    - **New (review, confirmed):** `cca scale m243big 0` stops both instances, but no delete of their unplaced facts
      follows in 20 s → M262.
  - **Refused apply:** apply `memory 512MB`; the apply is refused with field and value, and the store is unchanged.
  - **Request no node can meet:** apply a new service with `memory 64Gi`. The instance is unplaced, `cca describe
    service` shows `insufficient_capacity`, and the cluster-scale controller writes a capacity request. No
    lower-priority instance is preempted for it (D12).
  - **M241 4.1 capacity case:** roll a running service to a config that requests more memory than any node, with
    `progress_deadline 2m`. The rollout reason names `insufficient_capacity`; after 2 minutes `rollback_reason` names
    it too.
  - **Filling a worker:** `cpu 750m` with one instance more than a worker fits. The extra instance is unplaced with
    `insufficient_capacity`, and no worker hosts more requests than its capacity (`cca get instances`, per-node count ×
    request). `cca describe node` still shows detected capacity, the stated non-goal.
  - **No vertical scaling:** confirm no deploy workload declares `scale vertical` (D8).
- [ ] 4.2 Independent review: `code-reviewer` (Opus, effort high) reviews the milestone diff against this design and
  the spec. Record each finding as fixed or rejected, with one line of reasoning.
  - Round 1 (2026-11-23, on 329e0fa): REQUEST CHANGES. Each finding verified in the code by the main session:

    | # | Finding | Resolution |
    |---|---|---|
    | 1 | Vertical memory compares bytes but writes rounded-up MiB: same put every cycle, a write loop via the `intent/autoscaler/` watch; above a non-MiB max | **fix** 3d.1 (D7) |
    | 2 | Eviction drops unreadable-request services from candidates, making them immune; D6's reasoning was backwards | **fix** 3d.4 (D6, user decision A: candidate with 0 MiB credit) |
    | 3 | Migration only at process start; a new-version server that becomes leader later reads bare values an old leader wrote | **fix** 3d.2 (D9, migrate on becoming leader) |
    | 4 | Preemption stops a victim without checking it makes room (pre-existing, now reachable): churn loop | **fix** 3d.5 (D12, user decision B: single victim that makes room; victim sets out of scope) |
    | 5 | `cpu_limit`/`memory_limit` stored but never applied; "CPU forms" wording implied it | **deferred** to M254; spec wording fixed, requests-only QoS pinned (3d.7, user decision C) |
    | N1 | Graph line numbers `splitServiceResourceKey`, `computeP95FromSamples` | **fix** 3d.8 |
    | N2 | Corrupt node memory fact warned every cycle | **fix** 3d.7 |
    | N3 | Migration logs only the last plan's count after a retry | **fix** 3d.3 |
    | N4 | "no vertical recommendation" logged for services without a vertical policy | **fix** 3d.7 |
    | N5 | `.5` refused though Kubernetes accepts it | **fix** 3d.6 (D2) |
    | N6 | Migration rewrites an effective value copied from desired (`2` → `2m`) | **fix** 3d.3 (only with a recommendation) |
  - Round 2 (2026-11-23, code-reviewer Opus high, on the uncommitted 3d tree): REQUEST CHANGES. Round-1 findings 1,
    2, 3, 5 and N1–N6 confirmed fixed; 4 partly (I1). Verified by the main session:

    | # | Finding | Resolution |
    |---|---|---|
    | I1 | Preemption ignores the requester's per-node mode: allowed nodes come from `filterByConstraints` only (scheduler.go:300), placement also drops nodes already hosting the service (:1090), so a victim on a node placement never uses is stopped (loop) | **fix** 3e.1 |
    | I2 | `types.FormatMemoryMiB` has only test callers (dead code); D1/D7/D9 still name it | **fix** 3e.2 |
    | P1 | Placement runs in service-name order (scheduler.go:1047), so a victim's replacement can take the freed room before the higher-priority requester (pre-existing) | **new milestone** M258 (placement order by priority); D12 goal holds only for the cannot-make-room case until then |
    | P2 | Grouped requesters vs single-instance preemption check (pre-existing) | **folded into** M258 |
    | P3 | `LeaderElection.release` never calls `onLost`, so shutdown does not cancel the controller ctx (pre-existing; the process exits) | **rejected for M243**: pre-existing, no effect beyond exit; noted for M180 HA correctness |
    | N-a | Migration does not enforce a max below 1 MiB (rounds to 0 = unset) | **fix** 3e.3 |
    | N-b | Error log on leadership loss when the cancelled ctx aborts the migration | **fix** 3e.3 |
    | N-c | Placement-key parsing repeated in 3 places, each under 10 lines | **rejected**: below the duplication threshold |
    | N-d | `preemptionInputs` carries mutable state; `victimCandidate` fields lack inline comments | **fix** 3e.4 (comments required by CLAUDE.md; rename/split if it stays small) |
    | N-e | Log-once `sync.Map`s never pruned | **rejected**: bounded by distinct bad values written; acceptable |
    | N-f | Startup migration gate now redundant with the leadership migration | **rejected**: kept on purpose (D9), costs one scan |
  - Round 3 (2026-11-23, same reviewer, on the 3e tree): APPROVE. I1, I2, N-a, N-b, N-d confirmed fixed (probes
    re-run: per-node requester, zero-request per-node, two per-node requesters, reservation); no regression in 3d;
    graphs match. One nit, verified by the main session:

    | # | Finding | Resolution |
    |---|---|---|
    | R3-N1 | With a declared `max 0` (CPU) or a max under 1 MiB, the migration writes `0m` / `0Mi`; both runtimes apply no limit for 0 (`runtime/container.go:287,290`, `runtime/containerd.go:564,568`), so the cap becomes unlimited | **fix** 3e.6 |
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
