# Domain Language (DSL) Reference

No YAML. A human-oriented declarative language.

## Complete Example

```
service web {
    image nginx:1.27
    instances 3

    expose 8080

    init {
        exec "db-migrate --run"
        timeout 30s
        retry 3
    }

    init {
        exec "cache-warm"
        timeout 10s
    }

    health {
        http /health
        every 10s
    }

    resources {
        cpu 500m
        memory 512Mi
    }

    scale {
        horizontal {
            min 3
            max 30
            target cpu = 60%
            target requests_per_second = 500
            event { source payments.pending target 20 messages/instance }
            schedule { weekdays 08:00-18:00 minimum 10 }
        }
        vertical {
            cpu { min 250m max 4 }
            memory { min 512Mi max 8Gi }
        }
    }

    placement {
        mode per-node
        architecture amd64
        zone spread
        require gpu = true
        prefer region = us-east
        accept dedicated-compute
    }
}

volume database {
    size 100Gi
    persistent true
}

group frontend {
    process proxy
    process web
    share network
    share volume cache
}

role developer {
    allow service.read
    allow service.update
}

grant developer to group developers

policy team-isolation {
    allow service.update
    when subject.team == resource.team
}

config api {
    env "LOG_LEVEL" = "info"
    env "PORT" = "8080"

    file "/etc/api/config.yaml" = "..."
}

secret database.password

service database {
    image "postgres:16"
    instances 1

    secret database.password {
        mount "/run/secrets/database-password"
    }
}

tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
    }
}

network {
    allow frontend/web -> payments/checkout port 443
    deny frontend/web -> payments/database
}

export platform/dns {
    allow frontend
    allow payments
}
```

## Templates and Values

`.cca` files support Go `text/template` syntax for parameterization. Values are
provided via `.ccv` files or CLI flags.

### Values files (.ccv)

Flat `key: value` format with dot-separated keys for nesting:

```
# values.ccv
registry: 192.168.100.215:5000
db.image: postgres:16
db.cpu: 500m
airflow.image: apache/airflow:2.10.5
```

### Using values in .cca files

```
service metadata-db {
    image "{{ .db.image }}"
    resources {
        cpu {{ .db.cpu }}
    }
}
```

The `default` function provides a fallback when a value is not set:

```
service web {
    image "{{ default "docker.io" .registry }}/nginx:1.27"
}
```

### CLI flags

```bash
# Explicit values file (multiple allowed, later overrides earlier)
cca apply --values prod.ccv app.cca

# Inline override (highest precedence after env vars)
cca apply --set registry=192.168.100.43:5000 app.cca

# Override from environment variable
cca apply --set-from-env DB_HOST app.cca

# Preview rendered output without applying
cca apply --dry-run --values prod.ccv app.cca
```

### Auto-discovery

For a template `airflow.cca`, the parser automatically loads
`airflow/values.ccv` from the same directory if it exists — no `--values`
flag needed.

### Available functions

`default`, `required`, `quote`, `upper`, `lower`, `title`, `toJson`, `indent`, `nindent`, `ternary`.

## Compilation

The parser compiles DSL into facts:

```
service(name="web", image="nginx:1.27", instances=3)
exposes(service="web", port=8080)
requires(service="web", cpu=500m, memory=512Mi)
```

**Architectural boundary**: everything above the fact store is human-facing; everything below is machine-facing.
