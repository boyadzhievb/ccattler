# CCattler HA Call Graph Reference

Machine-readable function call index for the HA control plane: leader election,
HARunner, controller lifecycle, and failover. Purpose: avoid re-reading source
files to trace HA flow. Load this file instead.

## Leader Election (controllers/leader.go)

### LeaderElection.Run → entry point
```
LeaderElection.Run                                  [controllers/leader.go:94]
├── tryAcquireOrRenew                               [immediate, first tick]
└── FOR EACH ticker.C (every renewInterval=5s):
    ├── tryAcquireOrRenew                           [on tick]
    └── ON ctx.Done:
        └── release                                 [graceful shutdown]
```

### tryAcquireOrRenew → acquire or renew lease
```
tryAcquireOrRenew                                   [controllers/leader.go:129]
├── mutex.Lock
├── IF isLeader:
│   └── renewLeaseWithCAS(nowStr)                   [renew path]
│
├── store.Get("leader/controlplane")
│   ├── IF err (no lease):
│   │   └── acquireLease(nowStr)                    [fresh acquisition]
│   │
│   └── IF lease found:
│       ├── parseLeaseTimestamp(leaseFact.Value)
│       ├── leaseAge = now - leaseTimestamp
│       └── IF leaseAge > leaseDuration:
│           ├── store.Transaction                   [CAS: expired lease takeover]
│           │   ├── Compare: leaderLeaseKey.revision == leaseFact.Revision
│           │   └── Then: Put(leaseKey, now) + Put(holderKey, nodeID)
│           └── IF ok:
│               ├── isLeader = true
│               ├── lastSuccessfulRenewal = now
│               └── onAcquired()                    [→ HARunner.startControllers]
└── mutex.Unlock
```

### acquireLease → first-time lease creation
```
acquireLease(nowStr)                                [controllers/leader.go:177]
├── store.Transaction                               [CAS: revision==0, key must not exist]
│   ├── Compare: leaderLeaseKey.revision == 0
│   └── Then: Put(leaseKey, now) + Put(holderKey, nodeID)
├── IF err → log WARN, return
└── IF ok:
    ├── isLeader = true
    ├── lastSuccessfulRenewal = now
    └── onAcquired()                                [→ HARunner.startControllers]
```

### renewLeaseWithCAS → leader renews lease
```
renewLeaseWithCAS(nowStr)                           [controllers/leader.go:206]
├── store.Get("leader/controlplane/holder")
│   └── IF err → handleTransientRenewalFailure("holder read failed")
├── IF holderFact.Value != nodeID:
│   └── relinquishLeadership("holder changed")
├── store.Transaction                               [CAS: holder revision matches]
│   ├── Compare: holderKey.revision == holderFact.Revision
│   └── Then: Put(leaseKey, nowStr)
├── IF txnErr → handleTransientRenewalFailure("CAS transaction error")
├── IF !renewed → relinquishLeadership("CAS failed")
└── ELSE → lastSuccessfulRenewal = now
```

### handleTransientRenewalFailure → tolerates brief etcd hiccups
```
handleTransientRenewalFailure(reason)               [controllers/leader.go:235]
├── sinceLast = time.Since(lastSuccessfulRenewal)
├── IF sinceLast > leaseDuration:
│   └── relinquishLeadership(reason + " exceeded lease duration")
└── ELSE:
    └── log WARN "transient renewal failure, retaining leadership"
```

### relinquishLeadership → give up leader role
```
relinquishLeadership(reason)                        [controllers/leader.go:248]
├── isLeader = false
├── log WARN "lost leadership during renewal"
└── onLost()                                        [→ HARunner.stopControllers]
```

### release → graceful shutdown cleanup
```
release(ctx)                                        [controllers/leader.go:259]
├── mutex.Lock
├── IF !isLeader → return
├── store.Get("leader/controlplane/holder")
│   └── IF err OR holderFact.Value != nodeID:
│       ├── isLeader = false
│       └── return                                  [skip delete — not our lease]
├── store.Transaction                               [CAS: safe delete]
│   ├── Compare: holderKey.revision == holderFact.Revision
│   └── Then: Delete(leaseKey) + Delete(holderKey)
├── isLeader = false
└── onLost()                                        [→ HARunner.stopControllers]
```

