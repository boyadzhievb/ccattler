I checked the current master branch of CCattler specifically for the authorization direction. The repository is currently Go-based and was updated on September 28, 2026.

Short answer

The implementation does not appear to have an authorization system yet.

I searched the current code for authorization/RBAC/ABAC concepts, permissions, roles, groups, principals, policies, authentication, TLS identity, OIDC/JWT, etc., and found no implemented authorization subsystem.

So the good news is: we haven't accidentally committed to a bad Kubernetes-style authorization model yet.

That means we can design this properly before it becomes entangled with the rest of CCattler.

Where CCattler currently stands

The architecture we've been building has several things that authorization will eventually attach to:

                    Principal
                       │
                       ▼
                    API
                       │
                       ▼
                 Desired state
                       │
              ┌────────┴────────┐
              │                 │
          Controllers          Store
              │                 │
              ▼                 ▼
          Reconciliation     Facts

But currently there isn't a gate like:

Principal
    ↓
Authorize
    ↓
Policy
    ↓
Mutation

That is the missing layer.

And we should put it at the API/state-mutation boundary, rather than trying to bolt authorization into every controller.

I would change our previous design slightly

After looking at the actual project direction, I think the Linux groups idea is better as the foundation than introducing Kubernetes-style Roles/RoleBindings.

I'd make CCattler's authorization model:

IDENTITY
   │
   ├── user
   ├── group
   └── service identity
          │
          ▼
     PERMISSION
          │
          ▼
        SCOPE
          │
          ▼
      CONDITIONS
          │
          ▼
    ALLOW / DENY

For example:

alice
  groups:
    - developers
    - payments

Then policy:

group developers {
    allow workload.read
    allow workload.create
    allow workload.update
}

group payments {
    allow scope team/payments
}

Therefore:

alice
  + developers
  + payments
       │
       ▼
workload.update
team/payments/api
       │
       ▼
      ALLOW

But:

alice
  + developers
  + payments
       │
       ▼
workload.update
team/billing/api
       │
       ▼
      DENY

This is much closer to Unix's user/group/permission philosophy.

The important part: groups shouldn't be stored as "Linux groups" internally

I would make the abstraction:

type Principal struct {
    ID     string
    Groups []string
    Kind   PrincipalKind
}

Where:

PrincipalKind:
    User
    Service
    Node

Then the authentication layer can populate the groups.

For a laptop:

/etc/passwd
/etc/group

could be the source.

For an enterprise installation:

LDAP
Active Directory
FreeIPA
OIDC

could be the source.

For a CCattler-native installation:

CCattler identity store

could be the source.

The authorization engine doesn't care where the groups came from.

Service identities are critical

This is where I would not simply copy Linux groups.

CCattler has internal actors:

scheduler
network-controller
storage-controller
autoscaler
node-agent/node01

They need identities.

For example:

identity scheduler

allow:
    workload.read
    node.read
    placement.write

Whereas:

identity network-controller

allow:
    endpoint.read
    network.write

And:

identity node/node01

allow:
    assignment.read
        scope node/node01

    observation.write
        scope node/node01

That last one is particularly important.

A compromised node shouldn't be able to say:

"I am node01, therefore I can write observations for node02."

The authorization decision must include the node identity/scope.

This fits our StateStore architecture very well

We previously established that the store is the authoritative mutation boundary.

So I would introduce something like:

type AuthorizationRequest struct {
    Principal Principal
    Action    Action
    Resource  ResourceRef
    Scope     Scope
    Context   Attributes
}

type Authorizer interface {
    Authorize(
        context.Context,
        AuthorizationRequest,
    ) (Decision, error)
}

Then:

API request
     │
     ▼
Authenticate
     │
     ▼
Principal
     │
     ▼
Authorize
     │
 ┌───┴────┐
DENY     ALLOW
 │         │
403        ▼
         Policy/
         admission
           │
           ▼
      State transaction

This is much safer than:

API
 ↓
write StateStore
 ↓
hopefully some controller checks permission

