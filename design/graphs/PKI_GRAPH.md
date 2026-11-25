# CCattler PKI Graph (PKI)

Which CA signs which certificate, which usages each certificate carries, where it lives, and which listener trusts
it for client authentication. Call trees stay in [AUTHN_GRAPH.md](AUTHN_GRAPH.md) (CA, rotation, enrollment) and
[NETWORK_GRAPH.md](NETWORK_GRAPH.md) (RouteTLSController, gate proxy); this graph links to them. Flow "Cluster PKI"
in [runtime-invariant-flows.md](../runtime-invariant-flows.md) gives the lifecycle in steps.

State: **after M280** (separate-cluster-cas). New clusters (`cca init` from M280) have three CAs; clusters from
before keep one CA and their files, and `cca server` warns at every start (D6).

**Before M280** (bc9343a): one CA (`ca.pem`) signed every certificate, `IssueCertificate` gave every certificate
ServerAuth and ClientAuth, and etcd and the API both trusted that CA. A gate certificate for host `ccattler-server`
authenticated to the API as `node:ccattler-server` (cluster-admin) and to etcd as a root client. Red evidence:
openspec change `separate-cluster-cas`, tasks 1.1-1.3.

## 1. Inventory (new clusters)

File names are constants in `cmd/cca/cluster_pki_files.go`; every file starts with a `# CCattler ...` description
line, which `pem.Decode` and `openssl` skip. Keys are the same name with `-key.pem`, mode 0600.

| Certificate | Issued by | Subject OU / CN | ExtKeyUsage | TTL / rotation | Where | Loaded by |
|---|---|---|---|---|---|---|
| cluster identity CA | self (`NewCertificateAuthority`, security/ca.go:55) | cluster identity CA / CCattler cluster identity CA | CA | 10 y | `cluster-identity-ca.pem` (+ copy in `/etc/ccattler`) | server `--ca`/`--ca-key`, agent `--ca`, CLI |
| etcd access CA | self | etcd access CA / CCattler etcd access CA | CA | 10 y | `etcd-access-ca.pem` | etcd `ETCD_TRUSTED_CA_FILE`; server and agent `--etcd-ca`; server `--etcd-access-ca-key` |
| gate HTTPS CA | self | gate HTTPS CA / CCattler gate HTTPS CA | CA | 10 y | `gate-https-ca.pem` | server `--gate-ca-cert`/`--gate-ca-key`; clients of TLS gates |
| API server identity | cluster identity CA (cca init) | API server identity / `ccattler-server` | ServerAuth + ClientAuth | 1 y, none | `api-server-identity-certificate.pem` | `cca server --cert` (API listener; client of agent debug APIs) |
| node identity for API | cluster identity CA (cca init, or `SignCSR` at enrollment) | node identity for API / node ID | ClientAuth | 1 y, re-join | `node-identity-for-api.pem` | agent `--cert` |
| etcd serving | etcd access CA (cca init) | etcd serving certificate / `etcd-<node>` | ServerAuth + ClientAuth (etcd dials its own client port with it, etcd issue 9785; kubeadm model) | 1 y, none | `etcd-serving-certificate.pem` | etcd `ETCD_CERT_FILE`; HA `--cert-file` |
| etcd peer | etcd access CA (cca init) | etcd peer certificate / `etcd-peer-<node>` | ServerAuth + ClientAuth (HA peers authenticate each other) | 1 y, none | `etcd-peer-certificate.pem` | HA `--peer-cert-file` (etcd-cluster.conf.j2) |
| cca-server etcd client | etcd access CA (cca init) | cca-server etcd client / `etcd-client-cca-server` | ClientAuth | 1 y, none | `cca-server-etcd-client-certificate.pem` | `cca server --etcd-cert` |
| node etcd client | etcd access CA (cca init, or `signNodeEtcdClientCSR` at enrollment, security/enrollment.go:372) | node etcd client / `etcd-client-<node>` | ClientAuth | 1 y, re-join | `node-etcd-client-certificate.pem` | agent `--etcd-cert`; CLI etcd default |
| server (rotating, `--tls` without files) | auto cluster CA, `CertificateRotator` | API server identity / `ccattler-server` | ServerAuth + ClientAuth | 24 h, 70% | memory | API listener |
| gate (auto TLS) | gate HTTPS CA, else the cluster CA (old clusters), `issueSealedGateCertificate` (controllers/route_tls.go:166) | gate HTTPS / route host | **ServerAuth only** (both kinds of cluster) | 90 d, renewed 30 d before expiry; re-issued at once when it carries ClientAuth or another issuer signed it (:199) | `derived/route/{name}/tls/cert`, sealed key `secrets/route/{name}/tls/key` | agent gate proxy |

