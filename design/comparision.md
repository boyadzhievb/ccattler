You're right — I can access **CCattler** now. The distinction is actually quite interesting, because **CCattler is not a management layer like Portainer/Rancher; it is itself an orchestrator**. Its design is also substantially different from Kubernetes, Swarm, and Nomad.

### The simplest way to see it



Think of the stack like this:

| | What it is | Makes scheduling decisions? | Runs workloads? |
|---|---|---:|---:|
| **Portainer** | Management UI/control layer | ❌ | ❌ |
| **Rancher** | Kubernetes management layer | ❌* | ❌* |
| **Kubernetes** | Orchestrator | ✅ | ✅ |
| **Docker Swarm** | Orchestrator | ✅ | ✅ |
| **Nomad** | Orchestrator | ✅ | ✅ |
| **CCattler** | **Orchestrator** | **✅** | **✅** |

\* Rancher can provision/manage Kubernetes clusters, but the Kubernetes control plane underneath performs workload orchestration.

Portainer's own article makes essentially this distinction: Kubernetes, Swarm and Nomad are the **orchestrators**, while Portainer/Rancher are **orchestration-management platforms layered on top**. [Portainer](https://www.portainer.io/blog/container-orchestration-platforms)

## So what is unusual about CCattler?

CCattler's pitch is not "we made another UI for Kubernetes."

It says:

> **"Container orchestration, redesigned."**

