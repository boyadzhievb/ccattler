// Package main implements the ccattler CLI — the entry point for the CCattler
// container orchestrator. It provides commands for applying configurations,
// running workloads (as processes or containers), querying cluster status,
// and injecting simulated metrics.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"time"

	"math/rand"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/api"
	"github.com/boyadzhievb/ccattler/chaos"
	"github.com/boyadzhievb/ccattler/cloud"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/infra"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// version is set at build time via -ldflags.
var version = "dev"

// statusAPIListenAddress is the address the HTTP status API binds to when running
// in live mode (run, run-container, demo). The status and metric commands query this.
const statusAPIListenAddress = "127.0.0.1:9770"

// main parses the CLI command and dispatches to the appropriate handler function.
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
		if parsedJoinConfig.serverAddress == "" || parsedJoinConfig.joinToken == "" || parsedJoinConfig.nodeID == "" {
			fmt.Fprintln(os.Stderr, "usage: cca join <server-url> <token> --node-id <id> [--ca-cert <path>] [--data-dir <path>]")
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

// runCommandConfig holds all parsed flags and arguments for the "run" and
// "run-container" commands, including the store backend selection, etcd
// endpoint list, and key prefix for multi-cluster isolation.
type runCommandConfig struct {
	// configFilePath is the path to the .ccattler DSL file to apply.
	configFilePath string
	// watchModeEnabled enables periodic status output when true.
	watchModeEnabled bool
	// storeBackend selects the state store implementation: "memory" or "etcd".
	storeBackend string
	// etcdEndpoints is the comma-separated list of etcd server addresses.
	etcdEndpoints string
	// storeKeyPrefix is the key prefix for namespacing within a shared etcd cluster.
	storeKeyPrefix string
}

// applyCommandConfig holds parsed flags for the "apply" command, which can
// optionally connect to a remote store instead of running a local simulation.
type applyCommandConfig struct {
	// configFilePath is the path to the .ccattler DSL file to apply.
	configFilePath string
	// storeBackend selects the state store implementation: "memory" or "etcd".
	storeBackend string
	// etcdEndpoints is the comma-separated list of etcd server addresses.
	etcdEndpoints string
	// storeKeyPrefix is the key prefix for namespacing within a shared etcd cluster.
	storeKeyPrefix string
}

// parseApplyCommandArgs extracts the config file path and optional store flags
// from the arguments following "apply".
func parseApplyCommandArgs(args []string) applyCommandConfig {
	parsedConfig := applyCommandConfig{
		storeBackend:   "memory",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
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
		default:
			if parsedConfig.configFilePath == "" {
				parsedConfig.configFilePath = currentArg
			}
		}
	}

	if parsedConfig.storeBackend != "memory" && parsedConfig.storeBackend != "etcd" {
		fmt.Fprintf(os.Stderr, "error: unknown store backend %q (must be \"memory\" or \"etcd\")\n", parsedConfig.storeBackend)
		os.Exit(1)
	}

	return parsedConfig
}

// parseRunCommandArgs extracts the config file path, --watch flag, and store
// backend flags from the arguments following "run" or "run-container". Uses an
// index-based loop so paired flags (--store <value>) can consume the next argument.
func parseRunCommandArgs(args []string) runCommandConfig {
	parsedConfig := runCommandConfig{
		storeBackend:   "memory",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
	}

	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--watch", "-w":
			parsedConfig.watchModeEnabled = true
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
		default:
			if parsedConfig.configFilePath == "" {
				parsedConfig.configFilePath = currentArg
			}
		}
	}

	if parsedConfig.storeBackend != "memory" && parsedConfig.storeBackend != "etcd" {
		fmt.Fprintf(os.Stderr, "error: unknown store backend %q (must be \"memory\" or \"etcd\")\n", parsedConfig.storeBackend)
		os.Exit(1)
	}

	return parsedConfig
}

// createStateStoreFromConfig builds the appropriate StateStore implementation based
// on the parsed run-command configuration. For "memory" it returns an in-memory store.
// For "etcd" it connects to the specified endpoints with the given key prefix.
func createStateStoreFromConfig(parsedConfig runCommandConfig) (store.StateStore, error) {
	if parsedConfig.storeBackend == "etcd" {
		endpointList := strings.Split(parsedConfig.etcdEndpoints, ",")
		return store.NewEtcdStore(store.EtcdStoreConfig{
			Endpoints:   endpointList,
			KeyPrefix:   parsedConfig.storeKeyPrefix,
			DialTimeout: 5 * time.Second,
		})
	}
	return store.NewMemoryStore(), nil
}

// serverCommandConfig holds parsed flags for the "server" command, which runs
// the control plane (controllers + API) against a shared state store.
type serverCommandConfig struct {
	// storeBackend selects the state store implementation: "memory" or "etcd".
	storeBackend string
	// etcdEndpoints is the comma-separated list of etcd server addresses.
	etcdEndpoints string
	// storeKeyPrefix is the key prefix for namespacing within a shared etcd cluster.
	storeKeyPrefix string
	// listenAddress is the host:port the API server binds to.
	listenAddress string
	// tlsEnabled turns on mTLS for the API server with an auto-generated CA.
	tlsEnabled bool
	// tlsCertPath is the path to a PEM-encoded server certificate file.
	tlsCertPath string
	// tlsKeyPath is the path to a PEM-encoded server private key file.
	tlsKeyPath string
	// tlsCACertPath is the path to a PEM-encoded CA certificate for verifying client certs.
	tlsCACertPath string
	// dnsEnabled starts the built-in DNS server alongside the control plane.
	dnsEnabled bool
	// dnsListenAddress is the host:port the DNS server binds to (default ":15353").
	dnsListenAddress string
	// logLevel controls the minimum severity of log messages (debug/info/warn/error).
	logLevel string
	// logFormat selects human-readable or JSON log output (human/json).
	logFormat string
	// apiOnly runs the stateless API server without controllers, for horizontal scaling.
	apiOnly bool
	// controllersOnly runs leader-elected controllers without the API server.
	controllersOnly bool
	// nodeID identifies this control-plane replica for leader election (defaults to hostname).
	nodeID string
	// cloudProviderName selects the cloud provider for node lifecycle, load balancers,
	// and routes. Empty means no cloud integration. Valid: "aws", "gcp", "azure", "simulator".
	cloudProviderName string
	// cloudRegion is the cloud region for the provider (e.g. "us-east-1").
	cloudRegion string
}

// parseServerCommandArgs extracts store-related flags from the arguments
// following "server".
func parseServerCommandArgs(args []string) serverCommandConfig {
	parsedConfig := serverCommandConfig{
		storeBackend:   "etcd",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
		listenAddress:  "0.0.0.0:9770",
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
		case "--listen":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.listenAddress = args[argIndex]
			}
		case "--tls":
			parsedConfig.tlsEnabled = true
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
		case "--dns":
			parsedConfig.dnsEnabled = true
		case "--dns-listen":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.dnsListenAddress = args[argIndex]
				parsedConfig.dnsEnabled = true
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
		case "--api-only":
			parsedConfig.apiOnly = true
		case "--controllers-only":
			parsedConfig.controllersOnly = true
		case "--node-id":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		case "--cloud-provider":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.cloudProviderName = args[argIndex]
			}
		case "--cloud-region":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.cloudRegion = args[argIndex]
			}
		}
	}

	if parsedConfig.dnsEnabled && parsedConfig.dnsListenAddress == "" {
		parsedConfig.dnsListenAddress = ":15353"
	}

	if parsedConfig.storeBackend != "memory" && parsedConfig.storeBackend != "etcd" {
		fmt.Fprintf(os.Stderr, "error: unknown store backend %q (must be \"memory\" or \"etcd\")\n", parsedConfig.storeBackend)
		os.Exit(1)
	}

	if parsedConfig.tlsCertPath != "" || parsedConfig.tlsKeyPath != "" || parsedConfig.tlsCACertPath != "" {
		if parsedConfig.tlsCertPath == "" || parsedConfig.tlsKeyPath == "" || parsedConfig.tlsCACertPath == "" {
			fmt.Fprintln(os.Stderr, "error: --cert, --key, and --ca must all be specified together")
			os.Exit(1)
		}
		parsedConfig.tlsEnabled = true
	}

	if parsedConfig.apiOnly && parsedConfig.controllersOnly {
		fmt.Fprintln(os.Stderr, "error: --api-only and --controllers-only are mutually exclusive")
		os.Exit(1)
	}

	if parsedConfig.nodeID == "" {
		hostname, _ := os.Hostname()
		if hostname != "" {
			parsedConfig.nodeID = hostname
		} else {
			parsedConfig.nodeID = "controlplane-1"
		}
	}

	return parsedConfig
}

// agentCommandConfig holds parsed flags for the "agent" command, which runs
// the node agent against a shared state store.
type agentCommandConfig struct {
	storeBackend     string
	etcdEndpoints    string
	storeKeyPrefix   string
	nodeID           string
	runtimeBackend   string
	advertiseAddress   string
	tlsCertPath        string
	tlsKeyPath         string
	tlsCACertPath      string
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

// configureLogger sets up the process-wide structured logger from CLI flags.
func configureLogger(logLevel string, logFormat string) {
	level := logging.LevelInfo
	if logLevel != "" {
		level = logging.ParseLevel(logLevel)
	}
	format := logging.FormatHuman
	if logFormat == "json" {
		format = logging.FormatJSON
	}
	logging.SetDefault(logging.New(os.Stderr, level, format))
}

// createStateStoreFromServerConfig builds the appropriate StateStore for
// the server or agent command configuration.
func createStateStoreFromServerConfig(storeBackend, etcdEndpoints, storeKeyPrefix string) (store.StateStore, error) {
	if storeBackend == "etcd" {
		endpointList := strings.Split(etcdEndpoints, ",")
		return store.NewEtcdStore(store.EtcdStoreConfig{
			Endpoints:   endpointList,
			KeyPrefix:   storeKeyPrefix,
			DialTimeout: 5 * time.Second,
		})
	}
	return store.NewMemoryStore(), nil
}

// executeServerCommand starts the control plane: all reconciliation controllers
// and the HTTP API server. It connects to the shared state store and blocks
// until Ctrl+C. No node agent or runtime — that runs separately via "cca agent".
func executeServerCommand(parsedConfig serverCommandConfig) {
	configureLogger(parsedConfig.logLevel, parsedConfig.logFormat)
	factStore, storeCreationError := createStateStoreFromServerConfig(
		parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer factStore.Close()

	if parsedConfig.storeBackend == "etcd" {
		fmt.Printf("CCattler server connected to etcd at %s (prefix: %s)\n", parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	runControllers := !parsedConfig.apiOnly
	runAPIServer := !parsedConfig.controllersOnly

	eventLog := types.NewEventLog(factStore, 1000)

	if runControllers {
		instanceController := controllers.NewInstanceController()
		schedulerController := scheduler.NewScheduler()
		endpointController := controllers.NewEndpointController()
		failureController := controllers.NewFailureController()
		nodeFailureController := controllers.NewNodeFailureController()
		networkController := controllers.NewNetworkController()
		autoscaleController := controllers.NewAutoscaleController()
		intentResolverController := controllers.NewIntentResolverController()
		rolloutController := controllers.NewRolloutController()
		initController := controllers.NewInitController()
		warmZeroController := controllers.NewWarmZeroController()

		metricsCollector := controllers.NewMetricsCollector()

		controllerList := []controllers.Controller{
			instanceController, schedulerController, endpointController,
			failureController, nodeFailureController, networkController,
			autoscaleController, intentResolverController, rolloutController,
			initController, warmZeroController,
		}

		if parsedConfig.cloudProviderName != "" {
			cloudProviderInstance := createCloudProvider(parsedConfig.cloudProviderName, parsedConfig.cloudRegion)
			if cloudProviderInstance != nil {
				controllerList = append(controllerList,
					controllers.NewNodeLifecycleController(cloudProviderInstance),
					controllers.NewCloudLoadBalancerController(cloudProviderInstance),
					controllers.NewCloudRouteController(cloudProviderInstance),
				)
				fmt.Printf("Cloud controllers enabled (provider: %s)\n", parsedConfig.cloudProviderName)
			}
		}

		haControllerRunner := controllers.NewHARunner(factStore, parsedConfig.nodeID, metricsCollector,
			controllerList...)

		go func() {
			if runError := haControllerRunner.Run(ctx); runError != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "controller runner error: %v\n", runError)
			}
		}()
		fmt.Printf("Controllers started with leader election (node: %s)\n", parsedConfig.nodeID)
	}

	if runAPIServer {
		var serverTLSConfig *tls.Config
		var clusterCertificateAuthority *security.CertificateAuthority
		enrollmentEnabled := false
		if parsedConfig.tlsCertPath != "" {
			serverTLSConfig = loadServerTLSConfig(parsedConfig.tlsCertPath, parsedConfig.tlsKeyPath, parsedConfig.tlsCACertPath)
		} else if parsedConfig.tlsEnabled {
			serverTLSConfig, clusterCertificateAuthority = buildServerTLSConfig(ctx, parsedConfig.listenAddress)
			enrollmentEnabled = true
		}

		statusAPIServer := launchStatusAPIServer(factStore, parsedConfig.listenAddress, serverTLSConfig, enrollmentEnabled)
		statusAPIServer.SetEventLog(eventLog)
		statusAPIServer.SetWatchMultiplexer(api.NewWatchMultiplexer(factStore))

		if parsedConfig.apiOnly {
			statusAPIServer.SetServerMode(api.ServerModeAPIOnly)
		}

		if clusterCertificateAuthority != nil {
			enrollmentService := security.NewEnrollmentService(factStore, clusterCertificateAuthority, nil, 24*time.Hour)
			statusAPIServer.SetEnrollmentService(enrollmentService)
			fmt.Println("Node enrollment enabled — use 'cca token create' to generate join tokens")
		}

		if parsedConfig.dnsEnabled {
			serviceResolver := network.NewStoreBackedResolver(factStore)
			dnsServer := network.NewDNSServer(serviceResolver, parsedConfig.dnsListenAddress)
			go func() {
				if dnsStartError := dnsServer.Start(ctx); dnsStartError != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "DNS server error: %v\n", dnsStartError)
				}
			}()
			fmt.Printf("DNS server listening on %s (resolving *.%s)\n",
				parsedConfig.dnsListenAddress, network.DefaultDNSDomain)
		}
	}

	modeLabel := "full"
	if parsedConfig.apiOnly {
		modeLabel = "api-only"
	} else if parsedConfig.controllersOnly {
		modeLabel = "controllers-only"
	}

	protocol := "http"
	if parsedConfig.tlsEnabled {
		protocol = "https (mTLS)"
	}

	if runAPIServer {
		fmt.Printf("Server running (%s). API on %s (%s). Press Ctrl+C to stop.\n",
			modeLabel, parsedConfig.listenAddress, protocol)
	} else {
		fmt.Printf("Server running (%s). Leader election active (node: %s). Press Ctrl+C to stop.\n",
			modeLabel, parsedConfig.nodeID)
	}

	<-ctx.Done()
	fmt.Println("\nServer shutting down...")
}

