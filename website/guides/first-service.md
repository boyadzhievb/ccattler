# Deploy your first service

This guide walks through deploying a service from scratch — starting simple and progressively adding health checks, configuration, secrets, and scaling.

## Step 1: Basic service

Create `myapp.ccattler`:

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
# Simulated (no Docker needed)
cca apply myapp.ccattler

# Real containers
cca run-container myapp.ccattler
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

# View cluster event log
cca logs
cca logs web    # filtered by service
```

## Step 7: Update

Change the image in your `.ccattler` file and re-apply:

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
cca apply myapp.ccattler
```

The rolling update controller starts new instances, waits for them to become healthy, then stops old instances — one at a time with `max_unavailable 1`.

If new instances fail health checks, the system automatically rolls back.
