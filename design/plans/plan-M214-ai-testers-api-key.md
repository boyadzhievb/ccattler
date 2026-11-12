# M214 — AI testers in CI: Anthropic API key setup (deferred)

**Status:** deferred (decision 2026-11-12) — not being set up now. Review this before enabling the manual AI run.

Context: milestone M214 (adversarial user testing), `design/adversarial-testing.md`. Two kinds of runs:

| Run | Trigger | Needs an Anthropic key |
| --- | --- | --- |
| Ansible replay of confirmed cases (`deploy/ansible/adversarial.yml`) | Every green Deploy Testbed run, gating | No |
| Two AI testers (headless `claude -p`, model haiku) | Manual only (`workflow_dispatch`, approved by a person), non-gating | Yes |

Everything below applies only to the manual AI run.

## 1. A dedicated workspace with a hard spend cap

All tester spend goes into one Claude Console workspace, `ccattler-ci`, separate from personal use.

- **Spend limit:** a monthly limit on the workspace (Console → workspace settings → Limits), e.g. $20. Haiku 5.5 costs $0.10 per million input tokens and $0.50 per million output tokens, so one two-tester run should cost cents; the cap is a backstop, not the budget.
- **Rate limits:** set the workspace's limits below the organization's, so a runaway loop is throttled instead of spending.

## 2. Credentials: no long-lived key if possible

| Option | How it works | Leak risk | Upkeep |
| --- | --- | --- | --- |
| Workload identity federation (preferred) | GitHub's OIDC token is exchanged for a short-lived Anthropic token; a federation rule ties it to this repository, its environment and the `ccattler-ci` workspace | Nothing stored to leak | The first federation rule is created by a person in the Console |
| Workspace-scoped API key (fallback) | A key created inside `ccattler-ci`; it can spend only there, under that workspace's limits | A stored secret | Deactivate it after each test campaign (Console or Admin API); create a new one next time |

- [ ] Verify in the Console that GitHub Actions is accepted as a federation issuer before choosing the first option.
- [ ] Check whether API keys can be given an expiry date — not confirmed, so this plan assumes manual deactivation.

## 3. Protecting it in GitHub

- A GitHub **environment** `ai-adversarial` with **required reviewers** (the repository owner): every run waits for approval — "manually run and controlled" enforced by GitHub, not by convention.
- The key (or the federation IDs) is a secret of **that environment only**, never a repository-wide secret.
- The workflow triggers only on `workflow_dispatch` — never on push, pull request or schedule.
- It runs on the self-hosted `deploy` runner (m4800), which already reaches the deploy cluster; GitHub-hosted runners never see the key.

## 4. Per-run caps

| Limit | Value | Enforced by |
| --- | --- | --- |
| Job wall time | 75 min | `timeout-minutes` on the job (GitHub kills it) |
| Model | haiku | `claude -p --model haiku` |
| Agent turns | fixed cap | `claude -p` turn-limit flag (check `claude --help` for its exact name) |
| Cluster calls per tester | 150 | tester prompt + wrapper log review |
| Time per `cca` command | 60 s | `timeout 60` in the forced-command wrapper (`~/adv/bin/cca-as.sh`) |
| Cluster credentials | 1 h tokens, users deleted at the end | the job creates `adv-dev` / `adv-reader` and removes them |
| What testers can run | allowlisted `cca` API commands only, no host paths | forced-command SSH keys + wrapper |

## 5. Monitoring and open items

- Spend per run: the Console's usage and cost pages filtered to `ccattler-ci`; with an Admin API key, the usage and cost report endpoints can be pulled into the run's report automatically.
- Each run uploads the tester transcripts (`adv-dev.md`, `adv-reader.md`, Markdown: command, full output, exit code) and the findings report as workflow artifacts.
- A finding from an AI run still goes through the manual path: reproduce → failing test → milestone item → a replay case for the Ansible suite.

Open before enabling:

- [ ] Federation vs key decided (section 2).
- [ ] Wrapper and tester SSH keys provisioned by `scripts/setup-deploy-host.sh` instead of by hand (today they were created manually on m4800 for run 1).
- [ ] Tester prompts moved from the session into `test/e2e/adversarial/prompts/`.
- [ ] Spend cap and rate limits set on the workspace.