// buildServerTLSConfig creates an ephemeral CA, issues a server certificate
// createCloudProvider returns a CloudProvider for the given provider name, or
// nil if the name is unrecognized. This is the factory used by `cca server`
// to instantiate the right cloud adapter based on the --cloud-provider flag.
func createCloudProvider(providerName string, region string) cloud.CloudProvider {
	switch providerName {
	case "aws":
		return cloud.NewAWSCloudProvider(region)
	case "gcp":
		return cloud.NewGCPCloudProvider("", region)
	case "azure":
		return cloud.NewAzureCloudProvider("", "", region)
	case "simulator":
		return cloud.NewSimulatorCloudProvider()
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown cloud provider %q, cloud controllers disabled\n", providerName)
		return nil
	}
}

// with auto-rotation, and returns a tls.Config plus the CA. The TLS config uses
// VerifyClientCertIfGiven so the enrollment endpoint can accept unauthenticated
// connections while all other endpoints enforce client certs via middleware. The
// CA certificate is written to ca.pem in the .ccattler/ data directory.
func buildServerTLSConfig(ctx context.Context, listenAddress string) (*tls.Config, *security.CertificateAuthority) {
	certificateAuthority, err := security.NewCertificateAuthority(10 * 365 * 24 * time.Hour)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating CA: %v\n", err)
		os.Exit(1)
	}

	listenHost, _, splitError := net.SplitHostPort(listenAddress)
	if splitError != nil {
		listenHost = listenAddress
	}

	var serverIPAddresses []net.IP
	if parsedIP := net.ParseIP(listenHost); parsedIP != nil {
		serverIPAddresses = append(serverIPAddresses, parsedIP)
	}
	serverIPAddresses = append(serverIPAddresses, net.ParseIP("127.0.0.1"))

	serverDNSNames := []string{"localhost"}
	if net.ParseIP(listenHost) == nil && listenHost != "" {
		serverDNSNames = append(serverDNSNames, listenHost)
	}

	certificateRequest := security.IssueCertificateRequest{
		CommonName:  "ccattler-server",
		DNSNames:    serverDNSNames,
		IPAddresses: serverIPAddresses,
		TTL:         24 * time.Hour,
	}

	serverCertRotator, err := security.NewCertificateRotator(certificateAuthority, certificateRequest, 0.7)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating server certificate: %v\n", err)
		os.Exit(1)
	}
	serverCertRotator.Start(ctx)

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(certificateAuthority.CACertificatePEM())

	dataDirectory := ".ccattler"
	if mkdirError := os.MkdirAll(dataDirectory, 0700); mkdirError != nil {
		fmt.Fprintf(os.Stderr, "error creating data directory: %v\n", mkdirError)
		os.Exit(1)
	}

	caCertPath := dataDirectory + "/ca.pem"
	if writeError := os.WriteFile(caCertPath, certificateAuthority.CACertificatePEM(), 0644); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing CA certificate: %v\n", writeError)
		os.Exit(1)
	}
	fmt.Printf("CA certificate written to %s\n", caCertPath)

	return &tls.Config{
		GetCertificate: serverCertRotator.GetCertificate,
		ClientCAs:      caCertPool,
		ClientAuth:     tls.VerifyClientCertIfGiven,
		MinVersion:     tls.VersionTLS13,
	}, certificateAuthority
}

// loadServerTLSConfig reads PEM-encoded certificate, key, and CA files from
// disk and returns a tls.Config that serves mTLS using those credentials.
// Used when --cert/--key/--ca flags are provided instead of --tls auto-generation.
func loadServerTLSConfig(certPath, keyPath, caCertPath string) *tls.Config {
	serverCertificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading server certificate: %v\n", err)
		os.Exit(1)
	}

	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading CA certificate: %v\n", err)
		os.Exit(1)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCertPEM) {
		fmt.Fprintln(os.Stderr, "error: CA certificate file contains no valid certificates")
		os.Exit(1)
	}

	fmt.Printf("TLS: cert=%s key=%s ca=%s\n", certPath, keyPath, caCertPath)

	return &tls.Config{
		Certificates: []tls.Certificate{serverCertificate},
		ClientCAs:    caCertPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// executeAgentCommand starts the node agent, which watches the shared state store
// for placements assigned to this node and reconciles the local runtime. It
// registers the node, starts the appropriate runtime, and blocks until Ctrl+C.
func executeAgentCommand(parsedConfig agentCommandConfig) {
	configureLogger(parsedConfig.logLevel, parsedConfig.logFormat)
	factStore, storeCreationError := createStateStoreFromServerConfig(
		parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer factStore.Close()

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

	types.WriteNode(ctx, factStore, types.Node{
		ID: parsedConfig.nodeID, State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

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

	go nodeAgent.Run(ctx)

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
		nodeAgent.DataPlaneProvider().Cleanup(context.Background())
	}
	if processRuntime, ok := runtimeAdapter.(*runtime.ProcessRuntime); ok {
		processRuntime.StopAll(context.Background())
	}
	if containerRuntime, ok := runtimeAdapter.(*runtime.ContainerRuntime); ok {
		containerRuntime.StopAll(context.Background())
	}
}

// tokenCommandConfig holds parsed flags for the "token" command, which manages
// enrollment join tokens stored in the shared fact store.
type tokenCommandConfig struct {
	action         string // "create", "list", or "revoke"
	nodeID         string // optional: scope token to a specific node
	tokenTTL       string // default "15m"
	tokenValue     string // for revoke: the token prefix to match
	storeBackend   string
	etcdEndpoints  string
	storeKeyPrefix string
}

// parseTokenCommandArgs extracts the subcommand and flags from the arguments
// following "token".
func parseTokenCommandArgs(args []string) tokenCommandConfig {
	parsedConfig := tokenCommandConfig{
		storeBackend:   "etcd",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
		tokenTTL:       "15m",
	}

	if len(args) > 0 {
		parsedConfig.action = args[0]
	}

	for argIndex := 1; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--node-id":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		case "--ttl":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.tokenTTL = args[argIndex]
			}
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
		default:
			if parsedConfig.action == "revoke" && parsedConfig.tokenValue == "" {
				parsedConfig.tokenValue = currentArg
			}
		}
	}

	return parsedConfig
}

// executeTokenCommand dispatches to the appropriate token subcommand: create
// generates a new join token, list shows active tokens, and revoke removes one.
func executeTokenCommand(parsedConfig tokenCommandConfig) {
	factStore, storeCreationError := createStateStoreFromServerConfig(
		parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer factStore.Close()

	enrollmentService := security.NewEnrollmentService(factStore, nil, nil, 0)

	ctx := context.Background()

	switch parsedConfig.action {
	case "create":
		tokenDuration, parseError := time.ParseDuration(parsedConfig.tokenTTL)
		if parseError != nil {
			fmt.Fprintf(os.Stderr, "error: invalid TTL %q: %v\n", parsedConfig.tokenTTL, parseError)
			os.Exit(1)
		}

		joinToken, createError := enrollmentService.GenerateJoinToken(ctx, parsedConfig.nodeID, tokenDuration)
		if createError != nil {
			fmt.Fprintf(os.Stderr, "error creating token: %v\n", createError)
			os.Exit(1)
		}

		fmt.Printf("Token:   %s\n", joinToken.Token)
		fmt.Printf("Expires: %s (in %s)\n", joinToken.ExpiresAt.Format("2006-01-02 15:04:05"), parsedConfig.tokenTTL)
		if parsedConfig.nodeID != "" {
			fmt.Printf("Node:    %s (scoped)\n", parsedConfig.nodeID)
		} else {
			fmt.Println("Node:    any")
		}
		fmt.Println()
		fmt.Println("Join command:")
		fmt.Printf("  cca join https://<server>:9770 %s --node-id <id>\n", joinToken.Token)

	case "list":
		tokens, listError := enrollmentService.ListJoinTokens(ctx)
		if listError != nil {
			fmt.Fprintf(os.Stderr, "error listing tokens: %v\n", listError)
			os.Exit(1)
		}

		if len(tokens) == 0 {
			fmt.Println("no active join tokens")
			return
		}

		fmt.Printf("%-72s  %-12s  %s\n", "TOKEN", "NODE", "EXPIRES")
		for _, token := range tokens {
			nodeScope := "any"
			if token.NodeID != "" {
				nodeScope = token.NodeID
			}
			expiresIn := time.Until(token.ExpiresAt).Round(time.Second)
			fmt.Printf("%-72s  %-12s  %s (in %s)\n",
				token.Token, nodeScope, token.ExpiresAt.Format("15:04:05"), expiresIn)
		}

	case "revoke":
		if parsedConfig.tokenValue == "" {
			fmt.Fprintln(os.Stderr, "usage: cca token revoke <token-prefix>")
			os.Exit(1)
		}
		revokeError := enrollmentService.RevokeJoinToken(ctx, parsedConfig.tokenValue)
		if revokeError != nil {
			fmt.Fprintf(os.Stderr, "error revoking token: %v\n", revokeError)
			os.Exit(1)
		}
		fmt.Println("Token revoked.")

	default:
		fmt.Fprintln(os.Stderr, "usage: cca token <create|list|revoke> [flags]")
		os.Exit(1)
	}
}

// joinCommandConfig holds parsed flags for the "join" command, which enrolls
// a node with the cluster by presenting a join token to the server.
type joinCommandConfig struct {
	serverAddress string // https://host:port
	joinToken     string
	nodeID        string
	caCertPath    string // optional: verify server cert against this CA
	dataDirectory string // where to write cert/key/ca (default ".ccattler")
}

// parseJoinCommandArgs extracts the server URL, token, and flags from the
// arguments following "join".
func parseJoinCommandArgs(args []string) joinCommandConfig {
	parsedConfig := joinCommandConfig{
		dataDirectory: ".ccattler",
	}

	positionalIndex := 0
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--node-id":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.nodeID = args[argIndex]
			}
		case "--ca-cert":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.caCertPath = args[argIndex]
			}
		case "--data-dir":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.dataDirectory = args[argIndex]
			}
		default:
			if positionalIndex == 0 {
				parsedConfig.serverAddress = currentArg
			} else if positionalIndex == 1 {
				parsedConfig.joinToken = currentArg
			}
			positionalIndex++
		}
	}

	return parsedConfig
}

// executeJoinCommand enrolls this node with the cluster by contacting the
// server's enrollment endpoint, presenting the join token, and saving the
// issued certificate material to the data directory.
func executeJoinCommand(parsedConfig joinCommandConfig) {
	fmt.Printf("Enrolling node %s with %s...\n", parsedConfig.nodeID, parsedConfig.serverAddress)

	var transportTLSConfig *tls.Config
	if parsedConfig.caCertPath != "" {
		caCertPEM, readError := os.ReadFile(parsedConfig.caCertPath)
		if readError != nil {
			fmt.Fprintf(os.Stderr, "error reading CA certificate: %v\n", readError)
			os.Exit(1)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCertPEM) {
			fmt.Fprintln(os.Stderr, "error: CA certificate file contains no valid certificates")
			os.Exit(1)
		}
		transportTLSConfig = &tls.Config{
			RootCAs:    caCertPool,
			MinVersion: tls.VersionTLS13,
		}
	} else {
		transportTLSConfig = &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS13,
		}
		fmt.Println("WARNING: no --ca-cert provided, server certificate will not be verified")
	}

	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: transportTLSConfig},
		Timeout:   30 * time.Second,
	}

	localIPAddresses := detectLocalIPAddresses()
	ipStrings := make([]string, len(localIPAddresses))
	for ipIndex, ipAddr := range localIPAddresses {
		ipStrings[ipIndex] = ipAddr.String()
	}

	enrollmentRequestBody, _ := json.Marshal(map[string]interface{}{
		"token":        parsedConfig.joinToken,
		"node_id":      parsedConfig.nodeID,
		"ip_addresses": ipStrings,
	})

	enrollmentURL := parsedConfig.serverAddress + "/api/enroll"
	httpResponse, requestError := httpClient.Post(enrollmentURL, "application/json",
		strings.NewReader(string(enrollmentRequestBody)))
	if requestError != nil {
		fmt.Fprintf(os.Stderr, "error contacting server: %v\n", requestError)
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	responseBody, _ := io.ReadAll(httpResponse.Body)

	var enrollmentResponse struct {
		CertificatePEM string `json:"certificate_pem"`
		PrivateKeyPEM  string `json:"private_key_pem"`
		CACertPEM      string `json:"ca_cert_pem"`
		Principal      string `json:"principal"`
		Error          string `json:"error"`
	}
	if jsonError := json.Unmarshal(responseBody, &enrollmentResponse); jsonError != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", jsonError)
		os.Exit(1)
	}

	if enrollmentResponse.Error != "" {
		fmt.Fprintf(os.Stderr, "enrollment failed: %s\n", enrollmentResponse.Error)
		os.Exit(1)
	}

	if mkdirError := os.MkdirAll(parsedConfig.dataDirectory, 0700); mkdirError != nil {
		fmt.Fprintf(os.Stderr, "error creating data directory: %v\n", mkdirError)
		os.Exit(1)
	}

	certPath := parsedConfig.dataDirectory + "/node.pem"
	keyPath := parsedConfig.dataDirectory + "/node-key.pem"
	caCertPath := parsedConfig.dataDirectory + "/ca.pem"

	if writeError := os.WriteFile(certPath, []byte(enrollmentResponse.CertificatePEM), 0644); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing certificate: %v\n", writeError)
		os.Exit(1)
	}
	if writeError := os.WriteFile(keyPath, []byte(enrollmentResponse.PrivateKeyPEM), 0600); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing private key: %v\n", writeError)
		os.Exit(1)
	}
	if writeError := os.WriteFile(caCertPath, []byte(enrollmentResponse.CACertPEM), 0644); writeError != nil {
		fmt.Fprintf(os.Stderr, "error writing CA certificate: %v\n", writeError)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("Enrolled as %s\n", enrollmentResponse.Principal)
	fmt.Printf("  Certificate: %s\n", certPath)
	fmt.Printf("  Private key: %s\n", keyPath)
	fmt.Printf("  CA cert:     %s\n", caCertPath)
	fmt.Println()
	fmt.Println("Start the agent with:")
	fmt.Printf("  cca agent --node-id %s --cert %s --key %s --ca %s\n",
		parsedConfig.nodeID, certPath, keyPath, caCertPath)
}

// fileExists returns true if the given path exists and is a regular file.
func fileExists(filePath string) bool {
	fileInfo, statError := os.Stat(filePath)
	return statError == nil && !fileInfo.IsDir()
}

// detectLocalIPAddresses returns the non-loopback IPv4 addresses of this machine.
func detectLocalIPAddresses() []net.IP {
	var localAddresses []net.IP
	networkInterfaces, interfaceError := net.Interfaces()
	if interfaceError != nil {
		return localAddresses
	}
	for _, networkInterface := range networkInterfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		interfaceAddresses, addressError := networkInterface.Addrs()
		if addressError != nil {
			continue
		}
		for _, interfaceAddress := range interfaceAddresses {
			var ipAddress net.IP
			switch typedAddress := interfaceAddress.(type) {
			case *net.IPNet:
				ipAddress = typedAddress.IP
			case *net.IPAddr:
				ipAddress = typedAddress.IP
			}
			if ipAddress != nil && ipAddress.To4() != nil && !ipAddress.IsLoopback() {
				localAddresses = append(localAddresses, ipAddress)
			}
		}
	}
	return localAddresses
}

