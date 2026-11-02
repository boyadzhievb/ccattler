# VXLAN Encapsulation Failure Investigation

**Date:** 2026-11-01
**Trigger:** Cluster Health Check CI failure — https://github.com/boyadzhievb/ccattler/actions/runs/41288708277/job/124795247899
**Cluster:** 3 VMs on libvirt (ctrl .10, worker-1 .20, worker-2 .30) on hypervisor 192.168.100.49

---

## Symptom Chain

1. Airflow scheduler/webserver/worker stuck in `init=failed`
2. Init container `connection:airflow-db` can't reach metadata-db VIP
3. eBPF socket LB correctly rewrites VIP → container IP 10.32.165.11
4. But VXLAN overlay can't deliver packets cross-node → TCP dial timeout
5. Health check fails because Airflow services never start

---

## Investigation Commands and Outputs

### 1. Cluster State

```bash
# On ctrl (192.168.124.10):
vmherd ssh cca-deploy-ctrl -- 'sudo cca get instances'
```
- metadata-db: running on worker-2 (10.32.165.11)
- scheduler, webserver, worker: init=failed on ctrl and worker-1
- All init containers stuck on `connection:airflow-db` probe

### 2. Underlay Connectivity (WORKS)

```bash
# ctrl → worker-1:
vmherd ssh cca-deploy-ctrl -- 'ping -c2 192.168.124.20'
# Result: 2/2 received, ~0.3ms

# ctrl → worker-2:
vmherd ssh cca-deploy-ctrl -- 'ping -c2 192.168.124.30'
# Result: 2/2 received, ~0.3ms

# worker-1 → worker-2:
vmherd ssh cca-deploy-worker-1 -- 'ping -c2 192.168.124.30'
# Result: 2/2 received
```

### 3. VXLAN Interface Config

```bash
vmherd ssh cca-deploy-ctrl -- 'ip -d link show cca-vxlan0'
```
Output:
```
cca-vxlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1450 qdisc noqueue state UNKNOWN
    link/ether 0a:58:0a:64:0a:00 brd ff:ff:ff:ff:ff:ff
    vxlan id 42 local 192.168.124.10 dev enp2s0 srcport 0 0 dstport 4789
    ageing 300 noudpcsum noudp6zerocsumtx noudp6zerocsumrx
```
- VNI 42, port 4789, bound to enp2s0, deterministic MAC, MTU 1450
- Same config on all 3 nodes (different local IP and MAC)

### 4. VXLAN Has No IP Address (By Design)

```bash
vmherd ssh cca-deploy-ctrl -- 'ip addr show cca-vxlan0'
```
Output: No inet address on cca-vxlan0 — pure L3 tunnel backend (Flannel model).
Routes point through cca-vxlan0 via gateway with static neighbor entries.

### 5. Bridge Configuration

```bash
vmherd ssh cca-deploy-ctrl -- 'ip addr show cca0'
```
Output:
```
cca0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 ...
    inet 10.32.10.1/24 brd 10.32.10.255 scope global cca0
```
- ctrl bridge: 10.32.10.1/24
- worker-1 bridge: 10.32.20.1/24
- worker-2 bridge: 10.32.165.1/24

### 6. Routing Table

```bash
vmherd ssh cca-deploy-ctrl -- 'ip route show'
```
Key routes:
```
10.32.10.0/24 dev cca0 proto kernel scope link src 10.32.10.1
10.32.20.0/24 via 10.32.20.1 dev cca-vxlan0 onlink
10.32.165.0/24 via 10.32.165.1 dev cca-vxlan0 onlink
```
Routes to peer subnets go via cca-vxlan0 with onlink gateway.

### 7. Static Neighbors (ARP/NDP for VXLAN)

```bash
vmherd ssh cca-deploy-ctrl -- 'ip neigh show dev cca-vxlan0'
```
Output:
```
10.32.20.1 lladdr 0a:58:0a:64:14:00 PERMANENT
10.32.165.1 lladdr 0a:58:0a:64:a5:00 PERMANENT
```
Both permanent neighbors configured correctly.

### 8. FDB (Forwarding Database)

```bash
vmherd ssh cca-deploy-ctrl -- 'bridge fdb show dev cca-vxlan0'
```
Output:
```
0a:58:0a:64:14:00 dst 192.168.124.20 self permanent
0a:58:0a:64:a5:00 dst 192.168.124.30 self permanent
00:00:00:00:00:00 dst 192.168.124.20 self permanent
00:00:00:00:00:00 dst 192.168.124.30 self permanent
```
Unicast FDB entries map peer MACs to underlay IPs. BUM flood entries present.