## HARunner (controllers/ha_runner.go)

### NewHARunner → constructor
```
NewHARunner(factStore, nodeID, metrics, controllers...) [controllers/ha_runner.go:59]
├── create HARunner{factStore, controllers, metrics}
└── NewLeaderElection(factStore, LeaderElectionConfig{
        NodeID:     nodeID,
        OnAcquired: haRunner.startControllers,
        OnLost:     haRunner.stopControllers,
    })
```

### HARunner.Run → entry point
```
HARunner.Run(ctx)                                   [controllers/ha_runner.go:78]
├── awaitPendingOperation()                         [mutex.Lock/Unlock, blocks until start/stop done]
└── election.Run(ctx)                               [blocks until ctx cancelled]
```

### startControllers → called by OnAcquired callback
```
startControllers()                                  [controllers/ha_runner.go:101]
├── mutex.Lock
├── IF cancelRunner != nil → return                 [already running, guard]
├── context.WithCancel(context.Background())
├── FOR EACH controller:
│   └── wrap with metricsWrappedController{inner, metrics}
├── NewRunner(factStore, wrappedControllers...)
├── IF eventLog != nil:
│   └── runner.SetEventLog(eventLog)
├── runnerDone = make(chan struct{})
├── go runner.Run(controllerCtx)                    [goroutine]
│   └── defer close(runnerDone)
└── log "controllers started (leader)"
```

### stopControllers → called by OnLost callback
```
stopControllers()                                   [controllers/ha_runner.go:140]
├── mutex.Lock
├── IF cancelRunner != nil:
│   ├── cancelRunner()                              [cancel all controllers]
│   ├── select:
│   │   ├── <-runnerDone → log "controllers stopped (lost leadership)"
│   │   └── <-time.After(10s) → log ERROR "runner shutdown timed out"
│   ├── cancelRunner = nil
│   ├── runner = nil
│   └── runnerDone = nil
└── mutex.Unlock
```

### metricsWrappedController → reconciliation metrics
```
metricsWrappedController.Reconcile(ctx, facts)      [controllers/ha_runner.go:171]
├── startTime = time.Now()
├── inner.Reconcile(ctx, facts)                     [delegate to wrapped controller]
└── metrics.RecordReconciliation(name, duration, len(changes), err)
```

## Controller Runner (controllers/runner.go)

### Runner.Run → starts all controllers
```
Runner.Run(ctx)                                     [controllers/runner.go:196]
├── context.WithCancel(ctx)
├── IF eventLog != nil:
│   ├── NewEventProjector(store, eventLog)
│   └── go eventProjector.Run(childCtx)             [goroutine]
├── FOR EACH controller:
│   └── go runSingleController(childCtx, controller) [goroutine]
├── select:
│   ├── <-ctx.Done → firstError = ctx.Err()
│   └── err from controllerErrors channel
├── cancelChildren()
└── waitGroup.Wait()
```

### runSingleController → retry with backoff
```
runSingleController(ctx, controller)                [controllers/runner.go:238]
└── FOR attempt 0..∞:
    ├── runControllerLoop(ctx, controller)
    ├── IF ctx.Err() → return
    ├── delay = backoff[attempt]                    [100ms, 500ms, 1s, 2s, 5s, 30s]
    ├── log WARN "controller failed, retrying"
    └── time.After(delay)
```

