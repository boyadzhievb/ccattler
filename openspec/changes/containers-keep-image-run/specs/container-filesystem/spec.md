# Spec Delta

## Purpose
States what a container sees of its image's filesystem: the image's own content, plus only the paths CCattler deliberately mounts (hosts, resolv.conf, config files, secrets, volumes).

## ADDED Requirements

### Requirement: Containers see the image's /run
A main container and an init container SHALL see the image's `/run` directory (and `/var/run` through it) with the content the image ships. CCattler SHALL NOT mount a tmpfs or any other filesystem over `/run`; the only mounts below `/run` SHALL be the secret files the instance is granted.

#### Scenario: Container spec has no mount on /run
- **WHEN** the agent builds the spec for a main container or an init container
- **THEN** the spec contains no mount whose destination is `/run`
- Test: `TestContainerSpecKeepsImageRun`, `TestInitContainerSpecKeepsImageRun`

#### Scenario: Image that needs its /run content
- **GIVEN** the WordPress workload, whose MariaDB image ships `/run/mysqld`
- **WHEN** it is deployed on the deploy testbed
- **THEN** the MariaDB instance stays running with no `instance.exit` events, and WordPress answers through the nginx VIP and the gate
- Check: `deploy-run:wordpress`

### Requirement: Secret files under /run stay read-only bind mounts from node memory
A granted secret SHALL appear in the container at `/run/cca-secrets/<name>` (and at its explicit mount path) as a read-only bind mount of a file on the node's memory filesystem, whether or not the image has a `/run` directory. Secret values SHALL NOT be written to the container's writable layer.

#### Scenario: Secret mount without a /run tmpfs
- **WHEN** the agent builds the spec for a container granted the secret `db-password`
- **THEN** the spec mounts `/run/cca-secrets/db-password` read-only (`ro`, `nosuid`, `nodev`, `noexec`) from the instance's secret directory under `/run/ccattler/secrets/`, and has no mount on `/run`
- Test: `TestContainerSpecKeepsImageRun`

#### Scenario: Secret readable in a deployed container
- **GIVEN** the WordPress workload with its database password granted to MariaDB and WordPress
- **WHEN** it is deployed
- **THEN** WordPress connects to MariaDB with that password (it answers with the installation page)
- Check: `deploy-run:wordpress`
