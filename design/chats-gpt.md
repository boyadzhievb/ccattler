# ChatGPT Conversation Record — CCattler

> **Purpose:** Working record of the CCattler design conversations.
>
> **Important:** This file is a reconstructed project conversation record from the conversation context available to ChatGPT. It is **not a verbatim export** of every historical ChatGPT message. Where exact historical wording was unavailable, the discussion is summarized rather than fabricated.

## Project

**CCattler — Container Cattler**

A container orchestration system inspired by Kubernetes, but intentionally avoiding Kubernetes' user-facing object/YAML ontology. The design is based on facts, relations, desired/observed state, constraints, policies, identity, and reconciliation.

Repository: `boyadzhievb/ccattler`

## Core architectural philosophy

The central control loop is:

```
FACTS
  ↓
RULES / POLICIES
  ↓
DESIRED STATE
  ↓
RECONCILIATION
  ↓
REALITY
  ↓
OBSERVATIONS
  ↓
FACTS
```

The user-facing model should describe intent and relationships rather than Kubernetes-style Pods, ReplicaSets, Deployments, etc. Internal objects are acceptable where useful, but they should not define the public conceptual model.

A fundamental invariant was established:

> **Controllers produce plans. The store commits state. Agents produce observations. Nothing treats an action as truth.**

A related invariant:

> **No component assumes another component performed an action; components only react to state.**

The system should therefore be declarative, idempotent, convergent, and state-driven.

## Target architecture

```
                       USER
                        │
                        ▼
                  ┌──────────┐
                  │   CLI    │
                  └────┬─────┘
                       │
                       ▼
                  ┌──────────┐
                  │   API    │
                  └────┬─────┘
                       │
                       ▼
                ┌───────────────┐
                │     FACTS     │
                │ desired       │
                │ observed      │
                │ policy        │
                │ placement     │
                │ effective     │
                └───────┬───────┘
                        │
                   controllers
                        │
            ┌───────────┼───────────┐
            ▼           ▼           ▼
        scheduler    network     autoscaler
            │
            ▼
        placement
            │
            ▼
      ┌───────────────┐
      │ STATE STORE   │
      │     etcd      │
      └───────┬───────┘
              │
     ┌────────┼────────┐
     ▼        ▼        ▼
   Agent    Agent    Agent
   Host A   Host B   Host C
     │        │        │
  runtime  runtime  runtime
     │        │        │
 containers containers containers
     │        │        │
     └────────┼────────┘
              │
         observations
              │
              ▼
             FACTS
```

## State model

The desired domains discussed were:

- `/ccattler/desired/` — user intent
- `/ccattler/observed/` — runtime/agent truth
- `/ccattler/derived/` — controller-owned derived state
- `/ccattler/policy/` — policy and constraints
- `/ccattler/placement/` — scheduler decisions
- `/ccattler/effective/` — policy-resolved effective intent

The conceptual lifecycle is:

```
INTENT
  →
DESIRED
  →
POLICY
  →
EFFECTIVE
  →
PLACEMENT
  →
RECONCILIATION
  →
REALITY
  →
OBSERVATION
  →
DESIRED
```

An important ownership rule emerged:

- Controllers should not write runtime truth into `/observed`.
- Agents publish observations.
- Controllers derive desired/derived/effective state from observations.

## State store

The current StateStore interface was:

```go
type StateStore interface {
    Get(ctx context.Context, key string) (*Fact, error)
    Put(ctx context.Context, key string, value []byte) (int64, error)
    Delete(ctx context.Context, key string) error
    Scan(ctx context.Context, prefix string) ([]Fact, error)
    Watch(ctx context.Context, key string, opts WatchOption) (<-chan Event, error)
    Transaction(ctx context.Context, compares []Compare, onSuccess []Op, onFailure []Op) (bool, error)
    Revision(ctx context.Context) (int64, error)
    Close() error
}
```

A target interface was proposed:

```go
type StateStore interface {
    Get(ctx context.Context, key string) (Fact, error)
    List(ctx context.Context, prefix string) (Snapshot, error)
    Put(ctx context.Context, key string, value []byte) (WriteResult, error)
    Delete(ctx context.Context, key string) (WriteResult, error)
    Txn(ctx context.Context, txn Transaction) (TxnResult, error)
    Watch(ctx context.Context, prefix string, opts WatchOptions) (Watch, error)
    Revision(ctx context.Context) (Revision, error)
    Close() error
}
```

