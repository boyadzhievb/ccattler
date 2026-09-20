package scheduler

import (
	"context"
	"fmt"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func buildSchedulerBenchFacts(nodeCount int, pendingInstances int) []store.Fact {
	var facts []store.Fact
	for index := 0; index < nodeCount; index++ {
		nodeID := fmt.Sprintf("node-%d", index)
		facts = append(facts,
			store.Fact{Key: types.KeyObservedNodeState(nodeID), Value: []byte("alive")},
			store.Fact{Key: types.KeyObservedNodeCapacityCPU(nodeID), Value: []byte("8000")},
			store.Fact{Key: types.KeyObservedNodeCapacityMemory(nodeID), Value: []byte("17179869184")},
			store.Fact{Key: types.KeyObservedNodeAvailableCPU(nodeID), Value: []byte("6000")},
			store.Fact{Key: types.KeyObservedNodeAvailableMemory(nodeID), Value: []byte("12884901888")},
		)
	}
	for index := 0; index < pendingInstances; index++ {
		instanceID := fmt.Sprintf("inst-%d", index)
		facts = append(facts,
			store.Fact{Key: types.KeyObservedInstanceService(instanceID), Value: []byte("bench-svc")},
			store.Fact{Key: types.KeyObservedInstanceState(instanceID), Value: []byte("pending")},
		)
	}
	facts = append(facts,
		store.Fact{Key: types.KeyDesiredServiceResourcesCPU("bench-svc"), Value: []byte("500")},
		store.Fact{Key: types.KeyDesiredServiceResourcesMemory("bench-svc"), Value: []byte("536870912")},
	)
	return facts
}

func BenchmarkSchedule10Nodes10Instances(b *testing.B) {
	placementScheduler := NewScheduler()
	facts := buildSchedulerBenchFacts(10, 10)
	benchContext := context.Background()

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		placementScheduler.Reconcile(benchContext, facts)
	}
}

func BenchmarkSchedule100Nodes100Instances(b *testing.B) {
	placementScheduler := NewScheduler()
	facts := buildSchedulerBenchFacts(100, 100)
	benchContext := context.Background()

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		placementScheduler.Reconcile(benchContext, facts)
	}
}

func BenchmarkSchedule100Nodes1000Instances(b *testing.B) {
	placementScheduler := NewScheduler()
	facts := buildSchedulerBenchFacts(100, 1000)
	benchContext := context.Background()

	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		placementScheduler.Reconcile(benchContext, facts)
	}
}
