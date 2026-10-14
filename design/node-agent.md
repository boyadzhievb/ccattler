# Node Agent

Small binary on each machine with three internal components:

```
          NODE AGENT
              │
   ┌──────────┼──────────┐
   ▼          ▼          ▼
observer   reconciler  reporter
```

- **Observer** — reads Linux state (`/proc`, `/sys`, cgroups, container runtime) to determine what is actually running
- **Reconciler** — compares desired state (from store) with observed state, executes `ensure_*()` operations via containerd/runc. Runs init steps before main workload start (sequential, with retry/backoff). Resolves config and secrets at reconciliation time: reads config facts, obtains authorized secret copies, materializes both into the container as environment variables, config files, or secret files
- **Reporter** — publishes actual state, health, capacity, and events back to the store
- **Telemetry** — collects per-node workload count and per-instance CPU/memory usage, writes as observed facts (powers `cca top`)

The node agent is the materialization boundary for config and secrets:

```
CCattler configuration
        │
        ├── environment variables
        ├── config files
        └── secret files
                │
                ▼
           Node Agent
                │
                ▼
       Runtime Adapter
                │
        ┌───────┼────────┐
        ▼       ▼        ▼
   Process   Container  Simulator
```

This keeps all three runtimes interchangeable — the agent translates declarative config/secret facts into whatever the runtime needs.

Node registration: publishes `node(id)`, `capacity_cpu`, `capacity_memory`, `available_cpu`, `available_memory`, `architecture`, `zone`.

Lease: maintains a TTL lease in etcd. If the agent disappears, lease expires → control plane derives `node_state(node, unreachable)`.

Container runtime interface (pluggable — containerd, CRI-O, Podman, systemd-nspawn):
```
ensure_container(id, image, resources, environment, mounts, network, desired_state)
```
