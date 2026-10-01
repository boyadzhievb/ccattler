package security

import "testing"

// TestEvaluateConditionsTeamIsolation verifies that subject.team == resource.team
// enforces team-based isolation: only matching teams pass.
func TestEvaluateConditionsTeamIsolation(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.team", Operator: ConditionOperatorEqual, Value: "resource.team"},
	}
	principal := Principal{
		Kind: PrincipalKindUser,
		Name: "alice@example.com",
		Attributes: []Attribute{
			{Key: "team", Value: "payments"},
		},
	}

	matchingResource := &ResourceContext{Team: "payments", Name: "checkout", Type: "service"}
	nonMatchingResource := &ResourceContext{Team: "frontend", Name: "web", Type: "service"}

	if !EvaluateConditions(conditions, principal, matchingResource) {
		t.Error("expected ALLOW when subject.team == resource.team (both payments)")
	}
	if EvaluateConditions(conditions, principal, nonMatchingResource) {
		t.Error("expected DENY when subject.team != resource.team (payments vs frontend)")
	}
}

// TestEvaluateConditionsProductionGate verifies that subject.environment != "production"
// blocks production access for non-deployers.
func TestEvaluateConditionsProductionGate(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.environment", Operator: ConditionOperatorNotEqual, Value: "production"},
	}

	stagingUser := Principal{
		Kind:       PrincipalKindUser,
		Name:       "dev@example.com",
		Attributes: []Attribute{{Key: "environment", Value: "staging"}},
	}
	productionUser := Principal{
		Kind:       PrincipalKindUser,
		Name:       "ops@example.com",
		Attributes: []Attribute{{Key: "environment", Value: "production"}},
	}

	if !EvaluateConditions(conditions, stagingUser, nil) {
		t.Error("expected ALLOW for staging user (environment != production)")
	}
	if EvaluateConditions(conditions, productionUser, nil) {
		t.Error("expected DENY for production user (environment == production)")
	}
}

// TestEvaluateConditionsMissingAttributeDeny verifies fail-closed behavior:
// if a required attribute is missing from the principal, the condition returns false.
func TestEvaluateConditionsMissingAttributeDeny(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.team", Operator: ConditionOperatorEqual, Value: "resource.team"},
	}
	principalWithoutTeam := Principal{
		Kind: PrincipalKindUser,
		Name: "noattr@example.com",
	}
	resource := &ResourceContext{Team: "payments", Name: "checkout", Type: "service"}

	if EvaluateConditions(conditions, principalWithoutTeam, resource) {
		t.Error("expected DENY when subject.team attribute is missing (fail-closed)")
	}
}

// TestEvaluateConditionsMissingResourceAttributeDeny verifies fail-closed
// when the resource context is nil or missing the referenced attribute.
func TestEvaluateConditionsMissingResourceAttributeDeny(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.team", Operator: ConditionOperatorEqual, Value: "resource.team"},
	}
	principal := Principal{
		Kind:       PrincipalKindUser,
		Name:       "alice@example.com",
		Attributes: []Attribute{{Key: "team", Value: "payments"}},
	}

	if EvaluateConditions(conditions, principal, nil) {
		t.Error("expected DENY when resource context is nil (fail-closed)")
	}

	emptyResource := &ResourceContext{}
	if EvaluateConditions(conditions, principal, emptyResource) {
		t.Error("expected DENY when resource.team is empty (fail-closed)")
	}
}

// TestEvaluateConditionsMultiConditionAND verifies that multiple conditions
// use AND semantics — all must pass.
func TestEvaluateConditionsMultiConditionAND(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.team", Operator: ConditionOperatorEqual, Value: "resource.team"},
		{Field: "subject.environment", Operator: ConditionOperatorNotEqual, Value: "production"},
	}
	principal := Principal{
		Kind: PrincipalKindUser,
		Name: "alice@example.com",
		Attributes: []Attribute{
			{Key: "team", Value: "payments"},
			{Key: "environment", Value: "staging"},
		},
	}
	resource := &ResourceContext{Team: "payments", Name: "checkout", Type: "service"}

	if !EvaluateConditions(conditions, principal, resource) {
		t.Error("expected ALLOW when both conditions pass (same team + staging)")
	}

	productionPrincipal := Principal{
		Kind: PrincipalKindUser,
		Name: "bob@example.com",
		Attributes: []Attribute{
			{Key: "team", Value: "payments"},
			{Key: "environment", Value: "production"},
		},
	}
	if EvaluateConditions(conditions, productionPrincipal, resource) {
		t.Error("expected DENY when second condition fails (production environment)")
	}
}

// TestEvaluateConditionsInOperator verifies the "in" operator checks
// membership in a comma-separated list.
func TestEvaluateConditionsInOperator(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.team", Operator: ConditionOperatorIn, Value: "payments,frontend,platform"},
	}

	allowedPrincipal := Principal{
		Kind:       PrincipalKindUser,
		Name:       "alice@example.com",
		Attributes: []Attribute{{Key: "team", Value: "payments"}},
	}
	deniedPrincipal := Principal{
		Kind:       PrincipalKindUser,
		Name:       "mallory@example.com",
		Attributes: []Attribute{{Key: "team", Value: "external"}},
	}

	if !EvaluateConditions(conditions, allowedPrincipal, nil) {
		t.Error("expected ALLOW for team 'payments' in [payments,frontend,platform]")
	}
	if EvaluateConditions(conditions, deniedPrincipal, nil) {
		t.Error("expected DENY for team 'external' not in [payments,frontend,platform]")
	}
}

