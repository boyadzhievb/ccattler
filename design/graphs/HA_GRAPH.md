# CCattler High-Availability Architecture Graph

Machine-readable reference for the HA control plane: etcd cluster, leader
election, controller partitioning, and failover behavior. Purpose: understand
multi-node control-plane operations without reading source files.

## Cluster Topology

```
┌──────────────────────────────────────────────────────────────────────────┐
│                        HA CCattler Cluster                               │
│                                                                          │
│  ┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐            │
│  │   node-1         │ │   node-2         │ │   node-3         │            │
│  │   (controlplane) │ │   (controlplane) │ │   (controlplane) │            │
│  │   192.168.100.215│ │   192.168.100.30 │ │   192.168.100.31 │            │
│  │                  │ │                  │ │                  │            │
│  │  ┌────────────┐  │ │  ┌────────────┐  │ │  ┌────────────┐  │            │
│  │  │ etcd       │  │ │  │ etcd       │  │ │  │ etcd       │  │            │
│  │  │ :2379/:2380│  │ │  │ :2379/:2380│  │ │  │ :2379/:2380│  │            │
│  │  └────────────┘  │ │  └────────────┘  │ │  └────────────┘  │            │
│  │  ┌────────────┐  │ │  ┌────────────┐  │ │  ┌────────────┐  │            │
│  │  │ cca-server │  │ │  │ cca-server │  │ │  │ cca-server │  │            │
│  │  │ :9770 API  │  │ │  │ :9770 API  │  │ │  │ :9770 API  │  │            │
│  │  │ HARunner   │  │ │  │ HARunner   │  │ │  │ HARunner   │  │            │
│  │  │ [LEADER]   │  │ │  │ [STANDBY]  │  │ │  │ [STANDBY]  │  │            │
│  │  └────────────┘  │ │  └────────────┘  │ │  └────────────┘  │            │
│  └─────────────────┘ └─────────────────┘ └─────────────────┘            │
│                                                                          │
│  ┌─────────────────┐                                                     │
│  │   node-4         │                                                     │
│  │   (worker)       │                                                     │
│  │   192.168.100.51 │                                                     │
│  │                  │                                                     │
│  │  ┌────────────┐  │                                                     │
│  │  │ cca-agent  │  │                                                     │
│  │  │ containerd │  │                                                     │
│  │  └────────────┘  │                                                     │
│  └─────────────────┘                                                     │
└──────────────────────────────────────────────────────────────────────────┘
```

### Node Roles

| Role | Count | Components | Registers as Node? |
|------|-------|------------|--------------------|
| controlplane | 3 | etcd + cca-server (HARunner) | No |
| worker | N | cca-agent + containerd | Yes |

Workers register as nodes in the fact store. Control-plane nodes do NOT run
`cca agent` and do NOT appear in `observed/node/` facts.

## etcd Cluster Configuration

### Endpoints and Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 2379 | HTTPS | Client API (cca-server → etcd) |
| 2380 | HTTPS | Peer communication (etcd ↔ etcd) |

### Initial Cluster Formation

```
etcd-cluster role (deploy/ansible/roles/etcd-cluster/tasks/main.yml):
  1. Build initial-cluster string from inventory:
     node-1=https://192.168.100.215:2380,
     node-2=https://192.168.100.30:2380,
     node-3=https://192.168.100.31:2380
  2. Stop etcd, wipe /var/lib/etcd (fresh bootstrap)
  3. Install systemd drop-in with cluster config
  4. Start etcd, wait for endpoint health
  5. Verify quorum via etcdctl member list
```

### etcd TLS

All etcd communication uses mTLS with the same PKI as cca-server:

```
/etc/ccattler/pki/ca.pem        — cluster CA certificate
/etc/ccattler/pki/node.pem      — per-node certificate (used for both server & peer)
/etc/ccattler/pki/node-key.pem  — per-node private key
```

### EtcdStore Connection (store/etcd.go)

