// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/scheduler"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestStatefulServiceThreeInstancesOrderedStartup verifies the full stateful
// lifecycle: ordinal IDs, ordered startup (one at a time), per-instance DNS,
// and per-ordinal volumes.
func TestStatefulServiceThreeInstancesOrderedStartup(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	controllerList := []controllers.Controller{
		controllers.NewInstanceController(),
		scheduler.NewScheduler(),
		controllers.NewEndpointController(),
		controllers.NewFailureController(),
		controllers.NewIntentResolverController(),
		controllers.NewStatefulDNSController(),
		controllers.NewStatefulVolumeController(),
	}

	controllerRunner := controllers.NewRunner(factStore, controllerList...)
	controllerRunner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	types.WriteNode(ctx, factStore, types.Node{
		ID: "node-1", State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	go controllerRunner.Run(ctx)

	// Deploy a stateful service with 3 instances.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("3"))
	factStore.Put(ctx, types.KeyEffectiveServiceStateful("postgres"), []byte("true"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("postgres"), []byte("3"))

	// Set up a volume for per-ordinal volume tests.
	factStore.Put(ctx, types.KeyDesiredVolume("pgdata"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredVolumeSize("pgdata"), []byte("100Gi"))
	factStore.Put(ctx, types.KeyDesiredVolumePersistent("pgdata"), []byte("true"))
	factStore.Put(ctx, types.KeyDesiredServiceVolume("postgres", "pgdata"), []byte("/var/lib/postgresql/data"))

	// Step 1: Wait for postgres-0 to be created (pending).
	waitFor(t, 3*time.Second, "postgres-0 created", func() bool {
		fact, _ := factStore.Get(ctx, types.KeyObservedInstanceState("postgres-0"))
		return fact != nil && string(fact.Value) == string(types.InstancePending)
	})

	// Verify postgres-1 is NOT created yet (ordered startup).
	fact1, _ := factStore.Get(ctx, types.KeyObservedInstanceState("postgres-1"))
	if fact1 != nil {
		t.Fatalf("postgres-1 should not exist before postgres-0 is running, got state=%q", fact1.Value)
	}

	// Step 2: Simulate postgres-0 becoming running.
	factStore.Put(ctx, types.KeyObservedInstanceState("postgres-0"), []byte("running"))
	factStore.Put(ctx, types.KeyObservedInstanceIP("postgres-0"), []byte("10.0.1.4"))

	// Wait for postgres-1 to be created.
	waitFor(t, 3*time.Second, "postgres-1 created", func() bool {
		fact, _ := factStore.Get(ctx, types.KeyObservedInstanceState("postgres-1"))
		return fact != nil
	})

	// Step 3: Simulate postgres-1 becoming running.
	factStore.Put(ctx, types.KeyObservedInstanceState("postgres-1"), []byte("running"))
	factStore.Put(ctx, types.KeyObservedInstanceIP("postgres-1"), []byte("10.0.2.8"))

	// Wait for postgres-2 to be created.
	waitFor(t, 3*time.Second, "postgres-2 created", func() bool {
		fact, _ := factStore.Get(ctx, types.KeyObservedInstanceState("postgres-2"))
		return fact != nil
	})

	// Step 4: Simulate postgres-2 becoming running with IP for DNS test.
	factStore.Put(ctx, types.KeyObservedInstanceState("postgres-2"), []byte("running"))
	factStore.Put(ctx, types.KeyObservedInstanceIP("postgres-2"), []byte("10.0.3.2"))

	// Step 5: Verify per-instance DNS entries are created.
	waitFor(t, 3*time.Second, "per-instance DNS entries", func() bool {
		dns0, _ := factStore.Get(ctx, types.KeyNetworkDNSInstance("postgres-0"))
		dns1, _ := factStore.Get(ctx, types.KeyNetworkDNSInstance("postgres-1"))
		dns2, _ := factStore.Get(ctx, types.KeyNetworkDNSInstance("postgres-2"))
		return dns0 != nil && dns1 != nil && dns2 != nil
	})

	dns0Fact, _ := factStore.Get(ctx, types.KeyNetworkDNSInstance("postgres-0"))
	expectedDNS0 := "postgres-0.ccattler.local=10.0.1.4"
	if string(dns0Fact.Value) != expectedDNS0 {
		t.Errorf("DNS for postgres-0: got %q, want %q", dns0Fact.Value, expectedDNS0)
	}

	dns2Fact, _ := factStore.Get(ctx, types.KeyNetworkDNSInstance("postgres-2"))
	expectedDNS2 := "postgres-2.ccattler.local=10.0.3.2"
	if string(dns2Fact.Value) != expectedDNS2 {
		t.Errorf("DNS for postgres-2: got %q, want %q", dns2Fact.Value, expectedDNS2)
	}

	// Step 6: Verify per-ordinal volumes were created.
	waitFor(t, 3*time.Second, "per-ordinal volumes with size and persistent", func() bool {
		vol0, _ := factStore.Get(ctx, types.KeyDesiredVolume("postgres-0-pgdata"))
		vol1, _ := factStore.Get(ctx, types.KeyDesiredVolume("postgres-1-pgdata"))
		vol2, _ := factStore.Get(ctx, types.KeyDesiredVolume("postgres-2-pgdata"))
		size0, _ := factStore.Get(ctx, types.KeyDesiredVolumeSize("postgres-0-pgdata"))
		persist0, _ := factStore.Get(ctx, types.KeyDesiredVolumePersistent("postgres-0-pgdata"))
		return vol0 != nil && vol1 != nil && vol2 != nil &&
			size0 != nil && string(size0.Value) == "100Gi" &&
			persist0 != nil && string(persist0.Value) == "true"
	})
}

// TestStatefulServiceScaleDown verifies that scaling down a stateful service
// removes the highest ordinals first.
func TestStatefulServiceScaleDown(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	controllerList := []controllers.Controller{
		controllers.NewInstanceController(),
		controllers.NewIntentResolverController(),
	}

	controllerRunner := controllers.NewRunner(factStore, controllerList...)
	controllerRunner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Pre-populate 3 running stateful instances.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("3"))
	factStore.Put(ctx, types.KeyEffectiveServiceStateful("postgres"), []byte("true"))
	for ordinal := 0; ordinal < 3; ordinal++ {
		instanceID := "postgres-" + itoa(ordinal)
		factStore.Put(ctx, types.KeyObservedInstance(instanceID), []byte(""))
		factStore.Put(ctx, types.KeyObservedInstanceService(instanceID), []byte("postgres"))
		factStore.Put(ctx, types.KeyObservedInstanceState(instanceID), []byte("running"))
		factStore.Put(ctx, types.KeyObservedInstanceOrdinal(instanceID), []byte(itoa(ordinal)))
	}

	go controllerRunner.Run(ctx)

	// Scale down to 1.
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("postgres"), []byte("1"))

	// Wait for ordinals 2 and 1 to be stopped.
	waitFor(t, 3*time.Second, "ordinal 2 stopped", func() bool {
		fact, _ := factStore.Get(ctx, types.KeyObservedInstanceState("postgres-2"))
		return fact != nil && string(fact.Value) == string(types.InstanceStopped)
	})
	waitFor(t, 3*time.Second, "ordinal 1 stopped", func() bool {
		fact, _ := factStore.Get(ctx, types.KeyObservedInstanceState("postgres-1"))
		return fact != nil && string(fact.Value) == string(types.InstanceStopped)
	})

	// Verify ordinal 0 is still running.
	state0Fact, _ := factStore.Get(ctx, types.KeyObservedInstanceState("postgres-0"))
	if state0Fact == nil || string(state0Fact.Value) != string(types.InstanceRunning) {
		t.Errorf("postgres-0 should still be running, got %v", state0Fact)
	}
}

// itoa is a simple int-to-string helper for test readability.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	result := make([]byte, 0, 4)
	for value > 0 {
		result = append([]byte{byte('0' + value%10)}, result...)
		value /= 10
	}
	return string(result)
}
