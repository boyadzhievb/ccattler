# Tasks

Milestone M275. Design: [design.md](design.md). No spec delta (`skip_specs: true`, a pure move), so there are no new
scenario tests to write first; group 1 records the baseline that the move is checked against instead.

## 1. Baseline (before any move)

- [x] 1.1 Start on a clean tree after M267 rework 3f is committed (`git status --short` empty). Record the base
  commit (`BASE`) and `wc -l types/keys.go`.
  Result (2026-11-24): `BASE` = 8d43699 (the M275 proposal commit, on top of M267 3g 6dbc3a4). Work done in a git
  worktree (branch `m275-split-type-keys`) because M267 rework 3g was in progress in the main tree. `git status
  --short` printed nothing at the start; `git rev-parse 8d43699` = 8d43699482b9e5acb5c7f7aacfe0634dab7be76d; `git show
  8d43699:types/keys.go | wc -l` = `2634`.
  BASE re-recorded 2026-11-24 after the rebase onto 6dbc3a4; types/keys.go is identical in ce45772 and 8d43699 (`git
  diff --stat ce45772 8d43699 -- types` empty).
- [x] 1.2 Record the baseline on `BASE`: `go build ./...` passes, `go test -count=1 ./types ./integration -run
  'TestDocCrossCheck'` passes, and the declaration count (`grep -c '^func ' types/keys.go`). Apply the D1 rules to
  the declarations on `BASE` and record the per-file counts; record every difference from the counts in design.md
  D1 (declarations added since 92feee5) with the file the rules give them.
  Result: `go build ./...` exit 0. `go test -count=1 ./types ./integration -run 'TestDocCrossCheck'`: `ok types
  [no tests to run]`, `ok integration` (5 PASS: FactPrefixesExist "verified 10 fact key prefixes", StoreInterface,
  RuntimeAdapters, ControllerInterface, CLICommandsExist). `grep -c '^func '` = `336`. `go/parser` listing: 377
  top-level declarations (376 plus the `import`). D1 rules applied in order to each first name (helper output checked
  against an independent re-application of the rules: identical, 0 unmatched): keys.go 4 (import, `Root`, prefix
  block, shared scan block), keys_cloud 40, keys_service 39, keys_instance 33, keys_placement 31, keys_secret 31,
  keys_service_scale 28, keys_volume 27, keys_job 24, keys_rollout 22, keys_route 22, keys_tenant 20, keys_node 19,
  keys_network 14, keys_probe 12, keys_auth 11. No difference from design.md D1: `git diff --stat 92feee5 8d43699 --
  types` is empty, so nothing was added since 92feee5.

## 2. Move

- [x] 2.1 Create the 15 `types/keys_*.go` files of D1, each with the copyright/SPDX header, `package types` and
  `import "fmt"` only where it is used. Move each declaration with its doc comment unchanged and in its original
  relative order. Drop the `// ----` separator lines and keep every floating comment before its declaration (D2,
  as changed on 2026-11-24; the first wording dropped floating comments too). Verify `go build ./...` and
  `go vet ./types`.
  Result: the 15 files were generated from `git show 8d43699:types/keys.go` by copying each declaration's line span
  (doc comment included, from `go/parser`) in base order, one blank line between declarations; header lines 1-2
  copied from keys.go; `import "fmt"` added only when the file body contains `fmt.` (all 15 subject files; keys.go
  has none). Rework after the 2026-11-24 D2 change (floating comments kept): every floating comment of the base
  (the non-blank lines in the gap before a declaration) is kept byte-identical and placed directly before the base
  declaration it preceded, in that declaration's new file, with one blank line before the declaration so it stays a
  floating comment; only the 10 `// ----…` separator lines are removed. 34 floating lines kept, in 15 groups:
  keys_cloud.go :8-12 (Cloud identity, first in file, after the import) and :141-147 (Cloud controller manager),
  keys_instance.go :118-120, keys_node.go :80-81, keys_placement.go :130, :162, :192, keys_probe.go :16-18 and
  :76-77, keys_rollout.go :50 (Derived key functions), keys_route.go :8 (first in file), keys_secret.go :24,
  keys_service.go :186-188 and :214-215, keys_tenant.go :131. Extra check beyond D3 (`declcheck.sh`): each new file
  re-parsed, and every declaration's text (doc comment included) compared byte-for-byte (`cmp`) and in order
  against its base span: `declarations checked: 376`, no mismatch, so no floating comment merged into a doc
  comment. `go build ./...` exit 0; `go vet ./types` exit 0.
