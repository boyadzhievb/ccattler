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

**Attack surface:** etcd listens on port 2379 without TLS or authentication. Any host on the network can read/write all cluster state.

**Impact:** Full cluster takeover — attacker can:
- Read all secrets (encrypted, but can delete or corrupt them)
- Modify placements to schedule workloads on attacker-controlled nodes
- Inject desired state to run arbitrary container images
- Delete all state (denial of service)
- Overwrite node enrollment records

**Mitigations (existing):** None for the etcd connection. The secret store uses AES-256-GCM encryption at rest, limiting plaintext secret exposure even if etcd is compromised.

**Mitigations (needed):**
- Add TLS config to `EtcdStoreConfig` and wire through CLI flags
- Configure etcd with `--client-cert-auth` and mTLS certificates
- Validate endpoint schemes (`https://` required when TLS configured)

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

**Mitigations (needed):**
- Validate `Spec.Image` format before passing to any runtime
- Sanitize or restrict `ExecSpec.Command` content
- Audit all `fmt.Sprintf` patterns building command arguments

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

**Mitigations (needed):**
- Audit all logging near secret/credential paths for value leakage
- Ensure error messages never include plaintext secret content
- Verify CLI `describe` output does not print secret values

### T4: Node Enrollment MITM (HIGH)

**Attack surface:** `cca join --ca-cert` is optional. Without it, `InsecureSkipVerify: true` allows MITM during bootstrap.

**Impact:** Attacker intercepts enrollment, obtains valid node certificate, joins cluster as a trusted node.

**Mitigations (existing):**
- Warning printed when `--ca-cert` is missing
- TLS 1.3 minimum version
- Bootstrap tokens are single-use and time-limited

**Mitigations (needed):**
- Make `--ca-cert` required (or implement TOFU with fingerprint confirmation)
- Log enrollment source IP in audit trail

### T5: Privilege Escalation via Fact Store (MEDIUM)

**Attack surface:** A compromised node agent has write access to `observed/` prefixes. If write domain enforcement is only in the controller runner, a malicious agent could write to arbitrary keys.

**Impact:** A compromised node could manipulate placement, endpoint, or service facts.

**Mitigations (existing):**
- RBAC + ABAC authorization via `AuthorizedStore` wrapper
- Controller write domain enforcement in runner (Phase 43)
- Node agents are bound to specific key prefixes via RBAC roles

**Residual risk:** If RBAC is not configured (development mode), agents have full store access.

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

**Mitigations (needed):**
- API prefix denylist blocking `secrets/`, `credentials/`, `enrollment/token/`, `bootstrap/` from state/watch endpoints
- Store join token hashes instead of plaintext values

### T8: Enrollment Sends Private Key Over HTTP (HIGH)

**Attack surface:** The enrollment response includes the node's private key PEM. If the server is running without TLS (allowed by default), the key transits in cleartext.

**Impact:** Node identity compromise — any network observer obtains a valid node key.

**Mitigations (existing):** TLS is available but optional for the API server.

**Mitigations (needed):**
- Switch to CSR-based enrollment (node generates own key, sends CSR)
- Or enforce TLS for enrollment endpoint

### T9: Config File Path Traversal (MEDIUM)

**Attack surface:** Container config file mount paths from DSL (`config web { file "../../etc/shadow" = "..." }`) are passed to `filepath.Join` without containment validation. A `../` path can write arbitrary files on the host.

**Impact:** Arbitrary file write as the CCattler process user.

**Mitigations (needed):**
- Validate joined path stays within temp directory using `strings.HasPrefix`
- Reject config paths containing `..`

### T10: Container Escape (LOW — out of scope)

**Attack surface:** Standard container runtime escape vectors (kernel exploits, misconfigured capabilities). CCattler delegates to nerdctl/docker — this is a container runtime concern, not an orchestrator concern.

**Mitigations (existing):**
- No `--privileged` flag in container start
- No capability additions beyond defaults

## File Permission Summary

| File | Expected | Actual | Status |
|------|----------|--------|--------|
| CA cert (`ca.pem`) | 0644 | 0644 | OK (public) |
| CA key | Removed from non-CA nodes | Removed | OK |
| Node cert (`node.pem`) | 0600 | 0644 | **FIX NEEDED** |
| Node key (`node-key.pem`) | 0600 | 0600 | OK |
| Materialized secrets | 0600 | 0600 | OK |
| Secret mount directories | 0700 | 0700 | OK |

## Priority Fixes

| Priority | Item | Threat | Status |
|----------|------|--------|--------|
| P0 | Add TLS to etcd client connection | T1 | **FIXED (Phase 45)** |
| P0 | API prefix denylist for sensitive keys | T7 | **FIXED (Phase 45)** |
| P0 | Configure etcd deployment with mTLS | T1 | Ansible (infra) |
| P1 | Validate image format before runtime exec | T2 | **FIXED (Phase 45)** |
| P1 | Config file path traversal protection | T9 | **FIXED (Phase 45)** |
| P1 | Env var key validation | T2 | **FIXED (Phase 45)** |
| P1 | Fix node cert permissions (0644 → 0600) | T3 | **FIXED (Phase 45)** |
| P1 | Validate etcd endpoint schemes | T1 | **FIXED (Phase 45)** |
| P2 | Make `--ca-cert` required for `cca join` | T4 | Documented |
| P2 | Switch to CSR-based enrollment | T8 | Documented |
| P2 | Store join token hashes, not plaintext | T7 | Documented |
| P2 | Redact config values matching sensitive patterns | T3 | Documented |
