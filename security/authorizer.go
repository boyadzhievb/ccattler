package security

// Authorizer defines the authorization contract for checking whether a principal
// is permitted to perform a given operation on a given fact store key. Both
// RBACAuthorizer and CombinedAuthorizer satisfy this interface.
type Authorizer interface {
	// Authorize checks whether the principal may perform the operation on the key.
	// Returns nil if allowed, or an error describing why the request was denied.
	Authorize(principal string, operation Permission, key string) error
}
