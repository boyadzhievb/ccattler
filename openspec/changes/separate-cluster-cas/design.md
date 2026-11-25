# Design

## Context

See proposal.md (Why) for the certificate table and the attack. Code read on 2026-11-25:

- **`security/ca.go`:**
  - `IssueCertificate` gives every certificate ServerAuth and ClientAuth (`:211`);
  - `SignCSR` gives ClientAuth only (`:356`);
  - `ServerTLSConfig` sets `ClientAuth: RequireAndVerifyClientCert` over one pool (`:257-258`).
- **`cmd/cca/command_init.go`:**
  - issues `node.pem` and `etcd.pem` from one CA (`:266-278`);
  - setup.sh units pass `--cert node.pem`, `--etcd-cert node.pem` (`:457-492`) and the etcd TLS drop-in with
    `ETCD_TRUSTED_CA_FILE=ca.pem`.
- **`cmd/cca/command_server.go`:**
  - the rotated server certificate has CN `ccattler-server` (`:770-777`), and `bindServerPrincipal` binds
    `node:<server CN>` to cluster-admin (`:476`);
  - `ClientCAs: caCertPool` (`:799`, `:842`).
- **`security/authn_mtls.go:34`:** any verified client certificate becomes `node:<CN>`.
- **`controllers/route_tls.go:162`:** gate certificates come from the cluster CA with CN = the route's hostname, TTL
  90 days, renewed on staleness or expiry. They are the only writer of `derived/route/{name}/tls/*` and
  `secrets/route/{name}/tls/key`.
- **`security/enrollment.go`:** `EnrollmentResponse` carries one certificate and the CA (`:169-174`).
- **Ansible:** the agent and HA templates use `ca.pem` for `--etcd-ca` and etcd's trust.
- The agent and server already take separate `--etcd-cert`, `--etcd-key` and `--etcd-ca` flags, so the etcd side
  needs new files, not new code paths.

No runtime flow in `design/runtime-invariant-flows.md` covers PKI. This change adds one (D7). The store keys
`derived/route/{name}/tls/*` keep their single writer, RouteTLSController; no new store keys.

## Goals / Non-Goals

**Goals:**
- A certificate made for one purpose cannot authenticate for another, on new clusters by construction.
- Old clusters close the gate hole without migration.
- A PKI graph a developer can read to answer "which CA, which file, who trusts it".

**Non-Goals:**
- Migrating existing clusters (user decision, option 1).
- etcd RBAC.
- Validating route hostnames.
- HA etcd peer certificates beyond what the Ansible template needs to keep working.

## Decisions

### D1. Three CAs, created by cca init
- **Files:** `ca.pem`/`ca-key.pem` (cluster), `etcd-ca.pem`/`etcd-ca-key.pem`, `gate-ca.pem`/`gate-ca-key.pem`, each
  self-signed, 10 years like today's CA, keys at 0600. setup.sh installs them in `/etc/ccattler/pki`.
- **The etcd CA** signs `etcd.pem` (ServerAuth, SANs as today) and `etcd-client.pem` (ClientAuth, CN
  `etcd-client-<node>`) for the control-plane node's server and agent.
- **The gate CA key** is needed only by `cca server`, where RouteTLSController runs; it lives on control-plane hosts.
- **Kubernetes comparison:** kubeadm creates `ca`, `etcd/ca` and `front-proxy-ca`; the etcd CA is not trusted by the
  API server for user clients.

### D2. Usage per purpose, not one default
`IssueCertificate` gets a required `Usage` field with typed constants: `UsageServer`, `UsageClient`,
`UsageServerAndClient`. That forces every caller to choose; no default exists.
- gate: `UsageServer`;
- etcd serving: `UsageServer`;
- etcd client: `UsageClient`;
- node: `UsageClient`, as SignCSR does today;
- server: `UsageServerAndClient`, because the server is also a client of agents' debug API (`node_debug_proxy.go`).

