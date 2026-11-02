---
name: commit
description: Commit the current changes following CCattler conventions (lint, tests, milestone-prefixed message)
disable-model-invocation: true
---

# Commit Changes

Create a commit for the current changes following CCattler conventions.

## Steps

1. Run `git status` and `git diff` to review what changed.
2. Run `gofmt -l .` and `golangci-lint run ./...` — fix every issue before staging.
3. Run `go test ./...` to verify everything passes.
4. Stage only the relevant files by name (never `git add -A` with unrelated changes).
5. Commit with a message prefixed by the current milestone (see `milestones.md`).

## Commit Message Format

```
M{N}: short description of the change

- bullet per notable change (optional)
```

`N` is the milestone number, which equals the phase number (e.g. `M169:` for Phase 169).

## Never

- `--amend`, `reset`, or rebase — fix mistakes with a new commit.
- Push — the user pushes.
- `Co-Authored-By` lines or `[skip ci]`.
