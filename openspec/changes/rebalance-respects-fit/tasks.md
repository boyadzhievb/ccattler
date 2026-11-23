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
- [ ] 3.4 Closing full Deploy Testbed run (job summary says `full run`), including the new churn check. Record the
  run ID and the whoami instance count.
- [ ] 3.5 Update the milestones.md M267 row to the final scope, link this change from Phase 267, archive (syncing
  `instance-rebalancing`), release with `scripts/release.sh`.
- [ ] 3.6 Note: the ctrl agent logged `etcd watch event dropped (channel buffer full)` during the loop. After the fix,
  check whether it still appears in the closing run's logs; if it does, record it as its own milestone.
