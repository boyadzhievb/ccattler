# Subagent Implementation Trial (M233 vs M234)

Decided 2026-11-16 (user: "yes"). Question: should a subagent implement an OpenSpec change's tasks so the main session stays small, and can a cheaper model do it?

## Setup

| | M233 (crash-loop back-off and restart count) | M234 (restart deletes the previous log) |
|---|---|---|
| Implementation | `lead-developer` subagent, model **Sonnet**, effort **medium** (default; neither the agent file nor the call set it — seen in the UI by the user), briefed with the change (proposal, design, tasks) only; started 22:44 | main session, **Opus**, as before |
| Proposal, decisions with the user | main session | main session |
| Reviews (`code-reviewer`, `security-audit` when needed) | subagents, **Sonnet, effort high** (their agent files say `model: sonnet`; earlier notes said Opus, which was wrong — every review so far ran on Sonnet) | same as M233 |

Change during the trial (user, 2026-11-16, after M233's implementation): every Sonnet agent file got `effort: high`. M233's implementation therefore ran at **medium**; its reviews and everything after run at **high**. A later subagent implementation would run at high — compare with that in mind.
| Testbed runs, release | main session | main session |

The subagent's brief: failing tests first, then the fix, docs and graphs, gofmt + golangci-lint + unit tests, commit, short report (files, test results, anything it could not do). It does not touch the testbed, push or release.

Both milestones are bug fixes of similar size in `agent/` and `runtime/`. They are not identical; read the comparison with that in mind.

## Measurements

| Metric | How | M233 | M234 |
|---|---|---|---|
| Main-session context growth | `/context` before proposal and after release | before: 378.9k total, 346.2k messages (2026-11-16, after the M233 design questions; earlier baseline 339.7k during M235) | |
| Implementation tokens | subagent completion notice (M233); `/cost` delta (M234) | 156.8k subagent tokens, 66 tool calls (groups 0–2: 25 files, +643/−62, 2 commits) | |
| Wall time, "go" → ready for review | timestamps | 11.3 min (22:44 → 22:55) | |
| Quality of the hand-back | main session re-ran build, vet, lint, tests; read the core file | all green on re-run; 6 doubts reported, 2 were real inconsistencies in the change's own spec/design (fixed in d41a1f1), 1 a pre-existing rollout gap, 1 a testbed risk to check | |
| Review findings | count and severity in tasks.md 2.2 | | |
| Rework rounds after review | count | | |
| Failed test/testbed runs caused by the implementation | count | | |
| User interventions during implementation | count | | |

## Result

(to fill in after both milestones are released)