Important store semantics:

- Snapshots should carry a coherent revision.
- A reconciliation plan should identify the revision it was based on.
- One transaction should ideally correspond to one committed revision.
- Idempotent Put should not create a new revision/event when the bytes are unchanged.
- Watch delivery is a hint to re-check state, not a durable source of truth.
- Missed, overflowed, or compacted watches must trigger a full resync.
- Watch cancellation must unregister resources.
- After close, operations must return `ErrStoreClosed`.
- Store boundaries should deep-copy byte slices.

A proposed reconciliation plan:

```go
type ReconcilePlan struct {
    BaseRevision  Revision
    Preconditions []Compare
    Changes       []Change
}
```

### Critical store bug identified

The etcd Put implementation had a concurrency hole:

1. Read current value/revision.
2. Attempt revision CAS.
3. If CAS fails, fall back to unconditional PUT.

That fallback can overwrite a concurrent writer. It must be removed. Put must either be explicitly unconditional with no-op suppression, or return a conflict and retry from a fresh read.

### Watch issue

The event abstraction included `Prev *Fact`, but the etcd watch was not using `WithPrevKV()`. Either previous values must actually be populated or the API guarantee should be weakened.

## Reconciliation protocol

The current controller interface was:

```go
type Controller interface {
    Name() string
    Watch() []string
    Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error)
}
```

The desired direction was:

```go
type Controller interface {
    Name() string
    Dependencies() []Dependency
    Reconcile(ctx context.Context, snapshot Snapshot) (Plan, error)
}
```

Dependencies should distinguish:

- reads
- watches
- writes

Controllers should not own overlapping authoritative keyspaces.

### Critical reconciliation bug

The current runner scans watched prefixes and records a maximum snapshot revision, but does not use that revision as a precondition.

It then compares only keys being changed.

This permits stale decisions. Example:

1. Controller observes instance A as Ready.
2. It plans an endpoint.
3. A becomes NotReady.
4. Controller transaction only compares the endpoint output key.
5. Transaction succeeds even though its input changed.

The reconciliation protocol needs either:

- explicit read/watch dependency preconditions, or
- a global revision guard, where appropriate.

The long-term design should make the controller's read set explicit.

### Controller runner

The current runner:

- starts controllers in goroutines,
- restarts failed controller loops with exponential backoff,
- watches prefixes,
- debounces triggers,
- performs an initial reconcile,
- periodically resyncs,
- retries transaction conflicts,
- emits events after changes.

Issues:

- sibling lifecycle cancellation should be stronger, ideally using `errgroup.WithContext`,
- health should be exposed,
- semantic events should not be emitted directly from controller intent,
- overlapping watch prefixes can duplicate work,
- the current snapshot revision is not enforced,
- periodic resync remains necessary as a correctness backstop.

Desired event flow:

```
committed state
      ↓
EventProjector
      ↓
semantic event
```

rather than:

```
controller intent
      ↓
event
```

## Runtime and agent truth

Runtime abstraction was intended to remain independent of Docker/containerd:

```go
type Runtime interface {
    Start(...)
    Stop(...)
    Status(...)
    List(...)
    Exec(...)
}
```

Potential providers include:

- simulator
- process runtime
- OCI/containerd-style runtime
- optional Docker provider

A future Stats API was suggested for `cca top`.

### Critical runtime truth issue

The agent currently publishes `InstanceRunning` immediately after `runtime.Start()` succeeds.

That only proves the runtime accepted the start request. It does not necessarily prove the workload is running.

Correct sequence:

```
Start
  ↓
Status / List
  ↓
observe actual runtime state
  ↓
publish observed state
```

## Agent architecture

The current agent was identified as a "god loop" handling too many responsibilities:

- node registration and heartbeat
- runtime
- init
- storage
- secrets
- networking
- health
- probes
- telemetry
- cleanup
- endpoint metadata

The desired direction is to split these into independently owned components while retaining a clear lifecycle coordinator.

## Initialization

Initialization should be a first-class concept without adopting Kubernetes' "init container" user ontology.

Example user-facing model:

```text
service api {
    image my-api:1.4
    instances 3

    init {
        exec "migrate-db"
        timeout 30s
    }

    init {
        exec "generate-config"
        timeout 10s
    }

    startup {
        http /startup
        port 8080
        every 2s
        timeout 1s
        failure_threshold 30
    }

    readiness {
        http /health/ready
        port 8080
        every 5s
    }
}
```

Dependency example:

```text
service api {
    depends_on database {
        condition ready
    }
}
```

Rules:

- init is sequential by default,
- a DAG with explicit `after` can come later,
- init must be restart-safe/idempotent,
- the main workload starts only after initialization completes,
- initialization failure is represented as observed state,
- controllers derive consequences from that state.

Example:

```text
observed:
  initialization:
    phase: failed
    step: migrate-db
    reason: exit_code
    code: 1
```

Key semantic distinction:

> **Initialization establishes prerequisites. Startup establishes application health. Readiness establishes service eligibility. Liveness establishes continued operation.**

A lifecycle conflict was found: both InitController and the agent were writing init phase. The preferred model is for the agent to publish step observations while the InitController derives phase.

The current init command implementation uses host process execution directly despite runtime-backed design comments. This should either become runtime-backed or explicitly model host-side semantics.

## Probes and health

Desired probe abstraction:

```go
type Probe interface {
    Check(ctx context.Context, target Target) Observation
}
```

Initial protocols:

- HTTP
- TCP
- Exec

Potential later support:

- gRPC

Rich health state should include:

- Unknown / Pending / Healthy / Unhealthy
- startup Pending/Succeeded/Failed
- consecutive successes/failures
- timestamps
- reason
- latency
- transitions

Semantics:

- Startup: initialized and started.
- Readiness: eligible for service traffic.
- Liveness: continuing to operate.

Probe principle:

> **Probes report reality. Controllers derive desired state. Agents reconcile.**

Probe scheduling should be independent from reconciliation. The current agent couples probes to reconciliation and uses throttling timestamps; this should become a dedicated scheduler.

A critical bug was identified:

- missing instance IP currently falls back to `127.0.0.1`.

That must never happen. Missing network identity should result in something like NetworkUnavailable/Unknown rather than accidentally probing the local agent.

## Networking

Networking should use a high-level pluggable provider rather than hard-coding CNI.

User intent can be expressed as relationships:

```text
allow frontend/web → payments/checkout:443
deny frontend/web → payments/database:5432
```

Possible providers:

- Linux nftables/iptables
- eBPF/Cilium-style provider
- Calico-style provider
- WireGuard
- cloud security groups

Identity-based policy is preferred.

Endpoint membership should be derived from observed readiness.

A race was identified in VIP allocation: the NetworkController calculates the next VIP from existing state, so concurrent reconciliations can choose the same address. Allocation needs transactional reservation or deterministic collision handling.

## Service ports and endpoints

The current service-port representation was too loose, effectively resembling `map[string]int`.

A typed representation was preferred:

```go
type ServicePort struct {
    Name     string
    Port     int
    Protocol string
}
```

Endpoints should carry at least:

- service
- instance
- port
- protocol
- address

## Autoscaling

Autoscaling should be another controller/state transformation rather than direct runtime manipulation.

Flow:

```
Signals
  →
Scaling Policy
  →
Recommendation
  →
Constraints
  →
Desired State
  →
Reconciliation
```

Possible modes:

- horizontal
- vertical
- event-driven
- scheduled

Constraints include:

- minimum/maximum
- quotas
- security/policy
- available capacity

For explainability, retain both recommendation and allowed/effective desired state.

Cluster autoscaling should observe unsatisfied scheduling demand rather than manipulating runtimes directly.

## Scopes and security

Scope is a hierarchical administrative boundary for:

- naming
- ownership
- policy inheritance
- discovery

Scope is not itself:

- security
- networking
- quota
- secret management

Core distinction:

> **Scope organizes. Policies enforce. Ownership defines responsibility.**

Ownership and access are separate concepts.

Security model discussed:

- RBAC + ABAC
- least-privilege controller identities
- fact-domain permission boundaries
- workload identity, potentially SPIFFE-style
- encrypted secrets with envelope encryption
- identity-based network authorization
- audit logging
- zero-trust defaults
- mTLS
- internal CA
- short-lived certificates
- node enrollment with one-time bootstrap
- human OIDC/OAuth2/LDAP/SAML

Layering:

```
Identity
  →
Authentication
  →
Authorization
  →
Isolation
  →
Audit
```

## Storage and persistence

The architecture should use a provider abstraction for storage.

The state store is for authoritative current state, not time-series telemetry.

Recovery and persistence need to cover:

- restart
- crash
- state-store recovery
- partial writes
- interrupted reconciliation
- watch resync
- controller restart
- agent restart

## Observability and operations

Built-in operational visibility discussed:

```
cca status
cca top nodes
cca top workloads
cca get workload api
cca describe workload api
cca logs api
cca events
cca watch
cca metric
```

Different information types have different homes:

- State telemetry = what is true now
- Metrics = what has been happening
- Events = what changed
- Traces = why something took time
- Logs = workload output

Time-series metrics should not be stored in etcd.

Metrics should include:

- node CPU/memory
- workload CPU/memory
- restarts
- startup duration
- probe success/failure
- reconciliation duration
- reconciliation errors
- scheduler latency

External integration should use standards such as OpenTelemetry and Prometheus.

## Testing strategy

Testing priorities identified:

### P0

- store correctness
- deep-copy semantics
- closed-store behavior
- no-op Put suppression
- one transaction = one revision
- snapshot revision
- revision-aware watch/resync
- watch cancellation/unregister
- transactional controller plans
- runner lifecycle
- concurrency/race tests

### P1

- read/watch/write domains
- agent decomposition
- runtime observation correctness
- lease heartbeat
- health scheduler

### P2

- typed keys/codecs
- simulator
- chaos testing

### P3

- security hardening
- network/provider hardening

The release philosophy is:

> **v0.1 should be a correctness release, not a feature release.**

## Distributed correctness and chaos

Important failure cases to test:

- controller crashes
- agent crashes
- runtime disappears
- state store becomes unavailable
- watch stream is interrupted
- watch overflows
- watch is compacted
- concurrent writers race
- node disappears during placement
- workload disappears between observation and action
- partial upgrade
- interrupted upgrade
- stale controller snapshots
- duplicate events
- delayed observations

The system should converge after interruptions rather than rely on procedural action history.

## Desired project layout

```
ccattler/
  api/
  cli/
  lang/
  state/
    model/
    keys/
    codec/
    validation/
  store/
    store.go
    memory.go
    sqlite.go
    errors.go
  controller/
    controller.go
    runner.go
    plan.go
    scheduler/
    endpoint/
    service/
    network/
    storage/
    autoscaling/
  agent/
    agent.go
    node/
    runtime/
    init/
    network/
    storage/
    secrets/
    health/
    telemetry/
  runtime/
    runtime.go
    process/
    oci/
  network/
    provider.go
    linux/
  storage/
    provider.go
  identity/
  policy/
  observability/
  sim/
  tests/
    convergence/
    failure/
    concurrency/
    chaos/
```

## Audit / execution plan

A comprehensive execution plan was created as:

`chat-plan-20sep.md`

It covers:

1. Mission and execution rules
2. Confirmed P0/P1 findings
3. Repository and architecture inventory
4. State-store correctness
5. Reconciliation protocol redesign
6. State ownership/truth model
7. Runtime/agent correctness
8. Controller-by-controller audit
9. Agent decomposition/lifecycle
10. Initialization
11. Probes/health
12. DSL/API
13. Security
14. Network/isolation
15. Storage providers
16. Persistence/recovery
17. Distributed correctness
18. Performance
19. Scalability/load
20. Scheduler
21. Autoscaling
22. Observability/operations
23. Event model
24. API/CLI UX
25. Fuzzing/property tests
26. Race/leak/lifecycle
27. Dependency/supply-chain
28. Build/release/reproducibility
29. Documentation
30. Chaos/recovery
31. Formal invariants/model tests
32. Architecture cleanup
33. Performance optimization
34. Final integration tests
35. Required deliverables
36. Recommended execution order/gates
37. P0 backlog
38. Definition of done
39. Final instruction to executing model

The plan was renamed to `chat-plan-20sep.md`.

## Repository history discussed

A reviewed commit was:

`3e1b2a8` — **Clean up tech debt — remove dead types, extract shared helpers, add doc comments**

The plan rename resulted in commits:

- `1484c377b39400430d452022b4be925e4763a8b2`
- `1c7429296b22bd2051d3224f7927007692f72f8f`

The repository is `boyadzhievb/ccattler`.

