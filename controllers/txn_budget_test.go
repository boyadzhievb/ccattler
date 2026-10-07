package controllers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestInstanceControllerMaxCreationsPerCycle verifies that the InstanceController
// produces at most MaxCreationsPerCycle × 3 changes.
func TestInstanceControllerMaxCreationsPerCycle(t *testing.T) {
	instanceController := controllers.NewInstanceController()
	// Default is 20
	t.Logf("MaxCreationsPerCycle: %d", instanceController.MaxCreationsPerCycle)

	// 10 services × 100 desired, 0 existing
	var facts []store.Fact
	for i := 0; i < 10; i++ {
		svc := fmt.Sprintf("svc-%02d", i)
		facts = append(facts, store.Fact{
			Key:   types.KeyEffectiveServiceInstances(svc),
			Value: []byte("100"),
		})
	}
	store.SortFacts(facts)

	changes, err := instanceController.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("changes produced: %d (expected max %d)", len(changes), instanceController.MaxCreationsPerCycle*3)
	if len(changes) > instanceController.MaxCreationsPerCycle*3 {
		t.Fatalf("InstanceController produced %d changes, expected at most %d",
			len(changes), instanceController.MaxCreationsPerCycle*3)
	}
}
