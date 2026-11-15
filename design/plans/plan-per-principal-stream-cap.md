# Plan — Per-Principal Stream Cap (M225)

Status: decided 2026-11-15 (D1 32 per principal; D2 admin reserve dropped). Found by the M216 stream review
(finding S-11); design reviewed by an independent agent the same day (findings R-1 … R-8 below).

## Problem

`/api/watch`, `/api/events/stream` and `/api/audit?follow=true` share one cap of 1000 open streams per API
server (`maxConcurrentWatchConnections`, `reserveWatchConnection` in `api/server.go`). Nothing limits one
principal. A user with only `workload.read` can open 1000 idle streams (a loop of `cca watch desired &`)
and every other user gets `503 too many concurrent watch connections` on every stream command.

The admin is not locked out of acting: plain `cca audit` (no `--follow`) is not a stream, and revoking the
grant or deleting the user ends the offender's streams at the next re-check (M216, ≤ 36 s). What the admin
lacks is a way to **see who holds the slots**: stream opens are not audited and the stream gauge has no
principal.

What does not help today:
- the rate limiter is per client IP and counts requests, not open streams; it also trusts any client's
  `X-Forwarded-For` (bug, M226), so its 50 requests/s is not even a real bound;
- the M216 re-check and 1 h lifetime end streams, but the client reconnects at once;
- per IP is the wrong key here: CI runners, NAT and a bastion host share one IP for many users.

Only the CLI opens streams (`cca watch`, `cca events --follow`, `cca audit --follow`); agents, controllers,
the website and the load tests do not. Each stream is one live `cca` process (none of them reconnects).

## How others do it

| System | Limit on long-lived streams | Key |
|---|---|---|
| Kubernetes | Watches are exempt from `--max-requests-inflight`; API Priority and Fairness counts a watch only while it initialises (shuffle-sharded queues per FlowSchema: user, group, service account). No cap on how many watches a user keeps open; memory is bounded by the watch cache. | user/group (at admission only) |
| Consul | `limits.http_max_conns_per_client` (default 200) on all HTTP connections, blocking queries included | client IP |
| Nomad | `limits.http_max_conns_per_client` (default 100), event stream included | client IP |
| Vault | Rate-limit and lease-count quotas per path / namespace; no open-stream cap | path, namespace |
| AWS API Gateway (WebSocket) | Account quota on new connections per second, 2 h maximum connection duration, 10 min idle timeout | account |

None of them caps open streams per authenticated principal. Kubernetes gives fairness at admission but lets
a user hold many watches. Consul and Nomad cap per IP, which CCattler should not copy (shared IPs above,
spoofable `X-Forwarded-For`). CCattler authenticates and re-checks every stream (M216), so the principal is
known and stable for the stream's life: it is the natural key.

## Design

1. **Per-principal limit** `maxStreamsPerPrincipal = 32` open streams per principal per API server, keyed by
   the full `kind:name` (`security.PrincipalStructFromContext(ctx).String()`, so `user:x` and `node:x` never
   share). Over the limit: `429`, `"too many open streams for user:alice (limit 32): close some cca watch /
   events --follow / audit --follow"`. No `Retry-After` (a slot frees when the user closes a stream, not
   after a time). No exemption for admins.
2. **Shared identities share one allowance** (documented, tested): every local-token caller on the control
   node is `user:local-admin` and demo mode is `user:local-demo`, so all operators, scripts and CI jobs on
   that token share 32. Guidance: one identity per CI pipeline (`cca user create` + direct capability
   grants), not a higher limit.
3. **No admin reserve** (D2 dropped). It does not stop the attack it was meant for: with or without it,
   about 30 colluding principals lock out every other non-admin, and the admin's remedies do not need a
   stream slot.
4. **Who holds the slots** (replaces the reserve):
   - each refusal is logged with the principal and its count (at most once per principal per minute, not
     one line per 429);
   - `GET /api/streams` (requires `cluster.admin`) returns the open-stream count per principal and the
     total; `cca streams` prints it. Read from the same map, no store access.
