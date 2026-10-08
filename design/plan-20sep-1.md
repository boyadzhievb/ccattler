# CCattler Comprehensive Audit Plan — September 20, 2026

## Project Snapshot

| Metric | Value |
|--------|-------|
| Go source files | 107 |
| Test files | 65 |
| Total Go LOC | ~54,000 |
| Packages | 20 |
| Tests (unit+bench+fuzz) | 758 |
| Milestones complete | M1–M36 |
| Latest tag | v0.36.0 |
| Go version | 1.26.5 |
| Test coverage (6 key pkgs) | 67.6% |

## Instructions for Executing Model

Read CLAUDE.md first — it contains the full architecture, code style rules, and completed phase list. Read the memory files in `/Users/boyadboz/.claude/projects/-Users-boyadboz-REPOS/memory/` for workflow rules (commit prefix, naming conventions, no abbreviations, etc.).

For each section below: investigate, report findings with file:line references, then fix what you can. For items you cannot fix without user input, document the issue and recommended approach. Commit each section separately with prefix "M36-audit:" so the user can review incrementally.

---

## 1. CODE QUALITY & CONSISTENCY

### 1.1 Unchecked Error Returns
**Priority: HIGH**

Multiple store operations (Put, Delete) have their errors silently dropped, especially in:
- `tenant/lifecycle.go` — ~10 Put() calls with no error check (lines 59-88)
- `tenant/quota.go` — 3 Put() calls at lines 117-121
- `tenant/shared_service.go` — Delete() calls at lines 62-64
- `network/userspace_proxy.go` — Put() in `writeActivatingState` and `recordLastRequestTime`

**Action**: Grep for `\.Put(ctx` and `\.Delete(ctx` not preceded by `if` or assigned to a variable. Decide per-site: log the error, return it, or document why it's safe to ignore.

### 1.2 Duplicate Code
**Priority: MEDIUM**

Two separate `parseDurationSeconds` implementations:
- `controllers/autoscale.go:304` — `parseDurationSeconds(string) int`
- `network/userspace_proxy.go:217` — `parseDurationSecondsFromString(string) int`

Both do the same thing (parse "60s", "5m" → seconds). Extract to `types/` or a shared `util` package.

Also check: `extractActiveInstanceCounts` in autoscale.go vs `countRunningInstancesPerService` in warmzero.go — similar logic, potentially consolidatable.

### 1.3 Large Functions
**Priority: MEDIUM**

Functions exceeding 100 lines that should be broken down:
- `lang/compiler.go:158` — `compileServiceDeclaration` (299 lines). Extract sub-compilers for scale, placement, probes, config.
- `scheduler/scheduler.go:46` — `Reconcile` (150 lines). Extract scoring, filtering, placement phases.

**Action**: Measure cyclomatic complexity. Refactor functions >150 lines or >20 cyclomatic complexity into named sub-functions.

### 1.4 Magic Numbers
**Priority: LOW**

- `tenant/fair_scheduler.go:115-117` — `1000`, `500` used in priority scoring without named constants
- `cmd/mcp/tools.go` — `30*time.Second` timeout repeated 5+ times, `500` line limit repeated twice
- `network/userspace_proxy.go` — `500 * time.Millisecond` poll interval, `30 * time.Second` default timeout

**Action**: Extract to package-level constants with descriptive names.

### 1.5 Context Usage
**Priority: LOW**

23 uses of `context.Background()` in non-test code. Audit each: should it use a passed-in context instead? Especially in goroutines spawned from request handlers.

---

## 2. TEST COVERAGE & QUALITY

### 2.1 Missing Test Files
**Priority: HIGH**

Three packages have zero test files:
- `cmd/cca/` — the main CLI. Complex flag parsing, ~1500 lines. Needs table-driven tests for flag combinations and at minimum smoke tests for each subcommand.
- `cmd/mcp/` — MCP server. 14 tools, auth, audit log. Needs tool dispatch tests, auth rejection tests, read-only mode tests.
- `infra/` — infrastructure provider interface. Needs at minimum `SimulatorInfraProvider` tests.

### 2.2 Low Coverage Areas
**Priority: HIGH**

From coverage analysis, these functions have <50% coverage:
- `api/describe.go` — `buildServiceInitSteps` (12.5%), `buildServiceConfig` (20%), `buildServiceProbes` (30%), `buildServiceHealth` (33%), `buildServiceRollout` (33%), `buildServiceSecrets` (37.5%), `buildServiceUpdateStrategy` (40%), `buildServiceAutoscaling` (42.9%), `buildServicePlacement` (45.5%)
- `api/server.go` — `Handler()` (0%)