### runControllerLoop → watch + reconcile
```
runControllerLoop(ctx, controller)                  [controllers/runner.go:274]
├── setupControllerWatches(watchCtx, controller, trigger, watchLost, deltaBuffer)
│   └── FOR EACH prefix in controller.Watch():
│       ├── store.Watch(prefix, {Prefix: true})
│       └── go forwardWatchEvents(...)              [goroutine]
├── executeReconciliationCycle(ctx, controller, nil) [initial]
└── runReconcileSelectLoop(ctx, controller, trigger, watchLost, deltaBuffer)
    └── select:
        ├── reconcileTrigger → debounce(50ms) → executeReconciliationCycle
        ├── resyncTicker(30s) → executeReconciliationCycle
        └── watchLost → return error → restart controller
```

### executeReconciliationCycle → optimistic concurrency retries
```
executeReconciliationCycle(ctx, controller, deltaEvents) [controllers/runner.go:409]
└── FOR attempt 0..4:
    ├── attemptSingleReconciliation(ctx, controller, events)
    │   ├── scanFactsForController → allFacts[]
    │   ├── computeReconciliationChanges → changes[]
    │   │   ├── IF deltaEvents + DeltaController:
    │   │   │   └── ReconcileDelta(ctx, facts, events)
    │   │   └── ELSE:
    │   │       └── controller.Reconcile(ctx, facts)
    │   ├── enforceWriteDomain(controllerName, changes)
    │   ├── validateAndCapChanges(controllerName, changes)
    │   └── store.Transaction(compares, operations, nil)
    ├── IF !conflictDetected → return nil
    └── ELSE → backoff (10ms–500ms + jitter), retry
```

## Server Startup (cmd/cca/command_server.go)

### startControllersWithLeaderElection → production HA path
```
startControllersWithLeaderElection(ctx, factStore, eventLog, config) [cmd/cca/command_server.go:370]
├── coreControllers()                               [cmd/cca/helpers.go:286]
│   ├── NewInstanceController()
│   ├── NewScheduler()
│   ├── NewEndpointController()
│   ├── NewFailureController()
│   ├── NewAutoscaleController()
│   ├── NewIntentResolverController()
│   ├── NewRolloutController()
│   ├── NewInitController()
│   ├── NewStatefulDNSController()
│   ├── NewStatefulVolumeController()
│   ├── NewRouteController()
│   ├── NewEvictionController()
│   ├── NewJobController()
│   ├── NewCronController()
│   ├── NewInstanceGCController()
│   └── NewAuditLogGCController()
├── append HA-only controllers:
│   ├── NewNodeFailureController()
│   ├── NewDrainController()
│   ├── NewNetworkController() or NewNetworkControllerWithCIDR(vipCIDR)
│   ├── NewNetworkPolicyController()
│   └── NewWarmZeroController()
├── IF cloudProviderName != "":
│   ├── NewNodeLifecycleController(cloudProvider)
│   ├── NewCloudLoadBalancerController(cloudProvider, factStore)
│   └── NewCloudRouteController(cloudProvider, factStore)
├── NewHARunner(factStore, nodeID, metricsCollector, controllerList...)
├── haRunner.SetEventLog(eventLog)
└── go haRunner.Run(ctx)                            [goroutine]
```

## Node Failure Detection (controllers/nodefailure.go)

### NodeFailureController.Reconcile
```
NodeFailureController.Reconcile(ctx, facts)         [controllers/nodefailure.go:92]
├── parseNodeLeaseTimestamps(facts)                 [lease/node/* → map[nodeID]int64]
├── parseCurrentNodeStates(facts)                   [observed/node/*/state → map[nodeID]string]
├── identifyUnreachableNodes(currentTime, timestamps, states)
│   ├── FOR EACH node with heartbeat:
│   │   ├── IF unreachable + heartbeat fresh → recovered → Change: state=alive
│   │   ├── IF unreachable + heartbeat stale → add to unreachableNodeSet
│   │   └── IF alive + heartbeat stale → add to unreachableNodeSet
│   └── FOR newly unreachable nodes → Change: state=unreachable
│       [capped at MaxNodeStateChangesPerCycle=12]
├── markInstancesOnUnreachableNodesAsFailed(facts, unreachableNodeSet, budget)
│   ├── parse placement → instanceID→nodeID
│   ├── FOR instances on unreachable nodes (running/pending/starting):
│   │   └── Change: Put derived/instance/{id}/node_failure = "true"
│   └── [capped at remainingBudget]
└── cleanupStaleNodeFailureMarkers(facts, unreachableNodeSet, budget)
    └── Delete derived/instance/{id}/node_failure for instances on recovered nodes
```

