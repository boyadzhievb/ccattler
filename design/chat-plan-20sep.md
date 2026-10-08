# CCattler — Full Project Review & Improvement Execution Plan
Date: 2026-09-20
Repository: `boyadzhievb/ccattler`
Review baseline: commit `3e1b2a8` ("Clean up tech debt — remove dead types, extract shared helpers, add doc comments")
Purpose: execution plan for another model/engineer to systematically audit, test, harden, optimize, and improve the entire project.

---

## 0. Mission and execution rules

This is a **correctness-first infrastructure project**. Do not optimize for feature count. The primary goal is to prove that CCattler is:

1. correct under concurrency,
2. convergent after failures and restarts,
3. honest about observed reality,
4. safe under malformed/untrusted input,
5. operationally diagnosable,
6. deterministic where deterministic behavior is expected,
7. performant at realistic cluster sizes,
8. maintainable as controllers, agents, runtimes, and providers grow.

### Non-negotiable architectural invariant

> **Controllers produce plans. The store commits state. Agents produce observations. Nothing treats an action as truth.**

The review must repeatedly test this invariant.

### Second invariant

> **No component assumes another component performed an action; components react only to committed state and observations.**

### Execution discipline

- Start by auditing the current repository. Do not assume this document describes the current code perfectly.
- Record every finding with:
  - severity: P0/P1/P2/P3,
  - exact file/function,
  - failure mode,
  - evidence,
  - recommended change,
  - test proving the fix.
- Prefer small, reviewable commits.
- Do not mix correctness fixes with unrelated refactors.
- Add regression tests before or together with fixes.
- Run the race detector for concurrency-sensitive changes.
- Never declare a subsystem fixed because a happy-path test passes.
- For distributed behavior, test delayed, duplicated, reordered, dropped, stale, and conflicting observations/events.
- Do not silently weaken semantics to make tests pass.
- Preserve the facts/relations/reconciliation model; do not redesign CCattler into Kubernetes-like user-facing objects.

---

# 1. Current review: confirmed high-priority issues

The following were rechecked against commit `3e1b2a8`.

## P0.1 — Reconciliation snapshot/CAS is incomplete

Current flow in `controllers/runner.go`:

```
ScanWithRevision
    ↓
Reconcile(facts)
    ↓
build compares only for changed/output keys
    ↓
Transaction
```

The scan revision is collected but is not used as a transaction precondition. A fact read by the controller can change after the scan while an unrelated output key remains unchanged, allowing a stale plan to commit.

Example:

1. controller observes instance A = Ready,
2. computes endpoint inclusion,
3. instance A changes to NotReady,
4. endpoint key did not exist before,
5. transaction checks only endpoint key,
6. stale endpoint can still be committed.

### Required target

```
Snapshot @ revision R
    ↓
Reconcile(snapshot)
    ↓
Plan {
    BaseRevision: R
    Preconditions: [...]
    Changes: [...]
}
    ↓
Store.Txn(plan)
    ↓
atomic commit or conflict
```

Preferred long-term design:

```text
Controller
  Reads    = facts whose values influence plan
  Watches  = facts that trigger reconciliation
  Writes   = authoritative key domains owned by controller
```

The transaction must protect the assumptions that influenced the plan, not merely the keys being written.

---

## P0.2 — etcd Put has a concurrency hole

`store/etcd.go` currently:

1. reads current value,
2. attempts a ModRevision CAS,
3. if CAS fails, performs an unconditional PUT.

That means:

```
A reads revision 10
B writes revision 11
A CAS fails
A unconditional PUT overwrites B
```

This is unsafe.

### Required target

Choose one explicit semantic:

- unconditional Put with idempotent no-op suppression, or
- conditional/CAS Put returning conflict.

Do not silently convert a failed CAS into an unconditional overwrite.

If preserving unconditional Put semantics, implement no-op suppression without claiming the operation is conditional.

---

## P0.3 — Runtime action is incorrectly treated as observed truth

In `agent/agent.go`, after `runtime.Start()` succeeds, the agent immediately publishes `InstanceRunning`.

A successful Start call proves only that the runtime accepted/started the operation. It does not necessarily prove the workload is alive.

### Required target

```
desired placement
    ↓
runtime.Start()
    ↓
runtime.Status()/List()
    ↓
publish /observed from runtime observation
```

The agent may record an operation/error separately, but lifecycle truth must originate from runtime observation.

Test cases must include:

- Start returns nil but workload immediately exits,
- Start returns nil but runtime reports not-running,
- runtime is temporarily unavailable,
- process/container exists but health is not ready.

---

## P0.4 — Controllers write observed state

`InstanceController` creates instance identity/state under `/observed/instance/*`.

It also marks excess instances as `InstanceStopped`.

This conflicts with a clean ownership model where:

- `/desired/*` = user intent,
- `/effective/*` = policy-resolved intent,
- `/placement/*` = scheduler decisions,
- `/derived/*` = controller-owned derived state,
- `/observed/*` = agent/runtime truth.

### Required target

Move controller-owned lifecycle intent out of `/observed`.

Possible model:

```
/desired
/effective
/placement
/derived
/observed
```

The exact key layout may change, but every authoritative key must have exactly one clear writer/owner.

---

## P0.5 — Network VIP allocation is race-prone

`controllers/network.go` calculates the next VIP by scanning existing VIPs and incrementing the highest fourth octet.

Two concurrent reconciliations can choose the same free VIP.

### Required target

Use one of:

- transactional allocation/reservation,
- deterministic allocation from stable identity with collision checking,
- an atomic allocator key/range.

Test concurrent creation of many services and assert:

- no duplicate VIP,
- no lost allocation,
- stable allocation after retries,
- correct recovery after deletion/recreation,
- exhaustion is explicit and observable.

---

## P1.1 — Event generation is based on controller changes, not committed transitions

`controllers/runner.go` calls `emitEventsForChanges()` after a transaction.

This means semantic events are inferred from controller intent rather than actual before/after committed state.

### Required target

Introduce an Event Projector:

```
committed state transition
        ↓
EventProjector
        ↓
semantic event
```

The projector should use actual committed old/new values and revision metadata.

This also makes events independent of which controller produced the mutation.

---

## P1.2 — etcd Event.Prev is not guaranteed to be populated

The event abstraction contains `Prev *Fact`, but the etcd watch path must use etcd `WithPrevKV()` if previous values are promised.

Either:

- implement previous-value population and test it, or
- remove/relax the field's semantic guarantee.

Do not expose a field whose documented meaning is false.

---

## P1.3 — Probe scheduling remains coupled to reconciliation

The agent invokes health/probe execution from the workload reconciliation path and throttles with timestamps.

Required target:

```
Agent
├── Reconciliation loop
└── Probe scheduler
    ├── startup
    ├── readiness
    └── liveness
```

Probe execution must have its own scheduling lifecycle, cancellation, backoff, thresholds, and observation reporting.

---

## P1.4 — localhost fallback is dangerous

Health checking currently defaults a missing instance IP to `127.0.0.1`.

This can cause a missing/invalid workload network address to probe an unrelated local service.

### Required target

Missing address => explicit Unknown/NetworkUnavailable.

Never silently probe localhost.

Add a regression test proving that a missing IP cannot result in a localhost probe.

---

## P1.5 — Init phase has multiple authorities

The initialization phase is derived from step observations, but the agent also writes phase facts.

Choose one owner.

Preferred:

```
Agent → step observations
InitController → derived initialization phase
```

Do not have two independent writers for the same authoritative state.

---

## P1.6 — Init execution semantics must be made explicit

The runtime interface has `Exec()`, but the agent's init implementation has historically used host-process execution paths.

Audit the current implementation.

Required decision:

- init executes inside/through the workload runtime, OR
- init is explicitly host-side and modeled as host initialization.

For containerized workloads, prefer runtime-backed semantics so init does not accidentally execute on the control host.

Test namespace, environment, filesystem, identity, timeout, exit status, and restart behavior.

---

## P1.7 — Agent remains too broad

`agent/agent.go` still owns many responsibilities:

- node registration/heartbeat,
- runtime reconciliation,
- init,
- storage,
- secrets,
- network,
- health,
- probes,
- telemetry,
- cleanup,
- endpoint metadata.

Refactor by ownership boundaries, not merely file size:

```
agent/
  agent.go
  node/reporter.go
  runtime/reconciler.go
  init/executor.go
  network/reconciler.go
  storage/reconciler.go
  secrets/materializer.go
  health/scheduler.go
  health/reporter.go
  telemetry/collector.go
```

The main Agent should compose these services.

---

## P1.8 — Multiple service ports are still not represented cleanly

Current service exposure effectively reduces ports to a map/single port in parts of the implementation.

Introduce a typed representation such as:

```go
type ServicePort struct {
    Name     string
    Port     int
    Protocol string
}
```

Endpoint data should identify:

- service,
- instance,
- address,
- port,
- protocol,
- readiness/eligibility.

---

# 2. Phase 1 — Repository and architecture inventory

Before changing code, produce an inventory.

## 2.1 Repository map

Inspect:

- all Go packages,
- CLI commands,
- DSL lexer/parser/AST/compiler,
- store implementations,
- controller implementations,
- runner,
- scheduler,
- agent,
- runtime implementations,
- network providers,
- storage providers,
- secret providers,
- identity/security code,
- API server,
- telemetry/observability,
- tests,
- examples,
- scripts,
- CI/CD,
- Docker/container/OCI integration,
- docs and architecture documents.

Deliverable:

`audit/repository-map.md`

Include dependency direction and unexpected cycles.

## 2.2 Build/test baseline

Run and record:

```bash
go version
go test ./...
go test -race ./...
go vet ./...
go test -run=^$ ./...
go list -deps ./...
go mod verify
go mod tidy -diff
```

Also inspect:

```bash
git status
git log --oneline -20
git diff
```

Do not modify generated or dependency files merely to make checks quiet without understanding why.

## 2.3 Static inventory

Use:

```bash
go tool nm
go list -json ./...
```

and suitable static analysis tools available in the environment.

Search for:

