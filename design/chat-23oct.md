CCattler — fresh review, October 23, 2026
My assessment: CCattler is making real engineering progress, but I still wouldn’t recommend it as a production Kubernetes replacement without a controlled trial first. The latest changes improve testing and operations, while a few important reliability and validation gaps remain.

Repository: boyadzhievb/ccattler
What changed since the last review

1. More realistic multi-node testing

The project added a third node to its end-to-end test cluster and improved node-failure test setup and virtual-machine cleanup. A successful E2E run on October 22 is encouraging evidence that the test suite was working on that revision.




2. Better diagnostics

A recent change adds decision logging across controllers, including instance replacement, node-failure detection, scaling, draining and route updates. This should make unexpected behavior easier to diagnose. Commit




3. Test infrastructure is being adjusted

The latest infrastructure change moves the deploy cluster to 4 GB Vagrant VMs and excludes Airflow from the E2E workload because it needs more memory than the previous 2 GB nodes provided. This is a sensible response to a resource mismatch, but the new setup still needs a confirmed successful test run. Commit




4. Security and build checks are active

On the newest commit, Trivy and the GitHub Pages deployment had passed. The main test suite and CodeQL were still running, and the E2E run for the newest revision was pending at the time of this check.



Current CI status
Snapshot of the latest GitHub Actions runs:
Check	Latest observed status
Trivy security scan	Passed
GitHub Pages deployment	Passed
Main tests	In progress
CodeQL	In progress
E2E on latest commit	Pending

There is also a relevant operational warning: repeated cluster health checks failed. The log reported 0 of 3 deploy VMs reachable, an unhealthy control-plane endpoint and no running containers. That looks consistent with a testbed or deployment availability problem, rather than proving that the application logic itself is broken. But it is still a real gap in operational validation, and I found no completed passing health check after the latest infrastructure change.
The important gaps
1. The 5,000-workload result is useful, but not production-scale proof
The project’s status document reports these synthetic-cluster results:
Reported configuration	Convergence	Failure recovery	Scale-up
50 nodes / 1,000 instances	8.9 s	4 min	12.6 s
100 nodes / 2,000 instances	35 s	5 min	1 min 34 s
200 nodes / 5,000 instances	5 min 41 s	10 min 7 s	15 min 3 s

These are project-reported benchmarks, not results I could independently confirm from the latest standard CI run. The load tests have a loadtest build tag, and normal CI uses short-mode tests. The 200-node test uses a simulated cluster and in-memory MemoryStore, with a relaxed near-convergence threshold. Its results are meaningful for testing controller and scheduler behavior under synthetic load, but do not establish equivalent performance with real machines, etcd, containers, networking and application traffic.

Source: load-test implementation.
2. Cloud autoscaling remains incomplete
There is real cloud-provider code; it would be inaccurate to call the entire cloud implementation a stub. The AWS adapter uses AWS SDK APIs for instance and load-balancer operations.

However, the current status document says the cluster autoscaler uses SimulatorInfraProvider, with real AWS EC2 and GCP Compute implementations still future work. The Azure adapter explicitly returns “not yet implemented” errors.

For an orchestrator intended to replace Kubernetes in serious deployments, a demonstrated end-to-end test of provisioning, joining, scaling down and recovering real cloud nodes remains an important missing proof point.
3. Documentation and release metadata have drifted
The repository’s documents disagree about project state:
- CLAUDE.md says M131 is complete.
- design/STATUS.md still says M121 and release v1.48.0-beta.
- design/TESTING.md describes an older test inventory.
The latest published release I found is v1.57.2-beta, published October 20. Master has advanced since then. This does not invalidate the implementation, but it makes it harder for users to tell exactly which capabilities and benchmark figures match a released version.
4. The newest MCP remote server deserves a security review
The latest commit adds cca-mcp-remote, a remote administration tool exposing shell execution and file operations over SSH stdio.

In its implementation, exec passes a supplied command to /bin/sh -c, while file tools accept caller-supplied paths. I see no additional authorization or path restrictions inside this server. That may be intentional for remote administration, but anyone who can invoke it through the configured SSH identity effectively gets shell and file access with that account’s permissions. It should be treated as a privileged interface: use tightly scoped SSH access, a least-privilege account, and trusted MCP clients. I also found no test file in the new command’s directory.
Bottom line
CCattler looks more substantial than a toy project: it has a Go-based control plane, scheduling and reconciliation, security features, real networking work, cloud integrations and multi-node E2E coverage. The recent work on nftables, diagnostics and test infrastructure is credible progress.

What remains unproven is whether the whole system is reliably operable under real workloads at meaningful scale. The synthetic benchmark does not answer that by itself, cloud autoscaling has not been demonstrated end to end, the deploy-cluster health checks have been failing, and the newest E2E configuration has not yet reported a result.

My recommendation remains: consider it for a lab, controlled pilot or noncritical workloads; don’t make it your default production platform yet. The evidence that would most change that view is a repeatable real three-node deployment and failure-recovery test, followed by a documented scale test using real container workloads and an actual cloud autoscaler.