- [x] 2.2 `keys.go` keeps `Root`, the layer-prefix block and the shared scan block (D2), plus the file map comment
  under the import (one line per file and subject). Verify `wc -l types/keys.go` is under 250 and
  `TestDocCrossCheckFactPrefixesExist` passes unchanged.
  Result: keys.go has no `fmt` use, so no import; the file map sits under `package types` (one line per file, the
  `keys_service.go` subject wraps to a second line). `wc -l types/keys.go` = `191`. `go test -count=1 -v ./integration
  -run TestDocCrossCheckFactPrefixesExist`: "verified 10 fact key prefixes from CLAUDE.md exist in types/keys.go",
  `--- PASS`; test file untouched.
- [x] 2.3 Any declaration placed by hand against the D1 rules: list it here with one line of reasoning (none
  expected).
  Result: none. All 377 declarations placed by the rules; the full assignment list was read by subject and no
  declaration is in the wrong subject file.
- [x] 2.4 Run the D3 line-multiset check against `BASE` and paste its output here. Explain every remaining line as
  one of the allowed differences (header, `package`, `import`, file map, dropped separator or floating comment); any
  other line fails this task. Verify `git diff --stat "$BASE"` lists only `types/keys*.go` and the docs of group 3.
  Result (rework after the 2026-11-24 D2 change, floating comments kept): `BASE=8d43699`, the D3 command exits 1
  with 120 lines of output (10 `<`, 79 `>`). Rerun with `BASE=8d43699` after the rebase, before the file-map
  wording fix: `diff` against the earlier pasted ce45772 output printed nothing (identical). Output below is after
  the file-map wording fix (keys_cloud.go "cloud routes", keys_placement.go "cluster autoscale bounds"); it differs
  from the previous paste only in those two `>` file-map lines:

  ```text
  12,21c12
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  < // ---------------------------------------------------------------------------
  ---
  > //
  420a412
  > // constructors, scan prefixes and key constants are in its own file:
  448a441,455
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  > // Copyright (C) 2026 CCattler Contributors
  477a485
  > // Fact-store keys are split by subject. This file holds what every subject
  843a852,865
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  > import "fmt"
  1187a1210,1224
  > //	keys_auth.go          roles, grants, groups, policies, users
  > //	keys_cloud.go         cloud provider, load balancers, cloud routes, cloud identity, credentials
  > //	keys_instance.go      instance facts
  > //	keys_job.go           jobs and cron jobs
  > //	keys_network.go       endpoints, subnets, allocations, VIPs, DNS, network policy
  > //	keys_node.go          nodes, leases, utilization, drain, pressure
  > //	keys_placement.go     placement, groups, priority, unplaced demand, capacity requests, cluster autoscale bounds
  > //	keys_probe.go         probe config and results
  > //	keys_rollout.go       update pacing, disruption budget, rollout state
  > //	keys_route.go         routes and route TLS key secret names
  > //	keys_secret.go        secret generations, delivery, gate keys, keyring
  > //	keys_service.go       the rest of a service: image, command, logging, lifecycle, expose,
  > //	keys_service_scale.go instance counts, scaling policy, autoscaler intents, metrics, scale-to-zero
  > //	keys_tenant.go        tenants, quotas, usage, export/import
  > //	keys_volume.go        volumes
  1240a1278,1292
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  > package types
  1622a1675
  > //	                      resources, health, tags, stateful, config, secrets, connections, init steps
  2104a2158
  > // shares: Root, the layer prefixes and the shared scan prefixes. Each subject's
  2106a2161,2175
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  > // SPDX-License-Identifier: GPL-3.0-only
  ```

  Classification. Removed (10): the 10 `// ----…` separator lines (base :1446/:1450, :1508/:1511, :1527/:1533,
  :1634/:1636, :1867/:1875), the only allowed removal after the D2 change. Added (79): 15 copyright and 15 SPDX
  lines (16 files, base had one each); 15 `package types` (16 files vs 1); 14 `import "fmt"` (15 files vs 1); 20
  file-map lines in keys.go (3 prose lines, 1 `//` blank-comment line, 15 file lines and the `keys_service.go`
  continuation). No other line. `git diff --stat 8d43699` (committed move aaa1326 plus the working tree): 23 files,
  `design/graphs/AUDIT_GRAPH.md`, `design/graphs/OBSERVABILITY_GRAPH.md`, `design/graphs/STATEFUL_SERVICES_GRAPH.md`,
  `docs/design-connection-block.md`, `openspec/changes/split-type-keys/{design,proposal,tasks}.md`, `types/keys.go`
  and the 15 `types/keys_*.go`; nothing else.

