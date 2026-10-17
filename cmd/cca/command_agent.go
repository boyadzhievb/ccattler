// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Agent command implementation for the cca CLI.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// agentCommandConfig holds parsed flags for the "agent" command, which runs
// the node agent against a shared state store.
type agentCommandConfig struct {
	storeBackend           string
	etcdEndpoints          string
	storeKeyPrefix         string
	nodeID                 string
	runtimeBackend         string
	advertiseAddress       string
	tlsCertPath            string
	tlsKeyPath             string
	tlsCACertPath          string
	etcdCertPath           string
	etcdKeyPath            string
	etcdCACertPath         string
	proxyEnabled           bool
	proxyListenAddress     string
	logLevel               string
	logFormat              string
	kmsProvider            string // kmsProvider selects the KMS backend for secret decryption ("aws-kms", "gcp-kms", or empty for local).
	kmsKeyID               string // kmsKeyID is the KMS key ARN (AWS) or resource name (GCP) for secret decryption.
	debugListenAddress     string // debugListenAddress is the address for the agent debug HTTP API (e.g. "127.0.0.1:9771").
	secretAPIListenAddress string // secretAPIListenAddress is the address for the per-instance secret HTTP API (e.g. "0.0.0.0:9772").
	dataPlaneBackend       string // dataPlaneBackend selects the data plane provider: "auto", "nftables", or "iptables" (default "auto").
}

