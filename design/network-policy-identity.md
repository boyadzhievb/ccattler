# Identity-Based Network Policy on eBPF (M196 design note)

Status: draft for review (2026-11-04). Inspired by Cilium's identity model
(cilium.io blog, 2026-08-28, "Zero-Trust Developer Platforms with Cilium
Network Policies").

## Problem

Policy is declared by identity but enforced by address:

```
network {
    allow frontend/web -> payments/checkout port 443
    deny  frontend/web -> payments/database
}
```

`NetworkPolicyController` resolves each rule to instance IPs and writes
per-node `derived/network/rule/{node}/{index}` = `srcIP:dstIP:port:action`.
Every reschedule, scale or restart rewrites rules on every node, and the rule
count grows with sources × targets. M196 as planned ("allow/deny by source
CIDR") keeps that model in eBPF.

## Proposal

Enforce on **identity**, not IP. Only the IP→identity table changes when
instances move; policy entries change only when policy changes.

### 1. Identities

- Every service gets a numeric identity: `derived/network/identity/{team}/{service}` = `u32`,
  allocated by `NetworkPolicyController` (stable, never reused while the
  service exists).
- Reserved identities: `host` (node itself), `gate` (agent gate proxy),
  `world` (outside the cluster), `cluster-dns`.

### 2. BPF maps (per node)

| Map | Key → Value | Written from | Changes when |
|---|---|---|---|
| `cca_ipcache` | instance IP → identity | endpoint facts (all nodes) | an instance starts/stops/moves (1 entry) |
| `cca_policy` | {src id, dst id, port, proto} → allow/deny | compiled policy facts | policy changes |
| `cca_policy_selected` | dst id → default-deny flag | compiled policy facts | policy changes |

### 3. Enforcement

tc ingress program on the destination node's container interfaces:
`src = ipcache[saddr]`, `dst = ipcache[daddr]`; if `dst` is selected,
allow only on a matching `cca_policy` entry (deny entries win), else drop.
Replies pass via conntrack (established). Enforcing at the destination keeps
one decision point per flow.

### 4. Default-deny derived from intent (main idea from the article)

Developers should not write firewall rules. CCattler already knows intent:

| Declaration | Derived allow |
|---|---|
| any service | same team (tenant) |
| `export {team}/{service} { allow other-team }` | listed teams |
| `gate ... service X port P` | `gate` → X:P |
| `expose` only | same team only |
| explicit `network { allow/deny }` | as written, on top |

So every service is default-deny outside its team by construction; explicit
rules add exceptions. Derived policy is recompiled on every reconcile, so it
cannot drift from the declarations.

### 5. Rollout

- `--network-policy enforce|audit|off` on the agent, default `audit` first:
  drops are counted and logged with identities, not applied.
- `cca get network-drops` (or `cca events`) shows `frontend/web → payments/database:5432 denied`,
  named by identity, not IP.
- nftables path stays as fallback until the M196 elimination gate.

## Out of scope (later)

- Egress / FQDN allowlists (needs a DNS proxy).
- L7 rules (HTTP method/path) — would need a proxy like Envoy; not planned.
- Identity carried in VXLAN headers; `cca_ipcache` from endpoint facts is enough.

## Open questions

1. Is "same team allowed by default" the right baseline, or strict per-service deny?
2. Should `audit` be the default for one release before `enforce`?
3. Identity scope: `{team}/{service}` only, or also environment?