```
EtcdStoreConfig:
  Endpoints     []string      ← comma-split from --endpoints or /etc/ccattler/endpoints
  KeyPrefix     string        ← --store-prefix (namespace within shared etcd)
  DialTimeout   5s (default)  ← defaultEtcdDialTimeout (store/etcd.go:23)
  Username      string        ← optional etcd auth
  Password      string        ← optional etcd auth
  TLSConfig     *tls.Config   ← from --etcd-cert/--etcd-key/--etcd-ca

Startup sequence (NewEtcdStore, store/etcd.go:67):
  1. clientv3.New(config) — connect to etcd cluster
  2. Status(endpoints[0]) — verify connectivity within DialTimeout
  3. Return EtcdStore with write rate limiter
```

## Leader Election

### Architecture

Leader election is implemented at the CCattler application layer using
compare-and-swap (CAS) transactions on etcd keys — NOT etcd's native
election API. This gives full control over lease semantics.

### Store Keys

| Key | Type | Purpose |
|-----|------|---------|
| `leader/controlplane` | Lease timestamp (Unix millis) | When the lease was last renewed |
| `leader/controlplane/holder` | Node ID string | Which node holds the lease |

### Constants (controllers/leader.go:54-68)

| Constant | Default | Description |
|----------|---------|-------------|
| `defaultLeaderLeaseDuration` | 15s | Lease validity window |
| `defaultLeaderLeaseRenewInterval` | 5s | Renewal tick frequency |
| `stopControllersTimeout` | 10s | Max wait for controller shutdown on leadership loss |

### Election State Machine

```
                    ┌─────────────────────────────────────┐
                    │                                     │
                    ▼                                     │
              ┌──────────┐                                │
              │ STANDBY  │ ◄──── startup state            │
              │          │                                │
              └────┬─────┘                                │
                   │                                      │
                   │ tryAcquireOrRenew() every 5s         │
                   │                                      │
          ┌────────┴────────────┐                         │
          │                     │                         │
     No lease exists     Lease exists but expired         │
          │                     │                         │
          ▼                     ▼                         │
   CAS: revision==0     CAS: revision==lease.rev         │
   Put lease + holder   Put lease + holder                │
          │                     │                         │
     ┌────┴────┐          ┌────┴────┐                     │
     │ CAS ok? │          │ CAS ok? │                     │
     └────┬────┘          └────┬────┘                     │
      yes │ no             yes │ no                       │
          │  │                 │  │                        │
          ▼  └─(retry)─┐      ▼  └─(retry)─┐             │
     ┌──────────┐       │ ┌──────────┐      │             │
     │ LEADER   │       │ │ LEADER   │      │             │
     │          │       │ │          │      │             │
     └────┬─────┘       │ └────┬─────┘      │             │
          │             │      │            │             │
          │ renewLeaseWithCAS() every 5s    │             │
          │                                 │             │
     ┌────┴─────────────────────────────┐   │             │
     │                                  │   │             │
  Holder still == nodeID           Holder changed         │
     │                                  │                 │
     ▼                                  ▼                 │
  CAS: Put new timestamp        relinquishLeadership()   │
     │                                  │                 │
  success → update lastRenewal   onLost() callback ───────┘
     │
  failure (transient) → check sinceLast > leaseDuration?
     │                         │
     yes                       no
     │                         │
     ▼                         ▼
  relinquishLeadership()    retain (log warning)
```

### Acquisition Flow (controllers/leader.go:129-173)

```
tryAcquireOrRenew():
  if isLeader:
    renewLeaseWithCAS()
    return

  leaseFact = store.Get("leader/controlplane")
  if err (no lease):
    acquireLease()  — CAS with revision==0
    return

  leaseAge = now - parseTimestamp(leaseFact.Value)
  if leaseAge > leaseDuration:
    CAS transaction:
      compare: leaderLeaseKey.revision == leaseFact.Revision
      success: Put(leaseKey, now) + Put(holderKey, nodeID)
    if ok:
      isLeader = true
      onAcquired()
```

