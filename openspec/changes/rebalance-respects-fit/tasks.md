# Tasks

Milestone M267. Design: [design.md](design.md). Spec delta: [specs/instance-rebalancing/spec.md](specs/instance-rebalancing/spec.md).

## 1. Tests first (record each failing before section 2)

- [x] 1.1 Scheduler: `TestRebalanceSkipsTargetWithoutRoom`, `TestRebalanceSkipsTargetUnderPressure`,
  `TestRebalanceCountsRoomUsedByEarlierMoves`, `TestRebalanceMovesToTargetWithRoom` (the last should pass already;
  record it). Record the failures.
  Result (`scheduler/rebalance_fit_test.go`, before section 2): WithoutRoom `moved [web-00 web-01] to node-2, which
  has no room for a 512Mi request`; UnderPressure `scheduler does not watch derived/node/ ...` and `moved [web-00
  web-01] to node-2, which is under memory pressure`; CountsRoom `moved [web-00 web-01] to node-2, want exactly one`;
  MovesToTargetWithRoom passes.
- [x] 1.2 Agent: `TestAgentDoesNotMarkMovedInstanceStopped` (moved placement → container removed, no `stopped`
  write; sub-case `controller stop` → `stopped` written). Record the failure.
  Result (`agent/moved_instance_test.go`, 7 sub-cases): `moved running`, `moved exited` fail with `stopped written =
  true, want false`; `placement read error` fails the same plus `the placement was never read`; `controller stop`,
  `controller stop after move`, `no placement`, `no placement exited` pass.

## 2. Implementation

- [x] 2.1 D1: shared `candidateFitsRequests` helper used by `node_heap.go` placement and `selectRebalanceTarget`;
  scheduler `Watch()` gains `derived/node/`; skip candidates with `derived/node/{id}/pressure`; `executeRebalanceMoves`
  updates the target's and source's remaining capacity after each move.
  Result: `candidateFitsRequests` (node_heap.go) replaces the inline checks in `buildNodeHeap`, `stopMakesRoom` and
  `NodeCapacityCache.RecordPlacement` and is used by `selectRebalanceTarget`; `extractNodesUnderPressure` feeds a
  `rebalanceInputs` parameter object; `transferRemainingCapacity` after each move. The 4 tests of 1.1 pass.
- [x] 2.2 D2: `cleanupUndesiredInstance` and `cleanupStoppedStaleInstance` skip the `stopped` write when
  `placement/instance/{id}` names another node and there is no controller stop; on a read error they skip it and log.
  Result: both call `publishStoppedUnlessMovedElsewhere` → `isInstanceMovedToAnotherNode`; all 7 sub-cases of
  `TestAgentDoesNotMarkMovedInstanceStopped` pass.
  Rework (main session, design D2 corrected): the move check now runs first, before any removal
  (`checkStaleInstanceMove`); a read error returns with container and store untouched, so the next cycle retries the
  whole cleanup; `publishRemovedInstanceState` replaces `publishStoppedUnlessMovedElsewhere`. Test: `placement read
  error` (and new `placement read error exited`) now have no placement and assert no removal, no `stopped`, then a
  readable next cycle removes and writes `stopped`. Red before the reorder: both read-error sub-cases failed with
  `first cycle: container removed = true, want false`; all 8 pass after. `failingRecordedImageStore` renamed
  `failingKeyStore` (serves both tests).
- [x] 2.3 D3: `cluster_test.sh` records whoami instance IDs at each status poll and fails above the bound explained
  by the test's steps (named constant, comment). Run the script's syntax check (`bash -n`, shellcheck) and run the new
  check by hand against the deploy cluster before committing (feedback: test scripts remotely).
  Implemented: `fetch_cluster_status` records whoami IDs from every `/api/status` read from Step 5.5 on; bound =
  whoami's `desired` after deploy + 1 per scale-up to 2 (5.10, extended 5.13) + whoami instances on worker-1 when
  5.11 stops its agent + `WHOAMI_CHURN_MARGIN` (2); checked in Step 6 before the stability check. `bash -n` passes;
  shellcheck adds only info-level SC2086/SC2317 of kinds the script already has.
  Hand run (main session, 2026-11-23): the script's own `fetch_cluster_status` and churn block, cut verbatim from
  the working tree into a harness (expected 2, margin 2), polled the deploy cluster (still on 819c02c, loop live)
  every 5 s for 60 s as bojan with the run's client certs: `12 distinct whoami instance IDs seen, bound 4` →
  `FAIL: whoami-churn FAILED — 12 whoami instance IDs during the run, the test's steps explain at most 4`, with the
  ID list. The check catches the real loop.
