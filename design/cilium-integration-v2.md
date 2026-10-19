# Cilium eBPF Integration — Design v2

**Date:** 2026-10-19
**Status:** Proposed
**Supersedes:** networking.md "Future: Cilium eBPF Integration" section
**Depends on:** [cilium-kvstore-research.md](cilium-kvstore-research.md), [cilium-integration-guide.md](cilium-integration-guide.md), [networking.md](networking.md), [ebpf-data-plane.md](ebpf-data-plane.md)
**Debug log:** [cilium-ebpf-debug.md](cilium-ebpf-debug.md)

---

## Corrections to Prior Design

The original plan in [networking.md](networking.md) §"Future: Cilium eBPF Integration" stated:

> *"Integration boundary: CCattler writes endpoint and policy facts to etcd. Cilium reads them. No direct API calls between the two."*

This is **partially wrong**. Bare-metal testing on 2026-10-19 revealed:

### What works via kvstore (etcd)

- **Identity allocation** — Cilium allocates and reads identities from `cilium/state/identities/v1/`
- **IP → identity mapping** — CCattler writes `cilium/state/ip/v1/<cluster>/<ip>`, Cilium reads them
- **Node registration** — Cilium agents register at `cilium/state/nodes/v1/<cluster>/<node>`, discover peers

### What does NOT work via kvstore

- **Service definitions** — Writing to `cilium/state/services/v1/` has NO effect. Cilium's standalone (non-K8s) agent does not watch this prefix for service registration. This was predicted by [cilium-integration-guide.md](cilium-integration-guide.md) §10.5: *"Service features limited — Cilium's L4 service proxy and load balancer features assume Kubernetes Services."*

### Additional findings

- **`--routing-mode=native` requires backend reachability.** Cilium's socket LB rewrites VIP → backend IP at the connect() syscall level. The kernel then routes the packet normally. If the backend is a container IP on a remote node, standard IP routing must deliver the packet. CCattler's VXLAN overlay (`cca-vxlan0`) does NOT bridge host-namespace traffic to the container bridge — ARP resolution fails for all remote container IPs.

- **`iptables: Incompatible with this kernel`** — Ubuntu 24.04 is nftables-only. Cilium's internal iptables reconciler fails continuously with `--install-iptables-rules=false` because it still tries to clean up old chains. Fix: symlink `iptables` → `iptables-nft` via `update-alternatives`.

---

## Revised Architecture

### Design split: two independent data plane modes

```
--dataplane auto     →  nftables DNAT + CCattler VXLAN overlay (M121-M122)
--dataplane cilium   →  Cilium eBPF (tunnel mode) + Cilium service API (this design)
```

These are **completely independent** code paths. They do not coexist on the same node. The `auto` path does not need Cilium installed. The `cilium` path does not need nftables rules. The only shared component is the container bridge (`cca-net`), which nerdctl manages regardless of data plane mode.

### What Cilium owns (when dataplane=cilium)

| Responsibility | Was | Now |
|---|---|---|
| Cross-node encapsulation | CCattler VXLAN (`cca-vxlan0`) | Cilium VXLAN (`cilium_vxlan`) |
| VIP load balancing | nftables `ip cca` table | eBPF socket LB + tc BPF |
| MASQUERADE / SNAT | nftables postrouting | Cilium BPF masquerade |
| Network policy (future) | Application-layer engine | eBPF per-packet enforcement |

### What CCattler keeps

| Responsibility | Notes |
|---|---|
| VIP allocation | `NetworkController` allocates VIPs, writes `network/vip/` facts |
| DNS resolution | `*.cca.local` → VIP, reads `network/vip/` facts |
| Subnet allocation | `desired/network/subnet/{node}` — drives IPAM |
| Container lifecycle | nerdctl create/start/stop, container bridge |
| Endpoint tracking | `endpoint/service/{name}/{instance}` facts |
| Service registration to Cilium | **NEW**: agent calls Cilium API per reconciliation cycle |

### Component diagram

