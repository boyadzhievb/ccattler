// command_benchmark.go implements `cca benchmark` — a chaos benchmark that
// deploys workloads, injects failures, and reports structured recovery metrics.
package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/boyadzhievb/ccattler/chaos"
	"github.com/boyadzhievb/ccattler/store"
)

const (
	// benchmarkDefaultNodeCount is the default number of simulated nodes.
	benchmarkDefaultNodeCount = 10
	// benchmarkDefaultServiceCount is the default number of services to deploy.
	benchmarkDefaultServiceCount = 5
	// benchmarkDefaultInstancesPerService is the default instance count per service.
	benchmarkDefaultInstancesPerService = 10
	// benchmarkDeployConvergenceDeadline is the maximum time for initial convergence.
	benchmarkDeployConvergenceDeadline = 120 * time.Second
	// benchmarkConvergencePollInterval is the polling interval during convergence.
	benchmarkConvergencePollInterval = 100 * time.Millisecond
	// benchmarkInjectionDuration is how long chaos events are injected.
	benchmarkInjectionDuration = 60 * time.Second
	// benchmarkInjectionInterval is the minimum time between injections.
	benchmarkInjectionInterval = 3 * time.Second
	// benchmarkPerInjectionTimeout is the convergence timeout after each injection.
	benchmarkPerInjectionTimeout = 30 * time.Second

	// benchmarkWatchChannelBufferSize is the channel buffer capacity for fact store watches
	// during benchmark runs. Larger buffers prevent blocking under high event throughput.
	benchmarkWatchChannelBufferSize = 4096
	// benchmarkRecoverySuccessRateThreshold is the minimum recovery success rate percentage
	// required for the benchmark to report PASS.
	benchmarkRecoverySuccessRateThreshold = 80

	// benchmarkSmallClusterNodeThreshold is the maximum node count classified as a small cluster.
	benchmarkSmallClusterNodeThreshold = 20
	// benchmarkSmallClusterAgentInterval is the agent reconciliation interval for small clusters.
	benchmarkSmallClusterAgentInterval = 100 * time.Millisecond
	// benchmarkSmallClusterControllerDebounce is the controller debounce duration for small clusters.
	benchmarkSmallClusterControllerDebounce = 50 * time.Millisecond
	// benchmarkSmallClusterMaxReconciliationAttempts is the maximum reconciliation retries for small clusters.
	benchmarkSmallClusterMaxReconciliationAttempts = 5
	// benchmarkSmallClusterLeaseTimeout is the node lease TTL for small clusters.
	benchmarkSmallClusterLeaseTimeout = 5 * time.Second

	// benchmarkMediumClusterNodeThreshold is the maximum node count classified as a medium cluster.
	benchmarkMediumClusterNodeThreshold = 100
	// benchmarkMediumClusterAgentInterval is the agent reconciliation interval for medium clusters.
	benchmarkMediumClusterAgentInterval = 200 * time.Millisecond
	// benchmarkMediumClusterControllerDebounce is the controller debounce duration for medium clusters.
	benchmarkMediumClusterControllerDebounce = 100 * time.Millisecond
	// benchmarkMediumClusterMaxReconciliationAttempts is the maximum reconciliation retries for medium clusters.
	benchmarkMediumClusterMaxReconciliationAttempts = 10
	// benchmarkMediumClusterLeaseTimeout is the node lease TTL for medium clusters.
	benchmarkMediumClusterLeaseTimeout = 30 * time.Second

	// benchmarkLargeClusterAgentInterval is the agent reconciliation interval for large clusters (above medium threshold).
	benchmarkLargeClusterAgentInterval = 500 * time.Millisecond
	// benchmarkLargeClusterControllerDebounce is the controller debounce duration for large clusters.
	benchmarkLargeClusterControllerDebounce = 200 * time.Millisecond
	// benchmarkLargeClusterMaxReconciliationAttempts is the maximum reconciliation retries for large clusters.
	benchmarkLargeClusterMaxReconciliationAttempts = 15
	// benchmarkLargeClusterLeaseTimeout is the node lease TTL for large clusters.
	benchmarkLargeClusterLeaseTimeout = 60 * time.Second
)

// benchmarkConfig holds parsed command-line options for the benchmark command.
type benchmarkConfig struct {
	// jsonOutput enables JSON output of the RecoveryReport.
	jsonOutput bool
	// nodeCount is the number of simulated nodes.
	nodeCount int
	// serviceCount is the number of services to deploy.
	serviceCount int
	// instancesPerService is the instance count per service.
	instancesPerService int
}

