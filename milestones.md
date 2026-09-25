## Implementation Phases

### Phase 0 — Foundation
- [x] Language: **Go**
- [x] Backing store: **etcd**
- [x] etcd key layout & consistency model → see [etcd-schema.md](etcd-schema.md)
- [x] Set up repo: `lang/`, `store/`, `scheduler/`, `controllers/`, `agent/`, `api/`, `cli/`, `types/`
- [x] Add: `runtime/` (simulator, process, container adapters), `security/` (authZ, quota, network), `identity/` (local, mTLS, OIDC)

### Phase 1 — Fact Store
- [x] Define the store interface (Get/Put/Delete/Scan/Watch/Transaction)
- [x] Implement in-memory store (for tests and local dev)
- [x] Implement etcd adapter (distributed production) — see Phase 15
- [x] Store integration tests — concurrency, watch ordering, transaction conflicts

### Phase 2 — Domain Language & Parser
- [x] Design formal grammar for the DSL
- [x] Write lexer/parser → AST
- [x] AST → facts compiler (the human-to-machine boundary)
- [x] Validation layer — image refs, port ranges, resource units, duplicates
- [x] `apply` command — parse file → compile → transactionally write facts

### Phase 3 — Reconciliation Engine
- [x] Controller framework — generic watch → reconcile → write loop
- [x] Instance controller (desired vs actual instance count)
- [x] Endpoint controller (running instances → endpoint facts)
- [x] Failure controller (dead instances/nodes → replacement facts)
- [x] Simulator runtime (fake world — no real processes, for semantic testing)
- [x] Deterministic reconciliation tests (state A + observation B + policy C → state D)

### Phase 4 — Scheduler
- [x] Scoring function: `schedule(requirements, nodes) → placement`
- [x] Integration with instance controller
- [x] Anti-affinity / spread rules
- [x] Resource accounting (allocated vs available per node)

### Phase 5 — Single Machine (M2 target)
- [x] Process runtime adapter (Linux processes, not containers yet)
- [x] End-to-end on laptop: `cca apply web.ccl` → facts → reconciler → real processes
- [x] `cca status` showing SERVICE / DESIRED / RUNNING / CPU
- [x] `cca metric set web cpu 90` for simulated autoscaling feedback
- [x] Container runtime adapter (containerd) — pull, start, stop
- [x] Health checking (HTTP, TCP, exec probes)
- [x] Graceful shutdown (SIGTERM → grace period → SIGKILL)
- [x] Node agent with observer/reconciler/reporter

### Phase 6 — Three Machines (M3 target)
- [x] Logical node simulation (3 fake nodes on one laptop)
- [x] Multi-node agent registration, leases, heartbeats
- [x] Distributed scheduling across nodes
- [x] Node failure detection (lease expiry → unreachable → reschedule)
- [x] `service web { instances 10 }` distributes across nodes
- [x] Simulated node kill → verify convergence

### Phase 7 — Networking
- [x] Instance IP allocation from pool
- [x] Endpoint aggregation
- [x] DNS / service discovery (CoreDNS integration or custom)
- [x] Load balancing (per-node proxy or centralized)

### Phase 8 — Storage
- [x] Volume facts and attach/mount lifecycle
- [x] Constraint enforcement (exclusive attach, node compatibility)
- [x] Storage driver interface (local, NFS, cloud block)

### Phase 9 — Failure & Chaos Testing
- [x] Kill node — does the system reschedule?
- [x] Kill agent — does the lease expire and trigger recovery?
- [x] Kill scheduler/controller — does another instance take over?
- [x] Network partition — do both sides stay safe?
- [x] Restart etcd — does the cluster converge?
- [x] The only question: **does the system eventually converge to desired state?**

### Phase 10 — Policies & Autoscaling
- [x] Unified scaling engine (signal → policy → recommendation → constraints → desired state)
- [x] Horizontal autoscaling (CPU, memory, RPS targets)
- [x] Vertical autoscaling (resource requirement changes, in-place resize vs replace)
- [x] Event-driven scaling (queue depth, custom metrics)
- [x] Scheduled scaling (time-based minimums)
- [x] Stabilization windows (scale-up 60s, scale-down 5m, no oscillation)
- [x] Multi-policy resolution (single decision function, no competing writers)
- [x] Quota-aware scaling (tenant limits visible, not silently capped)
- [x] Cluster autoscaling (unsatisfied demand → desired nodes → infra provider)
- [x] Intent layers (user, autoscaler, policy — derived effective state)
- [x] Placement constraints (architecture, zone, spread, affinity)
- [x] Rolling update controller with max_unavailable/max_extra
- [x] Rollback on health check failure

### Phase 11 — API, CLI & Security
- [x] Query API (GET/QUERY/APPLY/WATCH)
- [x] CLI tool (apply, get, scale, watch, status)
- [x] Internal CA with ECDSA P-256, short-lived leaf certificates
- [x] mTLS server and client TLS config generation
- [x] Certificate auto-rotation (CertificateRotator with configurable threshold)
- [x] Node enrollment (`cca join` with bootstrap token → CSR → certificate)
- [x] Human auth via OIDC/OAuth2
- [x] RBAC: roles with fact-prefix permissions (6 builtin roles)
- [x] ABAC: attribute-based policies (team isolation, production gates), combined RBAC+ABAC
- [x] Per-controller least privilege (builtin roles: node-agent, scheduler, controller)
- [x] Authorized Store wrapper (authN + authZ on every operation)
- [x] Config subsystem — DSL `config` block (env vars + config files), config facts in store
- [x] Secrets subsystem — AES-256-GCM encrypted store, grant-based access
- [x] Secret grants — `secret(name, path)` in DSL, grant checked before delivery
- [x] Secret delivery — file-mounted, lifecycle-aware (materialize on start, remove on stop)
- [x] Secret rotation — overwrite file on each reconciliation cycle, no restart required
- [x] Node agent config/secret materialization — env vars + secrets resolved and delivered at start time
- [x] Workload-to-workload network policies (identity-based, deny-by-default)
- [x] Immutable audit log (principal, action, target, decision, policy)
- [x] Cluster bootstrap (one-time token, validated once, then destroyed)

### Phase 12 — Multi-Tenancy
- [x] Tenant model (tenant facts, ownership relations)
- [x] Hierarchical naming (`/tenant/service`)
- [x] Resource quotas per tenant (admission checks)
- [x] Fair scheduling (weighted tenant priorities, borrowable guarantees)
- [x] Identity-based network isolation (SPIFFE identities, derived firewall rules)
- [x] Secret isolation (tenant-scoped, encrypted delivery)
- [x] Shared service exports/imports
- [x] Tenant lifecycle (create → provision boundaries, delete → garbage collect)
- [x] Policy gates pipeline (syntax → schema → authZ → quota → security → mutation → commit)
- [x] Per-tenant audit views

