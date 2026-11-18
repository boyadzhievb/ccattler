# Design

## Context

The rollout controller (`controllers/rollout.go`, CONTROLLER_DATA_FLOW_GRAPH.md § rollout) compares each service's desired image with `observed/instance/{id}/image`. It stops old-image instances by writing `observed/instance/{id}/state = stopped`, and the instance controller then creates replacements. It rolls back when 3 new-image instances are `failed`. The agent picks the image for every start, in-place restarts included, from `desired/service/{svc}/image` (`reconcileDesiredInstance` → `lookupServiceImageFromStore`, agent/agent.go). Rollout tracking starts only when `rollout/previous_image` is empty, and that key is never deleted. The proposal lists the defects; the user decided the approach on 2026-11-18.

Flows: this change adds **Flow 34: Rolling Update and Rollback** to design/runtime-invariant-flows.md and links Flow 18 (max_extra) and Flow 33 (in-place restart) to it.

## Goals / Non-Goals

**Goals:** roll back a crash-looping new image for services of any size, keep each instance on its own image, replace unhealthy old instances first, keep the rollout state correct across repeated rollouts, and make stop order deterministic.

A waiting rollout always says why, a rollout without progress is rolled back, and every rollback records its reason.

**Non-Goals:** progressive config rollout (M239), keeping IPs (M237).

## Decisions

### D1 — The instance controller records each instance's image when it creates it
New key `derived/instance/{id}/image`, written by the instance controller in the same change group that creates the instance (both the stateless path and the stateful-ordinal path, controllers/instance.go). Its value is `desired/service/{svc}/image` at that moment. The agent's start path reads it first. When it is absent, the agent uses the service image, which covers instances created by an older server, and logs that it fell back once per instance. instance-gc already deletes `derived/instance/{id}/*`.
- Why the controller and not the agent: a stateful ordinal recreated under the same ID must get the *current* image. Only the creator knows that a new life of the ID begins. This matches Kubernetes, where the image is part of the pod spec and fixed when the pod is created.
- Rejected: pinning to `observed/instance/{id}/image`. That key survives an ordinal recreation until instance-gc runs, so a recreated ordinal could come back on the old image.

### D2 — Creating an instance deletes its restart count
In the same creation group, the instance controller deletes `observed/instance/{id}/restarts`. With D1, a live instance never changes image, so its count is always "restarts on this image". No agent change is needed for B1.

### D3 — Counting failures and the threshold
New-image instances are the active instances whose observed image equals the desired image, or is still empty. For them:
`failures = Σ restarts + count(state == failed)`. Instances with `node_failure` are skipped, which uses the raw `node_failure` flag rather than the effective state. `threshold = clamp(ceil(desired/2), 3, 200)`, as with the ECS circuit breaker. `desired` is `desired/service/{svc}/instances`, or 1 when missing. The controller writes `rollout/failures` only while the state is `rolling` and only when the value differs from the stored fact. `cca describe service` already reads that key.

### D4 — Unavailability and stop order
`unavailable(instance)` is true when the state is `failed`, `crash-loop-backoff` or `image-pull-backoff`, or `node_failure` is set, for an instance of any image. Each pass works like this:
1. Every unavailable old instance is stopped. This uses no budget.
2. `canStop = maxUnavailable − unavailable instances of the new image`. Old instances stopped in step 1 are not counted: they are being replaced. Healthy old instances are stopped up to `canStop`, sorted by instance ID. Nothing is stopped when new-image instances exist and none is healthy (D10); before D10 this said "running".
`pending`, `pulling`, `starting` and `waiting-for-secrets` are not counted as unavailable, which keeps today's pacing. Counting them as Kubernetes does would change surge behaviour (Flow 18) and is out of scope.

### D5 — Rollout bookkeeping
New key `derived/service/{svc}/rollout/target_image`. A state machine per service:

| State before | Condition | Action |
|---|---|---|
| none / `complete` / `rolled-back` | some active instance runs a different image than desired | `rolling`. `previous_image` = the most common old image (ties broken by name). `target_image` = desired. revision+1, history, surge allowance |
| `rolling` / `rollback` | desired ≠ `target_image` (image changed during a rollout) | `rolling`, `target_image` = desired, revision+1, history. `previous_image` is kept (the last known-good image) |
| `rolling` | failures ≥ threshold and `previous_image` ≠ "" | desired image = `previous_image`, `target_image` = `previous_image`, `rollback`, surge deleted |
| `rolling` | finished (below) | `complete`, delete `previous_image`, `failures`, surge |
| `rollback` | finished (below) | `rolled-back`, delete `previous_image`, `failures`, surge |

**Finished** (amended 2026-11-18 during implementation, task 2.3): no old instance is left, every new instance is running, and at least the expected count is running (`effective/service/{svc}/instances`, else `desired`). The literal rule "no instance runs a different image" ended a one-instance rollout as soon as its old instance was stopped, before the crash loop could be counted. It would also have ended a rollback with its replacement still unplaced, so the rollback could never become `stalled`.
Amended again 2026-11-18 (D10, testbed task 4.1): "running" here means healthy, so finishing needs every new instance healthy and at least the expected count healthy.

Tracking starts from the state, not from an empty `previous_image`, so a stale `previous_image` left by an older server is simply overwritten at the next rollout. No migration is needed. The rollback writes `desired/service/{svc}/image`, like Nomad's `auto_revert`. Applying the configuration again starts a new rollout, which is the user's explicit intent.

### D7 — Reason and progress deadline (user, 2026-11-18)
- `rollout/reason` is plain text, built deterministically each pass and written only when it changes. It is the first matching of: paused; rolling back while the previous image also fails (`N failures on <image>`); waiting for new-image instance `<id>` (`<state>: <status reason>`, the lowest ID among those not running); `max_unavailable` used up by `<ids>`. Status reasons come from `observed/instance/{id}/status_reason`, which is already watched under `observed/instance/`. Reasons are cut to a named maximum length.
- `rollout/progress_at` holds an RFC 3339 timestamp, set when tracking starts and whenever a pass stops an old instance or sees more running new-image instances than the stored `rollout/running_new` count, which is also written. The controller gets a `Now func() time.Time` field, set to `time.Now` by `NewRolloutController`, as `NodeFailureController.Now` and `FailureController.NowFunc` do, so tests can set the time.
- For an unplaced new-image instance the reason quotes the scheduler's `derived/scheduler/unplaced/{id}/reason` (`insufficient_capacity`, `no_nodes`, …) and its `requirements`. `derived/scheduler/` is added to Watch.
- **Deadline → rollback (user, 2026-11-18):** in `rolling` and not paused, once now − `progress_at` > deadline, the controller rolls back exactly as on failures (D5 row 3) and writes `rollout/rollback_reason` = `no progress for <deadline>: <current reason>`. A failure-threshold rollback writes `<n> failures ≥ <threshold> on <image>: <exit reason of the failing instance with most restarts>`. `rollback_reason` survives `rolled-back`/`complete` and is deleted when the next rollout starts, so the user can read it after the fact. A warning with the same text is logged once.
- In `rollback`, a deadline without progress moves the state to `stalled` (reason kept, warning logged once). No second rollback. Progress or completion leaves `stalled`.
- Deadline: `desired/service/{svc}/update/progress_deadline` (DSL `update { progress_deadline 15m }`), default `rolloutDefaultProgressDeadline` = 10 min. An invalid value is refused by the parser, never read as 0. A paused rollout does not time out; resuming restarts the deadline.
- Compared with others: Kubernetes only reports `ProgressDeadlineExceeded` and never rolls back, and ECS rolls back only on task failures. Nomad's `progress_deadline` with `auto_revert` does revert on the deadline, which is the model here, plus the recorded reason. Trade-off: on a capacity stall the rollback may not free room either (an old-image replacement needs the same resources). It then becomes `stalled`, saying so.

