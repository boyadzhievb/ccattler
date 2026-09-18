# CCattler

**Container Cattler** — a Kubernetes-alternative container orchestrator built around facts, rules, and reconciliation instead of an object hierarchy.

CCattler herds and manages containers by maintaining state and constraints, rather than exposing an object model to the user. No Pods. No ReplicaSets. No YAML. Just declare what you want, and the system makes it so.

## Quick start

```bash
# Install
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash

# See the reconciliation loop in action (simulated — no containers)
cca demo

# Run real OCI containers (requires containerd + nerdctl)
cca run-container examples/basic.ccattler

# Run real OS processes (no containerd needed)
cca run examples/basic.ccattler
```

CCattler has three runtime modes:

| Command | Runtime | What it does |
|---|---|---|
| `cca apply` / `cca demo` | Simulator | Shows reconciliation output — no real processes or containers |
| `cca run` | Process | Starts real OS processes managed by the reconciler |
| `cca run-container` | Container (nerdctl/containerd) | Pulls images, starts real OCI containers |

The container runtime uses **nerdctl** (the containerd CLI) to pull images and manage containers. Docker Desktop includes containerd, or you can install containerd + nerdctl standalone.

## Why?

Kubernetes is powerful, but its user-facing abstraction is its internal object model: Deployments, ReplicaSets, Pods, Services, `apiVersion/kind`, and deeply nested YAML serializing implementation details. CCattler asks: what if the user never had to think in those terms?

## How it works

```
        USER
          |
          v
  +---------------+
  | DOMAIN LANG.  |    .ccl files
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
sched  network  storage    (controllers watch facts, derive actions)
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

The cluster stores **facts** (desired state, observed state, constraints), not objects. Controllers are rule engines that watch facts, compare desired vs actual, and derive actions. The reconciliation loop runs continuously:

```
facts -> rules -> new facts -> actions -> reality -> observations -> facts
```

## Configuration language

No YAML. A human-oriented declarative language:

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
```

Multi-tenancy, security, and network policies are also declarative:

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

## Key differences from Kubernetes

| | Kubernetes | CCattler |
|---|---|---|
| **State model** | API objects (Deployment, ReplicaSet, Pod...) | Facts and relations |
| **Config format** | YAML serializing internal objects | Domain language expressing intent |
| **Extensibility** | CRDs + custom controllers | Typed facts + schemas + rules |
| **Networking** | Label selectors | Identity-based policies |
| **Multi-tenancy** | Namespaces (one concept for everything) | Separate ownership, quotas, isolation |
| **Autoscaling** | HPA/VPA/KEDA (separate systems) | Unified scaling engine |
| **Security** | RBAC on API resources | RBAC + ABAC on fact prefixes |

## Architecture

- **Fact Store** (etcd) — desired state, observed state, and constraints as key-value facts with transactions, watches, and revisions
- **Controllers** — independent rule engines (instance, endpoint, failure, autoscale, network, storage) that communicate only through state, never through each other
- **Scheduler** — pure function: `schedule(requirements, nodes) -> placement`
- **Node Agents** — observer/reconciler/reporter on each machine, running containers via containerd
- **Unified Scaling Engine** — horizontal, vertical, event-driven, and scheduled scaling through one mechanism
- **Security** — mTLS everywhere, internal CA, RBAC + ABAC, per-controller least privilege, zero-trust
- **Multi-tenancy** — tenant ownership, resource quotas, fair scheduling, identity-based network isolation

## Project structure

```
ccattler/
  lang/           DSL lexer, parser, AST, compiler
  store/          StateStore interface + in-memory + etcd adapters
  scheduler/      Pure scheduling function
  controllers/    Instance, endpoint, failure, autoscale, network, storage
  agent/          Node agent (observer, reconciler, reporter)
  api/            HTTP/gRPC server
  cli/            CLI tool
  types/          Shared fact types and constants
```

## Status

20 milestones complete — from the core fact store through distributed state, multi-host deployment, VIP data plane, node enrollment, and service networking with DNS and placement constraints. 516+ tests across 14 packages. Deployed and tested on real multi-host clusters.

## Documentation

Full documentation at **[ccattler.org](https://ccattler.org)** — getting started, installation, concepts, guides, CLI reference, DSL grammar, and operations.

## Design documents

- [CLAUDE.md](CLAUDE.md) — Complete architecture, design philosophy, and phased implementation plan
- [etcd-schema.md](etcd-schema.md) — etcd key layout, consistency model, and worked examples

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).
