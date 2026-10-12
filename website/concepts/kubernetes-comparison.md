# Kubernetes comparison

CCattler takes a different approach to container orchestration. Where Kubernetes provides a generic API server that requires dozens of add-on tools for production use, CCattler builds these capabilities into its fact-based architecture. Scaling, networking, secrets, policy, and mTLS are all facts and controllers — not separate operators to install and maintain.

## Core concepts

| Kubernetes | CCattler | Key difference |
|---|---|---|
| Cluster | Fact Store + Agents | No explicit cluster object — nodes register by writing facts |
| Pod | Instance + Group | An instance is one container; groups share network/volumes |
| Deployment / ReplicaSet | Service fact + Instance controller | `desired=5, actual=3` → create 2 more. No object chain |
| Namespace | Tenant + hierarchical paths | `/payments/checkout` instead of `namespace: payments` |
| Control Plane | Runner + Controllers + Store | Controllers are stateless rule engines, not resource managers |
| Reconciliation loop | `facts → controllers → changes → agents → observations → repeat` | Single loop for all capabilities |
| YAML manifests | `.cca` domain language | Human-readable intent, not serialized API objects |
| `apiVersion/kind` | Typed facts + schemas | No version matrices or GVK resolution |
| CRDs | Typed facts + controller SDK | New facts + new constraints, not new API endpoints |

## Placement and scheduling

| Kubernetes | CCattler | Key difference |
|---|---|---|
| Taints / Tolerations | `restrict` / `accept` | Human-readable: `restrict gpu` / `accept gpu` |
| Node affinity | `require gpu = true` | Direct label matching, no complex expressions |
| Pod anti-affinity | `zone spread` | Built-in spread across zones, no affinity rules |
| Preferred scheduling | `prefer region = us-east` | Soft constraints as a first-class keyword |
| kube-scheduler | Pure function: `schedule(requirements, nodes) → placement` | No plugin chains or extension points to configure |

## Production tooling

Kubernetes in production requires a constellation of add-on tools. CCattler integrates these capabilities directly.

### Cluster navigation and troubleshooting

**Kubernetes**: K9s, Lens, kubectl with plugins.

**CCattler**: Built into the CLI.

```bash
cca status                    # cluster overview
cca top nodes                 # CPU/memory per node
cca top workloads             # CPU/memory per instance
cca watch desired/service/    # stream fact changes in real time
cca agent debug               # inspect local container runtime
cca node-inspect <node>       # query remote agent state
cca exec <instance> -- sh     # exec into running container
cca logs <service> --follow   # aggregate container logs
```

### GitOps and deployment

**Kubernetes**: Argo CD, Flux — separate controllers that sync Git to the cluster API.

**CCattler**: `cca apply` is the deployment primitive. The `.cca` files are declarative intent — store them in Git, apply on merge. The reconciliation loop is the core, not an add-on.

```bash
cca apply myapp.cca           # deploy from file
cca diff myapp.cca            # preview what would change
```

### Autoscaling

**Kubernetes**: HPA for horizontal, VPA for vertical, KEDA for event-driven — three separate controllers with different APIs.

**CCattler**: One unified scaling engine with all dimensions in a single DSL block.

```hcl
service web {
    scale {
        horizontal {
            min 3
            max 30
            target cpu = 60
            target requests_per_second = 500
            event payments.pending = 20
            schedule {
                days weekdays
                start "08:00"
                end "18:00"
                minimum 10
            }
        }
    }
}
```

Multiple metrics evaluated independently: `desired = max(cpu_rec, rps_rec, queue_rec, scheduled_min)`. Asymmetric stabilization prevents oscillation (fast scale-up, slow scale-down).

### Node autoscaling

**Kubernetes**: Karpenter or Cluster Autoscaler — separate controllers that provision cloud instances.

**CCattler**: Cluster autoscaler is planned. Unsatisfied scheduling demand signals the node provisioner:

```
application autoscaler → desired workload → scheduler → unsatisfied capacity
    → cluster autoscaler → desired nodes → infrastructure provider
```

### Network policy

