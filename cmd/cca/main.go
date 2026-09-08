// Package main implements the ccattler CLI — the entry point for the CCattler
// container orchestrator. It provides commands for applying configurations,
// running workloads (as processes or containers), querying cluster status,
// and injecting simulated metrics.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/infra"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
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
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: cca apply <file>")
			os.Exit(1)
		}
		executeApplyCommand(os.Args[2])
	case "run":
		configFilePath, watchModeEnabled := parseRunCommandArgs(os.Args[2:])
		if configFilePath == "" {
			fmt.Fprintln(os.Stderr, "usage: cca run [--watch] <file>")
			os.Exit(1)
		}
		executeLiveProcessCommand(configFilePath, watchModeEnabled)
	case "run-container":
		configFilePath, watchModeEnabled := parseRunCommandArgs(os.Args[2:])
		if configFilePath == "" {
			fmt.Fprintln(os.Stderr, "usage: cca run-container [--watch] <file>")
			os.Exit(1)
		}
		executeLiveContainerCommand(configFilePath, watchModeEnabled)
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
	case "status":
		executeStatusCommand()
	case "logs":
		logsTarget := ""
		if len(os.Args) >= 3 {
			logsTarget = os.Args[2]
		}
		executeLogsCommand(logsTarget)
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
		prefix := types.Root + "/"
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
	default:
		printUsage()
		os.Exit(1)
	}
}

// parseRunCommandArgs extracts the config file path and --watch flag from the
// arguments following "run" or "run-container". Returns empty path if no file is found.
func parseRunCommandArgs(args []string) (string, bool) {
	watchModeEnabled := false
	configFilePath := ""
	for _, arg := range args {
		if arg == "--watch" || arg == "-w" {
			watchModeEnabled = true
		} else if configFilePath == "" {
			configFilePath = arg
		}
	}
	return configFilePath, watchModeEnabled
}

// printUsage prints the CLI help text listing all available commands to stderr.
func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: cca <command>")
	fmt.Fprintln(os.Stderr, "  apply <file>   parse .ccattler file, show reconciliation (simulated)")
	fmt.Fprintln(os.Stderr, "  run [--watch] <file>     start real processes (--watch for live status)")
	fmt.Fprintln(os.Stderr, "  demo           built-in demo with simulated runtime (1 node)")
	fmt.Fprintln(os.Stderr, "  demo-distributed  3 simulated nodes, kills one to show recovery")
	fmt.Fprintln(os.Stderr, "  demo-network      3 nodes with IP allocation, VIPs, DNS, load balancing")
	fmt.Fprintln(os.Stderr, "  demo-storage      3 nodes with persistent volumes, kills node to show migration")
	fmt.Fprintln(os.Stderr, "  chaos             random failure injection, live convergence reporting")
	fmt.Fprintln(os.Stderr, "  status         show cluster status (queries running instance)")
	fmt.Fprintln(os.Stderr, "  get <resource> show services, instances, nodes, volumes, networking, secrets, or config")
	fmt.Fprintln(os.Stderr, "  logs [service] show cluster event log (optionally filtered by service)")
	fmt.Fprintln(os.Stderr, "  scale <svc> <n> scale a service to n instances")
	fmt.Fprintln(os.Stderr, "  watch [prefix] stream fact store changes as they happen")
	fmt.Fprintln(os.Stderr, "  run-container [--watch] <file>  start real containers (--watch for live status)")
	fmt.Fprintln(os.Stderr, "  metric set <service> <metric> <value>")
}