## EventProjector (controllers/event_projector.go)

### EventProjector.Run → watch committed state transitions
```
EventProjector.Run(ctx)                             [controllers/event_projector.go:49]
├── watch prefixes:
│   ├── observed/instance/
│   ├── derived/instance/
│   ├── observed/node/
│   ├── placement/instance/
│   └── effective/service/
├── mergedChannel = make(chan Event, 64)
├── FOR EACH prefix:
│   └── go watchAndForward(ctx, prefix, mergedChannel) [goroutine per prefix]
└── processEvents(ctx, mergedChannel)               [blocks, emits semantic events]
```

## Store Key Patterns (HA subsystem reads/writes)

### Leader Election Keys
| Key Pattern | Read By | Written By |
|---|---|---|
| `leader/controlplane` | `tryAcquireOrRenew` | `acquireLease`, `renewLeaseWithCAS` |
| `leader/controlplane/holder` | `renewLeaseWithCAS`, `release`, `CurrentLeader` | `acquireLease`, `tryAcquireOrRenew` (CAS) |

### Node Failure Keys
| Key Pattern | Read By | Written By |
|---|---|---|
| `lease/node/{nodeID}` | `NodeFailureController.parseNodeLeaseTimestamps` | Agent (heartbeatLoop) |
| `observed/node/{nodeID}/state` | `NodeFailureController.parseCurrentNodeStates` | `identifyUnreachableNodes`, Agent |
| `derived/instance/{id}/node_failure` | Agent (`isInstanceMarkedForSkip`) | `markInstancesOnUnreachableNodesAsFailed` |

## Reverse Index (who calls this function?)

| Function | Called By |
|---|---|
| `LeaderElection.Run` | `HARunner.Run` |
| `tryAcquireOrRenew` | `LeaderElection.Run` (initial + every renewInterval tick) |
| `acquireLease` | `tryAcquireOrRenew` (when no lease exists) |
| `renewLeaseWithCAS` | `tryAcquireOrRenew` (when isLeader) |
| `handleTransientRenewalFailure` | `renewLeaseWithCAS` (on Get error or CAS error) |
| `relinquishLeadership` | `renewLeaseWithCAS` (holder changed/CAS failed), `handleTransientRenewalFailure` (exceeded lease) |
| `release` | `LeaderElection.Run` (on ctx.Done) |
| `parseLeaseTimestamp` | `tryAcquireOrRenew` |
| `HARunner.Run` | `startControllersWithLeaderElection` (goroutine) |
| `startControllers` | `LeaderElection.onAcquired` callback |
| `stopControllers` | `LeaderElection.onLost` callback |
| `awaitPendingOperation` | `HARunner.Run` (before election start) |
| `metricsWrappedController.Reconcile` | `Runner.executeReconciliationCycle` via `attemptSingleReconciliation` |
| `Runner.Run` | `startControllers` (goroutine), `startControllerRunner` (non-HA path) |
| `runSingleController` | `Runner.Run` (goroutine per controller) |
| `runControllerLoop` | `runSingleController` |
| `setupControllerWatches` | `runControllerLoop` |
| `forwardWatchEvents` | `setupControllerWatches` (goroutine per prefix) |
| `runReconcileSelectLoop` | `runControllerLoop` |
| `executeReconciliationCycle` (server) | `runControllerLoop` (initial), `runReconcileSelectLoop` (on trigger) |
| `attemptSingleReconciliation` | `executeReconciliationCycle` (up to 5 retries) |
| `scanFactsForController` | `attemptSingleReconciliation` |
| `computeReconciliationChanges` | `attemptSingleReconciliation` |
| `enforceWriteDomain` | `attemptSingleReconciliation` |
| `validateAndCapChanges` | `attemptSingleReconciliation` |
| `buildReconciliationTransaction` | `attemptSingleReconciliation` |
| `coreControllers` | `startControllersWithLeaderElection`, `startControllerRunner` |
| `startControllersWithLeaderElection` | `runServerCommand` (if runControllers=true) |
| `NewEventProjector` | `Runner.Run` (if eventLog != nil) |
| `EventProjector.Run` | `Runner.Run` (goroutine) |
| `NodeFailureController.Reconcile` | `Runner` via `attemptSingleReconciliation` |
| `identifyUnreachableNodes` | `NodeFailureController.Reconcile` |
| `markInstancesOnUnreachableNodesAsFailed` | `NodeFailureController.Reconcile` |
| `cleanupStaleNodeFailureMarkers` | `NodeFailureController.Reconcile` |
| `parseNodeLeaseTimestamps` | `NodeFailureController.Reconcile` |
| `parseCurrentNodeStates` | `NodeFailureController.Reconcile` |

