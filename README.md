# CCattler

**Container Cattler** — a Kubernetes-alternative container orchestrator built around facts, rules, and reconciliation instead of an object hierarchy.

CCattler herds and manages containers by maintaining state and constraints, rather than exposing an object model to the user. No Pods. No ReplicaSets. No YAML. Just declare what you want, and the system makes it so.

## Quick start

```bash
# Install
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash

# See the reconciliation loop in action (simulated — no containers)
cca demo

# Run real OS processes (no container runtime needed)
cca run examples/basic.cca

# Run real OCI containers (requires nerdctl, docker, or lima)
cca run-container examples/basic.cca
```

Single-process modes:

| Command | Runtime | What it does |
|---|---|---|
| `cca apply` / `cca demo` | Simulator | Shows reconciliation output — no real processes or containers |
| `cca run` | Process | Starts real OS processes managed by the reconciler |
| `cca run-container` | Container (nerdctl/docker/lima) | Pulls images, starts real OCI containers |

## Multi-node cluster

A cluster is one control plane (`cca server` with etcd) and an agent (`cca agent`) on each machine. Nodes join with a short-lived token over mTLS:

```bash
cca init                                          # cluster CA and certificates (control plane)
cca server --listen 0.0.0.0:9770 --tls --dns      # controllers + API
cca token create --node-id worker-1 --ttl 15m     # join token
cca join 192.168.1.10:9770 <token> --node-id worker-1   # on the worker
cca agent --node-id worker-1                      # on the worker
cca apply examples/web.cca                        # declare the workload
cca status                                        # see it converge
```

