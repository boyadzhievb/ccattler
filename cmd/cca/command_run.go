// command_run.go contains the run, run-container, and demo command configurations,
// argument parsing, store creation, and execution logic extracted from main.go.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/infra"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/storage"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// runInitialSettleTime is the delay after starting controllers before
	// printing the first status snapshot. Shorter than the watch-mode settle
	// because the user is watching and wants quick feedback.
	runInitialSettleTime = 1 * time.Second

	// networkDemoStatusInterval is the status print interval for the network
	// demo command, which runs longer and benefits from less frequent updates.
	networkDemoStatusInterval = 5 * time.Second
)

// runCommandConfig holds all parsed flags and arguments for the "run" and
// "run-container" commands, including the store backend selection, etcd
// endpoint list, and key prefix for multi-cluster isolation.
type runCommandConfig struct {
	// configFilePath is the path to the .cca DSL file to apply.
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

// createStateStoreFromRunConfig builds the appropriate StateStore for the run
// command configuration. Delegates to createStateStoreFromServerConfig with
// empty TLS paths since run commands don't support etcd TLS.
func createStateStoreFromRunConfig(parsedConfig runCommandConfig) (store.StateStore, error) {
	return createStateStoreFromServerConfig(
		parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix,
		"", "", "")
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

	factStore, storeCreationError := createStateStoreFromRunConfig(parsedRunConfig)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedRunConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer func() { _ = factStore.Close() }()

	if parsedRunConfig.storeBackend == "etcd" {
		fmt.Printf("Connected to etcd at %s (prefix: %s)\n", parsedRunConfig.etcdEndpoints, parsedRunConfig.storeKeyPrefix)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// This machine is the single node in single-machine mode.
	localNodeID := "local"
	registerLocalNode(ctx, factStore, localNodeID)

	// No cluster autoscaler in single-machine mode — no infra provider to add nodes.
	controllerList := append(coreControllers(), controllers.NewWarmZeroController())
	eventLog := startControllerRunner(ctx, factStore, controllerList)

	// Start node agent with process runtime for real OS process execution.
	processRuntime := runtime.NewProcessRuntime()
	nodeAgent := agent.New(localNodeID, factStore, processRuntime)
	go func() {
		if runError := nodeAgent.Run(ctx); runError != nil {
			logging.Default().Error("node agent exited with error", "node", localNodeID, "error", runError.Error())
		}
	}()

	statusAPIServer := launchStatusAPIServer(factStore, statusAPIListenAddress, nil, false)
	statusAPIServer.SetEventLog(eventLog)

	fmt.Printf("Applying %s...\n", parsedRunConfig.configFilePath)
	if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(err, parsedRunConfig.configFilePath))
		os.Exit(1)
	}

	fmt.Printf("Running. Status API on %s. Press Ctrl+C to stop.\n\n", statusAPIListenAddress)
	runSingleNodeStatusLoop(ctx, factStore, parsedRunConfig.watchModeEnabled, "Shutting down...",
		func() { processRuntime.StopAll(context.Background()) })
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

	factStore, storeCreationError := createStateStoreFromRunConfig(parsedRunConfig)
	if storeCreationError != nil {
		fmt.Fprintf(os.Stderr, "error creating %s store: %v\n", parsedRunConfig.storeBackend, storeCreationError)
		os.Exit(1)
	}
	defer func() { _ = factStore.Close() }()

	if parsedRunConfig.storeBackend == "etcd" {
		fmt.Printf("Connected to etcd at %s (prefix: %s)\n", parsedRunConfig.etcdEndpoints, parsedRunConfig.storeKeyPrefix)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	localNodeID := "local"
	registerLocalNode(ctx, factStore, localNodeID)

	controllerList := append(coreControllers(),
		controllers.NewNetworkController(),
		controllers.NewNetworkPolicyController())
	eventLog := startControllerRunner(ctx, factStore, controllerList)

	// Start node agent with container runtime for real nerdctl container execution.
	containerRuntime := runtime.NewContainerRuntime()
	containerRuntime.SetNetwork("cca-net", network.DefaultClusterCIDR)

	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()
	nodeAgent := agent.New(localNodeID, factStore, containerRuntime)
	nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
	go func() {
		if runError := nodeAgent.Run(ctx); runError != nil {
			logging.Default().Error("node agent exited with error", "node", localNodeID, "error", runError.Error())
		}
	}()

	statusAPIServer := launchStatusAPIServer(factStore, statusAPIListenAddress, nil, false)
	statusAPIServer.SetEventLog(eventLog)

	fmt.Printf("Applying %s (container mode)...\n", parsedRunConfig.configFilePath)
	if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(err, parsedRunConfig.configFilePath))
		os.Exit(1)
	}

	fmt.Printf("Running. Status API on %s. Press Ctrl+C to stop.\n\n", statusAPIListenAddress)
	runSingleNodeStatusLoop(ctx, factStore, parsedRunConfig.watchModeEnabled, "Shutting down containers...",
		func() { containerRuntime.StopAll(context.Background()) })
}

