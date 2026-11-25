# Tasks

Milestone M280. Design: [design.md](design.md). Specs: [cluster-pki](specs/cluster-pki/spec.md),
[host-setup](specs/host-setup/spec.md).

## 1. Graph and tests first

- [x] 1.0 `design/graphs/PKI_GRAPH.md` from today's code (D7): inventory, trust matrix, lifecycle links and debugging
  one-liners. It shows the single CA trusted by etcd and the API, and gate certificates with ClientAuth. Verify
  every row against the code (file:line).
  - Done in the main session at bc9343a; rewritten to the after-M280 state in 2.6 with a "Before M280" paragraph.
- [x] 1.1 `TestGateCertificateIsRefusedAsAPIClient`: a gate certificate for host `ccattler-server` presented to the
  API's mTLS. Red today, recording the principal it yields.
  - Red on bc9343a (scratch worktree, test adapted to the old API: `NewCertificateAuthority(ttl)`, no `Usage`; the
    client presents its certificate through `GetClientCertificate`): `go test -count=1 -run
    TestGateCertificateIsRefusedAsAPIClient ./cmd/cca/` → FAIL "the API accepted a gate certificate as a client
    identity and yielded principal "node:ccattler-server"". Green on the working tree.
- [x] 1.2 `TestGateCertificateHasServerAuthOnly`, `TestRouteTLSReissuesGateCertificateWithClientAuth`: red.
  - Red on bc9343a (same worktree): "gate certificate ExtKeyUsage = [serverAuth clientAuth], want [ServerAuth]
    only"; "gate certificate with ClientAuth was not re-issued (0 changes)". Green on the working tree, with
    `TestRouteTLSReissuesGateCertificateFromAnotherIssuer` and `TestRouteTLSKeepsCurrentGateCertificate` (no loop).
- [x] 1.3 `TestInitWritesThreeCAs`, `TestSetupScriptUsesEtcdCA`, `TestEnrollmentIssuesEtcdClientCertificate`,
  `TestJoinWritesEtcdClientCertificate`, `TestAPIRefusesNonClusterCACertificates`,
  `TestServerWarnsOnSingleCACluster`: red.
  - Red on bc9343a: `TestInitWritesThreeCAs` "cca init did not write cluster-identity-ca.pem";
    `TestSetupScriptUsesEtcdCA` "setup.sh etcd drop-in lacks ETCD_TRUSTED_CA_FILE=…/etcd-access-ca.pem" (and every
    unit flag); `TestAPIRefusesNonClusterCACertificates` exits "open …/api-server-identity-certificate.pem: no such
    file"; `TestServerWarnsOnSingleCACluster` does not build ("undefined: singleCAClusterWarning").
  - `TestEnrollmentIssuesEtcdClientCertificate` and `TestJoinWritesEtcdClientCertificate` were red in the working tree
    before 2.4: `go vet` "SetEtcdClientCertificateAuthority undefined", "unknown field EtcdCertificatePEM".
  - Test fix (2026-11-25, lead-developer): `requestPrincipalWithClientCertificate` now always presents the client
    certificate (`GetClientCertificate`). Go's client otherwise sends none when the server's CA list does not name the
    issuer, so a 401 "no principal" passed for a refusal; the server-side check is now what is tested. Stricter, not weaker.
  - Outputs: /tmp/claude-1000/-home-bozhan-REPOS-ccattler/5003c5af-85ee-4f73-92c5-22f9f192ac3a/scratchpad/m280-evidence.txt.
