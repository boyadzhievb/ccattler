## Updated findings — October 10, 2026

The latest commits materially change the review. Several weaknesses I identified earlier have since been addressed in `master`, so they should not be presented as current defects.

The current branch includes:

| Commit           | Change                                                                                           | Effect on the previous review                                                                       |
| ---------------- | ------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------- |
| `7722ac47` — M80 | Fix transaction authorization bypass                                                             | The missing authorization check on the transaction failure branch has been addressed in the source. |
| `d6b81180` — M81 | Update project status and add installer checksum verification                                    | The stale status document and missing installer checksum verification have been addressed.          |
| `7398987f` — M79 | Correctness work involving capacity requests, cloud load balancers, and storage intent alignment | The external-operation and reconciliation design has been improved.                                 |
| `83f04cce` — M82 | Fair-share instance creation to prevent service starvation                                       | Scheduling fairness has received additional attention.                                              |

Sources: [M80](https://github.com/boyadzhievb/ccattler/commit/7722ac47), [M81](https://github.com/boyadzhievb/ccattler/commit/d6b81180), [M79](https://github.com/boyadzhievb/ccattler/commit/7398987f), and [M82](https://github.com/boyadzhievb/ccattler/commit/83f04cce).

The latest commit I found was `83f04cce`, dated October 10, 2026. Its CI runs were still in progress when I checked, so I would not call that commit fully validated yet.

## What remains wrong in the current version

### 1. The target-scale performance problem is not proven fixed

Highest priority

The current [`design/STATUS.md`](https://github.com/boyadzhievb/ccattler/blob/master/design/STATUS.md) still identifies a serious known issue: the 200-node, 5,000-workload test was timing out during initial convergence, reaching only approximately 2,400 of 5,000 workloads after 30 minutes.

M82 addresses a plausible cause: service starvation in the instance-creation budget. Its new [unit test](https://github.com/boyadzhievb/ccattler/blob/master/controllers/instance_test.go) checks fair allocation across three services.

That is a sensible fix, but there is a significant gap in the evidence:

- The actual [200-node/5,000-workload test](https://github.com/boyadzhievb/ccattler/blob/master/loadtest/loadtest_test.go) skips when Go tests run in short mode.
- The standard [CI test workflow](https://github.com/boyadzhievb/ccattler/blob/master/.github/workflows/test.yml) runs tests with `-short`, so this scalability test is not part of that normal gate.
- M82 adds a focused unit test, but its commit does not add a new large-scale regression test proving that the original timeout has been eliminated.

The latest automatic end-to-end test did pass on M82, but that workflow is explicitly a two-node test, not the 200-node benchmark. The main test and CodeQL workflows were still running when I last checked. [E2E run](https://github.com/boyadzhievb/ccattler/actions/runs/38075722367) · [latest test run](https://github.com/boyadzhievb/ccattler/actions/runs/38075722255)

Assessment: the fix looks reasonable, but the evidence does not yet establish that the stated target scale works. For software whose purpose is orchestration, convergence and recovery are core product requirements, not optional performance tuning.

### 2. Cloud autoscaling is still a major product gap

The project has AWS and GCP provider code, but its infrastructure-provisioning path is not equivalent to a fully functioning cloud autoscaler.

The [`infra`](https://github.com/boyadzhievb/ccattler/tree/master/infra)[ package](https://github.com/boyadzhievb/ccattler/tree/master/infra) contains an infrastructure-provider interface and a simulator that creates simulated nodes. The project's own status document explicitly lists real cloud implementations for autoscaling as deferred.

That means the project has separate cloud API implementations and a simulator-backed autoscaling path, but has not demonstrated that the autoscaler can reliably provision and decommission real cloud machines through an integrated production workflow.

The latest [Cloud Provider Tests workflow](https://github.com/boyadzhievb/ccattler/actions/runs/38075722333) passed. That is positive evidence, but the selected tests in [`cloud/cloud_test.go`](https://github.com/boyadzhievb/ccattler/blob/master/cloud/cloud_test.go) largely test simulator behavior, provider construction, configuration validation, and resource-name handling. They do not establish the end-to-end correctness of real VM provisioning, load-balancer updates, and route changes.

For AWS and GCP, this is a validation gap rather than proof that their provider code does nothing. For Azure, however, the limitation is explicit.

### 3. Azure is a declared feature that is not implemented

[`cloud/azure.go`](https://github.com/boyadzhievb/ccattler/blob/master/cloud/azure.go) still has a stub implementation. Its methods for listing, creating, and terminating VMs, managing load balancers, and managing routes return “not yet implemented” errors.

The Azure tests verify those errors, so a green test suite is compatible with Azure being entirely unusable for these operations.

The project needs to mark Azure as unsupported until implemented, or actually complete the provider. Listing an interface and constructor is not the same thing as providing that capability.

### 4. Some recovery tests do not test the entire recovery mechanism

The long-running synthetic test also has a limitation in its failure-recovery phase. In [`loadtest/loadtest_test.go`](https://github.com/boyadzhievb/ccattler/blob/master/loadtest/loadtest_test.go), the test directly writes nodes as unreachable and running instances as failed into the in-memory store.

This tests how the system recovers after failure has been declared. It does not, by itself, prove that the real failure detector correctly discovers that an agent has died.

There is also a small inconsistency in the test: the comment says it kills 20 nodes, while `nodesToKill` is set to 10.

Neither point invalidates the whole test. They do mean that the test should not be treated as end-to-end evidence for failure detection without additional tests that stop agents and let the detector discover the failure naturally.

### 5. Contributor instructions and project status are already out of sync

There are still inconsistencies in the repository:

- [`go.mod`](https://github.com/boyadzhievb/ccattler/blob/master/go.mod) declares Go `1.26.9`, while [`CONTRIBUTING.md`](https://github.com/boyadzhievb/ccattler/blob/master/CONTRIBUTING.md) says Go `1.22+`.
- The current project status and README still report completion through M80, even though the current branch contains M81 and M82.

The first issue can directly mislead a developer trying to build the project. The second is a smaller documentation problem, but it shows how quickly the project status falls behind the implementation.

The M81 commit corrected the older status document, so this is not the same stale M30 issue I raised previously. It is a new, smaller inconsistency introduced by subsequent development.

### 6. The product advantage remains unproven

The biggest strategic concern is unchanged. CCattler replaces familiar orchestration interfaces with a custom DSL and a custom control plane, but it still needs to provide the reliability and operational tooling expected of an orchestrator.

The README describes differences from Kubernetes, but I could not establish a demonstrated advantage from the reviewed documentation in deployment effort, operational cost, performance, recovery, or maintainability.

The repository's current GitHub metadata also shows zero stars, one fork, and no open issues. The project is very young—it was created on September 6, 2026—so these figures do not prove poor quality. They do mean there is little visible evidence of independent community adoption or external validation yet. [Repository](https://github.com/boyadzhievb/ccattler)

## Final verdict

My updated position is more specific than my original review.

CCattler is not empty or technically worthless. The recent fixes demonstrate real engineering: transaction authorization was corrected, installer integrity checking was added, storage and cloud reconciliation received additional fixes, and scheduling fairness was improved.

However, the project still has three major barriers to being a credible general-purpose Kubernetes replacement:

1. Scalability is not established. A documented 5,000-workload convergence failure remains unresolved in the published status, and the latest fairness fix has not yet been demonstrated by a passing large-scale regression run.
2. Cloud autoscaling is incomplete. Real provider-backed machine provisioning is not integrated into the autoscaling infrastructure path, and Azure remains a stub.
3. The benefit does not yet justify the scope. The custom language and fact-based architecture add a great deal of software to maintain, without compelling published evidence that users receive a better operational outcome.

I would treat CCattler as an ambitious early-stage orchestration project, not as a production-ready replacement for Kubernetes.

My recommendation would be to stop expanding the feature set until the maintainers can demonstrate a passing 200-node/5,000-workload benchmark, validate real cloud provisioning and recovery, and publish a reproducible comparison against existing alternatives. Until then, adding more milestones risks increasing the size of the codebase faster than its reliability or practical value.