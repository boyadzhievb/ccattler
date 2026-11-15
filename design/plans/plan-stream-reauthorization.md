# Stream re-authorization (M216 open item)

Status: steps 1–4 done 2026-11-15 (Go level; replay `stream` step and two rows, run on the deploy cluster: 23 passed, 0 failed, 2 skipped). Open: independent review (step 5). Reviewed by security-audit before implementation (verdict "sound with changes"); its findings F1–F10 are folded in below.

## Problem

`/api/watch`, `/api/events/stream` and `/api/audit?follow=true` authenticate and authorize once, when the stream opens (`authenticationMiddleware`, then the handler's capability or visibility check). A stream keeps sending data after its user is deleted, its grant is revoked or narrowed, or its token expires.

Verified in the code:
- the watch visibility (`api/server.go` `handleWatch` → `streamWatchEventsAsSSE`) and the audit tenant filter (`handleAuditFollow` → `resolveAuditTenantFilter`) are computed once at open, so a narrowed grant keeps the old, wider filter;
- `AuthenticationResult.ExpiresAt` is a real expiry only for JWT/OIDC (`exp`) and mTLS (`NotAfter`); the webhook sets now + cache TTL (2 min), the local token now + 24 h on every call;
- `LocalUserRecordAuthenticator` refuses on any store error;
- `handleAuditFollow` takes no slot of `maxConcurrentWatchConnections` (audit-follow streams are unlimited).

## How others do it

| System | Long-lived reads | Exposure after revocation |
|---|---|---|
| Kubernetes | watch authenticated once; random timeout 30–60 min (`--min-request-timeout`), client reconnects | up to ~1 h |
| Consul | blocking queries end after ≤ 10 min, re-checked on the next call | up to ~10 min |
| Vault | token TTL and leases; renewals re-check the token | up to the lease |

## Design

1. **One wrapper** (`withStreamReauthorization`) around the three stream routes (audit only with `follow=true`). No-op without an authenticator chain (demo mode).
2. **Re-check every `streamReauthorizationInterval` (30 s, ±20 % jitter):**
   - re-run the authenticator chain on the original request; the principal must be unchanged;
   - re-run the route's capability check with the re-authenticated principal;
   - re-resolve the watch visibility / audit tenant filter; any change closes the stream (the reconnect gets the new filter).
3. **Hard deadline only for real expiries**, captured at open: JWT/OIDC `exp`, mTLS `NotAfter`. Webhook and local-token streams: re-check plus `streamMaximumLifetime` (1 h) as a backstop. A zero expiry means no deadline.
4. **Transient failures do not cut streams:** store and webhook outages return `ErrAuthenticationUnavailable`; the stream closes after `streamReauthorizationMaxUnavailable` (3) consecutive ones. A definitive denial (user gone, revision mismatch, invalid token, capability or filter change) closes at once.
5. **The client is told:** the wrapper writes a final SSE frame `event: closed` with a generic reason (`credentials expired`, `access changed`); `cca watch`, `cca events --follow` and `cca audit --follow` print it and exit non-zero. The forced close is audited (`stream.close`, deny) and the real reason logged.
6. **Audit-follow takes a stream slot** (shared cap of 1000).
7. **Per-write deadline** (`http.ResponseController.SetWriteDeadline`) so a client that stops reading cannot hold a handler forever.

Documented limits (not fixed here): mTLS revocation is a TLS-layer concern (a revoked certificate is accepted until `NotAfter`); webhook revocation is seen up to cache TTL + 30 s late; a user re-created within the same second still accepts an old token (already recorded in M216).

Cost: only `LocalUserRecordAuthenticator` reads etcd (one `Get` per re-check); capability and visibility checks are in memory. Worst case 1000 streams / 30 s ≈ 33 single-key reads per second.

## Tests as real users

Each persona row is a test at one or both levels: **Go** (`api/` and `cmd/cca/`, every push) and **replay** (`test/e2e/adversarial/cases.yml` on the deploy cluster). Replay needs a new step type: `stream` starts a command in the background (`watch`, `events --follow`, `audit --follow`), the case does something, then asserts the stream ended within a time limit and what it printed.

### Careless developer
| Scenario | Expected | Level |
|---|---|---|
| Leaves `cca watch` open in a terminal for days with a user token; the token expires | stream ends at the token's `exp` with "credentials expired", exit non-zero | Go (short-lived JWT) |
| Script loops `while true; do cca audit --follow; done` after its user was deleted | every reconnect gets 401; no data after the deletion | replay |
| CI job uses an old token copied into a variable, after the user was re-created | 401 at open; an already-open stream ends at the next re-check | Go + replay |
| Opens `cca events --follow` with the node certificate on a control node (mTLS) | runs until the certificate's `NotAfter`, not longer | Go |

### Rough user
| Scenario | Expected | Level |
|---|---|---|
| Opens 1001 streams (any mix of watch, events, audit-follow) | the 1001st gets 503; audit-follow counts too | Go |
| Admin revokes their `workload.read` while they stream `cca watch` | stream ends within one interval with "access changed"; reconnect 403 | Go + replay |
| Cluster-wide reader narrowed to `team/a` while streaming | stream ends; the reconnect only shows team `a` | Go + replay |
| Stops reading (slow client) to pin a server handler | write deadline ends the handler | Go |
| Reconnects in a tight loop after being cut | each reconnect authenticated and refused; audit shows the refusals | replay |

### Careless user
| Scenario | Expected | Level |
|---|---|---|
| Account deleted by an admin while `cca audit --follow` runs in a forgotten window | stream ends within one interval, audit entry `stream.close` deny | Go + replay |
| Demoted from cluster admin to a team reader during a follow | stream ends (tenant filter changed); reconnect shows only own team | Go |
| etcd leader election during a long watch (not their fault) | stream survives (fewer than 3 unavailable checks in a row) | Go |
| Webhook IdP briefly down | stream survives a short outage; closes after 3 failed checks | Go |

Positive controls (every level): an unchanged user streams across several intervals with no interruption and no extra close frame.

## Order of work

1. Flow 25 invariants (`design/runtime-invariant-flows.md`): a stream ends within one interval of deletion, revocation or narrowing, and at a real token expiry; the end is signalled; filters are re-resolved.
2. Failing tests first: the Go rows above (deleted user, revoked grant, narrowed grant, expired JWT, audit-follow cap, transient outage survives).
3. Implementation (items 1–7), `AUTHN_GRAPH.md` / `AUTHZ_GRAPH.md` updates.
4. Replay `stream` step type and the replay rows; run on the deploy cluster before committing them.
5. Independent review (code-reviewer + security-audit), Deploy Testbed.
