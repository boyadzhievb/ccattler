## Executive conclusion

The repo confirms a real contention problem, but the failure mode is slightly different from the prompt's model:

**There are two independent problems.**

1. **Structural transaction overflow:** the failure path can generate transactions far larger than etcd's 128-operation limit. This is deterministic and exists even with zero agent contention.
2. **Shared-key CAS contention:** controllers and node agents both write `observed/instance/*`, so any agent write to a key guarded by the controller can invalidate the controller transaction. The controller's current exponential backoff with jitter helps statistically, but it cannot make an oversized transaction succeed and cannot eliminate persistent ownership conflicts.

The most important finding is that **raw global revision churn is not itself the cause**. CCattler's CAS compares are per-key `ModRevision`; a heartbeat write to `lease/node/n42` does not invalidate a compare on `observed/instance/i17/state`. The conflict only occurs when an agent actually modifies one of the controller's guarded keys. The repo's `MemoryStore` confirms this directly, and the etcd adapter translates the same semantics to `ModRevision` equality. etcd transactions apply their compare predicates atomically before the success block. ([etcd][1])

---

# 1. What the reconciliation protocol actually does

The path in `controllers/runner.go` is:

`scan → Reconcile() → write-domain validation → build compares + ops → etcd Txn → retry on failed compares`

For each proposed `Change`, `buildReconciliationTransaction()`:

* adds the write operation;
* adds a revision compare for an existing key;
* adds `revision=0` for a newly created key;
* optionally adds input-key guards.

With `SetMaxInputKeyGuards(0)`, **only the input guards disappear**. Change-set guards remain.

That distinction matters: disabling input guards does **not** make controller output writes unconditional.

The retry loop currently has:

* default 5 attempts;
* 50-node benchmark: 10 attempts;
* exponential backoff starting at 10 ms;
* cap 500 ms;
* full-range additive jitter of 0…base delay.

So the retry machinery is no longer the simplistic fixed-delay loop described in the original prompt. M44 explicitly added exponential backoff+jitter and configurable input guards.

### One subtle architectural issue

`scanFactsForController()` does a separate `ScanWithRevision()` for every watched prefix. There is therefore **not one multi-prefix snapshot revision**; prefix scans can observe different points in time.

That is not the primary livelock issue, but it makes retries and reasoning about cross-prefix consistency more complicated.

---

# 2. Write-domain overlap: yes, and it is substantial

The repository's `key-ownership-matrix.md` explicitly documents `observed/instance/` as a shared prefix.

### Node agent writes

The agent writes, among other things:

```text
observed/instance/<id>
observed/instance/<id>/service
observed/instance/<id>/state
observed/instance/<id>/node
observed/instance/<id>/image
observed/instance/<id>/ip
observed/instance/<id>/probe/*
observed/instance/<id>/init/step/*
observed/instance/<id>/cpu
observed/instance/<id>/memory
```

It also writes:

```text
observed/node/*
lease/node/*
```

The important implementation detail is that these are **ordinary unconditional `Put()` calls**, not transactions.

In `agent/agent.go`, every normal reconciliation pass can call `publishInstanceStateToStore()`, which individually puts the observed instance fields.

The store implementations suppress writes when the value is unchanged, so this does **not** mean every 200 ms tick necessarily produces an MVCC revision. That is particularly important for the simulator: steady `state/service/node` values are normally no-ops.

### Failure controller writes

`controllers/failure.go` writes:

For a drain:

```text
observed/instance/<id>/probe/readiness
derived/instance/<id>/drain_since
```

For a failed instance:

```text
observed/instance/<old-id>/state = stopped
observed/instance/<new-id>
observed/instance/<new-id>/service
observed/instance/<new-id>/state = pending
```

So the failure controller directly overlaps the agent on:

```text
observed/instance/<id>/state
observed/instance/<id>/probe/*
```

### Node-failure controller writes

`controllers/nodefailure.go` writes:

```text
observed/node/<node>/state
observed/instance/<id>/state
```

Again, both are agent-owned observation areas.

### Instance controller writes

`controllers/instance.go` also writes `observed/instance/`:

* creates new instance markers/service/state;
* stops excess instances.

So the shared lifecycle prefix is not just controller-vs-agent; it is also **controller-vs-controller**.

### Historical evidence that this pattern is problematic

The repo itself already solved a very similar problem for draining.

M64 (`8d1d821...`) changed the drain controller from directly writing observed instance state to writing:

```text
derived/node/<node>/drain/evict/<instance>
```

The agent consumes that marker and performs the actual runtime stop/state observation.

That is strong evidence that **separating controller intent from agent observation is the intended architectural direction**.

---

# 3. The biggest issue: transaction size

This is more severe than the probabilistic contention problem.

