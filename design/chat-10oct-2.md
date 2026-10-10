I checked the latest `master` of [`boyadzhievb/ccattler`](https://github.com/boyadzhievb/ccattler). The current head is M82, `83f04cc` — fair-share instance creation.

Verdict: the previous capacity-request and storage-cleanup fixes are now present, but I found a new high-priority cloud load-balancer issue and a remaining stale-route issue. The latest CI run also has a lint failure.

## Findings

### 1. P1 — Cloud load-balancer reconciliation can continuously call the provider

File: [`controllers/cloud_loadbalancer.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/cloud_loadbalancer.go#L69-L125)

M79 changed the controller to emit an `ensure` operation on every reconciliation for every externally exposed service. The controller also watches the derived load-balancer prefix containing its pending operations.

This creates a feedback loop:

1. Reconciliation writes a pending `ensure` intent.
2. The post-commit executor calls `EnsureLoadBalancer`, updates observed state, and deletes the pending intent.
3. Those writes trigger the controller's watch.
4. The next reconciliation emits another `ensure`, even if the desired configuration has not changed.

This can generate sustained cloud API traffic, throttling, and unnecessary load-balancer updates even when the service is healthy. If provider calls fail, the same loop can turn retries into rapid repeated calls.

Fix: calculate a stable hash of the desired LB configuration, including the backend list, ports, and protocol. Emit an intent only when that hash differs from the last successfully applied configuration. Persist the applied hash only after the provider succeeds.

### 2. P1 — A pending route can still be installed for a node that has become unreachable

File: [`controllers/cloud_routes.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/cloud_routes.go#L45-L160)

The route controller correctly excludes non-alive nodes when building its desired route set. However, it only derives cleanup from observed routes; it doesn't cancel a pending `ensure` when that route is no longer desired.

A failure sequence is possible:

- Route creation fails, leaving a pending route intent.
- The target node becomes unreachable before the route is recorded as observed.
- Reconciliation sees no observed route to delete and leaves the pending intent untouched.
- The post-commit executor retries the pending `ensure` without revalidating that the target node is still eligible.

The provider can therefore install a route pointing at a dead node.

Fix: reconcile pending route intents against current desired routes and revalidate the target node immediately before executing `EnsureRoute`. Supersede stale intents with durable delete or cancellation operations. Use revision guards for completion so an older executor cannot overwrite newer intent state.

### 3. P2 — Event-projector recovery can still lose semantic events

File: [`controllers/event_projector.go`](https://github.com/boyadzhievb/ccattler/blob/master/controllers/event_projector.go)

The projector now tracks revisions, reconnects watches, and scans after compaction or overflow. That's an improvement. But `resyncFromScan` only returns a new revision; it doesn't reconstruct or emit the semantic transitions missed before that scan.

As a result, the watch can resume without a permanent stall, while the event log still misses transitions during the recovery window. That matters if consumers rely on the log for audit or debugging history.

Fix: define whether the event log promises complete transition history. If it does, preserve/replay transitions from durable history or use a durable event source rather than treating a current-state scan as recovery of past events.

## What is fixed from the previous review

The current code does contain the M79 fixes:

- Capacity requests in all states count as covered during request creation, avoiding same-cycle writes and deletes to the same request keys. A regression test was added.
- Storage resize and snapshot intents use distinct keys.
- Storage cleanup deletes pending resize/snapshot intents and the last-operation marker; the executor checks that the volume still exists before invoking the provider.
- Orphaned pending load-balancer ensures are canceled when the service is no longer externally exposed and no observed LB exists.

Those fixes are useful, although the load-balancer fix now needs the configuration-hash gating described above.

## CI status for M82

E2E Cluster Test

Passed on `83f04cc`.

[View run](https://github.com/boyadzhievb/ccattler/actions/runs/38075722367)

Cloud Provider Tests, CodeQL, Trivy

All passed on the same commit.

[Cloud tests](https://github.com/boyadzhievb/ccattler/actions/runs/38075722333) · [CodeQL](https://github.com/boyadzhievb/ccattler/actions/runs/38075722278) · [Trivy](https://github.com/boyadzhievb/ccattler/actions/runs/38075722322)

Test workflow — lint failed

`golangci-lint` reports two unused functions following the M82 refactor:

`controllers/instance.go:323` — `countInstanceCreations` `controllers/instance.go:338` — `(*InstanceController).reconcileStatefulService`

Remove the dead functions or use them if they're still needed. The race-enabled test job was still running when I checked, so its final outcome is not yet confirmed.

[View workflow](https://github.com/boyadzhievb/ccattler/actions/runs/38075722255)

## Recommendation

Prioritize stopping the load-balancer ensure loop, then fix stale pending route execution. Clean up the two unused functions so the M82 Test workflow can go green. M82's round-robin creation change addresses service starvation, and the E2E result is encouraging, but the cloud-operation feedback loop is a separate correctness and operational risk.