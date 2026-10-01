package integration

import (
	"context"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// TestABACPolicyDSLRoundTrip verifies the full ABAC condition pipeline:
// DSL policy block → parse → compile to auth/policy/ facts → auth controller
// reconcile → conditional enforcement in APIAuthorizer.
func TestABACPolicyDSLRoundTrip(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	input := `
policy team-isolation {
    allow workload.update
    when subject.team == resource.team
}

policy staging-only {
    allow workload.create
    when subject.environment != "production"
}

policy restricted-teams {
    allow scaling.write
    when subject.team in "payments,platform,frontend"
}
`

	err := lang.Apply(context.Background(), factStore, input)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	apiAuthorizer := security.NewAPIAuthorizer()
	authController := controllers.NewAuthController(apiAuthorizer)

	runner := controllers.NewRunner(factStore, authController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.Run(ctx)

	paymentsDev := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "alice@example.com",
		Attributes: []security.Attribute{
			{Key: "team", Value: "payments"},
			{Key: "environment", Value: "staging"},
		},
	}
	frontendDev := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "bob@example.com",
		Attributes: []security.Attribute{
			{Key: "team", Value: "frontend"},
			{Key: "environment", Value: "staging"},
		},
	}
	productionUser := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "ops@example.com",
		Attributes: []security.Attribute{
			{Key: "team", Value: "platform"},
			{Key: "environment", Value: "production"},
		},
	}
	externalUser := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "contractor@example.com",
		Attributes: []security.Attribute{
			{Key: "team", Value: "external"},
		},
	}

	paymentsResource := &security.ResourceContext{Team: "payments", Name: "checkout", Type: "service"}
	frontendResource := &security.ResourceContext{Team: "frontend", Name: "web", Type: "service"}

	waitFor(t, 2*time.Second, "policies loaded into authorizer", func() bool {
		return apiAuthorizer.AuthorizeAPI(
			paymentsDev, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, paymentsResource,
		) == nil
	})

	t.Run("team_isolation_allows_matching_team", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			paymentsDev, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, paymentsResource,
		)
		if err != nil {
			t.Errorf("payments dev should update payments resource: %v", err)
		}
	})

	t.Run("team_isolation_denies_cross_team", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			paymentsDev, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, frontendResource,
		)
		if err == nil {
			t.Error("payments dev should NOT update frontend resource")
		}
	})

	t.Run("staging_only_allows_non_production", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			paymentsDev, security.CapabilityWorkloadCreate,
			security.ScopeCluster, nil,
		)
		if err != nil {
			t.Errorf("staging user should be allowed to create: %v", err)
		}
	})

	t.Run("staging_only_denies_production", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			productionUser, security.CapabilityWorkloadCreate,
			security.ScopeCluster, nil,
		)
		if err == nil {
			t.Error("production user should NOT be allowed to create (staging-only policy)")
		}
	})

	t.Run("restricted_teams_allows_listed_team", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			paymentsDev, security.CapabilityScalingWrite,
			security.ScopeCluster, nil,
		)
		if err != nil {
			t.Errorf("payments team should have scaling.write: %v", err)
		}
	})

	t.Run("restricted_teams_denies_unlisted_team", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			externalUser, security.CapabilityScalingWrite,
			security.ScopeCluster, nil,
		)
		if err == nil {
			t.Error("external team should NOT have scaling.write")
		}
	})

	t.Run("no_policy_no_grant_denies", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			paymentsDev, security.CapabilityNodeManage,
			security.ScopeCluster, nil,
		)
		if err == nil {
			t.Error("should deny capability with no matching policy or grant")
		}
	})

	t.Run("cross_team_with_frontend_resource", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			frontendDev, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, frontendResource,
		)
		if err != nil {
			t.Errorf("frontend dev should update frontend resource: %v", err)
		}
	})
}