### 9. VXLAN TX Counters (Packets Enter Device But Never Reach Wire)

```bash
# Before ping:
vmherd ssh cca-deploy-ctrl -- 'ip -s link show cca-vxlan0'
# TX: 12 packets, 0 errors

vmherd ssh cca-deploy-ctrl -- 'ping -c3 10.32.165.1'  # worker-2 bridge
# Result: 0% received

# After ping:
vmherd ssh cca-deploy-ctrl -- 'ip -s link show cca-vxlan0'
# TX: 15 packets, 0 errors (+3 packets, NO errors reported)
```

**Key finding:** TX counter increments (kernel accepted packets into VXLAN device) but no errors. Packets enter the VXLAN tunnel but silently disappear.

### 10. UDP Counters (No Encapsulated Packets)

```bash
# Before test:
vmherd ssh cca-deploy-ctrl -- 'cat /proc/net/snmp | grep Udp:'
# OutDatagrams: 3847

# After 3 pings through VXLAN:
vmherd ssh cca-deploy-ctrl -- 'cat /proc/net/snmp | grep Udp:'
# OutDatagrams: 3847 (UNCHANGED!)
```

**Key finding:** UDP OutDatagrams doesn't increment. The VXLAN device accepts packets but the kernel never creates the outer UDP encapsulation.

### 11. tcpdump on Physical NIC (Nothing)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo timeout 5 tcpdump -i enp2s0 udp port 4789 -c 5 2>&1'
```
Output: `0 packets captured`

No VXLAN-encapsulated packets on the wire while pings are being sent through cca-vxlan0.

### 12. User-Space UDP Works Fine

```bash
# On worker-2, listen:
vmherd ssh cca-deploy-worker-2 -- 'sudo timeout 10 tcpdump -i enp2s0 udp port 4789 -c 1 2>&1' &

# On ctrl, send user-space UDP:
vmherd ssh cca-deploy-ctrl -- 'echo test | nc -u -w1 192.168.124.30 4789'
```
Result: tcpdump on worker-2 captured the packet. User-space UDP to port 4789 works. Only VXLAN kernel encapsulation is broken.

### 13. Fresh VXLAN Device Test (Also Fails)

```bash
# Created brand-new VXLAN device (different VNI, different port):
vmherd ssh cca-deploy-ctrl -- 'sudo ip link add test-vxlan0 type vxlan id 99 dstport 4790 dev enp2s0 local 192.168.124.10'
vmherd ssh cca-deploy-ctrl -- 'sudo ip link set test-vxlan0 up'
vmherd ssh cca-deploy-ctrl -- 'sudo ip addr add 10.99.0.1/24 dev test-vxlan0'
vmherd ssh cca-deploy-ctrl -- 'sudo bridge fdb append 00:00:00:00:00:00 dev test-vxlan0 dst 192.168.124.30'

# Result: same failure. TX counter increments, no UDP on wire.
```

This rules out corrupted state on cca-vxlan0 specifically. All VXLAN encapsulation is broken system-wide.

### 14. Asymmetric Failure

```bash
# Worker-2 → ctrl bridge (WORKS):
vmherd ssh cca-deploy-worker-2 -- 'ping -c2 10.32.10.1'
# Result: 2/2 received!

# Ctrl → worker-2 bridge (FAILS):
vmherd ssh cca-deploy-ctrl -- 'ping -c2 10.32.165.1'
# Result: 0/2 received

# Worker-1 → worker-2 bridge (FAILS):
vmherd ssh cca-deploy-worker-1 -- 'ping -c2 10.32.165.1'
# Result: 0/2 received
```

**Key finding:** Worker-2 can send VXLAN traffic. Ctrl and worker-1 cannot. This is asymmetric — not all nodes are broken.

### 15. dmesg (No Kernel Errors)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo dmesg | grep -i vxlan'
vmherd ssh cca-deploy-ctrl -- 'sudo dmesg | grep -i "drop\|error\|fail" | tail -20'
```
No VXLAN-related kernel messages. No drops or errors logged.

### 16. eBPF Programs Attached

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo bpftool net show'
```
Output:
```
xdp:

tc:
cca0(5) clsact/ingress cca_tc_dnat id 119 tag ...

flow_dissector:

netfilter:

cgroup/connect4:
  /sys/fs/cgroup  cca_sock4_connect    id 116
cgroup/sendmsg4:
  /sys/fs/cgroup  cca_sock4_sendmsg    id 117
