Yes. Let's make it concrete.

I'd design it as a distributed declarative management system, but deliberately avoid making objects the center of the architecture.

The central idea is:

The database stores facts about the desired and observed world. Controllers derive actions from those facts. Nodes execute actions and report facts back.

I'll call the system Orbit for now.

1. Overall architecture
                         ┌───────────────┐
                         │   CLI / API   │
                         └───────┬───────┘
                                 │
                                 ▼
                      ┌────────────────────┐
                      │   Command Engine   │
                      │ parse / validate   │
                      │ authorize / commit │
                      └─────────┬──────────┘
                                │
                                ▼
                  ┌───────────────────────────┐
                  │    DISTRIBUTED STORE      │
                  │                           │
                  │ desired + observed facts  │
                  │ indexes + leases          │
                  └────────────┬──────────────┘
                               │
                         watches / changes
                               │
            ┌──────────────────┼──────────────────┐
            ▼                  ▼                  ▼
       Reconciler          Scheduler          Network
            │                  │                  │
            └──────────────────┼──────────────────┘
                               │
                               ▼
                       ┌───────────────┐
                       │ Node Agents   │
                       └───────┬───────┘
                               │
                               ▼
                          Linux / VM

The important thing is that controllers don't call each other.

They communicate through state.

2. The configuration language

A user writes:

service web {
    image nginx:1.28
    instances 3

    expose 8080

    resources {
        cpu 500m
        memory 512Mi
    }

    health {
        http "/health"
        every 10s
    }
}

For a database:

service database {
    image postgres:18
    instances 1

    volume data {
        size 100Gi
        persistent true
    }

    resources {
        cpu 2
        memory 4Gi
    }
}

For placement:

service web {
    image nginx:1.28
    instances 3

    placement {
        architecture amd64
        zone spread
    }
}

No:

apiVersion
kind
metadata
spec
status

Those are implementation/API concepts, not things the user should need to understand.

3. What gets stored?

The parser turns that into facts.

For example:

service("web")
image("web", "nginx:1.28")
desired_instances("web", 3)

exposes("web", 8080)

requires_cpu("web", 500m)
requires_memory("web", 512Mi)

health_http("web", "/health")
health_interval("web", 10s)

The database might physically store them as:

facts/
    service/web
    service/web/image
    service/web/desired_instances
    service/web/expose/8080
    service/web/resources/cpu
    service/web/resources/memory
    service/web/health/path
    service/web/health/interval

But that's an implementation detail.

The conceptual model is:

              FACTS
                │
       ┌────────┼─────────┐
       ▼        ▼         ▼
    desired   observed   metadata
4. Desired state and observed state are separate

This is extremely important.

Suppose the user requests:

instances = 3

We store:

desired_instances(web) = 3

But the node agents report:

running_instances(web) = 2

We never overwrite desired state with reality.

Instead:

DESIRED                         OBSERVED

web = 3                         web-1 running
                                web-2 running
                                web-3 missing

The difference is the work.

desired = 3
actual  = 2

difference = +1

The reconciler derives:

create_instance(web)
5. Instances don't need to be user-facing objects

Here's an important distinction.

Internally we obviously need identifiers:

instance/web/a8f31
instance/web/b72c9
instance/web/c913d

But these aren't "objects" in the conceptual sense.

They're simply identities attached to facts.

For example:

instance(web, a8f31)
runs_on(a8f31, node-2)
state(a8f31, running)

This is closer to a relational model.

6. Nodes

A node agent periodically publishes:

node(node-1)
node_state(node-1, alive)

capacity_cpu(node-1, 16)
capacity_memory(node-1, 64Gi)

available_cpu(node-1, 11)
available_memory(node-1, 48Gi)

architecture(node-1, amd64)
zone(node-1, eu-west-1)

The node also maintains a lease:

lease/node-1

with an expiry.

If the agent disappears:

lease expired

the control plane derives:

node_state(node-1, unreachable)
7. Node failure

Suppose:

web-1 → node-1
web-2 → node-1
web-3 → node-2

and node-1 dies.

Observed state becomes:

node-1 = unreachable

The scheduler now knows:

web:
    desired = 3
    healthy = 1

It derives:

replacement web-1
replacement web-2

and places them:

web-4 → node-3
web-5 → node-4

Notice what doesn't happen:

There is no:

DeploymentController calls ReplicaSetController
    calls PodController
        calls Scheduler

Instead:

FACT CHANGE
    ↓
controllers observe it
    ↓
derive new facts/actions

Much cleaner.

8. But we need to prevent two controllers from fighting

This is where transactional semantics matter.

Suppose two schedulers simultaneously see:

web needs one instance

Both might try:

place web-4 on node-2

So the store needs compare-and-swap transactions.

Conceptually:

transaction {
    read service/web revision

    if revision == 8172 {
        create instance/web/a8f31
        assign a8f31 → node-2
    }
}

If another scheduler changed the state first:

revision != 8172

the transaction fails.

The scheduler rereads state and recalculates.

This is a crucial property.

9. Scheduler design

I'd make scheduling mostly a pure computation:

schedule(requirement, nodes, existing_placements)
    → placement

