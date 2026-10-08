I did another full review of the current `master`, not just the M73 diff. The current HEAD is **`b471df6f3f02b665ca8248be74481e6c07ab2566`**, M73. [Current HEAD / M73 commit](https://github.com/boyadzhievb/ccattler/commit/b471df6f3f02b665ca8248be74481e6c07ab2566?utm_source=chatgpt.com)

The M72/M73 work fixed several serious issues, but I found **two high-severity correctness bugs that I would fix before sign-off**, plus several important scale/concurrency problems.

## Verdict

**Status: improved, but not production-signoff ready.**

The most important remaining issue is surprisingly subtle: the new derived `controller_stopped` marker interacts badly with stateful ordinal reuse.

### 1. P0 — `controller_stopped` permanently poisons reusable stateful instance IDs

M73 made failure replacement use:

```text
derived/instance/<id>/controller_stopped = true
```

and `effectiveInstanceState()` gives this marker priority over the observed state.

That is correct for the lifetime of the failed/replaced instance.

The problem is that **stateful instance IDs are reusable ordinals**.

Example:

```text
postgres-2 running
        ↓
failure
        ↓
controller_stopped(postgres-2)=true
        ↓
FailureController creates replacement with another ID
```

Later the service scales back up and `InstanceController` sees that ordinal `2` is missing from its **active** instances.

It therefore creates `postgres-2` again.

But the old:

```text
derived/instance/postgres-2/controller_stopped=true
```

is still present.

So:

```text
observed state = pending/running
controller_stopped = true
effective state = stopped
```

The agent skips it, and the InstanceController does not count it as active.

That ordinal can therefore become permanently unusable.

The same lifecycle problem exists for `node_failure` markers when placement disappears before cleanup.

**Fix:** make failure markers generation-specific, or explicitly remove/replace the marker when a stateful ordinal is intentionally recreated. Better still, don't encode permanent lifecycle state directly into a reusable ID.

This is not just theoretical; the current stateful controller explicitly recreates missing ordinals. [Instance controller](https://github.com/boyadzhievb/ccattler/blob/master/controllers/instance.go?utm_source=chatgpt.com)

---

### 2. P1 — M72 reintroduced the atomicity bug via runner capping

M71 correctly changed truncation → rejection.

M72 then changed it back to:

```text
truncate to 60
commit first 60
```

The difference is that this time the individual controllers were given additional caps.

That still isn't enough because the runner knows nothing about **logical operation groups**.

A concrete failure exists in the current `InstanceController`.

One stateful creation generates **4 changes**:

```text
observed/instance/<id>
observed/instance/<id>/ordinal
observed/instance/<id>/service
observed/instance/<id>/state
```

The creation count is 18, so 18 stateful creations can produce:

```text
18 × 4 = 72 changes
```

The controller then caps to 58.

That can leave the final stateful creation with only part of its four writes committed.

You therefore get a transaction containing something like:

```text
instance root
instance ordinal
```

but no service/state, or some other incomplete combination depending on sorting.

The same class of issue exists in:

* FailureController's 4-write replacement group;
* StatefulVolume's per-volume multi-key groups;
* StorageController's multi-key lifecycle operations.

The runner's duplicate-key validation is good, but **change grouping is the missing abstraction**.

The system needs:

```text
ChangeGroup
    ├── change A
    ├── change B
    ├── change C
    └── change D
```

and batching should only cut between complete groups.

etcd transactions are atomic, but modifications of the same key in one transaction are forbidden, so transaction construction needs to respect these invariants explicitly. ([etcd][1]) The repo's conservative 60-change ceiling is also reasonable relative to etcd's default `--max-txn-ops=128`. ([etcd][2])

---

### 3. P1 — M73's guard-scoping does not actually eliminate the main CAS contention

This was the intended headline fix of M73, but the implementation is broader than the design claims.

`appendInputKeyGuards()` now restricts guards to the controller's write domain.

But the write domain still says:

```text
failure:
    observed/instance/
    derived/instance/

instance:
    observed/instance/

rollout:
    observed/instance/
    ...
```

Therefore those controllers still add revision guards over the huge:

```text
observed/instance/
```

space.

That includes agent-owned telemetry such as:

```text
observed/instance/*/cpu
observed/instance/*/memory
```

and other agent-written fields.

So an agent can still change a telemetry key while a controller is trying to commit and invalidate the controller transaction.

The change from “all scanned prefixes” to “write-domain prefixes” reduces contention, but it **does not remove the broad shared domain**.

The clean solution is a separate declaration for:

```text
input guard ownership
```

rather than inferring it from output prefixes.

For example:

```text
failure output:
    derived/instance/
    observed/instance/<new-id>/...

failure input guards:
    derived/instance/
```

That would finally stop unrelated agent writes from invalidating the controller.

---

### 4. P1 — controller watch restart leaks the old watch subscriptions

This is a real resource leak in `Runner`.

When one watch closes:

```text
watch goroutine
    ↓
watchLost
    ↓
runControllerLoop returns
    ↓
runSingleController starts a new runControllerLoop
```

the other watch goroutines from the previous loop are still using the original `ctx`.

`runControllerLoop` doesn't cancel a local watch context when it returns.

So after repeated compaction/closure:

```text
old watches remain registered
new watches are created
old goroutines keep running
```

With `MemoryStore`, this is especially clear because the old watchers remain in `activeWatchers` until their context is cancelled.

The fix is to create a per-loop child context:

```go
watchCtx, cancel := context.WithCancel(ctx)
defer cancel()
```

and use `watchCtx` for every watch and watch-forwarder goroutine.

That way one failed watch tears down the entire generation cleanly before the next is created.

---

### 5. P1 — the public instance read paths now disagree

M73 updated `ListInstances()` to combine observed + derived state.

But `ReadInstance()` still only reads:

```text
observed/instance/<id>/*
```

and directly uses:

```text
state = observed.state
```

So the same instance can simultaneously appear as:

```text
ListInstances() -> stopped
ReadInstance()  -> running
```

when `controller_stopped=true`.

That is an API correctness bug introduced by the new derived-state model.

The same effective-state function needs to be used by both paths.

[types/codec.go](https://github.com/boyadzhievb/ccattler/blob/master/types/codec.go)

---

### 6. P1 — heartbeat is still coupled to potentially very long reconciliation work

The N² placement-watch heartbeat write is correctly gone.

But heartbeat is still performed in the main agent ticker:

```text
heartbeat
reconcile all local instances
telemetry for all local instances
dataplane reconcile
```

A long reconciliation therefore delays the next heartbeat.

At the scale you're testing, `executeReconciliationCycle()` walks every placed workload sequentially, and telemetry also iterates every running workload.

If that work ever exceeds the node failure timeout, the controller can conclude:

```text
agent is alive
but heartbeat is old
→ node unreachable
```

even though the process is healthy.

Heartbeat should have its **own dedicated ticker/goroutine**, independent of reconciliation latency.

---

### 7. P1 — telemetry is still capable of dwarfing controller traffic

The earlier 50+ node contention concern remains important here.

The agent writes per-workload CPU and memory telemetry under `observed/instance/`.

At 5,000 running workloads and a 200ms reporting interval, the theoretical upper bound is roughly:

```text
5,000 workloads × 2 writes / 0.2 sec
≈ 50,000 Put attempts/sec
```

Even if many values are unchanged and the store suppresses some revisions, that is a huge amount of read/write traffic.

Worse, those keys live inside the same `observed/instance/` prefix that several controllers watch and guard.

This means telemetry isn't just store load; it is also **reconciliation trigger and CAS contention load**.

Telemetry should be moved to a dedicated prefix that normal instance-state controllers don't watch or guard.

---

### 8. P1 — several controllers are still unbounded, so the 60-change safety net remains active

Current controllers such as Endpoint, StatefulDNS, StatefulVolume, Storage, Network, and NetworkPolicy don't have equivalent hard per-cycle budgeting.

The runner therefore remains responsible for capping them.

That creates two problems:

1. groups can still be split;
2. a large desired-state diff can take many cycles even when the controller itself could have selected a safe batch.

The most important examples are:

**StatefulVolume:** multiple writes per ordinal volume.

**Storage:** creation/migration/cleanup can produce multiple writes per volume.

**Endpoint:** potentially one write per instance × exposed port.

The correct design is controller-level bounded planning plus group-aware runner batching, not a generic `[:60]`.

---

### 9. P1 — Storage performs external side effects before the transaction commits

This is one of the more serious second-order issues.

`StorageController.Reconcile()` can call:

```text
ResizeVolume()
SnapshotVolume()
```

before the runner's etcd transaction succeeds.

Therefore:

```text
external side effect
       ↓
CAS conflict / transaction cap
       ↓
store state not committed
       ↓
next reconciliation
       ↓
same external side effect again
```

And with runner capping, later operations may execute externally even though their corresponding store changes are not in the committed batch.

For snapshots this can produce multiple snapshots; for other providers it can mean duplicate or non-idempotent operations.

Reconciliation functions should ideally be pure planners. External effects need an explicit operation state machine / intent record so execution is separately tracked.

---

### 10. P1 — EventProjector still isn't resilient to partial watch loss

The main runner and agent got watch restart behavior.

`EventProjector` did not.

It merges multiple watch channels and waits for **all** channels to close before terminating.

Therefore if one etcd watch is compacted:

```text
prefix A watch closes
prefix B/C/D continue
```

the projector continues running, but **never recreates prefix A's watch**.

Semantic events for that prefix can be silently lost indefinitely.

This is especially relevant because `EventProjector` now watches `derived/instance/`, which is part of the new failure-state model.

---

### 11. P1/P2 — node-failure marker cleanup is incomplete

`cleanupStaleNodeFailureMarkers()` only removes a marker when it can find a current placement and that placement is healthy.

If the placement has disappeared:

```text
derived/instance/X/node_failure=true
placement/X deleted
```

the marker survives.

That becomes another stale-ID problem if the same stateful ordinal is reused later.

This should be cleaned when:

```text
placement missing
```

and the instance itself no longer exists, or moved to a new identity generation.

---

### 12. P2 — topological dependency sorting is still not wired into execution

The repository has a fairly elaborate dependency sorter, but `NewRunner()` still starts controllers concurrently.

Also, the output-prefix table still doesn't fully describe the active controller set, including the stateful controllers.

So the topology system currently isn't the authoritative execution model it appears to be.

That's acceptable only if controller ordering is explicitly designed to be irrelevant; otherwise it needs to become operational.

---

### 13. P2 — `enforceWriteDomain()` still silently drops invalid controller output

A controller producing:

```text
wrong-prefix/key
```

doesn't fail reconciliation. The runner logs and drops the change.

That's dangerous for a correctness-oriented control plane because a programming error can become:

```text
controller appears healthy
desired change silently discarded
```

I'd make write-domain violations fatal for built-in controllers, or at least return an error.

---

### 14. P2 — default parser behavior still turns malformed desired data into zero

For example, `collectIntValuesBySuffix()` logs a malformed integer but then stores the zero value.

That's better than silently ignoring the parse error, but zero can itself be destructive:

```text
desired replicas = corrupt
→ parsed as 0
→ controller may scale/delete toward zero
```

For desired-state data, malformed values should generally make the fact invalid rather than producing a valid-looking zero.

---

# What is now solid

The following fixes look good after re-review:

**M70/M71 transaction ceiling:** the repository correctly recognizes the etcd transaction-size constraint; current etcd v3.6 documentation lists `--max-txn-ops=128`. ([etcd][2])

**Duplicate-key detection:** now explicitly checked before transaction construction, which matches etcd's requirement that transaction mutation keys be unique. ([etcd][1])

**Derived failure markers:** architecturally much better than controllers fighting the agent over observed runtime state.

**Failure/node-failure separation:** substantially reduces direct same-key CAS contention.

**Placement-watch heartbeat amplification:** correctly removed.

**Agent failed-instance handling:** good.

**Endpoint drain-readiness watch:** good correction.

**HA runner waiting for shutdown:** materially better than the previous split-brain window, although the timeout case still isn't a true forced stop.

**Watch reconnect in the agent:** correct direction.

**Node heartbeat recovery:** the unreachable→alive recovery behavior is now much more sensible.

---

# CI / validation status

Current HEAD has green GitHub Actions for the normal:

* Test
* E2E Cluster Test
* CodeQL
* Pages

runs. [HEAD Test workflow](https://github.com/boyadzhievb/ccattler/actions/runs/37765429428?utm_source=chatgpt.com) [HEAD E2E workflow](https://github.com/boyadzhievb/ccattler/actions/runs/37765429528?utm_source=chatgpt.com) [HEAD CodeQL workflow](https://github.com/boyadzhievb/ccattler/actions/runs/37765429480?utm_source=chatgpt.com)

However, the normal Test workflow runs:

```text
go test -race -short -count=1 -timeout=5m ./...
```

so the long convergence/load tests are not part of that standard push-time gate.

Also, M72 itself reported a pre-existing loadtest timeout, while M73's commit says the broader chaos tests pass. I couldn't independently execute the repository locally because the environment cannot clone GitHub, so I would treat those long-scale results as reported rather than independently reproduced.

## My recommended next-fix order

**1. Fix stateful marker lifecycle.** This is the most concrete new correctness bug.

**2. Replace generic `[:60]` capping with atomic change groups.** This removes the remaining transaction-integrity problem.

**3. Separate controller output domains from input-guard domains.** This is the change most likely to finally eliminate the original 50+ agent CAS contention.

**4. Make watch generations cancellable.** Prevent leaked watchers after repeated compaction/reconnect.

**5. Decouple heartbeat and telemetry from the main reconciliation loop.**

**6. Move storage/provider side effects behind an intent/execution state machine.**

After those, I would consider the original livelock problem fundamentally addressed rather than merely tuned around.

**Bottom line: M73 is a substantial improvement, but I still would not approve it for production. The stateful-ordinal marker bug and the reintroduced transaction truncation are the two issues I'd block on first.**

[1]: https://etcd.io/docs/v3.6/learning/api/?utm_source=chatgpt.com "etcd API | etcd"
[2]: https://etcd.io/docs/v3.6/op-guide/configuration/?utm_source=chatgpt.com "Configuration options | etcd"
