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
New fact `desired/secret_generation/{name}` = decimal generation. `PutSecret`: read the current value (if any); if absent, write the envelope and generation 1; if present and the new plaintext equals it, write nothing (and keep the generation); else write the envelope and generation+1, in one store transaction guarded on the previous `secrets/<name>` revision so two concurrent sets cannot share a generation. `DeleteSecret` removes the generation too. Keyring rotation writes envelopes directly and never touches the generation. Alternative rejected: a digest of the value (offline guessing of low-entropy secrets by anyone who can read the hash); a keyed HMAC would need a key the agents hold.

### D2. Hash inputs, appended in a fixed order
After the existing `env:`, `file:`, `secret:` lines (unchanged bytes): `connection:<name>=<definition JSON>` for each connection reference of the service (sorted, definition from `desired/connection/{name}`; a missing definition hashes as `connection:<name>=` so its arrival is a change), `pull_secret=<name>` if set, and `generation:<secret>=<n>` for every secret the service uses (grants, connection secrets, pull secret; sorted) **whose generation is greater than 1**. Any read error returns an error (M235: a read error is never a change). Services with none of the new inputs keep their exact hash.

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
- **Secret deleted** while a service still references it: the generation fact disappears (no hash line); delivery already keeps such instances waiting for secrets (Flow 27) — unchanged.
- **Plaintext comparison** decrypts the current value on the API server only, inside `PutSecret`, which already holds the codec; nothing new leaves the server.
