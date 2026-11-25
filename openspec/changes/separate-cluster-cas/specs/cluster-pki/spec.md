# Spec Delta

## Purpose
States which CA signs which certificate in a CCattler cluster, which usages each certificate carries, and which CAs
each component trusts for client authentication, so a certificate made for one purpose cannot act as an identity for
another.

## ADDED Requirements

### Requirement: Gate certificates cannot authenticate clients
Every gate (route TLS) certificate SHALL carry only the ServerAuth extended key usage, in clusters with or without
a gate CA. A gate certificate SHALL be refused as a client certificate by the API and by etcd. A stored gate
certificate that still carries ClientAuth SHALL be re-issued.

#### Scenario: A gate named like the server principal
- **GIVEN** a route whose TLS host is `ccattler-server`, and its issued gate certificate and key
- **WHEN** that certificate is presented as a client certificate to the API and to etcd
- **THEN** both refuse the TLS handshake, and the API never yields the principal `node:ccattler-server`
- Test: `TestGateCertificateIsRefusedAsAPIClient` (red first: accepted today),
  `TestGateCertificateIsRefusedByEtcd` (real etcd, `etcd_integration` tag); check `deploy-run:gate-cert-not-client`

#### Scenario: Usages of a new gate certificate
- **GIVEN** any route with TLS
- **WHEN** `RouteTLSController` issues its certificate
- **THEN** the certificate has ExtKeyUsage ServerAuth only
- Test: `TestGateCertificateHasServerAuthOnly`

#### Scenario: An old gate certificate with ClientAuth
- **GIVEN** a stored gate certificate whose usages include ClientAuth (issued by an earlier release)
- **WHEN** `RouteTLSController` reconciles
- **THEN** it re-issues the certificate with ServerAuth only, without waiting for expiry
- Test: `TestRouteTLSReissuesGateCertificateWithClientAuth`

### Requirement: New clusters have three CAs
`cca init` SHALL create three CAs: the cluster CA for node and server identities, the etcd CA for etcd's serving
certificate and its clients, and the gate CA for gate serving certificates. Each CA's key SHALL be written with mode
0600.

#### Scenario: Files written by cca init
- **GIVEN** `cca init --advertise-address 192.0.2.10 --node-id ctrl -o DIR`
- **WHEN** it finishes
- **THEN** DIR holds the CAs `cluster-identity-ca.pem`, `etcd-access-ca.pem` and `gate-https-ca.pem`, each with
  its `-key.pem` at 0600, and these leaf certificates, each with its own key:
  - `etcd-serving-certificate.pem` (CN `etcd-server-ctrl`) and `etcd-peer-certificate.pem` (CN `etcd-peer-ctrl`) from
    the etcd access CA (ServerAuth and ClientAuth);
  - `cca-server-etcd-client-certificate.pem` (CN `etcd-client-cca-server`) and `node-etcd-client-certificate.pem`
    (CN `etcd-client-node-ctrl`) from the etcd access CA (ClientAuth only);
  - `api-server-identity-certificate.pem` (CN `ccattler-server`, ServerAuth and ClientAuth) and
    `node-identity-for-api.pem` (CN `ctrl`, ClientAuth only) from the cluster identity CA.

  No two CAs share a key. Every certificate has O=CCattler and the OU of its purpose, and every file starts with a
  `# CCattler …` description line and still parses (design.md D1a)
- Test: `TestInitWritesThreeCAs`, `TestInitFilesParseWithDescriptionLine`

### Requirement: etcd trusts only the etcd CA, and its clients use etcd client certificates
etcd SHALL trust only the etcd CA for client certificates. The server and every agent SHALL connect to etcd with an
etcd client certificate signed by the etcd CA. A node or server certificate from the cluster CA SHALL be refused by
etcd.

#### Scenario: Node certificate refused by etcd
- **GIVEN** a cluster set up with `cca init` and `setup.sh`
- **WHEN** a client presents `node-identity-for-api.pem` or `api-server-identity-certificate.pem` (cluster identity
  CA) to etcd
- **THEN** etcd refuses both, while `node-etcd-client-certificate.pem` and `cca-server-etcd-client-certificate.pem`
  are accepted
- Test: `TestEtcdRefusesClusterCACertificates` (real etcd, `etcd_integration` tag); check
  `deploy-run:gate-cert-not-client`

#### Scenario: A worker gets its etcd client certificate at enrollment
- **GIVEN** a cluster with an etcd CA and a valid join token
- **WHEN** `cca join` enrolls a worker
- **THEN** the response carries the node identity for the API (cluster identity CA) and the node's own etcd client
  certificate (etcd access CA, ClientAuth only, CommonName `etcd-client-node-<node>`). `cca join` writes
  `node-identity-for-api.pem`, `node-etcd-client-certificate.pem` and `etcd-access-ca.pem` with their keys, and the
  agent's etcd connection uses the etcd client certificate. No two nodes share an etcd client certificate or key
- Test: `TestEnrollmentIssuesEtcdClientCertificate`, `TestJoinWritesEtcdClientCertificate`

### Requirement: etcd's own certificates come from the etcd CA with both usages
etcd's serving and peer certificates SHALL be signed by the etcd CA and carry ServerAuth and ClientAuth (kubeadm
model). etcd dials itself with its serving certificate (etcd issue 9785), and HA peers authenticate to each other with
the peer certificate. (User decision 2026-11-25, option 1a.)

