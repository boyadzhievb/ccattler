# Config & Secrets

Config and secrets are two separate mechanisms. Controllers reason about references and desired state. The node agent is the only component that materializes config and secrets into workloads.

## Config

Config is declarative desired state, not imperative "set this environment variable":

```
config api {
    env "LOG_LEVEL" = "info"
    env "PORT" = "8080"

    file "/etc/api/config.yaml" = "..."
}

service api {
    image "my-api:v3"
    config api
}
```

The fact store holds config facts (`desired/service/api/config/...`), not rendered environment variables. The scheduler and controllers don't care how config becomes runtime input — that's the node agent's job.

Config supports two delivery modes:
- **Environment variables** — `env "KEY" = "value"`
- **Config files** — `file "/path" = "content"`

## Secrets

Secrets are references, never values. The fact store holds grants, not plaintext:

```
secret database.password

service api {
    secret database.password {
        mount "/run/secrets/database-password"
    }
}
```

Stored as:
```
secret(database.password)              — secret exists
secret_grant(api, database.password)   — api may access it
```

Never:
```
secret(database.password, "super-secret-password")   — WRONG
```

Actual secret values live in the encrypted secret subsystem (envelope encryption with KMS). The scheduler and network controller never see plaintext secrets.

**File-mounted secrets are preferred over environment variables:**
- Avoids exposing secrets through environment inspection
- Avoids accidental logging of environment variables
- Makes rotation easier (overwrite file, signal process)
- Works with applications that already consume secret files
- Gives the agent control over permissions (`0400`, memory-backed, ephemeral)

## Materialization Boundary

```
             CONTROL PLANE
                   │
      ┌────────────┴────────────┐
      │                         │
 desired config           secret reference
      │                         │
      └────────────┬────────────┘
                   │
             Fact Store
                   │
                   ▼
              Node Agent
                   │
         ┌─────────┴─────────┐
         │                   │
    config resolver     secret resolver
         │                   │
         └─────────┬─────────┘
                   ▼
              Container
```

Don't put rendered config into the fact store. Store `desired(instance, running)` + `config(api, ...)` + `secret_grant(api, database.password)`. The agent resolves effective configuration at reconciliation time.

## Secret Lifecycle

Secret access is tied to container lifecycle:

```
instance api-7 assigned to node-3
  → node-3 obtains authorized secret copy
  → secret materialized locally
  → container starts
  → container stops
  → secret material removed
```

If the container moves from node-3 to node-5, node-5 gets a fresh authorized copy. The old node removes its copy. This fits the zero-trust + short-lived credentials model.

## Connection URL Assembly

See [runtime-invariant-flows.md](runtime-invariant-flows.md) section 9 for the detailed compiler → agent → container flow.

---

## Implementation status vs this document (audit 2026-11-03)

The sections above describe the intended model. The code does not enforce most of it. M174 closes these gaps; until then, treat the "Actual" column as the security model.

| Documented | Actual (code as of `0dea8fa`) |
|---|---|
| Master key never leaves the control plane; "the scheduler and network controller never see plaintext" | Every node receives the cluster master key in the enrollment response (`api/server.go` `handleEnroll` → `SecretMasterKey`), `cca join` writes it to `/etc/ccattler/secret-master.key`, and the agent decrypts locally (`agent.NewLocalSecretProvider`). With raw etcd access (threat model T5), any node can decrypt every secret of every tenant, and every cloud credential under `credentials/`. |
| A node obtains only secrets for instances assigned to it | The agent checks only the service grant (`desired/service/{svc}/secret/{name}`), not that an instance of that service is placed on this node. It reads ciphertext straight from etcd. |
| Master key rotation without data loss | `POST /api/secret/rotate` re-wraps DEKs under a new key held only in memory: the new key is not written to disk, so a restart loads the old key and every re-wrapped secret becomes unreadable. Other API replicas and all agents keep the old key. The field is updated without a lock. |
| Envelope encryption | Envelope = wrapped DEK + AES-GCM ciphertext, with no key version and no binding to the secret name: after an interrupted rotation a reader cannot tell which key wrapped a secret, and an etcd writer can move one secret's envelope under another secret's name. |
| Secret files are `0400`, memory-backed, ephemeral, removed when the container stops | Decrypted secrets and connection passwords are written as config files under `/var/lib/ccattler/logs/{instance}/config/` with mode `0755` on disk (`runtime/containerd.go`), removed only with the log directory. |
| (not documented) gate TLS keys | `RouteTLSController` writes generated private keys as plain facts at `derived/route/{name}/tls/key`; manual keys from DSL are plain facts at `desired/route/{name}/tls/key`. M173 hides them from the state/watch API, but they are plaintext in etcd. |
| (not documented) agent without a key file | `loadOrGenerateSecretMasterKey` silently creates a new random key, so decryption fails later with no clear cause. |

## M174 design: secrets architecture

### Goals

1. Master key isolation: node agents never hold a key that can decrypt secrets.
2. Scoped delivery: an agent receives a secret only for an instance placed on its node whose service holds the grant.
3. Durable keyring: key versions survive restarts and are the same on every API replica.
4. Crash-safe rotation: an interrupted rotation leaves every secret readable and resumes.
5. Gate TLS private keys are encrypted at rest and never plain facts.
6. Enrollment no longer distributes key material.

Non-goals: protecting secrets from a compromised control plane (it holds the keys by design); store access isolation for agents (M176). M174 makes secrets confidential even while agents can still read etcd, because ciphertext without a key is useless.

