// command_chaos.go contains CLI commands for chaos testing and related helpers.
package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"time"

	"github.com/boyadzhievb/ccattler/chaos"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// chaosInitialConvergenceDeadline is the time allowed for the initial
	// deployment to converge before chaos injection starts.
	chaosInitialConvergenceDeadline = 15 * time.Second

	// chaosConvergencePollInterval is the polling interval when checking
	// whether the cluster has converged during the initial deployment phase.
	chaosConvergencePollInterval = 100 * time.Millisecond

	// chaosInjectionDuration is the total time chaos events are injected.
	chaosInjectionDuration = 30 * time.Second

	// chaosInjectionInterval is the time between consecutive chaos injections.
	chaosInjectionInterval = 3 * time.Second

	// chaosConvergenceTimeout is the time allowed for the cluster to recover
	// after each chaos injection.
	chaosConvergenceTimeout = 10 * time.Second

	// chaosConvergencePassRate is the minimum percentage of injections that
	// must converge for the chaos test to pass.
	chaosConvergencePassRate = 80
)

// executeChaosCommand runs a 30-second chaos test on a 3-node simulated cluster.
// It deploys two services, then randomly injects node kills, network partitions,
// controller restarts, and scale changes — printing live convergence results.
func executeChaosCommand() {
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()

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

	convergenceDeadline := time.Now().Add(chaosInitialConvergenceDeadline)
	for time.Now().Before(convergenceDeadline) {
		converged, statusDescription := chaosCluster.CheckConvergence(ctx)
		if converged {
			fmt.Printf("Initial deployment converged: %s\n", statusDescription)
			break
		}
		time.Sleep(chaosConvergencePollInterval)
	}

	fmt.Println()
	fmt.Println("Starting chaos injection for 30 seconds...")
	fmt.Println()

	chaosConfig := chaos.ChaosConfig{
		Duration:           chaosInjectionDuration,
		InjectionInterval:  chaosInjectionInterval,
		ConvergenceTimeout: chaosConvergenceTimeout,
		EnabledScenarios: []chaos.FailureScenario{
			chaos.ScenarioNodeKill,
			chaos.ScenarioNodePartition,
			chaos.ScenarioControllerRestart,
			chaos.ScenarioScaleChange,
		},
		RandSource: rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // math/rand for jitter
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
	printChaosSummary(ctx, chaosCluster, chaosEvents)
}

// printChaosSummary prints the chaos test results: injection count, convergence
// rate, max recovery time, final cluster state, and pass/fail verdict.
func printChaosSummary(ctx context.Context, chaosCluster *chaos.SimulatedChaosCluster, chaosEvents []chaos.ChaosEvent) {
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

	if convergenceRate >= chaosConvergencePassRate {
		fmt.Println("\nResult: PASS — cluster resilient under chaos")
	} else {
		fmt.Printf("\nResult: FAIL — convergence rate below %d%%\n", chaosConvergencePassRate)
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
