# Runtime Invariant Flows

Cross-component configuration chains that must stay consistent. Each flow describes a path where a mismatch between components causes a hard-to-diagnose runtime failure.

Updated: 2026-10-21

---

## 1. Init Container Environment Parity

**Chain:** `agent/agent.go:buildRuntimeSpecForInstance` → `agent/init.go:runInitCommand` → `runtime/container.go:ExecInit`

Init containers (temporary `nerdctl run --rm` containers for `init { exec "..." }` blocks) must receive the same network, DNS, and env vars as the main container.

**What must match:**
- Network: init container is on the same overlay network (e.g. `cca-net`)
- DNS: same `--dns` and `--dns-search` flags as the main container
- Env vars: same service config env vars (so init steps can access DB connection strings, etc.)

**What intentionally differs:**
- No allocated IP (init container is ephemeral)
- No config file bind-mounts (these are for the main workload)
- No secret file mounts (use connection probes for secret-dependent checks)
- No port mappings or resource limits

**Shell:** `ExecInit` uses `bash -c` (not `sh -c`) because the `cca-secret` helper uses a hyphen in its name, which POSIX sh (dash) rejects.

**Past bugs:** Init containers ran without network or env vars, causing `airflow db check` to fail because it couldn't reach `metadata-db` and didn't have `AIRFLOW__DATABASE__SQL_ALCHEMY_CONN_CMD`. Fixed 2026-10-14.

---

## 2. DNS Configuration Chain

**Chain:** Host resolv.conf → VM dnsmasq → `cca server --dns` → container `--dns` flag → `--dns-search` domain

**What must match:**
- `network.DefaultDNSDomain` in `network/dns.go` must equal `runtime.defaultDNSSearchDomain` in `runtime/container.go` (both `cca.local` — independently declared constants)
- DNS server listens on `--dns-listen` address (default `:15353`)
- Container `--dns` flag is set to the agent's `advertiseAddress` (not 127.0.0.1)
- VM dnsmasq must forward `*.cca.local` queries to the CCattler DNS port

**Invariants:**
- Upstream DNS must be derived from the VM's default gateway, not hardcoded (air-gap support)
- If dnsmasq scope isn't limited to `cca.local`, it breaks general DNS
- DNS resolution only works after VIPs exist in the store (service must be deployed first)

**Past bugs:** Hardcoded 8.8.8.8/1.1.1.1, dnsmasq reconfiguring all DNS, upstream DNS derived from wrong interface. Multiple fixes in commits `9074d96` through `9538c28`.

---

## 3. Secret Master Key Distribution

**Chain:** `cca server` (generates/loads key) → `cca join` (receives in enrollment response) → `cca agent` (loads from disk) → `SecretStore` envelope decryption

**What must match:** The exact same 32-byte key must exist at `/etc/ccattler/secret-master.key` on every node.

**Flow:**
1. Server generates or loads key at startup (`command_server.go:loadOrGenerateSecretMasterKey`)
2. `cca join` receives hex-encoded key in enrollment response, writes to same path
3. Agent reads key from same path (`command_agent.go:configureAgentSecretProvider`)
4. All secret decryption uses this key (AES-256-GCM envelope encryption)

**Failure mode:** If a node is provisioned without `cca join` (manual cert distribution), the master key file is missing or auto-generated (wrong key). All secret decryption silently fails with "decrypt secret" errors — no schema validation catches key mismatch.

---

## 4. mTLS Certificate Trust Chain

**Chain:** `cca server --tls` → `cca token create` → `cca join` → `cca agent --cert/--key/--ca`

**What must match:**
- CA used to sign server cert = CA that signs agent certs
- Certificate CN = `"node:" + nodeID` → becomes the RBAC principal
- CA fingerprint in join token must match actual CA

**Requirements:**
- Server needs `--ca-key` flag when using disk-provisioned certs (enables enrollment API)
- TLS mode switches to `VerifyClientCertIfGiven` when enrollment is enabled (so unauthenticated enrollment requests can reach the enrollment endpoint)
- Certificate `NotBefore` has 1-minute clock skew tolerance — nodes with >1min skew reject certs

