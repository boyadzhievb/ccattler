Yes. Kubernetes’ **init containers** are important, but I would **not copy the Kubernetes concept directly** into CCattler.

For CCattler, I’d model this as a **startup dependency / initialization phase** of a workload.

### The key distinction

An init container means:

> “Before this workload is considered started, these initialization steps must complete successfully.”

That fits CCattler very naturally as:

```text
DESIRED
   │
   ▼
INITIALIZING
   │
   ├── init step 1
   ├── init step 2
   └── init step 3
   │
   ▼
STARTABLE
   │
   ▼
RUNNING
   │
   ├── readiness
   └── liveness
```

The important part is that **initialization is state**, not an imperative instruction.

## I would call it `init`

For example:

```text
service api {
    image my-api:1.4
    instances 3

    init {
        exec "migrate-db"
        timeout 30s
    }

    init {
        exec "generate-config"
        timeout 10s
    }

    startup {
        http /startup
        port 8080
        every 2s
        timeout 1s
        failure_threshold 30
    }

    readiness {
        http /health/ready
        port 8080
        every 5s
    }
}
```

But there is an important question: **should `init` run inside the same runtime environment as the main workload?**

I'd support both concepts eventually.

### 1. Workload-local initialization

Something equivalent to:

```text
init {
    exec "generate-config"
}
```

The init process shares the workload's filesystem/network/environment as appropriate.

This is useful for:

* generating configuration
* migrations
* preparing files
* permissions
* downloading/transforming local data
* bootstrap scripts

### 2. Dependency initialization

More interesting for CCattler:

```text
service api {
    depends_on database {
        condition ready
    }
}
```

Or even:

```text
init {
    wait_for database
    condition ready
}
```

This is different from an init container. It's a **relation between workloads**.

That distinction is valuable because CCattler's architecture is already based around relations.

---

# The state model

I wouldn't make the runtime say:

> "I started the init container, therefore initialization succeeded."

Instead:

```text
desired:
    workload/api
        init:
            migrate-db
            generate-config

observed:
    workload/api
        initialization:
            migrate-db = succeeded
            generate-config = succeeded
            phase = complete
```

Then the controller derives:

```text
initialization complete
        ↓
workload is allowed to start
        ↓
runtime reconciler starts main workload
```

So again:

**Agent performs. Agent observes. Controller decides. Store records truth.**

---

# Failure semantics

This is where CCattler can improve on simply copying Kubernetes.

Suppose:

```text
init migrate-db
```

fails.

The observation becomes:

```text
initialization:
    phase: failed
    step: migrate-db
    reason: exit_code
    code: 1
```

The controller can then derive:

```text
main workload should NOT run
```

It should **not** automatically restart the main workload, because it isn't running yet.

You could have:

```text
init {
    exec "migrate-db"

    retry 5
    backoff exponential
}
```

And the initialization controller/runtime reconciler handles that.

---

# Initialization should be ordered

I'd make init steps deterministic and sequential by default:

```text
init {
    exec "prepare-data"
}

init {
    exec "migrate-db"
}

init {
    exec "generate-config"
}
```

means:

```text
prepare-data
      ↓
migrate-db
      ↓
generate-config
      ↓
main workload
```

But eventually allow explicit dependencies:

```text
init prepare-data {
    exec "prepare-data"
}

init schema {
    exec "validate-schema"
}

init migrate {
    exec "migrate-db"
    after [prepare-data, schema]
}
```

That gives you a DAG rather than forcing everything into one sequence.

---

# And this fits beautifully with probes

I would make the lifecycle:

```text
              desired workload
                     │
                     ▼
              INITIALIZATION
                     │
            ┌────────┴────────┐
            │                 │
          failed           succeeded
            │                 │
            ▼                 ▼
        InitFailed         STARTING
                              │
                              ▼
                         STARTUP PROBE
                              │
                       ┌──────┴──────┐
                       │             │
                    failing       succeeded
                       │             │
                       ▼             ▼
                   Starting       READY?
                                      │
                              ┌───────┴───────┐
                              │               │
                           not ready         ready
                              │               │
                              ▼               ▼
                         no endpoint       endpoint
                                              │
                                              ▼
                                          RUNNING
                                              │
                                              ▼
                                         LIVENESS
```

This gives the three concepts very clean responsibilities:

| Mechanism     | Question                                  |
| ------------- | ----------------------------------------- |
| **Init**      | Has required initialization completed?    |
| **Startup**   | Has the application successfully started? |
| **Readiness** | Should traffic be sent here?              |
| **Liveness**  | Is the running application still healthy? |

That's much cleaner than treating all four as "health checks."

---

# One more thing: don't make `init` necessarily a container

This is where I'd deliberately diverge from Kubernetes.

Instead of:

```text
initContainer
```

think:

```text
InitializationStep
```

The execution backend could be:

```text
exec
container
script
job
rpc
dependency
```

For example:

```text
init {
    exec "/opt/migrate"
}

init {
    container "migration:1.2"
}

init {
    wait_for database
    condition ready
}
```

The controller doesn't care how the initialization is performed.

It only cares about the resulting observation:

```text
InitializationSucceeded
```

or

```text
InitializationFailed
```

That fits CCattler's provider-oriented architecture much better.

## The architecture I'd aim for

```text
                 DESIRED
                    │
                    ▼
             ┌──────────────┐
             │ Init Manager │
             └──────┬───────┘
                    │
              initialization
                 observations
                    │
                    ▼
             ┌──────────────┐
             │   Runtime    │
             │ Reconciler   │
             └──────┬───────┘
                    │
                 start app
                    │
                    ▼
             ┌──────────────┐
             │    Probe     │
             │  Scheduler   │
             └──────┬───────┘
                    │
          ┌─────────┼─────────┐
          ▼         ▼         ▼
       startup   readiness  liveness
          │         │         │
          └─────────┼─────────┘
                    ▼
                OBSERVED
                    │
                    ▼
                 FACTS
```

So I would add **initialization as a first-class lifecycle concept**, but not as a Kubernetes-specific "init container" abstraction.

The CCattler principle becomes:

> **Initialization establishes prerequisites. Startup establishes application health. Readiness establishes service eligibility. Liveness establishes continued operation.**

That gives you a very coherent lifecycle model and leaves room for both process and container runtimes.
