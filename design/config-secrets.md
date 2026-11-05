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
| (not documented) agent secret API | `SecretAPIServer` (agent, `:9772`) serves `GET /v1/secrets/{name}` to containers by decrypting locally with the master key; the per-instance bearer token is passed to the container as the `CCA_SECRET_TOKEN` environment variable. |
| (not documented) cloud credentials | The `node-agent` store role reads `credentials/` directly. |

Connection URLs are already delivered as files (`/run/connections/{name}`, Flow 9), and `env` values are literal: no code path places a secret value in a container environment variable. The only credential in the environment is `CCA_SECRET_TOKEN`.

## M174 design: secrets architecture

**Status:** direction approved 2026-11-05 (decisions below). Tenant-scoped names, the cross-component flow ([Flow 27](runtime-invariant-flows.md#flow-27-secret-delivery-m174)) and agent restart/node reboot are written; this section and Flow 27 are reviewed together before implementation starts.

### Review decisions (2026-11-05)

| # | Decision | Outcome |
|---|---|---|
| 1 | Root key → KEK → DEK hierarchy | Approved. Root key separate from the CA key. |
| 2 | Envelope v2 bound to its store key (AAD) | Approved. Store keys are the tenant-scoped canonical names. |
| 3 | Rotation as a fact, done by a leader-elected controller | Approved, with create-only KEK writes, revision guards, bounded batches, idempotent resume, grace-period retirement. |
| 4 | Control plane decrypts, delivers plaintext to agents over mTLS | Approved. Failure behavior must be explicit (see "Availability"). Per-node envelope encryption is not part of M174. |
| 5 | Passwords in environment variables | Changed: files are the only delivery mechanism. Environment delivery is not added; if ever added, only as an explicit opt-in with a documented warning. |
| 6 | `tls { key secret "name" }` | Approved. Inline PEM stays for compatibility and is sealed on apply; Git-managed configs use secret references. |
| 7 | Control plane → agents → rotation → remove worker keys | Approved, with a strict cutover gate before rotation (step 3 is the rollback boundary). |

### Goals

1. Master key isolation: node agents never hold a key that can decrypt secrets.
2. Scoped delivery: an agent receives a secret only for an instance placed on its node whose service holds the grant.
3. Durable keyring: key versions survive restarts and are the same on every API replica.
4. Crash-safe rotation: an interrupted rotation leaves every secret readable and resumes.
5. Gate TLS private keys are encrypted at rest and never plain facts.
6. Enrollment no longer distributes key material.
7. Secrets belong to a tenant; another tenant's workloads cannot use them without an explicit share.

Non-goals: protecting secrets from a compromised control plane (it holds the keys by design); store access isolation for agents (M176); per-node envelope encryption (a possible later availability improvement). M174 makes secrets confidential even while agents can still read etcd, because ciphertext without a key is useless.

**Security boundary.** A compromised node can obtain the plaintext currently authorized for instances placed on it (and the gate keys it serves, see below). It cannot decrypt other secrets in etcd, and its node certificate does not give it another tenant's secrets.

### Key hierarchy

```
root key (control-plane nodes only: /etc/ccattler/secret-root.key, or a KMS key)
  └── wraps KEK versions, stored in etcd: keyring/kek/{version}
        └── wraps a per-secret DEK, stored in the envelope
              └── encrypts the secret value (AES-256-GCM)
```

- **Root key:** a 32-byte key on control-plane nodes only, cryptographically separate from the CA key, file mode `0400` owned by root. Ansible's control-plane role provisions it through the same trusted path as the CA material (HA replicas get the same file). It is never sent to agents. With `--kms-provider`, the root key is a KMS key and never touches disk. The existing `secret-master.key` becomes the root key during migration.
- **Startup checks:** a missing, unreadable, wrongly sized or group/world-readable root key file is a startup error (no silent generation). Each replica opens `keyring/root_check` (a fixed value sealed with the root key, written when the keyring is created); failing to open it is a startup error, so a replica with a different root key never serves.
- **Keyring in etcd:** `keyring/kek/{version}` = KEK wrapped by the root key, `keyring/active` = active version, `keyring/kek/{version}/state` = `active | retiring | retired`. Every API replica reads and watches it, so all replicas use the same versions without copying files.
- **Envelope v2:** `magic | format=2 | kek_version | wrapped_dek | nonce | ciphertext`. The AES-GCM additional data is the canonical store key (e.g. `secrets/tenant/payments/database.password`), so an envelope only opens under its own name. Moving or renaming a secret is a re-encryption with a new DEK, not a re-wrap. Envelopes without the magic are v1 (DEK wrapped directly by the root key) and stay readable until migrated.

### Tenant-scoped secret names

Canonical store keys:

```
secrets/tenant/{tenant}/{name}     tenant secret, e.g. secrets/tenant/payments/database.password
secrets/cluster/{name}             cluster secret, e.g. secrets/cluster/registry-credentials
secrets/route/{gate}/tls/key       sealed gate TLS key (written by the system, not by users)
```

Rules:

- **Ownership:** a secret belongs to the tenant of the configuration that declares it (the same owner resolution as services: `owner`, else the hierarchical name). Writing a tenant secret needs `secret.write` at `team/{tenant}`; a cluster secret needs it at `cluster`.
- **Resolution:** in a service, an unqualified reference (`secret database.password`) resolves to the service's own tenant: `secrets/tenant/{service tenant}/database.password`. Qualified references are explicit: `secret cluster/registry-credentials`, `secret tenant/platform/web-gate-key`.
- **No implicit visibility:** a reference to another tenant's secret is valid only if the owner declared a share (`secret database.password { share tenant frontend }` → `desired/secret_share/payments/database.password/frontend`). Applying a share needs `secret.write` in the owner's scope; applying the consuming reference needs `secret.use` in the consumer's scope. Cluster secrets need a grant at `cluster` scope for the applying user.
- **Checks in two places:** the compiler/apply path rejects unresolvable or unshared references with a clear error; the delivery endpoint re-checks the grant and share on every request, so a removed share stops delivery on the next start.
- **Services without an owner** belong to the built-in tenant `default` (open question A below).
- **Grant facts** carry the canonical name: `desired/service/{svc}/secret/{canonical name}`; connection auto-grants (Flow 9) do the same.

**Migrating unqualified names.** Existing `secrets/{name}` keys are not reinterpreted as tenant or cluster secrets. `cca secret migrate-names --plan` lists every legacy secret with the services and tenants that use it and writes a mapping file; the operator edits it and runs `cca secret migrate-names --apply mapping.ccv`. Every legacy secret must have an explicit target (`tenant/{t}/{name}` or `cluster/{name}`); apply refuses while any is unmapped. Apply re-encrypts each value under its new key (AAD changes) and rewrites grants and connection references in one transaction per secret. Until mapped, a legacy secret keeps today's behavior (only services with an existing grant can use it) and no new grant may reference it.

### Rotation (crash-safe)

Rotation is a fact, not a command: `POST /api/secret/rotate` writes `intent/keyring/rotation = {target_version}` and returns. A leader-elected `keyring` controller does the work:

1. Create the new KEK, wrap it with the root key, write `keyring/kek/{new}` with a create-only transaction. A crash here leaves an unused version.
2. Set `keyring/active = {new}`. New secrets use it from now on; every replica sees the change through its watch.
3. Re-wrap envelopes whose `kek_version` is older, in bounded batches (`keyringRewrapBatchSize`, e.g. 100 per transaction round): unwrap the DEK with the old KEK, wrap with the new one, write with a revision guard (CAS). A guard conflict re-reads that envelope in the next round; concurrent user writes are never overwritten. Only the wrapped DEK changes.
4. When no envelope references a version, set it `retiring`; delete it after `--keyring-retire-grace` (default 7 days). A version that is still referenced is never deleted.

Every envelope names its KEK version, so after a crash at any step every secret is readable, and the controller resumes by scanning for older versions (idempotent: re-running a finished step changes nothing). Root key rotation (re-wrap only `keyring/kek/*` and `keyring/root_check`) is a separate operator command and never runs automatically.

### Scoped delivery to agents

- **Endpoint:** `POST /api/node/secrets` with body `{instance_id}` on the API server, mTLS only, `node:` principals only (users and service tokens are refused). It returns every secret the instance needs: mounted secrets, connection passwords and the image pull secret.
- **Authorization per request:** the instance is placed on the calling node (`placement/instance/{id}` = caller's node ID); the node is enrolled and not disabled; for each secret the instance's service holds the grant, and for another tenant's secret the share exists. Partial results are not returned: any missing grant fails the request with the names that failed (values never).
- **Audit:** each delivery and each refusal is recorded (node, instance, service, secret names, outcome; never values).
- **Narrow store access:** delivery runs in the API server process but through its own store principal `service:secret-delivery`, not the generic `api-server` role (which keeps having no rule for `secrets/`, `credentials/` or `keyring/`). The `secret-delivery` role reads `secrets/`, `credentials/`, `keyring/`, `placement/instance/`, `desired/service/`, `desired/secret_share/`, `desired/connection/` and node state, and writes only its audit stream. The `keyring` controller gets its own role (`keyring/`, `secrets/` re-wrap, `intent/keyring/`).
- **Gate keys:** `POST /api/node/gate-keys` returns the sealed keys of gates the calling node serves. Today every agent's gate proxy serves every gate, so every agent node receives every gate key; restricting gate serving to selected nodes would narrow this later (documented limitation).
- **Cloud credentials** (`credentials/`) go through the same endpoint and checks; the `node-agent` store role loses its `credentials/` read.
- **The agent never reads `secrets/`, `credentials/` or `keyring/`**, holds no key and does not decrypt. Connection URLs are still assembled on the agent from the delivered password and the (non-secret) connection definition.

### Materialization on the node

- All delivered material is written to `/run/ccattler/secrets/{instance}/` (tmpfs; directory `0700`, files `0400`, owned by the container's user where the runtime supports it, else root) and bind-mounted read-only at the declared paths (`/run/secrets/...`, `/run/connections/...`). Nothing secret is written under `/var/lib/ccattler/logs/` or any other disk path.
- Connection URLs with embedded passwords are files only (decision 5). Secret values never appear in logs, `cca` status/describe output, events or traces.
- The agent secret API (`:9772`) serves from the instance's tmpfs directory instead of decrypting; its bearer token is delivered as a file in the same directory (`/run/ccattler/token`) instead of `CCA_SECRET_TOKEN` (the environment variable is kept for one release as a deprecated compatibility path).
- Cleanup: the instance's directory is removed after its container has stopped and will not be restarted on this node (stopped, removed or re-placed elsewhere).

### Availability and failure behavior

The control plane must be reachable to deliver a secret. Required behavior:

| Situation | Behavior |
|---|---|
| Container running, API becomes unreachable | Keep running; nothing is removed or restarted. |
| New instance needs secrets | State `waiting-for-secrets` until delivery succeeds; never start without them. |
| Container restarts (crash or liveness) and needs secrets | Material still in tmpfs from this boot → reuse it, no fetch needed. Missing → fetch first. |
| Agent process restarts, same boot | Reconcile (below); do not delete valid material, do not stop healthy containers. |
| Node reboots | tmpfs is empty; every instance fetches again before starting. |
| API stays unreachable | Retry with backoff (`secretDeliveryRetryInitial` 2s, capped at `secretDeliveryRetryMax` 30s); stay `waiting-for-secrets`. |
| Delivery refused (no grant, no share, not placed here) | `waiting-for-secrets` with reason `not_authorized` and the secret names; retried when the relevant desired/placement facts change, not on a timer. |

`waiting-for-secrets` is a new typed instance state (`types.InstanceWaitingForSecrets`), published with `observed/instance/{id}/reason` (`api_unreachable | not_authorized | secret_missing`) and `observed/instance/{id}/next_retry`. It is not a failure: it does not count toward crash-loop backoff, the instance is not replaced or rescheduled because of it, and the node-failure controller ignores it. `cca get instances` shows it with the reason.

**Agent restart on the same boot:** the agent lists `/run/ccattler/secrets/*` and its running containers. A directory whose instance is still running and still placed on this node is kept as is. A directory with no running instance and no placement on this node is removed. Running containers are not touched; their material is refreshed only when they next start.

**Re-placement:** the new node fetches its own copy before starting. The old node removes its directory after the old instance has stopped.

**Limitation:** a node that is disconnected keeps the plaintext already delivered to it until it can clean up. Revocation cannot erase data on an unreachable machine; removing a grant takes effect on the next delivery.

### Gate TLS private keys

- `RouteTLSController` seals generated keys at `secrets/route/{gate}/tls/key`; the certificate and expiry stay plain facts.
- Manual keys reference a secret:
  ```
  gate web-public {
      host "app.example.com"
      service web port 8080
      tls {
          cert "/etc/ccattler/certs/web.pem"
          key secret "platform/web-gate-key"
      }
  }
  ```
  An inline PEM key is still accepted and is sealed by the apply path before anything is written, so no `desired/route/*/tls/key` fact exists. Encrypting on apply does not make a key safe to commit: configurations kept in Git (M202) must use `key secret`, and `cca apply` warns when a file with an inline key comes from a Git source.
- Agents fetch gate keys through `POST /api/node/gate-keys` and keep them in memory only (the gate proxy needs them, containers do not). Existing plaintext key facts are migrated: sealed, then deleted.

### Enrollment and Ansible

- `handleEnroll` drops `SecretMasterKey`; `cca join` no longer writes a key file; agents ignore `/etc/ccattler/secret-master.key` and log once that the file is unused. Removing existing copies from workers is an explicit migration step (Ansible task), not done silently by the agent.
- The worker role's join task uses `creates: /etc/ccattler/pki/node.pem` instead of `secret-master.key`.
- The control-plane role provisions `secret-root.key` (`0400`, root) on every control-plane node.

### Migration (upgrade order) and cutover gate

1. Upgrade the control plane: keyring created from the existing master key (v1 envelopes readable), `root_check` written, delivery endpoints available, keyring controller running.
2. Upgrade agents: they switch to delivery; each reports `observed/node/{id}/secret_delivery = v2`. Plaintext gate key facts are sealed and deleted.
3. Cutover gate, then one rotation: all envelopes move to v2 under a fresh KEK. **This is the rollback boundary.**
4. Verify reads and delivery, then remove the key file from workers (Ansible). From here a leaked worker key file is useless.

`cca secret rotate` refuses the first v2 rotation (`409`, listing what is missing) unless all of these hold:

- every API replica reports `observed/keyring/replica/{id}` with the active version and a passed `root_check`;
- every enrolled, non-disabled node reports `secret_delivery = v2`, and no agent of an older version is registered;
- no component reads v1 envelopes (all readers are v2-capable binaries);
- the operator passes `--confirm-backup`, stating that a verified etcd snapshot exists and the restore procedure has been tested (runbook in `design/TESTING.md`).

After step 3 there is no binary rollback: recovery is a forward fix, or a coordinated restore of the pre-rotation etcd snapshot together with the previous binaries.

### Open questions

- **A. Services without an owner** (all current e2e workloads): built-in tenant `default` as proposed, or require an owner for any service that uses secrets?
- **B. Gate key exposure:** accept "every agent node receives every gate key" for M174, or restrict gate keys to nodes that host an instance of the gate's service?
- **C. Secret API token:** keep `CCA_SECRET_TOKEN` for one release (proposed) or remove it in M174?

### Tests

- Envelope v2: AAD binding (an envelope moved to another key fails to open), v1 still readable, version recorded; rename re-encrypts.
- Root key: missing, wrong size, wrong mode, wrong key (`root_check`) → startup error.
- Rotation: kill at each step and restart; every secret readable; resume completes; batches bounded; CAS conflict retried; referenced versions never deleted; replicas read the same versions.
- Tenants: unqualified name resolves to own tenant; another tenant's secret refused without share, delivered with share, refused again after the share is removed; cluster secret needs cluster grant; `migrate-names --apply` refuses unmapped secrets.
- Delivery: wrong node, unplaced instance, missing grant, disabled node, user principal: refused and audited; correct case returns only granted secrets; `secret-delivery` role cannot write outside its audit stream; `api-server` role still cannot read `secrets/`.
- Availability: API down with running container → keeps running; new instance → `waiting-for-secrets`, no replacement, starts when API returns; agent restart keeps material of running instances and removes orphans; node reboot re-fetches before start.
- Materialization: tmpfs, `0700`/`0400`, removed after stop; nothing under the logs directory; no secret value in logs, events or `cca` output.
- Enrollment response has no key; a joined worker has no key file.
- Gate keys: no plaintext key fact after reconcile and after apply of an inline PEM; gate TLS e2e (T1–T15) unchanged.
- Cutover gate: rotation refused while one agent reports v1 or `--confirm-backup` is missing.
- Deploy cluster: secrets and connection strings still reach Airflow; rotation during traffic.