- TODO/FIXME,
- panic,
- log.Fatal,
- os.Exit,
- ignored errors,
- context.Background inside request paths,
- time.Sleep in control loops,
- goroutine creation,
- unchecked type assertions,
- global mutable state,
- localhost fallbacks,
- direct runtime access from controllers,
- direct store writes to observed state,
- shell execution,
- credential/token handling,
- unsafe deserialization,
- arbitrary file writes,
- network listeners.

---

# 3. Phase 2 — State-store correctness audit (P0 gate)

The store is the foundation. Do not continue to higher-level optimization until this gate passes.

## 3.1 StateStore semantics

Define exact semantics for:

- Get,
- List/Scan,
- Put,
- Delete,
- Transaction,
- Watch,
- Revision,
- Close.

For every method document:

- linearizability/consistency expectation,
- revision semantics,
- idempotency,
- error behavior,
- cancellation,
- closed-store behavior,
- byte ownership/deep-copy behavior.

## 3.2 Revision model

Prove:

- every committed transaction has one logical revision,
- all writes in one transaction share that revision,
- no-op writes do not create semantic changes,
- scans return a coherent snapshot revision,
- a snapshot can be safely reconciled against that revision,
- revision numbers are monotonic,
- revision behavior is consistent across MemoryStore and etcd.

## 3.3 Transaction tests

Create concurrency tests for:

- two writers same key,
- two writers different keys,
- delete vs put,
- put vs put,
- compare existing revision,
- compare missing key revision 0,
- multiple compares,
- failure branch,
- transaction cancellation,
- transaction on closed store,
- atomicity across many keys.

## 3.4 Put semantics

Explicitly test:

```
same value → no event/no semantic revision bump
different value → one write
concurrent writers → no lost update
```

Fix the etcd fallback described above.

## 3.5 Watch semantics

Define watch as **loss-tolerant notification**, not guaranteed delivery.

Required behavior:

- event contains key/type/revision,
- overflow is observable,
- compaction is observable,
- cancellation unregisters watcher,
- close terminates watchers,
- consumer can resync from a known revision,
- no event channel leak,
- no blocked producer leak.

Test:

- burst > channel capacity,
- consumer slower than producer,
- cancellation while producer is blocked,
- store close,
- watcher prefix isolation,
- overlapping prefixes,
- compaction/restart,
- event ordering.

## 3.6 MemoryStore vs etcd equivalence

Build a conformance suite that runs against both implementations.

The same semantic test suite should execute against:

- MemoryStore,
- EtcdStore,
- future SQLite store if retained.

---

# 4. Phase 3 — Reconciliation protocol redesign (P0 gate)

Implement the transactional plan model.

Suggested API:

```go
type ReconcilePlan struct {
    BaseRevision  Revision
    Preconditions []Compare
    Changes       []Change
}
```

Preferred store API direction:

```go
type StateStore interface {
    Get(ctx context.Context, key string) (Fact, error)
    List(ctx context.Context, prefix string) (Snapshot, error)
    Put(ctx context.Context, key string, value []byte) (WriteResult, error)
    Delete(ctx context.Context, key string) (WriteResult, error)
    Txn(ctx context.Context, txn Transaction) (TxnResult, error)
    Watch(ctx context.Context, prefix string, opts WatchOptions) (Watch, error)
    Revision(ctx context.Context) (Revision, error)
    Close() error
}
```

Do not blindly copy this API if the current code exposes a better equivalent; preserve semantics over names.

## 4.1 Snapshot coherence

A controller must reconcile against a defined snapshot.

If multiple prefixes are scanned separately, prove they refer to one coherent revision. If the store cannot provide that cheaply, redesign List/Scan to accept/return a revision or use one transactional read.

## 4.2 Dependency-aware preconditions

Controllers should declare:

```
Reads
Watches
Writes
```

The runner can then:

- watch dependencies,
- read dependencies,
- construct a plan,
- create compares for read assumptions,
- reject writes outside owned domains.

## 4.3 Determinism

For identical snapshot state:

```
Reconcile(snapshot) == same plan
```

except for explicitly allocated IDs/non-deterministic fields.

Instance ID generation needs special treatment. Consider deterministic IDs or transactional reservation.

## 4.4 Conflict handling

Define:

- conflict error type,
- retry count,
- jitter/backoff,
- starvation prevention,
- metrics,
- logs,
- final failure behavior.

Do not silently abandon a plan without making the condition observable.

---

# 5. Phase 4 — State ownership and truth model

Produce a key ownership matrix.

Example:

| Domain | Owner | Readers | Source of truth |
|---|---|---|---|
| desired | API/user | controllers/agents | user intent |
| effective | policy/effective controller | scheduler/agents | derived intent |
| placement | scheduler | agents/network | scheduler decision |
| derived | named controller | other controllers | controller-owned derivation |
| observed | agent/runtime | controllers/API | runtime reality |
| policy | API/policy subsystem | controllers | policy |
| events | projector | API/CLI | committed transitions |

Every key prefix must have exactly one authoritative writer.

## Tests

Add ownership enforcement where practical.

A controller attempting to write another controller's domain should fail in tests and, preferably, at runtime.

