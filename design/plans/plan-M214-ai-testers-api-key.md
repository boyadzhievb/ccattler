# M214 — AI testers in CI: Anthropic API key setup (deferred)

**Status:** to set up (decision 2026-11-12: wanted — the AI testers find what scripted tests miss). Steps A–C are done by the repository owner; D is a code task. The run stays manual-only (`workflow_dispatch` + required reviewer).

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
| Service account API key, scoped to `ccattler-ci` (start here) | A key linked to a CI service account and scoped to the workspace; it can spend only there, under that workspace's limits, and expires after 7 days | A stored secret, short-lived | A new key per test campaign (steps A4–A5) |

- [x] GitHub Actions is a supported federation identity provider (Authentication / Workload Identity Federation docs, checked 2026-11-12).
- [x] API keys take an expiration at creation (3 h, 1 d, 7 d, 30 d, custom, Never); use 7 days per campaign (step A5).
- Note: for the key option, use a **service account key** (step A4–A5), not the legacy workspace key.

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

## Exact setup steps (API-key option)

Done by a person; nothing here is automated. Workload identity federation can replace steps A4 and B3 later (section 2).

### A. Claude Console (platform.claude.com, as an organization admin)

Checked against the Claude docs on 2026-11-12: [Workspaces](https://platform.claude.com/docs/en/manage-claude/workspaces), [Rate limits](https://platform.claude.com/docs/en/api/rate-limits), [Authentication](https://platform.claude.com/docs/en/manage-claude/authentication). Only organization admins can create workspaces and service accounts.

**A1. Create the workspace**

1. Open **Workspaces**: `platform.claude.com/settings/workspaces`.
2. Click **Create workspace** (opens `platform.claude.com/settings/workspaces/new`).
3. Name: `ccattler-ci`; pick any color. Click **Create workspace**.
4. Note its ID from the **ID** column (`wrkspc_…`) — used to verify later which workspace a request hit (`anthropic-workspace-id` response header).

**A2. Cap the spend**

Per-workspace spend limits are an early-access preview for Console organizations (Admin API docs) — on 2026-11-12 the `ccattler-ci` panel showed **no Spend limits** entry. Use what exists:

1. **If the workspace panel shows Spend limits** (Workspaces → select `ccattler-ci` → **Spend limits**): set e.g. $20/month with alerts at 50 % and 80 %. At the cap requests fail with HTTP 400 "You have reached your specified workspace API usage limits".
2. **Otherwise** the hard ceiling comes from the workspace rate limits (A3) times the job timeout (75 min, section 4). With 200,000 input and 40,000 output tokens per minute on Haiku 5.5 ($0.10 / $0.50 per million tokens), one run can spend at most 15M input + 3M output tokens ≈ **$3**, enforced by the API and by GitHub, whatever the testers do.
3. Optionally also set the **organization** spend limit at `platform.claude.com/settings/spend-limits` (**Adjust limit** / **Set limit**) — it caps all usage in the organization, not only CI, so choose it with your other usage in mind.
4. Watch spend per run on the **Usage** page filtered to `ccattler-ci`.

**A3. Lower the rate limits**

1. On **Workspaces**, select `ccattler-ci`; the side panel shows **Rate limits →** marked *Org default* (and **Security →**, no Spend limits — seen 2026-11-12). Click **Rate limits →** (or open `platform.claude.com/usage/limits` and pick `ccattler-ci` in the **Workspace** dropdown).
2. Afterwards the panel should no longer show *Org default*.
3. Click **Edit** next to **Claude Haiku 5.5** and set requests per minute, input tokens per minute and output tokens per minute well below the organization's, e.g. 50 RPM / 200,000 ITPM / 40,000 OTPM (two testers need a few requests per minute). Unset limits default to the organization's; the organization limit always applies on top.
4. Optionally do the same for other models so a misconfigured run cannot use a more expensive model at full rate.

**A4. Create a service account (the CI identity)**

The docs recommend a service account key for CI: it belongs to the workload, not to a person, and stops working when the service account is archived. (The older "workspace key" type is legacy.)

1. Open **Service accounts**: `platform.claude.com/settings/service-accounts`; create a service account named `ccattler-ci-adversarial`.
2. In its menu (or on its page) choose **Add to workspace** → `ccattler-ci`.

**A5. Create the key**

1. Open **API keys**: `platform.claude.com/settings/keys` → **Create key**.
2. Name: `ccattler-ci-adversarial-<YYYYMMDD>` (the campaign date).
3. **Linked account**: the service account `ccattler-ci-adversarial` (not yourself).
4. **Workspace**: scope the key to `ccattler-ci` (it then works only there and needs no workspace header).
5. **Expiration**: **7 days** (presets: 3 hours, 1 day, 7 days, 30 days, custom, Never — never choose Never here). Expiration cannot be changed later; an expired key returns 401 and must be replaced.
6. Copy the key once into step B3. Do not paste it into chat, a repository file or a command line.
7. Optional: turn on IP allowlisting for the organization ([IP allowlist](https://platform.claude.com/docs/en/manage-claude/ip-allowlist)) so a leaked key works only from the runner's egress address.

**A6. Later: replace the key with federation**

GitHub Actions is a supported identity provider for Workload Identity Federation ([guide](https://platform.claude.com/docs/en/manage-claude/workload-identity-federation)): service account + federation issuer + federation rule in the Console, then the job exchanges its GitHub OIDC token for a short-lived token — no key to store. Worth doing once the manual runs are routine.

### B. GitHub (repository `boyadzhievb/ccattler`)

**Done 2026-11-12**, verified through the API: environment `ai-adversarial` with required reviewer `boyadzhievb` and deployment branch `master` only; `ANTHROPIC_API_KEY` is an environment secret (not a repository secret). Console steps A1, A3–A5 done by the owner the same day (no workspace spend limit available; cost bounded by A3 rate limits × job timeout).

Web UI: **Settings → Environments → New environment** `ai-adversarial` → tick **Required reviewers** and add yourself → **Deployment branches and tags**: *Selected branches*, add `master` → **Environment secrets → Add secret** `ANTHROPIC_API_KEY` = the key from A4.

Or with `gh` (the secret is read from the prompt, never from the command line):

```bash
OWNER_ID=$(gh api user --jq .id)
gh api -X PUT repos/boyadzhievb/ccattler/environments/ai-adversarial --input - <<EOF
{"reviewers":[{"type":"User","id":$OWNER_ID}],
 "deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}
EOF
gh api -X POST repos/boyadzhievb/ccattler/environments/ai-adversarial/deployment-branch-policies -f name=master
gh secret set ANTHROPIC_API_KEY --env ai-adversarial    # paste the key when prompted
gh api repos/boyadzhievb/ccattler/environments/ai-adversarial --jq '.protection_rules[].type'   # expect: required_reviewers, branch_policy
```

Never add `ANTHROPIC_API_KEY` as a repository-level secret.

### C. Runner host (m4800, as the runner user)

1. Install the Claude Code CLI: `npm install -g @anthropic-ai/claude-code` (needs Node.js), then `claude --version`.
2. Check the headless flags on that version: `claude --help` (`-p`, `--model`, `--max-turns`, `--allowedTools`, `--output-format`).
3. Provision the tester wrapper and SSH keys (`~/adv/bin/cca-as.sh`, forced-command keys) — today by hand as in M214 run 1; to be moved into `scripts/setup-deploy-host.sh` (open item below).

### D. Workflow (code task, not yet written)

`.github/workflows/ai-adversarial.yml`: `on: workflow_dispatch` only; one job with `environment: ai-adversarial`, `runs-on: [self-hosted, deploy]`, `timeout-minutes: 75`; steps: create the two tester users with 1 h tokens → run both testers with `claude -p --model haiku --max-turns <n>` restricted to the tester SSH key → upload `adv-*.md` transcripts and the findings report as artifacts → delete the users (in an `if: always()` step).

### E. After each test campaign

The key expires by itself after 7 days (A5). To stop it earlier: **API keys** page → **Disable** (reversible, **Re-enable** restores it) or **Delete** (permanent). Next campaign: create a new key (A5) and replace the environment secret (`gh secret set ANTHROPIC_API_KEY --env ai-adversarial`).

## 5. Monitoring and open items

- Spend per run: the Console's usage and cost pages filtered to `ccattler-ci`; with an Admin API key, the usage and cost report endpoints can be pulled into the run's report automatically.
- Each run uploads the tester transcripts (`adv-dev.md`, `adv-reader.md`, Markdown: command, full output, exit code) and the findings report as workflow artifacts.
- A finding from an AI run still goes through the manual path: reproduce → failing test → milestone item → a replay case for the Ansible suite.

Open before enabling:

- [ ] Federation vs key decided (section 2).
- [ ] Wrapper and tester SSH keys provisioned by `scripts/setup-deploy-host.sh` instead of by hand (today they were created manually on m4800 for run 1).
- [ ] Tester prompts moved from the session into `test/e2e/adversarial/prompts/`.
- [ ] Spend cap and rate limits set on the workspace.
