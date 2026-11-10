# CCattler Test Plan

1836 tests across 19 packages. Every milestone has a "must test" headline scenario, supported by unit and integration tests.

## How to Run

```bash
go test ./...                          # all tests
go test ./integration/ -v              # integration tests only
go test ./... -run TestVolumeSurvives  # single test by name
go run ./cmd/cca/ demo-storage         # live demo with volume migration
```

## Examples

The `examples/` directory contains working `.cca` configs — one per feature area. Each can be applied with `cca apply`, and all are validated by `TestAllExamplesParseAndApply`.

| File | Features Demonstrated |
|------|----------------------|
| `basic.cca` | Single service, 3 instances, exposed port, resources |
| `multi-service.cca` | Two services, different instance counts and resources |
| `health-checks.cca` | HTTP health probe with path, TCP health probe, intervals |
| `distributed.cca` | 6 instances spread across 3 nodes, failure recovery |
| `networking.cca` | VIPs, DNS, load balancing across 2 services |
| `storage.cca` | Persistent volume, service-to-volume binding, migration |
| `web.cca` | Process runtime (sleep command as image) |
| `web-container.cca` | Container runtime with SSI hostname display |
| `autoscaling.cca` | Horizontal/vertical/event-driven scaling policies |
| `zabbix.cca` | Monitoring integration with Zabbix |
| `reference.cca` | **All DSL constructs** — annotated config reference (every block, every field, with comments) |

## Test Coverage by Milestone

### M1 — State Machine (Phases 0–4)

**Headline**: Apply config, see facts reconcile in the store.

| Category | Scenario | Status | Test |
|----------|----------|--------|------|
| **Must** | Desired service → instances created → placed on node | Covered | `TestDesiredToPlaced` |
| **Must** | Scale up creates new instances and places them | Covered | `TestScaleUpPlacesNewInstances` |
| **Must** | Scale down stops excess instances | Covered | `TestScaleDown` |
| **Must** | Failed instance → replacement created | Covered | `TestFailureReplacesFailedInstance` |
| **Must** | Endpoints created for running instances with ports | Covered | `TestEndpointCreatedForRunningInstance` |
| **Must** | Stale endpoints removed when instance stops | Covered | `TestStaleEndpointRemoved` |
| Should | Scale down prefers pending over running | Covered | `TestScaleDownPrefersPending` |
| Should | No placement without alive nodes | Covered | `TestNoAliveNodes` |
| Should | Scheduler skips unreachable nodes | Covered | `TestSkipUnreachableNodes` |
| Should | Stopped instances not counted toward desired | Covered | `TestStoppedInstancesNotCounted` |
| Should | Multiple services reconciled independently | Covered | `TestMultipleServices` |
| Could | Transaction conflict during concurrent writes | Covered | `TestTransactionConflict` |
| Could | Watch delivers events in order | Covered | `TestWatchExactKey`, `TestWatchPrefix` |

### M2 — Single Machine (Phase 5)

**Headline**: CLI → parser → store → reconciler → running container.

| Category | Scenario | Status | Test |
|----------|----------|--------|------|
| **Must** | Agent starts instance placed on its node | Covered | `TestAgentStartsPlacedInstance` |
| **Must** | Agent stops removed instance | Covered | `TestAgentStopsRemovedInstance` |
| **Must** | Agent ignores placements for other nodes | Covered | `TestAgentIgnoresOtherNode` |
| **Must** | Agent registers node in store | Covered | `TestAgentRegistersNode` |
| **Must** | Agent writes heartbeat lease | Covered | `TestAgentWritesHeartbeat` |
| **Must** | HTTP health probe reports healthy/unhealthy | Covered | `TestAgentReportsHealthy`, `TestAgentReportsUnhealthy` |
| **Must** | DSL parse + compile end-to-end | Covered | `TestApplyEndToEnd` |
| Should | No health check when service has no health config | Covered | `TestAgentNoHealthWithoutConfig` |
| Should | TCP health probe works | Covered | `TestTCPHealthy`, `TestTCPConnectionRefused` |
| Should | Simulator runtime tracks start/stop/list | Covered | `TestSimStart` through `TestSimInterfaceCompliance` |
| Should | Process runtime starts real OS processes | Covered | `TestProcessStartAndStop` through `TestProcessInterfaceCompliance` |
| Could | Container runtime (docker/nerdctl/lima) | Manual | `cca run-container examples/web-container.cca` |

### M3 — Distributed (Phase 6)

**Headline**: 10 instances spread across 3 nodes.

| Category | Scenario | Status | Test |
|----------|----------|--------|------|
| **Must** | Instances spread evenly across nodes | Covered | `TestDistributedSpreadAcrossNodes` |
| **Must** | Node failure → reschedule to survivors | Covered | `TestDistributedNodeFailureReschedules` |
| **Must** | Resource-aware scheduling (fit + spread) | Covered | `TestResourceFit`, `TestResourceSpreadWithAccounting` |
| **Must** | Node failure detector (lease expiry) | Covered | `TestNodeFailureDetectsExpiredLease` |
| Should | Heartbeats visible in store | Covered | `TestDistributedHeartbeatsVisibleInStore` |
| Should | Multiple services deployed simultaneously | Covered | `TestDistributedMultiServiceSpread` |
| Should | Instances on dead node marked failed | Covered | `TestNodeFailureMarksInstancesAsFailed` |
| Should | Already-unreachable nodes not re-detected | Covered | `TestNodeFailureSkipsAlreadyUnreachable` |
| Could | Resource exhaustion (more instances than capacity) | Covered | `TestResourceExhaustion` |

