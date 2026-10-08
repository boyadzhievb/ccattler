I did a deeper read of the security-sensitive parts of `ccattler`—API/authentication, secret handling, workload execution, enrollment, etcd, and CI/CD.

## Executive summary

I found **one critical authentication vulnerability**, plus several high/medium-risk hardening issues.

| Finding                                                                                   | Severity                            | Area                           |
| ----------------------------------------------------------------------------------------- | ----------------------------------- | ------------------------------ |
| Spoofable `X-CCattler-User` grants cluster-admin in default server mode                   | **Critical**                        | Authentication / authorization |
| Default API binds `0.0.0.0` while TLS is disabled                                         | **Critical when network-reachable** | Network exposure               |
| Secret API returns plaintext secrets to callers with `secret.use`                         | **High / design risk**              | Secret management              |
| Unbounded request bodies on `/api/apply` and `/api/secret`                                | **High**                            | DoS                            |
| SSE watches can consume persistent server resources                                       | **Medium**                          | DoS                            |
| Legacy `/metric` endpoint bypasses API capability authorization                           | **Medium–High**                     | Authorization                  |
| GitHub Actions dependencies/actions aren't pinned immutably                               | **Medium**                          | Supply chain                   |
| `govulncheck@latest` in CI is mutable                                                     | **Medium**                          | Supply chain                   |
| Some authorization/resource operations use cluster-wide scope rather than resource scope  | **Medium**                          | Multi-tenancy                  |
| etcd dependency is patched for the major 2026 etcd advisories, but should be kept current | **Low/maintenance**                 | Dependencies                   |

### 1. Critical: local-user authentication is spoofable

This is the most important finding.

The non-TLS authenticator treats an HTTP header as the user's identity:

```text
X-CCattler-User: <username>
```

and turns it directly into `user:<username>`.

The server then explicitly binds:

```text
user:* -> cluster-admin
```

in non-TLS mode.

The API's capability layer does the same thing: `user:*` receives `cluster-admin`.

Worse, the default server address is:

```text
0.0.0.0:9770
```

and TLS is **off by default**.

So the default deployment is effectively:

```text
remote attacker
      |
      | HTTP
      v
0.0.0.0:9770
      |
      | X-CCattler-User: attacker
      v
user:attacker
      |
      v
cluster-admin
```

That means a network-reachable default server can be impersonated by simply choosing the username in the header.

This isn't merely an identity-spoofing issue: because `cluster-admin` includes all capabilities, it potentially enables workload creation/modification, scaling, node management, and secret operations. The API authorizer explicitly treats `cluster.admin` as implicitly granting every capability.

**Recommended fix:** never treat a client-controlled HTTP header as authentication unless the transport itself establishes that the header came from a trusted local boundary.

At minimum:

* bind non-TLS mode to `127.0.0.1`/Unix socket only;
* or require a real credential even in development mode;
* do **not** grant `user:*` cluster-admin;
* ideally make TLS/mTLS the default for server mode.

I'd treat this as a **release-blocking vulnerability**.

---

### 2. Critical deployment risk: insecure network exposure is the default

The combination of:

* `listenAddress = 0.0.0.0:9770`
* `tlsEnabled = false`
* spoofable local-user authentication

is particularly dangerous.

The README presents `cca server` as the cluster control plane, so this isn't an obscure test-only path.

A safer default would be something like:

```text
--listen 127.0.0.1:9770
```

for insecure/local mode, with an explicit `--listen 0.0.0.0:9770 --tls` required for network deployment.

---

### 3. High: secret retrieval is intentionally plaintext

`GET /api/secret?name=X` returns:

```json
{
  "name": "...",
  "value": "PLAINTEXT_SECRET"
}
```

and it requires `secret.use`.

That's potentially legitimate for a secret-management API, but it creates a large blast radius because `secret.use` means the caller can directly retrieve secret material—not merely use/mount it.

The built-in `api-writer` role does **not** receive `secret.use`, which is good; it only receives secret metadata access.

However, I'd strongly consider separating:

* `secret.metadata.read`
* `secret.mount`
* `secret.read`
* `secret.write`

and making direct plaintext retrieval an exceptional, auditable capability.

The underlying credential store does use envelope encryption, which is a positive control.

---

### 4. High: request bodies aren't bounded

Several handlers consume attacker-controlled bodies with `io.ReadAll` or unrestricted JSON decoding.

For example `/api/apply` does:

```go
rawBody, err := io.ReadAll(request.Body)
```

with no size limit.

The secret endpoint similarly reads the entire request body.

Because the API is a cluster control plane, an attacker who can authenticate—or exploit finding #1—can send extremely large bodies and force memory allocation.

**Fix:**

Use a hard limit:

```go
request.Body = http.MaxBytesReader(w, request.Body, 1<<20)
```

and use explicit limits appropriate to each endpoint.

I'd also limit JSON nesting/complexity where applicable.

---

### 5. Medium: persistent SSE connections need stronger controls

`/api/watch` creates a long-lived SSE connection and maintains it until the request context closes.

There is an `activeWatches` metric, but I don't see a hard maximum number of watches in this handler.

The normal rate limiter only limits **requests**, not persistent connections.

An authenticated client can therefore establish many long-lived streams, potentially consuming:

* goroutines
* file descriptors
* memory
* etcd/watch resources

The multiplexer reduces etcd pressure, which is good, but doesn't eliminate the API-side resource exhaustion vector.