**Action**: Write targeted tests for each low-coverage function in `api/describe_test.go`. These are pure functions that take facts and return structs — straightforward to test.

### 2.3 Integration Test Gaps
**Priority: MEDIUM**

No end-to-end test for the warm-zero activation flow (proxy → activation state → autoscaler override → endpoint appears → request forwarded). Create an integration test in `integration/` that:
1. Creates a MemoryStore
2. Applies a service with `min 0, idle_timeout 5s`
3. Starts controllers + proxy
4. Sends an HTTP request
5. Verifies activation → scaling → endpoint → response

### 2.4 Negative/Edge Case Tests
**Priority: MEDIUM**

Test what happens when:
- Store becomes unavailable during proxy activation (should timeout gracefully)
- Activation timeout is set to 0
- idle_timeout is set without min=0
- Multiple services activate simultaneously
- DSL has idle_timeout but no activation_timeout (should use default 30s)
- Service is deleted while activation is in progress

### 2.5 Benchmark Gaps
**Priority: LOW**

No benchmarks exist for:
- Controller reconciliation (especially autoscale with many services)
- Proxy request forwarding throughput
- Warm-zero activation latency
- API handler response time

**Action**: Add `BenchmarkAutoscaleReconcile`, `BenchmarkProxyForward`, `BenchmarkWarmZeroActivation` in respective `_test.go` files.

---

## 3. SECURITY AUDIT

### 3.1 API Input Validation
**Priority: HIGH**

Several API handlers accept query parameters without validation:
- `POST /api/activate?service={name}` — no validation that `service` is a valid service name (could be used to write arbitrary keys if the name contains path separators)
- `POST /api/metric?service=X&metric=Y&value=Z` — `value` is written directly to store without numeric validation
- `POST /api/scale` — check if `count` is validated against min/max bounds
- `GET /api/state?key=X` — is `key` validated to prevent reading sensitive prefixes?

**Action**: Add service name validation (alphanumeric + hyphen, max length). Add value type validation for metrics. Add prefix-based access control checks if RBAC is enabled.

### 3.2 Proxy Request Smuggling
**Priority: HIGH**

`UserSpaceProxy` extracts service name from Host header. Check:
- Can Host header contain path traversal (`../admin`)?
- Can it contain null bytes?
- What happens with very long Host headers?
- Is the Host header sanitized before being used as a store key prefix?

**Action**: Add `extractServiceName` test cases for malicious inputs. Add length and character validation.

### 3.3 Store Key Injection
**Priority: HIGH**

The activation webhook writes `derived/service/{name}/activation/state` where `{name}` comes from an HTTP query parameter. If `name = "../../desired/service/web/instances"`, the key becomes malformed.

**Action**: Validate service names at the API boundary — reject names containing `/`, `..`, or non-printable characters. Apply this validation to all handlers that accept service/node/instance names.

### 3.4 RBAC for New Endpoints
**Priority: MEDIUM**

The new `POST /api/activate` endpoint bypasses RBAC — it writes to `derived/service/` prefix directly. If the API has the Authorized Store enabled, this may already be caught, but verify:
- Does the activate handler go through the authorized store wrapper?
- What RBAC role is needed to activate a service?
- Should there be a dedicated `warm-zero-activator` role?

### 3.5 Denial of Service via Proxy
**Priority: MEDIUM**

The proxy holds goroutines open during cold activation (up to activation_timeout). An attacker could:
1. Send thousands of requests to a scaled-to-zero service
2. Each spawns a goroutine that blocks for up to 60s
3. Exhaust server goroutines/memory

**Action**: Add per-service concurrent activation limit (e.g., max 100 waiting requests). Return 503 immediately when limit exceeded.

### 3.6 Secret Scanning
**Priority: LOW**

Run: `grep -rn 'BEGIN.*PRIVATE\|password.*=\|secret.*=\|api_key\|access_key' --include='*.go' --include='*.yml' --include='*.yaml' --include='*.json'`

Verify no real credentials are committed. Check `.gitignore` covers `.ccattler/`, `*.pem`, `*.key`.

---

## 4. PERFORMANCE

### 4.1 Store Scan Hot Paths
**Priority: HIGH**

Controllers call `Scan()` on every reconciliation tick. The `WarmZeroController.Watch()` returns 5 prefixes — all facts under those prefixes are loaded into memory every tick.