### D8 — Stalled and rolled-back services are visible in status (user, 2026-11-18)
The status API's service entries (api/status.go) gain `rollout_state`, `rollout_reason` and `rollout_rollback_reason`. `renderRuntimeHealthSummary` (cmd/cca/command_server.go) adds a `Rollouts:` line per `stalled` or `rolled-back` service, with the reason cut to the terminal line. test/e2e/health_check.sh Check 6 warns for them, as it does for crash-loop-backoff instances. A `stalled` service keeps retrying: progress or new capacity (the cluster-scale controller already asks for a node on `insufficient_capacity`) lets the rollback finish, so the state clears without user action. If it can't clear, the user sees it in `cca status`.

### D9 — Applying a rolled-back image warns (user, 2026-11-18)
Every rollback group also writes `rollout/rolled_back_image` (the bad target image) and `rollout/rolled_back_at` (RFC 3339, from the controller clock). They are deleted with `rollback_reason` when the next rollout starts. The apply handler (api/server.go, both apply paths, next to the M217 instance-count warnings) compares each compiled `desired/service/{svc}/image` with the stored `rolled_back_image` and appends a warning to the existing `applyResponse.Warnings`. `cca apply` already prints those warnings. It only warns, because a fixed image may reuse the tag. This keeps a scheduled CI apply from rolling a broken image out and back silently: each attempt says why the last one was rolled back.

### D10 — A new instance counts once it stays healthy (user, 2026-11-18)
Found by testbed task 4.1: rolling `m241roll` from whoami to `mariadb:11` (exits with code 1 about 1–2 s after start, no password set) logged `rollout started` at 10:15:29 and `rollout finished state=complete` at 10:15:52. The new instance reported `running` once, the old instance was stopped (D4), `isFinished` held (controllers/rollout.go, checked before the failure count), and the instance then crash-looped 41 times under a `complete` rollout.
- New key `observed/instance/{id}/started_at` (RFC 3339), written by the agent each time it starts the container, in-place restarts included, in the same write that publishes `running`.
- **Healthy** new instance: state `running`, `now − started_at ≥ min_healthy_time`, and, when the service has a readiness probe (`desired/service/{svc}/probe/readiness/*`), `observed/instance/{id}/probe/readiness` = `ready`. A restart sets a new `started_at`, so the window starts again.
- "Running" in D4 (healthy old instances are stopped only while a new one is healthy), D5 Finished, and D7 progress (`rollout/running_new` counts healthy new instances) all become "healthy". Failure counting (D3) is unchanged.
- `min_healthy_time`: `desired/service/{svc}/update/min_healthy_time` (DSL `update { min_healthy_time 30s }`), default `rolloutDefaultMinHealthyTime` = 10 s; the parser refuses an invalid or negative value (0 is allowed and means "running is enough"). The rollout controller needs a pass after the window passes even without a new fact, which the periodic pass used by the progress deadline already gives; the reason says `waiting for <id> to stay healthy (<remaining>)`.
- Missing `started_at` (agent older than the server): a running instance counts as healthy, today's behaviour, so a mixed-version upgrade does not block rollouts.
- Clocks: the agent stamps and the controller compares, as with Kubernetes' `minReadySeconds` (kubelet condition time vs controller-manager). Skew of a few seconds only shifts the window; the cluster already relies on NTP for leases.
- Compared with others: Nomad `min_healthy_time` default 10 s (the model), Kubernetes `minReadySeconds` default 0 (same gap as before D10 unless a readiness probe is set), ECS needs health checks with its circuit breaker.