- [x] 1.4 `TestGateCertificateIsRefusedByEtcd` and `TestEtcdRefusesClusterCACertificates` with a real etcd
  (`etcd_integration` tag, as the existing etcd tests run): red.
  - Written in `cmd/cca/cluster_pki_etcd_test.go` (they start a real etcd from `ETCD_BIN` or PATH with
    `--client-cert-auth`, old single-CA and new three-CA setups). First attempt had no etcd binary.
  - Red on bc9343a with etcd v3.5.36 (`ETCD_BIN=/tmp/claude-1000/-home-bozhan-REPOS-ccattler/5003c5af-85ee-4f73-92c5-22f9f192ac3a/scratchpad/etcd-dl/etcd-v3.5.36-linux-amd64/etcd`, SHA-256 checked by
    the main session): `TestSingleCAEtcdRefusesGateAndNodeCertificates` (old `cca init` files, etcd trusting `ca.pem`)
    → FAIL "etcd accepted the gate certificate for ccattler-server as a client (full store access)" and "etcd accepted
    the node.pem as a client" (/tmp/claude-1000/-home-bozhan-REPOS-ccattler/5003c5af-85ee-4f73-92c5-22f9f192ac3a/scratchpad/red-1.4.txt).
  - Green on the working tree after 1a: `TestEtcdRefusesClusterCACertificates` (node and server identities refused,
    both etcd client certificates accepted) and `TestGateCertificateIsRefusedByEtcd` (new and old setups) PASS
    (/tmp/claude-1000/-home-bozhan-REPOS-ccattler/5003c5af-85ee-4f73-92c5-22f9f192ac3a/scratchpad/etcd-integration-final.txt).

## 2. Implementation

