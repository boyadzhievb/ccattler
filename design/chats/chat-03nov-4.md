CCattler — review as of November 3, 2026
There has been substantial progress since October 23. The project has moved from milestone M131 to work around M173, expanded its security and authorization work, improved its real-cluster tests, and added VXLAN-related networking fixes.

However, the latest commit does not currently have a green E2E result. The distinction between passing unit/security checks and passing the full cluster test matters here.
Repository snapshot
M173+
Latest source commit: d87e9770, November 3, 15:26 UTC
Main tests, CodeQL, Trivy, security and authentication tests
Passing

Latest three-node E2E
Failing

Latest testbed deployment
Passing

Sources: latest commit, latest CI runs.

1. The strongest new evidence is the three-node deployment
The latest successful Deploy Testbed Cluster run, on the latest commit, is substantially more convincing than a unit-test-only result.

It exercised real containers on three nodes and reported:
Test	Result
Workloads deployed	10 containers across 3 nodes
VIP reachability and DNS	Passed across all nodes
Cross-node HTTP traffic	Passed
Backend churn during scale-down	Passed
Worker failure and traffic recovery	Passed
API authorization checks (M173)	Passed
Gate TLS checks T1–T15	Passed
Final container stability	10/10 running

The TLS checks are particularly useful: automatic and manual certificates, SNI selection, HTTPS redirects, secure sticky cookies, certificate rotation, server and agent restarts, and removal of TLS configuration were exercised.

The latest scheduled cluster health check also passed with zero warnings. It reported 10 running instances and no failed instances.
A useful clarification about the failed E2E workflow
The separate E2E Cluster Test workflow on the latest commit failed before reaching the application tests. GitHub Actions repeatedly timed out downloading the pinned actions/setup-go archive.

That is a CI infrastructure problem, not an application test failure. The successful deployment workflow on the same commit built CCattler from source and executed the cluster test script successfully. So the latest source does have substantial real-cluster validation.
2. Networking is better, but multi-port support is not complete
A particularly useful fix landed today:

Commit d87e977 — repair existing VXLAN interfaces

The previous deployment had exposed a real failure: an agent could stop while creating the VXLAN interface, leaving it present but administratively down. On restart, the new agent trusted the existing interface, and routes repeatedly failed with Nexthop device is not up.

The reconciler now checks and repairs the interface state and MTU rather than assuming that an existing interface is healthy. The subsequent three-node deployment succeeded.

There is also an important admission of a remaining feature gap. The latest successful run reports:
Multi-port VIP status
Port 80 VIP
Working

Port 8081 VIP
Known gap

The per-port hostport facts exist, but port 8081 has no service VIP or corresponding nftables verdict-map entry.

The project now explicitly labels this as M186 rather than allowing a green test to imply that both ports work. The milestone description explains that the networking controller currently provides one VIP port per service.

This is good engineering practice: identifying and documenting a gap is better than hiding it behind a passing test. But it remains a functional limitation.
3. Security has improved substantially, but the remaining work is important
The October 23 review already raised concerns about the security model. The November 3 work directly addresses several serious issues.
Milestone	What changed	Assessment
M171	Prevented enrollment certificates from taking a forged server/admin identity	Implemented
M172	Protected the agent debug API with mTLS and an authenticated API proxy	Implemented
M173	Corrected API authorization synchronization, state-read restrictions and authentication-chain behavior	Implemented
M174	Per-node secret delivery, master-key removal and key rotation	Not complete
M175	Lease-based leader election, transaction fencing and HA correctness	Not complete
M176	Store-level access isolation for agents and controllers	Not complete
M177	Additional security hardening	Not complete

Sources: milestone checklist, security model.

Two unfinished details stand out:

Store access isolation: the security model explicitly says that only the API server currently uses AuthorizedStore. Agents and controllers still use the raw etcd client. That leaves the intended per-component least-privilege boundary unenforced for those components.

ABAC enforcement: the same document says ABAC conditions can be parsed and evaluated, but are not yet wired into the production admission pipeline.

M174 also plans to stop distributing the cluster master key to nodes and to introduce per-node secret delivery. M175 addresses control-plane leadership and fencing. These are not minor polish items; they matter directly to the impact of compromised nodes and to the correctness of a highly available cluster.

My interpretation: the security work is becoming more serious, but I would not yet describe the entire system as having complete zero-trust isolation.
4. eBPF and high availability remain experimental areas
The latest release includes separate cca-ebpf binaries, and the repository contains a substantial eBPF data-plane implementation.

However, the current M178 checklist identifies unfinished work:
- Validation of the conntrack-aware SNAT fix over repeated CI runs.
- Container-initiated egress testing.
- Safe BPF map persistence through agent restarts.
- TCP-level integration tests covering the packet-flow matrix.
M192 still holds eBPF network policy and the full removal of nftables dependencies.

Likewise, M179's HA validation is still planned rather than complete. Its goals include a three-member etcd cluster, leader failover in under 30 seconds, and recovery from an etcd member failure.

This means the project has a real eBPF implementation and HA-related code, but those should not yet be confused with demonstrated production-grade eBPF networking or highly available control-plane operation.
5. Cloud autoscaling and scale testing
One important distinction remains unchanged.

AWS and GCP adapters exist in cloud/ and contain real provider SDK operations. But the autoscaler uses a separate InfrastructureProvider interface. The current infra/ implementation is a simulator, and the CLI wires NewClusterAutoscaleController to NewSimulatorInfraProvider.

Sources: infrastructure interface, simulator, controller wiring.

So CCattler has cloud integration, but real AWS/GCP-driven cluster autoscaling is still not demonstrated.

The scale tests also remain synthetic. The 200-node/5,000-instance test uses the simulated cluster and in-memory store, and the load-test package is guarded by the loadtest build tag. The standard CI test workflow runs go test -race -short, so it does not continuously exercise those longer scale tests.

That is useful algorithmic testing, but not evidence of equivalent performance with real machines, etcd, container runtimes and network traffic.
6. Release and project maturity
The latest published release I found is v1.68.0-beta, published November 2. It includes regular, cloud-enabled and eBPF artifacts.

The November 3 master branch is newer than that release. Anyone evaluating the latest VXLAN fix or security changes should not assume those changes are already included in the published binaries.

The repository has one public star, one fork and no open issues. That is limited external adoption, not proof of poor engineering, but it means there is little independent usage evidence.

Final verdict
My assessment has improved
Credible experimental orchestrator
Not yet a proven production Kubernetes replacement.

Compared with October 23, there is now stronger evidence for actual three-node operation. In particular, node-failure traffic recovery, cross-node networking, API authorization checks and the complete gate-TLS test suite have all passed in the deploy environment.

I would now be comfortable recommending a serious controlled pilot, with rollback and monitoring, rather than merely a lab experiment.

I would still not put critical production workloads on it as the default platform. My top remaining concerns are store access isolation, secret distribution, HA control-plane validation, real cloud autoscaling, and the unfinished eBPF networking tests.

The most impressive change is not the milestone count increasing. It is that the project is now finding and documenting architectural security problems, fixing real networking failures, and validating more behavior against an actual multi-node deployment. That is meaningful engineering progress.