**Past bugs:** Cert generation condition wrong (ctrl node is in both `controlplane` and `workers` groups), EC PARAMETERS PEM block not skipped by key loader.

---

## 5. Secret API Token Lifecycle

**Chain:** Agent registers token → injects `CCA_SECRET_URL` + `CCA_SECRET_TOKEN` into container env → `cca-secret` helper → `SecretAPIServer.authenticateRequest`

**What must match:**
- Token registered BEFORE init steps (so both init and main containers can use it)
- `CCA_SECRET_URL` uses `advertiseAddress` (not 127.0.0.1) — containers on Docker bridge can't reach localhost
- Token revoked on instance stop or start failure

**Dual mechanism:** The `cca-secret` helper script (`cat /run/cca-secrets/$1`) uses file-based mounts, NOT the HTTP API. `materializeSecretFiles` still runs even when Secret API is enabled. Both mechanisms coexist: file mounts for `cca-secret` helper, HTTP API for programmatic access.

---

## 6. Advertise Address Consistency

**Chain:** `--advertise-address` → five consumers

A single IP (`--advertise-address`) propagates to:
1. Store: `KeyObservedNodeAddress(nodeID)` for cross-host data plane resolution
2. Runtime: `containerRuntime.SetDNS(advertiseAddress)` for container DNS
3. Agent: `resolveSecretAPIBaseURL` for secret API URL in container env
4. DataPlane: local backend addresses for VIP DNAT rules
5. Debug: publishable address for debug API

**If absent:** DNS not configured on containers, data plane not enabled, secret API URL uses 0.0.0.0, cross-host networking broken. All fail silently.

---

## 7. Network IP Allocation and Endpoint Chain

**Chain:** `NetworkProvider.AllocateIP` → `runtime.Spec.IP` → `nerdctl --ip` → `EndpointController` → DNS/VIP/DataPlane

**What must match:**
- Cluster CIDR (`10.100.0.0/16`) matches nerdctl network subnet
- VIP CIDR (`10.200.0.0/24`) doesn't overlap with cluster CIDR
- `AllocateIP` is idempotent (same ID returns same IP)

**Cross-host traffic requires three facts:** remote node advertise address, instance host port mapping, and the endpoint fact. Missing any one causes fallback to container IP (unreachable cross-host).

---

## 8. Enrollment-to-Etcd Endpoint Discovery

**Chain:** `cca join https://server:9770 <token>` → enrollment response → `/etc/ccattler/endpoints` → `cca agent` connects to etcd

`cca join` derives the etcd endpoint from the server URL by extracting the host and assuming port 2379. Written to `/etc/ccattler/endpoints`.

**Limitation:** Assumes `etcd_host == api_server_host` and `etcd_port == 2379`. Breaks with separate etcd clusters.

---

## 9. Connection URL Assembly

**Chain:** DSL `connection` block → store facts → `assembleConnectionURL` → `materializeConnectionFiles` → bind-mounted file

**Detailed flow:**

```
1. DSL parse:
   connection airflow-db { driver "...", secret airflow.db-password, ... }
   service webserver { connection airflow-db "/run/connections/airflow-db" }

2. Compiler (lang/compiler.go):
   a. compileConnectionDeclaration → fact: desired/connection/airflow-db = {JSON}
   b. compileServiceConnectionRefFacts:
      - fact: desired/service/webserver/connection/airflow-db = "/run/connections/airflow-db"
      - auto-grant: desired/service/webserver/secret/airflow.db-password = "connection"
        (skipped if service already has an explicit secret declaration for the same name)

3. Agent reconciliation (agent/agent.go):
   a. materializeConnectionFiles reads desired/service/{name}/connection/* refs
   b. For each ref, loads desired/connection/{name} JSON definition
   c. assembleConnectionURL calls secretProvider.GetSecretForService(service, secret)
      → IsAuthorized checks desired/service/{name}/secret/{secret} exists (the auto-grant)
      → GetSecret decrypts the secret value from encrypted store
   d. Assembles URL: driver://user:password@host:port/database
   e. Writes URL to mount path (/run/connections/airflow-db)
   f. Bind-mounts file into container

4. Container reads URL via _CMD = "cat /run/connections/airflow-db"
```

