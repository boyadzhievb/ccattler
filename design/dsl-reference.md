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
        rebalance true            # opt-in: scheduler may move running instances to even out load
    }

    update {
        max_unavailable 1         # expected instances that may be not healthy at once (default 25%, rounded down)
        max_extra 1               # extra new-image instances created ahead of stops (default 25%, rounded up)
        progress_deadline 15m     # no progress this long → roll back (default 10m)
        min_healthy_time 30s      # a new instance counts once it ran this long (default 10s)
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

## Resource Units

`resources { cpu, memory, cpu_limit, memory_limit }` and `scale { vertical { cpu { min max } memory { min max } } }`
use Kubernetes notation. Every component reads a stored value through one parser (`types/resource_quantity.go`), so
it has one meaning everywhere: CPU in millicores, memory in bytes.

| Value | Accepted forms | Meaning |
|---|---|---|
| CPU | `250m` (millicores); `0.5`, `2` (cores) | `250m` = 250m, `0.5` = 500m, **bare `2` = 2 cores = 2000m** |
| Memory | a number with a binary suffix `Ki Mi Gi Ti Pi Ei` (powers of 1024), a decimal suffix `k M G T P E` (powers of 1000), or no suffix | `512Mi` = 536870912 bytes, `1.5Gi` = 1610612736, `1G` = 10⁹, **bare `2048` = 2048 bytes** |

A value finer than 1 millicore or 1 byte is rounded **up**, as in Kubernetes: `cpu 0.0005` = 1m, `1.5m` = 2m,
`memory 1.1Gi` = 1181116007 bytes, `memory 1.5` = 2 bytes. `cca apply` refuses, and writes nothing, when a value is not
one of these forms (the check runs on the value after template substitution). The error names the service, the field
and the value. Refused, for example: `512MB` and `512mi` (unknown suffix), `1e3` (exponent), values that overflow 64
bits, and negative values (a lexer error). `cpu 0` is allowed. A CPU value written without `m` that is 64 cores or more
(`cpu 500`, `cpu { max 4000 }`) is accepted with a warning from `cca apply` and `cca lint`: it is read as cores and was
probably meant as `500m`. Stored `desired/` values are never migrated; re-apply with units.

The scheduler places an instance only on a node whose remaining CPU (millicores) and memory (MiB, the request rounded
up to whole MiB) cover the request; when no node can, the instance is unplaced with `insufficient_capacity`, which
cluster scaling and the rollout reason use. It reads the effective request (from the vertical autoscaler) before the
desired one. A stored value that cannot be read (written before this check existed) leaves new instances of the
service unplaced with `invalid_resource_request`, shown by `cca status`; re-apply the configuration to fix it.
Behaviour: `openspec/specs/resource-requests`; the chain: Flow 10 of `runtime-invariant-flows.md`.

## Rolling Updates

Changing a service's `image` starts a rollout: old-image instances are replaced so that no more than `update { max_unavailable }` of the expected instances are not healthy at once (crash-looping or failed old ones are replaced first, without using the budget), `max_extra` extra new-image instances may run meanwhile, and the rollout rolls back to the previous image when the new image fails (restarts plus failed instances of the rollout's own new instances reach half the desired count, between 3 and 200) or makes no progress for `progress_deadline` (a positive Go duration such as `10m` or `1h30m`; an invalid or zero value is a parse error). A new instance counts (for stopping old instances, progress and finishing) only once it has run for `min_healthy_time` since its last start and, when the service has a readiness probe, is ready (a Go duration of 0 or more, default `10s`; `0s` means running is enough; a negative or invalid value is a parse error). `update { progress_deadline }` compiles to `desired/service/{svc}/update/progress_deadline`. `update { min_healthy_time }` compiles to `desired/service/{svc}/update/min_healthy_time`. Unset `max_unavailable` and `max_extra` take Kubernetes' defaults for the service's kind, each on its own:

| Kind | `max_unavailable` unset | `max_extra` unset | Explicit `max_extra` |
|---|---|---|---|
| stateless | 25 % of the expected instances, rounded down | 25 %, rounded up | used |
| `stateful` | 1 | 0 | ignored, `cca apply` warns (no extra ordinal) |
| `placement { mode per-node }` | 1 | 0 | ignored, `cca apply` warns (no node has room) |

When both end up 0, `max_unavailable` is 1. A rollback (and a stalled one) has no surge and always uses `max_unavailable` of at least 1. So a 1-instance stateless service starts its new instance before stopping the old one, while an explicit `max_unavailable 1` keeps its meaning (the old instance may be stopped first). The surge lasts only while old-image instances remain and never exceeds their number, so a rollout creates one new instance per old one. Only fields written in the block compile to facts. Removing a field from `update {}` does not restore its default: `cca apply` writes facts and prunes only gate facts, so the stored value stays (as do the `max_unavailable 1` / `max_extra 1` an older server compiled for every `update` block) until the service is deleted and applied again; write the value you want explicitly instead. `cca describe service` shows the effective values and which are defaults, forced to 1 or ignored. Setting the image back to the previous image during a rollout is a revert: it has no previous image and never rolls back. Behaviour: `openspec/specs/service-rollout`; the chain: Flow 34 of `runtime-invariant-flows.md`.

## Instance Count Limits

A service's instance count is checked whenever it is set: `instances` and the autoscaler's `scale { horizontal { min / max } }` in a configuration (the largest of the three counts), and `cca scale`. Behaviour is specified in `openspec/specs/service-scaling`, the chain in Flow 31 of `runtime-invariant-flows.md`.

| Count | Result |
|---|---|
| below 500 | accepted |
| 500 to 600 | accepted with a warning that `cca` prints |
| above 600 | refused, unless approved by a `cluster.admin` repeating the exact count: `cca apply --approve-above-limit 700`, `cca scale web 700` (asks to type the count again on a terminal) or `--approve-above-limit 700` |

There is no absolute ceiling. The limit catches mistakes such as `100000` for `100`; it is **not a capacity promise**. Instances beyond what the nodes can hold stay pending, every instance is about 15 facts in etcd, and counts in the tens of thousands are untested and can overload etcd and the controllers. A tenant `quota { instances }` still caps a team's total.

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

Re-applying a config replaces each gate it declares: an option removed from
the gate block (for example `tls` or `sticky`) is removed from the cluster
(`cca diff` lists it as `- key`). Gates the config no longer declares are not
deleted by `cca apply`.

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

The files are read when you run `cca apply`. The certificate is stored as a
fact. The private key is never a fact: after writing the gate, `cca apply`
sends it to the API server (`PUT /api/gate-key`, needs `secret.write` at
cluster scope), which checks that it matches the certificate and stores it
encrypted. If that fails, `cca apply` exits with an error naming the gate.
Relative paths are resolved from the `.cca` file's directory. To rotate a
manual certificate, replace the files and re-apply.

Instead of a key file, the key can be a secret created with `cca secret set`:

```
gate custom-tls {
    host "api.example.com"
    service api port 3000
    tls {
        cert "/etc/cca/certs/api.pem"
        key secret "platform/api-gate-key"
    }
}
```

Use `key secret` for configurations kept in Git: encrypting an inline key on
apply does not make the key file safe to commit. `POST /api/apply` accepts
only `key secret` (it cannot seal a key read from the client's files).

Rules (enforced by the parser and `cca lint`):

- `tls` requires `host`.
- `cert` and `key` must be given together.
- `mode auto` cannot be combined with `cert` or `key`.
- `key secret "name"` takes a valid secret name; `key` is given once.

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
