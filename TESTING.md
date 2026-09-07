# CCattler Test Plan

249 tests across 11 packages. Every milestone has a "must test" headline scenario, supported by unit and integration tests.

## How to Run

```bash
go test ./...                          # all tests
go test ./integration/ -v              # integration tests only
go test ./... -run TestVolumeSurvives  # single test by name
go run ./cmd/cca/ demo-storage         # live demo with volume migration
```

## Examples

The `examples/` directory contains working `.ccattler` configs — one per feature area. Each can be applied with `cca apply`, and all are validated by `TestAllExamplesParseAndApply`.

| File | Features Demonstrated |
|------|----------------------|
| `basic.ccattler` | Single service, 3 instances, exposed port, resources |
| `multi-service.ccattler` | Two services, different instance counts and resources |
| `health-checks.ccattler` | HTTP health probe with path, TCP health probe, intervals |
| `distributed.ccattler` | 6 instances spread across 3 nodes, failure recovery |
| `networking.ccattler` | VIPs, DNS, load balancing across 2 services |
| `storage.ccattler` | Persistent volume, service-to-volume binding, migration |
| `web.ccattler` | Process runtime (sleep command as image) |
| `web-container.ccattler` | Container runtime (nginx Docker image) |

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
| Could | Container runtime (Docker CLI) | Manual | `cca run-container examples/web-container.ccattler` |

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
4. Add an example `.ccattler` file to `examples/` — it is automatically validated by `TestAllExamplesParseAndApply`.
5. After completing the milestone, review the **Known Gaps** section and promote any that became relevant.
