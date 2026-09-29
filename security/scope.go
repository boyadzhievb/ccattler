package security

import "strings"

// Scope represents a hierarchical authorization scope. Scopes are path-like
// strings where a parent scope includes all its children:
//
//	"cluster"          — the entire cluster
//	"team/payments"    — the payments team subtree
//	"node/node-01"     — a specific node
//
// A grant at "cluster" scope includes everything; a grant at "team/payments"
// includes "team/payments/checkout" but not "team/frontend".
type Scope string

const (
	// ScopeCluster is the root scope that encompasses the entire cluster.
	ScopeCluster Scope = "cluster"
)

// TeamScope returns a scope for the given team name (e.g. "team/payments").
func TeamScope(teamName string) Scope {
	return Scope("team/" + teamName)
}

// NodeScope returns a scope for the given node ID (e.g. "node/node-01").
func NodeScope(nodeID string) Scope {
	return Scope("node/" + nodeID)
}

// ServiceScope returns a scope for the given service name (e.g. "service/web").
func ServiceScope(serviceName string) Scope {
	return Scope("service/" + serviceName)
}

// Contains returns true if this scope includes the given child scope. The
// cluster scope contains everything. A scope contains itself. A parent
// scope contains its children ("team/payments" contains "team/payments/db").
func (scope Scope) Contains(child Scope) bool {
	if scope == ScopeCluster {
		return true
	}
	if scope == child {
		return true
	}
	parentPrefix := string(scope) + "/"
	return strings.HasPrefix(string(child), parentPrefix)
}

// String returns the scope as a plain string.
func (scope Scope) String() string {
	return string(scope)
}
