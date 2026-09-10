# Implement Improvement Plan Item

Implement a specific item from the CCattler improvement plan (`improvments.md`).

## Steps

1. Read `improvments.md` to understand the full item description and rationale.
2. Read the current CLAUDE.md to check if the item is already marked complete.
3. Identify the files that need to change by reading the relevant source code.
4. Implement the change following the code style rules in CLAUDE.md:
   - Comment every function
   - Use long descriptive variable names
   - Document struct fields with inline comments
   - Descriptive function names
   - Receiver names match the type
5. Write tests that verify the new behavior.
6. Run `go test ./...` to ensure nothing is broken.
7. Update CLAUDE.md to mark the item as complete.

## Architecture Rules

- Never change the `StateStore` interface without checking all implementations and callers.
- Never change the `Controller` interface without updating every controller.
- The store is the consistency boundary. Controllers propose changes; the runner commits them.
- Desired state and observed state are always separate key domains.