### Renewal Flow (controllers/leader.go:206-230)

```
renewLeaseWithCAS():
  holderFact = store.Get("leader/controlplane/holder")
  if err → handleTransientRenewalFailure("holder read failed")
  if holderFact.Value != nodeID → relinquishLeadership("holder changed")

  CAS transaction:
    compare: holderKey.revision == holderFact.Revision
    success: Put(leaseKey, now)
  if txnErr → handleTransientRenewalFailure("CAS transaction error")
  if !renewed → relinquishLeadership("CAS failed")
  else → lastSuccessfulRenewal = now
```

### Transient Failure Tolerance (controllers/leader.go:235-245)

```
handleTransientRenewalFailure(reason):
  sinceLast = time.Since(lastSuccessfulRenewal)
  if sinceLast > leaseDuration:
    relinquishLeadership(reason + exceeded lease)
  else:
    log WARN, retain leadership
```

A single renewal failure does NOT immediately lose leadership. The leader
tolerates transient etcd timeouts for up to `leaseDuration` (15s) since
the last successful renewal.

### Graceful Release (controllers/leader.go:259-289)

```
release() — called on context cancellation (Ctrl+C / SIGTERM):
  holderFact = store.Get("leader/controlplane/holder")
  if err or holderFact.Value != nodeID:
    isLeader = false  (skip delete — not our lease)
    return

  CAS transaction:
    compare: holderKey.revision == holderFact.Revision
    success: Delete(leaseKey) + Delete(holderKey)
  isLeader = false
  onLost()
```

CAS on release prevents accidentally deleting another node's valid lease
after a concurrent takeover.

## Controller Partitioning

### Leader-Only Controllers (controllers/ha_runner.go)

All reconciliation controllers run only on the elected leader via HARunner:

```
coreControllers() — always registered:
  1.  instance               (controllers/instance.go)
  2.  scheduler              (scheduler/scheduler.go)
  3.  endpoint               (controllers/endpoint.go)
  4.  failure                (controllers/failure.go)
  5.  autoscale              (controllers/autoscale.go)
  6.  intent-resolver        (controllers/intent.go)
  7.  rollout                (controllers/rollout.go)
  8.  init                   (controllers/init.go)
  9.  stateful-dns           (controllers/stateful_dns.go)
  10. stateful-volume        (controllers/stateful_volume.go)
  11. route                  (controllers/route.go)
  12. eviction               (controllers/eviction.go)
  13. job                    (controllers/job.go)
  14. cron                   (controllers/cronjob.go)
  15. instance-gc            (controllers/instance_gc.go)

startControllersWithLeaderElection() adds:
  16. node-failure           (controllers/nodefailure.go)
  17. drain                  (controllers/drain.go)
  18. network                (controllers/network.go)
  19. network-policy         (controllers/network_policy.go)
  20. warm-zero              (controllers/warmzero.go)

If --cloud-provider set:
  21. cloud-node-lifecycle   (controllers/cloud_node_lifecycle.go)
  22. cloud-loadbalancer     (controllers/cloud_loadbalancer.go)
  23. cloud-routes           (controllers/cloud_routes.go)
```

### Always-Running Components (all control-plane nodes)

```
On EVERY control-plane node, regardless of leader status:
  - API server (HTTP :9770)     ← serves /api/*, /status, /healthz, /metrics
  - AuthController              ← rebuilds in-memory RBAC from store
  - MetricsCollector            ← Prometheus metrics export
  - EventProjector              ← watch-based event log (via HARunner.SetEventLog)
  - DNS server (if --dns)       ← resolves service names from store
```

### Split: --api-only vs --controllers-only

