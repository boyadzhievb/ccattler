package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestAutoscaleScalesUpOnHighCPU(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 3)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, endpointController,
		failureController, autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service web {
    image nginx:1.27
    instances 3
    expose 8080
    scale {
        horizontal {
            min 2
            max 10
            target cpu = 60
        }
    }
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "3 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 3
	})

	// Inject high CPU metric: 90% average per instance, target is 60%.
	// Recommendation: ceil(3 * 90 / 60) = ceil(4.5) = 5
	factStore.Put(ctx, types.KeyObservedMetric("web", "cpu"), []byte("90"))

	waitFor(t, 5*time.Second, "5 running instances after CPU spike", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 5
	})

	runningCount := countRunningInstancesForService(ctx, factStore, "web")
	if runningCount < 5 {
		t.Fatalf("expected at least 5 running instances, got %d", runningCount)
	}
}

func TestAutoscaleScalesDownOnLowCPU(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 3)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	failureController := controllers.NewFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		failureController, autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service api {
    image api:2.0
    instances 6
    scale {
        horizontal {
            min 2
            max 10
            target cpu = 50
        }
    }
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "6 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "api") >= 6
	})

	// Inject low CPU: 20% average. Recommendation: ceil(6 * 20 / 50) = ceil(2.4) = 3
	// But user intent is 6, so effective = max(6, 3) = 6 (no scale-down below user intent).
	// However, if we lower user intent to 2, scale-down can happen.
	// Let's test with user intent = 2 and initial autoscaler driving it up, then down.

	// Reapply with lower user intent.
	inputScaledDown := `
service api {
    image api:2.0
    instances 2
    scale {
        horizontal {
            min 2
            max 10
            target cpu = 50
        }
    }
}
`
	if err := lang.Apply(ctx, factStore, inputScaledDown); err != nil {
		t.Fatalf("re-apply failed: %v", err)
	}

	// First, set high CPU to keep it scaled up.
	factStore.Put(ctx, types.KeyObservedMetric("api", "cpu"), []byte("80"))

	waitFor(t, 5*time.Second, "autoscaler scales up from 2", func() bool {
		return countRunningInstancesForService(ctx, factStore, "api") >= 3
	})

	// Now drop CPU to 10%. Recommendation: ceil(currentCount * 10 / 50) should be small.
	factStore.Put(ctx, types.KeyObservedMetric("api", "cpu"), []byte("10"))

	waitFor(t, 5*time.Second, "scale down to 2 (min)", func() bool {
		count := countRunningInstancesForService(ctx, factStore, "api")
		return count <= 2
	})

	finalCount := countRunningInstancesForService(ctx, factStore, "api")
	if finalCount > 2 {
		t.Fatalf("expected scale-down to 2 (min), got %d", finalCount)
	}
}

func TestAutoscaleRespectsMaxBound(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 3)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	failureController := controllers.NewFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		failureController, autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service worker {
    image worker:1.0
    instances 2
    scale {
        horizontal {
            min 2
            max 5
            target cpu = 30
        }
    }
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "2 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "worker") >= 2
	})

	// Inject extremely high CPU: 95%. Without max, recommendation would be ceil(2*95/30)=7.
	// But max is 5, so should cap at 5.
	factStore.Put(ctx, types.KeyObservedMetric("worker", "cpu"), []byte("95"))

	waitFor(t, 5*time.Second, "scale up to 5 (max)", func() bool {
		return countRunningInstancesForService(ctx, factStore, "worker") >= 5
	})

	// Give a moment for potential over-scaling.
	time.Sleep(500 * time.Millisecond)

	finalCount := countRunningInstancesForService(ctx, factStore, "worker")
	if finalCount > 5 {
		t.Fatalf("expected max 5 instances, got %d (max bound violated)", finalCount)
	}
}

func TestAutoscaleMultipleMetricsTakesMax(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 3)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	failureController := controllers.NewFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		failureController, autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service gateway {
    image gateway:1.0
    instances 2
    scale {
        horizontal {
            min 2
            max 10
            target cpu = 60
            target requests_per_second = 500
        }
    }
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "2 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "gateway") >= 2
	})

	// CPU says 3 instances: ceil(2 * 80 / 60) = ceil(2.67) = 3
	// RPS says 4 instances: ceil(2 * 900 / 500) = ceil(3.6) = 4
	// Result should be max(3, 4) = 4
	factStore.Put(ctx, types.KeyObservedMetric("gateway", "cpu"), []byte("80"))
	factStore.Put(ctx, types.KeyObservedMetric("gateway", "requests_per_second"), []byte("900"))

	waitFor(t, 5*time.Second, "4 running instances (max of CPU and RPS recommendations)", func() bool {
		return countRunningInstancesForService(ctx, factStore, "gateway") >= 4
	})
}