- [x] 2.1 D2: `IssueCertificate` takes a required typed `Usage`; every caller is updated. PKI_GRAPH is updated.
  - `TestIssueCertificateRequiresUsage` (zero and unknown usage refused, each usage's exact ExtKeyUsage); AUTHN_GRAPH
    "Certificate issuance" and PKI_GRAPH §1.
- [x] 2.2 D3: RouteTLSController issues with `UsageServer` from the gate CA when configured, else the cluster CA, and
  re-issues ClientAuth or foreign-issuer certificates. 1.1, 1.2 and 1.4 (gate) are green. NETWORK_GRAPH and
  PKI_GRAPH are updated.
  - Green: `TestGateCertificateIsRefusedAsAPIClient`, `TestGateCertificateHasServerAuthOnly`,
    `TestRouteTLSReissuesGateCertificateWithClientAuth`, `…FromAnotherIssuer`, `TestRouteTLSKeepsCurrentGateCertificate`,
    `TestGateCertificateIsRefusedByEtcd` (real etcd). NETWORK_GRAPH "RouteTLSController" and PKI_GRAPH §1 updated.
- [x] 2.3 D1, D1a and setup.sh: cca init creates the three CAs and the six leaf certificates of the D1a table (five
  originally; `etcd-peer-certificate.pem` added by user decision 1a, 2026-11-25), each with its description line.
  Evidence: `TestInitWritesThreeCAs` (six leaf certificates, etcd serving and peer certificates with both usages),
  `TestSetupScriptUsesEtcdCA` and `TestInitFilesParseWithDescriptionLine` green; openssl reads every file
  (scratchpad openssl-init.txt, openssl-peer.txt); full `go test -count=1 ./...` exit 0 (full-test-2.txt). HA secondaries take all six existing-CA flags or none. setup.sh installs them, etcd
  trusts `etcd-access-ca.pem`, and each unit uses its own process's certificates. Server flags `--gate-ca-cert` and
  `--gate-ca-key`. 1.3 init and setup, and `TestInitFilesParseWithDescriptionLine`, are green.
  - Name alignment (2026-11-25, main session): the spec deltas, design D1/D4 and this list were updated to the D1a
    names, which the user decided during apply.
  - New spec requirements: "Descriptions never authorize" (`TestMTLSPrincipalIgnoresSubjectUnit`) and "etcd client
    identities are distinct but not yet separately authorized" (the residual-trust check in 2.6).
  - Green: `TestInitWritesThreeCAs`, `TestInitCertificatesHaveDescriptiveSubjects`,
    `TestInitFilesParseWithDescriptionLine` (renamed from …Comment to the spec name), `TestSetupScriptUsesEtcdCA`
    (also asserts `--etcd-access-ca-key`), `TestInitRefusesPartialExistingCAFlags` (new),
    `TestInitFlagsEveryFlagReachesItsField` (now all six CA flags). `openssl x509 -noout -subject -issuer -ext
    extendedKeyUsage` and `openssl pkey -noout` read every file of a real `cca init` (output in the evidence file).
  - 1a (user decision 2026-11-25): `etcd-serving-certificate.pem` is now ServerAuth+ClientAuth, and cca init writes
    `etcd-peer-certificate.pem` (CN `etcd-peer-<node>`, OU "etcd peer certificate", etcd access CA, both usages, own
    key, description line); setup.sh installs it and chowns it to etcd with the serving pair. `TestInitWritesThreeCAs`
    checks both (six leaves). openssl: both certificates show "TLS Web Server Authentication, TLS Web Client
    Authentication" (/tmp/claude-1000/-home-bozhan-REPOS-ccattler/5003c5af-85ee-4f73-92c5-22f9f192ac3a/scratchpad/openssl-peer.txt). The task text still says five leaves: left to the main session.
- [x] 2.4 D4: enrollment returns the etcd client certificate; cca join writes it, and the agent unit uses it. 1.3
  enrollment and join are green. AUTHN_GRAPH and PKI_GRAPH are updated.
  - Server: `--etcd-access-ca-key` (needs `--etcd-ca`; preflight 0600), set by setup.sh and the HA unit;
    `loadEtcdAccessCertificateAuthority` → `EnrollmentService.SetEtcdClientCertificateAuthority`. `EnrollNode` signs
    `EtcdCSRPEM` before the token is consumed (CN `etcd-client-<node>`, OU "node etcd client", ClientAuth only) and
    refuses an etcd CSR with the node identity's key. `POST /api/enroll` carries `etcd_csr_pem`,
    `etcd_certificate_pem`, `etcd_ca_cert_pem`.
  - cca join, what it reads and writes: it reads `--ca-cert`, default `/etc/ccattler/pki/cluster-identity-ca.pem`,
    else `ca.pem`, else the token's fingerprint; it sends two CSRs (two keys); with an etcd certificate in the answer
    it writes `node-identity-for-api.pem`, `cluster-identity-ca.pem`, `node-etcd-client-certificate.pem`,
    `etcd-access-ca.pem` (+ keys, description lines) and prints an agent command with the etcd flags; against an old
    server it writes `node.pem`, `node-key.pem`, `ca.pem` as before. It still writes `/etc/ccattler/endpoints`
    (`https://<server host>:2379`) and `server-address` as before. The agent and CLI defaults try the M280 names
    first, then the old ones.
  - Green: `TestEnrollmentIssuesEtcdClientCertificate`, `TestEnrollmentWithoutEtcdCAReturnsNoEtcdCertificate`,
    `TestEnrollmentRefusesEtcdCSRReusingNodeKey`, `TestEnrollReturnsEtcdClientCertificate` (API payload),
    `TestJoinWritesEtcdClientCertificate`, `TestJoinAgainstOldServerKeepsLegacyFiles`.
- [x] 2.5 D5 and D6: the API trusts the cluster CA only, and the single-CA warning. 1.3 API and warning, and 1.4
  etcd, are green.
  - Green: `TestAPIRefusesNonClusterCACertificates`, `TestServerWarnsOnSingleCACluster`,
    `TestMTLSPrincipalIgnoresSubjectUnit`, and 1.4 `TestEtcdRefusesClusterCACertificates` against etcd v3.5.36.
- [x] 2.6 Ansible templates (the worker agent's etcd cert and CA; HA etcd trust); the guide (new files, TLS gate
  clients trust `gate-https-ca.pem`; the HA role passes all six existing-CA flags to `cca init`);
  `design/security-model.md`, `design/gate-tls-termination.md` and the threat model;
  Flow "Cluster PKI" in runtime-invariant-flows.md linking PKI_GRAPH.
  Residual trust (user decision 2026-11-25; design.md D1a) is stated in the security model, the threat model,
  PKI_GRAPH and the release notes:
  - until M181 (Store Access Isolation, etcd roles), every accepted etcd client certificate, the server's or any node's, has full etcd access;
  - a compromised node, gate nodes included, can still use its own node etcd client certificate;
  - nowhere may the new CommonNames be called separately authorized etcd identities.

  Verify with `grep -n "etcd" design/security-model.md design/threat-model.md` that the statement is there.
  - Done: worker agent unit and join guard; controlplane guard; HA role (fetches and distributes the three CAs, six
    `cca init` flags, server unit with the D1a files, `--gate-ca-*`, `--etcd-access-ca-key`); playbook, e2e and
    caddy-sync paths; guide, backup, disaster-recovery, troubleshooting; security model, threat model (T1 + file
    table), gate-tls-termination note; Flow 36 "Cluster PKI"; AUTHN_GRAPH, NETWORK_GRAPH, PKI_GRAPH (after state).
    `grep -c M181`: security-model 3, threat-model 4, PKI_GRAPH 3.
  - HA etcd (decided 1a, 2026-11-25): `etcd-cluster.conf.j2` uses `--cert-file etcd-serving-certificate.pem`,
    `--peer-cert-file etcd-peer-certificate.pem` (keys likewise) and `etcd-access-ca.pem` for both trusted-CA flags.
    `TestAnsibleEtcdClusterTemplateUsesEtcdAccessCA` was red first (six flag mismatches, /tmp/claude-1000/-home-bozhan-REPOS-ccattler/5003c5af-85ee-4f73-92c5-22f9f192ac3a/scratchpad/red-ansible-etcd.txt),
    green now. PKI_GRAPH (peer row, HA peer trust row, residual note), Flow 36, guide table and security model updated.
    The release notes are written at release (Workflow follow-up).

## 3. Checks

- [x] 3.1 gofmt, golangci-lint, `go test -count=1 ./...`, the `etcd_integration` tests, and -race on security,
  controllers and cmd/cca.
  - 2026-11-25 working tree: gofmt clean; `go vet ./...` and with `-tags etcd_integration` clean; golangci-lint 0
    issues (also with `--build-tags etcd_integration` on cmd/cca); `go test -count=1 ./...` exit 0; `go test -race
    -count=1 ./security/ ./controllers/ ./cmd/cca/` ok. Open: the `etcd_integration` tests (no etcd here, see 1.4).
  - Re-run after 1a (2026-11-25): gofmt clean; `go vet ./...` and `-tags etcd_integration` clean; golangci-lint 0
    issues (also `--build-tags etcd_integration` on cmd/cca and store); `go test -count=1 ./...` exit 0; `-race` on
    security, controllers, cmd/cca exit 0; every `etcd_integration` test (store's 20 against a plain etcd on
    127.0.0.1:2379, the two PKI tests with TLS etcd) PASS with etcd v3.5.36 (/tmp/claude-1000/-home-bozhan-REPOS-ccattler/5003c5af-85ee-4f73-92c5-22f9f192ac3a/scratchpad/etcd-integration-final.txt).
- [ ] 3.2 Deploy check `deploy-run:gate-cert-not-client`: on the deploy cluster, a gate certificate and key presented
  to etcd and to the API fail the TLS handshake. Then `install-host:three-cas` on a fresh VM with the guide.
- [ ] 3.3 Independent review: `security-audit` and `code-reviewer`, with a patterns review, at full rigor: every real finding is fixed, no round cap (user decision 2026-11-25, "do 2": security core code). Earlier draft said: one round; only HIGH and
  MEDIUM findings are fixed, lows are listed (the user's cap for install work applies here too unless the user says
  otherwise).
- [ ] 3.4 One full Deploy Testbed run (job summary `full run`); record the run ID.

### 3b. Review round 1 (2026-11-25, on bc9343a..81a186d)

Both verdicts request changes. The evidence is in scratchpad m280-security-audit.txt and m280-code-review.txt (Codex
job review-mwvziltf-64n9nl). Every finding was verified in the code by the main session (command_server.go:519-532,
enrollment.go:276-287, bootstrapAdminGrants :1001).

**Root cause of the two HIGH findings and two MEDIUM ones.** Before M280, `node:<init host>` was both the server
identity and the agent identity. Three behaviours hung on it, and all three broke when the identities were split:
- the cluster-admin grant, which host scripts relied on by using node.pem as an admin client;
- the control-plane enrollment record;
- the protection of that node ID from enrollment by an unbound token.

| ID | Finding | Disposition |
|---|---|---|
| SA-H1 / CR-M2 | The init host (and HA secondaries) are no longer recorded as enrolled (the CN of the server certificate is now `ccattler-server`). The real agent is refused secrets, and an unbound token can enroll as `ctrl` | Fix: the server records its node from `node-identity-for-api.pem` (new flag set by setup.sh and the HA unit); old clusters keep today's path; tests for both |
| CR-H1 | `node:ctrl` has lost cluster-admin; the e2e tests, caddy-sync, Ansible status checks, health_check and the HA tests use node.pem as an admin client → 403 | Fix (user decision 2026-11-25, "the automation replays the steps a real human admin does"; see below). No certificate is an admin credential |
| SA-L5 | (moved) | Milestone M282, to confirm first |

**CR-H1 decision (user, 2026-11-25).** The automation replays the guide's admin steps; no certificate is used as an
admin credential:
1. **Bootstrap** on the control-plane host with `sudo cca …`. It uses the root-only `/etc/ccattler/local-token`
   (`user:local-admin`), which `cca server` writes at start (security/authn_local_token.go). The CLI sends no client
   certificate.
2. **An `e2e-admin` user** created with `cca user create` and a direct grant `--capability cluster.admin --scope
   cluster`. All later checks run as it through `CCA_TOKEN`; raw API checks (`/api/state`, the debug proxy) send the
   same bearer token.
3. **caddy-sync** gets a least-privilege service user, granted only the capabilities its reads need, with a fresh
   token from each deploy run in a root-only file its unit reads. Its renewal is not built: M281 removes its API
   access altogether (Caddy forwards to the gates).
4. **The guide** stops saying that root uses the node's certificate; it names the local token.

Scope: every caller listed in CR-H1 (cluster_test.sh, caddy-sync.sh and its unit, the Ansible playbooks' status
checks, health_check.sh, baremetal_ha_test.sh, vm-ha/ha_test.sh). A test or check proves that `sudo cca status` works
on a new three-CA cluster.
| SA-M1 | Old clusters: gate certificates issued before the upgrade keep ClientAuth until they expire (≤ 90 days); the docs say "fixed" | Fix: the docs state the window and the remedy (re-initialize), as the user decided (no migration) |
| SA-M2 / CR-M3 | Node ID `cca-server` gets the server's etcd CN `etcd-client-cca-server` | Fix: reserve it in enrollment and cca init; test |
| CR-M4 | Test gaps found by mutation (gate CA wiring, `SetEtcdClientCertificateAuthority`, empty etcd CSR, key modes 0600, peer certificate install, etcd default files) | Fix: one test per mutation, each shown to fail on the mutation |
| SA-L1 | A worker's etcd client certificate is accepted on the HA peer port 2380 (same CA as kubeadm; narrower than before M280) | Fix: Ansible limits 2380 to the etcd members |
| SA-L2 / CR-L5 | The single-CA warning is silent for: gate CA = cluster CA, an `--etcd-ca` bundle, no `--etcd-ca`, auto `--tls` | Fix: all four cases; test |
| SA-L3 | `cca init` accepts reserved node IDs | Fix: validate; test |
| SA-L4 | Stale "until M280 is fixed" text (command_init.go:248) | Fix |
| SA-L5 | Pre-existing, old clusters only, PLAUSIBLE: the debug proxy verifies agent server certificates with ExtKeyUsageAny (api/node_debug_proxy.go:184) | Not M280's change: recorded as a follow-up bug for the user's queue |
| CR-L6 | An old `cca join` binary against a new server gets no etcd certificate and no error | Fix: the server refuses an enrollment without the etcd CSR when it has the etcd access CA key, with an error naming the version |
| CR-L7 | Old single-CA clusters cannot add an HA secondary (all six flags required) | Fix: accept `--ca/--ca-key` alone when the cluster is single-CA, documented; test |
| CR patterns | CA description strings duplicated (command_init.go:166, join_credentials.go:459); `/etc/ccattler/pki` literal in 3 places; raw `etcd-`/`etcd-peer-` CN prefixes | Fix: one constant each |
| CI | No workflow runs `-tags etcd_integration` | Belongs to M270 (real-etcd test tier); the real-etcd tests ran here (2i) |

### 3c. Rework round 1 (2026-11-25, lead-developer; red → green per finding, outputs in /tmp/claude-1000/-home-bozhan-REPOS-ccattler/5003c5af-85ee-4f73-92c5-22f9f192ac3a/scratchpad/rw-*.txt)

| ID | Red (before the fix) | Fix and green |
|---|---|---|
| SA-H1 / CR-M2 | `TestServerRecordsControlPlaneNodeEnrollment` (server started with setup.sh's flags, init host and an HA secondary): "control-plane node "ctrl" is not recorded as enrolled", "an unbound join token enrolled as control-plane node "ctrl"" (same for ctrl-2) | Server flag `--control-plane-node-cert` (setup.sh and the HA unit pass `node-identity-for-api.pem`); without it the server certificate's CN as before (`TestServerRecordsNodeFromServerCertificateOnOldClusters`). Green: both enrolled, unbound tokens refused, the agent gets its gate keys (`DeliverGateKeys`) |
| CR-H1 | — | **Stopped, needs a decision** (see the report): `cca user token` JWTs are signed by a per-process key (`NewWorkloadTokenIssuer`, command_server.go), so an `e2e-admin` or caddy-sync token dies at every cca-server restart and is not accepted by other HA replicas. Done: guide wording (local token, no certificate is an admin credential); `TestLocalAdminCLIReachesThreeCAServer` proves the `sudo cca status` path on a three-CA server (CA from `cluster-identity-ca.pem`, no client certificate, local token → 200; node identity alone → 403). Scripts unchanged |
| SA-M1 | docs said "Fixed" | Window (≤ 90 days, gateCertificateTTL, nothing revokes) and remedy (re-initialize) in the security model, threat model, gate-tls-termination, PKI_GRAPH §3, Flow 36 and the guide |
| SA-M2 / CR-M3 | `TestEnrollmentRefusesReservedNodeIDs`: "node ID "cca-server" enrolled; it is reserved" | `security.CCAServerEtcdClientName` reserved; `ValidateNodeIdentity` used by enrollment and cca init; the server's etcd CN is built from it. Green |
| SA-L3 | `TestInitRefusesReservedOrInvalidNodeIDs`: cca init accepted ccattler-server, cca-server, Bad_Node, -ctrl | `parseInitCommandArgs` validates `--node-id` (after the hostname default). Green |
| CR-M4 | Mutations M12, M13, M14, M15, M16, M18 each applied and restored; each fails its test (`rw-mutations.txt`) | New tests: `TestServerWiresGateCAIntoRouteTLS` (M12, via new `buildRouteTLSController`), `TestServerEnrollmentIssuesEtcdClientCertificates` (M13, `buildEnrollmentService`), `TestJoinEnrollmentRequestCarriesEtcdCSR` (M14, `buildJoinEnrollmentRequest`), `TestSetupScriptInstallsEveryInitFileWithItsMode` (M15, M16), `TestCLIEtcdDefaultsPreferM280Files` (M18). Green |
| SA-L1 | `TestAnsibleEtcdPeerPortLimitedToMembers`: no peer firewall template | etcd-cluster role: nftables table `inet ccattler_etcd_peers` drops 2380/tcp from non-members, loaded by `ccattler-etcd-peer-firewall.service` before etcd and at boot. Template rendered with jinja2 (3 members); `ansible-playbook --syntax-check ha-deploy.yml` passes. Green. Not run on hosts: deploy run |
| SA-L2 / CR-L5 | `TestServerWarnsOnEverySingleCACase`: no warning for auto --tls, gate CA = cluster CA, etcd CA bundle with the cluster CA, no --etcd-ca | `singleCAClusterWarning` compares every certificate of the files (bundles) and warns when a CA is missing or shared, and for auto --tls. Green |
| SA-L4 | `TestEtcdClientNetworkWarningStatesResidualTrust`: warning said "until M280 is fixed" | Text names M181 residual trust. `TestInitWarnsOnWideEtcdClientCIDR` now looks for "M181" instead of "M280" (same assertion, the reviewed text) |
| CR-L6 | `TestEnrollmentRefusesMissingEtcdCSR` (build: undefined `ErrEtcdClientCSRRequired`); `TestEnrollWithoutEtcdCSRNamesServerVersion`: "HTTP 200, error """ | Enrollment refuses with `ErrEtcdClientCSRRequired` before the token is consumed; `/api/enroll` adds "install cca <server version> … and run cca join again". Green |
| CR-L7 | `TestInitSingleCASecondaryWritesOldLayout`: "--ca … go together" | `--ca/--ca-key` alone → `singleCAClusterPKILayout` (`ca.pem`, `node.pem` and `etcd.pem` server+client, setup.sh on those files, no gate or etcd access CA flags). Documented in the guide and PKI_GRAPH. `TestInitRefusesPartialExistingCAFlags` dropped its `--ca/--ca-key` case (now allowed by this decision) and gained two partial cases. Green |
| Patterns | — | CA descriptions are constants (cca init and cca join); `/etc/ccattler/pki` only in `clusterPKIDirectory` (join default, server preflight, setup.sh `PKI_DIR`); `etcdServingCommonNamePrefix`, `etcdPeerCommonNamePrefix`; one PEM parser `pemCertificateDERs` for the warning and `--control-plane-node-cert` |

Checks after the rework: gofmt clean; `go vet ./...` and with `-tags etcd_integration` clean; golangci-lint 0 issues
(also `--build-tags etcd_integration` on cmd/cca and store); `go test -count=1 ./...` exit 0; `-race` on security,
controllers and cmd/cca exit 0; `etcd_integration` (ETCD_BIN v3.5.36, store tests against a plain etcd on
127.0.0.1:2379) exit 0, 23 PASS (`rw-etcd-integration.txt`).

**Stop-time check (Codex, 2026-11-25): "the etcd firewall reload is not idempotent". Rejected after a run.**
- **What the file does:** etcd-peer-firewall.nft.j2 uses the nftables atomic-replace idiom (`table …` / `delete table
  …` / `table … {}`), applied by `nft -f` in one transaction.
- **The run:** the rendered file (three members) loaded three times in a row with nftables 1.1 (`unshare -rn`, a
  throwaway namespace), then `nft list ruleset`.
- **The result:** exactly one table with one 2380 rule (scratchpad `peer-fw-idempotency.txt`).

## Workflow follow-up

- Release with `scripts/release.sh M280 <version>`. The changelog says:
  - new clusters have three CAs and self-explanatory file names;
  - TLS-gate clients trust `gate-https-ca.pem`;
  - old clusters warn and need re-initializing to separate the CAs;
  - until M181, every accepted etcd client certificate has full etcd access. Then archive (sync cluster-pki
  and host-setup).
