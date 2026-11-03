# CCattler Threat Model

## System Overview

CCattler is a fact-based container orchestrator. The control plane stores all cluster state as key-value facts in etcd. Node agents observe runtime state and reconcile workloads. Communication flows:

```
User CLI ──→ API Server ──→ etcd (fact store)
                 ↑                ↑
            Node Agent ───────────┘
                 ↓
         Container Runtime (nerdctl/docker/process)
```

## Trust Boundaries

| Boundary | From | To | Mechanism |
|----------|------|----|-----------|
| B1: User → API | Untrusted user | API server | mTLS + RBAC + ABAC |
| B2: Agent → API | Node agent | API server | mTLS with node certificate |
| B3: Agent → etcd | Node agent | etcd store | **UNPROTECTED (see F1)** |
| B4: API → etcd | API server | etcd store | **UNPROTECTED (see F1)** |
| B5: Agent → Runtime | Agent code | os/exec (nerdctl/docker/shell) | Process isolation |
| B6: DSL → Compiler | User-authored .cca files | `cca apply` parser | Input validation |
| B7: MCP → etcd | MCP server | etcd store | **UNPROTECTED (see F1)** |
| B8: Node enrollment | New node | API server | One-time bootstrap token + mTLS |

## Assets

| Asset | Location | Sensitivity |
|-------|----------|-------------|
| Cluster state (all facts) | etcd | HIGH — full cluster control |
| Secret store (AES-256-GCM encrypted) | etcd under `secrets/` prefix | CRITICAL — encrypted at rest |
| Master encryption key (KEK) | Process memory | CRITICAL — decrypts all secrets |
| CA private key | Server process memory / disk | CRITICAL — can mint any identity |
| Node private keys | `/etc/ccattler/node-key.pem` | HIGH — node impersonation |
| Bootstrap tokens | etcd under `enrollment/token/` | HIGH — single-use, time-limited |
| Cloud credentials (AWS/GCP/Azure) | Materialized to filesystem | HIGH — cloud account access |
| Workload JWT tokens (OIDC) | Projected to filesystem | MEDIUM — scoped, short-lived |

## Threat Categories

### T1: etcd Compromise (CRITICAL)

**Attack surface (historical — FIXED via Phase 45/110):** etcd originally listened on port 2379 without TLS or authentication. Now secured with mTLS (Phase 45 added TLS client connections, Phase 110 configured etcd deployment with mutual TLS). Without mTLS, any host on the network could read/write all cluster state.

**Impact:** Full cluster takeover — attacker can:
- Read all secrets (encrypted, but can delete or corrupt them)
- Modify placements to schedule workloads on attacker-controlled nodes
- Inject desired state to run arbitrary container images
- Delete all state (denial of service)
- Overwrite node enrollment records

**Mitigations (existing):**
- etcd configured with `--client-cert-auth=true` and mTLS certificates (Phase 110)
- `EtcdStoreConfig` supports `--etcd-cert/--etcd-key/--etcd-ca` flags for client TLS (Phase 45)
- All deployment templates (Ansible, Lima) use `https://` endpoints and pass TLS flags
- E2E tests use TLS flags for etcdctl commands
- Secret store uses AES-256-GCM encryption at rest
- Endpoint scheme validation rejects `http://` when TLS is configured (Phase 45)

### T2: Command Injection via Runtime Exec (HIGH)

**Attack surface:** User-supplied values flow into os/exec calls:
- `Spec.Image` → ProcessRuntime splits on whitespace and execs directly
- `ExecSpec.Command` → passed to container runtime exec
- `Spec.Env` → injected as environment variables
- Container image names → passed to nerdctl/docker CLI

**Impact:** Arbitrary command execution on the node host.

**Mitigations (existing):**
- ContainerRuntime passes image as a single argument (no shell expansion)
- ProcessRuntime uses `exec.Command` (no shell), splits on whitespace
- ValidateResourceName at API boundaries (Phase 39a) — rejects most injection payloads in service/instance names

**Mitigations (added, Phase 45):**
- Image format validation before passing to any runtime
- Env var key validation rejects shell metacharacters
- `ValidateResourceName` at API boundaries rejects injection payloads

### T3: Secret Leakage (MEDIUM)

**Attack surface:** Decrypted secret values flow through multiple code paths:
- SecretProvider.GetSecretForService returns plaintext
- Agent writes plaintext to filesystem (mount path)
- Error messages may include secret metadata
- Logging calls near secret operations

**Impact:** Plaintext secret exposure in logs, errors, or CLI output.

**Mitigations (existing):**
- Secret store operations log secret names but not values
- MCP server redacts sensitive fields from tool output
- AES-256-GCM encryption in the store

