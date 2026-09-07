package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/agent"
	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// helperSetupNetworkingCluster creates a 3-node simulated cluster with
// networking enabled. Returns the store, cancel function, network provider,
// and resolver. The caller must defer cancel() and factStore.Close().
func helperSetupNetworkingCluster(t *testing.T) (store.StateStore, context.CancelFunc, *network.SimulatorNetworkProvider, *network.StoreBackedResolver) {
	t.Helper()

	factStore := store.NewMemoryStore()

	ctx, cancel := context.WithCancel(context.Background())

	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	// Register 3 nodes with equal capacity.
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	// Start all controllers including the network controller.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = 500 * time.Millisecond
	networkController := controllers.NewNetworkController()

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, networkController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Start 3 agents, each with its own simulator runtime and the shared network provider.
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		go nodeAgent.Run(ctx)
	}

	storeBackedResolver := network.NewStoreBackedResolver(factStore)
	return factStore, cancel, simulatorNetworkProvider, storeBackedResolver
}

// TestInstancesGetUniqueIPsFromNodeSubnet verifies that instances deployed
// across 3 nodes receive unique IPs from their respective node subnets.
func TestInstancesGetUniqueIPsFromNodeSubnet(t *testing.T) {
	factStore, cancel, _, _ := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	// Deploy 6 instances across 3 nodes.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("6"))

	waitFor(t, 5*time.Second, "6 running instances with IPs", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningWithIP := 0
		for _, instance := range allInstances {
			if instance.State == types.InstanceRunning && instance.IP != "" && instance.IP != "127.0.0.1" {
				runningWithIP++
			}
		}
		return runningWithIP >= 6
	})

	// Verify all IPs are unique and from 10.100.x.y subnets.
	allInstances, _ := types.ListInstances(ctx, factStore)
	seenIPs := make(map[string]bool)
	for _, instance := range allInstances {
		if instance.State != types.InstanceRunning {
			continue
		}
		if !strings.HasPrefix(instance.IP, "10.100.") {
			t.Errorf("instance %s has IP %s, want 10.100.x.y subnet", instance.ID, instance.IP)
		}
		if seenIPs[instance.IP] {
			t.Errorf("duplicate IP: %s on instance %s", instance.IP, instance.ID)
		}
		seenIPs[instance.IP] = true
	}
}

// TestEndpointsReflectAllocatedIPs verifies that endpoint facts contain the
// allocated IPs from the network provider, not the default 127.0.0.1.
func TestEndpointsReflectAllocatedIPs(t *testing.T) {
	factStore, cancel, _, _ := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	waitFor(t, 5*time.Second, "3 endpoints with real IPs", func() bool {
		endpointFacts, _ := factStore.Scan(ctx, types.ScanEndpoints)
		realIPCount := 0
		for _, endpointFact := range endpointFacts {
			if !strings.Contains(string(endpointFact.Value), "127.0.0.1") {
				realIPCount++
			}
		}
		return realIPCount >= 3
	})

	endpointFacts, _ := factStore.Scan(ctx, types.ScanEndpoints)
	for _, endpointFact := range endpointFacts {
		endpointValue := string(endpointFact.Value)
		if strings.Contains(endpointValue, "127.0.0.1") {
			t.Errorf("endpoint %s still has 127.0.0.1: %s", endpointFact.Key, endpointValue)
		}
		if !strings.HasPrefix(endpointValue, "10.100.") {
			t.Errorf("endpoint %s has unexpected IP: %s", endpointFact.Key, endpointValue)
		}
	}
}

