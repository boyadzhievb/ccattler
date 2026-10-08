What about helm charts

Yes — Helm charts should be part of the project audit, but I would treat them as a deployment/integration layer, not as part of CCattler's core architecture.

For CCattler specifically, I'd add a dedicated Helm/Kubernetes compatibility and deployment audit to chat-plan-20sep.md.

What should be checked

1. Helm chart structure

Chart.yaml
values.yaml
templates
helpers
CRDs, if any
NOTES
_helpers.tpl
chart dependencies
chart version vs CCattler version

2. Installation correctness
Test:

helm lint ./charts/ccattler
helm template ...
helm install --dry-run ...
helm upgrade ...
helm rollback ...
helm uninstall ...

Test both fresh installation and upgrade from previous versions.

3. Configuration
Every important server/agent setting should be configurable without editing templates:

etcd endpoints
API address/ports
node/agent configuration
runtime configuration
resource requests/limits
affinity/anti-affinity
tolerations
node selectors
security context
persistence
TLS
secrets
service configuration
observability
network configuration

Avoid embedding environment-specific assumptions in the chart.

4. Security

This deserves particular attention:

runAsNonRoot
readOnlyRootFilesystem
dropped Linux capabilities
seccomp
privileged containers only where genuinely required
host networking/host PID/host mounts
containerd/nerdctl socket access
ServiceAccount permissions
RBAC least privilege
Secret handling
NetworkPolicies
Pod Security Standards compatibility
TLS configuration
image provenance and digest pinning

A Helm chart can accidentally undo a lot of the security architecture elsewhere in the project.

5. HA behavior

Test:

helm install
    ↓
server replicas
    ↓
agent replicas
    ↓
etcd
    ↓
restart one component
    ↓
upgrade
    ↓
rollback

Verify that Helm/Kubernetes lifecycle operations don't violate CCattler's reconciliation model.

6. Upgrade safety

Explicitly test:

N → N+1 upgrade
configuration changes
image changes
schema/state changes
controller restart during upgrade
agent restart during upgrade
rollback
partially failed upgrade

The important question is not merely “does Helm upgrade succeed?” but:

Does the cluster converge correctly after an interrupted or partially completed upgrade?

7. Resource behavior

Add chart-level defaults for:

CPU/memory requests
CPU/memory limits
probes
termination grace periods
priority classes where appropriate
disruption budgets where appropriate

But don't blindly add Kubernetes concepts merely because they exist. They should map to actual CCattler semantics.

8. Chart testing

Add automated CI for:

helm lint
helm template
helm unittest / equivalent
helm install
helm upgrade
helm rollback

Ideally run an integration test against a disposable Kubernetes cluster such as kind/k3d when CI permits.

One important architectural question

I'd also explicitly decide whether Helm is:

A. A deployment mechanism for CCattler, or

B. A compatibility layer that makes CCattler itself operate as a Kubernetes workload.

I strongly recommend treating it as A.

That keeps the conceptual boundary clean:

Helm/Kubernetes
       │
       │ deploys
       ▼
   CCattler
       │
       ├── API
       ├── controllers
       ├── agents
       ├── runtime
       └── state store

Helm should not leak Kubernetes' Pod/ReplicaSet/Deployment ontology into CCattler's user-facing model.

And yes, I would add this as a dedicated section to chat-plan-20sep.md, along with OCI images, container registry, SBOM/provenance, Kubernetes integration, and release packaging.