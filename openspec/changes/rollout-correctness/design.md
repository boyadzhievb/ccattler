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
2. `canStop = maxUnavailable − unavailable instances of the new image`. Old instances stopped in step 1 are not counted: they are being replaced. Healthy old instances are stopped up to `canStop`, sorted by instance ID. As today, nothing is stopped when new-image instances exist and none is running.
`pending`, `pulling`, `starting` and `waiting-for-secrets` are not counted as unavailable, which keeps today's pacing. Counting them as Kubernetes does would change surge behaviour (Flow 18) and is out of scope.

### D5 — Rollout bookkeeping
New key `derived/service/{svc}/rollout/target_image`. A state machine per service:

| State before | Condition | Action |
|---|---|---|
| none / `complete` / `rolled-back` | some active instance runs a different image than desired | `rolling`. `previous_image` = the most common old image (ties broken by name). `target_image` = desired. revision+1, history, surge allowance |
| `rolling` / `rollback` | desired ≠ `target_image` (image changed during a rollout) | `rolling`, `target_image` = desired, revision+1, history. `previous_image` is kept (the last known-good image) |
| `rolling` | failures ≥ threshold and `previous_image` ≠ "" | desired image = `previous_image`, `target_image` = `previous_image`, `rollback`, surge deleted |
| `rolling` | no active instance runs a different image | `complete`, delete `previous_image`, `failures`, surge |
| `rollback` | no active instance runs a different image | `rolled-back`, delete `previous_image`, `failures`, surge |

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
