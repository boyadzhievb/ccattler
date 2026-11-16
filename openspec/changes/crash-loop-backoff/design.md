# Design

## Context

- Exited-container path today (`agent/agent.go`, `executeReconciliationCycle`): `!exists || !runtimeStatus.Running` → `publishExitReasonFromStatus` (if exit code ≠ 0) → `reconcileDesiredInstance`, every cycle. Containerd's `Start` on an exited container calls `Stop` first (`runtime/containerd.go`), so each restart is a new container, a new network namespace and, through CNI `host-local`, a new IP.
- Existing back-off state in the agent: `imagePullBackoffs map[string]*imagePullBackoffEntry` (attempts, retryAfter, lastError) with the formula `base * 2^(attempts-1)` capped; the failure controller has the same formula in `calculateCrashLoopBackoff` (base 10 s, max 5 min).
- Instance states are a typed enum in `types/state.go`; `image-pull-backoff` is the precedent for an agent-side waiting state.
- New flow: **Flow 33: In-Place Restart With Back-off (M233)** in design/runtime-invariant-flows.md (Flow 32 is taken by M230's branch). Related: Flow 16 (config reload).

## Goals / Non-Goals

**Goals:** back-off between in-place restarts, a real restart count, a visible waiting state, no replacement by the failure controller, one back-off formula.

**Non-Goals:** keeping the IP across restarts (M237); rescheduling to another node; previous-run logs (M234); a per-service restart policy in the DSL (`restart never|on-failure`) — not asked for.

## Decisions

### D1. Per-instance back-off state in agent memory
`crashLoopBackoffs map[string]*crashLoopBackoffEntry` on the agent (attempts, retryAfter, lastStartedAt, lastExitReason), like `imagePullBackoffs`. On an exit: if the previous start is older than the reset period (10 min), attempts restart at 0; then attempts++, retryAfter = now + delay(attempts). Before retryAfter the cycle only publishes state; at or after it, `reconcileDesiredInstance` runs. The entry is removed when the instance is no longer desired on the node. An agent restart loses the map: an exited container the agent has no entry for is restarted once at once (it gets an entry on that start), then backs off from the base delay — safe, and the store count survives.

### D2. Count in the store, incremented per restart attempt
When the agent starts an exited instance again (not its first start), it increments `observed/instance/{id}/restarts` (read, +1, write; the agent is the only writer for its instances). First start writes nothing, so `cca describe` keeps showing an empty count for never-restarted instances as today.

### D3. New state `crash-loop-backoff`
`types.InstanceCrashLoopBackoff InstanceState = "crash-loop-backoff"`, published with status reason `exited with <reason>; restarting in <wait>` while waiting. During apply, every switch or comparison on instance states (controllers, api status counts, scheduler, endpoints, rollout, CLI colouring) is checked so the new state counts as "not running, not failed".

### D4. One back-off helper
`backoff.Delay(base, max time.Duration, attempt int) time.Duration` in a new small package `backoff/` (attempt 1 → base; doubles; capped at max; attempt < 1 → base), used by the image pull back-off, the failure controller and this change. Existing delays stay identical (unit test on the formula).

### D5. A config change skips the wait
If the service's config hash (Flow 16) differs from the stored `config-hash` of the instance while it waits, the back-off is reset and the instance starts on this cycle: a user who fixes the env or a secret should not wait up to 5 min. Test: `TestAgentRestartsAtOnceWhenConfigChangesDuringBackoff`.

### D6. Testable delays
The agent keeps the base, maximum and reset durations in unexported fields set from named constants in `New`; agent tests in the same package set short values. No new exported setter.

## Risks / Trade-offs

Failure cases and what happens:
- **Agent restarts during a back-off**: in-memory state is lost; the instance is restarted once without waiting, then backs off from the base delay. Count continues from the store.
- **Instance removed or moved during a back-off**: the reconcile loop no longer sees it as desired; its entry is deleted with the stale-instance cleanup.
- **Container exits with code 0** (a one-shot process in a service): treated as an exit like any other — services are meant to keep running; jobs have their own path (M212).
- **Store write of the count fails**: logged; the restart still happens; the count may undercount by one.
- **IP churn** (accepted): each restart gets a new IP; the testbed check verifies old IPs are released and no stale backend or identity remains (task 3.2); a failure there is its own bug milestone.