```
cca server --api-only:
  ✓ API server + Auth + Metrics
  ✗ HARunner (no leader election, no controllers)

cca server --controllers-only:
  ✗ API server
  ✓ HARunner (leader election + all controllers)

cca server (default, full mode):
  ✓ API server + Auth + Metrics
  ✓ HARunner (leader election + all controllers)
```

## HARunner (controllers/ha_runner.go)

### Lifecycle

```
NewHARunner(factStore, nodeID, metrics, controllers...):
  Creates LeaderElection with callbacks:
    OnAcquired → haRunner.startControllers
    OnLost     → haRunner.stopControllers

Run(ctx):
  awaitPendingOperation()  — wait for any in-flight start/stop
  election.Run(ctx)        — blocks until ctx cancelled

startControllers() — called by OnAcquired callback:
  mutex.Lock()
  wrap each controller with metricsWrappedController
  create new Runner(factStore, wrappedControllers...)
  if eventLog != nil → runner.SetEventLog(eventLog)
  launch runner.Run(ctx) in goroutine
  log "controllers started (leader)"

stopControllers() — called by OnLost callback:
  mutex.Lock()
  cancelRunner()
  wait for runnerDone (max 10s = stopControllersTimeout)
  if timeout → log ERROR "runner shutdown timed out, forcing stop"
  clear runner, cancelRunner, runnerDone
```

### Difference from Regular Runner

| Aspect | Runner | HARunner |
|--------|--------|----------|
| Leader election | None | LeaderElection wrapper |
| When controllers run | Always | Only when leader |
| Failover | N/A | Automatic on lease expiry |
| Metrics | Optional | Always (metricsWrappedController) |
| Event log | Optional | Forwarded from SetEventLog |
| Stop behavior | Context cancel | 10s timeout + force |

## Failover Timeline

```
T=0s    node-1 is LEADER, renewing lease every 5s
T=0s    node-2, node-3 are STANDBY, checking lease every 5s

T=5s    node-1 crashes (process killed, network partition, etc.)
        Last successful renewal was at T=0s
        Lease timestamp in etcd: T=0s

T=5s    node-2 checks lease: age = 5s < 15s → still valid → stays STANDBY
T=10s   node-2 checks lease: age = 10s < 15s → still valid → stays STANDBY
T=15s   node-2 checks lease: age = 15s == 15s → EXPIRED
        node-2 CAS: compare lease.revision → Put new timestamp + holder=node-2
        CAS succeeds → node-2 becomes LEADER
        OnAcquired → startControllers()
        Controllers resume from current store state (stateless)

T=15s   node-3 also checks lease at same tick
        CAS fails (node-2 already updated revision) → stays STANDBY

Total failover time: leaseDuration (15s) + up to renewInterval (5s) worst case
                   = 15–20 seconds
```

### Graceful Shutdown Failover

```
T=0s    node-1 receives SIGTERM
        release() → CAS delete lease + holder keys
        OnLost → stopControllers() (10s timeout)

T=0s    Lease keys deleted from etcd

T=5s    node-2 tries to acquire: no lease exists
        acquireLease() → CAS revision==0 → Put lease + holder
        Becomes LEADER

Total failover time: up to renewInterval (5s) — no lease expiry wait
```

## Split-Brain Prevention

### etcd Quorum

```
3-node etcd cluster: quorum = 2
  - 1 node failure: quorum maintained, cluster operational
  - 2 node failure: quorum lost, etcd read-only, no writes

Leader election uses CAS transactions on etcd:
  - CAS is linearizable (etcd guarantee)
  - Only one CAS can succeed for the same revision
  - Two nodes cannot both become leader simultaneously
```

### Defense-in-Depth

```
1. CAS transactions prevent concurrent lease acquisition
   (controllers/leader.go:154-161, 178-186)

2. Holder verification on every renewal tick
   (controllers/leader.go:207-215)

3. CAS on release prevents deleting another node's lease
   (controllers/leader.go:274-279)

4. stopControllers() blocks until runner exits before allowing re-election
   (controllers/ha_runner.go:140-155)
   Prevents overlap: old leader's controllers stop BEFORE new leader starts

5. etcd quorum guarantees linearizable reads/writes
   Network-partitioned minority cannot form a separate leader
```