etcd's configured default maximum transaction size is **128 operations**. ([etcd][2])

The repo also explicitly models the 128 limit in `controllers/runner.go`.

## Five-node failure

At the 50-node / 1000-instance scale, the load test distributes roughly 20 instances/node, so killing five nodes means roughly **100 affected instances**.

### NodeFailureController phase

For five failed nodes, approximately:

* 5 `observed/node/*/state` changes
* ~100 `observed/instance/*/state` changes

≈ **105 writes**

And because every changed existing key gets a revision compare:

≈ **105 compares + 105 writes = ~210 transaction items**

So the node-failure controller's all-at-once transaction is already over the repository's assumed 128-item budget.

### FailureController replacement phase

For every failed instance, `stopAndReplace()` emits **4 writes**.

For ~100 failed instances:

* 100 old instances stopped
* 100 replacement root keys
* 100 replacement service keys
* 100 replacement state keys

= **400 success operations**

Plus roughly 400 change-set compares.

That's ~800 logical transaction items.

Even before considering compares, **400 success operations alone are greater than etcd's 128-operation default**. ([etcd][2])

So a single transaction containing all 100 replacements is not merely "likely to conflict."

**It cannot fit.**

---

# 4. Why `maxInputKeyGuards=0` does not solve this

The current code correctly prioritizes change-set guards over input guards:

```text
operation for changed key
compare for changed key
...
then optional input-key compares
```

The `maxInputKeyGuards=0` setting only skips the last category.

For the 100-instance replacement example, you still have approximately:

```text
400 operations
400 change-set compares
```

Input guards being zero changes essentially nothing about the transaction's basic infeasibility.

The load tests therefore exercise a configuration that reduces contention from **read-only inputs**, but leaves the much more important **write-domain overlap** and **transaction-size** problems intact.

---

# 5. The 50-agent / 200 ms contention math

The prompt says:

> 50 agents × 200 ms = ~250 writes/sec

That arithmetic is fine as an upper bound for one definite heartbeat write per agent:

$$
50 / 0.2 = 250\ writes/sec
$$

But the proposed "1/250 chance per millisecond" is dimensionally wrong.

250 writes/sec is:

$$
0.25\ writes/ms
$$

not `1/250` writes/ms.

More importantly, **250 total writes/sec is not the relevant conflict rate**.

The relevant rate is:

> writes/sec to keys that are actually in the controller's compare set.

A heartbeat to:

```text
lease/node/node-17
```

does not invalidate:

```text
observed/instance/i123/state
```

because the compare is on a specific key's revision.

## Better model

Let:

* `N` = number of controller-guarded keys
* `r` = average agent write rate per guarded key
* `T` = scan-to-commit interval

Then:

$$
P(\text{conflict}) \approx 1 - (1-rT)^N
$$

and for small `rT`:

$$
P(\text{conflict}) \approx 1-e^{-NrT}
$$

That's the correct way to reason about the problem.

### Example hypothetical

Suppose the agents really were modifying 1,000 candidate instance keys once every 200 ms, uniformly:

$$
250/1000 = 0.25\ writes/sec/key
$$

Over a 100 ms window:

$$
rT = 0.025
$$

Then:

$$
P(\text{conflict}) \approx 1-e^{-0.025N}
$$

That gives approximately:

| Guarded keys N | Conflict probability |
| -------------: | -------------------: |
|             20 |                  39% |
|             50 |                  71% |
|            100 |                  92% |
|            184 |                  99% |
|            200 |                99.3% |

So once a controller is guarding on the order of a hundred actively changing keys, a 100 ms race window can become practically unwinnable.

### But the current repo is different

In the steady-state simulator, agent instance state writes are often identical-value writes and therefore **do not advance the revision**.

The consistently changing write is the heartbeat:

```text
lease/node/<id>
```

That key is not in the failure controller's change-set guards when input guards are disabled.

So the repository's current steady-state load is **less pathological than "250 random conflicting writes/sec."**

The real contention occurs when the agent is actually updating overlapping `observed/instance/*` keys—for example state transitions, probes, startup/failure observation, telemetry, or cleanup.

---

# 6. Retry behavior: improved, but insufficient

The current runner already implements the solution suggested in the prompt:

```text
10ms + jitter
20ms + jitter
40ms + jitter
80ms + jitter
...
500ms + jitter
```

With 10 attempts, there are 9 waits.

Average cumulative backoff is about:

**3.2 seconds**

Worst case is about:

**4.26 seconds**

With 25 attempts, the cumulative sleep alone becomes roughly:

**14.4 seconds average / 19.3 seconds worst case**

and that's before counting the repeated scans and controller computation.

