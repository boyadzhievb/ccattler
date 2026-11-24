# Proposal

Milestone: **M275 — Fact Key Constructors Split by Subject** (milestones.md Phase 275, refactor).

## Why

`types/keys.go` is 2,634 lines with 336 functions: every fact-store key constructor and scan prefix for every
subject (services, nodes, instances, volumes, routes, jobs, tenants, cloud, auth, secrets, keyring) in one file. It
is one of the five files of 2,200–2,700 lines found by the M259 measurement. Finding the keys of one subject means
searching the whole file, and its sections have drifted: probe, rollout and instance keys are each spread over
several places.

## What Changes

- `types/keys.go` keeps only what every subject shares: `Root`, the layer prefixes (`PrefixDesired`, `PrefixObserved`,
  …) and the shared scan prefixes, plus a file map comment naming where each subject's keys live.
- Each subject's constructors, scan prefixes and key-related constants move unchanged into their own file in package
  `types` (`keys_service.go`, `keys_node.go`, `keys_instance.go`, … listed in design.md).
- Move only: no exported name, signature, doc comment or returned key path changes, and no caller outside `types`
  changes.
- Graph entries that point at `types/keys.go:<line>` are updated to the new files.
- Milestones (done with this change): the M230 section notes that `TestEveryServiceKeyConstructorUsesARegisteredRoot`
  (branch `m230-hierarchical-service-names`) must read every non-test file in `types` once rebased, not only
  `keys.go`.

Out of scope:
- Removing repetition between constructors (shared helpers such as `desiredServiceField(name, field)`; option B
  discussed 2026-11-24). It changes every constructor body and needs a path-equality test of its own.
- Renaming constructors or changing any key path.
- File-length or other code-rule linters (M259).
- Splitting the other large files (`lang/parser.go`, `cmd/cca/command_status.go`, `api/server.go`, `agent/agent.go`).

## Capabilities

### New Capabilities

### Modified Capabilities

None: no behaviour changes, so the change sets `skip_specs: true`. Proof that nothing changed is in design.md (the
line-multiset check) and tasks.md.

## Impact

- `types/keys.go` and 15 new `types/keys_*.go` files; no other Go code. (Corrected 2026-11-24 during apply: the
  draft said 14, but the D1 table always listed 15.)
- `integration/doc_crosscheck_test.go` (`TestDocCrossCheckFactPrefixesExist`) reads `types/keys.go` for the layer
  prefixes; they stay there, so the test is unchanged.
- Graphs: `AUDIT_GRAPH.md`, `STATEFUL_SERVICES_GRAPH.md`, `OBSERVABILITY_GRAPH.md` (line references).
- M230 (`hierarchical-service-names`, paused on its branch): its constructor test parses `keys.go` by name and checks
  at least 90 constructors, so after a rebase it fails until it reads the whole package. It fails loudly, not
  silently.
- Ordering: implemented after M267 rework 3f is committed, because 3f edits `OBSERVABILITY_GRAPH.md` too.

How it is proven done: `go build ./...`, the line-multiset check (design.md D3) with every remaining difference
explained, `gofmt`/`golangci-lint`, `go test ./...`, the patterns and correctness reviews, and one full Deploy Testbed
run (CLAUDE.md, M231).
