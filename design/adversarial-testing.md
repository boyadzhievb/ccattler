# Adversarial User Testing (M214)

Purpose: find what unit tests, reviews and the scripted deploy run miss, by having testers act as real users — careless and hostile ones — on a deployed cluster. Every confirmed finding becomes a failing automated test and a prioritized milestone item (bugs before features).

## Setup: two AI testers in parallel

Two Claude Code subagents on the cheapest model (`haiku`), started in the same turn in the background by the orchestrating session. Each gets its own credentials and its own goal. The orchestrator (the main session, cluster admin) never hands its admin token to a tester, verifies every finding itself, and records the results.

| Tester | Identity | Rights | Goal |
|---|---|---|---|
| A "careless developer" | user `adv-dev`, team `payments` | `api-writer` (workload create/update at cluster today; at `team/payments` after M179) | Session 1 (names and identity) and a small Session 4 (limits): break things by applying odd but plausible configurations |
| B "nosy neighbour" | user `adv-reader`, team `frontend` | `api-reader` | Session 2 (authorization): read or change anything outside its rights — secrets, other teams' services, admin endpoints |

Sessions 3 (killing processes, partitions, clock skew) and 5 (data plane from inside containers) need host access and stay with the orchestrator; testers never get SSH to the cluster VMs.

### Why this is safe enough

- **Forced-command SSH key per tester.** On the deploy host (m4800), each tester gets its own key in `authorized_keys` with `command="/home/bojan/adv/cca-as-<tester>.sh",no-port-forwarding,no-agent-forwarding,no-pty`. The wrapper runs only `cca "$@"` (taken from `SSH_ORIGINAL_COMMAND`, parsed into arguments, no shell evaluation) with `CCA_TOKEN` read from that tester's token file and `CCA_CONTEXT` set to that tester's context. Whatever a tester types, it can only run `cca` as its own user. Every call is appended to `~/adv/<tester>.log` with a timestamp.
- **Configuration files** a tester applies are uploaded through the same wrapper (`cca apply -f -` is not used; memory rule: no stdin deploys): the wrapper accepts `put-config <name>` reading the file body into `~/adv/<tester>/configs/<name>.cca` (name validated `[a-z0-9-]+`), and `cca apply` may only reference files in that directory.
- **Short-lived tokens:** `--ttl 4h`; revoked at the end (`cca user delete`).
- **Budget:** each tester has at most 150 commands and 60 minutes; the prompt says to stop and report at either limit.
- **Deploy cluster only** (m4800, `192.168.124.10/.20/.30`), never the e2e or HA sets; the GitHub runner must be idle, and no Deploy Testbed run may start during a session.
- **Restore afterwards:** `vmherd destroy` from the runner checkout, then a Deploy Testbed run.

## Step-by-step plan

### 0. Preconditions (orchestrator)

1. M210 committed, pushed by the user, Deploy Testbed green on it; cluster up with the default workloads (Airflow, gate-tls).
2. Runner idle (`gh run list --limit 5`, `systemctl status` of the runner on m4800).
3. Record the cluster baseline: `cca status`, `cca get instances`, `cca role list`, `cca secret list` (as admin, from the ctrl VM) into `~/adv/baseline.txt`.

### 1. Credentials (orchestrator, as cluster admin on the ctrl VM)

```
cca user create adv-dev --team payments
cca user create adv-reader --team frontend
cca role grant user:adv-dev --role api-writer
cca role grant user:adv-reader --role api-reader
cca user token adv-dev --ttl 4h      > adv-dev.token     (chmod 600)
cca user token adv-reader --ttl 4h   > adv-reader.token  (chmod 600)
cca role check user:adv-reader --capability secret.read   → must be denied
```

Then on m4800: copy the token files to `~/adv/` (0600), create a `cca` context per tester (server URL + cluster CA, no admin credentials), write the two wrapper scripts, generate one SSH key per tester (`~/.ssh/adv-dev`, `~/.ssh/adv-reader` on the orchestrator machine), and add the forced-command lines. Verify before starting: `ssh -i ~/.ssh/adv-reader bojan@192.168.100.71 whoami` prints the reader identity from `cca whoami`, and `ssh … 'ls /'` runs `cca ls /` (an error), not `ls`.

Seed data the testers can target (orchestrator, as admin): a `payments` service `checkout` with secret `db.password`, a `frontend` service `web`, a stateful service `ledger` (2 replicas), a gate `shop` with TLS, a connection `db`.

### 2. Launch both testers in parallel

One message with two `Agent` calls, `model: "haiku"`, `run_in_background: true`. Each prompt contains: its identity and rights; the exact way to run commands (`ssh -i ~/.ssh/adv-<t> bojan@192.168.100.71 <cca args>` and `put-config`); its goal and the session checklist from milestones.md Phase 214; the budget; and the rules:

- only use the given SSH key; never try other hosts, keys or users; never try to read the wrapper, token or other testers' files (report it as a finding if something lets you);
- no denial-of-service by volume beyond the Session 4 items (no loops of more than 50 creates);
- after every attempt record: command, what a correct system should do, what happened, evidence (output excerpt), suspected severity;
- a refusal with a clear error is a pass, not a finding; unclear errors, silent acceptance, crashes, other users' data, and inconsistent state are findings;
- end with a findings table and the list of attempts that passed.