## Helm charts and deployment packaging

A later discussion asked whether Helm charts should be covered.

Conclusion: **Yes. Helm belongs in the project audit as a deployment/integration layer, not as part of CCattler's core architecture.**

The Helm/Kubernetes deployment audit should cover:

### Chart structure

- Chart.yaml
- values
- templates
- helpers
- CRDs if any
- NOTES
- dependencies
- chart versioning

### Installation correctness

- `helm lint`
- `helm template`
- dry-run
- install
- upgrade
- rollback
- uninstall

### Configuration

Audit configuration for:

- etcd endpoints
- API
- agents/nodes
- runtime
- resources
- affinity
- tolerations
- node selectors
- security context
- persistence
- TLS
- secrets
- services
- observability
- networking

### Security

Check:

- runAsNonRoot
- readOnlyRootFilesystem where possible
- dropped Linux capabilities
- privileged mode only where genuinely required
- host networking/PID/mount usage
- container runtime socket exposure
- ServiceAccount/RBAC least privilege
- secret handling
- NetworkPolicies
- Pod Security Standards compatibility
- TLS
- image provenance
- digest pinning

### HA and lifecycle

Verify:

- server replicas
- agent behavior
- etcd assumptions
- restart behavior
- upgrade behavior
- rollback
- controller restart
- agent restart
- convergence after lifecycle interruptions

### Upgrade safety

Test:

- N → N+1
- configuration changes
- image changes
- state/schema changes
- component restarts
- rollback
- partial failure
- interrupted upgrade

Key question:

> Does the cluster converge after an interrupted or partial Helm upgrade?

### Kubernetes resource configuration

Review:

- requests/limits
- probes
- termination grace periods
- priority where appropriate
- PodDisruptionBudgets where appropriate

Do not blindly import Kubernetes user concepts into the CCattler model just because Helm deploys CCattler onto Kubernetes.

### Helm tests

Desired layers:

- lint
- template rendering
- chart/unit tests
- install tests
- upgrade tests
- rollback tests
- disposable Kubernetes integration tests, potentially using kind/k3d

### Architectural boundary

Preferred model:

```
Helm / Kubernetes
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
```

Helm is therefore a deployment mechanism for CCattler, not a reason to make CCattler's internal model Kubernetes-shaped.

## Further packaging topics to audit

The deployment/release layer should also cover:

- OCI images
- container registry publishing
- immutable image tags/digests
- SBOM generation
- provenance/attestations
- vulnerability scanning
- reproducible builds
- release manifests
- Helm chart publishing/versioning
- Kubernetes integration testing

These should remain deployment and supply-chain concerns rather than leaking Kubernetes ontology into the CCattler language.

## Current guiding principles

The project repeatedly returned to these principles:

1. **State is authoritative; actions are not.**
2. **Controllers derive plans.**
3. **The store commits state atomically.**
4. **Agents observe runtime reality.**
5. **Controllers should not write runtime truth.**
6. **Watches are hints; state re-read is authoritative.**
7. **Reconciliation must be idempotent and convergent.**
8. **Concurrency must be protected by explicit preconditions.**
9. **User intent should remain independent from implementation mechanics.**
10. **Kubernetes can be a deployment environment without becoming CCattler's user-facing ontology.**
11. **Scope organizes; policy enforces; ownership defines responsibility.**
12. **v0.1 prioritizes correctness over feature breadth.**

## Open P0/P1 issues at the time of this record

### P0

- Make reconciliation snapshots transactionally safe.
- Remove the etcd Put CAS-failure unconditional overwrite.
- Stop publishing runtime running state based solely on Start success.
- Prevent controllers from writing observed runtime truth.
- Make network VIP allocation race-safe.

### P1

- Correct etcd previous-value watch semantics.
- Move semantic event generation to committed-state projection.
- Decouple probe scheduling from reconciliation.
- Remove localhost fallback for missing network identity.
- Establish one authority for initialization phase.
- Define runtime-backed init semantics.
- Introduce typed service ports/endpoints.
- Split the agent into clear ownership domains.
- Strengthen controller lifecycle, health, restart, and cancellation semantics.

## Conversation boundary

This document is intended as a durable project memory for future CCattler work. It should be updated when significant architecture decisions, audits, implementation findings, or release criteria are agreed in subsequent conversations.