- [x] 2.4 Docs: SCHEDULING_GRAPH §4 and Watch list, INSTANCE_LIFECYCLE_GRAPH / CALL_GRAPH (agent cleanup), and a
  "Rebalance move" entry in runtime-invariant-flows (scheduler moves → source removes without `stopped` → target
  starts).
  Result: SCHEDULING_GRAPH §1/§4/§7/§8, reads, reverse index and line numbers; CALL_GRAPH and INSTANCE_LIFECYCLE_GRAPH
  agent cleanup; OBSERVABILITY_GRAPH line numbers; runtime-invariant-flows Flow 35 and a §10 rebalance bullet.

## 3. Checks

- [x] 3.1 gofmt, golangci-lint, `go test -count=1 ./...`, `-race` on `scheduler` and `agent`.
  Result: `gofmt -l` empty; `golangci-lint run ./...` 0 issues; `go test -count=1 ./...` 20 ok, 2 without tests;
  `go test -count=1 -race ./scheduler/ ./agent/` ok (1.4 s, 29.7 s). Re-run after the D2 rework: same results
  (0 issues; 20 ok, 2 without tests; race ok 1.4 s, 29.7 s).
- [x] 3.2 Deploy cluster by hand on the cluster a push run leaves: `cca describe service whoami` events for 2 minutes
  show no created/stopped cycle; `journalctl -u cca-server | grep "rebalancing instance"` moves only to nodes without
  allocation pressure; a moved instance keeps its ID and runs on the target. Record commands and output here.
  - 2026-11-23 on the cluster push run 45243076404 (fd8c09d, success; Test 45243076597 also green) left: the run's
    churn check `3 distinct whoami instance IDs seen, bound 5`; live, `cca describe service whoami` created-events
    1 → 1 over 120 s; `journalctl -u cca-server --since -10min | grep -c "rebalancing instance"` → 0; nodes 3/2/2
    instances. **No churn.** Not exercised here: an actual move (the cluster is balanced) — covered by the unit and
    agent tests; the closing full runs (3.4) include the node-failure step that unbalances it.
- [ ] 3.3 Independent review (CLAUDE.md): `code-reviewer` (Opus, effort high) on the milestone diff against this design
  and the spec, including the patterns review against `design/Anti-pattern.md`. Record each finding as fixed or
  rejected, with one line of reasoning.
  - Round 1 (2026-11-23, on 800c0cc; Codex: approve, no findings). Verified by the main session:

    | # | Finding | Resolution |
    |---|---|---|
    | B1 | Rebalance room ignores placements made earlier in the same cycle (scheduler.go:584 uses stored `placements`); reproduced: a placement and a move both land on a node with room for one | **fix** 3b.1 |
    | B2 | Source cleanup of a moved instance deletes `observed/instance/{id}/pull_status` and `network/allocation/{id}` (keyed by ID only) the target may have written | **fix** 3b.2 (skip both store deletes when moved; keep node-local `ReleaseIP`) |
    | B3 | `WORKER1_WHOAMI_INSTANCES=$(fetch_cluster_status …)` has no fallback under `set -e` (cluster_test.sh:1493) | **fix** 3b.3 |
    | N1 | A move is stop-old-then-start-new for that instance ID: the VIP keeps the removed source container until the target publishes | **decision with the user** (see 3b.6) |
    | D1 | SCHEDULING_GRAPH placement call sites and store-table lines stale by +44 | **fix** 3b.4 |
    | S1 | `cleanupUndesiredInstance` / `cleanupStoppedStaleInstance` share a near-duplicate teardown >10 lines, now plus the move check | **fix** 3b.5 (shared teardown helper) |
    | S2 | `checkStaleInstanceMove` is a thin wrapper | **fix** 3b.5 (fold into the helper) |
    | S3 | `controller_stopped` read as `== "true"` here, as presence in `isInstanceMarkedForSkip` | **fix** 3b.5 (one helper for both) |
    | S4 | WARN "load imbalance" every cycle when no target fits | **rejected**: accepted in the design; noise only, logged with the counts |

