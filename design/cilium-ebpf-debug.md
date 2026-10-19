# Cilium eBPF Data Plane — Debug Report

**Date:** 2026-10-19
**Cluster:** 3-node bare-metal testbed (Ubuntu 24.04, kernel 6.8.0-147-generic)
**Cilium:** v1.16.4, kvstore (etcd) mode, native routing, socket-based LB
**CCattler:** v1.53.0-beta (M127), `--dataplane cilium` build tag

---

## Cluster Topology

| Node    | IP              | Role                    | Subnet         | Containers |
|---------|-----------------|-------------------------|----------------|------------|
| node-1  | 192.168.100.215 | controlplane+etcd+worker | 10.56.166.0/24 | 4          |
| node-2  | 192.168.100.30  | worker                  | 10.56.69.0/24  | 4          |
| node-3  | 192.168.100.31  | worker                  | 10.56.226.0/24 | 3          |

**CIDR configuration:**
- Cluster CIDR: `10.56.0.0/16` (container overlay)
- VIP CIDR: `10.169.0.0/24` (service virtual IPs)

**Workloads deployed:**
- whoami (6 instances, VIP 10.169.0.1:80)
- nginx (3 instances, VIP 10.169.0.2:80)
- redis (2 instances, VIP 10.169.0.3:6379)

---

## What Works

1. **Cilium agent starts and compiles BPF programs on all 3 nodes.**
2. **Socket-based LB (bpf-lb-sock) hooks into connect() syscall** — VIP connections are intercepted and translated to backend IPs.
3. **Local backends work.** From node-1, `curl 10.169.0.1:80` reaches local containers (10.56.166.2, 10.56.166.3).
4. **CCattler's CiliumFactTranslator writes service/endpoint/identity facts to etcd** under `cilium/state/` prefix.
5. **Cilium reads node information from etcd** — `cilium node list` shows all 3 nodes.
6. **CCattler agent runs in `--dataplane cilium` mode** — no nftables rules installed (no `ip cca` table).

## What Does NOT Work

1. **Remote backend container IPs are unreachable** — VIP load balancing fails for backends on other nodes.
2. **VXLAN overlay does not carry any traffic** — cross-node container-to-container also fails.
3. **Cilium kvstore quorum check fails continuously** — "quorum check failed N times in a row."
4. **Node-2 and node-3 have empty Cilium service lists** — only node-1 has the manually registered service.
5. **iptables reconciliation fails every 10 seconds** — Ubuntu 24.04 is nftables-only, legacy iptables doesn't work.

---

## Root Cause Analysis

### Issue 1: VXLAN overlay is non-functional

**Symptom:** `ping -c1 10.56.69.2` from node-1 → 100% packet loss. ARP entries for all remote container IPs show `FAILED`.

**Commands to reproduce:**

```bash
# On node-1:
ip neigh show dev cca-vxlan0
# Output: 10.56.69.2 FAILED / 10.56.226.2 FAILED / etc.

ping -c1 -W2 10.56.69.2
# Output: 100% packet loss
```

**Root cause:** `cca-vxlan0` is a standalone VXLAN interface that is **NOT a port on the container bridge** (`br-82b173278bd6`). The containers live behind the bridge on veth pairs. When a packet is routed to `cca-vxlan0`, the VXLAN encapsulates it and sends to the remote node. On the remote node, the packet arrives on `cca-vxlan0` — but that interface is not in the bridge, so ARP requests for container IPs go unanswered. The containers only have their IP on their namespace veth (connected to the bridge), not on `cca-vxlan0`.

**Evidence:**

```bash
# On any node:
bridge link show dev cca-vxlan0
# Output: (empty — not in any bridge)

# Bridge only has container veths:
bridge link show
# Output: vethXXX master br-82b173278bd6

# Routes send remote subnet traffic to the disconnected VXLAN:
ip route show | grep cca-vxlan0
# 10.56.69.0/24 dev cca-vxlan0 scope link
# 10.56.226.0/24 dev cca-vxlan0 scope link
```

**Topology diagram (broken):**

```
Node-1:                                Node-2:
  Host namespace                         Host namespace
  ┌──────────────┐                       ┌──────────────┐
  │ cca-vxlan0   │ ←── VXLAN tunnel ──→  │ cca-vxlan0   │
  │ (standalone) │                       │ (standalone) │
  └──────────────┘                       └──────────────┘
                     ← NO connection →
  ┌──────────────┐                       ┌──────────────┐
  │ br-82b1...   │                       │ br-82b1...   │
  │  ├── veth1   │                       │  ├── veth1   │
  │  └── veth2   │                       │  └── veth2   │
  └──────────────┘                       └──────────────┘
```

