# Ecosystem comparison

CCattler is a container orchestrator. It makes scheduling decisions and runs workloads directly on machines, placing it in the same category as Kubernetes, Docker Swarm, and HashiCorp Nomad -- not management layers like Portainer or Rancher.

## Where CCattler fits

| | What it is | Makes scheduling decisions? | Runs workloads? |
|---|---|---|---|
| **Portainer** | Management UI / control layer | No | No |
| **Rancher** | Kubernetes management layer | No | No |
| **Kubernetes** | Orchestrator | Yes | Yes |
| **Docker Swarm** | Orchestrator | Yes | Yes |
| **Nomad** | Orchestrator | Yes | Yes |
| **CCattler** | Orchestrator | Yes | Yes |

Portainer and Rancher provide operational interfaces over existing orchestrators. The underlying orchestrator still makes scheduling and lifecycle decisions. CCattler replaces that underlying orchestrator entirely.

## Orchestrator comparison

| | Kubernetes | Docker Swarm | Nomad | CCattler |
|---|---|---|---|---|
| **Primary abstraction** | API objects | Services / tasks | Jobs / allocations | Facts / relations |
| **Configuration** | YAML | Compose / CLI | HCL | CCattler DSL |
| **Reconciliation** | Multiple independent controllers | Manager reconciliation | Scheduler + client model | Unified reconciliation engine |
| **Networking** | CNI plugins + NetworkPolicy | Built-in overlay | Consul integration | Identity-based policies via facts |
| **Storage** | CSI driver ecosystem | Docker volume plugins | Plugin system | Storage controller + fact model |
| **Autoscaling** | HPA + VPA + KEDA (3 systems) | Limited | Autoscaler plugin | Unified scaling engine |
| **Multi-tenancy** | Namespaces + third-party policies | Limited | Namespaces + ACLs | Built-in tenants, quotas, isolation |
| **Security model** | RBAC + ecosystem add-ons | Swarm mutual TLS | ACLs + Consul | RBAC + ABAC + mTLS + identity policies |
| **Configuration language** | YAML serializing internal objects | YAML / Compose | HCL | Domain language expressing intent |

## The key architectural difference

### Objects vs facts

Kubernetes represents the world as **typed API objects** organized in a hierarchy:

```
Deployment → ReplicaSet → Pod → Container
```

Each layer has its own controller. A Deployment controller manages ReplicaSets. A ReplicaSet controller manages Pods. The scheduler places Pods on nodes. This creates a chain of resources and controllers.

CCattler represents the world as **facts about desired and observed reality**:

```
desired/service/web/instances = "3"
observed/instance/web-1/state = "running"
observed/instance/web-2/state = "running"
```

The gap between desired and observed is the work. There is no object hierarchy -- controllers read facts, compute what's missing, and write new facts.

**Kubernetes says:** "The cluster is a collection of typed resources and controllers operating on those resources."

**CCattler says:** "The cluster is a collection of facts about desired and observed reality, and rules derive what needs to happen."

### One reconciliation engine

Kubernetes is deliberately modular. The scheduler, networking, storage, autoscaling, and policy are all separate controllers with separate APIs, separate configuration, and separate failure modes. Production use requires assembling 10+ tools.

```
          Kubernetes API
               |
     +---------+---------+
     |         |         |
  Scheduler  Controllers  Operators
     |         |         |
     +----+----+---------+
          |
    separate subsystems:
    networking / storage /
    autoscaling / policy / ...
```

CCattler routes all capabilities through a single reconciliation engine. Scheduling, scaling, networking, storage, and policy are all controllers watching facts in the same store, executed in the same loop.

```
            Facts
              |
        +-----------+
        | Reconcile |
        |  engine   |
        +-----+-----+
              |
    +---------+---------+
    |         |         |
scheduling networking storage
    |         |         |
    +---------+---------+
              |
           Actions
```

This means there is no gap between "core CCattler" and "production CCattler." The production capabilities -- autoscaling, mTLS, secrets, network policy, multi-tenancy -- are the core.

## Management layers vs orchestrators

A common question is how CCattler compares to Portainer or Rancher. The answer is: it doesn't. They solve different problems.

**Portainer / Rancher ask:** "How can I manage Kubernetes, Swarm, or other orchestrators more easily?"

**Kubernetes / Swarm / Nomad ask:** "How do I orchestrate workloads across machines?"

**CCattler asks:** "What if we redesigned the orchestration engine itself around facts, declarative intent, and a unified reconciliation loop?"

```
    Portainer / Rancher              CCattler
          |                             |
    +-----+-----+              +-------+-------+
    |     |     |              | facts + rules  |
    K8s  Swarm Podman          | + reconcile    |
    |     |     |              +-------+-------+
  workloads  workloads                 |
                               node agents
                                       |
                              +--------+--------+
                              |        |        |
                            host     host     host
```

Portainer layers on top of an existing orchestrator. CCattler is the orchestrator.