// runSingleNodeStatusLoop prints the initial status after reconciliation settles,
// then either enters a periodic watch loop or blocks until interrupted. The
// shutdownMessage is printed on exit, and cleanupFunc is called to stop the runtime.
func runSingleNodeStatusLoop(ctx context.Context, factStore store.StateStore, watchModeEnabled bool, shutdownMessage string, cleanupFunc func()) {
	time.Sleep(runInitialSettleTime)
	fmt.Printf("[%s]\n", time.Now().Format("15:04:05"))
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	if watchModeEnabled {
		statusPrintTicker := time.NewTicker(types.DefaultStatusPrintInterval)
		defer statusPrintTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				fmt.Printf("\n%s\n", shutdownMessage)
				cleanupFunc()
				return
			case <-statusPrintTicker.C:
				fmt.Printf("\n[%s]\n", time.Now().Format("15:04:05"))
				fmt.Print(buildStatusTextOutput(ctx, factStore))
			}
		}
	}

	// Default: block until Ctrl+C without repeating status.
	<-ctx.Done()
	fmt.Printf("\n%s\n", shutdownMessage)
	cleanupFunc()
}

// executeDemoCommand runs a built-in demo with a hardcoded service config
// and the SimulatorRuntime. Useful for testing the reconciliation pipeline
// without real processes or containers.
func executeDemoCommand() {
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	localNodeID := "local"
	registerLocalNode(ctx, factStore, localNodeID)

	controllerList := append(coreControllers(),
		controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore)))
	eventLog := startControllerRunner(ctx, factStore, controllerList)

	// Node agent with simulator runtime — no real processes, just state tracking.
	simulatorRuntime := runtime.NewSimulatorRuntime()
	nodeAgent := agent.New(localNodeID, factStore, simulatorRuntime)
	go func() {
		if runError := nodeAgent.Run(ctx); runError != nil {
			logging.Default().Error("node agent exited with error", "node", localNodeID, "error", runError.Error())
		}
	}()

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
	time.Sleep(runInitialSettleTime)
	fmt.Println()
	fmt.Print(buildStatusTextOutput(ctx, factStore))
}