#### Scenario: HA etcd peers use the peer certificate
- **GIVEN** the Ansible HA etcd template
- **WHEN** it is rendered
- **THEN** `--peer-cert-file` is `etcd-peer-certificate.pem`, `--cert-file` is `etcd-serving-certificate.pem`, and
  both trusted-CA flags name `etcd-access-ca.pem`
- Test: `TestAnsibleEtcdClusterTemplateUsesEtcdAccessCA`

### Requirement: Descriptions never authorize
The Subject OU and the description line SHALL be descriptive only. Authorization SHALL use the certificate's usage,
its issuer's trust per listener, and the CommonName; never the OU.

#### Scenario: A client certificate with a misleading OU
- **GIVEN** a cluster identity CA certificate with CN `worker-1` and OU `API server identity`
- **WHEN** it is presented to the API
- **THEN** its principal is `node:worker-1`, the same as without the OU
- Test: `TestMTLSPrincipalIgnoresSubjectUnit`

### Requirement: etcd client identities are distinct but not yet separately authorized
The cca server and every node SHALL each have their own etcd client certificate and key. Until etcd authentication
and roles exist (milestone M181), every accepted etcd client certificate SHALL be documented as having full etcd
access. The security model, the threat model, PKI_GRAPH and the release notes SHALL state that residual trust and
SHALL NOT call the identities separately authorized. (User decision 2026-11-25.)

#### Scenario: Residual trust is written down
- **GIVEN** the M280 documentation changes
- **WHEN** the security model, the threat model and PKI_GRAPH are read
- **THEN** each states that until M181 any accepted etcd client certificate, the server's or a node's, has full etcd
  access, and that a compromised node keeps store access through its own etcd client certificate
- Check: `grep -n "M181" design/security-model.md design/threat-model.md design/graphs/PKI_GRAPH.md` (task 2.6)

### Requirement: The API accepts client identities only from the cluster CA
The API SHALL verify client certificates against the cluster CA only, and map only those to a `node:<CommonName>`
principal. Certificates from the etcd CA or the gate CA SHALL be refused.

#### Scenario: Certificates from other CAs
- **GIVEN** a valid etcd client certificate, a valid gate certificate and a ClientAuth certificate from the gate CA
- **WHEN** each is presented to the API as a client certificate (and the last also to etcd)
- **THEN** the handshake fails and no principal is produced: the CAs are separate, not only the usages (SA4-L1)
- Test: `TestAPIRefusesNonClusterCACertificates`, `TestGateCertificateIsRefusedByEtcd` (etcd_integration), T19 on the
  deploy cluster

### Requirement: cca server records only a verified control-plane node
A given `--control-plane-node-cert` SHALL be verified before the store is created (chain to `--ca`, valid now,
ClientAuth, valid unreserved node ID). If it fails, or its enrollment record is not confirmed, `cca server` SHALL exit
non-zero naming the path and the reason. Without the flag, the server certificate's CN SHALL be recorded on a cluster
from before M280 (a failure only warns), and `ccattler-server` SHALL be warned about, never recorded. (User decision 1.)

#### Scenario: Invalid control-plane node certificate
- **GIVEN** a server started as setup.sh starts it, against a real etcd, with a `--control-plane-node-cert` from a
  foreign CA
- **WHEN** it starts
- **THEN** it exits 1 naming the path and the reason, prints nothing to stdout, and etcd holds the same keys as before
- Test: `TestInvalidNodeCertificateLeavesEtcdUntouched` (etcd_integration),
  `TestServerExitsOnInvalidNodeCertificateBeforeStartup`, `TestControlPlaneNodeCertificateMustBelongToTheCluster`

#### Scenario: Enrollment record not confirmed
- **GIVEN** a verified `--control-plane-node-cert` and a store whose write fails or is not found afterwards
- **WHEN** the server records the node
- **THEN** startup fails naming the path and the reason; without the flag (a cluster from before M280) it only warns
- Test: `TestControlPlaneNodeRecordMustBeConfirmed`

#### Scenario: No control-plane node certificate
- **GIVEN** a server started without `--control-plane-node-cert`
- **WHEN** its certificate names `ccattler-server` (three-CA) or a node (a cluster from before M280)
- **THEN** the first warns and records nothing; the second records that node
- Test: `TestThreeCAServerWithoutNodeCertFlagRecordsNoServerName`, `TestServerRecordsNodeFromServerCertificateOnOldClusters`

### Requirement: cca server checks the kind of each CA file
`cca server` SHALL refuse at startup a CA file given as `--ca`, `--etcd-ca` or `--gate-ca-cert` in which any
certificate's Subject OU names another kind than its flag's, bundles included. A CA from before M280 (no kind) SHALL
be accepted.

#### Scenario: CA file of the wrong kind
- **GIVEN** `etcd-access-ca.pem` as `--gate-ca-cert`, or another CA of the wrong kind for its flag
- **WHEN** the server starts
- **THEN** it is refused, naming the flag, the file and both kinds; CAs from before M280 are accepted
- Test: `TestServerCAKindChecks`

### Requirement: A single-CA cluster warns at every start
When `cca server` starts without an etcd CA or a gate CA, it SHALL log a warning on every start. The warning SHALL say
that one CA is trusted by etcd, the API and the gates, and that the cluster must be re-initialized with `cca init` to
separate them. The cluster SHALL keep working. (User decision 2026-11-25, option 1: no migration.)

#### Scenario: Legacy cluster start
- **GIVEN** a server started with `--ca` but no `--etcd-ca`/gate CA files
- **WHEN** it starts
- **THEN** it logs the single-CA warning once per start, keeps serving, and still issues gate certificates with
  ServerAuth only
- Test: `TestServerWarnsOnSingleCACluster`
