package main

import (
	"reflect"
	"testing"
)

// TestLogsSelectsCrashLoopingInstances checks that `cca logs <service>` (and
// --previous) finds an instance waiting to restart, whose container and logs
// are still on the node, but not a stopped one (M234 testbed finding).
func TestLogsSelectsCrashLoopingInstances(t *testing.T) {
	observedFacts := []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}{
		{Key: "observed/instance/web-a/service", Value: "web"},
		{Key: "observed/instance/web-a/state", Value: "running"},
		{Key: "observed/instance/web-b/service", Value: "web"},
		{Key: "observed/instance/web-b/state", Value: "crash-loop-backoff"},
		{Key: "observed/instance/web-c/service", Value: "web"},
		{Key: "observed/instance/web-c/state", Value: "stopped"},
	}
	selectedInstanceIDs := findInstanceIDsForService(observedFacts, "web")
	if want := []string{"web-a", "web-b"}; !reflect.DeepEqual(selectedInstanceIDs, want) {
		t.Errorf("selected %v, want %v", selectedInstanceIDs, want)
	}
}