```
                    ┌──────────────────────────────────┐
                    │   CCattler Control Plane          │
                    │   (etcd: facts + controllers)     │
                    │                                    │
                    │   Writes to etcd:                  │
                    │   • desired/*, observed/*          │
                    │   • network/vip/*, endpoint/*      │
                    │   • cilium/state/identities/       │
                    │   • cilium/state/ip/               │
                    └──────────┬────────────────────────┘
                               │
          ┌────────────────────┼────────────────────┐
          │                    │                     │
    ┌─────▼──────┐      ┌─────▼──────┐       ┌─────▼──────┐
    │ CCattler    │      │ CCattler    │       │ CCattler    │
    │ Agent       │      │ Agent       │       │ Agent       │
    │ (node-1)   │      │ (node-2)   │       │ (node-3)   │
    │             │      │             │       │             │
    │ Reconciles: │      │ Reconciles: │       │ Reconciles: │
    │ • containers│      │ • containers│       │ • containers│
    │ • services  │      │ • services  │       │ • services  │
    │   → Cilium  │      │   → Cilium  │       │   → Cilium  │
    │     API     │      │     API     │       │     API     │
    └──────┬──────┘      └──────┬──────┘       └──────┬──────┘
           │ localhost          │ localhost           │ localhost
    ┌──────▼──────┐      ┌──────▼──────┐       ┌──────▼──────┐
    │ Cilium Agent│      │ Cilium Agent│       │ Cilium Agent│
    │ (eBPF)     │      │ (eBPF)     │       │ (eBPF)     │
    │             │      │             │       │             │
    │ Reads etcd: │      │ Reads etcd: │       │ Reads etcd: │
    │ • identities│      │ • identities│       │ • identities│
    │ • ip/       │      │ • ip/       │       │ • ip/       │
    │ • nodes/    │      │ • nodes/    │       │ • nodes/    │
    └─────────────┘      └─────────────┘       └─────────────┘
           │                    │                     │
    ═══════╪════════════════════╪═════════════════════╪═══════
           │     cilium_vxlan tunnel mesh (VNI auto)  │
    ═══════╪════════════════════╪═════════════════════╪═══════
```

