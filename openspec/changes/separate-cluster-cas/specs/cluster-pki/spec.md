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
certificate and its clients, and the gate CA for gate serving certificates. The etcd serving certificate SHALL be
signed by the etcd CA and carry ServerAuth only. Each CA's key SHALL be written with mode 0600.

#### Scenario: Files written by cca init
- **GIVEN** `cca init --advertise-address 192.0.2.10 --node-id ctrl -o DIR`
- **WHEN** it finishes
- **THEN** DIR holds `ca.pem`, `etcd-ca.pem` and `gate-ca.pem`, each with its `-key.pem` at 0600. `etcd.pem` is
  signed by the etcd CA (ServerAuth only), `etcd-client.pem` by the etcd CA (ClientAuth only), and `node.pem` by
  the cluster CA. No two CAs share a key
- Test: `TestInitWritesThreeCAs`

### Requirement: etcd trusts only the etcd CA, and its clients use etcd client certificates
etcd SHALL trust only the etcd CA for client certificates. The server and every agent SHALL connect to etcd with an
etcd client certificate signed by the etcd CA. A node or server certificate from the cluster CA SHALL be refused by
etcd.

#### Scenario: Node certificate refused by etcd
- **GIVEN** a cluster set up with `cca init` and `setup.sh`
- **WHEN** a client presents `node.pem` (cluster CA) to etcd
- **THEN** etcd refuses it, while `etcd-client.pem` is accepted
- Test: `TestEtcdRefusesClusterCACertificates` (real etcd, `etcd_integration` tag); check
  `deploy-run:gate-cert-not-client`

#### Scenario: A worker gets its etcd client certificate at enrollment
- **GIVEN** a cluster with an etcd CA and a valid join token
- **WHEN** `cca join` enrolls a worker
- **THEN** the response carries the node certificate (cluster CA) and an etcd client certificate (etcd CA, ClientAuth
  only, CommonName `etcd-client-<node>`). `cca join` writes both, and the agent's etcd connection uses the etcd
  client certificate
- Test: `TestEnrollmentIssuesEtcdClientCertificate`, `TestJoinWritesEtcdClientCertificate`

### Requirement: The API accepts client identities only from the cluster CA
The API SHALL verify client certificates against the cluster CA only, and map only those to a `node:<CommonName>`
principal. Certificates from the etcd CA or the gate CA SHALL be refused.

#### Scenario: Certificates from other CAs
- **GIVEN** a valid etcd client certificate and a valid gate certificate
- **WHEN** each is presented to the API as a client certificate
- **THEN** the handshake fails and no principal is produced
- Test: `TestAPIRefusesNonClusterCACertificates`

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
