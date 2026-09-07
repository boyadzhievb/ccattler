package controllers

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// helperApplyChangesToStore applies a list of controller changes to the store,
// simulating what the runner does. Used by network controller tests to verify
// the reconciliation output.
func helperApplyChangesToStore(ctx context.Context, factStore store.StateStore, changes []Change) {
	for _, change := range changes {
		switch change.Type {
		case store.OpPut:
			factStore.Put(ctx, change.Key, change.Value)
		case store.OpDelete:
			factStore.Delete(ctx, change.Key)
		}
	}
}

// helperSetupServiceWithEndpoints writes the desired service config and a set
// of endpoint facts to the store, simulating a service with running instances.
func helperSetupServiceWithEndpoints(ctx context.Context, factStore store.StateStore, serviceName string, port int, endpointCount int) {
	factStore.Put(ctx, types.KeyDesiredServiceExpose(serviceName, port), []byte(""))
	for endpointIndex := 0; endpointIndex < endpointCount; endpointIndex++ {
		instanceID := serviceName + "-" + strconv.Itoa(endpointIndex)
		ipAddress := "10.100.1." + strconv.Itoa(endpointIndex+2)
		factStore.Put(ctx, types.KeyEndpoint(serviceName, instanceID),
			[]byte(ipAddress+":"+strconv.Itoa(port)))
	}
}

// helperReconcileNetworkController runs a single reconciliation cycle of the
// network controller against the current store state and returns the changes.
func helperReconcileNetworkController(ctx context.Context, factStore store.StateStore, networkController *NetworkController) ([]Change, error) {
	var allFacts []store.Fact
	for _, prefix := range networkController.Watch() {
		scannedFacts, _ := factStore.Scan(ctx, prefix)
		allFacts = append(allFacts, scannedFacts...)
	}
	return networkController.Reconcile(ctx, allFacts)
}

// TestNetworkControllerName verifies the controller identifies itself as "network".
func TestNetworkControllerName(t *testing.T) {
	networkController := NewNetworkController()
	if networkController.Name() != "network" {
		t.Errorf("Name() = %s, want network", networkController.Name())
	}
}

// TestNetworkControllerAllocatesVIPForServiceWithEndpoints verifies that a
// service with running endpoints and an exposed port gets a VIP allocated.
func TestNetworkControllerAllocatesVIPForServiceWithEndpoints(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	helperSetupServiceWithEndpoints(ctx, factStore, "web", 8080, 3)

	networkController := NewNetworkController()
	changes, err := helperReconcileNetworkController(ctx, factStore, networkController)
	if err != nil {
		t.Fatal(err)
	}

	// Expect 3 changes: VIP address, VIP port, DNS record.
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes, got %d", len(changes))
	}

	helperApplyChangesToStore(ctx, factStore, changes)

	vipFact, err := factStore.Get(ctx, types.KeyNetworkVIPService("web"))
	if err != nil {
		t.Fatal("VIP should exist")
	}
	if !strings.HasPrefix(string(vipFact.Value), "10.200.0.") {
		t.Errorf("VIP = %s, want prefix 10.200.0.", vipFact.Value)
	}
}

// TestNetworkControllerCreatesDNSRecordForService verifies that the controller
// creates a DNS mapping from service name to VIP.
func TestNetworkControllerCreatesDNSRecordForService(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	helperSetupServiceWithEndpoints(ctx, factStore, "web", 8080, 2)

	networkController := NewNetworkController()
	changes, _ := helperReconcileNetworkController(ctx, factStore, networkController)
	helperApplyChangesToStore(ctx, factStore, changes)

	dnsFact, err := factStore.Get(ctx, types.KeyNetworkDNS("web"))
	if err != nil {
		t.Fatal("DNS record should exist")
	}
	vipFact, _ := factStore.Get(ctx, types.KeyNetworkVIPService("web"))
	if string(dnsFact.Value) != string(vipFact.Value) {
		t.Errorf("DNS = %s, VIP = %s, they should match", dnsFact.Value, vipFact.Value)
	}
}

