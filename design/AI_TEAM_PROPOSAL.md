# CCattler AI Agent Team — Proposal

**Created:** 2026-09-18
**Status:** COMPLETE — All 5 phases implemented (Tier 1-3 agents, 7 workflows, pre-commit hook, STATUS.md)

---

## Team Structure

### Tier 1 — Priority (create first)

#### lead-developer (opus)
- **Role:** Primary programming agent. Writes new features, complex cross-cutting implementations, architectural decisions.
- **Model:** opus — needs deep reasoning for 40K+ line codebase with fact-based architecture
- **Knows:** Full CLAUDE.md, all design principles, controller patterns, DSL grammar, etcd schema
- **Triggers:** New milestone work, feature implementation, complex bug fixes
- **Output:** Code changes, test updates, CLAUDE.md phase updates

#### project-manager (haiku)
- **Role:** Tracks milestones, generates status reports, prioritizes backlog, estimates effort, maintains roadmap.
- **Model:** haiku — structured text generation, low cost for frequent runs
- **Knows:** Milestones table, phase status, git log, issue tracker
- **Triggers:** On request, after milestone completion, weekly status
- **Output:** Status reports, roadmap updates, priority lists, effort estimates

#### code-reviewer (sonnet)
- **Role:** Reviews diffs for correctness bugs, style violations (naming rules, doc comments), architectural drift.
- **Model:** sonnet — analytical, good at pattern matching and rule enforcement
- **Knows:** Code style rules, architectural invariants, naming conventions, existing patterns
- **Triggers:** Before commits, on PR creation, on request
- **Output:** Structured findings with severity, file, line, fix suggestion

#### devops-engineer (sonnet)
- **Role:** Manages Ansible roles, Vagrantfile, Lima configs, release workflow, deployment testing.
- **Model:** sonnet — needs to understand infrastructure code and multi-host topology
- **Knows:** deploy/ directory, Ansible roles, Vagrant providers, release.yml, install scripts
- **Triggers:** Deployment changes, infrastructure updates, release preparation
- **Output:** Ansible playbook updates, deployment configs, release validation

### Tier 2 — High Value

#### technical-writer (haiku)
- **Role:** Keeps CLAUDE.md updated, writes CLI help text, maintains website docs, generates changelog.
- **Model:** haiku — text-heavy work, doesn't need deep code understanding
- **Knows:** CLAUDE.md structure, website content, CLI command list, git tags
- **Triggers:** After milestone completion, CLI changes, new features
- **Output:** Documentation updates, changelogs, help text

#### performance-engineer (sonnet)
- **Role:** Runs benchmarks, profiles hot paths, identifies allocations in reconciliation loops.
- **Model:** sonnet — needs to understand Go performance patterns
- **Knows:** Controller reconciliation loop, scheduler scoring, store operations, runtime adapters
- **Triggers:** After performance-sensitive changes, before releases, on request
- **Output:** Benchmark results, profiling reports, optimization PRs

#### refactoring-agent (sonnet)
- **Role:** Identifies tech debt, dead code, duplicated patterns across controllers. Proposes cleanup.
- **Model:** sonnet — analytical, good at cross-file pattern detection
- **Knows:** All controller implementations, common patterns, Go idioms
- **Triggers:** Between milestones, on request, after large features land
- **Output:** Refactoring PRs with before/after, dead code removal

### Tier 3 — Future

#### deployment-tester (sonnet)
- **Role:** Runs install script against Vagrant/Lima/bare-metal, validates cluster end-to-end.
- **Model:** sonnet — needs to drive infrastructure and validate results
- **Knows:** install.sh, install-demo.sh, Vagrant, Lima, expected cluster behavior
- **Triggers:** Before releases, after deployment changes
- **Output:** Pass/fail with logs, regression reports

#### dsl-designer (opus)
- **Role:** Designs new DSL constructs, ensures grammar consistency, handles edge cases.
- **Model:** opus — language design requires deep reasoning about ambiguity, precedence, composability
- **Knows:** Current DSL grammar, lexer/parser, AST types, fact compilation
- **Triggers:** When new DSL features are needed (new phases)
- **Output:** Grammar proposals, parser changes, test cases