### Phase 13 — Extensibility & Hardening
- [x] Typed fact schemas for plugins
- [x] Custom controller SDK
- [x] Append-only event log for audit trail
- [x] Metrics (reconciliation latency, scheduling decisions, instance transitions)
- [x] Multi-node control plane with leader election (3 or 5 control-plane nodes)
- [x] Stateless controllers — multiple copies, shared state, automatic failover

### Phase 14 — Store & Controller Correctness
- [x] Deep-copy fact values on read paths (Get returns shared []byte with internal map)
- [x] ErrStoreClosed guards on all store methods (Get/Put/Delete/Scan/Watch/Transaction reject after Close)
- [x] Idempotent Put (skip revision bump and event when value is unchanged)
- [x] Atomic transactions (one transaction = one revision, not one per operation)
- [x] Watch context cancellation (unregister watcher when ctx.Done fires)
- [x] Runner errgroup lifecycle (cancel all controllers on failure, wait for all to finish)
- [x] Periodic resync timer (30s full reconciliation as correctness backstop)
- [x] Controller restart with exponential backoff (100ms → 500ms → 1s → 2s → 5s → 30s max)
- [x] Health check interval scheduling (respect DSL `every 10s` instead of checking every tick)

### Phase 15 — Distributed State (etcd)
- [x] Add go.etcd.io/etcd/client/v3 dependency
- [x] Implement EtcdStore (store/etcd.go) — full StateStore interface over etcd v3 API
- [x] Key prefix namespacing — multiple CCattler clusters can share one etcd instance
- [x] Idempotent Put — Get-compare-skip to avoid unnecessary revision bumps
- [x] Watch bridging — etcd watch channel → CCattler Event channel with goroutine forwarding
- [x] Transaction mapping — Compare/Op → etcd Txn (Revision==0 → CreateRevision==0 check)
- [x] Integration tests (store/etcd_test.go) — build tag `etcd_integration`, 15 test functions
- [x] Wire EtcdStore into CLI (`cca run --store etcd --endpoints localhost:2379`)

### Phase 16 — Multi-Process Architecture
- [x] `cca server` command — runs controllers + API against shared etcd store
- [x] `cca agent --node-id <id>` command — runs node agent against shared etcd store
- [x] Update `cca apply` to accept `--store etcd` for writing facts to shared store
- [x] End-to-end test: etcd + server + agent + apply workflow

### Phase 17 — Init Lifecycle & Observability
- [x] Init step types — `InitStepState` (pending/running/succeeded/failed), `InitPhase` (pending/running/complete/failed)
- [x] Init step fact keys — desired step definitions (exec, timeout, retry) and observed step state/reason
- [x] DSL `init` block — parsed into `InitStepDecl`, compiled to init step facts per service
- [x] Init controller — watches desired init steps + observed results, derives per-instance init phase
- [x] Agent init execution — sequential steps with timeout, exponential backoff retry, state reporting
- [x] Runtime `Exec` interface — ExecSpec on all three runtime adapters (simulator, process, container)
- [x] Observability telemetry — agent collects per-node workload count and per-instance CPU/memory
- [x] `cca top` command — node and workload resource utilization tables
- [x] API enrichment — InstanceStatus includes CPU, memory, init phase, restarts; NodeStatus includes utilization
- [x] Init controller wired into all runner creation sites

### Phase 18 — Probes & Readiness Gates
- [x] Probe state types — `StartupProbeState` (pending/succeeded/failed), `LivenessProbeState` (healthy/unhealthy/unknown), `ReadinessProbeState` (ready/not-ready/unknown)
- [x] Probe fact keys — desired probe config (method, path, port, interval, timeout, thresholds, initial_delay) and observed probe state per instance
- [x] DSL `startup`, `liveness`, `readiness` blocks — parsed into `ProbeDecl`, compiled to probe facts per service
- [x] Agent probe execution engine — per-instance per-probe-type tracking with consecutive success/failure counters
- [x] Startup gating — liveness and readiness probes blocked until startup probe succeeds
- [x] Readiness-gated endpoints — endpoint controller only includes instances with readiness=ready (or no readiness probe configured)
- [x] API enrichment — InstanceStatus includes startup, liveness, readiness probe state fields
- [x] Reuses existing CheckHealth infrastructure for HTTP and TCP probes

### Phase 19 — Container Default & Remote Management
- [x] Container runtime as default — `cca agent` defaults to `--runtime container`, process runtime is opt-in via `--runtime process`
- [x] MCP server (`cmd/mcp/`) — JSON-RPC 2.0 over stdio, 14 guardrailed tools for remote management
- [x] MCP guardrails — no arbitrary command execution, validated parameters, component allowlist, output truncation
- [x] MCP tools: git (status/log/pull), build, test, cluster status, process list, container list, component logs, stop/start/deploy, apply config, disk usage
- [x] Injection prevention — command injection blocked (exec.Command, no shell), path traversal blocked, package path regex validation
- [x] Auto-logging hook — PostToolUse hook on Bash tool auto-logs commands to COMMAND_HISTORY.md with sensitive data filtering
- [x] Sensitive data redaction — SSH key paths, passwords, tokens, Bearer headers, long base64 strings filtered from command log
- [x] Auth token — `--token` or `--token-file` flag, constant-time comparison, blocks all requests if invalid
- [x] Read-only mode — `--read-only` flag hides mutation tools from list and blocks execution
- [x] Structured audit log — `--audit-log` flag writes JSON entries (timestamp, tool, args, duration, success/error) with sensitive arg redaction

### Phase 20 — Multi-Host Server
- [x] `--listen` flag — `cca server` binds to configurable address (default `0.0.0.0:9770`) instead of hardcoded localhost
- [x] `--tls` flag — auto-generates ephemeral CA and server certificate with auto-rotation
- [x] mTLS enforcement — API server requires and verifies client certificates via TLS 1.3
- [x] CA certificate output — writes `ca.pem` to `.ccattler/` data directory for agent/client trust
- [x] Certificate auto-rotation — `CertificateRotator` renews server cert at 70% of TTL
- [x] Rotator-based mTLS test — validates rotator + mTLS handshake + unauthenticated rejection
- [x] `--cert`/`--key`/`--ca` flags — load external PEM certificates for server and agent (replaces auto-CA)
- [x] `loadServerTLSConfig()` — reads cert/key/CA from disk, returns mTLS tls.Config
- [x] Agent TLS flags — `cca agent --cert/--key/--ca` for agent-side mTLS credential loading
- [x] Vagrant + Ansible deployment — `deploy/` directory with dual-provider Vagrantfile (libvirt + VirtualBox) and 5 Ansible roles
- [x] Deployment plan updated — dual-provider testing on Linux, libvirt primary, VirtualBox secondary
- [x] Multi-host demo — real cluster across .43 (ctrl+worker-2) and .215 (worker-1), 4 nginx + 2 redis containers, accessible from LAN

