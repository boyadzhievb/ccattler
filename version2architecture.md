Yes. When CCattler is ready, I would deploy it with a **control-plane / node-agent architecture very similar to Kubernetes**, but keep CCattler's internal model based on facts, desired state, observations, and reconciliation.

The important distinction is:

> **Kubernetes deploys Pods through a control plane. CCattler should deploy desired workloads through a distributed state + reconciliation system.**

### The target architecture

```text
                         ┌─────────────────────────┐
                         │       ccattlerctl        │
                         │    CLI / API clients     │
                         └────────────┬────────────┘
                                      │
                                      ▼
                         ┌─────────────────────────┐
                         │      CCattler API       │
                         │  auth / validation / DSL │
                         └────────────┬────────────┘
                                      │
                                      ▼
                 ┌──────────────────────────────────────┐
                 │          Distributed State            │
                 │                                      │
                 │  desired / observed / policy / ...  │
                 │       etcd or FoundationDB           │
                 └───────────────┬──────────────────────┘
                                 │
              ┌──────────────────┼──────────────────┐
              │                  │                  │
              ▼                  ▼                  ▼
        ┌───────────┐      ┌───────────┐      ┌───────────┐
        │ Scheduler │      │  Network  │      │ Autoscale │
        │Controller │      │Controller │      │ Controller│
        └─────┬─────┘      └───────────┘      └───────────┘
              │
              ▼
       placement facts
              │
       ┌──────┴──────────────────────────────┐
       │                                     │
       ▼                                     ▼
┌──────────────────┐                ┌──────────────────┐
│      Host A      │                │      Host B      │
│                  │                │                  │
│ ccattler-agent   │                │ ccattler-agent   │
│ runtime          │                │ runtime          │
│ network          │                │ network          │
│ storage          │                │ storage          │
│ health           │                │ health           │
│                  │                │                  │
│ containers       │                │ containers       │
└──────────────────┘                └──────────────────┘
       │                                     │
       ▼                                     ▼
   workloads                             workloads
```

And you can have:

```text
                    CONTROL PLANE
        ┌───────────────────────────────────┐
        │                                   │
        │ API + Controllers + State Store   │
        │                                   │
        │ 3 machines for HA                 │
        │                                   │
        └───────────────────────────────────┘

                    DATA PLANE

     Host 1          Host 2          Host 3
   ┌────────┐      ┌────────┐      ┌────────┐
   │ Agent  │      │ Agent  │      │ Agent  │
   │ Docker │      │ Docker │      │ Docker │
   │ /OCI   │      │ /OCI   │      │ /OCI   │
   └────────┘      └────────┘      └────────┘
```

## 1. A host runs `ccattler-agent`

This is the equivalent of the general role that `kubelet` plays.

On every machine:

```text
ccattler-agent
├── node reporter
├── runtime reconciler
├── network reconciler
├── storage reconciler
├── secret materializer
└── health reporter
```

The agent does **not** decide what should run.

It observes:

```text
"What does this machine currently look like?"
```

and reconciles against:

```text
"What does CCattler say should be running here?"
```

For example:

```text
/ccattler/placement/instance/abc123
    node = node-02
```

Agent on `node-02` sees that fact and works toward it.

---

# 2. You need a control-plane cluster

For a real multi-host installation, don't run the scheduler/API/state store on every machine independently.

Instead:

```text
             CCattler Control Plane

       ┌───────────┐
       │ CP node 1 │
       └─────┬─────┘
             │
       ┌─────┴─────┐
       │           │
┌──────▼──────┐ ┌──▼──────────┐
│ CP node 2   │ │ CP node 3   │
└─────────────┘ └─────────────┘
```

The control plane contains things such as:

* API server
* DSL parser/validator
* scheduler
* endpoint controller
* network controller
* storage controller
* autoscaler
* policy engine
* reconciliation runner
* state-store client

The important thing is that **controllers should be stateless**.

Their state lives in the distributed state store.

That means controller 1 can die:

```text
controller-1 💥
```

and controller 2 can continue reconciling from the same state.

---

# 3. Use etcd initially for the distributed state

Your current:

```go
StateStore
```

abstraction is exactly what makes this possible.

Eventually you can have:

