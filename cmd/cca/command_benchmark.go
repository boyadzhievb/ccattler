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

// executeBenchmarkCommand runs a chaos benchmark on a simulated cluster and
// prints recovery metrics. With --json, outputs a full RecoveryReport as JSON.
func executeBenchmarkCommand(config benchmarkConfig) {
	totalInstances := config.serviceCount * config.instancesPerService

	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	factStore.SetWatchChannelBufferSize(4096)

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

	for serviceIndex := 0; serviceIndex < config.serviceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		benchmarkCluster.DeployService(ctx, serviceName, fmt.Sprintf("app:v%d", serviceIndex), config.instancesPerService)
	}

	deployStart := time.Now()
	deployConverged := waitForBenchmarkConvergence(ctx, benchmarkCluster, benchmarkDeployConvergenceDeadline)
	deployDuration := time.Since(deployStart)

	if !deployConverged {
		fmt.Fprintf(os.Stderr, "deployment did not converge within %v\n", benchmarkDeployConvergenceDeadline)
		os.Exit(1)
	}

	if !config.jsonOutput {
		fmt.Printf("Deployment converged in %v\n\n", deployDuration.Round(time.Millisecond))
		fmt.Println("Starting chaos injection for 60 seconds...")
	}

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
	if !config.jsonOutput {
		chaosRunner.SetEventCallback(func(event chaos.ChaosEvent) {
			convergenceLabel := "TIMEOUT"
			if event.Converged {
				convergenceLabel = fmt.Sprintf("CONVERGED in %v", event.ConvergenceTime.Round(time.Millisecond))
			}
			fmt.Printf("[%v] %-20s target=%-14s %s\n",
				event.Elapsed.Round(time.Second), event.Scenario, event.Target, convergenceLabel)
		})
	}

	chaosEvents := chaosRunner.Run(ctx)

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

	if metrics.RecoverySuccessRate >= 80 {
		fmt.Println("\nResult: PASS")
	} else {
		fmt.Println("\nResult: FAIL — recovery rate below 80%")
	}
}

// configureBenchmarkCluster sets agent and controller tuning parameters
// appropriate for the given node count.
func configureBenchmarkCluster(cluster *chaos.SimulatedChaosCluster, nodeCount int) {
	switch {
	case nodeCount <= 20:
		cluster.SetAgentInterval(100 * time.Millisecond)
		cluster.SetControllerDebounce(50 * time.Millisecond)
		cluster.SetMaxReconciliationAttempts(5)
		cluster.SetLeaseTimeout(5 * time.Second)
	case nodeCount <= 100:
		cluster.SetAgentInterval(200 * time.Millisecond)
		cluster.SetControllerDebounce(100 * time.Millisecond)
		cluster.SetMaxReconciliationAttempts(10)
		cluster.SetLeaseTimeout(30 * time.Second)
	default:
		cluster.SetAgentInterval(500 * time.Millisecond)
		cluster.SetControllerDebounce(200 * time.Millisecond)
		cluster.SetMaxReconciliationAttempts(15)
		cluster.SetLeaseTimeout(60 * time.Second)
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
