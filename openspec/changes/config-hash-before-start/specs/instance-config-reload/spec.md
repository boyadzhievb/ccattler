# Spec Delta

## Purpose
States when a running instance must be restarted so that it runs with its service's current env vars, config files and secret grants, whatever order those facts are written in.

## ADDED Requirements

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
