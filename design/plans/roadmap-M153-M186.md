# CCattler Roadmap — M153 to M187

Created 2026-10-29. Revised 2026-10-29 (removed K8s-isms, aligned with
fact-based architecture). Comprehensive milestone plan for production-grade
readiness.

**Prerequisite:** M152 (circuit breaker) complete. All milestones below build on
the M1–M152 foundation.

**Design principles applied:**
- Facts, rules, and reconciliation — not resource types
- Extend existing DSL blocks and controllers — don't create new resource kinds
- Service-centric placement rules — not label selectors
- Compiler-level policy — not webhook round-trips
- Fact store as the extension point — not CRD registries

---

## Phase A — Multi-User Auth & Identity (M153–M158)

CCattler has the auth primitives (authenticator chain, OIDC JWT validation,
capability permissions, audit log persistence) but lacks the user-facing flows
that let multiple humans use a cluster. K8s removed password-based auth in v1.19
— we follow suit and go OIDC-first.

### M153 — OIDC Browser Login Flow
**What exists:** `cca login --token <jwt>` requires manual token. OIDCDiscoveryClient
fetches JWKS. Comment in CLI: "OIDC browser login will be available in a future release."
**Deliverables:**
- `cca login <server>` (no `--token`) starts OAuth2 PKCE flow
- Localhost HTTP callback listener on ephemeral port
- Exchanges authorization code for tokens via IdP token endpoint
- Stores access + refresh tokens in credentials.json
- Token refresh on `cca` invocation when access token < 5min from expiry
- Works with Google, GitHub, and generic OIDC providers
**Files:** `cmd/cca/command_login.go`, `security/oidc_browser_flow.go` (new)
**Tests:** Unit test with mock IdP, integration test with test OIDC server

### M154 — Credential Storage & Server Contexts
**What exists:** Single `~/.ccattler/credentials.json` with one server/token.
**Deliverables:**
- `~/.ccattler/config.json` with named contexts (like kubeconfig)
- Each context: server address, auth method, token/cert paths, default team
- `cca context list/use/current/delete` commands
- `cca login` creates/updates context for the target server
- `--context <name>` flag on all CLI commands
- Backward-compatible: old credentials.json auto-migrated to "default" context
**Files:** `cmd/cca/command_context.go` (new), `cmd/cca/command_login.go`
**Tests:** Migration test, context switching test, concurrent context file access

### M155 — Webhook Token Authentication
**What exists:** AuthenticatorChain supports pluggable authenticators. No webhook type.
**Deliverables:**
- `WebhookAuthenticator` sends token to configured HTTPS endpoint
- Endpoint returns JSON with principal, groups, attributes
- Configurable via `cca server --authn-webhook-url <url>`
- Response caching with configurable TTL (default 2min)
- Timeout + fallback behavior (fail-closed)
**Files:** `security/authn_webhook.go` (new), `security/authn_webhook_test.go` (new)
**Tests:** Unit tests with httptest server, timeout/error scenarios

### M156 — Runtime Role Management CLI
**What exists:** Capability type with 15 permissions. CapabilityAuthorizer in API layer.
No CLI to manage grants.
**Deliverables:**
- `cca role grant <principal> <capability> [--scope <path>]`
- `cca role revoke <principal> <capability> [--scope <path>]`
- `cca role list [--principal <p>]` — show all grants
- `cca role check <principal> <capability> [--scope <path>]` — test access
- Grants stored at `config/role/{principal}/{capability}` in fact store
- Built-in "cluster-admin" role auto-granted to init principal
**Files:** `cmd/cca/command_role.go` (new), `security/role_store.go` (new)
**Tests:** Grant/revoke lifecycle, scope inheritance, deny-by-default verification

