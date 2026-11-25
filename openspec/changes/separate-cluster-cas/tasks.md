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

## Workflow follow-up

- Release with `scripts/release.sh M280 <version>`. The changelog says:
  - new clusters have three CAs and self-explanatory file names;
  - TLS-gate clients trust `gate-https-ca.pem`;
  - old clusters warn and need re-initializing to separate the CAs;
  - until M181, every accepted etcd client certificate has full etcd access. Then archive (sync cluster-pki
  and host-setup).
