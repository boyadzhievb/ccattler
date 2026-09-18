# Multi-Tenancy

CCattler has no namespaces. Instead, tenancy is built from three separate concerns: **ownership**, **identity**, and **policy**.

## Create a tenant

```hcl
tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
        volumes 50
        storage 10Ti
    }
}

tenant frontend {
    quota {
        cpu 50
        memory 128Gi
        instances 200
    }
}
```

Creating a tenant automatically provisions: identity scope, quota, network boundary, secret space, and audit stream.

## Hierarchical naming

Services live under their tenant's path:

```
/frontend/web
/payments/checkout
/payments/database
/platform/dns
```

Each tenant owns its subtree — `/payments/*` is naturally isolated.

## Resource quotas

Quotas are stored as facts and enforced at admission time:

```
quota_cpu(payments, 100)
usage_cpu(payments, 74)
```

Admission is arithmetic: `74 + 30 > 100 → DENY`. No hidden capping — the denial is visible and explains why.

## Fair scheduling

Beyond quotas, weighted fairness prevents starvation:

| Tenant | Weight | Guaranteed CPU |
|---|---|---|
| platform | 5 | 50 |
| payments | 3 | 30 |
| frontend | 2 | 20 |

Unused guarantees become borrowable — if `frontend` only uses 10 CPU, the remaining 10 is available to `payments` or `platform`.

## Identity-based network isolation

No label selectors. Policies reference service identities:

```hcl
network {
    allow frontend/web -> payments/checkout port 443
    allow payments/checkout -> payments/database port 5432
    deny frontend/web -> payments/database
}
```

Every instance gets a SPIFFE identity (`spiffe://ccattler/payments/database`). mTLS between services is automatic.

## Secret isolation

Secrets belong to tenants. Only explicitly granted services receive them:

```hcl
secret payments/database.password

service payments/checkout {
    secret payments/database.password {
        mount "/run/secrets/db-password"
    }
}
```

The scheduler and network controller never see plaintext secrets.

## Shared services

Cross-tenant infrastructure via exports:

```hcl
export platform/dns {
    allow frontend
    allow payments
}
```

Consumers declare `uses platform/dns`. The export is explicit — no service is accidentally exposed.

## Tenant lifecycle

- **Create** — automatically provisions identity scope, quota, network boundary, secret space, audit stream
- **Delete** — triggers ownership-driven garbage collection of all resources under that tenant

## Per-tenant visibility

| Component | Tenant sees | Platform sees |
|---|---|---|
| Services | Own | All |
| Secrets | Own | Metadata only |
| Network policies | Own | All |
| Audit logs | Own | All |
| Node health | Aggregated | Full |

## Policy gates

Every change passes through a pipeline:

```
APPLY → syntax validation → schema validation → RBAC/ABAC
      → quota check → security policy → mutation → commit
```

Each gate is a separate fact-based check. Failures explain which gate rejected the change and why.