**Mitigations (added, Phase 110):**
- API `describe` and `status` endpoints redact config values whose key matches sensitive patterns (PASSWORD, SECRET, TOKEN, API_KEY, AUTH, CONN_CMD, etc.)
- Node cert permissions set to 0600 in all deployment paths

### T4: Node Enrollment MITM (HIGH)

**Attack surface:** `cca join --ca-cert` is optional. Without it, `InsecureSkipVerify: true` allows MITM during bootstrap.

**Impact:** Attacker intercepts enrollment, obtains valid node certificate, joins cluster as a trusted node.

**Mitigations (existing):**
- CA fingerprint embedded in composite join token — `cca token create` returns `<token>.<ca-fingerprint>` and `cca join` verifies the server cert fingerprint during TLS handshake (Phase 45)
- If `--ca-cert` is provided, standard CA validation is used
- TLS 1.3 minimum version
- Bootstrap tokens are single-use and time-limited
- Enrollment source IP logged in audit trail (Phase 110)

### T5: Privilege Escalation via Fact Store (MEDIUM)

**Attack surface:** A compromised node agent has write access to `observed/` prefixes. If write domain enforcement is only in the controller runner, a malicious agent could write to arbitrary keys.

**Impact:** A compromised node could manipulate placement, endpoint, or service facts.

**Mitigations (existing):**
- RBAC + ABAC authorization via `AuthorizedStore` wrapper
- Controller write domain enforcement in runner (Phase 43)
- Node agents are bound to specific key prefixes via RBAC roles

**Residual risk (corrected 2026-11-03):** Agents connect to etcd directly with a CA-signed certificate and do not use `AuthorizedStore`, and etcd has no roles configured. Every agent therefore has full read/write access to every key in all modes, not just development mode. A compromised node can rewrite desired state, grants and enrollment records. Fix: M176.

### T6: Denial of Service (MEDIUM)

**Attack surface:**
- API rate limiter protects HTTP endpoints
- No rate limiting on store Watch connections (etcd watch multiplexer helps)
- No resource limits on workload count per node

**Mitigations (existing):**
- API rate limiter (Phase 39a)
- Watch multiplexer reduces etcd load
- Tenant quotas (Phase 12)

### T7: API State Endpoint Exposes Sensitive Prefixes (CRITICAL)

**Attack surface:** `/api/state` and `/api/watch` endpoints allow querying any fact store prefix including `secrets/`, `credentials/`, `enrollment/token/`, and `bootstrap/`. An authenticated API client can read encrypted secrets, enrollment tokens (stored with plaintext token values as keys), and the bootstrap token.

**Impact:** Token theft (cluster enrollment), encrypted secret ciphertext exposure (enables offline attack), bootstrap token theft.

**Mitigations (existing):** RBAC restricts which principals can access which prefixes — but only when RBAC is configured.

**Mitigations (existing):**
- API prefix denylist blocks `secrets/`, `credentials/`, `enrollment/token/`, `bootstrap/` from state/watch endpoints (Phase 45)
- Join tokens stored as SHA-256 hashes — plaintext never persisted to etcd (Phase 110)
- Token listings show only the first 8 characters

**Correction (2026-11-03):** the Phase 45 denylist matched whole prefixes only, so `?prefix=secret`, `?prefix=s` or `?prefix=enroll` still returned those keys, and gate TLS private keys under `desired/route/{name}/tls/key` and `derived/route/{name}/tls/key` were readable with `workload.read`. **Mitigations (added, M173):** an allowlist of readable roots (Flow 25) plus a per-key filter for `/tls/key` on scans, single keys and watch events.

### T8: Enrollment Sends Private Key Over Network (HIGH)

**Attack surface:** The legacy enrollment flow has the server generate the node's keypair and return the private key PEM in the enrollment response.

**Impact:** Node identity compromise — any network observer (or compromised server) obtains a valid node key.

**Mitigations (existing):**
- CSR-based enrollment (Phase 110): node generates its own ECDSA P-256 keypair locally, sends only the CSR to the server, server signs and returns only the certificate. Private key never leaves the node.
- Legacy flow preserved for backward compatibility with old servers, but logs a deprecation warning.
- TLS is available for the API server.

**Residual risk:** Legacy enrollment flow (without CSR) is still available for backward compatibility but logs a deprecation warning. A future release should remove the legacy path entirely.

### T9: Config File Path Traversal (MEDIUM)

**Attack surface:** Container config file mount paths from DSL (`config web { file "../../etc/shadow" = "..." }`) are passed to `filepath.Join` without containment validation. A `../` path can write arbitrary files on the host.