For example:

requirement:
    cpu = 500m
    memory = 512Mi
    architecture = amd64

Candidate nodes:

node-1:
    available = 2 CPU
    zone = eu-a

node-2:
    available = 10 CPU
    zone = eu-b

node-3:
    available = 1 CPU
    zone = eu-a

It might calculate:

node-2

Then a transaction commits the decision.

10. Don't store "actions" as the source of truth

This is subtle and important.

I would not make the database primarily:

commands:
    start container
    stop container
    move container

because commands are transient.

Instead store the desired result:

desired instance a8f31 {
    node = node-2
    state = running
}

The node agent compares:

desired:
    running on node-2

actual:
    stopped

and executes:

start()

Then reports:

actual:
    running

This makes the system naturally retryable.

If start() fails:

desired = running
actual  = stopped

Nothing special needs to happen.

The agent tries again.

11. Idempotency becomes fundamental

Every operation should be safe to repeat.

Bad:

start_container()

where calling it twice causes trouble.

Better:

ensure_running(container)

Likewise:

ensure_attached(volume, node)
ensure_route(service, endpoint)
ensure_image(image)
ensure_directory(path)

The whole system becomes:

Ensure reality satisfies desired state.

That's the fundamental primitive.

12. Node agent

The node agent has three components:

              NODE AGENT
                  │
       ┌──────────┼──────────┐
       ▼          ▼          ▼
    observer   reconciler  reporter
Observer

Reads Linux:

/proc
/sys
cgroups
network
container runtime
filesystem

and determines:

what is actually running?
Reconciler

Reads:

desired state

and executes:

ensure(...)
Reporter

Publishes:

actual state
health
capacity
events
13. Container runtime

I'd avoid making Orbit dependent on Kubernetes' container abstraction.

The node agent could use:

containerd
CRI-O
Podman
systemd-nspawn

or eventually its own lightweight runtime.

The interface could simply be:

ensure_container(
    id,
    image,
    resources,
    environment,
    mounts,
    network,
    desired_state
)

Again, the user doesn't see this abstraction.

14. Networking

Suppose:

service web
instances = 3
port = 8080

Observed:

web-1 = 10.1.0.5
web-2 = 10.1.0.9
web-3 = 10.1.0.12

The network controller derives:

route web:
    10.1.0.5:8080
    10.1.0.9:8080
    10.1.0.12:8080

When web-2 dies:

web:
    10.1.0.5
    10.1.0.12

No explicit:

remove_endpoint()

needs to be sent.

The desired network state changes because the underlying facts changed.

15. Storage

Storage would use the same mechanism.

User:

volume database.data {
    size 100Gi
    persistent true
}

Facts:

volume(data)
volume_size(data, 100Gi)
volume_persistent(data, true)

If database moves:

database → node-4

storage derives:

attach(data, node-4)

Node agent ensures:

volume attached
filesystem mounted
16. Events

Events are still useful, but they're not authoritative state.

I'd maintain an append-only event stream:

2026-09-06 18:42 service.web.changed
2026-09-06 18:42 instance.a8f31.created
2026-09-06 18:42 instance.a8f31.placed
2026-09-06 18:42 instance.a8f31.started
2026-09-06 18:45 node.node-2.unreachable
2026-09-06 18:45 instance.a8f31.failed

This gives operators an explanation of how the system got here.

But current truth remains in the state store.

So:

STATE = truth
EVENTS = history

That's a useful separation.

17. What database?

For the first serious implementation I'd use etcd or FoundationDB, but I'd hide it behind our own interface.

Something like:

interface StateStore {

    read(query)

    write(transaction)

    watch(query)

    lease(ttl)

}

The rest of Orbit never knows whether the backend is:

etcd
FoundationDB
Postgres
something else

That's important because the semantic model belongs to Orbit, not the database.

18. Controller interface

Controllers become remarkably small.

Something like:

interface Controller {

    name()

    watch()

    reconcile(facts)

}

But I'd go further and make reconciliation:

facts → proposed changes

rather than allowing arbitrary database manipulation.

For example:

reconcile_web(facts)

returns:

ensure_instance(web)
ensure_instance(web)
ensure_instance(web)

The command layer validates and commits those changes transactionally.

19. Controller independence

Imagine these controllers:

service controller
scheduler
network controller
storage controller
health controller
autoscaler
security controller

None needs to know the implementation of the others.

The dependency is:

                    FACT STORE
                        │
        ┌───────────────┼────────────────┐
        │               │                │
        ▼               ▼                ▼
     scheduler       network          storage
        │               │                │
        ▼               ▼                ▼
     placement       routes          attachments

That's a much more scalable mental model.

20. Autoscaling

Now something interesting becomes possible.

User:

service web {
    instances 3

    autoscale {
        cpu > 70%
        min 3
        max 30
    }
}

The autoscaler observes:

cpu(web) = 84%

and changes:

desired_instances(web) = 5

The service reconciler doesn't care why desired instances became 5.

It just sees:

desired = 5
actual = 3

and creates two more.

This is a beautiful property of the architecture.

21. Multiple writers

Now we hit a harder problem.