The latter is fundamentally wrong because unauthorized state has already entered the system.

And authorization must apply to controllers too

This is particularly important given the architecture we've developed.

We shouldn't have:

API users → authorization
controllers → trusted forever

Instead every actor has an identity.

For example:

Principal	Can read	Can write
alice / developers	workloads in permitted scopes	workloads in permitted scopes
scheduler	workload requirements, node facts	placement
network controller	endpoints/network facts	network state
autoscaler	metrics/workload state	scaling intent
node01	node01 assignments	node01 observations
storage controller	storage state	storage state

This also reinforces the state ownership model we've already been moving toward.

Authorization and state ownership become complementary:

Authorization:
    "Can you write this?"

Ownership:
    "Is this your domain to write?"

Both should be checked.

What I would NOT implement

I would avoid making CCattler's model:

Role
ClusterRole
RoleBinding
ClusterRoleBinding

just because Kubernetes does.

Kubernetes' RBAC is designed around its API resources and verbs; its authorization system evaluates identities and permissions against API/resource attributes.

CCattler doesn't need to inherit that resource hierarchy.

Instead:

group
permission
scope
condition

is enough for the initial model.

And if later we need richer policies:

ABAC conditions

can be added without replacing the basic system.

One more thing: authorization ≠ policy

I'd explicitly maintain three layers:

Authentication
    "Who are you?"

Authorization
    "Are you allowed to do this?"

Admission / Policy
    "Is this operation acceptable?"

Example:

alice
 │
 │ authenticated
 ▼
developers + payments
 │
 │ authorized?
 ▼
YES
 │
 │ workload.create
 ▼
Policy
 │
 ├── image trusted?       YES
 ├── quota available?     YES
 ├── privileged?          NO
 └── allowed region?      YES
 │
 ▼
commit desired state

That gives us a very clean architecture.

My assessment of the current project

I'd currently classify authorization as:

Architecture: not implemented yet

Design direction: still open

Good news: no major authorization-model debt found

Priority: P0 architecture decision / P1 implementation

And I would add this explicitly to chat-plan-20sep.md as a security architecture workstream:

Authorization
├── Principal model
├── User/group model
├── Service identities
├── Node identities
├── Permission vocabulary
├── Scope enforcement
├── Optional ABAC conditions
├── Authorization API
├── API mutation enforcement
├── StateStore mutation boundary
├── Controller authorization
├── Secret authorization
├── Network authorization
├── Audit records
├── deny-by-default
├── fail-closed behavior
└── authorization tests

The next thing I'd design before writing code is the exact permission vocabulary and scope model. That decision will influence namespaces, secrets, network policies, controllers, the API, and even the DSL.

What should be the next step in architecture of the authorization 

The next step should be to freeze the authorization model before implementing RBAC/ABAC code.

For CCattler, I would do it in this order:

1. Define the security principals

Start with only three:

User
Group
ServiceIdentity

Examples:

User:
  alice

Groups:
  developers
  payments

ServiceIdentity:
  scheduler
  network-controller
  node/node01

Don't introduce Kubernetes ServiceAccount, Role, or RoleBinding equivalents yet.

2. Define the permission vocabulary

This is the most important next design decision.

I'd make permissions capabilities, rather than Kubernetes verbs.

For example:

workload.read
workload.create
workload.update
workload.delete

service.read
service.update

network.read
network.update

storage.read
storage.update

secret.metadata.read
secret.use

node.read
node.manage

placement.read
placement.write

scaling.read
scaling.write

Notice the distinction:

secret.metadata.read

versus:

secret.read

A user may be allowed to know that a secret exists without being allowed to retrieve its value.

3. Define scopes

Then define where a permission applies.

I'd use the CCattler namespace/scope architecture we're already discussing:

cluster
team/payments
team/frontend
team/analytics
node/node01

So a permission becomes:

WHAT + WHERE

For example:

workload.update
scope: team/payments

This is considerably simpler than Kubernetes' distinction between namespaced and cluster-scoped resources.

4. Define conditions

Only after the basic model works should we introduce ABAC.

