# Design

## Context

- Flow 16 ([design/runtime-invariant-flows.md#16-configsecret-reload](../../../design/runtime-invariant-flows.md)) is the flow this change extends: `computeServiceConfigHash` (agent/agent.go) hashes `env:`, `file:` and `secret:<grant key>=<mount path>` lines; taken before the build (M235), compared every cycle by `detectConfigChange`, which stops the instance so the next cycle restarts it. `TestServiceConfigHashFormatUnchanged` pins the bytes.
- The agent already builds a `requirementsVersion` from grants, connection references (`connection=<name>@<secret>`) and the pull secret (`loadServiceSecretRequirements`, agent/secret_delivery.go) for secret delivery, but it does not cover a connection's full definition or any secret value, and the hash does not use it.
- Secrets: `SecretStore.PutSecret` (security/secrets.go) seals the plaintext and writes `secrets/<name>`; agents cannot read `secrets/`. Key rotation re-wraps envelopes through the keyring controllers (Flow 30) without touching `PutSecret`. `cca secret set` → `/api/secret` → `PutSecret`.
- Call graphs: SECRETS_GRAPH.md (SecretStore), INSTANCE_LIFECYCLE_GRAPH.md (hash, detectConfigChange).

## Goals / Non-Goals

**Goals:** connections (full definition), pull secret reference and secret value changes restart the consumers through today's path; no secret value or digest in any hash; no restart for services whose inputs did not change.

**Non-Goals:** progressive replacement and convergence reporting (M239); in-place file updates; M179 key layout.

## Decisions

### D1. Value generation, written by the API server
New fact `desired/secret_generation/{name}` = decimal generation. `PutSecret`: read the current value (if any); if absent, write the envelope and the stored generation + 1 (no fact → 1); if present and the new plaintext equals it, write nothing (and keep the generation); if the current value cannot be opened, return an error (a transient keyring problem must not restart every consumer; the operator can delete and set); else write the envelope and generation+1 (no fact → 2), in one store transaction guarded on the previous `secrets/<name>` revision so two concurrent sets cannot share a generation. A lost guard is `ErrSecretChangedConcurrently`, which `/api/secret` answers with HTTP 409 "secret changed concurrently, retry". `DeleteSecret` deletes the envelope only and **keeps the generation as a name-only tombstone** (review): a deleted secret never changes its consumers' hashes, and delete + set always raises the generation, so consumers never keep a replaced (perhaps leaked) credential; gate-key cleanups that delete envelopes directly leave the same harmless tombstone. Keyring rotation writes envelopes directly and never touches the generation. Alternative rejected: a digest of the value (offline guessing of low-entropy secrets by anyone who can read the hash); a keyed HMAC would need a key the agents hold.

### D1a. A changed hash re-fetches secret material (review)
`reconcileDesiredInstance` drops the instance's tmpfs material when a stored `observed/instance/{id}/config-hash` exists and differs from the hash taken before the build. Without it a placed but not running instance (crash loop, exited container, agent restart) would reuse the old values (material is matched by name only) and store the new hash — stuck on the old value.

### D1b. Generations are hidden from the API and admin-only to write (security review)
`desired/secret_generation/` is dropped from `/api/state` scans, gets and watches (api/state_access.go, like `/tls/key`): it would list every secret name and its rotation timing to any team-scoped reader, and no API consumer needs it. It is in the cluster-admin-only write prefixes of `RequiredCapabilityForFactWrite`, so any future apply path stays deny-by-default.

### D2. Hash inputs, appended in a fixed order
After the existing `env:`, `file:`, `secret:` lines (unchanged bytes): `connection:<name>@<mount path>=<definition JSON>` for each connection reference of the service (sorted, mount path from `desired/service/{svc}/connection/{name}`, definition from `desired/connection/{name}`; a missing definition hashes as `connection:<name>@<mount path>=` so its arrival is a change), `pull_secret=<name>` if set, and `generation:<secret>=<n>` for every secret the service uses (grants, connection secrets, pull secret; sorted) **whose generation is greater than 1**. Any read error returns an error (M235: a read error is never a change). Services with none of the new inputs keep their exact hash.

### D3. Agents read the generation
Agents read `desired/` already; confirm the store RBAC for node agents covers `desired/secret_generation/` and add it if not (read only). API users never see secret values through it (a number).

### D4. Tested in the agent with facts only
Agent tests write the facts directly (connection definition, pull secret, generation) and assert a second start (`lateFactRuntime` pattern from M235); the generation rule is tested in `security/` with the memory store and a test codec.

## Risks / Trade-offs

Failure cases and what happens:
- **Upgrade**: services with connections or a pull secret get new hash lines and restart once after the agent upgrade, all their instances at once (until M239). Documented in the release notes text.
- **All replicas restart at once on a rotation** (until M239): a single-instance service has a short outage; documented in Flow 16 and the website secrets page.
- **Concurrent `cca secret set`**: the transaction guard makes one of them retry or fail; generations never repeat.
- **Generation fact missing** (secrets created before this change): treated as generation 1 (no hash line); the first changed value writes 2 and restarts the consumers.
- **Secret deleted** while a service still references it: the generation fact stays (tombstone), so the hash does not change and running instances keep running; a new start waits for secrets (Flow 27) — unchanged. Recreating it raises the generation and restarts the consumers.
- **Plaintext comparison** decrypts the current value on the API server only, inside `PutSecret`, which already holds the codec; nothing new leaves the server.
