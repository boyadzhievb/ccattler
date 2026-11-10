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

> Replaced by [Flow 27](#flow-27-secret-delivery-m174) in M174: nodes no longer receive the key and agents do not decrypt. The control plane still loads `/etc/ccattler/secret-master.key` until M177 replaces it with a fresh root key.

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
- Certificate CN = node ID, and the mTLS principal is `"node:" + CN`. For enrolled nodes the CA sets the CN (Flow 11), never the CSR.
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

## 11. Node Enrollment (CSR-Based)

**Chain:** `cmd/cca/command_misc.go:executeJoinCommand` → HTTP POST `/api/enroll` → `security/enrollment.go:EnrollNode` → `security/ca.go:SignCSR`

Enrollment is the trust bootstrap between a new node and the cluster. The node generates its own ECDSA P-256 keypair locally, builds a CSR with its node ID as CN and local IPs as SANs, and sends the CSR (not the private key) to the server. The server validates the bootstrap token, signs the CSR with the cluster CA, and returns only the signed certificate + CA cert.

**What must hold:**
- Private key never leaves the node (generated locally, never serialized to network)
- CSR signature is verified before signing (prevents key substitution)
- The CA sets the certificate identity itself (M171): CN = the validated node ID, SANs = `<node-id>`, `<node-id>.cca.local` and the request IPs. The CSR contributes only its public key; its subject and SANs are ignored.
- Enrolled node certificates carry client-auth only. They authenticate the node to the API server and etcd and can never act as a server certificate. Control-plane and etcd server certificates come from `cca init`.
- The node ID passes `ValidateNodeID` (lowercase DNS name, no `/`), because it becomes both a store key segment and the mTLS principal `node:<id>`.
- A node ID is refused when it is reserved (`ccattler-server`) or already holds grants under `auth/grant/node/<id>/` (the server principal is `node:<server-cn>` and holds cluster-admin).
- Re-enrolling an already-enrolled node ID needs a token bound to that node ID (`cca token create --node-id`).
- The token is consumed and `enrollment/node/<id>` written in one transaction guarded by both revisions, so two concurrent requests with one token cannot both succeed. The certificate is signed before the transaction and returned only if the transaction succeeds.

**What must NOT happen:**
- Private key PEM in the enrollment HTTP response (the old flow — removed)
- Secret key material in the enrollment response (after M174: no `SecretMasterKey`, `cca join` writes no key file; [Flow 27](#flow-27-secret-delivery-m174))
- CSR accepted without signature verification
- A CSR-chosen CN or SAN in the issued certificate (a CN of `ccattler-server` or a control-plane node ID would map to a cluster-admin principal)
- Enrollment without `--ca-cert` or CA fingerprint (unless `--insecure` explicitly set)

## 12. etcd mTLS

**Chain:** `deploy/ansible/roles/etcd/templates/etcd.service.j2` → etcd `--client-cert-auth` → `store/etcd.go:NewEtcdStore` → `--etcd-cert/--etcd-key/--etcd-ca` CLI flags

etcd must require mTLS for all client connections. The cluster CA signs etcd server and client certificates. The cca server and agents present client certs when connecting. Plain HTTP (`http://`) endpoints must not exist in production.

**What must hold:**
- etcd `--client-cert-auth=true` and `--cert-file/--key-file/--trusted-ca-file` set
- All `etcd_endpoints` configs use `https://` scheme
- cca-server.service passes `--etcd-cert/--etcd-key/--etcd-ca`
- etcdctl in E2E tests passes `--cacert/--cert/--key`
- etcd cert SANs include `127.0.0.1` (for local health checks)

## 13. Node Failure Recovery — Two-Phase Traffic Continuity

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

## 14. Image Pull Lifecycle

**Chain:** `agent/agent.go:reconcileInstance` → `runtime.ImagePuller.PullImage` → `runtime/container.go:Start` (with `--pull=never`)

Container orchestrators must handle image availability as a first-class lifecycle phase. Unlike Kubernetes (which hides pull status behind opaque pod conditions), CCattler surfaces pull state explicitly in the instance status.

### Instance states

```
placed → pulling → running
                ↘ image-pull-backoff → pulling (retry)
```

After M174 ([Flow 27](#flow-27-secret-delivery-m174)), an instance that needs secrets (mounted secrets, connection passwords or an image pull secret) passes `waiting-for-secrets` first, also when its image is cached:

```
placed → waiting-for-secrets → pulling → starting → running
```

- **`pulling`**: Agent is actively pulling the image. Visible via `cca get instances` with pull progress.
- **`image-pull-backoff`**: Pull failed, waiting for retry. Shows reason (timeout, auth failure, not found) and next retry time.

Neither state triggers replacement by the failure controller — the instance is not failed, it's waiting for its image.

### Agent reconcile flow

```
0. (M174) Instance needs secrets and has no material from this boot?
   → Write instance state: waiting-for-secrets; deliver (Flow 27 step 5–8)
   → Continue only after materialization succeeded

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

## 15. Crash Loop Backoff

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

## 16. Config/Secret Reload

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

## 17. OOM Kill Detection

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

## 18. Rolling Update MaxSurge (max_extra)

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

## 19. Node-Pressure Eviction

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

## 20. Secret Key Rotation

> Current behavior. M174 replaces it with the keyring controller ([config-secrets.md](config-secrets.md#rotation-crash-safe), Flow 27).

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

## 21. Job/Batch Workloads

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

## 22. Image Pull Secrets

**Chain:** DSL `image_pull_secret "secret-name"` on service/job → `desired/service/{name}/image_pull_secret` → Agent reads secret name at pull time → resolves credentials via secret API → passes `--creds user:pass` to `nerdctl pull`

Image pull secrets let services and jobs authenticate against private container registries. The secret value is a JSON object `{"username":"...","password":"..."}` stored via `cca secret set`. Credentials are resolved at pull time on each agent, never stored in the fact store.

### Pull flow with credentials

```
1. User creates secret: cca secret set registry-creds '{"username":"bot","password":"token123"}'
2. User applies: service "api" { image "registry.example.com/api:v1" image_pull_secret "registry-creds" }
3. Compiler emits desired/service/api/image = "registry.example.com/api:v1"
4. Compiler emits desired/service/api/image_pull_secret = "registry-creds"
5. Scheduler assigns instance to node, agent picks it up
6. Agent reads desired/service/api/image_pull_secret = "registry-creds"
7. Agent calls secret API: GET /secrets/registry-creds → {"username":"bot","password":"token123"}
8. Agent calls runtime.PullImage("registry.example.com/api:v1", credentials="bot:token123")
9. Container runtime: nerdctl pull --creds bot:token123 registry.example.com/api:v1
10. Pull succeeds → agent starts container with --pull=never (standard Flow 14)
```

After M174, steps 6–7 change ([Flow 27](#flow-27-secret-delivery-m174)): the pull credential is part of the instance's delivered material from `POST /api/node/secrets`, fetched in `waiting-for-secrets` before `pulling`, and read from `/run/ccattler/secrets/{instance}/`. The agent no longer calls a local secret API or decrypts. A rotated credential takes effect on the next delivery (next instance start after the material is gone, or a new instance), not on every pull.

### Pull flow without credentials

```
1. User applies: service "web" { image "nginx:latest" }
2. No image_pull_secret field → no credential lookup
3. Agent calls runtime.PullImage("nginx:latest", credentials="")
4. Container runtime: nerdctl pull nginx:latest (no --creds flag)
```

### What must match

- Image pull secrets use the existing secret infrastructure (Flow 3) — same encryption, same API, same key distribution via `cca join`. After M174: Flow 27 delivery, no key on the node.
- Credentials are resolved per-pull, not cached — a rotated secret takes effect on the next pull without restarting the agent.
- Job instances follow the same pull path as service instances — `image_pull_secret` on `JobDecl` emits `desired/job/{name}/image_pull_secret`.
- The secret value is opaque JSON. The agent parses `username` and `password` fields and joins them as `user:pass` for `--creds`.
- Pull failure due to bad credentials is reported the same way as any pull failure — the instance stays in `pulling` state, agent retries on next reconcile cycle.

---

## 23. CronJobs (Scheduled Recurring Jobs)

**Chain:** DSL `cronjob "name" { schedule "*/5 * * * *" image "img" command "cmd" }` → `desired/cronjob/{name}/schedule`, `/image`, `/command` → CronController checks schedule each resync → creates `desired/job/{name}-{timestamp}/...` → JobController takes over run-to-completion

CronJobs are recurring jobs fired on a cron schedule. The CronController piggybacks on the 30-second runner resync cycle, checks whether the current minute matches the schedule, and creates a one-shot job for the JobController to execute.

### Scheduling flow

```
1. User applies: cronjob "cleanup" { schedule "0 2 * * *" image "app:v1" command "cleanup.sh" }
2. Compiler emits desired/cronjob/cleanup/schedule = "0 2 * * *"
3. Compiler emits desired/cronjob/cleanup/image = "app:v1"
4. Compiler emits desired/cronjob/cleanup/command = "cleanup.sh"
5. CronController resync fires at 02:00:xx — schedule matches current minute
6. CronController checks derived/cronjob/cleanup/last_scheduled — not within current minute
7. CronController checks for active job with prefix "cleanup-" — none found
8. CronController creates desired/job/cleanup-20260301-0200/{image,command,completions,backoff_limit}
9. CronController writes derived/cronjob/cleanup/last_scheduled = "2026-03-01T02:00:00Z"
10. JobController picks up the new job, creates instances, runs to completion
11. CronController mirrors job status to derived/cronjob/cleanup/last_status
```

### Double-fire prevention

```
1. CronController resync fires twice within the same minute (e.g. 02:00:05 and 02:00:35)
2. Second resync: last_scheduled = "2026-03-01T02:00:00Z" — same minute as now
3. CronController skips — no duplicate job created
```

### Active job suppression

```
1. Schedule matches but a job "cleanup-20260301-0200" is still running (status != "complete", != "failed")
2. CronController skips — waits for previous run to finish before creating next
```

### Cron expression format

5-field standard: `minute hour day-of-month month day-of-week`. Supports `*` (any), fixed values, comma-separated lists. No ranges, no step values — keeps the parser simple.

### What must match

- CronController only creates job facts — the JobController owns the job lifecycle from there. No direct instance management.
- Job names include a timestamp suffix (`{cronjob}-{YYYYMMDD-HHMM}`) to avoid collision.
- The `last_scheduled` derived fact is the sole mechanism for double-fire prevention. It must be written atomically with job creation.
- CronController watches `ScanDesiredCronJobs`, `ScanDerivedCronJobs`, and `ScanDesiredJobs` to detect active jobs.

---

## Flow 13: Gate TLS Termination (M169, listener revised in M170)

**Problem**: A gate with `tls auto` must have a valid cert issued by the cluster CA before the agent can serve HTTPS. Manual mode must have cert/key content available in the fact store at apply time.

**Invariant**: Every TLS gate has cert material available before the agent proxy starts listening on HTTPS. No gate serves TLS with expired or missing certs.

**Invariant (M170)**: TLS is terminated only on the agent gate proxy's node HTTPS listener. Service VIPs never carry TLS from the gate; in-cluster callers of a service port see the same protocol whether or not a gate has `tls`.

### Auto Mode Flow

```
cca apply (gate with tls auto)
  → compiler writes desired/route/{name}/tls/mode = "auto"
  → RouteTLSController sees mode=auto, checks derived/route/{name}/tls/not-after
  → if missing or within 30 days of expiry:
       CertificateAuthority.IssueCertificate(DNSNames: [host], TTL: 90d)
       → writes derived/route/{name}/tls/cert = <PEM>
       → seals the key at secrets/route/{name}/tls/key (M176, Flow 28)
       → writes derived/route/{name}/tls/not-after = <timestamp>
  → agent fetches cert + key pairs via POST /api/node/gate-keys (Flow 28),
       caches them in /run/ccattler/gate-keys/, builds host → certificate map
  → client connects to node:443 with SNI = host
       → GetCertificate(SNI) returns the gate cert (unknown SNI → handshake fails)
       → Host header → gate → service → healthy endpoint (plain HTTP)
```

### Manual Mode Flow

```
cca apply (gate with tls { cert "path" key "path" })
  → compiler reads PEM files at compile time
  → writes desired/route/{name}/tls/mode = "manual"
  → writes desired/route/{name}/tls/cert = <PEM content>
  → key: inline PEM → PUT /api/gate-key, sealed at secrets/route/{name}/tls/key;
         key secret "n" → desired/route/{name}/tls/key_secret = n (M176, Flow 28)
  → agent gets the pair through gate-key delivery (manual takes precedence over auto)
  → same request path as auto mode on node:443
```

### Critical Invariants

- `tls` requires `host` — validation error at parse time if missing.
- Auto mode cert issuance happens in the RouteTLSController, not in the compiler or agent.
- The agent never generates certs — it only consumes delivered cert/key pairs (Flow 28).
- Certificates stay plain facts (auto in `derived/route/`, manual in `desired/route/`); private keys exist only as envelopes under `secrets/` (M176).
- If TLS material is missing, the gate has no entry in the certificate map: handshakes for its host fail until the cert appears (picked up within one refresh).
- TLS terminates on the node HTTPS listener (`--proxy-tls-listen`, default :443), never via VIP dispatch. VIP data planes (nftables, iptables, eBPF) do not know about TLS.
- The plain-HTTP listener answers requests for a TLS gate's host with a 308 redirect to `https://`.
- A request whose Host belongs to a different gate than its SNI gets 421 Misdirected Request.
- Cert renewal happens server-side. The agent picks up new certs on its next reconcile cycle.
- `cca apply` replaces each declared gate (`collectRemovedGateFacts`): stale `desired/route/{name}/` keys such as a removed `tls` block are deleted after the new facts are written. Only the compiler writes `desired/route/`, so no controller-owned key is lost. Exception: a pre-M176 plaintext `tls/key` is left to the gate-key migration (Flow 28 step 7).

---

## Flow 14: Shared Volume Attach (ReadWriteMany)

**Problem**: A service with several instances mounting one NFS volume had its
instances on other nodes wait forever ("waiting for volumes"), because the
agent treated every volume as attachable to one node only. A single
`node`/`instance` record could not describe several attachments either.

**Invariant**: An instance of a service mounting a shared-capable volume
(`StorageProvider.SupportsSharedAttach()`) never waits for another node's
attachment. Every attachment of a shared volume has its own fact, owned by
the agent that made it; only `StorageController` writes the shared volume's
`state`. An exclusive volume is attached to at most one node.

```
observed/volume/{name}/
├── access = shared                          ← agent, on first shared attach
├── state  = attached | available            ← StorageController only (shared)
└── attachment/{node}/{instance} = mountPath ← owned by that node's agent

agent: instance placed, image ready
  → for each volume the service mounts:
       provider shared:
           AttachVolume(this node) → mountPath
           put access=shared, attachment/{this node}/{instance}=mountPath → ready
       provider exclusive (unchanged):
           state attached to this node      → ready
           state available / migrating      → AttachVolume, write state/node/instance/mount_path
           state attached to another node   → wait (retry next cycle)
  → start instance; mount path from attachment/{node}/{instance} (shared)
    or observed/volume/{name}/mount_path (exclusive)

agent: instance stopped
  → shared:    delete attachment/{this node}/{instance};
               DetachVolume(this node) only when no attachment/{this node}/* remains
  → exclusive: DetachVolume, write state=available

StorageController (access=shared):
  → ≥1 attachment key        → state=attached
  → 0 attachment keys        → state=available
  → attachment on a node that is unreachable or gone → delete that key
    (no snapshot, no migrating state: other nodes keep using the volume)
  → volume no longer desired → delete access + all attachment keys
```

### Critical Invariants

- Shared vs exclusive on the agent is decided by the agent's provider for that volume (`storageProviderForVolume`); the controller uses the `access` fact the agent wrote.
- One writer per key: each agent writes only its own `attachment/{node}/…` keys, the controller alone writes a shared volume's `state`. Two nodes detaching at once cannot leave the state wrong.
- Keys are per instance, so stopping one of two instances on the same node keeps the node attached.
- Shared volumes do not use the `node`, `instance`, `mount_path` or `migration_source` facts.

---

## Flow 24: Agent Debug API Access (M172)

**Chain:** `cca exec` / `cca logs` / `cca node-inspect` / `cca images` → `GET|POST /api/node/debug/{node}/{endpoint}` on the API server (user token or client cert, capability check) → reads `observed/node/{node}/debug_address` → mTLS request to the agent `/debug/{endpoint}` with the API server's certificate → agent checks the client certificate principal holds a cluster-admin grant → runtime.

The debug API can run commands in any container, so users never reach it directly. User authorization stays in the API layer (CLAUDE.md two-layer rule); the agent authenticates only the control plane.

| Endpoint | Method | API capability |
|----------|--------|----------------|
| `containers`, `images`, `stats` | GET | `node.read` |
| `logs` | GET (streams with `follow=true`) | `workload.read` |
| `exec` | POST | `workload.exec` |
| `images/pull` | POST | `node.manage` |

**What must hold:**
- With `--cert/--key/--ca` (or the auto-discovered `/etc/ccattler/pki` files) the agent serves the debug API over TLS 1.3 and requires a client certificate signed by the cluster CA. It publishes `https://<advertise>:<port>`.
- The agent allows a request only when `auth/grant/node/<client-cn>/role/cluster-admin` exists. The server principal gets that grant in `bootstrapAdminGrants`; enrolled nodes never do (Flow 11).
- Without TLS credentials the agent refuses a non-loopback `--debug-listen` and publishes `http://127.0.0.1:<port>` (single-host dev mode).
- The API server verifies the agent certificate against the cluster CA and requires its CN to equal the target node ID. Enrolled node certificates are client-auth only (Flow 11), so this check accepts any extended key usage. Hostname verification is not used because the address comes from the store.
- The API server never forwards the caller's `Authorization` header to the agent.
- Plain `http://` debug addresses are proxied only to loopback hosts.
- `exec` takes `args`, a JSON array, and the runtime runs it as given: no implicit shell, so it works in shell-less images and arguments are never re-parsed. `cca exec <id> -- <args...>` sends its arguments unchanged; use `-- sh -c '…'` for shell syntax. Probes, lifecycle hooks and init commands are DSL strings and keep `sh -c`.

**What must NOT happen:**
- A debug endpoint reachable on a host IP without mTLS
- The CLI or e2e scripts calling the agent debug port directly
- An unknown endpoint path forwarded to the agent

---

## Flow 25: API Authentication and Authorization (M173)

**Chain:** request → `authenticationMiddleware` → `AuthenticatorChain` (mTLS, local token, internal JWT, external OIDC, webhook) → `requireCapability` → `APIAuthorizer` (bootstrap grants + store grants + ABAC policies) ← `AuthController` runner in each API process ← `auth/` facts (`cca role grant`, DSL roles and policies, `bootstrapAdminGrants`).

**What must hold:**
- Each authenticator claims only its own credentials: mTLS by client certificate, the local token by its 64-hex shape, each JWT authenticator by matching `iss`. Unclaimed tokens fall through; a claimed token that fails validation is denied.
- The webhook is last, so the local admin token and internal JWTs are never sent to it.
- Clients get a generic 401 message; the reason is logged on the server.
- Every API process runs its own `AuthController` runner (not leader-elected), because each process has its own in-memory `APIAuthorizer`. Grant changes reach every replica within one reconcile cycle.
- `GrantRole` grants (local-admin, the server principal) are bootstrap grants and survive `ReplaceGrants`. Store-derived grants replace each other atomically.
- A grant to a builtin role without capability facts (`cca role grant --role api-reader`) uses the builtin role's capabilities.
- An ABAC policy without conditions is ignored, so it can never grant a capability to every principal.
- `/api/state` and `/api/watch` read only the allowlisted roots (`desired`, `effective`, `observed`, `placement`, `endpoint`, `intent`, `network`, `derived`, `event`). A prefix must equal a root or continue it with `/`. Keys ending in `/tls/key` are dropped from scans and watch streams and refused as single keys.
- The concurrent watch limit is reserved by incrementing first, then checking.
- Users are authorized only at the API layer, by capability (CLAUDE.md two-layer rule). The API server reads and writes the store as its own internal principal `service:api-server`, bound to the store-layer role `api-server`: read and watch on the orchestration roots plus `auth`, `leader`, `lease`, `policy`; write and delete only on the prefixes its handlers change. It has no rule for `secrets/`, `credentials/`, `enrollment/`, `bootstrap/` or `audit/`, so a handler bug cannot reach them through the generic store path. Store audit entries record `service:api-server` with the end user in `on_behalf_of`.
- Applying DSL (`/api/apply`, policy gate and direct path) checks a capability for every compiled fact: `auth/`, `policy/network/` and cloud identity facts need `cluster.admin`; `desired/service/*/secret/` and image pull secrets need `secret.use`; other `desired/`, `effective/`, `intent/`, `derived/service/` facts need `workload.create`; anything else needs `cluster.admin`. A `workload.create` user therefore cannot write grants and escalate.
- Status, metrics and state reads return a store error as an error, never as an empty cluster.
- Workload visibility for `/api/state`, `/api/watch` and `/api/status` comes only from `workload.read` grants (direct or via `cluster.admin`). A `cluster`-scope grant sees every team. A `team/<name>` grant sees that team only. Membership (`team` attribute, `platform` group) adds nothing, and deeper scopes (`team/<name>/...`, node or service scopes) give no team visibility. No grant at any scope is a 403. The same per-key filter applies to scans, single-key reads and watch events. A read grant gives no `secret.*` or `cluster.admin` rights, and the readable-root allowlist and `/tls/key` filter still apply.

**What must NOT happen:**
- A prefix such as `secret`, `s` or `enroll` returning keys from `secrets/`, `credentials/` or `enrollment/`
- One authenticator's error for a token it does not own blocking the next authenticator
- Store grants or policies written but never applied (the pre-M173 state)
- A user authorized at the API layer denied by the store layer because it has no store role binding (500 "has no role bindings", found in run 41637959333)
- Team membership hiding facts from a principal with an explicit cluster-scope grant, or showing a team's facts to a member without a grant

---

## Flow 26: Node-Local Volume Home Node

**Problem**: A `type local` volume is a directory on one node
(`LocalPathStorageProvider`). Nothing kept its instances on that node. After a
replacement, node failure or rebalance, the agent on the new node attached its
own directory of the same name: empty (the database started fresh, silent data
loss) or a stale copy left by an earlier run there (run 41838979669: Postgres
`PANIC: could not locate a valid checkpoint record`). Volume "migration" and
"replication" only change facts; no data is copied.

**Invariant**: A node-local volume's data exists on exactly one node, its home
node. Once recorded, an instance that mounts the volume runs only on the home
node. If the home node is down, the instance stays unplaced; it never starts
on another node's directory. Network volumes (NFS, simulator) are unchanged.

```
observed/volume/{name}/home_node = nodeID   ← agent, first node-local attach;
                                              never rewritten by agents

agent: instance placed, exclusive volume
  → provider.NodeLocal() and home_node set and home_node != this node
        → do not attach, do not start; log "volume lives on {home_node}"
  → provider.NodeLocal() and home_node absent
        → AttachVolume, write home_node = this node (create-only)
  → otherwise unchanged (Flow 14)

scheduler (placement, every path; rebalance already skips volume services):
  → service mounts volume V with home_node H
        → candidates = alive nodes ∩ {H} (hard; no fallback to other nodes)
        → H down or not a candidate → unplaced, reason volume_home_node_unavailable
  → a service mounting two node-local volumes with different home nodes
        → no candidate (unplaced)
```

Moving a node-local volume's data to another node is an operator action (copy
the directory, then clear `home_node`); there is no `cca` command for it yet.

## Flow 27: Secret Delivery (M174)

**Status:** steps 1 (mounts, connections, pull secret), 5–10 and the failure paths implemented in M174 (2026-11-06); gate keys M176, keyring and v2 envelopes M177, tenant names M179 (second review 2026-11-06: fresh root key, CLI ownership/shares, gate-key tmpfs cache, retry fallback, delivery before pull). Replaces Flow 3 (master key on every node) and the rotation part of Flow 20 when M174 lands. Key hierarchy, envelope format and tenant naming: [config-secrets.md](config-secrets.md#m174-design-secrets-architecture).

**Problem**: every node holds the cluster master key and decrypts locally, so one compromised node can read every secret of every tenant from etcd.

**Invariant**: only control-plane processes decrypt, using a root key that was never on a worker (the old `secret-master.key` is a legacy key that only opens v1 envelopes until migration). A node receives plaintext only for instances placed on it whose service is granted the secret (and, for another tenant's secret, shared). An instance is never pulled or started without its required secrets, and losing the API never stops a running container.

```
0. Secret administration (CLI, not DSL)
   cca secret set database.password --tenant payments     (secret.write at team/payments)
   cca secret set registry.credentials --cluster          (secret.write at cluster)
   cca secret set legacy-app.password                     (default tenant, team/default)
   cca secret share database.password --tenant payments --with-tenant frontend
     → desired/secret_share/payments/database.password/frontend

1. Declaration (DSL, apply)
   service checkout { owner payments  secret database.password }
   connection db { secret database.password ... }    auto-grant (Flow 9)
   gate web { tls { key secret "platform/web-gate-key" } }

2. Compilation (lang/compiler.go)
   tenant of a service: explicit owner → hierarchical name → built-in default
   resolve each reference to its canonical name:
     unqualified        → secrets/tenant/{service tenant}/{name}
     cluster/{name}     → secrets/cluster/{name}       (needs cluster grant)
     tenant/{t}/{name}  → needs desired/secret_share/{t}/{name}/{service tenant}
   unresolvable / unshared reference → apply error naming the secret
   facts: desired/service/{svc}/secret/{canonical} = mount path
   inline gate PEM key → sealed by the apply path, never a fact

3. Encryption and store (API server, secret write)
   DEK = random; ciphertext = AES-GCM(DEK, value, AAD = canonical key)
   envelope v2 = magic | 2 | kek_version(keyring/active) | wrap(KEK, DEK) | nonce | ciphertext
   put secrets/tenant/payments/database.password = envelope
   (before key migration, while old agents still decrypt locally: writes stay v1;
    the legacy key never wraps a KEK, a v2 DEK or root_check)

4. Placement (scheduler, unchanged)
   placement/instance/{id} = node-3

5. Agent start path (node-3)
   instance placed here, needs secrets?
     material in /run/ccattler/secrets/{id}/ from this boot → reuse (step 8)
     else → state waiting-for-secrets, POST /api/node/secrets {instance_id} (mTLS)
   this happens before pulling, also when the image is cached:
     placed → waiting-for-secrets → pulling → starting → running   (Flow 14)
   the image pull secret is part of the delivered material (Flow 22)

6. Authorization (API server, secret-delivery store principal)
   caller = node:node-3, enrolled, not disabled
   placement/instance/{id} == node-3
   for each secret of the service: grant exists; foreign tenant → share exists
   any check fails → 403 with secret names, audit refusal

7. Decryption and response
   KEK = unwrap(root key, keyring/kek/{version}); DEK = unwrap(KEK)
   plaintext = AES-GCM open(DEK, ciphertext, AAD = canonical key)
   audit: node, instance, service, secret names (never values)
   response over TLS 1.3

8. Materialization (agent)
   /run/ccattler/secrets/{id}/ (tmpfs, dir 0700 root), files 0444, Spec.SecretMounts
   secret files, connection URLs (agent assembles from password + definition);
   bind-mount read-only at declared paths

9. Image pull (Flow 14, credentials from step 8), container starts; state running.
   only if the service sets legacy_secret_api true (deprecated, removed after M174):
     per-instance CCA_SECRET_TOKEN/CCA_SECRET_URL env; secret API (:9772)
     answers from /run/ccattler/secrets/{id}/; token revoked at stop; no decryption

10. Cleanup
    container stopped and not restarting here (stopped, removed, re-placed)
      → remove /run/ccattler/secrets/{id}/
```

**Failure and recovery paths:**

```
API unreachable at step 5
  → waiting-for-secrets, reason api_unreachable, retry 2s … 30s backoff
  → not a crash: no crash-loop count, no replacement, no reschedule

delivery refused at step 6
  → waiting-for-secrets, reason not_authorized (names listed)
  → retried immediately when the instance's placement, its service's grant facts
    or a relevant desired/secret_share fact change (agent watches metadata only,
    never secrets/), and at the latest every 5 minutes as a fallback
  → refusal audit records rate-limited per (node, instance, reason)

container crash / liveness restart
  → material present (same boot) → restart without fetching

API unreachable while running
  → nothing happens; container keeps running

agent process restart (same boot)
  → for each /run/ccattler/secrets/{id}/:
       instance running and placed here → keep
       otherwise → remove
  → running containers untouched

node reboot
  → tmpfs empty → every instance goes through step 5 before starting

instance re-placed node-3 → node-5
  → node-5: step 5 (own copy); node-3: step 10 after its container stopped

node disconnected
  → keeps already delivered plaintext until it can clean up (documented limit)

rotation in progress (keyring controller)
  → envelopes carry kek_version; step 7 unwraps with that version; no reader waits

key migration in progress (cca secret migrate-keys, after the migration gate)
  → v1 envelopes opened with the legacy key, re-encrypted as v2 (AAD = current key)
    in batches; observed/keyring/migration persists legacy_v1_envelope_count
  → step 7 reads v1 or v2; every new write is v2
  → after count = 0 and the retirement gate: legacy key retired, v1 refused

gate keys
  → every gate-serving agent: POST /api/node/gate-keys at start and on gate change;
    separately authorized and audited
  → cached in /run/ccattler/gate-keys/{gate}/ (tmpfs, private key 0400),
    never mounted into containers; removed/replaced on gate removal or key rotation
  → agent restart (same boot): reload from tmpfs, keep serving TLS
  → API unreachable: keep serving with cached keys
  → reboot with API unreachable: no TLS on this node, gate_keys = waiting
  → a node without a local backend still terminates TLS (proxies to the VIP)
```

**What must match:**
- The canonical name used as AAD at encryption (step 3) is the name the grant fact references (step 2) and the name delivery looks up (step 7).
- Every API replica opens `keyring/root_check` with its root key; a replica that cannot does not start. The root key is not the legacy key.
- The agent's state machine treats `waiting-for-secrets` like `pulling`: in progress, not failed. It always precedes `pulling`.
- No secret value is written outside `/run/ccattler/secrets/` and `/run/ccattler/gate-keys/` on a node, or into logs, events or `cca` output.

---

## Flow 28: Gate Key Sealing & Delivery (M176)

**Status:** designed 2026-11-07 (decisions: inline keys sealed through the API, plaintext deleted only after every agent fetches gate keys). Replaces the key half of Flow 13; certificates stay plain facts. Spec: [config-secrets.md](config-secrets.md#gate-tls-private-keys).

**Problem**: gate TLS private keys are plain facts (`derived/route/{gate}/tls/key` for auto, `desired/route/{gate}/tls/key` for inline manual keys). Anyone with etcd read access, including every agent, holds every gate's TLS identity.

**Invariant**: a gate private key exists in etcd only as an envelope under `secrets/`. Only the API server decrypts it, and only for an enrolled, non-disabled node whose gate proxy is enabled (decision B: every such node serves every gate). The agent's gate proxy uses a certificate/key pair delivered together, never a certificate from facts with a key from elsewhere.

```
Where the key of a TLS gate lives
  tls auto                         → secrets/route/{gate}/tls/key   (system key, route-tls)
  tls { cert "…" key "…" }  inline → secrets/route/{gate}/tls/key   (system key, sealed via API)
  tls { cert "…" key secret "n" }  → secrets/{n}                    (user secret, cca secret set)
                                     desired/route/{gate}/tls/key_secret = n
  certificate: derived/route/{gate}/tls/cert (auto) or desired/route/{gate}/tls/cert (manual)
  expiry:      derived/route/{gate}/tls/not-after (auto); all plain facts

1. Auto issuance (RouteTLSController, leader, holds the server's secret sealer)
   mode=auto, not-after missing or within 30 days
     → CA issues cert + key
     → one change group (one transaction):
         put derived/route/{gate}/tls/cert, derived/route/{gate}/tls/not-after
         put secrets/route/{gate}/tls/key = seal(key PEM)
     → no derived/route/{gate}/tls/key fact

2. Inline manual key (cca apply, writes facts directly to etcd)
   compiler: key PEM is kept out of the fact list
   apply writes the gate facts first (tls/mode = manual, tls/cert), then
     PUT /api/gate-key {gate, private_key_pem}   (capability secret.write, cluster scope)
       → server validates the gate name and that the PEM is a private key matching tls/cert
       → put secrets/route/{gate}/tls/key = seal(key PEM); audit (gate, principal)
   PUT fails → apply exits non-zero naming the gate; the gate has no key until a
     successful apply (handshakes for its host fail, as with a missing cert)
   order matters: facts first, so step 4 never sees a sealed key without its gate

3. key secret reference (compiler)
   desired/route/{gate}/tls/key_secret = n   (ValidateSecretName, never a route/
     name; writing it needs secret.use like any secret binding; the secret itself
     is created with cca secret set); no key material passes through apply
   the key secret counts only in manual mode (types.GateUsesKeySecret: delivery,
     route-tls and the migration share this rule)

4. Ownership and cleanup (RouteTLSController)
   system key needed ⇔ gate exists ∧ tls set ∧ ¬GateUsesKeySecret(mode, key_secret)
   auto gate with a valid certificate but no sealed key (and no plaintext key
     waiting for migration) → issue a new pair
   not needed → delete secrets/route/{gate}/tls/key (with the stale derived facts)
   auto → manual inline: apply's PUT overwrites the system key; not deleted
   manual → auto: not-after missing → step 1 issues and overwrites

5. Delivery (agent → API, mTLS node principal)
   POST /api/node/gate-keys  (no body)
     → caller node:{id}, enrolled, not disabled, observed/node/{id}/gate_proxy = enabled
     → for every gate with tls: read cert (fact) + key (secrets/…, opened here)
       pair checked with tls.X509KeyPair; mismatch or missing → gate listed as pending
     → response: [{gate, host, certificate_pem, private_key_pem}], pending: [gate]
     → audit: node, gate names (never key material); refusals rate-limited
   store principal: secret-delivery (reads secrets/, desired/route/, derived/route/)

6. Agent cache (tmpfs) and gate proxy
   /run/ccattler/gate-keys/{gate}/certificate.pem, private-key.pem
     (dirs 0700, key 0400, root; write temp file + rename; gone gates removed)
   gate proxy certificate map ← cache only (facts still give host/service/sticky)
   fetch: at start; when the set of TLS gates or a certificate fact changes
     (store watch on desired|derived/route/*/tls/*); every 5 min as fallback;
     API unreachable or a gate pending → back off 2s…30s, keep serving from the cache
   cache loaded before the gate proxy starts; capability facts below are
     retried with backoff until written
   observed/node/{id}/gate_keys = ready | waiting (reason: unreachable, refused, pending)
   written at agent start (new facts):
     observed/node/{id}/gate_key_delivery = v1        (capability, every agent of this version)
     observed/node/{id}/gate_proxy = enabled | disabled   (--proxy; disabled nodes never fetch)

7. Cutover (gate-key-migration controller, leader, one-time and idempotent)
   for each plaintext desired|derived/route/{gate}/tls/key:
     secrets/route/{gate}/tls/key absent → seal it there (desired wins, as in the proxy)
   when every enrolled or registered, non-disabled node reports gate_key_delivery = v1
   (registered nodes count too, so a cluster without enrollment never completes early):
     delete the plaintext facts (the one sanctioned writer of desired/route/ besides
     the compiler; cca apply never removes desired/route/{gate}/tls/key and Diff
     never shows it); until then old agents keep reading them
   only gates that use a system key are sealed (a key_secret gate's leftover
     plaintext is just deleted); deleting facts leaves old etcd revisions:
     compact and defragment etcd after the migration completes
   persists observed/gate-keys/migration = {plaintext_count, state}
```

**Failure and recovery paths:**

```
server upgraded, some agents not yet
  → existing keys: plaintext kept until every agent reports v1 (step 7)
  → keys issued, renewed or applied in this window reach upgraded agents only (documented)
agent restart, same boot, API down → serve from /run/ccattler/gate-keys/
node reboot, API down → no gate TLS on this node; gate_keys = waiting (unreachable)
key rotated (renewal or re-apply) → certificate fact changes → agent re-fetches the pair
gate removed → step 4 deletes the system key; agent removes /run/ccattler/gate-keys/{gate}/
node disabled or unenrolled → delivery refused (403, audited); cached keys kept until
  the next successful fetch replaces them (documented: no remote wipe)
```

**What must match:**
- The key the delivery opens (step 5) is chosen by the same rule as ownership (step 4): `key_secret` if set, else the system key.
- The agent never combines a certificate from facts with a key from the cache: both come from one delivery response.
- `api/state_access.go` keeps hiding `/tls/key` facts from state and watch (defense in depth for the cutover window).
- `gate_proxy` is self-reported by the agent; it narrows delivery to nodes that run a proxy, it is not an authorization boundary beyond decision B.
- Server-side compiles (`/api/apply`, `/api/diff`, the policy gate) never read local files: gate `tls` cert/key paths and config `from` files are refused (`ValidateNoLocalFileReferences`), so inline keys reach the server only through `PUT /api/gate-key`, which the CLI refuses to send over plain HTTP to a non-loopback server.
- Controllers seal with the secret store's current key provider (`KeyProviderSource`), so a key rotation applies to new envelopes at once.
- The delivery response is capped at 4 MiB on the agent (about 1000 gates); beyond that the agent reports `unreachable` (known limit).

## Flow 29: Root Key, Keyring & Legacy Key Migration (M177)

**Status:** designed 2026-11-10. Decisions: fresh clusters start directly on v2 with no legacy key; M177 is delivered in four ordered steps and one release. Spec: [config-secrets.md](config-secrets.md#key-hierarchy) (key hierarchy, R1, R7, R8) and "Migration (upgrade order) and cutover gates".

**Problem**: every envelope under `secrets/` and `credentials/` is wrapped directly by `/etc/ccattler/secret-master.key`. That key was copied to every worker before M174, so it is treated as compromised. The server also generates the key silently when the file is missing, and the HA role does not copy it, so HA replicas can each hold a different key (a secret written through one replica cannot be opened by another).

**Invariant**: only API replicas hold the root key; every replica holds the same one (proved by `keyring/root_check`) or refuses to start. New envelopes are v2 (active KEK, AAD = store key) once the cluster is past the migration gate, or from the first write on a fresh cluster. The legacy key never wraps anything new, and after retirement no replica opens a v1 envelope.

```
Inputs on every API replica
  root key    /etc/ccattler/secret-root.key (32 bytes, 0400, root) | --kms-provider <name>
  legacy key  /etc/ccattler/secret-master.key (optional; upgraded clusters only)
  in-memory store (local simulation, tests): ephemeral root key generated in memory,
    never written; every persistent store requires the file or --kms-provider

1. Startup checks (before serving)
   root key missing, unreadable, size ≠ 32, group/world-readable → exit
   legacy key present and byte-identical to the root key           → exit
   no silent generation: loadOrGenerateSecretMasterKey is removed

2. Keyring bootstrap (first replica only; create-only transaction)
   keyring/active absent →
     KEK₁ = random 32 bytes
     txn [compare keyring/active rev 0]:
       put keyring/kek/1         = wrap(root, KEK₁)
       put keyring/kek/1/state   = active
       put keyring/active        = 1
       put keyring/root_check    = seal(root, fixed value)
       put keyring/legacy        = none | present
       put observed/keyring/migration = {state: completed, legacy_v1_envelope_count: 0}
                                        (fresh)  | {state: not_started} (upgrade)
     fresh  ⇔ no legacy key file on this replica ∧ no envelope under secrets/ or credentials/
     upgrade ⇔ otherwise (legacy key required; v1 envelopes without it → exit with a hint)
   CAS lost (another replica bootstrapped) → re-read keyring/*, continue at step 3

3. Every replica, every start
   open keyring/root_check with the root key; failure → exit (different root key)
   load KEKs it can unwrap; watch keyring/ (new versions, keyring/legacy changes)
   report observed/keyring/replica/{node-id} = {version, active_kek, root_check: ok}
     with a lease (TTL), so stopped replicas disappear from the set

4. Sealing and opening (one codec behind SecretStore, CredentialStore, controllers)
   write mode  = v2 if migration state ∈ {in_progress, completed} else v1
   seal v2     = magic | 2 | active_kek | wrap(KEK, DEK) | nonce | AES-GCM(DEK, value, AAD = store key)
   seal v1     = legacy layout, legacy key (upgraded clusters before the gate only)
   open        = magic → v2: KEK by version (unknown version → re-read keyring, else error)
                 no magic → v1: legacy key, unless keyring/legacy = retired → error
   AAD binds the envelope to its key: a value copied to another key does not open

5. Migration gate (POST /api/secret/migrate-keys {confirm_backup: true}; cca secret migrate-keys --confirm-backup)
   409 with every unmet condition unless:
     confirm_backup set
     every live replica report has this version, the active KEK, root_check ok
     every enrolled, non-disabled node reports secret_delivery = v2 (M174 report)
   → put intent/keyring/legacy_migration; migration state = in_progress (writes are v2 from here)

6. Migration (keyring controller, leader, bounded batches)
   scan secrets/ and credentials/ for v1 envelopes
   per batch (≤ keyringMigrationBatchSize, within the runner's transaction budget):
     open v1 with the legacy key → seal v2 (new DEK, AAD = its store key) → put with revision guard
     revision conflict → re-read in the next round (a concurrent user write is already v2)
     legacy key cannot open it → count as unreadable (e.g. written by an HA replica that
       generated its own key); never deleted, never skipped silently
   after each batch persist observed/keyring/migration =
     {state, legacy_v1_envelope_count, unreadable_count, migrated, started, completed}
   v1 count = 0 ∧ unreadable = 0 → state = completed
   crash at any point → resumes from the remaining v1 envelopes (idempotent)
   THIS IS THE ROLLBACK BOUNDARY

7. Retirement gate (POST /api/secret/retire-legacy-key; cca secret retire-legacy-key)
   409 unless: migration completed; full scan finds 0 v1 envelopes; no plaintext
     desired|derived/route/*/tls/key fact; every enrolled node reports legacy_key_file = absent
   → put keyring/legacy = retired; replicas drop the legacy key from memory and refuse v1
   Ansible then deletes secret-master.key on the control plane and asserts absence
   a replica that starts later with the file present: warns once, never loads it
```

**Failure and recovery paths:**

```
replica with another root key        → root_check fails → exits; others keep serving
two replicas bootstrap at once       → one CAS wins; the other reads its keyring
pre-M177 replica still running       → cannot report (step 3) and cannot open v2: upgrade
                                       every control-plane replica before migrate-keys
                                       (documented; the gate cannot see old replicas)
unreadable v1 envelopes              → migration stays in_progress with unreadable_count;
                                       operator re-sets those secrets (cca secret set) or
                                       copies the right legacy key; retirement stays refused
etcd restored to a pre-migration snapshot → restore the previous binaries too (runbook)
legacy key lost before migration     → v1 secrets unreadable; re-create them (documented)
```

**What must match:**
- One codec seals and opens for `SecretStore`, `CredentialStore`, `RouteTLSController`, `GateKeyMigrationController` and `PUT /api/gate-key`; nothing calls `SealEnvelope`/`OpenEnvelope` with a raw key any more.
- The AAD is the exact store key the envelope is written to (`secrets/{name}`, `secrets/route/{gate}/tls/key`, `credentials/{name}`); every writer passes it.
- The legacy key never wraps a KEK, `root_check` or a v2 DEK; tests prove it cannot open any of them.
- Store roles: a `keyring` principal owns `keyring/`, `intent/keyring/`, `observed/keyring/` and re-writes `secrets/` and `credentials/`; the `api-server` role still has no rule for `secrets/`, `credentials/` or `keyring/`.
- `cca init` writes `secret-root.key` (not `secret-master.key`) for new clusters; the HA role copies `secret-root.key` to every replica like the CA files.

## Checklist for New Features

When adding a new feature that touches the runtime:

1. **Does the main container get it?** If yes, check if init containers need it too (Flow 1).
2. **Does it need DNS?** If yes, verify the chain from dnsmasq to container `--dns` (Flow 2).
3. **Does it use secrets?** If yes, verify master key distribution via `cca join` (Flow 3). After M174: secrets reach nodes only through delivery (Flow 27); never read `secrets/` on an agent.
4. **Does it use advertise address?** If yes, ensure it's propagated consistently (Flow 6).
5. **Does it need cross-host networking?** If yes, verify all three endpoint facts exist (Flow 7).
6. **Does it add a new env var or config to containers?** If yes, ensure it's resolved at materialization time, not stored pre-rendered.
7. **Does it use instance state to make decisions?** If yes, determine whether the decision is about runtime behavior (use `endpointEligibleState`) or scheduling intent (use `effectiveInstanceState`). Never use `node_failure` markers for runtime decisions (Flow 8).
8. **Does it start containers?** If yes, verify it goes through the image pull lifecycle (Flow 14) — secrets delivered first when needed (Flow 27), then pull, start with `--pull=never`. Never assume the image is cached.
9. **Does it expose external HTTPS?** If yes, verify certificates flow as facts and private keys only through sealed storage and gate-key delivery (Flows 13, 28). The agent never issues certs.
10. **Does it mount volumes from several instances?** If yes, check whether the provider supports shared attach (Flow 14); exclusive volumes need all instances on one node.
