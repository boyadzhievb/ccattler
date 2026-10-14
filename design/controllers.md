# Controllers (Rule Engine)

Controllers watch fact prefixes, compare desired vs actual state, and write new facts/actions.

## Instance Controller
```
RULE maintain_instances(service):
    desired = service.instances
    actual  = count(instances where instance.service = service)
    if actual < desired: create(desired - actual)
    if actual > desired: remove(actual - desired)
```

## Endpoint Controller
```
RULE expose_running(service):
    for instance where instance.service = service AND instance.state = running:
        create endpoint(service, instance)
```

## Failure Controller
```
RULE replace_failed_instance(instance):
    if instance.state = failed:
        create replacement
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

## Controller Interface
```go
type Controller interface {
    Name() string
    Watch() []string          // fact prefixes to observe
    Reconcile(facts) []Change // proposed changes, not arbitrary mutations
}
```

When a node dies, the fact `node(node-a).state = dead` invalidates all dependent facts. Controllers converge the system without explicit notification to every object.
