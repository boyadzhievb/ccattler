package loadtest

import (
	"context"
	"fmt"
	"testing"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// buildClusterFacts creates a sorted fact slice simulating a cluster with the
// given number of nodes and running instances spread across services. Each
// node gets capacity and state facts; each instance gets service, state,
// image, and placement facts. This is the input to controller Reconcile calls.
func buildClusterFacts(nodeCount int, serviceCount int, instancesPerService int) []store.Fact {
	var facts []store.Fact

	for nodeIndex := 0; nodeIndex < nodeCount; nodeIndex++ {
		nodeID := fmt.Sprintf("node-%03d", nodeIndex)
		facts = append(facts,
			store.Fact{Key: types.KeyObservedNode(nodeID), Value: []byte("")},
			store.Fact{Key: types.KeyObservedNodeState(nodeID), Value: []byte("alive")},
			store.Fact{Key: types.KeyObservedNodeCapacityCPU(nodeID), Value: []byte("8000")},
			store.Fact{Key: types.KeyObservedNodeCapacityMemory(nodeID), Value: []byte("17179869184")},
			store.Fact{Key: types.KeyObservedNodeAvailableCPU(nodeID), Value: []byte("6000")},
			store.Fact{Key: types.KeyObservedNodeAvailableMemory(nodeID), Value: []byte("12884901888")},
		)
	}

	instanceIndex := 0
	for serviceIndex := 0; serviceIndex < serviceCount; serviceIndex++ {
		serviceName := fmt.Sprintf("svc-%02d", serviceIndex)
		facts = append(facts,
			store.Fact{Key: types.KeyEffectiveServiceInstances(serviceName), Value: []byte(fmt.Sprintf("%d", instancesPerService))},
			store.Fact{Key: types.KeyDesiredServiceImage(serviceName), Value: []byte(fmt.Sprintf("app:v%d", serviceIndex))},
		)

		for instanceOffset := 0; instanceOffset < instancesPerService; instanceOffset++ {
			instanceID := fmt.Sprintf("inst-%05d", instanceIndex)
			nodeID := fmt.Sprintf("node-%03d", instanceIndex%nodeCount)

			facts = append(facts,
				store.Fact{Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
				store.Fact{Key: types.KeyObservedInstanceService(instanceID), Value: []byte(serviceName)},
				store.Fact{Key: types.KeyObservedInstanceState(instanceID), Value: []byte("running")},
				store.Fact{Key: types.KeyObservedInstanceImage(instanceID), Value: []byte(fmt.Sprintf("app:v%d", serviceIndex))},
				store.Fact{Key: types.KeyPlacementInstance(instanceID), Value: []byte(nodeID)},
			)
			instanceIndex++
		}
	}

	store.SortFacts(facts)
	return facts
}

// buildPendingSchedulingFacts creates facts for a scheduling benchmark:
// nodeCount alive nodes and pendingCount unplaced pending instances.
func buildPendingSchedulingFacts(nodeCount int, pendingCount int) []store.Fact {
	var facts []store.Fact

	for nodeIndex := 0; nodeIndex < nodeCount; nodeIndex++ {
		nodeID := fmt.Sprintf("node-%03d", nodeIndex)
		facts = append(facts,
			store.Fact{Key: types.KeyObservedNode(nodeID), Value: []byte("")},
			store.Fact{Key: types.KeyObservedNodeState(nodeID), Value: []byte("alive")},
			store.Fact{Key: types.KeyObservedNodeCapacityCPU(nodeID), Value: []byte("8000")},
			store.Fact{Key: types.KeyObservedNodeCapacityMemory(nodeID), Value: []byte("17179869184")},
			store.Fact{Key: types.KeyObservedNodeAvailableCPU(nodeID), Value: []byte("6000")},
			store.Fact{Key: types.KeyObservedNodeAvailableMemory(nodeID), Value: []byte("12884901888")},
		)
	}

	for instanceIndex := 0; instanceIndex < pendingCount; instanceIndex++ {
		instanceID := fmt.Sprintf("inst-%05d", instanceIndex)
		facts = append(facts,
			store.Fact{Key: types.KeyObservedInstance(instanceID), Value: []byte("")},
			store.Fact{Key: types.KeyObservedInstanceService(instanceID), Value: []byte("bench-svc")},
			store.Fact{Key: types.KeyObservedInstanceState(instanceID), Value: []byte("pending")},
		)
	}

	facts = append(facts,
		store.Fact{Key: types.KeyDesiredServiceImage("bench-svc"), Value: []byte("app:latest")},
		store.Fact{Key: types.KeyEffectiveServiceInstances("bench-svc"), Value: []byte(fmt.Sprintf("%d", pendingCount))},
	)

	store.SortFacts(facts)
	return facts
}

// BenchmarkScheduler50Nodes1000Instances measures scheduler placement throughput
// for 1000 pending instances across 50 nodes.
func BenchmarkScheduler50Nodes1000Instances(b *testing.B) {
	facts := buildPendingSchedulingFacts(50, 1000)
	schedulerController := scheduler.NewScheduler()

	b.ResetTimer()
	for range b.N {
		changes, reconcileError := schedulerController.Reconcile(context.Background(), facts)
		if reconcileError != nil {
			b.Fatal(reconcileError)
		}
		if len(changes) == 0 {
			b.Fatal("expected placement changes")
		}
	}
}

// BenchmarkInstanceController50Nodes1000Running measures instance controller
// cycle time when all 1000 instances are already running (steady state).
func BenchmarkInstanceController50Nodes1000Running(b *testing.B) {
	facts := buildClusterFacts(50, 10, 100)
	instanceController := controllers.NewInstanceController()

	b.ResetTimer()
	for range b.N {
		changes, reconcileError := instanceController.Reconcile(context.Background(), facts)
		if reconcileError != nil {
			b.Fatal(reconcileError)
		}
		if len(changes) != 0 {
			b.Fatalf("expected no changes in steady state, got %d", len(changes))
		}
	}
}

// BenchmarkEndpointController50Nodes1000Running measures endpoint controller
// cycle time at 1000-instance scale.
func BenchmarkEndpointController50Nodes1000Running(b *testing.B) {
	facts := buildClusterFacts(50, 10, 100)
	endpointController := controllers.NewEndpointController()

	b.ResetTimer()
	for range b.N {
		_, reconcileError := endpointController.Reconcile(context.Background(), facts)
		if reconcileError != nil {
			b.Fatal(reconcileError)
		}
	}
}

// BenchmarkFailureController50Nodes1000Running measures failure controller
// cycle time at 1000-instance scale.
func BenchmarkFailureController50Nodes1000Running(b *testing.B) {
	facts := buildClusterFacts(50, 10, 100)
	failureController := controllers.NewFailureController()

	b.ResetTimer()
	for range b.N {
		_, reconcileError := failureController.Reconcile(context.Background(), facts)
		if reconcileError != nil {
			b.Fatal(reconcileError)
		}
	}
}

// BenchmarkStoreScan50KFacts measures scan throughput with a realistic
// fact count for a 50-node/1000-instance cluster.
func BenchmarkStoreScan50KFacts(b *testing.B) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	facts := buildClusterFacts(50, 10, 100)
	for _, fact := range facts {
		memoryStore.Put(ctx, fact.Key, fact.Value)
	}

	b.ResetTimer()
	for range b.N {
		results, scanError := memoryStore.Scan(ctx, types.ScanObservedInstances)
		if scanError != nil {
			b.Fatal(scanError)
		}
		if len(results) == 0 {
			b.Fatal("expected facts from scan")
		}
	}
}

// BenchmarkStoreTransaction1000Facts measures transaction throughput with
// the typical compare-count used by the controller runner (128-op cap).
func BenchmarkStoreTransaction1000Facts(b *testing.B) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	facts := buildClusterFacts(50, 10, 100)
	for _, fact := range facts {
		memoryStore.Put(ctx, fact.Key, fact.Value)
	}

	b.ResetTimer()
	for range b.N {
		compares := []store.Compare{
			{Key: types.KeyObservedNodeState("node-000"), Revision: 1},
		}
		operations := []store.Op{
			{Type: store.OpPut, Key: "benchmark/temp", Value: []byte("value")},
		}
		_, transactionError := memoryStore.Transaction(ctx, compares, operations, nil)
		if transactionError != nil {
			b.Fatal(transactionError)
		}
	}
}