### M157 — Persistent Audit Log & CLI
**What exists:** StoreBackedAuditLog writes to `audit/` prefix. No query interface.
**Deliverables:**
- `cca audit [--principal <p>] [--action <a>] [--since <time>] [--limit N]`
- `cca audit --follow` streams new entries via SSE
- `/api/audit` REST endpoint with query parameters
- Automatic audit log rotation (configurable retention, default 7 days)
- Audit entries include auth method used (mTLS, OIDC, webhook, local-token)
**Files:** `cmd/cca/command_audit.go` (new), `api/audit_handler.go` (new)
**Tests:** Query filtering, rotation, SSE streaming

### M158 — Multi-User Concurrent Auth Test Suite
**What exists:** Auth tests are single-user. Tenant isolation tested but not concurrent auth.
**Deliverables:**
- Integration test: 3 users (admin, team-a-dev, team-b-readonly)
- Test concurrent API calls with different tokens
- Verify tenant isolation under concurrent access
- Verify audit log captures all principals correctly
- Verify token expiry mid-session handling
- Negative test: escalation attempts (team-a user accessing team-b resources)
**Files:** `integration/multi_user_auth_test.go` (new)
**Dependencies:** M153–M157

---

## Phase B — Scheduling & Placement (M159–M166)

CCattler's scheduler already has zone spread, architecture constraints, and
service group co-scheduling. These milestones extend the EXISTING placement DSL
and scheduler scoring pipeline — no new resource types, no label selectors, no
separate controllers for placement concerns.

### M159 — Per-Node Placement Mode
**What exists:** Instance controller places by replica count. No per-node guarantee.
**Deliverables:**
- Add `mode` field to PlacementDecl: `placement { mode per-node }`
- Compiler emits `desired/service/{name}/placement/mode = per-node`
- Instance controller reads mode fact, sets desired count = alive node count
- Scheduler places exactly one instance per alive node, skips nodes that have one
- Node join/leave auto-adjusts count (no separate DaemonSet controller)
**Dependencies:** None
**Tests:** Unit: compiler emits mode fact, instance controller adjusts count. Integration: deploy per-node service on 3-node cluster, add 4th node → 4th instance appears.

### M160 — Service Priority & Preemption
**What exists:** QoS classes (Guaranteed, Burstable, BestEffort) for eviction only.
**Deliverables:**
- Add `priority` integer field to ServiceDecl (0-1000, default 0)
- Compiler emits `desired/service/{name}/priority = N`
- Eviction controller uses priority as secondary sort after QoS class
- Scheduler preempts: when all nodes full, stops lowest-priority instance to place higher-priority
- No separate PriorityClass objects — priority is a field on the service
**Dependencies:** None
**Tests:** Unit: eviction sorts by priority within QoS tier, scheduler preempts low for high. Integration: fill cluster with priority-0 services, deploy priority-100 → it preempts and runs.

### M161 — Colocate & Separate Placement Hints
**What exists:** Service groups for co-scheduling. No per-service affinity hints.
**Deliverables:**
- Add `colocate` and `separate` string lists to PlacementDecl
- `placement { colocate "redis" }` — prefer nodes where "redis" runs (scoring bonus)
- `placement { separate "redis" }` — avoid nodes where "redis" runs (scoring penalty)
- Soft hints via `rankByPreferences`, not hard constraints — placement still proceeds if no ideal node
- Name services directly — no label selectors or match expressions
**Dependencies:** None
**Tests:** Unit: scheduler scores colocate nodes higher, separate nodes lower. Integration: redis + app with colocate → same node; 2 copies of web with separate → different nodes.

### M162 — Zone Spread with MaxSkew
**What exists:** Zone spread is boolean ("spread" or not). No skew control.
**Deliverables:**
- Extend ZonePolicy: `placement { spread zone maxSkew 1 }`
- Add `MaxSkew int` to PlacementDecl, compiler emits zone/max_skew fact
- Scheduler rejects nodes in zones where `(zone_count - min_zone_count) >= maxSkew`
- Default maxSkew=1 preserves current behavior
- maxSkew=0 means soft spread only (no hard constraint)
**Dependencies:** None
**Tests:** Unit: scheduler rejects over-skewed zones. Integration: 3-zone cluster, 9 instances with maxSkew=1 → exactly 3 per zone.

