Yes — and I think **CCattler should make observability a first-class part of the platform**, rather than requiring a separately installed "metrics server" just to answer basic questions.

In fact, this is an opportunity to make CCattler's observability model significantly cleaner than Kubernetes.

## What I'd aim for

A user should be able to immediately do:

```text
cca status
cca top nodes
cca top workloads
cca get workload api
cca describe workload api
cca logs api
cca events
cca watch
```

without installing Prometheus, Metrics Server, Grafana, etc.

Those systems can still be plugged in later for long-term monitoring.

The architecture should look roughly like:

```text
                    RUNTIME
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
       CPU/mem       network      disk/io
       process       packets      storage
          │            │            │
          └────────────┼────────────┘
                       ▼
                ┌─────────────┐
                │   Agent     │
                │  telemetry  │
                └──────┬──────┘
                       │
                 observations
                       │
                       ▼
                 ┌───────────┐
                 │ State /   │
                 │ Telemetry │
                 └─────┬─────┘
                       │
              ┌────────┼────────┐
              ▼        ▼        ▼
             CLI     API     Exporters
              │
              ▼
       cca top / status / etc.
```

But I'd make an important architectural distinction.

# 1. Don't put every metric into the state store

Your etcd store should contain **state and facts**, not become a time-series database.

For example:

```text
/ccattler/observed/nodes/node-a
/ccattler/observed/workloads/api/...
/ccattler/observed/health/api/...
```

can contain current observations such as:

```text
node-a:
    cpu:
        capacity: 8
        allocated: 5
        utilization: 62%

    memory:
        capacity: 32Gi
        allocated: 18Gi
        utilization: 56%

    disk:
        capacity: 500Gi
        used: 212Gi

    status: Ready
```

But you **don't** want:

```text
/metrics/cpu/node-a/2026-09-10T11:00
/metrics/cpu/node-a/2026-09-10T11:01
/metrics/cpu/node-a/2026-09-10T11:02
...
```

in etcd.

That's what a metrics/time-series system is for.

---

# 2. Have two observability planes

I'd explicitly separate:

### State telemetry

"What is happening **now**?"

```text
Node:
    Ready
    CPU 61%
    Memory 54%
    Disk 42%

Workload:
    Running
    CPU 120m
    Memory 384Mi
    Ready 2/3
```

This powers:

```text
cca status
cca top nodes
cca top workloads
cca describe
```

### Time-series telemetry

"What has been happening over time?"

```text
CPU utilization
memory utilization
network traffic
restart count
request latency
error rate
probe failures
container start duration
scheduling latency
```

This goes to something like Prometheus/OpenTelemetry-compatible infrastructure.

---

# 3. `cca top` should be built in

This is one area where I wouldn't make users install anything.

Something like:

```text
$ cca top nodes

NODE       STATUS   CPU        MEMORY       DISK
node-a     Ready    3.2/8 CPU  12.4/32 Gi   42%
node-b     Ready    6.1/8 CPU  24.8/32 Gi   71%
node-c     Ready    1.8/8 CPU   8.2/32 Gi   31%
```

And:

```text
$ cca top workloads

WORKLOAD          NODE      STATUS     CPU       MEMORY     READY
frontend/web-1    node-a    Running    120m      84Mi       yes
frontend/web-2    node-b    Running     98m      79Mi       yes
payments/api-1    node-b    Running    430m     512Mi       yes
payments/api-2    node-c    Running    390m     488Mi       yes
```

No Metrics Server concept is required for this.

The **agent already knows the runtime**.

---

# 4. But don't call it "resource utilization" blindly

There are actually several different quantities that are useful.

For a node:

```text
capacity
allocatable
requested
reserved
actual_usage
```

For example:

```text
CPU
    capacity:     8 cores
    allocatable:  7.5 cores
    requested:    5.2 cores
    actual:       4.1 cores
```

This is much more informative than simply:

```text
CPU: 55%
```

Because the scheduler cares about **capacity and reservations**, while operators care about **actual usage**.

Same for memory.

I'd expose all of them.

---

# 5. Workload status should be richer than Kubernetes Pod status

This is particularly important for CCattler.

Instead of:

```text
Pod:
    Running
```

I'd show something like:

```text
$ cca get workload payments/api

NAME:       payments/api
DESIRED:    3
PLACED:     3
RUNNING:    3
READY:      2
HEALTHY:    3
INITIALIZED: 3

STATUS:     Degraded

CPU:        1.2 cores
MEMORY:     1.8 GiB

INIT:
    migrate-db       succeeded
    generate-config  succeeded

STARTUP:
    3/3 succeeded

READINESS:
    2/3 ready

LIVENESS:
    3/3 healthy

RESTARTS:
    api-2             4
```

That fits your model much better because these aren't arbitrary lifecycle labels — they're **observations from different dimensions**.

---

# 6. Probes become observability data

This connects directly to what we discussed about startup/readiness/liveness.

Don't merely store:

```text
ready = false
```

Store useful observations:

```text
readiness:
    state: unhealthy
    consecutive_failures: 3
    last_check: ...
    latency: 42ms
    reason: connection_refused
```

Then:

```text
cca describe workload api
```

could explain:

```text
READINESS
  endpoint: HTTP :8080/health/ready
  state:    Unhealthy
  failures: 3
  reason:   connection_refused
  last check: 2s ago

  Endpoint excluded from service discovery.
```

This is **observability feeding state**, rather than observability being an afterthought.

---

# 7. Events are another critical piece

I'd also have a first-class event stream:

```text
$ cca events

TIME      SUBJECT          EVENT
11:02:14  payments/api     Scheduled on node-b
11:02:15  payments/api     Initialization started
11:02:17  payments/api     Initialization succeeded
11:02:18  payments/api     Startup succeeded
11:02:19  payments/api     Readiness succeeded
11:02:19  payments/api     Endpoint added
11:07:31  payments/api     Readiness failed
11:07:31  payments/api     Endpoint removed
11:07:45  payments/api     Readiness recovered
```

This is incredibly useful when debugging reconciliation.

And importantly, events should be **projected from committed state transitions**, not generated by controllers saying "I attempted X."

---

# 8. Metrics should eventually use OpenTelemetry

For external observability, I'd avoid inventing a proprietary metrics protocol.

Conceptually:

```text
CCattler Agent
      │
      ├── current observations ──► CCattler State
      │
      └── metrics/traces/logs ───► OpenTelemetry
                                      │
                       ┌──────────────┼──────────────┐
                       ▼              ▼              ▼
                   Prometheus      Grafana      Jaeger/etc.
```

That means users can choose their monitoring stack.

And CCattler itself doesn't need to become:

> "yet another Prometheus replacement."

---

# 9. I would add a built-in metrics endpoint

For example:

```text
GET /metrics
```

on the agent/control-plane.

Then Prometheus can scrape:

```text
node_cpu_usage
node_memory_usage
workload_cpu_usage
workload_memory_usage
workload_restarts_total
workload_startup_duration
probe_success
probe_failures_total
reconciliation_duration
reconciliation_errors_total
scheduler_latency
```

This makes external monitoring extremely easy.

But again:

**Prometheus is optional.**

Basic operational visibility works without it.

---

# 10. Tracing is especially interesting for CCattler

Because your architecture is reconciliation-heavy, distributed tracing can show:

```text
user request
    │
    ▼
API
    │
    ▼
state commit
    │
    ▼
scheduler reconcile
    │
    ▼
placement commit
    │
    ▼
agent reconcile
    │
    ▼
runtime operation
    │
    ▼
observation
    │
    ▼
state commit
```

Imagine:

```text
cca trace workload payments/api
```

showing:

```text
11:02:14.102  API request
11:02:14.108  desired state committed
11:02:14.121  scheduler started
11:02:14.125  placement committed
11:02:14.141  node-b agent observed placement
11:02:14.150  runtime start
11:02:14.431  startup probe passed
11:02:14.517  readiness probe passed
11:02:14.522  endpoint published
```

That's extremely powerful for diagnosing **why convergence took 420 ms**, for example.

---

# 11. The CLI could become a major differentiator

I'd aim for this experience:

```text
cca status
```

```text
CLUSTER

CONTROL PLANE    2/2 healthy
NODES            3/3 ready
WORKLOADS        18 desired / 18 running
READY            17/18
CPU              42%
MEMORY           57%

DEGRADED
  payments/api-3
    readiness probe failing
```

Then:

```text
cca top nodes
```

```text
NODE       CPU       MEMORY      WORKLOADS
node-a     42%       51%         7
node-b     71%       63%         6
node-c     28%       41%         5
```

Then:

```text
cca top workloads
```

And:

```text
cca describe payments/api
```

And:

```text
cca events --follow
```

And:

```text
cca logs payments/api
```

This gives you a very complete operational experience **without pretending that the state store is a monitoring database**.

---

## The architecture I'd add to CCattler

I'd introduce an explicit observability subsystem:

```text
observability/
    metrics/
        collector.go
        registry.go
    events/
        projector.go
    logs/
        collector.go
    tracing/
        tracer.go
    exporter/
        otel.go
        prometheus.go
```

And on the agent:

```text
agent/
    node/
        reporter.go

    runtime/
        reconciler.go
        observer.go

    health/
        scheduler.go
        reporter.go

    telemetry/
        collector.go
```

With the conceptual pipeline:

```text
                 RUNTIME
                    │
                    ▼
              OBSERVATIONS
                    │
             ┌──────┴──────┐
             │             │
             ▼             ▼
        CURRENT STATE    METRICS
             │             │
             ▼             ▼
          STATE STORE    OTEL
             │             │
             ▼             ▼
       cca status/top   Prometheus
             │           Grafana
             │           tracing
             ▼
           EVENTS
```

### And I'd make this a core CCattler principle:

> **State tells you what is true now. Metrics tell you how it changes over time. Events tell you what changed. Traces tell you why it took time. Logs tell you what the workload said.**

That gives CCattler a much more complete observability story than simply recreating Kubernetes' Metrics Server + `kubectl top`.

Most importantly, **the agent already has the natural vantage point to collect node/runtime utilization**, so requiring a separate metrics component for basic `top` functionality would be unnecessary complexity in CCattler. The external metrics stack should be an **integration**, not a prerequisite.
