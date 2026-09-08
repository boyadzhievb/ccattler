container config and secrets as two separate but related mechanisms, with the node agent being the only component that turns them into runtime inputs.

1. Config should be declarative and non-secret

A service could declare configuration like:

service api {
    image "my-api:v3"

    config api.config {
        "LOG_LEVEL" = "info"
        "PORT" = "8080"
        "DATABASE_HOST" = "database"
    }
}

The important distinction is that config is desired state, not an imperative command like “set this environment variable.”

The fact store would contain something conceptually like:

desired(service.api.config, {
    LOG_LEVEL: "info",
    PORT: "8080",
    DATABASE_HOST: "database"
})

The scheduler doesn't need to care how that ultimately becomes an environment variable or file.

2. Secrets should be references, never values

For secrets:

secret database.password

service api {
    image "my-api:v3"

    secret database.password
}

The DSL/store should contain:

secret(database.password)
secret_grant(api, database.password)

not:

secret(database.password, "super-secret-password")

The actual secret value belongs in the encrypted secret subsystem.

This fits the security model already described in CLAUDE.md: secrets live in an encrypted store, are scoped to services, and controllers such as the scheduler/network controller should never receive plaintext secrets.

3. The node agent should materialize them

I think this is the key architectural boundary:

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

The node agent should be responsible for converting the declarative facts into whatever the runtime needs.

That means the runtime interface could eventually look conceptually like:

type ContainerSpec struct {
    Image       string
    Environment []EnvironmentVariable
    Mounts      []Mount
    Resources   Resources
}

But the important part is that the controller doesn't construct this.

4. Secrets should preferably be files, not environment variables

I'd support both eventually, but make file-mounted secrets the preferred mechanism:

service api {
    secret database.password {
        mount "/run/secrets/database-password"
    }
}

Then inside the container:

/run/secrets/database-password

contains the secret.

Why prefer this?

avoids exposing secrets through environment inspection
avoids accidental logging of environment variables
makes rotation easier
works naturally with applications that already consume secret files
gives the agent more control over permissions
allows secrets to exist only on the node where they're needed

For example:

/run/secrets/database-password
    owner: application user
    mode: 0400
    memory-backed / ephemeral

The exact implementation could depend on the runtime.

5. Config should support both env and files

I'd make configuration explicitly typed:

config api {
    env "LOG_LEVEL" = "info"

    file "/etc/api/config.yaml" = """
    port: 8080
    log_level: info
    """
}

Then the agent translates this into the runtime-specific mechanism.

So the abstraction becomes:

CCattler configuration
        │
        ├── environment
        ├── config files
        └── secret files
                │
                ▼
           Node Agent
                │
                ▼
       Runtime Adapter
                │
        ┌───────┼────────┐
        ▼       ▼        ▼
   Process   Container  Simulator

This keeps ContainerRuntime, ProcessRuntime, and SimulatorRuntime interchangeable, which is consistent with the existing architecture.

6. Don't put rendered config into the fact store

This is an important design choice.

I would not have:

container_config(instance-123, {
    DATABASE_PASSWORD: "..."
})

Instead:

desired(instance-123, running)

config(api, ...)
secret_grant(api, database.password)

Then the agent resolves the effective configuration when it reconciles the instance.

This preserves the project's principle:

Store facts and desired results, not commands.

And it prevents plaintext secrets from accidentally becoming part of the general-purpose fact database.

7. Secret delivery should be lease/lifecycle aware

I'd also tie secret access to the container lifecycle.

For example:

instance api-7
       │
       ├── assigned to node-3
       │
       ▼
node-3 obtains secret
       │
       ▼
secret materialized locally
       │
       ▼
container starts
       │
       ▼
container stops
       │
       ▼
secret material removed

If the container moves from node-3 to node-5, node 5 gets a fresh authorized copy.

The old node should eventually remove its copy.

That fits particularly well with the project's zero-trust + short-lived credentials + node failure detection direction.

The model I'd recommend for CCattler

I'd make the API roughly:

secret database.password

config api {
    env "LOG_LEVEL" = "info"
    env "PORT" = "8080"

    file "/etc/api/config.yaml" = "..."
}

service api {
    image "my-api:v3"

    config api

    secret database.password {
        mount "/run/secrets/database-password"
    }
}

Internally:

                DSL
                 │
        ┌────────┴─────────┐
        ▼                  ▼
   Config Facts       Secret Grants
        │                  │
        │            encrypted secret
        │                 store
        │                  │
        └────────┬─────────┘
                 ▼
            Node Agent
                 │
        resolve + authorize
                 │
                 ▼
          Runtime Adapter
                 │
                 ▼
             Container

The central rule should be:

Controllers reason about references and desired state. The node agent is responsible for securely materializing config and secrets into the workload.

That gives CCattler a clean separation between intent → authorization → materialization → runtime, and avoids accidentally turning the fact store into a Kubernetes-style object/config dump.