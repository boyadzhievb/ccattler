# CCattler

**Container Cattler** — a Kubernetes-alternative container orchestrator built around facts, rules, and reconciliation instead of an object hierarchy.

CCattler herds and manages containers by maintaining state and constraints, rather than exposing an object model to the user. No Pods. No ReplicaSets. No YAML. Just declare what you want, and the system makes it so.

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

Early development. The fact store interface and in-memory implementation are complete with tests. See [CLAUDE.md](CLAUDE.md) for the full design and implementation roadmap.

## Design documents

- [CLAUDE.md](CLAUDE.md) — Complete architecture, design philosophy, and phased implementation plan
- [etcd-schema.md](etcd-schema.md) — etcd key layout, consistency model, and worked examples
- [design.md](design.md) — Detailed system design (store, controllers, node agents, API)
- [auths.md](auths.md) — Security model (mTLS, CA, RBAC, ABAC, secrets, audit)
- [tenancy.md](tenancy.md) — Multi-tenancy (ownership, quotas, isolation, shared services)
- [autoscaliing.md](autoscaliing.md) — Unified autoscaling engine
- [miniccat.md](miniccat.md) — Laptop-first development strategy

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).