// TestNetworkControllerMultipleServicesGetDifferentVIPs verifies that each
// service gets its own unique VIP address.
func TestNetworkControllerMultipleServicesGetDifferentVIPs(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	helperSetupServiceWithEndpoints(ctx, factStore, "web", 8080, 2)
	helperSetupServiceWithEndpoints(ctx, factStore, "api", 3000, 1)

	networkController := NewNetworkController()
	changes, _ := helperReconcileNetworkController(ctx, factStore, networkController)
	helperApplyChangesToStore(ctx, factStore, changes)

	webVIP, _ := factStore.Get(ctx, types.KeyNetworkVIPService("web"))
	apiVIP, _ := factStore.Get(ctx, types.KeyNetworkVIPService("api"))

	if string(webVIP.Value) == string(apiVIP.Value) {
		t.Errorf("services should have different VIPs, both got %s", webVIP.Value)
	}
}

// TestNetworkControllerNoVIPWithoutEndpoints verifies that a service with
// no running endpoints does not get a VIP allocated.
func TestNetworkControllerNoVIPWithoutEndpoints(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	// Service exists with an exposed port but no endpoints.
	factStore.Put(ctx, types.KeyDesiredServiceExpose("web", 8080), []byte(""))

	networkController := NewNetworkController()
	changes, _ := helperReconcileNetworkController(ctx, factStore, networkController)

	if len(changes) != 0 {
		t.Errorf("expected 0 changes for service without endpoints, got %d", len(changes))
	}
}

// TestNetworkControllerNoVIPWithoutExposedPort verifies that a service with
// endpoints but no exposed port does not get a VIP.
func TestNetworkControllerNoVIPWithoutExposedPort(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	// Endpoint exists but service has no expose config.
	factStore.Put(ctx, types.KeyEndpoint("web", "aaa"), []byte("10.100.1.2:8080"))

	networkController := NewNetworkController()
	changes, _ := helperReconcileNetworkController(ctx, factStore, networkController)

	if len(changes) != 0 {
		t.Errorf("expected 0 changes for service without exposed port, got %d", len(changes))
	}
}

// TestNetworkControllerRemovesVIPWhenEndpointsDisappear verifies that when a
// service loses all its endpoints, the VIP and DNS facts are cleaned up.
func TestNetworkControllerRemovesVIPWhenEndpointsDisappear(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	// Setup service with endpoints and get a VIP.
	helperSetupServiceWithEndpoints(ctx, factStore, "web", 8080, 2)
	networkController := NewNetworkController()
	changes, _ := helperReconcileNetworkController(ctx, factStore, networkController)
	helperApplyChangesToStore(ctx, factStore, changes)

	// Verify VIP exists.
	_, err := factStore.Get(ctx, types.KeyNetworkVIPService("web"))
	if err != nil {
		t.Fatal("VIP should exist before endpoint removal")
	}

	// Remove all endpoints.
	factStore.Delete(ctx, types.KeyEndpoint("web", "web-0"))
	factStore.Delete(ctx, types.KeyEndpoint("web", "web-1"))

	// Reconcile again — should produce delete changes.
	changes, _ = helperReconcileNetworkController(ctx, factStore, networkController)
	helperApplyChangesToStore(ctx, factStore, changes)

	_, err = factStore.Get(ctx, types.KeyNetworkVIPService("web"))
	if err == nil {
		t.Error("VIP should be deleted after all endpoints removed")
	}
	_, err = factStore.Get(ctx, types.KeyNetworkDNS("web"))
	if err == nil {
		t.Error("DNS should be deleted after all endpoints removed")
	}
}

// TestNetworkControllerVIPIsStableAcrossReconciliations verifies that
// running reconciliation again with the same state does not change or
// re-allocate the VIP.
func TestNetworkControllerVIPIsStableAcrossReconciliations(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	helperSetupServiceWithEndpoints(ctx, factStore, "web", 8080, 2)
	networkController := NewNetworkController()

	// First reconciliation creates VIP.
	firstChanges, _ := helperReconcileNetworkController(ctx, factStore, networkController)
	helperApplyChangesToStore(ctx, factStore, firstChanges)

	originalVIP, _ := factStore.Get(ctx, types.KeyNetworkVIPService("web"))

	// Second reconciliation should produce no changes.
	secondChanges, _ := helperReconcileNetworkController(ctx, factStore, networkController)
	if len(secondChanges) != 0 {
		t.Errorf("second reconciliation should produce 0 changes, got %d", len(secondChanges))
	}

	// VIP should be unchanged.
	currentVIP, _ := factStore.Get(ctx, types.KeyNetworkVIPService("web"))
	if string(currentVIP.Value) != string(originalVIP.Value) {
		t.Errorf("VIP changed: %s -> %s", originalVIP.Value, currentVIP.Value)
	}
}