### Phase 21 — VIP Data Plane
- [x] `DataPlaneProvider` interface — pluggable VIP forwarding abstraction (`ReconcileVIPDataPlane`, `Cleanup`)
- [x] `ServiceVIPConfig` / `DataPlaneBackend` types — describe desired forwarding state per service
- [x] `IptablesDataPlane` implementation — iptables nat-table DNAT rules with round-robin via statistic module (--mode nth), per-service chains (`CCA_SVC_*`), `CCA_SERVICES` main chain, `cca0` dummy interface for VIP addresses
- [x] `SimulatorDataPlane` implementation — records reconciliation calls for test verification
- [x] Agent data plane reconciliation — reads VIP + endpoint facts, resolves local (container IP) vs remote (host IP + host port) backends, calls data plane provider each tick
- [x] Cross-host backend resolution — local instances use container IP:port, remote instances use node advertise address + Docker host port mapping
- [x] Node advertise address — `--advertise-address` flag on agent, published to store for cross-host resolution
- [x] Host port tracking — `ContainerRuntime.HostPortForInstance()`, agent publishes host port facts after container start
- [x] Fact keys — `observed/node/{id}/address` for node LAN address, `observed/instance/{id}/hostport` for Docker host port mapping
- [x] Local backend host port resolution — agents use `127.0.0.1:<hostPort>` for local backends instead of container IP:port when no Docker network IP is assigned
- [x] POSTROUTING MASQUERADE for cross-host DNAT — fixes source address for packets DNAT'd to remote hosts (mirrors kube-proxy masquerade)
- [x] End-to-end test — `curl http://10.200.0.1:80` round-robins across 4 nginx containers on .43 and .215, redis VIP returns `+PONG`

### Phase 22 — Node Enrollment
- [x] `POST /api/enroll` endpoint — validates join token, issues certificate, binds RBAC role, returns cert/key/CA
- [x] Server enrollment wiring — auto-CA mode creates `EnrollmentService`, TLS uses `VerifyClientCertIfGiven` with middleware enforcing client certs on all non-enrollment endpoints
- [x] `cca token create` — generates join token stored in etcd, prints join command
- [x] `cca token list` — shows active (non-expired) tokens with masked values
- [x] `cca token revoke <token>` — removes a join token from the store
- [x] `cca join <server> <token> --node-id <id>` — contacts server, presents token, receives and writes cert/key/CA to `.ccattler/`
- [x] `--ca-cert` flag for join — verifies server certificate against provided CA (recommended for production)
- [x] Agent cert auto-discovery — detects `.ccattler/node.pem`, `node-key.pem`, `ca.pem` when no `--cert/--key/--ca` flags provided
- [x] Local IP detection — `cca join` automatically includes node's non-loopback IPv4 addresses in the certificate
- [x] Enrollment tests — token validation, certificate issuance, token consumption (one-time use), field validation

### Phase 23 — Service Networking & Placement
- [x] Cross-host endpoints — EndpointController uses nodeAddress:hostPort instead of 127.0.0.1 for remote instances
- [x] DNS server wired to CLI — `cca server --dns` starts UDP DNS resolving `*.ccattler.local` → VIP (default `:15353`)
- [x] UserSpaceProxy HTTP reverse proxy — routes by Host header with round-robin load balancing, replaces unreliable iptables DNAT
- [x] Proxy wired to CLI — `cca agent --proxy` starts HTTP proxy on configurable address (default `0.0.0.0:80`)
- [x] Placement fact keys — `placement/require/{label}`, `placement/prefer/{label}`, `placement/accept/{label}`, `observed/node/{id}/label/{label}`, `observed/node/{id}/restrict/{label}`
- [x] Placement DSL — `require label = value` (hard constraint), `prefer label = value` (soft preference), `accept label` (tolerate restricted nodes)
- [x] Scheduler placement logic — filters by require labels and node restrictions, scores by prefer labels, combined with existing architecture/zone/resource constraints
- [x] Human-readable naming — replaces K8s "affinity/anti-affinity/taints/tolerations" with require/prefer/restrict/accept
- [x] Ansible DNS + proxy — server template enables `--dns`, agent template enables `--proxy` by default
- [x] Ansible examples download — controlplane role fetches `.ccattler` examples from GitHub repo at deploy time
- [x] Ansible testapp role — deploys test service after cluster is up
- [x] Install script Ansible fix — `install.sh` and `install-demo.sh` wrap ansible-playbook in python3 Popen to avoid non-blocking IO errors
- [x] Website install docs — clarified passwordless SSH prerequisite and inventory editing flow

### Phase 24 — CLI & UX
- [x] `cca describe <service|node|instance>` — detailed single-resource view showing all related facts, health state, placement, recent events
- [x] `cca events [--follow] [--service <name>]` — real-time event stream with optional filtering by service
- [x] `cca diff <file>` — dry-run apply that shows what facts would change (added/modified/removed) without committing
- [x] ~Colored terminal output~ — dropped: data is already readable without colors, not worth the complexity
- [x] Better error messages — DSL parse errors show line/column with source context, runtime errors suggest corrective actions
- [x] Shell completions — bash and zsh completion scripts for commands, subcommands, and resource names (generated from `cca get` output)
- [x] `cca get` column formatting — aligned columns, human-readable durations (e.g. "3m ago" instead of timestamps), truncation for long values
- [x] API `describe` endpoint — `GET /api/describe?type=service&name=web` returns aggregated detail view

### Phase 25 — Observability
- [x] Prometheus `/metrics` endpoint on API server — reconciliation duration, scheduling decisions, instance state transitions, store operation latency, active watches
- [x] Structured JSON logging — configurable log level (debug/info/warn/error), JSON format for machine consumption, human-readable format for terminal
- [x] `cca logs <service> [--follow] [--instance <id>]` — aggregate container stdout/stderr logs across instances
- [x] Health endpoint — `GET /healthz` returns controller health, etcd connectivity, certificate expiry status
- [x] Grafana dashboard templates — cluster overview (nodes, instances, services), per-service detail (instances, health, restarts), per-node detail (CPU, memory, workloads)
- [x] Alert rule templates — node unreachable, instance crash-looping, scheduling failures, certificate approaching expiry, etcd latency

### Phase 26 — P0 Correctness
- [x] Runtime observation authoritative — add `InstanceStarting` state, agent observes via `Status()` after `Start()` instead of treating action as truth
- [x] Remove 127.0.0.1 fallback — no IP assigned without NetworkProvider, health/probes skip when IP unavailable
- [x] Watch overflow → full resync — agent handles `EventOverflow` on placement watch, `Watch()` contract documented
- [x] Init restart-safety — stale "running" init steps treated as failed on agent restart, re-executed with retries
- [x] Decouple probes from reconciliation — independent `runProbeScheduler` goroutine with own ticker
- [x] Race/concurrency tests — agent runs with `-race`, concurrent reconciliation + probes verified safe
- [x] Agent restart lifecycle tests — restart during starting, running, and init phases all converge correctly

