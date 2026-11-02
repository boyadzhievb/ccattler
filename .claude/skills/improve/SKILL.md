---
name: improve
description: Implement one open checklist item from milestones.md with tests, then tick it off
disable-model-invocation: true
argument-hint: "<phase number or checklist item text>"
---

# Implement Milestone Checklist Item

Implement one open (`- [ ]`) item from `milestones.md`. The argument names the phase or item; if none is given, list the open items and ask which one.

## Steps

1. Find the item: `grep -n -A 30 '^### Phase <N>' milestones.md`. Never read `milestones-archive.md` whole.
2. Read the related design doc in `design/` if one exists. If the change adds or alters a runtime flow, update `design/runtime-invariant-flows.md` first.
3. Identify the files that need to change by reading the relevant source code.
4. Implement following the CLAUDE.md style rules (already in context).
5. Write tests that verify the new behavior.
6. Run `gofmt -l .`, `golangci-lint run ./...`, and `go test ./...`.
7. Mark the item `- [x]` in `milestones.md`.

## Architecture Rules

- Never change the `StateStore` interface without checking all implementations and callers.
- Never change the `Controller` interface without updating every controller.
- The store is the consistency boundary. Controllers propose changes; the runner commits them.
- Desired state and observed state are always separate key domains.
