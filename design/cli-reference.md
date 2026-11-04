# CLI Reference

Command overview for `cca`. `integration/doc_crosscheck_test.go` verifies every
command listed here exists in `cmd/cca/main.go`. Run `cca --help` for full usage.

## CLI

```
# core
cca apply <file>              # deploy config
cca run [--watch] <file>      # start real OS processes
cca run-container [--watch] <file>  # start real Docker containers
cca server [flags]            # start control plane
cca agent --node-id <id>      # start node agent (--storage-backend local|nfs --storage-dir <path>)
cca token create/list/revoke  # bootstrap token management
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
cca status                     # show cluster status
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
cca secret create/get/list     # secret management
cca gc instances               # garbage-collect stopped instance facts
cca audit [flags]              # query/stream audit log (--principal, --action, --since, --follow)

# dsl & templates
cca diff [flags] <file>        # dry-run apply showing fact changes
cca render [flags] <template>  # render a template with values to stdout
cca fmt [--check] <file>
cca lint <file>

# testing & benchmarks
cca readiness [--json]         # run 16-area readiness report
cca benchmark [--json] [flags] # chaos benchmark with recovery metrics
cca chaos                      # random failure injection
```


## Shell Completion

`cca completion bash` or `cca completion zsh` prints a completion script.