### Phase 27 — Storage Resilience
- [x] Volume migration on node failure — VolumeMigrating state, migration metadata (source node), force-detach → migrating → reattach lifecycle, migration metadata cleared after reattach
- [x] Volume snapshot before migration — SnapshotVolume in StorageProvider, controller snapshots before force-detach, last_snapshot fact recorded
- [x] Storage health monitoring — VolumeUsage in StorageProvider, agent reports used_bytes/capacity_bytes, `cca top volumes` command
- [x] Volume resize — ResizeVolume in StorageProvider, controller detects desired size != observed size, expand-only
- [x] Volume replication — desired replicas config, replica_count/replica_state observed facts, syncing state on scale-up

### Phase 28 — Identity DSL & Facts
- [x] `CloudIdentityDecl` AST node — provider, role, service_account, pool, client_id, tenant_id
- [x] `CloudIdentityBindingDecl` AST node — identity name, mount path, deliver mode (credentials or token)
- [x] `CredentialBrokerDecl` AST node — oidc_issuer, credential_ttl, refresh_before
- [x] DSL `cloud_identity` top-level block — provider-specific fields (aws: role ARN, gcp: service_account+pool, azure: client_id+tenant_id)
- [x] DSL `cloud_identity` in service block — binding with mount path and deliver mode
- [x] DSL `credential_broker` top-level configuration block
- [x] DSL validation — provider-specific required fields (aws: role, gcp: service_account+pool, azure: client_id+tenant_id), unknown provider rejection
- [x] Compiler — cloud identity facts (`desired/cloud_identity/{name}/...`), service binding facts, broker config facts
- [x] Fact key functions — `KeyDesiredCloudIdentity*`, `KeyDesiredServiceCloudIdentity*`, `KeyObservedCredential*`, `KeyDesiredCredentialBroker*`
- [x] Scan prefix constants — `ScanDesiredCloudIdentities`, `ScanDesiredServiceCloudIdentities`, `ScanObservedCredentials`

### Phase 29 — OIDC Infrastructure
- [x] OIDC signing key — ECDSA P-256 key pair for JWT signing, deterministic key ID from public key hash, `NewWorkloadTokenIssuer` and `NewWorkloadTokenIssuerWithKey`
- [x] OIDC token issuer — `MintWorkloadToken(spiffeID, audience, ttl)` creates signed ES256 JWTs with SPIFFE subject, issuer, audience, iat/exp claims, kid header
- [x] OIDC discovery endpoint — `GET /.well-known/openid-configuration` on API server via `SetWorkloadTokenIssuer`, returns issuer, jwks_uri, supported algs
- [x] OIDC JWKS endpoint — `GET /oidc/jwks` serves public signing key as JWK with EC P-256 coordinates, padded to 32 bytes

### Phase 30 — Cloud Provider Adapters
- [x] Cloud provider adapter interface — `CloudProviderAdapter` with `ExchangeToken(ctx, jwt, identityConfig) → CloudCredential` and `ProviderName()`
- [x] AWS STS adapter — `AWSSTSAdapter` validates role ARN, stub for `AssumeRoleWithWebIdentity` (requires real AWS endpoint)
- [x] GCP STS adapter — `GCPSTSAdapter` validates service_account+pool, stub for Google Security Token Service
- [x] Azure adapter — `AzureADAdapter` validates client_id+tenant_id, stub for Azure AD federated credential exchange
- [x] Credential store — `CredentialStore` with AES-256-GCM encrypted storage at `credentials/` prefix, put/get/delete/list + `SimulatorCloudAdapter` for tests

### Phase 31 — Credential Broker
- [x] Credential broker controller — `CredentialBrokerController` watches cloud identities, service bindings, running instances; mints JWT via `WorkloadTokenIssuer`, exchanges via `CloudProviderAdapter`, writes state facts
- [x] Credential broker proactive refresh — `needsRefresh()` checks expires_at against configurable `refreshBefore` window, re-issues when within window
- [x] Credential broker garbage collection — deletes state/expires_at/issued_at/error facts for instances no longer running or no longer existing
- [x] Credential state facts — `observed/credential/{instance}/{identity}/state` (active/error), `/expires_at`, `/issued_at`, `/error` written by broker reconciliation

### Phase 32 — Agent Credential Materialization
- [x] `CredentialProvider` interface in agent — `GetCredentialForInstance(ctx, instanceID, identityName) → MaterializedCredential`
- [x] `MaterializedCredential` struct — provider, keys, token, expiry, deliver mode, mount path
- [x] `CredentialFileContent()` — renders provider-specific file content and filename
- [x] `TrackedCredential` — tracks materialized credentials for cleanup on instance stop
- [x] AWS credential file format — INI-style `[default]` with access key, secret key, session token
- [x] GCP credential file format — Application Default Credentials JSON with external_account type and token file reference
- [x] Azure credential file format — raw access token file for Azure SDK
- [x] Token projection mode — `deliver token` returns raw JWT directly, filename "token"

### Phase 33 — Identity RBAC, CLI & Tests
- [x] RBAC role `credential-broker` — reads cloud_identity + service + credential_broker + instance facts, writes observed/credential and credentials/ store
- [x] RBAC update `node-agent` — added read access to `credentials/` and `observed/credential/` prefixes
- [x] `CloudIdentityStatus` in API — status endpoint includes cloud identities with provider and bound services
- [x] `cca get cloud-identities` — lists declared identities with NAME, PROVIDER, SERVICES columns
- [x] RBAC tests — credential-broker can read identities/write credentials, node-agent can read but not write credentials
- [x] API test — status endpoint includes cloud identity data with service bindings
- [x] Prior phases include deterministic tests: SimulatorCloudAdapter verifies credential lifecycle (8 broker tests), agent format tests (6 tests)

### Phase 34 — API Horizontal Scalability
- [x] Separate API server from controller runner — API can be deployed as independent replicas
- [x] Stateless API replicas — all reads/writes go directly to etcd, no local state
- [x] Leader election only for controllers — API replicas serve requests regardless of leadership
- [x] `cca server --api-only` flag — runs API without controllers (for scaling API independently)
- [x] `cca server --controllers-only` flag — runs controllers without API (leader-elected)
- [x] Load balancer readiness — `/healthz` returns ready only when etcd is reachable
- [x] Watch multiplexing — shared etcd watches across API replicas to reduce etcd load

### Phase 35a — P0 Correctness Pass (Architecture Review)
- [x] etcd Put simplification — removed CAS transaction fallback that defeated optimistic concurrency
- [x] Transaction snapshot CAS — runner guards input-set facts (not just output keys) in transactions, capped at etcd 128-op limit
- [x] VIP allocation race — resolved by input-set CAS (network VIP facts protected during allocation)
- [x] `derived/` prefix — controller-derived state separated from agent-observed state (`derived/service/`, `derived/instance/`, `derived/credential/`)
- [x] Migrated clean keys — rollout state, init phase, drain_since, credential lifecycle moved from `observed/` to `derived/`
- [x] Init dual authority resolved — agent no longer writes init phase; InitController is sole authority via `derived/instance/{id}/init/phase`
- [x] RBAC updated — `controller`, `credential-broker`, `node-agent` roles include `derived/` prefix permissions