### D11 — max_unavailable counts unavailable capacity (user, 2026-11-18, review 4.2 D-b)
Before, stopped old instances and new instances not yet healthy were not counted, so once one new instance was healthy each pass stopped one more old instance and a service of N lost about N−1 within seconds (pre-existing, contradicts the spec). Now, as Kubernetes' Deployment controller and Nomad's `max_parallel`: `unavailable = expected − healthy instances of any image` (healthy old = running and not unavailable per D4; healthy new per D10; `expected` as in D5 Finished). Healthy old instances may be stopped up to `max_unavailable − unavailable`, never below 0, in ID order. Unavailable old instances (D4 step 1) are still stopped without budget. A stop already written (observed state `stopped`) is not proposed again and is not progress (review B2); old instances on a lost node are left to the failure controller.

### D12 — Kubernetes defaults when update {} leaves them unset (user, 2026-11-18, review 4.2 D-a)
Unset `max_unavailable` = floor(25 % of expected), unset `max_extra` = ceil(25 % of expected), each defaulted on its own; if both end up 0, `max_unavailable` = 1 (Kubernetes refuses that combination; here it would deadlock). A 1-instance service therefore surges: new instance first, old stopped once the new one is healthy. Explicit values keep their meaning (an explicit `max_unavailable 1` on a 1-instance service accepts the outage). Replaces `rolloutDefaultMaxUnavailable` = 1. The disruption-budget cap still applies. `cca describe service` shows the effective values and whether they are defaults.

### D13 — Failures count only this rollout's instances; re-applying the previous image is a revert (review 4.2 B3)
New-image failures (D3) count only new-image instances created at or after the rollout started (new key `rollout/started_at`, RFC 3339, set with `progress_at` when tracking starts, deleted at finish; instance `created_at` compared). Instances that already ran the target image before the rollout do not add their old restarts. When the desired image changes to the stored `previous_image` during `rolling`, the new rollout has no `previous_image` (nothing to roll back to) and so can never roll back; it finishes as `complete`.
Implementation notes (2c, accepted by the main session): `created-at` is the agent's clock and `started_at` the controller's, so instances created up to 30 s before `started_at` still count (skew allowance; otherwise the first new instance could be missed). `started_at` is also reset when a rollback starts, so "previous image also fails" counts only the rollback's own replacements. An instance without `created-at`, or a rollout without `started_at` (older server), counts as before. Recreating an ID deletes `restarts`, `created-at`, `started_at`, `last_exit` and `probe/*` (shared helper used by the instance and failure controllers). New agent key `observed/instance/{id}/last_exit`, kept across restarts, gives the rollback reason the real exit. An `update` block that sets only one of `max_unavailable` / `max_extra` gets the 25 % default for the other (as Kubernetes).

### D14 — Defaults per workload kind; a rollback can always move; surge only while old instances remain (review 4.2 round 2)
Following D12's intent (Kubernetes' defaults), which differ by kind:
- **Stateful** (like StatefulSet) and **per-node** (like DaemonSet, where one instance per node leaves no room for a surge) services default to `max_unavailable 1`, `max_extra 0`, and never surge: an explicit `max_extra` on them is ignored and `cca apply` warns. Before, a 1-instance stateful service surged an extra ordinal (`db-1`, a second member) and a per-node service's surge instance could never be placed, so the rollout never finished and was rolled back by the deadline.
- **Rollback and stalled:** the effective `max_unavailable` is at least 1 and the surge allowance is not used, so a rollback can always replace instances (before: defaults 0/1 plus the deleted surge left a 3-instance rollback with budget 0 forever).
- **Surge only while old instances remain** (like a Deployment, whose new ReplicaSet never exceeds the replica count): the instance controller adds the surge allowance only while old-image instances exist, so the rollout no longer creates one surplus new instance at the end and then scales a random one down. The rollout deletes `surge_allowance` when the last old instance is stopped.
- The budget reason says when the budget is 0 by configuration (`max_unavailable 0: waiting for an extra instance to become healthy`) instead of "0 of N not healthy ()"; describe says when max_unavailable was forced to 1.
- A retarget after a revert (no `previous_image`) uses the current target as the known-good image, never `mostCommonImage`, which could be the bad one.
- Removing a field from `update {}` does not restore its default: `cca apply` never deletes a stored service fact (only gate facts), so the stored value stays until the service is deleted and applied again, as do the implicit 1/1 values older servers compiled for every `update` block. Pre-existing for every field; split to M244 (apply never removes fields deleted from a configuration). Documented in dsl-reference.md.