### M163 — Stateful Shutdown Order
**What exists:** `stateful true` with ordinal IDs, ordered startup, highest-ordinal-first removal.
**Deliverables:**
- Add `shutdown_order` field: "reverse" (default, current behavior), "parallel", "sequential" (lowest first)
- Drain controller and rollout controller read `desired/service/{name}/shutdown_order`
- Respect ordering during scale-down, drain, and rolling update
- Extends existing stateful services — no new StatefulSet type
**Dependencies:** None
**Tests:** Unit: drain controller respects sequential vs reverse. Integration: stateful with shutdown_order=sequential, scale 3→1, verify instance-0 stops before instance-1.

### M164 — Job TTL & CronJob Enhancements
**What exists:** JobController with completions, parallelism, backoff. CronJobController exists.
**Deliverables:**
- Add `ttl_after_finished` duration to JobDecl: `job "x" { ttl_after_finished "1h" }`
- Job controller deletes instances and facts when TTL expires after completion
- Add `concurrency_policy` to CronJobDecl: "allow", "forbid", "replace"
- Fields on existing job/cronjob blocks — no new controllers
**Dependencies:** None
**Tests:** Unit: job controller cleans up after TTL, cronjob respects concurrency policy. Integration: job with ttl_after_finished=10s, verify cleanup.

### M165 — External Metrics Scaling
**What exists:** Autoscaler with CPU/memory targets, event-driven scaling, vertical scaling.
**Deliverables:**
- Add `external` metric source: `scale { horizontal { target external "queue.depth" = 100 endpoint "http://metrics:9090/query" } }`
- Autoscale controller polls endpoint (Prometheus query API format)
- Feeds result into existing scaling algorithm alongside CPU/memory
- Pluggable metrics adapter interface for future backends
- Not a new HPA type — just another target line in `scale { horizontal { ... } }`
**Dependencies:** None
**Tests:** Unit: autoscale controller parses external metric, computes recommendation. Integration: mock metrics endpoint, verify scale-up when target exceeded.

### M166 — Placement Integration Tests
**What exists:** Individual scheduler tests. No combined placement test suite.
**Deliverables:**
- End-to-end suite: per-node + priority preemption on full cluster
- Colocate + zone spread interaction
- Stateful shutdown during drain with disruption budgets
- Job TTL cleanup, external metrics driving scale-up
- Update design/dsl-reference.md with all new placement fields
**Dependencies:** M159–M165
**Tests:** Multi-service test combining per-node, priority, colocate, zone spread, and stateful shutdown in a 4-node simulated cluster.

---

## Phase C — Networking & Service Discovery (M167–M171)

CCattler has overlay networking, VIP load balancing, and network policies. These
milestones add cluster DNS, L7 routing, and mesh capabilities using existing
CCattler primitives.

### M167 — Cluster DNS
**What exists:** Container `/etc/resolv.conf` points to node. No cluster DNS.
**Deliverables:**
- Deploy CoreDNS as a CCattler service with `placement { mode per-node }`
- Auto-generate DNS records: `<service>.svc.ccattler.local`
- Watch service/endpoint facts, update CoreDNS config via reconciler
- Headless services: A records per instance
**Dependencies:** M159 (per-node placement mode)

### M168 — Ingress Controller
**What exists:** VIP data plane (nftables DNAT). No L7 routing.
**Deliverables:**
- DSL `ingress "name" { host "app.example.com" path "/" service "frontend" port 8080 }`
- IngressController reconciles Caddy/Envoy config from ingress facts
- TLS termination with auto-generated or user-provided certs
- Path-based and host-based routing
**Dependencies:** M167 (DNS for backend resolution)

### M169 — Service Mesh via Groups
**What exists:** Network policy controller, SPIFFE identity per workload, `group { share network }`.
**Deliverables:**
- DSL `mesh { mtls "strict" }` on a service — compiler auto-adds proxy process to service group
- Proxy uses existing SPIFFE identity for mTLS between services
- Traffic metrics (request count, latency, error rate) per service pair as derived facts
- Uses group network sharing — no sidecar injection webhook
**Dependencies:** M168

