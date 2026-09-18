# Security

CCattler is zero-trust by default — being inside the cluster grants nothing. Every component authenticates and is authorized independently.

## Five security layers

```
Identity → Authentication → Authorization → Isolation → Audit
```

## mTLS everywhere

All cluster communication uses mutual TLS:

```
CLI ─── mTLS/OIDC ───► API
API ─── mTLS ─────────► Store
Controller ── mTLS ────► Store
Node ── mTLS ─────────► Store/API
```

No unauthenticated cluster communication.

### Certificate Authority hierarchy

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
- Short-lived certificates (1 hour) with automatic rotation at 70% of TTL
- SPIFFE-style workload identities: `spiffe://cluster/node/node-1`

## Node enrollment

```bash
cca join <server> <bootstrap-token> --node-id worker-1
```

Bootstrap tokens are short-lived, single-use, and scoped to enrollment only. The node generates its own private key, sends a CSR, receives a signed certificate, and the bootstrap token is destroyed.

## Human authentication

Standard identity providers (OIDC, OAuth 2.0):

```
Developer → Identity Provider → OIDC token → CCattler API
```

## Authorization: RBAC + ABAC

**RBAC** for broad authority — permissions over fact prefixes:

```hcl
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

```hcl
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

Every API operation evaluates: `authorize(principal, action, resource, context) → ALLOW | DENY`.

## Per-controller least privilege

Every controller gets its own identity and minimum permissions:

| Controller | Read | Write |
|---|---|---|
| Scheduler | nodes, instances, requirements | placements |
| Network | instances, endpoints | routing |
| Autoscaler | health, metrics | intent/autoscaler |
| Node agent | desired/node-assignments | observed/node-X/* |

A compromised autoscaler cannot modify user configuration. A compromised node cannot set `desired_instances(database) = 0`.

## Secrets

Secrets are references, never values. The fact store holds grants, not plaintext:

```hcl
secret database.password

service api {
    secret database.password {
        mount "/run/secrets/database-password"
    }
}
```

Stored as:
- `secret(database.password)` — secret exists
- `secret_grant(api, database.password)` — api may access it

Actual secret values live in an encrypted store (AES-256-GCM envelope encryption with KMS integration). File-mounted secrets are preferred over environment variables — avoids accidental logging and makes rotation easier.

### Secret lifecycle

Secret access is tied to container lifecycle:

1. Instance assigned to node → node obtains authorized secret copy
2. Secret materialized locally → container starts
3. Container stops → secret material removed
4. Instance moves to another node → new node gets fresh copy, old node removes its copy

## Audit logging

Every security-sensitive operation produces an immutable record:

```
principal:   alice
action:      service.update
target:      service/web
change:      image nginx:1.27 → nginx:1.28
decision:    ALLOW
policy:      production-deployer
```

## Workload-to-workload network policy

Identity-based, not IP-based:

```hcl
network {
    allow frontend -> api port 443
    allow api -> database port 5432
    deny frontend -> database
}
```

The network controller translates identity policies into iptables/nftables/eBPF rules. When instances move, policies don't change — only derived firewall rules change.
