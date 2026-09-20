package security

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
)

func TestDetectPolicyCyclesSimpleTriangle(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	policyEngine := NewNetworkPolicyEngine(memoryStore)

	// A → B → C → A forms a cycle.
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "ab", SourceService: "frontend/web", TargetService: "payments/api", Port: 443, Action: PolicyAllow})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "bc", SourceService: "payments/api", TargetService: "payments/db", Port: 5432, Action: PolicyAllow})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "ca", SourceService: "payments/db", TargetService: "frontend/web", Port: 8080, Action: PolicyAllow})

	cycles, detectError := policyEngine.DetectPolicyCycles(ctx)
	if detectError != nil {
		t.Fatalf("detect: %v", detectError)
	}

	if len(cycles) == 0 {
		t.Fatal("expected at least one cycle")
	}

	// The cycle should contain all 3 services.
	if len(cycles[0].Services) != 3 {
		t.Errorf("expected 3 services in cycle, got %d: %v", len(cycles[0].Services), cycles[0].Services)
	}
}

func TestDetectPolicyCyclesNoCycle(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	policyEngine := NewNetworkPolicyEngine(memoryStore)

	// Linear chain: A → B → C (no cycle).
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "ab", SourceService: "frontend", TargetService: "api", Port: 443, Action: PolicyAllow})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "bc", SourceService: "api", TargetService: "database", Port: 5432, Action: PolicyAllow})

	cycles, detectError := policyEngine.DetectPolicyCycles(ctx)
	if detectError != nil {
		t.Fatalf("detect: %v", detectError)
	}

	if len(cycles) != 0 {
		t.Errorf("expected no cycles, got %d", len(cycles))
	}
}

func TestDetectPolicyCyclesSelfLoop(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	policyEngine := NewNetworkPolicyEngine(memoryStore)

	// Service allows traffic to itself.
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "self", SourceService: "cache", TargetService: "cache", Port: 6379, Action: PolicyAllow})

	cycles, detectError := policyEngine.DetectPolicyCycles(ctx)
	if detectError != nil {
		t.Fatalf("detect: %v", detectError)
	}

	if len(cycles) == 0 {
		t.Fatal("expected self-loop cycle")
	}
}

func TestDetectPolicyCyclesIgnoresDenyRules(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	policyEngine := NewNetworkPolicyEngine(memoryStore)

	// A → B (allow), B → C (deny), C → A (allow). The deny breaks the cycle.
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "ab", SourceService: "frontend", TargetService: "api", Port: 443, Action: PolicyAllow})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "bc", SourceService: "api", TargetService: "database", Port: 5432, Action: PolicyDeny})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "ca", SourceService: "database", TargetService: "frontend", Port: 8080, Action: PolicyAllow})

	cycles, detectError := policyEngine.DetectPolicyCycles(ctx)
	if detectError != nil {
		t.Fatalf("detect: %v", detectError)
	}

	if len(cycles) != 0 {
		t.Errorf("deny rule should break cycle, got %d cycles", len(cycles))
	}
}

func TestDetectPolicyCyclesIgnoresWildcards(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	policyEngine := NewNetworkPolicyEngine(memoryStore)

	// Wildcard rules should not form meaningful cycles.
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "any-to-api", SourceService: "*", TargetService: "api", Port: 443, Action: PolicyAllow})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "api-to-any", SourceService: "api", TargetService: "*", Port: 0, Action: PolicyAllow})

	cycles, detectError := policyEngine.DetectPolicyCycles(ctx)
	if detectError != nil {
		t.Fatalf("detect: %v", detectError)
	}

	if len(cycles) != 0 {
		t.Errorf("wildcard rules should not form cycles, got %d", len(cycles))
	}
}

func TestDetectPolicyCyclesMultipleCycles(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	policyEngine := NewNetworkPolicyEngine(memoryStore)

	// Two independent cycles: A↔B and C↔D.
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "ab", SourceService: "svc-a", TargetService: "svc-b", Port: 80, Action: PolicyAllow})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "ba", SourceService: "svc-b", TargetService: "svc-a", Port: 80, Action: PolicyAllow})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "cd", SourceService: "svc-c", TargetService: "svc-d", Port: 80, Action: PolicyAllow})
	policyEngine.AddRule(ctx, NetworkPolicyRule{Name: "dc", SourceService: "svc-d", TargetService: "svc-c", Port: 80, Action: PolicyAllow})

	cycles, detectError := policyEngine.DetectPolicyCycles(ctx)
	if detectError != nil {
		t.Fatalf("detect: %v", detectError)
	}

	if len(cycles) < 2 {
		t.Errorf("expected at least 2 cycles, got %d", len(cycles))
	}
}

func TestDetectPolicyCyclesEmptyPolicies(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	policyEngine := NewNetworkPolicyEngine(memoryStore)

	cycles, detectError := policyEngine.DetectPolicyCycles(ctx)
	if detectError != nil {
		t.Fatalf("detect: %v", detectError)
	}

	if len(cycles) != 0 {
		t.Errorf("expected no cycles for empty policy set, got %d", len(cycles))
	}
}