// parseBenchmarkCommandArgs parses `cca benchmark` flags.
func parseBenchmarkCommandArgs(args []string) benchmarkConfig {
	config := benchmarkConfig{
		nodeCount:           benchmarkDefaultNodeCount,
		serviceCount:        benchmarkDefaultServiceCount,
		instancesPerService: benchmarkDefaultInstancesPerService,
	}

	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--json":
			config.jsonOutput = true
		case "--nodes":
			if index+1 < len(args) {
				index++
				parsedValue, parseError := strconv.Atoi(args[index])
				if parseError != nil {
					fmt.Fprintf(os.Stderr, "invalid --nodes value %q: must be an integer\n", args[index])
					os.Exit(1)
				}
				config.nodeCount = parsedValue
			}
		case "--services":
			if index+1 < len(args) {
				index++
				parsedValue, parseError := strconv.Atoi(args[index])
				if parseError != nil {
					fmt.Fprintf(os.Stderr, "invalid --services value %q: must be an integer\n", args[index])
					os.Exit(1)
				}
				config.serviceCount = parsedValue
			}
		case "--instances":
			if index+1 < len(args) {
				index++
				parsedValue, parseError := strconv.Atoi(args[index])
				if parseError != nil {
					fmt.Fprintf(os.Stderr, "invalid --instances value %q: must be an integer\n", args[index])
					os.Exit(1)
				}
				config.instancesPerService = parsedValue
			}
		}
	}

	return config
}

// deployBenchmarkWorkloads creates the configured number of services on the
// cluster and waits for all instances to reach a converged state. Returns the
// wall-clock duration of the convergence wait and whether it succeeded within
// the deadline.
func deployBenchmarkWorkloads(ctx context.Context, benchmarkCluster *chaos.SimulatedChaosCluster, config benchmarkConfig) (time.Duration, bool) {
	for serviceIndex := 0; serviceIndex < config.serviceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		benchmarkCluster.DeployService(ctx, serviceName, fmt.Sprintf("app:v%d", serviceIndex), config.instancesPerService)
	}

	deployStart := time.Now()
	deployConverged := waitForBenchmarkConvergence(ctx, benchmarkCluster, benchmarkDeployConvergenceDeadline)
	deployDuration := time.Since(deployStart)

	return deployDuration, deployConverged
}

// runBenchmarkChaosInjection configures the chaos runner with standard failure
// scenarios (node kill, partition, controller restart, scale change, recovery)
// and executes injection for the benchmark duration. When not in JSON output
// mode, prints per-event progress as each injection resolves or times out.
func runBenchmarkChaosInjection(ctx context.Context, benchmarkCluster *chaos.SimulatedChaosCluster, jsonOutput bool) []chaos.ChaosEvent {
	chaosConfig := chaos.ChaosConfig{
		Duration:           benchmarkInjectionDuration,
		InjectionInterval:  benchmarkInjectionInterval,
		ConvergenceTimeout: benchmarkPerInjectionTimeout,
		EnabledScenarios: []chaos.FailureScenario{
			chaos.ScenarioNodeKill,
			chaos.ScenarioNodePartition,
			chaos.ScenarioControllerRestart,
			chaos.ScenarioScaleChange,
			chaos.ScenarioNodeRecovery,
		},
		RandSource: rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // math/rand for chaos jitter
	}

	chaosRunner := chaos.NewChaosRunner(chaosConfig, benchmarkCluster)
	if !jsonOutput {
		chaosRunner.SetEventCallback(func(event chaos.ChaosEvent) {
			convergenceLabel := "TIMEOUT"
			if event.Converged {
				convergenceLabel = fmt.Sprintf("CONVERGED in %v", event.ConvergenceTime.Round(time.Millisecond))
			}
			fmt.Printf("[%v] %-20s target=%-14s %s\n",
				event.Elapsed.Round(time.Second), event.Scenario, event.Target, convergenceLabel)
		})
	}

	return chaosRunner.Run(ctx)
}

// buildAndOutputBenchmarkReport computes recovery metrics from chaos events,
// assembles the full report, and outputs it as JSON (when jsonOutput is set
// in the config) or as a human-readable summary to stdout.
func buildAndOutputBenchmarkReport(ctx context.Context, benchmarkCluster *chaos.SimulatedChaosCluster, config benchmarkConfig, totalInstances int, deployDuration time.Duration, chaosEvents []chaos.ChaosEvent) {
	finalConverged, finalStatus := benchmarkCluster.CheckConvergence(ctx)
	recoveryMetrics := chaos.ComputeRecoveryMetrics(chaosEvents)

	report := chaos.RecoveryReport{
		NodeCount:             config.nodeCount,
		ServiceCount:          config.serviceCount,
		TotalInstances:        totalInstances,
		DeployConvergenceTime: deployDuration,
		ChaosEvents:           chaosEvents,
		Metrics:               recoveryMetrics,
		FinalConverged:        finalConverged,
		FinalStatus:           finalStatus,
	}

	if config.jsonOutput {
		jsonBytes, marshalError := report.ToJSON()
		if marshalError != nil {
			fmt.Fprintf(os.Stderr, "json marshal error: %v\n", marshalError)
			os.Exit(1)
		}
		fmt.Println(string(jsonBytes))
		return
	}

	printBenchmarkSummary(report)
}

