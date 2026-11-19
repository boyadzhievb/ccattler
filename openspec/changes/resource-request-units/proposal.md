# Proposal: Resource requests are read with their units (M243)

Implements **M243 — Scheduler Ignores Resource Requests** (milestones.md, Phase 243).

## Why

The compiler stores resource requests exactly as written (`500m`, `64Gi`), but the scheduler reads them with
`strconv.ParseInt` and drops the error, so every request with a unit is 0. Nodes are overcommitted. On the deploy
testbed a `memory 64Gi` service went onto a 3 GiB node, and on a fresh docs-test host three `memory 512Mi` instances
went onto a node with 1167 MiB while `cca status` still showed all memory free. Because of this,
`insufficient_capacity` is never written, cluster scaling never asks for a node, and M241's capacity rollback can never
fire.

Six separate parsers read the same values, and they disagree. A bare `cpu 2` means 2 millicores to the scheduler and
2 cores to the agent. A bare `memory 2048` means 2048 MiB to the scheduler and 2048 bytes to the agent. The eviction
controller compares request bytes against node MiB. The vertical autoscaler reads its `min`/`max` bounds with `Atoi`,
so `max 8Gi` is 0, and it writes bare integers that the agent then misreads.

## What Changes

- **One parser:** a shared quantity parser in `types/` for CPU (to millicores) and memory (to bytes), in Kubernetes
  notation:
  - CPU: `250m`, `0.5`, `2`.
  - Memory: `Ki Mi Gi Ti Pi Ei`, `k M G T P E`, or plain bytes.

  Any other value is an error.
- **BREAKING (bare numbers):** a bare CPU number now means cores everywhere (`cpu 2` = 2000m), and a bare memory
  number means bytes everywhere (`memory 2048` = 2 KiB), as in Kubernetes. The scheduler read both as millicores or
  MiB. Configs and fixtures that use bare values are updated in this change: `design/storage.md` (`cpu 200`,
  `memory 134217728`), `lang/benchmark_test.go`, `integration/priority_preemption_test.go`,
  `integration/phase10_test.go`, `rollout_kinds_test.go`, `slow_runtime_test.go`, `scheduler/benchmark_test.go`,
  `scheduler/fuzz_test.go` and `controllers/autoscale_test.go`.
- **BREAKING (apply refusals):** an apply is refused when a `resources` or `scale vertical` value cannot be parsed or
  is finer than 1m CPU or 1 byte. The error names the service, the field and the value, and nothing is written. A
  negative value is already refused by the lexer.
- **BREAKING (behavior):** vertical scaling starts working. Its bounds were read as 0, so `scale vertical` did nothing;
  configs that declare it will now get their requests resized.
- **Capacity-correct placement:**
  - The scheduler checks node capacity against the parsed requests, with memory rounded up to whole MiB.
  - It reads and watches the effective request first and falls back to the desired one, as the agent does.
  - A request no node can meet writes `insufficient_capacity`, which cluster scaling, the M241 rollout reason and the
    deadline rollback then use.
- **Stored values that cannot be read are never read as 0 for new work:**
  - The scheduler leaves that service's unplaced instances unplaced with a new reason, `invalid_resource_request`, and
    does not preempt or rebalance for it. Cluster scaling ignores the reason.
  - Its already-running instances are left alone, but their size is unknown, so they count as 0 in node capacity. This
    is a stated exception, and the service is shown in `cca status`.
- **Effective values follow the autoscaler:**
  - The autoscaler rewrites any recommendation stored without a unit, and deletes its recommendation when a service has
    no vertical policy.
  - The intent controller deletes an effective value whose recommendation is gone, so the user's request applies again.
- **The other readers switch to the shared parser:**
  - Agent runtime limits, the autoscaler, tenant usage and the fair scheduler.
  - Eviction, which now reads effective-first.
  - The compiler's QoS class, so `1` and `1000m` count as equal.

  Node capacity facts stay plain MiB integers. Each reader has a stated rule for a value it cannot parse.

## Capabilities

### New Capabilities
- `resource-requests`: how CPU and memory requests and limits are written, validated at apply, and used for placement,
  autoscaling and capacity reporting.

### Modified Capabilities
None. The service-rollout requirement "A rollout without progress is rolled back with its reason" already covers the
`insufficient_capacity` case; this change makes it reachable.

## Out of scope

- `cca describe node` and `cca status` showing allocated requests per node. Today they show the agent's detected
  capacity, which never changes. This is a follow-up milestone.
- Refusing a request larger than its limit, defaults for requests, and limit ranges.
- Tenant quota CPU. It is an integer in millicores (`lang/ast.go`, `types/keys.go`) and is unchanged; how quota is
  compared with usage belongs to M179.
- Volume `size` parsing.
- M244 (removed fields), M245 (per-node "full"), M246 (resources released before the container is gone), M247 (drain
  budget).

## Comparison

Kubernetes `resource.Quantity` uses the notation adopted here: canonical integers (millicores, bytes), and the API
server refuses malformed values at admission. Nomad (MHz, MB) and ECS (CPU units, MiB) use plain integers and refuse
anything else at submit time. None of them turns a bad value into 0. We keep the Kubernetes notation that our configs
already use, and refuse at apply.

## Impact

- **New:** `types/resource_quantity.go`, plus tests.
- **Changed:**
  - `lang/compiler.go`: validation and QoS.
  - `scheduler/scheduler.go`: `Watch`, `extractServiceResourcesFromFacts`, the new reason, preemption, rebalance.
  - `types/state.go`.
  - `agent/telemetry.go`.
  - `controllers/helpers.go`, `controllers/eviction.go`, `controllers/autoscale.go`, `controllers/intent.go`.
  - `tenant/quota.go`, `tenant/fair_scheduler.go`.
  - The fixtures listed above.
- **Removed:** `parseMillicores`, `parseMemoryBytes`, `parseResourceQuantity`.
- **Docs:** `design/dsl-reference.md` (units), `design/storage.md`, `design/graphs/SCHEDULING_GRAPH.md`, and Flow 10 in
  `design/runtime-invariant-flows.md`.