// executeDistributedDemoCommand runs a multi-node demo with 3 simulated nodes.
// It deploys 6 instances spread across the nodes, then after 5 seconds kills
// node-1 to demonstrate failure detection, instance rescheduling, and recovery.
func executeDistributedDemoCommand() {
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	nodeIDs := []string{"node-1", "node-2", "node-3"}
	registerSimulatedNodes(ctx, factStore, nodeIDs)
	fmt.Println("Registered 3 simulated nodes: node-1, node-2, node-3")

	controllerList := append(coreControllers(),
		controllers.NewNodeFailureController(),
		controllers.NewDrainController(),
		controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore)))
	eventLog := startControllerRunner(ctx, factStore, controllerList)

	// Start 3 agents, each with its own simulator runtime.
	// node-1 gets a separate cancel context so we can kill it later.
	node1Context, killNode1 := context.WithCancel(ctx)
	defer killNode1()
	for _, nodeID := range nodeIDs {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		if nodeID == "node-1" {
			go func() {
				if runError := nodeAgent.Run(node1Context); runError != nil {
					logging.Default().Error("node agent exited with error", "node", nodeID, "error", runError.Error())
				}
			}()
		} else {
			go func() {
				if runError := nodeAgent.Run(ctx); runError != nil {
					logging.Default().Error("node agent exited with error", "node", nodeID, "error", runError.Error())
				}
			}()
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
	time.Sleep(types.DefaultPostStartupSettleTime)
	fmt.Println()
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	fmt.Println("\n--- Killing node-1 in 5 seconds to demonstrate failure recovery ---")
	select {
	case <-ctx.Done():
		return
	case <-time.After(types.DefaultEtcdDialTimeout):
	}

	// Kill node-1's agent — it stops writing heartbeats.
	killNode1()
	fmt.Println("node-1 agent killed. Waiting for failure detection and rescheduling...")
	fmt.Println()

	runDemoStatusLoop(ctx, factStore, types.DefaultStatusPrintInterval)
}

// executeNetworkDemoCommand runs a multi-node demo with networking enabled:
// IP allocation from per-node subnets, VIP assignment, DNS resolution, and
// load-balanced traffic routing. Deploys two services and shows the complete
// networking state including per-instance IPs, service VIPs, and DNS records.
func executeNetworkDemoCommand() {
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	nodeIDs := []string{"node-1", "node-2", "node-3"}
	registerSimulatedNodes(ctx, factStore, nodeIDs)
	for _, nodeID := range nodeIDs {
		if _, putError := factStore.Put(ctx, types.KeyNetworkNodeSubnet(nodeID), []byte(simulatorNetworkProvider.NodeSubnet(nodeID))); putError != nil {
			logging.Default().Error("failed to write node subnet", "node", nodeID, "error", putError.Error())
		}
	}
	fmt.Println("Registered 3 simulated nodes with networking:")
	for _, nodeID := range nodeIDs {
		fmt.Printf("  %s  subnet=%s\n", nodeID, simulatorNetworkProvider.NodeSubnet(nodeID))
	}

	controllerList := append(coreControllers(),
		controllers.NewNodeFailureController(),
		controllers.NewDrainController(),
		controllers.NewNetworkController(),
		controllers.NewNetworkPolicyController(),
		controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore)))
	eventLog := startControllerRunner(ctx, factStore, controllerList)

	// Start 3 agents, each with its own simulator runtime and the shared network provider.
	for _, nodeID := range nodeIDs {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		go func() {
			if runError := nodeAgent.Run(ctx); runError != nil {
				logging.Default().Error("node agent exited with error", "node", nodeID, "error", runError.Error())
			}
		}()
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
	time.Sleep(types.DefaultPostStartupSettleTime)
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
	runDemoStatusLoop(ctx, factStore, networkDemoStatusInterval)
}

// executeStorageDemoCommand runs a multi-node demo with persistent volumes.
// It deploys a postgres service with a volume and a web service without one,
// then kills the node running postgres to demonstrate that the persistent
// volume migrates to the replacement node.
func executeStorageDemoCommand() {
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	simulatorStorageProvider := storage.NewSimulatorStorageProvider()
	if createVolumeError := simulatorStorageProvider.CreateVolume(ctx, "pgdata", 50*1024*1024*1024); createVolumeError != nil {
		logging.Default().Error("failed to create volume", "volume", "pgdata", "error", createVolumeError.Error())
	}

	nodeIDs := []string{"node-1", "node-2", "node-3"}
	registerSimulatedNodes(ctx, factStore, nodeIDs)
	fmt.Println("Registered 3 simulated nodes: node-1, node-2, node-3")

	controllerList := append(coreControllers(),
		controllers.NewNodeFailureController(),
		controllers.NewDrainController(),
		controllers.NewStorageController(),
		controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore)))
	eventLog := startControllerRunner(ctx, factStore, controllerList)

	// Track which context each node's agent uses so we can kill one later.
	nodeAgentContexts := make(map[string]context.CancelFunc)
	for _, nodeID := range nodeIDs {
		nodeContext, nodeCancel := context.WithCancel(ctx)
		nodeAgentContexts[nodeID] = nodeCancel

		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetStorageProvider(simulatorStorageProvider)
		go func() {
			if runError := nodeAgent.Run(nodeContext); runError != nil {
				logging.Default().Error("node agent exited with error", "node", nodeID, "error", runError.Error())
			}
		}()
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

	time.Sleep(types.DefaultPostStartupSettleTime)
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
	case <-time.After(types.DefaultEtcdDialTimeout):
	}

	if killFunc, exists := nodeAgentContexts[postgresNodeID]; exists {
		killFunc()
	}
	if forceDetachError := simulatorStorageProvider.ForceDetach(ctx, "pgdata"); forceDetachError != nil {
		logging.Default().Error("failed to force-detach volume", "volume", "pgdata", "error", forceDetachError.Error())
	}
	fmt.Printf("%s agent killed. Waiting for failure detection, volume force-detach, and rescheduling...\n\n", postgresNodeID)

	runDemoStatusLoop(ctx, factStore, types.DefaultStatusPrintInterval)
}