---

# 6. Phase 5 — Runtime truth and agent correctness

## 6.1 Runtime conformance suite

Every runtime implementation must pass the same contract:

- Start idempotency,
- Stop idempotency,
- Status accuracy,
- List completeness,
- Exec semantics,
- context cancellation,
- timeout behavior,
- exit code reporting,
- process/container disappearance,
- crash detection.

## 6.2 Start/status truth

Implement:

```
Start
  ↓
observe Status/List
  ↓
publish observed state
```

Never infer Running solely from a successful Start call.

## 6.3 Runtime failure matrix

Test:

| Failure | Expected result |
|---|---|
| start rejected | observed Failed/Pending with reason |
| start accepted then exits | observed stopped/failed based on exit |
| runtime unavailable | observed Unknown/degraded |
| agent restarts | state recovered from runtime List |
| controller restarts | convergence |
| duplicate start | no duplicate workload |
| stale placement | agent stops/reconciles safely |

---

# 7. Phase 6 — Controller-by-controller audit

Audit every controller using the same checklist.

Controllers currently include at least:

- instance,
- endpoint,
- failure,
- autoscale,
- network,
- storage,
- initialization/effective/policy-related controllers as present.

For each controller document:

1. Inputs/read dependencies.
2. Watch dependencies.
3. Output/write ownership.
4. Invariants.
5. Idempotency.
6. Determinism.
7. Conflict assumptions.
8. Failure behavior.
9. Stale-observation behavior.
10. Scale characteristics.
11. Complexity.
12. Metrics.
13. Unit tests.
14. concurrency tests.
15. convergence tests.

## Instance controller

Specifically test:

- scale up/down concurrently,
- repeated reconcile,
- generated ID collision,
- stale observed state,
- controller restart,
- failed agent,
- pending instance cleanup,
- zero desired count,
- rapid scale changes.

Do not let the controller invent runtime truth.

## Endpoint controller

Test:

- readiness transitions,
- endpoint removal,
- duplicate endpoints,
- stale endpoint cleanup,
- multiple ports,
- protocol,
- endpoint ordering/determinism.

## Network controller

Test:

- concurrent VIP allocation,
- VIP exhaustion,
- deletion/recreation,
- stale DNS,
- endpoint disappearance,
- multi-port services,
- collision recovery.

## Failure controller

Ensure failure handling creates/updates desired/derived intent rather than writing false observed reality.

## Autoscaler

Verify:

```
signals
 → recommendation
 → constraints
 → desired/effective capacity
 → instance reconciliation
```

It must not directly start/stop workloads.

Test oscillation, cooldown, noisy metrics, conflicting signals, min/max, quota, capacity, and concurrent policy changes.

---

# 8. Phase 7 — Agent decomposition and lifecycle

Split by ownership boundary.

## Node reporter

Audit:

- enrollment,
- identity,
- heartbeat,
- leases,
- stale node detection,
- restart behavior,
- clock assumptions.

Use leases/TTL semantics where appropriate.

## Runtime reconciler

Own only:

- desired placement,
- runtime state,
- start/stop,
- runtime observation.

## Probe scheduler

Own:

- schedule,
- timeout,
- retries,
- thresholds,
- startup/readiness/liveness transitions.

## Health reporter

Only writes observations.

## Storage reconciler

Test:

- attach/detach idempotency,
- crash during attach,
- stale volume ownership,
- cleanup after workload disappearance.

## Network reconciler

Test:

- allocation/release,
- duplicate cleanup,
- restart recovery,
- provider failure.

## Secret materializer

Test:

- least privilege,
- permissions,
- cleanup,
- rotation,
- crash recovery,
- secret leakage in logs/errors.

---

# 9. Phase 8 — Initialization semantics

Make initialization first-class but not Kubernetes-shaped.

Required lifecycle:

```
PENDING
  ↓
INITIALIZING
  ↓
INITIALIZED
  ↓
STARTING
  ↓
RUNNING
```

Failure should retain:

- step,
- reason,
- exit code,
- timestamp,
- attempt count.

Tests:

- sequential init,
- timeout,
- non-zero exit,
- retry,
- restart-safe/idempotent init,
- crash between steps,
- main workload blocked until init complete,
- init cancellation,
- secret/config availability,
- runtime namespace correctness.

If DAG initialization is not implemented, do not pretend it is.

---

# 10. Phase 9 — Probes and health model

Use richer observations than bool.

Suggested model:

```
Unknown
Pending
Healthy
Unhealthy
```

Track:

- first observed,
- last observed,
- consecutive successes,
- consecutive failures,
- transition time,
- reason,
- latency.

Semantics:

- startup: is initialization/startup complete?
- readiness: should receive traffic?
- liveness: should workload be restarted/recovered?

Readiness failure must not automatically mean restart.

Liveness failure must be policy-driven and thresholded.

Test state-machine transitions exhaustively.

---

# 11. Phase 10 — DSL and API audit

Audit lexer/parser/AST/compiler/API independently from orchestration.

## DSL

Test:

- malformed syntax,
- duplicate declarations,
- invalid values,
- integer overflow,
- duration parsing,
- resource parsing,
- Unicode identifiers if supported,
- comments,
- whitespace,
- ambiguous constructs,
- error locations,
- deterministic compilation.

Fuzz:

```go
go test -fuzz=Fuzz... ./lang/...
```

No parser fuzz input should panic or hang.

## API

Audit:

- authentication,
- authorization,
- validation,
- request size limits,
- timeouts,
- cancellation,
- pagination/list limits,
- error mapping,
- idempotency,
- concurrency,
- rate limiting,
- audit logging.

Never trust client-supplied state as observed truth.

---

# 12. Phase 11 — Security audit

Treat security as a first-class review, not a final checkbox.

## 12.1 Threat model

Write:

`security/threat-model.md`

Actors:

- unauthenticated external user,
- authenticated human,
- compromised client,
- compromised node,
- compromised workload,
- malicious tenant,
- compromised controller,
- malicious/misconfigured provider,
- compromised etcd credentials.

Assets:

- workload identity,
- secrets,
- cluster state,
- node credentials,
- etcd,
- API credentials,
- network access,
- host filesystem,
- container runtime socket.

Threat categories:

- privilege escalation,
- tenant escape,
- secret disclosure,
- forged observations,
- fake node enrollment,
- malicious workload command execution,
- state corruption,
- replay,
- impersonation,
- denial of service.

## 12.2 Authentication

Verify:

- node enrollment,
- one-time bootstrap credentials,
- mTLS,
- certificate rotation,
- certificate revocation/expiry,
- internal CA protection.

## 12.3 Authorization

Implement/check:

- RBAC,
- ABAC,
- fact-prefix permissions,
- controller identities,
- node identity,
- tenant boundaries,
- cross-scope access.

Key principle:

> Scope organizes. Policy enforces. Ownership defines responsibility.

## 12.4 Secrets

Check:

- encryption at rest,
- envelope encryption,
- key rotation,
- no plaintext logs,
- file permissions,
- memory lifetime,
- cleanup,
- rotation,
- access audit.

## 12.5 Command execution

This is a critical attack surface.

Audit every use of:

- os/exec,
- shell,
- sh -c,
- bash -c,
- nerdctl,
- containerd,
- runtime Exec.

Check:

- argument injection,
- shell injection,
- environment injection,
- path traversal,
- arbitrary host command execution,
- working directory,
- UID/GID,
- namespaces,
- capabilities,
- seccomp/AppArmor/SELinux where applicable.

## 12.6 etcd security

Verify:

- TLS,
- client authentication,
- least-privilege credentials,
- no secrets in command-line arguments,
- endpoint validation,
- certificate verification,
- timeout behavior.

---

# 13. Phase 12 — Network and isolation audit

Audit the NetworkProvider abstraction.

Required properties:

- provider-neutral controller logic,
- identity-based policy,
- explicit default-deny semantics where intended,
- deterministic policy compilation,
- safe rule updates,
- rollback/recovery,
- stale-rule cleanup.

Test:

- allow A→B,
- deny A→B,
- unrelated traffic,
- policy change during traffic,
- provider restart,
- node restart,
- duplicate rule application,
- stale endpoint removal.

Never use labels/strings as security identity without authentication/binding.

---

# 14. Phase 13 — Storage provider audit

For each provider:

- ownership,
- lifecycle,
- attach/detach idempotency,
- crash recovery,
- concurrent attachment,
- cleanup,
- capacity,
- permissions,
- encryption,
- stale mounts.

Test agent restart at every lifecycle boundary.

---

# 15. Phase 14 — Persistence and recovery

Test real restart scenarios.

## Store restart

- controller reconnect,
- agent reconnect,
- watches re-established,
- revision continuity,
- resync,
- no duplicated workloads.

## Controller restart

Kill/restart each controller during:

- before scan,
- after scan,
- during reconcile,
- before commit,
- after commit,
- after watch creation.

Expected result: convergence, not corruption.

## Agent restart

Kill agent while:

- starting workload,
- stopping workload,
- initializing,
- attaching storage,
- allocating IP,
- materializing secrets,
- running probe.

After restart, reconstruct truth from runtime/provider state.

---

# 16. Phase 15 — Distributed-systems correctness

Create a deterministic fault-injection simulator.

Inject:

- dropped watch event,
- duplicate watch event,
- reordered event,
- delayed event,
- stale fact,
- transaction conflict,
- controller restart,
- agent restart,
- network partition,
- delayed store response,
- provider failure,
- runtime crash.

For each scenario assert:

```
eventual convergence
+
no duplicate ownership
+
no false observed truth
+
no lost committed state
```

Define convergence formally:

Given stable desired state and eventually available dependencies, the system eventually reaches a fixed point where:

```
desired/effective/placement
        ↓
runtime reality
        ↓
observed
```

matches the controller invariants.

---

# 17. Phase 16 — Performance audit

Do not optimize by intuition. Measure.

## 17.1 Baselines

Benchmark:

- store Get,
- store Put,
- store Scan/List,
- transactions,
- watches,
- controller reconcile,
- scheduler,
- DSL compile,
- API request latency,
- runtime List/Status,
- probe execution.

