Absolutely. In fact, I would design the first implementation specifically to run on one laptop. You don't want to start by building a distributed system; you want to prove the semantics first.

The trick is to make the architecture distributed by interface, but locally executable.

1. Start with a single-process "mini cluster"

On your laptop:

                    orbit
                      │
        ┌─────────────┼─────────────┐
        │             │             │
      Store        Controllers    Runtime
        │             │             │
        │        ┌────┴────┐        │
        │        │         │        │
        │    Scheduler  Autoscaler  │
        │        │         │         │
        └────────┴─────────┴─────────┘
                      │
                 Local Linux

You don't need Kubernetes, Docker, VMs, or even containers initially.

Your laptop itself can be the cluster.

2. Build the state store first

Start with an in-memory implementation:

StateStore
    get()
    query()
    transaction()
    watch()

Something like:

StateStore
    │
    ├── MemoryStore       ← laptop development
    │
    ├── SQLiteStore       ← persistence/testing
    │
    └── DistributedStore  ← production

The controllers shouldn't know which one they're using.

That is extremely important.

Your scheduler should see:

store.query(...)

not:

postgres.query(...)

or:

etcd.get(...)
3. Use SQLite before a distributed database

For the first real implementation I'd probably use SQLite.

Not because SQLite is the eventual backend, but because it gives you:

transactions
persistence
indexing
SQL queries
crash/restart testing
very little infrastructure

You can run:

./orbit

and have everything stored in:

orbit.db

Later:

StateStore
      │
      ├── SQLite
      │
      └── FoundationDB / etcd

without rewriting the control logic.

4. Don't implement the entire platform

The first milestone should be tiny.

I'd make this work:

service web {
    image nginx
    instances 3
}

Then:

orbit apply web.orb

State becomes:

service(web)
image(web, nginx)
desired_instances(web, 3)

The scheduler produces:

placement(web, local-node, 3)

The runtime reconciler produces:

instance(web, 1)
instance(web, 2)
instance(web, 3)

And your laptop actually runs them.

5. Use Linux processes initially

Don't even start with containers.

If the desired state says:

instances(web) = 3

the runtime can simply launch:

nginx
nginx
nginx

or, for an easier test:

sleep 100000
sleep 100000
sleep 100000

Give each process an identity:

instance(web, abc123)
instance(web, def456)
instance(web, ghi789)

Now you've already tested the fundamental reconciliation model.

6. Then introduce containers

Once the process-based runtime works:

Orbit
  ↓
container runtime
  ↓
container

I'd use an OCI-compatible runtime rather than making Orbit responsible for container mechanics.

For example:

Orbit runtime adapter
        ↓
     containerd

or another OCI runtime.

The architecture remains:

desired state
     ↓
reconciler
     ↓
runtime adapter
     ↓
Linux
7. You can simulate multiple nodes on one laptop

This is where it gets interesting.

You don't need three physical machines.

Represent:

node laptop-1
node laptop-2
node laptop-3

as three logical nodes.

Initially they can all run inside the same OS.

Laptop
│
├── logical-node-1
│
├── logical-node-2
│
└── logical-node-3

Give each node:

cpu = 4
memory = 8Gi
zone = local-a

The scheduler doesn't know they're fake.

It simply sees:

node(laptop-1)
capacity_cpu(laptop-1, 4)

node(laptop-2)
capacity_cpu(laptop-2, 4)

node(laptop-3)
capacity_cpu(laptop-3, 4)

That's enough to test scheduling.

8. Then deliberately kill nodes

This is one of the most valuable tests.

Start:

node-1
node-2
node-3

Place:

web-1 → node-1
web-2 → node-2
web-3 → node-3

Now simulate:

node-2 DEAD

The node lease expires.

State becomes:

node-2 = unavailable
web-2 = missing

Reconciliation derives:

replacement required

Scheduler chooses:

web-2 → node-1

The runtime launches it.

You have just tested one of the core Kubernetes-like behaviors without Kubernetes.

9. Autoscaling can also run entirely locally

You don't need Prometheus initially.

Create a fake metric source:

MetricSource

For example:

cpu(web) = 80%

Autoscaler:

target = 50%

derives:

desired_instances = 5

Then the normal reconciler creates five processes.

You can even make a CLI:

orbit metric set web cpu 90

Then:

metric
  ↓
autoscaler
  ↓
desired_instances = 5
  ↓
reconciler
  ↓
5 processes
10. Event-driven scaling is equally easy to simulate

Create:

orbit metric set queue/payments depth 800

Policy:

20 messages / instance

The system derives:

800 / 20 = 40

Then:

desired_instances(payment-worker) = 40

Your laptop probably shouldn't actually launch 40 heavyweight containers, but that's okay.

