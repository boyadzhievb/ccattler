# Key Ownership Matrix

Every fact store key prefix has exactly one authoritative writer. This matrix is the formal reference for the write domain enforcement implemented in M40 (Phase 43). The runner validates controller changes against these declarations at commit time.

## Controller Write Domains

Each controller may only write keys under its declared output prefixes. The runner drops any change outside the controller's domain.

| Controller | Name | Output Prefixes (write domain) |
|-----------|------|-------------------------------|
| IntentResolverController | `intent-resolver` | `effective/service/` |
| InstanceController | `instance` | `observed/instance/` |
| Scheduler | `scheduler` | `placement/` |
| EndpointController | `endpoint` | `endpoint/` |
| FailureController | `failure` | `observed/instance/`, `derived/instance/` |
| NodeFailureController | `node-failure` | `observed/node/`, `observed/instance/` |
| NetworkController | `network` | `network/vip/`, `network/dns/` |
| AutoscaleController | `autoscale` | `intent/autoscaler/` |
| RolloutController | `rollout` | `observed/instance/`, `derived/service/`, `desired/service/` |
| StorageController | `storage` | `observed/volume/` |
| WarmZeroController | `warm-zero` | `derived/service/` |
| InitController | `init` | `derived/instance/` |
| CredentialBrokerController | `credential-broker` | `derived/credential/` |
| NodeLifecycleController | `cloud-node-lifecycle` | `observed/cloud/instance/`, `observed/node/` |
| CloudLoadBalancerController | `cloud-loadbalancer` | `observed/cloud/loadbalancer/` |
| CloudRouteController | `cloud-routes` | `observed/cloud/route/` |

## Non-Controller Writers

| Writer | Output Prefixes | Notes |
|--------|----------------|-------|
| `cca apply` (DSL compiler) | `desired/service/`, `desired/volume/`, `intent/user/` | User-declared desired state |
| Node Agent (observer) | `observed/instance/`, `observed/node/`, `lease/node/` | Runtime observations |
| Node Agent (telemetry) | `observed/instance/{id}/cpu`, `observed/instance/{id}/memory` | Resource telemetry |
| Node Agent (init steps) | `observed/instance/{id}/init/step/` | Init step execution results |
| Node Agent (probes) | `observed/instance/{id}/probe/` | Health probe observations |
| Node Agent (host port) | `observed/instance/{id}/hostport` | Docker host port mapping |
| Node Agent (address) | `observed/node/{id}/address` | Node advertise address |
| API server (scale) | `desired/service/{svc}/instances` | `cca scale` command |
| API server (metric) | `observed/metric/service/{svc}/{metric}` | `cca metric set` |
| API server (activate) | `derived/service/{svc}/activation/state` | Activation webhook |
| Proxy (last request) | `observed/service/{svc}/last_request_time` | Warm-zero tracking |
| Proxy (activation) | `derived/service/{svc}/activation/state` | Cold-start activation |

## Shared Prefix Overlaps

Some prefixes have multiple writers. These are intentional and documented:

| Prefix | Writers | Reason |
|--------|---------|--------|
| `observed/instance/` | InstanceController, FailureController, NodeFailureController, RolloutController, Node Agent | Instance lifecycle has multiple authorities: creation (instance), failure replacement (failure), node failure (node-failure), rollout stopping (rollout), runtime observation (agent) |
| `observed/node/` | NodeFailureController, NodeLifecycleController, Node Agent | Node state has three writers: agent publishes capacity/address, node-failure marks unreachable, cloud-lifecycle marks cordoned |
| `derived/service/` | WarmZeroController, RolloutController | Disjoint sub-prefixes: warm-zero writes `activation/`, rollout writes `rollout/` |
| `derived/instance/` | FailureController, InitController | Disjoint sub-prefixes: failure writes `drain_since`, init writes `init/phase` |
| `desired/service/` | DSL compiler, RolloutController, API server | Rollout writes only `desired/service/{svc}/image` during rollback — this is an intentional cross-layer write |

## Key Prefix Taxonomy

```
desired/                    — user-declared desired state (DSL compiler, API)
  service/{svc}/            — service definitions (image, instances, expose, resources, health, etc.)
  volume/{vol}/             — volume declarations (size, persistent, replicas)
  cloud_identity/{name}/    — cloud identity definitions
  credential_broker/        — credential broker configuration
  cluster/                  — cluster-level desired state

effective/                  — resolved effective state after intent layer merge
  service/{svc}/            — effective instance count, resources

observed/                   — actual state from runtime observation
  instance/{id}/            — instance state, service, node, image, ip, health, probes, telemetry
  node/{id}/                — node state, capacity, available, architecture, zone, address, labels
  service/{svc}/            — service-level observations (last_request_time)
  volume/{vol}/             — volume state, size, replicas, snapshots
  metric/service/{svc}/     — autoscaling metric values
  cloud/instance/{id}/      — cloud provider instance state
  cloud/loadbalancer/{svc}/ — cloud load balancer state
  cloud/route/{cidr}        — cloud VPC route state

derived/                    — controller-computed state (not directly observed)
  service/{svc}/            — rollout state, activation state
  instance/{id}/            — init phase, drain_since
  credential/{inst}/{id}/   — credential lifecycle state

intent/                     — per-layer intent facts
  user/service/{svc}/       — user intent (min, max, instances)
  autoscaler/service/{svc}/ — autoscaler recommendations

placement/                  — scheduler placement decisions
  instance/{id}             — instance-to-node assignment

endpoint/                   — derived network endpoints
  service/{svc}/{inst}/{p}  — reachable address per instance per port

network/                    — networking state
  vip/service/{svc}         — virtual IP assignments
  dns/{svc}                 — DNS name-to-VIP mappings
  node/{id}/subnet          — per-node subnet assignments
  allocation/{inst}         — per-instance IP allocations

lease/                      — heartbeat leases
  node/{id}                 — node lease timestamp

credentials/                — encrypted credential store (AES-256-GCM)
```

## Enforcement

Write domain enforcement is implemented in `controllers/runner.go:enforceWriteDomain()`. The authoritative declaration is `controllers/topological_sort.go:controllerOutputPrefixes()`. Changes outside the declared domain are dropped and logged with a `ccattler_write_domain_violations_total` Prometheus counter.

Custom controllers (via the SDK) are not subject to write domain enforcement — they have no entry in the output prefixes map, so all their changes pass through. This is intentional: custom controllers define their own write domains at registration time.
