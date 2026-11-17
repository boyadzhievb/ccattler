# Proposal

## Why

Milestone **M236 — Connection Changes Do Not Reach Running Instances** (bug, milestones.md Phase 236), found in the M235 security audit and verified in the code: the agent's config hash (`computeServiceConfigHash`, Flow 16) covers env vars, config files and secret grant facts only. A running instance is therefore never restarted when:
- a connection it uses is added, removed or changed (`desired/service/{svc}/connection/*`, `desired/connection/{name}`: host, port, database, password secret) — the connection file and password stay stale; a connection that lands after the agent's scan (an apply still writing, M235's race) is never mounted;
- its image pull secret reference changes (`desired/service/{svc}/image_pull_secret`);
- the **value** of a secret it uses changes (`cca secret set` with a new password) — the instance keeps the old value until something else restarts it, although the control plane considers the new value applied. Flow 16 said "secrets — any change triggers restart", which was not true.

User decision (2026-11-17, option 1 with option A): a changed secret value restarts the instances that use it, because secrets are delivered as files and env vars read at start, so updating files in place would silently do nothing for most applications. Progressive replacement within the rollout policy for every config change is split off to **M239**; M236 makes the hash cover the missing inputs and keeps today's restart path (each agent restarts its instances when their hash differs).

How others do it: Nomad re-renders templates and restarts the task when a referenced secret changes; Kubernetes updates mounted secret files in place but never restarts for env-var secrets, so rotations need a manual rollout. CCattler takes Nomad's rule: the effective configuration includes secret values.

## What Changes

- The config hash also covers, per service: each connection the service uses with its full definition, the image pull secret reference, and the **value generation** of every secret the service uses (grants, connection passwords, pull secret).
- Each secret gets a **value generation**: a number the API server raises only when `cca secret set` stores a different plaintext (it compares with the current value). Creating a secret starts it at 1; setting the same value, re-encryption and key rotation leave it unchanged. The hash contains the generation, never the value or a digest of it, so nothing that reads the hash can test password guesses.
- A secret at generation 1 adds nothing to the hash, so services whose secrets were never changed keep their hash across the upgrade.
- Docs: Flow 16 states exactly what the hash covers and that a secret value change restarts its consumers — all instances of the service at once until M239, so a single-instance service has a short outage.

Out of scope: progressive replacement within `maxUnavailable` and convergence reporting for config changes (M239); updating secret files in place; the tenant-scoped secret keys of M179 (the generation key follows the secret key when M179 lands).

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `instance-config-reload`: connections, the image pull secret and secret value changes also restart the instances that use them.

## Impact

- Code: `security/secrets.go` (`PutSecret` raises the generation only on a changed value), `types/keys.go` (generation key), `agent/agent.go` (`computeServiceConfigHash` inputs), store RBAC if agents cannot read the new key yet.
- Tests: agent tests (connection change, pull secret change, secret generation change → restart; unchanged hash for services without new inputs); security test for the generation rule (same value, new value, re-encryption); testbed: a secret's value changed with `cca secret set` reaches the running instance.
- Docs: Flow 16, design/graphs/SECRETS_GRAPH.md (generation key), INSTANCE_LIFECYCLE_GRAPH.md, design/etcd-schema.md, website secrets page (rotation restarts consumers).
- Upgrade: services with connections or a pull secret restart once after the agent upgrade (new hash inputs), all replicas at once (M239).
