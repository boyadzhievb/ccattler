// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
)

// secretPathPattern matches /secret/ and everything after it for redaction.
var secretPathPattern = regexp.MustCompile(`/secret/.*`)

// sanitizeForAudit removes newlines and control characters from a string and
// redacts secret-related paths to prevent log injection and sensitive data
// exposure when recording audit entries.
func sanitizeForAudit(input string) string {
	sanitized := strings.NewReplacer("\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(input)
	sanitized = secretPathPattern.ReplaceAllString(sanitized, "/secret/[REDACTED]")
	return sanitized
}

// principalContextKey is the context key for the authenticated principal identity.
type principalContextKey struct{}

// authMethodContextKey is the context key for the authentication method used.
type authMethodContextKey struct{}

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

// WithAuthMethod returns a context carrying the authentication method name.
func WithAuthMethod(ctx context.Context, authMethod string) context.Context {
	return context.WithValue(ctx, authMethodContextKey{}, authMethod)
}

// AuthMethodFromContext extracts the authentication method from a context, or
// returns an empty string if none is set.
func AuthMethodFromContext(ctx context.Context) string {
	authMethod, _ := ctx.Value(authMethodContextKey{}).(string)
	return authMethod
}

// AuthorizedStore wraps a StateStore and enforces authorization on every
// operation. The principal is extracted from the context via WithPrincipal,
// unless a service principal is set: then every operation is authorized as
// that internal component and the context principal is audited as
// on_behalf_of (the API server, Flow 25).
type AuthorizedStore struct {
	inner            store.StateStore
	authorizer       Authorizer
	auditLog         AuditLogger
	servicePrincipal string // servicePrincipal, when set, is the store-layer identity used for every operation
}

// NewAuthorizedStore creates a store wrapper that checks authorization before
// delegating to the inner store. If auditLog is nil, no audit entries are written.
func NewAuthorizedStore(inner store.StateStore, authorizer Authorizer, auditLog AuditLogger) *AuthorizedStore {
	return &AuthorizedStore{
		inner:      inner,
		authorizer: authorizer,
		auditLog:   auditLog,
	}
}

// SetServicePrincipal makes the store authorize every operation as the given
// internal principal (e.g. "service:api-server") instead of the caller in the
// context. Users are authorized by capability at the API layer; store-layer
// RBAC applies to internal components only (CLAUDE.md two-layer rule).
func (authorizedStore *AuthorizedStore) SetServicePrincipal(servicePrincipal string) {
	authorizedStore.servicePrincipal = servicePrincipal
}

// authorizingPrincipal returns the principal store-layer RBAC checks: the
// service principal when set, otherwise the principal in the context.
func (authorizedStore *AuthorizedStore) authorizingPrincipal(ctx context.Context) string {
	if authorizedStore.servicePrincipal != "" {
		return authorizedStore.servicePrincipal
	}
	return PrincipalFromContext(ctx)
}

// onBehalfOf returns the end user an operation is performed for when a
// service principal is set, or "" when the context principal itself is checked.
func (authorizedStore *AuthorizedStore) onBehalfOf(ctx context.Context) string {
	if authorizedStore.servicePrincipal == "" {
		return ""
	}
	return sanitizeForAudit(PrincipalFromContext(ctx))
}

// Get reads a fact, checking read permission first.
func (authorizedStore *AuthorizedStore) Get(ctx context.Context, key string) (*store.Fact, error) {
	principal := authorizedStore.authorizingPrincipal(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionRead, key); err != nil {
		authorizedStore.logDenied(ctx, principal, "get", key)
		return nil, err
	}
	authorizedStore.logAllowed(ctx, principal, "get", key)
	return authorizedStore.inner.Get(ctx, key)
}

// Put writes a fact, checking write permission first.
func (authorizedStore *AuthorizedStore) Put(ctx context.Context, key string, value []byte) (int64, error) {
	principal := authorizedStore.authorizingPrincipal(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionWrite, key); err != nil {
		authorizedStore.logDenied(ctx, principal, "put", key)
		return 0, err
	}
	authorizedStore.logAllowed(ctx, principal, "put", key)
	return authorizedStore.inner.Put(ctx, key, value)
}

// Delete removes a fact, checking delete permission first.
func (authorizedStore *AuthorizedStore) Delete(ctx context.Context, key string) error {
	principal := authorizedStore.authorizingPrincipal(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionDelete, key); err != nil {
		authorizedStore.logDenied(ctx, principal, "delete", key)
		return err
	}
	authorizedStore.logAllowed(ctx, principal, "delete", key)
	return authorizedStore.inner.Delete(ctx, key)
}

// Scan reads all facts with the given prefix, checking read permission.
func (authorizedStore *AuthorizedStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	principal := authorizedStore.authorizingPrincipal(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionRead, prefix); err != nil {
		authorizedStore.logDenied(ctx, principal, "scan", prefix)
		return nil, err
	}
	authorizedStore.logAllowed(ctx, principal, "scan", prefix)
	return authorizedStore.inner.Scan(ctx, prefix)
}

// ScanWithRevision reads all facts with the given prefix and the store revision, checking read permission.
func (authorizedStore *AuthorizedStore) ScanWithRevision(ctx context.Context, prefix string) (*store.ScanResult, error) {
	principal := authorizedStore.authorizingPrincipal(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionRead, prefix); err != nil {
		authorizedStore.logDenied(ctx, principal, "scan", prefix)
		return nil, err
	}
	authorizedStore.logAllowed(ctx, principal, "scan", prefix)
	return authorizedStore.inner.ScanWithRevision(ctx, prefix)
}

// Watch creates a watcher on the given key/prefix, checking watch permission.
func (authorizedStore *AuthorizedStore) Watch(ctx context.Context, key string, opts store.WatchOption) (<-chan store.Event, error) {
	principal := authorizedStore.authorizingPrincipal(ctx)
	if err := authorizedStore.authorizer.Authorize(principal, PermissionWatch, key); err != nil {
		authorizedStore.logDenied(ctx, principal, "watch", key)
		return nil, err
	}
	authorizedStore.logAllowed(ctx, principal, "watch", key)
	return authorizedStore.inner.Watch(ctx, key, opts)
}

// Transaction delegates to the inner store. Authorization is checked per-key
// in the compare list and in both the success and failure operation lists.
func (authorizedStore *AuthorizedStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	principal := authorizedStore.authorizingPrincipal(ctx)
	for _, compareItem := range compares {
		if err := authorizedStore.authorizer.Authorize(principal, PermissionRead, compareItem.Key); err != nil {
			authorizedStore.logDenied(ctx, principal, "transaction-compare", compareItem.Key)
			return false, err
		}
	}
	for _, operation := range onSuccess {
		permission := PermissionWrite
		if operation.Type == store.OpDelete {
			permission = PermissionDelete
		}
		if err := authorizedStore.authorizer.Authorize(principal, permission, operation.Key); err != nil {
			authorizedStore.logDenied(ctx, principal, "transaction-op", operation.Key)
			return false, err
		}
	}
	for _, failureBranchOp := range onFailure {
		permission := PermissionWrite
		if failureBranchOp.Type == store.OpDelete {
			permission = PermissionDelete
		}
		if err := authorizedStore.authorizer.Authorize(principal, permission, failureBranchOp.Key); err != nil {
			authorizedStore.logDenied(ctx, principal, "transaction-op", failureBranchOp.Key)
			return false, err
		}
	}
	totalOperations := len(onSuccess) + len(onFailure)
	authorizedStore.logAllowed(ctx, principal, "transaction", fmt.Sprintf("%d ops", totalOperations))
	return authorizedStore.inner.Transaction(ctx, compares, onSuccess, onFailure)
}

// Revision returns the current store revision. No authorization check needed.
func (authorizedStore *AuthorizedStore) Revision(ctx context.Context) (int64, error) {
	return authorizedStore.inner.Revision(ctx)
}

// GrantLease delegates to the underlying store.
func (authorizedStore *AuthorizedStore) GrantLease(ctx context.Context, ttlSeconds int64) (int64, error) {
	return authorizedStore.inner.GrantLease(ctx, ttlSeconds)
}

// PutWithLease delegates to the underlying store.
func (authorizedStore *AuthorizedStore) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (int64, error) {
	return authorizedStore.inner.PutWithLease(ctx, key, value, leaseID)
}

// RenewLease delegates to the underlying store.
func (authorizedStore *AuthorizedStore) RenewLease(ctx context.Context, leaseID int64) (int64, error) {
	return authorizedStore.inner.RenewLease(ctx, leaseID)
}

// RevokeLease delegates to the underlying store.
func (authorizedStore *AuthorizedStore) RevokeLease(ctx context.Context, leaseID int64) error {
	return authorizedStore.inner.RevokeLease(ctx, leaseID)
}

// Close closes the underlying store.
func (authorizedStore *AuthorizedStore) Close() error {
	return authorizedStore.inner.Close()
}

// logAllowed writes an audit entry for an allowed operation.
func (authorizedStore *AuthorizedStore) logAllowed(ctx context.Context, principal, action, target string) {
	if authorizedStore.auditLog != nil {
		authorizedStore.auditLog.Log(AuditEntry{
			Principal:  sanitizeForAudit(principal),
			Action:     sanitizeForAudit(action),
			Target:     sanitizeForAudit(target),
			Decision:   AuditDecisionAllow,
			AuthMethod: AuthMethodFromContext(ctx),
			OnBehalfOf: authorizedStore.onBehalfOf(ctx),
		})
	}
}

// logDenied writes an audit entry for a denied operation.
func (authorizedStore *AuthorizedStore) logDenied(ctx context.Context, principal, action, target string) {
	if authorizedStore.auditLog != nil {
		authorizedStore.auditLog.Log(AuditEntry{
			Principal:  sanitizeForAudit(principal),
			Action:     sanitizeForAudit(action),
			Target:     sanitizeForAudit(target),
			Decision:   AuditDecisionDeny,
			AuthMethod: AuthMethodFromContext(ctx),
			OnBehalfOf: authorizedStore.onBehalfOf(ctx),
		})
	}
}
