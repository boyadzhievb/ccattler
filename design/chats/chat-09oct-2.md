I checked the live [`boyadzhievb/ccattler`](https://github.com/boyadzhievb/ccattler) repository, including the current `master` code and the October 8–9, 2026 commits. You're right: I should have based the previous comparison on the implementation itself, not treated CCattler's capacity-management work as something still to be designed from scratch.

The project already has a `ClusterAutoscaleController`, an `InfrastructureProvider` interface, cloud-node lifecycle handling, cloud-route management, transactional change groups, controller write-domain checks, a separate event projector, and an independent agent heartbeat/probe path. The recent M73–M76 work has also addressed several correctness concerns from the earlier review.

The more useful conclusion is this: CCattler already has the beginnings of a Karpenter-like capacity manager. The next step should be to make its capacity decisions durable, constraint-aware, and safely reconcilable—not to add another broad feature layer.

## What I would change in the design

The priorities below are based on the code currently on `master`, not just the original design plan.

### 1. P0 — Fix the controller snapshot and concurrency contract

Relevant code: `controllers/runner.go` · `store/store.go`

This remains the most important architectural concern I found.

The runner has `ScanWithRevision`, optimistic transaction comparisons, and input-key guards. However, `scanFactsForController()` scans each watched prefix separately and discards each scan's revision. Later, `appendInputKeyGuards()` only guards scanned facts in the controller's output domain, intentionally excluding facts from read-only input domains.

That allows a controller to make a decision using inputs that change before its write commits.

For example, the endpoint controller reads an instance's observed state and readiness, then writes an endpoint. If readiness changes to not-ready between the read and transaction, the endpoint write may still commit based on stale inputs. The periodic resync can eventually correct it, but traffic could be directed to an instance that should no longer be eligible.

My recommendation is to stabilize the concurrency contract before adding more controllers.

* Separate controller dependencies into `Reads`, `Watches`, and `Writes`. A watch tells the runner what should trigger reconciliation; it is not by itself a safe read-set declaration.

* Provide a coherent snapshot across all prefixes a controller reads, rather than combining scans performed at different revisions.

* Ensure the transaction detects changes to relevant inputs, including changes where a previously absent fact is created or a fact disappears. Revision guards on existing keys alone cannot detect all such changes.

* Use bounded, explicitly versioned read domains or generation keys where guarding every individual input would exceed etcd's transaction limit. Preserve isolation between unrelated domains so agents and controllers don't create excessive conflicts.

Do not simply compare the store's global revision on every reconciliation: high-frequency agent observations could cause unnecessary conflicts across the whole system. The goal is to guard the information a decision actually depends on, with a clear and testable correctness contract.

### 2. P0 — Repair the storage operation lifecycle

Relevant code: `controllers/storage.go` · `controllers/runner.go` · `design/m76-correctness-iv.md`

M76 made a good move by putting resize and snapshot operations behind durable pending-operation facts and executing them after a successful commit. But the current implementation still has a correctness gap.

In `reconcileVolumeResize()`, CCattler writes both a pending resize operation and `observed/volume/.../size` with the desired target size in the same change set. That means the observed size can claim the resize succeeded before the provider has done so.

There is also a retry problem: `ExecutePostCommitOperations()` runs after a successful transaction, but the runner returns early when reconciliation produces no changes. If a provider resize fails, the pending operation remains, while the stored observed size already equals the target. On the next pass, reconciliation may produce no changes and therefore never invoke the post-commit executor again.

I would make the operation executor a separate, continuously retryable component:

1. The controller writes a deterministic operation intent, without changing observed provider state.

2. The executor claims and runs the operation using a stable idempotency key.

3. The executor records success or failure and retry metadata.

4. The provider is queried, or otherwise confirms its resulting state.

5. Only then does CCattler publish the observed size or other actual state.

The important distinction is that desired size, operation status, and observed size are three different facts. Keep them distinct in the store and in the CLI.

Apply the same pattern to snapshots, volume migration, and other provider operations. Do not rely on a successful unrelated reconciliation to retry pending work.

### 3. P0 — Close the authorization gaps

Relevant code: `security/authorized_store.go` · `security/abac.go`

I found a concrete policy-enforcement gap in `AuthorizedStore.Transaction()`.

The wrapper authorizes the comparison keys and the `onSuccess` operations, but it does not authorize operations in `onFailure` before forwarding both branches to the underlying store. The interface permits writes in that failure branch, so the wrapper must not assume it is empty or harmless.

Authorize every possible mutation branch before the transaction executes. Add a regression test in which the comparison fails and the failure branch contains a write the principal is not allowed to perform; the transaction must be rejected and the key must remain unchanged.

There is a second semantic question worth settling now: `CombinedAuthorizer` grants access when either RBAC or ABAC allows the operation. If ABAC is meant to impose a mandatory constraint—for example, a production change gate—an RBAC grant can bypass that constraint. Decide explicitly whether ABAC supplies an alternative grant or a mandatory policy guard. If it is a guard, implement deny-overrides or equivalent semantics and test the combination.

Finally, `enforceWriteDomain()` in the controller runner drops invalid changes individually. That may leave the valid remainder of a multi-key `Change.Group` and commit only part of what the controller intended to be atomic. For built-in controllers, a write-domain violation should reject the affected atomic group—or the entire plan—rather than silently filtering out one member.

### 4. P1 — Make capacity management a real state machine

Relevant code: `controllers/clusterscale.go` · `infra/infra.go`

This is where the Karpenter comparison is most useful.

CCattler already has the beginnings of a node autoscaler, but the current `ClusterAutoscaleController` has a much simpler contract than a full capacity manager:

* It invokes `RequestNode()` and `RemoveNode()` directly inside `Reconcile()`, rather than first committing an intent.

* It counts pending instances without placements, but does not distinguish capacity shortages from unsatisfiable placement constraints.

* It ignores errors from `NodeCount()`.

* It ignores the node ID returned by `RequestNode()`, and there is no durable CCattler-side record of an outstanding capacity request in this controller.

* Node removal is delegated to the provider's `RemoveNode()` contract, rather than represented as a lifecycle CCattler can inspect and explain through facts.

Those are architectural limitations, not reasons to throw away the current implementation.

Karpenter's useful ideas are its separation of capacity requests from the cloud API call, its provisioning lifecycle, and its constraint-driven node selection. Its NodeClaims track the progression through launch, registration, and initialization; NodePools constrain capacity choices and disruption behavior. 【turn841663search2】【turn841663search3】 Karpenter also distinguishes unschedulable workload demand from ordinary pending state and handles node disruption as a managed operation. 【turn841663search6】【turn841663search5】

I'd adapt those ideas without adopting Kubernetes' user-facing object model.

## Proposed CCattler capacity flow

Placement decision

Placed, waiting, or unschedulable—with a reason

Capacity policy

Can extra capacity actually satisfy the request?

Durable capacity request

Requirements, stable ID, limits, status, retry information

Infrastructure executor

Launches or terminates capacity idempotently

Agent and provider observations

Registration, initialization, available resources, actual state

Use internal state such as:

```
capacity/request/<id>/requirements
capacity/request/<id>/provider
capacity/request/<id>/state
capacity/request/<id>/reason
capacity/request/<id>/created_at
capacity/request/<id>/last_attempt
```

The state progression might be `pending → requested → launching → registered → initialized → ready`, with explicit failed and terminating paths.

The exact key structure can be finalized later; the important part is that a provider request is a durable, inspectable operation rather than a call hidden inside reconciliation.

Before requesting a node, CCattler should know why placement failed. If the workload requires an unsupported architecture or violates a placement constraint, launching more generic nodes is not a remedy. The scheduler should emit structured unsatisfied-demand facts, including the reason and requirements. Capacity management should act only when a feasible capacity choice can resolve the shortage.

Then extend `InfrastructureProvider` to accept a typed capacity request—resources, architecture, zone, capacity class, and other relevant constraints—instead of only `RequestNode(ctx)`. Start with the smallest useful set of fields rather than designing a universal cloud API.

For scale-down, add an explicit disruption plan: re-evaluate placement feasibility, check constraints and storage implications, stop new placements onto the candidate node, drain it, request deprovisioning, and observe completion. Later, support underutilized-node consolidation, time-based disruption limits, and cost-aware replacement. Karpenter's NodePool disruption budgets and consolidation behavior are useful references for that later stage. 【turn841663search3】【turn841663search5】

### 5. P1 — Make event projection recoverable, not just reconnectable

Relevant code: `controllers/event_projector.go` · `store/store.go`

M76 addresses the problem of one closed watch stopping the projector. That is good progress, but reconnection alone doesn't guarantee complete event history.

The projector creates watches without a `StartRevision`; after reconnecting, it subscribes only to future events. It also forwards overflow and compaction notifications into the merged channel, but its classification logic does not turn those notifications into a recovery action. Events missed during the gap can therefore be permanently absent from the semantic event log.

This matters more for events than for ordinary reconciliation: a periodic state resync can repair current state, but cannot reconstruct every transition that happened while a watch was disconnected.

I recommend storing a projection checkpoint, resuming from a revision where possible, and handling overflow or compaction with an explicit recovery strategy. Define whether semantic events are best-effort or durable. If durability matters, project state transitions with stable event identity—such as revision plus key and event type—and make event writes idempotent. Add fault-injection tests for overflow, compaction, restart, and event-log write failure.

### 6. P1 — Move cloud route changes behind committed intent

Relevant code: `controllers/cloud_routes.go`

The route controller calls `EnsureRoute()` and `DeleteRoute()` during `Reconcile()` and then proposes changes to record the result. That makes external side effects happen before the store transaction commits.

Even if the operations are idempotent in a particular cloud provider, the controller's store view can lag the provider after a CAS conflict or a process failure. The cleaner design is the same intent/executor boundary used for storage: commit an operation intent, execute it with a stable ID, then publish confirmed provider state. This also makes route failures and retries visible through the regular CCattler state and diagnostic tools.

## What I would deliberately not redesign

Several previous concerns have been addressed in the current code, so I would not spend the next milestone repeating them:

* Runtime truth: the agent now calls `observeInstanceState()` after `runtime.Start()` succeeds, instead of equating a successful start request with an observed running workload. Source

* Heartbeat ownership: the heartbeat loop now writes timestamps; node-state transitions belong to the controller, and startup publication has CAS guards. Source

* Probe scheduling: probes run through a separate `ProbeScheduler`, not solely as a side effect of the main agent reconciliation cycle. Source

* Multi-key change groups: the runner has group-aware budgeting so it does not intentionally split a grouped operation just to fit a transaction. This should be complemented by rejecting write-domain violations atomically.

* VIP allocation and event-projector reconnect: these have received recent fixes; I would keep testing their failure paths, rather than assume the earlier versions of those problems still exist.

## A practical next-milestone order

| Milestone                                    | Main goal                                                                                | Acceptance test                                                                                                      |
| -------------------------------------------- | ---------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| M77 — Safety boundaries                      | Fix authorization for both transaction branches; reject invalid atomic changes as a unit | Unauthorized failure-branch writes and partial grouped commits are impossible                                        |
| M78 — Snapshot correctness                   | Coherent multi-prefix snapshots and read-dependency guards                               | Concurrent readiness, desired-state, insertion, and deletion races never commit stale derived state                  |
| M79 — Storage executor                       | Reliable retries and truthful observed state                                             | Failed resize retries even when reconciliation has no new changes; observed size changes only after confirmation     |
| M80 — Capacity requests                      | Durable, constraint-aware infrastructure lifecycle                                       | Repeated reconciliations don't create duplicate requests; impossible placement does not trigger endless provisioning |
| M81 — Provider operations and event recovery | Intent/executor for cloud routes; revision-aware event projection                        | Provider actions recover after interruption, and projector recovers across overflow/compaction                       |

After those milestones, I'd prioritize capacity consolidation and cost-aware placement rather than expanding the number of controllers.

My overall assessment: CCattler's differentiator is still the right one—facts, policies, desired state, reconciliation, and observations rather than exposing implementation objects to users. The implementation already contains substantial functionality. The greatest improvement now is to make the state and side-effect contracts strict enough that concurrent writes, provider retries, and controller restarts cannot make CCattler claim that something happened when it did not. Keep the user-facing DSL high-level; make capacity requests, lifecycle phases, operation IDs, and explanations internal facts exposed through `cca describe`, `cca events`, and related diagnostic commands.

Sources for the Karpenter comparison: [NodeClaims and capacity lifecycle](https://karpenter.sh/docs/concepts/nodeclaims/?utm_source=chatgpt.com) 【turn841663search2】, [NodePool constraints and disruption controls](https://karpenter.sh/docs/concepts/nodepools/?utm_source=chatgpt.com) 【turn841663search3】, [Karpenter's provisioning model](https://karpenter.sh/docs/?utm_source=chatgpt.com) 【turn841663search6】.