## Node Join / Leave

### Control-Plane Node Join

```
1. Ansible distributes CA from first node:
   deploy/ansible/roles/ha-controlplane/tasks/main.yml:24-52

2. Generate per-node certificate:
   cca init --ca /etc/ccattler/pki/ca.pem --ca-key ca-key.pem
   --advertise-address <IP> --node-id <name>

3. Write /etc/ccattler/endpoints (all etcd endpoints)

4. Start cca-server with --node-id
   → HARunner starts leader election loop
   → Node enters STANDBY (existing leader still active)
```

### Worker Node Join

```
1. Worker role deploys cca binary + containerd
   deploy/ansible/roles/worker/tasks/main.yml

2. Worker runs:  cca join <server-address> <bootstrap-token>
   → mTLS enrollment via /api/enroll
   → Receives signed node certificate from CA
   → Starts cca-agent with --node-id

3. Agent heartbeats → observed/node/{id}/state = "alive"
   → IntentResolver re-evaluates per-node services
   → Scheduler considers new node for placement
```

### Node Leave / Failure

```
Worker failure:
  1. Agent stops heartbeating
  2. NodeFailureController detects: observed/node/{id}/state → "unreachable"
  3. After failure timeout: marks instances as node_failure
  4. FailureController creates replacements
  5. Scheduler places new instances on remaining nodes

Control-plane failure:
  1. cca-server process dies or network partition
  2. Lease renewal stops (no CAS renewal)
  3. After 15s: lease expires
  4. Standby node acquires leadership via CAS
  5. New leader starts all controllers from current store state
```

## Health and Readiness

### Endpoints

| Endpoint | Purpose | Auth Required |
|----------|---------|---------------|
| `/healthz` | Liveness probe — server process alive | No |
| `/metrics` | Prometheus metrics export | No |
| `/status` | Full cluster status (JSON or text) | Yes (mTLS or bearer) |
| `/api/get?type=nodes` | Node list via API | Yes |

### Deployment Health Checks (ha-deploy.yml:53-79)

```
After etcd cluster formation:
  1. Restart cca-server on all control-plane nodes
  2. wait_for: port 9770, timeout 45s
  3. curl --cacert/--cert/--key https://{host}:9770/healthz (retries: 10, delay: 3)
```

### Readiness Command (cmd/cca/command_readiness.go)

```
cca readiness [--json]

Runs 16 test areas including:
  - "control-plane": TestControlPlaneRestart, TestRollingControlPlaneUpgrade,
                     TestLeaderElection
  - "recovery": TestDisasterRecovery, TestSplitBrain, TestNetworkPartition
```

## Deployment Summary

### PKI Bootstrap (ha-controlplane role)

```
First control-plane node (serial: 1):
  1. cca init --advertise-address <IP> --node-id <name>
     → Generates CA (ca.pem, ca-key.pem)
     → Generates node cert (node.pem, node-key.pem)
     → Writes /etc/ccattler/pki/*
  2. Ansible fetches ca.pem + ca-key.pem to controller

Remaining control-plane nodes:
  3. Ansible distributes ca.pem + ca-key.pem
  4. cca init --ca ca.pem --ca-key ca-key.pem
     → Generates unique node cert signed by shared CA
  5. Writes /etc/ccattler/endpoints (all 3 etcd endpoints)
```

### Inventory Format (deploy/ansible/baremetal-e2e-ha-inventory.ini)

```ini
[etcd]
node-1 ansible_host=192.168.100.215
node-2 ansible_host=192.168.100.30
node-3 ansible_host=192.168.100.31

[controlplane]
node-1
node-2
node-3

[workers]
node-4 ansible_host=192.168.100.51

[ccattler:children]
controlplane
workers

[all:vars]
etcd_endpoints=https://192.168.100.215:2379,https://192.168.100.30:2379,https://192.168.100.31:2379
```