// TestEvaluateConditionsNotInOperator verifies the "not_in" operator rejects
// membership in a comma-separated list.
func TestEvaluateConditionsNotInOperator(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.role", Operator: ConditionOperatorNotIn, Value: "guest,readonly"},
	}

	allowedPrincipal := Principal{
		Kind:       PrincipalKindUser,
		Name:       "admin@example.com",
		Attributes: []Attribute{{Key: "role", Value: "operator"}},
	}
	deniedPrincipal := Principal{
		Kind:       PrincipalKindUser,
		Name:       "viewer@example.com",
		Attributes: []Attribute{{Key: "role", Value: "guest"}},
	}

	if !EvaluateConditions(conditions, allowedPrincipal, nil) {
		t.Error("expected ALLOW for role 'operator' not_in [guest,readonly]")
	}
	if EvaluateConditions(conditions, deniedPrincipal, nil) {
		t.Error("expected DENY for role 'guest' not_in [guest,readonly]")
	}
}

// TestEvaluateConditionsGroupsMembership verifies that subject.groups resolves
// to the principal's group list with contains semantics.
func TestEvaluateConditionsGroupsMembership(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.groups", Operator: ConditionOperatorEqual, Value: "developers"},
	}

	memberPrincipal := Principal{
		Kind:   PrincipalKindUser,
		Name:   "alice@example.com",
		Groups: []string{"developers", "payments-team"},
	}
	nonMemberPrincipal := Principal{
		Kind:   PrincipalKindUser,
		Name:   "mallory@example.com",
		Groups: []string{"contractors"},
	}
	noGroupsPrincipal := Principal{
		Kind: PrincipalKindUser,
		Name: "nobody@example.com",
	}

	if !EvaluateConditions(conditions, memberPrincipal, nil) {
		t.Error("expected ALLOW when 'developers' is in subject.groups")
	}
	if EvaluateConditions(conditions, nonMemberPrincipal, nil) {
		t.Error("expected DENY when 'developers' is not in subject.groups")
	}
	if EvaluateConditions(conditions, noGroupsPrincipal, nil) {
		t.Error("expected DENY when subject has no groups (fail-closed)")
	}
}

// TestEvaluateConditionsSubjectName verifies that subject.name resolves
// to the principal's Name field.
func TestEvaluateConditionsSubjectName(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.name", Operator: ConditionOperatorEqual, Value: "alice@example.com"},
	}

	matchingPrincipal := Principal{Kind: PrincipalKindUser, Name: "alice@example.com"}
	nonMatchingPrincipal := Principal{Kind: PrincipalKindUser, Name: "bob@example.com"}

	if !EvaluateConditions(conditions, matchingPrincipal, nil) {
		t.Error("expected ALLOW when subject.name matches literal")
	}
	if EvaluateConditions(conditions, nonMatchingPrincipal, nil) {
		t.Error("expected DENY when subject.name does not match literal")
	}
}

// TestEvaluateConditionsResourceType verifies conditions on resource.type.
func TestEvaluateConditionsResourceType(t *testing.T) {
	conditions := []Condition{
		{Field: "resource.type", Operator: ConditionOperatorEqual, Value: "service"},
	}
	principal := Principal{Kind: PrincipalKindUser, Name: "alice@example.com"}

	serviceResource := &ResourceContext{Type: "service", Name: "web"}
	nodeResource := &ResourceContext{Type: "node", Name: "node-1"}

	if !EvaluateConditions(conditions, principal, serviceResource) {
		t.Error("expected ALLOW when resource.type == service")
	}
	if EvaluateConditions(conditions, principal, nodeResource) {
		t.Error("expected DENY when resource.type == node (not service)")
	}
}

// TestEvaluateConditionsEmptyConditions verifies that an empty condition list
// always returns true (vacuously true).
func TestEvaluateConditionsEmptyConditions(t *testing.T) {
	principal := Principal{Kind: PrincipalKindUser, Name: "anyone"}
	if !EvaluateConditions(nil, principal, nil) {
		t.Error("expected ALLOW with no conditions (vacuously true)")
	}
	if !EvaluateConditions([]Condition{}, principal, nil) {
		t.Error("expected ALLOW with empty conditions slice")
	}
}

// TestEvaluateConditionsUnknownFieldDeny verifies that referencing a
// non-existent field object (not subject/resource) fails closed.
func TestEvaluateConditionsUnknownFieldDeny(t *testing.T) {
	conditions := []Condition{
		{Field: "context.time", Operator: ConditionOperatorEqual, Value: "morning"},
	}
	principal := Principal{Kind: PrincipalKindUser, Name: "alice"}

	if EvaluateConditions(conditions, principal, nil) {
		t.Error("expected DENY when field uses unknown object 'context' (fail-closed)")
	}
}

// TestEvaluateConditionsInWithWhitespace verifies that the "in" operator
// trims whitespace from comma-separated list entries.
func TestEvaluateConditionsInWithWhitespace(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.team", Operator: ConditionOperatorIn, Value: "payments, frontend, platform"},
	}
	principal := Principal{
		Kind:       PrincipalKindUser,
		Name:       "alice@example.com",
		Attributes: []Attribute{{Key: "team", Value: "frontend"}},
	}

	if !EvaluateConditions(conditions, principal, nil) {
		t.Error("expected ALLOW — 'in' operator should trim whitespace from list entries")
	}
}

// TestEvaluateConditionsUnknownOperator verifies that an unrecognized operator
// fails closed rather than silently allowing.
func TestEvaluateConditionsUnknownOperator(t *testing.T) {
	conditions := []Condition{
		{Field: "subject.team", Operator: ConditionOperator(">="), Value: "payments"},
	}
	principal := Principal{
		Kind:       PrincipalKindUser,
		Name:       "alice@example.com",
		Attributes: []Attribute{{Key: "team", Value: "payments"}},
	}

	if EvaluateConditions(conditions, principal, nil) {
		t.Error("expected DENY for unknown operator (fail-closed)")
	}
}
