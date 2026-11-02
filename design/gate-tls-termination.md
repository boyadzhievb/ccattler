# Gate TLS Termination — M169 Design

## Problem

Gates forward traffic via L4 DNAT (nftables) or L7 cookie proxy (Go reverse proxy), but neither terminates TLS. Exposing HTTPS endpoints requires the backend application to handle its own certificates. This is awkward: every service needs cert management, and the cluster CA — which already issues node and mTLS certs — sits idle for workload-facing TLS.

## Existing Infrastructure

- **`gate` block** (M118): host/path matching, service targeting, sticky sessions
- **RouteController**: `desired/route/` → `derived/route/` backend resolution
- **Agent data plane**: nftables DNAT for non-sticky, embedded `httputil.ReverseProxy` for cookie-sticky
- **Cluster CA** (`security/ca.go`): `IssueCertificate(CommonName, DNSNames, IPAddresses, TTL)` → PEM cert+key
- **Enrollment service**: issues node certs via CSR flow

## DSL

### Auto mode — cluster CA issues cert for the gate's host

```
gate web-public {
    host "app.example.com"
    service frontend port 8080
    tls {
        mode auto
    }
}
```

### Manual mode — user-provided PEM files

```
gate internal-api {
    host "api.internal"
    service api port 3000
    tls {
        cert "/etc/cca/certs/api.pem"
        key  "/etc/cca/certs/api-key.pem"
    }
}
```

### Minimal form

```
gate web {
    host "web.example.com"
    service web port 80
    tls auto
}
```

`tls auto` is shorthand for `tls { mode auto }`.

### Constraints

- `tls` requires `host` — you need a hostname to issue a certificate for.
- `tls { cert ... key ... }` reads PEM files at `cca apply` time and stores content in facts (same pattern as `config` blocks with `from`).
- `tls auto` and `tls { cert ... }` are mutually exclusive.
- Removing `tls` from a gate and re-applying turns HTTPS off: `cca apply` replaces every declared gate's `desired/route/{name}/` keys, deleting ones the new config no longer produces.

## Fact Model

A `tls` block compiles to facts under `desired/route/{name}/tls/`:

```
# auto mode
desired/route/web-public/tls/mode     = "auto"

# manual mode
desired/route/internal-api/tls/mode   = "manual"
desired/route/internal-api/tls/cert   = "<PEM content>"
desired/route/internal-api/tls/key    = "<PEM content>"
```

Derived facts (written by TLS certificate controller for auto mode):

```
derived/route/web-public/tls/cert      = "<PEM content>"
derived/route/web-public/tls/key       = "<PEM content>"
derived/route/web-public/tls/not-after = "2027-01-15T12:00:00Z"
```

For manual mode the agent reads cert/key directly from `desired/route/{name}/tls/cert` and `desired/route/{name}/tls/key`.

## Architecture

### Components

1. **Parser** — `tls` sub-block inside `gate`, parsed by `parseRouteTLSField()`
2. **Compiler** — `tls` → `desired/route/{name}/tls/` facts; for manual mode, reads PEM files at compile time
3. **TLS Certificate Controller** (new) — watches `desired/route/` for `tls/mode=auto`, uses cluster CA to issue cert, writes to `derived/route/{name}/tls/`, auto-renews when approaching expiry
4. **Agent gate listener** (revised in M170) — the agent's node-level gate proxy gets an HTTPS listener (`:443`) that picks the certificate by SNI and routes by Host to the gate's service. Service VIPs are never touched by TLS.

### TLS Certificate Controller

New controller: `controllers/route_tls.go`

```
watch: desired/route/, derived/route/
write: derived/route/{name}/tls/cert, derived/route/{name}/tls/key, derived/route/{name}/tls/not-after
```

On reconcile:
1. Scan `desired/route/{name}/tls/mode` — collect routes with `auto`
2. For each auto route, check `derived/route/{name}/tls/not-after`
3. If missing or within renewal window (30 days before expiry), issue cert via `CertificateAuthority.IssueCertificate()` with `DNSNames: [host]`, `TTL: 90 days`
4. Write PEM cert+key to derived facts
5. Delete stale derived TLS facts for routes that no longer have `tls/mode=auto`

The controller needs access to the cluster CA. The server already loads it for enrollment — pass the same `*CertificateAuthority` to this controller.

### Node HTTPS Listener (M170)

> **Supersedes the M169 design**, where TLS was terminated by the per-route
> cookie proxy reached through a VIP:port dispatch override. That design made a
> TLS gate take over the whole service port: cluster-internal plain-HTTP callers
> of the service broke, two gates could not share a service port, TLS-only gates
> were never dispatched on nftables, and eBPF ignored TLS entirely.

The agent already runs a node-level gate proxy (`network/userspace_proxy.go`,
enabled with `--proxy`, listening on `--proxy-listen`). It maps gate hosts to
services from `desired/route/*/host` and forwards to healthy endpoints with
least-connections, circuit breaking, and warm-zero activation. M170 adds a
second, TLS listener to the same proxy:

```
client ──HTTPS──▶ node:443  (agent gate proxy, --proxy-tls-listen)
                    │ 1. TLS handshake: GetCertificate(SNI) → gate cert
                    │ 2. Host header → gate → service (same routeHostMap as :80)
                    │ 3. Host must belong to the SNI's gate, else 421
                    ▼
                  backend endpoint (plain HTTP, overlay IP)

client ──HTTP───▶ node:80/8080 (existing listener)
                    └─ host of a TLS gate → 308 redirect to https://
in-cluster ─HTTP─▶ service VIP:port → unchanged, never TLS
```