// printUsage prints the CLI help text listing all available commands to stderr.
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
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "cluster management:")
	fmt.Fprintln(os.Stderr, "  status                       show cluster status (queries running instance)")
	fmt.Fprintln(os.Stderr, "  describe <type> <name>       detailed view (service, node, instance)")
	fmt.Fprintln(os.Stderr, "  top <nodes|workloads>        resource utilization overview")
	fmt.Fprintln(os.Stderr, "  get <resource>               services, instances, nodes, volumes, networking, secrets, config")
	fmt.Fprintln(os.Stderr, "  diff [flags] <file>           dry-run apply showing fact changes (add/modify)")
	fmt.Fprintln(os.Stderr, "  events [flags]               event stream (--follow for live, --service to filter)")
	fmt.Fprintln(os.Stderr, "  logs <service> [--follow] [--instance <id>]  container stdout/stderr")
	fmt.Fprintln(os.Stderr, "  scale <svc> <n>              scale a service to n instances")
	fmt.Fprintln(os.Stderr, "  watch [prefix]               stream fact store changes")
	fmt.Fprintln(os.Stderr, "  metric set <svc> <m> <v>     inject simulated metric")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags for run / run-container / apply:")
	fmt.Fprintln(os.Stderr, "  --watch, -w                  print status every 2s (run only)")
	fmt.Fprintln(os.Stderr, "  --store memory|etcd          state store backend (default: memory)")
	fmt.Fprintln(os.Stderr, "  --endpoints host:port,...    etcd endpoints (default: localhost:2379)")
	fmt.Fprintln(os.Stderr, "  --store-prefix /path/        etcd key prefix (default: /ccattler/)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags for server:")
	fmt.Fprintln(os.Stderr, "  --listen host:port           API listen address (default: 0.0.0.0:9770)")
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

// executeApplyCommand parses a .ccattler file and writes facts to the state store.
// In remote mode (--store etcd), it connects to the shared store, writes facts,
// and exits — controllers running in "cca server" handle reconciliation.
// In local mode (default), it runs a local simulation with 3 simulated nodes.
func executeApplyCommand(parsedConfig applyCommandConfig) {
	fileData, err := os.ReadFile(parsedConfig.configFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", parsedConfig.configFilePath, err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if parsedConfig.storeBackend == "etcd" {
		factStore, storeCreationError := createStateStoreFromServerConfig(
			parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
		if storeCreationError != nil {
			fmt.Fprintf(os.Stderr, "error connecting to etcd: %v\n", storeCreationError)
			os.Exit(1)
		}
		defer factStore.Close()

		fmt.Printf("Connected to etcd at %s (prefix: %s)\n", parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
		fmt.Printf("Applying %s...\n", parsedConfig.configFilePath)
		if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(err, parsedConfig.configFilePath))
			os.Exit(1)
		}
		fmt.Println("Facts written to store. Controllers will reconcile.")
		return
	}

	factStore := store.NewMemoryStore()
	defer factStore.Close()

	// Register 3 simulated nodes with equal capacity.
	for _, simulatedNodeID := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: simulatedNodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
			Architecture: "amd64",
		})
	}
	fmt.Println("Registered 3 simulated nodes")

	// Create and start all reconciliation controllers.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()
	rolloutController := controllers.NewRolloutController()
	initController := controllers.NewInitController()
	warmZeroController := controllers.NewWarmZeroController()
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController, initController, warmZeroController)
	go controllerRunner.Run(ctx)

	fmt.Printf("Applying %s...\n", parsedConfig.configFilePath)
	if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(err, parsedConfig.configFilePath))
		os.Exit(1)
	}

	// Wait for reconciliation to settle before printing status.
	time.Sleep(500 * time.Millisecond)
	fmt.Print(buildStatusTextOutput(ctx, factStore))
}

// executeLiveProcessCommand parses a .ccattler file and starts real OS processes
// via the ProcessRuntime and node agent. When watchModeEnabled is true, prints
// status every 2 seconds with timestamps; otherwise prints status once and blocks
// until Ctrl+C.
func executeLiveProcessCommand(parsedRunConfig runCommandConfig) {
	fileData, err := os.ReadFile(parsedRunConfig.configFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", parsedRunConfig.configFilePath, err)
		os.Exit(1)
	}

	factStore, storeCreationError := createStateStoreFromConfig(parsedRunConfig)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedRunConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer factStore.Close()

	if parsedRunConfig.storeBackend == "etcd" {
		fmt.Printf("Connected to etcd at %s (prefix: %s)\n", parsedRunConfig.etcdEndpoints, parsedRunConfig.storeKeyPrefix)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// This machine is the single node in single-machine mode.
	localNodeID := "local"
	types.WriteNode(ctx, factStore, types.Node{
		ID: localNodeID, State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	// Create and start reconciliation controllers. No cluster autoscaler in
	// single-machine mode — there is no infrastructure provider to add real nodes.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()
	rolloutController := controllers.NewRolloutController()
	initController := controllers.NewInitController()
	warmZeroController := controllers.NewWarmZeroController()

	eventLog := types.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, autoscaleController, intentResolverController, rolloutController, initController, warmZeroController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Start node agent with process runtime for real OS process execution.
	processRuntime := runtime.NewProcessRuntime()
	nodeAgent := agent.New(localNodeID, factStore, processRuntime)
	go nodeAgent.Run(ctx)

	statusAPIServer := launchStatusAPIServer(factStore, statusAPIListenAddress, nil, false)
	statusAPIServer.SetEventLog(eventLog)

	fmt.Printf("Applying %s...\n", parsedRunConfig.configFilePath)
	if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(err, parsedRunConfig.configFilePath))
		os.Exit(1)
	}

	fmt.Printf("Running. Status API on %s. Press Ctrl+C to stop.\n\n", statusAPIListenAddress)

	// Initial status after reconciliation settles.
	time.Sleep(1 * time.Second)
	fmt.Printf("[%s]\n", time.Now().Format("15:04:05"))
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	if parsedRunConfig.watchModeEnabled {
		statusPrintTicker := time.NewTicker(2 * time.Second)
		defer statusPrintTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				fmt.Println("\nShutting down...")
				processRuntime.StopAll(context.Background())
				return
			case <-statusPrintTicker.C:
				fmt.Printf("\n[%s]\n", time.Now().Format("15:04:05"))
				fmt.Print(buildStatusTextOutput(ctx, factStore))
			}
		}
	}

	// Default: block until Ctrl+C without repeating status.
	<-ctx.Done()
	fmt.Println("\nShutting down...")
	processRuntime.StopAll(context.Background())
}

// executeLiveContainerCommand parses a .ccattler file and starts real OCI containers
// via the ContainerRuntime (nerdctl CLI) and node agent. When watchModeEnabled is
// true, prints status every 2 seconds with timestamps; otherwise prints status once
// and blocks until Ctrl+C.
func executeLiveContainerCommand(parsedRunConfig runCommandConfig) {
	if _, err := exec.LookPath("nerdctl"); err != nil {
		fmt.Fprintln(os.Stderr, "error: nerdctl is not installed or not in PATH")
		fmt.Fprintln(os.Stderr, "install nerdctl from https://github.com/containerd/nerdctl")
		fmt.Fprintln(os.Stderr, "alternatively, use 'cca run <file>' to run as OS processes instead")
		os.Exit(1)
	}

	fileData, err := os.ReadFile(parsedRunConfig.configFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", parsedRunConfig.configFilePath, err)
		os.Exit(1)
	}

	factStore, storeCreationError := createStateStoreFromConfig(parsedRunConfig)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedRunConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer factStore.Close()

	if parsedRunConfig.storeBackend == "etcd" {
		fmt.Printf("Connected to etcd at %s (prefix: %s)\n", parsedRunConfig.etcdEndpoints, parsedRunConfig.storeKeyPrefix)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	localNodeID := "local"
	types.WriteNode(ctx, factStore, types.Node{
		ID: localNodeID, State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	// Create and start reconciliation controllers including network controller.
	// No cluster autoscaler in single-machine mode — there is no infrastructure
	// provider to add real nodes.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	networkController := controllers.NewNetworkController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()
	rolloutController := controllers.NewRolloutController()
	initController := controllers.NewInitController()

	eventLog := types.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, networkController,
		autoscaleController, intentResolverController, rolloutController, initController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Start node agent with container runtime for real nerdctl container execution.
	containerRuntime := runtime.NewContainerRuntime()
	containerRuntime.SetNetwork("cca-net", network.DefaultClusterCIDR)

	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()
	nodeAgent := agent.New(localNodeID, factStore, containerRuntime)
	nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
	go nodeAgent.Run(ctx)

	statusAPIServer := launchStatusAPIServer(factStore, statusAPIListenAddress, nil, false)
	statusAPIServer.SetEventLog(eventLog)

	fmt.Printf("Applying %s (container mode)...\n", parsedRunConfig.configFilePath)
	if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(err, parsedRunConfig.configFilePath))
		os.Exit(1)
	}

	fmt.Printf("Running. Status API on %s. Press Ctrl+C to stop.\n\n", statusAPIListenAddress)

	// Initial status after reconciliation settles.
	time.Sleep(1 * time.Second)
	fmt.Printf("[%s]\n", time.Now().Format("15:04:05"))
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	if parsedRunConfig.watchModeEnabled {
		statusPrintTicker := time.NewTicker(2 * time.Second)
		defer statusPrintTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				fmt.Println("\nShutting down containers...")
				containerRuntime.StopAll(context.Background())
				return
			case <-statusPrintTicker.C:
				fmt.Printf("\n[%s]\n", time.Now().Format("15:04:05"))
				fmt.Print(buildStatusTextOutput(ctx, factStore))
			}
		}
	}

	// Default: block until Ctrl+C without repeating status.
	<-ctx.Done()
	fmt.Println("\nShutting down containers...")
	containerRuntime.StopAll(context.Background())
}

// executeDemoCommand runs a built-in demo with a hardcoded service config
// and the SimulatorRuntime. Useful for testing the reconciliation pipeline
// without real processes or containers.
func executeDemoCommand() {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	localNodeID := "local"
	types.WriteNode(ctx, factStore, types.Node{
		ID: localNodeID, State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	// Create and start all reconciliation controllers.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()
	rolloutController := controllers.NewRolloutController()
	initController := controllers.NewInitController()
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	eventLog := types.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController, initController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Node agent with simulator runtime — no real processes, just state tracking.
	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := agent.New(localNodeID, factStore, simulatorRuntime)
	go nodeAgent.Run(ctx)

	builtinDemoConfig := `service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}`
	fmt.Println("Applying config:")
	fmt.Println(builtinDemoConfig)

	statusAPIServer := launchStatusAPIServer(factStore, statusAPIListenAddress, nil, false)
	statusAPIServer.SetEventLog(eventLog)

	if err := lang.Apply(ctx, factStore, builtinDemoConfig); err != nil {
		fmt.Fprintf(os.Stderr, "apply error: %v\n", err)
		os.Exit(1)
	}

	// Wait for reconciliation to settle before printing status.
	time.Sleep(1 * time.Second)
	fmt.Println()
	fmt.Print(buildStatusTextOutput(ctx, factStore))
}

// executeDistributedDemoCommand runs a multi-node demo with 3 simulated nodes.
// It deploys 6 instances spread across the nodes, then after 5 seconds kills
// node-1 to demonstrate failure detection, instance rescheduling, and recovery.
func executeDistributedDemoCommand() {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Register 3 simulated nodes with equal capacity.
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	for _, nodeID := range nodeIDs {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}
	fmt.Println("Registered 3 simulated nodes: node-1, node-2, node-3")

	// Create and start all controllers including the node failure detector.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()
	rolloutController := controllers.NewRolloutController()
	initController := controllers.NewInitController()
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	eventLog := types.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController,
		autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController, initController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Start 3 agents, each with its own simulator runtime.
	// node-1 gets a separate cancel context so we can kill it later.
	node1Context, killNode1 := context.WithCancel(ctx)
	defer killNode1()
	for _, nodeID := range nodeIDs {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		if nodeID == "node-1" {
			go nodeAgent.Run(node1Context)
		} else {
			go nodeAgent.Run(ctx)
		}
	}

	distributedDemoConfig := `service web {
    image nginx:1.28
    instances 6
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}`
	fmt.Println("\nApplying config:")
	fmt.Println(distributedDemoConfig)

	statusAPIServer := launchStatusAPIServer(factStore, statusAPIListenAddress, nil, false)
	statusAPIServer.SetEventLog(eventLog)

	if err := lang.Apply(ctx, factStore, distributedDemoConfig); err != nil {
		fmt.Fprintf(os.Stderr, "apply error: %v\n", err)
		os.Exit(1)
	}

	// Wait for initial reconciliation to settle.
	time.Sleep(2 * time.Second)
	fmt.Println()
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	fmt.Println("\n--- Killing node-1 in 5 seconds to demonstrate failure recovery ---")
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	// Kill node-1's agent — it stops writing heartbeats.
	killNode1()
	fmt.Println("node-1 agent killed. Waiting for failure detection and rescheduling...")
	fmt.Println()

	// Print status periodically to show the recovery in progress.
	statusPrintTicker := time.NewTicker(2 * time.Second)
	defer statusPrintTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nShutting down...")
			return
		case <-statusPrintTicker.C:
			fmt.Print(buildStatusTextOutput(ctx, factStore))
			fmt.Println()
		}
	}
}

