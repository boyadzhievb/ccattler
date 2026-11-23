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
- [x] 3.2 Deploy cluster by hand, `deploy-run:unplaced-cleanup`, on the cluster a push run leaves: apply a service with
  `memory 64Gi` and `instances 2`, `cca watch derived/scheduler/unplaced/` shows one put per key per instance;
  `cca scale <svc> 0` → a delete of each key within 10 s; then nothing more. Record commands and output here.
  - 2026-11-23 on the cluster push run 45207059879 (819c02c, success) left; `/tmp/m262*.cca` on cca-deploy-ctrl
    (whoami, `memory 64Gi`, `cpu 100m`).
    - **Pass.** `m262big` (apply, 2 instances): one put of reason and requirements per instance (rev 2278);
      `cca scale m262big 0` → deletes of all four keys (rev 2450), within the 10 s window; nothing after rev 2456.
      `m262b` (`cca scale` 1, then 2, then 0): put once, deleted 2 revisions after `controller_stopped`, GC 3 later.
    - **Transient re-put, healed by M262 (pre-existing cause → M265):** after a fresh apply (`m262c`), scale 0 gave
      put (4345) → marker (4523) → delete (4526) → GC of the whole instance, atomic `gc-<id>` group (4528) → **put
      again (4532)** → delete (4533). No single revision has the instance pending without a marker: the runner scans
      each watched prefix separately (`scanFactsForController`, runner.go:603), so one cycle read `observed/instance/`
      before 4528 and `derived/instance/` after it; the input-key guards are capped, so the commit went through. D1
      deleted the stray facts one cycle later (before M262 they would have stayed).
    - **Unrelated finding → M266:** every running instance's unchanged `observed/instance/{id}/state = running` is
      re-put once per second (20 puts per instance in 20 s, 7 instances): `publishInstanceStateUnlessStopped`
      (agent.go:2097, since M241) always commits its CAS; etcd `Put` skips unchanged values, etcd transactions do not,
      and the memory store's transactions do (memory.go:514), so unit tests cannot see it.
- [ ] 3.3 Independent review (CLAUDE.md): `code-reviewer` (Opus, effort high) on the milestone diff against this design
  and the spec, including the patterns review against `design/Anti-pattern.md`. Record each finding as fixed or
  rejected, with one line of reasoning.
  - Round 1 (2026-11-23, on 3048f72; Codex: approve, no findings). Verified by the main session:

    | # | Finding | Resolution |
    |---|---|---|
    | 1 | Uncapped cleanup: >~30 IDs fill the runner's 60-change budget (runner.go:44-55, ungrouped deletes sort first), deferring placements for several cycles; design said the runner splits (wrong) | **fix** 3a.1 (D4, one 40-change budget for unplaced puts and deletes, puts first) + design/spec corrected |
    | 2 | ~58 SCHEDULING_GRAPH line numbers shifted by this commit; STATEFUL_SERVICES_GRAPH.md:367 `scheduler.go:520` | **fix** 3a.3 |
    | 3 | `endpointEligibleState` name/doc speak of endpoints, now also decides cluster-scaling demand | **fix** 3a.2 (rename `stateWithControllerStop`, doc names both callers) |
    | 4 | `extractUnplacedDemand` now filters; name hides it at both call sites | **fix** 3a.2 (`extractWaitingUnplacedDemand`) |
    | 5 | Test helper `factOf` duplicates `buildFacts(kv(...))` | **rejected**: 2 places, below the 3+ threshold |
    | 6 | Rollout builds a second instance-field index when demand exists | **rejected**: O(n), no benchmark showing cost (CLAUDE.md optimization principle) |

## 3a. Rework from review round 1

- [x] 3a.1 D4 (revised after the stop-time Codex check: capping only the deletes leaves the uncapped puts able to fill
  the budget): named constant `maxSchedulerBookkeepingChangesPerCycle = 40` shared by the unplaced puts
  (`emitUnplacedDemandFacts`) and deletes (`cleanupStaleUnplacedFacts`); puts first, then deletes, each in sorted
  instance-ID order, whole instances only (2 changes each; an unchanged put that is skipped costs nothing). Red first:
  `TestSchedulerBudgetsUnplacedFactChanges` with the three spec sub-cases (`deletes only`, `puts first`,
  `puts over budget`), including that the placement of a fitting pending instance is still proposed.
  Red (pre-3a code + constant only): `deletes only` 100 deletes / want 40, `puts first` 60 deletes / want 10,
  `puts over budget` 60 puts / want 40 (second cycles 0 / want 40, 40, 20). Green: budget passed from `Reconcile`
  (`emitUnplacedDemandFacts` puts, then `cleanupStaleUnplacedFacts` gets the rest); `unplacedFactsPerInstance = 2`.
