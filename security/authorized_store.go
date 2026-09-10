package security

import (
	"context"
	"fmt"

	"github.com/boyadzhievb/ccattler/store"
)

// principalContextKey is the context key for the authenticated principal identity.
type principalContextKey struct{}

// WithPrincipal returns a context carrying the given principal identity.
func WithPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext extracts the principal identity from a context, or
// returns an empty string if none is set.
func PrincipalFromContext(ctx context.Context) string {
	principal, _ := ctx.Value(principalContextKey{}).(string)
	return principal
}

// AuthorizedStore wraps a StateStore and enforces RBAC authorization on every
// operation. The principal is extracted from the context via WithPrincipal.
type AuthorizedStore struct {
	inner      store.StateStore
	authorizer *RBACAuthorizer
	auditLog   AuditLogger
}

// NewAuthorizedStore creates a store wrapper that checks authorization before
// delegating to the inner store. If auditLog is nil, no audit entries are written.
func NewAuthorizedStore(inner store.StateStore, authorizer *RBACAuthorizer, auditLog AuditLogger) *AuthorizedStore {
	return &AuthorizedStore{
		inner:      inner,
		authorizer: authorizer,
		auditLog:   auditLog,
	}
}

// Get reads a fact, checking read permission first.
func (authorizedStore *AuthorizedStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	principal := PrincipalFromContext(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionRead, key); err != nil {
		authorizedStore.logDenied(principal, "get", key)
		return nil, err
	}
	authorizedStore.logAllowed(principal, "get", key)
	return authorizedStore.inner.Get(ctx, key)
}

// Put writes a fact, checking write permission first.
func (authorizedStore *AuthorizedStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	principal := PrincipalFromContext(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionWrite, key); err != nil {
		authorizedStore.logDenied(principal, "put", key)
		return 0, err
	}
	authorizedStore.logAllowed(principal, "put", key)
	return authorizedStore.inner.Put(ctx, key, value)
}

// Delete removes a fact, checking delete permission first.
func (authorizedStore *AuthorizedStore) Delete(ctx context.Context, key string) error {
	principal := PrincipalFromContext(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionDelete, key); err != nil {
		authorizedStore.logDenied(principal, "delete", key)
		return err
	}
	authorizedStore.logAllowed(principal, "delete", key)
	return authorizedStore.inner.Delete(ctx, key)
}

// Scan reads all facts with the given prefix, checking read permission.
func (authorizedStore *AuthorizedStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	principal := PrincipalFromContext(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionRead, prefix); err != nil {
		authorizedStore.logDenied(principal, "scan", prefix)
		return nil, err
	}
	authorizedStore.logAllowed(principal, "scan", prefix)
	return authorizedStore.inner.Scan(ctx, prefix)
}

// ScanWithRevision reads all facts with the given prefix and the store revision, checking read permission.
func (authorizedStore *AuthorizedStore) ScanWithRevision(ctx context.Context, prefix string) (*store.ScanResult, error) {
	principal := PrincipalFromContext(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionRead, prefix); err != nil {
		authorizedStore.logDenied(principal, "scan", prefix)
		return nil, err
	}
	authorizedStore.logAllowed(principal, "scan", prefix)
	return authorizedStore.inner.ScanWithRevision(ctx, prefix)
}

// Watch creates a watcher on the given key/prefix, checking watch permission.
func (authorizedStore *AuthorizedStore) Watch(ctx context.Context, key string, opts store.WatchOption) (<-chan store.Event, error) {
	principal := PrincipalFromContext(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionWatch, key); err != nil {
		authorizedStore.logDenied(principal, "watch", key)
		return nil, err
	}
	authorizedStore.logAllowed(principal, "watch", key)
	return authorizedStore.inner.Watch(ctx, key, opts)
}

// Transaction delegates to the inner store. Authorization is checked per-key
// in the compare and operation lists.
func (authorizedStore *AuthorizedStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	principal := PrincipalFromContext(ctx)
	for _, compareItem := range compares {
		if err := authorizedStore.authorizer.Authorize(principal, PermissionRead, compareItem.Key); err != nil {
			authorizedStore.logDenied(principal, "transaction-compare", compareItem.Key)
			return false, err
		}
	}
	for _, operation := range onSuccess {
		permission := PermissionWrite
		if operation.Type == store.OpDelete {
			permission = PermissionDelete
		}
		if err := authorizedStore.authorizer.Authorize(principal, permission, operation.Key); err != nil {
			authorizedStore.logDenied(principal, "transaction-op", operation.Key)
			return false, err
		}
	}
	authorizedStore.logAllowed(principal, "transaction", fmt.Sprintf("%d ops", len(onSuccess)))
	return authorizedStore.inner.Transaction(ctx, compares, onSuccess, onFailure)
}

// Revision returns the current store revision. No authorization check needed.
func (authorizedStore *AuthorizedStore) Revision(ctx context.Context) (int64, error) {
	return authorizedStore.inner.Revision(ctx)
}

// Close closes the underlying store.
func (authorizedStore *AuthorizedStore) Close() error {
	return authorizedStore.inner.Close()
}

// logAllowed writes an audit entry for an allowed operation.
func (authorizedStore *AuthorizedStore) logAllowed(principal, action, target string) {
	if authorizedStore.auditLog != nil {
		authorizedStore.auditLog.Log(AuditEntry{
			Principal: principal,
			Action:    action,
			Target:    target,
			Decision:  "allow",
		})
	}
}

// logDenied writes an audit entry for a denied operation.
func (authorizedStore *AuthorizedStore) logDenied(principal, action, target string) {
	if authorizedStore.auditLog != nil {
		authorizedStore.auditLog.Log(AuditEntry{
			Principal: principal,
			Action:    action,
			Target:    target,
			Decision:  "deny",
		})
	}
}
