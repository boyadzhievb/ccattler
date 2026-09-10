# Commit Changes

Create a commit for the current changes following CCattler conventions.

## Steps

1. Run `git status` to see what changed.
2. Run `git diff` to review the actual changes.
3. Run `go test ./...` to verify everything passes before committing.
4. Stage the relevant files (avoid staging unrelated changes).
5. Write a commit message with:
   - Prefix matching the current milestone (e.g., `M11:` for Phase 14 work)
   - Brief description of what changed and why

## Commit Message Format

```
M{N}: short description of the change
```
