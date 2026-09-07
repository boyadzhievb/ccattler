# CCattler Development Log

## 2026-09-07: Types Package

**Files:** `types/keys.go`, `types/state.go`, `types/types.go`, `types/codec.go`, `types/id.go`

Built the core types package that everything else depends on.

- **keys.go** — Key path constants and helpers matching the etcd schema. All store keys follow the `desired/`, `observed/`, `placement/`, `endpoint/`, `effective/`, `intent/` hierarchy from `etcd-schema.md`. Scan prefixes for controllers to watch.
- **state.go** — Typed string enums for `InstanceState` (pending, running, failed, stopped), `NodeState` (alive, unreachable, draining), `HealthStatus` (healthy, unhealthy, unknown). Strings, not iota ints, since they're stored as values in the fact store.
- **types.go** — Core structs: `Service`, `Instance`, `Node`, `Placement`, `Endpoint`. These are the logical entities that controllers work with, assembled from flat store keys.
- **codec.go** — Read/write helpers that convert entities to/from flat store keys. `WriteService` writes multiple keys under `desired/service/{name}/`. `ReadService` scans the prefix and populates a struct. Same pattern for Instance, Node. `ListInstances`/`ListNodes` scan all instances/nodes and group by ID. `WritePlacement`/`WriteEndpoint`/`DeleteEndpoint` for scheduler and network controller output.
- **id.go** — `NewInstanceID()` generates 5-byte random hex IDs. `IDFunc` type allows deterministic injection in tests.

**Design decision:** Flat keys, not JSON blobs. Each field is its own key (`/ccattler/observed/instance/a8f31/state` → `"running"`). This enables fine-grained watches and atomic field updates, matching the etcd schema. The codec layer bridges between flat keys and Go structs.

## 2026-09-07: Instance Controller

**Files:** `controllers/instance.go`, `controllers/instance_test.go`

The core reconciliation logic: compares desired instance count vs actual and proposes creates/removes.

