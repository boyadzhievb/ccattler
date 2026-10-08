# Phase 40 — System Design Patterns

Research source: https://github.com/ByteByteGoHq/system-design-101

## Selected Patterns for CCattler

### 1. Circuit Breaker
Where: `network/userspace_proxy.go` (forwardToBackend)
Currently the proxy forwards blindly. A circuit breaker tracks failure rates per backend and trips open after threshold (e.g. 5 consecutive 5xx in 10s), sending 503 directly instead of hammering a dying backend. Half-open state probes periodically to detect recovery.

### 2. Rate Limiting
Where: `api/server.go` (all API endpoints)
Token-bucket or sliding-window rate limiter per client identity. Protects the control plane from runaway automation or misbehaving tenants. Returns 429 with Retry-After header. Per-tenant limits stored as facts.

### 3. Advanced Load Balancing
Where: `network/userspace_proxy.go` (forwardToBackend)
Replace round-robin with least-connections or weighted load balancing. Track active connections per backend, select the one with the fewest. Supports backends with unequal capacity.

### 4. Distributed Tracing
Where: All HTTP paths (proxy, API, agent)
Propagate trace-id headers (W3C Trace Context) through the system. Each component logs trace-id. Enables end-to-end latency analysis for activation flows (proxy → API → controller → agent → container start).

### 5. Read-Path Caching
Where: `api/server.go` (status, describe endpoints)
Cache expensive aggregation queries (status, describe) with short TTL. Cluster-wide status changes infrequently; avoid re-scanning all facts on every API call.

### 6. Bulkhead Isolation
Where: Controller runner, API server
Isolate controller reconciliation loops so a slow controller cannot starve others. Use bounded worker pools per controller. API request handling uses separate goroutine pool from controller loop.

### 7. Dead Letter Queue (DLQ)
Where: Event system
Events that fail processing (e.g. webhook delivery, audit log rotation) go to a DLQ instead of being lost. Enables replay and debugging. Stored as facts with `dlq/` prefix.

### 8. Change Data Capture (CDC)
Where: `store/` package
Expose a CDC stream from the fact store for external consumers. Enables building secondary indexes, feeding analytics pipelines, or syncing with external systems without polling. Built on top of existing Watch infrastructure.

## Implementation Order

1. Circuit breaker (highest user-facing impact, prevents cascading failures)
2. Rate limiting (security hardening)
3. Advanced load balancing (performance)
4. Bulkhead isolation (resilience)
5. Distributed tracing (observability)
6. Read-path caching (performance)
7. DLQ (operational)
8. CDC (extensibility)

## Estimated Effort

Each pattern is 1-2 days of implementation + tests. Full phase: ~2-3 weeks.