## 3. Docs

- [x] 3.1 Graphs: `AUDIT_GRAPH.md:548-549` and `OBSERVABILITY_GRAPH.md:388` point at the new `keys.go` lines;
  `STATEFUL_SERVICES_GRAPH.md` section 7 heading and `:352` row point at `keys_service.go`, `keys_instance.go`,
  `keys_network.go` and `keys_volume.go` with current line numbers. Verify `grep -rn 'keys\.go:' design/graphs/`
  matches the code (`grep -n` on each named declaration).
  Result: AUDIT `PrefixEvent` :46 → :65, `PrefixAudit` :63 → :82; OBSERVABILITY `PrefixObservedTelemetry` :65 →
  :87 (the old :65 was the doc-comment start; the definition was at :68, so the new reference points at the
  definition line). STATEFUL section 7 heading → `types/keys_service.go, keys_instance.go, keys_network.go,
  keys_volume.go`; the Line column now names file:line for all seven rows (the stale :800/:807/:566 and the four
  `—` entries replaced): `keys_service.go:146,153`, `keys_instance.go:114`, `keys_network.go:49`,
  `keys_volume.go:10,16,22`; :352 row → `types/keys_service.go:146,153`, `types/keys_instance.go:114`. Checked with
  `grep -n 'PrefixEvent = \|PrefixAudit = \|PrefixObservedTelemetry = ' types/keys.go` (65, 82, 87) and `grep -n
  '^func \(KeyDesiredServiceStateful\|…\)(' types/keys*.go` (146, 153, 114, 49, 10, 16, 22).
  `grep -rn 'keys\.go:\|keys_[a-z_]*\.go:' design/graphs/` shows only these refs plus unrelated
  `cmd/cca/server_secret_keys.go:*` ones. After the floating-comment rework the same greps give the same numbers
  (no kept comment precedes these declarations). Also (rework asked by the main session, 2026-11-24):
  `docs/design-connection-block.md:235` now names `types/keys_service.go` for `KeyDesiredConnection`,
  `ScanDesiredConnections`, `KeyDesiredServiceConnection`, `ScanDesiredServiceConnections` (`grep -n` finds
  them at keys_service.go:192, 197, 204, 210); `design/k8s-parity-gaps.md` left alone (historical file lists).