// TestABACPolicyWithRBACGrants verifies that unconditional RBAC grants
// take precedence over conditional policies — if a grant matches, the
// request is allowed regardless of policy conditions.
func TestABACPolicyWithRBACGrants(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	input := `
role admin {
    allow cluster.admin
}

grant admin to user "superadmin@example.com"

policy team-isolation {
    allow workload.update
    when subject.team == resource.team
}
`

	err := lang.Apply(context.Background(), factStore, input)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	apiAuthorizer := security.NewAPIAuthorizer()
	authController := controllers.NewAuthController(apiAuthorizer)

	runner := controllers.NewRunner(factStore, authController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.Run(ctx)

	superAdmin := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "superadmin@example.com",
	}
	frontendResource := &security.ResourceContext{Team: "frontend", Name: "web", Type: "service"}

	waitFor(t, 2*time.Second, "admin grant loaded", func() bool {
		return apiAuthorizer.AuthorizeAPI(
			superAdmin, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, nil,
		) == nil
	})

	t.Run("admin_bypasses_team_isolation", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			superAdmin, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, frontendResource,
		)
		if err != nil {
			t.Errorf("cluster admin should bypass team isolation: %v", err)
		}
	})

	t.Run("non_admin_subject_team_isolation", func(t *testing.T) {
		regularUser := security.Principal{
			Kind:       security.PrincipalKindUser,
			Name:       "dev@example.com",
			Attributes: []security.Attribute{{Key: "team", Value: "payments"}},
		}
		err := apiAuthorizer.AuthorizeAPI(
			regularUser, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, frontendResource,
		)
		if err == nil {
			t.Error("regular user should NOT bypass team isolation for different team")
		}
	})
}

// TestABACPolicyAuditLogging verifies that denied requests due to condition
// failure produce audit entries.
func TestABACPolicyAuditLogging(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	input := `
policy team-isolation {
    allow workload.update
    when subject.team == resource.team
}
`

	err := lang.Apply(context.Background(), factStore, input)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	auditLog := security.NewInMemoryAuditLog(100)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.SetAuditLogger(auditLog)
	authController := controllers.NewAuthController(apiAuthorizer)

	runner := controllers.NewRunner(factStore, authController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.Run(ctx)

	paymentsDev := security.Principal{
		Kind:       security.PrincipalKindUser,
		Name:       "alice@example.com",
		Attributes: []security.Attribute{{Key: "team", Value: "payments"}},
	}
	frontendResource := &security.ResourceContext{Team: "frontend", Name: "web", Type: "service"}

	waitFor(t, 2*time.Second, "policy loaded", func() bool {
		paymentsResource := &security.ResourceContext{Team: "payments", Name: "checkout", Type: "service"}
		return apiAuthorizer.AuthorizeAPI(
			paymentsDev, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, paymentsResource,
		) == nil
	})

	_ = apiAuthorizer.AuthorizeAPI(
		paymentsDev, security.CapabilityWorkloadUpdate,
		security.ScopeCluster, frontendResource,
	)

	entries := auditLog.Entries()
	foundConditionFailure := false
	for _, entry := range entries {
		if entry.Decision == "deny" && entry.Principal == "user:alice@example.com" {
			if entry.Policy == "team-isolation:condition_failed" {
				foundConditionFailure = true
			}
		}
	}
	if !foundConditionFailure {
		t.Error("expected audit entry with decision=deny and policy=team-isolation:condition_failed")
	}
}

// TestABACPolicyMultiCondition verifies that multiple when clauses in a
// single policy use AND semantics.
func TestABACPolicyMultiCondition(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	input := `
policy strict-update {
    allow workload.update
    when subject.team == resource.team
    when subject.environment != "production"
}
`

	err := lang.Apply(context.Background(), factStore, input)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	apiAuthorizer := security.NewAPIAuthorizer()
	authController := controllers.NewAuthController(apiAuthorizer)

	runner := controllers.NewRunner(factStore, authController)
	runner.SetDebounce(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runner.Run(ctx)

	stagingDev := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "alice@example.com",
		Attributes: []security.Attribute{
			{Key: "team", Value: "payments"},
			{Key: "environment", Value: "staging"},
		},
	}
	productionDev := security.Principal{
		Kind: security.PrincipalKindUser,
		Name: "bob@example.com",
		Attributes: []security.Attribute{
			{Key: "team", Value: "payments"},
			{Key: "environment", Value: "production"},
		},
	}
	paymentsResource := &security.ResourceContext{Team: "payments", Name: "checkout", Type: "service"}

	waitFor(t, 2*time.Second, "multi-condition policy loaded", func() bool {
		return apiAuthorizer.AuthorizeAPI(
			stagingDev, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, paymentsResource,
		) == nil
	})

	t.Run("both_conditions_pass", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			stagingDev, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, paymentsResource,
		)
		if err != nil {
			t.Errorf("staging dev on matching team should pass: %v", err)
		}
	})

	t.Run("second_condition_fails_production", func(t *testing.T) {
		err := apiAuthorizer.AuthorizeAPI(
			productionDev, security.CapabilityWorkloadUpdate,
			security.ScopeCluster, paymentsResource,
		)
		if err == nil {
			t.Error("production dev should fail even with matching team")
		}
	})
}

