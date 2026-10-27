# CCattler Controller Data Flow Graph

Machine-readable reference for controller-to-controller data flow.
Purpose: trace how facts cascade through the control plane without
reading controller source files.

## Controller Inventory

### Registration Order (coreControllers + extras)

```
coreControllers():
  1. instance          (controllers/instance.go)
  2. scheduler         (scheduler/scheduler.go)
  3. endpoint          (controllers/endpoint.go)
  4. failure           (controllers/failure.go)
  5. autoscale         (controllers/autoscale.go)
  6. intent-resolver   (controllers/intent.go)
  7. rollout           (controllers/rollout.go)
  8. init              (controllers/init.go)
  9. stateful-dns      (controllers/stateful_dns.go)
  10. stateful-volume  (controllers/stateful_volume.go)
  11. route            (controllers/route.go)
  12. eviction         (controllers/eviction.go)
  13. job              (controllers/job.go)
  14. cron             (controllers/cronjob.go)

startControllersWithLeaderElection() adds:
  15. node-failure     (controllers/nodefailure.go)
  16. drain            (controllers/drain.go)
  17. network          (controllers/network.go)
  18. network-policy   (controllers/network_policy.go)
  19. warm-zero        (controllers/warmzero.go)

If --cloud-provider is set:
  20. cloud-node-lifecycle  (controllers/cloud_node_lifecycle.go)
  21. cloud-loadbalancer    (controllers/cloud_loadbalancer.go)
  22. cloud-routes          (controllers/cloud_routes.go)

Non-controller components (same runner):
  - EventProjector         (controllers/event_projector.go)  — watch-based event log
  - AuthController         (controllers/auth.go)              — rebuilds in-memory authorizer
  - MetricsCollector       (controllers/metrics.go)           — Prometheus metrics
  - Bulkhead               (controllers/bulkhead.go)          — concurrency limiter
```

### Timing Constants (controllers/runner.go)

```
Debounce interval:       50ms   (quiet period between cycles per controller)
Resync interval:         30s    (forced full reconciliation)
CAS retry base delay:    10ms   (exponential backoff)
CAS retry max delay:     500ms
Max transaction changes: 60     (etcd 128-op limit ÷ 2 items/change)
Max CAS retries:         5
```

## Controller Watch Prefixes and Output Keys

### intent-resolver
```
Watches:
  intent/user/service/              ← user-set instance counts
  intent/autoscaler/service/        ← autoscaler-computed counts
  desired/service/                  ← scale policy bounds, quota
  effective/service/                ← current effective counts (self)

Writes:
  effective/service/{name}/instances            ← resolved instance count
  effective/service/{name}/resources/cpu        ← resolved CPU
  effective/service/{name}/resources/memory     ← resolved memory
```

### instance
```
Watches:
  effective/service/                ← from intent-resolver
  observed/instance/                ← instance state, service membership
  derived/instance/                 ← controller_stopped, node_failure marks
  derived/service/                  ← rollout surge allowance

Writes:
  observed/instance/{id}                        ← existence marker
  observed/instance/{id}/service                ← owning service name
  observed/instance/{id}/state                  ← "pending" (new) or "stopped" (scale down)
  observed/instance/{id}/ordinal                ← stateful ordinal
  DELETE derived/instance/{id}/controller_stopped  ← clear on re-create
  DELETE derived/instance/{id}/node_failure         ← clear on re-create
```

### scheduler
```
Watches:
  placement/instance/               ← current placements (self)
  observed/node/                    ← node state, heartbeat, resources
  observed/instance/                ← instance state, service membership
  desired/service/                  ← placement constraints, resources
  desired/group/                    ← co-scheduling groups
  derived/scheduler/unplaced/       ← own unplaced-demand facts

Writes:
  placement/instance/{id}                       ← assigned node ID
  placement/node/{nodeID}/{id}                  ← per-node placement index
  derived/scheduler/unplaced/{id}/reason        ← why unplaceable
  derived/scheduler/unplaced/{id}/requirements  ← resource requirements JSON
```