// TestNetworkControllerVIPPortMatchesServicePort verifies that the VIP port
// matches the service's exposed port.
func TestNetworkControllerVIPPortMatchesServicePort(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	helperSetupServiceWithEndpoints(ctx, factStore, "api", 3000, 1)
	networkController := NewNetworkController()
	changes, _ := helperReconcileNetworkController(ctx, factStore, networkController)
	helperApplyChangesToStore(ctx, factStore, changes)

	portFact, err := factStore.Get(ctx, types.KeyNetworkVIPServicePort("api"))
	if err != nil {
		t.Fatal("VIP port should exist")
	}
	if string(portFact.Value) != "3000" {
		t.Errorf("VIP port = %s, want 3000", portFact.Value)
	}
}

// TestNetworkControllerIdempotentNoDoubleChanges verifies that reconciling
// multiple times with the same facts never produces duplicate VIP allocations
// or spurious changes.
func TestNetworkControllerIdempotentNoDoubleChanges(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	helperSetupServiceWithEndpoints(ctx, factStore, "web", 8080, 3)
	networkController := NewNetworkController()

	for reconcileIteration := 0; reconcileIteration < 5; reconcileIteration++ {
		changes, _ := helperReconcileNetworkController(ctx, factStore, networkController)
		helperApplyChangesToStore(ctx, factStore, changes)
	}

	// Should have exactly one VIP and one DNS record.
	vipFacts, _ := factStore.Scan(ctx, types.ScanNetworkVIPs)
	// VIP key + VIP port key = 2 facts
	if len(vipFacts) != 2 {
		t.Errorf("expected 2 VIP facts (address + port), got %d", len(vipFacts))
	}

	dnsFacts, _ := factStore.Scan(ctx, types.ScanNetworkDNS)
	if len(dnsFacts) != 1 {
		t.Errorf("expected 1 DNS fact, got %d", len(dnsFacts))
	}
}

// TestNetworkControllerNewServiceGetsNextVIP verifies that when a new
// service appears after others already have VIPs, it gets the next sequential
// VIP address.
func TestNetworkControllerNewServiceGetsNextVIP(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	ctx := context.Background()

	// First service gets a VIP.
	helperSetupServiceWithEndpoints(ctx, factStore, "web", 8080, 1)
	networkController := NewNetworkController()
	changes, _ := helperReconcileNetworkController(ctx, factStore, networkController)
	helperApplyChangesToStore(ctx, factStore, changes)

	webVIP, _ := factStore.Get(ctx, types.KeyNetworkVIPService("web"))

	// Second service appears later.
	helperSetupServiceWithEndpoints(ctx, factStore, "api", 3000, 1)
	changes, _ = helperReconcileNetworkController(ctx, factStore, networkController)
	helperApplyChangesToStore(ctx, factStore, changes)

	apiVIP, _ := factStore.Get(ctx, types.KeyNetworkVIPService("api"))

	// Extract fourth octets — api's should be web's + 1.
	webParts := strings.Split(string(webVIP.Value), ".")
	apiParts := strings.Split(string(apiVIP.Value), ".")
	webFourthOctet, _ := strconv.Atoi(webParts[3])
	apiFourthOctet, _ := strconv.Atoi(apiParts[3])

	if apiFourthOctet != webFourthOctet+1 {
		t.Errorf("api VIP fourth octet = %d, want %d (web=%s, api=%s)",
			apiFourthOctet, webFourthOctet+1, webVIP.Value, apiVIP.Value)
	}
}

// TestNetworkControllerWatchPrefixes verifies the controller watches the
// expected fact prefixes.
func TestNetworkControllerWatchPrefixes(t *testing.T) {
	networkController := NewNetworkController()
	watchPrefixes := networkController.Watch()

	expectedPrefixes := map[string]bool{
		types.ScanEndpoints:       false,
		types.ScanDesiredServices: false,
		types.ScanNetworkVIPs:     false,
		types.ScanNetworkDNS:      false,
	}

	for _, prefix := range watchPrefixes {
		if _, expected := expectedPrefixes[prefix]; !expected {
			t.Errorf("unexpected watch prefix: %s", prefix)
		}
		expectedPrefixes[prefix] = true
	}

	for prefix, found := range expectedPrefixes {
		if !found {
			t.Errorf("missing expected watch prefix: %s", prefix)
		}
	}
}
