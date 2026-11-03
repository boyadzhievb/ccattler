Yes. I think OpenSpec would be a good addition to CCattler, particularly for the GitOps feature we just discussed.

My recommendation is to use it as a structured engineering workflow around Claude Code, while keeping CCattler's existing architecture documents, `CLAUDE.md`, and milestone system.

I checked the current `master` branch: I couldn't find an existing `openspec/config.yaml`, so it does not appear to be initialized yet.

## 1. What OpenSpec would give CCattler

OpenSpec introduces a structured process for turning an idea into an implementation:

1. Proposal

   Why are we building this? What problem does it solve?
2. Specification

   What observable behavior must the system support?
3. Design

   How should it fit into the existing architecture?
4. Tasks

   What exactly needs to be changed and tested?
5. Implementation and verification

   Claude implements the tasks, and the result is checked against the proposal.
6. Archive

   The completed change is archived and its specification updates become part of the maintained specifications.

This is OpenSpec's standard spec-driven workflow.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://openspec.dev\&sz=32)

OpenSpec+1



For CCattler, this means Claude should understand not only what code to write, but also why a feature exists, which architectural invariants it must preserve, and what evidence is required to consider it complete.

## 2. Pros and cons

| Pros                                                       | Cons                                                    |
| ---------------------------------------------------------- | ------------------------------------------------------- |
| Better planning before coding                              | More preparation for each substantial feature           |
| Explicit requirements and acceptance criteria              | Specifications can become outdated                      |
| Better separation of architecture and implementation tasks | Potential duplication with existing `design/` documents |
| Preserves decisions and rationale                          | More files to maintain                                  |
| Helps Claude work across multiple packages consistently    | AI-generated plans still need engineering review        |
| Makes large changes easier to divide into stages           | Adds context and token overhead                         |
| Easier to review what was intended versus what shipped     | Does not guarantee correctness or replace tests         |

There is one particularly important risk for CCattler: creating two competing sources of architectural truth.

You already have substantial engineering documentation in `design/`, `CLAUDE.md`, and `milestones.md`. I would not migrate all of that into OpenSpec.

Instead, give each system a clear job:

| Existing system     | Responsibility                                                              |
| ------------------- | --------------------------------------------------------------------------- |
| `CLAUDE.md`         | Permanent engineering rules and architectural invariants                    |
| `design/`           | Detailed architecture, data flows, call graphs and technical decisions      |
| `milestones.md`     | Project roadmap and milestone tracking                                      |
| `openspec/changes/` | Proposals, requirements, design decisions and tasks for changes in progress |
| `openspec/specs/`   | Maintained behavioral specifications of implemented capabilities            |

This avoids maintaining the same information in several places.

## 3. How I would configure it for CCattler

Start with the standard `spec-driven` schema. It already provides proposal, specifications, design, and implementation tasks.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://openspec.dev\&sz=32)

OpenSpec



I would give Claude a small amount of project-specific guidance:

```
schema: spec-driven

context: |
  CCattler is a Go-based, fact-driven container orchestrator.

  Architectural invariants:
  - Desired state and observed state are separate.
  - Controllers produce plans; the store commits state.
  - Agents produce observations.
  - Actions are never treated as proof of reality.
  - Controllers communicate through committed facts.
  - Reconciliation must be idempotent and convergent.

  Read CLAUDE.md and relevant design documents
  before proposing or implementing changes.
  Do not introduce Kubernetes object ontology
  into CCattler's user-facing model.

rules:
  proposal:
    - Explain the problem and explicitly identify non-goals.
    - Identify affected existing capabilities.

  specs:
    - Specify observable behavior and failure cases.
    - Preserve desired/observed state separation.

  design:
    - Explain ownership, concurrency, recovery and security.
    - Reference existing design documents instead of duplicating them.

  tasks:
    - Include unit, integration and end-to-end tests.
    - Include documentation and call-graph updates where needed.

operations:
  apply:
    guidance:
      - Inspect the current implementation before editing.
      - Follow CLAUDE.md and existing design constraints.
      - Run relevant tests and report actual results.
```

OpenSpec supports separate context, artifact-specific rules, and implementation guidance. Keeping this configuration concise is important because it is included in the agent's workflow instructions.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://openspec.dev\&sz=32)

OpenSpec+1



## 4. How I would use it for the GitOps feature

I would make this the first substantial OpenSpec change:

`git-backed-configuration-sources`

Recommended first change

Proposal

Introduce Git repositories as managed sources of CCattler desired configuration.

Capabilities to specify:

- Git source registration and revision tracking.
- Repository fetching and configuration compilation.
- Source-owned desired-state facts.
- Safe pruning and ownership enforcement.
- Synchronization status versus workload health.
- Failure recovery and retry behavior.

Explicit non-goals for the first implementation:

- Kubernetes manifest compatibility.
- Replacing the existing controller runner.
- Pull-request preview environments.
- Complex deployment strategies.
- Webhooks before polling works reliably.

This is a good first OpenSpec project because it crosses several important architectural boundaries: DSL compilation, store transactions, resource ownership, authorization, controller reconciliation, and failure handling.

I would insist that the specifications answer these questions before implementation:

1. What happens when Git becomes unavailable?
2. What happens when a commit contains invalid configuration?
3. How do we prevent one source deleting another source's facts?
4. How do we safely handle partial application?
5. What happens when the Git revision changes during synchronization?
6. How do we distinguish a synchronized source from a healthy deployment?

Those are architectural requirements, not just implementation details.

## 5. Installation and workflow

OpenSpec's current official setup supports Claude Code directly.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://openspec.dev\&sz=32)

OpenSpec



From the CCattler repository:

```
# Install OpenSpec if it isn't installed already
npm install -g @fission-ai/openspec@latest

# Go to CCattler
cd ccattler

# Check that your working tree is clean
git status --short

# Initialize Claude Code integration
openspec init --tools claude
```

Because CCattler already has custom `.claude/` agents, skills, and workflows, inspect the generated files and Git diff before committing the installation. OpenSpec adds its own skills and commands under `.claude/`; it should complement, not replace, your existing setup.

Restart Claude Code after initialization.

Then the intended workflow is:

```
/opsx:explore Git-backed configuration sources

/opsx:propose git-backed-configuration-sources

# Review proposal, specifications, design and tasks

/opsx:apply

# Check implementation against the plan
# Install the optional verify workflow if desired.

/opsx:sync

/opsx:archive
```

The standard core profile includes explore, propose, apply, update, sync, and archive. Verification is an optional workflow.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://openspec.dev\&sz=32)

OpenSpec



## My verdict

I recommend adopting OpenSpec

For CCattler, I see three particularly valuable benefits:

1. Architectural discipline: changes must respect the fact-based architecture and its invariants.
2. Better Claude execution: complex changes become explicit, reviewable, and testable tasks rather than a long sequence of loosely connected coding instructions.
3. Long-term project memory: completed capability specifications preserve expected behavior as the project evolves.

But I would establish one rule:

OpenSpec describes what a change should accomplish. CCattler's design documents explain how the system is architected. Tests prove whether the implementation works.

Don't turn OpenSpec into another documentation repository or a mandatory ceremony for every small code edit.

I would initialize it, configure it for CCattler, and use Git-backed configuration sources as the first real change.