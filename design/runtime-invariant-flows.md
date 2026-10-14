# Runtime Invariant Flows

Cross-component configuration chains that must stay consistent. Each flow describes a path where a mismatch between components causes a hard-to-diagnose runtime failure.

Updated: 2026-10-14

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

## Checklist for New Features

When adding a new feature that touches the runtime:

1. **Does the main container get it?** If yes, check if init containers need it too (Flow 1).
2. **Does it need DNS?** If yes, verify the chain from dnsmasq to container `--dns` (Flow 2).
3. **Does it use secrets?** If yes, verify master key distribution via `cca join` (Flow 3).
4. **Does it use advertise address?** If yes, ensure it's propagated consistently (Flow 6).
5. **Does it need cross-host networking?** If yes, verify all three endpoint facts exist (Flow 7).
6. **Does it add a new env var or config to containers?** If yes, ensure it's resolved at materialization time, not stored pre-rendered.
