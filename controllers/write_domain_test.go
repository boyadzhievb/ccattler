package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// TestWriteDomainEnforcement verifies that the runner drops changes whose keys
// fall outside the controller's declared output prefixes.
func TestWriteDomainEnforcement(t *testing.T) {
	changes := []Change{
		{Type: store.OpPut, Key: "observed/instance/i1/state", Value: []byte("running")},
		{Type: store.OpPut, Key: "network/vip/service/web", Value: []byte("10.0.0.1")},
	}

	validated, err := enforceWriteDomain("instance", changes)
	if err != nil {
		t.Fatal(err)
	}

	if len(validated) != 1 {
		t.Fatalf("expected 1 validated change, got %d", len(validated))
	}
	if validated[0].Key != "observed/instance/i1/state" {
		t.Errorf("expected observed/instance/i1/state, got %s", validated[0].Key)
	}
}

// TestWriteDomainAllowsAllDeclaredPrefixes verifies that every declared prefix
// for a controller is accepted by enforceWriteDomain.
func TestWriteDomainAllowsAllDeclaredPrefixes(t *testing.T) {
	for controllerName, prefixes := range controllerOutputPrefixes() {
		for _, prefix := range prefixes {
			testKey := prefix + "test/key"
			changes := []Change{
				{Type: store.OpPut, Key: testKey, Value: []byte("val")},
			}
			validated, err := enforceWriteDomain(controllerName, changes)
			if err != nil {
				t.Fatalf("%s: %v", controllerName, err)
			}
			if len(validated) != 1 {
				t.Errorf("%s: prefix %s should be allowed but was rejected", controllerName, prefix)
			}
		}
	}
}

// TestWriteDomainRejectsWrongPrefix verifies that a change to a prefix not
// declared for the controller is dropped.
func TestWriteDomainRejectsWrongPrefix(t *testing.T) {
	testCases := []struct {
		controllerName string
		key            string
	}{
		{"instance", "desired/service/web/image"},
		{"endpoint", "observed/instance/i1/state"},
		{"network", "endpoint/service/web/i1/80"},
		{"autoscale", "effective/service/web/instances"},
		{"init", "observed/instance/i1/state"},
	}

	for _, testCase := range testCases {
		changes := []Change{
			{Type: store.OpPut, Key: testCase.key, Value: []byte("val")},
		}
		validated, err := enforceWriteDomain(testCase.controllerName, changes)
		if err != nil {
			t.Fatalf("%s: %v", testCase.controllerName, err)
		}
		if len(validated) != 0 {
			t.Errorf("%s: key %s should be rejected but was allowed", testCase.controllerName, testCase.key)
		}
	}
}

// TestWriteDomainPassesThroughUnknownControllers verifies that controllers not
// present in the output prefixes map (e.g. custom SDK controllers) are not
// blocked — all their changes pass through unmodified.
func TestWriteDomainPassesThroughUnknownControllers(t *testing.T) {
	changes := []Change{
		{Type: store.OpPut, Key: "any/prefix/key", Value: []byte("val")},
	}
	validated, err := enforceWriteDomain("custom-user-controller", changes)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated) != 1 {
		t.Error("unknown controller changes should pass through")
	}
}

// TestWriteDomainMultiplePrefixes verifies that a controller with multiple
// declared output prefixes can write to any of them.
func TestWriteDomainMultiplePrefixes(t *testing.T) {
	changes := []Change{
		{Type: store.OpPut, Key: "observed/instance/i1/state", Value: []byte("stopped")},
		{Type: store.OpPut, Key: "derived/service/web/rollout/state", Value: []byte("rolling")},
		{Type: store.OpPut, Key: "desired/service/web/image", Value: []byte("nginx:1.27")},
		{Type: store.OpPut, Key: "network/vip/service/web", Value: []byte("10.0.0.1")},
	}

	validated, err := enforceWriteDomain("rollout", changes)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated) != 3 {
		t.Fatalf("rollout should allow 3 of 4 changes, got %d", len(validated))
	}
}