### M170 — Network Policy Egress Rules
**What exists:** NetworkPolicyController with ingress rules only.
**Deliverables:**
- DSL `network_policy "name" { egress { to_cidr "10.0.0.0/8" port 443 } }`
- nftables output chain rules for egress filtering
- Default-deny egress when any egress policy applies
- DNS-based egress rules (`to_fqdn "api.example.com"`)
**Dependencies:** M167 (DNS for FQDN resolution)

### M171 — Multi-Cluster Service Discovery
**What exists:** Single-cluster fact store.
**Deliverables:**
- Federated service discovery across multiple CCattler clusters
- `cca cluster register <remote-server>` links clusters
- Exported services visible as `<service>.<cluster>.svc.ccattler.global`
- Cross-cluster health checking
**Dependencies:** M167, M154 (server contexts)

---

## Phase D — Storage & State (M172–M174)

CCattler has config/secret management. Stateful services already have ordinal
IDs and ordered startup. These milestones add persistent volume support as DSL
fields on services — not as separate PVC/PV resource types.

### M172 — Persistent Volumes
**What exists:** Config and secrets mounted into containers. No block/file storage.
**Deliverables:**
- DSL `volume "data" { size "10Gi" mount "/data" }` as a service block
- Volume controller manages lifecycle: provision → attach → mount → unmount → delete
- Local provisioner (host path with quota enforcement)
- Volume binding at scheduling time (scheduler considers volume locality)
- Per-instance volumes for stateful services (volume-per-ordinal, not shared)
**Dependencies:** None

### M173 — Volume Snapshots
**What exists:** Nothing.
**Deliverables:**
- `cca volume snapshot create <service> --name <snap>`
- Snapshot stored as filesystem copy (local provisioner)
- Restore from snapshot to new volume
- Snapshot retention policy (count-based, age-based)
**Dependencies:** M172

### M174 — Storage Plugin Interface
**What exists:** Local volume provisioner (M172).
**Deliverables:**
- CSI-compatible plugin model for external storage backends
- Driver registration and capability discovery
- External provisioners (NFS, cloud block storage)
- Volume attach/detach lifecycle for remote volumes
**Dependencies:** M172

---

## Phase E — Observability & Operations (M175–M178)

CCattler has events, logs, and basic status. Production clusters need metrics,
tracing, quotas, and better debugging tools.

### M175 — Prometheus Metrics Endpoint
**What exists:** `cca top` shows CPU/memory. No Prometheus scraping.
**Deliverables:**
- `/metrics` endpoint in Prometheus exposition format
- Controller metrics: reconcile duration, queue depth, error rate
- Cluster metrics: node count, instance count, service count by state
- Autoscaler metrics: scaling decisions, cooldown status
**Dependencies:** None

### M176 — Resource Quota Controller
**What exists:** Per-container resource limits. No per-team quotas.
**Deliverables:**
- DSL `quota "team-a" { cpu "8" memory "16Gi" services 50 }`
- QuotaController blocks apply when quota would be exceeded
- `cca quota list/describe` shows usage vs limits
- Quota scoped to teams (tenant boundary)
**Dependencies:** None

### M177 — Debug & Troubleshooting CLI
**What exists:** `cca exec`, `cca logs`, `cca status`, `cca events`.
**Deliverables:**
- `cca debug <service>` — comprehensive service health report
- `cca describe <resource>` — detailed resource view with events
- `cca diff <file>` — show what would change without applying
- `cca explain <service>` — why instances are placed where they are
**Dependencies:** None

### M178 — Distributed Tracing
**What exists:** Nothing.
**Deliverables:**
- Trace context propagation through controller reconciliation
- OpenTelemetry trace export (OTLP)
- Trace correlation: API request → controller reconcile → agent action
- `cca trace <request-id>` shows end-to-end trace
**Dependencies:** M175

---

## Phase F — Extensibility (M179–M183)

