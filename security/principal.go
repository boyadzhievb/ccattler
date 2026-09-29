package security

import "context"

// PrincipalKind identifies the type of entity making a request.
type PrincipalKind string

const (
	// PrincipalKindUser represents a human user authenticated via OIDC or local header.
	PrincipalKindUser PrincipalKind = "user"

	// PrincipalKindNode represents a node agent authenticated via mTLS certificate.
	PrincipalKindNode PrincipalKind = "node"

	// PrincipalKindService represents a workload with a SPIFFE identity.
	PrincipalKindService PrincipalKind = "service"

	// PrincipalKindSystem represents an internal system component (controller, scheduler).
	PrincipalKindSystem PrincipalKind = "system"
)

// Principal is the typed identity of an authenticated caller. It carries the
// kind of entity, its name, group memberships, and arbitrary attributes for
// ABAC evaluation.
type Principal struct {
	Kind       PrincipalKind // what kind of entity this is
	Name       string        // unique identifier within kind (e.g. "alice@example.com", "node-1")
	Groups     []string      // group memberships from the identity provider
	Attributes []Attribute   // key-value attributes for ABAC (e.g. team, role)
}

// String returns the canonical string form "kind:name" for backward
// compatibility with the store-layer string principal.
func (principal Principal) String() string {
	return string(principal.Kind) + ":" + principal.Name
}

// HasGroup returns true if the principal is a member of the named group.
func (principal Principal) HasGroup(groupName string) bool {
	for _, memberGroup := range principal.Groups {
		if memberGroup == groupName {
			return true
		}
	}
	return false
}

// principalStructContextKey is the context key for the typed Principal struct.
type principalStructContextKey struct{}

// WithPrincipalStruct returns a context carrying the typed Principal. This
// exists alongside WithPrincipal (string) during migration — the store layer
// uses the string form, while the API layer uses the struct for richer
// authorization decisions.
func WithPrincipalStruct(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalStructContextKey{}, principal)
}

// PrincipalStructFromContext extracts the typed Principal from a context, or
// returns a zero-value Principal if none is set.
func PrincipalStructFromContext(ctx context.Context) Principal {
	principal, _ := ctx.Value(principalStructContextKey{}).(Principal)
	return principal
}

// PrincipalFromKindAndName constructs a Principal from a "kind:name" string.
// This is used when converting from the legacy string principal to the typed
// form.
func PrincipalFromKindAndName(kindColonName string) Principal {
	for separatorIndex := 0; separatorIndex < len(kindColonName); separatorIndex++ {
		if kindColonName[separatorIndex] == ':' {
			return Principal{
				Kind: PrincipalKind(kindColonName[:separatorIndex]),
				Name: kindColonName[separatorIndex+1:],
			}
		}
	}
	return Principal{Kind: PrincipalKindUser, Name: kindColonName}
}