### M4 — Networking (Phase 7)

**Headline**: Services reachable by name, traffic balances.

| Category | Scenario | Status | Test |
|----------|----------|--------|------|
| **Must** | Instances get unique IPs from node subnets | Covered | `TestInstancesGetUniqueIPsFromNodeSubnet` |
| **Must** | Service gets VIP and DNS record | Covered | `TestServiceGetsVIPAndDNS` |
| **Must** | DNS resolves service name to VIP | Covered | `TestDNSResolvesServiceToVIP` |
| **Must** | Load balancer distributes across instances | Covered | `TestLoadBalancingDistributesTraffic` |
| **Must** | Two services reachable by name (headline) | Covered | `TestServiceReachableByName` |
| Should | Endpoints reflect allocated IPs, not 127.0.0.1 | Covered | `TestEndpointsReflectAllocatedIPs` |
| Should | Scale up adds to load balancer pool | Covered | `TestScaleUpAddsToLoadBalancerPool` |
| Should | Node failure updates networking | Covered | `TestNodeFailureUpdatesNetworking` |
| Should | IP released on instance stop | Covered | `TestInstanceIPReleasedOnStop` |
| Should | Two services have independent networking | Covered | `TestMultipleServicesIndependentNetworking` |
| Should | Agent with network provider writes allocation facts | Covered | `TestAgentWithNetworkProviderWritesAllocationFact` |
| Should | Agent without network provider uses 127.0.0.1 | Covered | `TestAgentWithoutNetworkProviderUsesLoopback` |
| Could | DNS server returns NXDOMAIN for unknown service | Covered | `TestDNSServerReturnsNXDOMAINForUnknownService` |
| Could | Concurrent IP allocations don't collide | Covered | `TestConcurrentAllocationsDoNotCollide` |

### M5 — Storage (Phase 8)

**Headline**: Persistent volumes survive node moves.

| Category | Scenario | Status | Test |
|----------|----------|--------|------|
| **Must** | Volume created from desired state (available) | Covered | `TestVolumeCreatedFromDesiredState` |
| **Must** | Volume attached when instance starts | Covered | `TestVolumeAttachedWhenInstanceStarts` |
| **Must** | Volume detached when instance stops | Covered | `TestVolumeDetachedWhenInstanceStops` |
| **Must** | Volume survives node failure (headline) | Covered | `TestVolumeSurvivesNodeFailure` |
| **Must** | Volume force-detached on unreachable node | Covered | `TestVolumeForceDetachOnUnreachableNode` |
| Should | Multiple services with independent volumes | Covered | `TestMultipleServicesIndependentVolumes` |
| Should | Service without volume unaffected | Covered | `TestServiceWithoutVolumeUnaffected` |
| Should | All volume facts present in store | Covered | `TestVolumeStateReflectedInStore` |
| Should | Volume reattaches after force-detach | Covered | `TestVolumeReattachAfterForceDetach` |
| Should | Agent without storage provider ignores volumes | Covered | `TestAgentWithoutStorageProviderIgnoresVolumes` |
| Should | Agent does not start when volume unavailable | Covered | `TestAgentDoesNotStartWhenVolumeUnavailable` |
| Should | Exclusive attach enforced (ReadWriteOnce) | Covered | `TestExclusiveAttachEnforced` |
| Should | StorageController idempotent | Covered | `TestStorageControllerIdempotent` |
| Could | Concurrent attach/detach thread-safe | Covered | `TestConcurrentAttachDetachSafe` |
| Could | Volume DSL round-trip (parse → compile → facts) | Covered | `TestCompileVolumeDeclaration`, `TestParseVolumeDeclaration` |

### Cross-Cutting

