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
- [ ] **Only the deploy run proves:** `sudo cca status`/`scale`/`get` and the helper curl over ssh on the real hosts
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
detection on real hosts (fresh three-CA run; a pre-M280 cluster only through `--from-release`); the etcd drop-in's
`Requires=` at boot.

## Workflow follow-up

- Release with `scripts/release.sh M280 <version>`. The changelog says:
  - new clusters have three CAs and self-explanatory file names;
  - TLS-gate clients trust `gate-https-ca.pem`;
  - old clusters warn and need re-initializing to separate the CAs;
  - until M181, every accepted etcd client certificate has full etcd access. Then archive (sync cluster-pki
  and host-setup).