For example:

allow workload.update
  scope team/payments
  when owner == principal

Or:

allow observation.write
  scope node/node01
  when principal == node/node01

This is particularly important for node agents.

5. Define the authorization decision

Freeze one central API:

type AuthorizationRequest struct {
    Principal Principal
    Action    Action
    Resource  ResourceRef
    Scope     Scope
    Context   Attributes
}

type Decision struct {
    Allowed bool
    Reason  string
}

Then everything goes through this abstraction.

CLI
 │
API
 │
 ▼
Authorize()
 │
 ├── DENY → stop
 │
 └── ALLOW
       │
       ▼
    admission
       │
       ▼
   state mutation
6. Define policy source

Only then decide where policies live.

For the first implementation, I'd strongly consider:

/etc/ccattler/authorization.conf

for a single-host installation, with a distributed equivalent later.

For example:

group developers {
    allow workload.read
    allow workload.create
    allow workload.update
}

group payments {
    allow scope team/payments
}

But don't implement the parser yet.

First define its semantics independently of its syntax.

7. Define authentication separately

Then establish how the Principal gets constructed:

Local Linux user/group
        │
        ▼
   Authentication
        │
        ▼
Principal{alice, [developers,payments]}

Later:

OIDC
LDAP
mTLS
certificates

can produce the same Principal.

This keeps authentication replaceable.

8. Define the trust boundaries

This is particularly important for CCattler:

                 ┌───────────────┐
                 │     User      │
                 └───────┬───────┘
                         │
                    authenticate
                         │
                         ▼
                    ┌─────────┐
                    │   API   │
                    └────┬────┘
                         │
                     authorize
                         │
                         ▼
                    desired state
                         │
             ┌───────────┼───────────┐
             ▼           ▼           ▼
         Scheduler     Network     Agent
             │           │           │
             └───────────┴───────────┘

Each internal component gets its own identity and permissions.

The immediate deliverable

I would make the next architecture document:

docs/authorization.md

containing only these decisions:

1. Security principals
2. Groups
3. Service identities
4. Permission vocabulary
5. Scope model
6. Conditions
7. Authentication → Principal mapping
8. Authorization decision algorithm
9. Deny/fail-closed semantics
10. State mutation boundary
11. Controller permissions
12. Node-agent permissions
13. Secret permissions
14. Audit requirements
15. Future OIDC/LDAP/mTLS integration

Then write table-driven authorization tests against that specification before implementing the engine.

For example:

Principal	Action	Scope	Result
alice + developers	workload.read	team/payments	Allow
alice + developers	workload.update	team/payments	Allow
alice + developers	workload.update	team/billing	Deny
node/node01	observation.write	node/node01	Allow
node/node01	observation.write	node/node02	Deny
scheduler	placement.write	cluster	Allow
scheduler	secret.read	cluster	Deny

That specification is the real next architectural step. Once it is stable, implementing the authorization engine becomes relatively mechanical, and we avoid accidentally designing CCattler around Kubernetes' RBAC model.

What do you think needs stabilization?

For authorization specifically, I think the core idea is already clear enough. What needs stabilization is the set of boundaries and semantics that everything else will depend on.

I'd stabilize these in this order:

1. Principal model — stabilize now

Decide exactly what can make an authorization request:

User
Group
ServiceIdentity
NodeIdentity

I'd actually model NodeIdentity as a specialized ServiceIdentity rather than another top-level type.

Principal
 ├── User
 └── ServiceIdentity
       └── NodeIdentity

Also decide whether groups are attributes of a user supplied by authentication, rather than authorization objects.

This should be frozen.

2. Permission vocabulary — stabilize now

Don't start coding until we know what "permission" means.

I'd avoid:

get
list
watch
create
update
delete

as the fundamental vocabulary because those are strongly tied to Kubernetes APIs.

Instead:

workload.read
workload.create
workload.update
workload.delete

network.read
network.update

placement.read
placement.write

secret.metadata.read
secret.use

The question to settle is:

Are permissions about capabilities or API operations?