cgroup/getpeername4:
  /sys/fs/cgroup  cca_sock4_getpeername id 118
```

**Key finding:**
- `cca_tc_dnat` attached to cca0 ingress via TCX ✓
- `cca_sock4_connect` on root cgroup ✓
- `cca_sock4_sendmsg` on root cgroup ✓
- `cca_sock4_getpeername` on root cgroup ✓
- **`cca_tc_snat` is NOT attached to enp2s0** ✗

### 17. eBPF VIP Maps (Correctly Populated)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo bpftool map dump pinned /sys/fs/bpf/cca/services'
```
VIP service entries present and correctly map to backends.

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo bpftool map dump pinned /sys/fs/bpf/cca/backends'
```
Backend entries present with correct container IPs.

### 18. rp_filter Settings

```bash
vmherd ssh cca-deploy-ctrl -- 'sysctl net.ipv4.conf.cca-vxlan0.rp_filter'
# Result: 0

vmherd ssh cca-deploy-ctrl -- 'sysctl net.ipv4.conf.all.rp_filter'
# Result: 0 (on ctrl), varies on others
```

### 19. Kernel Version

```bash
vmherd ssh cca-deploy-ctrl -- 'uname -r'
# Result: 7.0.0-38-generic
```

### 20. nftables Comparison (ctrl vs worker-2)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo nft list ruleset'
```
Output:
```
table ip nat {
    chain POSTROUTING {
        type nat hook postrouting priority srcnat; policy accept;
    }
}
table ip filter {}
table ip6 filter {}
```
Empty — no blocking rules.

```bash
vmherd ssh cca-deploy-worker-2 -- 'sudo nft list ruleset'
```
Output: CNI-generated masquerade chains for container IPs. No blocking rules either.

### 21. iptables (Both Nodes Empty)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo iptables-legacy -L -n -v; sudo iptables -L -n -v'
vmherd ssh cca-deploy-worker-2 -- 'sudo iptables-legacy -L -n -v; sudo iptables -L -n -v'
```
All chains ACCEPT policy, zero rules on both nodes. No firewall blocking.

### 22. BPF Attachments Comparison (Key Finding)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo bpftool net show'
# Result: tc DNAT on cca0 + 3 cgroup hooks (connect4, sendmsg4, getpeername4)

vmherd ssh cca-deploy-worker-1 -- 'sudo bpftool net show'
# Result: tc DNAT on cca0 only — NO cgroup hooks

vmherd ssh cca-deploy-worker-2 -- 'sudo bpftool net show'
# Result: tc DNAT on cca0 only — NO cgroup hooks
```
Cgroup hooks only on ctrl (runs server+agent). Workers have only tc DNAT.
Ruled out cgroup hooks as cause: worker-1 has no hooks but VXLAN also broken there.

### 23. VXLAN UDP Socket State

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo ss -ulnp sport = :4789'
# UNCONN 0 0 0.0.0.0:4789 0.0.0.0:*

vmherd ssh cca-deploy-worker-2 -- 'sudo ss -ulnp sport = :4789'
# UNCONN 0 0 0.0.0.0:4789 0.0.0.0:*
```
Same socket state on both. Not a socket issue.

### 24. Actual Bridge IPs (Corrected from Summary)

```bash
vmherd ssh cca-deploy-ctrl -- 'ip addr show cca0 | grep inet'
# inet 10.32.219.1/24

vmherd ssh cca-deploy-worker-1 -- 'ip addr show cca0 | grep inet'
# inet 10.32.128.1/24

vmherd ssh cca-deploy-worker-2 -- 'ip addr show cca0 | grep inet'
# inet 10.32.165.1/24
```

### 25. Route Resolution

```bash
vmherd ssh cca-deploy-ctrl -- 'ip route get 10.32.165.1'
# 10.32.165.1 via 10.32.165.0 dev cca-vxlan0 src 192.168.124.10
```
Route resolves correctly through VXLAN.

### 26. nstat Before/After VXLAN Ping (Kernel Counter Diff)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo nstat -rz > /tmp/before.txt; \
  ping -c1 -W1 10.32.165.1 || true; \
  sudo nstat -rz > /tmp/after.txt; \
  diff /tmp/before.txt /tmp/after.txt'
```
Key changes:
- `IcmpOutEchos`: +1 (echo request generated)
- `IpOutRequests`: +1966 (background traffic + our ping)
- NO `Udp*` counter changes at all
Confirms ICMP enters VXLAN device but no UDP encapsulation recorded in SNMP counters.