### Phase 35 — Cloud Controller Manager
- [x] `CloudProvider` interface — node lifecycle (add/remove cloud instances), cloud load balancers, cloud routes
- [x] AWS cloud provider — EC2 instance management, ELB/NLB service load balancers, VPC route table entries (stub, requires AWS SDK)
- [x] GCP cloud provider — GCE instance management, Cloud Load Balancing, VPC routes (stub, requires Google Cloud SDK)
- [x] Azure cloud provider — VM management, Azure Load Balancer, route tables (stub, requires Azure SDK)
- [x] Node lifecycle controller — detect terminated cloud instances, cordon + drain, remove stale node facts
- [x] Cloud load balancer controller — watch services with `expose external`, create/update/delete cloud LBs
- [x] Cloud route controller — program cloud VPC routes for pod-to-pod cross-node networking
- [x] DSL `cloud` top-level block — provider, region, credentials reference, instance types
- [x] DSL `expose external <port> [protocol]` in service block — marks ports for cloud load balancer exposure
- [x] RBAC `cloud-controller` role — least-privilege permissions for cloud controllers
- [x] SimulatorCloudProvider — in-memory cloud provider for testing with call tracking
- [x] CLI `cca server --cloud-provider <name> --cloud-region <region>` — enables cloud controllers

### Phase 36 — P1 Correctness Pass (Architecture Review)
- [x] etcd `WithPrevKV()` — watch options include `clientv3.WithPrevKV()` so watch events carry previous values for state-transition detection
- [x] EventProjector from committed state — watches 4 store prefixes (observed/instance, observed/node, placement/instance, effective/service), classifies state transitions into semantic events using Prev field, replaces old `emitEventsForChanges` approach
- [x] `mergeWatchChannels` — goroutine-per-channel fan-in pattern (no busy-poll), output channel closed when all inputs close
- [x] Init runtime isolation — `Runtime.ExecInit(ctx, image, ExecSpec)` routes init commands through the runtime abstraction; container runtime uses `nerdctl run --rm`, process runtime uses host exec, simulator records calls
- [x] Multi-port endpoint model — `extractServiceExposedPorts` returns `map[string][]int`, `KeyEndpoint` takes port parameter, endpoint key is `endpoint/service/{svc}/{instance}/{port}`, one endpoint per port per instance
- [x] Agent sub-reconciler extraction — `executeReconciliationCycle` reduced from 70+ lines to orchestration-only loop; per-instance startup logic extracted to `reconcileDesiredInstance`, teardown to `cleanupUndesiredInstance`

### Phase 37 — Runtime Stats API
- [x] `ResourceStats` type — `CPUMillicores` and `MemoryBytes` fields for observed resource usage
- [x] `Runtime.Stats(ctx, id)` interface method — returns actual resource usage per workload
- [x] SimulatorRuntime Stats — returns spec's requested CPU/memory as synthetic usage for running workloads
- [x] ProcessRuntime Stats — reads /proc/{pid}/stat (CPU ticks) and /proc/{pid}/statm (RSS pages) on Linux
- [x] ContainerRuntime Stats — queries `nerdctl stats --no-stream` and parses CPU percentage + memory usage
- [x] `parseNerdctlStats` / `parseMemoryValue` — parse nerdctl stats output (CPU%, memory with MiB/GiB/KiB units)
- [x] Agent telemetry uses Stats() — `reportWorkloadTelemetry` calls `runtime.Stats()` instead of estimating from desired-state resource requests
- [x] Dead code removed — `estimateInstanceCPU`, `estimateInstanceMemory`, `parseMillicores`, `parseMemoryBytes` removed from telemetry

### Phase 38 — Project Hardening (Audit Findings)
- [x] Makefile — `build`, `test`, `test-race`, `lint`, `bench`, `fuzz` targets
- [x] `.golangci.yml` — linter configuration (govet, staticcheck, errcheck, gosec, ineffassign, unused, gocritic, misspell, gofmt)
- [x] CI test workflow — `.github/workflows/test.yml` runs `go test -race` and `golangci-lint` on push/PR
- [x] Benchmark tests — store (Put/Get/Scan/Transaction/PutParallel), scheduler (10/100/1000 instances), parser (lex/parse/full config)
- [x] Security package tests — authorized store Delete/Scan/Transaction denial, audit JSON serialization, ABAC removal/no-principal, no-principal store access
- [x] Tenant package tests — lifecycle full quota, lifecycle delete cleanup, fair scheduler no-tenants, quota usage updates
- [x] Cloud package tests — simulator terminate-nonexistent, LB backend updates, route idempotency, GCP/Azure full stub verification
- [x] Fuzz testing — `FuzzParse` and `FuzzLexer` with seed corpus covering all DSL constructs
- [x] Structured error types — `CCattlerError` with `ErrorCode` (not_found, already_exists, conflict, invalid_input, unauthorized, forbidden, quota_exceeded, internal, unavailable), `IsErrorCode()` helper

### Phase 39 — Scale-to-Zero (Warm-Zero)
- [x] DSL `idle_timeout` and `activation_timeout` keywords in horizontal scale block — parser, AST fields, compiler emits facts
- [x] Fact keys — `KeyDesiredServiceScaleIdleTimeout`, `KeyDesiredServiceScaleActivationTimeout`, `KeyObservedServiceLastRequestTime`, `KeyDerivedServiceActivationState`
- [x] `WarmZeroController` — watches desired/observed/derived services + instances + endpoints, manages activation state machine (inactive → activating → active → inactive)
- [x] Idle timeout detection — controller transitions active → inactive when `now - last_request_time > idle_timeout`
- [x] Autoscale activation override — `AutoscaleController` watches `ScanDerivedServices`, overrides recommendation to 1 when service is `activating` with recommendation=0
- [x] Proxy cold activation — `UserSpaceProxy` detects warm-zero services (idle_timeout fact present), writes `activating` state, polls for endpoints with 500ms interval, forwards on ready
- [x] Concurrent activation — per-service `activationChannel` (chan struct{}) shared across goroutines, closed once endpoints appear, all waiting requests unblock simultaneously
- [x] Last request time tracking — proxy writes `observed/service/{name}/last_request_time` as Unix millis, throttled to 1 write/sec per service
- [x] Activation timeout — configurable per service (default 30s), proxy returns 503 when exceeded
- [x] Activation webhook — `POST /api/activate?service={name}` writes activation state for non-HTTP triggers (CI, cron, queue consumers)
- [x] Controller wired into all runner creation sites (server, run, run-container)

