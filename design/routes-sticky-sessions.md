# Routes & Sticky Sessions — M118 Design

## Problem

CCattler has VIPs and round-robin DNAT, but no way to:
1. Route external traffic by hostname or path to specific services
2. Pin a client to the same backend instance across requests (session affinity)

Traefik calls this IngressRoute, nginx calls it upstream+server, HAProxy calls it frontend+backend. We call it **route**.

## DSL

```
route web-public {
    host "app.example.com"       # match by Host header (optional)
    path "/api"                  # match by URL prefix (optional)
    service api port 3000        # target service and port

    sticky cookie {
        name "session"           # cookie name (default: __CCA_STICKY_{service})
        secure true              # Secure flag
        http-only true           # HttpOnly flag
        same-site "strict"       # SameSite: strict, lax, none
    }
}
```

Minimal form (just sticky, no host/path filtering):
```
route api-sticky {
    service api port 3000
    sticky cookie
}
```

Source-IP affinity (alternative to cookie):
```
route api-ip {
    service api port 3000
    sticky source-ip
}
```

## Fact Model

A `route` block compiles to facts under `desired/route/{name}/`:

```
desired/route/web-public              = ""                    # route marker
desired/route/web-public/host         = "app.example.com"
desired/route/web-public/path         = "/api"
desired/route/web-public/service      = "api"
desired/route/web-public/port         = "3000"
desired/route/web-public/sticky       = "cookie"             # or "source-ip"
desired/route/web-public/cookie/name  = "session"
desired/route/web-public/cookie/secure     = "true"
desired/route/web-public/cookie/httponly   = "true"
desired/route/web-public/cookie/samesite   = "strict"
```

## Architecture

### Components

1. **Parser** — new `parseRouteDeclaration()` in `lang/parser.go`, `RouteDecl` AST node
2. **Compiler** — `route` → `desired/route/{name}/` facts
3. **Route Controller** (new) — watches `desired/route/` and `observed/endpoint/`, writes `derived/route/{name}/backends` with resolved backend addresses
4. **Agent Data Plane** — reads `derived/route/` facts, programs:
   - **iptables (current)**: DNAT rules with `--match recent` or `statistic` for source-IP; cookie affinity requires userspace proxy
   - **Userspace proxy (new, for cookie affinity)**: lightweight L7 proxy (Go `net/http/httputil.ReverseProxy`) that sets/reads the sticky cookie and forwards to the selected backend

### Why Userspace Proxy for Cookies

iptables operates at L3/L4 — it cannot read HTTP cookies. Cookie-based sticky sessions require L7 inspection. Two options:

1. **Userspace Go proxy** — simple, portable, works everywhere. Adds one hop but only for routes with `sticky cookie`. Non-sticky routes stay pure iptables.
2. **eBPF** — could parse HTTP headers in-kernel, but significantly more complex. Defer to the eBPF milestone.

Decision: userspace proxy for cookies now, eBPF optimization later.

### Source-IP Affinity (L4, no proxy)

Source-IP sticky uses iptables `--match recent` or a BPF map to track client_ip → backend mappings. No L7 proxy needed — stays in the kernel data path.

### Cookie Mechanics

1. Client sends request without cookie → proxy picks backend via round-robin, adds `Set-Cookie: __CCA_STICKY_api=<instance-id>; HttpOnly; Secure; SameSite=strict`
2. Client sends request with cookie → proxy extracts instance ID, routes to that backend
3. Backend goes down → proxy detects failure, picks new backend, sets new cookie
4. Cookie value is the instance ID (e.g. `api-2`), not an opaque hash — makes debugging easier

### Route Controller

The route controller is a standard CCattler controller:

```
watch: desired/route/, observed/endpoint/
write: derived/route/{name}/backends
```

On reconcile:
1. Read route facts → resolve target service
2. Read endpoint facts for that service → collect healthy backend addresses
3. Write `derived/route/{name}/backends = "10.100.1.3:3000,10.100.2.5:3000"`
4. If sticky=cookie, also write `derived/route/{name}/proxy-port = "19001"` (allocated from a port range)

### Agent Integration

The agent's data plane reconciler already programs iptables for VIP DNAT. For routes:

- **Non-sticky routes** with host/path: program iptables DNAT (same as VIP, just with the route's explicit target)
- **Source-IP sticky**: iptables with `xt_recent` module to pin client IPs
- **Cookie sticky**: start a Go reverse proxy on the allocated proxy-port, program iptables to redirect matching traffic to the proxy port

The proxy lifecycle is managed by the agent: started when a cookie-sticky route appears, stopped when removed.

## Implementation Plan

### Phase 1 — DSL + Facts
- `RouteDecl` AST node and parser
- Compiler: route → facts
- Parser tests (host, path, sticky cookie, sticky source-ip, minimal forms)
- Compiler tests (fact generation)
- Add `route` to `examples/reference.cca`

### Phase 2 — Route Controller
- New controller in `controllers/route.go`
- Watches route + endpoint facts
- Writes resolved backend lists to `derived/route/`
- Integration tests

### Phase 3 — Agent Data Plane
- Source-IP affinity via iptables
- Cookie affinity via embedded reverse proxy
- Backend health-aware routing (skip failed backends, rewrite cookie)
- E2E test with whoami workload + curl sticky validation

## Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Top-level block name | `route` | Plain English, not K8s jargon |
| Cookie vs source-IP | Both, cookie default | Cookie works across NAT/proxies, source-IP for L4 |
| Cookie value | Instance ID | Debuggable, no extra mapping table |
| L7 proxy | Go httputil.ReverseProxy | Simple, portable, only for cookie routes |
| Default cookie name | `__CCA_STICKY_{service}` | Namespaced per service, no collisions |

## Comparison

| System | Resource | Sticky Config | L7 Method |
|--------|----------|---------------|-----------|
| Nginx | `upstream` + `server` | `sticky cookie` / `ip_hash` | Native |
| Traefik | `IngressRoute` | `sticky.cookie` | Native |
| HAProxy | `backend` + `frontend` | `cookie SERVERID insert` | Native |
| CCattler | `route` | `sticky cookie` / `sticky source-ip` | Embedded proxy |
