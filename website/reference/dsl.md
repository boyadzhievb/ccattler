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
    command "nginx -g 'daemon off;'"
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
        env "LOG_LEVEL" = "info"
        env "PORT" = "8080"
        file "/etc/app/config.yaml" = "key: value"
        file "/usr/share/nginx/html/index.html" from "index.html"
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

#### command

The `command` directive overrides the container's default startup command. Write it as a plain shell string — exactly as you would type it in a terminal.

```hcl
# Simple command
service webserver {
    image "apache/airflow:2.10.5"
    command "airflow webserver"
    instances 1
}

# Complex command with shell features (pipes, &&, variables)
service app {
    image "openjdk:21"
    command "source /init/envfile && exec java -jar -Dspring.profiles.active=ci app.jar --spring.config.location=/opt/app.yaml"
    instances 2
}

# Script execution
service init-worker {
    image "python:3.12"
    command "./scripts/init.py --type base --secrets /mnt/secrets-store/secret"
    instances 1
}
```

Commands are always executed through `sh -c`, so shell features like pipes (`|`), chaining (`&&`), variable expansion (`$VAR`), and subshells work out of the box. There is no array form and no separate `args` field — unlike Kubernetes, CCattler does not distinguish between ENTRYPOINT and CMD. If you omit `command`, the container runs its image default.

For complex startup logic, mount a script and call it — keeps the .cca readable:

```hcl
service app {
    image "openjdk:21"
    command "/opt/scripts/start.sh"
    instances 2
    config {
        file "/opt/scripts/start.sh" from "scripts/start-app.sh"
    }
}
```

Triple-quoted strings (`"""`) support multi-line commands as a fallback when a separate script is overkill:

```hcl
command """
source /init/envfile && \
exec java -jar -Dspring.profiles.active=ci app.jar
"""
```

**Signal handling:** Since commands run inside `sh`, the shell process is PID 1. For clean shutdown, prefix long-running processes with `exec` (e.g., `command "exec java -jar app.jar"`) so the process replaces the shell and receives SIGTERM directly.

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

Configuration block for environment variables and config files. Can appear inside a service block or as a standalone top-level block. The `=` sign between key and value is optional.

Config files are injected into containers via bind mounts — the agent writes the content to a temp directory on the host and mounts it read-only into the container. This is simpler than Kubernetes ConfigMaps, which require creating a separate resource and referencing it in a volume mount. In CCattler, the file content is part of the service declaration.

Two ways to provide file content:

- **Inline**: `file "/path" = "content"` — content is in the DSL string
- **From file**: `file "/path" from "local-file.txt"` — content is loaded from a local file at apply time, relative to the `.cca` file's directory

For multi-line content, use triple-quoted strings (`"""`):

```hcl
config api {
    env "LOG_LEVEL" = "info"
    file "/etc/api/config.yaml" = """
server:
  port: 8080
  log_level: info
database:
  host: db.internal
  pool_size: 10
"""
    file "/etc/api/custom.conf" from "custom.conf"
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
| `command` | service | Container command override (shell string) |
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
| `file` | config | Config file — inline content or `from` local file |
| `from` | file | Load config file content from a local file |
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

## Templates

Any `.cca` or `.ccattler` file can use Go template syntax (`{{ }}`) to parameterize values. Templates are rendered before parsing, so the output must be valid CCattler DSL.

### Values files (`.ccv`)

Values files use CCattler's own `key: value` format (not YAML) — one entry per line, comments with `#`, dot notation for nesting:

```
# airflow/values.ccv
airflow.image: apache/airflow:2.10.5
worker.instances: 3
worker.cpu: 1000m
```

Dot notation creates nested maps: `airflow.image` becomes `{{ .airflow.image }}` in the template.

### Directory convention

The template sits at the top level. A same-name subdirectory holds the values files. The `values.ccv` file inside the subdirectory is the defaults file — it is **auto-loaded** when rendering the template (no `--values` flag needed):

```
airflow.cca                            # template
airflow/                               # values for this template
├── values.ccv                         # defaults (auto-loaded)
├── values-dev.ccv                     # dev overrides
├── values-staging.ccv                 # staging overrides
└── values-prod.ccv                    # production overrides
```

### Rendering

```bash
# Uses only defaults (auto-loaded from airflow/values.ccv)
cca apply airflow.cca

# Defaults + production overrides
cca apply --values airflow/values-prod.ccv airflow.cca

# Preview rendered output
cca render --values airflow/values-prod.ccv airflow.cca

# Override individual values from CLI
cca apply --set worker.instances=10 airflow.cca

# Read value from environment variable
cca apply --values airflow/values-prod.ccv --set-from-env airflow.image=AIRFLOW_TAG airflow.cca
```

**Precedence** (lowest to highest): auto-loaded `values.ccv` < `--values` files (in order) < `--set` < `--set-from-env`.

### Template functions

| Function | Description | Example |
|---|---|---|
| `default` | Fallback if value is empty | `{{ default "3" .worker.instances }}` |
| `required` | Fail if value is missing | `{{ required "image is required" .airflow.image }}` |
| `quote` | Wrap in double quotes | `{{ .airflow.image \| quote }}` |
| `upper` / `lower` | Case conversion | `{{ .env \| upper }}` |
| `ternary` | Conditional value | `{{ ternary "true" "false" .debug }}` |
| `toJson` | Serialize to JSON | `{{ .config \| toJson }}` |
| `indent` / `nindent` | Indent text | `{{ indent 4 .block }}` |

### Example: Airflow across environments

Template at `examples/templates/airflow.cca` — all values come from `airflow/values.ccv` (auto-loaded defaults), environment files only override what differs:

```hcl
service worker {
    image {{ .airflow.image | quote }}
    command "airflow celery worker"
    instances {{ .worker.instances }}
    resources {
        cpu {{ .worker.cpu }}
        memory {{ .worker.memory }}
    }
    scale {
        horizontal {
            min {{ .worker.minInstances }}
            max {{ .worker.maxInstances }}
        }
    }
}
```

Default values in `airflow/values.ccv`:

```
airflow.image: apache/airflow:2.10.5
worker.instances: 3
worker.cpu: 1000m
worker.memory: 2048Mi
worker.minInstances: 1
worker.maxInstances: 10
```

Production overrides in `airflow/values-prod.ccv`:

```
worker.instances: 5
worker.cpu: 2000m
worker.memory: 4096Mi
worker.minInstances: 3
worker.maxInstances: 20
```

Deploy to dev:

```bash
cca apply --values airflow/values-dev.ccv airflow.cca
```

Deploy to prod:

```bash
cca apply --values airflow/values-prod.ccv airflow.cca
```

Override image tag in CI:

```bash
cca apply --values airflow/values-prod.ccv \
  --set-from-env airflow.image=CI_AIRFLOW_IMAGE airflow.cca
```