### endpoint
```
Watches:
  observed/instance/                ← state, IP, service, host_port
  observed/node/                    ← advertise address
  endpoint/service/                 ← existing endpoints (self)
  desired/service/                  ← port configuration
  derived/instance/                 ← readiness probe results

Writes:
  endpoint/service/{svc}/{instanceID}/{port}    ← endpoint entry (IP:port)
  DELETE endpoint/service/{svc}/{id}/{port}      ← stale endpoint removal
```

### failure
```
Watches:
  observed/instance/                ← state, health, liveness
  derived/instance/                 ← drain-readiness, drain-since
  derived/crash-loop/               ← crash loop counters

Writes:
  derived/instance/{id}/controller_stopped      ← mark instance for stop
  derived/instance/{id}/drain_readiness         ← liveness drain timestamp
  derived/instance/{id}/drain_since             ← when drain started
  derived/crash-loop/{service}/counter          ← crash loop tracking
  observed/instance/{replacementID}             ← create replacement
  observed/instance/{replacementID}/service     ← replacement service
  observed/instance/{replacementID}/state       ← "pending" for replacement
  observed/instance/{id}/state                  ← "stopped" for old instance
```

### node-failure
```
Watches:
  lease/node/                       ← heartbeat timestamps
  observed/node/                    ← current node state
  observed/instance/                ← instance state on failed nodes
  derived/instance/                 ← existing failure marks
  placement/instance/               ← instance-to-node mapping

Writes:
  observed/node/{nodeID}/state                  ← "unreachable"
  derived/instance/{id}/node_failure            ← mark instances on dead node
  DELETE derived/instance/{id}/node_failure      ← clear when node returns
```

### drain
```
Watches:
  observed/node/                    ← node state (draining)
  placement/instance/               ← instance placements
  observed/instance/                ← instance states
  derived/instance/                 ← instance markers
  desired/service/                  ← disruption budgets
  derived/node/                     ← drain-complete markers

Writes:
  derived/node/{nodeID}/drain/evict/{instanceID}  ← eviction marker
  derived/node/{nodeID}/drain/complete             ← drain done marker
```

### network
```
Watches:
  endpoint/service/                 ← from endpoint controller
  desired/service/                  ← port configuration
  network/vip/service/              ← existing VIP allocations (self)
  network/dns/                      ← existing DNS mappings (self)

Writes:
  network/vip/service/{name}                    ← VIP address
  network/vip/service/{name}/port               ← VIP port
  network/dns/{name}                            ← service-name → VIP mapping
```

### stateful-dns
```
Watches:
  effective/service/                ← from intent-resolver (stateful flag)
  observed/instance/                ← instance IP, state
  network/dns/instance/             ← existing per-instance DNS (self)

Writes:
  network/dns/instance/{instanceID}             ← per-instance DNS entry
```

### init
```
Watches:
  desired/service/                  ← init step definitions
  observed/instance/                ← init step observations
  derived/instance/                 ← current init phase

Writes:
  derived/instance/{id}/init_phase              ← pending/running/complete/failed
```

### rollout
```
Watches:
  desired/service/                  ← desired image, update policy
  observed/instance/                ← running image per instance
  derived/instance/                 ← instance markers
  derived/service/                  ← rollout state (self)
  placement/instance/               ← instance placements

Writes:
  derived/service/{name}/rollout/state          ← rolling/complete/rollback
  derived/service/{name}/rollout/image          ← target image
  derived/service/{name}/rollout/revision       ← revision counter
  derived/service/{name}/rollout/history/{rev}  ← rollout history entry
  derived/service/{name}/rollout/surge          ← surge allowance
  observed/instance/{id}/state                  ← "stopped" (old instances)
  desired/service/{name}/image                  ← rollback image
```

### autoscale
```
Watches:
  desired/service/                  ← scale policy, targets
  observed/metric/                  ← metric values
  observed/instance/                ← active instance count
  derived/instance/                 ← instance markers
  intent/autoscaler/service/        ← current autoscaler intent (self)
  derived/service/                  ← activation state (warm-zero)

Writes:
  intent/autoscaler/service/{name}/instances    ← recommended count
```

### warm-zero
```
Watches:
  desired/service/                  ← warm-zero config
  observed/service/                 ← last request time
  derived/service/                  ← activation state (self)
  observed/instance/                ← running instance count
  endpoint/service/                 ← endpoint count

Writes:
  derived/service/{name}/activation_state       ← active/inactive/activating
```