**Kubernetes**: NetworkPolicy objects with label selectors. Requires a CNI that implements them (Calico, Cilium). Policies break when labels change.

**CCattler**: Identity-based network policy. Policies reference service identities, not IP addresses or labels.

```hcl
network {
    allow frontend/web -> payments/checkout port 443
    allow payments/checkout -> payments/database port 5432
    deny frontend/web -> payments/database
}
```

When instances move, policies don't change — only derived firewall rules change.

### Service mesh (mTLS, traffic management)

**Kubernetes**: Istio, Linkerd — sidecar proxies injected into every pod, separate control plane, complex configuration.

**CCattler**: mTLS everywhere by default. No sidecars. Every component authenticates with X.509 certificates from the cluster CA. Workloads get SPIFFE identities: `spiffe://ccattler/payments/database`.

```
CLI ─── mTLS/OIDC ──► API
API ─── mTLS ────────► Store
Agent ── mTLS ────────► Store
```

Short-lived certificates (1hr) with automatic rotation. Being inside the cluster grants nothing.

### Secrets management

**Kubernetes**: Secrets Store CSI Driver to integrate external secret managers. Base64-encoded secrets in etcd by default (not encrypted). Requires external operators for rotation.

**CCattler**: Envelope encryption with KMS integration built in. Secrets are references, never values. The agent materializes secrets as bind-mounted files at container start, removes them on stop.

```hcl
secret database.password

service api {
    secret database.password {
        mount "/run/secrets/database-password"
    }
}
```

The scheduler and network controller never see plaintext secrets. File-mounted delivery preferred over environment variables — avoids accidental logging, enables rotation by file overwrite.

### Policy enforcement

**Kubernetes**: Kyverno, OPA/Gatekeeper — separate admission webhooks with their own policy languages.

**CCattler**: Built-in admission pipeline. Every change passes through:

```
APPLY → syntax validation → schema validation → RBAC/ABAC → quota check
    → security policy → mutation → commit
```

RBAC for broad authority, ABAC for context-aware conditions:

```hcl
role developer {
    allow service.read
    allow service.update
}

policy team-isolation {
    allow service.update
    when subject.team == resource.team
}
```

### Monitoring and metrics

**Kubernetes**: Prometheus + Grafana — separate deployments, ServiceMonitors, Grafana dashboards.

**CCattler**: Utilization telemetry is built into the agent. Resource usage is stored as observed facts, queryable via the CLI.

```bash
cca top nodes                 # node-level CPU/memory/instance count
cca top workloads             # per-instance CPU/memory
cca metric set web cpu 75     # inject simulated metrics for testing
```

External Prometheus/Grafana integration is planned for dashboards and alerting.

### Distributed tracing

**Kubernetes**: Jaeger, Zipkin — instrument every service, deploy collectors.

**CCattler**: Audit logging with request IDs for every security-sensitive operation. Full distributed tracing across microservices is not yet implemented — that's application-level instrumentation, not orchestrator responsibility.

### Rolling updates and rollback

**Kubernetes**: Deployment rollout strategy, `kubectl rollout undo`. Requires correct `revisionHistoryLimit` configuration.

**CCattler**: Rolling updates are built into the rollout controller. Automatic rollback when 3+ new-image instances fail health checks.

```hcl
service api {
    image myapp:v3
    instances 10
    update {
        max_unavailable 2
        max_extra 2
    }
    disruption {
        min_available 8
    }
}
```

Manual rollback: change the image back and `cca apply`. No revision history to manage — the desired state is the truth.

## What's different

The fundamental difference isn't feature-by-feature. It's architectural:

- **Kubernetes** is a generic API server. Production use requires assembling 10+ tools, each with its own configuration language, upgrade cycle, and failure modes. The API server doesn't know about scaling, networking, secrets, or policy — those are all separate controllers watching separate API objects.

- **CCattler** is a fact-based reconciliation engine. Scaling, networking, secrets, policy, and mTLS are all facts in the same store, processed by controllers in the same loop. There's no gap between "core Kubernetes" and "production Kubernetes" — the production capabilities are the core.