### Phase 39a — Scale-to-Zero Audit & Hardening
- [x] Security: `types.ValidateResourceName()` at API boundaries — handleMetric, handleActivate, handleScale, handleDescribe, proxy ServeHTTP
- [x] Security: proxy Host header sanitization — rejects path traversal, uppercase, spaces, null bytes via `ValidateResourceName`
- [x] Security: DoS protection — `maxActivationWaiters = 100` per-service cap on concurrent activation goroutines, 503 on overload
- [x] Code quality: unchecked error returns fixed in `tenant/lifecycle.go` (~15 Put calls), `tenant/quota.go` (3 Put calls), `tenant/shared_service.go` (Scan + Delete)
- [x] Code quality: `types.ParseDurationSeconds()` — shared duration parser eliminates duplicate `parseDurationSeconds` in controllers and proxy
- [x] Observability: Prometheus metrics for warm-zero — `ccattler_activation_total` (counter), `ccattler_activation_duration_seconds` (histogram), `ccattler_activation_waiting_requests` (gauge)
- [x] Performance: warm-zero config cache with 5s TTL in `UserSpaceProxy.warmZeroCache` — avoids store reads on every request
- [x] Tests: WarmZeroController edge cases — inactive→active on endpoints, multiple services independent, unknown state no-op, unparseable idle timeout
- [x] Tests: warm-zero integration test — full lifecycle (inactive → activating → autoscaler override → running → active → idle timeout → inactive → scale to zero)
- [x] Graceful proxy shutdown — `httpServer.Shutdown()` with 15s timeout replaces `httpServer.Close()` for in-flight request draining
- [x] Autoscaler fix: min=0 services always participate in autoscaler loop (not skipped when no targets), enabling correct activation override and scale-back-to-zero

### Phase 40 — System Design Patterns
- [x] Circuit breaker for proxy backends — per-backend failure tracking, trip open after 5 consecutive failures, half-open probing, 503 when circuit-broken
- [x] Rate limiter for API server — token-bucket per client IP, 429 with Retry-After header, /healthz and /metrics exempt
- [x] Least-connections load balancing — min-heap selection replaces round-robin, tracks active connections per backend
- [x] Bulkhead isolation for controllers — semaphore-based concurrency limiter per controller, prevents starvation
- [x] Distributed tracing with W3C Trace Context — traceparent header propagation through proxy and API, context injection
- [x] Read-path caching for API status — 2s TTL response cache for expensive status aggregation endpoint
- [x] Dead letter queue for failed events — DLQ under dlq/ prefix with deduplication, retry counting, age-based cleanup
- [x] Change data capture stream from fact store — CDC built on Watch infrastructure, multi-subscriber, prefix-filtered

### Phase 42 — Store Correctness (M39, Gate A)
- [x] WatchOption.StartRevision — resume watches from a known revision, closing the Scan-to-Watch gap
- [x] EventCompacted event type — signals revision was compacted/evicted, consumer must resync
- [x] MemoryStore event history ring buffer — bounded 4096-entry ring for revision-based replay
- [x] MemoryStore Watch with StartRevision — replays matching events from history, emits EventCompacted on eviction
- [x] EtcdStore Watch with StartRevision — passes WithRev() to etcd client
- [x] EtcdStore compaction detection — CompactRevision > 0 emits EventCompacted and closes channel
- [x] Agent EventCompacted handling — triggers full resync on compaction
- [x] Controller runner EventCompacted handling — triggers reconciliation on compaction
- [x] ChangeStream skips EventCompacted — no dispatch for meta-events
- [x] 12 conformance tests: gap-free Scan+Watch, overflow→resync convergence, compaction detection, history wraparound, revision ordering, delete replay, prefix filtering, transaction replay, future revision handling

### Phase 43 — Truth & Reconciliation (M40, Gate B+C)
- [x] Deterministic plan verification — `sortChangesByKey()` in runner guarantees deterministic commit order; instance controller sorts map iterations for deterministic scale-down selection; endpoint controller sorts map iterations for deterministic endpoint ordering
- [x] Controller write domain enforcement — `enforceWriteDomain()` validates every Change key against `controllerOutputPrefixes()`, drops violations with error log and `ccattler_write_domain_violations_total` Prometheus counter
- [x] `controllerOutputPrefixes()` audit — fixed 6 mismatches: failure (added `derived/instance/`), rollout (added `derived/service/`, `desired/service/`), cloud-node-lifecycle/cloud-loadbalancer/cloud-routes (added missing entries), cluster-autoscale (removed phantom declaration)
- [x] Key ownership matrix — `key-ownership-matrix.md` formal document: 16 controller write domains, 12 non-controller writers, 5 shared prefix overlaps with justification, full key prefix taxonomy
- [x] Non-determinism fixes — instance controller and endpoint controller map iterations sorted for reproducible output; runner-level `sortChangesByKey()` as safety net for all controllers
- [x] 10 deterministic plan tests — each controller verified: same snapshot → same plan across 5 iterations with fresh controller instances
- [x] 6 write domain tests — enforcement, all-prefixes acceptance, wrong-prefix rejection, unknown controller passthrough, multi-prefix validation, completeness cross-check

### Phase 44 — Agent Decomposition (M41, Gate D)
- [x] ProbeScheduler struct — extracted from Agent, owns probe state tracking, health check execution, probe scheduling loop; Run() method with pluggable instance discovery; exported CleanupInstance and HasConfiguredProbes
- [x] NodeReporter struct — extracted from Agent, owns telemetry collection, heartbeat writing, node registration, advertise address publishing; CollectAndReportTelemetry, WriteHeartbeat, PublishAliveState, PublishAdvertiseAddress, RegisterNode methods
- [x] DataPlaneReconciler struct — extracted from Agent, owns VIP DNAT reconciliation, backend resolution, advertise address publishing; Reconcile method, package-level publishInstanceHostPort
- [x] Agent composition — Agent struct delegates to ProbeScheduler, NodeReporter, DataPlaneReconciler; Run() wires sub-components; no probe/telemetry/dataplane methods remain on Agent
- [x] Runtime conformance suite — 15 behavioral contract tests exercising Start/Stop/Status/List/Stats/Logs/Exec across SimulatorRuntime and ProcessRuntime via factory pattern
- [x] Failure matrix tests — 7 edge-case tests (empty ID, exec on unknown, stats after stop, concurrent start/stop, env vars, follow logs, exec init) plus 4 implementation-specific tests (exec failure injection, start rejected, container image rejected, logs close)

### Phase 45 — Security Audit (M42, Gate E)
- [x] Formal threat model — `threat-model.md` covering 10 threat categories (etcd compromise, command injection, secret leakage, enrollment MITM, privilege escalation, DoS, API state exposure, enrollment key transport, config path traversal, container escape), trust boundaries, asset inventory, priority matrix
- [x] Command execution audit — 16 `exec.Command`/`exec.CommandContext` call sites across 6 files; ProcessRuntime Start() rejects path traversal and shell metacharacters; ContainerRuntime validates OCI image reference regex; env var key validation (`isValidEnvVarName`) in both runtimes
- [x] Secret leakage audit — API prefix denylist (`isSensitivePrefix`) blocks state/watch queries to `secrets/`, `credentials/`, `enrollment/token/`, `bootstrap/`; node cert permissions fixed (0644 → 0600); config file path traversal protection in `materializeConfigFilesToTempDirectory`
- [x] etcd TLS audit — `EtcdStoreConfig.TLSConfig` field added, `--etcd-cert/--etcd-key/--etcd-ca` CLI flags for server and agent commands, endpoint scheme validation rejects `http://` when TLS configured, TLS 1.3 minimum enforced