Use:

```go
go test -bench=. -benchmem ./...
```

Profile realistic workloads.

## 17.2 Complexity audit

For every hot loop identify:

- time complexity,
- memory complexity,
- number of store reads,
- number of store writes,
- number of network round trips.

Pay particular attention to:

- agent scanning all placements,
- per-instance Get calls,
- controller scanning overlapping prefixes,
- endpoint recomputation,
- VIP allocation,
- autoscaler scans,
- watch fan-out.

Avoid accidental:

```
O(instances × store requests)
```

when a single snapshot can provide the data.

## 17.3 Store optimization

Investigate:

- batching,
- one snapshot per reconcile,
- fewer scans,
- prefix indexing,
- no-op suppression,
- watch coalescing,
- transaction size,
- serialization cost.

Do not sacrifice consistency for speed.

## 17.4 Memory

Profile:

- fact copies,
- []byte allocations,
- repeated prefix scans,
- event buffers,
- watch channels,
- controller snapshots,
- secret materialization.

Use:

```
go test -bench=. -benchmem
go test -race ./...
```

and CPU/heap profiles.

---

# 18. Phase 17 — Scalability and load testing

Create synthetic clusters:

- 10 nodes / 100 workloads,
- 50 nodes / 1,000 workloads,
- 100 nodes / 10,000 workloads,
- larger only if architecture supports it.

Measure:

- steady-state CPU,
- memory,
- etcd QPS,
- controller reconcile rate,
- watch count,
- event throughput,
- scheduling latency,
- API latency,
- convergence time after mass change.

Test mass operations:

- scale 1 → 10,000,
- delete 1,000 workloads,
- node failure storm,
- controller restart storm,
- watch reconnect storm.

Define explicit targets before optimizing.

---

# 19. Phase 18 — Scheduler audit

Scheduler should remain close to a pure function:

```
schedule(requirements, nodes, policies) -> placement
```

Audit:

- determinism,
- tie-breaking,
- fairness,
- resource capacity,
- constraints,
- affinity/anti-affinity,
- zones,
- architecture,
- quotas,
- fragmentation,
- unschedulable explanations.

Tests:

- same input → same output,
- capacity exhaustion,
- conflicting constraints,
- node disappearance,
- concurrent desired changes,
- fairness across tenants.

Performance benchmark scheduler separately from store/runtime.

---

# 20. Phase 19 — Autoscaling audit

Build a model-based test suite.

Properties:

- never below min,
- never above max,
- quota respected,
- unavailable capacity handled,
- noisy metrics do not cause uncontrolled oscillation,
- cooldown respected,
- scale decisions converge,
- desired state remains explainable.

Keep:

- recommendation,
- constraints,
- final desired value,
- reason.

This is valuable for `cca describe`.

---

# 21. Phase 20 — Observability and operations

Operational interfaces should answer:

- What do I want?
- What is effective?
- Where is it placed?
- What is actually running?
- Why is it not running?
- What changed?
- Which controller owns it?
- What is failing?
- How long has it been failing?

Audit CLI:

```
cca status
cca top nodes
cca top workloads
cca get workload <name>
cca describe workload <name>
cca logs <name>
cca events
cca watch
cca metric
```

Add:

- controller health,
- reconcile duration,
- reconcile failures,
- conflict retries,
- store latency,
- watch overflow,
- scheduler latency,
- runtime errors,
- probe transitions,
- workload restart count.

Distinguish:

- state telemetry,
- metrics,
- events,
- traces,
- logs.

Do not store time-series metrics in etcd.

---

# 22. Phase 21 — Event model

Implement event projection from committed state.

Event projector should support:

- actual old/new state,
- event revision,
- timestamp,
- subject,
- reason,
- source domain,
- correlation/reconcile ID if available.

Events must not be used as authoritative state.

Test duplicate/replayed transitions.

---

# 23. Phase 22 — API/CLI UX and operability

Audit every CLI command for:

- useful errors,
- exit codes,
- deterministic output,
- machine-readable output,
- timeouts,
- context cancellation,
- no secret leakage,
- no misleading state claims.

Add JSON output where operational automation needs it.

Avoid presenting implementation objects as the primary user model.

The CLI should speak in terms such as:

- workload/service,
- desired state,
- effective state,
- placement,
- observed state,
- readiness,
- initialization,
- policy,
- events.

---

# 24. Phase 23 — Fuzzing and property-based testing

Fuzz:

- DSL parser,
- DSL compiler,
- state codecs,
- key parsing,
- API payloads,
- network policy parsing,
- resource quantities,
- durations,
- event decoding.

Properties:

- parse invalid input never panics,
- encode/decode round trip,
- key construction/parsing round trip,
- reconciliation is idempotent,
- no-op state produces no changes,
- store implementations satisfy same contract.

---

# 25. Phase 24 — Race, leak, and lifecycle audit

Run:

```go
go test -race ./...
```

Repeatedly.

Also inspect goroutines around:

- controller watches,
- event channels,
- store watchers,
- agent loops,
- probes,
- telemetry,
- API requests.