// executeApplyCommand parses a .ccattler file with simulated nodes and no real processes.
// It registers 3 simulated nodes, runs all controllers, applies the config, and prints status.
func executeApplyCommand(configFilePath string) {
	fileData, err := os.ReadFile(configFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", configFilePath, err)
		os.Exit(1)
	}

	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

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
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController)
	go controllerRunner.Run(ctx)

	fmt.Printf("Applying %s...\n", configFilePath)
	if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
		fmt.Fprintf(os.Stderr, "apply error: %v\n", err)
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
func executeLiveProcessCommand(configFilePath string, watchModeEnabled bool) {
	fileData, err := os.ReadFile(configFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", configFilePath, err)
		os.Exit(1)
	}

	factStore := store.NewMemoryStore()
	defer factStore.Close()

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

	eventLog := controllers.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, autoscaleController, intentResolverController, rolloutController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Start node agent with process runtime for real OS process execution.
	processRuntime := runtime.NewProcessRuntime()
	nodeAgent := agent.New(localNodeID, factStore, processRuntime)
	go nodeAgent.Run(ctx)

	statusAPIServer := launchStatusAPIServer(factStore)
	statusAPIServer.SetEventLog(eventLog)

	fmt.Printf("Applying %s...\n", configFilePath)
	if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
		fmt.Fprintf(os.Stderr, "apply error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Running. Status API on %s. Press Ctrl+C to stop.\n\n", statusAPIListenAddress)

	// Initial status after reconciliation settles.
	time.Sleep(1 * time.Second)
	fmt.Printf("[%s]\n", time.Now().Format("15:04:05"))
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	if watchModeEnabled {
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
// via the ContainerRuntime (docker CLI) and node agent. When watchModeEnabled is
// true, prints status every 2 seconds with timestamps; otherwise prints status once
// and blocks until Ctrl+C.
func executeLiveContainerCommand(configFilePath string, watchModeEnabled bool) {
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "error: docker is not installed or not in PATH")
		fmt.Fprintln(os.Stderr, "install Docker Desktop (macOS/Windows) or docker-ce (Linux)")
		fmt.Fprintln(os.Stderr, "alternatively, use 'cca run <file>' to run as OS processes instead")
		os.Exit(1)
	}

	fileData, err := os.ReadFile(configFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", configFilePath, err)
		os.Exit(1)
	}

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

	eventLog := controllers.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, networkController,
		autoscaleController, intentResolverController, rolloutController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Start node agent with container runtime for real Docker container execution.
	containerRuntime := runtime.NewContainerRuntime()
	containerRuntime.SetDockerNetwork("cca-net", network.DefaultClusterCIDR)

	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()
	nodeAgent := agent.New(localNodeID, factStore, containerRuntime)
	nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
	go nodeAgent.Run(ctx)

	statusAPIServer := launchStatusAPIServer(factStore)
	statusAPIServer.SetEventLog(eventLog)

	fmt.Printf("Applying %s (container mode)...\n", configFilePath)
	if err := lang.Apply(ctx, factStore, string(fileData)); err != nil {
		fmt.Fprintf(os.Stderr, "apply error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Running. Status API on %s. Press Ctrl+C to stop.\n\n", statusAPIListenAddress)

	// Initial status after reconciliation settles.
	time.Sleep(1 * time.Second)
	fmt.Printf("[%s]\n", time.Now().Format("15:04:05"))
	fmt.Print(buildStatusTextOutput(ctx, factStore))

	if watchModeEnabled {
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
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	eventLog := controllers.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController)
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

	statusAPIServer := launchStatusAPIServer(factStore)
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
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	eventLog := controllers.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController,
		autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController)
	controllerRunner.SetEventLog(eventLog)
	go controllerRunner.Run(ctx)

	// Start 3 agents, each with its own simulator runtime.
	// node-1 gets a separate cancel context so we can kill it later.
	node1Context, killNode1 := context.WithCancel(ctx)
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

	statusAPIServer := launchStatusAPIServer(factStore)
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
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	eventLog := controllers.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, networkController,
		autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController)
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

	statusAPIServer := launchStatusAPIServer(factStore)
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
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore))

	eventLog := controllers.NewEventLog(factStore, 1000)

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, storageController,
		autoscaleController, intentResolverController, rolloutController, clusterAutoscaleController)
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

	statusAPIServer := launchStatusAPIServer(factStore)
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

// executeLogsCommand queries the cluster event log from a running ccattler
// instance. When a target is provided, only events affecting that resource are
// shown. Prints events as a formatted table with timestamp, kind, target, and detail.
func executeLogsCommand(target string) {
	apiURL := "http://" + statusAPIListenAddress + "/api/logs"
	if target != "" {
		apiURL += "?target=" + target
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

// executeStatusCommand queries the status API of a running ccattler instance
// and prints the cluster status to stdout. Requires a running 'run' or 'demo' instance.
func executeStatusCommand() {
	httpResponse, err := http.Get("http://" + statusAPIListenAddress + "/status")
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

	var output interface{}
	switch resourceType {
	case "services", "svc":
		output = status.Services
	case "instances", "inst":
		output = status.Instances
	case "nodes":
		output = status.Nodes
	case "volumes", "vol":
		output = status.Volumes
	case "networking", "net":
		output = status.Networking
	case "secrets", "secret":
		output = status.Secrets
	case "config", "cfg":
		output = status.Config
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s (use services, instances, nodes, volumes, networking, secrets, config)\n", resourceType)
		os.Exit(1)
	}

	formattedJSON, _ := json.MarshalIndent(output, "", "  ")
	fmt.Println(string(formattedJSON))
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

// launchStatusAPIServer starts the HTTP API server in the background. It hosts
// both the legacy /status and /metric endpoints and the new /api/* endpoints.
// Returns the api.Server so callers can attach optional components like EventLog.
func launchStatusAPIServer(factStore store.StateStore) *api.Server {
	apiServer := api.NewServer(factStore)

	httpMux := http.NewServeMux()
	httpMux.Handle("/api/", apiServer.Handler())

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

	listener, err := net.Listen("tcp", statusAPIListenAddress)
	if err != nil {
		return apiServer
	}
	go http.Serve(listener, httpMux)
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