So increasing retries from 10 to 25 is not an architectural fix. It just buys more lottery tickets.

Worse, every failed attempt repeats:

```text
ScanWithRevision(observed/instance/*)
ScanWithRevision(derived/instance/*)
Reconcile()
build transaction
Txn()
```

For the failure controller, the watched instance set can be large, so persistent contention causes **repeated full scans of the same large population**.

The M43 complexity work improved controller-local algorithms, but the runner still rescans the controller's whole watch domain on every retry.

---

# 7. The current 50-node benchmark does not actually validate the production failure mode

This is an important testing gap.

`loadtest/loadtest_test.go` has the explicit 50-node / 1000-instance / kill-five-nodes scenario, but it uses:

```text
store.NewMemoryStore()
```

and then manually does:

```text
Put observed/node/<id>/state = unreachable
Put observed/instance/<id>/state = failed
```

It does **not** rely on `NodeFailureController` to discover the failures.

It then accepts **95% near-convergence**, so the test can pass with roughly 50 of the 1000 workloads still not recovered.

Meanwhile `MemoryStore.Transaction()` has **no 128-operation transaction limit**.

That means the test can report success even when the production etcd path would reject the corresponding transaction as oversized.

This is probably the most important benchmark flaw to fix.

The newer `chaos/chaos_benchmark_test.go` also configures:

```text
50 nodes
1000 instances
200 ms agent interval
10 reconciliation attempts
maxInputKeyGuards = 0
```

but its recovery benchmark is oriented toward overall chaos recovery, not specifically proving that a mass-failure transaction fits etcd's operation budget.

---

# 8. Recommended solution ranking

## P0 — Add transaction budgeting and bounded batching

This is mandatory.

The runner should never construct a transaction that cannot fit the backend's limit.

At minimum:

```text
transaction budget = maxTxnOps
                  - compare count
                  - success operations
                  - failure operations
```

and the runner should split work before calling `Transaction()`.

For the failure replacement path:

* 4 writes per instance
* 4 compares per instance
* ~8 transaction items/instance

At the theoretical 128 limit:

$$
128/8 = 16
$$

So **16 replacements is the absolute ceiling** under the repository's accounting.

I would use **8–12 replacements per transaction**, not 16, leaving room for future guards and avoiding edge-of-limit behavior.

For a 100-instance failure:

```text
~9–13 transactions
```

instead of one impossible transaction.

### Do this in a controller-aware way

I would not blindly split arbitrary `Change` slices in the generic runner.

For `FailureController`, introduce something like:

```text
maxReplacementsPerCycle = 10
```

and generate at most that many replacements.

That preserves controller semantics and lets the next watch event / reconciliation cycle continue the work.

`DrainController` already demonstrates this design pattern: it deliberately rate-limits evictions to avoid a thundering herd.

---

# 9. P0 — Make MemoryStore enforce the same transaction limit

The in-memory implementation should fail oversized transactions just as production etcd does.

Otherwise tests can prove:

> "the reconciliation converges"

while production actually gets:

> `etcd: transaction has too many operations`

Add an explicit backend-independent limit, or enforce it in the runner and test the runner against it.

A good design is to expose a store capability such as:

```text
MaxTransactionOperations() int
```

with 128 for the current etcd-compatible implementation.

Even better, make the runner's transaction builder own the budget so all backends get the same safety invariant.

---

# 10. P0/P1 — Separate controller intent from agent observation

This is the deeper architectural fix.

Today the model is effectively:

```text
FailureController ----\
InstanceController ----+--> observed/instance/*
NodeFailureController -/
Node Agent ------------/
```

That is inherently contentious.

The much cleaner model is:

```text
controllers --> desired/derived/command-like keys
agent -------> observed/*
```

For example, failure handling could become:

```text
derived/instance/<id>/replacement = true
```

or:

```text
derived/instance/<id>/evict = true
```

Then the agent:

1. sees the intent;
2. stops/changes runtime state;
3. publishes the resulting `observed/instance/*`.

That is exactly the design direction demonstrated by M64's drain eviction marker.

### This has a major benefit

Once a controller's output domain is genuinely exclusive, **unconditional controller writes become reasonable**.

Until then, they don't.

---

# 11. Why I would not choose "unconditional controller writes" as the immediate fix

The proposed option:

> Controllers are authoritative for their output domain — skip CAS.

would reduce conflicts, but **FailureController is not actually the sole writer of `observed/instance/*`.**

The agent is explicitly an authoritative writer of observations there.

So replacing CAS with unconditional puts would turn:

```text
CAS conflict
```

into:

```text
last writer wins
```

which can reintroduce exactly the lifecycle races M64 had to fix.

The safe sequence is:

**first establish exclusive ownership, then relax CAS where justified.**

---

