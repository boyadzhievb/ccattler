package tenant

import (
	"context"
	"fmt"
	"strings"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
)

// SPIFFEDomain is the trust domain for CCattler SPIFFE identities.
const SPIFFEDomain = "ccattler"

// TenantNetworkIsolation enforces identity-based network isolation between
// tenants. By default, cross-tenant traffic is denied. Only same-tenant
// traffic is allowed unless explicit cross-tenant rules exist.
type TenantNetworkIsolation struct {
	factStore    store.StateStore
	registry     *TenantRegistry
	policyEngine *security.NetworkPolicyEngine
}

// NewTenantNetworkIsolation creates a network isolation layer backed by the
// given store and policy engine.
func NewTenantNetworkIsolation(
	factStore store.StateStore,
	registry *TenantRegistry,
	policyEngine *security.NetworkPolicyEngine,
) *TenantNetworkIsolation {
	return &TenantNetworkIsolation{
		factStore:    factStore,
		registry:     registry,
		policyEngine: policyEngine,
	}
}

// SPIFFEIdentity returns the SPIFFE identity URI for a service.
// Format: spiffe://ccattler/{tenant}/{service-leaf}
// For flat-named services with an explicit owner: spiffe://ccattler/{owner}/{service}
func SPIFFEIdentity(tenantName, serviceName string) string {
	leafName := serviceName
	if slashIndex := strings.IndexByte(serviceName, '/'); slashIndex > 0 {
		leafName = serviceName[slashIndex+1:]
	}
	return fmt.Sprintf("spiffe://%s/%s/%s", SPIFFEDomain, tenantName, leafName)
}

// EvaluateTraffic checks whether traffic from sourceService to targetService
// on the given port is allowed, applying tenant isolation rules on top of
// the existing network policy engine.
//
// Rules:
//  1. Same-tenant traffic: allowed by default (unless explicitly denied by policy)
//  2. Cross-tenant traffic: denied by default (unless explicitly allowed by policy)
func (isolation *TenantNetworkIsolation) EvaluateTraffic(
	ctx context.Context,
	sourceService string,
	targetService string,
	port int,
) (security.PolicyAction, string, error) {
	sourceTenant, sourceErr := isolation.registry.ResolveTenantForService(ctx, sourceService)
	targetTenant, targetErr := isolation.registry.ResolveTenantForService(ctx, targetService)

	// If either service has no tenant, fall through to the base policy engine.
	if sourceErr != nil || targetErr != nil {
		action, err := isolation.policyEngine.Evaluate(ctx, sourceService, targetService, port)
		return action, "no-tenant-fallback", err
	}

	// Check the base policy engine first — explicit deny always wins.
	baseAction, err := isolation.policyEngine.Evaluate(ctx, sourceService, targetService, port)
	if err != nil {
		return security.PolicyDeny, "error", err
	}

	if sourceTenant == targetTenant {
		if baseAction == security.PolicyDeny {
			// Check if there's an explicit deny rule (not just default deny).
			if isolation.hasExplicitDenyRule(ctx, sourceService, targetService, port) {
				return security.PolicyDeny, "explicit-deny-same-tenant", nil
			}
		}
		return security.PolicyAllow, "same-tenant", nil
	}

	// Cross-tenant: only allow if there's an explicit allow rule.
	if baseAction == security.PolicyAllow {
		return security.PolicyAllow, "explicit-allow-cross-tenant", nil
	}

	return security.PolicyDeny, "cross-tenant-denied", nil
}

// hasExplicitDenyRule checks if there's a specific deny rule matching the traffic.
func (isolation *TenantNetworkIsolation) hasExplicitDenyRule(
	ctx context.Context,
	sourceService string,
	targetService string,
	port int,
) bool {
	rules, err := isolation.policyEngine.ListRules(ctx)
	if err != nil {
		return false
	}

	for _, rule := range rules {
		if rule.Action != security.PolicyDeny {
			continue
		}
		if rule.SourceService != "*" && rule.SourceService != sourceService {
			continue
		}
		if rule.TargetService != "*" && rule.TargetService != targetService {
			continue
		}
		if rule.Port != 0 && rule.Port != port {
			continue
		}
		return true
	}
	return false
}

// DeriveFirewallRules generates the set of allow/deny rules needed to enforce
// tenant isolation for all currently known services. This is the translation
// from identity-based policies to concrete rules that the network controller
// can apply as iptables/nftables/eBPF rules.
func (isolation *TenantNetworkIsolation) DeriveFirewallRules(ctx context.Context) ([]DerivedFirewallRule, error) {
	tenants, err := isolation.registry.ListTenants(ctx)
	if err != nil {
		return nil, err
	}

	var rules []DerivedFirewallRule

	// Within each tenant, allow all traffic by default.
	for _, tenant := range tenants {
		services, err := isolation.registry.ListServicesForTenant(ctx, tenant.Name)
		if err != nil {
			continue
		}
		for _, source := range services {
			for _, target := range services {
				if source == target {
					continue
				}
				rules = append(rules, DerivedFirewallRule{
					SourceIdentity: SPIFFEIdentity(tenant.Name, source),
					TargetIdentity: SPIFFEIdentity(tenant.Name, target),
					Action:         security.PolicyAllow,
					Reason:         "same-tenant",
				})
			}
		}
	}

	// Add explicit policy rules from the network policy engine.
	policyRules, err := isolation.policyEngine.ListRules(ctx)
	if err != nil {
		return rules, nil
	}

	for _, policyRule := range policyRules {
		sourceTenant, _ := isolation.registry.ResolveTenantForService(ctx, policyRule.SourceService)
		targetTenant, _ := isolation.registry.ResolveTenantForService(ctx, policyRule.TargetService)

		sourceIdentity := policyRule.SourceService
		if sourceTenant != "" {
			sourceIdentity = SPIFFEIdentity(sourceTenant, policyRule.SourceService)
		}
		targetIdentity := policyRule.TargetService
		if targetTenant != "" {
			targetIdentity = SPIFFEIdentity(targetTenant, policyRule.TargetService)
		}

		rules = append(rules, DerivedFirewallRule{
			SourceIdentity: sourceIdentity,
			TargetIdentity: targetIdentity,
			Port:           policyRule.Port,
			Action:         policyRule.Action,
			Reason:         "explicit-policy:" + policyRule.Name,
		})
	}

	return rules, nil
}

// DerivedFirewallRule is a concrete allow/deny rule derived from identity-based
// policies, ready for translation into iptables/nftables/eBPF rules.
type DerivedFirewallRule struct {
	SourceIdentity string               // SPIFFE identity of the source
	TargetIdentity string               // SPIFFE identity of the target
	Port           int                  // target port (0 means any)
	Action         security.PolicyAction // allow or deny
	Reason         string               // why this rule exists
}