// executeNetworkDemoCommand runs a multi-node demo with networking enabled:
// IP allocation from per-node subnets, VIP assignment, DNS resolution, and
// load-balanced traffic routing. Deploys two services and shows the complete
// networking state including per-instance IPs, service VIPs, and DNS records.
func executeNetworkDemoCommand() {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	// Register 3 simulated nodes with equal capacity.
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	for _, nodeID := range nodeIDs {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
		factStore.Put(ctx, types.KeyNetworkNodeSubnet(nodeID),
			[]byte(simulatorNetworkProvider.NodeSubnet(nodeID)))
	}
	fmt.Println("Registered 3 simulated nodes with networking:")
	for _, nodeID := range nodeIDs {
		fmt.Printf("  %s  subnet=%s\n", nodeID, simulatorNetworkProvider.NodeSubnet(nodeID))
	}

	// Create all controllers including the network controller.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	networkController := controllers.NewNetworkController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()
	rolloutController := controllers.NewRolloutController()
	initController := controllers.NewInitController()
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	eventLog := types.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, networkController,
		autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController, initController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Start 3 agents, each with its own simulator runtime and the shared network provider.
	for _, nodeID := range nodeIDs {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		go nodeAgent.Run(ctx)
	}

	networkDemoConfig := `service web {
    image nginx:1.28
    instances 4
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}

service api {
    image myapp:latest
    instances 2
    expose 3000
    resources {
        cpu 250m
        memory 256Mi
    }
}`
	fmt.Println("\nApplying config:")
	fmt.Println(networkDemoConfig)

	statusAPIServer := launchStatusAPIServer(factStore, statusAPIListenAddress, nil, false)
	statusAPIServer.SetEventLog(eventLog)

	if err := lang.Apply(ctx, factStore, networkDemoConfig); err != nil {
		fmt.Fprintf(os.Stderr, "apply error: %v\n", err)
		os.Exit(1)
	}

	// Wait for reconciliation to settle.
	time.Sleep(2 * time.Second)
	fmt.Println()
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	// Demonstrate load balancing via the simulator proxy.
	storeBackedResolver := network.NewStoreBackedResolver(factStore)
	simulatorProxy := network.NewSimulatorProxy(storeBackedResolver)

	fmt.Println("\n--- Load Balancing Demo ---")
	for _, serviceName := range []string{"web", "api"} {
		fmt.Printf("\nRouting 6 requests to %s:\n", serviceName)
		for requestIndex := 0; requestIndex < 6; requestIndex++ {
			selectedEndpoint, err := simulatorProxy.RouteRequest(ctx, serviceName)
			if err != nil {
				fmt.Printf("  request %d: ERROR %v\n", requestIndex+1, err)
				continue
			}
			fmt.Printf("  request %d → %s:%d (instance %s)\n",
				requestIndex+1, selectedEndpoint.IP, selectedEndpoint.Port, selectedEndpoint.InstanceID)
		}
	}

	fmt.Printf("\nRunning. Status API on %s. Press Ctrl+C to stop.\n", statusAPIListenAddress)

	statusPrintTicker := time.NewTicker(5 * time.Second)
	defer statusPrintTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nShutting down...")
			return
		case <-statusPrintTicker.C:
			fmt.Println()
			fmt.Print(buildStatusTextOutput(ctx, factStore))
		}
	}
}

// executeStorageDemoCommand runs a multi-node demo with persistent volumes.
// It deploys a postgres service with a volume and a web service without one,
// then kills the node running postgres to demonstrate that the persistent
// volume migrates to the replacement node.
func executeStorageDemoCommand() {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	simulatorStorageProvider := storage.NewSimulatorStorageProvider()
	simulatorStorageProvider.CreateVolume(ctx, "pgdata", 50*1024*1024*1024)

	nodeIDs := []string{"node-1", "node-2", "node-3"}
	for _, nodeID := range nodeIDs {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}
	fmt.Println("Registered 3 simulated nodes: node-1, node-2, node-3")

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	storageController := controllers.NewStorageController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()
	rolloutController := controllers.NewRolloutController()
	initController := controllers.NewInitController()
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	eventLog := types.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, storageController,
		autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController, initController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Track which context each node's agent uses so we can kill one later.
	nodeAgentContexts := make(map[string]context.CancelFunc)
	for _, nodeID := range nodeIDs {
		nodeContext, nodeCancel := context.WithCancel(ctx)
		nodeAgentContexts[nodeID] = nodeCancel

		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetStorageProvider(simulatorStorageProvider)
		go nodeAgent.Run(nodeContext)
	}

	storageDemoConfig := `volume pgdata {
    size 50Gi
    persistent true
}

service postgres {
    image postgres:16
    instances 1
    expose 5432
    resources {
        cpu 1000m
        memory 2048Mi
    }
    volume pgdata /var/lib/postgresql/data
}

service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}`
	fmt.Println("\nApplying config:")
	fmt.Println(storageDemoConfig)

	statusAPIServer := launchStatusAPIServer(factStore, statusAPIListenAddress, nil, false)
	statusAPIServer.SetEventLog(eventLog)

	if err := lang.Apply(ctx, factStore, storageDemoConfig); err != nil {
		fmt.Fprintf(os.Stderr, "apply error: %v\n", err)
		os.Exit(1)
	}

	time.Sleep(2 * time.Second)
	fmt.Println()
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	// Find which node is running postgres so we can kill it.
	postgresNodeID := findNodeRunningService(ctx, factStore, "postgres")
	if postgresNodeID == "" {
		fmt.Println("\nCould not determine which node runs postgres. Exiting.")
		return
	}

	fmt.Printf("\n--- Killing %s (running postgres) in 5 seconds to demonstrate volume migration ---\n", postgresNodeID)
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	if killFunc, exists := nodeAgentContexts[postgresNodeID]; exists {
		killFunc()
	}
	simulatorStorageProvider.ForceDetach(ctx, "pgdata")
	fmt.Printf("%s agent killed. Waiting for failure detection, volume force-detach, and rescheduling...\n\n", postgresNodeID)

	statusPrintTicker := time.NewTicker(2 * time.Second)
	defer statusPrintTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nShutting down...")
			return
		case <-statusPrintTicker.C:
			fmt.Print(buildStatusTextOutput(ctx, factStore))
			fmt.Println()
		}
	}
}

// executeChaosCommand runs a 30-second chaos test on a 3-node simulated cluster.
// It deploys two services, then randomly injects node kills, network partitions,
// controller restarts, and scale changes — printing live convergence results.
func executeChaosCommand() {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	nodeIDs := []string{"node-1", "node-2", "node-3"}
	chaosCluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	chaosCluster.Start(ctx)

	fmt.Println("=== CCattler Chaos Mode ===")
	fmt.Println()
	fmt.Println("Cluster: 3 nodes (node-1, node-2, node-3)")
	fmt.Println("Deploying: web (6 instances) + api (3 instances)")

	chaosCluster.DeployService(ctx, "web", "nginx:1.28", 6)
	chaosCluster.DeployService(ctx, "api", "myapp:latest", 3)

	convergenceDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(convergenceDeadline) {
		converged, statusDescription := chaosCluster.CheckConvergence(ctx)
		if converged {
			fmt.Printf("Initial deployment converged: %s\n", statusDescription)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	fmt.Println()
	fmt.Println("Starting chaos injection for 30 seconds...")
	fmt.Println()

	chaosConfig := chaos.ChaosConfig{
		Duration:           30 * time.Second,
		InjectionInterval:  3 * time.Second,
		ConvergenceTimeout: 10 * time.Second,
		EnabledScenarios: []chaos.FailureScenario{
			chaos.ScenarioNodeKill,
			chaos.ScenarioNodePartition,
			chaos.ScenarioControllerRestart,
			chaos.ScenarioScaleChange,
		},
		RandSource: rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	chaosRunner := chaos.NewChaosRunner(chaosConfig, chaosCluster)
	chaosRunner.SetEventCallback(func(event chaos.ChaosEvent) {
		elapsedSeconds := int(event.Elapsed.Seconds())
		convergenceStatus := "TIMEOUT"
		if event.Converged {
			convergenceStatus = fmt.Sprintf("CONVERGED in %.1fs", event.ConvergenceTime.Seconds())
		}
		_, statusDescription := chaosCluster.CheckConvergence(ctx)
		fmt.Printf("[%02d:%02d] INJECT  %-20s target=%-12s  %s  (%s)\n",
			elapsedSeconds/60, elapsedSeconds%60,
			event.Scenario, event.Target, convergenceStatus, statusDescription)
	})

	chaosEvents := chaosRunner.Run(ctx)

	fmt.Println()
	fmt.Println("=== Chaos Summary ===")

	convergedCount := 0
	var maxRecoveryTime time.Duration
	for _, event := range chaosEvents {
		if event.Converged {
			convergedCount++
		}
		if event.ConvergenceTime > maxRecoveryTime {
			maxRecoveryTime = event.ConvergenceTime
		}
	}

	convergenceRate := 0.0
	if len(chaosEvents) > 0 {
		convergenceRate = float64(convergedCount) / float64(len(chaosEvents)) * 100
	}

	fmt.Printf("Injections:      %d\n", len(chaosEvents))
	fmt.Printf("Converged:       %d/%d (%.0f%%)\n", convergedCount, len(chaosEvents), convergenceRate)
	fmt.Printf("Max recovery:    %.1fs\n", maxRecoveryTime.Seconds())

	_, finalStatus := chaosCluster.CheckConvergence(ctx)
	fmt.Printf("Final state:     %s\n", finalStatus)

	if convergenceRate >= 80 {
		fmt.Println("\nResult: PASS — cluster resilient under chaos")
	} else {
		fmt.Println("\nResult: FAIL — convergence rate below 80%")
	}
}

// findNodeRunningService looks up the node that is currently running an
// instance of the named service by scanning placements and instance facts.
func findNodeRunningService(ctx context.Context, factStore store.StateStore, serviceName string) string {
	allInstances, err := types.ListInstances(ctx, factStore)
	if err != nil {
		return ""
	}
	for _, instance := range allInstances {
		if instance.Service == serviceName && instance.State == types.InstanceRunning {
			placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID))
			if err == nil {
				return string(placementFact.Value)
			}
		}
	}
	return ""
}

// diffCommandConfig holds parsed flags for the "diff" command.
type diffCommandConfig struct {
	// configFilePath is the path to the .ccattler DSL file to diff.
	configFilePath string
	// storeBackend selects the state store implementation: "memory" or "etcd".
	storeBackend string
	// etcdEndpoints is the comma-separated list of etcd server addresses.
	etcdEndpoints string
	// storeKeyPrefix is the key prefix for namespacing within a shared etcd cluster.
	storeKeyPrefix string
}

// parseDiffCommandArgs extracts the config file path and optional store flags
// from the arguments following "diff".
func parseDiffCommandArgs(args []string) diffCommandConfig {
	parsedConfig := diffCommandConfig{
		storeBackend:   "memory",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
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
		default:
			if !strings.HasPrefix(currentArg, "-") {
				parsedConfig.configFilePath = currentArg
			}
		}
	}
	return parsedConfig
}

// executeDiffCommand parses a .ccattler file and shows what facts would change
// without writing anything. In etcd mode it compares against the live store.
// In memory mode (default) everything is an "add" since the store is empty.
func executeDiffCommand(parsedConfig diffCommandConfig) {
	fileData, err := os.ReadFile(parsedConfig.configFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", parsedConfig.configFilePath, err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if parsedConfig.storeBackend == "etcd" {
		factStore, storeCreationError := createStateStoreFromServerConfig(
			parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
		if storeCreationError != nil {
			fmt.Fprintf(os.Stderr, "error connecting to etcd: %v\n", storeCreationError)
			os.Exit(1)
		}
		defer factStore.Close()

		changes, diffError := lang.Diff(ctx, factStore, string(fileData))
		if diffError != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(diffError, parsedConfig.configFilePath))
			os.Exit(1)
		}
		printDiffChanges(changes)
		return
	}

	apiURL := "http://" + statusAPIListenAddress + "/api/diff"
	httpResponse, diffErr := http.Post(apiURL, "text/plain", strings.NewReader(string(fileData)))
	if diffErr != nil {
		factStore := store.NewMemoryStore()
		defer factStore.Close()

		changes, diffError := lang.Diff(ctx, factStore, string(fileData))
		if diffError != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(diffError, parsedConfig.configFilePath))
			os.Exit(1)
		}
		printDiffChanges(changes)
		return
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "diff error: %s\n", string(responseBody))
		os.Exit(1)
	}

	var changes []lang.FactChange
	if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&changes); decodeErr != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeErr)
		os.Exit(1)
	}
	printDiffChanges(changes)
}

// printDiffChanges renders fact changes as human-readable diff output.
func printDiffChanges(changes []lang.FactChange) {
	addedCount := 0
	modifiedCount := 0
	unchangedCount := 0

	for _, change := range changes {
		switch change.Type {
		case "add":
			addedCount++
			fmt.Printf("+ %s = %s\n", change.Key, change.NewValue)
		case "modify":
			modifiedCount++
			fmt.Printf("~ %s\n", change.Key)
			fmt.Printf("  - %s\n", change.OldValue)
			fmt.Printf("  + %s\n", change.NewValue)
		case "unchanged":
			unchangedCount++
		}
	}

	fmt.Println()
	fmt.Printf("%d added, %d modified, %d unchanged\n", addedCount, modifiedCount, unchangedCount)
}

// eventsCommandConfig holds parsed flags for the "events" command.
type eventsCommandConfig struct {
	// followMode enables real-time streaming of new events via SSE.
	followMode bool
	// serviceFilter limits output to events whose target contains this service name.
	serviceFilter string
}

// parseEventsCommandArgs extracts --follow and --service flags from the
// arguments following "events".
func parseEventsCommandArgs(args []string) eventsCommandConfig {
	parsedConfig := eventsCommandConfig{}
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--follow", "-f":
			parsedConfig.followMode = true
		case "--service", "-s":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.serviceFilter = args[argIndex]
			}
		}
	}
	return parsedConfig
}

// executeEventsCommand shows cluster events. In default mode it fetches recent
// events from the API. With --follow it connects to the SSE stream and prints
// new events in real time. The --service flag filters events by service name.
func executeEventsCommand(parsedConfig eventsCommandConfig) {
	if parsedConfig.followMode {
		executeEventsFollowMode(parsedConfig.serviceFilter)
		return
	}

	apiURL := "http://" + statusAPIListenAddress + "/api/logs"
	if parsedConfig.serviceFilter != "" {
		apiURL += "?target=" + parsedConfig.serviceFilter
	}
	httpResponse, err := http.Get(apiURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	var events []struct {
		Timestamp time.Time `json:"timestamp"`
		Kind      string    `json:"kind"`
		Target    string    `json:"target"`
		Detail    string    `json:"detail"`
		Source    string    `json:"source"`
	}
	if err := json.NewDecoder(httpResponse.Body).Decode(&events); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("no events")
		return
	}

	fmt.Printf("%-24s  %-22s  %-20s  %s\n", "TIMESTAMP", "KIND", "TARGET", "DETAIL")
	for _, event := range events {
		formattedTimestamp := event.Timestamp.Format("2006-01-02 15:04:05.000")
		fmt.Printf("%-24s  %-22s  %-20s  %s\n", formattedTimestamp, event.Kind, event.Target, event.Detail)
	}
}

// executeEventsFollowMode connects to the SSE event stream and prints new
// events as they arrive. Blocks until interrupted with Ctrl-C.
func executeEventsFollowMode(serviceFilter string) {
	apiURL := "http://" + statusAPIListenAddress + "/api/events/stream"
	if serviceFilter != "" {
		apiURL += "?service=" + serviceFilter
	}

	httpResponse, err := http.Get(apiURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	if httpResponse.Header.Get("Content-Type") != "text/event-stream" {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "unexpected response: %s\n", string(responseBody))
		os.Exit(1)
	}

	fmt.Printf("%-24s  %-22s  %-20s  %s\n", "TIMESTAMP", "KIND", "TARGET", "DETAIL")

	scanner := bufio.NewScanner(httpResponse.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		eventJSON := strings.TrimPrefix(line, "data: ")
		var event struct {
			Timestamp time.Time `json:"timestamp"`
			Kind      string    `json:"kind"`
			Target    string    `json:"target"`
			Detail    string    `json:"detail"`
		}
		if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
			continue
		}
		formattedTimestamp := event.Timestamp.Format("2006-01-02 15:04:05.000")
		fmt.Printf("%-24s  %-22s  %-20s  %s\n", formattedTimestamp, event.Kind, event.Target, event.Detail)
	}
}

