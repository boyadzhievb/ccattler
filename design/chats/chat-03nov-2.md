Agreed. Your revised approach is better, and I would proceed with it. I would make one important additional correction to the release sequence so we don't leave broken milestone links.

## 1. Adopt your documentation split

I would make this the permanent rule:

| Location                            | Responsibility                                                              |
| ----------------------------------- | --------------------------------------------------------------------------- |
| `CLAUDE.md`                         | Engineering rules and architectural invariants                              |
| `design/`                           | Architecture, implementation details, and cross-component flows             |
| `design/runtime-invariant-flows.md` | End-to-end chains across components                                         |
| `openspec/specs/`                   | Behavioral requirements for maintained capabilities                         |
| `openspec/changes/`                 | Proposals, specifications, design decisions, and tasks for work in progress |
| `milestones.md`                     | Roadmap and milestone tracking                                              |

Your distinction around Flows 11, 24 and 25 is important. Keep them in their current location. New capability specifications should link to those flows where relevant; the flow document should link back to the capability specification instead of copying its requirements.

No migration of existing flows.

## 2. Use a minimal, verified OpenSpec configuration

I checked the current official configuration reference. Both `rules` and `operations.apply.guidance` are supported fields.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://openspec.dev\&sz=32)

OpenSpec+1



I would use:

```
schema: spec-driven

context: |
  Read CLAUDE.md and relevant design/ documents before planning or implementing.
  Do not introduce Kubernetes object ontology into CCattler's user-facing model.

rules:
  proposal:
    - State goals, non-goals, affected capabilities, and related milestones.

  specs:
    - Specify observable behavior, failure cases, and recovery requirements.
    - Link to existing cross-component flows instead of duplicating them.

  design:
    - Explain how the change fits the existing architecture.
    - Reference existing design documents rather than reproducing them.

  tasks:
    - Include relevant unit, integration, and end-to-end tests.
    - Include required documentation and call-graph updates.

operations:
  apply:
    guidance:
      - Inspect the implementation and relevant call graphs before editing.
      - Follow CLAUDE.md and report actual test results.
```

This keeps the permanent rules in one place rather than copying CCattler's architectural invariants into OpenSpec.

## 3. Pin the version and inspect `.claude/` first

The current npm package is OpenSpec 1.16.0. Its npm installation requires Node.js 22.12.0 or newer.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://www.npmjs.com\&sz=32)

npm+1



For a pinned installation:

```
npm install -g @fission-ai/openspec@1.16.0
openspec --version
```

Then, from a clean CCattler working tree:

```
openspec init --tools claude
git status --short
git diff -- .claude/
```

The current setup documentation describes six skills and six commands for the standard Claude Code integration.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://openspec.dev\&sz=32)

OpenSpec+1



I would specifically review both file-level and workflow-level conflicts.

My proposed ownership rule:

- OpenSpec drives proposal, specification, tasks, implementation, and archiving for OpenSpec-managed capabilities.
- `feature-development.js` must not independently run a competing planning and implementation pipeline for the same change.
- Existing project-manager and technical-writer agents remain useful.
- Existing release workflows remain responsible for release execution.

I would preserve existing files unless an actual conflict is identified. No wholesale replacement of `.claude/`.

## 4. Make GitOps the first OpenSpec change

Change: `git-backed-configuration-sources`

First maintained capability specification:

`openspec/specs/git-backed-configuration-sources/spec.md`

Initial change artifacts:

```
openspec/changes/git-backed-configuration-sources/
├── proposal.md
├── specs/
│   └── git-backed-configuration-sources/
│       └── spec.md
├── design.md
└── tasks.md
```

Illustrative structure for the proposed change.

I would make M201's existing ownership and pruning requirements the starting point, rather than asking Claude to invent a new design from scratch.

In particular, the specification must explicitly resolve all nine questions:

| Area                      | Required decision                                            |
| ------------------------- | ------------------------------------------------------------ |
| Git outage                | Preserve last successfully applied state                     |
| Invalid revision          | Do not replace last good desired state                       |
| Source ownership          | Prevent cross-source deletion and overwrites                 |
| Partial application       | Define resumability and idempotency                          |
| Concurrent revisions      | Pin each synchronization to an exact commit                  |
| Synchronization vs health | Report them independently                                    |
| `cca scale`               | Define manual override precedence and lifecycle              |
| Manual `cca apply`        | Define `cli` ownership and conflict/adoption behavior        |
| etcd transactions         | Guarantee resource-group atomicity within transaction limits |

The last item deserves particular attention. A resource with more fact changes than a single etcd transaction can accommodate cannot simply be declared atomic. The design must enforce a maximum, or introduce a staged generation-and-promotion mechanism. Source-wide application should be resumable even when individual resource groups are atomic.

## 5. Correct the archive and release sequence

I agree with your release ordering:

1. All implementation tasks complete.
2. Verification and relevant tests pass.
3. OpenSpec archives the change and merges its specifications.
4. M201/M202 documentation links are updated.
5. Run `scripts/release.sh`.

One detail: OpenSpec moves archived changes into a date-prefixed directory, such as `openspec/changes/archive/2026-11-03-git-backed-configuration-sources/`.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://openspec.dev\&sz=32)

OpenSpec+1



Therefore, milestone links to the active change directory will become stale.

I suggest updating M201 and M202 after archiving so they reference the permanent capability specification and, where useful, the dated archive containing the original proposal and tasks.

That gives us a clean history without broken links.

## The execution order I would approve

1. Install OpenSpec 1.16.0, pinned.
2. Run initialization against a clean tree.
3. Review the complete `.claude/` diff before committing anything.
4. Establish which workflow drives OpenSpec-managed feature work.
5. Add the minimal `openspec/config.yaml`.
6. Draft GitOps specifications using M201/M202 and existing architecture as inputs.
7. Review the ownership, override, pruning, and transaction semantics before implementation.

My decision: adopt OpenSpec, use GitOps as the pilot, and migrate none of the existing documentation.

I haven't installed it or changed your repository. The version and configuration points above are checked against current documentation; the `.claude/` collision still needs to be evaluated against the actual initialization diff.