You can have a simulation runtime:

SimulationRuntime

which says:

requested = 40
simulated_running = 40

without actually launching anything.

This gives you two runtime modes:

                Runtime
                   │
          ┌────────┴────────┐
          │                 │
      Simulator           Linux
          │                 │
      fake world       real processes

That will be incredibly useful for testing.

11. Security can also be simulated

You don't need to build the CA infrastructure on day one.

Create principals:

alice
bob
scheduler
network-controller
node-1

And policies:

alice → modify /payments/**
bob   → read /payments/**
node-1 → write /observed/nodes/node-1/**

Then test:

alice modifies payments
    → ALLOW

bob modifies payments
    → DENY

node-1 modifies desired state
    → DENY

Later replace the simulated identity provider with:

OIDC
mTLS
certificates
SPIFFE

The authorization engine doesn't need to change.

12. You can test multi-tenancy too

On one laptop:

tenant payments
tenant frontend
tenant analytics

Give them quotas:

payments  = 10 instances
frontend  = 5 instances
analytics = 20 instances

Then:

orbit apply payments.orb

Try requesting:

instances = 15

The policy engine should produce:

DENIED

reason:
tenant quota exceeded

requested: 15
limit:     10

Again, no Kubernetes required.

13. The ideal development progression

I'd build it in these stages:

Phase 0 — Pure simulation
config
 ↓
facts
 ↓
rules
 ↓
desired state
 ↓
simulated reality

No Linux processes.

This lets you develop the semantic engine quickly.

Phase 1 — Real processes
desired state
 ↓
Linux processes

Now reconciliation is real.

Phase 2 — Logical nodes
node-1
node-2
node-3

on one laptop.

Test scheduling and failure.

Phase 3 — Containers
Orbit
 ↓
container runtime
Phase 4 — Persistence
SQLite

Test restart and recovery.

Phase 5 — Security
identity
 ↓
authentication
 ↓
authorization
 ↓
audit
Phase 6 — Distributed control plane

Only now introduce:

          orbit-1
         /       \
    orbit-2 ----- orbit-3
          \       /
        distributed store

Replace:

SQLiteStore

with:

DistributedStore

The controllers remain almost unchanged.

14. And here's the really important testing strategy

I'd make the entire control plane deterministic.

Given:

state A
+
observation B
+
policy C

the controller should produce:

desired state D

That means you can write tests like:

INPUT

nodes:
    n1 = 4 CPU
    n2 = 4 CPU

service:
    desired = 5
    cpu = 1


EXPECTED

placement:
    n1 = 3
    n2 = 2

Then:

INPUT

n2 = dead

Expected:

placement:
    n1 = 4
    unsatisfied = 1

Then bring n2 back:

EXPECTED

placement:
    n1 = 3
    n2 = 2

These tests don't need a cluster.

15. Eventually you can have a "chaos mode"

This would be particularly fun for this architecture.

orbit chaos

Randomly inject:

node failures
network delays
process crashes
stale observations
controller restarts
duplicate events
lost messages
slow storage

Then assert:

Eventually observed state converges to a state satisfying all constraints.

That's arguably a more important test than whether the CLI accepts some particular configuration syntax.

What I'd actually build on the laptop

The first repository could be remarkably small:

orbit/
│
├── parser/
│   └── config language
│
├── facts/
│   └── fact model
│
├── state/
│   ├── store.go
│   └── sqlite.go
│
├── policy/
│   ├── authorization.go
│   ├── quota.go
│   └── network.go
│
├── controllers/
│   ├── scheduler.go
│   ├── reconciler.go
│   ├── autoscaler.go
│   └── garbage_collector.go
│
├── runtime/
│   ├── simulator.go
│   └── linux.go
│
├── identity/
│   └── local.go
│
├── cli/
│   └── orbit.go
│
└── tests/
    ├── scheduling/
    ├── failures/
    ├── scaling/
    ├── security/
    └── tenancy/

And the first command I'd want working is simply:

orbit run example.orb

with:

service web {
    image demo
    instances 3

    scale horizontally {
        min 2
        max 10
        cpu 60%
    }
}

Then:

$ orbit status

SERVICE    DESIRED    RUNNING    CPU
web        3          3          41%

Change the simulated load:

$ orbit metric set web cpu 90

and eventually:

$ orbit status

SERVICE    DESIRED    RUNNING    CPU
web        5          5          90%

That would give you a real, locally executable proof of the architecture before committing to the much harder problems of distributed consensus, container networking, certificates, and multi-machine operation.

The most important architectural decision at this stage is therefore: build the semantic control plane first, and make Linux/container/distributed infrastructure replaceable adapters underneath it.