// logsCommandConfig holds parsed flags for the "logs" command.
type logsCommandConfig struct {
	serviceName string
	instanceID  string
	follow      bool
}

// parseLogsCommandArgs extracts flags from the arguments following "logs".
func parseLogsCommandArgs(args []string) logsCommandConfig {
	parsedConfig := logsCommandConfig{}
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--follow", "-f":
			parsedConfig.follow = true
		case "--instance":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.instanceID = args[argIndex]
			}
		default:
			if !strings.HasPrefix(currentArg, "-") && parsedConfig.serviceName == "" {
				parsedConfig.serviceName = currentArg
			}
		}
	}
	return parsedConfig
}

// executeLogsCommand streams container stdout/stderr logs for a service or
// specific instance. Uses nerdctl/docker logs on the local machine.
func executeLogsCommand(parsedConfig logsCommandConfig) {
	if parsedConfig.serviceName == "" && parsedConfig.instanceID == "" {
		fmt.Fprintln(os.Stderr, "usage: cca logs <service> [--follow] [--instance <id>]")
		os.Exit(1)
	}

	if parsedConfig.instanceID != "" {
		streamContainerLogs(parsedConfig.instanceID, parsedConfig.follow)
		return
	}

	apiURL := "http://" + statusAPIListenAddress + "/api/state?prefix=" + "observed/instance/"
	httpResponse, err := http.Get(apiURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run', 'server', or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	var facts []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if decodeError := json.NewDecoder(httpResponse.Body).Decode(&facts); decodeError != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeError)
		os.Exit(1)
	}

	instanceIDs := findInstanceIDsForService(facts, parsedConfig.serviceName)
	if len(instanceIDs) == 0 {
		fmt.Fprintf(os.Stderr, "no running instances found for service %q\n", parsedConfig.serviceName)
		os.Exit(1)
	}

	if parsedConfig.follow {
		streamContainerLogs(instanceIDs[0], true)
		return
	}

	for _, instanceID := range instanceIDs {
		fmt.Printf("==> %s <==\n", instanceID)
		streamContainerLogs(instanceID, false)
		fmt.Println()
	}
}

// findInstanceIDsForService scans observed facts to find instance IDs belonging
// to the given service that are in a running state.
func findInstanceIDsForService(facts []struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}, serviceName string) []string {
	serviceMap := make(map[string]bool)
	stateMap := make(map[string]string)

	for _, fact := range facts {
		key := fact.Key
		if strings.HasSuffix(key, "/service") && fact.Value == serviceName {
			instanceID := strings.TrimPrefix(key, "observed/instance/")
			instanceID = strings.TrimSuffix(instanceID, "/service")
			serviceMap[instanceID] = true
		}
		if strings.HasSuffix(key, "/state") {
			instanceID := strings.TrimPrefix(key, "observed/instance/")
			instanceID = strings.TrimSuffix(instanceID, "/state")
			stateMap[instanceID] = fact.Value
		}
	}

	var instanceIDs []string
	for instanceID := range serviceMap {
		if stateMap[instanceID] == "running" {
			instanceIDs = append(instanceIDs, instanceID)
		}
	}
	sort.Strings(instanceIDs)
	return instanceIDs
}

// streamContainerLogs runs nerdctl logs for the given instance and streams
// output to stdout. Falls back to docker if nerdctl is not available.
func streamContainerLogs(instanceID string, follow bool) {
	containerTool := "nerdctl"
	if _, lookupErr := exec.LookPath("nerdctl"); lookupErr != nil {
		containerTool = "docker"
	}

	logsArgs := []string{"logs"}
	if follow {
		logsArgs = append(logsArgs, "--follow")
	}
	logsArgs = append(logsArgs, "cca-"+instanceID)

	logsCommand := exec.Command(containerTool, logsArgs...)
	logsCommand.Stdout = os.Stdout
	logsCommand.Stderr = os.Stderr
	if runError := logsCommand.Run(); runError != nil {
		fmt.Fprintf(os.Stderr, "logs for %s: %v\n", instanceID, runError)
	}
}

// executeStatusCommand queries the status API of a running ccattler instance
// and prints the cluster status to stdout. Requires a running 'run' or 'demo' instance.
func executeStatusCommand() {
	statusRequest, _ := http.NewRequest("GET", "http://"+statusAPIListenAddress+"/status", nil)
	statusRequest.Header.Set("Accept", "text/plain")
	httpResponse, err := http.DefaultClient.Do(statusRequest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()
	responseBody, _ := io.ReadAll(httpResponse.Body)
	fmt.Print(string(responseBody))
}

// executeMetricSetCommand sends a simulated metric value to a running ccattler instance
// via the status API. The metric is stored in the fact store at observed/metric/service/{service}/{metric}.
func executeMetricSetCommand(serviceName, metricName, metricValue string) {
	requestURL := fmt.Sprintf("http://%s/metric?service=%s&metric=%s&value=%s",
		statusAPIListenAddress, serviceName, metricName, metricValue)
	httpResponse, err := http.Post(requestURL, "", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()
	responseBody, _ := io.ReadAll(httpResponse.Body)
	fmt.Print(string(responseBody))
}

// executeTopCommand queries the cluster API and displays resource utilization
// in a tabular format, similar to `kubectl top`.
func executeTopCommand(resourceType string) {
	apiBaseURL := "http://" + statusAPIListenAddress + "/api/status"
	httpResponse, err := http.Get(apiBaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	var clusterStatus api.ClusterStatus
	if err := json.NewDecoder(httpResponse.Body).Decode(&clusterStatus); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	switch resourceType {
	case "nodes", "node":
		fmt.Printf("%-14s  %-12s  %-16s  %-16s  %s\n", "NODE", "STATUS", "CPU", "MEMORY", "WORKLOADS")
		for _, nodeStatus := range clusterStatus.Nodes {
			cpuDisplay := formatResourceUsage(nodeStatus.CapacityCPU-nodeStatus.AvailableCPU, nodeStatus.CapacityCPU, "m")
			memoryDisplay := formatResourceUsage(nodeStatus.CapacityMemory-nodeStatus.AvailableMemory, nodeStatus.CapacityMemory, "Mi")
			fmt.Printf("%-14s  %-12s  %-16s  %-16s  %d\n",
				nodeStatus.ID, nodeStatus.State, cpuDisplay, memoryDisplay, nodeStatus.PlacedInstances)
		}
	case "workloads", "workload", "instances", "inst":
		fmt.Printf("%-24s  %-14s  %-10s  %-10s  %-10s  %s\n", "WORKLOAD", "NODE", "STATUS", "CPU", "MEMORY", "HEALTH")
		for _, instanceStatus := range clusterStatus.Instances {
			healthDisplay := instanceStatus.HealthState
			if healthDisplay == "" || healthDisplay == "-" {
				healthDisplay = "-"
			}
			nodeDisplay := instanceStatus.NodeID
			if nodeDisplay == "" {
				nodeDisplay = "-"
			}
			cpuDisplay := instanceStatus.CPUMillis
			if cpuDisplay == "" {
				cpuDisplay = "-"
			} else {
				cpuDisplay = cpuDisplay + "m"
			}
			memDisplay := instanceStatus.MemoryBytes
			if memDisplay == "" {
				memDisplay = "-"
			}
			workloadName := instanceStatus.ServiceName + "/" + instanceStatus.ID
			fmt.Printf("%-24s  %-14s  %-10s  %-10s  %-10s  %s\n",
				workloadName, nodeDisplay, instanceStatus.State, cpuDisplay, memDisplay, healthDisplay)
		}
	case "volumes", "volume", "vol":
		fmt.Printf("%-16s  %-10s  %-12s  %-14s  %-16s  %s\n", "VOLUME", "STATE", "SIZE", "NODE", "USAGE", "INSTANCE")
		for _, volumeStatus := range clusterStatus.Volumes {
			nodeDisplay := volumeStatus.Node
			if nodeDisplay == "" {
				nodeDisplay = "-"
			}
			instanceDisplay := volumeStatus.Instance
			if instanceDisplay == "" {
				instanceDisplay = "-"
			}
			usageDisplay := "-"
			if volumeStatus.CapacityBytes > 0 {
				usageDisplay = formatResourceUsage(volumeStatus.UsedBytes, volumeStatus.CapacityBytes, "B")
			}
			fmt.Printf("%-16s  %-10s  %-12s  %-14s  %-16s  %s\n",
				volumeStatus.Name, volumeStatus.State, volumeStatus.Size, nodeDisplay, usageDisplay, instanceDisplay)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s (use 'nodes', 'workloads', or 'volumes')\n", resourceType)
		os.Exit(1)
	}
}

// formatResourceUsage formats a used/capacity pair as "used/capacity unit" or
// "used/capacity unit (pct%)" for display in `cca top`.
func formatResourceUsage(used, capacity int64, unit string) string {
	if capacity <= 0 {
		return fmt.Sprintf("%d%s", used, unit)
	}
	percentage := (used * 100) / capacity
	return fmt.Sprintf("%d/%d%s (%d%%)", used, capacity, unit, percentage)
}

// executeDescribeCommand queries the describe API for a single resource and
// prints a detailed human-readable view of all related facts, health state,
// placement, networking, and recent events.
func executeDescribeCommand(resourceType, resourceName string) {
	normalizedType := resourceType
	switch resourceType {
	case "service", "svc":
		normalizedType = "service"
	case "node":
		normalizedType = "node"
	case "instance", "inst":
		normalizedType = "instance"
	default:
		fmt.Fprintf(os.Stderr, "unknown resource type: %s (use service, node, or instance)\n", resourceType)
		os.Exit(1)
	}

	apiURL := fmt.Sprintf("http://%s/api/describe?type=%s&name=%s",
		statusAPIListenAddress, normalizedType, resourceName)
	httpResponse, err := http.Get(apiURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode == http.StatusNotFound {
		var errorBody map[string]string
		json.NewDecoder(httpResponse.Body).Decode(&errorBody)
		fmt.Fprintf(os.Stderr, "%s\n", errorBody["error"])
		os.Exit(1)
	}
	if httpResponse.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(httpResponse.Body)
		fmt.Fprintf(os.Stderr, "error: %s\n", string(responseBody))
		os.Exit(1)
	}

	switch normalizedType {
	case "service":
		var serviceDetail api.ServiceDescribe
		if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&serviceDetail); decodeErr != nil {
			fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeErr)
			os.Exit(1)
		}
		printServiceDescribe(serviceDetail)
	case "node":
		var nodeDetail api.NodeDescribe
		if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&nodeDetail); decodeErr != nil {
			fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeErr)
			os.Exit(1)
		}
		printNodeDescribe(nodeDetail)
	case "instance":
		var instanceDetail api.InstanceDescribe
		if decodeErr := json.NewDecoder(httpResponse.Body).Decode(&instanceDetail); decodeErr != nil {
			fmt.Fprintf(os.Stderr, "decode response: %v\n", decodeErr)
			os.Exit(1)
		}
		printInstanceDescribe(instanceDetail)
	}
}

// printServiceDescribe renders a ServiceDescribe as human-readable text output.
func printServiceDescribe(serviceDetail api.ServiceDescribe) {
	fmt.Printf("Name:       %s\n", serviceDetail.Name)
	fmt.Printf("Image:      %s\n", serviceDetail.Image)
	fmt.Printf("Instances:  %d desired, %d running\n", serviceDetail.DesiredInstances, serviceDetail.RunningInstances)
	if len(serviceDetail.ExposedPorts) > 0 {
		portStrings := make([]string, len(serviceDetail.ExposedPorts))
		for portIndex, port := range serviceDetail.ExposedPorts {
			portStrings[portIndex] = fmt.Sprintf("%d", port)
		}
		fmt.Printf("Ports:      %s\n", strings.Join(portStrings, ", "))
	}

	if serviceDetail.Resources != nil {
		fmt.Println()
		fmt.Println("Resources:")
		if serviceDetail.Resources.CPU != "" {
			fmt.Printf("  CPU:      %s\n", serviceDetail.Resources.CPU)
		}
		if serviceDetail.Resources.Memory != "" {
			fmt.Printf("  Memory:   %s\n", serviceDetail.Resources.Memory)
		}
	}

	if serviceDetail.Placement != nil {
		fmt.Println()
		fmt.Println("Placement:")
		if serviceDetail.Placement.Architecture != "" {
			fmt.Printf("  Architecture:  %s\n", serviceDetail.Placement.Architecture)
		}
		if serviceDetail.Placement.ZonePolicy != "" {
			fmt.Printf("  Zone policy:   %s\n", serviceDetail.Placement.ZonePolicy)
		}
		for label, value := range serviceDetail.Placement.Require {
			fmt.Printf("  Require:       %s = %s\n", label, value)
		}
		for label, value := range serviceDetail.Placement.Prefer {
			fmt.Printf("  Prefer:        %s = %s\n", label, value)
		}
		for _, label := range serviceDetail.Placement.Accept {
			fmt.Printf("  Accept:        %s\n", label)
		}
	}

	if serviceDetail.Health != nil {
		fmt.Println()
		fmt.Println("Health Check:")
		fmt.Printf("  Method:    %s\n", serviceDetail.Health.Method)
		if serviceDetail.Health.Path != "" {
			fmt.Printf("  Path:      %s\n", serviceDetail.Health.Path)
		}
		if serviceDetail.Health.Interval != "" {
			fmt.Printf("  Interval:  %s\n", serviceDetail.Health.Interval)
		}
	}

	if len(serviceDetail.Probes) > 0 {
		fmt.Println()
		fmt.Println("Probes:")
		for _, probe := range serviceDetail.Probes {
			fmt.Printf("  %s:\n", probe.Type)
			fmt.Printf("    Method:    %s\n", probe.Method)
			if probe.Path != "" {
				fmt.Printf("    Path:      %s\n", probe.Path)
			}
			if probe.Port != "" {
				fmt.Printf("    Port:      %s\n", probe.Port)
			}
			if probe.Interval != "" {
				fmt.Printf("    Interval:  %s\n", probe.Interval)
			}
			if probe.Timeout != "" {
				fmt.Printf("    Timeout:   %s\n", probe.Timeout)
			}
		}
	}

	if len(serviceDetail.InitSteps) > 0 {
		fmt.Println()
		fmt.Println("Init Steps:")
		for _, step := range serviceDetail.InitSteps {
			fmt.Printf("  [%d] exec %q", step.Index, step.Exec)
			if step.Timeout != "" {
				fmt.Printf("  timeout=%s", step.Timeout)
			}
			if step.Retry != "" {
				fmt.Printf("  retry=%s", step.Retry)
			}
			fmt.Println()
		}
	}

	if serviceDetail.Autoscaling != nil {
		fmt.Println()
		fmt.Println("Autoscaling:")
		fmt.Printf("  Min: %d  Max: %d\n", serviceDetail.Autoscaling.Min, serviceDetail.Autoscaling.Max)
		for _, target := range serviceDetail.Autoscaling.Targets {
			fmt.Printf("  Target:  %s = %d\n", target.Metric, target.Value)
		}
	}

	if serviceDetail.UpdateStrategy != nil {
		fmt.Println()
		fmt.Println("Update Strategy:")
		if serviceDetail.UpdateStrategy.MaxUnavailable != "" {
			fmt.Printf("  Max unavailable:  %s\n", serviceDetail.UpdateStrategy.MaxUnavailable)
		}
		if serviceDetail.UpdateStrategy.MaxExtra != "" {
			fmt.Printf("  Max extra:        %s\n", serviceDetail.UpdateStrategy.MaxExtra)
		}
	}

	if serviceDetail.Rollout != nil {
		fmt.Println()
		fmt.Println("Rollout:")
		fmt.Printf("  State:  %s\n", serviceDetail.Rollout.State)
		if serviceDetail.Rollout.PreviousImage != "" {
			fmt.Printf("  Previous image:  %s\n", serviceDetail.Rollout.PreviousImage)
		}
		if serviceDetail.Rollout.Failures != "" {
			fmt.Printf("  Failures:        %s\n", serviceDetail.Rollout.Failures)
		}
	}

	if serviceDetail.Networking != nil {
		fmt.Println()
		fmt.Println("Networking:")
		if serviceDetail.Networking.VIP != "" {
			fmt.Printf("  VIP:   %s\n", serviceDetail.Networking.VIP)
		}
		if serviceDetail.Networking.Port > 0 {
			fmt.Printf("  Port:  %d\n", serviceDetail.Networking.Port)
		}
		if serviceDetail.Networking.DNS != "" {
			fmt.Printf("  DNS:   %s\n", serviceDetail.Networking.DNS)
		}
	}

	if len(serviceDetail.Config) > 0 {
		fmt.Println()
		fmt.Println("Config:")
		for _, configEntry := range serviceDetail.Config {
			fmt.Printf("  [%s] %s = %s\n", configEntry.Type, configEntry.Key, configEntry.Value)
		}
	}

	if len(serviceDetail.Secrets) > 0 {
		fmt.Println()
		fmt.Println("Secrets:")
		for _, secretGrant := range serviceDetail.Secrets {
			fmt.Printf("  %s -> %s\n", secretGrant.Name, secretGrant.MountPath)
		}
	}

	if len(serviceDetail.Endpoints) > 0 {
		fmt.Println()
		fmt.Println("Endpoints:")
		for _, endpoint := range serviceDetail.Endpoints {
			fmt.Printf("  %s\n", endpoint)
		}
	}

	if len(serviceDetail.Instances) > 0 {
		fmt.Println()
		fmt.Println("Instances:")
		fmt.Printf("  %-20s  %-10s  %-14s  %-16s  %-10s  %s\n",
			"ID", "STATE", "NODE", "IP", "HEALTH", "RESTARTS")
		for _, instanceSummary := range serviceDetail.Instances {
			restartDisplay := instanceSummary.Restarts
			if restartDisplay == "" {
				restartDisplay = "0"
			}
			fmt.Printf("  %-20s  %-10s  %-14s  %-16s  %-10s  %s\n",
				instanceSummary.ID, instanceSummary.State, instanceSummary.NodeID,
				instanceSummary.IPAddress, instanceSummary.Health, restartDisplay)
		}
	}

	printDescribeEvents(serviceDetail.Events)
}

