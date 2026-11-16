# Design

## Context

- Flow 16 ([design/runtime-invariant-flows.md#16-configsecret-reload](../../../design/runtime-invariant-flows.md)) is the flow this change corrects: "1. resolve config, 2. compute hash, 3. start, 4. store hash". The code does 1, 3, then computes the hash at step 4 (`startInstanceAndPublishState` calls `computeServiceConfigHash` after `runtime.Start`), so the hash describes the store at store time, not the container.
- `reconcileDesiredInstance` reads, in order: secret material (`prepareInstanceSecretMaterial`), pull credentials, image pull, volumes, then `buildRuntimeSpecForInstance` (env, config files) and `buildInstanceSecretMounts` (grants). The pull alone can take many seconds (16 s in run 43906605190).
- `computeServiceConfigHash` returns `""` when the service has no env, config files or secret grants; `startInstanceAndPublishState` stores nothing then, and `detectConfigChange` returns false when no hash is stored.
- Call graph: `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md` (start path, `detectConfigChange`).

## Goals / Non-Goals

**Goals:**
- The stored hash is never newer than the facts the container was built from, so any later or concurrent change restarts the instance.
- A start with no config inputs is tracked like any other.

**Non-Goals:**
- Atomic apply (one etcd transaction): see proposal.
- Changing what the hash covers (env, config files, secret grant facts) or restart mechanics (stop, next cycle starts). M179 (`tenant-scoped-secrets`) later changes the grant value format and requires the hash to ignore that change; it builds on `computeServiceConfigHash` as this change leaves it (when it is taken, not what it covers).

## Decisions

### D1. Compute the hash first, carry it to the store step
`reconcileDesiredInstance` computes `configHashBeforeBuild := computeServiceConfigHash(...)` as its first store read, before secret material, pull and spec building, and passes it to `startInstanceAndPublishState`, which stores it instead of recomputing. Any fact written before the hash read is in both the hash and the spec; one written after it may or may not be in the spec, but is never in the hash, so the next `detectConfigChange` sees a difference and restarts. The worst case is one extra restart (the fact did make it into the spec), never a missed one. Alternative: build the spec and the hash from one store snapshot (`ScanWithRevision` of all inputs) — exact, but the spec reads are spread over secret delivery, credentials and volumes; rejected as a larger change for no correctness gain.

### D2. Hash of no inputs is a real hash
`computeServiceConfigHash` returns the hash of the empty input instead of `""` when the service has no inputs, and `startInstanceAndPublishState` always stores it. `detectConfigChange` keeps returning false when no hash is stored (instances started by an older agent). Callers that used `""` as "nothing to compare" are only these two; checked with grep during apply.

### D3. Proof on the testbed is the WordPress push run
The bug needs a real apply racing a real agent. The agent tests force the race deterministically (the test runtime writes the fact during `Start`); the Deploy Testbed push run with WordPress shows it on a cluster.

## Risks / Trade-offs

Failure cases and what happens:
- **Fact written during the build that did make it into the container**: one unnecessary restart (D1). Acceptable; it only happens when config changes during a start.
- **Transient store read error while hashing**: `computeServiceConfigHash` skips inputs whose scan fails (pre-existing), so the hash taken before the build may lack them → one extra restart once reads work. Same direction as above, never a missed change.
- **Upgrade**: instances started by the old agent keep their (post-start) hash; a change after the upgrade still restarts them. Instances with no stored hash (started with no inputs) are picked up after their next restart.
- **Restart storms**: none new; a restart happens only when the stored hash differs, and the restarted instance stores the hash taken before its own build.
