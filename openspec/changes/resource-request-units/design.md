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

### D2. Kubernetes notation, rounded up to the unit (revised 2026-11-20, user decision D-b)
- **CPU:** a decimal number with `m` (millicores, e.g. `250m`, `1.5m`), or without it (cores, converted to
  millicores). A value finer than 1 millicore is rounded **up** to the next millicore (`0.0005` → `1m`, `1.5m` → `2m`),
  as Kubernetes does.
- **Memory:** a decimal number with an optional suffix: `Ki Mi Gi Ti Pi Ei` (powers of 1024), `k M G T P E` (powers of
  1000), or no suffix for bytes. A value that is not a whole number of bytes is rounded **up** to the next byte
  (`1.1Gi` → 1181116007, `0.1Gi` → 107374183, `1.5` → 2).
- **Integer arithmetic:** on the split digits (`math/big`), never `float64`, so the rounding is exact.
- **Refused (malformed):** unknown suffixes, int64 overflow, empty strings, whitespace, signs and exponents.

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
- **Placement:** placement skips the service's unplaced instances. `attemptPreemptions` skips them as preemptors, and
  `findPreemptionVictim` skips its running instances as victims, as it skips stateful ones: their size is unknown, so
  stopping one frees an unknown amount (added 2026-11-20, B4). `extractRebalancePinnedServices` pins the service, so
  its placed instances are never moved.
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
  an unreadable current value gets no recommendation, and the autoscaler logs once. The same holds when either
  vertical bound of a resource cannot be read: that resource gets no recommendation; a bound is never taken as 0
  (S4, 2026-11-20).
- **Tenant usage, fair share:** sum millicores via `ParseCPUMillicores`. An unreadable value is skipped and logged
  once. It cannot be refused at that point, and leaving it out never blocks admission.
- **QoS:** compares parsed values. An unreadable value was already refused by D3.

`parseMillicores`, `parseMemoryBytes` and `parseResourceQuantity` are deleted.

### D7. Effective values follow the recommendation
- **Units:** `computeVerticalResourceChange` writes `FormatCPUMillicores` / `FormatMemoryMiB`. Its bounds are parsed
  with the shared parser.
- **Legacy recommendations:** migrated once at server start (D9). The per-cycle rewrite in the autoscaler was removed
  (2026-11-20). It fixed only the intent, while the scheduler and the autoscaler read the bare effective value as cores
  until the intent resolver caught up (review B1). The migration makes the per-cycle rewrite redundant: every
  server runs the migration before its runner starts, so in a rolling upgrade the last server to restart migrates
  whatever an older leader still wrote, and after that no old writer remains.
- **Removed policies, per resource (revised 2026-11-20, B3):** when a service has no
  `scale/vertical/{cpu|memory}/` fact for a resource, the autoscaler deletes that resource's
  `intent/autoscaler/service/{svc}/resources/{cpu|memory}`. Removing only `cpu {}` deletes the CPU recommendation and
  keeps the memory one.
- **Effective values per resource (revised 2026-11-20, B3):**
  - `resolveEffectiveResourceChanges` sets effective cpu or memory to the recommendation for that resource, and no
    longer copies the desired value.
  - `deleteEffectiveResourcesWithoutIntent` deletes effective cpu or memory when that resource has no
    recommendation, also when desired has no value. Readers then fall back to desired.
- **Hierarchical names (B5):** these readers find the service name by matching the key's known suffix
  (`/resources/cpu`, `/scale/vertical/cpu/min`, …), not by splitting at the first slash, so `team/web` works.
- *Rejected:* reading a bare intent above 64 as millicores. That heuristic would have to be carried forever.

### D8. Vertical scaling starts working
With bounds parsed, every service that declares `scale vertical` starts receiving recommendations after the upgrade.
This is called out as BREAKING in the changelog. The deploy workloads declare no vertical scaling. That is checked in
task 4.1.

