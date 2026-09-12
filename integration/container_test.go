package integration

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
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

// testNetworkName is used by container integration tests to avoid
// colliding with the "cca-net" network used by the live run-container command.
const testNetworkName = "cca-test-net"

// skipIfNerdctlUnavailable skips the test when the nerdctl CLI is not installed
// or the container runtime is not responding.
func skipIfNerdctlUnavailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("nerdctl"); err != nil {
		t.Skip("nerdctl not found in PATH, skipping container integration test")
	}
	nerdctlInfoCommand := exec.Command("nerdctl", "info")
	if err := nerdctlInfoCommand.Run(); err != nil {
		t.Skip("nerdctl runtime not running, skipping container integration test")
	}
}

// TestContainerGetsIPAndServesConfigFile is a full end-to-end integration test
// that deploys an nginx container through the CCattler control plane, verifies
// it receives an IP from the cluster network, mounts a custom index.html via
// config file materialization, and confirms the page content via HTTP from a
// curl container on the same network. Requires nerdctl to be installed and
// running; skipped automatically when nerdctl is unavailable.
func TestContainerGetsIPAndServesConfigFile(t *testing.T) {
	skipIfNerdctlUnavailable(t)

	factStore := store.NewMemoryStore()
	defer factStore.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register a single node with sufficient capacity.
	localNodeID := "test-node"
	types.WriteNode(ctx, factStore, types.Node{
		ID: localNodeID, State: types.NodeAlive,
		CapacityCPU: 4000, CapacityMemory: 8192,
		AvailableCPU: 4000, AvailableMemory: 8192,
	})

	// Start controllers.
	instanceController := controllers.NewInstanceController()
	schedulerController := scheduler.NewScheduler()
	endpointController := controllers.NewEndpointController()
	failureController := controllers.NewFailureController()
	networkController := controllers.NewNetworkController()

	controllerRunner := controllers.NewRunner(factStore, instanceController,
		schedulerController, endpointController, failureController, networkController)
	controllerRunner.SetDebounce(10 * time.Millisecond)
	go controllerRunner.Run(ctx)

	// Create ContainerRuntime with a dedicated test nerdctl network.
	containerRuntime := runtime.NewContainerRuntime()
	containerRuntime.SetNetwork(testNetworkName, network.DefaultClusterCIDR)
	defer containerRuntime.StopAll(context.Background())

	// Create a network provider for IP allocation.
	simulatorNetworkProvider := network.NewSimulatorNetworkProvider()

	// Create and start the node agent with ContainerRuntime.
	nodeAgent := agent.New(localNodeID, factStore, containerRuntime)
	nodeAgent.SetNetworkProvider(simulatorNetworkProvider)
	nodeAgent.SetInterval(100 * time.Millisecond)
	go nodeAgent.Run(ctx)

	// Define the custom HTML content for the nginx index page.
	customPageContent := "<h1>CCattler Config File Test</h1>"

	// Write desired state: nginx with exposed port and custom index.html.
	factStore.Put(ctx, types.KeyDesiredServiceImage("nginx-test"), []byte("nginx:1.28"))
	factStore.Put(ctx, types.KeyDesiredServiceExpose("nginx-test", 80), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceConfigFile("nginx-test",
		"/usr/share/nginx/html/index.html"), []byte(customPageContent))
	factStore.Put(ctx, types.KeyEffectiveServiceInstances("nginx-test"), []byte("1"))

	// Wait for the instance to become running with an allocated IP.
	var allocatedInstanceIP string
	waitFor(t, 30*time.Second, "nginx instance running with allocated IP", func() bool {
		allInstances, _ := types.ListInstances(ctx, factStore)
		for _, instance := range allInstances {
			if instance.Service == "nginx-test" && instance.State == types.InstanceRunning &&
				instance.IP != "" && instance.IP != "127.0.0.1" {
				allocatedInstanceIP = instance.IP
				return true
			}
		}
		return false
	})

	t.Logf("nginx container running at IP %s", allocatedInstanceIP)

	// Verify the IP is from the expected cluster subnet.
	if !strings.HasPrefix(allocatedInstanceIP, "10.100.") {
		t.Errorf("expected IP in 10.100.x.y subnet, got %s", allocatedInstanceIP)
	}

	// Give nginx a moment to finish binding port 80 inside the container.
	time.Sleep(2 * time.Second)

	// Verify the custom page content by running a curl container on the same
	// nerdctl network. This works on all platforms including macOS where
	// container IPs on bridge networks are not reachable from the host.
	curlTargetURL := fmt.Sprintf("http://%s:80/", allocatedInstanceIP)
	curlCommand := exec.CommandContext(ctx, "nerdctl", "run", "--rm",
		"--network", testNetworkName,
		"curlimages/curl:latest",
		"-s", "-f", "--max-time", "5",
		curlTargetURL)
	var curlStdout bytes.Buffer
	var curlStderr bytes.Buffer
	curlCommand.Stdout = &curlStdout
	curlCommand.Stderr = &curlStderr

	if err := curlCommand.Run(); err != nil {
		t.Fatalf("curl to nginx at %s failed: %v\nstderr: %s", curlTargetURL, err, curlStderr.String())
	}

	responseBody := curlStdout.String()
	if !strings.Contains(responseBody, "CCattler Config File Test") {
		t.Errorf("expected custom index.html content in response, got:\n%s", responseBody)
	}

	t.Logf("nginx served custom config file content at %s:80", allocatedInstanceIP)
}