CCattler's fact store is the natural extension point — any controller can write
arbitrary prefixes. These milestones add schema validation, compiler-level
policy, and out-of-process controller transport. No CRDs, no admission webhooks,
no Operator SDK.

### M179 — Custom Fact Types with Schema Validation
**What exists:** Fact store accepts any key prefix. `custom/` convention exists. PolicyGate pipeline.
**Deliverables:**
- Register schema at `meta/fact-type/{typeName}` with field definitions and validation rules
- PolicyGate validates writes to `custom/{type}/` against registered schema
- `cca get <typeName>` auto-discovers custom types from `meta/fact-type/` prefix
- `cca describe fact-type <name>` shows the registered schema
- No API group versioning — facts are key-value pairs with optional schema enforcement
**Dependencies:** None
**Tests:** Register "database" fact type, apply invalid → rejected, apply valid → committed, `cca get database` lists it.

### M180 — DSL Admission Policy Rules
**What exists:** PolicyDecl supports ABAC capability grants. PolicyGate has staged evaluation.
**Deliverables:**
- Extend DSL: `policy "no-latest" { on apply match { service where image =~ ":latest$" } deny "not allowed" }`
- Mutating: `policy "add-owner" { on apply match { service } mutate { set label "managed-by" = "ccattler" } }`
- Compiler emits `admission/policy/{name}/...` facts, PolicyGate evaluates them
- Policies are .cca files — no webhook servers, no HTTP round-trips
- Escape hatch: `validate_url "https://..."` for cases that genuinely need a remote call
**Dependencies:** None
**Tests:** Apply deny policy, then matching service → rejected. Apply mutate policy → labels injected.

### M181 — Controller SDK gRPC Transport
**What exists:** CustomController interface (`controllers/sdk.go`), `TestEcosystem_ControllerSDKPlugin` proves in-process plugins work.
**Deliverables:**
- ControllerService protobuf: `Reconcile(stream FactChange) returns (stream ProposedChange)`
- Runner streams fact changes over gRPC to registered external controllers
- `cca controller register --name X --address host:port` writes `meta/controller/{name}/address`
- Health monitoring via gRPC health check, auto-deregister after 3 failed checks
**Dependencies:** None
**Tests:** Unit: gRPC server streams facts, client returns changes. Integration: out-of-process controller binary reconciles a fact through gRPC.

### M182 — DSL Templates & Imports
**What exists:** Fixed DSL with built-in blocks only.
**Deliverables:**
- Templates: `template "web-app" (imageName, portNumber) { service $name { image $imageName; expose $portNumber } }`
- Imports: `import "lib/common.cca"` resolved relative to importing file
- Usage: `use "web-app" "nginx:1.27" 8080`
- Pure substitution (no conditionals, no loops) — configuration language, not programming
- Standard library at `stdlib/` with common patterns: web-app, worker, stateful-db
**Dependencies:** None
**Tests:** Parser resolves import, expands template, catches circular imports. Integration: apply file using template → services appear correctly.

### M183 — Controller Scaffolding CLI
**What exists:** Nothing.
**Deliverables:**
- `cca controller init <name>` scaffolds Go project: main.go with gRPC client, reconcile.go stub, Makefile, test harness with MemoryStore
- Scaffold includes working example (alert controller) that compiles and tests out-of-box
- `cca controller init --language python` for Python scaffold with gRPC stubs
- NOT an operator lifecycle manager — just a project template for fact-watching reconcilers
**Dependencies:** M181
**Tests:** Scaffold generates valid Go that compiles. Integration: init → build → register → reconcile a test fact.

---

## Phase G — Production Hardening (M184–M187)

Final milestones for production-grade reliability.

### M184 — Etcd Backup & Restore
**What exists:** etcd is the fact store. No backup tooling.
**Deliverables:**
- `cca backup create [--output <file>]` snapshots etcd
- `cca backup restore <file>` restores from snapshot
- Scheduled automatic backups with retention policy
- Point-in-time restore to specific revision
**Dependencies:** None

