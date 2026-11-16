# Tasks

M230. Every group lands its own tests and docs. Failing tests are recorded (command and failure line) before the code that fixes them. Design decisions D1–D7 are in `design.md`.

## 0. Failing tests first

- [ ] 0.1 `lang/TestCompileRefusesServiceNamesWithFieldRoots` (valid: `payments/checkout`, `frontend/web`, `payments/checkout/api`, `payments/cpu`, flat `image`; invalid: `web/image`, `web/instances`, `payments/checkout/image`) and `api/TestApplyRefusesServiceNameWithFieldRoot` (both apply paths). Record the failures
- [ ] 0.2 `controllers/TestIntentResolverMarkerMustBeEmptyValued` and `controllers/TestIntentResolverHierarchicalServiceCounts` (spec service-scaling: `payments/checkout` count 3 reaches the effective count; `payments` and `payments/checkout` side by side). Record the failures
- [ ] 0.3 `api/TestApplyRefusedOverStoredNameCollision` and `cmd/cca/TestServerReportsStoredInvalidServiceNames`, each for both declaration orders of `web` and `web/image`. Record the failures

## 1. Rule, roots and splitter

- [ ] 1.1 Flow 32 "Service Names and Fact Keys" in `design/runtime-invariant-flows.md` (name rule, splitter, declaration marker, stored-damage report and refusal), and the key layout section of `design/etcd-schema.md` (hierarchical names, the roots, the rule; link, do not copy the list)
- [ ] 1.2 Field roots in `types` and `types/TestEveryServiceKeyConstructorUsesARegisteredRoot` (D1, parses `types/keys.go`). Verify by adding a throwaway constructor with an unregistered root: the test fails naming it
- [ ] 1.3 `types.SplitServiceFactKey` (D2) and `types/TestSplitServiceFactKeyRoundTrip` (flat, 2- and 3-segment names, markers, every root, nested and dynamic field paths, every service prefix family). Verify `go test ./types`
- [ ] 1.4 `types.ValidateServiceName` (D3) with `types/TestValidateServiceNameFieldRoots`

## 2. Readers

- [ ] 2.1 Inventory: classify every non-test reader of a service prefix (31 files) as service-key splitter, generic entity splitter, or neither; write the result as a new section in a call graph (`design/graphs/SERVICE_KEY_GRAPH.md`): function and line, prefix parsed, splitter used, callers, marker or field fact. Verify by grepping `strings.(Split|SplitN|Index|Cut|TrimPrefix|HasPrefix|LastIndex)` in those files against the classification
- [ ] 2.2 `controllers/`: helpers (service variant of `collect*ValuesBySuffix`, `splitServiceFactKey` removed, generic splitter renamed), intent (marker detection D4, counts), autoscale, drain, warmzero, rollout, eviction, network, cloud_*, credential_broker, stateful_*, instance, endpoint, clusterscale, init, instance_gc as classified. Verify 0.2 passes and `go test ./controllers`
- [ ] 2.3 `scheduler/scheduler.go`, `api/status.go`, `api/handler_gc.go`, `cmd/cca/command_server.go` as classified. Verify `go test ./scheduler ./api ./cmd/cca`
- [ ] 2.4 `tenant/` (scoped_query, fair_scheduler, quota, garbage_collector, lifecycle, tenant) and `security/` (fact_authorizer, secret_delivery) as classified; the two hand-written field lists removed. Verify `go test ./tenant ./security`
- [ ] 2.5 `controllers/TestServiceKeyReadersAgreeOnOverlappingNames`: every migrated reader, on a store with `payments` and `payments/checkout`, attributes each key to the same service. Verify it passes, and that it fails with `splitServiceFactKey` restored

## 3. Write paths

- [ ] 3.1 `lang.Compile` calls `ValidateServiceName` (covers apply through gate and direct, `--simulate`, `cca run`). Verify 0.1 passes
- [ ] 3.2 `cca lint` and `cca apply --simulate`: `cmd/cca/TestLintAndSimulateRefuseServiceNameWithFieldRoot`
- [ ] 3.3 Scale API: `api/TestScaleRefusesServiceNameWithFieldRoot`

## 4. Stored damage

- [ ] 4.1 Startup scan (D5) in `cca server`: log each invalid stored name with the colliding key and the possibly damaged service, keep running. Verify `TestServerReportsStoredInvalidServiceNames` passes for both orders
- [ ] 4.2 Apply refusal over stored collisions (D6) on both paths. Verify `TestApplyRefusedOverStoredNameCollision` passes for both orders; measure the extra scan on a store with 100 services
- [ ] 4.3 `design/multi-tenancy.md`: the name rule next to the hierarchical naming section; what an operator sees for a stored invalid name and that removal comes with M206

## 5. Verification and review

- [ ] 5.1 `gofmt`, `golangci-lint run ./...` (default and `cloudprovider` tags), `go test ./... -count=1`
- [ ] 5.2 M214 replay case: `apply` of a configuration with `web/image` is refused naming the reserved segment; check `test/e2e/` workloads for names with a reserved segment
- [ ] 5.3 Independent review: `code-reviewer` and `security-audit` on the change diff against this change's specs and design; record every finding as fixed or rejected with one line in this file
- [ ] 5.4 Deploy Testbed run green on the final commit; its server log shows the startup scan found no invalid names (user pushes and dispatches)

## 6. Roadmap

- [ ] 6.1 `milestones.md` Phase 230 links this change
- [ ] 6.2 Changelog row for M230 names the behaviour changes (names with a reserved segment refused; hierarchical services get counts)
- [ ] 6.3 M206 requirement in `milestones.md`: pruning removes an invalid legacy service while restoring or keeping the surviving service's facts, in either declaration order, with atomicity, idempotency and ownership tests, and an explicit removal in `cca diff`

## Workflow follow-up

- Release with `scripts/release.sh M230 <version>` after 5.4, then archive this change (syncing `service-naming` and the `service-scaling` addition into `openspec/specs/`).