**What must match:**
- Connection secret reference must name an existing secret with a grant for the service (auto-generated by compiler since M104)
- Assembled URL format must match what the application expects (e.g. `postgresql+psycopg2://...` for SQLAlchemy)
- `host` in connection definition must be resolvable from inside the container (requires DNS chain working)
- When both an explicit `secret` declaration and a connection reference the same secret, the explicit mount path takes precedence over the auto-generated "connection" marker
- Auto-grant value is `"connection"` (not an absolute path). `materializeSecretFiles` only bind-mounts secrets whose value starts with `/` — auto-grants are authorization-only, the connection URL goes to the connection mount path instead

---

## 10. Resource Limit Effective/Desired Fallback

**Chain:** DSL `resources` → `desired/service/{name}/resources/cpu` → vertical autoscaler → `effective/service/{name}/resources/cpu` → `lookupServiceResourcesFromStore` → `nerdctl --cpus`

- Agent reads `effective/` first, falls back to `desired/`
- `ContainerRuntime.Resize` always returns `ErrResizeUnsupported` — resource changes cause stop-and-restart
- `appliedInstanceResources` is in-memory only — lost on agent restart

---

## 8. Node Enrollment (CSR-Based)

**Chain:** `cmd/cca/command_misc.go:executeJoinCommand` → HTTP POST `/api/enroll` → `security/enrollment.go:EnrollNode` → `security/ca.go:SignCSR`

Enrollment is the trust bootstrap between a new node and the cluster. The node generates its own ECDSA P-256 keypair locally, builds a CSR with its node ID as CN and local IPs as SANs, and sends the CSR (not the private key) to the server. The server validates the bootstrap token, signs the CSR with the cluster CA, and returns only the signed certificate + CA cert.

**What must hold:**
- Private key never leaves the node (generated locally, never serialized to network)
- CSR signature is verified before signing (prevents key substitution)
- Server-side SANs (DNS names, IPs) are merged with CSR SANs
- Legacy flow (server-generated keypair) preserved for backward compatibility during rolling upgrades
- Bootstrap token consumed (deleted) after successful enrollment

**What must NOT happen:**
- Private key PEM in the enrollment HTTP response (the old flow — deprecated)
- CSR accepted without signature verification
- Enrollment without `--ca-cert` or CA fingerprint (unless `--insecure` explicitly set)

## 9. etcd mTLS

**Chain:** `deploy/ansible/roles/etcd/templates/etcd.service.j2` → etcd `--client-cert-auth` → `store/etcd.go:NewEtcdStore` → `--etcd-cert/--etcd-key/--etcd-ca` CLI flags

etcd must require mTLS for all client connections. The cluster CA signs etcd server and client certificates. The cca server and agents present client certs when connecting. Plain HTTP (`http://`) endpoints must not exist in production.

**What must hold:**
- etcd `--client-cert-auth=true` and `--cert-file/--key-file/--trusted-ca-file` set
- All `etcd_endpoints` configs use `https://` scheme
- cca-server.service passes `--etcd-cert/--etcd-key/--etcd-ca`
- etcdctl in E2E tests passes `--cacert/--cert/--key`
- etcd cert SANs include `127.0.0.1` (for local health checks)

## 8. Node Failure Recovery — Two-Phase Traffic Continuity

**Chain:** `agent/dataplane.go:identifyStaleNodes` → `controllers/nodefailure.go` → `controllers/endpoint.go` → `controllers/network.go` → `network/nftables_dataplane.go`

When a node becomes unreachable, traffic to VIP-backed services must continue with minimal disruption. Recovery uses two independent, decoupled phases:

### Phase 1 — Fast path (data plane, ~30 seconds)

**Owner:** Each node agent's data plane reconciler.

Each agent reads heartbeat lease timestamps for all nodes (`lease/node/{id}`). If a node's last heartbeat exceeds `defaultNodeStalenessThreshold` (30s), the agent excludes that node's backends from nftables DNAT rules. Traffic shifts to healthy backends within seconds of detection.

**Key invariant:** The data plane acts on heartbeat staleness alone. It does not wait for the control plane to mark instances as failed, delete endpoints, or reschedule. This is what makes it fast.

