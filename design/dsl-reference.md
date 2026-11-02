# Domain Language (DSL) Reference

No YAML. A human-oriented declarative language.

## Complete Example

```
service web {
    image nginx:1.27
    instances 3
    priority 500

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
        zone spread maxSkew 2
        require gpu = true
        prefer region = us-east
        accept dedicated-compute
        colocate "redis"
        separate "logging"
    }
}

volume database {
    size 100Gi
    persistent true
    type local
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

gate web-public {
    host "app.example.com"
    path "/"
    service web port 8080
    sticky cookie
    tls auto
}
```

## Gates

A `gate` exposes a service to external HTTP traffic. It matches requests by
host and path prefix and forwards them to the target service's healthy
instances.

```
gate web-public {
    host "app.example.com"       # match by Host header (optional; required with tls)
    path "/api"                  # match by URL path prefix (optional)
    service web port 8080        # target service and port (required)

    sticky cookie {              # optional session affinity
        name "session"           # default: __CCA_STICKY_{service}
        secure true
        http-only true
        same-site "strict"       # strict, lax, none
    }

    tls auto                     # optional TLS termination
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `service <name> port <n>` | yes | Target service and port |
| `host "<hostname>"` | with `tls` | Host header match; also the certificate hostname |
| `path "<prefix>"` | no | URL path prefix match |
| `sticky cookie [{ ... }]` | no | L7 cookie affinity via the agent proxy |
| `sticky source-ip` | no | L4 client-IP affinity, no proxy |
| `tls auto` / `tls { ... }` | no | Terminate HTTPS at the gate |

Without `sticky` or `tls`, a gate is pure L4 DNAT with round-robin across
backends.

### TLS termination

A gate with `tls` serves HTTPS on every node's gate HTTPS listener (agent
`--proxy-tls-listen`, default port 443). The listener picks the gate's
certificate from the client's SNI and routes by Host to the gate's service,
forwarding plain HTTP to healthy instances (least connections, or the sticky
backend for `sticky cookie`, with the cookie marked `Secure`). On the plain
gate proxy port, requests for that host get a 308 redirect to `https://`.

The service itself is unchanged: in-cluster clients keep calling the service
name or VIP over plain HTTP. Several gates (hosts) may target the same service
port. Unknown SNI fails the handshake; a Host that belongs to a different gate
than the SNI gets 421.

**Auto mode** — the cluster CA issues a certificate for `host`:

```
gate web-tls {
    host "secure.example.com"
    service web port 8080
    tls auto                     # shorthand for: tls { mode auto }
}
```

Certificates are valid for 90 days. The controller re-issues them 30 days
before expiry. The certificate's only DNS name is `host`, so wildcard
certificates are not supported. Clients must trust the cluster CA.

**Manual mode** — provide your own PEM certificate and key:

```
gate custom-tls {
    host "api.example.com"
    service api port 3000
    tls {
        cert "/etc/cca/certs/api.pem"
        key  "/etc/cca/certs/api-key.pem"
    }
}
```

The files are read when you run `cca apply`, and their contents
are stored in the fact store. Relative paths are resolved from the `.cca`
file's directory. To rotate a manual certificate, replace the files and
re-apply.

Rules (enforced by the parser and `cca lint`):

- `tls` requires `host`.
- `cert` and `key` must be given together.
- `mode auto` cannot be combined with `cert` or `key`.

### Inspecting gates

```bash
$ cca get gates
NAME        HOST                PATH  SERVICE        TLS             EXPIRES     BACKENDS
web-public  app.example.com     /     web:8080       auto            2027-01-31  3
custom-tls  api.example.com     -     api:3000       manual          2027-06-01  2
web-tls     secure.example.com  -     web:8080       auto (pending)  -           3

$ cca describe gate web-public    # routing, TLS mode, cert host and expiry, backends
```

`auto (pending)` means the controller has not issued the certificate yet.
Key material never appears in `get`, `describe`, or `-o json` output.

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
cca apply --set registry=192.168.100.49:5000 app.cca

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
