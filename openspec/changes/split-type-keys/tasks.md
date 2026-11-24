# Tasks

Milestone M275. Design: [design.md](design.md). No spec delta (`skip_specs: true`, a pure move), so there are no new
scenario tests to write first; group 1 records the baseline that the move is checked against instead.

## 1. Baseline (before any move)

- [ ] 1.1 Start on a clean tree after M267 rework 3f is committed (`git status --short` empty). Record the base
  commit (`BASE`) and `wc -l types/keys.go`.
- [ ] 1.2 Record the baseline on `BASE`: `go build ./...` passes, `go test -count=1 ./types ./integration -run
  'TestDocCrossCheck'` passes, and the declaration count (`grep -c '^func ' types/keys.go`). Apply the D1 rules to
  the declarations on `BASE` and record the per-file counts; record every difference from the counts in design.md
  D1 (declarations added since 92feee5) with the file the rules give them.

## 2. Move

- [ ] 2.1 Create the 15 `types/keys_*.go` files of D1, each with the copyright/SPDX header, `package types` and
  `import "fmt"` only where it is used. Move each declaration with its doc comment unchanged and in its original
  relative order. Drop the `// ---` separators and floating section comments (D2). Verify `go build ./...` and
  `go vet ./types`.
- [ ] 2.2 `keys.go` keeps `Root`, the layer-prefix block and the shared scan block (D2), plus the file map comment
  under the import (one line per file and subject). Verify `wc -l types/keys.go` is under 250 and
  `TestDocCrossCheckFactPrefixesExist` passes unchanged.
- [ ] 2.3 Any declaration placed by hand against the D1 rules: list it here with one line of reasoning (none
  expected).
- [ ] 2.4 Run the D3 line-multiset check against `BASE` and paste its output here. Explain every remaining line as
  one of the allowed differences (header, `package`, `import`, file map, dropped separator or floating comment); any
  other line fails this task. Verify `git diff --stat "$BASE"` lists only `types/keys*.go` and the docs of group 3.

## 3. Docs

- [ ] 3.1 Graphs: `AUDIT_GRAPH.md:548-549` and `OBSERVABILITY_GRAPH.md:388` point at the new `keys.go` lines;
  `STATEFUL_SERVICES_GRAPH.md` section 7 heading and `:352` row point at `keys_service.go`, `keys_instance.go`,
  `keys_network.go` and `keys_volume.go` with current line numbers. Verify `grep -rn 'keys\.go:' design/graphs/`
  matches the code (`grep -n` on each named declaration).
- [ ] 3.2 `milestones.md` Phase 230: note that `TestEveryServiceKeyConstructorUsesARegisteredRoot` parses every
  non-test file in `types` after the rebase (D4). Verify the note names M275.

## 4. Checks and review

- [ ] 4.1 `gofmt -l types` empty, `golangci-lint run ./types/...`, `go test -count=1 ./...` (record pass counts).
- [ ] 4.2 Independent correctness review (`code-reviewer`, Opus high) of `BASE..HEAD`: it reruns the D3 check
  itself and confirms the D1 placement. Record each finding as fixed or rejected with one line of reasoning.
- [ ] 4.3 Patterns review (`code-reviewer`) of the diff against `design/Anti-pattern.md` and its references (file
  cohesion, no duplication introduced, doc comments intact). Record each finding as fixed or rejected.
- [ ] 4.4 One full Deploy Testbed run on the release commit or its parent (job summary says `full run`); record the
  run ID. A pure move cannot change runtime behaviour, but CLAUDE.md (M231) requires the closing run.

## Workflow follow-up

- Archive the change (no specs to sync) and release with `scripts/release.sh M275 <version>`.
