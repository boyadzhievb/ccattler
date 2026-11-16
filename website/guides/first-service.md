# Deploy your first service

This guide walks through deploying a service from scratch — starting simple and progressively adding health checks, configuration, secrets, and scaling.

## Step 1: Basic service

Create `myapp.cca`:

```hcl
service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}
```

Each keyword:
- `image` — the container image to run
- `instances` — how many copies to maintain
- `expose` — the port the service listens on
- `resources` — CPU and memory allocation per instance

### Apply it

```bash
# Simulated (no containers needed)
cca apply myapp.cca

# Real containers
cca run-container myapp.cca
```

### Check status

```bash
cca status
cca get services
cca get instances
```

## Step 2: Add health checks

Health checks determine when an instance is ready to receive traffic.

```hcl
service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
    health {
        http /health
        every 10s
    }
}
```

CCattler supports three probe types:

| Probe | When it runs | What happens on failure |
|---|---|---|
| `startup` | Before liveness/readiness | Blocks other probes until success |
| `liveness` | After startup succeeds | Restarts the instance |
| `readiness` | After startup succeeds | Removes from endpoint set (no traffic) |
| `health` | Always | General health reporting |

For a database, use a TCP probe:

```hcl
service database {
    image postgres:16
    instances 1
    expose 5432
    health {
        tcp
        every 5s
    }
}
```

## Step 3: Add configuration

Inject environment variables and config files:

```hcl
service api {
    image myapp:v3
    instances 2
    expose 3000
    resources {
        cpu 250m
        memory 256Mi
    }
    config {
        env "LOG_LEVEL" "info"
        env "PORT" "8080"
        file "/etc/api/config.yaml" "server:\n  port: 8080\n  log_level: info"
    }
}
```

Config is declarative desired state — the node agent materializes it into the container at start time.

## Step 4: Add secrets

Secrets are references, never plaintext in config files:

```hcl
secret database.password

service api {
    image myapp:v3
    instances 2
    expose 3000
    resources {
        cpu 250m
        memory 256Mi
    }
    secret database.password {
        mount "/run/secrets/database-password"
    }
}
```

File-mounted secrets are preferred — avoids accidental logging of environment variables.

## Step 5: Scale

Manually:

```bash
cca scale web 10
```

Or declaratively with autoscaling:

```hcl
service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
    scale {
        horizontal {
            min 3
            max 30
            target cpu = 60
        }
    }
}
```

Only a declared service can be scaled: `cca scale` refuses a name you have not applied yet.

### How far you can scale

| Count | What happens |
|---|---|
| below 500 | accepted |
| 500 to 600 | accepted, with a warning |
| above 600 | needs approval from a `cluster.admin`, who repeats the exact count |

```bash
cca scale web 2000                               # on a terminal: asks you to type 2000 again
cca scale web 2000 --approve-above-limit 2000    # in scripts
cca apply --approve-above-limit 2000 web.cca     # instances 2000, or scale max 2000, in a configuration
```

Repeating the number is deliberate: it catches `100000` typed for `100`, which a "yes" would not. There is no upper ceiling, so you can run a very large service if you mean to. The limit is not a capacity promise, though. Instances your nodes cannot hold stay pending, every instance adds about 15 facts to etcd, and counts in the tens of thousands are untested and can overload etcd and the controllers. To cap what a team can run, give its tenant an instance quota (see [Multi-Tenancy](./multi-tenancy)). Details: [CLI reference](../reference/cli#instance-count-limits).

## Step 6: Observe

```bash
# Cluster overview
cca status

# Resource utilization per node and workload
cca top
cca top nodes
cca top workloads

# Stream fact store changes in real time
cca watch

# View container stdout/stderr
cca logs web
cca logs web --follow   # live tail
```

## Step 7: Update

Change the image in your `.cca` file and re-apply:

```hcl
service web {
    image nginx:1.29   # updated from 1.28
    instances 3
    expose 8080
    update {
        max_unavailable 1
        max_extra 1
    }
}
```

```bash
cca apply myapp.cca
```

The rolling update controller starts new instances, waits for them to become healthy, then stops old instances — one at a time with `max_unavailable 1`.

If new instances fail health checks, the system automatically rolls back.
