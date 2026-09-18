# Concepts

CCattler takes a fundamentally different approach to container orchestration. Instead of managing mutable API objects (Pods, Deployments, ReplicaSets), CCattler operates on **facts** — immutable observations about what the world should look like and what it actually looks like.

## Core ideas

### Facts, not objects

The cluster stores key-value facts in etcd, organized into three namespaces:

- **`desired/`** — what the world should look like (written by users and policy controllers)
- **`observed/`** — what the world actually looks like (written by node agents)
- **`placement/`** — scheduling decisions (written by the scheduler)

The gap between `desired/` and `observed/` is the work. Controllers close that gap continuously.

### No YAML

CCattler uses a purpose-built domain language instead of YAML. You declare intent — the system figures out the implementation:

```hcl
service web {
    image nginx:1.28
    instances 3
    expose 8080
}
```

This compiles to facts: `service(name="web", image="nginx:1.28", instances=3)`, `exposes(service="web", port=8080)`.

### Everything communicates through state

No component calls another component directly. The scheduler writes a placement fact; the node agent notices it. The agent starts a container and writes an observed state fact; the endpoint controller notices it. This makes the system naturally resilient — any component can crash and restart without breaking the others.

## Kubernetes comparison

If you know Kubernetes, here's how CCattler maps:

| Kubernetes | CCattler | Key difference |
|---|---|---|
| Cluster | Fact Store + Agents | No explicit cluster object — nodes register by writing facts |
| Pod | Instance + Group | An instance is one container; groups share network/volumes |
| Deployment / ReplicaSet | Service fact + Instance controller | `desired=5, actual=3` → create 2 more. No object chain |
| Control Plane | Runner + Controllers + Store | Controllers are stateless rule engines, not resource managers |
| Reconciliation loop | `facts → controllers → changes → agents → observations → repeat` | Single loop for all capabilities |
| Taints/Tolerations | `restrict` / `accept` | Human-readable placement constraints |
| Affinity/Anti-affinity | `require` / `prefer` | Direct label matching, no complex expressions |

## Learn more

- [Facts & State](/concepts/facts) — the fact-based state model
- [Reconciliation](/concepts/reconciliation) — how the loop works
- [Controllers](/concepts/controllers) — rule engines that close the gap
- [Networking](/concepts/networking) — service discovery, VIPs, DNS
- [Security](/concepts/security) — zero-trust security model
