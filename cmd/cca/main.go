// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Package main implements the ccattler CLI — the entry point for the CCattler
// container orchestrator. It provides commands for applying configurations,
// running workloads (as processes or containers), querying cluster status,
// and injecting simulated metrics.
package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags.
var version = "dev"

// main parses the CLI command and dispatches to the appropriate handler function.
// This function exceeds 80 lines because it is a flat command-dispatch switch
// statement — each case is a simple validation-then-call block. Extracting
// sub-groups would add indirection without improving clarity.
func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("cca %s\n", version)
		return
	case "apply":
		parsedApplyConfig := parseApplyCommandArgs(os.Args[2:])
		if parsedApplyConfig.configFilePath == "" {
			fmt.Fprintln(os.Stderr, "usage: cca apply [--store memory|etcd] [--endpoints host:port,...] <file>")
			os.Exit(1)
		}
		executeApplyCommand(parsedApplyConfig)
	case "run":
		parsedRunConfig := parseRunCommandArgs(os.Args[2:])
		if parsedRunConfig.configFilePath == "" {
			fmt.Fprintln(os.Stderr, "usage: cca run [--watch] [--store memory|etcd] [--endpoints host:port,...] <file>")
			os.Exit(1)
		}
		executeLiveProcessCommand(parsedRunConfig)
	case "run-container":
		parsedRunConfig := parseRunCommandArgs(os.Args[2:])
		if parsedRunConfig.configFilePath == "" {
			fmt.Fprintln(os.Stderr, "usage: cca run-container [--watch] [--store memory|etcd] [--endpoints host:port,...] <file>")
			os.Exit(1)
		}
		executeLiveContainerCommand(parsedRunConfig)
	case "server":
		parsedServerConfig := parseServerCommandArgs(os.Args[2:])
		executeServerCommand(parsedServerConfig)
	case "token":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca token <create|list|revoke> [flags]")
			os.Exit(1)
		}
		parsedTokenConfig := parseTokenCommandArgs(os.Args[2:])
		executeTokenCommand(parsedTokenConfig)
	case "join":
		parsedJoinConfig := parseJoinCommandArgs(os.Args[2:])
		if parsedJoinConfig.serverAddress == "" || parsedJoinConfig.joinToken == "" || parsedJoinConfig.nodeID == "" || parsedJoinConfig.caCertPath == "" {
			fmt.Fprintln(os.Stderr, "usage: cca join <server-url> <token> --node-id <id> --ca-cert <path> [--data-dir <path>]")
			os.Exit(1)
		}
		executeJoinCommand(parsedJoinConfig)
	case "agent":
		parsedAgentConfig := parseAgentCommandArgs(os.Args[2:])
		if parsedAgentConfig.nodeID == "" {
			fmt.Fprintln(os.Stderr, "error: --node-id is required for agent mode")
			fmt.Fprintln(os.Stderr, "usage: cca agent --node-id <id> --store etcd [--endpoints host:port,...] [--store-prefix /path/]")
			os.Exit(1)
		}
		executeAgentCommand(parsedAgentConfig)
	case "demo":
		executeDemoCommand()
	case "demo-distributed":
		executeDistributedDemoCommand()
	case "demo-network":
		executeNetworkDemoCommand()
	case "demo-storage":
		executeStorageDemoCommand()
	case "chaos":
		executeChaosCommand()
	case "benchmark":
		parsedBenchmarkConfig := parseBenchmarkCommandArgs(os.Args[2:])
		executeBenchmarkCommand(parsedBenchmarkConfig)
	case "top":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca top <nodes|workloads|volumes>")
			os.Exit(1)
		}
		executeTopCommand(os.Args[2])
	case "status":
		executeStatusCommand()
	case "logs":
		parsedLogsConfig := parseLogsCommandArgs(os.Args[2:])
		executeLogsCommand(parsedLogsConfig)
	case "render":
		parsedRenderConfig := parseRenderCommandArgs(os.Args[2:])
		if parsedRenderConfig.configFilePath == "" {
			fmt.Fprintln(os.Stderr, "usage: cca render --values <file> [--set key=value] [--set-from-env KEY] <template>")
			os.Exit(1)
		}
		executeRenderCommand(parsedRenderConfig)
	case "diff":
		parsedDiffConfig := parseDiffCommandArgs(os.Args[2:])
		if parsedDiffConfig.configFilePath == "" {
			fmt.Fprintln(os.Stderr, "usage: cca diff [--store memory|etcd] [--endpoints host:port,...] <file>")
			os.Exit(1)
		}
		executeDiffCommand(parsedDiffConfig)
	case "events":
		parsedEventsConfig := parseEventsCommandArgs(os.Args[2:])
		executeEventsCommand(parsedEventsConfig)
	case "describe":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: cca describe <service|node|instance> <name>")
			os.Exit(1)
		}
		executeDescribeCommand(os.Args[2], os.Args[3])
	case "get":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca get <services|instances|nodes|volumes|networking|secrets|config>")
			os.Exit(1)
		}
		executeGetCommand(os.Args[2])
	case "scale":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: cca scale <service> <count>")
			os.Exit(1)
		}
		executeScaleCommand(os.Args[2], os.Args[3])
	case "watch":
		prefix := ""
		if len(os.Args) >= 3 {
			prefix = os.Args[2]
		}
		executeWatchCommand(prefix)
	case "metric":
		if len(os.Args) < 5 || os.Args[2] != "set" {
			fmt.Fprintln(os.Stderr, "usage: cca metric set <service> <metric> <value>")
			os.Exit(1)
		}
		executeMetricSetCommand(os.Args[3], os.Args[4], os.Args[5])
	case "drain":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca drain <node-id> [--grace-period 30]")
			os.Exit(1)
		}
		gracePeriod := parseDrainGracePeriod(os.Args[2:])
		executeDrainCommand(os.Args[2], gracePeriod)
	case "disable-node":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca disable-node <node-id>")
			os.Exit(1)
		}
		executeDisableNodeCommand(os.Args[2])
	case "enable-node":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca enable-node <node-id>")
			os.Exit(1)
		}
		executeEnableNodeCommand(os.Args[2])
	case "secret":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca secret <set|get|list|delete> [name] [value]")
			os.Exit(1)
		}
		executeSecretCommand(os.Args[2:])
	case "readiness":
		parsedReadinessConfig := parseReadinessCommandArgs(os.Args[2:])
		executeReadinessCommand(parsedReadinessConfig)
	case "completion":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca completion <bash|zsh>")
			os.Exit(1)
		}
		executeCompletionCommand(os.Args[2])
	default:
		printUsage()
		os.Exit(1)
	}
}