---

## Existing Agents (keep as-is)

All existing agents use Sonnet and remain unchanged:

| Agent | Purpose |
|---|---|
| architecture-guard | Verify architectural invariants |
| code-quality | Static analysis against project standards |
| security-audit | mTLS, secrets, auth, zero-trust enforcement |
| test-runner | Run tests, report failures, coverage gaps |
| integration-test | End-to-end etcd + server + agent tests |
| dsl-tester | DSL parser edge cases |
| test-website | Website dev server testing |

---

## Orchestration Workflows

### 1. Feature Development Pipeline

```
Trigger: User requests a new feature or milestone item

lead-developer (opus)
    │
    ├── Implements the feature
    ├── Writes tests
    │
    ▼
code-reviewer (sonnet)          architecture-guard (sonnet)
    │                               │
    ├── Reviews for bugs            ├── Checks invariants
    ├── Checks naming/style         ├── Controller isolation
    │                               │
    └───────────┬───────────────────┘
                │
                ▼
          test-runner (sonnet)
                │
                ├── Runs go test ./...
                ├── Runs go vet ./...
                │
                ▼
          [if all pass]
                │
    ┌───────────┴───────────┐
    ▼                       ▼
technical-writer (haiku)  project-manager (haiku)
    │                       │
    ├── Updates CLAUDE.md   ├── Updates milestone status
    ├── Updates docs        ├── Reports completion
```

### 2. Pre-Commit Validation

```
Trigger: Before any commit

[parallel]
├── code-reviewer → diff review
├── code-quality → static analysis
├── architecture-guard → invariant check
├── test-runner → go test ./...
│
▼
[all must pass] → commit allowed
```

### 3. Release Pipeline

```
Trigger: Milestone complete, user requests release

project-manager (haiku)
    │
    ├── Generates changelog from git log
    ├── Validates all milestone items complete
    │
    ▼
[parallel]
├── test-runner → full test suite
├── integration-test → etcd + server + agent
├── security-audit → security posture check
│
▼
[all pass]
    │
    ▼
devops-engineer (sonnet)
    │
    ├── Updates Ansible roles if needed
    ├── Tests deployment on Vagrant
    │
    ▼
technical-writer (haiku)
    │
    ├── Updates CLAUDE.md milestone table
    ├── Generates release notes
    │
    ▼
[ready for tag + push]
```

### 4. Bug Fix Pipeline

```
Trigger: Bug reported or test failure detected

lead-developer (opus)
    │
    ├── Diagnoses root cause
    ├── Writes fix + regression test
    │
    ▼
code-reviewer (sonnet)
    │
    ├── Reviews fix for correctness
    ├── Checks for side effects
    │
    ▼
test-runner (sonnet)
    │
    ├── Runs affected package tests
    ├── Runs full suite
```

### 5. Tech Debt Sprint

```
Trigger: Between milestones, periodic cleanup

refactoring-agent (sonnet)
    │
    ├── Scans for dead code, duplication
    ├── Proposes cleanup changes
    │
    ▼
[parallel]
├── code-reviewer → reviews each change
├── architecture-guard → validates invariants preserved
│
▼
test-runner (sonnet)
    │
    ├── Validates no regressions
```

### 6. Deployment Validation

```
Trigger: Changes to deploy/, Ansible, install scripts

devops-engineer (sonnet)
    │
    ├── Reviews infrastructure changes
    │
    ▼
deployment-tester (sonnet)
    │
    ├── Runs install.sh on Vagrant
    ├── Validates cluster comes up
    ├── Runs smoke tests (apply config, check services)
    │
    ▼
[pass/fail report]
```

### 7. Regression Detection

```
Trigger: Any test failure on a previously-passing test

test-runner (sonnet)
    │
    ├── Detects failure in previously-passing test
    ├── Runs git bisect to find breaking commit
    │
    ▼
lead-developer (opus)
    │
    ├── Analyzes the breaking change
    ├── Writes fix + regression test
    │
    ▼
code-reviewer (sonnet)
    │
    ├── Reviews fix
    │
    ▼
test-runner (sonnet)
    │
    ├── Confirms fix + no new regressions
```

