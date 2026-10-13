# DSL Connection Block

## Problem

Applications that connect to databases or message brokers need connection strings
assembled from host, port, credentials, and driver information. Today, CCA files
must encode this as shell commands in `_CMD` environment variables:

```
env AIRFLOW__DATABASE__SQL_ALCHEMY_CONN_CMD "bash -c 'echo postgresql+psycopg2://airflow:$(cca-secret airflow.db-password)@metadata-db:5432/airflow'"
```

This is fragile (depends on the target application's command execution model),
hard to read, and leaks implementation details into the service definition.

Kubernetes Helm charts solve this by constructing the connection string from
structured fields in `values.yaml` and injecting it via a Secret object.
CCattler should offer the same convenience natively.

## Design

A new top-level `connection` block declares a database or service connection
using structured fields. The CCattler agent assembles the connection string
at runtime and mounts it as a secret file — the password never appears in
plain config.

### Syntax

```cca
connection <name> {
    driver <driver-string>
    user <username>
    secret <secret-name>
    host <hostname>
    port <port>
    database <dbname>
}
```

All fields except `driver` and `host` are optional. The assembled URL follows
the standard format:

```
driver://user:password@host:port/database
```

When `user` is omitted, the URL has no credentials segment. When `secret` is
omitted, only the user (no password) appears. When `port` is omitted, the
driver default is used. When `database` is omitted, no path segment is added.

### Service Reference

Services reference a connection the same way they reference secrets:

```cca
service webserver {
    connection <name> "<mount-path>"
}
```

Default mount path: `/run/connections/<name>`. The agent writes the assembled
connection string (with the decrypted password) to that path, read-only.

### Examples

#### Airflow with PostgreSQL + Redis

```cca
connection airflow-db {
    driver "postgresql+psycopg2"
    user "airflow"
    secret airflow.db-password
    host "metadata-db"
    port 5432
    database "airflow"
}

connection airflow-celery-backend {
    driver "db+postgresql"
    user "airflow"
    secret airflow.db-password
    host "metadata-db"
    port 5432
    database "airflow"
}

service metadata-db {
    image postgres:16
    instances 1
    expose 5432
    config {
        env POSTGRES_DB "airflow"
        env POSTGRES_USER "airflow"
        env POSTGRES_PASSWORD_FILE "/run/secrets/db-password"
    }
    secret airflow.db-password "/run/secrets/db-password"
    health {
        tcp
        every 10s
    }
}

service redis {
    image redis:7
    instances 1
    expose 6379
    health {
        tcp
        every 10s
    }
}

service webserver {
    image "apache/airflow:2.10.5"
    command "webserver"
    instances 1
    expose 8080
    config {
        env AIRFLOW__CORE__EXECUTOR "CeleryExecutor"
        env AIRFLOW__DATABASE__SQL_ALCHEMY_CONN_SECRET "/run/connections/airflow-db"
        env AIRFLOW__CELERY__BROKER_URL "redis://redis:6379/0"
        env AIRFLOW__CELERY__RESULT_BACKEND_SECRET "/run/connections/airflow-celery-backend"
        env AIRFLOW__CORE__LOAD_EXAMPLES "true"
        env _AIRFLOW_DB_MIGRATE "true"
        env _AIRFLOW_WWW_USER_CREATE "true"
        env _AIRFLOW_WWW_USER_PASSWORD_CMD "cca-secret airflow.admin-password"
    }
    connection airflow-db "/run/connections/airflow-db"
    connection airflow-celery-backend "/run/connections/airflow-celery-backend"
    secret airflow.admin-password "/run/secrets/admin-password"
    health {
        tcp
        every 15s
    }
}

service scheduler {
    image "apache/airflow:2.10.5"
    command "scheduler"
    instances 1
    config {
        env AIRFLOW__CORE__EXECUTOR "CeleryExecutor"
        env AIRFLOW__DATABASE__SQL_ALCHEMY_CONN_SECRET "/run/connections/airflow-db"
        env AIRFLOW__CELERY__BROKER_URL "redis://redis:6379/0"
        env AIRFLOW__CELERY__RESULT_BACKEND_SECRET "/run/connections/airflow-celery-backend"
    }
    connection airflow-db "/run/connections/airflow-db"
    connection airflow-celery-backend "/run/connections/airflow-celery-backend"
    health {
        tcp
        every 30s
    }
}

service worker {
    image "apache/airflow:2.10.5"
    command "celery worker"
    instances 2
    expose 8793
    config {
        env AIRFLOW__CORE__EXECUTOR "CeleryExecutor"
        env AIRFLOW__DATABASE__SQL_ALCHEMY_CONN_SECRET "/run/connections/airflow-db"
        env AIRFLOW__CELERY__BROKER_URL "redis://redis:6379/0"
        env AIRFLOW__CELERY__RESULT_BACKEND_SECRET "/run/connections/airflow-celery-backend"
    }
    connection airflow-db "/run/connections/airflow-db"
    connection airflow-celery-backend "/run/connections/airflow-celery-backend"
    health {
        tcp
        every 15s
    }
}
```

Compare with the old approach where each service needed:
```cca
env AIRFLOW__DATABASE__SQL_ALCHEMY_CONN_CMD "bash -c 'echo postgresql+psycopg2://airflow:$(cca-secret airflow.db-password)@metadata-db:5432/airflow'"
```

#### Minimal example

```cca
connection app-db {
    driver "postgres"
    user "app"
    secret app.db-password
    host "database"
    port 5432
    database "myapp"
}

service api {
    image "myapp:v3"
    instances 3
    expose 8080
    connection app-db
}
```

The agent writes `postgres://app:<password>@database:5432/myapp` to
`/run/connections/app-db` inside the container.

## Fact Model

A connection block compiles to a single JSON fact:

```
desired/connection/<name> = {
    "driver": "postgresql+psycopg2",
    "user": "airflow",
    "secret": "airflow.db-password",
    "host": "metadata-db",
    "port": 5432,
    "database": "airflow"
}
```

A service connection reference compiles to:

```
desired/service/<svc>/connection/<name> = "/run/connections/airflow-db"
```

The agent scans `desired/service/<svc>/connection/` to discover connection
mounts, looks up the connection definition, retrieves the password secret,
assembles the URL, and writes it to the mount path.

## Implementation

| Layer | File | Change |
|-------|------|--------|
| AST | `lang/ast.go` | `ConnectionDecl` struct; `Connections` field on `ServiceDecl`; top-level `ConnectionBlock` on `File` |
| Parser | `lang/parser.go` | `parseConnectionBlock()` for top-level; `parseConnectionReference()` inside service; add to `parseServiceField` switch |
| Compiler | `lang/compiler.go` | `compileConnectionBlock()` → JSON fact; `compileServiceConnectionFacts()` → mount path facts |
| Fact keys | `types/keys.go` | `KeyDesiredConnection`, `ScanDesiredConnections`, `KeyDesiredServiceConnection`, `ScanDesiredServiceConnections` |
| Agent | `agent/agent.go` | `materializeConnectionFiles()` — retrieve secret, assemble URL, write to mount path (parallel to `materializeSecretFiles`) |
| Tests | `lang/parser_test.go`, `lang/compiler_test.go` | Parse and compile round-trip tests |

## Security

- The password is never stored in the connection fact — only a reference to
  the secret name. The agent decrypts at materialization time.
- The assembled connection string is written to a file with mode 0400 in a
  tmpfs, identical to how secrets are materialized today.
- Connection files are removed when the container stops, following the same
  lifecycle as secret files.

## Credential Probe (Init Gate)

### Problem

Stateful services like PostgreSQL bake credentials into their data directory
on first initialization. If the secret value changes later (rotation, redeployment
with a different password), the new password propagates to consumer containers
but the database still holds the old one on disk. The result: silent crash loops
with no clear error — containers restart every few minutes and logs are empty
because the application fails before producing output.

This is not a config distribution bug — CCattler delivers the secret correctly.
The mismatch is between what the secret store holds and what the database engine
accepted during `initdb`. No amount of connection string fixing helps when the
password itself is wrong.

### Design

A **credential probe** validates that a connection actually works as an init
step — before the container starts, not as a recurring health check. If the
password is correct at init time, it stays correct. If it's wrong, there's no
point starting the container at all.

The probe is declared inside an `init` block, not a `health` block:

```cca
service webserver {
    init {
        connection airflow-db
        timeout 30s
        retry 5
    }
}
```

The agent assembles the connection string from the `connection` block (same as
file materialization), then attempts a protocol-level connect + authenticate
before starting the main workload. This fits naturally into the existing init
step pipeline — sequential execution, timeout, retry with backoff.

If the probe fails after all retries, the instance stays in `init-failed`
state with a clear reason:

```
instance webserver-a8f31: init failed: connection airflow-db: password authentication failed for user "airflow"
```

The container never starts. No crash loop. No empty logs. The error is
immediately visible in `cca get instances` and `cca status`.

### Why Init, Not Health

| Concern | Init gate | Health check |
|---------|-----------|-------------|
| Wrong password | Caught before start, clear error | Container starts, crashes, restarts, crashes... |
| Backend not yet ready | Retries with backoff until ready | Container starts too early, crashes |
| Ongoing network partition | Not the right tool (can't predict) | Right tool — remove from endpoints |
| Credential rotation mid-run | Not the right tool | Would detect but can't fix |

The credential probe solves the **precondition** problem: "can this service
reach its backend before we bother starting it?" This is the same category
as `airflow db check` — a prerequisite, not a liveness signal.

For ongoing connectivity monitoring, the existing TCP/HTTP health checks
are sufficient — they detect when a running service loses its backend and
remove it from the endpoint set.

### Supported Drivers

The probe performs a lightweight connect-and-disconnect based on the driver
prefix:

| Driver prefix | Probe method |
|---------------|-------------|
| `postgres`, `postgresql` | `pgx.Connect()` → close |
| `mysql` | `sql.Open("mysql", ...)` → `Ping()` → close |
| `redis` | `AUTH` command → close |
| (other) | TCP connect to host:port (graceful fallback) |

Unknown drivers fall back to TCP — a weaker check, but still better than
silent failure. Driver-specific probes are opt-in: only compiled when the
corresponding Go driver is imported.

### Init Lifecycle

```
instance created
    → init step 1: connection airflow-db (credential probe)
        → success: proceed to next init step
        → failure + retries left: wait with backoff, retry
        → failure + no retries: init-failed, container never starts
    → init step 2: exec "airflow db check" (existing)
    → init step 3: exec "airflow db migrate" (existing)
    → all init steps pass
    → container starts
    → health probes begin (tcp, http — existing)
```

The credential probe is just another init step. It runs in order with other
init steps, respects the same timeout and retry semantics, and reports
success/failure through the same `init_phase` fact. No new lifecycle concept
needed.

### Example: Full Airflow Stack

```cca
connection airflow-db {
    driver "postgresql+psycopg2"
    user "airflow"
    secret airflow.db-password
    host "metadata-db"
    port 5432
    database "airflow"
}

service metadata-db {
    image postgres:16
    instances 1
    expose 5432
    config {
        env POSTGRES_DB "airflow"
        env POSTGRES_USER "airflow"
        env POSTGRES_PASSWORD_FILE "/run/secrets/db-password"
    }
    secret airflow.db-password "/run/secrets/db-password"
    health {
        tcp
        every 10s
    }
}

service webserver {
    image "apache/airflow:2.10.5"
    command "webserver"
    instances 1
    expose 8080
    connection airflow-db "/run/connections/airflow-db"
    config {
        env AIRFLOW__DATABASE__SQL_ALCHEMY_CONN_SECRET "/run/connections/airflow-db"
    }
    init {
        connection airflow-db
        timeout 30s
        retry 5
    }
    init {
        exec "airflow db check"
        timeout 120s
        retry 20
    }
    health {
        http /health
        every 15s
    }
}
```

If someone rotates `airflow.db-password` without running `ALTER USER` on
PostgreSQL, the credential probe catches it at init time:

```
$ cca get instances
SERVICE      ID              NODE     STATE        INIT
metadata-db  metadata-db-1   node-1   running      complete
webserver    webserver-a8f31  node-2   init-failed  connection:airflow-db authentication failed
```

Compare with the current behavior: webserver crash-loops with empty logs,
`cca get instances` shows `running` → `failed` → `running` → `failed`,
and the operator must SSH into the node and manually test the connection
to discover the password mismatch.

### Implementation

| Layer | File | Change |
|-------|------|--------|
| AST | `lang/ast.go` | `ConnectionName` field in `InitStepDecl` (mutually exclusive with `Exec`) |
| Parser | `lang/parser.go` | `"connection"` case in `parseInitStep` (alongside existing `"exec"`) |
| Compiler | `lang/compiler.go` | Compile to `desired/service/<svc>/init/<index>` with `type: connection` |
| Init runner | `agent/init.go` | `runConnectionInitStep()` — assemble URL, driver-specific connect |
| Drivers | `agent/probe_postgres.go`, `agent/probe_mysql.go` | Protocol-specific authentication checks |
| Agent | `agent/agent.go` | Wire connection init steps into the existing init pipeline |

### Interaction with Connection Block

The credential probe and the connection block share the same `connection`
definition. The probe does not need its own host/port/user/password fields —
it reads them from the named connection. This means:

- One definition, two uses: file materialization + init validation
- If the connection definition changes, both the mounted file and the probe
  update automatically
- No risk of the probe checking different credentials than the application uses

## Future Extensions

- **Options field**: `options { sslmode "require", connect_timeout "10" }` →
  appended as query parameters: `?sslmode=require&connect_timeout=10`
- **URI templates**: For non-standard connection formats, allow a template:
  `template "jdbc:postgresql://{host}:{port}/{database}?user={user}&password={secret}"`
- **Secret rotation hooks**: A `rotate` block on secrets that runs a command
  when the secret value changes (e.g., `ALTER USER ... WITH PASSWORD`),
  solving the root cause rather than just detecting the symptom