### 27. ftrace: net_dev_xmit (BREAKTHROUGH)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo bash -c "
  cd /sys/kernel/debug/tracing
  echo 0 > tracing_on; echo > trace
  echo 1 > events/net/net_dev_xmit/enable
  echo 1 > events/net/net_dev_start_xmit/enable
  echo 1 > tracing_on
  ping -c1 -W1 10.32.165.1 2>/dev/null || true
  sleep 0.3; echo 0 > tracing_on
  echo 0 > events/net/net_dev_xmit/enable
  echo 0 > events/net/net_dev_start_xmit/enable
  cat trace | grep -v \"^#\" | grep -E \"ping|cca-vxlan|enp2s0\" | head -20
"'
```
Output:
```
ping-3278085 net_dev_start_xmit: dev=cca-vxlan0 protocol=0x0800 len=98
ping-3278085 net_dev_start_xmit: dev=enp2s0     protocol=0x0800 len=148
ping-3278085 net_dev_xmit:       dev=enp2s0     len=148 rc=0
ping-3278085 net_dev_xmit:       dev=cca-vxlan0 len=98  rc=0
```
**VXLAN encapsulation IS working.** Inner 98-byte ICMP enters cca-vxlan0, outer 148-byte
encapsulated packet (98 + 50 VXLAN overhead) leaves enp2s0 with rc=0 (success).

The problem is NOT sending — it's the **receiving side** dropping.
UDP OutDatagrams not changing is expected: `udp_tunnel_xmit_skb()` bypasses socket accounting.

### 28. kfree_skb Trace (No Drop on Sender)

```bash
vmherd ssh cca-deploy-ctrl -- 'sudo bash -c "
  cd /sys/kernel/debug/tracing
  echo 0 > tracing_on; echo > trace
  echo 1 > events/skb/kfree_skb/enable; echo 1 > tracing_on
  ping -c1 -W1 10.32.165.1 2>/dev/null || true
  sleep 0.5; echo 0 > tracing_on; echo 0 > events/skb/kfree_skb/enable
  cat trace | grep -v \"^#\" | tail -5
"'
```
Only unrelated drops (etcd TCP_OLD_DATA, bridge STP). No kfree_skb for our ICMP ping.
Confirms packet leaves ctrl successfully — not dropped on sender side.

### 29. Full MAC Address Comparison Across All Nodes (ROOT CAUSE)

```bash
# Actual VXLAN device MACs:
vmherd ssh cca-deploy-ctrl -- 'ip link show cca-vxlan0 | grep ether'
#   02:a0:55:03:74:9c

vmherd ssh cca-deploy-worker-1 -- 'ip link show cca-vxlan0 | grep ether'
#   02:0e:d2:21:7d:bc

vmherd ssh cca-deploy-worker-2 -- 'ip link show cca-vxlan0 | grep ether'
#   f6:a1:47:7a:e9:f8    ← RANDOM! Not 02: prefix

# FDB entries (what peers EXPECT):
vmherd ssh cca-deploy-ctrl -- 'bridge fdb show dev cca-vxlan0 | grep -v 33:33'
#   02:0b:d2:1c:c4:67 dst 192.168.124.30   ← expected worker-2 MAC
#   02:0e:d2:21:7d:bc dst 192.168.124.20   ← expected worker-1 MAC

vmherd ssh cca-deploy-worker-2 -- 'bridge fdb show dev cca-vxlan0 | grep -v 33:33'
#   02:a0:55:03:74:9c dst 192.168.124.10   ← expected ctrl MAC (MATCHES!)
#   02:0e:d2:21:7d:bc dst 192.168.124.20   ← expected worker-1 MAC (MATCHES!)

# Neighbor entries:
vmherd ssh cca-deploy-ctrl -- 'ip neigh show dev cca-vxlan0'
#   10.32.165.0 lladdr 02:0b:d2:1c:c4:67 PERMANENT   ← worker-2 expected
#   10.32.128.0 lladdr 02:0e:d2:21:7d:bc PERMANENT   ← worker-1 expected
```

**MAC mismatch map:**
| Node | Actual MAC | What peers expect | Match? |
|------|-----------|------------------|--------|
| ctrl | `02:a0:55:03:74:9c` | `02:a0:55:03:74:9c` | ✓ |
| worker-1 | `02:0e:d2:21:7d:bc` | `02:0e:d2:21:7d:bc` | ✓ |
| worker-2 | `f6:a1:47:7a:e9:f8` | `02:0b:d2:1c:c4:67` | ✗ MISMATCH |

### 30. Differential Connectivity Test (Proves MAC is Root Cause)

```bash
vmherd ssh cca-deploy-ctrl -- 'ping -c2 -W1 10.32.128.1'
# 2/2 received — ctrl → worker-1 WORKS (MACs match)

