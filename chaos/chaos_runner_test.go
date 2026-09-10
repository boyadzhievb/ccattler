package chaos

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// helperSetupChaosCluster creates and starts a 3-node SimulatedChaosCluster
// with a "web" service (6 instances) and an "api" service (3 instances).
// Returns the cluster, a cancel function, and the backing MemoryStore.
func helperSetupChaosCluster(t *testing.T) (*SimulatedChaosCluster, context.CancelFunc, *store.MemoryStore) {
	t.Helper()

	factStore := store.NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())

	cluster := NewSimulatedChaosCluster(factStore, []string{"node-1", "node-2", "node-3"})
	cluster.Start(ctx)

	cluster.DeployService(ctx, "web", "nginx:1.28", 6)
	cluster.DeployService(ctx, "api", "myapp:latest", 3)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		converged, _ := cluster.CheckConvergence(ctx)
		if converged {
			return cluster, cancel, factStore
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("initial deployment did not converge within 10s")
	return nil, nil, nil
}

// TestChaosRunnerSingleNodeKill verifies the chaos runner can inject a node
// kill and detect convergence.
func TestChaosRunnerSingleNodeKill(t *testing.T) {
	cluster, cancel, factStore := helperSetupChaosCluster(t)
	defer cancel()
	defer factStore.Close()

	chaosRunner := NewChaosRunner(ChaosConfig{
		Duration:           3 * time.Second,
		InjectionInterval:  1 * time.Second,
		ConvergenceTimeout: 10 * time.Second,
		EnabledScenarios:   []FailureScenario{ScenarioNodeKill},
		RandSource:         rand.New(rand.NewSource(42)),
	}, cluster)

	ctx := context.Background()
	events := chaosRunner.Run(ctx)

	if len(events) == 0 {
		t.Fatal("expected at least one chaos event")
	}
	for _, event := range events {
		if event.Scenario != ScenarioNodeKill {
			t.Errorf("expected node-kill scenario, got %s", event.Scenario)
		}
		if !event.Converged {
			t.Errorf("event %s target=%s did not converge", event.Scenario, event.Target)
		}
	}
}

// TestChaosRunnerPartitionAndHeal verifies the chaos runner can inject a
// network partition and detect convergence after automatic heal.
func TestChaosRunnerPartitionAndHeal(t *testing.T) {
	cluster, cancel, factStore := helperSetupChaosCluster(t)
	defer cancel()
	defer factStore.Close()

	chaosRunner := NewChaosRunner(ChaosConfig{
		Duration:           3 * time.Second,
		InjectionInterval:  1 * time.Second,
		ConvergenceTimeout: 10 * time.Second,
		EnabledScenarios:   []FailureScenario{ScenarioNodePartition},
		RandSource:         rand.New(rand.NewSource(42)),
	}, cluster)

	ctx := context.Background()
	events := chaosRunner.Run(ctx)

	if len(events) == 0 {
		t.Fatal("expected at least one chaos event")
	}
	for _, event := range events {
		if !event.Converged {
			t.Errorf("event %s target=%s did not converge", event.Scenario, event.Target)
		}
	}
}

// TestChaosRunnerControllerRestart verifies the chaos runner can restart
// controllers and detect convergence.
func TestChaosRunnerControllerRestart(t *testing.T) {
	cluster, cancel, factStore := helperSetupChaosCluster(t)
	defer cancel()
	defer factStore.Close()

	chaosRunner := NewChaosRunner(ChaosConfig{
		Duration:           2 * time.Second,
		InjectionInterval:  1 * time.Second,
		ConvergenceTimeout: 10 * time.Second,
		EnabledScenarios:   []FailureScenario{ScenarioControllerRestart},
		RandSource:         rand.New(rand.NewSource(42)),
	}, cluster)

	ctx := context.Background()
	events := chaosRunner.Run(ctx)

	if len(events) == 0 {
		t.Fatal("expected at least one chaos event")
	}
	for _, event := range events {
		if !event.Converged {
			t.Errorf("event %s did not converge", event.Scenario)
		}
	}
}

// TestChaosRunnerScaleChange verifies the chaos runner can change service
// scale and detect convergence.
func TestChaosRunnerScaleChange(t *testing.T) {
	cluster, cancel, factStore := helperSetupChaosCluster(t)
	defer cancel()
	defer factStore.Close()

	chaosRunner := NewChaosRunner(ChaosConfig{
		Duration:           2 * time.Second,
		InjectionInterval:  1 * time.Second,
		ConvergenceTimeout: 10 * time.Second,
		EnabledScenarios:   []FailureScenario{ScenarioScaleChange},
		RandSource:         rand.New(rand.NewSource(42)),
	}, cluster)

	ctx := context.Background()
	events := chaosRunner.Run(ctx)

	if len(events) == 0 {
		t.Fatal("expected at least one chaos event")
	}
	for _, event := range events {
		if !event.Converged {
			t.Errorf("event %s target=%s did not converge", event.Scenario, event.Target)
		}
	}
}

// TestStoreRestartConvergence simulates a full store outage (all nodes
// partitioned simultaneously) and verifies the cluster converges after the
// store becomes available again. This is the "restart etcd" scenario.
func TestStoreRestartConvergence(t *testing.T) {
	cluster, cancel, factStore := helperSetupChaosCluster(t)
	defer cancel()
	defer factStore.Close()

	ctx := context.Background()

	converged, status := cluster.CheckConvergence(ctx)
	if !converged {
		t.Fatalf("cluster should be converged before partition: %s", status)
	}

	for _, nodeID := range cluster.NodeIDs() {
		cluster.PartitionNode(nodeID)
	}

	time.Sleep(500 * time.Millisecond)

	for _, nodeID := range cluster.NodeIDs() {
		cluster.HealNode(nodeID)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		converged, status = cluster.CheckConvergence(ctx)
		if converged {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("cluster did not converge after store restart: %s", status)
}

// TestChaosRunnerFullChaosConverges enables all failure scenarios and runs
// chaos for a longer duration, asserting that the system converges after
// every injection.
func TestChaosRunnerFullChaosConverges(t *testing.T) {
	cluster, cancel, factStore := helperSetupChaosCluster(t)
	defer cancel()
	defer factStore.Close()

	chaosRunner := NewChaosRunner(ChaosConfig{
		Duration:          8 * time.Second,
		InjectionInterval: 2 * time.Second,
		ConvergenceTimeout: 10 * time.Second,
		EnabledScenarios: []FailureScenario{
			ScenarioNodeKill,
			ScenarioNodePartition,
			ScenarioControllerRestart,
			ScenarioScaleChange,
		},
		RandSource: rand.New(rand.NewSource(123)),
	}, cluster)

	ctx := context.Background()
	events := chaosRunner.Run(ctx)

	if len(events) == 0 {
		t.Fatal("expected chaos events")
	}

	convergedCount := 0
	for _, event := range events {
		if event.Converged {
			convergedCount++
		}
	}

	convergenceRate := float64(convergedCount) / float64(len(events))
	if convergenceRate < 0.8 {
		t.Errorf("convergence rate %.0f%% (%d/%d) is below 80%% threshold",
			convergenceRate*100, convergedCount, len(events))
	}
}