Who owns:

desired_instances(web)

The user says 3.

Autoscaler says 5.

We can't simply let them overwrite each other.

So I'd introduce intent layers:

user intent:
    min = 3
    max = 30

autoscaler decision:
    desired = 5

The effective desired state is derived:

effective_instances(web) = 5

Likewise:

policy
user
autoscaler
scheduler
health

can contribute different facts.

This is another advantage of a fact-oriented architecture: different kinds of intent don't have to fight over the same giant object.

22. Security

Permissions could also operate on facts.

For example:

alice:
    can modify service/*
    cannot modify node/*

and:

deployment-agent:
    can modify observed/*
    cannot modify desired/user/*

This gives us an important security boundary:

                 ┌───────────────┐
                 │ desired/user  │
                 └───────┬───────┘
                         │
                     controllers
                         │
                 ┌───────▼───────┐
                 │ observed      │
                 └───────────────┘

A compromised node should not be able to say:

desired_instances(database) = 0

It should only be able to report:

database is currently stopped

That's a very clean security model.

23. Failure of the control plane

I'd run 3 or 5 control-plane nodes.

             ┌────────────┐
             │ controller │
             └─────┬──────┘
                   │
          ┌────────┼────────┐
          ▼        ▼        ▼
       store-1  store-2  store-3
          │        │        │
          └──── consensus ──┘

The store provides consensus.

Controllers can be mostly stateless and run multiple copies:

scheduler-1
scheduler-2
scheduler-3

If scheduler-1 dies, scheduler-2 takes over because state is shared.

That means we don't need complicated distributed coordination between controllers beyond transactional state changes.

24. What happens when everything disappears?

This is one of the best tests of the design.

Suppose:

control plane dies for 10 minutes

Nodes still have:

last known desired state

They can continue running workloads.

When control plane returns:

observed state
       +
desired state
       ↓
reconciliation

The cluster converges again.

That's an important design goal:

Temporary control-plane failure should not automatically destroy data-plane availability.

25. The API becomes tiny

Instead of hundreds of Kubernetes resource endpoints, I'd want perhaps:

GET    /state
QUERY  /state
APPLY  /config
WATCH  /changes

For example:

QUERY services
WHERE name = "web"

or:

QUERY instances
WHERE service = "web"
AND state = "running"

And:

APPLY

service web {
    image nginx:1.28
    instances 5
}

That's much closer to a management database with a domain language than an object REST API.

26. The entire system in one equation

The architecture can almost be summarized as:

              Desired
                 +
              Observed
                 +
              Policies
                 +
               Facts
                 │
                 ▼
          ┌──────────────┐
          │   DERIVATION │
          └──────┬───────┘
                 │
                 ▼
             new desired
                 │
                 ▼
          ┌──────────────┐
          │   EXECUTION  │
          └──────┬───────┘
                 │
                 ▼
              Reality
                 │
                 ▼
             Observed
                 │
                 └───────────────┐
                                 │
                                 ▼
                             DERIVATION

That's the system.

27. What I'd actually build first

I wouldn't attempt "Kubernetes replacement" immediately.

I'd build this sequence:

Phase 1 — single machine
CLI
 ↓
config parser
 ↓
state store
 ↓
reconciler
 ↓
Linux process/container

Support:

service
image
instances
resources

No networking, no cluster.

Phase 2 — three machines

Add:

node agents
leases
scheduler
distributed state

Now:

service web {
    instances 10
}

actually distributes ten instances.

Phase 3 — networking

Add:

expose
DNS
service discovery
routing
Phase 4 — storage

Add:

volume
persistent
mount
Phase 5 — failure

Test:

kill node
kill agent
kill scheduler
kill controller
partition network
restart database

The only question should be:

Does the system eventually converge to the desired state?

Phase 6 — policies

Add:

autoscaling
placement
affinity
anti-affinity
rolling updates
health checks
28. The most important design decision

I'd make this the architectural rule:

No component is allowed to assume that another component performed an action. Components only react to state.

So don't write:

scheduler → tell network controller
scheduler → tell storage controller

Write:

scheduler
   ↓
placement fact
   ↓
state store
   ↓
network controller notices it
storage controller notices it
node agent notices it

That gives you loose coupling, retries, crash recovery, replayability, and horizontal scaling almost for free.

And it gets us very far away from the object-oriented worldview you were questioning.

The resulting mental model

Kubernetes:

API Objects
    ↓
Controllers
    ↓
More Objects
    ↓
Controllers
    ↓
Actions

Our system:

               FACTS
                 ↓
          ┌────────────┐
          │   RULES    │
          └─────┬──────┘
                ↓
             STATE
                ↓
             ACTION
                ↓
             REALITY
                ↓
           OBSERVATION
                ↓
              FACTS

And the user's configuration is simply:

service web {
    image nginx:1.28
    instances 3
    expose 8080
}

rather than a serialization of an internal object graph.

That is the architecture I'd choose.

The next thing I'd design is the actual store in detail—the key layout, revisions, transactions, leases, watches, leader election, and exactly what happens during a network partition. That's where the design either becomes a robust distributed system or falls apart.