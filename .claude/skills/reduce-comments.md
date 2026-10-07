---
description: Reduce Go function comments to 4 lines max, move details to CLAUDE.md
---

# Reduce Go Function Comments

Scan Go source files and condense verbose function doc comments. Any function doc comment longer than 4 lines gets shortened in place, with the full original text preserved in the project's CLAUDE.md under the appropriate section.

## Inputs

- **Path** (optional): a Go file or directory to scan. Defaults to the current working directory. When a directory is given, scan all `.go` files recursively (skip `vendor/`, `.git/`, and any generated files).

## Procedure

### 1. Identify long doc comments

For every `.go` file in scope, find function and method doc comments that exceed 4 lines. A "doc comment" is the contiguous block of `//` lines immediately preceding a `func` declaration.

Count only comment-body lines (the text after `//`), not the `func` signature itself.

Skip:
- Comments inside function bodies (inline comments).
- Comments on types, vars, consts, or package declarations -- only function/method comments.
- Test files (`_test.go`).
- Files under `vendor/`.

### 2. For each long comment, do two things

#### a. Condense the in-source comment to at most 4 lines

- Keep the first sentence (the Go doc convention summary line).
- Retain the most important behavioral details (what it returns, key side effects, concurrency safety).
- Drop examples, rationale, history, parameter-by-parameter descriptions, and implementation notes -- those go to CLAUDE.md.
- Preserve the `//` style.
- Make sure the condensed comment still reads well and follows Go doc conventions.

#### b. Append the full original comment to CLAUDE.md

Find or create a section in the project's `CLAUDE.md` file for the package. Use the heading format:

```markdown
### <package-name> — Detailed Function Docs
```

Under that heading, append a sub-section for each function:

```markdown
#### <FunctionOrMethodName>

**File:** `<relative-path-to-go-file>`

<full original comment text, without the leading `//` markers, as plain prose>
```

If a sub-section for that function already exists, replace it with the new version.

### 3. Post-processing

- Run `gofmt` on every modified `.go` file after editing.
- Run `go vet ./...` on affected packages to confirm nothing broke.

### 4. Report

After processing, print a summary:

```
Reduced comments in N functions across M files.
CLAUDE.md updated with detailed docs for packages: foo, bar
```

If no comments exceeded 4 lines, print:

```
No function comments exceed 4 lines. Nothing to do.
```

## Rules

- Never delete a doc comment entirely. Every function must keep at least a one-line summary.
- Never modify function signatures, code, or non-doc comments.
- Preserve copyright headers and build tags untouched.
- Use the exact function name (including receiver for methods, e.g., `(*NodeAgent) reconcileInstance`) as the sub-heading.