// TestABACPolicyParsing verifies that the DSL parser correctly parses
// policy blocks with various operators.
func TestABACPolicyParsing(t *testing.T) {
	testCases := []struct {
		name           string
		input          string
		expectError    bool
		policyName     string
		capability     string
		conditionCount int
	}{
		{
			name: "basic_equality",
			input: `policy team-iso {
    allow workload.update
    when subject.team == resource.team
}`,
			policyName:     "team-iso",
			capability:     "workload.update",
			conditionCount: 1,
		},
		{
			name: "not_equal_literal",
			input: `policy prod-gate {
    allow workload.create
    when subject.environment != "production"
}`,
			policyName:     "prod-gate",
			capability:     "workload.create",
			conditionCount: 1,
		},
		{
			name: "in_operator",
			input: `policy team-restrict {
    allow scaling.write
    when subject.team in "payments,platform"
}`,
			policyName:     "team-restrict",
			capability:     "scaling.write",
			conditionCount: 1,
		},
		{
			name: "not_in_operator",
			input: `policy block-guests {
    allow workload.read
    when subject.role not_in "guest,readonly"
}`,
			policyName:     "block-guests",
			capability:     "workload.read",
			conditionCount: 1,
		},
		{
			name: "multiple_conditions",
			input: `policy strict {
    allow workload.update
    when subject.team == resource.team
    when subject.environment != "production"
}`,
			policyName:     "strict",
			capability:     "workload.update",
			conditionCount: 2,
		},
		{
			name:        "missing_allow",
			input:       `policy broken { when subject.team == resource.team }`,
			expectError: true,
		},
		{
			name:        "invalid_operator",
			input:       `policy broken { allow workload.read when subject.team > resource.team }`,
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			file, parseErr := lang.Parse(testCase.input)
			if testCase.expectError {
				if parseErr == nil {
					t.Error("expected parse error but got nil")
				}
				return
			}
			if parseErr != nil {
				t.Fatalf("unexpected parse error: %v", parseErr)
			}
			if len(file.Policies) != 1 {
				t.Fatalf("expected 1 policy, got %d", len(file.Policies))
			}

			policy := file.Policies[0]
			if policy.Name != testCase.policyName {
				t.Errorf("policy name: got %q, want %q", policy.Name, testCase.policyName)
			}
			if policy.Capability != testCase.capability {
				t.Errorf("capability: got %q, want %q", policy.Capability, testCase.capability)
			}
			if len(policy.Conditions) != testCase.conditionCount {
				t.Errorf("conditions: got %d, want %d", len(policy.Conditions), testCase.conditionCount)
			}
		})
	}
}

// TestABACPolicyCompilation verifies that compiled policy facts have the
// correct key layout under auth/policy/.
func TestABACPolicyCompilation(t *testing.T) {
	input := `
policy team-isolation {
    allow workload.update
    when subject.team == resource.team
}
`
	file, err := lang.Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	sourceLines := []string{"", "policy team-isolation {", "    allow workload.update", "    when subject.team == resource.team", "}"}
	facts, err := lang.CompileWithSource(file, sourceLines)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}

	expectedKeys := map[string]string{
		"auth/policy/team-isolation/capability":           "workload.update",
		"auth/policy/team-isolation/condition/0/field":    "subject.team",
		"auth/policy/team-isolation/condition/0/operator": "==",
		"auth/policy/team-isolation/condition/0/value":    "resource.team",
	}

	factMap := make(map[string]string)
	for _, fact := range facts {
		factMap[fact.Key] = fact.Value
	}

	for expectedKey, expectedValue := range expectedKeys {
		actualValue, exists := factMap[expectedKey]
		if !exists {
			t.Errorf("missing expected fact key %q", expectedKey)
			continue
		}
		if actualValue != expectedValue {
			t.Errorf("fact %q: got %q, want %q", expectedKey, actualValue, expectedValue)
		}
	}
}
