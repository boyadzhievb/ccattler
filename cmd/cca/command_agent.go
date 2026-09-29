// Agent command implementation for the cca CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/types"
)

// agentCommandConfig holds parsed flags for the "agent" command, which runs
// the node agent against a shared state store.
type agentCommandConfig struct {
	storeBackend       string
	etcdEndpoints      string
	storeKeyPrefix     string
	nodeID             string
	runtimeBackend     string
	advertiseAddress   string
	tlsCertPath        string
	tlsKeyPath         string
	tlsCACertPath      string
	etcdCertPath       string
	etcdKeyPath        string
	etcdCACertPath     string
	proxyEnabled       bool
	proxyListenAddress string
	logLevel           string
	logFormat          string
}

// parseAgentCommandArgs extracts store and agent flags from the arguments
// following "agent".
func parseAgentCommandArgs(args []string) agentCommandConfig {
	parsedConfig := agentCommandConfig{
		storeBackend:   "etcd",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
		runtimeBackend: "container",
	}

	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--store":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.storeBackend = args[argIndex]
			}
		case "--endpoints":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdEndpoints = args[argIndex]
			}
		case "--store-prefix":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.storeKeyPrefix = args[argIndex]
			}
		case "--node-id":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		case "--runtime":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.runtimeBackend = args[argIndex]
			}
		case "--cert":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tlsCertPath = args[argIndex]
			}
		case "--key":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tlsKeyPath = args[argIndex]
			}
		case "--ca":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tlsCACertPath = args[argIndex]
			}
		case "--etcd-cert":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdCertPath = args[argIndex]
			}
		case "--etcd-key":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdKeyPath = args[argIndex]
			}
		case "--etcd-ca":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdCACertPath = args[argIndex]
			}
		case "--advertise-address":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.advertiseAddress = args[argIndex]
			}
		case "--proxy":
			parsedConfig.proxyEnabled = true
		case "--proxy-listen":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.proxyListenAddress = args[argIndex]
				parsedConfig.proxyEnabled = true
			}
		case "--log-level":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.logLevel = args[argIndex]
			}
		case "--log-format":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.logFormat = args[argIndex]
			}
		}
	}

	if parsedConfig.proxyEnabled && parsedConfig.proxyListenAddress == "" {
		parsedConfig.proxyListenAddress = "0.0.0.0:80"
	}

	if parsedConfig.storeBackend != "memory" && parsedConfig.storeBackend != "etcd" {
		fmt.Fprintf(os.Stderr, "error: unknown store backend %q (must be \"memory\" or \"etcd\")\n", parsedConfig.storeBackend)
		os.Exit(1)
	}

	if parsedConfig.runtimeBackend != "process" && parsedConfig.runtimeBackend != "container" {
		fmt.Fprintf(os.Stderr, "error: unknown runtime %q (must be \"process\" or \"container\")\n", parsedConfig.runtimeBackend)
		os.Exit(1)
	}

	tlsFlagCount := 0
	if parsedConfig.tlsCertPath != "" {
		tlsFlagCount++
	}
	if parsedConfig.tlsKeyPath != "" {
		tlsFlagCount++
	}
	if parsedConfig.tlsCACertPath != "" {
		tlsFlagCount++
	}
	if tlsFlagCount > 0 && tlsFlagCount < 3 {
		fmt.Fprintln(os.Stderr, "error: --cert, --key, and --ca must all be provided together")
		os.Exit(1)
	}

	return parsedConfig
}

