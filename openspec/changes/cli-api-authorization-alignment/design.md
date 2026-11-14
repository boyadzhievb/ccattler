# Design

## Context

For the motivation, see proposal.md (Why). The authorization chain itself is [Flow 25](../../../design/runtime-invariant-flows.md#flow-25-api-authentication-and-authorization-m173): authenticator chain → `requireCapability` / `requireWorkloadVisibility` → `api-server` store principal → audit. This change adds Flow 25's missing first step, "the CLI reaches state only through the API", and closes the paths that skip the chain.

Current state, verified 2026-11-14:

- **Server mux** (`cmd/cca/command_server.go`, around line 1236). It wraps `apiServer.Handler()` for `/api/`, `/healthz`, `/metrics`, `/.well-known/` and `/oidc/`. It also serves its own `/status`, which authenticates but authorizes nothing, built by `buildClusterStatusJSON` and `buildStatusTextOutput`. Those two functions duplicate `api/status.go` with an unfiltered store.
- **`/status` callers:**
  - `cca status` (text).
  - `test/e2e/vm-ha/ha_test.sh` and `baremetal_ha_test.sh` (JSON `services`, `instances`, `nodes`; same field names as `/api/status`).
  - `scripts/caddy-sync.sh` fetches it but never reads the result.
- **`cca apply` and `cca diff`.** `resolveStoreDefaults` switches to etcd when the endpoints file exists, which is true on every control-plane host. They then write facts directly. Elsewhere they run a local simulation that looks like success (M218 finding A1). `/api/apply` exists, takes `{config}` (1 MiB limit), and refuses `tls cert/key` files and `config … from` files (`ValidateNoLocalFileReferences`), because the server must not read client paths (M176, security finding 3). Inline gate keys already go through `PUT /api/gate-key`.
- **`cca token create/list/revoke`** opens etcd and calls `EnrollmentService` directly. The Ansible worker role runs `cca token create --node-id … --ttl 15m` on the first control-plane host.
- **The M216 sweep** (`api/TestReadOnlyPrincipalCannotWriteThroughAnyRoute`) only sends POST, PUT and DELETE as an `api-reader`, and only to routes it lists by hand.

## Goals / Non-Goals

**Goals:**
- Only one HTTP surface: every route is an `api.Server` route, and every route is covered by one sweep test.
- Every route is visible to the sweep and the route-table cross-check: all registrations live in `api/server.go`, enforced by a test.

**Non-Goals:**
- Changing what any existing `/api/` route authorizes. Only coverage is added; a route that turns out wrong is fixed in its own task, with a failing test first.
- Changing authentication (the chain order stays as in Flow 25).

## Decisions

1. **Remove `/status` rather than gate it.**
   - Gating it would keep a second status builder that has to repeat the team visibility rules and the M175 snapshot loader.
   - Removing the route leaves one status source over HTTP, so the CLI can't show more than the API allows.
   - `cca status` renders the same text from `/api/status?all=true` JSON on the client (`renderStatusText`), as `kubectl get` does.
   - The legacy builders (`buildClusterStatusJSON`, `buildStatusTextOutput`) stay for the in-process modes only (`--simulate`, `cca run`, demos), which have no authorization to bypass.
   - Every script and playbook that called `/status` (e2e, HA, Ansible, Lima, `caddy-sync.sh`, docs) switches to `/api/status`; they authenticate with the control-plane node's certificate, which holds the server's `cluster.admin` grant.
   - Alternative rejected: wrapping the legacy handler in `requireWorkloadVisibility`. It would still serve an unfiltered snapshot to a team-scoped reader.
2. **The server mux delegates everything to `api.Server`.**
   - `cmd/cca` keeps only the TLS listener and enrollment middleware.
   - A test in `cmd/cca` checks that `/status` answers 404 and that every forwarded prefix reaches the API handler.
3. **No route registry; parse `server.go`, and guard against other files.**
   - The sweep and the route-table cross-check read the route list with the existing `collectRegisteredRoutes` (parses `server.go`, resolves constants through `routeConstants`, and fails when the number of matches differs from the number of `mux.HandleFunc(` calls).
   - `TestRoutesAreRegisteredOnlyInServerGo` fails when any other non-test file in `api/` registers a route, so a route added elsewhere cannot escape either check.
   - Alternative rejected: a `registerRoute` helper that records patterns. Only tests would read the list, which CLAUDE.md's no-dead-code rule rules out.
4. **Sweep test.**
   - The principal is an authenticated user with no grants and no groups.
   - Each route is probed at its registered path (prefix routes get a real endpoint suffix), with the query parameters handlers read, so the authorization check is reached before input validation.
   - All four methods are sent.
   - It asserts 401/403/405, except for the allowlist, and an unchanged store revision.
   - A route whose handler validates input before authorizing answers 400/404 and fails the test.
5. **Apply and diff through the API, with files sent by the CLI.**
   - The request grows an optional `files` map: client path → contents, only for `tls cert` and `config … from`.
   - The server compiles with a resolver that reads only from that map, and answers 400 for a path that isn't in it. So the server still never opens a client path.
   - Inline TLS keys keep using `PUT /api/gate-key` (sealed, `secret.write`). They are never put in the apply body. The server accepts a gate that names a key file without reading it; key contents in `files` are refused. Order unchanged: gate facts first, then the key.
   - All apply paths write through one function, `lang.WriteCompiledFacts`, which also deletes a declared gate's settings the configuration no longer sets (added 2026-11-14: the server paths did not prune, so moving `cca apply` to the API would have left HTTPS on after `tls` was removed, e2e T14).
   - Alternative rejected: new DSL syntax for inline certificates. It changes configurations users already have.
   - On a control-plane host the CLI authenticates with the local admin token, as other commands do. The resolution order is env token → context → local token.
6. **Simulation only with `--simulate`.**
   - Without it, an unreachable API is an error that names the address.
   - The old silent fallback is removed, because it printed a fake cluster status (M218 A1).
7. **Join token routes.**
   - `POST /api/node/join-token` takes `{node_id, ttl}` and returns the token once.
   - `GET /api/node/join-token` lists tokens by id and expiry, never their values.
   - `DELETE /api/node/join-token?id=…` revokes one.
   - All three require `cluster.admin` and are audited with the token id.
   - They use the server's existing `EnrollmentService` (`SetEnrollmentService`).
   - Ansible is unchanged: it still runs `cca token create` on the control plane, which now calls the API with the local admin token.
8. **Root key commands stay local.**
   - Flow 30 requires them while every API replica is stopped.
   - Before connecting, they check for the endpoints file and the root key path. If either is missing, they print "run on a control-plane node".
9. **Graphs.**
   - `AUTH_GRAPH.md` is split into `AUTHN_GRAPH.md`, `AUTHZ_GRAPH.md` and `SECRETS_GRAPH.md`.
   - Entries that name removed functions are dropped: `RewrapEnvelope` and `SecretStore.RotateKeyProvider` no longer exist.
   - `AUTHZ_GRAPH.md` gets the route table.
   - `TestDocCrossCheckAuthzRouteTable` parses the table's first column and compares it with the routes `collectRegisteredRoutes` returns.

## Failure cases

| Case | Behaviour |
|---|---|
| API down during `cca apply` on a control-plane host | Exits non-zero naming the address. It no longer writes etcd behind the API's back. Ansible and `cluster_test.sh` already wait for `/healthz` before applying; this is checked in the deploy run. |
| New CLI, old server (no `files` field) | The old server ignores `files` and refuses the file reference with its existing message. Upgrade the server first (same binary on control-plane hosts). |
| Configuration plus files over 1 MiB | 413 from the server. The limit becomes a named constant sized for certificate bundles. |
| A route that does its validation before authorization | The sweep gets 400 instead of 403 and fails. Fix: authorize first (the same rule M216 applied to `/api/gc`). |
| Local token missing on a control-plane host | `cca apply` answers 401 as a normal client would. Ansible's server role writes the token, as today. |
| Team-scoped reader and the status cache | `/api/status` caches only all-teams results (unchanged), so a team-scoped `cca status` always builds a filtered status. |
| HA script reads a field `/api/status` lacks | Found by the HA run, a task before release. The field names were compared: `services`, `instances` and `nodes` match. |
| Audit volume grows (applies are now audited) | Expected. Audit write cost is M181's concern. |

## Risks / Trade-offs

- [Applies need a healthy API, where etcd alone used to be enough] → Operators lose a backdoor that bypassed the policy gate. That is the intended outcome. The break-glass offline path is the root key commands only.
- [Removing `/status` breaks external scripts that call it] → It is beta. The release notes name the replacement (`/api/status`, same field names).
- [The sweep's probe parameters can rot when handlers change what they read] → A handler that validates before it authorizes answers 400 and fails the test, which is the behaviour we want.

## Migration Plan

1. Server side first: sweep, join-token routes, the `files` field and the `/status` removal, all in one release.
2. The CLI changes ship in the same binary. On control-plane hosts server and CLI are upgraded together, and a workstation CLI upgraded before the server gets a clear refusal.
3. Rollback: the previous binary restores the old paths. No store keys change, so nothing needs migrating back.