The etcd boundary is for **identity, IP mapping, and node discovery** (kvstore works).
The API boundary is for **service definitions** (kvstore doesn't work).

---

## Cilium Agent Configuration

### systemd unit flags

```
--kvstore etcd
--kvstore-opt etcd.config=/etc/cilium/etcd-config.yaml
--enable-ipv4=true
--enable-ipv6=false
--routing-mode=tunnel
--tunnel-protocol=vxlan
--datapath-mode=veth
--bpf-lb-sock=true
--bpf-lb-sock-hostns-only=false
--kube-proxy-replacement=true
--install-iptables-rules=true
--enable-ipv4-masquerade=true
--enable-l7-proxy=false
--enable-policy=default
--identity-allocation-mode=kvstore
```

### Changes from v1 (native routing attempt)

| Flag | v1 | v2 | Why |
|---|---|---|---|
| `--routing-mode` | `native` | `tunnel` | Cilium manages its own VXLAN; container IPs routable cross-node |
| `--tunnel-protocol` | (absent) | `vxlan` | Explicit; Geneve is alternative |
| `--install-iptables-rules` | `false` | `true` | Works after `iptables-nft` symlink; Cilium needs some NAT rules |
| `--enable-ipv4-masquerade` | `false` | `true` | Required for tunnel mode SNAT |
| `--ipv4-native-routing-cidr` | `<cluster-cidr>` | (removed) | Not applicable in tunnel mode |
| `--enable-endpoint-routes` | `true` | (removed) | Not applicable in tunnel mode |
| `--ipv4-range` | `<cluster-cidr>` | (removed) | Cilium IPAM not used; CCattler manages IPs |

---

## Service Registration via Cilium REST API

### Why not kvstore

Per [cilium-kvstore-research.md](cilium-kvstore-research.md) §2 and confirmed in testing:

> Cilium's service/backend model is strongly Kubernetes-centric. Standalone orchestrators typically don't need to populate these keys unless integrating with Cilium's service proxy features.

In standalone etcd mode, the Cilium agent does NOT watch `cilium/state/services/v1/` for service registration. Services must be pushed to each node's local Cilium agent via its REST API.

### API call per reconciliation cycle

The CCattler agent's `CiliumServiceReconciler` (new sub-reconciler, `agent/cilium_service_reconciler.go`) runs on each reconciliation cycle:

1. Read all `network/vip/{service}` and `endpoint/service/{name}/*` facts from the store
2. Build the desired service → backend mapping
3. Compare with Cilium's current service list (`GET /v1/service/`)
4. Create, update, or delete services via `PUT /v1/service/{id}` and `DELETE /v1/service/{id}`

All backends use **container IPs** (not host:port). In tunnel mode, Cilium routes cross-node traffic via `cilium_vxlan` — the kernel doesn't need to resolve ARP for remote container IPs because Cilium's BPF handles encapsulation before the packet hits the routing table.

### API schema

```json
PUT http://localhost:9234/v1/service/{id}
{
  "frontend-address": {
    "ip": "10.169.0.1",
    "port": 80,
    "protocol": "TCP"
  },
  "backend-addresses": [
    { "ip": "10.56.166.2", "port": 80, "id": 1 },
    { "ip": "10.56.69.2",  "port": 80, "id": 2 },
    { "ip": "10.56.226.2", "port": 80, "id": 3 }
  ],
  "flags": {
    "type": "ClusterIP",
    "trafficPolicy": "Cluster"
  }
}
```

### Idempotency

The reconciler follows CCattler's standard pattern: `ensure(desired_state)`, not `react_to(event)`. Each cycle computes the full desired service set and converges Cilium's state to match. Services not in the desired set are deleted. This handles all edge cases: service creation, backend changes, service deletion, agent restart.

---

## What Gets Disabled (dataplane=cilium)

| Component | Status |
|---|---|
| `cca-vxlan0` VXLAN interface | NOT created; agent's `VXLANReconciler` is skipped |
| nftables `ip cca` table | NOT created; agent's `DataPlaneReconciler` is skipped |
| iptables `CCA_SERVICES` chain | NOT created |
| `cca0` dummy interface (VIP holder) | NOT created; Cilium handles VIP addresses |
| Overlay route programming | NOT done; Cilium manages tunnel routes |
| Subnet allocation facts | Still written; containers still get IPs from node subnet |
| Container bridge (`cca-net`) | Still exists; nerdctl manages it |
| CiliumFactTranslator | **Retained** for identity + IP facts; service writes removed |

---

## Ansible Role Changes

### `roles/cilium/tasks/main.yml` additions

1. **iptables-nft symlink** (before Cilium agent start):
   ```yaml
   - name: Ensure iptables uses nft backend
     command: update-alternatives --set iptables /usr/sbin/iptables-nft
     changed_when: false
     failed_when: false

   - name: Ensure ip6tables uses nft backend
     command: update-alternatives --set ip6tables /usr/sbin/ip6tables-nft
     changed_when: false
     failed_when: false
   ```

2. **cilium-operator** on control plane node (for identity GC):
   ```yaml
   - name: Install Cilium operator (control plane only)
     template:
       src: cilium-operator.service.j2
       dest: /etc/systemd/system/cilium-operator.service
     when: "'controlplane' in group_names"
   ```

### `cilium-agent.service.j2` update

Replace `--routing-mode=native` block with the tunnel mode flags listed above.

---

## Implementation Plan

### Phase 1: Tunnel mode + iptables-nft (Ansible only, no code)

- Update `cilium-agent.service.j2` with tunnel mode flags
- Add `iptables-nft` symlink task to Cilium role
- Redeploy Cilium on testbed
- Verify: `cilium status` shows OK, `cilium bpf tunnel list` shows peer entries

### Phase 2: Manual service registration (validate tunnel LB works)

- Register whoami VIP via `cilium service update` CLI on all 3 nodes
- Test: `curl 10.169.0.1:80` from each node → all 6 backends respond
- This validates tunnel mode + socket LB works before writing Go code

### Phase 3: CiliumServiceReconciler (Go code)

- New file: `agent/cilium_service_reconciler.go`
- Implement `ReconcileCiliumServices(facts) → []cilium_api_call`
- Wire into agent reconciliation loop when `--dataplane cilium`
- Tests: unit (mock Cilium API), integration (real Cilium agent + etcd)

### Phase 4: Remove CiliumFactTranslator service writes

- CiliumFactTranslator continues writing identity + IP facts to etcd
- Remove service writes to `cilium/state/services/v1/` (dead code — Cilium doesn't read them)
- Update [networking.md](networking.md) "Future" section with corrected integration boundary

### Phase 5: Endpoint registration + network policy (optional)

- Register containers as Cilium endpoints via API
- Enable Cilium identity-based network policy
- Map CCattler's `policy/network/` facts to Cilium policy labels

---

## Risk: Tunnel Mode Performance

Tunnel mode adds ~5% throughput overhead vs native routing (VXLAN encap/decap per packet). This matches CCattler's own VXLAN overlay overhead. For the control plane use case (orchestrator, not hyperscale), this is acceptable.

If benchmarks show tunnel overhead matters at scale:
- Switch to `--routing-mode=native` + BGP peering (e.g., BIRD or FRRouting)
- Or use Geneve (slightly more efficient than VXLAN for large payloads)

---

## References

- [cilium-kvstore-research.md](cilium-kvstore-research.md) — Full kvstore prefix map, identity model, lease semantics
- [cilium-integration-guide.md](cilium-integration-guide.md) — Quick reference, label format, standalone flags
- [cilium-ebpf-debug.md](cilium-ebpf-debug.md) — Debug log from 2026-10-19 testing
- [networking.md](networking.md) — CCattler network architecture, VXLAN overlay, VIP model
- [ebpf-data-plane.md](ebpf-data-plane.md) — Custom eBPF path (alternative to Cilium, deferred)
- Cilium REST API: `cilium-agent --api-server-host 127.0.0.1 --api-server-port 9234`
