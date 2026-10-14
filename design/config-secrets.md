# Config & Secrets

Config and secrets are two separate mechanisms. Controllers reason about references and desired state. The node agent is the only component that materializes config and secrets into workloads.

## Config

Config is declarative desired state, not imperative "set this environment variable":

```
config api {
    env "LOG_LEVEL" = "info"
    env "PORT" = "8080"

    file "/etc/api/config.yaml" = "..."
}

service api {
    image "my-api:v3"
    config api
}
```

The fact store holds config facts (`desired/service/api/config/...`), not rendered environment variables. The scheduler and controllers don't care how config becomes runtime input — that's the node agent's job.

Config supports two delivery modes:
- **Environment variables** — `env "KEY" = "value"`
- **Config files** — `file "/path" = "content"`

## Secrets

Secrets are references, never values. The fact store holds grants, not plaintext:

```
secret database.password

service api {
    secret database.password {
        mount "/run/secrets/database-password"
    }
}
```

Stored as:
```
secret(database.password)              — secret exists
secret_grant(api, database.password)   — api may access it
```

Never:
```
secret(database.password, "super-secret-password")   — WRONG
```

Actual secret values live in the encrypted secret subsystem (envelope encryption with KMS). The scheduler and network controller never see plaintext secrets.

**File-mounted secrets are preferred over environment variables:**
- Avoids exposing secrets through environment inspection
- Avoids accidental logging of environment variables
- Makes rotation easier (overwrite file, signal process)
- Works with applications that already consume secret files
- Gives the agent control over permissions (`0400`, memory-backed, ephemeral)

## Materialization Boundary

```
             CONTROL PLANE
                   │
      ┌────────────┴────────────┐
      │                         │
 desired config           secret reference
      │                         │
      └────────────┬────────────┘
                   │
             Fact Store
                   │
                   ▼
              Node Agent
                   │
         ┌─────────┴─────────┐
         │                   │
    config resolver     secret resolver
         │                   │
         └─────────┬─────────┘
                   ▼
              Container
```

Don't put rendered config into the fact store. Store `desired(instance, running)` + `config(api, ...)` + `secret_grant(api, database.password)`. The agent resolves effective configuration at reconciliation time.

## Secret Lifecycle

Secret access is tied to container lifecycle:

```
instance api-7 assigned to node-3
  → node-3 obtains authorized secret copy
  → secret materialized locally
  → container starts
  → container stops
  → secret material removed
```

If the container moves from node-3 to node-5, node-5 gets a fresh authorized copy. The old node removes its copy. This fits the zero-trust + short-lived credentials model.

## Connection URL Assembly

See [runtime-invariant-flows.md](runtime-invariant-flows.md) section 9 for the detailed compiler → agent → container flow.
