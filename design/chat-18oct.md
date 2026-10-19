# CCattler: latest reassessment

As of October 18, 2026, the project has advanced from M104 to M121, with release `v1.48.0-beta`. My verdict is more nuanced now: this is a substantial, actively developed orchestration project, but its production readiness still depends on evidence that is missing or not yet independently verified.

The latest source: [CCattler on GitHub](https://github.com/boyadzhievb/ccattler?utm_source=chatgpt.com).

## What's genuinely improved

Multi-node deployment continues to pass

The [October 17 E2E run](https://github.com/boyadzhievb/ccattler/actions/runs/39055802134?utm_source=chatgpt.com) passed on the latest commit. The [testbed deployment](https://github.com/boyadzhievb/ccattler/actions/runs/39057515290?utm_source=chatgpt.com) and [documentation deployment](https://github.com/boyadzhievb/ccattler/actions/runs/39055802118?utm_source=chatgpt.com) also succeeded.

Networking is getting more sophisticated

Milestone M121 adds an nftables data-plane provider using verdict maps for O(1) dispatch. The following commits fix compatibility with older nftables and kernel versions and replace a broad ruleset flush with a scoped table deletion. These are concrete implementation and operational improvements, not just documentation changes.

[M121 implementation](https://github.com/boyadzhievb/ccattler/commit/13b13331efb84f9b13a366cc1b1160d441c81a15?utm_source=chatgpt.com) · [Ruleset safety fix](https://github.com/boyadzhievb/ccattler/commit/4775647668bd36eca7f16595514202c58505485a?utm_source=chatgpt.com)

The developer experience is improving

M120 adds DSL formatting and linting through `cca fmt` and `cca lint`. M119 adds service tags and cross-service filtering for logs and workload inspection. M118 introduces route declarations and sticky-session support.

[M120 formatter/linter](https://github.com/boyadzhievb/ccattler/commit/e0f644dc0b7e536a355c6c9cf7e792af7d432490?utm_source=chatgpt.com) · [M119 tags](https://github.com/boyadzhievb/ccattler/commit/2815048a928dad916abc93b6cfee930d943e7c66?utm_source=chatgpt.com)

Core CI and security checks are green

The latest main test, E2E, CodeQL and Trivy workflows all passed. There were earlier E2E failures during the nftables compatibility work, followed by fixes and passing runs. That is a normal development pattern; the important thing is that the latest run passes.

## What still blocks a confident production recommendation

### 1. Scalability remains the biggest unresolved question

The project's own [status document](https://github.com/boyadzhievb/ccattler/blob/master/design/STATUS.md?utm_source=chatgpt.com) reports that its 200-node, 5,000-workload benchmark timed out after roughly 2,400 workloads in 30 minutes.

There is an important caveat: that document is stale, still reporting M80 and release v1.14.2-beta. It cannot tell us whether the latest controllers perform better. But I did not find evidence in the recent changes or latest CI runs that the 5,000-workload test now passes.

The newer networking work is valuable, but it does not by itself establish controller throughput at that scale.

### 2. Real-cloud autoscaling is still a separate gap

The repository has AWS and GCP provider code, but the [infrastructure layer](https://github.com/boyadzhievb/ccattler/tree/master/infra?utm_source=chatgpt.com) was previously documented as simulator-backed, with real provider implementations deferred. Azure was also explicitly described as a stub.

I could not verify a successful end-to-end test that provisions cloud VMs, joins them to a cluster, replaces failed nodes, and scales them back down. Local VM deployments are useful evidence, but they are not equivalent to testing those cloud lifecycle operations.

### 3. Networking improvements need traffic-level validation

The nftables implementation is a meaningful step forward. The next question is whether actual traffic reliably reaches healthy services across nodes—not just whether the expected rules exist.

I would want automated tests covering:

- Requests to a service VIP from another node.
- Backend removal and addition while requests are flowing.
- Node failure and recovery without manual intervention.
- Sticky sessions and route changes.
- Cleanup that never deletes unrelated host firewall rules.

These tests should fail the CI run when traffic breaks, rather than merely report the presence of networking configuration.

### 4. The product still needs a convincing comparative advantage

The [ecosystem comparison](https://github.com/boyadzhievb/ccattler?utm_source=chatgpt.com) and recent feature additions may improve the pitch, but more features do not prove that CCattler is better than Kubernetes, Nomad, or a simpler container deployment system for a particular workload.

The useful comparison would measure deployment complexity, operational burden, recovery time, resource overhead, and performance using reproducible workloads. Public adoption is also still very limited: the repository currently shows zero stars and one fork. That's not a technical failure, especially for a young project, but it is not external validation.

## Final scorecard

| Dimension                           | My assessment                                 |
| ----------------------------------- | --------------------------------------------- |
| Active engineering                  | Strong                                        |
| Multi-node integration              | Demonstrated                                  |
| Networking implementation           | Advancing; needs stronger traffic-level proof |
| Test and security automation        | Good baseline                                 |
| Large-scale performance             | Unverified                                    |
| Cloud autoscaling                   | Not demonstrated end to end                   |
| Production readiness                | Not established                               |
| Differentiation from existing tools | Not yet proven                                |

My bottom line: CCattler has real engineering substance. The latest milestones make it harder to dismiss as a toy project, and the recent nftables work addresses a genuinely important part of an orchestrator. But the milestone count—now M121—is not a measure of reliability.

If I were deciding whether to adopt it, I would consider it for a controlled experiment or noncritical workload, not as the default platform for production services. The most valuable next milestone would be a reproducible scale benchmark plus automated failure-and-recovery tests, followed by a real-cloud autoscaling demonstration. Those results would change my assessment more than another dozen feature milestones.