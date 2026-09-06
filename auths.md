Exactly. Once you move away from Kubernetes' object model, security becomes one of the most important parts of the architecture, because we still need to answer:

Who are you?
How did you prove it?
What are you allowed to read?
What are you allowed to change?
Which component is allowed to make changes?
How do nodes authenticate to the control plane?
How do we prevent a compromised node from impersonating another node?
How do we audit all of this?

I would design security as a separate identity + authorization layer, rather than tying it to objects.

1. First principle: identity is not authorization

I'd keep these concepts completely separate:

                 ┌──────────────┐
                 │   Identity   │
                 │  "Who are?"  │
                 └──────┬───────┘
                        │
                        ▼
                 ┌──────────────┐
                 │ Authorization│
                 │ "Can they?"  │
                 └──────┬───────┘
                        │
                        ▼
                    operation

For example:

alice

is an identity.

Then:

alice → can modify services in team=payments

is authorization.

Don't mix the two.

2. I'd use cryptographic identities everywhere

The control plane and nodes should mutually authenticate using mTLS.

Something like:

                  CONTROL PLANE
                       │
                    TLS cert
                       │
                       ▼
                 ┌───────────┐
                 │ Node Agent│
                 └───────────┘
                       │
                    TLS cert
                       │
                       ▼
                  CONTROL PLANE

But I would not make manually generated certificates the primary identity-management experience.

Kubernetes' certificate model is powerful but operationally cumbersome.

I'd build a small internal identity authority.

3. Internal Certificate Authority

The cluster has a root of trust.

For example:

                     Cluster Root CA
                           │
                ┌──────────┴──────────┐
                │                     │
          Control-plane CA        Node CA
                │                     │
          ┌─────┴─────┐         ┌─────┴─────┐
          ▼           ▼         ▼           ▼
       API-1        API-2     node-1      node-2

The root CA should ideally not be online.

The operational CAs can issue short-lived certificates.

4. Node enrollment

This is where I'd depart significantly from Kubernetes.

When a machine joins the cluster, it needs an initial bootstrap identity.

I'd have:

orbit join <cluster> <bootstrap-token>

The bootstrap token is:

short-lived
single-use
scoped to node enrollment
not a permanent credential

The node generates its own private key:

node:

private key ────── stays on node
     │
     ▼
certificate signing request
     │
     ▼
control plane

The control plane signs the public key.

The node receives:

node certificate

From then on, it uses mTLS.

The bootstrap credential disappears.

5. Better yet: hardware identity

For serious deployments I'd support stronger enrollment mechanisms:

TPM
Secure Boot
cloud instance identity
machine identity service

For example, on a cloud provider:

VM
 │
 ├── cloud identity
 │
 ▼
Orbit enrollment
 │
 ▼
certificate

That avoids putting a long-lived cluster secret into machine provisioning scripts.

6. Human authentication

For humans, I wouldn't use client certificates as the primary mechanism.

I'd support standard identity providers:

OIDC
OAuth 2.0
LDAP
SAML

Typically:

Developer
    │
    ▼
Identity Provider
    │
    ▼
OIDC token
    │
    ▼
Orbit API

The token might establish:

subject = alice
groups = [
    developers,
    payments
]

Then authorization evaluates those attributes.

7. RBAC

I'd support RBAC because it's simple and predictable.

But I'd make permissions actions over facts, rather than permissions over Kubernetes-style objects.

For example:

role developer {

    allow read service/*
    allow modify service/*
    allow read instance/*
}

And:

role operator {

    allow read *
    allow modify service/*
    allow modify node/*
}

Then:

user alice
    roles developer

So:

alice → developer
developer → permissions
permissions → operations
8. But RBAC alone isn't enough

Suppose Alice is allowed:

modify service/*

Should she be able to modify:

service payments-db

if she belongs to the frontend team?

Probably not.

This is where ABAC becomes valuable.

I'd use a hybrid:

RBAC establishes broad authority; ABAC establishes context.

9. ABAC

An authorization policy might look like:

allow modify service
when
    subject.team == service.team

Or:

allow read service
when
    subject.team == service.team
    OR subject.role == "platform-admin"

Or:

allow deploy
when
    subject.environment != "production"
    OR subject.role == "production-deployer"

This is much more expressive than pure RBAC.

10. The authorization request

Every API operation becomes something conceptually like:

authorize(
    principal,
    action,
    resource,
    context
)

For example:

principal:
    alice

action:
    service.update

resource:
    service/web

context:
    team = payments
    environment = production

Authorization returns:

ALLOW

or:

DENY

The important thing is that the authorization engine doesn't care whether the underlying implementation calls something a "Deployment" or a "Pod."

It evaluates domain operations.

11. Security of controllers

This is actually more important than human RBAC.

Imagine a vulnerability in the autoscaler.

If all controllers share the same credential:

autoscaler → god mode

that's terrible.

Instead, every controller gets its own identity:

controller.scheduler
controller.network
controller.storage
controller.autoscaler
controller.health

And each receives minimum permissions.

For example:

scheduler:

READ:
    nodes
    instances
    requirements

WRITE:
    placements

But:

scheduler CANNOT:
    modify user configuration
    modify node identity
    modify firewall policy

That's least privilege.

12. This fits our fact model beautifully

Remember our separation:

desired/user
desired/scheduler
desired/network
observed/node
observed/health

We can make these security boundaries.

For example:

node-agent:

READ:
    desired/node-assignments

WRITE:
    observed/node-1/*

It cannot write:

desired/services/*

So even if somebody compromises node-1, they can't simply send:

desired_instances(database) = 0

because the authorization layer rejects it.

13. Node identity

Every node gets a cryptographic identity:

spiffe://cluster/node/node-1

Conceptually, I'd use SPIFFE-style workload identities internally.

Then we can distinguish:

spiffe://cluster/node/node-1
spiffe://cluster/controller/scheduler
spiffe://cluster/controller/network
spiffe://cluster/controller/storage

This becomes much cleaner than passing around usernames and passwords.

14. Controller-to-controller security

Controllers don't need to directly call one another.

They interact through the state store:

scheduler
   │
   │ identity = controller.scheduler
   ▼
state store
   ▲
   │ identity = controller.network
   │
network controller

The state store enforces:

scheduler:
    can write placement/*
    
network:
    can write routing/*

Even if the network controller is compromised, it can't modify scheduling decisions.

15. mTLS everywhere

I'd have a rule:

No authenticated cluster communication without cryptographic identity.

So:

CLI ─────────── mTLS/OIDC ──────────► API
API ─────────── mTLS ───────────────► Store
Controller ──── mTLS ───────────────► Store
Node ────────── mTLS ───────────────► Store/API

And preferably:

controller → controller

isn't even necessary.

16. Certificate rotation

Certificates should be short-lived.

For example:

certificate lifetime = 1 hour

Node has:

certificate expires in 15 min

It automatically requests a replacement:

node
 │
 ├── existing valid identity
 │
 ▼
CSR
 │
 ▼
CA
 │
 ▼
new certificate

No administrator intervention.

If a node disappears, its certificate naturally expires.

For higher security, we can also revoke identities, but short-lived credentials reduce the importance of revocation.

17. Root CA protection

I'd make the trust hierarchy:

                 offline root
                     CA
                     │
            ┌────────┴────────┐
            ▼                 ▼
       control-plane      workload
           CA                CA
            │                 │
       API/controller       services

The root private key should rarely, ideally never, be present on a running control-plane machine.

18. Secrets

This deserves its own subsystem.

I would not put secrets directly into ordinary configuration:

service database {
    password "super-secret"
}

Instead:

secret database.password

and:

service database {
    secret database.password
}

The actual secret should live in an encrypted secret store.

At minimum:

encrypted at rest

and preferably envelope encryption:

             KMS
              │
              ▼
        data encryption key
              │
              ▼
          secret store

Examples of external KMS integration:

AWS KMS
GCP KMS
Azure Key Vault
HashiCorp Vault
HSM
19. Secrets shouldn't flow everywhere

Suppose the database password belongs to:

service database

The scheduler doesn't need it.

The network controller doesn't need it.

The node agent might need it only if the workload does.

So permissions become:

scheduler       → NO secret access
network         → NO secret access
storage         → NO secret access
database agent  → database.password

This is another advantage of separating facts by semantic domain.

20. Audit logging

Every security-sensitive operation should generate an immutable audit record.

For example:

2026-09-06 18:51

principal:
    alice

authentication:
    OIDC

action:
    service.update

target:
    service/web

change:
    image nginx:1.27 → nginx:1.28

decision:
    ALLOW

policy:
    production-deployer

request_id:
    8f31...

And controller operations too:

principal:
    controller.scheduler

action:
    placement.create

target:
    instance/web-91af

decision:
    ALLOW

This becomes extremely useful when someone asks:

"Who moved my production workload?"

21. Security architecture

Putting everything together:

                         IDENTITY
                            │
             ┌──────────────┼───────────────┐
             ▼              ▼               ▼
           Human          Node          Controller
             │              │               │
           OIDC            mTLS            mTLS
             │              │               │
             └──────────────┼───────────────┘
                            ▼
                     ┌─────────────┐
                     │ Authorization│
                     │ RBAC + ABAC │
                     └──────┬──────┘
                            │
                            ▼
                      STATE STORE
                            │
                    ┌───────┴───────┐
                    ▼               ▼
                 desired         observed
                    │               │
                    └───────┬───────┘
                            ▼
                       controllers
                            │
                            ▼
                       node agents
22. And I'd make authorization declarative too

This is where I think the design gets particularly nice.

The cluster configuration could contain:

role developer {
    allow service.read
    allow service.update
}

role production-deployer {
    allow service.deploy
}

grant developer to group developers

grant production-deployer to group release-engineering

And ABAC:

policy team-isolation {
    allow service.update
    when subject.team == resource.team
}

So security policies themselves become declarative state.

But there must be one important exception:

A principal must never be able to grant itself additional privileges.

The authorization subsystem needs a protected administrative boundary.

23. The bootstrap problem

Every secure distributed system eventually encounters:

Who authorizes the first administrator?

I'd solve that explicitly.

Cluster creation generates:

cluster root identity
cluster admin identity

The initial admin credential could be:

one-time bootstrap token

or preferably a user-selected identity authenticated through an external IdP.

After bootstrap:

bootstrap credential
       ↓
create admin identity
       ↓
bootstrap credential destroyed

There should be no permanent "magic password."

24. What about workload-to-workload security?

That's a separate layer.

For example:

frontend → API
API → database

I'd allow policies such as:

allow frontend → api:443
allow api → database:5432
deny frontend → database:5432

This is effectively network authorization.

The identity can be:

service:web
service:api
service:database

rather than merely IP addresses.

Then the network layer translates identity policy into actual firewall/routing rules.

So:

policy
  ↓
identity
  ↓
network controller
  ↓
iptables / nftables / eBPF / CNI
25. Zero-trust becomes the default

The resulting security philosophy is:

                     DON'T TRUST
                         │
             ┌───────────┼───────────┐
             ▼           ▼           ▼
           user         node      controller
             │           │           │
          authenticate authenticate authenticate
             │           │           │
             └───────────┼───────────┘
                         ▼
                     authorize
                         │
                         ▼
                       action

Being "inside the cluster" grants nothing by itself.

A node doesn't automatically get administrative privileges.

A controller doesn't automatically get access to everything.

A user doesn't get access because they know the API endpoint.

26. One architectural change I'd make because of security

Earlier we said:

controllers → state store

I'd refine that to:

controllers
     │
     ▼
┌──────────────────┐
│ Authorized Store │
│                  │
│ identity         │
│ authentication   │
│ authorization    │
│ transactions     │
│ audit            │
└────────┬─────────┘
         │
         ▼
      state

The state store becomes a security boundary.

Every write is authenticated and authorized.

That's incredibly important.

27. The final security model

I'd summarize the whole thing as five layers:

1. IDENTITY
   "Who are you?"

        ↓

2. AUTHENTICATION
   "Prove it."
   mTLS / OIDC

        ↓

3. AUTHORIZATION
   "Are you allowed?"
   RBAC + ABAC

        ↓

4. ISOLATION
   "What can you affect?"
   namespaces/teams/network/secrets

        ↓

5. AUDIT
   "What happened?"
   immutable event trail

And the cryptographic trust model:

                 Root CA
                    │
        ┌───────────┼───────────┐
        ▼           ▼           ▼
      Users       Nodes     Controllers
       OIDC         mTLS        mTLS
        │           │           │
        └───────────┼───────────┘
                    ▼
             Authorization
              RBAC + ABAC
                    │
                    ▼
              State changes
                    │
                    ▼
                  Audit

So we don't actually lose anything by abandoning Kubernetes objects. In fact, I think security becomes cleaner because permissions are expressed in terms of capabilities and facts rather than permissions over an enormous hierarchy of API resource types.

The next hard piece I'd tackle is multi-tenancy: namespaces/teams, resource quotas, network isolation, secret isolation, and preventing one tenant's controllers or workloads from affecting another. That's where the RBAC/ABAC model really gets tested.