### Key hierarchy

```
root key (control-plane nodes only: /etc/ccattler/secret-root.key, or a KMS key)
  └── wraps KEK versions, stored in etcd: keyring/kek/{version}
        └── wraps a per-secret DEK, stored in the envelope
              └── encrypts the secret value (AES-256-GCM)
```

- **Root key:** the existing `secret-master.key` on control-plane nodes becomes the root key. It is never sent to workers. HA control-plane nodes get it like the CA key today (copied by the `ha-controlplane` role). With `--kms-provider`, the root key is a KMS key and never touches disk.
- **Keyring in etcd** (`keyring/kek/{version}` = KEK wrapped by the root key, `keyring/active` = active version). Every API replica reads and watches it, so all replicas use the same versions (goal 3, replica consistency) without copying files on rotation.
- **Envelope v2:** `magic | format=2 | kek_version | wrapped_dek | nonce | ciphertext`, with the store key (e.g. `secrets/db-password`) as AES-GCM additional data, so an envelope only opens under its own name. Envelopes without the magic are v1 (DEK wrapped directly by the root key) and stay readable until rotated.

### Rotation (crash-safe)

Rotation is a fact, not a command: `POST /api/secret/rotate` writes `intent/keyring/rotation = {target_version}` and returns. A leader-elected `keyring` controller does the work:

1. Create the new KEK, wrap it with the root key, write `keyring/kek/{new}` (create-only transaction). Nothing else changes yet, so a crash here leaves an unused version.
2. Set `keyring/active = {new}`. New secrets use it from now on; every replica sees the change through its watch.
3. Re-wrap each envelope whose `kek_version` is older: unwrap the DEK with the old KEK, wrap it with the new one, write with a revision guard (CAS), so concurrent writes are not lost. Only the wrapped DEK changes; the ciphertext stays.
4. When no envelope references an old version, mark it retired (kept for a grace period, then deleted).

Every envelope names its KEK version, so after a crash at any step all secrets are readable, and the controller resumes by scanning for old versions (goal 4). Root key rotation (re-wrapping only the keyring entries) is a separate, rare, operator-run command.

### Scoped delivery to agents

- New internal endpoint `GET /api/node/secrets?instance={id}` on the API server, mTLS only, for `node:` principals.
- The server authorizes per request: the instance is placed on the calling node (`placement/instance/{id}` = caller's node ID), the instance's service holds a grant for each requested secret, and the node is enrolled and not disabled. Each delivery is audited (node, instance, secret names; never values).
- The server decrypts with the keyring and returns the plaintext over TLS 1.3. Cloud credentials (`credentials/`) use the same endpoint and checks.
- The agent keeps delivered material in memory and in the instance's tmpfs only. It does not read `secrets/` or `credentials/` from etcd any more.
- Re-placement: the new node fetches on start; the old node deletes the instance's material on stop (see materialization).

### Materialization on the node

- Secret files go to `/run/ccattler/secrets/{instance}/` (tmpfs, directory `0700`, files `0400`), bind-mounted read-only, removed when the instance stops. Nothing secret is written under `/var/lib/ccattler/logs/`.
- Connection strings that embed a password stay environment variables for compatibility (documented residual risk); a file-based form is preferred and documented.

### Gate TLS private keys

- `RouteTLSController` seals generated keys with the secret store at `secrets/route/{name}/tls/key`; the certificate and expiry stay plain facts.
- Manual TLS keys: DSL references a secret (`tls { cert "…" key secret "gate-web-key" }`). An inline PEM in DSL is sealed by the apply path before it is written, so no `desired/route/*/tls/key` fact exists.
- Agents fetch gate keys through the node delivery endpoint (a node serving gate HTTPS gets the keys of the gates it serves). Existing plaintext facts are migrated: sealed, then deleted.

### Enrollment and Ansible

- `handleEnroll` drops `SecretMasterKey`; `cca join` no longer writes a key file; agents ignore `/etc/ccattler/secret-master.key` and log once that the file is unused. Removing existing copies from workers is an explicit migration step (Ansible task), not done silently by the agent.
- The worker role's join task uses `creates: /etc/ccattler/pki/node.pem` instead of `secret-master.key`.
- A missing root key on a control-plane node is a startup error, not a silently generated new key.

### Migration (upgrade order)

1. Upgrade control plane: keyring created from the existing master key (v1 envelopes readable), node delivery endpoint available, rotation controller running.
2. Upgrade agents: they switch to node delivery; plaintext gate key facts are sealed and deleted.
3. Run one rotation: all envelopes move to v2 under a fresh KEK; the old master key now only wraps keyring entries.
4. Remove the key file from workers (Ansible). From this point a leaked worker key file is useless.

### Tests

- Envelope v2: AAD binding (an envelope moved to another key fails to open), v1 still readable, version recorded.
- Rotation: kill at each step and restart; every secret readable; resume completes; replicas read the same versions.
- Delivery: wrong node, unplaced instance, missing grant, disabled node, user principal: all refused and audited; correct case returns only granted secrets.
- Enrollment response has no key; a joined worker has no key file; agent fails clearly when delivery is unavailable.
- Materialization: files on tmpfs with `0400`, removed on stop; nothing under the logs directory.
- Gate keys: no plaintext key fact after reconcile and after apply of an inline PEM; gate TLS e2e (T1–T15) unchanged.
- Deploy cluster: secrets and connection strings still reach Airflow; rotation during traffic.
