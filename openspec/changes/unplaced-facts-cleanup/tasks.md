# Tasks

Milestone M262. Design: [design.md](design.md). Spec delta: [specs/resource-requests/spec.md](specs/resource-requests/spec.md).

## 1. Tests first (record each failing before section 2)

- [x] 1.1 `TestSchedulerDeletesUnplacedFactsOfStoppedInstance` (scheduler): stored unplaced facts for `web-1`; two
  sub-cases, state `stopped`, and state `pending` with `derived/instance/web-1/controller_stopped = true`. One cycle
  proposes a delete of both keys. Record the failure.
  Red (scheduler/unplaced_lifetime_test.go), both sub-cases: `cycle proposed no delete of
  derived/scheduler/unplaced/web-1/reason` (and `/requirements`).
- [x] 1.2 `TestSchedulerDeletesUnplacedFactsOfRemovedInstance` (scheduler): stored unplaced facts, no
  `observed/instance/web-1/*`. One cycle deletes both keys. Record the failure.
  Red: `cycle proposed no delete of derived/scheduler/unplaced/web-1/reason` (and `/requirements`).
- [x] 1.3 `TestSchedulerDeletesUnplacedFactsOfPlacedInstance` (scheduler): stored placement, and a sub-case placed in
  this cycle. Expected to pass already (pins today's behaviour); record that.
  Passes before the fix, both sub-cases (`stored placement`, `placed in this cycle`).
- [x] 1.4 `TestClusterAutoscaleIgnoresUnplacedFactsOfInstancesNotWaiting` (controllers): `insufficient_capacity`
  facts for `web-1` with `web-1` stopped, controller-stopped, placed, and without instance facts; no capacity request
  in any sub-case. Record the failure.
  Red in all four sub-cases: `capacity request for web-1 = true, want false`; the added control row (`pending
  without placement`, request wanted) passes.
- [x] 1.5 `TestSchedulerDoesNotRewriteUnchangedUnplacedFacts` (exists, M243 3g.1): confirm it also asserts no delete
  for the waiting instance (it checks every change under the prefix); no change expected.
  Confirmed: its loop fails on any change type under `derived/scheduler/unplaced/`, deletes included; passes.

## 2. Implementation

- [x] 2.1 Scheduler (D1): compute the waiting set (`unplaced \ placedInThisCycle`) once in `Reconcile`; pass it to
  `emitUnplacedDemandFacts` and to `cleanupStaleUnplacedFacts`, which deletes the reason and requirements of every
  stored ID not in it. Keep functions under 80 lines, doc comments, descriptive names.
  New `selectInstancesWaitingForPlacement` (sorted slice, emit order); cleanup turns it into a set and deletes every
  other stored ID. 1.1 and 1.2 green, 1.3 and 1.5 still green.
- [x] 2.2 Cluster autoscale (D2): `extractUnplacedDemand` keeps an entry only when the instance's observed state is
  `pending`, it has no `controller_stopped = true` marker and no `placement/{id}`.
  Split into `parseUnplacedDemandFacts` + filter `isInstanceWaitingForPlacement`, reusing `parseInstanceFieldsFromFacts`,
  `endpointEligibleState` and `parsePlacementsByNode` (no new parser); early return with no demand. 1.4 green.
- [x] 2.3 Update the existing cluster-autoscale test fixtures that build unplaced facts without instance facts to
  include `observed/instance/{id}/state = pending` (design, Risks). No assertion changes.
  `pendingInstanceStateFact` added to all six fixtures (three went red without it); rollout tests unaffected.
- [x] 2.4 Docs: SCHEDULING_GRAPH §1/§6 and the Writes table (cleanup now keyed on the waiting set),
  CONTROLLER_DATA_FLOW_GRAPH cluster-autoscale entry, runtime-invariant-flows §10 scheduler bullet (facts live while
  the instance waits).
  Done, plus Deletes table, reverse indexes and shifted line numbers; new cluster-autoscale section in the data-flow graph.

## 3. Checks

- [x] 3.1 gofmt, golangci-lint, `go test -count=1 ./...`, `-race` on `scheduler` and `controllers`.
  `gofmt -l` empty; `golangci-lint run ./...` 0 issues; `go test -count=1 ./...` all ok; `-race` scheduler and
  controllers ok.
- [ ] 3.2 Deploy cluster by hand, `deploy-run:unplaced-cleanup`, on the cluster a push run leaves: apply a service with
  `memory 64Gi` and `instances 2`, `cca watch derived/scheduler/unplaced/` shows one put per key per instance;
  `cca scale <svc> 0` → a delete of each key within 10 s; then nothing more. Record commands and output here.
- [ ] 3.3 Independent review (CLAUDE.md): `code-reviewer` (Opus, effort high) on the milestone diff against this design
  and the spec, including the patterns review against `design/Anti-pattern.md`. Record each finding as fixed or
  rejected, with one line of reasoning.
- [ ] 3.4 Closing full Deploy Testbed run (job summary says `full run`). Record the run ID.
- [ ] 3.5 Update the milestones.md M262 row to the final scope, link this change from Phase 262, archive (syncing
  `resource-requests`), release with `scripts/release.sh`.