- Reads `effective/service/{name}/instances` for desired counts per service
- Scans `observed/instance/` to count active instances per service
- Stopped instances excluded from count; pending, running, and failed all count as active
- Scale up: creates new instances with state=pending (3 keys each: marker, service, state)
- Scale down: marks excess instances as stopped, preferring to stop pending instances before running ones
- Returns `[]Change` — never writes to the store directly (architectural rule #5: controllers return proposed changes)

**Tests:** 9 deterministic tests using injected sequential ID generator. Covers scale up from zero, partial scale up, already satisfied (no-op), scale down, scale down prefers pending, stopped not counted, multiple services, scale to zero.

## 2026-09-07: Controller Runner

**Files:** `controllers/runner.go`, `controllers/runner_test.go`

The generic watch → reconcile → apply loop that wires controllers to the store.

- Each controller runs in its own goroutine
- Sets up watches on all prefixes the controller declares via `Watch()`
- Any watch event triggers a reconciliation cycle
- Debounces rapid changes (configurable, default 50ms) to avoid redundant reconciliation
- Initial reconciliation on startup (doesn't wait for a change to run the first cycle)
- `reconcileOnce`: scans all watched prefixes → calls `Reconcile()` → applies returned changes sequentially
- `Run()` blocks until context cancellation or a controller error

**Integration tests:** 4 tests proving the end-to-end loop:
- Write `effective/service/web/instances=3` → runner fires instance controller → 3 pending instances appear in the store
- Scale up: 2 existing running instances → change desired to 5 → 3 new instances created
- Scale down: 4 running instances → change desired to 2 → 2 stopped
- Multiple controllers running concurrently

## 2026-09-07: Scheduler

**Files:** `scheduler/scheduler.go`, `scheduler/scheduler_test.go`

Pure-function scheduler implemented as a Controller. Finds pending unplaced instances and assigns them to nodes.

- Watches `placement/instance/`, `observed/node/`, `observed/instance/`
- Finds pending instances with no existing placement
- Filters to alive nodes only (skips unreachable/draining)
- Spread strategy: picks the node with the fewest existing placements (least-loaded first)
- Returns placement changes (`placement/instance/{id}` → `node-id`)

**Design decisions:**
- No resource accounting yet — spread-only scoring for M1. Resource fit (CPU/memory) will layer on top of the same scoring function later.
- Scheduler is a Controller, not a special-case component. It runs in the same runner as all other controllers, watching and reconciling through the same loop.

**Tests:** 9 deterministic unit tests. Covers: place pending instance, skip already-placed, skip running instances, spread across 3 nodes, spread with existing load (prefer less-loaded node), skip unreachable nodes, no alive nodes (no placements), no pending instances, interface compliance.

## 2026-09-07: End-to-End Integration Tests

**Files:** `integration/reconcile_test.go`

Full pipeline tests with instance controller + scheduler running together in the same runner.

- `TestDesiredToPlaced`: write `effective/service/web/instances=3` with 3 nodes → instances created by instance controller → placed by scheduler → spread 1 per node
- `TestScaleUpPlacesNewInstances`: start with 2, scale to 4 → new instances get placed, spread evenly
- `TestNoPlacementWithoutNodes`: desired=3 with no nodes → instances created but unplaced → add a node → all 3 get placed

These tests prove the architectural rule that components communicate only through state — the instance controller and scheduler never call each other, they react to facts in the store.

## 2026-09-07: Endpoint Controller

**Files:** `controllers/endpoint.go`, `controllers/endpoint_test.go`

Derives endpoint facts from running instances.

- Watches `observed/instance/`, `endpoint/service/`, `desired/service/` (for exposed ports)
- Creates endpoints for running instances that have an IP and a matching expose port
- Removes stale endpoints when instances stop or disappear
- No-op when endpoints already exist

**Tests:** 8 tests. Covers: create endpoint for running instance, skip pending, skip without IP, skip without expose port, remove stale endpoint (stopped instance), multiple instances, already-exists no-op, interface compliance.

## 2026-09-07: Failure Controller

**Files:** `controllers/failure.go`, `controllers/failure_test.go`

Replaces failed instances with new pending ones.

- Watches `observed/instance/`
- For each instance with state=failed: marks it as stopped, creates a replacement with state=pending
- The replacement then flows through the normal pipeline: instance controller counts it, scheduler places it
- Ignores running, pending, and stopped instances

**Tests:** 6 tests. Covers: replace failed instance, ignore running/pending/stopped, multiple failures, interface compliance.

## 2026-09-07: Scheduler Resource Accounting

**Files:** `scheduler/scheduler.go` (updated), `scheduler/scheduler_test.go` (4 new tests)

Added resource-aware scheduling to the existing spread strategy.

- Now also watches `desired/service/` to read CPU/memory requirements
- Filters out nodes that can't fit the instance's resource requirements
- Tracks consumed resources within a scheduling cycle (placing instance A reduces available capacity for instance B)
- No resource requirements = fits anywhere (backwards compatible with earlier tests)

**Tests:** 4 new resource tests (13 total). Covers: resource fit (skip undersized node), resource exhaustion (node full after N placements), resource spread with accounting, no requirements still works.

## 2026-09-07: DSL — Lexer, Parser, Compiler

**Files:** `lang/token.go`, `lang/lexer.go`, `lang/ast.go`, `lang/parser.go`, `lang/compiler.go` + tests

The human-to-machine boundary: `.ccattler` files → facts in the store.

**Lexer** (`token.go`, `lexer.go`):
- Tokenizes: identifiers (including `nginx:1.28`), numbers with unit suffixes (`500m`, `512Mi`, `10s`, `60%`), strings, braces, `/`, `=`, `#` comments, newlines
- Tracks line/col for error messages

**Parser** (`ast.go`, `parser.go`):
- Recursive descent parser producing an AST
- Supports `service` declarations with: `image`, `instances`, `expose`, `resources { cpu, memory }`, `health { http /path, every interval }`
- Error messages include line/col

**Compiler** (`compiler.go`):
- Converts AST → flat facts matching the etcd schema
- Writes `desired/`, `intent/user/`, and `effective/` keys (the compiler sets effective = desired for now; intent resolution will override later)
- Validates: name required, image required, port range 1–65535

**Apply** function: `lang.Apply(ctx, store, input)` — parse → compile → write to store in one call.

**Tests:** 28 lang tests total. Lexer: 9 (basic tokens, units, comments, nested blocks, slashes, line numbers, edge cases). Parser: 12 (minimal service, expose, resources, health, full service, multiple services, comments, empty file, error cases). Compiler: 7 (minimal, port, resources, multiple services, validation errors, Apply end-to-end with store verification).

## 2026-09-07: CLI Entry Point

**Files:** `cmd/ccattler/main.go`

Runnable binary that demonstrates M1.

- `cca apply <file>` — parses a `.ccattler` file, boots memory store with 3 simulated nodes, starts all 4 controllers (instance, scheduler, endpoint, failure), applies the config, waits for reconciliation, prints cluster status
- `cca demo` — same but with a built-in config, no file needed
- `printStatus` shows services, instances with placements, nodes, and per-node load distribution

**Demo output confirms M1**: DSL → parser → facts → instance controller creates 3 pending instances → scheduler places them spread 1 per node across 3 simulated nodes.

## 2026-09-07: Runtime Interface & Adapters

**Files:** `runtime/runtime.go`, `runtime/simulator.go`, `runtime/process.go` + tests

Pluggable runtime layer — controllers and agent don't know how workloads actually run.

- **Runtime interface**: `Start(Spec)`, `Stop(id)`, `Status(id)`, `List()`. `Spec` carries ID, Image, Env, CPUm, MemoryB.
- **SimulatorRuntime**: In-memory fake, tracks running/stopped state. Thread-safe. For tests and `cca demo`.
- **ProcessRuntime**: Runs workloads as OS processes. `Spec.Image` is the shell command. Uses `exec.Command` with process group (`Setpgid`). Graceful shutdown: SIGTERM → configurable grace period → SIGKILL. `StopAll()` for cleanup on exit. `StartError` type distinguishes start failures from runtime crashes.

**Design decision:** Image field dual-purpose — for ProcessRuntime it's the shell command, for ContainerRuntime (future) it's the OCI image reference. This lets us test the full pipeline with real processes before adding container support.

## 2026-09-07: Node Agent

**Files:** `agent/agent.go`, `agent/agent_test.go`

The bridge between the fact store and the runtime. Runs on each machine.

- Registers node as alive on startup
- Watches `placement/instance/` for changes, reconciles periodically (configurable interval, default 1s)
- `reconcileOnce`: finds instances placed on this node, starts missing ones via runtime, stops unplaced ones, reports state (running/failed) + node + IP back to store
- Skips stopped instances — won't restart something the failure controller has already replaced

**Tests:** 5 tests. Covers: start placed instance (verify runtime + store state), ignore other node's instances, register node on startup, stop removed instance, handle dynamically added placement.

## 2026-09-07: Health Checking

**Files:** `agent/health.go`, `agent/health_test.go`, `agent/agent.go` (updated)

Health probes integrated into the agent reconcile loop.

- **Probe types**: HTTP (GET request, 2xx/3xx = healthy) and TCP (dial connection). Configurable timeout (default 2s).
- **Store integration**: Compiler writes health config as `desired/service/{name}/health/{method,path,interval}`. Agent reads health config per service, runs probes on running instances, writes result to `observed/instance/{id}/health`.
- **Key additions**: `types/keys.go` got `KeyDesiredServiceHealthMethod/Path/Interval`. Compiler updated to emit health facts from DSL `health {}` block.

**Tests:** 6 probe tests (HTTP healthy/unhealthy/refused, TCP healthy/refused, port from string) + 3 agent integration tests (reports healthy with real HTTP server, reports unhealthy for refused port, no health written without config).

## 2026-09-07: CLI — Run & Demo with Agent

**Files:** `cmd/ccattler/main.go`, `examples/web.ccattler`

Updated CLI with live process execution and full agent integration.

- `cca run <file>` — starts real processes via ProcessRuntime + node agent on "local" node, prints status every 2s, graceful shutdown on Ctrl+C
- `cca demo` — built-in config with SimulatorRuntime + agent, instances report as "running"
- `cca apply <file>` — simulated mode with 3 nodes (no agent, no runtime)
- `printStatus` shows services with running count, active instances with health status, nodes with CPU/memory load

## 2026-09-07: Status API & Metric Command

**Files:** `cmd/ccattler/main.go` (refactored), `types/keys.go` (metric key)

Added HTTP status API and standalone `status`/`metric` CLI commands.

- **Status API** (`127.0.0.1:9770`): started by `run` and `demo` commands. Serves `/status` (text or JSON depending on Accept header) and `/metric` (POST to set observed metrics).
- **`cca status`**: queries the status API, prints cluster status (services, instances, nodes). Works from a separate terminal while `run` or `demo` is active.
- **`cca metric set <service> <metric> <value>`**: writes simulated metrics to the store via the API. Stored at `observed/metric/service/{name}/{metric}`. Ready for autoscaler controller consumption.
- **JSON output**: `Accept: application/json` on `/status` returns structured data (`serviceStatus`, `instanceStatus`, `nodeStatus`).
- Refactored `printStatus` into `buildStatusText`/`buildStatusJSON` — single source of truth for status rendering.

## 2026-09-07: Container Runtime Adapter

**Files:** `runtime/container.go`, `cmd/ccattler/main.go` (added `run-container` command)

OCI container runtime using the Docker CLI as backend.

- **ContainerRuntime**: implements `Runtime` interface by shelling out to `docker run/stop/inspect/rm`. Container names prefixed with `ccattler-` to avoid conflicts.
- Supports CPU limits (`--cpus`), memory limits (`--memory`), environment variables.
- `Start`: `docker run -d --name ccattler-{id} {image}`
- `Stop`: `docker stop -t 10` (graceful) → `docker rm -f` (cleanup)
- `Status`: `docker inspect --format {{.State.Running}}`
- `StopAll`: cleans up all tracked containers on shutdown
- `run-container` CLI command: like `run` but uses `ContainerRuntime` instead of `ProcessRuntime`

**Design decision:** Shell out to `docker` CLI rather than import the containerd Go client. The containerd client is a heavyweight dependency (Linux-only, many transitive deps). The docker CLI works on macOS (Docker Desktop) and Linux, and the performance overhead is negligible for an orchestrator that manages tens of containers, not thousands.

## 2026-09-07: Heartbeat Leases & Node Failure Detection (M3)

**Files:** `agent/agent.go` (heartbeat), `controllers/nodefailure.go`, `types/keys.go` (ScanLeaseNodes), `integration/distributed_test.go`

Lease-based node liveness and automatic failure recovery.

- **Agent heartbeat**: agent writes `time.Now().UnixMilli()` to `lease/node/{id}` on startup and every tick. Millisecond granularity avoids false expiry from second-level truncation.
- **NodeFailureController**: watches lease timestamps, observed node states, placements, and instance states. When a node's lease exceeds `LeaseTimeout` (default 5s), marks it `unreachable` and marks all its running/pending instances as `failed`.
- **Recovery pipeline**: NodeFailureController marks instances failed → FailureController creates replacements → Scheduler places on surviving nodes → agents start them. Full convergence in ~300ms in tests.
- **Docker check**: `run-container` now verifies `docker` is on PATH before attempting to start containers.

**Tests:** 7 unit tests for NodeFailureController (expired lease, fresh lease, marks instances, skips unreachable, skips stopped, interface, name). 4 distributed integration tests: spread across nodes, node failure reschedule, heartbeats visible, multi-service spread. 1 new agent test (heartbeat write).

**Bug found and fixed:** Initial implementation used `time.Now().Unix()` (1-second granularity) for heartbeat timestamps but 500ms lease timeouts in tests. The truncation error could make a 200ms-old heartbeat appear 1.1s old, causing false "unreachable" detections and flaky tests. Fixed by switching to millisecond timestamps throughout.

## 2026-09-07: Distributed Demo CLI

**Files:** `cmd/ccattler/main.go` (added `demo-distributed` command)

`cca demo-distributed` — 3 simulated nodes, deploys 6 instances spread 2 per node, then kills node-1 after 5 seconds to demonstrate the full failure recovery pipeline. Status prints every 2 seconds showing the transition: node-1 goes unreachable, its instances are rescheduled to node-2 and node-3, system converges back to 6 running instances.

## M3 Milestone Status: IN PROGRESS

Phase 6 partially complete:
- [x] Agent heartbeat leases (millisecond timestamps)
- [x] Node failure detector controller (lease expiry → unreachable → mark instances failed)
- [x] Multi-node simulation (3 agents in one process, each with own SimulatorRuntime)
- [x] Distributed scheduling across nodes (spread strategy)
- [x] Node failure detection → rescheduling → convergence
- [x] `cca demo-distributed` CLI command
- [x] 4 distributed integration tests

**Total: 140 tests across 9 packages, all passing.** `cca demo-distributed` proves end-to-end: deploy → spread → kill → detect → reschedule → converge.

## M2 Milestone Status: COMPLETE

All M2 deliverables are implemented and tested:
- Phase 5: Runtime interface + simulator/process/container adapters, node agent, health checking (HTTP/TCP integrated into agent loop), graceful shutdown, CLI with 6 commands (apply, run, run-container, demo, status, metric), HTTP status API (text + JSON), simulated metric injection ✓

**Total: 128 tests across 9 packages, all passing.** `cca run examples/web.ccattler` proves end-to-end with real processes. `cca status` queries the running instance.

## M1 Milestone Status: COMPLETE

All M1 deliverables are implemented and tested:
- Phase 0: Go project, etcd schema, repo structure ✓
- Phase 1: Store interface + in-memory store ✓
- Phase 2: DSL lexer, parser, AST → facts compiler ✓
- Phase 3: Controller framework + instance/endpoint/failure controllers ✓
- Phase 4: Scheduler with spread + resource accounting ✓

**Total: 78 tests across 7 packages, all passing.** The `cca demo` command proves end-to-end reconciliation.