**Goroutine isolation:** The data plane reconciler runs in its own goroutine (`dataPlaneLoop`) with an independent ticker (`dataPlaneReconcileInterval`), decoupled from the main agent reconciliation loop. This prevents slow etcd calls in the main loop (reconcile, telemetry) from stalling VIP rule updates and stale-node eviction.

### Phase 2 — Slow path (control plane, 1–3 minutes)

**Owner:** Node failure controller → instance controller → endpoint controller → network controller.

1. Node failure controller detects stale lease → writes `derived/instance/{id}/node_failure = true`
2. Instance controller sees failed instances → schedules replacements on healthy nodes
3. New instances start → agents publish observed state and endpoint facts
4. Data plane picks up new endpoints on next reconciliation cycle

### Critical decoupling invariant

**`node_failure` is a scheduling signal, not a runtime signal.**

The endpoint controller must NOT treat `node_failure=true` as "instance stopped". The instance may still be physically running (node is unreachable, not necessarily crashed). Endpoint facts must persist as long as the observed runtime state is `running`.

- Endpoint eligibility: use `endpointEligibleState()` — ignores `node_failure`, only checks observed `state` and `controller_stopped`
- Scheduling decisions: use `effectiveInstanceState()` — includes `node_failure` for rescheduling logic

If the endpoint controller deletes endpoints based on `node_failure`, the following cascade occurs:
1. Endpoints deleted → network controller sees zero endpoints for some services
2. Network controller deletes VIP facts from etcd
3. Data plane scans VIP facts, finds them missing → removes VIP addresses from dummy interface
4. All traffic to the service fails for the entire recovery window (60–90+ seconds)

This cascade defeats the fast-path eviction because the control plane's scheduling actions destroy the state the data plane needs to route traffic.

### Which controllers use which state function

| Controller | Function | Why |
|---|---|---|
| Endpoint | `endpointEligibleState` | Endpoints reflect runtime state, not scheduling intent |
| Stateful DNS | `effectiveInstanceState` | Per-instance DNS should update when rescheduled |
| Instance | `effectiveInstanceState` | Needs `node_failure` to trigger replacement |
| Failure | `effectiveInstanceState` | Needs `node_failure` for failure detection |
| Autoscale | `effectiveInstanceState` | Failed instances shouldn't count toward replica target |
| Rollout | `effectiveInstanceState` | Failed instances can't serve as rollout targets |
| Drain | `effectiveInstanceState` | Failed instances are already effectively drained |

### Past bugs

- **VIP deletion cascade (2026-10-21):** Endpoint controller used `effectiveInstanceState` which treated `node_failure` as `InstanceFailed`, deleting endpoints for instances on failed nodes. Network controller then deleted VIP facts. Fixed by introducing `endpointEligibleState`.
- **Slow VIP recovery (2026-10-21):** Before fast-path eviction, traffic waited for the full controller pipeline (60–90s). Fixed by adding heartbeat-based stale-node detection in the data plane.
- **Leader election fragility (2026-10-21):** Single etcd timeout during reconciliation storm caused self-inflicted leadership loss, stalling the entire control plane. Fixed by tolerating transient renewal failures within the lease window.
- **Data plane stall via main loop coupling (2026-10-21):** Data plane reconciler ran inside the main agent select loop alongside reconcile, telemetry, and VXLAN. A slow/hung etcd call in any of those steps froze the data plane — nftables rules went stale for minutes. Ctrl node lost fast-path eviction because the main loop blocked on etcd. Fixed by extracting data plane + VXLAN into an independent `dataPlaneLoop` goroutine.
- **VIP address instability during zero-endpoint windows (2026-10-21):** Network controller deleted VIP facts when a service had zero endpoints (e.g. all backends on a failed node). When replacement instances started, the controller allocated a NEW VIP address — clients curling the old VIP got connection refused for the entire recovery window. Root cause: `buildStaleVIPAndDNSRemovalChanges` checked `servicesWithEndpoints` instead of `definedServiceNames`. Fixed by tying VIP lifecycle to the service definition, not endpoint count.

---

## 11. Image Pull Lifecycle