---

## Shared Context Protocol

Agents don't share conversation context. They need a structured way to communicate state.

### How agents share knowledge

1. **Git as source of truth** — All code changes go through git. Agents read `git log`, `git diff`, `git blame` to understand what happened.

2. **STATUS.md** — A machine-readable status file that agents read/write:
   ```markdown
   ## Current Work
   - [in-progress] M21: Feature X — lead-developer working, 3/7 items done
   - [blocked] M21: Feature Y — blocked on DSL grammar decision

   ## Recent Completions
   - [2026-09-18] M20 Phase 23 — Service Networking & Placement complete

   ## Known Issues
   - Performance regression in scheduler scoring (>100 nodes)
   - Flaky test: TestWatchCancellation (race condition)
   ```

3. **Agent output conventions** — Each agent writes structured output:
   - code-reviewer: findings as JSON in `.claude/reviews/`
   - test-runner: results summary in `.claude/test-results/`
   - project-manager: status updates in `STATUS.md`
   - lead-developer: changes in git commits with descriptive messages

4. **CLAUDE.md as shared spec** — All agents read CLAUDE.md for architectural rules, code style, and design philosophy. This is the single source of truth for "how things should be."

### What each agent reads vs writes

| Agent | Reads | Writes |
|---|---|---|
| lead-developer | CLAUDE.md, STATUS.md, git log, all source | Code, tests, CLAUDE.md phase updates |
| project-manager | CLAUDE.md milestones, STATUS.md, git log | STATUS.md, roadmap, reports |
| code-reviewer | CLAUDE.md style rules, git diff | Review findings |
| devops-engineer | deploy/, Ansible, release.yml | Infrastructure configs |
| technical-writer | CLAUDE.md, git tags, CLI help | Docs, changelog, help text |
| test-runner | Test output, git log | Test result summaries |
| architecture-guard | CLAUDE.md principles, all source | Invariant violation reports |
| security-audit | Security sections of CLAUDE.md, source | Security findings |

---

## Cost Guardrails

### Per-Agent Token Limits

| Agent | Model | Max Input Tokens | Max Output Tokens | Rationale |
|---|---|---|---|---|
| lead-developer | opus | 100K | 16K | Needs full codebase context for complex changes |
| dsl-designer | opus | 60K | 8K | Grammar + parser context, focused output |
| code-reviewer | sonnet | 40K | 4K | Diff + style rules, structured findings |
| devops-engineer | sonnet | 30K | 8K | Infrastructure files are smaller |
| performance-engineer | sonnet | 40K | 4K | Benchmark results + profiling output |
| refactoring-agent | sonnet | 60K | 8K | Needs cross-file context |
| project-manager | haiku | 20K | 4K | Status summaries, not code |
| technical-writer | haiku | 30K | 8K | Docs can be verbose output |

### Budget Controls

- **Daily budget cap:** Set a maximum daily spend across all agents. Start with $20/day, adjust based on actual usage.
- **Per-workflow cap:** Each workflow pipeline has a total token budget. Feature Development Pipeline: 200K input + 40K output max.
- **Escalation gate:** If an agent exceeds 80% of its token limit without completing, it stops and reports what it accomplished + what remains.
- **Model downgrade rule:** If an Opus agent's task turns out to be simpler than expected (e.g., a one-file change), it should suggest re-routing to Sonnet next time.
- **Idle agent detection:** If an agent runs but produces no meaningful output (no code changes, no findings, no status update), flag it — the trigger condition may need tuning.

### Monitoring

Track per-agent and per-workflow:
- Token consumption (input + output)
- Wall-clock time
- Output quality (did code-reviewer findings get accepted? did tests pass after lead-developer changes?)
- Cost per milestone (how much did M21 cost vs M20?)

---

## Agent-Specific Knowledge Scoping

Each agent receives only the CLAUDE.md sections relevant to its role. This reduces token consumption and keeps agents focused.

### Knowledge matrix

