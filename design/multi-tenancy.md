# Multi-Tenancy

No namespaces. Tenancy is built from **ownership, identity, and policy** as separate concerns.

## Tenants & Ownership

```
tenant payments
tenant frontend
tenant platform
```

Every resource carries an owner: `owner(service:checkout, payments)`. Ownership drives authorization, quotas, and cleanup.

## Hierarchical Naming

Filesystem-style paths instead of namespace prefixes:

```
/frontend/web
/payments/checkout
/payments/database
/platform/dns
```

Tenant owns its subtree — `/payments/*` is naturally isolated.

## Resource Quotas

Attached to tenants, not namespaces:

```
tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
        volumes 50
        storage 10Ti
    }
}
```

Stored as facts (`quota_cpu(payments, 100)`), usage is observed (`usage_cpu(payments, 74)`). Admission is arithmetic: `74 + 30 > 100 → DENY`.

## Fair Scheduling

Beyond quotas — weighted fairness prevents starvation:

| Tenant | Weight | Guaranteed CPU |
|--------|--------|----------------|
| platform | 5 | 50 |
| payments | 3 | 30 |
| frontend | 2 | 20 |

Unused guarantees become borrowable.

## Network Isolation (Identity-Based)

No label selectors. Policies reference service identities:

```
network {
    allow frontend/web -> payments/checkout port 443
    allow payments/checkout -> payments/database port 5432
    deny frontend/web -> payments/database
}
```

When instances move, policies don't change — only derived firewall rules change.

## Service Identity

Every instance gets a SPIFFE identity: `spiffe://ccattler/payments/database`. mTLS between services is automatic — no shared secrets.

## Secret Isolation

Secrets belong to tenants. Only explicitly granted services receive them. Scheduler and network controller never see plaintext. Envelope encryption with master key rotation.

## Shared Services

Cross-tenant infrastructure via exports:

```
export platform/dns {
    allow frontend
    allow payments
}
```

Consumers declare `uses platform/dns`.

## Tenant Lifecycle

Creating a tenant automatically creates: identity scope, quota, network boundary, secret space, audit stream. Deleting a tenant triggers ownership-driven garbage collection of all resources.

## Policy Gates (Admission)

Every change passes through a pipeline (borrowed from K8s admission controllers):

```
APPLY → syntax validation → schema validation → RBAC/ABAC → quota check → security policy → mutation → commit
```

## Multi-Tenant Visibility

| Component | Tenant sees | Platform sees |
|-----------|-------------|---------------|
| Services | Own | All |
| Secrets | Own | Metadata only |
| Volumes | Own | All |
| Network policies | Own | All |
| Audit logs | Own | All |
| Node health | Aggregated | Full |