**Impact:** Arbitrary file write as the CCattler process user.

**Mitigations (existing):**
- Config file path containment validation added (Phase 45) — paths containing `..` are rejected, joined path verified to stay within temp directory

### T10: Container Escape (LOW — out of scope)

**Attack surface:** Standard container runtime escape vectors (kernel exploits, misconfigured capabilities). CCattler delegates to nerdctl/docker — this is a container runtime concern, not an orchestrator concern.

**Mitigations (existing):**
- No `--privileged` flag in container start
- No capability additions beyond defaults

### T11: Enrollment Identity Spoofing (CRITICAL)

**Attack surface:** `SignCSR` copied the CSR subject and SANs into the node certificate, and the mTLS authenticator maps the certificate CN to `node:<CN>`. The server certificate's CN is `ccattler-server`, and `node:ccattler-server` is bound to cluster-admin.

**Impact:** Anyone holding a join token could request CN `ccattler-server` and become cluster-admin. CSR SANs naming the API or etcd host produced a valid server certificate for those hosts.

**Mitigations (added, M171):** The CA sets CN to the validated node ID and ignores the CSR subject and SANs. Enrolled certificates are client-auth only. Reserved node IDs and IDs holding role grants are refused, and the join token is consumed in the same transaction that records the node.

### T12: Unauthenticated Agent Debug API (CRITICAL)

**Attack surface:** The agent debug API serves plain HTTP with no authentication. Ansible binds it to the host IP. `/debug/exec` runs any command in any container, and `/debug/images/pull` pulls any image.

**Impact:** Anyone who can reach port 9771 can execute commands in every container on the node.

**Mitigations (added, M172):** Users reach the debug API only through `/api/node/debug/{node}/{endpoint}` on the API server, which checks a per-endpoint capability (`exec` needs `workload.exec`, `images/pull` needs `node.manage`). The agent serves TLS 1.3, requires a cluster-CA client certificate, and admits only principals holding the cluster-admin grant (the API server). Without TLS credentials it refuses non-loopback addresses. The API server pins the agent certificate CN to the node ID and does not forward the caller's credentials (Flow 24).

### T13: Cluster Master Key on Every Node (CRITICAL)

**Attack surface:** The enrollment response includes the secret master key. Every node can decrypt every secret ciphertext it can read from etcd, which is all of them (see T5).

**Impact:** One compromised node exposes every tenant's secrets.

**Mitigations:** M174. Per-node secret delivery, versioned keyring.

## File Permission Summary

| File | Expected | Actual | Status |
|------|----------|--------|--------|
| CA cert (`ca.pem`) | 0644 | 0644 | OK (public) |
| CA key | Removed from non-CA nodes | Removed | OK |
| Node cert (`node.pem`) | 0600 | 0600 | OK |
| Node key (`node-key.pem`) | 0600 | 0600 | OK |
| Materialized secrets | 0600 | 0600 | OK |
| Secret mount directories | 0700 | 0700 | OK |

## Priority Fixes

| Priority | Item | Threat | Status |
|----------|------|--------|--------|
| P0 | Add TLS to etcd client connection | T1 | **FIXED (Phase 45)** |
| P0 | API prefix denylist for sensitive keys | T7 | **FIXED (Phase 45)** |
| P0 | Configure etcd deployment with mTLS | T1 | **FIXED (Phase 110)** |
| P1 | Validate image format before runtime exec | T2 | **FIXED (Phase 45)** |
| P1 | Config file path traversal protection | T9 | **FIXED (Phase 45)** |
| P1 | Env var key validation | T2 | **FIXED (Phase 45)** |
| P1 | Fix node cert permissions (0644 → 0600) | T3 | **FIXED (Phase 45)** |
| P1 | Validate etcd endpoint schemes | T1 | **FIXED (Phase 45)** |
| P2 | Make `--ca-cert` required for `cca join` | T4 | **FIXED (Phase 45)** — already enforced via CA fingerprint or `--ca-cert` |
| P2 | Switch to CSR-based enrollment | T8 | **FIXED (Phase 110)** |
| P2 | Store join token hashes, not plaintext | T7 | **FIXED (Phase 110)** |
| P2 | Redact config values matching sensitive patterns | T3 | **FIXED (Phase 110)** |
| P0 | CA sets node cert identity; atomic join-token use | T11 | **FIXED (M171)** — deploy validation pending |
| P0 | Authenticate the agent debug API | T12 | **FIXED (M172)** — deploy validation pending |
| P0 | Stop distributing the master key to nodes | T13 | Open (M174) |
| P0 | Restrict agent store access | T5 | Open (M176) |
