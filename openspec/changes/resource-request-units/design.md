# Design: Resource requests are read with their units (M243)

## Context

See proposal.md (Why). The current readers of `desired|effective/service/{svc}/resources/{cpu,memory}`:

| Reader | File | Today |
|---|---|---|
| Scheduler placement | `scheduler/scheduler.go` `extractServiceResourcesFromFacts` | `ParseInt`, error dropped: `500m` → 0, bare `2` → 2m, bare memory = MiB. Desired only; `Watch()` has no effective prefix |
| Agent runtime limits | `agent/telemetry.go` `parseMillicores` / `parseMemoryBytes` | bare CPU = cores, bare memory = bytes; `Ki..Ti` only; bad value → 0 |
| Eviction | `controllers/eviction.go` via `controllers/helpers.go` `parseResourceQuantity` | request bytes compared with node `capacity/memory` (bare MiB); desired only |
| Vertical autoscaler | `controllers/autoscale.go`: current value via `collectResourceValuesBySuffix` → `parseResourceQuantity` (cpu and memory alike); bounds via `Atoi` | bounds `8Gi`/`250m` → 0, so it never acts; writes bare ints |
| Intent → effective | `controllers/intent.go` `resolveEffectiveResourceChanges` | copies strings; writes effective only for services with an intent; never deletes |
| Tenant usage, fair share | `tenant/quota.go`, `tenant/fair_scheduler.go` | `TrimSuffix("m")` + `Atoi`: `0.5` → 0, `2` → 2 |
| QoS class | `lang/compiler.go` `classifyQoS` | string equality |
| Preemption, rebalance | `scheduler/scheduler.go` `attemptPreemptions`, `rebalancePlacedInstances` | use the same request map; no notion of an unreadable request |

Node facts (`observed/node/{id}/capacity|available/{cpu,memory}`) are bare integers in millicores and MiB
(`types/codec.go`, `agent/capacity.go`). The scheduler's `candidateNode` works in millicores and MiB. Templates are
rendered before lexing (`lang/template.go`). The lexer accepts `-` only as part of `->`, so a negative value never
reaches the compiler.