func TestIntentResolverMergesUserAndAutoscaler(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	intentResolverController := controllers.NewIntentResolverController()
	runner := controllers.NewRunner(factStore, intentResolverController)
	runner.SetDebounce(10 * time.Millisecond)

	go runner.Run(ctx)

	// User wants 3.
	factStore.Put(ctx, types.KeyIntentUserServiceInstances("web"), []byte("3"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleHorizontalMin("web"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleHorizontalMax("web"), []byte("10"))

	waitFor(t, 2*time.Second, "effective = 3 from user intent", func() bool {
		fact, err := factStore.Get(ctx, types.KeyEffectiveServiceInstances("web"))
		return err == nil && string(fact.Value) == "3"
	})

	// Autoscaler recommends 7.
	factStore.Put(ctx, types.KeyIntentAutoscalerServiceInstances("web"), []byte("7"))

	waitFor(t, 2*time.Second, "effective = 7 from autoscaler", func() bool {
		fact, err := factStore.Get(ctx, types.KeyEffectiveServiceInstances("web"))
		return err == nil && string(fact.Value) == "7"
	})

	// Autoscaler drops to 1, but min is 2 and user says 3 → effective = 3.
	factStore.Put(ctx, types.KeyIntentAutoscalerServiceInstances("web"), []byte("1"))

	waitFor(t, 2*time.Second, "effective = 3 (user intent > autoscaler)", func() bool {
		fact, err := factStore.Get(ctx, types.KeyEffectiveServiceInstances("web"))
		return err == nil && string(fact.Value) == "3"
	})
}

func TestDSLScaleBlockParsesAndCompiles(t *testing.T) {
	input := `
service web {
    image nginx:1.27
    instances 3
    expose 8080
    scale {
        horizontal {
            min 2
            max 20
            target cpu = 60
            target memory = 70
        }
    }
}
`
	file, err := lang.Parse(input)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(file.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(file.Services))
	}

	service := file.Services[0]
	if service.Scale == nil {
		t.Fatal("expected scale block, got nil")
	}
	if service.Scale.Horizontal == nil {
		t.Fatal("expected horizontal block, got nil")
	}
	if service.Scale.Horizontal.Min != 2 {
		t.Errorf("min: expected 2, got %d", service.Scale.Horizontal.Min)
	}
	if service.Scale.Horizontal.Max != 20 {
		t.Errorf("max: expected 20, got %d", service.Scale.Horizontal.Max)
	}
	if len(service.Scale.Horizontal.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(service.Scale.Horizontal.Targets))
	}

	facts, err := lang.Compile(file)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	expectedKeys := map[string]string{
		types.KeyDesiredServiceScaleHorizontalMin("web"):              "2",
		types.KeyDesiredServiceScaleHorizontalMax("web"):              "20",
		types.KeyDesiredServiceScaleHorizontalTarget("web", "cpu"):    "60",
		types.KeyDesiredServiceScaleHorizontalTarget("web", "memory"): "70",
	}
	factMap := make(map[string]string)
	for _, fact := range facts {
		factMap[fact.Key] = fact.Value
	}

	for key, expectedValue := range expectedKeys {
		if actualValue, exists := factMap[key]; !exists {
			t.Errorf("missing expected fact key: %s", key)
		} else if actualValue != expectedValue {
			t.Errorf("key %s: expected %q, got %q", key, expectedValue, actualValue)
		}
	}
}

func TestAutoscaleEndToEndWithDistributedCluster(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 3)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, endpointController,
		failureController, nodeFailureController,
		autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service web {
    image nginx:1.27
    instances 2
    expose 8080
    scale {
        horizontal {
            min 2
            max 8
            target cpu = 50
        }
    }
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "2 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 2
	})

	// Load increases: CPU = 80% → recommendation = ceil(2 * 80 / 50) = ceil(3.2) = 4
	factStore.Put(ctx, types.KeyObservedMetric("web", "cpu"), []byte("80"))

	waitFor(t, 5*time.Second, "4 running instances after load increase", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 4
	})

	// Verify instances are spread across nodes.
	instances, _ := types.ListInstances(ctx, factStore)
	nodeDistribution := make(map[string]int)
	for _, instance := range instances {
		if instance.Service == "web" && instance.State == types.InstanceRunning {
			nodeDistribution[instance.Node]++
		}
	}
	if len(nodeDistribution) < 2 {
		t.Errorf("expected instances spread across at least 2 nodes, got distribution: %v", nodeDistribution)
	}

	// Load drops: CPU = 20% → recommendation = ceil(4 * 20 / 50) = ceil(1.6) = 2
	factStore.Put(ctx, types.KeyObservedMetric("web", "cpu"), []byte("20"))

	waitFor(t, 5*time.Second, "scale down to 2", func() bool {
		count := countRunningInstancesForService(ctx, factStore, "web")
		return count <= 2
	})
}

// registerTestNodes creates the specified number of alive nodes in the store.
func registerTestNodes(ctx context.Context, factStore store.StateStore, count int) {
	for nodeIndex := 1; nodeIndex <= count; nodeIndex++ {
		nodeID := "node-" + strconv.Itoa(nodeIndex)
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}
}

// startTestAgents launches simulator-backed node agents for the given number
// of nodes, each with a fast reconciliation interval.
func startTestAgents(ctx context.Context, factStore store.StateStore, count int) {
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()
	for nodeIndex := 1; nodeIndex <= count; nodeIndex++ {
		nodeID := "node-" + strconv.Itoa(nodeIndex)
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(ctx)
	}
}

// countRunningInstancesForService counts instances in "running" state for the
// given service name.
func countRunningInstancesForService(ctx context.Context, factStore store.StateStore, serviceName string) int {
	instances, err := types.ListInstances(ctx, factStore)
	if err != nil {
		return 0
	}
	count := 0
	for _, instance := range instances {
		if instance.Service == serviceName && instance.State == types.InstanceRunning {
			count++
		}
	}
	return count
}