### eviction
```
Watches:
  observed/node/                    ← memory capacity, available
  observed/instance/                ← instance-to-node mapping
  desired/service/                  ← QoS class, memory requests
  derived/service/                  ← derived service facts

Writes:
  derived/node/{nodeID}/pressure                ← memory pressure flag
  derived/instance/{id}/controller_stopped      ← evict low-QoS instances
```

### route
```
Watches:
  desired/route/                    ← route definitions
  endpoint/service/                 ← service backends
  derived/route/                    ← existing derived backends (self)

Writes:
  derived/route/{name}/backends                 ← comma-separated backend IPs
```

### job
```
Watches:
  desired/job/                      ← job definitions
  derived/job/                      ← job status (self)
  observed/instance/                ← job instance states
  derived/instance/                 ← instance markers

Writes:
  derived/job/{name}/status                     ← pending/running/succeeded/failed
  derived/job/{name}/instance/{id}/owner        ← job-to-instance binding
  observed/instance/{id}                        ← create job instance
  observed/instance/{id}/service                ← job service name
  observed/instance/{id}/state                  ← "pending"
```

### cron
```
Watches:
  desired/cronjob/                  ← cron schedule definitions
  derived/cronjob/                  ← last-scheduled time (self)
  desired/job/                      ← existing jobs
  derived/job/                      ← job status

Writes:
  derived/cronjob/{name}/last_scheduled         ← timestamp
  derived/cronjob/{name}/last_status            ← last job result
  desired/job/{name}                            ← create job from schedule
  desired/job/{name}/image                      ← job image
  desired/job/{name}/completions                ← job completions
  desired/job/{name}/parallelism                ← job parallelism
  desired/job/{name}/backoff_limit              ← job backoff limit
  desired/job/{name}/command                    ← job command
```

### storage
```
Watches:
  desired/volume/                   ← volume declarations
  observed/volume/                  ← volume state
  observed/node/                    ← node state (for detach)
  derived/volume/                   ← pending operations (self)

Writes:
  observed/volume/{name}                        ← existence marker
  observed/volume/{name}/state                  ← available/migrating
  observed/volume/{name}/size                   ← current size
  observed/volume/{name}/replica_count          ← replica count
  observed/volume/{name}/replica_state          ← syncing
  observed/volume/{name}/migration_source       ← source node for migration
  derived/volume/{name}/pending_resize          ← resize request
  derived/volume/{name}/pending_snapshot        ← snapshot request
  DELETE observed/volume/{name}/*               ← garbage collect
```

### stateful-volume
```
Watches:
  effective/service/                ← from intent-resolver (stateful, count)
  desired/service/                  ← volume mount definitions
  desired/volume/                   ← existing per-ordinal volumes

Writes:
  desired/volume/{name}                         ← per-ordinal volume
  desired/volume/{name}/size                    ← volume size
  desired/volume/{name}/persistent              ← persistence flag
```

### network-policy
```
Watches:
  policy/network/                   ← policy rule definitions
  endpoint/service/                 ← service endpoints (for IP resolution)
  placement/instance/               ← instance placements
  derived/network/rule/             ← existing compiled rules (self)

Writes:
  derived/network/rule/{nodeID}/{index}         ← compiled IP firewall rules
```

### credential-broker
```
Watches:
  desired/cloud-identity/           ← identity bindings
  desired/credential-broker/        ← broker config
  derived/credential/               ← credential state (self)
  observed/instance/                ← running instances
  desired/service/                  ← service-to-identity bindings

Writes:
  derived/credential/{name}/*                   ← issued credentials
```

### cluster-autoscale
```
Watches:
  observed/instance/                ← instance state
  derived/instance/                 ← instance markers
  observed/node/                    ← node capacity
  placement/instance/               ← placements
  desired/service/                  ← resource requirements
  derived/scheduler/unplaced/       ← from scheduler
  derived/capacity/request/         ← existing requests (self)

Writes:
  derived/capacity/request/{id}/state           ← pending/removing
  derived/capacity/request/{id}/requirements    ← resource needs JSON
  derived/capacity/request/{id}/reason          ← human reason
  derived/capacity/request/{id}/node_id         ← target node for removal
```

