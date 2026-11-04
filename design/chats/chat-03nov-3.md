CCattler — updated code review
November 3, 2026 · latest revision d87e9770

Overall: substantial improvement. The previous sticky-route wiring issue is fixed, and the deploy-cluster validation is strong. I found one remaining medium-severity route lifecycle bug.
CI and deployment
Deploy validation passed
Check	Result
Unit and integration tests	Passed
CodeQL	Passed
Trivy	Passed
Security tests	Passed
GitHub Pages	Passed
Deploy Testbed Cluster	Passed
E2E Cluster Test workflow	Setup failed

The E2E workflow failure was environmental: the runner timed out downloading actions/setup-go before it started executing the test. It is not evidence of an application test failure.

The separate deploy workflow did execute the cluster checks successfully. Its logs show:
- Three live nodes and 10/10 instances still running.
- Cross-node VIP traffic passed.
- Backend-churn traffic and node-failure traffic passed.
- M173 API authorization checks passed.
- Gate TLS tests T1–T15 passed, including secure sticky-cookie behavior.
- Multi-port cross-node traffic on port 80 passed.
Sources: latest deploy run · latest E2E workflow.
Findings
1. Medium — deleting all routes does not clear data-plane state
In agent/dataplane.go, route reconciliation is conditional:
if len(routeConfigs) > 0 {
    if err := dataPlaneReconciler.dataPlaneProvider.
        ReconcileRouteDataPlane(ctx, routeConfigs); err != nil {
        // ...
    }
}


This creates a lifecycle bug when the final desired route is deleted.

The provider never receives an empty route list, so it cannot clear its old state:
- nftables: lastRouteConfigs retains previous routes. The next VIP reconciliation can continue directing traffic to their old route chains.
- iptables: stale route chains and jump rules are not removed.
- Cookie proxy: listeners belonging to deleted routes are not stopped.
The current nftables provider has the correct route-dispatch mechanism, but it depends on receiving the updated route configuration. See ReconcileRouteDataPlane.

Recommended fix: call ReconcileRouteDataPlane unconditionally, including when routeConfigs is empty. Add a regression test that creates a sticky route, removes it, and verifies that its dispatch entry, chain, and cookie listener are gone.
2. Known gap — second exposed service port
The deploy log still reports:
nftables verdict map entries — port 80: true, port 8081: false


Port 80 passes the cross-node test. Port 8081 is not yet represented in the VIP dispatch map.

This is documented as M186 in milestones.md. It is not a newly discovered regression, but it means complete multi-port VIP support is still unfinished.
3. Fixed — nftables sticky-route dispatch
The earlier concern about route chains existing without being reachable is resolved.

The current provider:
- Builds VIP-to-route dispatch overrides.
- Directs matching VIP/port traffic to the route chain.
- Appends route chains to the same atomic nftables ruleset.
- Retains route configurations for the next VIP reconciliation.
The deploy test also exercised sticky-cookie behavior successfully. See network/nftables_dataplane.go.
4. Fixed — VXLAN interface recovery
The latest commit repairs an existing VXLAN interface when it is down or has the wrong MTU. It also checks interface state during regular reconciliation instead of assuming that an existing interface is healthy.

This directly addresses the previously observed Nexthop device is not up failure. The latest deploy test passed its cross-node traffic and node-failure checks.

View the VXLAN repair commit.
Final assessment
Current assessment
One medium issue

The core cluster, networking, authentication, and deployment checks are in good shape. Route deletion still needs to propagate an empty configuration so deleted routes cannot remain active.
My priority order would be:
1. Fix empty-route reconciliation and test deletion for both nftables and iptables.
2. Complete M186 so port 8081 receives a working VIP path.
3. Rerun the E2E workflow once the runner can download its setup action.
I would not claim full route lifecycle correctness or complete multi-port networking yet. The successful deploy run is encouraging, but those two gaps remain.