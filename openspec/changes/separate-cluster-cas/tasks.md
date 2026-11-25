# Tasks

Milestone M280. Design: [design.md](design.md). Specs: [cluster-pki](specs/cluster-pki/spec.md),
[host-setup](specs/host-setup/spec.md).

## 1. Graph and tests first

- [ ] 1.0 `design/graphs/PKI_GRAPH.md` from today's code (D7): inventory, trust matrix, lifecycle links and debugging
  one-liners. It shows the single CA trusted by etcd and the API, and gate certificates with ClientAuth. Verify
  every row against the code (file:line).
- [ ] 1.1 `TestGateCertificateIsRefusedAsAPIClient`: a gate certificate for host `ccattler-server` presented to the
  API's mTLS. Red today, recording the principal it yields.
- [ ] 1.2 `TestGateCertificateHasServerAuthOnly`, `TestRouteTLSReissuesGateCertificateWithClientAuth`: red.
- [ ] 1.3 `TestInitWritesThreeCAs`, `TestSetupScriptUsesEtcdCA`, `TestEnrollmentIssuesEtcdClientCertificate`,
  `TestJoinWritesEtcdClientCertificate`, `TestAPIRefusesNonClusterCACertificates`,
  `TestServerWarnsOnSingleCACluster`: red.
- [ ] 1.4 `TestGateCertificateIsRefusedByEtcd` and `TestEtcdRefusesClusterCACertificates` with a real etcd
  (`etcd_integration` tag, as the existing etcd tests run): red.

## 2. Implementation

- [ ] 2.1 D2: `IssueCertificate` takes a required typed `Usage`; every caller is updated. PKI_GRAPH is updated.
- [ ] 2.2 D3: RouteTLSController issues with `UsageServer` from the gate CA when configured, else the cluster CA, and
  re-issues ClientAuth or foreign-issuer certificates. 1.1, 1.2 and 1.4 (gate) are green. NETWORK_GRAPH and
  PKI_GRAPH are updated.
- [ ] 2.3 D1 and setup.sh: cca init creates the etcd and gate CAs, `etcd.pem` (etcd CA, server) and
  `etcd-client.pem` (etcd CA, client). setup.sh installs them, etcd trusts `etcd-ca.pem`, and the units pass
  `--etcd-cert` and `--etcd-ca`. Server flags `--gate-ca-cert` and `--gate-ca-key`. 1.3 init and setup are green.
- [ ] 2.4 D4: enrollment returns the etcd client certificate; cca join writes it, and the agent unit uses it. 1.3
  enrollment and join are green. AUTHN_GRAPH and PKI_GRAPH are updated.
- [ ] 2.5 D5 and D6: the API trusts the cluster CA only, and the single-CA warning. 1.3 API and warning, and 1.4
  etcd, are green.
- [ ] 2.6 Ansible templates (the worker agent's etcd cert and CA; HA etcd trust); the guide (new files, TLS gate
  clients trust `gate-ca.pem`); `design/security-model.md`, `design/gate-tls-termination.md` and the threat model;
  Flow "Cluster PKI" in runtime-invariant-flows.md linking PKI_GRAPH.

## 3. Checks

- [ ] 3.1 gofmt, golangci-lint, `go test -count=1 ./...`, the `etcd_integration` tests, and -race on security,
  controllers and cmd/cca.
- [ ] 3.2 Deploy check `deploy-run:gate-cert-not-client`: on the deploy cluster, a gate certificate and key presented
  to etcd and to the API fail the TLS handshake. Then `install-host:three-cas` on a fresh VM with the guide.
- [ ] 3.3 Independent review: `security-audit` and `code-reviewer`, with a patterns review. One round; only HIGH and
  MEDIUM findings are fixed, lows are listed (the user's cap for install work applies here too unless the user says
  otherwise).
- [ ] 3.4 One full Deploy Testbed run (job summary `full run`); record the run ID.

## Workflow follow-up

- Release with `scripts/release.sh M280 <version>`. The changelog says: new clusters have three CAs; TLS-gate clients
  trust `gate-ca.pem`; old clusters warn and need re-initializing to separate the CAs. Then archive (sync cluster-pki
  and host-setup).
