package network

import (
	"context"
	"testing"
)

func TestSimulatorDataPlaneRecordsReconciliation(t *testing.T) {
	simulatorDataPlane := NewSimulatorDataPlane()
	ctx := context.Background()

	configs := []ServiceVIPConfig{
		{
			ServiceName: "web",
			VirtualIP:   "10.200.0.1",
			Port:        80,
			Backends: []DataPlaneBackend{
				{Address: "192.168.100.43", Port: 80},
				{Address: "192.168.100.215", Port: 80},
			},
		},
	}

	if err := simulatorDataPlane.ReconcileVIPDataPlane(ctx, configs); err != nil {
		t.Fatalf("ReconcileVIPDataPlane failed: %v", err)
	}

	if simulatorDataPlane.ReconcileCallCount() != 1 {
		t.Errorf("ReconcileCallCount = %d, want 1", simulatorDataPlane.ReconcileCallCount())
	}

	lastReconciled := simulatorDataPlane.LastReconciled()
	if len(lastReconciled) != 1 {
		t.Fatalf("LastReconciled has %d configs, want 1", len(lastReconciled))
	}

	if lastReconciled[0].ServiceName != "web" {
		t.Errorf("ServiceName = %q, want %q", lastReconciled[0].ServiceName, "web")
	}
	if lastReconciled[0].VirtualIP != "10.200.0.1" {
		t.Errorf("VirtualIP = %q, want %q", lastReconciled[0].VirtualIP, "10.200.0.1")
	}
	if len(lastReconciled[0].Backends) != 2 {
		t.Errorf("Backends count = %d, want 2", len(lastReconciled[0].Backends))
	}
}

func TestSimulatorDataPlaneCleanup(t *testing.T) {
	simulatorDataPlane := NewSimulatorDataPlane()
	ctx := context.Background()

	if simulatorDataPlane.IsCleanedUp() {
		t.Fatal("should not be cleaned up before Cleanup is called")
	}

	if err := simulatorDataPlane.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	if !simulatorDataPlane.IsCleanedUp() {
		t.Error("should be cleaned up after Cleanup")
	}
}

func TestSimulatorDataPlaneMultipleReconciliationsUpdateState(t *testing.T) {
	simulatorDataPlane := NewSimulatorDataPlane()
	ctx := context.Background()

	firstConfigs := []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80,
			Backends: []DataPlaneBackend{{Address: "10.0.0.1", Port: 80}}},
	}
	simulatorDataPlane.ReconcileVIPDataPlane(ctx, firstConfigs)

	secondConfigs := []ServiceVIPConfig{
		{ServiceName: "web", VirtualIP: "10.200.0.1", Port: 80,
			Backends: []DataPlaneBackend{{Address: "10.0.0.1", Port: 80}, {Address: "10.0.0.2", Port: 80}}},
		{ServiceName: "redis", VirtualIP: "10.200.0.2", Port: 6379,
			Backends: []DataPlaneBackend{{Address: "10.0.0.3", Port: 6379}}},
	}
	simulatorDataPlane.ReconcileVIPDataPlane(ctx, secondConfigs)

	if simulatorDataPlane.ReconcileCallCount() != 2 {
		t.Errorf("ReconcileCallCount = %d, want 2", simulatorDataPlane.ReconcileCallCount())
	}

	lastReconciled := simulatorDataPlane.LastReconciled()
	if len(lastReconciled) != 2 {
		t.Errorf("LastReconciled has %d configs, want 2", len(lastReconciled))
	}
}

func TestSanitizeChainNameReplacesInvalidCharacters(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"web", "web"},
		{"my-service", "my-service"},
		{"api_v2", "api_v2"},
		{"frontend/web", "frontend_web"},
		{"service.name", "service_name"},
		{"has spaces", "has_spaces"},
		{"UPPERCASE", "UPPERCASE"},
	}

	for _, testCase := range testCases {
		result := sanitizeChainName(testCase.input)
		if result != testCase.expected {
			t.Errorf("sanitizeChainName(%q) = %q, want %q", testCase.input, result, testCase.expected)
		}
	}
}