**Action**: Profile with `go test -cpuprofile -memprofile` on a test with 100+ services. Measure:
- Memory allocation per reconciliation cycle
- Time spent in `Scan()` vs actual reconciliation logic
- Whether `extractWarmZeroConfigs` iterates all facts or stops early

### 4.2 Proxy Per-Request Store Reads
**Priority: HIGH**

`isWarmZeroEnabled()` does a `factStore.Get()` on every request to check if the service has warm-zero configured. This is fine for low traffic but becomes a bottleneck at scale.

**Action**: Cache warm-zero configuration with a short TTL (5s) or use a watch-based cache that invalidates on change. The `StoreBackedResolver` already does per-request scans — consider if a combined cache would help.

### 4.3 Last Request Time Write Throttling
**Priority: MEDIUM**

Currently throttled to 1 write/sec per service using an in-memory map + mutex. This is correct but:
- The mutex is held during the Put() call — under contention, this blocks other goroutines
- Consider: release lock before Put(), accept slightly-stale throttle checks

### 4.4 Scheduler Performance at Scale
**Priority: MEDIUM**

`BenchmarkSchedule100Nodes1000Instances` takes 8.2ms — acceptable but verify O(n*m) scaling is not hiding a worse inner loop. Profile with 1000 nodes × 10000 instances.

### 4.5 Activation Channel Cleanup
**Priority: LOW**

`activationChannels` map grows unbounded if services are activated then deleted. Consider periodic cleanup of channels for services that no longer exist.

---

## 5. ARCHITECTURE & DESIGN

### 5.1 Warm-Zero State Machine Correctness
**Priority: HIGH**

Verify the state machine handles all edge cases:

```
              ┌──────────────┐
              │              │
    ┌────────►│   inactive   │◄──────────┐
    │         │              │           │
    │         └──────┬───────┘           │
    │                │                   │
    │         proxy writes          idle timeout
    │         "activating"          OR no instances
    │                │                   │
    │         ┌──────▼───────┐           │
    │         │              │           │
    │         │  activating  ├───────────┘
    │         │              │  (timeout)
    │         └──────┬───────┘
    │                │
    │         endpoints appear
    │                │
    │         ┌──────▼───────┐
    │         │              │
    └─────────┤    active    │
   (no inst)  │              │
              └──────────────┘
```

Missing transitions to verify:
- What if proxy writes "activating" but controller hasn't seen it yet and writes "inactive"? (Race between proxy and controller reconciliation)
- What if service config changes (idle_timeout removed) while in "activating" state?
- What if two controllers run simultaneously (HA mode) and both try to transition state?

**Action**: Write a state machine test that exercises every transition, including concurrent transitions.

### 5.2 Controller Ordering Dependencies
**Priority: MEDIUM**

The warm-zero flow has implicit ordering:
1. Proxy writes `activating` → 2. Autoscaler reads it, recommends 1 → 3. IntentResolver merges → 4. InstanceController creates → 5. Scheduler places → 6. Agent starts → 7. EndpointController creates endpoint → 8. WarmZeroController transitions to `active`

If any controller runs before its predecessor in a single reconciliation tick, the chain stalls for one tick. This is correct (eventual consistency) but could cause activation latency of `N * reconciliation_interval` in worst case.

**Action**: Document the expected activation latency as a function of reconciliation interval. Consider if the proxy should poll more aggressively in the first few seconds.

### 5.3 Proxy-Controller State Coupling
**Priority: MEDIUM**

The proxy writes `activating` state directly to the store, and also reads it. The WarmZeroController also reads and writes it. This dual-writer pattern could cause conflicts:
- Proxy writes "activating"
- Controller reads "activating", no endpoints → no change
- Endpoints appear, proxy detects via polling → signals channel → forwards request
- Controller sees endpoints → writes "active"
- Proxy also writes "last_request_time"

Verify there's no race where controller writes "inactive" while proxy is mid-activation.

### 5.4 Event-Driven Activation Gap
**Priority: LOW**

The plan mentions event-driven activation (queue metrics > 0 → autoscaler recommends > 0) works "naturally" with min=0. But verify:
- Does the autoscaler actually recommend > 0 when min=0 and there's queue depth? The activation override only kicks in when state is "activating" — but queue-driven scaling doesn't go through the proxy, so no one sets "activating".
- Should WarmZeroController also set "activating" when it detects autoscaler recommending > 0?

### 5.5 Graceful Proxy Shutdown
**Priority: LOW**

When the proxy context is cancelled, `httpServer.Close()` is called which drops in-flight requests. For warm-zero requests that have been waiting 30s for activation, this is a bad experience.

