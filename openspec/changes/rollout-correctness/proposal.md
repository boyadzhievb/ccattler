# Proposal

## Why

Milestone **M241 — Rollout Correctness** (bug, milestones.md Phase 241), split from M238 after its rework review. Since M233/M238 a crash-looping instance stays `crash-loop-backoff` instead of becoming `failed`. The rollout controller (`controllers/rollout.go`) rolls back only when 3 new-image instances are `failed`, so a rolling update to a broken image now stalls without rolling back. Reading the code for this milestone found these problems as well:

- **B1:** `observed/instance/{id}/restarts` only grows. A stateful ordinal recreated under the same ID keeps its old count, and an old instance restarted in place keeps counting under the new image. A restart count cannot drive a rollback until it is per image.
- **B2:** `node_failure` makes an instance's effective state `failed`, so losing a node counts as a failure of the new image.
- **B3:** old instances that are crash-looping or failed use up `max_unavailable` instead of being replaced first, so a rollout that fixes them goes slower. `currentUnavailable` counts only `failed` old instances, so a crash-looping one looks available.
- **B5:** the agent restarts an instance in place with the service's *desired* image (`lookupServiceImageFromStore`, agent/agent.go). An old instance that crashes once during a rollout comes back on the new image, which skips `max_unavailable`. If the new image is broken, healthy old instances that crash now and then become broken ones.
- **P1:** `derived/service/{svc}/rollout/previous_image` is never deleted. From the second rollout onward the controller writes no `rolling` state, revision, history entry or surge allowance, and a rollback reverts to the image from before the *first* rollout. The state `rollback` never ends.
- The fixed threshold of 3 failed instances never triggers for a service rolled out one instance at a time. `rollout/failures`, which `cca describe service` reads, is never written. Old instances are stopped in map order, so which one goes first is not deterministic.

How others do it:
- **Kubernetes Deployment** never rolls back on its own. It reports `ProgressDeadlineExceeded` after 10 minutes without progress. A pod keeps the image it was created with (the pod spec cannot change). `cleanupUnhealthyReplicas` scales down unhealthy old pods without using up `maxUnavailable`.
- The **AWS ECS deployment circuit breaker** counts task starts on the new revision that fail. It rolls back to the last completed deployment once the failures reach half the desired count, kept between 3 and 200.
- **Nomad** `auto_revert` writes the last stable job version back as a new version.

## What Changes

Decided with the user 2026-11-18 (all recommendations accepted):

- **Instances keep their image (B5).** When the instance controller creates an instance, it records the service's desired image for it. The agent starts that instance, and restarts it in place, with that image only. Only a rollout moves a service onto a new image, by replacing instances.
- **Restart counts belong to one run of one image (B1).** Creating an instance clears its restart count, including a stateful ordinal recreated under the same ID. Because instances keep their image, the count of a live instance always refers to that image.
- **Rollback on new-image failures, scaled to the service (Q1).** New-image failures are the restarts of new-image instances plus the new-image instances that are `failed`. The rollout rolls back once they reach half the desired count (rounded up), kept between 3 and 200, as the ECS circuit breaker does. The count is written to `rollout/failures`.
- **Losing a node is not an application failure (B2).** An instance on a lost node never counts as a new-image failure. It still counts as unavailable.
- **Unhealthy old instances go first (B3).** Old instances that are `failed`, `crash-loop-backoff`, `image-pull-backoff` or on a lost node are stopped first, without using up `max_unavailable`. Healthy old instances are then stopped within `max_unavailable` minus the instances that are unavailable for these reasons, in instance-ID order.
- **Rollout bookkeeping (P1).** When a rollout completes, `previous_image` is deleted. A rollback still writes the previous image to `desired/service/{svc}/image`, as Nomad does. It moves to `rolled-back` once no instance runs the bad image, and then deletes `previous_image`. Applying a new image during a rollout or rollback starts a new rollout, which keeps the last known-good image as its previous image.

- **A waiting rollout always says why (user, 2026-11-18: "nothing that doesn't tell why it is stuck").** While a rollout is in progress, `rollout/reason` names what it waits for: a new instance that is not running yet (with that instance's state and status reason), the `max_unavailable` budget in use, a pause, or a rollback that the previous image also fails. For an instance that can't be placed, the reason quotes the scheduler's unplaced reason (`insufficient_capacity`) and the instance's requirements.
- **Deadline rolls back, with its reason (user, 2026-11-18).** A rollout that has made no progress for 10 minutes (Kubernetes' default `progressDeadlineSeconds`, overridable per service) is rolled back, as with Nomad's `progress_deadline` and `auto_revert`. Every rollback, by deadline or by failures, records why in `rollout/rollback_reason`, for example `no progress for 10m: web-3 unplaced: insufficient_capacity (cpu 2, memory 1Gi)`. The reason is kept after the rollback until the next rollout starts. A rollback that cannot progress either becomes `stalled` with its reason. It keeps retrying and clears by itself once it can make progress. `cca describe service` shows all of this.
- **Never quietly stuck (user, 2026-11-18).** `cca status` (RUNTIME HEALTH) and the e2e health check list `stalled` and `rolled-back` services with their reason, without the user having to ask about a service.

Out of scope:
- Rolling out config changes progressively (M239).
- Keeping an instance's IP across in-place restarts (M237).

## Capabilities

### New Capabilities
- `service-rollout`: how a change of a service's image is rolled out to its instances. It covers pacing, which instances are stopped first, failure counting, automatic rollback and the rollout state the user sees.

### Modified Capabilities
- `instance-restart`: an in-place restart reuses the instance's own image, and the restart count starts at zero whenever an instance is created. The interim scenario "a crash-looping new image stalls the rollout" is replaced by `service-rollout`'s rollback.

## Impact

- Code: `api/status.go`, `cmd/cca/command_server.go` and `test/e2e/health_check.sh` (stalled and rolled-back services in status), `controllers/rollout.go` (classification, failure count, threshold, unhealthy-first stops, bookkeeping), `controllers/instance.go` (record the instance image and clear restarts when creating an instance), `agent/agent.go` (start from the instance's image, falling back to the service image for instances created before this change), `types/keys.go` (instance image key, rollout target image key).
- Tests: rollout controller unit tests for each scenario, instance controller and agent tests for image pinning and the restart reset, and a testbed check that rolls out a crash-looping image and sees it rolled back.
- Docs: design/runtime-invariant-flows.md (new rolling update and rollback flow next to Flow 18), CONTROLLER_DATA_FLOW_GRAPH.md (rollout section), INSTANCE_LIFECYCLE_GRAPH.md (image lookup), design/etcd-schema.md (new keys).
- DSL: `update { progress_deadline <duration> }` (default 10m), compiled to `desired/service/{svc}/update/progress_deadline`. docs: design/dsl-reference.md.
- API: `cca describe service` shows the failure count, the reason, and the `rolled-back` and `stalled` states.