**Chain:** `agent/agent.go:reconcileInstance` → `runtime.ImagePuller.PullImage` → `runtime/container.go:Start` (with `--pull=never`)

Container orchestrators must handle image availability as a first-class lifecycle phase. Unlike Kubernetes (which hides pull status behind opaque pod conditions), CCattler surfaces pull state explicitly in the instance status.

### Instance states

```
placed → pulling → running
                ↘ image-pull-backoff → pulling (retry)
```

- **`pulling`**: Agent is actively pulling the image. Visible via `cca get instances` with pull progress.
- **`image-pull-backoff`**: Pull failed, waiting for retry. Shows reason (timeout, auth failure, not found) and next retry time.

Neither state triggers replacement by the failure controller — the instance is not failed, it's waiting for its image.

### Agent reconcile flow

```
1. Instance placed on this node and image not cached locally?
   → Write instance state: pulling
   → Call runtime.PullImage(ctx, imageReference)
   
2. Pull succeeds?
   → Clear backoff counter
   → Call runtime.Start(ctx, spec) with --pull=never
   → Write instance state: running
   
3. Pull fails?
   → Increment backoff counter for this instance
   → Calculate delay: min(10s × 2^(attempts-1), 300s)
     [10s, 20s, 40s, 80s, 160s, 300s, 300s, ...]
   → Write instance state: image-pull-backoff
   → Write pull failure reason as instance metadata fact:
     pull-status/{instanceID} = {"error": "...", "attempts": N, "retryAfter": "..."}
   → Skip this instance on subsequent reconcile cycles until retryAfter
   
4. On next reconcile after retryAfter:
   → Go to step 1
```

### ContainerRuntime changes

- `Start()` adds `--pull=never` to nerdctl run args — it must not attempt an implicit pull.
- `PullImage()` already exists but needs a configurable timeout (default 5 minutes).
- New: `IsImageCached(ctx, imageReference) bool` — check if image exists locally before pulling.

### Status visibility

`cca get instances` output for a pulling instance:
```
ID          SERVICE    STATE              NODE         HEALTH
a1b2c3d4    web        pulling            node-1       -
e5f6g7h8    web        image-pull-backoff node-2       -
```

`cca status` includes pull detail:
```
instance a1b2c3d4: pulling nginx:1.25 (12% downloaded)
instance e5f6g7h8: image-pull-backoff nginx:1.25 — registry timeout, retry in 40s (attempt 3/∞)
```

The pull-status fact is queryable: `cca watch pull-status/` shows all active pulls.

### What must match

- Pull timeout must be longer than the agent reconcile interval (default 10s), otherwise pulls get cancelled every cycle.
- Backoff counter must be per-instance, not per-image — two instances of the same image can be at different backoff stages.
- Instance controller must treat `pulling` and `image-pull-backoff` like `pending` — do not create replacements, do not count toward failed quota.

### Controller interaction

| Controller | Behavior for pulling/image-pull-backoff |
|---|---|
| Instance | Do not replace — treat like pending |
| Endpoint | No endpoints — same as pending |
| Failure | Ignore — not a failure state |
| Autoscale | Count toward desired but not running |
| Rollout | Do not advance — wait for pull to complete |
| Drain | Can be evicted (cancel pull, reschedule elsewhere) |

---

## 12. Crash Loop Backoff

**Chain:** `controllers/failure.go:processInstanceForRecovery` → crash-loop counter → delayed replacement

When a container crashes immediately after starting, the failure controller replaces it. If the replacement also crashes, this loops at full speed — thousands of container starts per hour. Crash loop backoff breaks this cycle.

### Mechanism

The failure controller maintains a per-service crash-loop counter in the fact store:
`derived/crash-loop/{serviceName}` = `{"count": N, "since": <unix_ms>}`

### Failure controller flow

```
1. Instance state is "failed" and service has crash-loop counter:
   → Calculate backoff: min(10s × 2^(count-1), 300s)
   → If now < since + backoff:
     - Skip replacement (leave instance in failed state)
     - Log: "crash-loop-backoff for {service}, retry in Xs"
   → If backoff expired:
     - Replace normally (stopAndReplace)
     - Emit counter increment: count+1, since=now

2. Instance state is "failed" and NO crash-loop counter:
   → Replace normally (stopAndReplace)
   → Emit counter creation: count=1, since=now

3. Service recovery (no failed instances, all running):
   → Emit counter deletion for that service
```