**Fix options:**
- **A) Add `cca-vxlan0` to the bridge** — `ip link set cca-vxlan0 master br-82b173278bd6` on each node, remove the explicit vxlan routes (the bridge will handle L2 forwarding). Requires MTU alignment (bridge 1500 vs vxlan 1450).
- **B) Add proxy ARP** — `sysctl -w net.ipv4.conf.cca-vxlan0.proxy_arp=1` and matching on the bridge side, with static neighbor entries.
- **C) Use Cilium-managed tunnels** — switch to `--routing-mode=tunnel` so Cilium creates its own `cilium_vxlan` interface and handles cross-node encapsulation independently.
- **D) Use host IP:port backends** — the CiliumFactTranslator already writes remote backends as `<host-ip>:<host-port>` (visible in etcd), bypassing the need for container IP reachability entirely.

### Issue 2: Cilium kvstore quorum check failure

**Symptom:** `cilium status` shows "Kvstore service is not ready: quorum check failed N times in a row."

**Commands to reproduce:**

```bash
sudo cilium status
# KVStore: Failure — quorum check failed 13 times in a row
```

**But the connection works from CLI:**

```bash
sudo cilium kvstore get --recursive cilium/state/services
# Successfully connects: "Initial etcd session established"
```

**Root cause:** The Cilium daemon's internal etcd heartbeat/quorum-check loop is failing. This may be caused by:
- The iptables error storm (every 10 seconds) consuming agent resources
- The etcd client session timing out and not recovering
- The daemon having been running since startup without successfully completing the quorum check

**Evidence:** The iptables errors repeat every 10 seconds indefinitely:
```
level=error msg="iptables rules full reconciliation failed" error="...iptables: Incompatible with this kernel."
```

### Issue 3: iptables incompatible with kernel

**Symptom:** Cilium agent logs show `iptables: Incompatible with this kernel` every 10 seconds.

**Root cause:** Ubuntu 24.04 is nftables-only. The legacy iptables binary (`iptables-legacy`) is not compatible. Cilium still tries to reconcile iptables rules despite `--install-iptables-rules=false`.

**Evidence:**

```bash
sudo tail -5 /var/log/ccattler/cilium-agent.log
# error="...unable to run 'iptables -t nat -S OLD_CILIUM_POST_nat' iptables command:
#   exit status 1 stderr=\"iptables: Incompatible with this kernel.\\n\""
```

```bash
sudo nft list tables
# table ip nat
# table ip filter
# (no iptables-legacy tables)
```

**Fix:** These errors are non-fatal (Cilium continues operating), but they may affect the quorum check by consuming resources. Consider:
- Setting `--install-no-conntrack-iptables-rules=false` (if available in 1.16.4)
- Symlinking `iptables` → `iptables-nft` so Cilium uses the nft backend
- Upgrading to Cilium 1.17+ which has better nftables support

---

## Full Debug Command Reference

### Pre-requisites check

```bash
# Kernel version (needs >= 5.10 for BPF, >= 5.15 for TCX)
uname -r

# BTF support (required for CO-RE BPF programs)
ls -la /sys/kernel/btf/vmlinux

# BPF filesystem mounted
mount | grep bpf

# IP forwarding enabled
sysctl net.ipv4.ip_forward
```

### Network topology

```bash
# All interfaces with IPs
ip -br addr

# Bridge list
ip link show type bridge

# Bridge ports (which veths are on which bridge)
bridge link show

# VXLAN interface details (VNI, local IP, dst port)
ip -d link show cca-vxlan0

# Is VXLAN in any bridge?
bridge link show dev cca-vxlan0

# VXLAN FDB entries (remote node mappings)
bridge fdb show dev cca-vxlan0

# ARP table on VXLAN (FAILED = broken overlay)
ip neigh show dev cca-vxlan0

# Overlay routes
ip route show | grep -E '10\.(56|169)\.'

# Proxy ARP settings
cat /proc/sys/net/ipv4/conf/cca-vxlan0/proxy_arp
cat /proc/sys/net/ipv4/conf/br-*/proxy_arp
```

### Container networking

```bash
# nerdctl network details
nerdctl network ls
nerdctl network inspect cca-net

# Container list with ports
nerdctl ps --format 'table {{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Ports}}'

# Container routing table (from inside)
nerdctl exec <container-id> ip route

# Container IP
nerdctl exec <container-id> ip addr show eth0
```

### Connectivity tests

```bash
# Ping remote container IP (should work if overlay is healthy)
ping -c2 -W2 <remote-container-ip>

# Cross-node container-to-container
nerdctl exec <local-container> sh -c 'echo test | nc -w2 <remote-container-ip> 80'

# VIP test (uses Cilium socket LB if active)
curl -s --connect-timeout 3 http://<vip>:<port>/
```