// printNodeDescribe renders a NodeDescribe as human-readable text output.
func printNodeDescribe(nodeDetail api.NodeDescribe) {
	fmt.Printf("ID:            %s\n", nodeDetail.ID)
	fmt.Printf("State:         %s\n", nodeDetail.State)
	if nodeDetail.Address != "" {
		fmt.Printf("Address:       %s\n", nodeDetail.Address)
	}
	if nodeDetail.Architecture != "" {
		fmt.Printf("Architecture:  %s\n", nodeDetail.Architecture)
	}
	if nodeDetail.Zone != "" {
		fmt.Printf("Zone:          %s\n", nodeDetail.Zone)
	}
	if nodeDetail.Subnet != "" {
		fmt.Printf("Subnet:        %s\n", nodeDetail.Subnet)
	}

	fmt.Println()
	fmt.Println("Resources:")
	fmt.Printf("  CPU:     %d/%dm available\n", nodeDetail.AvailableCPU, nodeDetail.CapacityCPU)
	fmt.Printf("  Memory:  %d/%dMi available\n", nodeDetail.AvailableMemory, nodeDetail.CapacityMemory)
	if nodeDetail.UtilizationCPU != "" {
		fmt.Printf("  CPU utilization:     %s%%\n", nodeDetail.UtilizationCPU)
	}
	if nodeDetail.UtilizationMemory != "" {
		fmt.Printf("  Memory utilization:  %s%%\n", nodeDetail.UtilizationMemory)
	}
	if nodeDetail.WorkloadCount != "" {
		fmt.Printf("  Workloads:           %s\n", nodeDetail.WorkloadCount)
	}

	if len(nodeDetail.Labels) > 0 {
		fmt.Println()
		fmt.Println("Labels:")
		labelNames := make([]string, 0, len(nodeDetail.Labels))
		for labelName := range nodeDetail.Labels {
			labelNames = append(labelNames, labelName)
		}
		sort.Strings(labelNames)
		for _, labelName := range labelNames {
			fmt.Printf("  %s = %s\n", labelName, nodeDetail.Labels[labelName])
		}
	}

	if len(nodeDetail.Restrictions) > 0 {
		fmt.Println()
		fmt.Println("Restrictions:")
		for _, restriction := range nodeDetail.Restrictions {
			fmt.Printf("  %s\n", restriction)
		}
	}

	if len(nodeDetail.Instances) > 0 {
		fmt.Println()
		fmt.Println("Instances:")
		fmt.Printf("  %-20s  %-14s  %-10s  %-16s  %-10s  %s\n",
			"ID", "SERVICE", "STATE", "IP", "HEALTH", "RESTARTS")
		for _, instanceSummary := range nodeDetail.Instances {
			restartDisplay := instanceSummary.Restarts
			if restartDisplay == "" {
				restartDisplay = "0"
			}
			fmt.Printf("  %-20s  %-14s  %-10s  %-16s  %-10s  %s\n",
				instanceSummary.ID, instanceSummary.Service, instanceSummary.State,
				instanceSummary.IPAddress, instanceSummary.Health, restartDisplay)
		}
	}

	printDescribeEvents(nodeDetail.Events)
}

// printInstanceDescribe renders an InstanceDescribe as human-readable text output.
func printInstanceDescribe(instanceDetail api.InstanceDescribe) {
	fmt.Printf("ID:         %s\n", instanceDetail.ID)
	fmt.Printf("Service:    %s\n", instanceDetail.ServiceName)
	fmt.Printf("Node:       %s\n", instanceDetail.NodeID)
	fmt.Printf("State:      %s\n", instanceDetail.State)
	if instanceDetail.Image != "" {
		fmt.Printf("Image:      %s\n", instanceDetail.Image)
	}
	if instanceDetail.IPAddress != "" {
		fmt.Printf("IP:         %s\n", instanceDetail.IPAddress)
	}
	if instanceDetail.HostPort != "" {
		fmt.Printf("Host port:  %s\n", instanceDetail.HostPort)
	}
	if instanceDetail.Endpoint != "" {
		fmt.Printf("Endpoint:   %s\n", instanceDetail.Endpoint)
	}

	fmt.Println()
	fmt.Println("Health:")
	healthDisplay := instanceDetail.HealthState
	if healthDisplay == "" {
		healthDisplay = "-"
	}
	fmt.Printf("  Status:     %s\n", healthDisplay)
	if instanceDetail.InitPhase != "" {
		fmt.Printf("  Init:       %s\n", instanceDetail.InitPhase)
	}
	if instanceDetail.StartupProbe != "" {
		fmt.Printf("  Startup:    %s\n", instanceDetail.StartupProbe)
	}
	if instanceDetail.LivenessProbe != "" {
		fmt.Printf("  Liveness:   %s\n", instanceDetail.LivenessProbe)
	}
	if instanceDetail.ReadinessProbe != "" {
		fmt.Printf("  Readiness:  %s\n", instanceDetail.ReadinessProbe)
	}

	restartDisplay := instanceDetail.Restarts
	if restartDisplay == "" {
		restartDisplay = "0"
	}
	fmt.Printf("  Restarts:   %s\n", restartDisplay)

	if instanceDetail.CPUMillis != "" || instanceDetail.MemoryBytes != "" {
		fmt.Println()
		fmt.Println("Resources:")
		if instanceDetail.CPUMillis != "" {
			fmt.Printf("  CPU:     %sm\n", instanceDetail.CPUMillis)
		}
		if instanceDetail.MemoryBytes != "" {
			fmt.Printf("  Memory:  %s\n", instanceDetail.MemoryBytes)
		}
	}

	printDescribeEvents(instanceDetail.Events)
}

// printDescribeEvents renders the recent events section for any describe output.
func printDescribeEvents(events []api.DescribeEvent) {
	if len(events) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Events:")
	fmt.Printf("  %-20s  %-22s  %s\n", "TIMESTAMP", "KIND", "DETAIL")
	for _, event := range events {
		fmt.Printf("  %-20s  %-22s  %s\n", event.Timestamp, event.Kind, event.Detail)
	}
}

// executeGetCommand queries the API for a specific resource type and prints
// the result as formatted JSON.
func executeGetCommand(resourceType string) {
	apiBaseURL := "http://" + statusAPIListenAddress + "/api/status"
	httpResponse, err := http.Get(apiBaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	var status api.ClusterStatus
	if err := json.NewDecoder(httpResponse.Body).Decode(&status); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	switch resourceType {
	case "services", "svc":
		printServicesTable(status.Services)
	case "instances", "inst":
		printInstancesTable(status.Instances)
	case "nodes":
		printNodesTable(status.Nodes)
	case "volumes", "vol":
		printVolumesTable(status.Volumes)
	case "networking", "net":
		printNetworkingTable(status.Networking)
	case "secrets", "secret":
		printSecretsTable(status.Secrets)
	case "config", "cfg":
		printConfigTable(status.Config)
	case "cloud-identities", "cloud-identity", "identities":
		printCloudIdentitiesTable(status.CloudIdentities)
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s (use services, instances, nodes, volumes, networking, secrets, config, cloud-identities)\n", resourceType)
		os.Exit(1)
	}
}

// printAlignedTable renders rows as an aligned table with a header row. Column
// widths are computed from the maximum cell width in each column.
func printAlignedTable(header []string, rows [][]string) {
	columnWidths := make([]int, len(header))
	for columnIndex, columnHeader := range header {
		columnWidths[columnIndex] = len(columnHeader)
	}
	for _, row := range rows {
		for columnIndex, cell := range row {
			if columnIndex < len(columnWidths) && len(cell) > columnWidths[columnIndex] {
				columnWidths[columnIndex] = len(cell)
			}
		}
	}

	formatLine := func(cells []string) string {
		var builder strings.Builder
		for columnIndex, cell := range cells {
			if columnIndex > 0 {
				builder.WriteString("  ")
			}
			if columnIndex < len(columnWidths) {
				fmt.Fprintf(&builder, "%-*s", columnWidths[columnIndex], cell)
			} else {
				builder.WriteString(cell)
			}
		}
		return builder.String()
	}

	fmt.Println(formatLine(header))
	for _, row := range rows {
		fmt.Println(formatLine(row))
	}
}

// truncateValue shortens a string to maxLength, appending "..." if truncated.
func truncateValue(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	if maxLength <= 3 {
		return value[:maxLength]
	}
	return value[:maxLength-3] + "..."
}

func printServicesTable(services []api.ServiceStatus) {
	header := []string{"NAME", "IMAGE", "DESIRED", "RUNNING", "PORTS"}
	var rows [][]string
	for _, service := range services {
		portStrings := make([]string, len(service.ExposedPorts))
		for portIndex, port := range service.ExposedPorts {
			portStrings[portIndex] = fmt.Sprintf("%d", port)
		}
		portsDisplay := strings.Join(portStrings, ",")
		if portsDisplay == "" {
			portsDisplay = "-"
		}
		rows = append(rows, []string{
			service.Name,
			truncateValue(service.Image, 40),
			fmt.Sprintf("%d", service.DesiredCount),
			fmt.Sprintf("%d", service.RunningCount),
			portsDisplay,
		})
	}
	printAlignedTable(header, rows)
}

func printInstancesTable(instances []api.InstanceStatus) {
	header := []string{"ID", "SERVICE", "STATE", "NODE", "IP", "HEALTH"}
	var rows [][]string
	for _, instance := range instances {
		healthDisplay := instance.HealthState
		if healthDisplay == "" {
			healthDisplay = "-"
		}
		ipDisplay := instance.IPAddress
		if ipDisplay == "" {
			ipDisplay = "-"
		}
		rows = append(rows, []string{
			truncateValue(instance.ID, 12),
			instance.ServiceName,
			instance.State,
			instance.NodeID,
			ipDisplay,
			healthDisplay,
		})
	}
	printAlignedTable(header, rows)
}

func printNodesTable(nodes []api.NodeStatus) {
	header := []string{"ID", "STATE", "INSTANCES", "CPU (avail/total)", "MEMORY (avail/total)"}
	var rows [][]string
	for _, node := range nodes {
		cpuDisplay := fmt.Sprintf("%dm/%dm", node.AvailableCPU, node.CapacityCPU)
		memoryDisplay := fmt.Sprintf("%dMi/%dMi", node.AvailableMemory, node.CapacityMemory)
		rows = append(rows, []string{
			node.ID,
			node.State,
			fmt.Sprintf("%d", node.PlacedInstances),
			cpuDisplay,
			memoryDisplay,
		})
	}
	printAlignedTable(header, rows)
}

func printVolumesTable(volumes []api.VolumeStatus) {
	if len(volumes) == 0 {
		fmt.Println("No volumes found.")
		return
	}
	header := []string{"NAME", "SIZE", "STATE", "NODE", "INSTANCE", "MOUNT"}
	var rows [][]string
	for _, volume := range volumes {
		nodeDisplay := volume.Node
		if nodeDisplay == "" {
			nodeDisplay = "-"
		}
		instanceDisplay := volume.Instance
		if instanceDisplay == "" {
			instanceDisplay = "-"
		}
		mountDisplay := volume.MountPath
		if mountDisplay == "" {
			mountDisplay = "-"
		}
		rows = append(rows, []string{
			volume.Name,
			volume.Size,
			volume.State,
			nodeDisplay,
			truncateValue(instanceDisplay, 12),
			mountDisplay,
		})
	}
	printAlignedTable(header, rows)
}

func printNetworkingTable(networking []api.NetworkStatus) {
	if len(networking) == 0 {
		fmt.Println("No networking entries found.")
		return
	}
	header := []string{"SERVICE", "VIP", "PORT", "DNS"}
	var rows [][]string
	for _, entry := range networking {
		dnsDisplay := entry.DNS
		if dnsDisplay == "" {
			dnsDisplay = "-"
		}
		rows = append(rows, []string{
			entry.ServiceName,
			entry.VIP,
			fmt.Sprintf("%d", entry.Port),
			dnsDisplay,
		})
	}
	printAlignedTable(header, rows)
}

func printSecretsTable(secrets []api.SecretStatus) {
	if len(secrets) == 0 {
		fmt.Println("No secrets found.")
		return
	}
	header := []string{"NAME", "GRANTED TO"}
	var rows [][]string
	for _, secret := range secrets {
		grantedDisplay := strings.Join(secret.GrantedTo, ", ")
		if grantedDisplay == "" {
			grantedDisplay = "-"
		}
		rows = append(rows, []string{
			secret.Name,
			grantedDisplay,
		})
	}
	printAlignedTable(header, rows)
}

func printConfigTable(configEntries []api.ConfigStatus) {
	if len(configEntries) == 0 {
		fmt.Println("No config entries found.")
		return
	}
	header := []string{"SERVICE", "TYPE", "KEY", "VALUE"}
	var rows [][]string
	for _, entry := range configEntries {
		rows = append(rows, []string{
			entry.Service,
			entry.Type,
			entry.Key,
			truncateValue(entry.Value, 50),
		})
	}
	printAlignedTable(header, rows)
}

func printCloudIdentitiesTable(cloudIdentities []api.CloudIdentityStatus) {
	if len(cloudIdentities) == 0 {
		fmt.Println("No cloud identities found.")
		return
	}
	header := []string{"NAME", "PROVIDER", "SERVICES"}
	var rows [][]string
	for _, identity := range cloudIdentities {
		services := "-"
		if len(identity.Services) > 0 {
			services = strings.Join(identity.Services, ", ")
		}
		rows = append(rows, []string{
			identity.Name,
			identity.Provider,
			services,
		})
	}
	printAlignedTable(header, rows)
}

// executeScaleCommand sends a scale request to the API to change a service's
// desired instance count.
func executeScaleCommand(serviceName, countStr string) {
	requestBody := fmt.Sprintf(`{"service":%q,"instances":%s}`, serviceName, countStr)
	apiURL := "http://" + statusAPIListenAddress + "/api/scale"
	httpResponse, err := http.Post(apiURL, "application/json", strings.NewReader(requestBody))
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	responseBody, _ := io.ReadAll(httpResponse.Body)
	if httpResponse.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "scale failed: %s\n", string(responseBody))
		os.Exit(1)
	}
	fmt.Printf("scaled %s to %s instances\n", serviceName, countStr)
}

