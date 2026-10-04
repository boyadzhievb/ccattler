// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package tenant

import (
	"context"
	"fmt"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
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
	authorizer     security.Authorizer
	auditLog       security.AuditLogger
}

// NewPolicyGate creates a policy gate backed by the given components.
func NewPolicyGate(
	factStore store.StateStore,
	registry *TenantRegistry,
	quotaAdmission *QuotaAdmission,
	authorizer security.Authorizer,
	auditLog security.AuditLogger,
) *PolicyGate {
	return &PolicyGate{
		factStore:      factStore,
		registry:       registry,
		quotaAdmission: quotaAdmission,
		authorizer:     authorizer,
		auditLog:       auditLog,
	}
}

// GateResult contains the outcome of a policy gate evaluation.
type GateResult struct {
	Allowed  bool          // whether the change passed all gates
	Stage    string        // the gate stage that produced this result
	Reason   string        // human-readable explanation
	Facts    []lang.Fact   // compiled facts ready for commit (if allowed)
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

	// Stage 3: Authorization (RBAC/ABAC check on each fact key).
	if denial := gate.evaluateAuthorizationGate(principal, facts, startTime); denial != nil {
		return denial, nil
	}

	// Stage 4: Quota check (for services, check instance quotas).
	if denial := gate.evaluateQuotaGate(ctx, principal, file, facts, startTime); denial != nil {
		return denial, nil
	}

	// Stage 5: Security policy (tenant state check).
	if denial := gate.evaluateSecurityPolicyGate(ctx, principal, file, facts, startTime); denial != nil {
		return denial, nil
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

// evaluateAuthorizationGate checks RBAC/ABAC authorization for each compiled
// fact key. Returns a denial GateResult if any fact is not writable by the
// principal, or nil if all facts pass authorization.
func (gate *PolicyGate) evaluateAuthorizationGate(principal string, facts []lang.Fact, startTime time.Time) *GateResult {
	if gate.authorizer == nil {
		return nil
	}
	for _, fact := range facts {
		if err := gate.authorizer.Authorize(principal, security.PermissionWrite, fact.Key); err != nil {
			gate.logAudit(principal, "apply", fact.Key, "DENY", "authorization")
			return &GateResult{
				Allowed:  false,
				Stage:    "authorization",
				Reason:   fmt.Sprintf("not authorized to write %q: %v", fact.Key, err),
				Facts:    facts,
				Duration: time.Since(startTime),
			}
		}
	}
	return nil
}

// evaluateQuotaGate checks instance quota admission for each service declared
// in the DSL input. Returns a denial GateResult if any service exceeds its
// instance quota, or nil if all services pass.
func (gate *PolicyGate) evaluateQuotaGate(ctx context.Context, principal string, file *lang.File, facts []lang.Fact, startTime time.Time) *GateResult {
	if gate.quotaAdmission == nil {
		return nil
	}
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
				}
			}
		}
	}
	return nil
}

// evaluateSecurityPolicyGate checks tenant-level security policies. Specifically,
// it rejects changes to services owned by tenants that are being deleted. Returns
// a denial GateResult if any service belongs to a deleting tenant, or nil if all
// services pass.
func (gate *PolicyGate) evaluateSecurityPolicyGate(ctx context.Context, principal string, file *lang.File, facts []lang.Fact, startTime time.Time) *GateResult {
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
				}
			}
		}
	}
	return nil
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
	fact, err := gate.factStore.Get(ctx, types.KeyDesiredTenantState(tenantName))
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