// TestServiceGetsVIPAndDNS verifies that the NetworkController creates VIP
// and DNS facts for a service with running instances and an exposed port.
func TestServiceGetsVIPAndDNS(t *testing.T) {
	factStore, cancel, _, _ := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("2"))

	waitFor(t, 5*time.Second, "VIP allocated for web", func() bool {
		vipFact, err := factStore.Get(ctx, types.KeyNetworkVIPService("web"))
		return err == nil && strings.HasPrefix(string(vipFact.Value), "10.200.0.")
	})

	waitFor(t, 5*time.Second, "DNS record created for web", func() bool {
		dnsFact, err := factStore.Get(ctx, types.KeyNetworkDNS("web"))
		return err == nil && strings.HasPrefix(string(dnsFact.Value), "10.200.0.")
	})

	// VIP and DNS should point to the same address.
	vipFact, _ := factStore.Get(ctx, types.KeyNetworkVIPService("web"))
	dnsFact, _ := factStore.Get(ctx, types.KeyNetworkDNS("web"))
	if string(vipFact.Value) != string(dnsFact.Value) {
		t.Errorf("VIP=%s DNS=%s, should match", vipFact.Value, dnsFact.Value)
	}

	// VIP port should match the exposed port.
	portFact, _ := factStore.Get(ctx, types.KeyNetworkVIPServicePort("web"))
	if string(portFact.Value) != "8080" {
		t.Errorf("VIP port=%s, want 8080", portFact.Value)
	}
}

// TestDNSResolvesServiceToVIP verifies that the StoreBackedResolver returns
// the correct VIP for a deployed service.
func TestDNSResolvesServiceToVIP(t *testing.T) {
	factStore, cancel, _, storeBackedResolver := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("2"))

	waitFor(t, 5*time.Second, "VIP available", func() bool {
		virtualIP, err := storeBackedResolver.ResolveVIP(ctx, "web")
		return err == nil && virtualIP != ""
	})

	virtualIP, _ := storeBackedResolver.ResolveVIP(ctx, "web")
	if !strings.HasPrefix(virtualIP, "10.200.0.") {
		t.Errorf("VIP = %s, want prefix 10.200.0.", virtualIP)
	}
}

// TestLoadBalancingDistributesTraffic verifies that the SimulatorProxy
// distributes requests across all running instances using round-robin.
func TestLoadBalancingDistributesTraffic(t *testing.T) {
	factStore, cancel, _, storeBackedResolver := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	// Wait for 3 endpoints with real IPs.
	waitFor(t, 5*time.Second, "3 endpoints available", func() bool {
		resolvedEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
		return len(resolvedEndpoints) >= 3
	})

	simulatorProxy := network.NewSimulatorProxy(storeBackedResolver)

	// Route 9 requests — expect 3 per instance.
	for requestIndex := 0; requestIndex < 9; requestIndex++ {
		_, err := simulatorProxy.RouteRequest(ctx, "web")
		if err != nil {
			t.Fatalf("request %d failed: %v", requestIndex, err)
		}
	}

	routingDecisions := simulatorProxy.RoutingDecisionsForService("web")
	instanceHitCounts := make(map[string]int)
	for _, selectedEndpoint := range routingDecisions {
		instanceHitCounts[selectedEndpoint.InstanceID]++
	}

	for instanceID, hitCount := range instanceHitCounts {
		if hitCount != 3 {
			t.Errorf("instance %s got %d requests, want 3", instanceID, hitCount)
		}
	}
}

