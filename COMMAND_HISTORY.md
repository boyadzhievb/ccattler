# Command History

| Timestamp | Command | Description |
|-----------|---------|-------------|
| 2026-09-10 15:00 | `ssh -i ~/.ssh/id_ed25519-localvm bozhan@192.168.100.169 'sudo whoami'` | Verify passwordless sudo works on VM |
| 2026-09-10 15:01 | `ssh ... 'uname -a; cat /etc/os-release ...'` | Check VM OS, resources, and installed tools |
| 2026-09-10 15:01 | `ssh ... 'sudo apt-get update && sudo apt-get install -y golang-go etcd-server etcd-client'` | Install Go and etcd on VM (interrupted) |
| 2026-09-10 17:25 | `grep -rn 'runtimeBackend.*process' cmd/` | Check for hardcoded process runtime references |
| 2026-09-10 17:25 | `grep -rn 'runtimeBackend' --include='*_test.go' cmd/` | Check if tests reference runtime default |
| 2026-09-10 17:26 | `mkdir -p cmd/mcp` | Create MCP server directory |
| 2026-09-10 17:27 | `go build ./cmd/mcp/` | Compile MCP server |
| 2026-09-10 17:27 | `go build ./cmd/cca/ && go test ./...` | Build cca and run all tests |
| 2026-09-10 17:27 | `go test ./controllers/` | Verify controller tests pass individually |
| 2026-09-10 17:28 | `go test ./agent/ ./api/ ./store/ ./lang/ ./scheduler/` | Run core package tests individually |
| 2026-09-10 17:28 | `go build -o /tmp/ccattler-mcp ./cmd/mcp/` | Build MCP binary for local testing |
| 2026-09-10 17:29 | `echo '...' \| /tmp/ccattler-mcp --repo ...` | Test MCP initialize handshake |
| 2026-09-10 17:29 | `printf '...' \| /tmp/ccattler-mcp ...` | Test tools/list — verify all 14 tools returned |
| 2026-09-10 17:30 | `printf '...' \| /tmp/ccattler-mcp ...` | Test git_status and git_log tool calls |
| 2026-09-10 17:30 | `printf '...' \| /tmp/ccattler-mcp ...` | Test guardrails — injection, path traversal, invalid component |
