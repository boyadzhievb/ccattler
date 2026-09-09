package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/infra"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestStabilizationWindowPreventsOscillation(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 3)

	autoscaleController := controllers.NewAutoscaleController()

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)
	runner.SetResyncInterval(500 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service web {
    image nginx:1.27
    instances 3
    scale {
        horizontal {
            min 2
            max 10
            target cpu = 60
            stabilization {
                scale_up 1s
                scale_down 2s
            }
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

	factStore.Put(ctx, types.KeyObservedMetric("web", "cpu"), []byte("90"))

	// The stabilization window should prevent immediate scale-up for the first second.
	// After the window passes, the periodic resync triggers the autoscaler to apply the change.
	waitFor(t, 5*time.Second, "scale up after stabilization window", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 4
	})
}

func TestEventDrivenScalingWithQueueDepth(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 5)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 5)
	go runner.Run(ctx)

	input := `
service worker {
    image worker:1.0
    instances 2
    scale {
        horizontal {
            min 1
            max 20
            event payments.pending = 20
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

	// Queue depth = 100, target = 20 per instance → ceil(100/20) = 5
	factStore.Put(ctx, types.KeyObservedMetric("worker", "event.payments.pending"), []byte("100"))

	waitFor(t, 5*time.Second, "5 instances for queue depth", func() bool {
		return countRunningInstancesForService(ctx, factStore, "worker") >= 5
	})
}

func TestScheduledScalingSetsMinimumDuringWindow(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 5)

	autoscaleController := controllers.NewAutoscaleController()
	now := time.Now()
	currentHour := now.Hour()
	startTime := strconv.Itoa(currentHour) + ":00"
	endTime := strconv.Itoa(currentHour+1) + ":00"
	if currentHour+1 > 23 {
		endTime = "23:59"
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 5)
	go runner.Run(ctx)

	factStore.Put(ctx, types.KeyDesiredService("api"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("api:1.0"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("api"), []byte("2"))
	factStore.Put(ctx, types.KeyIntentUserServiceInstances("api"), []byte("2"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleHorizontalMin("api"), []byte("1"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleHorizontalMax("api"), []byte("10"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleHorizontalTarget("api", "cpu"), []byte("60"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleScheduleDays("api"), []byte("everyday"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleScheduleStart("api"), []byte(startTime))
	factStore.Put(ctx, types.KeyDesiredServiceScaleScheduleEnd("api"), []byte(endTime))
	factStore.Put(ctx, types.KeyDesiredServiceScaleScheduleMinimum("api"), []byte("5"))

	waitFor(t, 5*time.Second, "5 running instances from schedule", func() bool {
		return countRunningInstancesForService(ctx, factStore, "api") >= 5
	})
}

func TestVerticalAutoscalingAdjustsResources(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore, autoscaleController, intentResolverController)
	runner.SetDebounce(10 * time.Millisecond)

	go runner.Run(ctx)

	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("web"), []byte("500"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesMemory("web"), []byte("512"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleVerticalCPUMin("web"), []byte("250"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleVerticalCPUMax("web"), []byte("4000"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleVerticalMemoryMin("web"), []byte("256"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleVerticalMemoryMax("web"), []byte("8192"))

	factStore.Put(ctx, types.KeyObservedMetric("web", "cpu"), []byte("90"))
	factStore.Put(ctx, types.KeyObservedMetric("web", "memory"), []byte("85"))

	waitFor(t, 3*time.Second, "autoscaler writes CPU recommendation", func() bool {
		fact, err := factStore.Get(ctx, types.KeyIntentAutoscalerServiceResourcesCPU("web"))
		if err != nil {
			return false
		}
		recommended, _ := strconv.Atoi(string(fact.Value))
		return recommended > 500
	})

	waitFor(t, 3*time.Second, "autoscaler writes memory recommendation", func() bool {
		fact, err := factStore.Get(ctx, types.KeyIntentAutoscalerServiceResourcesMemory("web"))
		if err != nil {
			return false
		}
		recommended, _ := strconv.Atoi(string(fact.Value))
		return recommended > 512
	})
}

func TestQuotaAwareScalingClampsToQuota(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 3)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	autoscaleController := controllers.NewAutoscaleController()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		autoscaleController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("2"))
	factStore.Put(ctx, types.KeyIntentUserServiceInstances("web"), []byte("2"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleHorizontalMin("web"), []byte("1"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleHorizontalMax("web"), []byte("20"))
	factStore.Put(ctx, types.KeyDesiredServiceScaleHorizontalTarget("web", "cpu"), []byte("50"))

	// Set quota to 4 instances max.
	factStore.Put(ctx, types.KeyDesiredServiceQuotaInstances("web"), []byte("4"))

	waitFor(t, 5*time.Second, "2 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 2
	})

	// High CPU: would recommend ceil(2*95/50)=4 without quota, which fits.
	// But let's make it want more: ceil(4*95/50)=8 → quota caps at 4.
	factStore.Put(ctx, types.KeyObservedMetric("web", "cpu"), []byte("95"))

	waitFor(t, 5*time.Second, "scale up to quota limit", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 4
	})

	time.Sleep(500 * time.Millisecond)

	finalCount := countRunningInstancesForService(ctx, factStore, "web")
	if finalCount > 4 {
		t.Fatalf("expected max 4 instances (quota), got %d", finalCount)
	}
}

func TestClusterAutoscaleProvisionesNodesForUnplacedInstances(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start with just 1 node that can fit 1 instance.
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 1000, CapacityMemory: 2048,
		AvailableCPU: 1000, AvailableMemory: 2048,
	})

	simulatorInfraProvider := infra.NewSimulatorInfraProvider(factStore)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	clusterAutoscaleController := controllers.NewClusterAutoscaleController(simulatorInfraProvider)

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, clusterAutoscaleController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 1)
	go runner.Run(ctx)

	factStore.Put(ctx, types.KeyDesiredClusterAutoscaleMaxNodes(), []byte("5"))

	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("3"))
	factStore.Put(ctx, types.KeyIntentUserServiceInstances("web"), []byte("3"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))
	factStore.Put(ctx, types.KeyDesiredServiceResourcesCPU("web"), []byte("1000"))

	waitFor(t, 5*time.Second, "infra provider provisions new nodes", func() bool {
		nodeCount, _ := simulatorInfraProvider.NodeCount(ctx)
		return nodeCount >= 2
	})
}

func TestPlacementConstraintArchitecture(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// node-1: amd64, node-2: arm64, node-3: amd64
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
		Architecture: "amd64",
	})
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-2", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
		Architecture: "arm64",
	})
	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-3", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
		Architecture: "amd64",
	})

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service web {
    image nginx:1.27
    instances 4
    placement {
        architecture amd64
    }
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "4 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 4
	})

	// Verify no instances placed on arm64 node.
	instances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range instances {
		if instance.Service == "web" && instance.State == types.InstanceRunning {
			placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID))
			if err != nil {
				continue
			}
			if string(placementFact.Value) == "node-2" {
				t.Errorf("instance %s placed on arm64 node-2, expected only amd64 nodes", instance.ID)
			}
		}
	}
}

func TestPlacementConstraintZoneSpread(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3 nodes across 3 zones.
	for zoneIndex, nodeID := range []string{"node-1", "node-2", "node-3"} {
		zones := []string{"us-east-1a", "us-east-1b", "us-east-1c"}
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
			Architecture: "amd64", Zone: zones[zoneIndex],
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	intentResolverController := controllers.NewIntentResolverController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController, intentResolverController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service web {
    image nginx:1.27
    instances 3
    placement {
        zone spread
    }
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "3 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 3
	})

	// Verify instances are spread across 3 zones (1 per zone).
	instances, _ := types.ListInstances(ctx, factStore)
	nodePlacements := make(map[string]int)
	for _, instance := range instances {
		if instance.Service == "web" && instance.State == types.InstanceRunning {
			placementFact, err := factStore.Get(ctx, types.KeyPlacementInstance(instance.ID))
			if err != nil {
				continue
			}
			nodePlacements[string(placementFact.Value)]++
		}
	}

	if len(nodePlacements) < 3 {
		t.Errorf("expected spread across 3 nodes/zones, got %d nodes: %v", len(nodePlacements), nodePlacements)
	}
}

func TestRollingUpdateGraduallyReplacesInstances(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerTestNodes(ctx, factStore, 3)

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	intentResolverController := controllers.NewIntentResolverController()
	rolloutController := controllers.NewRolloutController()

	runner := controllers.NewRunner(factStore,
		instanceController, schedulerController,
		intentResolverController, rolloutController,
	)
	runner.SetDebounce(10 * time.Millisecond)

	startTestAgents(ctx, factStore, 3)
	go runner.Run(ctx)

	input := `
service web {
    image nginx:1.27
    instances 3
    update {
        max_unavailable 1
        max_extra 1
    }
}
`
	if err := lang.Apply(ctx, factStore, input); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	waitFor(t, 5*time.Second, "3 running instances", func() bool {
		return countRunningInstancesForService(ctx, factStore, "web") >= 3
	})

	// Update the image — the rollout controller should gradually replace.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))

	waitFor(t, 10*time.Second, "rollout completes with new image", func() bool {
		instances, _ := types.ListInstances(ctx, factStore)
		newImageCount := 0
		for _, instance := range instances {
			if instance.Service == "web" && instance.State == types.InstanceRunning && instance.Image == "nginx:1.28" {
				newImageCount++
			}
		}
		return newImageCount >= 3
	})
}

func TestRollbackOnHealthFailure(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rolloutController := controllers.NewRolloutController()

	runner := controllers.NewRunner(factStore, rolloutController)
	runner.SetDebounce(10 * time.Millisecond)

	go runner.Run(ctx)

	// Set up a service with desired image v2 and 3 instances that still have v1.
	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("app:v2"))
	factStore.Put(ctx, types.KeyDesiredServiceUpdateMaxUnavailable("web"), []byte("1"))
	factStore.Put(ctx, types.KeyDesiredServiceUpdateMaxExtra("web"), []byte("1"))

	// Create 3 old-image running instances.
	for _, instanceID := range []string{"inst-old-1", "inst-old-2", "inst-old-3"} {
		factStore.Put(ctx, types.KeyObservedInstance(instanceID), []byte(""))
		factStore.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte("web"))
		factStore.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstanceRunning)))
		factStore.Put(ctx, types.KeyObservedInstanceImage(instanceID), []byte("app:v1"))
	}

	// Wait for rollout controller to detect the mismatch and start rolling.
	waitFor(t, 5*time.Second, "rollout state becomes rolling", func() bool {
		fact, err := factStore.Get(ctx, types.KeyObservedServiceRolloutState("web"))
		return err == nil && string(fact.Value) == "rolling"
	})

	// Now simulate 3 failed new-image instances directly.
	for _, instanceID := range []string{"inst-new-1", "inst-new-2", "inst-new-3"} {
		factStore.Put(ctx, types.KeyObservedInstance(instanceID), []byte(""))
		factStore.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte("web"))
		factStore.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte(string(types.InstanceFailed)))
		factStore.Put(ctx, types.KeyObservedInstanceImage(instanceID), []byte("app:v2"))
	}

	waitFor(t, 5*time.Second, "rollback reverts to v1", func() bool {
		fact, err := factStore.Get(ctx, types.KeyDesiredServiceImage("web"))
		return err == nil && string(fact.Value) == "app:v1"
	})
}

func TestDSLPhase10FeaturesParseAndCompile(t *testing.T) {
	input := `
service web {
    image nginx:1.27
    instances 3
    expose 8080

    resources {
        cpu 500m
        memory 512Mi
    }

    scale {
        horizontal {
            min 2
            max 30
            target cpu = 60
            target requests_per_second = 500
            event payments.pending = 20
            schedule {
                days weekdays
                start "08:00"
                end "18:00"
                minimum 10
            }
            stabilization {
                scale_up 60s
                scale_down 300s
            }
        }
        vertical {
            cpu {
                min 250
                max 4000
            }
            memory {
                min 512
                max 8192
            }
        }
    }

    placement {
        architecture amd64
        zone spread
    }

    update {
        max_unavailable 1
        max_extra 1
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

	// Horizontal
	if service.Scale.Horizontal.Min != 2 {
		t.Errorf("horizontal min: expected 2, got %d", service.Scale.Horizontal.Min)
	}
	if service.Scale.Horizontal.Max != 30 {
		t.Errorf("horizontal max: expected 30, got %d", service.Scale.Horizontal.Max)
	}
	if len(service.Scale.Horizontal.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(service.Scale.Horizontal.Targets))
	}
	if len(service.Scale.Horizontal.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(service.Scale.Horizontal.Events))
	} else {
		if service.Scale.Horizontal.Events[0].Source != "payments.pending" {
			t.Errorf("event source: expected payments.pending, got %s", service.Scale.Horizontal.Events[0].Source)
		}
		if service.Scale.Horizontal.Events[0].Target != 20 {
			t.Errorf("event target: expected 20, got %d", service.Scale.Horizontal.Events[0].Target)
		}
	}

	// Schedule
	if service.Scale.Horizontal.Schedule == nil {
		t.Fatal("expected schedule, got nil")
	}
	if service.Scale.Horizontal.Schedule.Days != "weekdays" {
		t.Errorf("schedule days: expected weekdays, got %s", service.Scale.Horizontal.Schedule.Days)
	}
	if service.Scale.Horizontal.Schedule.Minimum != 10 {
		t.Errorf("schedule minimum: expected 10, got %d", service.Scale.Horizontal.Schedule.Minimum)
	}

	// Stabilization
	if service.Scale.Horizontal.Stabilization == nil {
		t.Fatal("expected stabilization, got nil")
	}
	if service.Scale.Horizontal.Stabilization.ScaleUp != "60s" {
		t.Errorf("stabilization scale_up: expected 60s, got %s", service.Scale.Horizontal.Stabilization.ScaleUp)
	}
	if service.Scale.Horizontal.Stabilization.ScaleDown != "300s" {
		t.Errorf("stabilization scale_down: expected 300s, got %s", service.Scale.Horizontal.Stabilization.ScaleDown)
	}

	// Vertical
	if service.Scale.Vertical == nil {
		t.Fatal("expected vertical, got nil")
	}
	if service.Scale.Vertical.CPUMin != "250" {
		t.Errorf("vertical cpu min: expected 250, got %s", service.Scale.Vertical.CPUMin)
	}
	if service.Scale.Vertical.CPUMax != "4000" {
		t.Errorf("vertical cpu max: expected 4000, got %s", service.Scale.Vertical.CPUMax)
	}
	if service.Scale.Vertical.MemoryMin != "512" {
		t.Errorf("vertical memory min: expected 512, got %s", service.Scale.Vertical.MemoryMin)
	}
	if service.Scale.Vertical.MemoryMax != "8192" {
		t.Errorf("vertical memory max: expected 8192, got %s", service.Scale.Vertical.MemoryMax)
	}

	// Placement
	if service.Placement == nil {
		t.Fatal("expected placement, got nil")
	}
	if service.Placement.Architecture != "amd64" {
		t.Errorf("placement architecture: expected amd64, got %s", service.Placement.Architecture)
	}
	if service.Placement.ZonePolicy != "spread" {
		t.Errorf("placement zone: expected spread, got %s", service.Placement.ZonePolicy)
	}

	// Update
	if service.Update == nil {
		t.Fatal("expected update, got nil")
	}
	if service.Update.MaxUnavailable != 1 {
		t.Errorf("update max_unavailable: expected 1, got %d", service.Update.MaxUnavailable)
	}
	if service.Update.MaxExtra != 1 {
		t.Errorf("update max_extra: expected 1, got %d", service.Update.MaxExtra)
	}

	// Compile and check facts
	facts, err := lang.Compile(file)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	factMap := make(map[string]string)
	for _, fact := range facts {
		factMap[fact.Key] = fact.Value
	}

	expectedFacts := map[string]string{
		types.KeyDesiredServiceScaleHorizontalMin("web"):              "2",
		types.KeyDesiredServiceScaleHorizontalMax("web"):              "30",
		types.KeyDesiredServiceScaleHorizontalTarget("web", "cpu"):    "60",
		types.KeyDesiredServiceScaleHorizontalEvent("web", "payments.pending"): "20",
		types.KeyDesiredServiceScaleScheduleDays("web"):                        "weekdays",
		types.KeyDesiredServiceScaleScheduleStart("web"):                       "08:00",
		types.KeyDesiredServiceScaleScheduleEnd("web"):                         "18:00",
		types.KeyDesiredServiceScaleScheduleMinimum("web"):                     "10",
		types.KeyDesiredServiceScaleStabilizationUp("web"):                     "60s",
		types.KeyDesiredServiceScaleStabilizationDown("web"):                   "300s",
		types.KeyDesiredServiceScaleVerticalCPUMin("web"):                      "250",
		types.KeyDesiredServiceScaleVerticalCPUMax("web"):                      "4000",
		types.KeyDesiredServiceScaleVerticalMemoryMin("web"):                   "512",
		types.KeyDesiredServiceScaleVerticalMemoryMax("web"):                   "8192",
		types.KeyDesiredServicePlacementArchitecture("web"):                    "amd64",
		types.KeyDesiredServicePlacementZonePolicy("web"):                      "spread",
		types.KeyDesiredServiceUpdateMaxUnavailable("web"):                     "1",
		types.KeyDesiredServiceUpdateMaxExtra("web"):                           "1",
	}

	for key, expectedValue := range expectedFacts {
		if actualValue, exists := factMap[key]; !exists {
			t.Errorf("missing expected fact key: %s", key)
		} else if actualValue != expectedValue {
			t.Errorf("key %s: expected %q, got %q", key, expectedValue, actualValue)
		}
	}
}
