Yes — I checked the **current repository**, and I would **add all three concepts**, but with a CCattler-specific design rather than copying Kubernetes verbatim.

### Current status

I don't see dedicated **Liveness / Readiness / Startup Probe** implementations in the current repo. The repository does have health-check functionality in the agent, and the latest milestone specifically mentions health-check scheduling, but the three probe semantics aren't yet modeled as first-class concepts.

So currently you're closer to:

```text
health check
    ↓
agent
    ↓
observed health
```

rather than:

```text
startup probe
liveness probe
readiness probe
        ↓
   observations
        ↓
   reconciliation
```

## Do we need them?

**Yes. Absolutely.**

But I would define them as **observations**, not imperative commands.

The key CCattler principle should remain:

> A probe never restarts a container. A probe produces an observation. Reconciliation decides what to do.

---

# 1. Startup Probe

**Yes, we need this.**

Purpose:

> "Has this workload successfully started yet?"

Example:

```text
service api {
    image my-api:1.0

    startup {
        http /startup
        port 8080
        every 2s
        timeout 1s
        failure_threshold 30
    }
}
```

Until startup succeeds:

```text
startup = pending
readiness = not-ready
liveness = not-applicable
```

This is important for applications that take:

* 30 seconds to boot
* migrations
* JVM startup
* large model loading
* cache initialization
* database initialization

Without a startup probe, liveness could kill a perfectly healthy application simply because it takes a long time to initialize.

---

# 2. Liveness Probe

**Yes, definitely.**

It answers:

> "Is this process/workload still alive and functioning?"

For example:

```text
liveness {
    http /health/live
    port 8080
    every 10s
    timeout 2s
    failure_threshold 3
}
```

Observation:

```text
/observed/instances/api-123/health/liveness

status = failed
consecutive_failures = 3
last_check = ...
```

Then reconciliation can decide:

```text
desired instance = running
observed instance = unhealthy
        ↓
restart required
```

Notice the distinction.

The probe does **not** do:

```text
restart()
```

It reports:

```text
liveness = failed
```

The runtime reconciler decides whether the correct action is restart/recreate.

---

# 3. Readiness Probe

**This one is extremely important for CCattler's networking model.**

It answers:

> "Should this workload currently receive traffic?"

Example:

```text
readiness {
    http /ready
    port 8080
    every 5s
    timeout 1s
    failure_threshold 2
}
```

You could have:

```text
api-1  ready
api-2  ready
api-3  not-ready
```

The endpoint/network controller then derives:

```text
service api
    endpoints:
        api-1
        api-2
```

instead of:

```text
api-1
api-2
api-3
```

This fits CCattler **very naturally** because you already have an Endpoint Controller.

---

# The important distinction

I would model the three like this:

| Probe         | Question                 | Primary consequence              |
| ------------- | ------------------------ | -------------------------------- |
| **Startup**   | Has it initialized?      | Gates liveness/readiness         |
| **Liveness**  | Is it still functioning? | May cause restart/reconciliation |
| **Readiness** | Can it receive traffic?  | Controls endpoint membership     |

So:

```text
                  WORKLOAD
                     │
          ┌──────────┼──────────┐
          ▼          ▼          ▼
       Startup    Liveness   Readiness
          │          │          │
          └──────────┼──────────┘
                     ▼
                 OBSERVATIONS
                     │
       ┌─────────────┼─────────────┐
       ▼             ▼             ▼
   Runtime       Endpoint       Network
 Reconciler      Controller     Controller
```

---

# I'd make probes a first-class CCattler concept

Not Kubernetes objects.

Your DSL could eventually look like:

```text
service api {
    image my-api:1.4
    instances 3

    expose 8080

    startup {
        http /startup
        port 8080
        every 2s
        timeout 1s
        failure_threshold 30
    }

    liveness {
        http /health/live
        port 8080
        every 10s
        timeout 2s
        failure_threshold 3
    }

    readiness {
        http /health/ready
        port 8080
        every 5s
        timeout 1s
        failure_threshold 2
    }
}
```