For CCattler, I'd choose capabilities.

3. Scope — absolutely stabilize

This is probably the most important unresolved piece.

We need to decide what a scope actually is.

Potential hierarchy:

cluster
 ├── team/payments
 │    ├── workload/api
 │    └── workload/worker
 │
 └── team/frontend

But then we need precise rules:

Does permission on team/payments
automatically apply to children?

Can permissions be delegated?

Can a child scope restrict permissions inherited from its parent?

Can a principal belong to multiple scopes?

Can a workload exist outside a scope?

Until these are defined, authorization cannot be implemented correctly.

4. Resource identity

We need a stable answer to:

What exactly are we authorizing against?

Not Kubernetes objects.

Probably something like:

workload/payments/api
service/payments/api
secret/payments/database
network-policy/payments/frontend

This should align with the key scheme in the StateStore.

Otherwise we'll eventually have:

authorization resource names
        ≠
state-store keys
        ≠
API resource names

That would become painful technical debt.

5. Ownership vs authorization

This distinction needs to be explicit.

Suppose:

alice → workload.update

Does that mean Alice can update any workload in her scope?

Or only workloads she owns?

Those are different:

Authorization:
    "Alice may update workloads."

Ownership:
    "This workload belongs to Alice."

I'd keep them separate.

Then ABAC can optionally say:

allow workload.update
when owner == principal

rather than baking ownership into every permission.

6. Service identity permissions

This needs stabilization before controllers mature further.

For example:

scheduler
    workload.read
    node.read
    placement.write

and:

node/node01
    assignment.read
    observation.write

We need a hard rule:

Every component that can mutate authoritative state has an identity and an explicit write domain.

This fits extremely well with the controller/state ownership work we've already done.

7. Secrets

I'd stabilize this separately because secret.read is too dangerous as a generic permission.

I'd distinguish:

secret.metadata.read
secret.use
secret.create
secret.rotate
secret.delete

A workload might have:

secret.use

without its operator having:

secret.value.read

That's an important security boundary.

8. Authentication boundary

We don't need to implement OIDC/LDAP/etc. yet.

But we do need to stabilize this contract:

Authentication
       ↓
Principal
       ↓
Authorization

Authorization must never care whether Alice came from:

Linux
LDAP
OIDC
certificate

It receives a trusted Principal.

That gives us freedom later.

9. Deny semantics

This should be extremely explicit:

default = DENY

And:

authentication failure → deny
authorization failure → deny
policy engine unavailable → deny
identity information incomplete → deny

For management-plane operations, I would make fail closed the default.

10. Where authorization happens

This needs to be frozen architecturally:

              API
               │
        Authentication
               │
        Authorization
               │
          Admission
               │
        State transaction
               │
             Store

Authorization cannot be something controllers optionally remember to call.

The authoritative mutation boundary must enforce it.

What I would not stabilize yet

Don't spend time yet on:

OIDC implementation
LDAP implementation
policy DSL syntax
authorization caching
distributed policy replication
UI for permissions
sophisticated ABAC expression language
external authorization webhooks

Those are implementation details.

First stabilize:

Principal
   +
Permission
   +
Scope
   +
Resource identity
   +
Ownership
   +
Decision semantics
   +
Mutation boundary

If those seven are correct, the implementation can evolve.

My priority ordering

I'd put it this way:

                 MUST STABILIZE
                       │
              ┌────────┴────────┐
              │                 │
          Principal          Scope
              │                 │
              └────────┬────────┘
                       │
                  Permission
                       │
                Resource identity
                       │
                 Ownership
                       │
              Decision semantics
                       │
             State mutation boundary
                       │
                       ▼
                 THEN IMPLEMENT
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
       RBAC-like      ABAC        Auth providers
       groups       conditions     OIDC/LDAP/etc.

The biggest thing I'd stabilize first is actually the relationship between scope + resource identity + permission. That becomes the foundation for namespaces, secrets, network isolation, controller permissions, node agents, and the API simultaneously.

I would resolve those semantics before touching the authorization implementation code.