```text
              StateStore interface
                     │
          ┌──────────┼──────────┐
          │          │          │
          ▼          ▼          ▼
       Memory     SQLite      etcd
                              │
                              ▼
                         FoundationDB
```

For the first real multi-host version, I'd choose **etcd**.

Not because CCattler should become "Kubernetes-like", but because you need:

* consistent reads
* revisions
* transactions
* compare-and-swap
* watches
* durable storage
* distributed consensus
* membership/failure handling

Your `StateStore` should hide etcd from the rest of CCattler.

---

# 4. How a workload gets deployed

Suppose you write:

```text
service web {
    image nginx:1.28
    instances 3

    resources {
        cpu 500m
        memory 512Mi
    }

    expose 8080
}
```

You run:

```bash
ccattler apply web.cat
```

The flow should be:

```text
ccattlerctl
     │
     ▼
API
     │
     ▼
DSL parser
     │
     ▼
Intent
     │
     ▼
Desired state
     │
     ▼
Scheduler
     │
     ▼
Placement
     │
     ▼
Node agents
     │
     ▼
Runtime
     │
     ▼
Container
```

For example:

```text
desired:

service web
instances = 3
```

becomes something conceptually like:

```text
desired/web
    instances = 3
```

Scheduler derives:

```text
placement/web/instance-1 → node-a
placement/web/instance-2 → node-b
placement/web/instance-3 → node-c
```

Agents observe their placement:

```text
node-a → instance-1
node-b → instance-2
node-c → instance-3
```

and reconcile their local runtime.

---

# 5. The really important part: agents communicate through state

You don't want this:

```text
Scheduler ──RPC──> Agent
Scheduler ──RPC──> Agent
Network ──RPC──> Agent
Storage ──RPC──> Agent
```

That creates a highly coupled system.

Instead:

```text
             STATE

               │
        ┌──────┴──────┐
        │             │
   Scheduler       Network
        │             │
        └──────┬──────┘
               │
               ▼
             STATE
               │
               ▼
             Agent
               │
               ▼
            Reality
               │
               ▼
           Observation
               │
               ▼
             STATE
```

That's the architectural property I'd protect most strongly.

---

# 6. How agents communicate with the control plane

There are actually two different communication paths.

### Control-plane → agent

The agent watches relevant state:

```text
placement
desired workload
network intent
storage intent
secret grants
```

through your state/watch mechanism.

### Agent → control plane

The agent writes observations:

```text
/observed/nodes/node-a
/observed/instances/abc
/observed/health/abc
```

So you get:

```text
             CONTROL PLANE
                   │
              desired state
                   │
                   ▼
              STATE STORE
                   │
                watches
                   │
                   ▼
                 AGENT
                   │
                runtime
                   │
                   ▼
               CONTAINER
                   │
                observe
                   │
                   ▼
              STATE STORE
```

This is much closer to your CCattler philosophy than direct imperative orchestration.

---

# 7. Node registration

When a new machine joins:

```bash
curl ... install-agent
```

or eventually:

```bash
ccattler join <control-plane>
```

The agent authenticates using its bootstrap credential/certificate.

Then it publishes something like:

```text
node/node-a

architecture = amd64
cpu = 16
memory = 64Gi
zone = sofia-1
runtime = containerd
network = linux
status = ready
```

The control plane observes that node.

Scheduler can now place workloads there.

---

# 8. Node failure

This is where your architecture becomes really interesting.

Suppose:

```text
node-a
   │
   ├── web-1
   ├── web-2
   └── web-3
```

dies.

The node's lease expires.

The state becomes:

```text
node-a = unavailable
```

Scheduler sees:

```text
desired instances = 3

observed healthy:
web-1 ❌
web-2 ❌
web-3 ❌
```

and derives new placement:

```text
web-1 → node-b
web-2 → node-c
web-3 → node-d
```

Then those agents reconcile.

No special:

```text
if node died:
    restart everything
```

is required.

That's exactly what you want.

It's just:

```text
Reality changed
      ↓
Observation changed
      ↓
Facts changed
      ↓
Scheduler reconciles
      ↓
Desired placement changes
      ↓
Agents reconcile
```

---

# 9. How you should package CCattler

When you reach v0.1, I'd produce at least these binaries:

```text
ccattler
ccattler-agent
ccattler-server
```

Possibly:

```text
ccattler
    CLI

ccattler-server
    API
    controllers
    control-plane

ccattler-agent
    node agent
    runtime
    network
    storage
    health
```

You could also combine the server components initially:

```text
ccattler-server
├── API
├── scheduler
├── controllers
└── state client
```

Don't prematurely split every controller into separate processes.

---

# 10. A five-machine cluster

A realistic early deployment could look like:

```text
                 ┌───────────────────┐
                 │ Load Balancer     │
                 │ ccattler API      │
                 └─────────┬─────────┘
                           │
              ┌────────────┼────────────┐
              │            │            │
              ▼            ▼            ▼
          CP node 1    CP node 2    CP node 3
          API           API          API
          controllers   controllers  controllers
              │            │            │
              └────────────┼────────────┘
                           │
                      ┌────▼────┐
                      │  etcd   │
                      │ cluster │
                      └────┬────┘
                           │
              ┌────────────┼────────────┐
              │            │            │
              ▼            ▼            ▼
           worker-1     worker-2     worker-3
           agent        agent        agent
           runtime      runtime      runtime
              │            │            │
           containers   containers   containers
```

Eventually you could run etcd separately, but during development I'd keep it simpler.

---

# 11. Don't make the agents depend on the API server

This is another architectural decision I'd make now.

Prefer:

```text
Agent → state store
```

rather than:

```text
Agent → API → controller → state store
```

for normal reconciliation.

The API is for **users and external clients**.

The state store is the shared coordination mechanism.

That gives you a much more resilient system.

---

# 12. How networking fits in

Suppose:

```text
service frontend
service api
service database
```

Your configuration says:

```text
allow frontend → api:443
allow api → database:5432
deny frontend → database:5432
```

The Network Controller derives network policy.

The Network Provider on each host translates that into the actual implementation:

```text
CCattler policy
      │
      ▼
Network Controller
      │
      ▼
NetworkProvider
      │
      ├── Linux
      ├── Cilium
      ├── Calico
      ├── cloud networking
      └── future providers
```

Again, the control plane doesn't need to know whether the host ultimately uses eBPF, nftables, WireGuard, etc.

---

# 13. How users actually install a cluster

Eventually you want the experience to be something like:

### Control plane

```bash
ccattler init \
    --control-plane node1.example.com \
    --state-store etcd
```

Then:

```bash
ccattler join-control-plane node2.example.com
ccattler join-control-plane node3.example
```

### Workers

```bash
ccattler join-worker node4.example.com
ccattler join-worker node5.example.com
ccattler join-worker node6.example.com
```

The installer would:

1. install `ccattler-agent`
2. install runtime dependencies
3. establish node identity
4. obtain certificates
5. register node
6. configure network provider
7. start agent
8. verify heartbeat
9. mark node ready

Similar user experience to Kubernetes, **without adopting Kubernetes's object model**.

---

# 14. But don't build the installer yet

This is important.

Your current project is **not ready for this stage yet**, and that's okay.

I'd build toward it in this order:

```text
                    CURRENT
                       │
                       ▼
               single-process
                  simulator
                       │
                       ▼
                real processes
                       │
                       ▼
             multiple logical nodes
                       │
                       ▼
                  containers
                       │
                       ▼
                 SQLite state
                       │
                       ▼
              multi-process agent
                       │
                       ▼
                    etcd
                       │
                       ▼
             multiple physical hosts
                       │
                       ▼
              HA control plane
```

This prevents you from debugging **distributed systems + containers + networking + scheduling + persistence** simultaneously.

---

## The architecture I would aim for

Ultimately:

```text
                     CCATTLER

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
                  │               │
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
       │        │        │
    Host A   Host B   Host C
       │        │        │
   runtime   runtime  runtime
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

That gives you the **operational shape of Kubernetes**—control plane, workers, scheduler, distributed state, agents, networking, storage, HA—while keeping the **conceptual model of CCattler**.

And the most important design rule remains:

> **The control plane does not tell machines what happened. It records what should be true. Agents make reality converge and report what actually happened.**

That is the foundation I'd use for a serious multi-host CCattler v0.1/v0.2.