// executeWatchCommand connects to the API's SSE watch endpoint and prints
// fact store changes as they occur.
func executeWatchCommand(prefix string) {
	apiURL := fmt.Sprintf("http://%s/api/watch?prefix=%s", statusAPIListenAddress, prefix)
	httpResponse, err := http.Get(apiURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is 'run' or 'demo' running?")
		os.Exit(1)
	}
	defer httpResponse.Body.Close()

	fmt.Printf("watching %s ...\n", prefix)
	buffer := make([]byte, 4096)
	for {
		bytesRead, readErr := httpResponse.Body.Read(buffer)
		if bytesRead > 0 {
			fmt.Print(string(buffer[:bytesRead]))
		}
		if readErr != nil {
			return
		}
	}
}

// clusterStatusResponse is the structured representation of the full cluster status,
// used for JSON serialization via the status API.
type clusterStatusResponse struct {
	Services   []serviceStatusEntry  `json:"services"`             // all registered services with desired/running counts
	Instances  []instanceStatusEntry `json:"instances"`            // all active (non-stopped) instances
	Nodes      []nodeStatusEntry     `json:"nodes"`                // all registered nodes with capacity info
	Networking []networkStatusEntry  `json:"networking,omitempty"` // service VIP and DNS assignments
	Volumes    []volumeStatusEntry   `json:"volumes,omitempty"`    // persistent volumes with attachment state
}

// serviceStatusEntry represents one service in the cluster status output.
type serviceStatusEntry struct {
	Name            string `json:"name"`           // service name from the DSL config
	Image           string `json:"image"`          // container image or process command
	DesiredCount    int    `json:"desired"`         // how many instances should be running
	RunningCount    int    `json:"running"`         // how many instances are currently running
	ExposedPorts    []int  `json:"ports,omitempty"` // ports exposed by this service
}

// instanceStatusEntry represents one instance in the cluster status output.
type instanceStatusEntry struct {
	ID          string `json:"id"`      // unique instance identifier
	ServiceName string `json:"service"` // which service this instance belongs to
	State       string `json:"state"`   // current state: pending, running, failed
	NodeID      string `json:"node"`    // which node this instance is placed on
	IPAddress   string `json:"ip"`      // allocated IP address
	HealthState string `json:"health"`  // health check result: healthy, unhealthy, or "-"
}

// networkStatusEntry represents a service's networking configuration.
type networkStatusEntry struct {
	ServiceName string `json:"service"` // service name
	VIP         string `json:"vip"`     // virtual IP address
	Port        int    `json:"port"`    // VIP port
	DNS         string `json:"dns"`     // DNS name mapping
}

// nodeStatusEntry represents one node in the cluster status output.
type nodeStatusEntry struct {
	ID              string `json:"id"`               // unique node identifier
	State           string `json:"state"`             // node state: alive, unreachable, draining
	PlacedInstances int    `json:"instances"`         // number of active instances on this node
	AvailableCPU    int64  `json:"available_cpu"`     // remaining CPU capacity in millicores
	CapacityCPU     int64  `json:"capacity_cpu"`      // total CPU capacity in millicores
	AvailableMemory int64  `json:"available_memory"`  // remaining memory capacity in MiB
	CapacityMemory  int64  `json:"capacity_memory"`   // total memory capacity in MiB
}

// volumeStatusEntry represents one persistent volume in the cluster status output.
type volumeStatusEntry struct {
	Name      string `json:"name"`                // volume name from the DSL config
	Size      string `json:"size"`                // declared size (e.g. "50Gi")
	State     string `json:"state"`               // current state: available, attached
	Node      string `json:"node,omitempty"`      // node the volume is attached to
	Instance  string `json:"instance,omitempty"`  // instance the volume is mounted into
	MountPath string `json:"mount_path,omitempty"` // filesystem mount path
}

// requireClientCertMiddleware wraps an HTTP handler to reject requests that
// lack a verified client certificate. The /api/enroll path is exempted because
// joining nodes do not yet have credentials.
func requireClientCertMiddleware(wrappedHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/enroll", "/healthz", "/metrics":
			wrappedHandler.ServeHTTP(responseWriter, request)
			return
		}
		if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
			http.Error(responseWriter, "client certificate required", http.StatusUnauthorized)
			return
		}
		wrappedHandler.ServeHTTP(responseWriter, request)
	})
}

// launchStatusAPIServer starts the HTTP API server in the background. It hosts
// both the legacy /status and /metric endpoints and the new /api/* endpoints.
// When serverTLSConfig is non-nil, the listener is wrapped with TLS for mTLS.
// When enrollmentEnabled is true, client certs are enforced via middleware
// (except on /api/enroll) instead of at the TLS layer.
// Returns the api.Server so callers can attach optional components like EventLog.
func launchStatusAPIServer(factStore store.StateStore, listenAddress string, serverTLSConfig *tls.Config, enrollmentEnabled bool) *api.Server {
	apiServer := api.NewServer(factStore)

	httpMux := http.NewServeMux()
	httpMux.Handle("/api/", apiServer.Handler())

	// Expose /healthz and /metrics at the root level so load balancers and
	// Prometheus scrapers can reach them without the /api/ prefix.
	httpMux.Handle("/healthz", apiServer.Handler())
	httpMux.Handle("/metrics", apiServer.Handler())

	// Expose OIDC endpoints at root level when configured.
	httpMux.Handle("/.well-known/", apiServer.Handler())
	httpMux.Handle("/oidc/", apiServer.Handler())

	// Legacy status endpoint for backward compatibility with 'cca status'.
	httpMux.HandleFunc("/status", func(responseWriter http.ResponseWriter, request *http.Request) {
		requestContext := request.Context()
		acceptHeader := request.Header.Get("Accept")
		if strings.Contains(acceptHeader, "application/json") {
			responseWriter.Header().Set("Content-Type", "application/json")
			json.NewEncoder(responseWriter).Encode(buildClusterStatusJSON(requestContext, factStore))
			return
		}
		responseWriter.Header().Set("Content-Type", "text/plain")
		responseWriter.Write([]byte(buildStatusTextOutput(requestContext, factStore)))
	})

	// Legacy metric endpoint for backward compatibility with 'cca metric set'.
	httpMux.HandleFunc("/metric", func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(responseWriter, "POST only", http.StatusMethodNotAllowed)
			return
		}
		serviceName := request.URL.Query().Get("service")
		metricName := request.URL.Query().Get("metric")
		metricValue := request.URL.Query().Get("value")
		if serviceName == "" || metricName == "" || metricValue == "" {
			http.Error(responseWriter, "service, metric, and value required", http.StatusBadRequest)
			return
		}
		requestContext := request.Context()
		metricKey := types.KeyObservedMetric(serviceName, metricName)
		factStore.Put(requestContext, metricKey, []byte(metricValue))
		fmt.Fprintf(responseWriter, "set %s.%s = %s\n", serviceName, metricName, metricValue)
	})

	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return apiServer
	}
	if serverTLSConfig != nil {
		listener = tls.NewListener(listener, serverTLSConfig)
	}

	var serverHandler http.Handler = httpMux
	if enrollmentEnabled {
		serverHandler = requireClientCertMiddleware(httpMux)
	}

	go func() {
		if serveError := http.Serve(listener, serverHandler); serveError != nil {
			fmt.Fprintf(os.Stderr, "status api server: %v\n", serveError)
		}
	}()
	return apiServer
}

// buildClusterStatusJSON collects the full cluster state from the fact store
// and assembles it into a structured clusterStatusResponse for JSON serialization.
func buildClusterStatusJSON(ctx context.Context, factStore store.StateStore) clusterStatusResponse {
	var statusResponse clusterStatusResponse

	// Load all instances and sort by ID for deterministic output.
	allInstances, _ := types.ListInstances(ctx, factStore)
	sort.Slice(allInstances, func(i, j int) bool { return allInstances[i].ID < allInstances[j].ID })

	// Discover all unique service names from the desired state.
	desiredFacts, _ := factStore.Scan(ctx, types.ScanDesiredServices)
	uniqueServiceNames := make(map[string]bool)
	for _, fact := range desiredFacts {
		relativePath := strings.TrimPrefix(fact.Key, types.ScanDesiredServices)
		serviceName := strings.SplitN(relativePath, "/", 2)[0]
		uniqueServiceNames[serviceName] = true
	}
	sortedServiceNames := make([]string, 0, len(uniqueServiceNames))
	for serviceName := range uniqueServiceNames {
		sortedServiceNames = append(sortedServiceNames, serviceName)
	}
	sort.Strings(sortedServiceNames)

	// Build service status entries with running instance counts.
	for _, serviceName := range sortedServiceNames {
		service, err := types.ReadService(ctx, factStore, serviceName)
		if err != nil {
			continue
		}
		runningInstanceCount := 0
		for _, instance := range allInstances {
			if instance.Service == serviceName && instance.State == types.InstanceRunning {
				runningInstanceCount++
			}
		}
		statusResponse.Services = append(statusResponse.Services, serviceStatusEntry{
			Name: service.Name, Image: service.Image, DesiredCount: service.Instances,
			RunningCount: runningInstanceCount, ExposedPorts: service.Ports,
		})
	}

	// Build instance status entries, excluding stopped instances.
	for _, instance := range allInstances {
		if instance.State == types.InstanceStopped {
			continue
		}
		placedNodeID := ""
		if placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); err == nil {
			placedNodeID = string(placementFact.Value)
		}
		healthDisplay := string(instance.Health)
		if healthDisplay == "" {
			healthDisplay = "-"
		}
		instanceIPAddress := instance.IP
		if instanceIPAddress == "" {
			instanceIPAddress = "-"
		}
		statusResponse.Instances = append(statusResponse.Instances, instanceStatusEntry{
			ID: instance.ID, ServiceName: instance.Service, State: string(instance.State),
			NodeID: placedNodeID, IPAddress: instanceIPAddress, HealthState: healthDisplay,
		})
	}

	// Build networking status entries from VIP and DNS facts.
	vipFacts, _ := factStore.Scan(ctx, types.ScanNetworkVIPs)
	dnsFacts, _ := factStore.Scan(ctx, types.ScanNetworkDNS)
	dnsMapping := make(map[string]string)
	for _, dnsFact := range dnsFacts {
		serviceName := strings.TrimPrefix(dnsFact.Key, types.ScanNetworkDNS)
		dnsMapping[serviceName] = string(dnsFact.Value)
	}
	vipByService := make(map[string]string)
	vipPortByService := make(map[string]int)
	for _, vipFact := range vipFacts {
		relativePath := strings.TrimPrefix(vipFact.Key, types.ScanNetworkVIPs)
		pathParts := strings.Split(relativePath, "/")
		if len(pathParts) == 1 {
			vipByService[pathParts[0]] = string(vipFact.Value)
		} else if len(pathParts) == 2 && pathParts[1] == "port" {
			portValue := 0
			fmt.Sscanf(string(vipFact.Value), "%d", &portValue)
			vipPortByService[pathParts[0]] = portValue
		}
	}
	for serviceName, vipAddress := range vipByService {
		dnsName := serviceName + "." + network.DefaultDNSDomain
		statusResponse.Networking = append(statusResponse.Networking, networkStatusEntry{
			ServiceName: serviceName,
			VIP:         vipAddress,
			Port:        vipPortByService[serviceName],
			DNS:         dnsName,
		})
	}
	sort.Slice(statusResponse.Networking, func(i, j int) bool {
		return statusResponse.Networking[i].ServiceName < statusResponse.Networking[j].ServiceName
	})

	// Build volume status entries from observed volume facts.
	allVolumes, _ := types.ListObservedVolumes(ctx, factStore)
	sort.Slice(allVolumes, func(i, j int) bool { return allVolumes[i].Name < allVolumes[j].Name })
	for _, volume := range allVolumes {
		volumeEntry := volumeStatusEntry{
			Name:      volume.Name,
			Size:      volume.Size,
			State:     string(volume.State),
			Node:      volume.Node,
			Instance:  volume.Instance,
			MountPath: volume.MountPath,
		}
		statusResponse.Volumes = append(statusResponse.Volumes, volumeEntry)
	}

	// Build node status entries with placement counts.
	allNodes, _ := types.ListNodes(ctx, factStore)
	sort.Slice(allNodes, func(i, j int) bool { return allNodes[i].ID < allNodes[j].ID })
	for _, node := range allNodes {
		placedInstanceCount := 0
		for _, instance := range allInstances {
			if instance.State == types.InstanceStopped {
				continue
			}
			if placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID)); err == nil && string(placementFact.Value) == node.ID {
				placedInstanceCount++
			}
		}
		statusResponse.Nodes = append(statusResponse.Nodes, nodeStatusEntry{
			ID: node.ID, State: string(node.State), PlacedInstances: placedInstanceCount,
			AvailableCPU: node.AvailableCPU, CapacityCPU: node.CapacityCPU,
			AvailableMemory: node.AvailableMemory, CapacityMemory: node.CapacityMemory,
		})
	}

	return statusResponse
}