But I'd also support:

```text
tcp
exec
http
```

Eventually perhaps:

```text
grpc
```

---

# And don't limit probes to containers

This is another place where CCattler can improve on the Kubernetes mental model.

Your probe abstraction should be something like:

```go
type Probe interface {
    Check(ctx context.Context, target Target) Observation
}
```

with:

```text
HTTPProbe
TCPProbe
ExecProbe
GRPCProbe
```

The target could be:

```text
process
container
service endpoint
```

That means your process runtime can use the **same health model** as your OCI runtime.

Very useful for your current development strategy.

---

# Probe state should be richer than `true/false`

I'd recommend an observation like:

```text
startup:
    state = succeeded
    consecutive_successes = 5
    consecutive_failures = 0
    last_transition = ...

liveness:
    state = healthy

readiness:
    state = ready
```

And states:

```text
Unknown
Pending
Healthy
Unhealthy
```

For startup specifically:

```text
Pending
Succeeded
Failed
```

You also want:

```text
last_check
last_success
last_failure
failure_reason
latency
```

This becomes incredibly useful for debugging.

---

# One important change to your current architecture

The current agent should **not run probes simply because its reconciliation loop happens to run**.

We discussed this previously, and the latest code has moved toward honoring the configured interval.

Instead:

```text
                    Agent
                      │
          ┌───────────┴───────────┐
          │                       │
   Reconciliation           Probe Scheduler
      loop                       │
                                 ├── startup every 2s
                                 ├── readiness every 5s
                                 └── liveness every 10s
```

Probe scheduling should be independent.

Otherwise:

```text
reconciliation = 1 second
```

would accidentally mean:

```text
liveness probe = 1 second
```

which is the wrong abstraction.

---

# And here's the really important CCattler flow

Imagine `api-3` starts.

```text
Runtime starts container
        │
        ▼
startup probe
        │
        ▼
startup = succeeded
        │
        ├──────────────┐
        ▼              ▼
 liveness probe    readiness probe
     │                 │
 healthy              ready
     │                 │
     └────────┬────────┘
              ▼
       observed state
              │
              ▼
      endpoint controller
              │
              ▼
       api-3 gets traffic
```

Later the application becomes unhealthy:

```text
liveness
   │
   ▼
FAILED
   │
   ▼
observation
   │
   ▼
runtime reconciler
   │
   ▼
restart
```

And if the application is alive but temporarily overloaded:

```text
liveness = healthy
readiness = failed
```

Then:

```text
NO restart
    │
    ▼
remove from endpoints
    │
    ▼
stop receiving traffic
```

That's exactly the behavior you want.

---

# One more thing: don't call them "actions"

I'd avoid an architecture where the state says:

```text
restart container X
```

Instead:

```text
desired:
    instance X = running

observed:
    instance X =
        process = alive
        startup = succeeded
        liveness = failed
        readiness = false
```

Then:

```text
reconciliation
    ↓
runtime needs to converge
    ↓
restart
```

So your fundamental invariant survives:

> **Probes report reality. Controllers derive desired state. Agents reconcile.**

---

## My recommendation for CCattler v0.1

I'd add them **now**, before calling the health system complete:

```text
P0
├── Probe interface
├── HTTP probe
├── TCP probe
├── Exec probe (process/container runtime)
├── Startup probe
├── Liveness probe
├── Readiness probe
├── independent probe scheduler
├── consecutive success/failure thresholds
├── timeout
├── initial delay
├── period
├── observed health facts
└── endpoint controller consumes readiness

P1
├── gRPC probe
├── graceful termination after liveness failure
├── probe metrics
└── richer failure reasons
```

And I would **not** copy Kubernetes' API/object model for this.

Make probes part of your existing:

**Intent → Desired → Reality → Observation → Reconciliation**

architecture.

That would make the health system feel like **CCattler**, rather than "Kubernetes with a different DSL."