Tester A starts with: the same name for service, job, cronjob, volume, gate and connection; job named like `ledger`; hierarchical names `checkout/secret/x`, `checkout/owner`; uppercase, `_`, `:`, unicode, 254-character names; re-applying `checkout` with fields removed; delete and re-create; then 30 services created and deleted (VIP reuse), one oversized configuration.

Tester B starts with: every `cca` command listed in `cca --help`; `cca secret get/set/list`, `cca describe service checkout`, `cca status`, `/api/state` and watch through any CLI command that reaches them; applying anything; `cca role grant` to itself; expired, revoked and malformed tokens (it may request a second short-TTL token from the orchestrator only through its report, never directly).

### 3. During the run (orchestrator)

Watch the cluster, not the testers: `cca get instances` and the agent/server journals every few minutes, to catch damage a tester does not notice (restarts of `ledger`, Airflow, gates). Stop both testers if a production-like workload breaks and record that as a finding.

### 4. After the run

1. Revoke: `cca user delete adv-dev`, `cca user delete adv-reader`; remove the forced-command lines and keys; keep the logs.
2. Diff the cluster against the baseline.
3. Verify every reported finding by reproducing it as admin from the logs; discard non-reproducible ones with a note.
4. For each confirmed finding: a failing automated test first, then a milestone item placed by severity (security and data loss first), recorded in milestones.md Phase 214.
5. Confirmed misuse cases go into `test/e2e/adversarial/` as scripted, asserted refusals for the Deploy Testbed workflow.
6. Restore the cluster: `vmherd destroy` from the runner checkout, then a Deploy Testbed run.

## Coverage checklist for every run (added 2026-11-12)

Adapted from a general QA checklist and TestRail's "QA best practices" to a CLI/API orchestrator (no UI, so device and browser checks become client checks: `cca` CLI vs raw API calls). Each run's tester prompts pick their items from here; the run report marks every item covered, skipped (why) or N/A.

| Area | What to try on CCattler | Run 1 |
|---|---|---|
| Smoke first | `status`, `get nodes`, `get services` succeed before anything else; a failed smoke stops the run | implicit |
| Happy path | Apply a small config (service + gate + secret grant), scale, rollout a new image, delete | partial (apply silently simulated → M218) |
| Persistence | After apply: restart `cca server`, an agent, etcd; the facts and instances are still there; `get`/`describe` agree | not covered |
| Downstream effects | VIP and DNS appear, endpoints follow instances, events and audit entries are written, gate serves the host | partial |
| Boundary values | instances 0 / 1 / limit / limit+1; names of 1, 63, 253, 254 chars; huge env values and secret sizes; TTL 0 / negative / years | partial (scale 100000 → M217) |
| Invalid formats | unicode, emoji, shell metacharacters and quotes in names, env, commands; letters in numeric fields; SQL-like strings; `../` paths | partial (names → M179) |
| Required fields blank | service without image, gate without service, secret set without value, scale without count | not covered |
| Interrupted actions | kill the CLI mid-apply; stop the API or partition a worker during apply, rollout and secret rotation; apply is sequential puts today — check for half-applied configs | not covered (needs orchestrator host access) |
| Rapid repeats and races | the same apply twice, two users applying the same service at once, scale up/down quickly, `restart` twice | not covered |
| Retry and "back" | re-run a failed command; Ctrl-C then retry; a stale CLI context after a server restart (HA token keys differ per replica today) | not covered |
| Unauthorized access | no token, expired, revoked, deleted user, malformed; raw API calls with `curl` that skip the CLI's own checks | covered (CLI); raw API not covered |
| Data isolation | another team's instance IDs in `logs`, `exec`, `restart`, `describe`; another team's names in API request bodies | partial (reads are cluster-wide today, M179/M181) |
| Regression | replay every confirmed finding of earlier runs (the Ansible replay suite) and the features next to the last fix | from run 2 |
| Performance / load | 300 services created and deleted; many concurrent watchers; large configs over one etcd transaction | partial (flood via scale) |
| Testing against the spec | for a milestone with an OpenSpec change, walk its `spec.md` scenarios as a user, not only as unit tests | from M179 |

Practices kept from the TestRail article:

- **Bug reports** carry reproduction steps, expected vs actual, environment (cluster, build, identity) — the transcripts already record command, output and exit code; the findings table adds expected and severity.
- **Metrics per run**: findings by severity, confirmed vs rejected, time from finding to fix, and findings that escaped an earlier run (regression escapes). Recorded under each run in milestones.md Phase 214.
- **Retrospective after each run**: what the testers missed, which prompts produced noise (e.g. scanner false positives), what to add to this checklist.
- **Separate environment**: only the deploy cluster, never the e2e or HA sets during CI (already a rule above).

## Cost and limits

`haiku` is the cheapest model; two testers × ≤150 commands each. Their reports are model output: every finding is verified by the orchestrator before it counts. A tester finding nothing does not mean the area is safe; the orchestrator's own Session 3 and 5 checks and later repeat sessions (after M179 and M181) continue the work.
