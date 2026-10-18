# Security Model

Five layers: Identity → Authentication → Authorization → Isolation → Audit.

**Zero-trust by default** — being inside the cluster grants nothing. Every component authenticates and is authorized independently.

## Transport: mTLS Everywhere

```
CLI ──────── mTLS/OIDC ──────► API
API ──────── mTLS ────────────► Store
Controller ── mTLS ────────────► Store
Node ──────── mTLS ────────────► Store/API
```

No unauthenticated cluster communication.

## Certificate Authority Hierarchy

```
              Offline Root CA
                    │
        ┌───────────┼───────────┐
        ▼           ▼           ▼
  Control-plane   Node CA    Workload CA
      CA            │           │
      │        ┌────┴────┐    services
  ┌───┴───┐   node-1  node-2
API-1  API-2
```

- Root CA is offline (never on a running control-plane machine)
- Short-lived certificates (1hr) with automatic rotation
- SPIFFE-style workload identities: `spiffe://cluster/node/node-1`, `spiffe://cluster/controller/scheduler`

## Node Enrollment

```
cca join <cluster> <bootstrap-token>
```

Bootstrap token is short-lived, single-use, scoped to enrollment only. Node generates its own private key, sends CSR, receives signed certificate, bootstrap token is destroyed. Supports hardware identity (TPM, cloud instance identity) for stronger enrollment.

## Human Authentication

Standard identity providers (OIDC, OAuth 2.0, LDAP, SAML):

```
Developer → Identity Provider → OIDC token → CCattler API
```

Token establishes: `subject=alice, groups=[developers, payments]`

## Authorization: RBAC + ABAC

**RBAC** for broad authority — permissions over fact prefixes:

```
role developer {
    allow service.read
    allow service.update
}

role operator {
    allow read *
    allow modify service/*
    allow modify node/*
}

grant developer to group developers
```

**ABAC** for context — attribute-based conditions:

```
policy team-isolation {
    allow service.update
    when subject.team == resource.team
}

policy production-gate {
    allow service.deploy
    when subject.environment != "production"
    OR subject.role == "production-deployer"
}
```

Every API operation evaluates: `authorize(principal, action, resource, context) → ALLOW | DENY`

## Per-Controller Least Privilege

Every controller gets its own identity and minimum permissions:

```
scheduler:          READ nodes, instances, requirements    WRITE placements
network:            READ instances, endpoints              WRITE routing
autoscaler:         READ health, metrics                   WRITE intent/autoscaler
node-agent:         READ desired/node-assignments          WRITE observed/node-X/*
```

A compromised autoscaler cannot modify user configuration. A compromised node cannot set `desired_instances(database) = 0` — it can only write to `observed/`.

## Authorized Store

The fact store is the security boundary — every write is authenticated, authorized, and audited:

```
controllers → Authorized Store (identity + authN + authZ + txn + audit) → state
```

## Secrets

Secrets live in an encrypted store, never in plain DSL config:

```
secret database.password

service database {
    secret database.password
}
```

Envelope encryption with KMS integration (AWS KMS, GCP KMS, Vault, HSM). Secrets scoped by service — the scheduler and network controller never see them. The agent materializes secrets to the host filesystem and bind-mounts them read-only (`-v`) into containers at the declared path (e.g., `/run/secrets/db-password`).

## Workload-to-Workload Network Policy

Identity-based, not IP-based:

```
allow frontend → api:443
allow api → database:5432
deny frontend → database:5432
```

Network controller translates identity policies → iptables/nftables rules.

## Audit Logging

Every security-sensitive operation produces an immutable record:

```
principal:   alice
auth:        OIDC
action:      service.update
target:      service/web
change:      image nginx:1.27 → nginx:1.28
decision:    ALLOW
policy:      production-deployer
request_id:  8f31...
```

## Bootstrap Problem

Cluster creation generates a one-time bootstrap credential → creates admin identity → bootstrap credential destroyed. No permanent "magic password."

## Cryptographic Trust Summary

```
        Root CA
           │
  ┌────────┼────────┐
  ▼        ▼        ▼
Users    Nodes   Controllers
 OIDC     mTLS      mTLS
  │        │        │
  └────────┼────────┘
           ▼
     Authorization
      RBAC + ABAC
           │
           ▼
      State changes
           │
           ▼
        Audit
```