## Error Propagation Paths

### Leader election errors
```
LeaderElection.Run
├── tryAcquireOrRenew
│   ├── store.Get fails → acquireLease (treats missing key as fresh)
│   ├── acquireLease → store.Transaction fails
│   │   └── LOG WARN "leader election transaction failed", return (retry next tick)
│   ├── CAS for expired lease → txnErr
│   │   └── return (retry next tick, another node may have taken it)
│   └── renewLeaseWithCAS
│       ├── store.Get(holder) fails
│       │   └── handleTransientRenewalFailure
│       │       ├── sinceLast < leaseDuration → LOG WARN, retain
│       │       └── sinceLast > leaseDuration → relinquishLeadership → onLost
│       ├── holder changed (another node)
│       │   └── relinquishLeadership → onLost
│       └── CAS txn error or failed
│           └── handleTransientRenewalFailure / relinquishLeadership → onLost
│
└── release (ctx.Done)
    ├── store.Get(holder) fails → isLeader = false, skip delete
    ├── holder != nodeID → isLeader = false, skip delete
    └── CAS delete fails → LOG ERROR, isLeader = false
```

### Controller runner errors
```
Runner.Run
├── EventProjector.Run fails → error on controllerErrors channel → cancelChildren
└── runSingleController
    └── runControllerLoop
        ├── setupControllerWatches → store.Watch fails
        │   └── return error → backoff retry (100ms, 500ms, 1s, 2s, 5s, 30s)
        ├── executeReconciliationCycle → scanFactsForController fails
        │   └── return error → restart controller with backoff
        └── runReconcileSelectLoop
            ├── watchLost (channel closed)
            │   └── return error → restart controller with backoff
            └── executeReconciliationCycle → reconcile error
                └── LOG ERROR, continue (non-fatal)
```

### HARunner stop timeout
```
stopControllers
└── cancelRunner() + select:
    ├── <-runnerDone (normal) → log "controllers stopped"
    └── <-time.After(10s) → LOG ERROR "runner shutdown timed out, forcing stop"
        [controllers may still be running — split-brain risk if new leader starts]
```

## Implicit Ordering Constraints

### 1. stopControllers completes BEFORE new leader starts controllers
```
Old leader loses leadership:
  stopControllers() → cancelRunner() → wait runnerDone (max 10s)
  Only AFTER stopControllers returns does the mutex unlock.

New leader acquires via CAS:
  startControllers() → mutex.Lock → creates fresh Runner
  The new leader's CAS on etcd ensures the old lease is stale.
```
**Why it matters:** If old leader's controllers overlap with new leader's
controllers, both write to the same keys → CAS conflicts escalate → convergence stalls.

