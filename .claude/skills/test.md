# Run Tests

Run the CCattler test suite and report results.

## Steps

1. Run `go vet ./...` to catch static analysis issues.
2. Run `go test ./...` for the full suite.
3. If any package fails, re-run that package with `-v` to get detailed output.
4. Report a summary: how many packages passed, which (if any) failed, and the root cause of failures.

## Notes

- The `chaos` package tests take ~15s due to convergence testing with simulated failures.
- The `integration` package tests take ~10s due to multi-node cluster simulations.
- If a test fails with "timed out waiting for", consider whether a `SetResyncInterval` is needed on the test's runner (since idempotent Put means controllers need periodic resync to catch state changes).