### Backoff schedule

| Count | Delay |
|-------|-------|
| 1 | 10s |
| 2 | 20s |
| 3 | 40s |
| 4 | 80s |
| 5 | 160s |
| 6+ | 300s (cap) |

### Status visibility

`cca get instances` for a crash-looping service:
```
ID          SERVICE    STATE    NODE      HEALTH
a1b2c3d4    web        failed   node-1    -        (crash-loop-backoff, retry in 40s)
```

The crash-loop counter is queryable: `cca watch derived/crash-loop/` shows all services in backoff.

### What must match

- Counter is per-service, not per-instance (each replacement has a new ID).
- Counter tracks the most recent replacement time, not creation time.
- Instance controller must NOT create extra instances for a service in crash-loop — the failed instance is "active" (not stopped), counting toward the desired replica count.
- Backoff resets when any instance of the service reaches running state and stays healthy.

### Controller interaction

| Controller | Behavior during crash-loop-backoff |
|---|---|
| Instance | Failed instance counts as active — no extra replacements |
| Endpoint | No endpoints for failed instance — same as normal failure |
| Failure | Delays replacement until backoff expires |
| Autoscale | Failed instances count toward desired, not running |

---

## 13. Config/Secret Reload

**Chain:** `agent/agent.go:reconcileDesiredInstance` → config hash → `observed/instance/{id}/config-hash` → steady-state comparison → restart

Configs and secrets are resolved at container start time. Without change detection, updating a config requires a full redeploy. This flow adds automatic restart when config or secrets change.

### Mechanism

The agent computes a SHA256 hash of all config inputs (env vars, config files, secrets) at start time and stores it as `observed/instance/{id}/config-hash`. On each steady-state cycle, the agent recomputes the hash from the current desired config and compares it to the stored value. If different, the agent stops the container — the next cycle restarts it with the new config.

### Agent flow

```
Start path (reconcileDesiredInstance):
  1. Resolve config env vars, config files, secrets
  2. Compute SHA256 hash of all config inputs
  3. Start container with resolved config
  4. Store hash: observed/instance/{id}/config-hash = hex(sha256)

Steady-state path (else branch):
  1. Read stored config-hash from observed/instance/{id}/config-hash
  2. Recompute hash from current desired config
  3. If hashes differ:
     → Log: "config changed, restarting instance"
     → Stop the container via runtime.Stop()
     → Next cycle detects !Running → reconcileDesiredInstance → fresh start
```

### What must match

- Hash includes env vars, config files, and secrets — any change triggers restart.
- Hash computation must be deterministic: sort keys before hashing.
- The rollout controller is not involved — config reload is agent-local.
- Init containers re-run on config-triggered restart (same as any restart).

---

## 14. OOM Kill Detection

**Chain:** `nerdctl inspect` → `runtime/container.go:Status()` → `agent/agent.go:observeInstanceState()` → exit-reason fact

Resource limits (`--cpus`, `--memory`) are already passed to `nerdctl run` via `runtime/container.go:buildRunArguments`. However, when a container is OOM-killed, the agent cannot distinguish it from a normal crash because `Status()` only queries `.State.Running` — not `.State.ExitCode` or `.State.OOMKilled`.

### Detection flow

```
1. Container exits due to OOM
2. nerdctl inspect reports:
   - .State.Running = false
   - .State.ExitCode = 137
   - .State.OOMKilled = true
3. ContainerRuntime.Status() parses all three fields:
   - ExitCode → Status.ExitCode
   - OOMKilled → Status.Error = "OOMKilled"
4. Agent.observeInstanceState() sees ExitCode != 0 → InstanceFailed
5. Agent publishes exit-reason fact:
   observed/instance/{id}/exit-reason = "OOMKilled" | "exit(N)"
6. cca get instances shows the reason in the STATUS column
```

### What must match

- OOM detection is informational only — the failure controller replaces OOM-killed instances identically to crashed instances.
- The exit-reason fact is ephemeral — deleted when the instance is cleaned up.
- ProcessRuntime already populates ExitCode correctly; only ContainerRuntime was missing it.
- SimulatorRuntime's KillWorkload already supports exit code 137 for testing.