| Agent | CLAUDE.md Sections |
|---|---|
| lead-developer | ALL (full document — needs complete architectural context) |
| code-reviewer | Code Style Rules, Architectural Rules, Design Philosophy |
| architecture-guard | Design Philosophy, Architectural Rules, Architecture diagram, Controller Interface |
| security-audit | Security Model (all subsections), Multi-Tenancy isolation sections |
| devops-engineer | CLI commands, Implementation Phases (deployment-related), Runtime Adapters |
| test-runner | Deterministic Testing, Chaos Mode, Runtime Adapters |
| dsl-tester | Domain Language (DSL), Core Fact Types |
| project-manager | Current Status, Implementation Phases, Milestones table |
| technical-writer | Current Status, CLI, Domain Language, Milestones table |
| performance-engineer | Reconciliation loop, Controller Interface, Scheduler, Store Interface |
| dsl-designer | Domain Language (DSL), Core Fact Types, Extensibility |
| refactoring-agent | Code Style Rules, Architectural Rules, Controller Interface |

### Implementation approach

Each agent's `.md` definition includes an `instructions:` section that extracts or references only its relevant CLAUDE.md sections. For agents needing small subsets (project-manager, technical-writer), inline the relevant sections. For agents needing large subsets (lead-developer), reference the full file.

---

## Model Cost Optimization

| Model | Agents | % of Runs | % of Cost | Use For |
|---|---|---|---|---|
| Opus | lead-developer, dsl-designer | ~20% | ~60% | Creative work, complex reasoning, architecture |
| Sonnet | code-reviewer, devops-engineer, performance-engineer, refactoring-agent, deployment-tester, + 7 existing | ~60% | ~35% | Analysis, testing, review, infrastructure |
| Haiku | project-manager, technical-writer | ~20% | ~5% | Structured text, status tracking, docs |

**Cost reduction strategies:**
- Use Haiku for anything that reads + summarizes (status, docs, changelog)
- Use Sonnet for anything that reads + judges (review, audit, test analysis)
- Reserve Opus for anything that creates + reasons (new code, architecture, DSL design)
- Avoid running Opus agents for simple tasks — route to the right tier

---

## Implementation Order

1. **Phase A** — Create Tier 1 agents (lead-developer, project-manager, code-reviewer, devops-engineer)
2. **Phase B** — Create Feature Development Pipeline and Pre-Commit Validation workflows
3. **Phase C** — Create Tier 2 agents (technical-writer, performance-engineer, refactoring-agent)
4. **Phase D** — Create Release Pipeline and Tech Debt Sprint workflows
5. **Phase E** — Create Tier 3 agents and remaining workflows

Each phase should take one session. Start with Phase A when ready.

---

## Pre-Commit Validation Chain

Beyond the workflow diagram (#2 above), this requires a concrete hook in `.claude/settings.json` that runs quality gates before commits are allowed.

### Current hooks (already in place)
- **PreToolUse:** Block Co-Authored-By in commits, enforce annotated tags
- **PostToolUse:** Lint naming on Edit/Write, log commands on Bash

### New hook to add (Phase B)

A PreToolUse hook on `git commit` that spawns validation agents before the commit proceeds:

```
PreToolUse on "Bash(git commit *)"
    │
    ├── 1. go vet ./...          (fast, blocks on failure)
    ├── 2. go test ./...         (fast, blocks on failure)
    ├── 3. code-quality agent    (reviews staged diff for style violations)
    ├── 4. architecture-guard    (checks staged diff for invariant violations)
    │
    ▼
    All pass → commit proceeds
    Any fail → commit blocked with reason
```

### Trade-offs

- **Full chain (all 4 gates):** Safest, but slow (~2-3 min per commit). Best for milestone-completing commits.
- **Fast chain (just vet + test):** Quick (~30s), catches compilation and test failures. Good for iterative development.
- **Configurable:** Add a `CCA_COMMIT_GATES=fast|full` env var that controls which level runs.

### Implementation note

Claude Code hooks run shell commands, not agents. The pre-commit hook would run `go vet` and `go test` directly. For the agent-based gates (code-quality, architecture-guard), those would be part of the workflow pipeline that runs *before* the user asks to commit, not as a blocking hook (too slow and expensive for every commit).