### auth
```
Watches:
  auth/role/                        ← role definitions
  auth/grant/                       ← grant bindings
  auth/group/                       ← group definitions
  auth/policy/                      ← ABAC policies

Writes:
  (none — side effect is rebuilding in-memory RBACAuthorizer)
```

### cloud-node-lifecycle (if cloud provider enabled)
```
Watches:
  observed/node/                    ← node-to-instance mapping
  observed/cloud/instance/          ← cloud instance state

Writes:
  observed/node/{nodeID}/state                  ← "draining" for terminated
  observed/cloud/instance/{id}/state            ← cloud instance state
```

### cloud-loadbalancer (if cloud provider enabled)
```
Watches:
  desired/service/                  ← external service config
  endpoint/service/                 ← service backends
  observed/node/                    ← node addresses
  observed/cloud/loadbalancer/      ← current LB state
  derived/cloud/loadbalancer/       ← derived LB state

Writes:
  observed/cloud/loadbalancer/{name}/*          ← LB state
  derived/cloud/loadbalancer/{name}/*           ← LB config
  (PostCommit: cloud API calls to ensure/delete LBs)
```

### cloud-routes (if cloud provider enabled)
```
Watches:
  network/node/                     ← node subnet assignments
  observed/node/                    ← node addresses
  observed/cloud/route/             ← current VPC routes
  derived/cloud/route/              ← derived route state

Writes:
  observed/cloud/route/{cidr}/*                 ← route state
  derived/cloud/route/{cidr}/*                  ← route config
  (PostCommit: cloud API calls to ensure/delete VPC routes)
```

## Authoritative Output Prefix Declaration

From `controllerOutputPrefixes()` in topological_sort.go. This is used by
`buildControllerDependencyGraph()` to determine controller ordering.

```
intent-resolver      → effective/service/
instance             → observed/instance/
scheduler            → placement/, derived/scheduler/unplaced/
endpoint             → endpoint/
failure              → observed/instance/, derived/instance/, derived/crash-loop/
node-failure         → observed/node/, derived/instance/
network              → network/vip/, network/dns/
autoscale            → intent/autoscaler/
rollout              → observed/instance/, derived/service/, desired/service/
storage              → observed/volume/, derived/volume/
warm-zero            → derived/service/
init                 → derived/instance/
credential-broker    → derived/credential/
cloud-node-lifecycle → observed/cloud/instance/, observed/node/
cloud-loadbalancer   → observed/cloud/loadbalancer/, derived/cloud/loadbalancer/
cloud-routes         → observed/cloud/route/, derived/cloud/route/
drain                → derived/node/
cluster-autoscale    → derived/capacity/request/
route                → derived/route/
auth                 → (none)
```

### Missing from controllerOutputPrefixes (controllers that write but are not declared)

```
eviction        writes: derived/node/, derived/instance/
network-policy  writes: derived/network/rule/
job             writes: observed/instance/, derived/job/
cron            writes: desired/job/, derived/cronjob/
stateful-dns    writes: network/dns/instance/
stateful-volume writes: desired/volume/
```

These controllers participate in the dependency graph implicitly through
their Watch prefixes, but their outputs are not declared — the topological
sort cannot detect controllers that depend on their output.

## Controller Dependency Graph

Derived from `buildControllerDependencyGraph()`: if controller B watches a
prefix that controller A is declared to output, then B depends on A.

### Adjacency List (controller → depends on)

