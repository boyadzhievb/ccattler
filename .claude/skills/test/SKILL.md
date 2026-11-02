---
name: test
description: Run go vet and the full CCattler test suite and summarize failures with root causes
disable-model-invocation: true
---

# Run Tests

Run the CCattler test suite and report results.

## Steps

1. Run `go vet ./...` to catch static analysis issues.
2. Run `go test ./... 2>&1 | grep -v '^ok\|no test files'` so only failures are printed.
3. If any package fails, re-run just that package with `-v -run <TestName>` for detail.
4. Report a summary: how many packages passed, which (if any) failed, and the root cause of failures.

## Notes

- The `chaos` package tests take ~15s due to convergence testing with simulated failures.
- The `integration` package tests take ~10s due to multi-node cluster simulations.
- If a test fails with "timed out waiting for", consider whether a `SetResyncInterval` is needed on the test's runner (since idempotent Put means controllers need periodic resync to catch state changes).