**Action**: Use `httpServer.Shutdown(ctx)` with a grace period instead of `Close()`.

---

## 6. DOCUMENTATION

### 6.1 DSL Documentation Gap
**Priority: MEDIUM**

The CLAUDE.md DSL example section doesn't show the warm-zero syntax. Add an example:
```
service api {
    image myapi:v3
    expose 8080
    scale {
        horizontal {
            min 0
            max 10
            target cpu = 60%
            idle_timeout 5m
            activation_timeout 60s
        }
    }
}
```

### 6.2 Operational Runbook
**Priority: LOW**

No documentation on:
- How to diagnose a stuck activation (service in "activating" forever)
- How to manually override activation state via API
- What metrics/logs to watch for warm-zero issues
- How idle_timeout interacts with stabilization windows

### 6.3 Architecture Decision Records
**Priority: LOW**

Key design decisions are in the plan file but not persisted:
- Why interceptor proxy over sidecar or queue-based activation
- Why channel-based concurrent activation vs mutex-based
- Why store-based warm-zero detection vs config-based

---

## 7. OPERATIONAL CONCERNS

### 7.1 Observability for Warm-Zero
**Priority: HIGH**

No Prometheus metrics for warm-zero:
- `ccattler_activation_duration_seconds` — histogram of cold-start latency
- `ccattler_activation_total` — counter of activations by service and outcome (success/timeout)
- `ccattler_activation_waiting_requests` — gauge of requests currently waiting during activation
- `ccattler_idle_scaledown_total` — counter of idle-timeout scale-downs

**Action**: Add metrics instrumentation to `handleColdActivation` and `WarmZeroController.Reconcile`.

### 7.2 Structured Logging
**Priority: MEDIUM**

The proxy has no logging for activation events. Add structured log entries for:
- "activation triggered" (service, request_id)
- "activation completed" (service, duration_ms)
- "activation timeout" (service, timeout_seconds)
- "idle timeout triggered" (service, idle_duration)

### 7.3 Health Check Integration
**Priority: LOW**

`/healthz` doesn't report warm-zero controller status. Consider adding:
- Count of services in each activation state
- Whether any activations are stuck (activating for > 2× activation_timeout)

---

## 8. SUGGESTED IMPROVEMENTS (FUTURE WORK)

### 8.1 Graduated Activation (Priority Queue)
When many services activate simultaneously, they compete for scheduler resources. Implement priority-based activation ordering.

### 8.2 Predictive Pre-warming
Track activation patterns (e.g., "this service activates every Monday at 9am") and pre-warm before expected traffic.

### 8.3 Activation History
Store activation events (when, duration, trigger source) for capacity planning and SLO reporting.

### 8.4 Client-Side Retry Headers
On 503 timeout, include `Retry-After` header so clients know when to retry.

### 8.5 WebSocket/gRPC Support
Current proxy only handles HTTP. Consider how WebSocket upgrades and gRPC streams should work during cold activation.

### 8.6 Multi-Port Activation
Current proxy activates based on Host header. Services with multiple ports may need activation from any port.

---

## Execution Order

Work through sections in this order (by impact × effort):

1. **3.1, 3.2, 3.3** — Security input validation (HIGH priority, quick fixes)
2. **2.1** — Missing test files for cmd/ packages (HIGH priority, medium effort)
3. **1.1** — Unchecked error returns (HIGH priority, medium effort)
4. **7.1** — Warm-zero observability metrics (HIGH priority, medium effort)
5. **2.2** — Low coverage in api/describe.go (HIGH priority, medium effort)
6. **4.2** — Proxy per-request store reads caching (HIGH priority, medium effort)
7. **5.1** — State machine edge case tests (HIGH priority, medium effort)
8. **3.5** — DoS protection for proxy activation (MEDIUM priority, quick fix)
9. **1.2** — Duplicate code extraction (MEDIUM priority, quick fix)
10. **2.3** — Integration test for warm-zero flow (MEDIUM priority, medium effort)
11. **4.1** — Profile store scan hot paths (MEDIUM priority, medium effort)
12. **5.5** — Graceful proxy shutdown (LOW priority, quick fix)
13. **1.3, 1.4, 1.5** — Code quality cleanup (LOW priority, medium effort)
14. **6.1** — DSL documentation update (LOW priority, quick fix)
15. **7.2, 7.3** — Logging and health (LOW priority, medium effort)

After completing items 1-7, run full test suite with race detector and verify coverage improved to >75% for key packages.
