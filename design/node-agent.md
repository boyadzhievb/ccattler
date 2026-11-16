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

Container runtime interface (pluggable — SimulatorRuntime, ProcessRuntime, ContainerRuntime/nerdctl, ContainerdRuntime):
```
ensure_container(id, image, resources, environment, mounts, network, desired_state)
```

What a container sees (ContainerdRuntime, spec [container-filesystem](../openspec/specs/container-filesystem/spec.md)): the image's filesystem, plus containerd's default `/proc`, `/dev`, `/dev/shm`, `/sys` mounts and only the paths CCattler adds: `/etc/hosts`, `/etc/resolv.conf`, config files (read-only), secret files (read-only, Flow 27) and volumes. There is no tmpfs on `/run` (removed in M232, `containerBaseSpecOptions`), so the image's `/run` and `/var/run` content is visible as under Docker; writes there go to the container's writable layer, not to memory. CCattler keeps secret values on the node's tmpfs (only empty mount points land in the writable layer), but a secret that an application copies to `/run` or any other writable path is stored on the node's disk until the container is stopped and its snapshot removed.
