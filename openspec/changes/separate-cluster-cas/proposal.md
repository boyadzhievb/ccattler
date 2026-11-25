# Proposal

Milestone: **M280 — Every Cluster-CA Certificate Is an etcd Root Client** (milestones.md Phase 280, security bug).

## Why

One CA, `ca.pem`, signs every certificate in a cluster, and both etcd and the API trust it for client authentication
(checked in the code, 2026-11-25):

| Certificate | Issued by | CommonName | Usages | Used for |
|---|---|---|---|---|
| node (`node.pem`, `cca init`, `cca join` CSR) | cluster CA | node ID | client | API and **etcd** client |
| server (rotated, `command_server.go:770`) | cluster CA | `ccattler-server` | server + client | API listener; principal bound to cluster-admin (`command_server.go:476`) |
| etcd (`etcd.pem`, `cca init`) | cluster CA | `etcd-<node>` | server + client | etcd listener |
| gate (`RouteTLSController`, `route_tls.go:162`) | cluster CA | **the route's hostname** | **server + client** | gate proxies; key delivered to gate nodes |

etcd has no RBAC, so any certificate the cluster CA issues with ClientAuth is a full-rights etcd client. The API's
`MTLSAuthenticator` (`security/authn_mtls.go:34`) turns any verified client certificate into `node:<CommonName>`.
A route hostname is chosen by a `workload.create` user and is not validated. So a gate named `ccattler-server` yields
a certificate that authenticates as the cluster-admin server principal to the API, and as an etcd root client.
Whoever holds the gate key, any gate node, can read and write every fact and secret. M171 closed this hole for join
tokens; gates bypass it. This is reasoned from the code; task 1 reproduces it in a test first.

Kubernetes avoids this by giving each purpose its own CA: the cluster CA for API identities, an etcd CA trusted only
by etcd, and a front-proxy CA. Serving certificates never act as client identities.

## What Changes

- **Three CAs for new clusters** (user decision 2026-11-25, option A). `cca init` creates:
  - the **cluster CA**, as today, for node and server identities only;
  - an **etcd CA**, which signs etcd's serving certificate and the etcd client certificates of the server and agents.
    etcd trusts only this CA;
  - a **gate CA**, which signs gate serving certificates only. Neither the API nor etcd trusts it.
- **Gate certificates carry ServerAuth only**, in every cluster, old ones too. Go's TLS server, used by the API, and
  etcd both refuse a client certificate without the ClientAuth usage, so a gate key cannot authenticate anywhere.
  Gate certificates that still carry ClientAuth are re-issued on the next reconcile.
- **Node enrollment issues the etcd client certificate.** `cca join` receives, next to the node certificate, an etcd
  client certificate signed by the etcd CA. The agent uses it for etcd (`--etcd-cert`, `--etcd-ca`).
- **Existing clusters are not migrated** (user decision 2026-11-25, option 1). A cluster with only the cluster CA keeps
  working. `cca server` warns at every start that it shares one CA for etcd, the API and gates, and says to
  re-initialize. Its gate certificates still get the ServerAuth-only fix.
- **Docs:** the guide, `design/security-model.md`, `design/gate-tls-termination.md` (clients of a TLS gate trust
  `gate-ca.pem`), the threat model, and the Ansible templates (worker agent `--etcd-cert` and `--etcd-ca`; HA etcd
  trust).

Out of scope:
- A migration of existing clusters (`cca pki migrate`, or automatic): a follow-up milestone if users need it.
- etcd RBAC.
- Validating route hostnames: ServerAuth-only gate certificates make it unnecessary for this bug.
- The M276 audit.

## Capabilities

### New Capabilities
- `cluster-pki`: which CA signs which certificate, which usages each certificate carries, and what each component
  trusts for client authentication.

### Modified Capabilities
- `host-setup`: setup.sh installs the etcd and gate CAs and configures etcd to trust only the etcd CA.

## Impact

- **Code:** `security/ca.go` (certificate usage per purpose), `cmd/cca/command_init.go` (three CAs, the etcd client
  certificate, setup.sh), `security/enrollment.go` and `cca join` (etcd client certificate),
  `controllers/route_tls.go` (gate CA, ServerAuth only, re-issue), `cmd/cca/command_server.go` (flags, legacy
  warning).
- **Ansible:** the worker and HA templates.
- **Graphs:** SECURITY/AUTH and NETWORK (gate TLS).
- **Compatibility:**
  - New clusters change file layout: new files `etcd-ca.pem`, `gate-ca.pem` and `etcd-client.pem`, with keys.
  - TLS-gate clients that pinned `ca.pem` must trust `gate-ca.pem` on new clusters.
  - Old clusters keep their files.

How it is proven done:
- tests that a gate certificate is refused by the API's mTLS and by etcd (red first: today it is accepted);
- the init, setup and enrollment tests;
- a deploy-cluster check that a gate certificate cannot reach etcd or the API;
- the security-audit and code-review rounds (CLAUDE.md);
- one full Deploy Testbed run, which builds a fresh cluster with three CAs.
