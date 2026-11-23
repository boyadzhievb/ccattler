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
- [ ] 3.2 Deploy cluster by hand on the cluster a push run leaves: `cca describe service whoami` events for 2 minutes
  show no created/stopped cycle; `journalctl -u cca-server | grep "rebalancing instance"` moves only to nodes without
  allocation pressure; a moved instance keeps its ID and runs on the target. Record commands and output here.
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

- [ ] 3b.1 B1: rebalance computes room from `mergeThisCyclePlacements(placements, changes)` (this cycle's placements
  and preemptions count). Red first: spec scenario and test "a placement and a move in one cycle share the target's
  room" (`TestRebalanceCountsRoomTakenByThisCyclePlacements`).
- [ ] 3b.2 B2: when the instance moved elsewhere, the source skips `clearPullStatusFromStore` and
  `DeleteNetworkAllocation` (keyed by ID), keeps the node-local `ReleaseIP`; extend `TestAgentDoesNotMarkMovedInstanceStopped`.
- [ ] 3b.3 B3: fallback for the `WORKER1_WHOAMI_INSTANCES` read (`|| echo 0` and a named `fail` if it must be known);
  `bash -n`, shellcheck.
- [ ] 3b.4 D1: regenerate every `scheduler.go` line number in SCHEDULING_GRAPH (call sites §1–§3, store tables), checked
  with `grep -n`.
- [ ] 3b.5 S1–S3: one teardown helper for both cleanup paths (move check first, then teardown); fold
  `checkStaleInstanceMove`; one `isControllerStopped` helper used by both readers.
- [ ] 3b.6 N1 (after the user's decision): see milestones.md Phase 267.
- [ ] 3b.7 Checks as in 3.1.
- [ ] 3.4 Closing full Deploy Testbed run (job summary says `full run`), including the new churn check. Record the
  run ID and the whoami instance count.
- [ ] 3.5 Update the milestones.md M267 row to the final scope, link this change from Phase 267, archive (syncing
  `instance-rebalancing`), release with `scripts/release.sh`.
- [ ] 3.6 Note: the ctrl agent logged `etcd watch event dropped (channel buffer full)` during the loop. After the fix,
  check whether it still appears in the closing run's logs; if it does, record it as its own milestone.
