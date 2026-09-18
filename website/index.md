---
layout: home

hero:
  name: CCattler
  text: Container orchestration, redesigned.
  tagline: A declarative container management system built on facts, relations, desired state, and reconciliation — not objects and YAML.
  actions:
    - theme: brand
      text: Get Started
      link: /getting-started
    - theme: alt
      text: GitHub
      link: https://github.com/boyadzhievb/ccattler

features:
  - title: Facts, not objects
    details: The cluster stores facts and relations — not mutable API objects. No Pods, no ReplicaSets, no Deployments. Just desired state, observed state, and the gap between them.
  - title: No YAML
    details: A purpose-built declarative language for expressing intent. No apiVersion/kind boilerplate, no templating hacks, no helm charts.
  - title: One reconciliation loop
    details: Scheduling, scaling, networking, storage — every capability runs through a single principled engine. Controllers communicate only through state.
  - title: Zero-trust security
    details: mTLS everywhere, internal CA with auto-rotation, RBAC + ABAC, per-controller least privilege, identity-based network policies.
  - title: Unified autoscaling
    details: Horizontal, vertical, event-driven, and scheduled scaling through one mechanism. No separate HPA/VPA/KEDA systems.
  - title: Multi-tenancy built in
    details: Tenant ownership, resource quotas, fair scheduling, and identity-based isolation from day one — not bolted on.
---

## How it works

```
        USER
          |
          v
  +---------------+
  | DOMAIN LANG.  |    .ccattler files
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
facts → rules → new facts → actions → reality → observations → facts
```

## CCattler vs Kubernetes

| | Kubernetes | CCattler |
|---|---|---|
| **State model** | API objects (Deployment, ReplicaSet, Pod...) | Facts and relations |
| **Config format** | YAML serializing internal objects | Domain language expressing intent |
| **Extensibility** | CRDs + custom controllers | Typed facts + schemas + rules |
| **Networking** | Label selectors | Identity-based policies |
| **Multi-tenancy** | Namespaces (one concept for everything) | Separate ownership, quotas, isolation |
| **Autoscaling** | HPA/VPA/KEDA (separate systems) | Unified scaling engine |
| **Security** | RBAC on API resources | RBAC + ABAC on fact prefixes |

## Configuration language

```hcl
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