**Certificate selection.** The proxy keeps a `host → tls.Certificate` map,
refreshed on the same 5 s TTL as `routeHostMap`, built from
`desired/route/{name}/tls/{cert,key}` (manual) or
`derived/route/{name}/tls/{cert,key}` (auto); desired wins. `GetCertificate`
looks up `ClientHelloInfo.ServerName`. Unknown or missing SNI → handshake
fails (no default certificate, so the node never presents an unrelated cert).

**Cookie sticky over HTTPS.** Backend selection from the sticky cookie
(`selectBackendFromCookie`, `buildStickyCookie` in `network/cookie_proxy.go`)
is extracted into shared helpers; the gate proxy applies them when the matched
gate has `sticky cookie`. Plain-HTTP cookie-sticky gates keep using the
per-route cookie proxy for now.

**Removed.** The `hasTLS` branches in the nftables and iptables route chains,
TLS support in `CookieStickyProxy`, and `TLSCertPEM`/`TLSKeyPEM` on
`RouteDataPlaneConfig`. VIP data planes no longer know about TLS.

**Agent flags.** `--proxy-tls-listen <addr>` (default: `--proxy-listen` host
with port 443 when `--proxy` is set). The Ansible worker template passes
`{{ ansible_host }}:{{ proxy_tls_port | default(443) }}`.

**Data plane independence.** Termination happens in userspace before
forwarding to endpoint IPs, so gate TLS works the same on nftables, iptables,
and eBPF.

## `cca get gates`

New resource type for `cca get`:

```
$ cca get gates
NAME          HOST             PATH  SERVICE        TLS             EXPIRES     BACKENDS
web-public    app.example.com  -     frontend:8080  auto            2027-01-31  3
internal-api  api.internal     -     api:3000       manual          2027-06-01  2
api-sticky    -                -     api:3000       -               -           2
```

TLS column shows `auto`, `manual`, `auto (pending)` (not yet issued), or `-` (no TLS).
EXPIRES is read from the effective certificate (desired for manual, derived for auto).
Gates are tenant-filtered by target service ownership, like services and instances.

`cca describe gate <name>` shows routing, sticky mode, TLS mode, certificate host
and expiry (with days remaining), and the resolved backend list. Neither command
ever returns key or certificate PEM material.

## Implementation Plan

### Phase 1 — DSL + Facts
- Add `RouteTLSDecl` to AST (`lang/ast.go`)
- Parse `tls` sub-block in `parseRouteField()` (`lang/parser.go`)
- Compile TLS facts; read PEM files for manual mode (`lang/compiler.go`)
- Parser tests: `tls auto`, `tls { mode auto }`, `tls { cert ... key ... }`, validation errors
- Compiler tests: fact generation for both modes
- Formatter support (`lang/formatter.go`)
- Lint: warn if `tls` without `host`

### Phase 2 — TLS Certificate Controller
- New `controllers/route_tls.go` with `RouteTLSController`
- Issue certs via cluster CA for `auto` mode
- Auto-renew within 30-day window
- Delete stale derived TLS facts
- Unit tests: issuance, renewal, cleanup

### Phase 3 — Agent Data Plane
- Extend `RouteDataPlaneConfig` with TLS fields
- Read TLS cert/key from derived (auto) or desired (manual) facts
- Upgrade embedded proxy to serve HTTPS when TLS material present
- Start proxy for non-sticky TLS gates (TLS requires L7)
- Integration tests

### Phase 4 — CLI + Polish
- `cca get gates` — list all gates with TLS status
- Update `examples/reference.cca` with TLS examples
- Update `design/dsl-reference.md`
- `cca describe gate <name>` — show cert expiry, backends, sticky config

## Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| No ACME/Let's Encrypt | Cluster CA only | Keep it simple; ACME is a future enhancement. Cluster CA already works. |
| No Caddy/Envoy | Embedded Go proxy | The agent gate proxy already exists. Adding TLS is just `tls.Config`. No new dependency. |
| PEM in facts (manual) | Read at apply time | Same pattern as `config { from "file" }`. Secrets are encrypted at rest in etcd. |
| TLS requires host | Validation error | Can't issue a cert without a hostname. IP-based TLS is a future edge case. |
| 90-day TTL, 30-day renewal | Matches Let's Encrypt convention | Short-lived certs, automatic rotation. |
| TLS on a node listener, not the VIP (M170) | `:443` on the agent gate proxy, SNI → cert | Service ports stay plain HTTP for in-cluster callers; many gates per service; works on every data plane. |
| No default certificate | Unknown SNI fails the handshake | Never present a cert for a host the client didn't ask for. |

## Security Considerations

- Manual mode PEM keys are stored in etcd encrypted at rest (existing secret encryption).
- Auto mode private keys are generated server-side by the CA, never leave the fact store unencrypted.
- The agent reads TLS material from the fact store over mTLS — same security boundary as secrets.
- Cert issuance is limited to hostnames declared in `gate` blocks — no wildcard certs.
- Private keys live only in agent memory (the cert map); they are never written to disk or logs on nodes.

## Not In Scope

- **ACME / Let's Encrypt**: requires external DNS challenge or HTTP-01 challenge infrastructure. Future milestone.
- **SNI-based routing without termination**: passthrough TLS. Possible future enhancement.
- **Wildcard certificates**: explicit hostnames only.
- **Client certificate verification (mTLS on the gate)**: separate from service mesh.
