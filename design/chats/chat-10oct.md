I checked the latest master of boyadzhievb/ccattler. The latest commit I found is 91b6bd3, from October 10, fixing GitHub Actions SHA pins. The main code changes are in M77 and M78.
Verdict: the previous fixes are largely in place, but I found three significant correctness issues in the newer cloud and capacity-request logic, plus one storage cleanup issue. I would address these before considering the controller work complete.
Findings

1. P1 — A failed capacity request can block autoscaling retries
clusterscale.go · request coverage · cleanup
M78 generates new scale-up requests before cleaning up requests in ready or failed state. However, collectCoveredInstances only considers pending and launching requests as covering an instance.
That creates a duplicate-key failure path:
1. A provider request fails and the existing capacity request transitions to failed.
2. The instance is still unplaced, so the next reconciliation tries to create another request with the same ID.
3. That same reconciliation also emits deletes for the existing failed request.
4. The new puts and cleanup deletes target the same keys. The runner's duplicate-key validation rejects the change set.
The controller can repeatedly hit this error and fail to clean up or retry the request while the underlying demand persists. A similar conflict can occur for a ready request while the instance remains unplaced.
Fix: ensure a reconciliation never emits both new writes and cleanup deletes for the same request keys. Clean up terminal requests first, or defer recreation to a subsequent cycle. Add a test for a failed request and persistent unplaced demand.




2. P1 — Existing cloud load balancers won't receive backend updates
cloud_loadbalancer.go, lines 69–100
In Reconcile, an externally exposed service with an existing load-balancer address is skipped:
previousAddress := existingLoadBalancers[serviceName]
if previousAddress != "" {
    continue
}


Consequently, after the first successful creation, endpoint changes do not generate another EnsureLoadBalancer intent. A deployment or node replacement can leave the cloud load balancer pointing at stale backends.
The controller's documented responsibility includes updating backend lists, but the current reconciliation logic only ensures the initial load balancer and handles deletion.
Fix: track the desired load-balancer configuration (or a stable hash of ports, protocol, and backends) and emit an ensure/update intent whenever that configuration changes. Add a test that creates an LB, changes its endpoint set, and verifies a second provider update.




3. P1/P2 — A pending load-balancer creation can outlive its service
cloud_loadbalancer.go
Pending operations are stored separately from observed LB state. But reconciliation determines whether to create or delete an LB from desired external services and observed LB addresses; it does not adequately reconcile a pending ensure against service removal.
For example, if EnsureLoadBalancer fails and leaves a pending ensure, then the service is deleted before an address is recorded, neither the desired-service loop nor the observed-LB deletion loop necessarily replaces or cancels that intent. Since the executor retries pending operations, it can create a load balancer for a service that no longer exists.
The same general stale-intent risk should be checked in the cloud-route controller.
Fix: reconcile pending operations against current desired state. Removing a service should supersede or cancel a pending ensure, with durable delete intent where an external resource may already have been created.




4. P2 — Deleted volumes can still have pending external operations
storage.go
The volume-cleanup logic deletes observed volume facts, but it does not remove pending resize or snapshot intents under derived/volume/. The post-commit executor scans that derived prefix and processes pending operations without first verifying the volume is still desired.
If a user removes a volume while a provider operation is pending, the executor can still resize or snapshot it. Depending on the provider, it may also keep retrying an operation for a volume that no longer exists.
Fix: define cancellation semantics for pending operations when a volume is removed, and make the executor verify the intent is still current before acting. Guard completion writes and pending-key deletion so a stale executor cannot erase a newer operation.



What improved since the previous review
The M77/M78 changes address the earlier issues meaningfully:
- Storage resize and snapshot calls are now post-commit, with distinct pending-resize and pending-snapshot keys.
- Resize no longer updates observed size before the provider confirms success.
- The runner now retries pending post-commit operations even when reconciliation produces no new changes.
- Cloud load-balancer and route provider mutations have moved out of Reconcile.
- The capacity controller now uses a durable request state machine rather than calling the infrastructure provider directly during reconciliation.
- The event projector now tracks revisions and attempts to recover from compaction and overflow.
These are good architectural changes. The remaining problems are mainly in keeping durable intents aligned with current desired state, and ensuring retries and cleanup don't conflict with each other.
CI status
The latest GitHub Actions results I found are encouraging:
Check	Result
Standard tests, lint and audit	Passed
E2E cluster test	Passed
Trivy security scan	Passed
Manually triggered cluster health check	Passed
Sources: Test, E2E, Trivy, cluster health.
One qualification: the standard test workflow runs go test -race -short, so the long-running heartbeat stress test is skipped there. These green results don't establish that the 50-node stress scenario passes in CI.
Recommended next step
I'd fix the capacity-request duplicate-key path first, then restore cloud load-balancer backend reconciliation and make pending cloud operations cancelable when their desired resources disappear. These are concrete functional correctness gaps rather than further tuning of transaction size or retry backoff.