### Playbook Execution Order (deploy/ansible/ha-deploy.yml)

```
1. cleanup      (all nodes)    — remove old state
2. common       (all nodes)    — install cca binary, containerd, deps
3. ha-controlplane (controlplane, serial:1) — PKI generation + distribution
4. etcd-cluster (etcd)         — form 3-node raft cluster
5. restart      (controlplane) — restart cca-server, verify API health
6. worker       (workers)      — deploy agent + containerd
7. testapp      (controlplane[0], tags: testapp) — optional demo workload
```

## Constants and Timeouts Reference

| Constant | Value | Location | Purpose |
|----------|-------|----------|---------|
| `defaultLeaderLeaseDuration` | 15s | controllers/leader.go:64 | Lease validity window |
| `defaultLeaderLeaseRenewInterval` | 5s | controllers/leader.go:68 | Renewal tick frequency |
| `stopControllersTimeout` | 10s | controllers/ha_runner.go:18 | Max wait for controller shutdown |
| `defaultEtcdDialTimeout` | 5s | store/etcd.go:23 | etcd connection timeout |
| `defaultEtcdWatchBuffer` | 64 | store/etcd.go:28 | Watch event channel buffer |
| `defaultControllerDebounceInterval` | 50ms | controllers/runner.go:26 | Quiet period between reconcile cycles |
| `defaultControllerResyncInterval` | 30s | controllers/runner.go:30 | Forced full reconciliation period |
| `reconcileRetryBaseDelayMs` | 10ms | controllers/runner.go:34 | CAS retry base backoff |
| `reconcileRetryMaxDelayMs` | 500ms | controllers/runner.go:38 | CAS retry max backoff |
| `maxTransactionChanges` | 60 | controllers/runner.go:55 | Max changes per etcd transaction |
| etcd client port | 2379 | deploy/ansible | Client API port |
| etcd peer port | 2380 | deploy/ansible | Peer communication port |
| API server port | 9770 | cmd/cca/command_server.go:37 | HTTP(S) API port |
| `defaultCACertificateValidity` | 10 years | cmd/cca/command_server.go:42 | CA certificate lifetime |
| `defaultServerCertificateTTL` | 24h | cmd/cca/command_server.go:46 | Server cert rotation period |
| `defaultNodeCertificateTTL` | 1 year | cmd/cca/command_server.go:50 | Enrolled node cert lifetime |
| `certificateExpiryWarningThreshold` | 7 days | cmd/cca/command_server.go:828 | Startup warning threshold |

## File Reference

| File | Purpose |
|------|---------|
| `controllers/leader.go` | Leader election: lease, CAS, acquire/renew/release |
| `controllers/leader_test.go` | 6 tests: acquire, contention, failover, release, stale holder, safe release |
| `controllers/ha_runner.go` | HARunner: leader-aware controller runner |
| `controllers/runner.go` | Inner Runner: watch, debounce, reconcile loop |
| `store/etcd.go` | EtcdStore: connect, CRUD, watch, transactions, leases |
| `cmd/cca/command_server.go` | Server startup: store, auth, controllers, API |
| `cmd/cca/command_readiness.go` | Production readiness test runner (16 areas) |
| `deploy/ansible/ha-deploy.yml` | HA deployment playbook |
| `deploy/ansible/baremetal-e2e-ha-inventory.ini` | HA cluster inventory (3 cp + 1 worker) |
| `deploy/ansible/roles/ha-controlplane/tasks/main.yml` | PKI generation and distribution |
| `deploy/ansible/roles/etcd-cluster/tasks/main.yml` | etcd 3-node raft cluster formation |
| `.github/workflows/e2e-ha.yml` | CI workflow for HA e2e tests |