### M185 — Control Plane HA
**What exists:** Single-server control plane.
**Deliverables:**
- Multi-server control plane with leader election
- `cca server --join <existing>` adds a control plane replica
- Automatic failover when leader becomes unhealthy
- Split-brain protection via etcd quorum
**Dependencies:** M184 (backup before HA migration)

### M186 — Cluster Upgrade Controller
**What exists:** No rolling upgrade mechanism for CCattler itself.
**Deliverables:**
- `cca upgrade plan` shows current vs available versions
- `cca upgrade apply` performs rolling control-plane + agent upgrade
- Drain → upgrade → uncordon per node
- Rollback on upgrade failure
**Dependencies:** M185 (HA for zero-downtime upgrade)

### M187 — Conformance Test Suite
**What exists:** Integration tests cover individual features.
**Deliverables:**
- End-to-end conformance suite: 100+ test cases across all phases
- `cca conformance run` executes against a live cluster
- Test matrix: single-node, multi-node, HA, multi-tenant
- Conformance badge for certified CCattler clusters
**Dependencies:** All previous milestones

---

## Dependency Graph

```
Phase A (Auth)         Phase B (Scheduling)     Phase C (Networking)
M153 ─→ M154          M159 (standalone)         M159 ─→ M167 ─→ M168
M155                   M160 (standalone)         M168 ─→ M169
M156                   M161 (standalone)         M167 ─→ M170
M157                   M162 (standalone)         M167+M154 ─→ M171
M153-157 ─→ M158       M163 (standalone)
                       M164 (standalone)         Phase D (Storage)
                       M165 (standalone)         M172 ─→ M173
                       M159-165 ─→ M166          M172 ─→ M174

Phase E (Observability)  Phase F (Extensibility)  Phase G (Production)
M175 (standalone)        M179 (standalone)        M184 ─→ M185 ─→ M186
M176 (standalone)        M180 (standalone)        M184-186 ─→ M187
M177 (standalone)        M181 (standalone)
M175 ─→ M178             M182 (standalone)
                         M181 ─→ M183
```

## Sequencing Strategy

1. **Phase A** first — multi-user auth is a prerequisite for real-world use
2. **Phase B** can start in parallel with Phase A (no auth dependency)
3. **Phase C** starts after M159 (per-node mode needed for DNS) + M154 (contexts for multi-cluster)
4. **Phase D** independent — start any time
5. **Phase E** independent — start any time
6. **Phase F** after Phase A + B basics (extensibility assumes a working cluster)
7. **Phase G** last — HA and upgrades cap the maturity curve

## What Was Removed (and Why)

| K8s Pattern | Why Removed | CCattler Alternative |
|---|---|---|
| DaemonSet controller | New resource type + controller for a scheduling concern | `placement { mode per-node }` on regular service (M159) |
| PriorityClass objects | Separate resource type for a number | `priority N` field on service (M160) |
| Pod affinity label selectors | Complex match expressions for "put near X" | `placement { colocate "svc" }` — name the service directly (M161) |
| Taints & tolerations | Complex effect/operator/value for "don't run here" | `restrict` constraint already exists in scheduler |
| StatefulSet controller | New resource type for ordered startup | `stateful true` already exists with ordinals + ordered startup (M163 adds shutdown order) |
| CRD + schema registry | API group versioning for custom data | Custom fact prefixes + `meta/fact-type/` schema (M179) |
| Admission webhooks | HTTP round-trips for policy | DSL policy rules compiled into PolicyGate (M180) |
| Operator SDK + lifecycle | K8s operator ceremony | `cca controller init` scaffolds a fact-watching reconciler (M183) |
| HPA v2 (separate milestone) | Separate resource type for metrics | `scale { horizontal { target external ... } }` on existing autoscaler (M165) |
| Sidecar injection webhook | Mutating admission for mesh proxies | `mesh { mtls "strict" }` → compiler adds proxy to service group (M169) |
| Password-based auth | K8s removed it in v1.19 as insecure | OIDC-first + webhook auth (M153, M155) |