- [x] 3.2 `milestones.md` Phase 230: note that `TestEveryServiceKeyConstructorUsesARegisteredRoot` parses every
  non-test file in `types` after the rebase (D4). Verify the note names M275.
  Result: already in `BASE` from the proposal commit 8d43699 (`git show 8d43699:milestones.md | grep -c 'Note from
  M275'` = 1). `milestones.md:931` "Note from M275 (2026-11-24): …parses `keys.go` only… fails until it parses every
  non-test file in `types`"; the nearest heading above it is `:921 ### Phase 230 — Hierarchical Service Names
  Overwrite Other Services (M230, bug)`. No edit needed.

## 4. Checks and review

- [x] 4.1 `gofmt -l types` empty, `golangci-lint run ./types/...`, `go test -count=1 ./...` (record pass counts).
  Result (rerun after the floating-comment rework): `gofmt -l types` printed nothing; `golangci-lint run
  ./types/...` → `0 issues.`; `go vet ./types` exit 0; `go test -count=1 -json ./...` exit 0: 2880 tests pass, 5
  skip, 0 fail; packages 20 ok, 2 skip, 0 fail; `TestDocCrossCheckFactPrefixesExist` PASS ("verified 10 fact key
  prefixes"); `openspec validate split-type-keys` → "Change 'split-type-keys' is valid". After the file-map wording
  fix (comment-only, same line count; `grep -n` still gives PrefixEvent :65, PrefixAudit :82,
  PrefixObservedTelemetry :87, so no graph change): `gofmt -l types` empty, `go build ./...` exit 0,
  `golangci-lint run ./types/...` `0 issues.`, `go test -count=1 ./types ./integration` → `ok types 0.029s`, `ok
  integration 8.131s`.
- [x] 4.2 Independent correctness review (`code-reviewer`, Opus high) of `BASE..HEAD`: it reruns the D3 check
  itself and confirms the D1 placement. Record each finding as fixed or rejected with one line of reasoning.
  Result (2026-11-24, on 8d43699..aaa1326, code-reviewer Opus high + Codex job review-mwuge1w0-9k93tu: APPROVE):
  - D3 rerun: 10 separators out, 79 header/package/import/map lines in.
  - Reviewer's go/ast check: 377 declarations, each exactly once, identical doc and body; base order kept; 15
    floating groups kept and still floating; failures 0. A mutation (one changed character, one merged floating
    comment) gave 5 failures.
  - D1 rules rerun: nothing misplaced.
  - `go doc -all ./types`: same 539 entries; const blocks appear in file order.
  - M230 test with the split files: fails loudly ("checked only 0"); over all files it finds 107, the same as base.
  - Graph references match; build, vet, lint and tests pass. Codex: no findings.
  Findings:
  - **Fixed:** BASE named ce45772, which is no longer an ancestor after the rebase; re-recorded as 8d43699 (same
    `types/keys.go`).
  - **Fixed:** the file map did not name the cluster autoscale bounds, now on the keys_placement.go line.
  - **Fixed:** "routes" appeared twice in the map; keys_cloud.go now says "cloud routes".
  - **Rejected:** two kept floating headers cover a narrower area after the split ("Observed init results are
    per-instance" in keys_service.go, "Derived key functions" in keys_rollout.go). They were already so in the base,
    and D2 keeps comments byte-identical.
  - **Info, no action:** the `go doc` order differs; the API is unchanged.
  Rechecked after the fixes: D3 differs only in the two map lines; gofmt and lint are clean; `go test ./types
  ./integration` ok; the graph line numbers are unchanged (65, 82, 87).
- [x] 4.3 Patterns review (`code-reviewer`) of the diff against `design/Anti-pattern.md` and its references (file
  cohesion, no duplication introduced, doc comments intact). Record each finding as fixed or rejected.
  Result (same run, a separate section of the report): APPROVE.
  - One subject per file, 84 to 301 lines; keys.go 191.
  - No dead code: every one of the 432 names is used at least twice.
  - No duplication beyond the headers.
  - No init, iota or directives; the one package var depends only on constants, so initialization order cannot
    change.
  - Placement is not enforced by a test: accepted in the design (M259).
  Suggestions 2 and 3 are fixed (see 4.2); suggestion 4 is rejected (see 4.2).
- [ ] 4.4 One full Deploy Testbed run on the release commit or its parent (job summary says `full run`); record the
  run ID. A pure move cannot change runtime behaviour, but CLAUDE.md (M231) requires the closing run.

## Workflow follow-up

- Archive the change (no specs to sync) and release with `scripts/release.sh M275 <version>`.
