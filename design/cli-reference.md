# CLI Reference

Command overview for `cca`. `integration/doc_crosscheck_test.go` verifies every
command listed here exists in `cmd/cca/main.go`. Run `cca --help` for full usage.

## CLI

```
# core
cca apply [--simulate] <file> # send config to POST /api/apply (+ referenced files); --simulate runs in memory
                              # an unreachable API is an error; --store/--endpoints removed (M219)
cca run [--watch] <file>      # start real OS processes
cca run-container [--watch] <file>  # start real Docker containers
cca server [flags]            # start control plane (--secret-root-key <file> | --root-key-provider aws-kms|gcp-kms --root-key-id <id>; --keyring-retire-grace <duration>, default 168h, minimum 1h)
cca agent --node-id <id>      # start node agent (--storage-backend local|nfs --storage-dir <path>)
cca token create/list/revoke  # join tokens via /api/node/join-token (cluster.admin)
cca join <server> <token>     # join node to cluster
cca init [flags]              # initialize cluster

# identity & auth
cca login <server>             # OIDC browser login (or --token <jwt>)
cca logout                     # remove stored credentials
cca whoami                     # show current identity and token expiry
cca user create/token/list/delete  # user account management
cca context list/use/current/delete  # server context management
cca role grant <principal> [flags]   # grant role or capability (--role, --capability, --scope)
cca role revoke <principal> [flags]  # revoke role or capability
cca role list [principal]            # list all grants
cca role check <principal> [flags]   # check if principal has capability

# cluster management
cca status                     # show cluster status (GET /api/status; needs workload.read, filtered to your teams)
cca health                     # control-plane health: store + controller leader, exit 1 if unhealthy
cca describe <type> <name>     # detailed view (service, node, instance, gate)
cca get <resource>             # services, instances, nodes, volumes, networking, secrets, config, endpoints, gates
cca scale <service> <count>
cca logs <service> [--follow]
cca events [--follow] [--service]  # event stream
cca watch [prefix]             # stream fact store changes
cca restart <container-name>   # restart a single instance
cca rollout restart <service>  # rolling restart all instances
cca drain <node-id> [flags]    # gracefully evict instances from a node
cca disable-node <node-id>     # exclude node from new placements
cca enable-node <node-id>      # return disabled node to normal scheduling
cca node-inspect <node-id>     # query agent debug API (via API server)
cca exec <id> -- <command> [args...]   # no shell; -- sh -c '…' for shell syntax
cca images list/pull
cca top [nodes|workloads]
cca metric set <svc> <m> <v>
cca secret set/get/list/delete/rotate   # secret management (M174 changes: see "Secrets (M174)")
cca secret generate-root-key [--path <file>]   # local, on a control-plane node: create the secret root key once (M177)
cca gc instances               # garbage-collect stopped instance facts
cca audit [flags]              # query/stream audit log (--principal, --action, --since, --follow)

# dsl & templates
cca diff [--simulate] <file>   # fact changes from POST /api/diff; --simulate compares with an empty store
cca render [flags] <template>  # render a template with values to stdout
cca fmt [--check] <file>
cca lint <file>

# testing & benchmarks
cca readiness [--json]         # run 16-area readiness report
cca benchmark [--json] [flags] # chaos benchmark with recovery metrics
cca chaos                      # random failure injection
```

## Secrets (M174)

Planned for M174 ([config-secrets.md](config-secrets.md#m174-design-secrets-architecture)). Secret values, ownership and shares are administered here; the DSL only declares references (`secret database.password { mount "…" }`) and consumption.

```
cca secret set <name> [--tenant <t> | --cluster] [--from-file <path>]   # value from stdin or file
cca secret get <name> [--tenant <t> | --cluster]
cca secret list [--tenant <t> | --cluster]
cca secret delete <name> [--tenant <t> | --cluster]
cca secret share <name> --tenant <owner> --with-tenant <consumer>
cca secret unshare <name> --tenant <owner> --with-tenant <consumer>
cca secret migrate-names --plan | --apply <mapping.ccv>
cca secret migrate-keys --confirm-backup     # re-encrypt v1 envelopes (after migration gate)
cca secret retire-legacy-key                 # stop using the old master key (after retirement gate)
cca secret rotate                            # new KEK version, re-wrap (refused until the legacy key is retired)
cca secret rotate-root-key --commit|--finish [--path <file>]  # local, every cca-server stopped, .next staged on every replica (run by rotate-root-key.yml)
cca secret root-key-status [--path <file>]   # local, read-only: stage=idle|staged|committed|invalid
cca secret keyring-status                    # migration + KEK rotation status (unreadable_keys), replica reports
```

Names starting with `route/` are reserved for system gate keys (M176): `cca secret` refuses and hides them; see [config-secrets.md](config-secrets.md).

| Target | Store key | Required permission |
|---|---|---|
| no flag (built-in `default` tenant) | `secrets/tenant/default/{name}` | `secret.write` at `team/default` |
| `--tenant payments` | `secrets/tenant/payments/{name}` | `secret.write` at `team/payments` |
| `--cluster` | `secrets/cluster/{name}` | `secret.write` at `cluster` |

- `--tenant` and `--cluster` are mutually exclusive; omitting both means `default`.
- The value is never a command-line argument (it would land in shell history and the process list).
- `share` writes `desired/secret_share/{owner}/{consumer}/{name}` (`owner` is a tenant or `cluster`) and needs `secret.write` in the owner's scope; `unshare` deletes it. A consuming service still needs `secret.use` in its own scope at apply. Delivery re-checks the share on every request and every container start fetches, so `unshare` stops delivery at the next start, crash restarts included. Planned in M179 (full flag set in `openspec/changes/tenant-scoped-secrets/`).
- `get` prints the value (as today) and needs `secret.read` in the secret's scope; `list` shows metadata only (name, tenant, KEK version, shares).
- `migrate-keys` and `retire-legacy-key` need `cluster.admin`, `rotate` needs `secret.write` at cluster; all three return `409` with the unmet gate conditions.

Examples:

```
printf '%s' "$DB_PASSWORD" | cca secret set database.password --tenant payments
cca secret set registry.credentials --cluster --from-file ./registry.json
cca secret share database.password --tenant payments --with-tenant frontend
```


## Shell Completion

`cca completion bash` or `cca completion zsh` prints a completion script.