// printUsage prints the full CLI help text to stderr.
// This function exceeds 80 lines because it is flat help text assembled via
// fmt.Fprintln calls — each line is an independent output statement. Extracting
// sub-groups would add indirection without improving clarity.
func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: cca <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "single-process mode (all-in-one):")
	fmt.Fprintln(os.Stderr, "  apply [flags] <file>         parse .ccattler file, show reconciliation (simulated)")
	fmt.Fprintln(os.Stderr, "  run [flags] <file>           start real processes")
	fmt.Fprintln(os.Stderr, "  run-container [flags] <file> start real containers")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "multi-process mode (distributed):")
	fmt.Fprintln(os.Stderr, "  server [flags]               run control plane (controllers + API)")
	fmt.Fprintln(os.Stderr, "  agent [flags]                run node agent (watches store, runs workloads)")
	fmt.Fprintln(os.Stderr, "  apply --store etcd <file>    write facts to shared store")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "node enrollment:")
	fmt.Fprintln(os.Stderr, "  token create [flags]         generate a join token for node enrollment")
	fmt.Fprintln(os.Stderr, "  token list [flags]           list active join tokens")
	fmt.Fprintln(os.Stderr, "  token revoke <token> [flags] revoke a join token")
	fmt.Fprintln(os.Stderr, "  join <server> <token> [flags] enroll this node with the cluster")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "demos:")
	fmt.Fprintln(os.Stderr, "  demo                         built-in demo with simulated runtime (1 node)")
	fmt.Fprintln(os.Stderr, "  demo-distributed             3 simulated nodes, kills one to show recovery")
	fmt.Fprintln(os.Stderr, "  demo-network                 3 nodes with IP allocation, VIPs, DNS, LB")
	fmt.Fprintln(os.Stderr, "  demo-storage                 3 nodes with persistent volumes, node kill")
	fmt.Fprintln(os.Stderr, "  chaos                        random failure injection, convergence reporting")
	fmt.Fprintln(os.Stderr, "  benchmark [--json] [--nodes N] [--services N] [--instances N]  chaos benchmark with recovery metrics")
	fmt.Fprintln(os.Stderr, "  readiness [--json]           run test suite across 16 areas, print readiness report")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "cluster management:")
	fmt.Fprintln(os.Stderr, "  status                       show cluster status (queries running instance)")
	fmt.Fprintln(os.Stderr, "  describe <type> <name>       detailed view (service, node, instance)")
	fmt.Fprintln(os.Stderr, "  top <nodes|workloads>        resource utilization overview")
	fmt.Fprintln(os.Stderr, "  get <resource>               services, instances, nodes, volumes, networking, secrets, config")
	fmt.Fprintln(os.Stderr, "  diff [flags] <file>           dry-run apply showing fact changes (add/modify)")
	fmt.Fprintln(os.Stderr, "  render [flags] <template>     render a template with values to stdout")
	fmt.Fprintln(os.Stderr, "  events [flags]               event stream (--follow for live, --service to filter)")
	fmt.Fprintln(os.Stderr, "  logs <service> [--follow] [--instance <id>]  container stdout/stderr")
	fmt.Fprintln(os.Stderr, "  scale <svc> <n>              scale a service to n instances")
	fmt.Fprintln(os.Stderr, "  drain <node-id> [flags]      gracefully evict instances from a node")
	fmt.Fprintln(os.Stderr, "  disable-node <node-id>       exclude node from new placements")
	fmt.Fprintln(os.Stderr, "  enable-node <node-id>        return disabled node to normal scheduling")
	fmt.Fprintln(os.Stderr, "  watch [prefix]               stream fact store changes")
	fmt.Fprintln(os.Stderr, "  metric set <svc> <m> <v>     inject simulated metric")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags for run / run-container / apply:")
	fmt.Fprintln(os.Stderr, "  --watch, -w                  print status every 2s (run only)")
	fmt.Fprintln(os.Stderr, "  --store memory|etcd          state store backend (default: memory)")
	fmt.Fprintln(os.Stderr, "  --endpoints host:port,...    etcd endpoints (default: localhost:2379)")
	fmt.Fprintln(os.Stderr, "  --store-prefix /path/        etcd key prefix (default: /ccattler/)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "template flags (apply / diff / render):")
	fmt.Fprintln(os.Stderr, "  --values, -f <file>          values file for template rendering (repeatable)")
	fmt.Fprintln(os.Stderr, "  --set key=value              override a template value (repeatable)")
	fmt.Fprintln(os.Stderr, "  --set-from-env KEY           read template value from environment variable")
	fmt.Fprintln(os.Stderr, "  --dry-run                    render + validate without applying (apply only)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags for server:")
	fmt.Fprintln(os.Stderr, "  --listen host:port           API listen address (default: 127.0.0.1:9770, 0.0.0.0:9770 with --tls)")
	fmt.Fprintln(os.Stderr, "  --tls                        enable mTLS (auto-generates CA, writes ca.pem)")
	fmt.Fprintln(os.Stderr, "  --cert <path>                PEM server certificate (requires --key and --ca)")
	fmt.Fprintln(os.Stderr, "  --key <path>                 PEM server private key")
	fmt.Fprintln(os.Stderr, "  --ca <path>                  PEM CA certificate for client verification")
	fmt.Fprintln(os.Stderr, "  --store memory|etcd          state store backend (default: etcd)")
	fmt.Fprintln(os.Stderr, "  --endpoints host:port,...    etcd endpoints (default: localhost:2379)")
	fmt.Fprintln(os.Stderr, "  --store-prefix /path/        etcd key prefix (default: /ccattler/)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags for agent:")
	fmt.Fprintln(os.Stderr, "  --node-id <id>               unique node identifier (required)")
	fmt.Fprintln(os.Stderr, "  --runtime process|container  workload runtime (default: container)")
	fmt.Fprintln(os.Stderr, "  --cert <path>                PEM agent certificate (requires --key and --ca)")
	fmt.Fprintln(os.Stderr, "  --key <path>                 PEM agent private key")
	fmt.Fprintln(os.Stderr, "  --ca <path>                  PEM CA certificate for server verification")
	fmt.Fprintln(os.Stderr, "  --store memory|etcd          state store backend (default: etcd)")
	fmt.Fprintln(os.Stderr, "  --endpoints host:port,...    etcd endpoints (default: localhost:2379)")
	fmt.Fprintln(os.Stderr, "  --store-prefix /path/        etcd key prefix (default: /ccattler/)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags for token:")
	fmt.Fprintln(os.Stderr, "  --node-id <id>               scope token to a specific node (optional)")
	fmt.Fprintln(os.Stderr, "  --ttl <duration>             token lifetime (default: 15m)")
	fmt.Fprintln(os.Stderr, "  --store memory|etcd          state store backend (default: etcd)")
	fmt.Fprintln(os.Stderr, "  --endpoints host:port,...    etcd endpoints (default: localhost:2379)")
	fmt.Fprintln(os.Stderr, "  --store-prefix /path/        etcd key prefix (default: /ccattler/)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags for join:")
	fmt.Fprintln(os.Stderr, "  --node-id <id>               unique node identifier (required)")
	fmt.Fprintln(os.Stderr, "  --ca-cert <path>             PEM CA certificate to verify server (recommended)")
	fmt.Fprintln(os.Stderr, "  --data-dir <path>            directory for cert/key files (default: .ccattler)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "shell completion:")
	fmt.Fprintln(os.Stderr, "  completion bash               output bash completion script")
	fmt.Fprintln(os.Stderr, "  completion zsh                output zsh completion script")
}
