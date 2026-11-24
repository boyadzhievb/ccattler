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
- [x] 3c.6 Stop-time Codex check on 0754484, verified by the main session: the placement guard covers only the state
  transaction; `publishInstanceStateToStore` then writes the marker, service, `ip` and `network/allocation/{id}`
  with separate Puts (agent.go ~2248–2290), so a move between the transaction and those Puts lets the source
  overwrite the target's IP (the VIP points at the removed container until the target's next publish, ~1 s).
  Fix: resolve the IP first (AllocateIP / runtime IP), then write state, node, marker, service, `ip` and the
  allocation in the one guarded transaction; `created-at` stays create-only. Red first: a test that moves the
  instance between the source's state write and its IP write (inject via a store wrapper) and expects the target's
  IP to remain. Checks as in 3.1.
  Result: `resolvePublishedContainerIP` runs before any write; `dropUnchangedPuts` keeps the marker, service, `ip`
  and allocation puts whose stored value differs (same reads and key writes as the former idempotent Puts on etcd,
  M266); they join state and node in the guarded transaction (≤ 6 ops + 2 compares); created-at stays create-only;
  `registerEndpointIdentityIfSupported` runs only after a commit. Red: `TestSourcePublishNeverOverwritesTargetIP`
  (store wrapper moves the instance and publishes it on node-2 right after the source's state transaction):
  `observed/instance/web-1/ip = "10.100.1.2" … want node-2's "10.101.1.2"`, same for `network/allocation/web-1`;
  passes after. Graphs (INSTANCE_LIFECYCLE, CALL, OBSERVABILITY line numbers), Flow 35, D4 updated. Checks:
  `gofmt -l` empty, golangci-lint 0 issues, `go test ./...` 20 ok / 2 without tests, `-race` scheduler/agent/
  controllers ok (1.4 s, 29.9 s, 6.7 s), `openspec validate` valid.
  - Decision (main session): allocating the IP before the transaction is accepted — `AllocateIP` already ran on every
    publish, and a lost transaction means stopped or moved, whose cleanup releases the IP on both paths.

## 3d. Review round 3 (2026-11-23, on 0754484; code-reviewer Opus high + Codex: APPROVE, no bugs in the diff)

R2-1..R2-4 and 3c.4 confirmed; new tests red on 7e7c3c0. Remaining notes, verified by the main session; all taken
so the same race class is closed in one place (the stop-time checks keep flagging it):

| # | Finding | Resolution |
|---|---|---|
| R3-S1 | Probe readiness writes (`agent/probes.go` ~230–351) are plain Puts: an in-flight source probe can write `ready` after the move, so the target gets traffic before its own probe passes | **fix** 3d.1 |
| R3-S2 | After a failed guarded publish, callers still write `status_reason`, `last_exit` and the secret-wait status | **fix** 3d.2 |
| R3-T1 | Doc comments of `publishInstanceStateUnlessStopped` / `publishInstanceStateToStore` are 6 lines and omit the placement guard | **fix** 3d.3 |
| R3-T2 | Budget constant comment arithmetic unclear; name says "unplaced" though it covers cooldown deletes | **fix** 3d.3 (comment "3 + 8 = 11"; rename `maxSchedulerBookkeepingChangesPerCycle`, docs updated) |
| R3-T3 | Worst-case test copies the runner cap (60) and does not assert the bookkeeping budget is used up | **fix** 3d.3 |
| R3-T4 | One test sub-case publishes the target with revision 0 (no guard) | **rejected**: the hook runs inside a store wrapper on purpose; documented in the test |

- [x] 3d.1 Probe result writes (readiness, liveness/startup progress) compare the probe pass's placement revision like
  the state publish; red test: in-flight source probe after a move does not set `ready`.
  Result: new `agent/placement_guard.go` (`placementGuard`, `dropUnchangedPuts`, `putGuardedInstanceFact`), shared
  with the state publish; all 7 probe-state Puts, the 4 health Puts and the progress Put go through
  `ProbeScheduler.writeInstanceFact` (unchanged → no write; else a transaction with the guard). The progress Delete
  stays plain. Red: `TestSourceProbeAfterMoveDoesNotSetReadiness` `node-1 wrote readiness = "ready" …`, `liveness =
  "healthy"`, `health = "healthy"` for web-1 after it moved; passes after.
- [x] 3d.2 `publishInstanceStateToStore` returns whether it committed; `publishStatusReason`, `publishInstanceLastExit`
  and the secret-wait status are written only when it did; test.
  Result: gated in `startInstanceAndPublishState` ("running"), `handleInstanceStartError`, `beginCrashLoopWait`,
  `recordSecretDeliveryWait`. Red: `TestStatusWrittenOnlyAfterCommittedPublish` (start failure, crash loop, secret
  wait) wrote `status-reason`, `last_exit` and `secret-wait` after the moved instance's publish failed; passes after.
  Trade-off: a publish lost to a concurrent state change also skips that exit's status (next exit writes it).
- [x] 3d.3 R3-T1..T3: doc comments ≤ 4 lines naming the guard; constant comment and rename (graphs/flows updated);
  worst-case test asserts the bookkeeping budget is fully used and checks the cap against one shared source.
  Result: both doc comments 4 lines naming the placement guard; `maxUnplacedFactChangesPerCycle` →
  `maxSchedulerBookkeepingChangesPerCycle` (code, tests, SCHEDULING_GRAPH, unplaced-facts-cleanup design/tasks;
  history files and this file's earlier records unchanged), comment "3 + 8 = 11"; the runner cap is exported as
  `controllers.MaxTransactionChanges` (runner, tests, 3 graphs, M262 design) and the worst-case test uses it; it
  now asserts 36 unplaced changes + 4 cooldown deletes == the budget (40).
- [x] 3d.4 Checks as in 3.1.
  Result: `gofmt -l` empty; golangci-lint 0 issues; `go test -count=1 ./...` 20 ok, 2 without tests; `-race`
  scheduler/agent/controllers ok (1.5 s, 29.9 s, 6.9 s); `openspec validate` rebalance-respects-fit and
  unplaced-facts-cleanup valid; SCHEDULING_GRAPH checker 0 mismatches.

## 3e. Review round 4 (2026-11-23, on e554525..1a7869d; code-reviewer Opus high + Codex: request changes)

3c.6 and 3d confirmed correct; their tests red on e554525. Remaining findings are all the same class: an agent write
to `observed/instance/{id}/*` outside the placement guard. Fixing them one by one has taken three rounds, so the fix
is structural (main session decision): **every agent write or delete under `observed/instance/{id}/` goes through
the guarded helper, and a test enforces it.**

| # | Finding | Resolution |
|---|---|---|
| B1 | Pull-failure path writes `pull-status` and status reason after a lost publish (agent.go ~879-895); never cleared on the target | **fix** 3e.1 |
| B2 | Start path writes host ports, `started_at`, `image`, `config-hash`, deletes readiness, and `reconcileDesiredInstance` writes "starting"/"waiting for volumes", all unguarded (agent.go ~779-1013): can reset the target's readiness and min_healthy_time and cause one needless config restart | **fix** 3e.1 |
| B3 | `exit_reason` written before the guarded crash-loop publish (crash_loop_backoff.go ~126/138) | **fix** 3e.1 |
| B4 | Probe-progress delete unguarded (probes.go ~644) (Codex) | **fix** 3e.1 (guarded delete) |
| Sg1 | Status reason, `last_exit`, secret-wait written after the commit, not in it (Codex) | **fix** 3e.1 (into the publish's `instanceFacts` where they belong to the same publish) |
| S1 | `types.WriteNetworkAllocation` has no production caller (dead code) | **fix** 3e.2 |
| S2 | `design/plans/reconciliation-consistency-contract.md` still names `maxTransactionChanges` | **fix** 3e.2 |
| Sg2 | Probe-after-move test has no positive control (a current revision still writes ready) | **fix** 3e.2 |

- [x] 3e.1 One guarded writer for the agent's per-instance facts: `putGuardedInstanceFact` / a new guarded delete
  (and the publish transaction's `instanceFacts` for facts of the same publish) are the only ways the agent writes
  or deletes `observed/instance/{id}/*`, except the `stopped` write and cleanup of an instance this node still owns
  (documented exceptions). Convert every call site found by the reviewer and any others. Red first: a subcase per
  finding (B1 pull failure, B2 start path readiness/started_at/config-hash, B3 exit reason, B4 progress delete)
  showing the source writes nothing after a move.
  Result: `writeGuardedInstanceFacts` (placement_guard.go) is the one writer (puts and deletes, no-op changes dropped
  so unchanged values cause no writes); `publishInstanceStateToStore(…, sameCycleFacts...)` carries the facts of the
  same publish (pull status + reason, start facts: host ports, started_at, readiness reset, image, config hash,
  "running"; failed + image + reason; crash loop: reason, last_exit, exit reason; secret-wait + reason; running path:
  host ports + exit-reason delete). Converted the rest: restarts, init step state/reason/progress, status reasons,
  pull-status/secret-wait clears, probe-progress delete, telemetry (`currentPlacementInfo` first); cleanup deletes
  (pull status, secret wait, network allocation) use `deleteFactsUnlessPlacedElsewhere`; created-at is create-only
  with the guard. Removed: `publishInstanceStartedAt`, `resetInstanceReadiness`, `publishInstanceImage`,
  `publishInstanceConfigHash`, `publishInstanceLastExit`, `publishExitReason(FromStatus)`, `deleteExitReason`,
  `republishHostPortIfNeeded`, `publishInstanceHostPorts` (now `hostPortChanges`). Red
  (`TestSourceWritesNothingAfterMove`): B1 status-reason and pull-status written; B2 start path started_at, readiness
  deleted, config-hash, image; B2 reconcileDesiredInstance "starting", started_at, readiness, config-hash, image; B3
  exit-reason; B4 probe-progress deleted. All pass after. Behaviour change: "controller stop after move" now keeps
  pull status and allocation (placed on another node); its test expectation records it.
- [x] 3e.2 Enforcement test (AST or source scan over `agent/*.go`, not tests): fails on a plain `store.Put`,
  `store.Delete` or unguarded transaction whose key comes from `types.KeyObservedInstance*` outside the guarded
  helpers and the listed exceptions. S1: delete `WriteNetworkAllocation` (or replace with an op builder used by the
  agent) and update its tests; S2 doc name; Sg2 positive control.
  Result: `TestAgentInstanceWritesAreGuarded` (go/ast over `agent/*.go`): store Put/Delete/PutWithLease of a
  `types.KeyObservedInstance*` key (direct or via a variable) and any Transaction in a function building such keys,
  outside `guardedInstanceWriters` (6 entries, each with its reason; a stale entry fails too). Red before 3e.1: 28
  violations (agent.go, crash_loop_backoff.go, dataplane.go, init.go, probes.go, secret_delivery.go, telemetry.go);
  0 after. `TestInstanceWriteScanDetectsViolations` is the scan's positive control (3 of 3 found). S1:
  `WriteNetworkAllocation` and `DeleteNetworkAllocation` (now unused too) deleted with their codec test. S2:
  reconciliation-consistency-contract.md names `controllers.MaxTransactionChanges`. Sg2: the probe test first
  checks node-1 writes ready/healthy/health with its placement current.
- [x] 3e.3 Docs: Flow 35 and D4 state the rule ("all per-instance observed writes are guarded by placement") and
  its exceptions; graphs updated.
  Result: Flow 35 rule bullet, D4 rule bullet; CALL_GRAPH (rule, writes table, trees, reverse index),
  INSTANCE_LIFECYCLE_GRAPH (start, running, crash loop, cleanup trees, reverse index, line numbers), DATA_PLANE_GRAPH
  and EXTENSIBILITY_GRAPH (hostPortChanges), OBSERVABILITY_GRAPH line numbers.
- [x] 3e.4 Checks as in 3.1.
  Result: `gofmt -l` empty; golangci-lint 0 issues; `go test -count=1 ./...` 20 ok, 2 without tests; `-race`
  scheduler/agent/controllers ok (1.4 s, 29.9 s, 6.6 s); `openspec validate` valid. `executeReconciliationCycle`
  was 114 lines before this group (over 80, pre-existing) and is 111 now (`publishRunningInstance` extracted).
- Run 45254885232 (first closing full run, dispatched on 1a7869d before this round) tests code that 3e changes: it
  can't count towards the two closing runs.

## 3f. Review round 5 (2026-11-23, on 44b6998..b430f2a; code-reviewer Opus high: request changes, minor)

The guarded writer is correct and every current per-instance write goes through it or a listed exception; new tests
red on 44b6998. Verified by the main session:

| # | Finding | Resolution |
|---|---|---|
| B1 | Host ports inside the start/running publish transaction: N+11 ops + 2 compares; ≥ ~116 ports exceed the limit (reproduced: 121 fail), so the instance is never published running | **fix** 3f.1 |
| B2 | One-time start facts (started_at, readiness reset, image, config-hash) are written only if the start publish commits; a store error loses them for good (a missing config-hash hides config changes for the container's life) | **fix** 3f.2 |
| B3 | `network/allocation/{id}` orphaned after a controller stop on a moved instance (GC does not delete it); simulator only, no reader | **fix** 3f.3 (GC deletes it with the instance tree) |
| T1 | Enforcement test bypassable (op builders in a Transaction, `op.Key` Puts, `var` keys, helpers, wrappers, concatenation, function values) — 8 patterns reported 0 violations | **fix** 3f.4 (closed-world scan) |
| T2 | `TestSourceWritesNothingAfterMove` checks only some keys; no subcases for restarts, init, telemetry, running path | **fix** 3f.5 |
| S1 | Init-step state op built inline 4 times (agent/init.go) | **fix** 3f.6 |
| P1 | `writeInstanceFacts` only forwards and its bool is unused; two forwarding levels for probes | **fix** 3f.6 (fold or use the result) |
| P2 | `publishRemovedInstanceState` (`stopped`) has no compare; several seconds between the move check and the write (pre_stop + grace) | **fix** 3f.6 (compare the `placement/instance/{id}` revision the move check read) |
| P3 | `dropNoOpChanges` does one Get per op; telemetry reads more | **rejected for now**: fewer writes than before; optimize only with a benchmark (CLAUDE.md), noted for M272 |
| D1 | INSTANCE_LIFECYCLE_GRAPH:835 callers list stale; flows overclaim the enforcement test | **fix** 3f.7 |
| Codex | "Guard failure after IP allocation leaks network allocation" | **rejected**: not new (allocation already preceded the transaction), simulator-only provider keyed by instance ID, released by teardown, no reader |

  Red runs below: the new tests copied into a detached worktree of 92feee5 (code without the 3f fixes),
  `go test -count=1 ./agent/ ./api/ ./controllers/ -run …`; green: the same tests in the working tree.
- [x] 3f.1 B1: host ports written by their own guarded call(s), chunked under the transaction limit, not inside the
  state transaction; red test with 130 exposed ports → instance published running with all ports stored.
  Result: `instanceFactWriter.writeGuarded` splits into transactions of ≤ `maxInstanceFactChangesPerTransaction`
  (100) changes + the guard; `startInstanceAndPublishState` and `publishRunningInstance` write
  `hostPortChangesIfSupported` through it before the state publish. Red
  (`TestStartPublishesInstanceWithManyHostPorts`, 130 ports): `transaction too large: 138 operations exceeds limit
  of 128`, `state = "pending" with 130 host ports, want running`, `host port of container port 9000 not stored`;
  passes after.
- [x] 3f.2 B2: start facts that did not commit are kept in memory per instance and carried in the next
  `publishRunningInstance` until they commit (dropped when the instance leaves this node); red test with a store error
  on the start publish → next cycle stores config-hash, image, started_at and clears readiness.
  Result: `Agent.uncommittedStartFacts[id]` set when the start publish does not commit, carried (cloned) in the next
  `publishRunningInstance`, deleted on commit and in `teardownStaleInstance`. Red
  (`TestStartFactsCarriedAfterStoreError`, store wrapper fails the start publish once with `etcd: request timed
  out`): config-hash `""` want `config-hash-1`, image `""` want `nginx:1.27`, readiness `"ready"` want `""`,
  `started_at not stored after the next cycle`; passes after.
- [x] 3f.3 B3: instance GC (and `api/handler_gc.go`) delete `network/allocation/{id}` with the instance tree; test.
  Result: `buildInstanceDeletionChanges` and `collectInstanceFactKeys` add `KeyNetworkAllocation(id)`; the API GC
  ignores `ErrKeyNotFound` on its per-key Deletes (the key is usually absent). Red:
  `TestGCDeletesNetworkAllocationWithInstance` `instance GC does not delete network/allocation/old-abc with the
  instance tree`; `TestGCInstanceKeysIncludeNetworkAllocation` `collectInstanceFactKeys [placement/instance/old-abc
  placement/node/node02/old-abc] lacks network/allocation/old-abc`; both pass after.
- [x] 3f.4 T1: closed-world scan: every `Transaction` call in agent production code is on an allow-list (instance
  writers + the listed non-instance transactions, each with a reason), and every Put/Delete/PutWithLease key is a
  direct `types.Key*` call that is not `KeyObservedInstance*`; anything else fails. Control test feeds the 8 bypass
  patterns from the review and expects each to fail. Fix the flows wording.
  Result: `TestAgentInstanceWritesAreGuarded` now checks `transactionWriters` (9 entries with reasons, stale entries
  fail), direct non-instance `types.Key*` keys for Put/Delete/PutWithLease, store methods used as values and
  `types.Write*/Delete*` other than `WriteObservedVolume`; package-level code is scanned too. Two volume-attachment
  key variables in agent.go inlined to satisfy it. Control `TestInstanceWriteScanDetectsBypasses` (op builders in a
  Transaction, `op.Key` Put, `var` key, local key helper, Put wrapper with key param, string concatenation,
  package-level func literal, method value): red against the 92feee5 scan, 8 of 8 `bypass … not detected`; green
  now. Flow 35 rule and D4 name what the scan checks and what it cannot see (other packages, reflection).
- [x] 3f.5 T2: compare `Scan("observed/instance/web-1/")` plus `network/allocation/web-1` before/after each path;
  add subcases for restart count, init step and progress, telemetry, running path.
  Result: `snapshotInstanceFacts` compares `observed/instance/web-1/`, `observed/telemetry/instance/web-1/` (added:
  telemetry keys live there, without it the telemetry subcase checked nothing), the marker and
  `network/allocation/web-1`, before/after, both directions; 4 new subcases. These paths were already guarded in 3e,
  so the red is a mutation on 92feee5: `placementGuard` returning nil, the guard dropped in
  `writeGuardedInstanceFacts` and the telemetry placement check bypassed → all 10 subcases fail (restart count
  `wrote …/restarts = "1"`, init `wrote …/init/step/0/state = "succeeded"`, telemetry `wrote
  observed/telemetry/instance/web-1/cpu = "0"`, running path `changed …/state from "pending" to "running"`); the
  old key-list test, under the guard drop alone, failed 2 of its 6 subcases. All 10 pass in the working tree.
- [x] 3f.6 S1 init-step op helper; P1 fold the forwarding layer or make its result used; P2 guard on the
  `placement/instance/{id}` revision.
  Result: S1 `initStepChanges` (4 call sites in init.go). P1 `writeInstanceFacts`, `putGuardedInstanceFact`,
  `deleteGuardedInstanceFact` removed; `instanceFactWriter` (held by Agent, ProbeScheduler, NodeReporter) with
  `writeGuarded` (no return value: no caller used it), `deleteUnlessPlacedElsewhere`, `currentPlacement`; probes
  keep one level (`writeInstanceFact`). P2 `isInstanceMovedToAnotherNode` returns the placement revision it read
  and `publishRemovedInstanceState` writes `stopped` in a transaction comparing it. Red
  (`TestStoppedNotWrittenWhenPlacementChangesDuringStop`, runtime moves the placement during the stop): `node-1
  wrote stopped for web-1, which was placed on node-2 during its stop`; passes after.
- [x] 3f.7 D1 docs; checks as in 3.1.
  Result: INSTANCE_LIFECYCLE_GRAPH reverse index (`instanceFactWriter.writeGuarded`, `publishInstanceStateToStore`
  callers, `initStepChanges`), start/running/cleanup trees, line numbers re-checked against agent.go; CALL_GRAPH,
  CONTROLLER_DATA_FLOW_GRAPH (GC writes), OBSERVABILITY_GRAPH line numbers; Flow 35 rule. Checks (2026-11-24):
  `gofmt -l .` empty; `golangci-lint run ./...` 0 issues; `go test -count=1 ./...` 20 ok, 2 without tests;
  `go test -count=1 -race ./agent/ ./controllers/ ./api/ ./scheduler/` ok (29.7 s, 6.8 s, 10.6 s, 1.4 s);
  `openspec validate rebalance-respects-fit` valid. `executeReconciliationCycle` still 111 lines (known, M259).

## 3g. Review round 6 (2026-11-24, on 92feee5..5092aec; code-reviewer Opus high + Codex: request changes)

B2, B3 and T2 are fixed and 130 ports publish. The new tests are red on 92feee5: the scanner control was rerun with
the 92feee5 scanner, 8 of 8 not detected; the 3f.5 mutation red was judged convincing. Every finding below was
reproduced by the main session on 5092aec with the reviewer's probe tests (`TestReview6*`, worktree
`scratchpad/review6-probe-5092aec`):

| # | Finding | Resolution |
|---|---|---|
| R6-1 | 3f B1 broke atomicity: host ports commit apart from state/node and `writeGuarded` returns nothing, so the state publish goes on after a failed or partial port write (`running` with 100 of 130 ports; `running` with stale 41000; a move target's port 30000 next to the source node when the state publish fails) | **fix** 3g.1 |
| R6-2 | The closed-world scan is still bypassable (allocation key counted as non-instance, aliased or dot import of `types`, an allow-listed name on another receiver, a Transaction inside an allow-listed function, a local named `types`); second round with scan bypasses | **fix** 3g.2 |
| R6-3 | `stopped` withheld on a placement change during the stop (correct), but no `pending` either: the stopped container stays `running`+`ready` until the target publishes | **fix** 3g.3 |
| R6-4 | `uncommittedStartFacts` kept for the agent's lifetime when the container is gone before teardown runs | **fix** 3g.4 |
| R6-5 | 3f.5 subcases use a runtime without host ports; no test for dropping B2 facts when the instance leaves | **fix** 3g.5 |
| — | Readiness delete carried by B2 can remove a fresh `ready` until the next probe | **accepted**: brief, rewritten by the next successful probe |
| — | `ProbeScheduler`/`NodeReporter` hold `factStore`/`nodeID` twice (also in `instanceFacts`) | **fix** in 3g.1 if touched, else rejected as style only |

Structural change for this round (openspec/config.yaml rework limit): the guarded-write class has come back each
round since round 2, so the fixes in this round come with a test that enforces the rule on real store operations,
not on source syntax (3g.0).

- [x] 3g.0 Tests first: an agent-test store decorator that (a) fails the Nth transaction or put on demand and
  (b) records every committed write. A sweep test runs the start path and the running path (and a move target) once
  per write index N, failing write N each time. After each run it asserts the rule: when `observed/instance/{id}/state`
  is `running`, `node` names this node and every host port stored equals the runtime's mapping; a failed publish
  leaves the facts as they were before it. A second check in the same decorator fails the test for any committed
  write of a key under `observed/instance/{id}/` or `network/allocation/{id}` whose transaction has no compare on
  `placement/node/{self}/{id}`, apart from the D4 exceptions, which are listed with reasons. Wire it into the move-gap
  and write-guard tests. Record the sweep and the audit failing on 5092aec (R6-1 cases, and R6-2 via a test-only
  unguarded write), before 3g.1.
  Result: `auditedInstanceStore` (agent/instance_write_audit_test.go) fails write number N (Put, Delete,
  PutWithLease or Transaction, 1-based) with a store error and records every committed write of an
  `observed/instance/{id}[/…]`, telemetry or `network/allocation/{id}` key; at test end it fails for any whose write
  does not compare `placement/node/{self}/{id}`, apart from 3 D4 exceptions matched on the change and its compares
  (stopped on `placement/instance/{id}`, cleanup deletes on `placement/instance/{id}`, pending/readiness delete on
  observed node + state). Sweep `TestPublishRuleHoldsWhenAnyWriteFails`: start path, running path (stale port
  41000) and move target (source's running/node-1/41000), each with 2 and 130 host ports, once without a failure and
  once per write. Audit wired into newMoveGapCluster (per agent), TestSourceWritesNothingAfterMove,
  TestTargetStartupFailureAfterMoveIsNotRunning, runSourceProbePass, TestStatusWrittenOnlyAfterCommittedPublish and
  the round-5 tests (`setupAuditedTestAgent`); control `TestInstanceWriteAuditDetectsUnguardedWrite` (2 of 2 found).
  Red on 5092aec (worktree `scratchpad/rework3g-red`, `go test -count=1 ./agent/ -run '…'`): the sweep fails in all 6
  scenarios, e.g. `running on node-1 with 2 of 2 host ports wrong, first 9000="41000" (want 30000)` (port write
  failed, publish went on), `state publish did not commit, yet 2 facts changed, first …/hostport/9000 "41000" ->
  "30000"` (move target), `running on node-1 with 30 of 130 host ports wrong, first 9100=""` (second chunk failed).
  The audit failed `TestRedAuditCatchesTestOnlyUnguardedWrite` (scratch only: an unguarded Put through the agent's
  store) and found a real test-only unguarded write in `TestSourceAgentMarksMovedInstancePending` ("target
  publishes between …": the target published with an unscanned info, so no placement compare): `audit (node-2):
  write 1 committed put observed/instance/web-1/state without comparing placement/node/node-2/web-1`. Fixed in the
  test (the target's info now comes from its scan).
- [x] 3g.1 R6-1: `writeGuarded` reports whether every chunk committed. Host ports go inside the state publish
  transaction when they fit under the limit; only the overflow is written first, in guarded chunks. When the
  overflow did not all commit, the state publish is skipped and the start facts stay in `uncommittedStartFacts`.
  3g.0 is green.
  Result: `writeGuarded` returns true when every chunk committed. `publishInstanceStateToStore` takes the host-port
  changes; `writeHostPortOverflow` drops no-ops, keeps those that fit beside the publish (100 − 2 state/node − the
  publish's other changes) for the publish transaction and writes the rest first through `writeGuarded`; false →
  no publish (start path keeps `uncommittedStartFacts`). Start and running paths pass
  `hostPortChangesIfSupported`, the other 7 callers nil. Green: `TestPublishRuleHoldsWhenAnyWriteFails` (6 scenarios,
  2 or 3 writes each, all pass), the review's probes `TestReview6*` now log 130 ports `state="pending"` (not
  published), stale 41000 with `state="pending"`, move target `running node-1 41000` (unchanged). Residual of this
  design (recorded under D4, 2026-11-24): with more ports than fit, the overflow can commit and the publish then
  fail, so a move target's overflow ports sit next to the source's running/node until the next publish; the sweep
  allows exactly that (overflow port = the runtime's own mapping) only in the 130-port scenarios.
  `ProbeScheduler`/`NodeReporter` duplicate fields: not touched, rejected as style only.
- [x] 3g.2 R6-2: the scan keys allow-listed writers by receiver type plus name, resolves `types` through the file's
  imports, flags dot imports, and treats `KeyNetworkAllocation` as an instance key. "A Transaction added inside an
  allow-listed function" is recorded in Flow 35 as a limit the 3g.0 audit covers. The control test gets the 6 new
  patterns, each failing.
  Result: `transactionWriters` keyed `Receiver.name` (functionKey) with a reason and a Transaction count (another
  Transaction in an allow-listed function fails); `types` resolved per file through its imports (alias honoured, dot
  import fails), any declaration of that name (params, results, receivers, :=, var, range, type, func) fails;
  `instanceKeyFunctionPrefixes` = `KeyObservedInstance`, `KeyNetworkAllocation`. Flow 35 and D4 state that the scan
  cannot see what an allow-listed Transaction's ops write and that the 3g.0 audit covers it. Control
  `TestInstanceWriteScanDetectsBypasses` gets `reviewRound6BypassSources` (6 patterns). Red: the 6 against the
  5092aec scanner (`TestRedRound6BypassesAgainstOldScan` in the red worktree): 6 of 6 `bypass … not detected`.
  Green: 14 of 14 detected; `TestAgentInstanceWritesAreGuarded` passes on production code unchanged.
- [x] 3g.3 R6-3: when the `stopped` swap fails, check the move again and, if the instance moved, mark it pending
  (`markMovedInstancePending`). Red test: placement moves to node-2 during the stop, then state `pending`, no readiness.
  Result: `publishRemovedInstanceState` on a lost `stopped` swap calls `isInstanceMovedToAnotherNode` again and, when
  moved, `markMovedInstancePending`. Red (`TestInstanceMovedDuringStopIsMarkedPending`, on 5092aec and before the
  fix): `state="running" readiness="ready", want pending without readiness`; passes after.
- [x] 3g.4 R6-4: sweep `uncommittedStartFacts` against the desired set each cycle (like
  `cleanupStaleCrashLoopBackoffs`). Red test: the instance leaves while its container is gone, so the entry is
  dropped.
  Result: `cleanupStaleUncommittedStartFacts(desiredByID)` (maps.DeleteFunc) after `cleanupStaleCrashLoopBackoffs`.
  Red (`TestUncommittedStartFactsDroppedWhenInstanceLeaves/container_gone_true`, on 5092aec and before the fix):
  `uncommittedStartFacts still holds web-1 after it left node-1`; passes after. The `container_gone_false` subcase
  (teardown drops it, R6-5's missing B2-drop test) passes on both.
- [x] 3g.5 R6-5: the 3f.5 snapshot subcases run on a runtime with host ports.
  Result: `TestSourceWritesNothingAfterMove` runs on `manyHostPortsRuntime` (2 ports) with the audit, and node-2's
  facts include `hostport/9000 = 31000`; the unused runtime parameter of runPath removed. No red was asked: the
  paths were already guarded. Mutation check (placementGuard always nil, reverted): `B2 start path` and `running
  path` fail with `node-1 changed observed/instance/web-1/hostport/9000 from "31000" to "30000"` plus the audit's
  `write 1 committed put …/hostport/9000 without comparing placement/node/node-1/web-1`.
- [x] 3g.6 Docs (graphs, Flow 35, D4 note if the rule wording changes, with date), checks as in 3.1.
  Result: Flow 35 rule bullet and chain (host ports in the publish, overflow residual, pending after a move during
  the stop, scan plus audit), D4 bullet and dated note (2026-11-24, lead-developer); CALL_GRAPH (cycle, start and
  cleanup trees, rule, writes table, reverse index), INSTANCE_LIFECYCLE_GRAPH (start, running, crash-loop end,
  cleanup trees with line numbers, reverse index), OBSERVABILITY_GRAPH line numbers, DATA_PLANE_GRAPH hostport
  key. Checks (2026-11-24): `gofmt -l .` empty; `golangci-lint run ./...` 0 issues; `go test -count=1 ./...` 20
  ok, 2 without tests; `go test -count=1 -race ./agent/ ./controllers/ ./api/ ./scheduler/` ok (30.4 s, 6.6 s,
  10.2 s, 1.4 s); `openspec validate rebalance-respects-fit` valid. `executeReconciliationCycle` 112 lines (one
  call added; over 80 since before this change, M259).

## 3h. Review round 7 (2026-11-24, on 5092aec..6dbc3a4; code-reviewer Opus high + Codex: approve, one decision)

R6-1 to R6-5 are fixed. Every new test is red on 5092aec and green on HEAD, and stable at `-count=30 -race`. The
main session reproduced R7-1 with the reviewer's probes (`TestReview7ResidualThreshold`,
`TestReview7ResidualViaStateReadError`, worktree `scratchpad/review7-probes-6dbc3a4`): with the publish failing, 92
ports store 0 target ports next to node-1, 98 store 6, and 130 store 38.

| # | Finding | Resolution |
|---|---|---|
| R7-1 | Overflow residual: with 93 or more ports (100 − 2 − the publish's other changes), the overflow commits and then the publish fails, or the state read fails, so the target's overflow ports sit next to the source's `running`/node. The sweep exempts exactly this case (`isOverflowHostPortFact`) | **fix** 3h.1. User decision 2026-11-24: "make instance pending on 90 ports instances". Implemented as `starting`, not `pending`: both are non-serving, but scale-down removes `pending` instances first (`controllers/instance.go:544`) |
| R7-2 | Only some agent tests use the write audit (9 of 104 constructions) | **follow-up**: wrapping the shared test constructors can cascade; recorded for M269/M270, not this change |
| R7-3 | Sweep limits: one fault per run; no lost compare, read error or commit-then-error; stop/crash-loop/pull paths not swept | **partly fixed** 3h.1 (read-error and lost-compare cases for the overflow path); the rest recorded as known limits in Flow 35 |
| R7-4 | The audit's cleanup exception allows any per-instance delete under the `placement/instance/{id}` compare | **fix** 3h.2 (only `pull-status`, `network/allocation/{id}`, `secret-wait`) |
| R7-5 | The R6-3 re-check drops a read error silently (`agent/agent.go:1128`) | **fix** 3h.2 (Warn log) |
| R7-6 | The scan misses a double import of `types` | **rejected**: gocritic `dupImport` (enabled) rejects it |
| — | Suggestions: `writeHostPortOverflow` name, double no-op filter, struct key for `transactionWriters`, `pos` abbreviation | **fix** the name and `pos` in 3h.2; the rest rejected (one extra Get only on overflow; the string key is never split) |
| Codex | "Cleanup exception accepts another instance's compare" | **rejected**: the reviewer's probe `TestReview7AuditCrossInstanceCleanup` reports it as a violation |

- [x] 3h.1 R7-1: when the overflow is not empty after the no-op filter, first publish `starting` with node = self
  through the guarded publish (state-revision compare, placement guard, stopped check), then write the overflow
  chunks, then do the final publish. Replace `isOverflowHostPortFact` with the invariant "`running` on node N ⇒
  every host port stored equals N's mapping, or is unchanged when N is not this node". Extend the sweep with a
  failed state read and a lost compare on the overflow path. Red first on 6dbc3a4 (130 ports, publish fails ⇒
  source's `running` with target ports), then green.
  Result: `inlineHostPortsAfterWritingOverflow` (was `writeHostPortOverflow`): with an overflow after the no-op
  filter, `publishInstanceStateUnlessStopped(starting)` (node = self, state-revision compare, placement guard,
  stopped check), then the guarded overflow chunks, then the final publish; either failing → false, no publish.
  `isOverflowHostPortFact` removed; `checkPublishRule` asserts running on this node ⇒ the runtime's mapping and
  running on another node ⇒ host ports unchanged (`checkHostPortsUnchanged`). The decorator gained `injectedFault`
  (failed write, lost compare on transaction N, failed Nth state read); the sweep runs every kind for every
  number. Red on 6dbc3a4 (worktree `scratchpad/rework3h-red`): 9 cases fail, all 130-port, e.g. move target `fail
  write 2`, `lose compare of write 2`, `fail state read 2`: `running on node-1, yet 39 of its host ports changed,
  first …/hostport/9091 "" -> "30091"`; start/running path: `state publish did not commit, yet 38 facts changed`.
  Green: `TestPublishRuleHoldsWhenAnyWriteFails` 58 subtests pass. Probes `TestReview7Residual*` now: publish
  failing ⇒ 0 target ports at 2..130 ports; state read failing after the overflow ⇒ `starting` on node-2 (not
  serving) with 38 ports.
- [x] 3h.2 R7-4, R7-5 and naming: narrow the cleanup exception (a control case: a delete of `state` under the
  cleanup compare fails the audit); Warn log on the re-check read error; rename `writeHostPortOverflow` so the
  name says it returns the inline ports; `pos` → `tokenPosition`.
  Result: cleanup exception narrowed to deletes of `pull-status`, `network/allocation/{id}`, `secret-wait`; control
  `TestInstanceWriteAuditDetectsUnguardedWrite` adds an allowed pull-status cleanup and a `state` delete under the
  cleanup compare, want 3 violations. Red with the 6dbc3a4 exception (red worktree): `audit reported 2 violations,
  want 3`; green after. `publishRemovedInstanceState` logs Warn "cannot read where the removed instance is placed,
  leaving its state" on the re-check read error. Renamed to `inlineHostPortsAfterWritingOverflow`; `pos` →
  `tokenPosition` in instance_write_guard_test.go.
- [x] 3h.3 Docs: Flow 35 and D4 (dated note: the user decision and `starting`), the known sweep limits (R7-3),
  graphs; checks as in 3.1.
  Result: Flow 35 rule bullet (starting before the overflow, the invariant, sweep fault kinds, R7-3 known limits:
  one fault per run, no commit-then-error, stop/crash-loop/pull not swept, audit in some tests only), D4 bullet and
  dated note (2026-11-24: user decision, `starting` because scale-down removes `pending` first); CALL_GRAPH and
  INSTANCE_LIFECYCLE_GRAPH (rename, starting step, Warn, line numbers), OBSERVABILITY_GRAPH line numbers. Checks
  (2026-11-24): `gofmt -l .` empty; `golangci-lint run ./...` 0 issues; `go test -count=1 ./...` 20 ok, 2 without
  tests; `go test -count=1 -race ./agent/ ./controllers/ ./api/ ./scheduler/` ok (29.9 s, 6.7 s, 10.9 s, 1.4 s);
  `openspec validate rebalance-respects-fit` valid.
- [ ] 3.4 Closing: two consecutive full Deploy Testbed runs (job summary says `full run`), both passing, with the
  whoami-churn check at zero unexplained instance IDs. Record both run IDs and the whoami ID counts.
- [ ] 3.5 Update the milestones.md M267 row to the final scope, link this change from Phase 267, archive (syncing
  `instance-rebalancing`), release with `scripts/release.sh`.
- [ ] 3.6 Note: the ctrl agent logged `etcd watch event dropped (channel buffer full)` during the loop. After the fix,
  check whether it still appears in the closing run's logs; if it does, record it as its own milestone.
