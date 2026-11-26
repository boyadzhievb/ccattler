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
  - **The deploy part is DONE** in run 45819982283 (a5fe10a), 14:35:12: "T19 PASS (deploy-run:gate-cert-not-client):
    etcd and the API refuse tls-auto's gate certificate and a ClientAuth certificate from the gate CA at the
    handshake; the same gate pair loads against the gate listener; the node etcd client certificate is accepted".
    "Gate TLS checks PASSED (T1–T19)"; 0 "T19 SKIP" lines.
  - **Open:** `install-host:three-cas` on a fresh VM.
- [x] 3.3 Independent review: `security-audit` and `code-reviewer`, with a patterns review.
  - **What the user decided:** "do 2" (2026-11-25), full rigor for security core code.
  - **"No round cap" was the main session's reading,** not the user's words. The user corrected it on 2026-11-26:
    stop after each round, at most 2 rounds per milestone (3r).
  - **Rounds:**
    - round 1 found HIGH findings (3b), fixed in reworks 1 and 2;
    - rounds 2 to 8 approved, with LOW findings only (3e, 3h, 3j, 3l, 3n, 3p, 3r);
    - the peer-firewall LOW loop was ended by the structural cut in 3r;
    - the small review of the removal approved (above), and its doc fixes landed in f1c3755.
  - **Evidence:** every verdict and finding table is in this file; the reports are in the scratchpad
    m280-*-r*.txt files.
- [x] 3.4 One full Deploy Testbed run (job summary `full run`); record the run ID.
  **Run 45819982283** (workflow_dispatch on a5fe10a, 2026-11-26): completed success.
  - **Run summary:** RUN_KIND full, DEPLOY_OUTCOME success, REPLAY_OUTCOME success.
  - **whoami churn:** 4 distinct IDs, within the bound of 6.
  - **The replay:** includes the user-token revocation checks (deleted-user-token-refused,
    recreated-user-old-token-refused).
  - **Both "SKIP (deferred to M281)" lines are present.** Log saved as scratchpad run-45819982283.log.

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
| CR-H1 | — | **Stopped then; decided 2026-11-25 (option c, see "CR-H1 revised" below)** (see the report): `cca user token` JWTs are signed by a per-process key (`NewWorkloadTokenIssuer`, command_server.go), so an `e2e-admin` or caddy-sync token dies at every cca-server restart and is not accepted by other HA replicas. Done: guide wording (local token, no certificate is an admin credential); `TestLocalAdminCLIReachesThreeCAServer` proves the `sudo cca status` path on a three-CA server (CA from `cluster-identity-ca.pem`, no client certificate, local token → 200; node identity alone → 403). Scripts unchanged |
| SA-M1 | docs said "Fixed" | Window (≤ 90 days, gateCertificateTTL, nothing revokes) and remedy (re-initialize) in the security model, threat model, gate-tls-termination, PKI_GRAPH §3, Flow 36 and the guide |
| SA-M2 / CR-M3 | `TestEnrollmentRefusesReservedNodeIDs`: "node ID "cca-server" enrolled; it is reserved" | `security.CCAServerEtcdClientName` reserved; `ValidateNodeIdentity` used by enrollment and cca init; the server's etcd CN is built from it. Green |
| SA-L3 | `TestInitRefusesReservedOrInvalidNodeIDs`: cca init accepted ccattler-server, cca-server, Bad_Node, -ctrl | `parseInitCommandArgs` validates `--node-id` (after the hostname default). Green |
| CR-M4 | Mutations M12, M13, M14, M15, M16, M18 each applied and restored; each fails its test (`rw-mutations.txt`) | New tests: `TestServerWiresGateCAIntoRouteTLS` (M12, via new `buildRouteTLSController`), `TestServerEnrollmentIssuesEtcdClientCertificates` (M13, `buildEnrollmentService`), `TestJoinEnrollmentRequestCarriesEtcdCSR` (M14, `buildJoinEnrollmentRequest`), `TestSetupScriptInstallsEveryInitFileWithItsMode` (M15, M16), `TestCLIEtcdDefaultsPreferM280Files` (M18). Green |
| SA-L1 | `TestAnsibleEtcdPeerPortLimitedToMembers`: no peer firewall template | etcd-cluster role: nftables table `inet ccattler_etcd_peers` drops 2380/tcp from non-members, loaded by `ccattler-etcd-peer-firewall.service` before etcd and at boot. Template rendered with jinja2 (3 members); `ansible-playbook --syntax-check ha-deploy.yml` passes. Green. Not run on hosts by the deploy run (corrected in rework 8, CR7-F1: baremetal-deploy.yml does not use the etcd-cluster role); proven by the template tests and systemd models, verified on a host at the next E2E HA run (ha-deploy.yml through e2e-ha.yml) |
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

**CR-H1 revised (user decision 2026-11-25, option c).** Rework round 1 found that user JWTs are signed by a key each
`cca server` generates at start (command_server.go:658-663). Such tokens die at every restart and work only on the
replica that minted them; that is now milestone M283. Therefore:
- **M280's automation uses `sudo cca` with the local admin token on the control-plane host.**
  - It authenticates through `LocalTokenAuthenticator`, not the JWT signing key, so it is independent of M283.
  - It replaces every caller that used node.pem as an admin client.
  - It must keep working through the certificate change and prove that the gate-certificate hole is closed.
- **Deferred to M283, and not claimed here:** the `e2e-admin`/`CCA_TOKEN` checks (JWT validity across restarts, HA
  replicas and key rotation). M280 does not replace the user-token test with the local-token test, and it claims no
  JWT continuity.
- **The guide:** step 3's user token must not be presented as surviving server restarts until M283 ships. Add a note
  there.
- **caddy-sync (user decision 2026-11-25, option i): whoever restarts the server refreshes its token.** This is
  temporary testbed infrastructure, removed in M281.
  - **Credentials:** the local admin token is used only on the control-plane host, to mint a least-privilege
    `caddy-sync` user JWT (`sudo cca user token caddy-sync`). Only that JWT goes to the testbed host. The
    cluster-admin credential never leaves the control plane, and caddy-sync never gets cluster-admin.
  - **Order** at deploy and at every step that restarts `cca-server`:
    1. restart and wait until the API is healthy;
    2. mint a fresh token;
    3. copy it to the testbed host and replace the token file atomically, root-only;
    4. only then (re)start caddy-sync.
  - **The token is never printed,** and never put in command-line arguments or logs.
  - **Pinning:** caddy-sync talks only to the API replica that minted its token, never through a load balancer
    (cross-replica rejection is the M283 bug).
- **After M283** removes the token-lifetime and replica problem, M281 removes caddy-sync altogether.

### 3d. Rework round 2 (CR-H1) (2026-11-25, lead-developer; outputs in scratchpad rw2-*.txt)