```
intent-resolver      → autoscale (watches intent/autoscaler/)
instance             → intent-resolver (watches effective/service/)
                     → failure (watches derived/instance/)
                     → node-failure (watches derived/instance/)
                     → init (watches derived/instance/)
                     → rollout (watches derived/service/)
scheduler            → instance (watches observed/instance/)
                     → node-failure (watches observed/node/)
                     → cloud-node-lifecycle (watches observed/node/)
endpoint             → instance (watches observed/instance/)
                     → failure (watches derived/instance/)
                     → node-failure (watches observed/node/, derived/instance/)
                     → cloud-node-lifecycle (watches observed/node/)
failure              → instance (watches observed/instance/)
                     → init (watches derived/instance/)
                     → node-failure (watches derived/instance/)
node-failure         → instance (watches observed/instance/)
                     → scheduler (watches placement/)
drain                → instance (watches observed/instance/)
                     → scheduler (watches placement/)
                     → node-failure (watches observed/node/)
                     → cloud-node-lifecycle (watches observed/node/)
network              → endpoint (watches endpoint/)
stateful-dns         → intent-resolver (watches effective/service/)
                     → instance (watches observed/instance/)
init                 → instance (watches observed/instance/)
rollout              → instance (watches observed/instance/)
                     → scheduler (watches placement/)
autoscale            → instance (watches observed/instance/)
                     → failure (watches derived/instance/)
                     → node-failure (watches derived/instance/)
                     → init (watches derived/instance/)
                     → warm-zero (watches derived/service/)
                     → rollout (watches derived/service/)
warm-zero            → instance (watches observed/instance/)
                     → endpoint (watches endpoint/)
                     → rollout (watches derived/service/)
eviction             → instance (watches observed/instance/)
                     → node-failure (watches observed/node/)
                     → cloud-node-lifecycle (watches observed/node/)
                     → rollout (watches derived/service/)
route                → endpoint (watches endpoint/)
network-policy       → endpoint (watches endpoint/)
                     → scheduler (watches placement/)
cluster-autoscale    → instance (watches observed/instance/)
                     → failure (watches derived/instance/)
                     → node-failure (watches observed/node/, derived/instance/)
                     → init (watches derived/instance/)
                     → scheduler (watches placement/, derived/scheduler/unplaced/)
                     → cloud-node-lifecycle (watches observed/node/)
job                  → instance (watches observed/instance/)
                     → failure (watches derived/instance/)
                     → node-failure (watches derived/instance/)
                     → init (watches derived/instance/)
cron                 → (no declared dependency — watches desired/*)
credential-broker    → instance (watches observed/instance/)
cloud-loadbalancer   → endpoint (watches endpoint/)
                     → node-failure (watches observed/node/)
                     → cloud-node-lifecycle (watches observed/node/)
cloud-routes         → node-failure (watches observed/node/)
                     → cloud-node-lifecycle (watches observed/node/)
```

### DAG (ASCII, simplified to major chains)

```
                     ┌──────────────┐
                     │  cca apply   │  (user writes desired/*)
                     └──────┬───────┘
                            │
                            ▼
                     ┌──────────────┐
                     │  autoscale   │  intent/autoscaler/ →
                     └──────┬───────┘
                            │
                            ▼
                  ┌──────────────────┐
                  │ intent-resolver  │  effective/service/ →
                  └────────┬─────────┘
                           │
              ┌────────────┼────────────┐
              │            │            │
              ▼            ▼            ▼
     ┌────────────┐ ┌────────────┐ ┌────────────────┐
     │  instance  │ │stateful-vol│ │  stateful-dns  │
     └─────┬──────┘ └────────────┘ └────────────────┘
           │
           │ observed/instance/ →
           │
     ┌─────┼──────────┬────────────┬──────────────┐
     │     │          │            │              │
     ▼     ▼          ▼            ▼              ▼
  ┌─────┐ ┌────┐ ┌────────┐ ┌──────────┐  ┌──────────┐
  │sched│ │init│ │rollout │ │ failure  │  │node-fail │
  └──┬──┘ └────┘ └────────┘ └──────────┘  └─────┬────┘
     │                                           │
     │ placement/ →                    observed/node/ →
     │                                 derived/instance/ →
     │     ┌──────────────┐                      │
     └────►│   endpoint   │◄─────────────────────┘
           └──────┬───────┘
                  │
                  │ endpoint/service/ →
                  │
     ┌────────────┼────────────┬──────────────┐
     │            │            │              │
     ▼            ▼            ▼              ▼
  ┌───────┐ ┌────────┐ ┌──────────┐  ┌──────────────┐
  │network│ │ route  │ │net-policy│  │cloud-loadbal │
  └───┬───┘ └────────┘ └──────────┘  └──────────────┘
      │
      │ network/vip/, network/dns/ →
      │
      ▼
  ┌────────────────────────────────┐
  │ DNS Server (reads network/dns) │
  │ Data Plane (reads endpoint/)   │
  └────────────────────────────────┘
```

### Side Chains (not on the main deploy path)