Flows: [Flow 10, Resource Limit Effective/Desired Fallback](../../../design/runtime-invariant-flows.md#10-resource-limit-effectivedesired-fallback)
is altered: the scheduler and eviction join the agent in reading effective-first, the autoscaler writes units, and the
intent controller deletes stale effective values. Call graph:
[SCHEDULING_GRAPH.md](../../../design/graphs/SCHEDULING_GRAPH.md).

## Goals / Non-Goals

**Goals:** one parser, one meaning per stored value, refusal at apply, capacity-correct placement that produces
`insufficient_capacity`, and effective values that cannot outlive their autoscaler recommendation.

**Non-Goals:** changing node capacity units or the store schema; showing allocated requests per node (follow-up);
volume `size`; request ≤ limit checks; tenant quota semantics.

## Decisions

### D1. Parser lives in `types/resource_quantity.go`
`ParseCPUMillicores(text) (int64, error)`, `ParseMemoryBytes(text) (int64, error)`, `FormatCPUMillicores(int64)`
(`"750m"`), `FormatMemoryMiB(bytes)` (rounded up, `"768Mi"`), and `MemoryBytesToMiBRoundedUp(int64)`. `types` is
already imported by every reader, and it holds the similar `duration_text.go`.
*Alternative:* `k8s.io/apimachinery/pkg/api/resource`. Rejected: a large dependency tree for two functions, and it
accepts exponent forms (`1e3`) we don't want.

### D2. Kubernetes notation, parsed exactly
- **CPU:** `<int>m`, or a decimal number of cores converted to millicores. A value that does not land on a whole
  millicore is refused.
- **Memory:** a decimal number with an optional suffix: `Ki Mi Gi Ti Pi Ei` (powers of 1024), `k M G T P E` (powers of
  1000), or no suffix for bytes. The result must be a whole number of bytes.
- **Exact arithmetic:** integer arithmetic on the split digits, never `float64`, so `1.1Gi` is exact.
- **Refused:** int64 overflow, empty strings, whitespace, signs and exponents.

### D3. Validation in the compiler
`validateServiceResourceValues(serviceDecl, sourceLines)` runs in `compileServiceDeclaration` before facts are built.
It covers the 4 `resources` fields and the vertical cpu/memory min/max, and returns a positioned error. Every apply path
compiles through here. `cpu 0` is allowed. Negative values are already refused by the lexer (`unexpected character
'-'`), and the parser's unit tests cover them.

### D4. Scheduler reads and watches effective, then desired
- **Watching:** `Scheduler.Watch()` adds `types.ScanEffectiveServices`. The runner passes only watched prefixes to
  `Reconcile` (`scanFactsForController`, controllers/runner.go), so without this the effective facts never arrive.
- **Reading:** `extractServiceResourcesFromFacts` reads effective-first per resource, CPU in millicores and memory as
  `ceil(bytes / MiB)`.
- **Errors:** a parse error marks the service invalid with key and value. An unreadable effective value does not fall
  back to desired, because the agent uses the effective value.
- **Testing:** the scheduler test goes through the runner's fact scan, not hand-built facts.

### D5. Unreadable stored values: `invalid_resource_request`
- **The reason:** `types.UnplacedInvalidResourceRequest`. `classifyUnplacedReason` checks it before the
  `aliveCount == 0` early return, so an invalid service never reads as `no_nodes`, which cluster scaling counts as
  demand.
- **Placement:** placement skips the service's unplaced instances. `attemptPreemptions` skips them as preemptors.
  `extractRebalancePinnedServices` pins the service, so its placed instances are never moved.
- **Running instances:** they keep their nodes and count as 0 in capacity accounting. This is the stated exception in
  the spec: the size is unknown, apply now refuses such values, and re-applying fixes them. `cca status` lists the
  service and value (from a derived fact the scheduler writes), and the warning is logged once per value.
- **Cluster scaling:** `filterInsufficientCapacityDemand` is an allow-list, so the reason is ignored with no change
  there. A test pins this, including with zero alive nodes.
- *Alternative:* stop placing on nodes that run such instances. Rejected because one bad service spread across every
  node would block the whole cluster.

### D6. Other readers, and what each does on a parse error
- **Agent** (`lookupServiceResourcesFromStore`): returns 0 for that resource and logs once. The container then starts
  without that limit, as for a missing value. The agent cannot refuse an already-placed instance, and the scheduler has
  already stopped new placements.
- **Eviction:**
  - Reads effective-first.
  - Converts request bytes to MiB.
  - Reads node capacity and availability as plain integers (MiB), never with `ParseMemoryBytes`.
  - On a request it cannot parse, skips that service when ranking eviction candidates and logs once. It does not count
    the service as using 0, which would protect it.
- **Autoscaler current value:** `collectResourceValuesBySuffix` is split into `collectCPUMillicoresBySuffix` and
  `collectMemoryBytesBySuffix`, so one parser never sees the other's units. They read effective-first. A service with
  an unreadable current value gets no recommendation, and the autoscaler logs once.
- **Tenant usage, fair share:** sum millicores via `ParseCPUMillicores`. An unreadable value is skipped and logged
  once. It cannot be refused at that point, and leaving it out never blocks admission.
- **QoS:** compares parsed values. An unreadable value was already refused by D3.

`parseMillicores`, `parseMemoryBytes` and `parseResourceQuantity` are deleted.

### D7. Effective values follow the recommendation
- **Units:** `computeVerticalResourceChange` writes `FormatCPUMillicores` / `FormatMemoryMiB`. Its bounds are parsed
  with the shared parser.
- **Legacy recommendations:** a stored recommendation without a unit is rewritten in unit form on every reconcile,
  even when the value does not change, or when no metrics exist. That closes the legacy window instead of waiting for a
  value change.
- **Removed policies:** when a service has no `scale/vertical/` facts, the autoscaler deletes its
  `intent/autoscaler/service/{svc}/resources/*`.
- **Stale effective values:** `resolveEffectiveResourceChanges` deletes `effective/service/{svc}/resources/*` for every
  service with no intent, so the desired request applies.
- *Rejected:* reading a bare intent above 64 as millicores. That heuristic would have to be carried forever.

### D8. Vertical scaling starts working
With bounds parsed, every service that declares `scale vertical` starts receiving recommendations after the upgrade.
This is called out as BREAKING in the changelog. The deploy workloads declare no vertical scaling. That is checked in
task 4.1.

## Failure cases

| Case | What happens |
|---|---|
| Apply with an unreadable value | Apply refused with service, field, value and line; store unchanged |
| Negative value | Lexer error with line; store unchanged |
| Request larger than any node | Unplaced `insufficient_capacity`; cluster-scale asks for a node; rollout reason and the deadline rollback name it |
| Stored unreadable value | New instances `invalid_resource_request`; no preemption, rebalance or node request; running ones stay and count 0; shown in `cca status`; fixed by re-applying |
| Effective value unreadable | Same as the stored case. No fallback to desired |
| Bare `cpu 2` / `memory 2048` applied before | Now 2000m / 2 KiB. May become `insufficient_capacity` or a too-small limit; visible in `cca describe service` (BREAKING) |
| Legacy bare recommendation | Rewritten with units on the next reconcile |
| Vertical scaling removed | Recommendation and effective value deleted; the desired request applies |
| Overflow (`9999999Ei`) | Refused at apply |
| Agent cannot parse at start | Container starts without that limit, logged once |
| Eviction or tenant cannot parse | Service skipped in that ranking or sum, logged once |

## Risks / Trade-offs

- [Existing stored bad values stop new placements for their services] → Shown in `cca status` and the logs, and fixed
  by re-applying. This is the fail-closed choice.
- [Running instances of such a service count as 0] → This is the stated exception. Leaving it unbounded is the price of
  not blocking the cluster (D5).
- [Real capacity checks expose overcommit that was silently tolerated] → The deploy workloads must fit; task 4.3
  checks it.
- [Vertical scaling begins resizing configs that declared it] → BREAKING note; no testbed workload uses it.
- [Memory rounded up to MiB overstates small requests] → Never an undercount, and under 1 MiB per instance.

## Migration Plan

There is no data migration. Legacy recommendations heal on the next reconcile (D7), and stored bad values surface as
`invalid_resource_request` (D5). Rolling back the binary restores the lenient reading. The new unplaced reason and the
derived invalid-request fact are ignored by older controllers.