Test:

- cancellation,
- shutdown,
- restart,
- repeated start/stop,
- watcher cancellation,
- store close,
- controller restart.

Use goroutine leak detection where practical.

No goroutine should survive the component lifecycle unless explicitly owned.

---

# 26. Phase 25 — Dependency and supply-chain security

Audit:

- `go.mod`,
- `go.sum`,
- direct vs indirect dependencies,
- abandoned dependencies,
- known vulnerabilities,
- transitive vulnerabilities,
- license compatibility.

Add CI checks for:

- dependency vulnerability scanning,
- secret scanning,
- static analysis,
- race tests,
- fuzz smoke tests,
- reproducible builds if practical.

Pin/review tool versions.

---

# 27. Phase 26 — Build/release/reproducibility

Audit:

- version injection,
- reproducible binaries,
- release artifacts,
- checksums,
- SBOM,
- provenance/signing,
- install script security,
- upgrade/downgrade behavior.

The README currently installs via:

```
curl -fsSL .../install.sh | bash
```

Treat the install path as a supply-chain-sensitive artifact.

Add checksum/signature verification where practical.

---

# 28. Phase 27 — Documentation correctness audit

Documentation must not claim capabilities that tests do not prove.

Cross-check:

- README,
- CLAUDE.md,
- etcd schema docs,
- DSL docs,
- CLI docs,
- architecture diagrams,
- examples,
- release notes.

Specifically verify claims such as:

- "516+ tests",
- "deployed and tested on real multi-host clusters",
- security features,
- mTLS,
- multi-tenancy,
- autoscaling,
- storage,
- network isolation,
- containerd support.

If a feature exists only partially, document its current status honestly.

---

# 29. Phase 28 — Chaos and recovery matrix

Build an automated matrix.

| Component | Failure | Expected |
|---|---|---|
| etcd | unavailable | retry/backoff, no false truth |
| controller | killed | restart + converge |
| agent | killed | runtime recovered + converge |
| runtime | unavailable | observed Unknown/degraded |
| workload | crashes | observed failure + policy recovery |
| watch | overflow | full resync |
| network | partition | safe degradation |
| storage | attach failure | workload not falsely Running |
| secret provider | unavailable | workload blocked safely |
| probe | timeout | correct threshold state |
| API | client disconnect | cancellation |
| scheduler | no capacity | explainable unschedulable state |

---

# 30. Phase 29 — Formal invariants and model tests

Write down invariants explicitly.

Examples:

### Truth

```
observed state is written only by observation-producing components
```

### Ownership

```
one authoritative owner per fact domain/key
```

### Reconciliation

```
same stable state + same policy → same desired/effective result
```

### Idempotency

```
reconcile(reconcile(S)) = reconcile(S)
```

### Convergence

```
stable intent + functioning dependencies → eventual fixed point
```

### Safety

```
failed/unknown observation must not be converted into healthy/running truth
```

### Transactionality

```
a committed plan changes all of its protected assumptions atomically
```

Build tests around these properties rather than only examples.

---

# 31. Phase 30 — Architecture cleanup after correctness

Only after P0/P1 correctness gates pass:

## 31.1 Typed state model

Move from stringly-typed facts toward typed:

- keys,
- codecs,
- values,
- domain schemas.

Keep serialization at store boundaries.

## 31.2 Separate effective state

Make policy resolution explicit:

```
desired
  +
policy
  +
quota
  +
capacity
  +
placement constraints
  ↓
effective
```

Make the reason explainable.

## 31.3 Dependency declarations

Replace `Watch()` with richer dependency metadata.

## 31.4 Controller ownership

Enforce writes against declared domains.

## 31.5 Agent composition

Keep Agent small and compositional.

---

# 32. Phase 31 — Performance optimization after profiling

Optimization order:

1. eliminate unnecessary store round trips,
2. eliminate inconsistent repeated scans,
3. reduce allocations/copies where safe,
4. batch writes,
5. reduce watch fan-out,
6. optimize serialization,
7. optimize controller algorithms,
8. optimize scheduler,
9. optimize runtime integration.

Never optimize:

- by removing conflict protection,
- by weakening observed-state semantics,
- by bypassing transactions,
- by caching authoritative truth indefinitely.

Every optimization must include:

- before benchmark,
- after benchmark,
- correctness test,
- race test where relevant.

---

# 33. Phase 32 — Final integration tests

Run the complete matrix:

### Unit
```
go test ./...
```

### Race
```
go test -race ./...
```

### Static
```
go vet ./...
```

### Benchmarks
```
go test -bench=. -benchmem ./...
```

### Fuzz smoke
Run all stable fuzz targets for a bounded duration.

### Integration
- MemoryStore,
- embedded etcd,
- real etcd,
- process runtime,
- containerd/nerdctl runtime where available.

### Distributed
- multi-process server,
- multiple agents,
- node failures,
- controller restarts,
- watch overflow,
- store restart.

### Security
- auth,
- authorization,
- tenant isolation,
- secret handling,
- command injection,
- certificate failure.

---

# 34. Required deliverables

The executing model should create/update:

