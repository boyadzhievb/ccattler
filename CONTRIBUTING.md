# Contributing to CCattler

CCattler is a fact-based container orchestrator — a Kubernetes alternative built around facts, rules, and reconciliation instead of an object hierarchy. We welcome contributions from anyone interested in container orchestration, distributed systems, or Go development.

## Getting Started

### Prerequisites

- **Go 1.26+** — see `go.mod` for the exact version
- **etcd** (optional) — for distributed store testing; memory store works for development
- **Docker/nerdctl** (optional) — only needed for container runtime testing
- **golangci-lint v2** — for linting (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`)

### Clone and Build

```bash
git clone https://github.com/boyadzhievb/ccattler.git
cd ccattler
go build ./...
```

### Run Tests

```bash
# Full test suite
go test ./... -timeout 120s

# Production readiness report (runs 1000+ tests across 16 areas)
go run ./cmd/cca/ readiness

# Single package
go test ./controllers/ -v

# With race detector
go test ./... -race -timeout 180s
```

### Try It Locally

```bash
# Simulate a 3-node cluster with 5 web instances
cat > demo.cca <<'EOF'
service web {
    image nginx:1.27
    instances 5
    expose 8080
}
EOF

go run ./cmd/cca/ apply demo.cca
```

## Code Style

Read the **Code Style Rules** section in [CLAUDE.md](CLAUDE.md) — it covers everything. The key points:

- **Comment every function** — exported and unexported
- **Long descriptive variable names** — `factStore` not `s`, `instanceController` not `ic`
- **No magic numbers** — timeouts, capacities, thresholds must be named constants
- **80-line function limit** — break long functions into sub-functions (flat switch/dispatch gets an exclusion comment)
- **No dead code** — every exported symbol must have a production caller
- **Deterministic iteration** — sort map keys before iterating

### Before Submitting

```bash
# Must all pass
go build ./...
go test ./... -timeout 120s
go vet ./...
golangci-lint run ./...
```

## Architecture Overview

```
USER → DSL Parser → Fact Store (etcd) → Controllers → Node Agents → Containers
```

- **Fact Store** — the spine; every component communicates through it
- **Controllers** — watch fact prefixes, compare desired vs actual, propose changes
- **Scheduler** — pure function: `schedule(requirements, nodes) → placement`
- **Node Agent** — reconciles local runtime to match desired state from the store
- **Runtime** — pluggable: SimulatorRuntime (tests), ProcessRuntime, ContainerRuntime

Key architectural rules:
1. Components only react to state — they never call each other
2. Desired state and observed state are always separate
3. Every operation is idempotent (`ensure_X()`, not `do_X()`)
4. Controllers return proposed changes, validated and committed transactionally

See [CLAUDE.md](CLAUDE.md) for the full design philosophy and security model.

## What to Work On

Check the [issues](https://github.com/boyadzhievb/ccattler/issues) — look for labels:

- **`good first issue`** — well-scoped tasks suitable for newcomers
- **`help wanted`** — tasks where contributions are especially welcome
- **`enhancement`** — new features and improvements
- **`bug`** — confirmed bugs

The [milestones.md](milestones.md) file has the full roadmap with checkboxes.

## Submitting Changes

1. Fork the repo and create a branch from `master`
2. Make your changes following the code style rules
3. Add tests for new functionality
4. Run the full check suite (build, test, vet, lint)
5. Open a pull request with a clear description of what and why

### PR Guidelines

- Keep PRs focused — one feature or fix per PR
- Include test evidence — new behavior should have tests
- Reference the issue number if applicable
- The CI pipeline runs tests automatically on PR

## Project Structure

```
cmd/cca/          CLI entry point and commands
cmd/mcp/          MCP server for AI-assisted development
agent/            Node agent (reconciler, probes, telemetry)
controllers/      Control plane controllers (instance, scheduler, network, etc.)
lang/             DSL lexer, parser, compiler
store/            State store interface + etcd/memory implementations
runtime/          Runtime adapters (simulator, process, container)
network/          Networking (DNS, VIP data plane, proxy)
security/         Authentication, authorization, audit
tenant/           Multi-tenancy (quotas, policy gates, lifecycle)
scheduler/        Placement scheduler
types/            Shared types, fact keys, codecs
integration/      Integration tests
loadtest/         Load/scale tests
chaos/            Chaos testing framework
```

## Questions?

Open an issue with the `question` label, or look at existing issues for context.
