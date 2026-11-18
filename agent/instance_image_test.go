// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/runtime"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// pinnedTestImage is the image the instance controller recorded for the test
// instance; the service's desired image (nginx:1.28) differs from it.
const pinnedTestImage = "nginx:1.27"

// newStoreWithRecordedImage returns a memory store holding the recorded image
// of instanceID, or no recorded image when recordedImage is "".
func newStoreWithRecordedImage(t *testing.T, instanceID string, recordedImage string) store.StateStore {
	t.Helper()
	factStore := store.NewMemoryStore()
	t.Cleanup(func() { factStore.Close() })
	if recordedImage != "" {
		if _, putError := factStore.Put(context.Background(), types.KeyDerivedInstanceImage(instanceID), []byte(recordedImage)); putError != nil {
			t.Fatal(putError)
		}
	}
	return factStore
}

// TestAgentRestartsInstanceWithItsOwnImage: spec instance-restart, "Old
// instance crashes during a rollout": the in-place restart uses the recorded
// image, not the service's newer desired image.
func TestAgentRestartsInstanceWithItsOwnImage(t *testing.T) {
	crashRuntime := newCrashingRuntime()
	factStore := newStoreWithRecordedImage(t, "own1", pinnedTestImage)
	_, _, ctx := startCrashLoopTestAgentOnStore(t, factStore, "own1", crashLoopTestBaseDelay, crashRuntime)

	waitFor(t, crashLoopTestTimeout, "instance restarted in place with its recorded image", func() bool {
		return len(crashRuntime.startsOf("own1")) >= 2 &&
			readFactValue(ctx, factStore, types.KeyObservedInstanceRestarts("own1")) != ""
	})
	if observedImage := readFactValue(ctx, factStore, types.KeyObservedInstanceImage("own1")); observedImage != pinnedTestImage {
		t.Errorf("observed image after the in-place restart = %q, want %q", observedImage, pinnedTestImage)
	}
	if crashRuntime.IsImageCached(ctx, "nginx:1.28") {
		t.Errorf("agent pulled the service's desired image nginx:1.28, want only %s", pinnedTestImage)
	}
}

// startImageTestAgent runs an agent with a plain simulator runtime on factStore
// for one "web" instance whose desired service image is nginx:1.28.
func startImageTestAgent(t *testing.T, factStore store.StateStore, instanceID string) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	nodeAgent := New("node-1", factStore, runtime.NewSimulatorRuntime())
	nodeAgent.SetInterval(crashLoopTestInterval)
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	types.WriteInstance(ctx, factStore, types.Instance{ID: instanceID, Service: "web", State: types.InstancePending})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: instanceID, NodeID: "node-1"})
	go nodeAgent.Run(ctx)
	return ctx
}

// TestAgentStartsInstanceWithRecordedImage: spec instance-restart, "New
// instance gets the desired image", plus the fallback for an instance created
// before images were recorded.
func TestAgentStartsInstanceWithRecordedImage(t *testing.T) {
	testCases := []struct {
		name          string // name describes the case.
		recordedImage string // recordedImage is the instance's recorded image; "" for none.
		wantImage     string // wantImage is the image the agent must start.
	}{
		{name: "recorded image", recordedImage: pinnedTestImage, wantImage: pinnedTestImage},
		{name: "no recorded image falls back to the service image", recordedImage: "", wantImage: "nginx:1.28"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			factStore := newStoreWithRecordedImage(t, "img1", testCase.recordedImage)
			ctx := startImageTestAgent(t, factStore, "img1")
			waitFor(t, crashLoopTestTimeout, "instance running with "+testCase.wantImage, func() bool {
				return readFactValue(ctx, factStore, types.KeyObservedInstanceState("img1")) == string(types.InstanceRunning) &&
					readFactValue(ctx, factStore, types.KeyObservedInstanceImage("img1")) == testCase.wantImage
			})
		})
	}
}