```
node-failure ──► drain ──► (evicts instances → instance restarts elsewhere)

cloud-node-lifecycle ──► node-failure (marks nodes unreachable)

autoscale ──► intent-resolver ──► instance (scales up/down)

cron ──► job ──► instance (creates job instances)

warm-zero ──► autoscale (gates scaling to zero)

eviction ──► (stops low-QoS instances on memory pressure)

cluster-autoscale ──► (requests cloud capacity when scheduler can't place)
```

## Reconcile Trigger Chain (fresh deploy)

When a user runs `cca apply airflow.cca`, this is the cascade of events:

```
T+0ms     User writes desired/service/*, desired/connection/*
          ↓ watch events fire

T+50ms    intent-resolver: reads desired/ + intent/
          → writes effective/service/{name}/instances
          ↓ watch event on effective/service/

T+100ms   instance: reads effective/ + observed/instance/
          → writes observed/instance/{id}/state=pending (N new instances)
          ↓ watch event on observed/instance/

T+150ms   scheduler: reads observed/instance/ + observed/node/ + placement/
          → writes placement/node/{nodeID}/{id} (places each instance)
          ↓ watch event on placement/node/

T+150ms   init: reads observed/instance/ + desired/service/{name}/init/
          → writes derived/instance/{id}/init_phase=pending
          ↓ Agent also watches placement/node/

T+200ms   Agent (node): reads placement/node/{nodeID}/, finds new instances
          → begins reconcileDesiredInstance sequence:
            1. lookupServiceImageFromStore
            2. ensureImageAvailable (pull if needed)
            3. executeInitializationSteps (if any)
            4. startInstanceAndPublishState
          → writes observed/instance/{id}/state=running
          → writes observed/instance/{id}/ip
          ↓ watch events on observed/instance/

T+250ms   endpoint: reads observed/instance/ (running + IP)
          → writes endpoint/service/{svc}/{id}/{port}
          ↓ watch event on endpoint/

T+300ms   network: reads endpoint/ + desired/service/
          → writes network/vip/service/{name} (VIP)
          → writes network/dns/{name} (DNS record)

T+300ms   route: reads endpoint/ + desired/route/
          → writes derived/route/{name}/backends

T+300ms   stateful-dns: reads observed/instance/ (stateful only)
          → writes network/dns/instance/{id}

T+1000ms  Data plane (agent, 1s loop):
          → reads endpoint/, network/vip/, derived/route/
          → programs nftables/iptables/eBPF rules

T+1000ms  DNS forwarder (agent):
          → resolves .cca.local via control plane DNS server
          → control plane reads network/dns/ for answers
```

### Timing Notes

- Each controller has a 50ms debounce, so the cascade doesn't fire instantly
- 30s forced resync catches anything missed by watches
- Agent reconcile loop runs on a 1s ticker OR watch events (whichever first)
- Data plane reconcile runs every 1s independently
- All times above are best-case; etcd roundtrip adds 1-5ms per step

## Failure Controller Chains

### Instance failure → replacement

```
1. Agent detects container exit (exitCode != 0)
   → writes observed/instance/{id}/state=failed
   → writes observed/instance/{id}/exit_reason

2. failure controller reads observed/instance/{id}/state=failed
   → writes derived/instance/{id}/controller_stopped=true
   → writes observed/instance/{replacementID}/state=pending
   → writes observed/instance/{replacementID}/service={svc}
   → increments derived/crash-loop/{service}/counter

3. instance controller sees controller_stopped
   → respects the stop (doesn't re-create old ID)

4. scheduler places the new replacement instance

5. Agent starts the replacement
```

### Node failure → instance rescue

```
1. node-failure reads lease/node/{nodeID}/heartbeat
   → detects expired heartbeat (> LeaseTimeout)
   → writes observed/node/{nodeID}/state=unreachable
   → writes derived/instance/{id}/node_failure=true (for all on that node)

2. failure controller reads derived/instance/{id}/node_failure
   → writes derived/instance/{id}/controller_stopped=true
   → creates replacement instance

3. scheduler places replacement on healthy node

4. Agent on healthy node starts the replacement
```

### Graceful drain → ordered eviction