The Ansible playbooks in [deploy/ansible](deploy/ansible) set this up end to end. See [installation](https://ccattler.org/installation) for details.

## Why?

Kubernetes is powerful, but its user-facing abstraction is its internal object model: Deployments, ReplicaSets, Pods, Services, `apiVersion/kind`, and deeply nested YAML serializing implementation details. CCattler asks: what if the user never had to think in those terms?

## How it works

```
        USER
          |
          v
  +---------------+
  |  .cca CONFIG  |    domain language
  +-------+-------+
          |
          v
  +---------------+
  |  FACT STORE   |    etcd
  +-------+-------+
          |
  +-------+--------+
  |       |        |
  v       v        v
sched  network  storage    (controllers watch facts, derive changes)
  |       |        |
  +-------+--------+
          |
          v
  +---------------+
  | NODE AGENTS   |    one per machine, runs containers
  +-------+-------+
          |
          v
      MACHINES
```

The cluster stores **facts** (desired state, observed state, constraints), not objects. Controllers are rule engines that watch facts, compare desired vs actual, and propose changes. Desired and observed state are always kept separate; the difference is the work. The reconciliation loop runs continuously:

```
facts -> rules -> new facts -> actions -> reality -> observations -> facts
```

## Configuration language

No YAML. A human-oriented declarative language (`.cca` files):

```
service web {
    image nginx:1.28
    instances 3

    expose 8080

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
        }
    }

    placement {
        architecture amd64
        zone spread
    }
}

gate web-public {
    host "app.example.com"
    service web port 8080
    tls auto                 # certificate issued and renewed by the cluster CA
}
```

Multi-tenancy, access, and network policies are also declarative:

```
tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
    }
}

role developer {
    allow service.read
    allow service.update
}

network {
    allow frontend/web -> payments/checkout port 443
    deny frontend/web -> payments/database
}
```

`cca fmt` formats a file, `cca lint` validates it, and `cca diff` shows the fact changes an apply would make. Full syntax: [DSL reference](design/dsl-reference.md). Editor support: [VS Code extension](editor/vscode-cca).

## Key differences from Kubernetes

| | Kubernetes | CCattler |
|---|---|---|
| **State model** | API objects (Deployment, ReplicaSet, Pod...) | Facts and relations |
| **Config format** | YAML serializing internal objects | Domain language expressing intent |
| **Extensibility** | CRDs + custom controllers | Typed facts + schemas + rules |
| **Networking** | Label selectors | Identity-based policies |
| **Ingress** | Ingress/Gateway objects + a separate controller | `gate` blocks served by every agent (HTTP and HTTPS by SNI) |
| **Multi-tenancy** | Namespaces (one concept for everything) | Separate ownership, quotas, isolation |
| **Autoscaling** | HPA/VPA/KEDA (separate systems) | Unified scaling engine |
| **Security** | RBAC verbs on API resources | Capabilities on hierarchical scopes, deny by default |

## Architecture

- **Fact store** (etcd) — desired state, observed state, and constraints as key-value facts with transactions, watches, and revisions
- **Controllers** — independent rule engines (instance, scheduler, endpoint, failure, autoscale, network, storage, rollout, gate TLS, …) that communicate only through facts and return proposed changes, committed with optimistic concurrency
- **Scheduler** — pure function: `schedule(requirements, nodes) -> placement`, zone-aware spreading and bin packing
- **Node agents** — observe, reconcile, and report on each machine; run containers through containerd/nerdctl
- **Networking** — VXLAN overlay (`--cluster-cidr`, one /24 per node), service VIPs (`--vip-cidr`) with nftables, iptables, or eBPF data planes, built-in DNS, and the gate proxy for external HTTP/HTTPS
- **Storage** — persistent volumes (local path and NFS, including shared read-write volumes), with volume migration on node failure
- **Security** — mTLS between all components, internal CA, token-based node enrollment, users via local tokens, JWT, or OIDC, capability-based API authorization plus store-layer prefix RBAC, and a persistent audit log
- **Secrets** — envelope-encrypted in etcd; only the control plane holds the key and delivers plaintext to the node running an instance (tmpfs, never on disk). Gate TLS private keys are sealed the same way
- **Multi-tenancy** — tenant ownership, resource quotas, fair scheduling, identity-based network isolation
- **Cloud** — node lifecycle, load balancers, and routes for AWS, GCP, and Azure

## Project structure

```
ccattler/
  lang/           DSL lexer, parser, AST, compiler, formatter
  store/          StateStore interface + in-memory and etcd adapters
  scheduler/      Pure scheduling function
  controllers/    Reconciliation controllers and the runner
  agent/          Node agent (observer, reconciler, reporter, secret and gate-key delivery)
  runtime/        Pluggable runtimes (simulator, process, container)
  network/        Overlay, VIP data planes (nftables, iptables, eBPF), DNS, gate proxy
  storage/        Volume providers (local, NFS, simulator)
  security/       CA, mTLS, enrollment, authorization, secrets, audit
  tenant/         Multi-tenancy, quotas, admission policy gate
  api/            HTTP API server
  cmd/cca/        CLI commands
  types/          Shared fact types, store keys, and constants
  infra/          Infrastructure provider interface (node lifecycle)
  cloud/          AWS, GCP, and Azure cloud controllers
  logging/        Structured logging
  metrics/        Prometheus metrics
  tracing/        W3C trace context propagation
  chaos/          Chaos testing and benchmarks
  integration/    In-process integration tests
  test/e2e/       Multi-VM end-to-end tests
  deploy/         Ansible playbooks, Lima and Vagrant test clusters, Grafana dashboards
  editor/         VS Code extension for .cca files
  website/        Documentation site (ccattler.org)
  design/         Design documents and call graphs
```

## Status

Beta. 175 milestones complete (latest release `v1.74.0-beta`): from the core fact store through distributed state, multi-host deployment, authorization, cloud providers, stateful workloads, gate TLS termination, eBPF data plane, and encrypted secret delivery to nodes. About 2,000 Go test functions. CI runs unit, security, and authentication tests and a 3-VM end-to-end cluster test on every push; a deploy-cluster test runs real workloads (including Airflow) before releases. Work in progress and upcoming milestones: [milestones.md](milestones.md); release notes: [changelog](website/changelog.md).

## Documentation

Full documentation at **[ccattler.org](https://ccattler.org)** — getting started, installation, concepts, guides, CLI reference, DSL grammar, and operations.

## Design documents

- [runtime-invariant-flows.md](design/runtime-invariant-flows.md) — end-to-end flows and the invariants each must keep
- [etcd-schema.md](design/etcd-schema.md) — etcd key layout, consistency model, and worked examples
- [controllers.md](design/controllers.md), [node-agent.md](design/node-agent.md), [networking.md](design/networking.md), [storage.md](design/storage.md)
- [security-model.md](design/security-model.md), [threat-model.md](design/threat-model.md), [config-secrets.md](design/config-secrets.md), [multi-tenancy.md](design/multi-tenancy.md)
- [cli-reference.md](design/cli-reference.md), [dsl-reference.md](design/dsl-reference.md), [TESTING.md](design/TESTING.md)
- [design/graphs/](design/graphs) — function-level call graphs per subsystem

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).
