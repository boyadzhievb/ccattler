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
| Main-session context growth | `/context` before proposal and after release | before 378.9k (346.2k messages), after release 459.5k (425k messages): **+80.6k** — proposal, design questions, verifying the hand-backs, two testbed crash-loop checks, review handling, release, conversation (mid-point 421.2k after the first testbed check) | before 459.5k, after release 605k (570.5k messages): **+145.5k** — proposal, implementation, rework, three testbed runs, diagnosing a failed full run, a new graph section, conversation about graphs |
| Implementation tokens | subagent completion notice (M233); `/cost` delta (M234) | 156.8k subagent tokens, 66 tool calls (groups 0–2: 25 files, +643/−62, 2 commits) | in the main session (counted in its context growth below); first pass 1fcacba: 13 files |
| Wall time, "go" → ready for review | timestamps | 11.3 min (22:44 → 22:55) | about 5 min by the clock (10:59:46 → 11:04:44; continuing session, the proposal and code reading were done before) |
| Quality of the hand-back | main session re-ran build, vet, lint, tests; read the core file | all green on re-run; 6 doubts reported, 2 were real inconsistencies in the change's own spec/design (fixed in d41a1f1), 1 a pre-existing rollout gap, 1 a testbed risk to check | |
| Review findings | count and severity in tasks.md 4.1 / 2.2 | 2 real (1 medium: bail-out counted as crash; 1 low-medium: metrics gauge), 1 pre-existing bug → M238, 5 nits; review 110.2k tokens, 35 tool calls, 4.9 min | 3 real (1 medium: log dirs leak on graceful agent stop; 1 low: path traversal on the new read path; 1 headline: `cca logs <svc> --previous` skipped crash-looping instances), 3 low, nits; review 111.0k tokens, 32 tool calls, 4.5 min |
| Rework rounds after review | count | 1 round by the same subagent (resumed): both defects fixed failing-first plus all nits, 4.3 min; cumulative subagent tokens 186.9k (156.8k first round), 88 tool calls; main session re-verified green | 1 round in the main session: all fixed failing-first (6 new tests), plus a new "Container Logs" graph |
| Failed test/testbed runs caused by the implementation | count | 0 (crash-loop checks passed before and after rework) | 1 (testbed 2.1 failed on the CLI selection before the rework; passed after) |
| User interventions during implementation | count | | |

## Result (2026-11-17, both released: M233 v1.89.0-beta, M234 v1.90.0-beta)

| | M233 — Sonnet subagent implements | M234 — Opus main session implements |
|---|---|---|
| Main-session context growth | +80.6k | +145.5k |
| Implementer tokens outside the main session | 186.9k (subagent, two rounds) | — |
| Review | 110.2k (Sonnet) | 111.0k (Sonnet) |
| Real defects found by review | 2 | 3 |
| Rework rounds | 1 | 1 |
| Implementation testbed failures | 0 | 1 (CLI selection) |
| Implementation time | 11.3 min + 4.3 min rework | about 5 min + rework |

Findings:
- **Context:** the main session grew about 1.8× more when it implemented. That growth is the expensive part: every later turn re-reads it. M234's figure also includes work the trial does not separate (a failed full run diagnosed, a new graph section, questions about graphs), so the real difference from implementation alone is smaller than 65k but clearly in the subagent's favour.
- **Quality:** comparable. Both needed one review round; the Sonnet subagent (at medium effort) produced fewer real defects, and both implementers flagged inconsistencies in the change's own artifacts. Neither is clearly better; the review and the testbed catch what both miss.
- **Speed:** Opus in the main session was faster to a first commit; the subagent needs no supervision while it works, so the main session can run testbed checks meanwhile.
- **What stays in the main session regardless:** design questions with the user, testbed runs and their diagnosis, reading review findings and deciding, releases. These made up most of both milestones' main-session growth.
- **Limits:** two different milestones, n = 1 each; the effort setting changed mid-trial (M233 implementation at medium, Sonnet agents at high afterwards).

Main session effort, confirmed by the user 2026-11-17: **Opus at medium** — so M234 was "Opus, medium, main session".

**Third arm (user's question: what if Opus at low effort?):** M236 implemented by the `lead-developer` subagent with `model: opus`, `effort: low` passed on the call (agent file unchanged: Sonnet, high). Same measurements; compare with M234 (same model, medium, main session) and M233 (Sonnet, medium, subagent). M236 context before: 605k (after M234's release); 632.5k (598k messages) after the trial write-up, the M236 design questions and its proposal (+27.5k).

Preliminary recommendation (before the third arm): implement well-specified OpenSpec changes (tasks with failing tests named) with the `lead-developer` subagent on Sonnet at high effort; keep proposals, decisions, testbed work, diagnosis, review handling and releases in the main session; send rework back to the same subagent (SendMessage keeps its context). Debugging and investigations stay in the main session. Pending the user's decision.
