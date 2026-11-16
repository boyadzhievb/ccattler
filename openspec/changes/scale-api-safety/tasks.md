# Tasks

M217. Every group lands its own tests and docs. Failing tests are recorded (command and failure line) before the code that fixes them. Flow 31 in `design/runtime-invariant-flows.md` is the chain; `design.md` D1–D7 the decisions.

## 0. Failing tests first

- [ ] 0.1 Write the scale API tests named in the spec (`TestScaleRefusesUndeclaredService`, `TestScaleReportsStoreError`, `TestScaleConflictsWhenServiceChanges`, `TestScaleWarnsFromThreshold`, `TestScaleAboveLimitNeedsApproval`, `TestScaleApprovalMustRepeatCount`, `TestScaleApprovalNeedsClusterAdmin`, `TestScaleApprovedAboveLimit`) in `api/`, and adjust `TestScaleService` to declare `web` first. Run them and record each failure line
- [ ] 0.2 Write `TestApplyWarnsFromThreshold`, `TestApplyAboveLimitNeedsApproval` and `TestApplyApprovalCoversOnlyThatCount` in `api/`, covering both the policy-gate and the direct apply path. Record the failures
- [ ] 0.3 Write `controllers/TestIntentResolverStopsUndeclaredServices`. Record the failure (today the undeclared name keeps its effective count)
- [ ] 0.4 Write `cmd/cca/TestScaleCommandSendsApproval` (no terminal without flag → exit 1 with the flag in the message; flag → `approved_instances` sent; mismatched retyped count → nothing sent). Record the failure

## 1. Limit rule

- [ ] 1.1 Add `ServiceInstanceWarningThreshold = 500`, `ServiceInstanceApprovalLimit = 600` and the check function (D1) in `types`, with table tests at 499, 500, 600, 601, approved equal, approved different, and 100000 approved. Verify `go test ./types -run InstanceLimit`

## 2. Scale API

- [ ] 2.1 `handleScale`: declaration check (404), limit rule, `cluster.admin` check only above the limit (403), one transaction guarded by the declaration revision (409 on conflict, 500 on store error), `warning` in the answer (D3, D4). Verify the 0.1 tests pass
- [ ] 2.2 API server audit logger: `SetAuditLogger` on `api.Server`, wired in `cmd/cca/command_server.go` with the policy gate's logger; record approvals and refused approvals (D7). Verify in `TestScaleApprovedAboveLimit` and `TestScaleApprovalNeedsClusterAdmin` that the entry exists and holds no token or secret
- [ ] 2.3 `AUTHZ_GRAPH.md`: note on `/api/scale` that an approval above the limit adds `cluster.admin`; `api/TestDocCrossCheckAuthzRouteTable` still passes

## 3. Apply

- [ ] 3.1 Count collection per service (`instances`, `scale min`, `scale max`, largest wins) and the limit check before the authorization stage, shared by the gate and the direct path; `approved_instances` in the apply request body; warnings in the apply answer (D5). Verify the 0.2 tests pass
- [ ] 3.2 `cca apply --approve-above-limit <count>` sends the approval and prints warnings; `cca apply --simulate` applies the rule locally without the capability check. Verify with a `cmd/cca` test for both
- [ ] 3.3 `design/dsl-reference.md`: `instances` and `scale` explain the warning, the limit and the approval

## 4. CLI scale

- [ ] 4.1 `cca scale`: on a terminal above the limit, ask for the count again and send it as approval; otherwise `--approve-above-limit <count>`; print warnings; print the server's error unchanged. Verify `TestScaleCommandSendsApproval` passes
- [ ] 4.2 `design/cli-reference.md` and `website/reference/cli.md`: `cca scale` and `cca apply` flags, the thresholds, and the capacity caveat (not a capacity promise; large counts can overload etcd and the controllers). `TestDocCrossCheckCLICommandsExist` passes

## 5. Orphan clean-up

- [ ] 5.1 Intent resolver: effective count 0 for a name with an intent but no `desired/service/{name}` (D6); declared services unchanged. Verify `TestIntentResolverStopsUndeclaredServices` passes and `controllers/TestWriteDomain*` still pass
- [ ] 5.2 `integration/TestOrphanScaleInstancesStopped`: user intent for an undeclared name with running instances → instances stopped. Verify it passes (`go test ./integration -run Orphan`)

## 6. Documentation and website

- [ ] 6.1 Flow 31 in `design/runtime-invariant-flows.md` matches what was built (review the draft written before this change); `SCHEDULING_GRAPH.md` or `CONTROLLER_DATA_FLOW_GRAPH.md` updated for the intent resolver rule
- [ ] 6.2 Website scaling guide (`website/guides/`): the warning, the approval, why there is no ceiling and that it is not a capacity promise. Verify the page builds (`test-website` agent or local build)
- [ ] 6.3 `milestones.md` Phase 217 links this change; open the follow-up bug milestone for `tenant/lifecycle.go` leaving instances running (design.md Follow-ups)

## 7. Verification and review

- [ ] 7.1 `gofmt`, `golangci-lint run ./...`, `go test ./... -count=1` all clean
- [ ] 7.2 M214 replay cases that scale undeclared services or huge counts now expect 404 / 400; check `test/e2e/adversarial/cases.yml` and the e2e scripts for scale-before-apply; replay passes (run via `ssh bojan@localhost`)
- [ ] 7.3 Independent review: `code-reviewer` and `security-audit` on the change diff against this change's specs and design; record every finding as fixed or rejected with one line in this file
- [ ] 7.4 Deploy Testbed run green on the final commit (user pushes and dispatches)

## Workflow follow-up

- Release with `scripts/release.sh M217 <version>` after 7.4, then archive this change (syncing `service-scaling` into `openspec/specs/`).
