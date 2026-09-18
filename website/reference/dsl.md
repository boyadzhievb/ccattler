# DSL Grammar

CCattler uses a purpose-built declarative language instead of YAML. Configuration files have the `.ccattler` extension.

## Overview

The language compiles to facts — `service web { image nginx:1.28, instances 3 }` becomes `service(name="web", image="nginx:1.28", instances=3)`. Everything above the fact store is human-facing; everything below is machine-facing.

## Top-level blocks

### service

The primary workload declaration.

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

    startup {
        http /healthz
        every 5s
        initial_delay 10s
        success_threshold 1
        failure_threshold 10
    }

    liveness {
        http /health
        every 10s
        timeout 3s
        failure_threshold 3
    }

    readiness {
        http /ready
        every 5s
        success_threshold 1
        failure_threshold 2
    }

    init {
        exec "db-migrate --run"
        timeout 30s
        retry 3
    }

    config {
        env "LOG_LEVEL" "info"
        env "PORT" "8080"
        file "/etc/app/config.yaml" "key: value"
    }

    secret database.password {
        mount "/run/secrets/db-password"
    }

    scale {
        horizontal {
            min 3
            max 30
            target cpu = 60
            target requests_per_second = 500
            event payments.pending = 20
            schedule {
                days weekdays
                start "08:00"
                end "18:00"
                minimum 10
            }
            stabilization {
                scale_up 60s
                scale_down 300s
            }
        }
        vertical {
            cpu { min 250m, max 4 }
            memory { min 512Mi, max 8Gi }
        }
    }

    placement {
        architecture amd64
        zone spread
        require gpu = true
        prefer region = us-east
        accept dedicated-compute
    }

    update {
        max_unavailable 1
        max_extra 1
    }

    volume pgdata /var/lib/postgresql/data
}
```

### volume

Persistent storage declaration.

```hcl
volume pgdata {
    size 100Gi
    persistent true
}
```

### group

Co-located containers sharing network and volumes.

```hcl
group frontend {
    process proxy
    process web
    share network
    share volume cache
}
```

### tenant

Multi-tenancy with resource quotas.

```hcl
tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
        volumes 50
        storage 10Ti
    }
}
```

### role

RBAC role with fact-prefix permissions.

```hcl
role developer {
    allow service.read
    allow service.update
}

role operator {
    allow read *
    allow modify service/*
    allow modify node/*
}
```

### grant

Bind a role to a group or user.

```hcl
grant developer to group developers
```

### policy

ABAC policy with attribute conditions.

```hcl
policy team-isolation {
    allow service.update
    when subject.team == resource.team
}
```

### config

Standalone config block (outside a service).

```hcl
config api {
    env "LOG_LEVEL" "info"
    file "/etc/api/config.yaml" "..."
}
```

### secret

Declare a secret exists.

```hcl
secret database.password
```

### network

Identity-based network policies.

```hcl
network {
    allow frontend/web -> payments/checkout port 443
    deny frontend/web -> payments/database
}
```

### export

Share a service across tenants.

```hcl
export platform/dns {
    allow frontend
    allow payments
}
```

## Keyword reference

| Keyword | Context | Description |
|---|---|---|
| `service` | top-level | Declare a workload |
| `image` | service | Container image reference |
| `instances` | service | Desired instance count |
| `expose` | service | Port to expose |
| `resources` | service | Resource constraints block |
| `cpu` | resources | CPU allocation (millicores: `500m`) |
| `memory` | resources | Memory allocation (`512Mi`, `2Gi`) |
| `health` | service | Legacy health check block |
| `startup` | service | Startup probe (gates liveness/readiness) |
| `liveness` | service | Liveness probe (restarts on failure) |
| `readiness` | service | Readiness probe (gates endpoint inclusion) |
| `http` | probe | HTTP probe method with path |
| `tcp` | probe | TCP probe method |
| `every` | probe | Probe interval |
| `timeout` | probe/init | Probe or init step timeout |
| `initial_delay` | probe | Delay before first probe |
| `success_threshold` | probe | Consecutive successes to pass |
| `failure_threshold` | probe | Consecutive failures to fail |
| `init` | service | Initialization step (sequential, before main) |
| `exec` | init | Command to execute |
| `retry` | init | Number of retries on failure |
| `config` | service/top-level | Configuration block |
| `env` | config | Environment variable |
| `file` | config | Config file with path and content |
| `secret` | top-level/service | Declare or reference a secret |
| `mount` | secret (in service) | Mount path for secret file |
| `scale` | service | Scaling configuration |
| `horizontal` | scale | Horizontal autoscaling |
| `vertical` | scale | Vertical autoscaling |
| `min` / `max` | scale | Instance count or resource bounds |
| `target` | horizontal | Scaling target metric |
| `event` | horizontal | Event-driven scaling source |
| `schedule` | horizontal | Time-based minimum |
| `stabilization` | horizontal | Scale-up/down stabilization windows |
| `placement` | service | Placement constraints |
| `architecture` | placement | CPU architecture filter |
| `zone` | placement | Zone spread strategy |
| `require` | placement | Hard label constraint |
| `prefer` | placement | Soft label preference |
| `accept` | placement | Accept restricted nodes |
| `update` | service | Rolling update strategy |
| `max_unavailable` | update | Max instances down during update |
| `max_extra` | update | Max extra instances during update |
| `volume` | top-level/service | Declare or attach a volume |
| `size` | volume | Volume size |
| `persistent` | volume | Whether volume survives restarts |
| `group` | top-level | Co-located container group |
| `tenant` | top-level | Multi-tenancy declaration |
| `quota` | tenant | Resource quota block |
| `role` | top-level | RBAC role definition |
| `allow` | role/policy | Permission grant |
| `grant` | top-level | Role binding |
| `policy` | top-level | ABAC policy definition |
| `when` | policy | Attribute condition |
| `network` | top-level | Network policy block |
| `deny` | network | Deny traffic rule |
| `export` | top-level | Cross-tenant service export |