### Phase 46 — Controller Algorithm Complexity Audit (M43, Gate F partial)
- [x] Audited all 17 controllers + runner for O(n²) or worse patterns
- [x] InstanceController — removed dead O(n²) code: `observedEntries` first pass, `findOrAddInstanceEntry` linear search, `instanceEntry` type (unused since second pass was added)
- [x] CredentialBrokerController — replaced 3 full fact-store scans with `FactsWithPrefix` binary search; merged `parseInstanceServices` + `parseRunningInstances` into single-pass `parseInstanceServiceAndRunState`
- [x] InitController — replaced 4 full fact-store scans (`countDesiredInitSteps`, `mapInstancesToServices`, `collectObservedInitStepStates`, `collectCurrentInitPhases`) with `FactsWithPrefix` binary search
- [x] IntentResolverController — replaced 2 full fact-store scans in `extractIntentCounts` with `FactsWithPrefix` binary search
- [x] RolloutController — replaced full fact-store scan in `extractRolloutState` with `FactsWithPrefix` using `ScanDerivedServices`
- [x] ClusterAutoscaleController — replaced full fact-store scan in `extractClusterAutoscaleConfig` with `FactsWithPrefix` using targeted prefix
- [x] SDK `NewFactMap` — replaced linear scan with `FactsWithPrefix` binary search for custom controller fact extraction
- [x] Cloud controllers — merged `extractNodeToProviderInstance` + `extractNodeStatesFromFacts` into single-pass `extractNodeProviderAndStates`, used by both `CloudNodeLifecycleController` and `CloudRouteController`
- [x] Runner — early exit on input-key guard loop when etcd 128-operation transaction cap reached, avoiding oversized allocation + truncation

### Phase 47 — Synthetic Cluster Load Test (M44, Gate F complete)
- [x] 5-phase load test: deploy 1000 instances on 50 nodes → verify placement → kill 5 nodes → scale to 1500 → verify store facts
- [x] SimulatedChaosCluster enhancements: configurable lease timeout, configurable max input-key guards, configurable max reconciliation attempts
- [x] Controller runner: exponential backoff with jitter between retry attempts, configurable input-key guard limit (disable/cap/unlimited)
- [x] Phase 1 — deploy 10 services × 100 instances, converges in ~24-54s depending on CPU load
- [x] Phase 2 — placement distribution verification (min/max/stddev across 50 nodes)
- [x] Phase 3 — kill 5 nodes, mark instances failed, recover with 95% convergence tolerance
- [x] Phase 4 — restart killed nodes, scale to 1500 instances, 95% convergence tolerance
- [x] Phase 5 — store fact count verification (~13K facts at scale)
- [x] Near-convergence helper with configurable tolerance for long-tail convergence under contention
- [x] Watch channel buffer size increased to 4096 for high-throughput load test

### Phase 42+ — Review Plan (from chat-plan20sep.md)

#### Gate A — Store Correctness (resolved: Phases 26, 35a, 42)
- [x] StateStore semantics documented
- [x] etcd Put race fixed (Phase 35a)
- [x] Transaction snapshot CAS (Phase 35a)
- [x] MemoryStore/EtcdStore conformance tests
- [x] Watch loss-tolerance formal verification (Phase 42) — StartRevision on WatchOption, event history ring buffer, gap-free Scan+Watch pattern, 12 conformance tests
- [x] Compaction/restart watch resync validation (Phase 42) — EventCompacted type, etcd CompactRevision handling, MemoryStore eviction detection, overflow→resync→resume convergence test

#### Gate B — Reconciliation Protocol (resolved: Phases 26, 35a, 43)
- [x] ReconcilePlan with preconditions (Phase 35a)
- [x] Snapshot coherence per reconcile
- [x] Conflict retry with backoff
- [x] Deterministic plan verification (Phase 43) — runner sorts changes by key, instance/endpoint controllers sort map iterations, 10 controller determinism tests
- [x] Controller write domain enforcement at runtime (Phase 43) — `enforceWriteDomain()` validates every Change against `controllerOutputPrefixes()`, violations dropped + logged + metered

#### Gate C — Truth Model (resolved: Phases 35a, 36, 43)
- [x] Observed state from runtime observation only (Phase 26)
- [x] derived/ prefix for controller state (Phase 35a)
- [x] Event projection from committed state (Phase 36)
- [x] Key ownership matrix audit (Phase 43) — formal document `key-ownership-matrix.md` mapping every prefix to its sole writer, shared overlaps documented

#### Gate D — Agent & Health Model (resolved: Phases 26, 36, 44)
- [x] Probe scheduling independent from reconciliation (Phase 26)
- [x] Init phase single authority (Phase 36)
- [x] Agent sub-reconciler extraction (Phase 36)
- [x] Agent full decomposition (Phase 44) — ProbeScheduler, NodeReporter, DataPlaneReconciler structs extracted from monolithic Agent; Agent composes and delegates to sub-components
- [x] Runtime conformance suite (Phase 44) — 15 behavioral contract tests + 7 failure matrix tests running against SimulatorRuntime and ProcessRuntime; factory-based test pattern
- [x] Failure matrix testing (Phase 44) — empty ID start, exec on unknown workload, stats after stop, concurrent start/stop, env vars, follow-mode logs, exec failure injection, start rejected, container image rejected

#### Gate E — Security Audit (resolved: Phases 11, 39a, 45)
- [x] mTLS, RBAC+ABAC, secrets (Phase 11)
- [x] ValidateResourceName at API boundaries (Phase 39a)
- [x] Host header sanitization (Phase 39a)
- [x] Formal threat model document (Phase 45) — `threat-model.md` with 10 threat categories, trust boundaries, asset inventory, priority fixes
- [x] Command execution audit (Phase 45) — 16 exec call sites audited, image validation, shell metacharacter rejection, env key validation added
- [x] Secret leakage audit (Phase 45) — API prefix denylist for secrets/credentials/enrollment/bootstrap, node cert permissions fixed
- [x] etcd TLS and credential audit (Phase 45) — TLS config added to EtcdStoreConfig, `--etcd-cert/key/ca` CLI flags, endpoint scheme validation

#### Gate F — Performance & Scalability
- [x] Runtime.Stats() live metrics (Phase 37)
- [x] Benchmarks in CI (Phase 38)
- [x] Synthetic cluster load test (50 nodes / 1K workloads) — Phase 47
- [x] Scheduler algorithm optimization (min-heap, binary search — Phase 41)
- [x] Store prefix indexing / trie (Phase 41)
- [x] Controller topological sort, BFS GC, cycle detection (Phase 41)
- [x] Controller algorithm complexity audit (Phase 46)

#### Gate G — Operations & Observability
- [x] Prometheus /metrics (Phase 25)
- [x] Structured JSON logging (Phase 25)
- [x] Event streaming (Phase 25)
- [ ] Distributed tracing end-to-end (proxy → API → controller → agent)
- [ ] Controller health/reconcile metrics
- [ ] Recovery documentation