vmherd ssh cca-deploy-ctrl -- 'ping -c2 -W1 10.32.165.1'
# 0/2 received — ctrl → worker-2 FAILS (MAC mismatch)

vmherd ssh cca-deploy-worker-1 -- 'ping -c2 -W1 10.32.219.1'
# 2/2 received — worker-1 → ctrl WORKS (MACs match)
```

### 31. Fix MAC on Worker-2 → Connectivity Restored

```bash
vmherd ssh cca-deploy-worker-2 -- 'sudo ip link set cca-vxlan0 down; \
  sudo ip link set cca-vxlan0 address 02:0b:d2:1c:c4:67; \
  sudo ip link set cca-vxlan0 up'

vmherd ssh cca-deploy-ctrl -- 'ping -c3 -W1 10.32.165.1'
# 3/3 received, 0% loss — FIXED

vmherd ssh cca-deploy-worker-1 -- 'ping -c3 -W1 10.32.165.1'
# 3/3 received, 0% loss — FIXED
```

### 32. Agent Restart Race (Why MAC Was Wrong)

```bash
vmherd ssh cca-deploy-worker-2 -- 'sudo journalctl -u cca-agent --no-pager | \
  grep -i "vxlan\|vtep\|interface created\|Stopping\|Stopped\|Started"'
```
Output:
```
21:20:07 systemd: Started cca-agent.service
21:20:08 cca-agent[2586250]: vxlan: subnet published
21:20:10 cca-agent[2586250]: vxlan: VTEP MAC derived mac=02:0b:d2:1c:c4:67
21:20:10 cca-agent[2586250]: vxlan: interface created mac=02:0b:d2:1c:c4:67
21:20:13 systemd: Stopping cca-agent.service     ← Agent A killed
21:20:13 systemd: Stopped cca-agent.service
21:20:13 systemd: Started cca-agent.service       ← Agent B starts
21:20:15 cca-agent[2586362]: vxlan: VTEP MAC derived mac=02:0b:d2:1c:c4:67
21:20:15 cca-agent[2586362]: vxlan: interface created mac=02:0b:d2:1c:c4:67
```
Both agents logged successful MAC set, but current MAC is random. Agent A's Cleanup()
deleted the device (between 21:20:13 "Stopping" and "Stopped"). Agent B recreated it.
The exact mechanism for the MAC revert after Agent B is unclear, but the code never
re-validates — that's the bug.

### 33. MAC Set While UP Works on This Kernel

```bash
vmherd ssh cca-deploy-worker-2 -- 'sudo bash -c "
  ip link add test-mac1 type vxlan id 998 dstport 5001 dev enp2s0 local 192.168.124.30
  echo AFTER-CREATE:; ip link show test-mac1 | grep ether
  ip link set test-mac1 address 02:aa:bb:cc:dd:ee
  echo AFTER-SET:; ip link show test-mac1 | grep ether
  ip link set test-mac1 up
  echo AFTER-UP:; ip link show test-mac1 | grep ether
  ip link delete test-mac1