---

## 15. Rolling Update MaxSurge (max_extra)

**Chain:** DSL `update { max_extra N }` → `desired/service/{name}/update/max_extra` → RolloutController emits surge allowance → InstanceController creates extra instances → RolloutController stops old instances → surge cleared on completion

The existing rollout controller implements `max_unavailable` but ignores `max_extra`. Without surge, rolling updates stop old instances first, causing temporary capacity dips. With surge, the instance controller pre-creates extra new-image instances before old ones are stopped.

### Surge flow

```
1. User deploys new image for service "web" (desired=4, max_extra=2)
2. RolloutController detects image mismatch, enters "rolling" state
3. RolloutController emits: derived/service/web/rollout/surge_allowance = 2
4. InstanceController reads surge allowance, effective desired = 4 + 2 = 6
5. InstanceController creates 2 extra instances (new image), total active = 6
6. New instances become healthy (running state)
7. RolloutController sees healthy new instances, stops old instances
   (paced by max_unavailable as before)
8. As old instances stop, InstanceController replaces them (maintains ceiling of 6)
9. Last old instance stopped → RolloutController sets state = "complete"
10. RolloutController deletes surge_allowance fact
11. InstanceController sees effective desired = 4, scales down excess → final count = 4
```

### What must match

- Surge allowance is a derived fact, not desired — only the rollout controller writes it.
- The instance controller scale-down path handles the post-surge cleanup naturally.
- Surge allowance is only emitted while rollout state is "rolling". When complete or rollback, it is deleted.
- max_extra = 0 (default) means no surge — behavior identical to before.
- The rollout controller's max_unavailable pacing is unchanged — surge only affects creation rate, not stop rate.

---

## 16. Node-Pressure Eviction

**Chain:** Agent reports `observed/node/{id}/available/memory` → EvictionController reads available vs capacity → compares against thresholds → evicts lowest-QoS instances first → FailureController replaces on healthy nodes

Node-pressure eviction protects the cluster from out-of-memory cascades. When a node's available memory drops below a threshold, the eviction controller marks the node as under pressure and selects instances to evict in QoS priority order.

### Eviction flow

```
1. Agent reports observed/node/{id}/available/memory = "200Mi" (capacity = "4Gi")
2. EvictionController reads all node capacity and available facts
3. Node memory usage = (4096 - 200) / 4096 = 95% → exceeds hard threshold (90%)
4. Controller emits: derived/node/{id}/pressure = "memory"
5. Controller scans instances on this node via observed/instance/{id}/node
6. For each instance, reads derived/service/{svc}/qos_class
7. Sorts eviction candidates: BestEffort first, then Burstable, then Guaranteed
8. Emits desired/instance/{id}/state = "stopped" for lowest-priority instances
9. Stops evicting once projected available memory rises above soft threshold (80%)
10. FailureController detects stopped instances, replaces on other nodes
11. When available memory recovers above soft threshold, deletes pressure fact
```

### Thresholds

- **Hard eviction threshold**: 90% memory utilization — eviction begins immediately.
- **Soft eviction threshold**: 80% memory utilization — eviction target, pressure cleared below this.
- Both are package-level constants, not configurable (simplicity over flexibility for now).

### What must match

- QoS class is read from `derived/service/{svc}/qos_class`, set by the compiler at apply time.
- Instance-to-node mapping is read from `observed/instance/{id}/node`, set by the agent.
- The eviction controller only stops instances — it never deletes or reschedules them directly. The failure controller handles replacement on healthy nodes.
- Eviction respects disruption budgets via the same `canEvictUnderDisruptionBudget` check used by the drain controller.
- A node under pressure is not the same as a node failure — evicted instances get a clean stop (with preStop hooks), not an immediate replacement.

---

## 17. Secret Key Rotation

**Chain:** `cca secret rotate` CLI → POST `/api/secret/rotate` → `SecretStore.RotateKeyProvider()` re-wraps DEKs → new master key distributed to agents via enrollment refresh