5. **One reservation helper** replaces `reserveWatchConnection`: `reserveStreamSlot(responseWriter, request)
   (releaseStreamSlot func(), reserved bool)`. One mutex guards the global count and a `map[string]int`;
   check and increment happen under it. The release is idempotent (`sync.Once`), decrements both and
   deletes the map entry at zero. It replaces the seven hand-written `activeWatchConnectionCount.Add(-1)`
   calls: the watch path's five early returns and its release goroutine on `request.Context().Done()`,
   and the `defer`s in the events and audit-follow handlers.
6. **Missing principal → 401** (fail closed). Behind the M216 wrapper it cannot happen. A server without an
   authenticator chain (never in production, M216 S-17) has no principal; tests that open streams on such
   a server get a chain.
7. **Constants, not flags,** like the global cap. **Per API server:** the cap protects each process's
   goroutines and memory, so per process is the right scope (three replicas: up to 96 per principal, each
   replica still bounded). A store-backed counter would cost a write per open and leave stale counts after
   a crash.
8. **CLI:** `cca events --follow` and `cca audit --follow` print the server's error message and exit 1 on a
   non-stream answer, like `cca watch` does (today events prints `unexpected response: {raw JSON}`).

Documented limits (M225 security review):
- *Ceiling:* 1000 / 32 means about 32 distinct principals at their limit fill a server; later principals
  (nodes and admins included) get 503 until slots free. Accepted with D2: the remedies (revoke, delete,
  `cca streams`) are not streams. A reserve or per-class sub-cap can follow if this is seen in practice.
- *Webhook principals must be stable per identity:* a webhook that puts a session or request id in
  `principal` gives every token its own allowance. The webhook is trusted server configuration.

Failure analysis:
- *Map leak:* every reserve returns a release that runs exactly once (`sync.Once`); the entry is deleted at
  zero. Tests assert both counts are 0 after every refusal path and after streams close.
- *Race:* check and increment under one mutex; a concurrent test proves 64 parallel opens admit exactly 32.
- *Lock contention:* one short critical section per stream open/close; opens are rare next to stream life.
- *Principal re-created:* M216 closes the old streams within one re-check; the count follows the releases.

## Tests (failing first)

| Persona | Scenario | Expected |
|---|---|---|
| Rough user | opens 33 watches with one token | 33rd gets 429 with the limit in the text; another user still opens a stream |
| Rough user | 64 parallel opens by one principal (`-race`) | exactly 32 admitted |
| Careless operator | 32 forgotten background watches on the local token, then `cca audit --follow` | 429 (shared `user:local-admin` allowance); `cca streams` shows `user:local-admin 32` |
| Admin | a user at the limit is revoked | the user's slots return to 0 within one re-check interval; another user is admitted |
| Any | stream refused (prefix missing, prefix not readable, access denied, cap) | global and per-principal counts back to 0, map empty |
| Any | `GET /api/streams` without `cluster.admin` | 403 |
| CLI | `cca events --follow` refused with 429 | prints the server's message, exit 1 |

Replay (`test/e2e/adversarial/cases.yml`): `adv-reader` opens `maxStreamsPerPrincipal + 1` background
watches; the last fails with "too many open streams"; `cca streams` as admin lists `user:adv-reader 32`.

## Design review (2026-11-15)

| # | Finding | Resolution |
|---|---|---|
| R-1 | The reserve does not show the admin who holds the slots | **accepted**: reserve dropped; refusal log + `GET /api/streams` / `cca streams` (Design 3, 4) |
| R-2 | Every local-token caller is `user:local-admin` and shares one bucket | **accepted**: documented, tested, CI guidance (Design 2); no admin exemption |
| R-3 | "Missing principal → 401" vs chain-less servers; accessor unnamed | **accepted**: accessor named, chain-less behaviour stated (Design 1, 6) |
| R-4 | Problem overstated the admin lockout; no revoke-frees-slots test | **accepted**: Problem reworded; test added |
| R-5 | `cca status --watch` does not exist; 7 release sites, not 9; stale line reference | **accepted**: corrected |
| R-6 | Rate limiter trusts `X-Forwarded-For` from any client | **accepted as a separate bug**: M226 |
| R-7 | 429 shared with the rate limiter; `cca events --follow` prints raw JSON | **accepted**: distinct text, CLI fix (Design 8) |
| R-8 | Release must run exactly once on every path | **accepted**: `sync.Once`, refusal-path and concurrency tests |