## 3b. Rework from review round 1

- [x] 3b.1 B1: rebalance computes room from `mergeThisCyclePlacements(placements, changes)` (this cycle's placements
  and preemptions count). Red first: spec scenario and test "a placement and a move in one cycle share the target's
  room" (`TestRebalanceCountsRoomTakenByThisCyclePlacements`).
  Result: `buildRebalanceCandidateNodes` uses the preemption reservations when a preemption ran (victims freed,
  requesters taken: the room the next cycle sees), else usage from `mergeThisCyclePlacements`; this cycle's victims
  are not movable (`isPreempted`). Red: `placement` `moved [web-00] to node-2, whose room this cycle's placement
  already took`; `preemption` `moved [web-00 web-01] to node-2, whose room this cycle's preemption already took`;
  `TestRebalanceDoesNotMovePreemptedVictim` `moved [web-00] to node-2, want one instance other than the preempted
  web-00`. All pass after.
- [x] 3b.2 B2: when the instance moved elsewhere, the source skips `clearPullStatusFromStore` and
  `DeleteNetworkAllocation` (keyed by ID), keeps the node-local `ReleaseIP`; extend `TestAgentDoesNotMarkMovedInstanceStopped`.
  Result: both facts are cleared exactly when stopped is written. Red: `moved running`/`moved exited` `first cycle:
  observed/instance/web-1/pull-status cleared = true, want false` and the same for `network/allocation/web-1`; all 8
  sub-cases pass after (`releaseStaleInstanceNetwork`).
- [x] 3b.3 B3: fallback for the `WORKER1_WHOAMI_INSTANCES` read (`|| echo 0` and a named `fail` if it must be known);
  `bash -n`, shellcheck.
  Result: `|| echo ""` plus a named `fail "whoami-churn: cannot read the whoami instances on …"` (the bound needs the
  count); the Step 5.5 `desired` read had the same gap (its named fail was unreachable under `set -e`) and got the same
  fallback. `bash -n` passes; shellcheck: 78 findings before and after, none on the changed lines.
- [x] 3b.4 D1: regenerate every `scheduler.go` line number in SCHEDULING_GRAPH (call sites §1–§3, store tables), checked
  with `grep -n`.
  Result: §1–§6 call sites and definitions, store tables, reverse index, constants and file size regenerated after all
  3b changes; a script checked every `name(…) :call/:def` pair against `scheduler.go` (0 mismatches). Agent numbers
  the 3b changes shifted are updated in INSTANCE_LIFECYCLE_GRAPH and OBSERVABILITY_GRAPH.
- [x] 3b.5 S1–S3: one teardown helper for both cleanup paths (move check first, then teardown); fold
  `checkStaleInstanceMove`; one `isControllerStopped` helper used by both readers.
  Result: `teardownStaleInstance(ctx, id, removeContainer func())` (each cleanup passes its stop step) replaces both
  bodies and `checkStaleInstanceMove`; `isControllerStopped` reads value `"true"` (as the scheduler and the API do;
  every writer writes `"true"`, so `isInstanceMarkedForSkip`'s presence check behaves the same; a read error is still
  "not marked" there and an error for the move check). Graphs and Flow 35 updated.
- N1 decided by the user (2026-11-23): option A, in-place moves under the same ID with a short, documented backend
  gap (design D4); zero-gap moves are a later milestone (milestones.md). Safeguards, red tests first:
- [x] 3b.6a Source does not leave the moved instance looking running: after removing a moved instance's container
  the source agent writes `observed/instance/{id}/state = pending` and clears its readiness, in one transaction
  conditioned on `observed/instance/{id}/node` still naming the source (and the state revision), so it never
  overwrites the target's publish; the agent's state publish writes `node` in the same transaction as `state`.
  Tests: the endpoint drops the source backend after removal; the target's later publish wins; the source never
  overwrites a target publish.
  Result: `markMovedInstancePending` (TXN on node + state revisions); `publishInstanceStateUnlessStopped` writes
  `node` in its state transaction. `TestSourceAgentMarksMovedInstancePending` red: `after the source removal: web-1
  state="running" node="node-1" readiness="ready", want state="pending" …`, `endpoints for [web-1], want []`, and the
  race case `the source never compared the node key`; all 3 sub-cases pass after (the race case makes the target's
  state transaction run between the source's reads and its write).
- [x] 3b.6b A failed target startup does not look healthy: tests that a target pull failure ends in
  `image_pull_backoff` and a start failure in `failed`, both published by the target, and that a dead target agent
  leaves the instance `pending` with its placement on the dead node, which node-failure handling marks.
  Result (verification, existing behaviour, passed first run): `TestTargetStartupFailureAfterMoveIsNotRunning`
  (agent, both sub-cases: node-2 publishes `image_pull_backoff` / `failed` with `node` = node-2),
  `TestNodeFailureMarksMovedInstanceOnDeadTarget` (controllers: placement on the dead node-2, state pending, observed
  node node-1 → `node_failure` marker).
- [x] 3b.6c Anti-flap: a moved instance is not moved again within `rebalanceMoveCooldown` (10 min), tracked in the
  scheduler-owned fact `derived/scheduler/rebalanced/{id}` (RFC3339 move time, written in the move's group, deleted
  once the instance is gone, M262-style budget); moves still need a strict count improvement. Test: not moved twice
  within the cooldown, movable after.
  Result: `rebalanceMoveCooldown` = 10 min, `extractRebalanceMoveTimes`, `findInstancesInRebalanceCooldown`,
  `cleanupGoneInstanceRebalanceFacts` (≤ `maxRebalanceCooldownDeletesPerCycle` = 10), injectable clock
  (`Scheduler.currentTime`), Watch and the scheduler's write domain (`controllers/topological_sort.go`) gain
  `derived/scheduler/rebalanced/`. Red: `within cooldown` `moved [web-00 web-01]: web-00 moved = true, want false`;
  both sub-cases `move of web-0x does not record derived/scheduler/rebalanced/…`; `TestRebalanceCooldownFactDeleted
  WithInstance` `scheduler does not watch …` and `the cooldown fact of gone-1 … was not deleted`. All pass after.
- [x] 3b.6d Tests for a multi-instance service (at least one ready backend during a move) and a single-instance
  service (the gap shows as not ready, not running, and recovers when the target runs).
  Result: `TestMoveGapMultiInstanceKeepsReadyBackend` red `during the gap: endpoints for [web-1 web-2], want [web-2]`
  (and the same before the target's probe passed); `TestMoveGapSingleInstanceShowsNotReady` red `during the gap:
  web-1 state="running" …`, `endpoints for [web-1], want []`. Both pass after (endpoints from the real
  `EndpointController` over the store).
- [x] 3b.6e Docs: Flow 35 and the spec state the gap (multi-instance: one backend briefly; single-instance: short
  outage) and that removal is never evidence that the new placement runs.
  Result: Flow 35 (chain, diagram, gap, evidence, cooldown), §10 rebalance bullet, design D4, spec requirement text and
  four scenarios, etcd-schema watch table.
- [x] 3b.7 Checks as in 3.1.
  Result: `gofmt -l` empty; `golangci-lint run ./...` 0 issues; `go test -count=1 ./...` 20 ok, 2 without tests;
  `go test -count=1 -race ./scheduler/ ./agent/ ./controllers/` ok (1.5 s, 29.7 s, 6.7 s); `openspec validate` valid
  (one warning: the requirement "A moved instance keeps running under its ID" is over 500 characters).

## 3c. Review round 2 (2026-11-23, on fd8c09d; code-reviewer Opus high + Codex: request changes)

Round-1 findings B1–B3, D1, S1–S3 confirmed fixed; new tests verified red on 800c0cc. Verified by the main session:

| # | Finding | Resolution |
|---|---|---|
| R2-1 | A source agent that is behind (long pull or init in the same cycle) still publishes `running` + `node=source` over the target's publish (the CAS compares only the state revision); its next cleanup then sets the instance `pending` while it runs on the target. Reproduced (Claude + Codex) | **fix** 3c.1 |
| R2-2 | Scheduler worst case without placements is 61 changes (unplaced 40 + preemption 3 + rebalance 2×4 + cooldown deletes 10) > runner's 60 | **fix** 3c.2 |
| R2-3 | `markMovedInstancePending` (and `publishInstanceStateUnlessStopped`) log a store error as "changed meanwhile" | **fix** 3c.3 |
| R2-4 | A move time in the future (clock skew between HA leaders) extends the cooldown by the skew | **fix** 3c.3 (one line in D4; bounded by skew, no code) |

- [x] 3c.1 R2-1: `findInstancesPlacedOnThisNode` keeps the revision of `placement/node/{self}/{id}` in `placedInstanceInfo`;
  the publish transaction compares it (a move deletes the key, so a behind source's publish fails). Red first: a test
  of exactly the reviewer's order (source scans → move → target publishes running → source publishes → source
  cleanup) ending with the instance running on the target with its endpoint. Update D4, Flow 35.
  Result: `placedInstanceInfo.placementRevision`; `publishInstanceStateToStore`/`publishInstanceStateUnlessStopped`
  take the `placedInstanceInfo`, so every production publish (8 call sites, all from the cycle's scan) carries it; 0 =
  no guard, only for infos tests build by hand. Red (sub-case `source behind` of
  `TestSourceAgentMarksMovedInstancePending`): `after the behind source's publish: web-1 state="running"
  node="node-1"`, `after the source cleanup: web-1 state="pending" node="node-1" readiness=""`, `endpoints for [],
  want [web-1]`; passes after. The test helpers now move as the scheduler does (delete `placement/node/node-1/…`).
  D4, Flow 35, spec scenario, INSTANCE_LIFECYCLE_GRAPH, CALL_GRAPH updated.
- [x] 3c.2 R2-2: size cooldown deletes from the remaining budget (as the unplaced cleanup does) so the scheduler's
  worst case without placements stays ≤ 60 with room for placements; fix the M262 "20 stay for placements" comment;
  a test asserting the worst-case output of one cycle stays within the runner's cap.
  Result: cooldown deletes share `maxUnplacedFactChangesPerCycle` (40) with the unplaced puts and deletes
  (`maxRebalanceCooldownDeletesPerCycle` removed); worst case without placements 40 + 3 + 8 = 51, comment says 9
  stay for placements. Red: `TestSchedulerWorstCaseOutputStaysWithinRunnerCap` `worst-case cycle has 61 changes,
  runner cap 60: room for placements -1`; passes after (3 preemptions and 2 moves asserted present).
  SCHEDULING_GRAPH line numbers remapped from the committed version and re-checked by script (0 mismatches).
- [x] 3c.3 R2-3/R2-4: store errors logged as errors (with the error) in both functions; D4 notes clock skew.
  Result: transaction errors and read errors log ERROR with the error; a lost compare stays INFO. D4 has the
  clock-skew line.
- [x] 3c.4 Shorten the long requirement "A moved instance keeps running under its ID" (move detail into scenarios)
  so `openspec validate` has no warning.
  Result: requirement text cut to four sentences; "removal is never evidence" moved into the gap scenario; new
  scenario "A source that is behind never overwrites the target". `openspec validate`: valid, no warning.
- [x] 3c.5 Checks as in 3.1.
  Result: `gofmt -l` empty; `golangci-lint run ./...` 0 issues; `go test -count=1 ./...` 20 ok, 2 without tests;
  `-race` on scheduler, agent, controllers ok (1.4 s, 29.8 s, 6.8 s).
- [ ] 3.4 Closing: two consecutive full Deploy Testbed runs (job summary says `full run`), both passing, with the
  whoami-churn check at zero unexplained instance IDs. Record both run IDs and the whoami ID counts.
- [ ] 3.5 Update the milestones.md M267 row to the final scope, link this change from Phase 267, archive (syncing
  `instance-rebalancing`), release with `scripts/release.sh`.
- [ ] 3.6 Note: the ctrl agent logged `etcd watch event dropped (channel buffer full)` during the loop. After the fix,
  check whether it still appears in the closing run's logs; if it does, record it as its own milestone.