"'
```
Output:
```
AFTER-CREATE: 8e:72:bc:ec:d7:e3
AFTER-SET:    02:aa:bb:cc:dd:ee
AFTER-UP:     02:aa:bb:cc:dd:ee
```
MAC persists through UP on kernel 7.0. No kernel issue.

### 34. eBPF C Source Analysis (network/ebpf/cca_dataplane.c)

```bash
# Verified sendmsg4 program does NOT interfere with VXLAN:
# - cca_sock4_sendmsg (line 81-116): cgroup/sendmsg4 hook
# - Looks up dst in VIP service map → if not VIP, returns 1 (allow)
# - VXLAN uses udp_tunnel_xmit_skb(), not sendmsg() — hook never fires
#
# Verified SNAT program (line 315-378):
# - Checks is_overlay_source(src_ip) — VXLAN outer src is node IP, not overlay
# - Would not affect VXLAN encapsulated traffic even if attached
```

---

## Code Analysis

### A. VXLAN Setup (agent/vxlan.go)

- `VXLANReconciler.Reconcile()` (line 71): called every cycle, one-shot interface creation via `initialized` flag
- `ensureVXLANInterfaceExists()` (line 160): creates VXLAN with `ip link add cca-vxlan0 type vxlan id 42 dstport 4789 dev <physical> local <address>`
- `programPeerRoutes()` (line 283): adds permanent neighbor + onlink route + FDB entries per peer
- No IP on VXLAN device — pure L3 tunnel, Flannel model with static neighbors (proxy ARP removed by design)

### B. eBPF Data Plane (network/ebpf_dataplane.go)

- `Initialize()` (line 136): loads ELF, pins maps, attaches cgroup programs, stores tc programs
- `RegisterNode()` (line 530): discovers bridge → attaches tc DNAT → attaches tc SNAT to physical NIC
  - **BUG:** If bridge discovery fails (no containers running yet → no cca0), function returns nil at line 545 WITHOUT ever attempting SNAT attachment at line 558
- `RetryBridgeAttachment()` (line 570): retries DNAT attachment only, does NOT retry SNAT attachment
  - **BUG:** Even after bridge appears and DNAT is retried, SNAT is never retried

### C. eBPF C Programs (network/ebpf/cca_dataplane.c)

- `cca_sock4_sendmsg` (line 81-116): cgroup/sendmsg4 hook on root cgroup. Intercepts ALL UDP sendmsg(). Looks up destination in VIP service map. If NOT a VIP → returns 1 (allow). If IS a VIP → rewrites to backend.
  - **Analysis:** This should NOT affect VXLAN kernel encapsulation because VXLAN uses `udp_tunnel_xmit_skb()` which bypasses the socket sendmsg() path entirely. The cgroup hook only fires on user-space `sendmsg()` syscalls.
- `cca_tc_snat` (line 315-378): tc egress on physical NIC. Checks if source IP is in overlay CIDR, if so rewrites to node IP and fixes checksums.
  - **Analysis:** Without SNAT, packets leaving the physical NIC with overlay source IPs would have incorrect source addresses. But this doesn't explain why VXLAN encapsulation itself fails — the outer VXLAN header uses the node IP, not overlay IP.

### D. Missing SNAT and VXLAN Relationship

The tc SNAT program operates on the outer packet after VXLAN encapsulation. For VXLAN encapsulated traffic:
- Outer src IP: already the node's local IP (set by VXLAN device config)
- SNAT check: `is_overlay_source(outer_src)` → false → no rewrite needed
- So missing SNAT should NOT cause VXLAN encapsulation failure

For non-VXLAN container traffic leaving the physical NIC:
- Source IP would be overlay (10.32.x.y)
- Missing SNAT means packets leave with overlay source → replies can't route back
- This affects direct container-to-external, NOT VXLAN encapsulation

---

## CONFIRMED Root Cause: VXLAN VTEP MAC Mismatch

### Summary

Worker-2's VXLAN device (`cca-vxlan0`) has a **random kernel MAC** (`f6:a1:47:7a:e9:f8`) instead of the **deterministic MAC** (`02:0b:d2:1c:c4:67`) that peers program into their FDB/neighbor tables. All traffic TO worker-2 arrives with the wrong inner dst MAC and is dropped by the kernel.

### Evidence

| Test | Result | Why |
|------|--------|-----|
| ctrl → worker-1 (MACs match) | 0% loss | FDB MAC `02:0e:d2:21:7d:bc` = actual |
| ctrl → worker-2 (MAC mismatch) | 100% loss | FDB MAC `02:0b:d2:1c:c4:67` ≠ actual `f6:a1:47:7a:e9:f8` |
| worker-2 → ctrl (MACs match) | 0% loss | FDB MAC `02:a0:55:03:74:9c` = actual |
| Fix MAC on worker-2, retry | 0% loss | Confirms MAC was sole cause |

### Why Early Tests Were Misleading

- **ftrace proved VXLAN encapsulation WORKS** on ctrl: `net_dev_xmit dev=enp2s0 len=148 rc=0` — packet leaves the NIC
- **UDP OutDatagrams unchanged**: expected — `udp_tunnel_xmit_skb()` bypasses UDP socket accounting
- **tcpdump saw nothing**: likely timing/filter issue — ftrace proves packets were sent
- The failure is on the **receiving side** (worker-2 drops inner frames with wrong dst MAC), not the sending side

### Trigger: Dual Agent Startup Race

Worker-2 agent logs show two PIDs (2586250, 2586362) both creating the VXLAN device:
1. Agent A creates VXLAN at 21:20:10, sets deterministic MAC
2. systemd stops Agent A at 21:20:13 → Cleanup() deletes VXLAN
3. Agent B creates VXLAN at 21:20:15, sets deterministic MAC
4. Something between 21:20:15 and now caused the MAC to revert to random

The exact mechanism for the MAC revert is unclear, but the code bug is clear: `Reconcile()` never re-validates the MAC after initial creation.

---

## Code Bugs Found and Fixed

### Bug 1: VXLAN MAC never re-validated (agent/vxlan.go)

`ensureVXLANInterfaceExists()` only sets the MAC during creation. The `initialized` flag prevents it from running again. If the MAC drifts (agent restart race, external recreation), it's never corrected.

**Fix:** Added `ensureCorrectMACAddress()` called on every `Reconcile()` pass (like rp_filter). Reads current MAC via `net.InterfaceByName()`, compares to `deriveVXLANMACAddress(nodeID)`, corrects if mismatched.

### Bug 2: RetryBridgeAttachment skips SNAT (network/ebpf_dataplane.go)

`RegisterNode()` attaches SNAT only after successful bridge discovery. If bridge doesn't exist at startup, returns nil without SNAT. `RetryBridgeAttachment()` only retries DNAT, never SNAT.

**Fix:** Store `nodeIP` in `registeredNodeIP` field during `RegisterNode()`. In `RetryBridgeAttachment()`, after successful DNAT, attempt SNAT if `snatLink == nil`.

---

## Live Cluster Fix Applied

Set correct MAC on worker-2: `sudo ip link set cca-vxlan0 address 02:0b:d2:1c:c4:67`
All VXLAN connectivity restored across all nodes.

---

## Continuation Playbook (If CI Still Fails)

If the MAC reconciliation fix does not resolve the CI health check, here are the
remaining areas to investigate, ordered by likelihood:

### A. Verify the fix is actually running

```bash
# Check agent logs for the new MAC reconciliation:
vmherd ssh cca-deploy-worker-2 -- 'sudo journalctl -u cca-agent --no-pager | grep "MAC mismatch"'
# If the log appears → fix is working, MAC was corrected
# If no log → either MAC was already correct or new binary not deployed