- [x] 3a.2 Renames: `endpointEligibleState` → `stateWithControllerStop` (doc names endpoints and cluster scaling as
  callers); `extractUnplacedDemand` → `extractWaitingUnplacedDemand` (clusterscale.go, rollout_facts.go).
  Done in code (no test referenced them) and in runtime-invariant-flows.md (Flow 8 table gains a cluster-autoscale row)
  and INSTANCE_LIFECYCLE_GRAPH reverse index.
- [x] 3a.3 Graphs: regenerate every `scheduler/scheduler.go` line number in SCHEDULING_GRAPH.md (index and §1/§5/§6
  trees) and STATEFUL_SERVICES_GRAPH.md:367, plus CONTROLLER_DATA_FLOW_GRAPH entries for the renames; check each
  against `grep -n`.
  All ~245 scheduler.go numbers regenerated (pairs `:call/:def`, in-body lines, tables, index, constants; most
  predated M262); a script matched 218 to their named function, the other 27 (loops, PUT lines) checked by eye;
  §6 trees, constants and Writes/Deletes rows describe the D4 budget. STATEFUL 365-367, DATA_FLOW helpers.go:173.
- [x] 3a.4 Checks as in 3.1, plus `go test -count=10 -run 'Unplaced' ./scheduler/`.
  `gofmt -l` empty; `golangci-lint run ./...` 0 issues; `go test -count=1 ./...` exit 0 (20 ok, 0 FAIL); `-race`
  scheduler and controllers ok; `-count=10 -run Unplaced ./scheduler/` ok.
- [x] 3a.5 Review round 2 (2026-11-23, on d44bd3f; code-reviewer Opus high: APPROVE; Codex: approve, no findings).
  Round-1 findings 1–4 confirmed fixed; new tests verified red on 3048f72. Suggestions, verified by the main session:

  | # | Finding | Resolution |
  |---|---|---|
  | S1 | No test pins "whole instances only": with all instances costing 2 and an even budget, a per-change budget would pass | **fixed**: sub-case `whole instances` added to `TestSchedulerBudgetsUnplacedFactChanges` (an instance with only its reason changed, cost 1, first; cycle 1 stops at 39, the next instance's 2 puts both come in cycle 2) |
  | S2 | `stateWithControllerStop` doc and Flow 8 table omit rollout, which depends on it via `extractWaitingUnplacedDemand` | **fixed** (main session): doc names all three callers; Flow 8 table has a rollout row |
  | S3 | Stale reasons while puts are deferred not in the failure table | **fixed** (main session): row added to design.md; not a regression |

  S1 result: `whole instances` passes (39 puts in cycle 1, new-20's 2 in cycle 2); red under a temporary per-change
  put loop (`first cycle puts: 40 unplaced keys, want 39`, `unexpected .../new-20/reason`); gofmt, lint, -race ok.
- [x] 3.4 Closing full Deploy Testbed run (job summary says `full run`). Record the run ID.
  - Run 45211387656 (workflow_dispatch, 819c02c): **failed**, `REGRESSED: 541014b05f (whoami) state=pending`.
    Cause, verified in the log, the code and live on the cluster: the scheduler's rebalancing moves whoami onto
    worker-2 by instance count although its memory is full (`selectRebalanceTarget` ignores requests), placement
    puts the replacement back on ctrl, every ~6 s (about 40 whoami instances on worker-2 in 4 minutes). Pre-existing
    (reachable since M243), not in M262's diff → M267. Not citable; a new full run is needed.
  - **Run 45254885232** (workflow_dispatch, 1a7869d, 2026-11-23): `RUN_KIND: full`, workloads airflow, whoami,
    whoami-multiport, gate-tls, no skipped checks, adversarial replay with every case, deploy and replay `success`;
    whoami-churn 4 IDs, bound 6. Closing run for M262 (its code since 819c02c; the scheduler budget it introduced was
    renamed `maxSchedulerBookkeepingChangesPerCycle` in M267 3d).
- [ ] 3.5 Update the milestones.md M262 row to the final scope, link this change from Phase 262, archive (syncing
  `resource-requests`), release with `scripts/release.sh`.