Old clusters keep `ca.pem`, `node.pem` (API and etcd client) and `etcd.pem`. The CLI, the agent and `cca join`
look for the new names first and fall back to these (`installedEtcdClientFileSets`, `installedNodeIdentityFileSets`,
`installedClusterCACandidatePaths`, cmd/cca/cluster_pki_files.go).

## 2. Trust matrix (client certificates)

| Listener | Trusts for client auth | Where | Identity |
|---|---|---|---|
| API (`--cert`/`--ca`) | cluster identity CA | `loadServerTLSConfig` `ClientCAs` (cmd/cca/command_server.go) | `node:<CN>` (security/authn_mtls.go); OU ignored |
| etcd (clients) | etcd access CA (new); cluster CA (old) | setup.sh `ETCD_TRUSTED_CA_FILE` (`buildSetupScriptEtcdStep`); HA `--trusted-ca-file` | none: no etcd RBAC, see §3 |
| etcd (HA peers) | etcd access CA | `--peer-trusted-ca-file`, `--peer-client-cert-auth` (etcd-cluster.conf.j2) | — |
| agent debug API | cluster identity CA | `SetMutualTLS`, agent/debug_api.go | `node:<CN>` holding cluster-admin |
| gate proxy | none (serving only) | network/gate_proxy.go | — |

The agent debug API trusts the cluster CA. After M280 gate certificates carry no ClientAuth, and on new clusters
they do not come from that CA at all, so a gate key cannot reach the debug API either.

## 3. Residual trust until M181

etcd's `--client-cert-auth` checks only that a client certificate chains to the CA it trusts; the CommonName
becomes an etcd user only once etcd authentication and roles are enabled (M181, Store Access Isolation). Until then:
- every accepted etcd client certificate, the server's or any node's, has **full etcd access**; so do etcd's own
  serving and peer certificates, which carry ClientAuth (they live only on etcd hosts);
- a compromised node, gate nodes included, keeps reading and writing the whole store with its own node etcd client
  certificate. M280 closes the gate-certificate path, not that one;
- `etcd-client-cca-server` and `etcd-client-<node>` are distinct identities that M181 can authorize differently;
  they are not separately authorized today.

## 4. Lifecycle entry points

| Step | Function | Call tree |
|---|---|---|
| CA creation and load | `NewCertificateAuthority(kind, …)`, `LoadCertificateAuthorityFromFiles` | AUTHN_GRAPH "CA lifecycle" |
| Issue / sign | `IssueCertificate` (required `Usage`), `SignCSR` | AUTHN_GRAPH "Certificate issuance" |
| Server certificate rotation | `CertificateRotator` | AUTHN_GRAPH "Certificate Rotation" |
| Node enrollment | `EnrollNode` → `signNodeCSR`, `signNodeEtcdClientCSR`; `cca join` → `writeEnrollmentCredentials` | AUTHN_GRAPH "EnrollNode" |
| Gate certificates | `RouteTLSController.Reconcile` → `gateCertificateNeedsReissue`, `issueSealedGateCertificate` | NETWORK_GRAPH "RouteTLSController" |
| Gate issuer, single-CA warning, enrollment etcd CA | `resolveGateCertificateIssuer`, `singleCAClusterWarning`, `loadEtcdAccessCertificateAuthority` (cmd/cca/server_cluster_cas.go) | — |
| Files and units | `executeInitCommand` → `resolveInitCertificateAuthorities`, `buildInitLeafCertificates`; `buildSetupScript` (cmd/cca/command_init.go) | — |

## 5. Debugging one-liners

```sh
# Who signed it, for whom, and for what:
openssl x509 -noout -issuer -subject -ext extendedKeyUsage -in /etc/ccattler/pki/node-etcd-client-certificate.pem
# A gate certificate from the store:
cca get gates   # shows not-after; the PEM is at derived/route/<gate>/tls/cert
# Does etcd accept this client certificate? (old clusters: -CAfile /etc/ccattler/pki/ca.pem)
openssl s_client -connect 127.0.0.1:2379 -CAfile /etc/ccattler/pki/etcd-access-ca.pem \
  -cert CERT.pem -key KEY.pem </dev/null 2>&1 | grep -E 'Verify return|alert'
```