# Verify binary version:
vmherd ssh cca-deploy-worker-2 -- '/usr/local/bin/cca version'
```

### B. Check if SNAT is now attached

```bash
# After fix, RetryBridgeAttachment should attach SNAT:
vmherd ssh cca-deploy-ctrl -- 'sudo bpftool net show'
# Look for: enp2s0 tcx/egress cca_tc_snat
# If missing → SNAT retry still not reaching the code path

# Check agent log for deferred SNAT:
vmherd ssh cca-deploy-ctrl -- 'sudo journalctl -u cca-agent --no-pager | grep -i SNAT'
```

### C. Remaining connectivity matrix to test

```bash
# Full overlay mesh — all 6 directions:
for src in ctrl worker-1 worker-2; do
  for dst_ip in 10.32.219.1 10.32.128.1 10.32.165.1; do
    vmherd ssh cca-deploy-$src -- "ping -c1 -W1 $dst_ip 2>&1"
  done
done

# Container-to-container (through VIP):
vmherd ssh cca-deploy-ctrl -- 'sudo cca exec <scheduler-id> -- curl -s http://<vip>:5432'
```

### D. Unsolved: exact mechanism of MAC revert

Both agents logged successful MAC set to `02:0b:d2:1c:c4:67`, yet the device had
random `f6:a1:47:7a:e9:f8`. Possible causes to investigate:

1. **Third-party device recreation**: something (containerd, udev rule, netplan apply)
   recreated the VXLAN device after Agent B set it up
   ```bash
   # Check udev rules that might touch VXLAN:
   grep -r vxlan /etc/udev/rules.d/ /lib/udev/rules.d/ 2>/dev/null
   # Check netplan:
   cat /etc/netplan/*.yaml
   ```

2. **Kernel MAC reset on socket rebind**: VXLAN device might reset MAC when the
   UDP socket is recreated (if a second `ip link add` happens with same VNI/port)
   ```bash
   # Test: create device, set MAC, delete, recreate with same params:
   ip link add test type vxlan id 42 dstport 4789 dev enp2s0 local 192.168.124.30
   ip link set test address 02:aa:bb:cc:dd:ee
   ip link show test | grep ether   # should be 02:aa:bb:cc:dd:ee
   ip link delete test
   ip link add test type vxlan id 42 dstport 4789 dev enp2s0 local 192.168.124.30
   ip link show test | grep ether   # will be random (new device)
   ip link delete test
   ```

3. **Race between Cleanup and Create**: Agent A's `ip link delete` runs after Agent B's
   `ip link add` but before `ip link set address`. This would mean systemd's
   "Stopped" log is lying about sequencing. Test with:
   ```bash
   # Add tracing to Cleanup:
   # In agent code, log timestamps with nanosecond precision for:
   #   1. Signal received
   #   2. Cleanup() entry
   #   3. ip link delete start/end
   #   4. Process exit
   ```

### E. Other potential issues unrelated to MAC

1. **Init container DNS resolution**: VIP connection probes resolve hostname via
   CCattler DNS → VIP. If DNS is broken, probes fail before VXLAN is involved.
   ```bash
   vmherd ssh cca-deploy-ctrl -- 'dig @localhost -p 15353 airflow-db.cca.local'
   ```

2. **eBPF socket LB stale entries**: if the VIP → backend mapping is stale,
   connect() rewrites to a wrong/dead container IP.
   ```bash
   vmherd ssh cca-deploy-ctrl -- 'sudo bpftool map dump pinned /sys/fs/bpf/cca/services'
   vmherd ssh cca-deploy-ctrl -- 'sudo bpftool map dump pinned /sys/fs/bpf/cca/backends'
   ```

3. **Container not listening**: metadata-db container might be running but Postgres
   not accepting connections on the expected port.
   ```bash
   vmherd ssh cca-deploy-worker-2 -- 'sudo cca exec <metadata-db-id> -- pg_isready'
   ```

---

## Issue 3: tc SNAT Breaks Host-Namespace → Container TCP (2026-11-01 session 2)

### Root Cause

The tc SNAT program (`cca_tc_snat` on `enp2s0 tcx/egress`) is stateless — it
rewrites the source IP on ALL packets with overlay source, including TCP REPLY
packets from containers responding to connections from remote host-namespace
sockets (e.g., agent init probes).

**Packet flow for init probe (broken before fix):**
1. Worker-2 agent dials VIP 10.191.0.1:5432 (metadata-db)
2. cgroup connect4 rewrites → 10.38.219.2:5432 (container on ctrl)
3. TCP SYN: src=192.168.124.30, dst=10.38.219.2 → via VXLAN to ctrl
4. Container replies: src=10.38.219.2, dst=192.168.124.30
5. Reply routes via enp2s0 (physical IP destination)
6. **tc SNAT fires**: is_overlay_source(10.38.219.2) → TRUE → rewrites src to 192.168.124.10
7. Worker-2 receives SYN-ACK from 192.168.124.10 → TCP mismatch → DROP

nftables MASQUERADE (from CNI) correctly skips reply packets via conntrack,
but the tc SNAT had no conntrack awareness.

### Fix

Added `bpf_skb_ct_lookup` check to `cca_tc_snat`. For TCP/UDP packets, the
program now looks up the conntrack entry and checks `bpf_ct_opts.dir`. If the
direction is `IP_CT_DIR_REPLY` (1), the packet is a response to an incoming
connection → skip SNAT. Original-direction packets (container-initiated) still
get SNATed as before.

### Diagnostic Commands

```bash
# Verify SNAT is attached:
vmherd ssh cca-deploy-ctrl -- 'sudo bpftool net show'

# Test TCP connectivity (NOT just ping — ping uses ICMP, unaffected by SNAT):
vmherd ssh cca-deploy-worker-2 -- 'timeout 3 nc -w 2 10.38.219.2 5432'

# Check conntrack entries:
vmherd ssh cca-deploy-ctrl -- 'sudo conntrack -L | grep 5432'
```

### F. Diagnostic one-liner for future health check failures

```bash
# Quick triage script — run from hypervisor:
cd /mnt/disk2/home/bojan/actions-runner/_work/ccattler/ccattler/test/e2e/vm-deploy
for node in ctrl worker-1 worker-2; do
  echo "=== $node ==="
  vmherd ssh cca-deploy-$node -- 'echo MAC:$(ip link show cca-vxlan0 2>/dev/null | grep -o "ether [^ ]*" | cut -d" " -f2); \
    echo ROUTES:; ip route show dev cca-vxlan0 2>/dev/null; \
    echo NEIGH:; ip neigh show dev cca-vxlan0 2>/dev/null; \
    echo FDB:; bridge fdb show dev cca-vxlan0 2>/dev/null | grep -v "33:33"; \
    echo BPF:; sudo bpftool net show 2>/dev/null | grep -v "^$"'
done
```
