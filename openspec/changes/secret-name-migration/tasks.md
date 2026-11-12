# Tasks

## 1. Design and failing tests first

- [ ] 1.1 Independent review of this draft (`code-reviewer` + `security-audit`); record findings in milestones.md Phase 209
- [ ] 1.2 Write Flow 31 (secret name migration) in `design/runtime-invariant-flows.md`
- [ ] 1.3 Write every test named in `specs/secret-name-migration/spec.md`; record the failing run

## 2. Plan and apply

- [ ] 2.1 `--plan`: legacy secrets, revisions, consumers, tenants, unreachable `route/` keys; mapping file
- [ ] 2.2 `--apply`: preconditions, mapping validation, foreign-tenant refusal, `intent/secret_names/` facts with hash; `intent/secret_names/` → `cluster.admin`

## 3. Controller

- [ ] 3.1 `secret-name-migration` controller with write domain in `controllerOutputPrefixes`; leader-elected
- [ ] 3.2 Phase 1 (create-only canonical envelope, grants, shares), 2a (old grants in batches), 2b (guarded legacy delete); re-copy on guard failure; observed progress
- [ ] 3.3 Delivery and agent de-duplicate dual grants by reference
- [ ] 3.4 Audit per moved secret

## 4. Verification

- [ ] 4.1 `gofmt`, `golangci-lint run ./...`, `go test ./...`
- [ ] 4.2 Deploy cluster: migrate legacy secrets under traffic (0 restarts)
- [ ] 4.3 Independent review of the implementation; record findings
