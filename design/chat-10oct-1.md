# CCattler: critical project review

Repository: [github.com/boyadzhievb/ccattler](https://github.com/boyadzhievb/ccattler)

My initial assessment is that CCattler has a potentially legitimate idea, but its scope is far larger than its demonstrated value. The strongest criticism is not that the repository contains no real engineering. It clearly does. The problem is whether building a new container orchestration platform is justified when Kubernetes and simpler deployment tools already solve much of the problem.

The project describes itself as a Kubernetes alternative that replaces Kubernetes objects and YAML manifests with a custom declarative language, a fact store, independent controllers, a scheduler, and node agents. It also attempts to cover networking, persistent storage, security, multi-tenancy, autoscaling, and cloud infrastructure. [Project README](https://github.com/boyadzhievb/ccattler/blob/master/README.md)

That is an enormous engineering commitment. Each subsystem needs to work correctly on its own, and the subsystems must work together during failures, restarts, network partitions, upgrades, and partial outages.

I reviewed the repository structure, documentation, CI workflows, cloud-provider code, test strategy, and available design-review notes. Below are the principal weaknesses, with a distinction between concrete findings and concerns that still require runtime verification.

## 1. The fundamental problem: it is solving the wrong problem

Severity: Critical product-strategy weakness

CCattler's central selling point is that users should not need to deal with Kubernetes objects and YAML. Instead, they express intent in a custom `.cca` language, and a new orchestration system handles the rest.

That is a legitimate preference, but it is not enough by itself to justify replacing an entire orchestration platform.

The project has to reproduce a large amount of difficult infrastructure: scheduling, health monitoring, state reconciliation, service discovery, networking, persistent storage, identity, authorization, upgrades, fault recovery, and cluster administration. It also has to make these capabilities as dependable as the alternatives it wants people to leave behind.

The trade-off looks like this:

| Claimed benefit                         | Corresponding cost                                                          |
| --------------------------------------- | --------------------------------------------------------------------------- |
| Less YAML and fewer Kubernetes concepts | A new language, compiler, documentation, and debugging model                |
| Facts instead of orchestration objects  | A custom state model and its consistency guarantees                         |
| Independent controllers                 | More interactions, retries, and failure paths to coordinate                 |
| One unified scaling engine              | New scheduling, infrastructure-provisioning, and lifecycle responsibilities |
| Built-in networking and security        | A new platform-wide security and networking attack surface                  |

The README explains why the project exists, but it does not establish that this added complexity produces a measurable advantage over existing tools. [Source: CCattler README](https://github.com/boyadzhievb/ccattler/blob/master/README.md)

The hardest question is not whether CCattler can run a few services. It is: what important real-world problem does CCattler solve materially better than an established alternative?

Without a convincing answer backed by benchmarks and operational evidence, the project is difficult to justify for adoption.

## 2. A concrete security weakness in transactional authorization

High priority

Severity: Potential authorization bypass

This is the most concerning source-level defect I identified.

In [`security/authorized_store.go`](https://github.com/boyadzhievb/ccattler/blob/master/security/authorized_store.go), the `AuthorizedStore.Transaction` method authorizes the comparison keys and the operations in the transaction's success branch. It then passes both the success and failure branches to the underlying store.

The method does not authorize operations in the `onFailure` branch.

The underlying [`StateStore.Transaction`](https://github.com/boyadzhievb/ccattler/blob/master/store/store.go)[ contract](https://github.com/boyadzhievb/ccattler/blob/master/store/store.go) explicitly supports operations being executed when comparison conditions fail. Therefore, a caller with an authenticated but insufficiently privileged identity could potentially submit a transaction whose comparison fails and whose failure branch writes or deletes a protected key.

The problematic pattern is conceptually:

```
// Conceptual example, not an executed exploit.
authorizedStore.Transaction(
    ctx,
    comparisonsThatFail,
    nil,
    []store.Op{unauthorizedWrite},
)
```

If the underlying store executes that failure branch as specified, the missing authorization check can permit a protected mutation.

I have not executed an exploit against a running CCattler installation. This is a source-level security finding based on the wrapper and store interface.

Required correction: authorize every mutation in both transaction branches before delegating to the underlying store. Add a regression test that submits an unauthorized failure-branch write and verifies that the operation is rejected and the protected key remains unchanged.

For a system advertising RBAC, ABAC, multi-tenancy, and least-privilege access, this is not a minor polish issue. It is a reason to withhold production approval until the behavior is fixed and tested.

## 3. Cloud capabilities are not uniformly implemented

Severity: High for users expecting cloud-ready infrastructure

The project presents itself as a broad orchestrator with cloud integration, but the implementation is uneven.

Azure: explicit stub implementation

[`cloud/azure.go`](https://github.com/boyadzhievb/ccattler/blob/master/cloud/azure.go) explicitly describes itself as a stub. Its methods for listing, creating, and terminating virtual machines, managing load balancers, and modifying routes return errors saying they are not yet implemented.

Consequently, the presence of an Azure provider type should not be interpreted as working Azure support.

Cluster autoscaling: simulator-backed infrastructure interface

The [`infra`](https://github.com/boyadzhievb/ccattler/tree/master/infra)[ package](https://github.com/boyadzhievb/ccattler/tree/master/infra) contains the infrastructure-provider interface and simulator implementation. The simulator creates synthetic node records in the state store rather than real cloud machines.

The [M78 capacity-state-machine document](https://github.com/boyadzhievb/ccattler/blob/master/design/m78-capacity-state-machine.md) explicitly lists real cloud provider implementations as deferred work.

This is an important limitation: a functioning simulated capacity lifecycle is not the same as production cloud autoscaling.

AWS and GCP have substantially more provider code under `cloud/`, so it would be inaccurate to call every cloud integration a stub. The issue is that implemented provider APIs, deployable infrastructure, and operationally verified end-to-end capabilities are different maturity levels.

That distinction needs to be made explicit in the product's capability matrix.

## 4. A large test suite does not establish production reliability

Severity: High

The README claims more than 1,595 tests across 21 packages and 78 completed milestones. The repository also has extensive test files, linting, vulnerability auditing, race detection, and a separate self-hosted end-to-end workflow.

These are genuine strengths. They are not evidence that the project has no value.

However, the coverage and quality gates need to be examined in context.

The standard [GitHub test workflow](https://github.com/boyadzhievb/ccattler/blob/master/.github/workflows/test.yml) runs:

```
go test -race -short -count=1 -timeout=5m ./...
```

That is useful, but `-short` excludes longer-running scenarios. For example, [`loadtest/heartbeat_stress_test.go`](https://github.com/boyadzhievb/ccattler/blob/master/loadtest/heartbeat_stress_test.go) explicitly skips its 50-node simulated heartbeat stress test in short mode.

There is also a separate [end-to-end workflow](https://github.com/boyadzhievb/ccattler/blob/master/.github/workflows/e2e.yml) that runs a two-node cluster test on a self-hosted runner. That provides evidence beyond pure unit testing, but it does not by itself demonstrate reliability across many machines, cloud-provider failures, storage failures, upgrades, or extended operation under load.

The important distinction is:

| Type of evidence                         | What it establishes                                                      |
| ---------------------------------------- | ------------------------------------------------------------------------ |
| Unit tests with simulated state          | Individual functions behave as expected in the tested cases              |
| In-memory integration tests              | Controllers and components interact correctly under simulated conditions |
| Container-runtime tests                  | Selected runtime operations work in the tested environment               |
| Multi-node end-to-end tests              | Actual components work together in a particular deployment               |
| Long-duration failure and recovery tests | Selected reliability properties hold during extended, realistic failures |

CCattler has evidence in several of these categories. What is not established simply by a high test count is the breadth of the scenarios tested, the realism of their failure conditions, and whether the critical ones run as mandatory release gates.

For an orchestrator, a test should not merely establish that a node eventually gets marked failed. It should test whether the real failure-detection mechanism notices a dead agent, whether workloads recover correctly, whether storage remains safe, and whether the system avoids accidental duplicate operations.

A high number of passing tests is not a substitute for that evidence.

## 5. The documentation and milestone counts undermine confidence

Severity: Medium, with implications for maintainability

There is a concrete documentation inconsistency in the repository.

The main README claims 78 completed milestones and more than 1,595 tests. The older [`design/STATUS.md`](https://github.com/boyadzhievb/ccattler/blob/master/design/STATUS.md) still reports:

- Milestones M1–M30.
- 512 tests across 13 packages in the associated [testing plan](https://github.com/boyadzhievb/ccattler/blob/master/design/TESTING.md).
- A latest release of `v0.20.1`.
- Metrics dated September 19, 2026.

Meanwhile, the repository had beta releases in the `v1.14.x-beta` series on October 9, 2026, and its recent commits refer to milestones M77 and M78.

The likely explanation is stale documentation rather than deliberate deception. Nevertheless, it shows that project-status information has not been kept consistent with the current implementation.

The repository also contains numerous detailed design notes and review transcripts. These can be valuable during development, but they should not substitute for a single current source of truth that clearly specifies:

- What works today.
- What is simulated.
- What requires manual setup.
- What is implemented but not production-validated.
- Which features remain incomplete.
- Which versions and environments are supported.

A project attempting to replace a mature platform needs unusually clear operational documentation. Users must be able to distinguish a completed implementation milestone from a feature that they can reliably deploy and maintain.

## 6. The supply-chain story is weaker than it could be

Severity: Medium

The installation instructions recommend downloading and executing `install.sh` directly from GitHub:

```
curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
```

This is a common installation pattern, but it deserves additional safeguards for software that may eventually control production machines.

The release workflow creates SHA-256 checksum files for release archives. However, the current [`install.sh`](https://github.com/boyadzhievb/ccattler/blob/master/install.sh) downloads and extracts the archive without verifying the published checksum.

This is not proof that the downloaded binary is malicious or compromised. It is a supply-chain integrity gap: the installation path does not itself verify that the archive matches the project's published checksum.

I would expect the installer to verify checksums and fail closed, with stronger artifact provenance or signature verification as an additional improvement.

## 7. The product is too broad for its demonstrated differentiation

Severity: Critical product risk

This is the strategic issue that ties the other weaknesses together.

CCattler is not just a simpler configuration language. It attempts to own an entire orchestration stack. Its repository contains a compiler, controllers, an etcd-backed store, a scheduler, node agents, network components, storage logic, authorization, tenant management, cloud integrations, and several runtime modes.

That creates a difficult competitive position:

- If someone only wants to avoid writing Kubernetes YAML, a custom orchestrator is a very expensive way to obtain that benefit.
- If someone wants to deploy containers on a small number of machines, a simpler solution may have less operational overhead.
- If someone needs enterprise-grade orchestration, they will ask about recovery guarantees, supported infrastructure, ecosystem integration, security review, upgrade compatibility, and long-term maintenance.
- If someone wants cloud autoscaling, they need actual provider-backed provisioning rather than only a simulated lifecycle.

The README does identify a distinctive design philosophy: model desired state as facts and derive actions through reconciliation. That is a reasonable architectural experiment. But a novel internal model is not automatically a better product.

The repository needs compelling evidence that the architecture delivers some combination of easier operations, fewer configuration errors, lower resource consumption, faster convergence, better recovery, or materially simpler deployments.

The published README does not provide a comparative benchmark demonstrating such an advantage. That doesn't mean no benchmarks exist in the code; it means the product case is not established by the documentation I reviewed.

## Overall assessment

## My verdict

Based on source inspection and documentation review, not a full executed production audit.

What deserves credit

The fact-based reconciliation model is a legitimate design direction. There is substantial Go implementation, automated testing, security tooling, an actual two-node E2E workflow, and real effort toward handling distributed-state problems.

What is not convincing

The product's differentiation is insufficiently demonstrated for the size of the system. Cloud support is uneven, the autoscaling infrastructure interface remains simulator-backed, documentation is inconsistent, and the authorization wrapper contains a potentially serious transaction-branch security gap.

Production recommendation

I would not approve CCattler for security-sensitive or business-critical workloads without fixing the authorization issue, testing the fix, establishing actual cloud-support boundaries, and obtaining stronger end-to-end evidence for storage, failure recovery, and scaling.

## What I would do next

I would stop adding features temporarily and require evidence for three things.

First, fix the transaction authorization problem and audit the rest of the security boundary. Second, choose one narrow, valuable use case and demonstrate that it works with real processes, containers, and infrastructure—not just a simulator. Third, publish reproducible comparisons against the simplest credible alternative, measuring setup effort, steady-state resource use, deployment time, recovery time, and failure behavior.

If CCattler cannot demonstrate a meaningful advantage on that narrow use case, I would not continue investing in it as a general-purpose Kubernetes replacement.

My conclusion is not that CCattler contains nothing useful. It is that the engineering breadth currently outpaces the demonstrated product advantage and the evidence needed to trust it. That is a much stronger and more defensible criticism than simply calling the code useless.