package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func seqIDGen() types.IDFunc {
	idCounter := 0
	return func() string {
		idCounter++
		return fmt.Sprintf("inst-%03d", idCounter)
	}
}

func buildFacts(entries ...struct{ k, v string }) []store.Fact {
	facts := make([]store.Fact, len(entries))
	for index, entry := range entries {
		facts[index] = store.Fact{Key: entry.k, Value: []byte(entry.v)}
	}
	return facts
}

func kv(k, v string) struct{ k, v string } {
	return struct{ k, v string }{k, v}
}

func TestScaleUpFromZero(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "3"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// 3 instances × 3 keys each (marker, service, state)
	if len(changes) != 9 {
		t.Fatalf("expected 9 changes, got %d", len(changes))
	}

	// Verify the three instances were created with sequential IDs.
	for i := 0; i < 3; i++ {
		base := i * 3
		id := fmt.Sprintf("inst-%03d", i+1)

		if changes[base].Key != types.KeyObservedInstance(id) {
			t.Errorf("change %d: got key %s, want %s", base, changes[base].Key, types.KeyObservedInstance(id))
		}
		if changes[base+1].Key != types.KeyObservedInstanceService(id) {
			t.Errorf("change %d: got key %s, want %s", base+1, changes[base+1].Key, types.KeyObservedInstanceService(id))
		}
		if string(changes[base+1].Value) != "web" {
			t.Errorf("change %d: service = %s, want web", base+1, changes[base+1].Value)
		}
		if string(changes[base+2].Value) != "pending" {
			t.Errorf("change %d: state = %s, want pending", base+2, changes[base+2].Value)
		}
	}
}

func TestAlreadySatisfied(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "2"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes when satisfied, got %d", len(changes))
	}
}

func TestScaleUpPartial(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "5"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Need 2 more instances × 3 keys each
	if len(changes) != 6 {
		t.Fatalf("expected 6 changes (2 new instances), got %d", len(changes))
	}
}

func TestScaleDown(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "1"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// Remove 2 instances (set state to stopped)
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (stop 2 instances), got %d", len(changes))
	}

	for _, ch := range changes {
		if ch.Type != store.OpPut {
			t.Errorf("expected OpPut (state update), got %d", ch.Type)
		}
		if string(ch.Value) != "stopped" {
			t.Errorf("expected stopped, got %s", ch.Value)
		}
	}
}

func TestScaleDownPrefersPending(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "1"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "pending"),
		kv(types.KeyObservedInstanceService("ccc"), "web"),
		kv(types.KeyObservedInstanceState("ccc"), "pending"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(changes))
	}

	// Both stopped instances should be the pending ones (bbb, ccc), not aaa (running).
	stoppedKeys := make(map[string]bool)
	for _, ch := range changes {
		stoppedKeys[ch.Key] = true
	}
	if stoppedKeys[types.KeyObservedInstanceState("aaa")] {
		t.Error("should not stop running instance aaa when pending instances exist")
	}
	if !stoppedKeys[types.KeyObservedInstanceState("bbb")] {
		t.Error("should stop pending instance bbb")
	}
	if !stoppedKeys[types.KeyObservedInstanceState("ccc")] {
		t.Error("should stop pending instance ccc")
	}
}

func TestStoppedInstancesNotCounted(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "2"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "stopped"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// bbb is stopped so not counted → actual=1, desired=2 → create 1
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes (1 new instance), got %d", len(changes))
	}
}

func TestMultipleServices(t *testing.T) {
	instanceController := NewInstanceController()
	instanceController.NewID = seqIDGen()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "2"),
		kv(types.KeyEffectiveServiceInstances("api"), "1"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	// web: need 1 more (3 keys), api: need 1 (3 keys) = 6 total
	if len(changes) != 6 {
		t.Fatalf("expected 6 changes (2 new instances across 2 services), got %d", len(changes))
	}

	services := make(map[string]int)
	for _, ch := range changes {
		if string(ch.Value) == "web" || string(ch.Value) == "api" {
			services[string(ch.Value)]++
		}
	}
	if services["web"] != 1 {
		t.Errorf("expected 1 new web instance, got %d", services["web"])
	}
	if services["api"] != 1 {
		t.Errorf("expected 1 new api instance, got %d", services["api"])
	}
}

func TestDesiredZeroScalesDownAll(t *testing.T) {
	instanceController := NewInstanceController()

	facts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "0"),
		kv(types.KeyObservedInstanceService("aaa"), "web"),
		kv(types.KeyObservedInstanceState("aaa"), "running"),
		kv(types.KeyObservedInstanceService("bbb"), "web"),
		kv(types.KeyObservedInstanceState("bbb"), "running"),
	)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (stop all), got %d", len(changes))
	}
	for _, ch := range changes {
		if string(ch.Value) != "stopped" {
			t.Errorf("expected stopped, got %s", ch.Value)
		}
	}
}

func TestControllerInterface(t *testing.T) {
	instanceController := NewInstanceController()

	var _ Controller = instanceController

	if instanceController.Name() != "instance" {
		t.Fatalf("name: got %s, want instance", instanceController.Name())
	}
	if len(instanceController.Watch()) != 2 {
		t.Fatalf("expected 2 watch prefixes, got %d", len(instanceController.Watch()))
	}
}