### D3. Gate certificates: gate CA when configured, ServerAuth only always
`RouteTLSController` takes an issuer: the gate CA when `--gate-ca-cert` and `--gate-ca-key` are set, otherwise the
cluster CA (old clusters). It issues `UsageServer` in both cases. On reconcile it parses the stored certificate and
re-issues when:
- its ExtKeyUsage includes ClientAuth (old certificates);
- or its issuer is not the configured gate CA, after an operator added one.

It is written as one change group, as today. Worst case per cycle: one re-issue per route, which is already the cost
of a renewal. The controller's change budget is unchanged.

### D4. etcd client certificates at enrollment
- **Enrollment:** `EnrollmentResponse` gains `EtcdCertificatePEM`, `EtcdPrivateKeyPEM` and `EtcdCAPEM`. The server
  signs a second CSR, or generates the key server-side, as today's node key flow does: D4 follows today's flow, with
  the etcd CA key, `UsageClient`, and CN `etcd-client-<node>`.
- **cca join:** writes `etcd-client.pem`, `etcd-client-key.pem` and `etcd-ca.pem`, and the agent unit uses them.
- **Old servers:** a server without an etcd CA returns none, and `cca join` keeps using `node.pem` for etcd, as
  today.

### D5. Trust per listener

| Listener | Client certificates trusted |
|---|---|
| API | cluster CA |
| etcd | etcd CA |
| agent debug API | cluster CA, as today |
| gate proxy | none (serving only) |

The API's `ClientCAs` is the cluster CA pool only. This is already so; it holds once gate certificates no longer come
from it. `MTLSAuthenticator` is unchanged: on a new cluster only node and server certificates exist under the cluster
CA, and on an old cluster gate certificates lack ClientAuth.

### D6. Old clusters
`cca server` checks at start whether `--etcd-ca` differs from `--ca` and whether gate CA flags are set. When the etcd
CA is the same file as, or equal to, the cluster CA, or no gate CA is set, it logs one `WARN` per start:

> this cluster uses one CA for etcd, the API and gates; re-initialize with cca init to separate them (M280)

It keeps running. D3 still applies.

### D7. PKI graph and flow
`design/graphs/PKI_GRAPH.md` (user request, 2026-11-25) holds:
- an inventory: each certificate's CA, CN, usages, TTL and rotation, disk path or store key, and the process that
  loads it;
- a trust matrix per listener;
- lifecycle entry points, linking the AUTHN_GRAPH and NETWORK_GRAPH call trees rather than copying them;
- debugging one-liners (`openssl x509 -noout -issuer -subject -ext extendedKeyUsage`).

It is written from today's code first (task 1.0), so it shows the single-CA problem, and updated with each task. A
short Flow "Cluster PKI" in runtime-invariant-flows.md links to it.

## Failure cases

| Case | Behaviour |
|---|---|
| Gate CA files configured but unreadable | `cca server` exits with the path and error, like the cluster CA today |
| Old cluster, no gate CA | Gate certificates come from the cluster CA with ServerAuth only; warning at start |
| Agent enrolled before M280 joins a new server | It keeps its node.pem for etcd; etcd (etcd CA only) refuses it; the agent logs a TLS error. Documented: re-run `cca join` (a new-cluster setup has no such agents) |
| Enrollment without an etcd CA on the server | No etcd certificate in the response; `cca join` keeps today's behaviour |
| Re-issue loop | A certificate with ServerAuth only from the configured issuer is never re-issued (test) |

## Risks / Trade-offs

- **[Ansible-built clusters]** The templates change with this release, and the deploy testbed builds fresh clusters,
  so the full run tests the new layout.
- **[Users who pinned ca.pem for TLS gates]** Their clients must trust `gate-ca.pem` on new clusters. This goes in
  the changelog and the guide.
- **[Old clusters stay exposed to the etcd/API identity mix]** The gate hole closes with D3, and old clusters get the
  warning. Node certificates remain etcd root clients there, as before. That is accepted under user decision 1.

## Migration Plan

None for existing clusters (user decision). New clusters: `cca init` from this release.