// parseAgentCommandArgs extracts store and agent flags from the arguments
// following "agent". This function exceeds 80 lines because it is a flat
// flag-to-field switch statement — each case is a simple one-liner assignment.
// Extracting sub-groups would add indirection without improving clarity.
func parseAgentCommandArgs(args []string) agentCommandConfig {
	defaults := resolveStoreDefaultsForDaemon()
	parsedConfig := agentCommandConfig{
		storeBackend:     defaults.storeBackend,
		etcdEndpoints:    defaults.etcdEndpoints,
		storeKeyPrefix:   defaults.storeKeyPrefix,
		runtimeBackend:   "container",
		dataPlaneBackend: "auto",
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
				parsedConfig.storeBackend = "etcd"
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
		case "--kms-provider":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.kmsProvider = args[argIndex]
			}
		case "--kms-key":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.kmsKeyID = args[argIndex]
			}
		case "--debug-listen":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.debugListenAddress = args[argIndex]
			}
		case "--secret-api-listen":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.secretAPIListenAddress = args[argIndex]
			}
		case "--dataplane":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.dataPlaneBackend = args[argIndex]
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

	if parsedConfig.dataPlaneBackend != "auto" && parsedConfig.dataPlaneBackend != "nftables" && parsedConfig.dataPlaneBackend != "iptables" {
		fmt.Fprintf(os.Stderr, "error: unknown dataplane %q (must be \"auto\", \"nftables\", or \"iptables\")\n", parsedConfig.dataPlaneBackend)
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

	loadAgentTLSCredentialsIfPresent(parsedConfig.nodeID, parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	registerLocalNode(ctx, factStore, parsedConfig.nodeID)

	runtimeAdapter := createAgentRuntimeAdapter(parsedConfig.runtimeBackend, parsedConfig.advertiseAddress)
	nodeAgent := agent.New(parsedConfig.nodeID, factStore, runtimeAdapter)

	if parsedConfig.advertiseAddress != "" {
		nodeAgent.SetAdvertiseAddress(parsedConfig.advertiseAddress)
		dataPlaneProvider := createDataPlaneProvider(parsedConfig.dataPlaneBackend)
		nodeAgent.SetDataPlaneProvider(dataPlaneProvider)
		fmt.Printf("Agent %s: data plane enabled (advertise-address: %s)\n",
			parsedConfig.nodeID, parsedConfig.advertiseAddress)
	}

	configureAgentSecretProvider(ctx, nodeAgent, factStore, parsedConfig)

	if parsedConfig.secretAPIListenAddress != "" && nodeAgent.SecretProvider() != nil {
		secretAPIServer := agent.NewSecretAPIServer(parsedConfig.nodeID, parsedConfig.secretAPIListenAddress, nodeAgent.SecretProvider(), factStore)
		nodeAgent.SetSecretAPIServer(secretAPIServer)
		go func() {
			if secretAPIError := secretAPIServer.Start(ctx); secretAPIError != nil {
				logging.Default().Error("secret API server error", "node", parsedConfig.nodeID, "error", secretAPIError.Error())
			}
		}()
		fmt.Printf("Agent %s: secret API listening on %s\n", parsedConfig.nodeID, parsedConfig.secretAPIListenAddress)
	}

	go func() {
		if runError := nodeAgent.Run(ctx); runError != nil {
			logging.Default().Error("node agent exited with error", "node", parsedConfig.nodeID, "error", runError.Error())
		}
	}()

	if parsedConfig.debugListenAddress != "" {
		debugServer := agent.NewDebugServer(parsedConfig.nodeID, parsedConfig.debugListenAddress, runtimeAdapter)
		go func() {
			if debugError := debugServer.Start(ctx); debugError != nil {
				logging.Default().Error("debug API server error", "node", parsedConfig.nodeID, "error", debugError.Error())
			}
		}()
		publishableDebugAddress := buildPublishableDebugAddress(parsedConfig.debugListenAddress, parsedConfig.advertiseAddress)
		if _, putError := factStore.Put(ctx, types.KeyObservedNodeDebugAddress(parsedConfig.nodeID), []byte(publishableDebugAddress)); putError != nil {
			logging.Default().Error("failed to publish debug address", "node", parsedConfig.nodeID, "error", putError.Error())
		}
		fmt.Printf("Agent %s: debug API listening on %s (published as %s)\n", parsedConfig.nodeID, parsedConfig.debugListenAddress, publishableDebugAddress)
	}

	if parsedConfig.proxyEnabled {
		startAgentHTTPProxy(ctx, factStore, parsedConfig.nodeID, parsedConfig.proxyListenAddress)
	}

	fmt.Printf("Agent %s running (runtime: %s). Watching for placements. Press Ctrl+C to stop.\n",
		parsedConfig.nodeID, parsedConfig.runtimeBackend)

	<-ctx.Done()
	fmt.Printf("\nAgent %s shutting down...\n", parsedConfig.nodeID)
	performAgentShutdownCleanup(nodeAgent, runtimeAdapter)
}

// loadAgentTLSCredentialsIfPresent discovers TLS credentials in the default data
// directory if no explicit paths were provided, and loads them if found. Prints
// status messages about credential discovery and loading.
func loadAgentTLSCredentialsIfPresent(nodeID, tlsCertPath, tlsKeyPath, tlsCACertPath string) {
	if tlsCertPath == "" {
		const systemPKIDirectory = "/etc/ccattler/pki"
		discoveredCertPath := systemPKIDirectory + "/node.pem"
		discoveredKeyPath := systemPKIDirectory + "/node-key.pem"
		discoveredCACertPath := systemPKIDirectory + "/ca.pem"
		if fileExists(discoveredCertPath) && fileExists(discoveredKeyPath) && fileExists(discoveredCACertPath) {
			tlsCertPath = discoveredCertPath
			tlsKeyPath = discoveredKeyPath
			tlsCACertPath = discoveredCACertPath
			fmt.Printf("Agent %s auto-discovered credentials from %s\n", nodeID, systemPKIDirectory)
		}
	}
	if tlsCertPath != "" {
		loadServerTLSConfig(tlsCertPath, tlsKeyPath, tlsCACertPath, false)
		fmt.Printf("Agent %s TLS credentials loaded\n", nodeID)
	}
}

// createAgentRuntimeAdapter creates the appropriate runtime adapter based on
// the configured backend. For "container" it creates a ContainerRuntime with
// the cluster network configured and DNS pointed at the node's advertise
// address (where dnsmasq forwards to the CCattler DNS server). For "process"
// it creates a ProcessRuntime.
func createAgentRuntimeAdapter(runtimeBackend string, advertiseAddress string) runtime.Runtime {
	if runtimeBackend == "container" {
		if _, lookupError := exec.LookPath("nerdctl"); lookupError != nil {
			fmt.Fprintln(os.Stderr, "error: nerdctl is not installed or not in PATH")
			os.Exit(1)
		}
		containerRuntime := runtime.NewContainerRuntime()
		containerRuntime.SetNetwork("cca-net", network.DefaultClusterCIDR)
		if advertiseAddress != "" {
			containerRuntime.SetDNS(advertiseAddress)
		}
		return containerRuntime
	}
	return runtime.NewProcessRuntime()
}

// createDataPlaneProvider selects the data plane implementation based on
// the --dataplane flag. "auto" tries nftables first, falling back to
// iptables. "nftables" or "iptables" forces a specific backend.
func createDataPlaneProvider(dataPlaneBackend string) network.DataPlaneProvider {
	switch dataPlaneBackend {
	case "nftables":
		fmt.Println("Data plane: nftables (forced)")
		return network.NewNftablesDataPlane()
	case "iptables":
		fmt.Println("Data plane: iptables (forced)")
		return network.NewIptablesDataPlane()
	default:
		if network.CheckNftablesAvailable() == nil {
			fmt.Println("Data plane: nftables (auto-detected)")
			return network.NewNftablesDataPlane()
		}
		fmt.Println("Data plane: iptables (nftables not available, falling back)")
		return network.NewIptablesDataPlane()
	}
}

// startAgentHTTPProxy starts the userspace HTTP proxy that routes requests to
// backend service instances based on the Host header.
func startAgentHTTPProxy(ctx context.Context, factStore store.StateStore, nodeID, proxyListenAddress string) {
	serviceResolver := network.NewStoreBackedResolver(factStore)
	serviceProxy := network.NewUserSpaceProxy(serviceResolver, proxyListenAddress, factStore)
	go func() {
		if proxyStartError := serviceProxy.Start(ctx); proxyStartError != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Proxy error: %v\n", proxyStartError)
		}
	}()
	fmt.Printf("Agent %s: HTTP proxy listening on %s (Host header routing)\n",
		nodeID, proxyListenAddress)
}