// TestEveryControllerOutputPrefixesComplete verifies that every built-in
// controller's actual Reconcile output keys fall within its declared output
// prefixes. This is a cross-check between the code and the declaration.
func TestEveryControllerOutputPrefixesComplete(t *testing.T) {
	allPrefixes := controllerOutputPrefixes()

	testCases := []struct {
		controllerName     string
		controllerInstance Controller
		inputFacts         []store.Fact
	}{
		{
			controllerName:     "instance",
			controllerInstance: newDeterministicInstanceController(),
			inputFacts: buildFacts(
				kv(types.KeyEffectiveServiceInstances("web"), "2"),
			),
		},
		{
			controllerName:     "endpoint",
			controllerInstance: NewEndpointController(),
			inputFacts: buildFacts(
				kv(types.KeyObservedInstanceState("i1"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i1"), "web"),
				kv(types.KeyObservedInstanceIP("i1"), "10.0.1.5"),
				kv(types.KeyDesiredServiceExpose("web", 8080), "8080"),
			),
		},
		{
			controllerName:     "init",
			controllerInstance: NewInitController(),
			inputFacts: buildFacts(
				kv("desired/service/web/init/0/exec", "migrate"),
				kv("observed/instance/i1/init/step/0/state", "succeeded"),
				kv(types.KeyObservedInstanceService("i1"), "web"),
			),
		},
		{
			controllerName:     "intent-resolver",
			controllerInstance: NewIntentResolverController(),
			inputFacts: buildFacts(
				kv(types.KeyIntentUserServiceInstances("web"), "3"),
			),
		},
		{
			controllerName: "autoscale",
			controllerInstance: func() Controller {
				autoController := NewAutoscaleController()
				autoController.timeNow = func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }
				return autoController
			}(),
			inputFacts: buildFacts(
				kv("desired/service/web/scale/horizontal/target/cpu", "60"),
				kv("desired/service/web/scale/horizontal/min", "1"),
				kv("desired/service/web/scale/horizontal/max", "10"),
				kv("observed/metric/service/web/cpu", "90"),
				kv(types.KeyObservedInstanceState("i1"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i1"), "web"),
			),
		},
		{
			controllerName:     "network",
			controllerInstance: NewNetworkController(),
			inputFacts: buildFacts(
				kv("endpoint/service/web/i1/8080", "10.0.1.5:8080"),
				kv(types.KeyDesiredServiceExpose("web", 8080), "8080"),
			),
		},
		{
			controllerName:     "warm-zero",
			controllerInstance: NewWarmZeroController(),
			inputFacts: buildFacts(
				kv("desired/service/web/scale/horizontal/idle_timeout", "300"),
				kv("endpoint/service/web/i1/8080", "10.0.1.5:8080"),
			),
		},
		{
			controllerName: "failure",
			controllerInstance: func() Controller {
				failureController := NewFailureController()
				failureController.NewID = seqIDGen()
				failureController.NowFunc = func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }
				return failureController
			}(),
			inputFacts: buildFacts(
				kv(types.KeyObservedInstanceState("i1"), string(types.InstanceFailed)),
				kv(types.KeyObservedInstanceService("i1"), "web"),
			),
		},
		{
			controllerName:     "rollout",
			controllerInstance: NewRolloutController(),
			inputFacts: buildFacts(
				kv(types.KeyDesiredServiceImage("web"), "nginx:1.28"),
				kv(types.KeyObservedInstanceState("i1"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i1"), "web"),
				kv(types.KeyObservedInstanceImage("i1"), "nginx:1.27"),
			),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.controllerName, func(t *testing.T) {
			changes, err := testCase.controllerInstance.Reconcile(context.Background(), testCase.inputFacts)
			if err != nil {
				t.Fatal(err)
			}
			if len(changes) == 0 {
				t.Skip("no changes produced (need richer input facts)")
			}

			allowedPrefixes := allPrefixes[testCase.controllerName]
			if len(allowedPrefixes) == 0 {
				t.Fatalf("controller %s has no declared output prefixes", testCase.controllerName)
			}

			for _, change := range changes {
				if !isKeyWithinWriteDomain(change.Key, allowedPrefixes) {
					t.Errorf("change key %q is outside declared output prefixes %v", change.Key, allowedPrefixes)
				}
			}
		})
	}
}

// TestDeterministicPlanVerification verifies that every controller is a pure
// function: given the same input facts, Reconcile produces identical changes
// every time. This is the central correctness property of the reconciliation
// protocol — deterministic plans enable safe retry on optimistic concurrency
// conflicts.
func TestDeterministicPlanVerification(t *testing.T) {
	const reconcileIterations = 5

	testCases := []struct {
		controllerName     string
		controllerFactory  func() Controller
		inputFacts         []store.Fact
	}{
		{
			controllerName: "instance",
			controllerFactory: func() Controller {
				return newDeterministicInstanceController()
			},
			inputFacts: buildFacts(
				kv(types.KeyEffectiveServiceInstances("web"), "3"),
				kv(types.KeyObservedInstanceState("existing-1"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("existing-1"), "web"),
			),
		},
		{
			controllerName: "endpoint",
			controllerFactory: func() Controller {
				return NewEndpointController()
			},
			inputFacts: buildFacts(
				kv(types.KeyObservedInstanceState("i1"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i1"), "web"),
				kv(types.KeyObservedInstanceIP("i1"), "10.0.1.5"),
				kv(types.KeyDesiredServiceExpose("web", 8080), "8080"),
				kv(types.KeyObservedInstanceState("i2"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i2"), "web"),
				kv(types.KeyObservedInstanceIP("i2"), "10.0.1.6"),
			),
		},
		{
			controllerName: "failure",
			controllerFactory: func() Controller {
				frozenTime := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
				failureController := NewFailureController()
				failureController.NewID = seqIDGen()
				failureController.NowFunc = func() time.Time { return frozenTime }
				return failureController
			},
			inputFacts: buildFacts(
				kv(types.KeyObservedInstanceState("i1"), string(types.InstanceFailed)),
				kv(types.KeyObservedInstanceService("i1"), "web"),
			),
		},
		{
			controllerName: "init",
			controllerFactory: func() Controller {
				return NewInitController()
			},
			inputFacts: buildFacts(
				kv("desired/service/web/init/0/exec", "migrate"),
				kv("observed/instance/i1/init/step/0/state", "succeeded"),
				kv(types.KeyObservedInstanceService("i1"), "web"),
			),
		},
		{
			controllerName: "intent-resolver",
			controllerFactory: func() Controller {
				return NewIntentResolverController()
			},
			inputFacts: buildFacts(
				kv(types.KeyIntentUserServiceInstances("web"), "5"),
				kv(types.KeyIntentAutoscalerServiceInstances("web"), "8"),
				kv(types.KeyEffectiveServiceInstances("web"), "5"),
			),
		},
		{
			controllerName: "autoscale",
			controllerFactory: func() Controller {
				frozenTime := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
				autoController := NewAutoscaleController()
				autoController.timeNow = func() time.Time { return frozenTime }
				return autoController
			},
			inputFacts: buildFacts(
				kv("desired/service/web/scale/horizontal/target/cpu", "60"),
				kv("desired/service/web/scale/horizontal/min", "2"),
				kv("desired/service/web/scale/horizontal/max", "20"),
				kv("observed/metric/service/web/cpu", "90"),
				kv(types.KeyObservedInstanceState("i1"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i1"), "web"),
				kv(types.KeyObservedInstanceState("i2"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i2"), "web"),
			),
		},
		{
			controllerName: "network",
			controllerFactory: func() Controller {
				return NewNetworkController()
			},
			inputFacts: buildFacts(
				kv("endpoint/service/web/i1/8080", "10.0.1.5:8080"),
				kv("endpoint/service/web/i2/8080", "10.0.1.6:8080"),
				kv(types.KeyDesiredServiceExpose("web", 8080), "8080"),
			),
		},
		{
			controllerName: "rollout",
			controllerFactory: func() Controller {
				return NewRolloutController()
			},
			inputFacts: buildFacts(
				kv(types.KeyDesiredServiceImage("web"), "nginx:1.28"),
				kv(types.KeyObservedInstanceState("i1"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i1"), "web"),
				kv(types.KeyObservedInstanceImage("i1"), "nginx:1.27"),
				kv(types.KeyObservedInstanceState("i2"), string(types.InstanceRunning)),
				kv(types.KeyObservedInstanceService("i2"), "web"),
				kv(types.KeyObservedInstanceImage("i2"), "nginx:1.28"),
			),
		},
		{
			controllerName: "warm-zero",
			controllerFactory: func() Controller {
				return NewWarmZeroController()
			},
			inputFacts: buildFacts(
				kv("desired/service/web/scale/horizontal/idle_timeout", "300"),
				kv("endpoint/service/web/i1/8080", "10.0.1.5:8080"),
			),
		},
		{
			controllerName: "storage",
			controllerFactory: func() Controller {
				return NewStorageController()
			},
			inputFacts: buildFacts(
				kv("desired/volume/dbvol/size", "100Gi"),
				kv("desired/volume/dbvol/persistent", "true"),
			),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.controllerName, func(t *testing.T) {
			var referenceChanges []Change
			for iteration := 0; iteration < reconcileIterations; iteration++ {
				controller := testCase.controllerFactory()
				changes, err := controller.Reconcile(context.Background(), testCase.inputFacts)
				if err != nil {
					t.Fatalf("iteration %d: %v", iteration, err)
				}

				if iteration == 0 {
					referenceChanges = changes
					continue
				}

				if !changesEqual(referenceChanges, changes) {
					t.Fatalf("iteration %d produced different changes than iteration 0\n"+
						"iteration 0: %s\niteration %d: %s",
						iteration, formatChanges(referenceChanges),
						iteration, formatChanges(changes))
				}
			}
		})
	}
}

// TestDeterministicPlanScaleDown verifies determinism during scale-down, where
// the controller must choose which instances to stop. The choice must be
// stable across invocations with the same input.
func TestDeterministicPlanScaleDown(t *testing.T) {
	const reconcileIterations = 10

	inputFacts := buildFacts(
		kv(types.KeyEffectiveServiceInstances("web"), "2"),
		kv(types.KeyObservedInstanceState("i1"), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceService("i1"), "web"),
		kv(types.KeyObservedInstanceState("i2"), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceService("i2"), "web"),
		kv(types.KeyObservedInstanceState("i3"), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceService("i3"), "web"),
		kv(types.KeyObservedInstanceState("i4"), string(types.InstanceRunning)),
		kv(types.KeyObservedInstanceService("i4"), "web"),
	)

	var referenceChanges []Change
	for iteration := 0; iteration < reconcileIterations; iteration++ {
		controller := NewInstanceController()
		changes, err := controller.Reconcile(context.Background(), inputFacts)
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}

		if iteration == 0 {
			referenceChanges = changes
			if len(changes) == 0 {
				t.Fatal("expected scale-down changes, got none")
			}
			continue
		}

		if !changesEqual(referenceChanges, changes) {
			t.Fatalf("scale-down is non-deterministic: iteration %d differs from iteration 0", iteration)
		}
	}
}

// newDeterministicInstanceController creates an InstanceController with a
// deterministic sequential ID generator for reproducible test output.
func newDeterministicInstanceController() *InstanceController {
	controller := NewInstanceController()
	controller.NewID = seqIDGen()
	return controller
}

// changesEqual compares two change slices for exact equality (order, keys,
// values, and operation types).
func changesEqual(expected []Change, actual []Change) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if expected[index].Key != actual[index].Key {
			return false
		}
		if expected[index].Type != actual[index].Type {
			return false
		}
		if string(expected[index].Value) != string(actual[index].Value) {
			return false
		}
	}
	return true
}

// formatChanges produces a human-readable summary of a change slice for
// failure messages.
func formatChanges(changes []Change) string {
	if len(changes) == 0 {
		return "[]"
	}
	parts := make([]string, len(changes))
	for index, change := range changes {
		operation := "PUT"
		if change.Type == store.OpDelete {
			operation = "DEL"
		}
		parts[index] = fmt.Sprintf("%s %s=%q", operation, change.Key, string(change.Value))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n  ")
}
