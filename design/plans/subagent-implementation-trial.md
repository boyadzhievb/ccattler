# Subagent Implementation Trial (M233 vs M234)

Decided 2026-11-16 (user: "yes"). Question: should a subagent implement an OpenSpec change's tasks so the main session stays small, and can a cheaper model do it?

## Setup

| | M233 (crash-loop back-off and restart count) | M234 (restart deletes the previous log) |
|---|---|---|
| Implementation | `lead-developer` subagent, model **Sonnet**, briefed with the change (proposal, design, tasks) only | main session, **Opus**, as before |
| Proposal, decisions with the user | main session | main session |
| Reviews (`code-reviewer`, `security-audit` when needed) | subagents, **Opus** | subagents, **Opus** |
| Testbed runs, release | main session | main session |

The subagent's brief: failing tests first, then the fix, docs and graphs, gofmt + golangci-lint + unit tests, commit, short report (files, test results, anything it could not do). It does not touch the testbed, push or release.

Both milestones are bug fixes of similar size in `agent/` and `runtime/`. They are not identical; read the comparison with that in mind.

## Measurements

| Metric | How | M233 | M234 |
|---|---|---|---|
| Main-session context growth | `/context` before proposal and after release | | |
| Implementation tokens | subagent completion notice (M233); `/cost` delta (M234) | | |
| Wall time, "go" → ready for review | timestamps | | |
| Review findings | count and severity in tasks.md 2.2 | | |
| Rework rounds after review | count | | |
| Failed test/testbed runs caused by the implementation | count | | |
| User interventions during implementation | count | | |

## Result

(to fill in after both milestones are released)