# 12. Agent write coalescing: useful, but secondary

Reducing heartbeat/telemetry traffic may lower store load, but it won't solve the core issue.

In particular:

* heartbeat writes are on `lease/node/*`;
* telemetry is on dedicated subkeys;
* the fatal conflict is on overlapping `observed/instance/*` keys;
* the 100-instance replacement transaction is independently too large.

So coalescing should be treated as a **store-load optimization**, not the correctness fix.

I would also avoid trying to create synchronized "quiet windows" where agents stop writing. That turns the store into a coordination protocol and is much harder to make correct across real distributed nodes.

---

# 13. Retry strategy should change after batching

Once batches are small, the current backoff+jitter becomes much more useful.

I would keep the existing basic policy:

```text
10 → 20 → 40 → 80 → 160 → 320 → 500ms
```

with jitter.

But add two protections:

### A. Retry only the failed batch

Don't rebuild an 800-operation plan after one conflict.

Reconcile into bounded work units and retry only the unit that failed.

### B. Avoid "abandoned == success"

`executeReconciliationCycle()` increments:

```text
reconciliationTotal(..., "abandoned")
```

but then returns `nil`.

The caller therefore treats the cycle as non-error even though no desired change was committed.

For a controller, "abandoned because all CAS attempts failed" is semantically different from "successful reconciliation."

At minimum, preserve that distinction in the control loop and instrumentation. Otherwise operationally it is very easy to miss the failure.

---

# 14. The benchmark I would add

I would add a regression test that specifically models the production failure:

```text
50 nodes
1000 instances
5 failed nodes
~100 affected instances
agent interval = 200 ms
etcd-compatible max txn ops = 128
input guards = 0
```

Then assert:

```text
failure transaction size <= maxTxnOps
replacement batches <= maxTxnOps
all 100 failed instances eventually receive replacements
0 abandoned reconciliation cycles
```

And separately run an intentional contention test where agents continuously mutate overlapping instance keys.

The test should record:

```text
controller
attempt
transaction compares
transaction ops
batch size
conflict count
commit latency
abandoned count
```

The existing metrics framework already has:

```text
ccattler_reconciliation_conflicts_total
ccattler_reconciliation_duration_seconds
ccattler_reconciliation_total
ccattler_reconciliation_changes_total
```

so extending that instrumentation would fit the repository's current observability model.

---

# Bottom line

I would classify the findings this way:

| Finding                                                     | Severity                | Why                                                    |
| ----------------------------------------------------------- | ----------------------- | ------------------------------------------------------ |
| FailureController can generate ~400 writes for 100 failures | **P0**                  | Cannot fit in etcd's 128-op transaction                |
| NodeFailureController can generate ~210 compare+write items | **P0**                  | Also exceeds transaction budget                        |
| Agent + controllers share `observed/instance/*`             | **P0/P1**               | Genuine CAS conflict surface and lifecycle race        |
| `maxInputKeyGuards=0`                                       | **Not sufficient**      | Removes read guards, not output guards                 |
| Exponential backoff + jitter                                | **Good / insufficient** | Already implemented; helps probability, not structure  |
| Full rescan on every retry                                  | **P1**                  | Amplifies cost under contention                        |
| Unconditional controller writes                             | **Unsafe today**        | Failure does not own `observed/instance/*` exclusively |
| Agent write coalescing                                      | **P2**                  | Useful optimization, not root fix                      |
| MemoryStore lacks etcd txn-size enforcement                 | **P0 test gap**         | Benchmark can pass invalid production behavior         |
| 95% near-convergence criterion                              | **P1 test gap**         | Can hide unrecovered workloads                         |

### Recommended implementation order

**1. Bound transaction size.
2. Rate-limit/batch FailureController work.
3. Make MemoryStore enforce the production limit.
4. Add a real 5-node regression test against that limit.
5. Refactor controller intent away from `observed/instance/*`.
6. Only then consider relaxing CAS on genuinely exclusive output domains.
7. Finally optimize retry scanning/coalescing.**

The key conceptual correction for engineers investigating this is:

> **The livelock is not caused by "the store is changing a lot." It is caused by large, multi-key CAS transactions whose guarded keys are sometimes being changed by another writer. And independently, some of those transactions are too large to be legal in etcd at all.**

That distinction should drive the implementation. ([etcd][3])

[1]: https://etcd.io/docs/v3.7/dev-guide/api_reference_v3/?utm_source=chatgpt.com "API reference | etcd"
[2]: https://etcd.io/docs/v3.4/op-guide/configuration/?utm_source=chatgpt.com "Configuration options | etcd"
[3]: https://etcd.io/docs/v3.7/learning/api/?utm_source=chatgpt.com "etcd API | etcd"