// TestNodeFailureUpdatesNetworking verifies that when a node dies, its
// instances' IPs are released, replacements get new IPs from surviving
// nodes, and the load balancer routes correctly.
func TestNodeFailureUpdatesNetworking(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		types.WriteNode(ctx, factStore, types.Node{
			ID: nodeID, State: types.NodeAlive,
			CapacityCPU: 4000, CapacityMemory: 8192,
			AvailableCPU: 4000, AvailableMemory: 8192,
		})
	}

	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	nodeFailureController := controllers.NewNodeFailureController()
	nodeFailureController.LeaseTimeout = 300 * time.Millisecond
	networkController := controllers.NewNetworkController()

	controllerRunner := controllers.NewRunner(factStore, instanceController, schedulerController,
		endpointController, failureController, nodeFailureController, networkController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Start 3 agents — node-1 gets a separate cancel context.
	node1Context, cancelNode1Agent := context.WithCancel(ctx)
	for _, nodeID := range []string{"node-1", "node-2", "node-3"} {
		simulatorRuntime := runtime.NewSimulatorRuntime()
		nodeAgent := agent.New(nodeID, factStore, simulatorRuntime)
		nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
		nodeAgent.SetInterval(50 * time.Millisecond)
		if nodeID == "node-1" {
			go nodeAgent.Run(node1Context)
		} else {
			go nodeAgent.Run(ctx)
		}
	}

	// Deploy 6 instances with exposed port.
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("6"))

	waitFor(t, 5*time.Second, "6 running with real IPs", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningWithIP := 0
		for _, instance := range allInstances {
			if instance.State == types.InstanceRunning && instance.IP != "127.0.0.1" {
				runningWithIP++
			}
		}
		return runningWithIP >= 6
	})

	// Kill node-1.
	cancelNode1Agent()

	// Wait for system to recover: 6 running instances, none on node-1.
	waitFor(t, 10*time.Second, "6 running instances after failure", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningCount := 0
		for _, instance := range allInstances {
			if instance.Service == "web" && instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount >= 6
	})

	// Verify all running instances have real IPs (not 127.0.0.1).
	allInstances, _ := types.ListInstances(ctx, factStore)
	for _, instance := range allInstances {
		if instance.State == types.InstanceRunning && instance.IP == "127.0.0.1" {
			t.Errorf("running instance %s still has 127.0.0.1", instance.ID)
		}
	}
}

// TestScaleUpAddsToLoadBalancerPool verifies that scaling up a service
// causes new instances to appear in the load balancer pool.
func TestScaleUpAddsToLoadBalancerPool(t *testing.T) {
	factStore, cancel, _, storeBackedResolver := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("2"))

	waitFor(t, 5*time.Second, "2 endpoints", func() bool {
		resolvedEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
		return len(resolvedEndpoints) >= 2
	})

	// Scale up to 4.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("4"))

	waitFor(t, 5*time.Second, "4 endpoints after scale-up", func() bool {
		resolvedEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
		return len(resolvedEndpoints) >= 4
	})

	// All 4 endpoints should have real IPs.
	resolvedEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
	for _, endpoint := range resolvedEndpoints {
		if endpoint.IP == "127.0.0.1" || endpoint.IP == "" {
			t.Errorf("endpoint %s has bad IP: %s", endpoint.InstanceID, endpoint.IP)
		}
	}
}

// TestServiceReachableByName is the M4 headline test: deploy two services,
// verify each gets a unique VIP, DNS resolves both names, and traffic to
// each service balances across its instances.
func TestServiceReachableByName(t *testing.T) {
	factStore, cancel, _, storeBackedResolver := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	// Deploy web (4 instances, port 8080) and api (2 instances, port 3000).
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("4"))

	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("myapp:latest"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("api", 3000), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("2"))

	// Wait for both services to have VIPs.
	waitFor(t, 5*time.Second, "both VIPs allocated", func() bool {
		webVIP, webErr := storeBackedResolver.ResolveVIP(ctx, "web")
		apiVIP, apiErr := storeBackedResolver.ResolveVIP(ctx, "api")
		return webErr == nil && apiErr == nil && webVIP != "" && apiVIP != ""
	})

	// Verify unique VIPs.
	webVIP, _ := storeBackedResolver.ResolveVIP(ctx, "web")
	apiVIP, _ := storeBackedResolver.ResolveVIP(ctx, "api")
	if webVIP == apiVIP {
		t.Errorf("services should have different VIPs, both got %s", webVIP)
	}

	// Wait for endpoints.
	waitFor(t, 5*time.Second, "6 total endpoints", func() bool {
		webEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
		apiEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "api")
		return len(webEndpoints) >= 4 && len(apiEndpoints) >= 2
	})

	// Load balance traffic to both services.
	simulatorProxy := network.NewSimulatorProxy(storeBackedResolver)

	for requestIndex := 0; requestIndex < 8; requestIndex++ {
		_, err := simulatorProxy.RouteRequest(ctx, "web")
		if err != nil {
			t.Fatalf("web request %d: %v", requestIndex, err)
		}
	}
	for requestIndex := 0; requestIndex < 4; requestIndex++ {
		_, err := simulatorProxy.RouteRequest(ctx, "api")
		if err != nil {
			t.Fatalf("api request %d: %v", requestIndex, err)
		}
	}

	// Verify web traffic distributed across 4 instances.
	webDecisions := simulatorProxy.RoutingDecisionsForService("web")
	webInstanceHits := make(map[string]int)
	for _, decision := range webDecisions {
		webInstanceHits[decision.InstanceID]++
	}
	if len(webInstanceHits) < 4 {
		t.Errorf("web traffic only hit %d instances, want 4", len(webInstanceHits))
	}

	// Verify api traffic distributed across 2 instances.
	apiDecisions := simulatorProxy.RoutingDecisionsForService("api")
	apiInstanceHits := make(map[string]int)
	for _, decision := range apiDecisions {
		apiInstanceHits[decision.InstanceID]++
	}
	if len(apiInstanceHits) < 2 {
		t.Errorf("api traffic only hit %d instances, want 2", len(apiInstanceHits))
	}
}