| Category | Scenario | Status | Test |
|----------|----------|--------|------|
| **Must** | All example configs parse and apply | Covered | `TestAllExamplesParseAndApply` |
| Should | Store value isolation (mutations don't leak) | Covered | `TestValueIsolation` |
| Should | Codec round-trips for all types | Covered | `TestServiceRoundTrip`, `TestInstanceRoundTrip`, `TestNodeRoundTrip`, `TestObservedVolumeRoundTrip` |
| Should | Key path format assertions | Covered | `TestKeyPaths`, `TestVolumeKeyPaths` |

## Known Gaps

Scenarios not yet tested that would strengthen confidence:

| Area | Scenario | Priority |
|------|----------|----------|
| Storage | Volume referenced by service but not declared in DSL | Should |
| Storage | Two services sharing the same volume (exclusive attach conflict) | Should |
| Storage | Volume size mismatch between desired and observed | Could |
| Storage | Delete desired volume while attached | Could |
| Networking | VIP pool exhaustion (more services than /24 addresses) | Could |
| Scheduling | Anti-affinity across availability zones | Future (M7) |
| DSL | Duplicate service names in same file | Should |
| DSL | Duplicate volume names in same file | Should |
| DSL | Empty service block (no image) error message | Covered | 
| Agent | Agent restart recovery (reconnect to existing runtime state) | Future (M6) |
| Controller | Controller restart (state rebuilt from store) | Future (M6) |
| Store | Watch ordering under concurrent writes | Could |
| End-to-end | Full lifecycle: deploy → scale → update image → rollback | Future (M7) |

## E2E Test Infrastructure

Two separate clusters, on separate hosts to avoid resource contention:

### E2E cluster (bridged LAN VMs on testbed-100.77)

Three vmherd VMs (Debian cloud image) on testbed-100.77, attached to the host bridge `br1` with vmherd bridge mode (vmherd >= 0.4.0), so they sit directly on the LAN like bare-metal nodes. Herdfile: `test/e2e/vm-lan/Herdfile`; inventory: `deploy/ansible/lan-e2e-inventory.ini`; values: `test/e2e/e2e-lan.ccv`. Layout matches the old bare-metal e2e cluster: the control-plane node runs no agent.

| VM | IP | Role | Resources |
|----|-----|------|-----------|
| cca-lan-ctrl | 192.168.100.50 | etcd + controlplane | 4096 MB / 2 vCPU |
| cca-lan-worker-1 | 192.168.100.51 | worker | 3072 MB / 2 vCPU |
| cca-lan-worker-2 | 192.168.100.52 | worker | 3072 MB / 2 vCPU |

- Addresses are static (cloud-init), from the range 192.168.100.50–.60 reserved for e2e VMs; keep that range outside the router's DHCP pool. Gateway and DNS are the LAN router (192.168.100.1), the host's upstream resolver — the host's own resolv.conf points at a 127.0.0.1 local resolver the VMs cannot use.
- Images come from the `registry:2` container on testbed-100.77 (`192.168.100.77:5000`).
- VMs stay running after a run (failed runs keep them for inspection); destroy with `cd test/e2e/vm-lan && vmherd destroy`.

Run: `./test/e2e/cluster_test.sh --env lan` (workflow `e2e.yml`, runner label `e2e` on the testbed-100.77 runner, on push to master)

### Deploy cluster (libvirt VMs on m4800)

Long-lived 3-node cluster on m4800 (192.168.100.71, 31 GB), managed by vmherd. Runs Airflow and other heavy workloads. Host setup, requirements and a manual step-by-step deployment: [deploy-host.md](deploy-host.md).

| VM | IP | Role | RAM |
|----|-----|------|-----|
| cca-deploy-ctrl | 192.168.124.10 | etcd + controlplane + worker | 4 GB |
| cca-deploy-worker-1 | 192.168.124.20 | worker | 4 GB |
| cca-deploy-worker-2 | 192.168.124.30 | worker | 4 GB |

VM state: `test/e2e/vm-deploy/.vmherd/`. Health check: `./test/e2e/health_check.sh` (hourly cron via `health-check.yml`).

### Resource requirements for workloads

The Airflow stack (postgres + redis + webserver + scheduler + 2 workers) needs at minimum 4 GB per node. On 2 GB nodes the Celery workers alone consume 500 MB–1 GB each, pushing the system deep into swap and causing multi-minute startup times. Lightweight workloads (whoami) run fine on 2 GB.

| Workload | Minimum per node | Recommended per node |
|----------|-----------------|---------------------|
| whoami, whoami-multiport | 1 GB | 2 GB |
| Airflow full stack | 4 GB | 8 GB |

### Two clusters, one host

The E2E VM cluster runs on testbed-100.77 (16 GB) and the deploy cluster on m4800. They use separate Herdfiles (`test/e2e/vm-lan/` and `test/e2e/vm-deploy/`): E2E VMs are bridged onto the LAN (192.168.100.50–.60), deploy VMs sit on a private libvirt NAT network (124.x). VM lifecycle managed by vmherd.

## Demo Commands

Each milestone has a corresponding demo command that exercises its headline feature interactively:

| Command | Milestone | What It Shows |
|---------|-----------|---------------|
| `cca demo` | M2 | Single node, 3 instances, simulated runtime |
| `cca demo-distributed` | M3 | 3 nodes, kills one, shows rescheduling |
| `cca demo-network` | M4 | VIPs, DNS, round-robin load balancing |
| `cca demo-storage` | M5 | Persistent volume migrates when node dies |

## Adding Tests for a New Milestone

1. Fill in the **Must/Should/Could** table for the milestone before starting implementation.
2. Write the **Must** tests as integration tests in `integration/`.
3. Write **Should** tests as unit tests in the relevant package.
4. Add an example `.cca` file to `examples/` — it is automatically validated by `TestAllExamplesParseAndApply`.
5. After completing the milestone, review the **Known Gaps** section and promote any that became relevant.