// buildStatusTextOutput renders the cluster status as human-readable formatted text.
// It delegates to buildClusterStatusJSON for data collection, then formats the result.
func buildStatusTextOutput(ctx context.Context, factStore store.StateStore) string {
	var textBuilder strings.Builder
	textBuilder.WriteString("=== CLUSTER STATUS ===\n\n")

	statusData := buildClusterStatusJSON(ctx, factStore)

	// Render services section.
	for _, service := range statusData.Services {
		fmt.Fprintf(&textBuilder, "SERVICE  %-12s  image=%-16s  desired=%d  running=%d",
			service.Name, service.Image, service.DesiredCount, service.RunningCount)
		if len(service.ExposedPorts) > 0 {
			fmt.Fprintf(&textBuilder, "  ports=%v", service.ExposedPorts)
		}
		textBuilder.WriteByte('\n')
	}

	// Render instances section with IP addresses.
	textBuilder.WriteByte('\n')
	fmt.Fprintf(&textBuilder, "INSTANCES (%d active)\n", len(statusData.Instances))
	for _, instance := range statusData.Instances {
		fmt.Fprintf(&textBuilder, "  %-12s  service=%-8s  state=%-8s  node=%-8s  ip=%-16s  health=%-8s\n",
			instance.ID, instance.ServiceName, instance.State, instance.NodeID, instance.IPAddress, instance.HealthState)
	}

	// Render nodes section.
	textBuilder.WriteByte('\n')
	fmt.Fprintf(&textBuilder, "NODES (%d)\n", len(statusData.Nodes))
	for _, node := range statusData.Nodes {
		fmt.Fprintf(&textBuilder, "  %-12s  state=%-12s  instances=%d  cpu=%d/%d  memory=%d/%d\n",
			node.ID, node.State, node.PlacedInstances, node.AvailableCPU, node.CapacityCPU,
			node.AvailableMemory, node.CapacityMemory)
	}

	// Render networking section if VIPs exist.
	if len(statusData.Networking) > 0 {
		textBuilder.WriteByte('\n')
		fmt.Fprintf(&textBuilder, "NETWORKING (%d services)\n", len(statusData.Networking))
		for _, networkEntry := range statusData.Networking {
			fmt.Fprintf(&textBuilder, "  %-12s  vip=%s:%d  dns=%s\n",
				networkEntry.ServiceName, networkEntry.VIP, networkEntry.Port, networkEntry.DNS)
		}
	}

	// Render volumes section if volumes exist.
	if len(statusData.Volumes) > 0 {
		textBuilder.WriteByte('\n')
		fmt.Fprintf(&textBuilder, "VOLUMES (%d)\n", len(statusData.Volumes))
		for _, volumeEntry := range statusData.Volumes {
			nodeDisplay := volumeEntry.Node
			if nodeDisplay == "" {
				nodeDisplay = "-"
			}
			instanceDisplay := volumeEntry.Instance
			if instanceDisplay == "" {
				instanceDisplay = "-"
			}
			mountPathDisplay := volumeEntry.MountPath
			if mountPathDisplay == "" {
				mountPathDisplay = "-"
			}
			fmt.Fprintf(&textBuilder, "  %-12s  size=%-8s  state=%-10s  node=%-8s  instance=%-8s  mount=%s\n",
				volumeEntry.Name, volumeEntry.Size, volumeEntry.State, nodeDisplay, instanceDisplay, mountPathDisplay)
		}
	}

	return textBuilder.String()
}

// executeCompletionCommand outputs shell completion scripts for bash or zsh.
func executeCompletionCommand(shellName string) {
	switch shellName {
	case "bash":
		fmt.Print(bashCompletionScript)
	case "zsh":
		fmt.Print(buildZshCompletionScript())
	default:
		fmt.Fprintf(os.Stderr, "unsupported shell %q (use bash or zsh)\n", shellName)
		os.Exit(1)
	}
}

const bashCompletionScript = `# cca bash completion — source this or add to .bashrc:
#   eval "$(cca completion bash)"

_cca_completions() {
    local current_word="${COMP_WORDS[COMP_CWORD]}"
    local previous_word="${COMP_WORDS[COMP_CWORD-1]}"

    local commands="apply run run-container server agent token join demo demo-distributed demo-network demo-storage chaos top status describe get diff events logs scale watch metric completion version"

    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=($(compgen -W "$commands" -- "$current_word"))
        return
    fi

    local command="${COMP_WORDS[1]}"

    case "$command" in
        get)
            COMPREPLY=($(compgen -W "services instances nodes volumes networking secrets config" -- "$current_word"))
            ;;
        top)
            COMPREPLY=($(compgen -W "nodes workloads" -- "$current_word"))
            ;;
        describe)
            if [ "$COMP_CWORD" -eq 2 ]; then
                COMPREPLY=($(compgen -W "service node instance" -- "$current_word"))
            fi
            ;;
        token)
            if [ "$COMP_CWORD" -eq 2 ]; then
                COMPREPLY=($(compgen -W "create list revoke" -- "$current_word"))
            fi
            case "$previous_word" in
                --store) COMPREPLY=($(compgen -W "memory etcd" -- "$current_word")) ;;
                --ttl|--node-id|--endpoints|--store-prefix) ;;
                create|list|revoke) COMPREPLY=($(compgen -W "--store --endpoints --store-prefix --node-id --ttl" -- "$current_word")) ;;
            esac
            ;;
        metric)
            if [ "$COMP_CWORD" -eq 2 ]; then
                COMPREPLY=($(compgen -W "set" -- "$current_word"))
            fi
            ;;
        completion)
            COMPREPLY=($(compgen -W "bash zsh" -- "$current_word"))
            ;;
        apply|diff)
            case "$previous_word" in
                --store) COMPREPLY=($(compgen -W "memory etcd" -- "$current_word")) ;;
                --endpoints|--store-prefix) ;;
                *) COMPREPLY=($(compgen -f -W "--store --endpoints --store-prefix" -- "$current_word")) ;;
            esac
            ;;
        run|run-container)
            case "$previous_word" in
                --store) COMPREPLY=($(compgen -W "memory etcd" -- "$current_word")) ;;
                --endpoints|--store-prefix) ;;
                *) COMPREPLY=($(compgen -f -W "--watch -w --store --endpoints --store-prefix" -- "$current_word")) ;;
            esac
            ;;
        server)
            case "$previous_word" in
                --store) COMPREPLY=($(compgen -W "memory etcd" -- "$current_word")) ;;
                --listen|--endpoints|--store-prefix) ;;
                --cert|--key|--ca) COMPREPLY=($(compgen -f -- "$current_word")) ;;
                *) COMPREPLY=($(compgen -W "--listen --tls --cert --key --ca --store --endpoints --store-prefix --dns --dns-listen" -- "$current_word")) ;;
            esac
            ;;
        agent)
            case "$previous_word" in
                --runtime) COMPREPLY=($(compgen -W "process container" -- "$current_word")) ;;
                --node-id|--endpoints|--store-prefix|--advertise-address) ;;
                --cert|--key|--ca) COMPREPLY=($(compgen -f -- "$current_word")) ;;
                *) COMPREPLY=($(compgen -W "--node-id --runtime --cert --key --ca --store --endpoints --store-prefix --advertise-address --proxy --proxy-listen" -- "$current_word")) ;;
            esac
            ;;
        join)
            case "$previous_word" in
                --node-id|--data-dir) ;;
                --ca-cert) COMPREPLY=($(compgen -f -- "$current_word")) ;;
                *) COMPREPLY=($(compgen -W "--node-id --ca-cert --data-dir" -- "$current_word")) ;;
            esac
            ;;
        events)
            case "$previous_word" in
                --service|-s) ;;
                *) COMPREPLY=($(compgen -W "--follow -f --service -s" -- "$current_word")) ;;
            esac
            ;;
    esac
}

complete -F _cca_completions cca
`

// buildZshCompletionScript generates the zsh completion script as a string.
// Built with a string builder because the script contains backticks that
// cannot appear inside a Go raw string literal.
func buildZshCompletionScript() string {
	var builder strings.Builder
	builder.WriteString("#compdef cca\n")
	builder.WriteString("# cca zsh completion — source this or add to fpath:\n")
	builder.WriteString("#   eval \"$(cca completion zsh)\"\n\n")
	builder.WriteString("_cca() {\n")
	builder.WriteString("    local -a commands\n")
	builder.WriteString("    commands=(\n")
	for _, entry := range []struct{ name, description string }{
		{"apply", "parse .ccattler file and show reconciliation"},
		{"run", "start real processes"},
		{"run-container", "start real containers"},
		{"server", "run control plane (controllers + API)"},
		{"agent", "run node agent"},
		{"token", "manage join tokens (create/list/revoke)"},
		{"join", "enroll this node with the cluster"},
		{"demo", "built-in demo with simulated runtime"},
		{"demo-distributed", "3 simulated nodes with recovery"},
		{"demo-network", "3 nodes with IP, VIPs, DNS, LB"},
		{"demo-storage", "3 nodes with persistent volumes"},
		{"chaos", "random failure injection"},
		{"top", "resource utilization overview"},
		{"status", "show cluster status"},
		{"describe", "detailed resource view"},
		{"get", "list resources"},
		{"diff", "dry-run apply showing fact changes"},
		{"events", "event stream"},
		{"logs", "cluster event log"},
		{"scale", "scale a service"},
		{"watch", "stream fact store changes"},
		{"metric", "inject simulated metric"},
		{"completion", "output shell completion script"},
		{"version", "print version"},
	} {
		fmt.Fprintf(&builder, "        '%s:%s'\n", entry.name, entry.description)
	}
	builder.WriteString("    )\n\n")
	builder.WriteString("    if (( CURRENT == 2 )); then\n")
	builder.WriteString("        _describe 'command' commands\n")
	builder.WriteString("        return\n")
	builder.WriteString("    fi\n\n")
	builder.WriteString("    case \"${words[2]}\" in\n")
	builder.WriteString("        get)\n")
	builder.WriteString("            local -a resources\n")
	builder.WriteString("            resources=('services' 'instances' 'nodes' 'volumes' 'networking' 'secrets' 'config')\n")
	builder.WriteString("            _describe 'resource' resources\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        top)\n")
	builder.WriteString("            local -a views\n")
	builder.WriteString("            views=('nodes' 'workloads')\n")
	builder.WriteString("            _describe 'view' views\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        describe)\n")
	builder.WriteString("            if (( CURRENT == 3 )); then\n")
	builder.WriteString("                local -a types\n")
	builder.WriteString("                types=('service' 'node' 'instance')\n")
	builder.WriteString("                _describe 'type' types\n")
	builder.WriteString("            fi\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        token)\n")
	builder.WriteString("            if (( CURRENT == 3 )); then\n")
	builder.WriteString("                local -a subcommands\n")
	builder.WriteString("                subcommands=('create:generate a join token' 'list:list active tokens' 'revoke:revoke a token')\n")
	builder.WriteString("                _describe 'subcommand' subcommands\n")
	builder.WriteString("            else\n")
	builder.WriteString("                _arguments \\\n")
	builder.WriteString("                    '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                    '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                    '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                    '--node-id[scope to node]:node:' \\\n")
	builder.WriteString("                    '--ttl[token lifetime]:duration:'\n")
	builder.WriteString("            fi\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        metric)\n")
	builder.WriteString("            if (( CURRENT == 3 )); then\n")
	builder.WriteString("                local -a subcommands\n")
	builder.WriteString("                subcommands=('set:inject simulated metric')\n")
	builder.WriteString("                _describe 'subcommand' subcommands\n")
	builder.WriteString("            fi\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        completion)\n")
	builder.WriteString("            local -a shells\n")
	builder.WriteString("            shells=('bash' 'zsh')\n")
	builder.WriteString("            _describe 'shell' shells\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        apply|diff)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                '*:file:_files -g \"*.ccattler *.ccl\"'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        run|run-container)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '(-w --watch)'{-w,--watch}'[print status periodically]' \\\n")
	builder.WriteString("                '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                '*:file:_files -g \"*.ccattler *.ccl\"'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        server)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '--listen[API listen address]:address:' \\\n")
	builder.WriteString("                '--tls[enable mTLS]' \\\n")
	builder.WriteString("                '--cert[PEM server certificate]:file:_files' \\\n")
	builder.WriteString("                '--key[PEM server private key]:file:_files' \\\n")
	builder.WriteString("                '--ca[PEM CA certificate]:file:_files' \\\n")
	builder.WriteString("                '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                '--dns[enable DNS server]' \\\n")
	builder.WriteString("                '--dns-listen[DNS listen address]:address:'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        agent)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '--node-id[unique node identifier]:id:' \\\n")
	builder.WriteString("                '--runtime[workload runtime]:runtime:(process container)' \\\n")
	builder.WriteString("                '--cert[PEM agent certificate]:file:_files' \\\n")
	builder.WriteString("                '--key[PEM agent private key]:file:_files' \\\n")
	builder.WriteString("                '--ca[PEM CA certificate]:file:_files' \\\n")
	builder.WriteString("                '--store[state store backend]:backend:(memory etcd)' \\\n")
	builder.WriteString("                '--endpoints[etcd endpoints]:endpoints:' \\\n")
	builder.WriteString("                '--store-prefix[etcd key prefix]:prefix:' \\\n")
	builder.WriteString("                '--advertise-address[node LAN address]:address:' \\\n")
	builder.WriteString("                '--proxy[enable HTTP proxy]' \\\n")
	builder.WriteString("                '--proxy-listen[proxy listen address]:address:'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        join)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '--node-id[unique node identifier]:id:' \\\n")
	builder.WriteString("                '--ca-cert[PEM CA certificate]:file:_files' \\\n")
	builder.WriteString("                '--data-dir[cert/key directory]:directory:_directories'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("        events)\n")
	builder.WriteString("            _arguments \\\n")
	builder.WriteString("                '(-f --follow)'{-f,--follow}'[stream events in real-time]' \\\n")
	builder.WriteString("                '(-s --service)'{-s,--service}'[filter by service]:service:'\n")
	builder.WriteString("            ;;\n")
	builder.WriteString("    esac\n")
	builder.WriteString("}\n\n")
	builder.WriteString("_cca \"$@\"\n")
	return builder.String()
}

// annotateErrorWithFileName sets the File field on a ParseError if the error
// is of that type. This adds the source filename to diagnostic output.
func annotateErrorWithFileName(originalError error, fileName string) error {
	if parseError, isParseError := originalError.(*lang.ParseError); isParseError {
		parseError.File = fileName
	}
	return originalError
}