Secret rotation re-wraps all stored secrets' data encryption keys (DEKs) under a new master key without decrypting the secret payloads themselves. This is envelope encryption rotation.

### Rotation flow

```
1. Admin runs: cca secret rotate
2. CLI sends POST /api/secret/rotate (authenticated, requires secret.admin capability)
3. API handler generates a new 32-byte master key via GenerateMasterKey()
4. API handler creates a new LocalKeyProvider with the new key
5. API handler calls SecretStore.RotateKeyProvider(ctx, newKeyProvider)
6. RotateKeyProvider re-wraps each secret's DEK from old key to new key
7. API handler updates its secretMasterKey field for future node enrollments
8. API returns success with count of rotated secrets
9. Existing agents continue working — they have the old DEK-encrypted secrets in memory
10. New node joins get the new master key via the enrollment response
11. Existing nodes pick up the new key on next agent restart or re-enrollment
```

### What must match

- The master key in the API server must match what `RotateKeyProvider` set — both are updated atomically within the handler.
- DEKs are re-wrapped, not regenerated — secret payloads remain unchanged.
- Running agents are not disrupted — they already decrypted secrets at startup. The new key takes effect on next restart.

---

## 18. Job/Batch Workloads

**Chain:** DSL `job "name" { ... }` → `desired/job/{name}/image`, `desired/job/{name}/completions`, `desired/job/{name}/parallelism` → JobController creates instances → Agent runs to completion → JobController tracks completions → marks job complete/failed

Jobs are run-to-completion workloads, unlike services which are long-running. A job creates instances that run until they exit successfully, tracking completions toward a target.

### Job flow

```
1. User applies: job "migrate-db" { image "app:v2" command "migrate up" completions 1 }
2. Compiler emits desired/job/migrate-db/image, /command, /completions=1, /parallelism=1
3. JobController reads desired job facts, creates instance with unique ID
4. Agent picks up instance, runs container — container exits 0
5. Agent reports observed/instance/{id}/state = "exited" with exit_code = 0
6. JobController reads exit state, increments derived/job/migrate-db/succeeded = 1
7. succeeded (1) >= completions (1) → JobController emits derived/job/migrate-db/status = "complete"
8. No more instances created. Job facts remain for inspection.
```

### Failure handling

```
1. Container exits non-zero → Agent reports state = "exited", exit_reason = "failed"
2. JobController increments derived/job/migrate-db/failed count
3. If failed < backoff_limit (default 3), JobController creates a replacement instance
4. If failed >= backoff_limit → derived/job/migrate-db/status = "failed"
```

### Parallelism

- `parallelism N` controls how many instances run concurrently. JobController creates up to N instances at a time.
- `completions M` is the total successful exits needed. Default 1 for simple jobs.
- Default parallelism is 1 (sequential).

### What must match

- Job instances use the same runtime path as service instances — same image pull, networking, lifecycle hooks.
- Job instances are NOT replaced by the FailureController — the JobController owns their lifecycle. Distinguished by `desired/job/{name}` prefix vs `desired/service/{name}`.
- The instance controller ignores job instances (they have no desired count to reconcile toward).
- Job status is a derived fact — only the JobController writes it.

---

## Checklist for New Features

When adding a new feature that touches the runtime:

1. **Does the main container get it?** If yes, check if init containers need it too (Flow 1).
2. **Does it need DNS?** If yes, verify the chain from dnsmasq to container `--dns` (Flow 2).
3. **Does it use secrets?** If yes, verify master key distribution via `cca join` (Flow 3).
4. **Does it use advertise address?** If yes, ensure it's propagated consistently (Flow 6).
5. **Does it need cross-host networking?** If yes, verify all three endpoint facts exist (Flow 7).
6. **Does it add a new env var or config to containers?** If yes, ensure it's resolved at materialization time, not stored pre-rendered.
7. **Does it use instance state to make decisions?** If yes, determine whether the decision is about runtime behavior (use `endpointEligibleState`) or scheduling intent (use `effectiveInstanceState`). Never use `node_failure` markers for runtime decisions (Flow 8).
8. **Does it start containers?** If yes, verify it goes through the image pull lifecycle (Flow 11) — pull first, start with `--pull=never`. Never assume the image is cached.