#### Gate H — Release & QA
- [x] Fuzz tests for parsers/codecs (Phase 38)
- [x] Race detector passes (Phase 38)
- [x] CI test workflow (Phase 38)
- [ ] Chaos test matrix automation
- [ ] Formal invariant tests
- [ ] Documentation correctness cross-check
- [ ] Dependency vulnerability scanning

### Milestones

| Milestone | Phases | Demo |
|-----------|--------|------|
| M1 — State machine | 0–4 | Apply config, see facts reconcile in the store |
| M2 — Single machine | 5 | CLI → parser → store → reconciler → running container |
| M3 — Distributed | 6 | 10 instances spread across 3 nodes |
| M4 — Networking | 7 | Services reachable by name, traffic balances |
| M5 — Storage | 8 | Persistent volumes survive node moves |
| M6 — Resilient | 9 | Kill anything, cluster converges |
| M7 — Smart | 10 | Autoscaling, rolling deploys, placement policies |
| M8 — Secure | 11 | mTLS, RBAC+ABAC, secrets, audit |
| M9 — Multi-tenant | 12 | Tenant isolation, quotas, fair scheduling, policy gates |
| M10 — Production | 13 | Observability, HA control plane, extensibility |
| M11 — Correctness | 14 | Store idempotency, atomic txns, watch safety, controller resilience |
| M12 — Distributed State | 15 | EtcdStore implementation, CLI `--store etcd`, key prefix isolation, integration tests |
| M13 — Multi-Process | 16 | Separate server + agent processes, shared etcd, multi-host ready |
| M14 — Init & Observability | 17 | Init step lifecycle, telemetry collection, `cca top`, runtime Exec |
| M15 — Probes & Readiness | 18 | Startup/liveness/readiness probes, readiness-gated endpoints |
| M16 — Remote Management | 19 | Container default, MCP server with guardrails, auto-logging hook |
| M17 — Multi-Host Server | 20 | `cca server --listen 0.0.0.0:9770 --tls` serves mTLS API to remote agents |
| M18 — VIP Data Plane | 21 | `curl http://10.200.0.1:80` round-robins across containers on multiple hosts |
| M19 — Node Enrollment | 22 | `cca token create` → `cca join` → agent auto-discovers certs and runs |
| M20 — Service Networking | 23 | `curl -H "Host: web" http://node:80` round-robins via proxy, DNS resolves VIPs, require/prefer/restrict/accept placement |
| M21 — CLI & UX | 24 | `cca describe web` shows full resource detail, `cca events --follow` streams live, `cca diff` previews changes, colored tables |
| M22 — Observability | 25 | `/metrics` serves Prometheus, structured JSON logs, `cca logs web --follow`, `/healthz`, Grafana dashboards |
| M23 — P0 Correctness | 26 | Runtime observation authoritative, no 127.0.0.1 fallback, watch overflow resync, init restart-safe, probes decoupled, race-safe |
| M24 — Storage Resilience | 27 | Volume migrates to new node on failure, snapshots before migration, `cca top volumes`, online resize |
| M25 — Identity DSL | 28 | `cloud_identity` and `credential_broker` DSL blocks, compiler emits identity facts, key functions and scan prefixes |
| M26 — OIDC Infrastructure | 29 | ECDSA signing key, `MintWorkloadToken` JWT issuer, `/.well-known/openid-configuration` and `/oidc/jwks` endpoints |
| M27 — Cloud Adapters | 30 | `ExchangeToken` interface, AWS STS / GCP STS / Azure adapters, AES-256-GCM credential store |
| M28 — Credential Broker | 31 | Broker controller issues/refreshes/revokes credentials, proactive TTL refresh, garbage collection |
| M29 — Agent Credentials | 32 | `CredentialProvider` interface, materialize/refresh/cleanup files, AWS/GCP/Azure formats, token projection |
| M30 — Identity RBAC & CLI | 33 | `credential-broker` RBAC role, audit trail, `cca get/describe cloud-identities`, lifecycle + e2e tests |
| M31 — API HA | 34 | Multiple stateless API replicas behind load balancer, controllers leader-elected separately |
| M31a — Correctness II | 35a | Transaction snapshot CAS, derived/ prefix, init dual authority resolved, etcd Put simplified |
| M32 — Cloud Controller | 35 | `cca server --cloud-provider aws` manages node lifecycle, `expose external 443 http` creates cloud LBs, VPC routes auto-programmed |
| M33 — Correctness III | 36 | etcd WithPrevKV, EventProjector from committed state, ExecInit runtime isolation, multi-port endpoints, agent sub-reconciler extraction |
| M34 — Runtime Stats | 37 | `Runtime.Stats()` returns actual CPU/memory per workload, agent telemetry uses live stats instead of desired-state estimates |
| M35 — Project Hardening | 38 | Makefile, golangci-lint, CI test workflow, benchmarks, security/tenant/cloud tests, fuzz tests, structured errors |
| M36 — Scale-to-Zero | 39 | KEDA-like HTTP activation proxy, `min 0` autoscaling, request buffering during cold start, event-driven activation signals |
| M36a — Audit & Hardening | 39a | Security validation, DoS protection, error handling, metrics, caching, graceful shutdown, integration test |
| M37 — System Design Patterns | 40 | Circuit breaker, rate limiter, least-connections LB, bulkhead, tracing, response cache, DLQ, CDC stream |
| M38 — Algorithms | 41 | Min-heap scheduler, binary search extraction, trie prefix scan, topological sort, cycle detection |
| M39 — Store Correctness | 42 | StartRevision watch replay, EventCompacted, event history buffer, 12 conformance tests proving gap-free observation |
| M40 — Truth & Reconciliation | 43 | Deterministic plans (sorted commits + sorted map iterations), write domain enforcement (enforceWriteDomain + Prometheus metric), key ownership matrix (16 controllers + 12 non-controller writers) |
| M41 — Agent Decomposition | 44 | ProbeScheduler + NodeReporter + DataPlaneReconciler extracted from Agent, 15 runtime conformance tests + 7 failure matrix tests across Simulator and Process runtimes |
| M42 — Security Audit | 45 | Formal threat model (10 categories), command exec audit (image validation, metachar rejection, env key validation), secret leakage audit (API prefix denylist, cert permissions), etcd TLS audit (TLSConfig, CLI flags, scheme validation) |
| M43 — Complexity Audit | 46 | Audited 17 controllers + runner; removed O(n²) dead code in InstanceController; replaced 11 full fact-store scans with FactsWithPrefix binary search across 6 controllers + SDK; merged redundant prefix scans in credential broker + cloud controllers; runner early-exit on txn cap |
| M44 — Load Test | 47 | 5-phase synthetic cluster load test (50 nodes, 1K→1.5K workloads): deploy convergence, placement verification, node failure recovery, scale-up, store verification; runner exponential backoff with jitter, configurable input-key guards |
| M45 — Release QA | Gate G+H | Chaos matrix, invariants, docs, dependency scan |

**Start with M1.** If the reconciliation loop and fact store work correctly, everything else layers on top. If they don't, nothing else matters.
