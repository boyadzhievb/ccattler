package security

import (
	"context"
	"fmt"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
)

// NetworkPolicyPrefix is the fact store prefix for network policy rules.
const NetworkPolicyPrefix = "/ccattler/policy/network/"

// PolicyAction represents the action to take on matching traffic.
type PolicyAction string

const (
	// PolicyAllow permits matching traffic.
	PolicyAllow PolicyAction = "allow"

	// PolicyDeny blocks matching traffic.
	PolicyDeny PolicyAction = "deny"
)

// NetworkPolicyRule describes an identity-based network policy between services.
type NetworkPolicyRule struct {
	Name          string       // unique rule name
	SourceService string       // source service identity (or "*" for any)
	TargetService string       // target service identity
	Port          int          // target port (0 means any port)
	Action        PolicyAction // allow or deny
}

// NetworkPolicyEngine evaluates network policy rules stored as facts.
type NetworkPolicyEngine struct {
	factStore store.StateStore
}

// NewNetworkPolicyEngine creates a policy engine backed by the given store.
func NewNetworkPolicyEngine(factStore store.StateStore) *NetworkPolicyEngine {
	return &NetworkPolicyEngine{factStore: factStore}
}

// AddRule writes a network policy rule to the fact store.
func (policyEngine *NetworkPolicyEngine) AddRule(ctx context.Context, rule NetworkPolicyRule) error {
	ruleKey := NetworkPolicyPrefix + rule.Name
	ruleValue := fmt.Sprintf("%s:%s:%d:%s", rule.SourceService, rule.TargetService, rule.Port, rule.Action)
	_, err := policyEngine.factStore.Put(ctx, ruleKey, []byte(ruleValue))
	return err
}

// RemoveRule deletes a network policy rule from the store.
func (policyEngine *NetworkPolicyEngine) RemoveRule(ctx context.Context, ruleName string) error {
	return policyEngine.factStore.Delete(ctx, NetworkPolicyPrefix+ruleName)
}

// ListRules returns all network policy rules from the store.
func (policyEngine *NetworkPolicyEngine) ListRules(ctx context.Context) ([]NetworkPolicyRule, error) {
	facts, err := policyEngine.factStore.Scan(ctx, NetworkPolicyPrefix)
	if err != nil {
		return nil, err
	}

	rules := make([]NetworkPolicyRule, 0, len(facts))
	for _, fact := range facts {
		ruleName := strings.TrimPrefix(fact.Key, NetworkPolicyPrefix)
		rule, err := parseNetworkPolicyRule(ruleName, string(fact.Value))
		if err != nil {
			continue
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// Evaluate checks whether traffic from sourceService to targetService on the
// given port is allowed. The default policy is deny-all unless an explicit
// allow rule matches.
func (policyEngine *NetworkPolicyEngine) Evaluate(ctx context.Context, sourceService, targetService string, port int) (PolicyAction, error) {
	rules, err := policyEngine.ListRules(ctx)
	if err != nil {
		return PolicyDeny, err
	}

	for _, rule := range rules {
		if rule.Action == PolicyDeny && matchesRule(rule, sourceService, targetService, port) {
			return PolicyDeny, nil
		}
	}

	for _, rule := range rules {
		if rule.Action == PolicyAllow && matchesRule(rule, sourceService, targetService, port) {
			return PolicyAllow, nil
		}
	}

	return PolicyDeny, nil
}

// matchesRule checks if a rule applies to the given traffic parameters.
func matchesRule(rule NetworkPolicyRule, sourceService, targetService string, port int) bool {
	if rule.SourceService != "*" && rule.SourceService != sourceService {
		return false
	}
	if rule.TargetService != "*" && rule.TargetService != targetService {
		return false
	}
	if rule.Port != 0 && rule.Port != port {
		return false
	}
	return true
}

// parseNetworkPolicyRule deserializes a stored rule value.
func parseNetworkPolicyRule(name, value string) (NetworkPolicyRule, error) {
	parts := strings.SplitN(value, ":", 4)
	if len(parts) != 4 {
		return NetworkPolicyRule{}, fmt.Errorf("invalid rule format: %s", value)
	}

	port := 0
	fmt.Sscanf(parts[2], "%d", &port)

	return NetworkPolicyRule{
		Name:          name,
		SourceService: parts[0],
		TargetService: parts[1],
		Port:          port,
		Action:        PolicyAction(parts[3]),
	}, nil
}