**Fix:** impose a per-principal and global concurrent-watch limit, and preferably idle/maximum stream lifetimes.

---

### 6. Medium–High: legacy `/metric` bypasses API capability authorization

The modern `/api/metric` endpoint requires:

```text
workload.update
```

but the legacy `/metric` endpoint directly writes into the raw fact store after only calling the legacy authentication helper.

That means the two interfaces don't have equivalent authorization semantics.

This matters especially because the legacy endpoint is exposed by the same server.

I would either:

1. remove the legacy mutation endpoint, or
2. route it through the same `APIAuthorizer`/capability check as `/api/metric`.

This is also an architectural warning: **all externally reachable mutation paths should converge on one authorization layer.**

---

### 7. Medium: resource authorization is largely cluster-scoped

Many handlers authorize with:

```text
ScopeCluster
```

rather than the actual service/tenant/resource being manipulated. For example, scaling and node-management operations use cluster-wide capability checks.

The codebase does have tenant-aware machinery—`PolicyGate`, `ScopedScan`, `TenantAuditView`, etc.—which is encouraging. But the API layer still has several operations where the authorization decision is effectively:

```text
Does this principal have X at cluster scope?
```

rather than:

```text
Does this principal have X for this particular service/node/tenant?
```

That creates risk of **cross-tenant privilege escalation** if a principal gets a capability that should only apply to its own tenant.

This deserves targeted authorization tests for:

* tenant A → tenant B service update
* tenant A → tenant B scaling
* tenant A → tenant B secret use
* tenant A → tenant B node operations
* wildcard principal grants
* nested/hierarchical scopes

---

### 8. CI/CD supply-chain weaknesses

The workflows use floating action tags such as:

```text
actions/checkout@v7
actions/setup-go@v7
actions/upload-artifact@v7
```

rather than immutable commit SHAs.

More notably, the security audit workflow executes:

```text
go install golang.org/x/vuln/cmd/govulncheck@latest
```

so the security tool itself isn't pinned.

For a security-sensitive project, I'd pin:

```text
action@<full-40-char-SHA>
```

and pin `govulncheck` to a known version, preferably with dependency verification.

The release workflow has `contents: write`, so supply-chain compromise in the build/release job could become a repository/release compromise.

---

### 9. Positive finding: command execution doesn't appear to use a shell for normal process startup

The process runtime uses `exec.Command(args[0], args[1:]...)` rather than `sh -c`, and additionally rejects common shell metacharacters for the main workload command.

The container runtime likewise passes arguments directly to `nerdctl`/Docker rather than constructing a shell command.

That's substantially better than string-interpolating user input into shell commands.

One caveat: `Exec`, `ExecInit`, and their callers deserve a separate review because the interface explicitly represents commands as strings, and those paths have fewer of the validation checks applied by `Start`.

---

### 10. Dependency posture: etcd 3.7.1 is currently on a patched security baseline

`ccattler` pins `go.etcd.io/etcd/client/v3` to **3.7.1**.

That's significant because two 2026 etcd vulnerabilities affecting earlier 3.7 releases—an RBAC Watch authorization bypass and a TLS handshake resource-exhaustion issue—were fixed specifically in 3.7.1. ([GitHub][1])

The current upstream changelog shows etcd **3.7.2** exists as of September 22, 2026, so I'd still run the repository's `govulncheck` against the complete dependency graph and consider updating to the latest compatible patch release. ([GitHub][2])

I would **not** classify the pinned 3.7.1 itself as vulnerable based on the advisories I checked.

---

## What I'd fix first

**P0 — immediately**

1. Remove `X-CCattler-User` as an authentication mechanism for remotely reachable servers.
2. Change the default server listener from `0.0.0.0` to loopback unless TLS/authentication is explicitly enabled.
3. Add a regression test proving that an arbitrary client cannot become `cluster-admin` by setting `X-CCattler-User`.

**P1**

4. Put hard request-size limits on `/api/apply`, `/api/secret`, enrollment, and all JSON APIs.
5. Put global/per-principal limits on SSE connections.
6. Make `/metric` use the same authorization path as `/api/metric`.
7. Add cross-tenant authorization tests for every mutating endpoint.
8. Pin GitHub Actions to immutable SHAs and pin `govulncheck`.

**P2**

9. Separate secret retrieval from secret-use permissions.
10. Review `Exec`/`ExecInit` end-to-end.
11. Update etcd and rerun `govulncheck`.
12. Add security-focused fuzzing around the DSL parser/compiler and API JSON/DSL boundaries.

### Bottom line

The code has **substantial security architecture already present**—mTLS, OIDC, capability authorization, tenant policy gates, encrypted secret storage, audit logging, and input validation—but the **default non-TLS authentication path undermines much of it**.

The `X-CCattler-User` + `user:* → cluster-admin` combination is the issue I'd remediate before treating the control plane as safe for any network-accessible deployment.

[1]: https://github.com/advisories/GHSA-6vch-q96h-7gc3?utm_source=chatgpt.com "etcd: `tlsListener.acceptLoop` spawns unbounded handshake goroutines with no deadline · CVE-2026-73500 · GitHub Advisory Database · GitHub"
[2]: https://github.com/etcd-io/etcd/blob/main/CHANGELOG/CHANGELOG-3.7.md?utm_source=chatgpt.com "etcd/CHANGELOG/CHANGELOG-3.7.md at main · etcd-io/etcd · GitHub"
