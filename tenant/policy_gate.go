package tenant

import (
	"context"
	"fmt"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// PolicyGate implements the admission pipeline that every change must pass
// through before being committed to the fact store:
//
//	APPLY → syntax validation → schema validation → RBAC/ABAC →
//	quota check → security policy → mutation → commit
type PolicyGate struct {
	factStore      store.StateStore
	registry       *TenantRegistry
	quotaAdmission *QuotaAdmission
	rbacAuthorizer *security.RBACAuthorizer
	auditLog       security.AuditLogger
}

// NewPolicyGate creates a policy gate backed by the given components.
func NewPolicyGate(
	factStore store.StateStore,
	registry *TenantRegistry,
	quotaAdmission *QuotaAdmission,
	rbacAuthorizer *security.RBACAuthorizer,
	auditLog security.AuditLogger,
) *PolicyGate {
	return &PolicyGate{
		factStore:      factStore,
		registry:       registry,
		quotaAdmission: quotaAdmission,
		rbacAuthorizer: rbacAuthorizer,
		auditLog:       auditLog,
	}
}

// GateResult contains the outcome of a policy gate evaluation.
type GateResult struct {
	Allowed  bool         // whether the change passed all gates
	Stage    string       // the gate stage that produced this result
	Reason   string       // human-readable explanation
	Facts    []lang.Fact  // compiled facts ready for commit (if allowed)
	Duration time.Duration // total pipeline evaluation time
}

// Evaluate runs the full admission pipeline on a DSL input string submitted
// by the given principal. Returns the result and any compiled facts.
func (gate *PolicyGate) Evaluate(ctx context.Context, principal, dslInput string) (*GateResult, error) {
	startTime := time.Now()

	// Stage 1: Syntax validation.
	file, err := lang.Parse(dslInput)
	if err != nil {
		gate.logAudit(principal, "apply", "dsl", "DENY", "syntax")
		return &GateResult{
			Allowed:  false,
			Stage:    "syntax",
			Reason:   fmt.Sprintf("syntax error: %v", err),
			Duration: time.Since(startTime),
		}, nil
	}

	// Stage 2: Schema validation (compile AST to facts).
	facts, err := lang.Compile(file)
	if err != nil {
		gate.logAudit(principal, "apply", "dsl", "DENY", "schema")
		return &GateResult{
			Allowed:  false,
			Stage:    "schema",
			Reason:   fmt.Sprintf("schema error: %v", err),
			Duration: time.Since(startTime),
		}, nil
	}

	// Stage 3: Authorization (RBAC check on each fact key).
	if gate.rbacAuthorizer != nil {
		for _, fact := range facts {
			if err := gate.rbacAuthorizer.Authorize(principal, security.PermissionWrite, fact.Key); err != nil {
				gate.logAudit(principal, "apply", fact.Key, "DENY", "authorization")
				return &GateResult{
					Allowed:  false,
					Stage:    "authorization",
					Reason:   fmt.Sprintf("not authorized to write %q: %v", fact.Key, err),
					Facts:    facts,
					Duration: time.Since(startTime),
				}, nil
			}
		}
	}

	// Stage 4: Quota check (for services, check instance quotas).
	if gate.quotaAdmission != nil {
		for _, serviceDecl := range file.Services {
			if serviceDecl.Instances > 0 {
				result, err := gate.quotaAdmission.CheckInstanceAdmission(ctx, serviceDecl.Name, serviceDecl.Instances)
				if err != nil {
					continue
				}
				if !result.Allowed {
					gate.logAudit(principal, "apply", serviceDecl.Name, "DENY", "quota")
					return &GateResult{
						Allowed:  false,
						Stage:    "quota",
						Reason:   result.Reason,
						Facts:    facts,
						Duration: time.Since(startTime),
					}, nil
				}
			}
		}
	}

	// Stage 5: Security policy (tenant state check — reject changes to deleting tenants).
	for _, serviceDecl := range file.Services {
		tenantName := serviceDecl.Owner
		if tenantName == "" {
			tenantName = ExtractTenantFromName(serviceDecl.Name)
		}
		if tenantName != "" {
			if state, err := gate.getTenantState(ctx, tenantName); err == nil && state == TenantDeleting {
				gate.logAudit(principal, "apply", serviceDecl.Name, "DENY", "security")
				return &GateResult{
					Allowed:  false,
					Stage:    "security",
					Reason:   fmt.Sprintf("tenant %q is being deleted", tenantName),
					Facts:    facts,
					Duration: time.Since(startTime),
				}, nil
			}
		}
	}

	// Stage 6: Mutation (no-op for now — facts pass through unchanged).

	// Stage 7: Commit.
	gate.logAudit(principal, "apply", "dsl", "ALLOW", "commit")
	return &GateResult{
		Allowed:  true,
		Stage:    "commit",
		Reason:   "all gates passed",
		Facts:    facts,
		Duration: time.Since(startTime),
	}, nil
}

// EvaluateAndCommit runs the pipeline and commits facts to the store if allowed.
func (gate *PolicyGate) EvaluateAndCommit(ctx context.Context, principal, dslInput string) (*GateResult, error) {
	result, err := gate.Evaluate(ctx, principal, dslInput)
	if err != nil {
		return nil, err
	}

	if !result.Allowed {
		return result, nil
	}

	for _, fact := range result.Facts {
		if _, err := gate.factStore.Put(ctx, fact.Key, []byte(fact.Value)); err != nil {
			return nil, fmt.Errorf("commit fact %q: %w", fact.Key, err)
		}
	}

	return result, nil
}

// getTenantState reads the tenant lifecycle state from the store.
func (gate *PolicyGate) getTenantState(ctx context.Context, tenantName string) (TenantState, error) {
	fact, err := gate.factStore.Get(ctx, fmt.Sprintf("/ccattler/desired/tenant/%s/state", tenantName))
	if err != nil {
		return "", err
	}
	return TenantState(fact.Value), nil
}

// logAudit records a policy gate decision in the audit log.
func (gate *PolicyGate) logAudit(principal, action, target, decision, policy string) {
	if gate.auditLog == nil {
		return
	}
	gate.auditLog.Log(security.AuditEntry{
		Principal: principal,
		Action:    action,
		Target:    target,
		Decision:  decision,
		Policy:    policy,
	})
}