```
audit/
  repository-map.md
  architecture.md
  state-ownership.md
  invariants.md
  threat-model.md
  performance.md
  scalability.md
  chaos-matrix.md
  findings.md
  test-matrix.md
```

And add regression tests under appropriate packages.

For every P0/P1 fix, record:

```
Finding
Evidence
Root cause
Fix
Regression test
Validation command
Result
```

---

# 35. Recommended execution order

Do NOT work feature-first.

## Gate A — Store

1. StateStore semantics
2. etcd Put race
3. snapshots/revisions
4. transactions
5. watches
6. conformance suite

**Must pass before Gate B.**

## Gate B — Reconciliation

1. ReconcilePlan
2. snapshot correctness
3. dependency preconditions
4. conflict retry
5. deterministic plans
6. ownership enforcement

**Must pass before Gate C.**

## Gate C — Truth model

1. observed-state ownership
2. runtime Start → Status
3. agent lifecycle recovery
4. event projection

**Must pass before Gate D.**

## Gate D — Agent and health

1. split agent responsibilities
2. independent probes
3. init ownership
4. runtime-backed init
5. localhost removal
6. storage/network lifecycle

## Gate E — Security

1. threat model
2. authn
3. authz
4. secrets
5. command execution
6. etcd TLS
7. tenant isolation
8. network policy

## Gate F — Performance

1. baseline
2. profile
3. remove pathological scans
4. optimize store access
5. controller performance
6. scheduler
7. load test

## Gate G — Operations

1. event projector
2. metrics
3. CLI diagnostics
4. logs
5. traces
6. recovery documentation

## Gate H — Release

1. fuzz
2. race
3. chaos
4. dependency audit
5. reproducibility
6. documentation
7. release validation

---

# 36. P0 backlog to start immediately

These are the first implementation tasks.

### P0-01
Fix etcd Put so a failed compare cannot fall back to unconditional overwrite.

### P0-02
Design and implement coherent reconciliation snapshots.

### P0-03
Introduce transactional ReconcilePlan with explicit preconditions.

### P0-04
Protect all facts read by a controller that influence its plan.

### P0-05
Make runtime observation authoritative for Running/Stopped/Failed.

### P0-06
Remove controller writes to authoritative observed lifecycle state.

### P0-07
Make VIP allocation atomic/concurrency-safe.

### P0-08
Add concurrency regression tests for all seven items above.

### P0-09
Create MemoryStore/EtcdStore conformance tests.

### P0-10
Run `go test -race ./...` and fix all races before proceeding.

---

# 37. Definition of done

CCattler should not be considered hardened until all of the following are true:

- [ ] Store semantics are explicitly documented.
- [ ] MemoryStore and EtcdStore pass the same semantic conformance tests.
- [ ] No unsafe Put fallback exists.
- [ ] Reconciliation uses a coherent snapshot.
- [ ] Plans protect the assumptions that produced them.
- [ ] Controller writes have explicit ownership.
- [ ] Observed state is produced from runtime/provider observations.
- [ ] Start success is not treated as proof of Running.
- [ ] Event history comes from committed transitions.
- [ ] Watches are loss-tolerant and resync correctly.
- [ ] Watch cancellation does not leak goroutines/resources.
- [ ] VIP allocation is concurrency-safe.
- [ ] Multiple ports/protocols are modeled explicitly.
- [ ] Probe scheduling is independent from reconciliation.
- [ ] Missing network identity never falls back to localhost.
- [ ] Init has one authoritative phase owner.
- [ ] Init execution semantics are explicit and tested.
- [ ] Agent responsibilities have clear ownership boundaries.
- [ ] Runtime implementations satisfy one conformance suite.
- [ ] Scheduler is deterministic and explainable.
- [ ] Autoscaling is constraint-aware and convergent.
- [ ] Security threat model exists.
- [ ] Authentication and authorization are tested.
- [ ] Secrets do not leak through logs/errors.
- [ ] Command execution paths are hardened.
- [ ] Network policy is tested for isolation.
- [ ] Fuzz targets cover parsers/codecs.
- [ ] Race detector passes.
- [ ] No material goroutine/resource leaks are known.
- [ ] Chaos tests demonstrate convergence.
- [ ] Performance has measured baselines and targets.
- [ ] Load tests cover realistic scale.
- [ ] CI runs correctness/security/static checks.
- [ ] Release artifacts are integrity-verifiable.
- [ ] Documentation matches actual behavior.

---

# 38. Final instruction to the executing model

Treat this as an **engineering investigation**, not a checklist to mark green.

If a test passes but the architecture still permits the failure, the item is not done.

If an implementation is correct only because events happen in a favorable order, the item is not done.

If an operation succeeds but observed state is not independently verified, the item is not done.

If a controller can overwrite state based on a stale snapshot, the item is not done.

If a security control exists in documentation but is not enforced in code and covered by tests, the item is not done.

The desired end state is a small, principled distributed control system whose correctness follows from explicit state ownership, transactional assumptions, observations, and convergence — not from timing, luck, or controller cooperation.

**Priority order: correctness → safety → convergence → security → operability → measured performance → feature expansion.**
