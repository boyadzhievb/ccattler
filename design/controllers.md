# Controllers (Rule Engine)

Controllers watch fact prefixes, compare desired vs actual state, and write new facts/actions.

## Instance Controller
```
RULE maintain_instances(service):
    desired = service.instances
    actual  = count(instances where instance.service = service AND effective_state != stopped)
    if actual < desired: create(desired - actual)
    if actual > desired: PUT derived/instance/{id}/controller_stopped = "true"
```
Scale-down writes to `derived/instance/{id}/controller_stopped` (not `observed/.../state`),
because the node agent continuously publishes observed runtime state — writing "stopped"
directly races with the agent writing "running".

## Endpoint Controller
```
RULE expose_running(service):
    for instance where instance.service = service AND instance.state = running:
        create endpoint(service, instance)
```

## Failure Controller
```
RULE replace_failed_instance(instance):
    effective_state = controller_stopped > node_failure > observed_state
    if effective_state = failed:
        if crash_loop_count >= circuit_breaker_threshold (10):
            if now < last_replacement + cooldown (10m): SKIP  (circuit open)
            else: allow one probe replacement                 (half-open)
        else if crash_loop_backoff not elapsed: SKIP          (backoff: 10s→5m)
        PUT derived/instance/{id}/controller_stopped = "true"
        create replacement instance (pending)
        increment derived/crash-loop/{service} counter

    if liveness = unhealthy AND running:
        begin drain (readiness=not-ready + drain_since)
        after grace period: stop and replace

    if startup = failed AND running:
        immediate stop and replace
```

## Node-Failure Controller
```
RULE detect_node_failure(node):
    if lease/node/{id} expired:
        PUT observed/node/{id}/state = "unreachable"
        for each instance on node:
            PUT derived/instance/{id}/node_failure = "true"
    if node recovers (fresh heartbeat):
        PUT observed/node/{id}/state = "alive"
        DELETE derived/instance/{id}/node_failure
```

## Init Controller
```
RULE derive_init_phase(instance):
    steps = desired_init_steps(instance.service)
    if all steps succeeded: phase = complete
    if any step failed:    phase = failed
    if any step running:   phase = running
    else:                  phase = pending
```

## Autoscaling (Unified Scaling Engine)

**Architectural rule**: autoscalers recommend and modify desired capacity — they never directly manipulate runtime instances.

One generic engine with three concepts: **Signal**, **Policy**, **Dimension**.

### Horizontal Scaling
```
autoscale checkout {
    dimension instances
    min 2, max 30
    target cpu = 60%
    target memory = 70%
    target requests_per_second = 500
}
```
Multiple metrics evaluated independently → `desired = max(cpu_rec, mem_rec, rps_rec)`.

### Vertical Scaling
Changes resource requirements instead of instance count:
```
scale vertically {
    cpu { min 250m, max 4 }
    memory { min 512Mi, max 8Gi }
}
```
Reconciler decides: resize in-place (live cgroup update) or replace instance.

### Event-Driven Scaling
External signals become observations, not imperative triggers:
```
scale horizontally {
    event { source payments.pending, target 20 messages/instance }
}
→ queue_depth(800) / 20 = 40 instances
```

### Scheduled Scaling
Time as an input signal:
```
schedule { weekdays 08:00-18:00, minimum 10 }
```

### Stabilization
Never scale from instantaneous metrics. Use time-windowed averages with asymmetric stabilization:
```
scale-up stabilization = 60s
scale-down stabilization = 5m
```

### Multi-Policy Resolution
Single scaling decision function — no competing writers:
```
desired = max(cpu_rec, queue_rec, scheduled_min)
    subject to: min, max, tenant quota, cluster capacity
```

### Capacity Chain
```
autoscaler recommendation → tenant quota → scheduler capacity → running
```
Each constraint is visible: "scaling constrained by tenant quota" vs silent capping.

### Cluster Autoscaling
Unsatisfied scheduling demand triggers node scaling:
```
application autoscaler → desired workload → scheduler → unsatisfied capacity
    → cluster autoscaler → desired nodes → infrastructure provider
```

### Complete Flow
```
Metrics/Events → Scaling Engine (policies, prediction, stabilization)
    → Policy/Limits (quota, min/max, capacity) → Desired State → Reconciliation → Reality
```

The instance controller doesn't care *why* desired changed — it just sees `desired=5, actual=3` and creates two more.

## Rollout Controller
```
RULE rolling_update(service):
    if observed_image != desired_image:
        replace instances in batches (max_unavailable, max_extra)
        track rollout state: rolling → complete
        record previous_image for rollback
```

## Route Controller
```
RULE derive_routes(gate):
    compile gate DSL → route facts with backend services
    resolve external hostnames → internal service names (cached 5s TTL)
```

## Network Policy Controller
```
RULE compile_network_policy(policy):
    compile identity-based firewall DSL → derived/network/rule/ facts
    rules consumed by nftables/eBPF data plane
```

## Drain Controller
```
RULE drain_node(node):
    when drain initiated: evict instances respecting termination grace period
    track drain progress via derived/node/{id}/drain/* facts
```

## Job Controller
```
RULE manage_job(job):
    create job instances, track completions
    respect backoff_limit on failure, parallelism constraints
```

## CronJob Controller
```
RULE schedule_cronjob(cronjob):
    on 30s resync: check if cron schedule matches current minute
    create job instance if match, prevent double-fire via last_scheduled
```

## Auth Controller
```
RULE reconcile_auth(policy):
    compile auth DSL (roles, grants, ABAC conditions) → auth/ fact tree
    evaluate capability checks against compiled policy
```

## Storage Controller
```
RULE manage_volumes(volume):
    track volume attach/detach state per node
    coordinate with scheduler for volume-aware placement
```

## Stateful DNS / Stateful Volume Controllers
```
RULE stateful_identity(instance):
    assign stable DNS names and ordinal indices to stateful instances
    bind persistent volumes to ordinal slots
```

## Cloud Controllers (cloud_loadbalancer, cloud_node_lifecycle, cloud_routes)
```
RULE cloud_reconciliation:
    cloud_loadbalancer: sync service VIPs to cloud LB (per-port granularity)
    cloud_node_lifecycle: provision/terminate nodes via InfrastructureProvider
    cloud_routes: sync overlay subnet routes to cloud VPC route tables
```

## Credential Broker Controller
```
RULE broker_credentials(instance):
    issue short-lived cloud credentials per SPIFFE identity
    track credential lifecycle via derived/credential/ facts
```

## Cluster Scale Controller
```
RULE cluster_autoscale:
    watch unplaced-demand from scheduler
    emit scale-up/scale-down intent to InfrastructureProvider
    durable-intent pattern (no direct provider calls in reconcile)
```

## Metrics Controller
```
RULE collect_metrics:
    read observed/telemetry/ facts (CPU, memory, OOM kills)
    expose via /metrics endpoint for Prometheus scraping
```

## Controller Interface
```go
type Controller interface {
    Name() string
    Watch() []string          // fact prefixes to observe
    Reconcile(facts) []Change // proposed changes, not arbitrary mutations
}
```

When a node dies, the fact `node(node-a).state = dead` invalidates all dependent facts. Controllers converge the system without explicit notification to every object.