// executeBenchmarkCommand runs a chaos benchmark on a simulated cluster and
// prints recovery metrics. With --json, outputs a full RecoveryReport as JSON.
func executeBenchmarkCommand(config benchmarkConfig) {
	totalInstances := config.serviceCount * config.instancesPerService

	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	factStore.SetWatchChannelBufferSize(benchmarkWatchChannelBufferSize)

	nodeIDs := make([]string, config.nodeCount)
	for nodeIndex := range nodeIDs {
		nodeIDs[nodeIndex] = fmt.Sprintf("node-%03d", nodeIndex)
	}

	benchmarkCluster := chaos.NewSimulatedChaosCluster(factStore, nodeIDs)
	configureBenchmarkCluster(benchmarkCluster, config.nodeCount)
	benchmarkCluster.Start(ctx)

	if !config.jsonOutput {
		fmt.Printf("=== CCattler Chaos Benchmark ===\n\n")
		fmt.Printf("Cluster: %d nodes, %d services × %d instances = %d total\n\n",
			config.nodeCount, config.serviceCount, config.instancesPerService, totalInstances)
		fmt.Println("Deploying workloads...")
	}

	deployDuration, deployConverged := deployBenchmarkWorkloads(ctx, benchmarkCluster, config)

	if !deployConverged {
		fmt.Fprintf(os.Stderr, "deployment did not converge within %v\n", benchmarkDeployConvergenceDeadline)
		os.Exit(1)
	}

	if !config.jsonOutput {
		fmt.Printf("Deployment converged in %v\n\n", deployDuration.Round(time.Millisecond))
		fmt.Println("Starting chaos injection for 60 seconds...")
	}

	chaosEvents := runBenchmarkChaosInjection(ctx, benchmarkCluster, config.jsonOutput)

	buildAndOutputBenchmarkReport(ctx, benchmarkCluster, config, totalInstances, deployDuration, chaosEvents)
}

// printBenchmarkSummary prints a human-readable summary of the benchmark results.
func printBenchmarkSummary(report chaos.RecoveryReport) {
	metrics := report.Metrics
	fmt.Printf("\n=== Benchmark Results ===\n")
	fmt.Printf("Injections:       %d\n", metrics.TotalInjections)
	fmt.Printf("Recovered:        %d/%d (%.1f%%)\n",
		metrics.SuccessfulRecoveries, metrics.TotalInjections, metrics.RecoverySuccessRate)
	fmt.Printf("Mean convergence: %v\n", metrics.MeanConvergenceTime.Round(time.Millisecond))
	fmt.Printf("P50 convergence:  %v\n", metrics.P50ConvergenceTime.Round(time.Millisecond))
	fmt.Printf("P95 convergence:  %v\n", metrics.P95ConvergenceTime.Round(time.Millisecond))
	fmt.Printf("P99 convergence:  %v\n", metrics.P99ConvergenceTime.Round(time.Millisecond))
	fmt.Printf("Max convergence:  %v\n", metrics.MaxConvergenceTime.Round(time.Millisecond))
	fmt.Printf("Final converged:  %v (%s)\n", report.FinalConverged, report.FinalStatus)

	if metrics.RecoverySuccessRate >= benchmarkRecoverySuccessRateThreshold {
		fmt.Println("\nResult: PASS")
	} else {
		fmt.Printf("\nResult: FAIL — recovery rate below %d%%\n", benchmarkRecoverySuccessRateThreshold)
	}
}

// configureBenchmarkCluster sets agent and controller tuning parameters
// appropriate for the given node count.
func configureBenchmarkCluster(cluster *chaos.SimulatedChaosCluster, nodeCount int) {
	switch {
	case nodeCount <= benchmarkSmallClusterNodeThreshold:
		cluster.SetAgentInterval(benchmarkSmallClusterAgentInterval)
		cluster.SetControllerDebounce(benchmarkSmallClusterControllerDebounce)
		cluster.SetMaxReconciliationAttempts(benchmarkSmallClusterMaxReconciliationAttempts)
		cluster.SetLeaseTimeout(benchmarkSmallClusterLeaseTimeout)
	case nodeCount <= benchmarkMediumClusterNodeThreshold:
		cluster.SetAgentInterval(benchmarkMediumClusterAgentInterval)
		cluster.SetControllerDebounce(benchmarkMediumClusterControllerDebounce)
		cluster.SetMaxReconciliationAttempts(benchmarkMediumClusterMaxReconciliationAttempts)
		cluster.SetLeaseTimeout(benchmarkMediumClusterLeaseTimeout)
	default:
		cluster.SetAgentInterval(benchmarkLargeClusterAgentInterval)
		cluster.SetControllerDebounce(benchmarkLargeClusterControllerDebounce)
		cluster.SetMaxReconciliationAttempts(benchmarkLargeClusterMaxReconciliationAttempts)
		cluster.SetLeaseTimeout(benchmarkLargeClusterLeaseTimeout)
	}
	cluster.SetMaxInputKeyGuards(0)
}

// waitForBenchmarkConvergence polls until the cluster converges or the timeout expires.
func waitForBenchmarkConvergence(ctx context.Context, cluster *chaos.SimulatedChaosCluster, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		converged, _ := cluster.CheckConvergence(ctx)
		if converged {
			return true
		}
		time.Sleep(benchmarkConvergencePollInterval)
	}
	return false
}