// executeAgentCommand starts the node agent, which watches the shared state store
// for placements assigned to this node and reconciles the local runtime. It
// registers the node, starts the appropriate runtime, and blocks until Ctrl+C.
func executeAgentCommand(parsedConfig agentCommandConfig) {
	configureLogger(parsedConfig.logLevel, parsedConfig.logFormat)
	factStore, storeCreationError := createStateStoreFromServerConfig(
		parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix,
		parsedConfig.etcdCertPath, parsedConfig.etcdKeyPath, parsedConfig.etcdCACertPath)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer func() { _ = factStore.Close() }()

	if parsedConfig.storeBackend == "etcd" {
		fmt.Printf("CCattler agent %s connected to etcd at %s (prefix: %s)\n",
			parsedConfig.nodeID, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
	}

	if parsedConfig.tlsCertPath == "" {
		discoveredCertPath := ".ccattler/node.pem"
		discoveredKeyPath := ".ccattler/node-key.pem"
		discoveredCACertPath := ".ccattler/ca.pem"
		if fileExists(discoveredCertPath) && fileExists(discoveredKeyPath) && fileExists(discoveredCACertPath) {
			parsedConfig.tlsCertPath = discoveredCertPath
			parsedConfig.tlsKeyPath = discoveredKeyPath
			parsedConfig.tlsCACertPath = discoveredCACertPath
			fmt.Printf("Agent %s auto-discovered credentials from .ccattler/\n", parsedConfig.nodeID)
		}
	}
	if parsedConfig.tlsCertPath != "" {
		_ = loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath)
		fmt.Printf("Agent %s TLS credentials loaded\n", parsedConfig.nodeID)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if writeError := types.WriteNode(ctx, factStore, types.Node{
		ID: parsedConfig.nodeID, State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	}); writeError != nil {
		logging.Default().Error("failed to write node", "node", parsedConfig.nodeID, "error", writeError.Error())
	}

	var runtimeAdapter runtime.Runtime
	if parsedConfig.runtimeBackend == "container" {
		if _, err := exec.LookPath("nerdctl"); err != nil {
			fmt.Fprintln(os.Stderr, "error: nerdctl is not installed or not in PATH")
			os.Exit(1)
		}
		containerRuntime := runtime.NewContainerRuntime()
		containerRuntime.SetNetwork("cca-net", network.DefaultClusterCIDR)
		runtimeAdapter = containerRuntime
	} else {
		runtimeAdapter = runtime.NewProcessRuntime()
	}

	nodeAgent := agent.New(parsedConfig.nodeID, factStore, runtimeAdapter)

	if parsedConfig.advertiseAddress != "" {
		nodeAgent.SetAdvertiseAddress(parsedConfig.advertiseAddress)
		iptablesDataPlane := network.NewIptablesDataPlane()
		nodeAgent.SetDataPlaneProvider(iptablesDataPlane)
		fmt.Printf("Agent %s: data plane enabled (advertise-address: %s)\n",
			parsedConfig.nodeID, parsedConfig.advertiseAddress)
	}

	go func() {
		if runError := nodeAgent.Run(ctx); runError != nil {
			logging.Default().Error("node agent exited with error", "node", parsedConfig.nodeID, "error", runError.Error())
		}
	}()

	if parsedConfig.proxyEnabled {
		serviceResolver := network.NewStoreBackedResolver(factStore)
		serviceProxy := network.NewUserSpaceProxy(serviceResolver, parsedConfig.proxyListenAddress, factStore)
		go func() {
			if proxyStartError := serviceProxy.Start(ctx); proxyStartError != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "Proxy error: %v\n", proxyStartError)
			}
		}()
		fmt.Printf("Agent %s: HTTP proxy listening on %s (Host header routing)\n",
			parsedConfig.nodeID, parsedConfig.proxyListenAddress)
	}

	fmt.Printf("Agent %s running (runtime: %s). Watching for placements. Press Ctrl+C to stop.\n",
		parsedConfig.nodeID, parsedConfig.runtimeBackend)

	<-ctx.Done()
	fmt.Printf("\nAgent %s shutting down...\n", parsedConfig.nodeID)
	if nodeAgent.DataPlaneProvider() != nil {
		if cleanupError := nodeAgent.DataPlaneProvider().Cleanup(context.Background()); cleanupError != nil {
			logging.Default().Error("data plane cleanup failed", "error", cleanupError.Error())
		}
	}
	if processRuntime, ok := runtimeAdapter.(*runtime.ProcessRuntime); ok {
		processRuntime.StopAll(context.Background())
	}
	if containerRuntime, ok := runtimeAdapter.(*runtime.ContainerRuntime); ok {
		containerRuntime.StopAll(context.Background())
	}
}
