# instance-config-reload Specification

## Purpose
States when a running instance must be restarted so that it runs with its service's current env vars, config files and secret grants, whatever order those facts are written in.

## Requirements

### Requirement: Config that arrives while an instance starts reaches it
When a service's env var, config file or secret grant is written after the agent began building an instance's container (for example while `cca apply` is still writing facts, or during the image pull), the agent SHALL restart that instance on a later cycle so that it runs with the new value. The stored `observed/instance/{id}/config-hash` SHALL describe the facts the container was built from, never newer ones.

#### Scenario: Env var written while the instance starts
- **GIVEN** service `web` with env var `DB_HOST=db.old.local` and an instance placed on the node
- **WHEN** `DB_HOST=db.new.local` is written while the agent is starting the container
- **THEN** the instance is restarted on a later cycle and its stored config hash changes
- Test: `TestAgentRestartsWhenConfigArrivesDuringStart`

#### Scenario: Secrets applied moments after the instance is placed
- **GIVEN** the WordPress workload applied on the deploy testbed
- **WHEN** an agent places a WordPress instance before its secret facts are written
- **THEN** WordPress ends up with its secret files, connects to MariaDB and answers through the nginx VIP and the gate
- Check: `deploy-run:push-fast` (push profile whoami + WordPress)

### Requirement: The first config of a service reaches running instances
An instance started while its service had no env vars, config files or secret grants SHALL be restarted when the first of them is written.

#### Scenario: First env var after a start with none
- **GIVEN** service `web` with only an image and a running instance
- **WHEN** env var `DB_HOST=db.local` is written
- **THEN** the instance is restarted on a later cycle with a stored config hash that includes it
- Test: `TestAgentRestartsWhenFirstConfigArrivesAfterStart`

### Requirement: Connection and pull secret changes reach running instances
When a connection a service uses is added, removed or changed (its definition or its password secret), or the service's image pull secret reference changes, the agent SHALL restart the service's running instances on a later cycle so they run with the new value, as for env vars and config files.

#### Scenario: Connection host changes
- **GIVEN** service `web` using connection `db` with host `db-old` and a running instance
- **WHEN** connection `db` is changed to host `db-new`
- **THEN** the instance is restarted on a later cycle
- Test: `TestAgentRestartsWhenConnectionChanges`

#### Scenario: Image pull secret reference changes
- **GIVEN** service `web` with image pull secret `registry-a` and a running instance
- **WHEN** the pull secret reference is changed to `registry-b`
- **THEN** the instance is restarted on a later cycle
- Test: `TestAgentRestartsWhenPullSecretChanges`

### Requirement: A changed secret value restarts the instances that use it
Every secret SHALL carry a value generation that the server raises only when a different plaintext is stored; storing the same value, re-encrypting it or rotating keys SHALL leave the generation unchanged. When the generation of a secret a service uses (grant, connection password or pull secret) changes, the agent SHALL restart the service's running instances on a later cycle. Neither the secret value nor a digest of it SHALL appear in the config hash, its inputs, logs, events or status.

#### Scenario: New value restarts the consumer
- **GIVEN** service `web` granted secret `api-key` and a running instance
- **WHEN** `api-key` is set to a different value
- **THEN** the instance is restarted on a later cycle and its new run reads the new value
- Test: `TestAgentRestartsWhenSecretValueGenerationChanges`, Check: `deploy-run:secret-rotation` (by hand)

#### Scenario: Same value, re-encryption
- **GIVEN** secret `api-key` at generation 2
- **WHEN** it is set again to the same value, or its envelope is re-encrypted by key rotation
- **THEN** its generation stays 2 and no instance restarts
- Test: `TestSecretGenerationRaisedOnlyOnValueChange`

#### Scenario: Upgrade keeps unchanged services running
- **GIVEN** a service with env vars, config files and granted secrets that were never changed after creation, and no connections or pull secret
- **WHEN** the agent is upgraded to this version
- **THEN** the service's config hash is the same as before, so its instances are not restarted
- Test: `TestServiceConfigHashFormatUnchanged`
