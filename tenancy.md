think multi-tenancy is where this architecture either proves itself or fails. Kubernetes uses namespaces as a catch-all boundary for naming, RBAC, quotas, and some policies. I'd split those concerns apart instead of making one concept carry everything.

The core principle would be:

A tenant is an identity and ownership boundary. Everything else—names, quotas, networks, secrets, and policies—is attached to that boundary.

Instead of "everything lives in a namespace," we model explicit relationships.

Layer 1: The Tenant Model

A tenant represents an organization, team, or application owner.

tenant payments
tenant frontend
tenant platform

Internally these become facts:

tenant(payments)
tenant(frontend)
tenant(platform)

Every managed resource carries an owner.

owner(service:web, frontend)
owner(service:checkout, payments)
owner(secret:db-password, payments)
owner(volume:payments-db, payments)

Ownership is fundamental because it drives authorization, quotas, and cleanup.

Layer 2: Naming Without Namespaces

Instead of Kubernetes' payments/web, I'd use hierarchical names.

/frontend/web
/payments/checkout
/payments/database
/platform/dns

Think of it more like a filesystem.

/
├── frontend
│   └── web
├── payments
│   ├── checkout
│   ├── database
│   └── secrets
└── platform

The tenant owns its subtree.

That means:

/payments/*

is naturally isolated.

Layer 3: Authorization

Permissions become path-based plus ownership.

Example:

role developer {

    allow read /frontend/**
    allow modify /frontend/**
}

Platform admins:

role platform-admin {

    allow read /**
    allow modify /**
}

Then add ownership rules.

allow modify
when owner(resource) == subject.tenant

This gives us RBAC + ABAC together.

Layer 4: Resource Quotas

Instead of attaching quotas to namespaces, attach them directly to tenants.

Example:

tenant payments {

    quota {

        cpu 100
        memory 256Gi

        instances 500

        volumes 50
        storage 10Ti

        services 200
    }
}

Stored as facts:

quota_cpu(payments,100)
quota_memory(payments,256Gi)
quota_storage(payments,10Ti)

Current usage is observed.

usage_cpu(payments,74)
usage_memory(payments,180Gi)

Admission becomes simple.

requested_cpu=30

74+30=104

quota=100

DENY

No controller has to remember this manually.

Layer 5: Fair Scheduling

Quotas prevent abuse.

But scheduling should also prevent starvation.

Suppose:

payments
frontend
analytics

Analytics requests:

1000 instances

while frontend needs:

5 instances

I'd separate:

quota

priority

fairness

Scheduler inputs become:

capacity

tenant quotas

tenant priorities

current allocations

Then use weighted fairness.

Example:

Tenant

	

Weight

	

Guaranteed CPU




platform

	

5

	

50




payments

	

3

	

30




frontend

	

2

	

20

Unused guarantees become borrowable.

This behaves more like modern cluster schedulers than simple first-come-first-served.

Layer 6: Network Isolation

This is one area where I'd improve Kubernetes.

Instead of writing network policies against labels:

podSelector:

I'd write identity policies.

Example:

network {

    allow frontend/web
        -> payments/checkout
        port 443

    allow payments/checkout
        -> payments/database
        port 5432

    deny frontend/web
        -> payments/database
}

This is much easier to reason about.

Conceptually:

IDENTITY POLICY

frontend/web
      │
      ▼
payments/checkout

payments/database

No IP addresses appear in policy.

How the network controller works

Current state:

web-1
web-2
checkout-7
database-1

Controller derives:

iptables
nftables
or eBPF rules

Example:

ALLOW
frontend/web

↓

checkout endpoint

If checkout moves:

checkout-9

The policy does not change.

Only the derived firewall rules change.

That's a huge usability improvement.

Layer 7: Service Identity

Every running instance gets an identity.

Something like SPIFFE.

spiffe://orbit/frontend/web
spiffe://orbit/payments/database

Now mTLS becomes automatic.

web

certificate

identity

↓

checkout verifies:
"Are you frontend/web?"

No shared secrets between services.

Layer 8: Secret Isolation

Secrets should belong to tenants.

Example:

tenant payments {

    secret db-password

}

Then:

service database {

    uses secret db-password

}

Only services explicitly granted access receive the secret.

Controller derives:

secret

↓

encrypted delivery

↓

node

↓

service

The scheduler never sees plaintext.

The network controller never sees plaintext.

Other tenants never see metadata beyond what policy allows.

Secret encryption

I'd use envelope encryption.

Master Key
     │
     ▼
Data Encryption Key
     │
     ▼
Encrypted Secret

Rotating the master key does not require rewriting every secret.

Layer 9: Storage Isolation

Volumes belong to tenants.

volume payments/database

Facts:

owner(volume,payments)
attached(volume,node-4)
mounted(volume,database-1)

Rules:

volume
    cannot attach
    outside owner's authorization

Snapshots inherit ownership.

snapshot database-2026-09-06

owner = payments

Cross-tenant volume sharing requires explicit grants.

Layer 10: Shared Services

Sometimes everyone needs DNS.

Or logging.

Or monitoring.

I'd introduce exports.

Example:

export platform/dns

allow frontend
allow payments
allow analytics

Then consumers declare:

uses platform/dns

This avoids copying infrastructure into every tenant.

Think of it like importing a library.

Layer 11: Tenant Lifecycle

Creating a tenant:

tenant payments

automatically creates:

identity scope

quota

network boundary

secret space

audit stream

Deleting a tenant:

delete tenant payments

derives cleanup.

services

volumes

routes

certificates

secrets

Everything owned by the tenant is removed—or preserved according to retention policy.

Ownership makes cleanup deterministic.

Layer 12: Audit Per Tenant

Every tenant gets its own audit view.

Example:

payments

2026-09-06

alice

updated checkout

image

1.4

→

1.5

Platform admins still have global visibility.

Tenants only see their own history.

Layer 13: Multi-Tenant Security Matrix

Component

	

Tenant Sees

	

Platform Sees




Services

	

Own

	

All




Secrets

	

Own

	

Metadata only (or policy-defined)




Volumes

	

Own

	

All




Routes

	

Own

	

All




Network policies

	

Own

	

All




Audit logs

	

Own

	

All




Node health

	

Aggregated

	

Full

This minimizes accidental information leakage.

Layer 14: Putting Everything Together

The complete control plane now looks like this.

Every layer consumes and produces facts, not opaque objects.

The One Thing I'd Borrow from Kubernetes

Although we've avoided Kubernetes' object model, there's one design I'd absolutely borrow: admission controllers.

In Orbit, I'd rename them policy gates.

Every change passes through a pipeline:

APPLY config

↓

Syntax validation

↓

Schema validation

↓

RBAC/ABAC

↓

Quota check

↓

Security policy

↓

Mutation

↓

Commit

Example:

service checkout {
    image postgres:18
}

Policy gate rejects it:

Production database images
must come from
registry.company.com

This keeps policy enforcement centralized instead of scattering checks throughout controllers.

Why I think this is cleaner than Kubernetes Namespaces

Concern

	

Kubernetes

	

Orbit




Ownership

	

Namespace

	

Explicit owner relation




Naming

	

Namespace prefix

	

Hierarchical paths




Authorization

	

RBAC on resources

	

RBAC + ownership + ABAC




Quotas

	

ResourceQuota

	

Tenant quotas




Network

	

Label selectors

	

Identity-based policies




Secrets

	

Namespace-scoped

	

Tenant-owned with explicit grants




Shared services

	

Awkward

	

Export/import model




Cleanup

	

Namespace deletion

	

Ownership-driven garbage collection

The result is a system where multi-tenancy emerges from ownership, identity, and policy, rather than overloading a single concept like "namespace" to solve five different problems.