**Decision (user's reviewer, 2026-11-25):** option (i) for caddy-sync is rejected (it replaces the caddy-sync
block above). caddy-sync is disabled for M280's closing run: cluster_test.sh no longer installs or starts caddy-sync
or Caddy on the testbed host (scripts/caddy-sync.sh and deploy/caddy-sync.service stay, M281 removes them). The
external route path and the Caddyfile assertion are deferred to M281; the run logs `SKIP (deferred to M281)` for
both and does not count them as passed. External testbed URLs are unavailable until M281. M280's gate checks are the
direct-node ones in gate_tls_checks.sh (curl on the control node to each agent node's :443 with --resolve), which
never depended on caddy-sync.

- [x] **Shared helper** test/e2e/local_admin_access.sh: `local_admin_cca_remote_command` (`sudo env CCA_API=<that
  host's replica> cca …`) and `local_admin_curl_remote_command` (raw API reads only where no cca command returns the
  data: full /api/status JSON, /api/state, the debug proxy). The curl reads `/etc/ccattler/local-token` inside the
  remote `sh` and passes `Authorization: Bearer` on stdin (`-H @-`); CA from the CLI's candidates, so old `ca.pem`
  clusters work. Red→green: `TestLocalAdminCurlSendsTokenOnStdinOnly` (base: helper missing, FAIL; now PASS: token
  absent from sudo's and curl's argv and output, header on stdin, old ca.pem picked).
- [x] **cluster_test.sh** (+ gate_tls_checks.sh, wordpress_checks.sh): `CURL_TLS` and the copied node certificate
  removed; status wait = `sudo cca status` on `CCA_API`; scale = `sudo cca scale`; status JSON, /api/state, debug proxy
  and the M173 prefix checks via `control_plane_api_curl`. The e2e-reader user-token check is unchanged (one server
  lifetime; no JWT continuity claimed). The CA fetch falls back to `ca.pem`.
- [x] **Ansible**: baremetal-deploy, ha-deploy, demo-deploy status → `cca get instances -o json` (become, `CCA_API`
  = that host); /healthz waits (unauthenticated) without a client certificate. Also found: roles/worker "Wait for
  agent to register" (delegated to controlplane[0]) used the ctrl node certificate → `cca get nodes -o json`.
  `ansible-playbook --syntax-check` passes for the three playbooks.
- [x] **health_check.sh, baremetal_ha_test.sh, vm-ha/ha_test.sh**: `control_plane_api_curl`/`api_curl` through the
  helper, run on the host whose replica the URL names (local tokens are per replica).
- [x] **Invariant test** `TestAutomationNeverUsesNodeIdentityAsAdminClient` (test/e2e, deploy/ansible): base FAIL,
  12 uses (baremetal-deploy :69 :87, demo-deploy :107, ha-deploy :72 :105 :123, worker tasks :96, baremetal_ha_test
  :91, cluster_test :633 :887, health_check :141, vm-ha/ha_test :97); now PASS (`rw2-red.txt`).
- [x] **Guide** (website/installation.md step 3): note that a user token does not survive a `cca server` restart and
  works only on the replica that created it until M283. No `CCA_TOKEN` checks added (M283).
- [x] **Checks:** shellcheck: local_admin_access.sh clean; the other changed scripts have no warning on a changed line
  and fewer or equal warnings than the base (cluster_test 91→59, gate_tls 6→1, wordpress 1→0). gofmt clean; `go vet
  ./...` with and without `-tags etcd_integration` clean; golangci-lint 0 issues (also `--build-tags
  etcd_integration` on cmd/cca and store); `go test -count=1 ./...` exit 0; `-race` on security, controllers, cmd/cca
  exit 0.
- [x] (run 45819982283 deployed, checked and replayed through the helper: deploy and replay success; both SKIP lines
  present; the HA tests' reads are verified when E2E HA next runs, per 3p CR7-F1) **Only the deploy run proves:**
  `sudo cca status`/`scale`/`get` and the helper curl over ssh on the real hosts
  (vmherd quoting, curl `-H @-` on the images), the Ansible status tasks and the worker registration wait, the HA tests'
  per-replica reads, and the two `SKIP (deferred to M281)` lines in the job log.

### 3e. Review round 2 (2026-11-25, on 81a186d..bb6c741)

Both verdicts approve with LOW findings only: security-audit (m280-security-audit-r2.txt) and code-reviewer
(m280-code-review-r2.txt, Codex job review-mww8bsg7-u0n3b4). Every round-1 finding is confirmed closed: the takeover
was reproduced and is now refused, and the round-1 mutations were re-applied and caught. Under the user's full-rigor
rule, every real finding is fixed.

| ID | Finding | Disposition |
|---|---|---|
| SA2-L1 / CR2-L1 | `--control-plane-node-cert` trusts any certificate (foreign CA, expired, reserved CN); without the flag, a three-CA server records `ccattler-server`; no test covers the HA unit's flag | Fix: verify the chain against `--ca`, check validity, `ValidateNodeIdentity`; never record `ServerCertificateCommonName` (skip and warn); template test for the HA unit |
| SA2-L2 | The docs say "no certificate is an admin credential", but the API server's identity certificate is cluster-admin by design; the guide omits the residual etcd trust | Fix the wording: no automation uses a certificate as an admin credential; the server's own certificate is root-only on control-plane hosts. Add the M181 note to the guide. The invariant test also covers the server certificate |
| CR2-L2 | The invariant test matches only M280 file names; old `node.pem` used as an admin client passes | Fix: it also matches the old names |
| SA2-L3 | The single-CA layout (and the six-flag path) accept a CA of the wrong kind, e.g. `etcd-access-ca.pem` as `--ca` | Fix: check each CA's kind by its Subject OU; refuse an M280-kind CA on the single-CA path |
| SA2-L4 | etcd CommonName prefixes collide (node `client-cca-server` → serving CN `etcd-client-cca-server`) | Fix: non-overlapping prefixes `etcd-server-<node>`, `etcd-peer-<node>`, `etcd-client-node-<node>`, `etcd-client-cca-server`. Changes the D1a CN column; recorded in design.md as a change during apply (main session, 2026-11-25) |
| SA2-L5 | etcd starts even if the peer-firewall unit fails at boot | Fix: an etcd.service drop-in with `Requires=` and `After=` on the firewall unit. `--peer-cert-allowed-hostname` rejected: it takes one hostname, so it cannot express several peers |
| CR2-L3 | A caddy-sync installed by earlier runs keeps polling on the testbed host | Fix: Step 4.5 stops and disables an existing caddy-sync (logged). Caddy itself is left alone |
| CR2-L4 | Old Ansible-built clusters: the playbooks render M280 file names, so re-running them on a pre-M280 cluster breaks its units; `--clear-workloads` against an old cluster uses the M280 etcd files | Fix: roles and cluster_test.sh detect the layout (`cluster-identity-ca.pem` present or not) and use the matching names; test for both layouts |
| CR2 suggestions | The CA candidate list duplicated in shell and Go; the guide note swallowed the next sentence; the job summary does not list the deferred SKIPs | Fix all three (a test ties the shell list to the Go list) |
| Open (3.2) | `deploy-run:gate-cert-not-client` has no script, so the full run would not execute it | Fix: a deploy check presents a real gate certificate and key to etcd (2379) and to the API (9770) and asserts that both handshakes are refused, in the default check set |
| Codex | (high) vmherd quoting; (medium) the unauthenticated `/healthz` wait | Rejected by the reviewer with evidence: the parse simulation plus vmherd's single-command OpenSSH exec; `/healthz` is public by design (api/server.go:306) and is followed by an authenticated task |

### 3f. Rework round 3 (2026-11-25, lead-developer, on bb6c741; outputs in scratchpad rw3-*.txt)

Red = the new or updated tests copied onto bb6c741 (`rw3-red-cmd.txt`, `rw3-red-sec-api.txt`); where a test passes
there by design, a mutation on the fixed tree shows it bites (`rw3-mutations.txt`, script `rw3-mutate.sh`).

| ID | Red | Fix and green |
|---|---|---|
| SA2-L1 / CR2-L1 | `TestControlPlaneNodeCertificateMustBelongToTheCluster`: foreign-CA `victim`, expired `expiredx`, reserved `cca-server`, the server certificate (`ccattler-server`) all "recorded = true"; `TestThreeCAServerWithoutNodeCertFlagRecordsNoServerName`: "recorded ccattler-server"; `TestHAControlPlaneUnitRecordsItsNode` (renders the HA unit): mutation M26 (flag removed) caught | `verifyControlPlaneNodeCertificate`: chain to `--ca`, valid now, ClientAuth, `ValidateNodeIdentity`; on failure, and when the fallback CN is `ccattler-server`, nothing is recorded and the server warns (skip and warn, as decided; it keeps serving). `EnsureControlPlaneNodeEnrolled` uses `ValidateNodeIdentity`. Mutations M36 (no chain check) and M42 (every reserved-name check removed) caught; M35/M37 (one of the two reserved-name layers removed) survive because the other layer still refuses, by design. Green |
| SA2-L2 | wording | Guide step 3, PKI_GRAPH and the script comments: no automation uses a certificate as an admin credential; the API server's own certificate is cluster-admin by design and root-only on control-plane hosts. Guide: until M181 every etcd client certificate has full store access |
| CR2-L2 | `TestAutomationNeverUsesCertificateAsAdminClient` passes on bb6c741 (no such use there); mutations M30 (`--cert …/node.pem` in health_check.sh) and M32 (`api-server-identity-certificate.pem`) caught | The invariant (renamed) also matches `api-server-identity-certificate.pem`, `node.pem`, their keys and the `cca_pki.node_*`/`server_*` variables; only the agent and server units, group_vars' table, `creates:` guards, comments and etcd-client layout lines may name them. Green |
| SA2-L3 | `TestInitRefusesEtcdAccessCAAsSingleCA` (binary): "exit 0, want the CA-kind refusal"; `TestCheckLoadedCAKind` does not build on bb6c741 | `CertificateAuthority.Kind` (Subject OU, ca.go constants); `checkLoadedCAKind`: the single-CA path refuses an M280 CA, the six-flag path requires each flag's own kind. `TestInitSingleCASecondaryWritesOldLayout` now uses a CA as cca init wrote it before M280 (O=CCattler, no OU); its assertions are unchanged. Mutation M34 (check unwired) caught. Green |
| SA2-L4 | `TestEtcdCommonNamesAreDisjoint`: five collisions (e.g. `etcd-client-cca-server` = serving CN of node `client-cca-server`); `TestInitWritesThreeCAs`, `TestEnrollmentIssuesEtcdClientCertificate`, `TestEnrollReturnsEtcdClientCertificate` with the new CNs fail | `etcd-server-<node>`, `etcd-peer-<node>`, `etcd-client-node-<node>`, `etcd-client-cca-server` (`etcdServingCommonNamePrefix`, `NodeEtcdClientCommonNamePrefix`, `ccaServerEtcdClientCommonName`). design.md D1a: CN column and a line recording the change during apply (main session, 2026-11-25, SA2-L4); spec scenario CNs, security-model, threat-model, Flow, PKI_GRAPH, AUTHN_GRAPH. Old clusters keep their certificates. `openspec validate separate-cluster-cas --strict`: valid. Green |
| SA2-L5 | `TestEtcdStartsOnlyAfterThePeerFirewall`: no `[Unit]` in the rendered drop-in | zz-cluster.conf has `Requires=` and `After=ccattler-etcd-peer-firewall.service`. The role now only starts that unit and reloads changed rules with `nft -f` (atomic table replace): with `Requires=`, restarting it would restart etcd on every member at once. Mutations M38 (restarted) and M39 (no Requires) caught. Green |
| CR2-L3 | — (shell, deploy run) | Step 4.5 stops and disables a caddy-sync.service an earlier run installed (`systemctl disable --now`, logged, fails the run if it cannot); Caddy is left alone |
| CR2-L4 | `TestAnsibleUnitsFollowTheClusterPKILayout`, `TestAnsibleEtcdClusterTemplateUsesEtcdAccessCA` (now rendered), `TestPKIFileRemoteExpressionFollowsTheLayout`: fail on bb6c741 (no `cca_pki`, no helper) | group_vars `cca_pki_layouts` (three_ca, single_ca) and `cca_pki`; `common/tasks/pki_layout.yml` sets `single_ca` when the first control-plane host has `ca.pem` but no `cluster-identity-ca.pem` (also included by rotate-root-key.yml). Templates, `creates:` guards, the HA secondary's `cca init` flags and etcdctl tasks use it. The test renders the server, agent and etcd units with jinja2 (trim_blocks, as Ansible) for both layouts: every file named exists in that layout, group_vars matches cca init's layouts field by field, three-CA-only flags appear only for three_ca; mutation M40 (a literal M280 name in the agent unit) caught. cluster_test.sh's etcdctl flags and gate_tls_checks.sh's gate CA use `pki_file_remote_expression` (evaluated on the host). `ansible-playbook --syntax-check`: baremetal, ha, demo, site, test-deploy, rotate-root-key ok. Green |
| CR2 suggestions | `TestLocalAdminCACandidatesMatchTheCLI` passes on bb6c741 (lists equal); mutation M33 (order swapped) caught | The test ties `LOCAL_ADMIN_CA_CANDIDATES` to `installedClusterCACandidatePaths`. The guide note ends at its own paragraph. The job summary has a "Deferred to M281 (SKIP)" row naming both skips |
| Open (3.2) | — (deploy run) | T19 `gate_tls_check_gate_cert_not_client` (gate-tls group, default set; TESTING.md): on the gate node, curl presents tls-auto's pair from the agent's root-only cache to etcd `:2379/health` and the API `:9770/healthz`. Both must refuse the handshake (HTTP 000, curl exit ≠ 0). Positive controls: the node etcd client certificate gets `200 0` from etcd, `/healthz` without a client certificate gets `200 0`. The key never leaves the node and only the two codes are returned. Remote command parsed with `sh -n` (stubbed ssh_vm) |

Checks: gofmt clean; `go vet ./...` with and without `-tags etcd_integration` clean; golangci-lint 0 issues (also
`--build-tags etcd_integration` on cmd/cca and store); `-race` on security, controllers, cmd/cca exit 0; etcd_integration
(ETCD_BIN v3.5.36, plain etcd on 127.0.0.1:2379 for store): 25 PASS, exit 0 (`rw3-etcd-integration.txt`); shellcheck:
local_admin_access.sh 0, gate_tls_checks.sh 1 (pre-existing SC2034), cluster_test.sh no warning on a changed line.
`go test -count=1 ./...` exit 1: one failure of `TestKeyringConcurrentBootstrapAgrees` (security, "keyring is
incomplete", `rw3-test.txt`). This diff does not touch the keyring. The test passed 30/30 alone on bb6c741 and on this
tree, and 3/3 in full package runs. **Classified as a test bug (flaky under full-suite load)**, recorded here; not
rerun until green; it needs its own fix.
**Reclassified (main session, 2026-11-25): likely a product race, now milestone M284** (user decision: "3
milestone").
- **The race:** `NewKeyringEnvelopeCodec` reads `keyring/active`, then separately scans `keyring/`
  (security/keyring.go:124-128).
- **How it fails:** if another replica commits its bootstrap transaction between the two reads, the scan finds a
  complete keyring and the code still reports "keyring is incomplete".
- **Not caused by M280,** which does not touch the keyring.

**User decisions on rework 3 (2026-11-25):**
- **(1) "exits":** a `--control-plane-node-cert` that is given but fails verification (foreign CA, expired,
  not ClientAuth, reserved or invalid CN) makes `cca server` exit with the reason. This is fail-closed: a skipped
  record would leave the node ID open to an unbound token. When the flag is absent, the server only warns, since a
  control plane without an agent has no node to record. Rework 3's skip-and-warn on a bad flag is replaced (3g).
- **(2) "fresh":** layout detection is confirmed as implemented: the old names only when `ca.pem` exists and
  `cluster-identity-ca.pem` does not; a fresh host gets the new layout.

### 3g. Rework (user decision 1: exit) (2026-11-25, lead-developer, on ea31a43; outputs in scratchpad rw3g-*.txt)

- [x] **Fix:** `bootstrapControlPlaneNodeEnrollment` returns an error when `--control-plane-node-cert` is given and
  `verifyControlPlaneNodeCertificate` fails (`--control-plane-node-cert <path> is not a valid node identity of this
  cluster: <reason>`); `cca server` prints it and exits 1 before serving. Without the flag nothing changes: the
  fallback (server certificate CN) records old clusters' node, and `ccattler-server` is never recorded (warning).
  PKI_GRAPH and SECRETS_GRAPH updated.
- [x] **Tests:** `TestControlPlaneNodeCertificateMustBelongToTheCluster` asserts, for each bad case (foreign CA, expired,
  reserved `cca-server`, invalid CN `Not_A_Label`, server-only certificate without ClientAuth, the API server
  certificate `ccattler-server`), an error naming the path and the reason, and no record; the valid node identity
  returns nil and is recorded. `TestThreeCAServerWithoutNodeCertFlagRecordsNoServerName` asserts no error, no record
  and the warning in the log. Other callers fail the test on an unexpected error.
- [x] **Red** on 7d863e3 (updated tests copied in): build fails, the function returns no value
  (`rw3g-red-base.txt`). With only the signature adapted (`return nil` on every path, skip-and-warn kept;
  `rw3g-red-shim.diff`), all six bad cases fail with "bootstrap error = <nil>; want a startup error naming …", the
  valid case and the no-flag test pass (`rw3g-red-shim.txt`). **Green** on this tree: all seven cases and the no-flag
  test pass.
- [x] **Checks:** gofmt clean; `go vet ./...` with and without `-tags etcd_integration` clean; golangci-lint 0 issues
  (also `--build-tags etcd_integration` on cmd/cca) (`rw3g-checks.txt`); `go test -count=1 ./cmd/cca/ ./security/`
  ok (`rw3g-test-pkg.txt`); `-race` on cmd/cca ok (`rw3g-race.txt`); `go test -count=1 ./...` exit 0, 20 packages
  ok, `TestKeyringConcurrentBootstrapAgrees` passed in this run (`rw3g-full-test.txt`).
- Not covered by a test: the `os.Exit(1)` on that error in `configureAndStartAPIServer` (command_server.go:711) (no binary-level `cca server`
  test); the deploy run starts the server with a valid flag only.

Only the deploy run proves: T19 against the real etcd and API; the caddy-sync stop on the testbed host; layout
detection on real hosts (fresh three-CA run; a pre-M280 cluster only through `--from-release`). Corrected in
rework 8 (CR7-F1): the etcd drop-in's `Requires=` at boot belongs to the etcd-cluster role, which the deploy run does
not use; the template tests prove it, and the next E2E HA run verifies it on a host.

**Stop-time check (Codex, 2026-11-25), CONFIRMED by the main session: an invalid node certificate is detected only
after startup side effects.** The 3g exit sits in `configureAndStartAPIServer` (command_server.go:711-714). By then
`executeServerCommand` has already:
- created the store and the keyring;
- started the controllers with leader election (its line 41, before the API setup);
- written the local token (`buildServerAuthenticatorAndBindings`);
- written the admin grants (`bootstrapAdminGrants`).

So a misconfigured server can act as leader briefly before it exits.
- **Fix (rework 4, batched with the round-3 findings):** load and verify `--control-plane-node-cert` in the preflight
  checks, before the store is created, and pass the verified identity to the enrollment record later. Add a test
  that a bad certificate leaves the store untouched.

### 3h. Review round 3 (2026-11-25, on bb6c741..af63794)

Both verdicts approve with LOW findings only: security-audit (m280-security-audit-r3.txt) and code-reviewer
(m280-code-review-r3.txt, Codex job review-mwwaacro-5fjzap).
- **Every round-2 finding is confirmed closed:**
  - real binary runs with etcd v3.5.36;
  - 0 CN collisions over 50,589 node IDs;
  - T19 run locally passes, and fails under a mutation that lets etcd trust the gate CA;
  - round-2 mutations re-applied are caught.
- **Under the full-rigor rule every real finding is fixed (rework 4).**

| ID | Finding | Disposition |
|---|---|---|
| Stop-time (above) | The node certificate is verified after startup side effects | Fix: verify in preflight, before the store is created |
| SA3-L1 | With the flag given, a failed enrollment-record write only warns and serves (first start), reopening the takeover | Fix: exit when the flag is given and the record cannot be written or confirmed |
| SA3-L2 | `cca server` does not check CA kinds (e.g. the etcd access CA as `--gate-ca-cert`) | Fix: the same `Kind()` check on `--ca`, `--etcd-ca` and `--gate-ca-cert` (a pre-M280 CA without an OU stays accepted) |
| SA3-L3 / CR3-L2 | T19 counts any `000 <non-zero>` as refused (a wrong key gives 58, a down endpoint gives 7) | Fix: accept only curl exit 35 or 56. Add controls: the same gate pair loads (presented to :443), and the API accepts a valid client certificate |
| SA3-L4 | `Requires=` proves only that the firewall unit ran; a flushed table is not restored before etcd restarts | Fix: `ExecStartPre=nft -f …` in the etcd drop-in |
| SA3 note / CR3-L3 | `pki_file_remote_expression` tests `-f` as the ssh user; a 0700 PKI dir from `cca join` picks the old names | Fix: `sudo test -f` |
| CR3-L1 | Ansible layout: only the three unit templates are tested; mutations M49, M50, M53, M54 and M55 survive | Fix: an invariant test for literal PKI names in deploy/ansible outside group_vars, plus renders of the layout condition and the HA init command for both layouts |
| CR3-L4 | design.md's failure table lacks "exit on an invalid `--control-plane-node-cert`" and no spec scenario covers it; the row "HA secondary given only `--ca/--ca-key` refuses" contradicts CR-L7/SA2-L3 | Fix: the table row and a spec scenario (with test); correct the row |
| CR3 style | `bootstrapControlPlaneNodeEnrollment` doc comment is 6 lines (max 4) | Fix |
| Codex | (high) the node certificate's OU is not checked | Rejected by the reviewer: in a three-CA cluster the cluster CA issues ClientAuth only to node identities and the reserved `ccattler-server`, which is refused; the OU never authorizes, by design (D1a) |
| Known | M45 (the `os.Exit` line) survives: no binary-level `cca server` test | Accepted; recorded in 3g |

### 3i. Rework round 4 (2026-11-25, lead-developer, on af63794; outputs in scratchpad rw4-*.txt)

Red is a mutation on the fixed tree that restores the old behaviour (script `rw4-mutate.sh`, `rw4-mutations.txt`),
or the reviewer's survivor now caught. Green is the same test on this tree.

| ID | Red | Fix and green |
|---|---|---|
| Stop-time | M63 (the identity check moved back after the store, as on af63794): `TestInvalidNodeCertificateLeavesEtcdUntouched` "etcd held 0 keys before and 13 after the refused start" (`rw4-red-order.txt`); M56 (error ignored): `TestServerExitsOnInvalidNodeCertificateBeforeStartup` | `executeServerCommand` → `checkServerIdentityFiles` (new server_startup_identity.go) runs before the store is created and passes the verified certificate to `bootstrapControlPlaneNodeEnrollment`; `loadControlPlaneNodeCertificate` removed. The etcd test runs the binary as setup.sh does against a real etcd (v3.5.36); the binary test also closes M45. Green |
| SA3-L1 | M57 (no read-back), M58 (record error only warns): `TestControlPlaneNodeRecordMustBeConfirmed` | With the flag, `recordControlPlaneNodeEnrollment` writes and reads back the record; a failure or a missing record exits. Without the flag (old cluster) it still only warns (same test). Green |
| SA3-L2 | M59 (kind check off), M60 (no-OU refused): `TestServerCAKindChecks` | `checkServerCAKinds`: `--ca`, `--etcd-ca`, `--gate-ca-cert` must not name another kind; no OU (before M280, external etcd CA) accepted; unreadable file refused. `security.CertificateAuthorityKindOf` (certificate only; `Kind()` delegates). Green |
| SA3-L3 / CR3-L2 | Live T19 (reviewer setup, `rw4live/t19.sh` with an `openssl s_server` gate listener on :443): bad key now fails at the :443 control ("000 58"); with that control removed, the predicate alone fails it ("etcd did not refuse … (000 58)"); eku-both fails (`rw4-t19-live.txt`) | `gate_tls_handshake_refused` accepts only `000 35`/`000 56`; control: the same gate pair against the gate listener must get a response. Normal mode: T19 PASS. **Not done: the API positive control with a valid client certificate** (see below) |
| SA3-L4 | M61 (ExecStartPre removed): `TestEtcdStartsOnlyAfterThePeerFirewall` | etcd drop-in `ExecStartPre=/usr/sbin/nft -f /etc/ccattler/etcd-peer-firewall.nft`; the test requires the line to equal the firewall unit's ExecStart. Green |
| CR3-L3 | M62 (probe without sudo): `TestPKIFileRemoteExpressionFollowsTheLayout` | `sudo -n test -f` (never prompts); the test runs a stub sudo that logs its call. Green |
| CR3-L1 | M49 → `TestAnsiblePKILayoutDetection`; M50 → `TestAnsibleHASecondaryInitFollowsTheLayout`; M53, M54, M55 → `TestAnsibleNamesPKIFilesOnlyThroughGroupVars` (all survived on af63794) | Invariant: no non-comment line outside group_vars names a PKI file of either layout. Renders (jinja2, from the task YAML): the layout condition with the stat results of its own probe list (4 host states), the HA secondary `cca init` flags for both layouts. Green |
| CR3-L4 | `openspec validate --strict` | design.md failure table: exit on an invalid `--control-plane-node-cert`, CA-kind exit, corrected HA-secondary row (recorded as a main-session change for CR3-L4). Spec: requirements "cca server records only a verified control-plane node" and "cca server checks the kind of each CA file", scenarios naming their tests. Valid |
| CR3 style | — | Doc comment 4 lines; `lockedLogBuffer` receiver renamed |

**Needs a decision (API positive control, SA3-L3):** the only cluster-CA client certificate on a gate node is the
node identity (`node-identity-for-api.pem`, `node.pem` on old clusters). Presenting it to `/healthz` fails
`TestAutomationNeverUsesCertificateAsAdminClient` (CR-H1/CR2-L2: no automation names it as an API client). The control
is left out until the user chooses: exempt this read-only `/healthz` probe from the invariant, or another certificate.

Checks: gofmt clean; `go vet ./...` with and without `-tags etcd_integration` clean; golangci-lint 0 issues (also
`--build-tags etcd_integration` on cmd/cca and store) (`rw4-checks.txt`); `go test -count=1 ./...` exit 0, 20
packages ok (`rw4-full-test.txt`); `-race` on security and cmd/cca ok, controllers FAIL once:
`TestDeterministicPlanVerification/rollout`, `progress_at`/`started_at` 16:04:03Z vs 16:04:04Z (wall clock crossed a
second between iterations; controllers untouched by this diff). **Classified as a test bug**, recorded, not rerun
(`rw4-race.txt`). etcd_integration (ETCD_BIN v3.5.36; plain etcd on 127.0.0.1:2379 for store): store and cmd/cca ok,
400 PASS (`rw4-etcd-integration.txt`). shellcheck: local_admin_access.sh 0, gate_tls_checks.sh 1 (pre-existing
SC2034). `ansible-playbook --syntax-check`: baremetal, ha, demo, site, test-deploy, rotate-root-key ok.

**Main session on rework 4 (2026-11-25):**
- **SA3-L3's API positive control is rejected as redundant.** The only cluster-identity client certificate on a gate
  node is the node identity. Naming it in automation would weaken the CR-H1 invariant
  (`TestAutomationNeverUsesCertificateAsAdminClient`, a user decision). The deploy run already proves that the API
  accepts a valid cluster-identity client certificate: every agent connects with `node-identity-for-api.pem`, and the
  node-registration checks pass. T19 keeps the exit 35/56 rule and the :443 gate-pair control.
- **`TestDeterministicPlanVerification/rollout` failed once under -race:** a test bug. The rollout case leaves
  `RolloutController.Now` on `time.Now` (controllers/rollout.go:58); only the failure controller's clock is frozen.
  Not caused by M280, not rerun; it becomes milestone M285.

### 3j. Review round 4 (2026-11-25, on af63794..3b06fe3)

Both verdicts approve with LOW findings only: security-audit (m280-security-audit-r4.txt) and code-reviewer
(m280-code-review-r4.txt, Codex job review-mwwbjjnk-1nxygq). Every round-3 finding is closed:
- the preflight writes 0 keys before it exits (15 on a valid start);
- the head binary exits with empty stdout, where the base binary printed "Secret keyring ready" first;
- 22 mutations are caught.

| ID | Finding | Disposition |
|---|---|---|
| SA4-L1 | T19 and `TestGateCertificateIsRefusedByEtcd` prove only that the ServerAuth-only gate certificate is refused, not CA separation. A ClientAuth certificate from the gate CA got TLS through etcd (200) and the API (403 at authorization) in a live run with the gate CA wrongly trusted | Fix: the Go real-etcd tests, and T19 on the deploy cluster, also present a ClientAuth certificate signed by the gate CA (T19 mints it on the control node in a root-only temp dir, never printed, removed after). Refusal must be at TLS (exit 35/56) |
| SA4-L2 | `checkServerCAKinds` reads only the first certificate of a CA file; a bundle passes | Fix: check every certificate in each CA file |
| SA4-L3 | `ExecStartPre=/usr/sbin/nft` runs as `User=etcd` on units without `PermissionsStartOnly` (setup.sh's own unit), so etcd never starts (fails closed) | Fix: `ExecStartPre=+/usr/sbin/nft …` |
| SA4-L4 | `systemctl restart nftables` flushes the peer rule while etcd keeps running | Fix: a reload unit with `PartOf=nftables.service` and `After=`, which re-applies the file; etcd does not require it |
| CR4-L1 | Only the etcd_integration test guards the startup order, and CI runs `-short` (M63 survives) | Fix: the binary test asserts empty stdout before the exit |
| CR4-L2 | Ignoring the record error in `configureAndStartAPIServer` fails no test (M58b) | Accepted, same class as M45: no binary-level `cca server` test; recorded |
| CR4-L3 | The spec and design say "without the flag it warns", but a pre-M280 cluster records the server certificate's CN silently; the record exit names the node ID, not the path | Fix: wording in the spec and design to match the code; the exit message names the path too |
| CR4 style | Test receivers `failingStore`/`droppingStore`; the `ansibleTaskField` doc says string but returns `any` | Fix |
| Codex | The read-back accepts any existing record | Rejected by the reviewer: every reader of `enrollment/node/{id}` checks existence only (secret_delivery.go:185, enrollment.go:308) |
| CI | No workflow runs `-tags etcd_integration` | M270 (real-etcd test tier), as recorded before |

### 3k. Rework round 5 (2026-11-25, lead-developer, on 3b06fe3; outputs in scratchpad rw5-*.txt)

Red is the auditor's configuration or a mutation on the fixed tree that restores the old behaviour (`rw4-mutate.sh`,
`rw5-mutations.txt`); green is the same check on this tree.

| ID | Red | Fix and green |
|---|---|---|
| SA4-L1 (Go) | M64, etcd trusting etcd access + gate CA and the API `--ca` = cluster + gate CA (auditor's trust-only): `TestGateCertificateIsRefusedByEtcd` "etcd did not refuse a ClientAuth certificate from the gate HTTPS CA at TLS: response HTTP/1.1 200"; `TestAPIRefusesNonClusterCACertificates` the same for the API plus "accepted … principal node:ctrl" (`rw5-red-trust.txt`) | `issueGateCAClientKeyPair` (ClientAuth from the gate CA) and `tlsServerRefusesClientCertificate` (TLS alert at the handshake or on the first read, not an HTTP status); the real-etcd test (new cluster) and the API test require refusal at TLS. Green, also under `-short` |
| SA4-L1 (T19) | Live harness `rw5live/t19.sh` (reviewer setup, rebuilt): trust-only with the 3b06fe3 binary: probe "200 0 200 0", T19 FAIL "etcd did not refuse a ClientAuth certificate from the gate CA" (`rw5-t19-live.txt`) | `gate_tls_check_gate_ca_client_refused` (called by T19): on the control node, as root, mints a ClientAuth certificate from the gate HTTPS CA in a `mktemp -d` dir (umask 077), checks it (`openssl verify -purpose sslclient`, key matches), presents it to :2379 and :9770, prints only "<code> <exit>", removes the files on exit (trap, `rm -f` + `rmdir`). Both must be 35/56. Normal: probe "000 56 000 56", T19 PASS; eku-both FAIL; no key file left in /tmp. A cluster from before M280 (no gate CA key) logs "T19 SKIP (gate CA client)": one CA there, nothing to separate. With this tree's binary, the trust-only `--ca` bundle stops the server (SA4-L2) |
| SA4-L2 | M69 (first certificate only): `TestServerCAKindChecks` | `checkCAFileKind` checks every certificate of each CA file; new cases: `--ca` bundle cluster + gate refused, cluster + pre-M280 CA accepted. Green |
| SA4-L3 | M65 (no "+"): `TestEtcdStartsOnlyAfterThePeerFirewall` | `ExecStartPre=+/usr/sbin/nft -f …`; the test requires `+` and the firewall unit's command. Green |
| SA4-L4 | M66 (no PartOf), M67 (no After), M68 (etcd Requires= the reload unit): same test | `ccattler-etcd-peer-firewall-reload.service` (PartOf= and After=nftables.service, WantedBy=nftables.service, the same nft command), enabled and started, never restarted; etcd does not depend on it. The test reads both units from the task YAML. Green |
| CR4-L1 | M63b (identity check after the store, af63794 order) now caught by the default-build `TestServerExitsOnInvalidNodeCertificateBeforeStartup` (stdout must be empty); runs under `-short` | Green |
| CR4-L3 | M70 (record error without the path): `TestControlPlaneNodeRecordMustBeConfirmed` | `verifiedNodeIdentity` carries the path; the record exit names it. Spec requirement and design row describe the no-flag cases as the code does (old cluster records the server certificate's CN; `ccattler-server` warns), new scenario "No control-plane node certificate" with its tests; the API scenario includes the gate-CA client certificate. `openspec validate --strict` valid |
| CR4 style | — | Receivers `failingTransactionStore`, `droppingTransactionStore`; `ansibleTaskField` doc says it returns any YAML value |

Checks: gofmt clean; `go vet ./...` with and without `-tags etcd_integration` clean; golangci-lint 0 issues (also
`--build-tags etcd_integration` on cmd/cca and store) (`rw5-checks.txt`); `go test -count=1 ./...` exit 0, 20
packages ok (`rw5-full-test.txt`); `-race` on security, controllers, cmd/cca ok (`rw5-race.txt`); etcd_integration
(ETCD_BIN v3.5.36; plain etcd on 127.0.0.1:2379 for store): store and cmd/cca ok, 400 PASS
(`rw5-etcd-integration.txt`); shellcheck: local_admin_access.sh 0, gate_tls_checks.sh 1 (pre-existing SC2034);
`ansible-playbook --syntax-check` on the 6 playbooks ok; `openspec validate separate-cluster-cas --strict` valid.
Only the deploy run proves the T19 mint on the real control node. Corrected in rework 8 (CR7-F1): the reload unit
(since replaced by the sync timer) is in the etcd-cluster role, which the deploy run does not use; the role is proven by
the template tests and systemd models and verified on a host at the next E2E HA run.

**Main session on rework 5 (2026-11-25):** the agent's own choice "T19 SKIP (gate CA client)" on a pre-M280
single-CA cluster (no gate CA key) is accepted. Such a cluster has one CA and no separation to prove (user decision
1: no migration). The closing full run builds a fresh three-CA cluster, and its log must show T19's gate-CA client
check ran and passed, not skipped. Check this when recording 3.4.

**Stop-time check (Codex, 2026-11-25), CONFIRMED by the main session:** T19's remote script treats a missing
`gate-https-ca-key.pem` as a pre-M280 cluster and returns `single-ca`, which the caller logs as a SKIP
(gate_tls_checks.sh:318, :332). A broken three-CA cluster missing its gate CA key would therefore pass without
proving separation.
- **Fix (rework 6, batched with review round 5):** decide the generation by the same rule as the layout detection.
  `single-ca` only when `ca.pem` exists and `cluster-identity-ca.pem` does not; a three-CA cluster missing the gate
  CA key FAILS T19.
- **Test:** the stub harness covers both cases.

### 3l. Review round 5 (2026-11-25, on 3b06fe3..8b2ccfc)

Both verdicts approve with LOW findings only: security-audit (m280-security-audit-r5.txt) and code-reviewer
(m280-code-review-r5.txt, Codex job review-mwwcd0zp-gjkjy7).
- **Live T19 runs:** it passes on a correct cluster and FAILs in every broken-trust setup: etcd trusting the gate CA,
  the API trusting it, or both.
- **All round-4 mutations are caught.**
- **The closing run cannot take the SKIP path:** the PKI is wiped, setup.sh installs the gate CA key, and the
  `cca server` preflight requires it.

| ID | Finding | Disposition |
|---|---|---|
| Stop-time / SA5-L1b / CR5-L1 | T19 decides "pre-M280" by the gate CA key alone; a three-CA cluster missing it SKIPs | Fix: `single-ca` only when `ca.pem` exists and `cluster-identity-ca.pem` does not; a three-CA cluster without the gate CA key FAILs |
| CR5-L2 | After a SKIP, the T19 PASS line still names the gate-CA check | Fix: the PASS line names only what ran |
| SA5-L1a / CR5-S2 | dash skips the EXIT trap on TERM/HUP/PIPE, so an interrupted T19 leaves the minted pair in the root-only temp dir | Fix: also `trap "exit 1" HUP INT TERM PIPE` |
| SA5-L2 | `systemctl reload nftables` (and a first start while the reload unit is active) flushes the peer rule; nothing re-applies it | Fix: `ReloadPropagatedFrom=nftables.service` plus `ExecReload=` on the reload unit; extend the template test. A manual `nft flush ruleset` stays an operator action (documented) |
| CR5-S1 | "mint-failed" gives no reason | Fix: return openssl's error line (it never contains key material) |
| CR5-S3 | `tlsServerRefusesClientCertificate` counts any TLS alert as a refusal | Fix: require a certificate-related alert (bad certificate, unknown CA, certificate required) |
| Codex | "curl exit 56 is generic" | Rejected: the exit 35/56 rule was accepted in round 4 (SA3-L3); T19's positive controls hit the same endpoints first; the Go tests check the TLS alert |
| Not findings | M63c (check moved before any write) and R5f/R5g (defensive branches) survive; no observable effect | Recorded |

### 3m. Rework round 6 (2026-11-26, lead-developer, on 494d07e; outputs in scratchpad rw6-*.txt)

A reboot cleared the scratchpad before this round, so the reviewers' round-5 harnesses were gone. They were rebuilt in
`rw6harness/` (T19 stub harness `t19stub.sh`, dash interrupt test `dashinterrupt.sh`) and `rw6systemd/model.sh`
(user-level systemd model rendered from the role's unit). Red is the 494d07e file run through the same harness.

| ID | Red | Fix and green |
|---|---|---|
| Stop-time / SA5-L1b / CR5-L1 | `t19stub.sh` on 494d07e: a three-CA PKI without `gate-https-ca-key.pem` logs "T19 SKIP (gate CA client)" and T19 PASS, exit 0 (`rw6-t19-harness.txt`) | Remote script prints `single-ca` only when `ca.pem` exists and `cluster-identity-ca.pem` does not (the `pki_file_remote_expression` rule); a three-CA cluster without the gate CA key prints "mint-failed: three-CA layout without …/gate-https-ca-key.pem" and T19 FAILs. Green: three-CA PASS, single-CA SKIP + PASS, no-key FAIL |
| CR5-L2 | Same harness on 494d07e, single-CA: the PASS line after the SKIP still names "a ClientAuth certificate from the gate CA" | `gate_tls_check_gate_ca_client_refused` returns 1 after its SKIP line; the caller drops the gate-CA claim. Green: single-CA PASS line names only the gate certificate |
| SA5-L1a / CR5-S2 | `dashinterrupt.sh` on 494d07e (dash, curl blocked, signal to the process group): TERM, HUP, INT and PIPE each leave key.pem, certificate.pem, request.pem in the temp dir (`rw6-dash-interrupt.txt`) | `trap "exit 1" HUP INT TERM PIPE` next to the EXIT trap. Green: no file left for all four signals |
| SA5-L2 | `TestEtcdStartsOnlyAfterThePeerFirewall`: "lacks ReloadPropagatedFrom=nftables.service", "lacks ExecReload=…" (`rw6-red-reload.txt`); `rw6systemd/model.sh` on the 494d07e unit: `systemctl reload` of the model nftables logs "flush" only (`rw6-systemd-model.txt`) | Reload unit has `ReloadPropagatedFrom=nftables.service` and `ExecReload=/usr/sbin/nft -f /etc/ccattler/etcd-peer-firewall.nft`; the test requires both. Green: test ok; model reload logs "flush apply", restart "flush apply". A manual `nft flush ruleset` is documented as an operator action (role task comment, PKI_GRAPH row) |
| CR5-S1 | `t19stub.sh` on 494d07e, unreadable gate CA: "mint-failed" with no reason | Each openssl step writes its stderr to a root-only file; failure prints "mint-failed: <step>: <first error line>" (self-signature and subject lines skipped). Green: "mint-failed: sign: Could not find CA certificate from …"; no "BEGIN" in any harness output |
| CR5-S3 | New `TestTLSRefusalCountsOnlyCertificateAlerts` on 494d07e's helper: handshake_failure, protocol_version and internal_error counted as refusals (`rw6-red-alert.txt`) | `isCertificateRefusalAlert`: a received alert (`net.OpError` "remote error") that is bad certificate, unknown CA or certificate required. Green (`rw6-green-alert.txt`); the real-etcd and API refusal tests still pass |

Checks: gofmt clean; `go vet ./...` with and without `-tags etcd_integration` clean; golangci-lint 0 issues (also
`--build-tags etcd_integration` on cmd/cca and store) (`rw6-checks.txt`); `go test -count=1 ./...` exit 0, 20
packages ok, no known-flaky failure (`rw6-full-test.txt`); `-race` on security, controllers, cmd/cca ok
(`rw6-race.txt`); etcd_integration (ETCD_BIN v3.5.36, re-downloaded, SHA256SUMS verified; plain etcd on
127.0.0.1:2379 for store): store and cmd/cca ok (`rw6-etcd-integration.txt`); shellcheck gate_tls_checks.sh 1
(pre-existing SC2034); `ansible-playbook --syntax-check` on the 7 playbooks ok (the two rotate-root-key-* files
are included task lists); `openspec validate separate-cluster-cas --strict` valid.

**User decision (2026-11-26, "go for a"): reconcile the peer rule with a timer, the kubeadm/kube-proxy model of
re-syncing instead of trusting.** This supersedes both the conditional start below and the reload unit.
- **The timer:** a systemd timer (`ccattler-etcd-peer-firewall-sync.timer` running a separate oneshot
  `-sync.service`; name corrected in rework 7, because a timer named after the boot unit would trigger that
  RemainAfterExit unit only once), every 30 s, plus at boot) runs
  `nft -f /etc/ccattler/etcd-peer-firewall.nft`, the atomic, idempotent table replace.
- **What it covers:** nftables' first start, reload, restart and stop, and a manual `nft flush ruleset`. The rule
  is missing for at most one interval plus the timer accuracy (about 31 s by default; wording corrected in rework 8);
  this is documented.
- **What is removed:** the reload unit (`PartOf=`, `ReloadPropagatedFrom=`, `ExecReload=`) and any start condition.
- **What stays:** etcd's `ExecStartPre=+` (etcd never starts without the rule) and the firewall unit at boot.
- **What the role never does:** enable or start `nftables.service`, whose `flush ruleset` would wipe the cca agent's
  data-plane tables on live re-runs.

**Superseded: resolved by the main session (2026-11-26), no decision needed; the question to the user is
withdrawn.**
- **The cause:** the reload unit is already `WantedBy=nftables.service`, so any start of nftables pulls it in. But the
  role starts it unconditionally (etcd-cluster/tasks/main.yml:118-123). While nftables is inactive the unit sits
  "active" (RemainAfterExit), so nftables' first start does not run it again.
- **Fix (rework 7, batched with review round 6):** the role checks `systemctl is-active nftables`. If nftables is
  active, it starts the reload unit. If not, it only enables the unit and keeps it stopped (an existing active
  instance is stopped), so nftables' first start runs it and re-applies the rule.
- **Meanwhile:** the peer rule comes from the firewall unit and from etcd's `ExecStartPre=+`.
- **What this avoids:** enabling or starting nftables.service on etcd members, whose `flush ruleset` would also
  wipe the cca agent's own tables.
- **Test:** the systemd model shows "flush apply" on a first start after the role ran with nftables inactive.

**Decided, no longer open (the user's timer decision above, 2026-11-26).** Rework 6 had left one case open, which
the timer decision now covers in rework 7: SA5-L2's second case.
- **The case:** a first `systemctl start nftables` while the reload unit was already active (nftables inactive when
  the role ran) flushed the peer rule without re-applying it.
- **Evidence:** `rw6systemd/model-first-start.sh` logged "flush" only (`rw6-systemd-first-start.txt`).
  `ReloadPropagatedFrom=` does not cover a start.
- **Rework 7's test:** the same model shows the timer re-applying the rule after that first start.

### 3n. Review round 6 (2026-11-26, on 8b2ccfc..30d1931)

Both verdicts approve with LOW findings only: security-audit (m280-security-audit-r6.txt, harnesses in audit-r6/)
and code-reviewer (m280-code-review-r6.txt, Codex job review-mwxeydem-420i73). Every 3l row is closed.
- **T19 under cluster_test.sh's `set -Eeuo pipefail`:** it PASSes, SKIPs and FAILs exactly as intended.
- **Interrupt cases:** 32 of them, under dash and bash, leave no files and always FAIL T19.

| ID | Finding | Disposition |
|---|---|---|
| CR6-L1 / SA6 | With OpenSSL 3, the verify step's "mint-failed" reason is the certificate subject, not the error | Fix: take the first `^error` line for that step |
| SA6 | `certificate_required` counts as a refusal, but Go sends it only when the client sent no certificate, so an empty key pair could "prove" separation | Fix: `tlsServerRefusesClientCertificate` fails the test when the client certificate is empty |
| CR6-L2 | The reload-unit test checks lines anywhere, not per section (ExecReload in [Unit] passes) | Moot: the reload unit is removed by the timer decision. The new timer test checks settings per section |
| CR6-L3 | PKI_GRAPH says a manual flush is repaired at the next nftables start | Fix: rewrite the line for the timer |
| SA6 note | A timer whose service has `RemainAfterExit=true` runs it once and idles | Fix in the timer design: a separate oneshot service without RemainAfterExit; don't reuse the firewall unit |
| Codex | The single-CA rule could hide a partial three-CA install | Rejected: the rule was decided in 3k, new clusters never write pki/ca.pem, and a three-CA cluster without `cluster-identity-ca.pem` has no API, so T19's own API check fails first |
| Suggestions | T19's remote script is one 1.7 KB line; test files are named after rework rounds | Recorded; not changed in M280 |

### 3o. Rework round 7 (2026-11-26, lead-developer, on 30d1931; outputs in scratchpad rw7-*.txt)

Red is the 30d1931 file (role, script or helper) or a mutation of this tree (`rw7-mutations.txt`); green is the same
check on this tree. Unit names follow the main session's rework-7 brief: `ccattler-etcd-peer-firewall-sync.timer`
runs `ccattler-etcd-peer-firewall-sync.service` (the decision block above says `ccattler-etcd-peer-firewall.timer`,
which by systemd's naming would run the boot unit with `RemainAfterExit=`, the SA6 note).

| ID | Red | Fix and green |
|---|---|---|
| Decision "go for a" (timer) | New `TestEtcdPeerFirewallSyncTimer` and `TestEtcdRoleLeavesNftablesServiceAlone` on the 30d1931 role: no role defaults, the reload-unit tasks still have `peer-firewall-reload`, `PartOf=nftables`, `ReloadPropagatedFrom=`, `WantedBy=nftables` (`rw7-red-timer.txt`). Model `rw6systemd/model-timer.sh` (model interval 3 s) on the 30d1931 reload unit: after nftables' first start and a manual flush, the rule is "NOT back within 5s" (`rw7-systemd-timer-model.txt`; its stop and reload rows are not claimed, the model does not enable the unit) | Reload unit and its tasks removed. `ccattler-etcd-peer-firewall-sync.service` (oneshot, no RemainAfterExit, the boot unit's nft command) and `.timer` (`OnBootSec=`, `OnUnitActiveSec=` = `etcd_peer_firewall_sync_interval` 30s, `AccuracySec=` 1s so the exposure stays one interval, `WantedBy=timers.target`), enabled and started; interval and accuracy are role defaults (new `roles/etcd-cluster/defaults/main.yml`). Boot unit and etcd's `ExecStartPre=+` kept; the role never manages nftables.service. Tests check settings per section ([Unit], [Service], [Timer], [Install]) and no RemainAfterExit; mutations caught: M71 RemainAfterExit in the service, M72 OnUnitActiveSec in [Install], M73 a task starting nftables. Model green: first start back after 3 s, stop 4 s, reload 3 s, manual flush 4 s (interval 3 s plus the 1 s accuracy and 0.5 s polling) |
| CR6-L1 | `t19stub.sh` case three-ca-noncA-gate (the reviewer's) on 30d1931: "mint-failed: verify: CN=gate" (`rw7-t19-harness.txt`) | `mint_failed` takes the first `^error` line, else the filtered first line. Green: "mint-failed: verify: error 79 at 1 depth lookup: invalid CA certificate"; the other harness cases unchanged; dash interrupt cases still leave no file (`rw7-dash-interrupt.txt`) |
| SA6 | New `TestTLSRefusalRejectsAnEmptyClientCertificate` with the empty-certificate check mutated away: "an empty client certificate did not fail the test: returned=true" (`rw7-red-empty.txt`) | `tlsServerRefusesClientCertificate` (now `testing.TB`) fails the test on an empty certificate; the alert test uses a real gate-CA client pair. Green (`rw7-green-empty.txt`); the real-etcd and API refusal tests pass |
| CR6-L3 | PKI_GRAPH said a manual flush lasts until the next etcd or nftables start or reload | PKI_GRAPH row, role task comment, etcd drop-in comment and the nft template header describe the timer: the longest exposure is one interval |

Checks: gofmt clean; `go vet ./...` with and without `-tags etcd_integration` clean; golangci-lint 0 issues (also
`--build-tags etcd_integration` on cmd/cca and store) (`rw7-checks.txt`); `go test -count=1 ./...` exit 0, 20
packages ok, no known-flaky failure (`rw7-full-test.txt`); `-race` on security, controllers, cmd/cca ok
(`rw7-race.txt`); etcd_integration (same ETCD_BIN v3.5.36; plain etcd on 127.0.0.1:2379 for store): store and
cmd/cca ok (`rw7-etcd-integration.txt`); shellcheck gate_tls_checks.sh only the pre-existing SC2034;
`ansible-playbook --syntax-check` on the 7 playbooks ok (`rw7-ansible.txt`); `openspec validate
separate-cluster-cas --strict` valid. Hosts that ran rework 5 or 6 keep a stale, harmless reload unit file; the
role does not remove it. Corrected reason (rework 8, CR7-F4): the cleanup role removes only the cca and etcd units and
HA VMs are reused, so the file can stay; it was never released and applies the same rule.

**Stop-time check (Codex, 2026-11-26), CONFIRMED:** the "longest exposure is one interval" wording is overstated.
`AccuracySec=1s` lets systemd fire up to 1 s late, so a flush just after a tick leaves the rule missing for up to
interval + AccuracySec, plus the nft run time (about 31 s with the defaults).
- **Fix (rework 8, batched with review round 7):** every place that states the bound says "at most one interval
  plus the timer accuracy (about 31 s by default)": PKI_GRAPH, the role and drop-in comments, the nft template
  header, and this file's decision block.

**Main session on rework 7 (2026-11-26):** all three of the agent's choices are accepted.
- **The `-sync` unit names,** per the brief; the decision block is corrected above.
- **`AccuracySec=1s`:** systemd's 1-minute default would stretch the window past one interval.
- **No removal task for the reload unit:** it only ever existed in unreleased reworks 5 and 6, so at most on
  testbed VMs. It applies the same rule and does no harm, and a removal task for a never-released artifact would be
  dead weight in the role.

### 3p. Review round 7 (2026-11-26, on 30d1931..da458db)

Both verdicts approve the code: security-audit (m280-security-audit-r7.txt, audit-r7/) and code-reviewer
(m280-code-review-r7.txt, cr-r7-mutations.txt, Codex). The systemd 257 models show the timer firing through failing
runs, a missing rule file and daemon reloads, and the rule coming back after a first start, stop, reload or flush.

| ID | Finding | Disposition |
|---|---|---|
| CR7-F1 (MEDIUM, evidence) | The closing Deploy Testbed run never runs the etcd-cluster role: it uses baremetal-deploy.yml (cluster_test.sh:245-246); only ha-deploy.yml through the manual e2e-ha.yml does. The claims at the old tasks.md :197 and :496 that the deploy run proves the timer, the peer firewall and `ExecStartPre=+` are wrong | Fix: correct those claims. The role's behaviour is proven by the template tests and the systemd models; on a real host it is verified on the next E2E HA run as it occurs (the user's standing rule: one dispatch, other CI scenarios verified as they occur). Not a closing condition of M280 |
| Stop-time | The exposure bound is interval plus AccuracySec (about 31 s), not one interval | Fix the wording everywhere (recorded above) |
| SA7-L1 | A oneshot has no start timeout, so a hung `nft` stops the timer forever | Fix: `TimeoutStartSec=` shorter than the interval on the sync service, as a role default |
| SA7-L2 | The timer test does not forbid `Unit=`; pointing it at the boot unit (RemainAfterExit) passes | Fix: forbid `Unit=` |
| SA7-L3 / CR7-F3 | The nftables test only checks systemd tasks; a `command`/`shell` enabling nftables or running `nft flush ruleset`, or `Wants=nftables.service` inside a unit, passes | Fix: no task content may mention `nftables.service` or `flush ruleset` |
| CR7-F2 | The tests find the units by task name and never check the copy destinations | Fix: assert both `/etc/systemd/system/ccattler-etcd-peer-firewall-sync.{service,timer}` |
| CR7-F4 | The reason for keeping the old reload unit ("testbed runs wipe the cluster") is wrong; the cleanup role removes only cca and etcd units, and HA VMs are reused | Fix: correct the reason. The decision stands: never released, harmless |
| Suggestions | The nft template hardcodes "every 30 s"; the raw alert code 116 appears twice; the empty-certificate test starts an unused server; the timer decision belongs in design.md | Fix all four (design.md gets a D8 for the peer-rule reconcile timer) |
| Codex | "Upgrade leaves the reload unit enabled" | Rejected as a defect (never released, harmless, accepted choice); its factual part is CR7-F4 |

### 3q. Rework round 8 (2026-11-26, lead-developer, on 0225604; outputs in scratchpad rw8-*.txt)

Red is the reviewers' mutation run on da458db (`cr-r7-mutations.txt`: R1, R2, R11 NOT CAUGHT; `audit-r7/mutation-timer-unit.txt`, `audit-r7/mutation-nft-command.txt`: pass); green is `rw8mut.sh` (their `cr-r7mut.sh` plus R12-R17) on a copy of this tree (`rw8src/`), `rw8-mutations.txt`: baseline pass, R1-R17 all caught.

| ID | Fix and green |
|---|---|
| CR7-F1 | Corrected in place: the SA-L1 row (3c), the "Only the deploy run proves" note (3g) and the rework-5 closing line (3k) no longer claim the deploy run proves the etcd-cluster role; design.md's Ansible risk names which roles the deploy run uses. The role is proven by the template tests and systemd models and verified on a host at the next E2E HA run. PKI_GRAPH made no such claim |
| Stop-time (bound) | "at most one interval plus the timer accuracy (about 31 s by default)" in the role comment, role defaults, the etcd drop-in comment, the nft template header, PKI_GRAPH, this file's decision block and design.md D8 |
| SA7-L1 | `TimeoutStartSec={{ etcd_peer_firewall_sync_start_timeout }}` (role default 20s) on the sync service; `TestEtcdPeerFirewallSyncTimer` requires it and a positive timeout below the interval. R15 (line removed), R16 (30s) caught |
| SA7-L2 | The timer test forbids `Unit=` in any section. R12 (`Unit=ccattler-etcd-peer-firewall.service`) caught |
| SA7-L3 / CR7-F3 | `TestEtcdRoleLeavesNftablesServiceAlone` scans every task's YAML (module, command, shell, unit content) for the word nftables and for `flush ruleset`. R11, R13 (`command: systemctl enable --now nftables`), R14 (`shell: nft flush ruleset`), R17 (`Wants=nftables.service` in the service) caught |
| CR7-F2 | Both copy destinations asserted (`/etc/systemd/system/ccattler-etcd-peer-firewall-sync.{service,timer}`). R1, R2 caught |
| CR7-F4 | Reason corrected in 3o: the cleanup role removes only the cca and etcd units and HA VMs are reused; the unit stays because it was never released and applies the same rule |
| Suggestions | nft template header uses `{{ etcd_peer_firewall_sync_interval }}` (the timer test renders it with the role defaults); `tlsAlertCertificateRequired` constant; the empty-certificate test dials nothing (`unreachableTLSAddress`), R9 still caught; design.md D8 records the peer-rule reconcile timer (user decision 2026-11-26) |

Checks: gofmt clean; `go vet ./...` with and without `-tags etcd_integration` clean; golangci-lint 0 issues (also
`--build-tags etcd_integration` on cmd/cca and store) (`rw8-checks.txt`); `go test -count=1 ./...` exit 0, 20
packages ok, no known-flaky failure (`rw8-full-test.txt`); `-race` on security, controllers, cmd/cca ok
(`rw8-race.txt`); etcd_integration (same ETCD_BIN; plain etcd on 127.0.0.1:2379 for store): store and cmd/cca ok
(`rw8-etcd-integration.txt`); shellcheck gate_tls_checks.sh only the pre-existing SC2034; `ansible-playbook
--syntax-check` on the 7 playbooks ok (`rw8-ansible.txt`); `openspec validate separate-cluster-cas --strict` valid.

**Stop-time check (Codex, 2026-11-26), CONFIRMED (wording):** "at most one interval plus the timer accuracy (about
31 s)" omits the sync command's own run time. A normal `nft -f` takes milliseconds; a hung one is cut off by
`TimeoutStartSec` (20 s), and the timer fires again on the next tick.
- **Fix (rework 9, batched with review round 8):** the wording becomes "one interval plus the timer accuracy plus the
  `nft` run time: normally about 31 s, at most interval + accuracy + start timeout if `nft` hangs", wherever the
  bound is stated.

### 3r. Review round 8 and the structural cut (2026-11-26)

Both round-8 verdicts approve (m280-security-audit-r8.txt, m280-code-review-r8.txt), with LOW findings only, all
about the HA etcd peer-port firewall and its sync timer.
- **Why stop:** that component, added in rework 1 for round 1's LOW SA-L1, produced findings in rounds 4 to 8.
  openspec/config.yaml:41 (three same-class rounds → structural fix and tell the user) was breached; the main
  session acknowledged this to the user.

**User decision (2026-11-26, "yes do that"): remove the peer firewall entirely; the product does not manage host
firewalls.**
- **What is removed:**
  - the etcd-cluster role's nft rule table and template;
  - its boot unit, sync service and timer, and their defaults;
  - the firewall lines in etcd's drop-in (`Requires=`, `After=`, `ExecStartPre=+`);
  - their tests, and D8 (superseded).
- **The Kubernetes model:** kubeadm documents the required ports ("2380: etcd peers only") and leaves firewalling to
  the operator. Its etcd peer and client certificates also come from one etcd CA.
- **What is documented instead:** the guide and the HA docs get a port table (2379 from the API servers and agents,
  2380 from etcd members only, 9770, 443/8080 for gates).
- **SA-L1 is re-dispositioned to an accepted risk:** a worker's etcd client certificate is accepted on 2380 if the
  operator leaves the port open. This is narrower than before M280, when every cluster certificate was accepted.
  It is stated in the threat model and moves to M181.
- **The round-8 LOWs** (SA8 1-5, CR8 F1-F4) are moot with the removal; the recorded suggestions stay follow-ups.
- **Process (user, the same day):** after each review round the main session stops and waits for the user's go. At
  most 2 review rounds per milestone. One small review of this removal was approved as part of the decision.

### 3s. Removal of the peer firewall (2026-11-26, lead-developer, on de884c7; outputs in scratchpad rw9-*.txt)

Implements the 3r user decision. Nothing new is ticked.
- **Removed (deploy/ansible/roles/etcd-cluster):**
  - templates/etcd-peer-firewall.nft.j2 and defaults/main.yml (only the sync timer's three variables; `git rm`);
  - tasks/main.yml: the rule-file task, the boot unit `ccattler-etcd-peer-firewall.service` and its start task, the
    sync service and timer and their start task, the "reload changed rules" task (no reload-unit leftovers remained);
  - templates/etcd-cluster.conf.j2: `[Unit] Requires=`/`After=` on the firewall unit, `ExecStartPre=+/usr/sbin/nft`,
    and their comment; a comment now says the operator limits 2380.
- **Removed tests (firewall only):** cmd/cca/cluster_pki_rework7_test.go (`TestEtcdPeerFirewallSyncTimer`,
  `TestEtcdRoleLeavesNftablesServiceAlone` and their helpers, used nowhere else; `git rm`),
  `TestEtcdStartsOnlyAfterThePeerFirewall` (rework3) and `TestAnsibleEtcdPeerPortLimitedToMembers` (rework). The
  layout-render tests of etcd-cluster.conf.j2 stay.
- **Docs:** design.md D8 marked superseded, the Risks list names the accepted risk and the HA-roles risk no longer
  lists the firewall; PKI_GRAPH's etcd (HA peers) row states the operator's job and the accepted risk.
- **Added:** a port table (2379, 2380, 9770, 443/8080, plus 4789/udp VXLAN) with "firewalling is the operator's
  job" in website/installation.md ("Ports and firewalling") and website/reference/architecture.md ("Multi-node
  control plane"); the accepted risk (2380 trusts the etcd access CA until M181) in design/threat-model.md (T1) and
  design/security-model.md (cluster PKI).

**Stop-time check (Codex, 2026-11-26): "existing deployments retain the peer firewall after upgrade". Recorded,
not acted on** (per the user's rule: stop-time findings never trigger work). The peer firewall was never released.
It existed only in unreleased M280 commits (reworks 1-8), so no user deployment has it; at most a testbed HA VM that
ran one of those commits.

**The small review of the removal (code-reviewer, 2026-11-26, de884c7..6dffd7e): approve.** The removal is complete
and safe; the HA playbook syntax check and `go test -short ./cmd/cca/` pass. One doc defect and three wording nits
were found, all fixed in one docs-only commit with no further review round (user's go, 2026-11-26):
1. **Defect:** the port tables lacked 15353/udp, the cluster DNS that agents query on the control plane. It is
   added to both tables; architecture.md no longer calls its table "the full table".
2. **Gate HTTP port:** 80 without `--proxy-listen`; the Ansible roles use 8080.
3. **Installation wording:** CCattler restricts no port beyond what `setup.sh` opens in firewalld or ufw (2379 only
   from `--etcd-client-cidr` networks).
4. **Threat and security models:** "narrower than before M280" is qualified to three-CA clusters; a single-CA
   cluster still accepts every cluster certificate on 2380.

Evidence: `go test -short -run 'Doc|Website|Install' ./cmd/cca/` ok.

## Workflow follow-up

- Release with `scripts/release.sh M280 <version>`. The changelog says:
  - new clusters have three CAs and self-explanatory file names;
  - TLS-gate clients trust `gate-https-ca.pem`;
  - old clusters warn and need re-initializing to separate the CAs;
  - until M181, every accepted etcd client certificate has full etcd access. Then archive (sync cluster-pki
  and host-setup).