### Cilium agent

```bash
# Agent process running?
systemctl is-active cilium-agent

# Full status (kvstore, routing mode, masquerading, BPF mode)
cilium status

# Brief status (OK/Failure one-liner)
cilium status --brief

# Service list (VIP → backends mapping)
cilium service list

# BPF LB map entries (what the eBPF program sees)
cilium bpf lb list

# Endpoint list (managed network endpoints)
cilium endpoint list

# Node list (cluster nodes known to Cilium)
cilium node list

# BPF tunnel map (VXLAN/Geneve encap targets)
cilium bpf tunnel list

# Identity list (security identities)
cilium identity list

# Agent configuration
cilium config

# Agent logs
tail -100 /var/log/ccattler/cilium-agent.log

# Agent logs excluding iptables noise (Ubuntu 24.04)
grep -v iptables /var/log/ccattler/cilium-agent.log | tail -50
```

### Cilium kvstore (etcd)

```bash
# Etcd config file
cat /etc/cilium/etcd-config.yaml

# Test kvstore connectivity from CLI
cilium kvstore get --recursive cilium/state/services

# Services in etcd (via cca watch)
cca watch 'cilium/state/services/'

# Identities in etcd
cca watch 'cilium/state/identities/'

# IP allocations in etcd
cca watch 'cilium/state/ip/'
```

### CCattler facts

```bash
# Cluster status (services, instances, nodes, VIPs)
cca status

# Watch specific fact prefix
cca watch 'network/vip/'
cca watch 'network/endpoint/'
cca watch 'observed/instance/'

# Agent systemd unit (check --dataplane flag)
cat /etc/systemd/system/cca-agent.service | grep -E 'ExecStart|dataplane'

# Server systemd unit
cat /etc/systemd/system/cca-server.service

# Agent logs
journalctl -u cca-agent --no-pager -n 50
```

### Firewall state

```bash
# nftables tables (should NOT have 'ip cca' when dataplane=cilium)
nft list tables

# nftables CCattler rules (if present)
nft list table ip cca

# iptables state (will fail on nftables-only kernels)
iptables -t nat -L -n
```

### Manually register Cilium service (for testing)

```bash
# Register a VIP with backends
cilium service update --id 1 \
  --frontend 10.169.0.1:80 \
  --backends 10.56.166.2:80,10.56.166.3:80,10.56.69.2:80

# Delete a service
cilium service delete 1
```

---

## Current State Summary (2026-10-19)

| Component           | Status  | Notes                                           |
|---------------------|---------|-------------------------------------------------|
| CCattler server     | OK      | 3 services, 11 instances, 3 nodes               |
| CCattler agent      | OK      | `--dataplane cilium` on all nodes                |
| Cilium agent        | PARTIAL | Running, BPF attached, kvstore quorum failing    |
| Socket LB (local)   | OK      | VIP → local container IP works                   |
| Socket LB (remote)  | FAIL    | Remote container IPs unreachable via VXLAN        |
| VXLAN overlay       | FAIL    | cca-vxlan0 not bridged, ARP resolution fails     |
| nftables data plane | OFF     | No `ip cca` table (expected with cilium mode)     |
| iptables compat     | FAIL    | Ubuntu 24.04 nftables-only, non-fatal errors     |
| CiliumFactTranslator| OK      | Writes services/endpoints/identities to etcd      |

### Etcd service facts written by CiliumFactTranslator

The translator already uses **host IP:port for remote backends**:

```json
{
  "name": "whoami",
  "frontend": {"address": "10.169.0.1", "port": 80},
  "backends": [
    {"address": "10.56.166.2", "port": 80},
    {"address": "192.168.100.30", "port": 80},
    {"address": "192.168.100.31", "port": 80},
    {"address": "10.56.166.3", "port": 80},
    {"address": "192.168.100.30", "port": 81},
    {"address": "192.168.100.31", "port": 81}
  ]
}
```

Local backends use container IPs (10.56.166.x), remote backends use host IP:port. If the kvstore quorum issue is fixed so Cilium reads these, the host-port backends should work without needing the VXLAN overlay for VIP traffic.

---

## Next Steps

1. **Fix Cilium kvstore quorum** — restart Cilium agent, or investigate etcd client session recovery.
2. **Symlink iptables → iptables-nft** — eliminate the error spam that may be affecting stability.
3. **Verify Cilium reads the CiliumFactTranslator service facts** — once kvstore works, `cilium service list` on all nodes should show the host-port backends.
4. **Test VIP with host-port backends** — should work cross-node without needing VXLAN fix.
5. **Fix VXLAN overlay** — add `cca-vxlan0` to bridge for general container-to-container cross-node traffic (separate from Cilium VIP path).