### D6 — Determinism
Services are processed in sorted name order, and instances are sorted by ID before classification. The proposed changes are then the same for the same facts (CLAUDE.md: deterministic iteration).

## Risks / Trade-offs

- **The failure count lives only in current facts.** A `failed` new-image instance that the failure controller replaces drops out of the count. Crash loops, now the common case, stay on the same instance and keep counting. Failed starts that keep getting replaced still reach the threshold slowly, because each replacement is counted while it is `failed`. Accepted for now. A cumulative counter would need idempotent bookkeeping and is out of scope.
- **The image pin means an instance on a bad image stays on it** until a rollout replaces it. That is the intent: only the rollout moves images.
- **Rollback during a paused rollout:** failures are still counted and a rollback still happens. Pausing stops only the pacing of old-instance stops, as today.

## Failure cases

| Case | What happens |
|---|---|
| New image crash-loops (1 instance, max_unavailable 1) | The new instance restarts. Failures reach 3 after the third restart, which takes about 70 s with back-off. Desired reverts to the previous image, the bad instance becomes "old" and unavailable and is stopped first, a replacement starts on the previous image, and the state becomes `rolled-back`. |
| New image runs ~2 s, then exits (1 instance, `update` unset) | Defaults (D12) give max_unavailable 0, max_extra 1: the new instance starts beside the old one, is never healthy (D10), restarts reach 3, the rollout rolls back. No outage. With an explicit `max_unavailable 1` and no `max_extra`, the old instance is stopped at once and the service is down until the rollback's replacement is healthy (testbed 4.1 rerun: about 40 s); that is the configured trade-off. |
| Agent older than the server (no `started_at`) | A running new instance counts as healthy at once, as before D10. |
| New image crash-loops (10 instances) | One old instance is stopped and one new instance created. It crash-loops, so no healthy new instance exists and no further old instance is stopped. Its restarts reach 5, then the rollout rolls back. |
| Node lost during a rollout | Instances on it are unavailable (budget) and are not failures, so no rollback. The node-failure controller replaces them. |
| Old instance crash-loops during a rollout | It stays on its old image (D1), is stopped first without budget (D4), and is replaced by a new-image instance. |
| Previous image also broken after a rollback | No second rollback during `rollback` (D5). The service crash-loops on the old image and stays in `rollback` until the user applies a fix. |
| Image changed during a rollout | New target, previous stays the last completed image (D5 row 2). |
| Server upgraded with a stale `previous_image` | Overwritten when the next rollout starts (D5). |
| Agent upgraded before the server | No `derived/instance/{id}/image` exists, so the agent falls back to the service image, as today. |
| Server upgraded before the agent | The old agent ignores the pin and keeps today's behaviour until it is upgraded. |
| New image cannot be pulled | Rollout waits. The reason names the instance and the pull error. After the deadline the rollout rolls back, and `rollback_reason` says why. |
| No node can place the new instance | The reason names the instance, `insufficient_capacity` and its requirements. After the deadline the rollout rolls back with that reason. If the old image cannot be placed either, the state becomes `stalled` with the same reason. |
| Server leader changes during a rollout | `progress_at` is in the store, so the new leader keeps counting from it. |
| Store write of the rollback fails | The controller proposes it again in the next cycle. The change group writes the desired image, target and state together. |

## Migration Plan

No data migration. Instances created after the server upgrade get a pin, and older ones fall back. Rollback is a plain image revert of the binaries: the new keys are ignored by older code.

## Open Questions

None. All decisions were taken 2026-11-18.
