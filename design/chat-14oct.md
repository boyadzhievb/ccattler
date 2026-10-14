I checked it again on October 14, 2026. My assessment has improved materially: CCattler is no longer fairly described as just a simulator or a half-formed idea. It has made real progress toward a working multi-node container orchestrator. But the evidence still falls short of proving that it is ready to replace Kubernetes in production.

## What changed since my previous review

Real multi-node deployments now pass

The [latest E2E run](https://github.com/boyadzhievb/ccattler/actions/runs/38586663844) successfully deployed Airflow and Zabbix onto two real VMs running under libvirt. The run confirmed that nine containers were running, the two cluster nodes registered, and VIP-related iptables rules were programmed.

A second, three-node deployment also passed

The [deployment workflow](https://github.com/boyadzhievb/ccattler/actions/runs/38587384679) passed with three VMs and the latest release binary. It brought up the same nine containers across the cluster. This is meaningful evidence of actual deployment and reconciliation, not merely unit tests.

The core quality checks are green

On the current commit, the main [Test workflow](https://github.com/boyadzhievb/ccattler/actions/runs/38586664000), [CodeQL](https://github.com/boyadzhievb/ccattler/actions/runs/38586664059), [Trivy security scan](https://github.com/boyadzhievb/ccattler/actions/runs/38586663993), and E2E workflow all passed.

The recent work includes fixes to cross-node VIP routing, DNS and container initialization, user authentication, connection-secret handling, and node enrollment. The repository is at [commit ](https://github.com/boyadzhievb/ccattler/commit/f2d483ea28ee98a740e71f3dcc5bcffd3a9a63b8)[`f2d483e`](https://github.com/boyadzhievb/ccattler/commit/f2d483ea28ee98a740e71f3dcc5bcffd3a9a63b8)[ — M104](https://github.com/boyadzhievb/ccattler/commit/f2d483ea28ee98a740e71f3dcc5bcffd3a9a63b8), with release `v1.33.0-beta`.

## What still concerns me

### 1. The 5,000-workload scalability claim remains unproven

The repository has a [200-node/5,000-workload test](https://github.com/boyadzhievb/ccattler/blob/master/loadtest/loadtest_test.go), but the normal CI workflow runs Go tests in `-short` mode, which skips this large test.

The last relevant test changes I found were on October 10, tuning controller creation budgets and retry settings. I could not find a newer successful run demonstrating that all 5,000 workloads converge. The [published status document](https://github.com/boyadzhievb/ccattler/blob/master/design/STATUS.md) reports that the test previously timed out after reaching roughly 2,400 of 5,000 workloads in 30 minutes.

That document is stale, so I would not treat the old timeout as proof that the current code still fails. But neither would I treat the scale problem as solved without a recorded successful benchmark.

### 2. The E2E tests still prove less than application readiness

The real-cluster tests are a substantial improvement. They verify node registration, container counts, debug API endpoints, image pulling, VIP facts, and the presence of iptables rules.

However, I don't see a hard assertion in those runs that a user-facing request successfully traverses the service network and reaches a healthy Airflow or Zabbix application. The [deployment playbook](https://github.com/boyadzhievb/ccattler/blob/master/deploy/ansible/deploy-deploy.yml) even has an Airflow health request followed by `|| true`, which means its failure does not fail that check.

A stronger release gate would make end-to-end HTTP requests through the actual service path and fail when the application is unhealthy.

### 3. Real cloud autoscaling is still incomplete

There is AWS- and GCP-specific code in the [`cloud`](https://github.com/boyadzhievb/ccattler/tree/master/cloud)[ package](https://github.com/boyadzhievb/ccattler/tree/master/cloud), so saying “there is no cloud code” would be wrong.

The gap is more specific: the [autoscaler's infrastructure interface](https://github.com/boyadzhievb/ccattler/tree/master/infra) contains only `infra.go` and `simulator.go`. I found no AWS or GCP implementation of that separate node-provisioning interface. And [Azure](https://github.com/boyadzhievb/ccattler/blob/master/cloud/azure.go) still explicitly returns “not yet implemented” errors for its cloud operations.

Consequently, passing multi-VM libvirt tests does not yet demonstrate that CCattler can safely provision, replace, and retire nodes in a real cloud environment.

### 4. The documentation site is currently failing to build

The latest [GitHub Pages workflow](https://github.com/boyadzhievb/ccattler/actions/runs/38586663947) failed because `website/changelog.md` contains a duplicate VitePress heading ID: `v1-33-0-beta`. That's a straightforward documentation bug, not a failure of the orchestrator itself.

There is also inconsistent project status reporting: [`design/STATUS.md`](https://github.com/boyadzhievb/ccattler/blob/master/design/STATUS.md) still says M80, while [`CLAUDE.md`](https://github.com/boyadzhievb/ccattler/blob/master/CLAUDE.md) and [`milestones.md`](https://github.com/boyadzhievb/ccattler/blob/master/milestones.md) report progress through M104.

## My updated verdict

| Area                                    | Assessment                                   |
| --------------------------------------- | -------------------------------------------- |
| Core engineering effort                 | Substantial                                  |
| Real multi-node deployment              | Demonstrated on 2–3 local VMs                |
| Automated tests and security checks     | Good baseline; major scale test excluded     |
| Application-level networking and health | Needs stronger end-to-end proof              |
| Large-cluster scalability               | Unverified                                   |
| Real cloud autoscaling                  | Incomplete                                   |
| Production replacement for Kubernetes   | Not yet demonstrated                         |
| Product differentiation                 | Plausible, but not yet measured convincingly |

The underlying idea—declare desired state in a simpler language and let controllers reconcile it—is reasonable. The project now has enough real implementation and integration testing that dismissing it as nonsense would be inaccurate.

The main issue is no longer whether CCattler can run real workloads. It can. The issue is how reliably it handles failure, scale, upgrades, and infrastructure outside its test environment.

My recommendation is to pause adding more milestones long enough to produce three pieces of evidence: a passing and repeatable 5,000-workload benchmark, application-level cross-node traffic and recovery tests, and a verified real-cloud autoscaling implementation. Those would do much more to establish production credibility than another batch of features.