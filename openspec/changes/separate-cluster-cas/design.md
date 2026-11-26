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
- **Files:** names as in D1a (they supersede the first draft's `ca.pem`, `etcd-ca.pem` and `gate-ca.pem`):
  `cluster-identity-ca.pem`, `etcd-access-ca.pem` and `gate-https-ca.pem`, each with `-key.pem`. Each is
  self-signed and valid 10 years like today's CA, with keys at 0600. setup.sh installs them in `/etc/ccattler/pki`.
- **The etcd access CA** signs `etcd-serving-certificate.pem` (ServerAuth, SANs as today) and two client
  certificates (ClientAuth): `cca-server-etcd-client-certificate.pem` (CN `etcd-client-cca-server`) and
  `node-etcd-client-certificate.pem` (CN `etcd-client-node-<node>`).
- **HA secondaries:** `cca init --ca/--ca-key --etcd-access-ca/--etcd-access-ca-key --gate-https-ca/--gate-https-ca-key`
  reuses all three CAs of the first node, all six flags or none. A secondary reusing only the cluster CA would mint
  CAs its peers do not trust.
- **The gate CA key** is needed only by `cca server`, where RouteTLSController runs; it lives on control-plane hosts.
- **Kubernetes comparison:** kubeadm creates `ca`, `etcd/ca` and `front-proxy-ca`; the etcd CA is not trusted by the
  API server for user clients.

### D1a. Self-explanatory names and descriptions (user request, 2026-11-25, during apply)
New clusters use file names that say what each certificate is, and every certificate describes itself in its
Subject (O, OU) and in a one-line comment before the PEM block. Go's `pem.Decode` and `openssl` ignore text before
`-----BEGIN`.

Names revised the same day (user, 2026-11-25): longer names that say who presents each certificate and to whom; the
etcd client certificates are split per presenting process, and the API server gets its own certificate. In `cca init`
clusters the API server used to present `node.pem` (setup.sh `--cert …/node.pem` for `cca server`,
command_init.go:457), the same file as the local agent's identity.

| File (new clusters) | Replaces | Signed by | Subject O / OU / CN | Usages |
|---|---|---|---|---|
| `cluster-identity-ca.pem` | `ca.pem` | self | CCattler / cluster identity CA / CCattler cluster identity CA | CA |
| `etcd-access-ca.pem` | (new) | self | CCattler / etcd access CA / CCattler etcd access CA | CA |
| `gate-https-ca.pem` | (new) | self | CCattler / gate HTTPS CA / CCattler gate HTTPS CA | CA |
| `api-server-identity-certificate.pem` | `node.pem` (as `cca server --cert`) | cluster identity CA | CCattler / API server identity / `ccattler-server` | server + client (client for the agent debug API) |
| `node-identity-for-api.pem` | `node.pem` (as agent `--cert`) | cluster identity CA | CCattler / node identity for API / `<node-id>` | client |
| `etcd-serving-certificate.pem` | `etcd.pem` | etcd access CA | CCattler / etcd serving certificate / `etcd-server-<node-id>` | server + client (etcd's self-connection, etcd issue 9785; decision 2026-11-25) |
| `etcd-peer-certificate.pem` | `etcd.pem` (as HA `--peer-cert-file`) | etcd access CA | CCattler / etcd peer certificate / `etcd-peer-<node-id>` | server + client (HA peers; decision 2026-11-25) |
| `cca-server-etcd-client-certificate.pem` | `node.pem` (as server `--etcd-cert`) | etcd access CA | CCattler / cca-server etcd client / `etcd-client-cca-server` | client |
| `node-etcd-client-certificate.pem` | `node.pem` (as agent `--etcd-cert`) | etcd access CA | CCattler / node etcd client / `etcd-client-node-<node-id>` | client |
| rotating server certificate (memory) | — | cluster identity CA | CCattler / API server identity / `ccattler-server` | server + client |
| gate certificates (store) | — | gate HTTPS CA | CCattler / gate HTTPS / `<route host>` | server |

- **CN column changed during apply** (main session decision, 2026-11-25, finding SA2-L4): the etcd CNs were
  `etcd-<node-id>` (serving) and `etcd-client-<node-id>` (node client), which overlap (node `client-cca-server` got the
  server's etcd client CN). They are now the disjoint prefixes `etcd-server-<node-id>`, `etcd-peer-<node-id>`,
  `etcd-client-node-<node-id>` and `etcd-client-cca-server`. Clusters set up before M280 keep their certificates.

- **Per-node identities:** each node has its own etcd client certificate and key (`cca join` issues the node's); none
  is shared between nodes or between the server and an agent.
- **What enforces the boundary:** certificate purpose (ExtKeyUsage), issuer trust per listener (D5) and explicit
  identity validation (the CommonName, M171 reserved names). Never the OU, which is descriptive only.
- **Out of M280, by user decision 2026-11-25:** restricting what each etcd identity may access (etcd RBAC: users,
  roles per key prefix, `auth enable`). It belongs to the existing milestone M181 "Store Access Isolation" (etcd roles from the certificate CommonName, an API gateway as the target, short-lived certificates; triaged from review 3 on 2026-11-23). A duplicate M281 was created by mistake and folded back into M181 the same day.
  - **Why separate:** M280 fixes which certificates are trusted; RBAC fixes what an accepted identity may do. The
    immediate fix does not depend on RBAC. Enabling etcd auth changes every client connection, and it needs an
    inventory of operations, a permission model, bootstrap and recovery, and tests for every path.
- **Residual trust, stated explicitly in M280:** etcd's `--client-cert-auth` only checks that a certificate chains to
  the etcd access CA. The CommonName becomes an etcd user only once etcd auth is enabled. Until M181, therefore:
  - every holder of an accepted etcd client certificate, the server's or any node's, has full etcd access;
  - a compromised node, gate nodes included, can still read and write the whole store with its own node etcd client
    certificate. M280 closes the gate-certificate path, not that one;
  - docs, PKI_GRAPH, the security model and the changelog SHALL NOT describe the new CommonNames as separately
    authorized etcd identities. They are distinct identities that M181 can authorize differently.

- **Keys:** the same names with `-key.pem`.
- **Comment line:** for example `# CCattler node etcd client of node ctrl, signed by etcd-access-ca.pem, used by cca
  agent to connect to etcd`.
- **Identity:** authorization keeps using the CommonName only; the OU is descriptive and never authorizes.
- **Old clusters:** they keep their files and units; no rename.
- **Where the names live:** the file names are constants in one place; setup.sh, `cca join` and the Ansible templates
  use them.

### D2. Usage per purpose, not one default
`IssueCertificate` gets a required `Usage` field with typed constants: `UsageServer`, `UsageClient`,
`UsageServerAndClient`. That forces every caller to choose; no default exists.
- gate: `UsageServer`;
- etcd serving and etcd peer: `UsageServerAndClient`.
  - **The decision:** user, 2026-11-25, option 1a, the kubeadm model.
  - **The draft:** said ServerAuth only for etcd serving.
  - **Found during apply:** HA peers present their certificate as a client certificate under
    `--peer-client-cert-auth` (etcd-cluster.conf.j2:22-25), and etcd dials itself with its serving certificate
    (etcd issue 9785; kubeadm's etcd server certificate carries ClientAuth for this reason).
  - **Why it is safe:** only etcd trusts the etcd access CA, so this ClientAuth reaches etcd and nothing else. Before
    M280, the same certificate was also accepted by the API.
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
  the etcd CA key, `UsageClient`, and CN `etcd-client-node-<node>`.
- **The server needs the etcd access CA key** to sign: new flags `--etcd-access-ca-key` (with `--etcd-ca`), set by
  setup.sh on control-plane hosts.
- **cca join:** writes `node-identity-for-api.pem`, `node-etcd-client-certificate.pem` and `etcd-access-ca.pem`, with
  their keys, and the agent unit uses them.
- **Old servers:** a server without an etcd access CA key returns none, and `cca join` keeps today's file names and
  uses the node certificate for etcd, as before.

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

### D8. The etcd peer rule is reconciled by a timer (user decision "go for a", 2026-11-26, during apply)
Every node's etcd client certificate comes from the etcd access CA, which etcd's peer port also trusts, so the
etcd-cluster role limits 2380/tcp to the etcd members with an nftables table (SA-L1). Anything that flushes the
ruleset while etcd runs removes that rule: nftables' first start, a reload, restart or stop, a manual
`nft flush ruleset`. Following `nftables.service` with a reload unit missed the first start (rework 6).
- **The decision:** re-sync instead of trusting, as kube-proxy does. `ccattler-etcd-peer-firewall-sync.timer` runs
  `ccattler-etcd-peer-firewall-sync.service` (oneshot, no `RemainAfterExit`, `TimeoutStartSec=` shorter than the
  interval) at boot and every `etcd_peer_firewall_sync_interval` (30 s, role default). `nft -f` replaces the table
  atomically, so each run is idempotent.
- **The bound:** the rule is missing for at most one interval plus the timer accuracy (about 31 s by default).
- **What stays:** the boot firewall unit, and etcd's `Requires=` on it plus `ExecStartPre=+`, so etcd never starts
  without the rule.
- **What the role never does:** enable, start or depend on `nftables.service`, whose `flush ruleset` would also wipe
  the cca agent's data-plane tables.
- **Evidence:** template tests (`TestEtcdPeerFirewallSyncTimer`, `TestEtcdRoleLeavesNftablesServiceAlone`) and
  systemd models; on a host at the next E2E HA run (the deploy run does not use this role).

## Failure cases

Rows changed during apply (main session, 2026-11-25, finding CR3-L4): the HA-secondary row follows CR-L7 and SA2-L3,
and the two `cca server` exit rows record user decision 1 and SA3-L2. Their wording was aligned with the code
(main session, 2026-11-25, findings CR4-L3 and SA4-L2).

| Case | Behaviour |
|---|---|
| Gate CA files configured but unreadable | `cca server` exits with the path and error, like the cluster CA today |
| Old cluster, no gate CA | Gate certificates come from the cluster CA with ServerAuth only; warning at start |
| Agent enrolled before M280 joins a new server | It keeps its old `node.pem` for etcd; etcd (etcd access CA only) refuses it; the agent logs a TLS error. Documented: re-run `cca join` (a new-cluster setup has no such agents) |
| HA secondary given only `--ca/--ca-key` | Accepted only for a CA from before M280 (no kind in its Subject OU): the secondary keeps the single-CA layout (CR-L7). An M280 CA there is refused, and the six-flag path requires each CA's own kind (SA2-L3) |
| `--control-plane-node-cert` given but invalid (foreign CA, expired, no ClientAuth, invalid or reserved CN) | `cca server` exits 1 at startup with the path and the reason, before it creates the store (user decision 1, 2026-11-25; rework 4). Without the flag, a three-CA server (certificate CN `ccattler-server`) warns and records nothing; a cluster from before M280 records the server certificate's CN, and a failed record there only warns (CR4-L3). With the flag, a record write that fails or is not confirmed also exits, naming the path (SA3-L1) |
| A CA file of the wrong kind given to `cca server` (`--ca`, `--etcd-ca`, `--gate-ca-cert`), alone or in a bundle | `cca server` exits 1 naming the flag, the file and both kinds; every certificate in the file is checked (SA4-L2); a CA from before M280 (no OU) is accepted (SA3-L2) |
| Enrollment without an etcd CA on the server | No etcd certificate in the response; `cca join` keeps today's behaviour |
| Re-issue loop | A certificate with ServerAuth only from the configured issuer is never re-issued (test) |

## Risks / Trade-offs

- **[Ansible-built clusters]** The templates change with this release, and the deploy testbed builds fresh clusters,
  so the full run tests the new layout of the controlplane and worker roles (baremetal-deploy.yml). The HA roles
  (ha-controlplane, etcd-cluster: peer firewall, sync timer, etcd's `ExecStartPre=+`) run only through ha-deploy.yml
  (e2e-ha.yml): the template tests and systemd models prove them, and the next E2E HA run verifies them on a host.
- **[Users who pinned ca.pem for TLS gates]** Their clients must trust `gate-https-ca.pem` on new clusters. This goes in
  the changelog and the guide.
- **[Old clusters stay exposed to the etcd/API identity mix]** The gate hole closes with D3, and old clusters get the
  warning. Node certificates remain etcd root clients there, as before. That is accepted under user decision 1.

## Migration Plan

None for existing clusters (user decision). New clusters: `cca init` from this release.