```
1. Admin sets observed/node/{nodeID}/state=draining
   (or cloud-node-lifecycle detects terminated instance)

2. drain controller reads observed/node/{nodeID}/state=draining
   → picks one instance per service per cycle
   → writes derived/node/{nodeID}/drain/evict/{instanceID}

3. Agent reads drain eviction marker
   → skips instance in reconcile (lets it terminate)

4. drain controller sees all instances evicted
   → writes derived/node/{nodeID}/drain/complete

5. (Meanwhile scheduler places replacements on other nodes)
```

### Liveness failure → graceful restart

```
1. Agent probe scheduler detects liveness failure
   → writes observed/instance/{id}/health=unhealthy

2. failure controller reads health=unhealthy
   → writes derived/instance/{id}/drain_readiness=<timestamp>
   → (grace period begins — endpoint controller removes endpoint)

3. On next cycle after grace period:
   → writes derived/instance/{id}/controller_stopped=true
   → creates replacement instance

4. scheduler places replacement; Agent starts it
```

## Topological Sort Result

The `SortControllersByDependency()` function uses Kahn's algorithm on the
dependency graph above. The computed order ensures producers run before
consumers within a single runner cycle.

### Expected execution order (derived from dependency analysis)

```
Tier 0 (no dependencies):
  auth, cron, credential-broker

Tier 1 (depends on desired/* only):
  autoscale

Tier 2:
  intent-resolver  (depends on: autoscale)

Tier 3:
  instance         (depends on: intent-resolver, failure, node-failure, init, rollout)
  stateful-volume  (depends on: intent-resolver)

Tier 4:
  scheduler        (depends on: instance, node-failure)
  init             (depends on: instance)
  rollout          (depends on: instance, scheduler)
  failure          (depends on: instance, init, node-failure)
  node-failure     (depends on: instance, scheduler)
  stateful-dns     (depends on: intent-resolver, instance)
  job              (depends on: instance, failure, node-failure, init)

Tier 5:
  endpoint         (depends on: instance, failure, node-failure)
  drain            (depends on: instance, scheduler, node-failure)
  eviction         (depends on: instance, node-failure, rollout)
  cluster-autoscale (depends on: instance, failure, node-failure, init, scheduler)

Tier 6:
  network          (depends on: endpoint)
  route            (depends on: endpoint)
  network-policy   (depends on: endpoint, scheduler)
  warm-zero        (depends on: instance, endpoint, rollout)
  cloud-loadbalancer (depends on: endpoint, node-failure)

Tier 7:
  (Agent reads all derived keys and programs the node)
```

**Note:** Tier 3-4 have circular dependencies (instance ↔ failure ↔ node-failure).
Kahn's algorithm handles this by processing what it can, then reporting the
cyclic controllers. In practice, these controllers converge through multiple
cycles: failure marks → instance sees marks → scheduler replaces.

## EventProjector (not a controller — watch-based)

```
EventProjector.Run
├── Watches all key prefixes:
│   observed/instance/
│   observed/node/
│   placement/
│   endpoint/
│   network/vip/
│   network/dns/
│   derived/instance/
│   derived/service/
│
├── On each watch event:
│   ├── classifyEvent (state change, placement, endpoint, etc.)
│   ├── buildEventDescription (human-readable message)
│   └── eventLog.Record(event)
│
└── Events available via: cca events, API /events
```

## Cross-Cutting: What the Agent Reads

The node agent is NOT a controller — it runs its own reconciliation loop
(1s ticker + watch events). But it consumes facts written by controllers:

```
FROM scheduler:           placement/node/{nodeID}/{id}
FROM instance:            observed/instance/{id}/state, /service
FROM init (controller):   derived/instance/{id}/init_phase
FROM node-failure:        derived/instance/{id}/node_failure
FROM failure:             derived/instance/{id}/controller_stopped
FROM drain:               derived/node/{nodeID}/drain/evict/{id}
FROM cca apply (user):    desired/service/{name}/*, desired/connection/{name}
```

The agent WRITES facts that controllers read:

```
TO instance/endpoint/etc: observed/instance/{id}/state (running/failed/stopped)
TO endpoint:              observed/instance/{id}/ip
TO node-failure:          observed/node/{nodeID}/heartbeat (via lease)
TO init (controller):     observed/instance/{id}/init/{step}/state
TO autoscale:             observed/metric/{service}/{metric}
```
