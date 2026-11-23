# Reconciliation Consistency Contract (draft, M269)

Status: **draft, not implemented.** Written 2026-11-23 from three external reviews and the M262/M265/M267 findings.
It states what a controller may assume when it reconciles and what the runner guarantees when it commits, and
lists, per guarantee, whether today's code meets it.

## Problem

Controllers implement `Reconcile(facts) → []Change`, and the runner (`controllers/runner.go`) scans, calls, guards and
commits. What a controller may rely on has never been written down, and the code meets some guarantees by accident:

- **Snapshot:** each watched prefix is scanned separately (`scanFactsForController`), so one reconcile can combine
  revisions. Seen on 2026-11-23: the scheduler wrote unplaced facts for an instance instance GC had already removed
  atomically (M262 testbed check). Tracked as M265 (and the same item in M180).
- **Read-set validation:** the runner guards a changed key by its scanned revision and adds input-key guards only for
  scanned keys inside the controller's own write domain (`appendInputKeyGuards`); read-only prefixes such as
  `observed/instance/` are excluded on purpose, to avoid contention with agents. So a decision based on agent facts
  can commit after those facts changed.
- **Absence:** a decision based on a key not existing is protected only when the controller also writes that key
  (create-only guard). A key created by another writer under a scanned prefix does not fail the transaction (M180
  item "Absence guards").
- **Budget:** a commit holds at most 60 changes (`controllers.MaxTransactionChanges`); the rest are deferred, sorted by
  (Group, Key). Each controller must cap its own output (runner contract); M262 D4 and M267 showed how a missing cap
  or a missing fit check turns into delayed placements or loops.
- **Unchanged writes:** etcd gives an unchanged put inside a transaction a new revision and a watch event; the memory
  store did not, so unit tests missed write loops (M243 F1, M266).
- **Leadership:** a deposed leader can still commit until fencing exists (M180).
- **Compaction:** the store emits `EventCompacted` and the runner restarts the watch with a full rescan
  (runner.go, watch forwarding); not covered by a real-etcd test.

## How others do it

- **Kubernetes:** controllers read from informer caches, which are eventually consistent per resource type and not
  consistent across types; writes are optimistic per object (`resourceVersion`). Controllers are written to tolerate
  stale reads: every write is re-validated by the API server against the object's current version, and a stale
  decision fails with a conflict and is retried. Cross-object invariants are kept by admission, owner references
  and level-triggered retries, not by multi-object transactions. A no-op update creates no new resourceVersion.
- **Consul:** reads can be `stale`, `default` or `consistent`; writes go through Raft; check-and-set (`cas`) per key.
- **etcd itself:** a range read at a fixed revision (`WithRev`) gives a consistent multi-prefix snapshot as long as
  the revision is not compacted; a transaction can compare any key's mod revision, including `0` for absence, but
  not "no key created under this prefix" directly; prefix absence needs a compare on a sentinel key or a revision
  bound.

## Proposed contract

| # | Guarantee | Runner / store mechanism | Today |
|---|---|---|---|
| C1 | One reconcile sees all watched prefixes at one store revision | first scan's revision, then `WithRev` for the others; compacted → restart the snapshot | **No** (M265) |
| C2 | A commit fails if any key the decision depended on changed since the snapshot | per-controller declared read set: changed keys (always), plus declared critical input prefixes guarded by revision; agents' high-churn prefixes excluded only when the controller declares it tolerates them | **Partial** (write-domain keys only) |
| C3 | A decision based on absence is protected | create-only guard for keys the controller writes; for "nothing exists under prefix P" a per-prefix sentinel revision (M180 item) | **Partial** |
| C4 | No unchanged writes | runner drops puts equal to the scanned value; agents compare before CAS; test store emits events for unchanged transactional puts | **No** (M266 proposed) |
| C5 | Every controller's output fits the budget, deferred work converges | controllers cap themselves (named constants); runner logs and counts deferrals; a test per controller with oversized input | **Partial** (scheduler since M262 D4) |
| C6 | Only the current leader commits | leader key revision compared in every transaction | **No** (M180) |
| C7 | Retries are bounded and fair | exponential back-off on CAS conflict (exists); a starvation metric per controller; a test with two contending controllers | **Partial** |
| C8 | After compaction or watch loss, state converges from a full rescan | `EventCompacted` → rescan (exists); real-etcd test | **Untested** (M270) |

## Recommendation

1. Land C1 (M265) and C4 (M266) first: both are small, and both caused real defects this week.
2. Write C2's declared read sets as part of M269: start with the scheduler, instance, GC and endpoint controllers,
   which act on agent facts; measure the conflict rate on the deploy cluster before guarding high-churn prefixes.
3. C3 and C6 stay in M180; C8 moves into the real-etcd test tier (M270).
4. Every guarantee gets a named test; this document becomes `openspec/specs/reconciliation-consistency/spec.md` when
   M269 is proposed.

## Failure analysis

| Failure | Without the contract | With it |
|---|---|---|
| Two prefixes read across an atomic GC | acts on a state that never existed (seen) | C1: impossible |
| Agent changes state between scan and commit | stale decision commits (e.g. place an instance just stopped) | C2: transaction fails, retried |
| Concurrent creation under a scanned prefix | duplicate creation | C3: sentinel revision fails the commit |
| Deposed leader commits | two leaders write | C6: rejected |
| Etcd compacts during a long outage | partial view until resync | C8: full rescan, tested |
| A controller re-puts unchanged facts | write loop, wakes every watcher | C4: dropped in the runner |