And its fundamental design choice is to replace the Kubernetes-style **object/control-loop model** with a **facts + relations + rules + reconciliation** model. [ccattler.org](https://ccattler.org/)

### Kubernetes

Conceptually:

```text
        Desired state
             ↓
       API objects
             ↓
    ┌─────────────────┐
    │ Controllers     │
    │ Scheduler       │
    │ Operators       │
    └────────┬────────┘
             ↓
          Actions
             ↓
           Nodes
```

Kubernetes represents things as objects:

```text
Deployment
    ↓
ReplicaSet
    ↓
Pods
```

The scheduler decides where Pods go, controllers continuously reconcile objects, and other controllers handle networking, storage, autoscaling, etc.

CCattler explicitly says it has **no Pods, ReplicaSets or Deployments**. Instead, its cluster stores facts and relations. [ccattler.org](https://ccattler.org/)

### CCattler

Its conceptual model is:

```text
              .ccattler
                 │
                 ▼
          ┌─────────────┐
          │  Fact Store  │
          │    (etcd)    │
          └──────┬──────┘
                 │
          facts + relations
                 │
       ┌─────────┼─────────┐
       ▼         ▼         ▼
    scheduler  network   storage
       │         │         │
       └─────────┼─────────┘
                 ▼
            Node Agents
                 │
                 ▼
             Machines
```

The key loop is:

```text
facts
  ↓
rules
  ↓
new facts
  ↓
actions
  ↓
reality
  ↓
observations
  ↓
facts
  ↺
```

That's CCattler's central architectural idea. [ccattler.org](https://ccattler.org/)

---

# The really important difference: "objects" vs "facts"

This is probably the most significant distinction.

With Kubernetes, you essentially say:

> "Here is a Deployment object. I want three replicas of this container."

Kubernetes then has a hierarchy of resources and controllers that turn that declaration into Pods.

CCattler instead wants you to express **intent** in its domain language:

```hcl
service web {
    image nginx:1.28
    instances 3

    expose 8080

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
``` :chatgpt-content-reference{index="5"}


The system then derives the necessary actions.

So there's a philosophical difference:

**Kubernetes**

> "The cluster is a collection of typed resources and controllers operating on those resources."

**CCattler**

> "The cluster is a collection of facts about desired and observed reality, and rules derive what needs to happen."

That's much more than a different configuration syntax.

---

# Another major difference: one reconciliation engine

This is where CCattler becomes particularly interesting.

CCattler says:

> scheduling, scaling, networking and storage all go through **one reconciliation engine**. :chatgpt-content-reference{index="6"}

Kubernetes is deliberately more modular.

You have:

```text
                Kubernetes API
                     │
       ┌─────────────┼─────────────┐
       ▼             ▼             ▼
  Scheduler     Controllers     Operators
       │             │             │
       └──────┬──────┴─────────────┘
              │
       various subsystems
              │
     networking / storage /
     autoscaling / etc.
```

CCattler's intended model is closer to:

```text
                 Facts
                   │
                   ▼
             ┌───────────┐
             │ Reconcile │
             │   engine  │
             └─────┬─────┘
                   │
        ┌──────────┼──────────┐
        ▼          ▼          ▼
    scheduling  networking  storage
        │          │          │
        └──────────┼──────────┘
                   ▼
                Actions
```

That means CCattler is attempting to make **the reconciliation mechanism itself the fundamental abstraction**, rather than making Kubernetes-style resources the fundamental abstraction.

---

# And this separates CCattler from Portainer completely

Portainer's architecture is basically:

```text
                 Portainer
                    │
       ┌────────────┼────────────┐
       ▼            ▼            ▼
   Kubernetes   Docker Swarm   Podman
       │            │            │
       ▼            ▼            ▼
    workloads    workloads    workloads
```

Portainer provides a common operational interface over those systems. The underlying orchestrators still make the scheduling/lifecycle decisions. Portainer describes itself as a management platform that layers on top of those orchestrators. [Portainer](https://www.portainer.io/blog/container-orchestration-platforms)

CCattler instead looks like:

```text
                    CCattler
                       │
             ┌─────────┴─────────┐
             │   CCattler core   │
             │ facts + rules +   │
             │ reconciliation    │
             └─────────┬─────────┘
                       │
                  Node agents
                       │
                ┌──────┼──────┐
                ▼      ▼      ▼
              host   host    host
```

So **CCattler occupies the same architectural category as Kubernetes/Swarm/Nomad, not Portainer.**

---

## CCattler vs the orchestrators in the Portainer article

The interesting comparison is therefore:

| | Kubernetes | Swarm | Nomad | **CCattler** |
|---|---|---|---|---|
| Category | Orchestrator | Orchestrator | Orchestrator | **Orchestrator** |
| Primary abstraction | Objects | Services/tasks | Jobs/allocations | **Facts/relations** |
| Configuration | YAML/API | Compose/CLI | HCL | **CCattler DSL** |
| Reconciliation | Multiple controllers | Manager reconciliation | Scheduler/client model | **Unified reconciliation** |
| Scheduler | Yes | Yes | Yes | **Yes** |
| Networking | Separate Kubernetes mechanisms | Built in | Integrated | **Controller/fact model** |
| Storage | CSI ecosystem | Docker volumes/plugins | Plugins | **Controller/fact model** |
| Autoscaling | HPA/VPA/KEDA etc. | More basic | Autoscaling mechanisms | **Unified scaling** |
| Multi-tenancy | Namespaces + policies | More limited | Namespaces/ACLs | **Built in as a core concept** |
| Security model | RBAC + ecosystem | Swarm security | ACLs | **RBAC + ABAC + identity policies** |
| UI/management layer | Many options | Many options | Many options | Not the core purpose |
| Underlying runtime | Container runtime ecosystem | Docker | Multiple workload types | **Node agents running containers** |

The CCattler column here reflects the project's own stated architecture and goals; it is currently labeled **v1.0.0-beta**, so some of these capabilities should be regarded as the project's design/implementation at this stage rather than equivalent maturity with established orchestrators. [ccattler.org](https://ccattler.org/)

### In one sentence

**Portainer asks:**  
*"How can I manage Kubernetes/Swarm/etc. more easily?"*

**Kubernetes/Swarm/Nomad ask:**  
*"How do I orchestrate workloads across machines?"*

**CCattler asks:**  
*"What if we redesigned the orchestration engine itself around facts, relations, declarative intent, and a unified reconciliation loop instead of Kubernetes-style objects and controllers?"*

That's the architectural distinction I think you were getting at.

If you're evaluating CCattler technically, the **next really interesting comparison is CCattler vs Kubernetes internals**—especially whether its *facts → rules → actions* model actually eliminates the complexity that Kubernetes' API objects + controllers + CRDs introduce, or merely moves that complexity somewhere else.