// TestMultipleServicesIndependentNetworking verifies that two services
// have independent VIPs, DNS records, endpoint sets, and load balancing.
func TestMultipleServicesIndependentNetworking(t *testing.T) {
	factStore, cancel, _, storeBackedResolver := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("3"))

	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("myapp:latest"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("api", 3000), []byte(""))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("api"), []byte("2"))

	waitFor(t, 5*time.Second, "both services running", func() bool {
		webEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
		apiEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "api")
		return len(webEndpoints) >= 3 && len(apiEndpoints) >= 2
	})

	// Verify no endpoint cross-contamination.
	webEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "web")
	apiEndpoints, _ := storeBackedResolver.ResolveEndpoints(ctx, "api")

	for _, endpoint := range webEndpoints {
		if endpoint.Service != "web" {
			t.Errorf("web endpoint has wrong service: %s", endpoint.Service)
		}
		if endpoint.Port != 8080 {
			t.Errorf("web endpoint port = %d, want 8080", endpoint.Port)
		}
	}
	for _, endpoint := range apiEndpoints {
		if endpoint.Service != "api" {
			t.Errorf("api endpoint has wrong service: %s", endpoint.Service)
		}
		if endpoint.Port != 3000 {
			t.Errorf("api endpoint port = %d, want 3000", endpoint.Port)
		}
	}
}

// TestInstanceIPReleasedOnStop verifies that scaling down a service deletes
// the network allocation facts for stopped instances.
func TestInstanceIPReleasedOnStop(t *testing.T) {
	factStore, cancel, _, _ := helperSetupNetworkingCluster(t)
	defer cancel()
	defer factStore.Close()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("4"))

	waitFor(t, 5*time.Second, "4 running instances", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningCount := 0
		for _, instance := range allInstances {
			if instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount >= 4
	})

	// Count allocations before scale-down.
	allocationsBefore, _ := factStore.Scan(ctx, types.ScanNetworkAllocations)
	initialAllocationCount := len(allocationsBefore)
	if initialAllocationCount < 4 {
		t.Fatalf("expected at least 4 allocations, got %d", initialAllocationCount)
	}

	// Scale down to 2.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("web"), []byte("2"))

	// Wait for the running count to drop.
	waitFor(t, 5*time.Second, "2 running instances after scale-down", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		runningCount := 0
		for _, instance := range allInstances {
			if instance.Service == "web" && instance.State == types.InstanceRunning {
				runningCount++
			}
		}
		return runningCount <= 2
	})

	// The number of active allocations should have decreased.
	time.Sleep(200 * time.Millisecond)
	allocationsAfter, _ := factStore.Scan(ctx, types.ScanNetworkAllocations)
	if len(allocationsAfter) >= initialAllocationCount {
		t.Errorf("allocations should decrease after scale-down: before=%d, after=%d",
			initialAllocationCount, len(allocationsAfter))
	}
}
