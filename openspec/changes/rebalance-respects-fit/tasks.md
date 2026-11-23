# Tasks

Milestone M267. Design: [design.md](design.md). Spec delta: [specs/instance-rebalancing/spec.md](specs/instance-rebalancing/spec.md).

## 1. Tests first (record each failing before section 2)

- [ ] 1.1 Scheduler: `TestRebalanceSkipsTargetWithoutRoom`, `TestRebalanceSkipsTargetUnderPressure`,
  `TestRebalanceCountsRoomUsedByEarlierMoves`, `TestRebalanceMovesToTargetWithRoom` (the last should pass already;
  record it). Record the failures.
- [ ] 1.2 Agent: `TestAgentDoesNotMarkMovedInstanceStopped` (moved placement → container removed, no `stopped`
  write; sub-case `controller stop` → `stopped` written). Record the failure.

## 2. Implementation

- [ ] 2.1 D1: shared `candidateFitsRequests` helper used by `node_heap.go` placement and `selectRebalanceTarget`;
  scheduler `Watch()` gains `derived/node/`; skip candidates with `derived/node/{id}/pressure`; `executeRebalanceMoves`
  updates the target's and source's remaining capacity after each move.
- [ ] 2.2 D2: `cleanupUndesiredInstance` and `cleanupStoppedStaleInstance` skip the `stopped` write when
  `placement/instance/{id}` names another node and there is no controller stop; on a read error they skip it and log.
- [ ] 2.3 D3: `cluster_test.sh` records whoami instance IDs at each status poll and fails above the bound explained
  by the test's steps (named constant, comment). Run the script's syntax check (`bash -n`, shellcheck) and run the new
  check by hand against the deploy cluster before committing (feedback: test scripts remotely).
- [ ] 2.4 Docs: SCHEDULING_GRAPH §4 and Watch list, INSTANCE_LIFECYCLE_GRAPH / CALL_GRAPH (agent cleanup), and a
  "Rebalance move" entry in runtime-invariant-flows (scheduler moves → source removes without `stopped` → target
  starts).

## 3. Checks

- [ ] 3.1 gofmt, golangci-lint, `go test -count=1 ./...`, `-race` on `scheduler` and `agent`.
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
