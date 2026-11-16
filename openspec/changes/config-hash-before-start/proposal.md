# Proposal

## Why

Milestone **M235 — Instance Starts Without Secrets Applied Moments Later** (bug, milestones.md Phase 235). Found by the first WordPress push run (Deploy Testbed 43906605190, 2026-11-16): both WordPress instances ran with no secret files, connected to MariaDB with an empty password and answered HTTP 500, although `cca describe service wordpress` listed all nine secrets.

Verified cause, two parts:
1. `cca apply` writes its facts one `Put` at a time (`lang.WriteCompiledFacts`, `lang/compiler.go`), so an agent can see an instance placed before the service's secret (or env, or config file) facts exist, and builds the container without them.
2. The agent then stores `observed/instance/{id}/config-hash` computed *after* the container started (`startInstanceAndPublishState`, `agent/agent.go`), from a store that by then has the late facts. `detectConfigChange` compares that hash with the store, finds no difference and never restarts the instance. Flow 16 (design/runtime-invariant-flows.md) says the hash is computed before the start; the code drifted from it.

A related hole in the same code: when a service has no env, config files or secrets at start, the hash is empty and is not stored, and `detectConfigChange` skips instances without a stored hash. The first env var, file or secret added later never restarts the instance.

How others handle it: Kubernetes also creates objects one by one (`kubectl apply` is not atomic across objects); the kubelet keeps the pod waiting until the secret volume's source exists and updates mounted secrets when they change. Nomad re-renders templates and restarts the task when a watched value changes. Both make the node side converge on whatever arrives later, instead of relying on an atomic write. CCattler's design already has that mechanism (Flow 16); the fix makes it correct.

## What Changes

- The agent computes the config hash **before** it reads anything to build the container (env, config files, secret grants and material) and stores that hash after the start. A fact that lands while the instance is being built or pulled makes the stored hash differ from the store, so the next cycle restarts the instance with it.
- An instance started with no config inputs gets a stored hash too (the hash of no inputs), so the first env var, config file or secret added later restarts it.
- Deploy Testbed: the WordPress push run passes (WordPress stays in the push profile, as decided in M232).

Out of scope: writing an apply in one etcd transaction. It would not fix this bug on its own (a later `cca secret` grant or a second apply hits the same agent path) and etcd limits a transaction to 128 operations by default, so large configurations would need batching anyway; the agent fix covers every ordering. Can be its own milestone if half-applied configurations show other effects.

## Capabilities

### New Capabilities
- `instance-config-reload`: a running instance reflects its service's current env vars, config files and secret grants; the agent restarts it when they differ from what it was started with.

### Modified Capabilities
(none)

## Impact

- Code: `agent/agent.go` (`reconcileDesiredInstance`, `startInstanceAndPublishState`, `computeServiceConfigHash`).
- Tests: two agent tests (late fact during start; first fact after a start with none); the Deploy Testbed push run with WordPress.
- Docs: Flow 16 in design/runtime-invariant-flows.md (when the hash is taken, empty-input hash), `design/graphs/INSTANCE_LIFECYCLE_GRAPH.md`.
- Behaviour change: instances started under the old code that have no stored hash keep the old behaviour until they restart once; after an upgrade, an instance that started without inputs is not restarted by the upgrade itself.