### 2. Leader lease must be renewed before leaseDuration expires
```
Leader renews every renewInterval (5s):
  renewLeaseWithCAS writes new timestamp to leader/controlplane

Standby checks every renewInterval (5s):
  tryAcquireOrRenew reads leader/controlplane
  IF leaseAge > leaseDuration (15s) → CAS takeover
```
**Failover window:** 15–20 seconds (leaseDuration + up to renewInterval worst case).

### 3. Graceful shutdown deletes lease → immediate failover
```
release() CAS-deletes leader/controlplane + holder
  → next standby tick: no lease exists → acquireLease (revision==0)
  → failover in ≤ renewInterval (5s), not leaseDuration (15s)
```

## Constants Reference

| Constant | Value | Location | Purpose |
|---|---|---|---|
| `defaultLeaderLeaseDuration` | 15s | controllers/leader.go:64 | Lease validity window |
| `defaultLeaderLeaseRenewInterval` | 5s | controllers/leader.go:68 | Renewal tick frequency |
| `stopControllersTimeout` | 10s | controllers/ha_runner.go:18 | Max wait for controller shutdown |
| `defaultControllerDebounceInterval` | 50ms | controllers/runner.go:26 | Quiet period between reconcile cycles |
| `defaultControllerResyncInterval` | 30s | controllers/runner.go:30 | Forced full reconciliation period |
| `reconcileRetryBaseDelayMs` | 10ms | controllers/runner.go:34 | CAS retry base backoff |
| `reconcileRetryMaxDelayMs` | 500ms | controllers/runner.go:38 | CAS retry max backoff |
| `defaultMaxReconciliationAttempts` | 5 | controllers/runner.go:402 | Max CAS retries per cycle |
| `maxTransactionChanges` | 60 | controllers/runner.go:55 | Max changes per etcd transaction |
| `etcdTransactionOperationLimit` | 128 | controllers/runner.go:42 | etcd hard limit |
| `defaultNodeFailureLeaseTimeout` | 30s | controllers/nodefailure.go:22 | Heartbeat expiry for node failure |
| `defaultMaxTotalChangesPerCycle` | 58 | controllers/nodefailure.go:29 | NodeFailure change budget |
| `defaultMaxNodeStateChangesPerCycle` | 12 | controllers/nodefailure.go:35 | Node state changes per cycle |
| `defaultEtcdDialTimeout` | 5s | store/etcd.go:23 | etcd connection timeout |
| `defaultEtcdWatchBuffer` | 64 | store/etcd.go:28 | Watch event channel buffer |
| `defaultWriteRatePerSecond` | 500 | store/write_rate_limiter.go:17 | Sustained etcd write rate |
| `controllerRestartBackoffDelays` | 100ms→30s | controllers/runner.go:61 | Controller restart backoff sequence |

## File Reference

| File | Purpose |
|---|---|
| `controllers/leader.go` | LeaderElection: lease acquire/renew/release with CAS |
| `controllers/leader_test.go` | 6 tests: acquire, contention, failover, release, stale holder, safe release |
| `controllers/ha_runner.go` | HARunner: leader-aware controller runner, start/stop lifecycle |
| `controllers/runner.go` | Inner Runner: watch, debounce, reconcile loop, optimistic concurrency |
| `controllers/nodefailure.go` | NodeFailureController: heartbeat monitoring, node→unreachable, instance→failed |
| `controllers/event_projector.go` | EventProjector: watch-based semantic event log |
| `store/etcd.go` | EtcdStore: connect, CRUD, watch, transactions, write rate limiter |
| `store/store.go` | StateStore interface: Get/Put/Delete/Scan/Watch/Transaction/Lease |
| `store/write_rate_limiter.go` | Token-bucket write rate limiter (500 writes/s, burst 1000) |
| `cmd/cca/command_server.go` | Server startup: startControllersWithLeaderElection |
| `cmd/cca/helpers.go:286` | coreControllers(): 16 built-in controllers |
| `security/enrollment.go` | Node enrollment: join tokens, re-enrollment after CA loss |
| `security/rotator.go` | CertificateRotator: auto-renewal before expiry |