// performAgentShutdownCleanup cleans up the data plane provider and stops all
// running workloads when the agent is shutting down.
func performAgentShutdownCleanup(nodeAgent *agent.Agent, runtimeAdapter runtime.Runtime) {
	if nodeAgent.DataPlaneProvider() != nil {
		if cleanupError := nodeAgent.DataPlaneProvider().Cleanup(context.Background()); cleanupError != nil {
			logging.Default().Error("data plane cleanup failed", "error", cleanupError.Error())
		}
	}
	if processRuntime, isProcessRuntime := runtimeAdapter.(*runtime.ProcessRuntime); isProcessRuntime {
		processRuntime.StopAll(context.Background())
	}
	if containerRuntime, isContainerRuntime := runtimeAdapter.(*runtime.ContainerRuntime); isContainerRuntime {
		containerRuntime.StopAll(context.Background())
	}
}

// configureAgentSecretProvider sets up the agent's secret provider based on
// KMS configuration. When --kms-provider is set, secrets are decrypted using
// the specified cloud KMS at materialization time. When not set, secrets use
// a local master key loaded from the data directory.
func configureAgentSecretProvider(ctx context.Context, nodeAgent *agent.Agent, factStore store.StateStore, parsedConfig agentCommandConfig) {
	if parsedConfig.kmsProvider != "" {
		if parsedConfig.kmsKeyID == "" {
			fmt.Fprintf(os.Stderr, "error: --kms-key is required when --kms-provider is set\n")
			os.Exit(1)
		}
		keyProvider, keyProviderError := agent.CreateAgentKeyProvider(ctx, parsedConfig.kmsProvider, parsedConfig.kmsKeyID)
		if keyProviderError != nil {
			fmt.Fprintf(os.Stderr, "error: create KMS key provider: %v\n", keyProviderError)
			os.Exit(1)
		}
		kmsSecretProvider := agent.NewKMSSecretProvider(factStore, keyProvider)
		nodeAgent.SetSecretProvider(kmsSecretProvider)
		fmt.Printf("Agent %s: secret decryption enabled (KMS provider: %s)\n",
			parsedConfig.nodeID, parsedConfig.kmsProvider)
		return
	}
	localMasterKey := loadOrGenerateSecretMasterKey()
	localSecretProvider, localError := agent.NewLocalSecretProvider(factStore, localMasterKey)
	if localError != nil {
		fmt.Fprintf(os.Stderr, "error: create local secret provider: %v\n", localError)
		os.Exit(1)
	}
	nodeAgent.SetSecretProvider(localSecretProvider)
	fmt.Printf("Agent %s: secret decryption enabled (local key)\n", parsedConfig.nodeID)
}

// buildPublishableDebugAddress returns the debug API address that remote
// clients should use. When the listen address binds to 0.0.0.0 or 127.0.0.1
// and an advertise address is configured, the advertise IP replaces the host
// portion so the address is reachable from other machines.
func buildPublishableDebugAddress(debugListenAddress string, advertiseAddress string) string {
	if advertiseAddress == "" {
		return debugListenAddress
	}
	host, port, splitError := net.SplitHostPort(debugListenAddress)
	if splitError != nil {
		return debugListenAddress
	}
	if host == "0.0.0.0" || host == "127.0.0.1" || host == "" {
		return net.JoinHostPort(advertiseAddress, port)
	}
	return debugListenAddress
}