### D9. Startup migration of legacy recommendations (added 2026-11-20, user decision D-a)
- **When:** at `cca server` start. `controllers.RunControllersAfterResourceMigration` runs
  `MigrateUnitlessResourceRecommendations` and starts the HA controller runner only after the migration succeeds
  (revised 2026-11-20, C2).
- **Failure:** the gate retries with bounded exponential back-off: 1 s, doubling up to 1 min
  (`resourceMigrationInitialRetryDelay`, `resourceMigrationMaximumRetryDelay`,
  `resourceMigrationRetryGrowthFactor`), until the context is cancelled. Each failure is logged with the attempt
  number and the next delay. The server never exits for it. The gate runs in its own goroutine, so the API server and
  the rest of the server come up while it retries; only the controllers wait. Starting them anyway would reopen the
  B1 window.
- **What it rewrites:** every bare (digits-only) value under `intent/autoscaler/service/{svc}/resources/{cpu,memory}`
  and `effective/service/{svc}/resources/{cpu,memory}`.
  - CPU: old values are millicores, written as `{n}m`.
  - Memory: old values are bytes, written as `FormatMemoryMiB` (whole MiB, rounded up).
  - Each value is clamped to the service's parsed `scale/vertical` [min, max] for that resource when the bounds can be
    read (B2: `cpu { min 250m max 4 }` with a legacy `4` → `250m`).
- **How:** one store transaction, with a revision compare on every key it rewrites. If another writer won the
  compare, it scans and tries again, up to 5 attempts per migration run.
  - When the plan exceeds the etcd limit, it is split into several transactions (C1). The limit counts compares plus
    operations, so each rewrite counts as 2 items (`transactionItemsPerRewrite`).
  - A service is never split across transactions, so each service is migrated atomically. User decision 2026-11-20:
    per-service batching above the limit is accepted.
- **Idempotent:** unit values are never touched. It never reads or writes `desired/`.
- **Why at start:** the scheduler, the agent and the autoscaler all read the effective value, so it must have units
  before any controller reconciles. The per-cycle rewrite (D7, removed) left a window in which they read the bare value
  as cores.

### D10. Warning for bare CPU values (added 2026-11-20, user decision D-c)
There is no migration of `desired/`. `lang.DeprecationWarnings` warns, and apply does not refuse, for a CPU request
or limit, or a vertical CPU bound, written without `m` that is 64 cores or more. The warning says it is read as cores
and that `{n}m` was probably meant. Both apply paths and `cca lint` print it.

### D11. Rounding instead of refusing (added 2026-11-20, user decision D-b)
See D2. Compile refuses only malformed values, and the compiled fact keeps the text as written.

## Failure cases

| Case | What happens |
|---|---|
| Apply with an unreadable value | Apply refused with service, field, value and line; store unchanged |
| Negative value | Lexer error with line; store unchanged |
| Request larger than any node | Unplaced `insufficient_capacity`; cluster-scale asks for a node; rollout reason and the deadline rollback name it |
| Stored unreadable value | New instances `invalid_resource_request`; no preemption, rebalance or node request; running ones stay and count 0; shown in `cca status`; fixed by re-applying |
| Effective value unreadable | Same as the stored case. No fallback to desired |
| Bare `cpu 2` / `memory 2048` applied before | Now 2000m / 2 KiB. May become `insufficient_capacity` or a too-small limit; visible in `cca describe service` (BREAKING) |
| Legacy bare recommendation or effective value | Rewritten with units (clamped to the bounds) at server start, before controllers run (D9) |
| Vertical scaling removed (one resource or both) | That resource's recommendation and effective value deleted; the desired request applies |
| Value finer than 1m / 1 byte | Rounded up to the next millicore / byte (D2) |
| Bare `cpu 500` applied | Accepted as 500 cores, with an apply warning (D10) |
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

`desired/` is never migrated: bare values there change meaning (BREAKING; D10 warns at apply). Legacy autoscaler
recommendations and effective values are migrated once at server start (D9). Stored bad values surface as
`invalid_resource_request` (D5). Rolling back the binary restores the lenient reading. The new unplaced reason and the
derived invalid-request fact are ignored by older controllers.
