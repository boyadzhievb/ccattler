package tenant

import (
	"context"
	"fmt"

	"github.com/boyadzhievb/ccattler/security"
)

// TenantSecretStore wraps a SecretStore with tenant-scoped access control.
// Secrets belong to tenants and only services within the same tenant can
// access them, unless an explicit cross-tenant grant exists.
type TenantSecretStore struct {
	secretStore *security.SecretStore
	registry    *TenantRegistry
}

// NewTenantSecretStore creates a tenant-scoped secret store.
func NewTenantSecretStore(secretStore *security.SecretStore, registry *TenantRegistry) *TenantSecretStore {
	return &TenantSecretStore{
		secretStore: secretStore,
		registry:    registry,
	}
}

// PutSecret stores an encrypted secret scoped to the given tenant.
// The secret is stored with the tenant prefix: "{tenant}/{secretName}".
func (tenantSecretStore *TenantSecretStore) PutSecret(ctx context.Context, tenantName, secretName string, plaintext []byte) error {
	scopedName := tenantName + "/" + secretName
	return tenantSecretStore.secretStore.PutSecret(ctx, scopedName, plaintext)
}

// GetSecret retrieves a secret only if the requesting service belongs to the
// same tenant that owns the secret. Cross-tenant access is denied.
func (tenantSecretStore *TenantSecretStore) GetSecret(ctx context.Context, serviceName, tenantName, secretName string) ([]byte, error) {
	serviceTenant, err := tenantSecretStore.registry.ResolveTenantForService(ctx, serviceName)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve tenant for service %q: %w", serviceName, err)
	}

	if serviceTenant != tenantName {
		return nil, fmt.Errorf("service %q (tenant %q) cannot access secrets of tenant %q",
			serviceName, serviceTenant, tenantName)
	}

	scopedName := tenantName + "/" + secretName
	return tenantSecretStore.secretStore.GetSecret(ctx, scopedName)
}

// GetSecretForService retrieves a secret for a service, automatically resolving
// the tenant from the service name and checking both tenant isolation and
// grant-based access.
func (tenantSecretStore *TenantSecretStore) GetSecretForService(ctx context.Context, serviceName, secretName string) ([]byte, string, error) {
	serviceTenant, err := tenantSecretStore.registry.ResolveTenantForService(ctx, serviceName)
	if err != nil {
		return nil, "", fmt.Errorf("cannot resolve tenant for service %q: %w", serviceName, err)
	}

	secretTenant := ExtractTenantFromName(secretName)
	if secretTenant == "" {
		secretTenant = serviceTenant
	}

	if serviceTenant != secretTenant {
		return nil, "", fmt.Errorf("service %q (tenant %q) cannot access secret %q (tenant %q)",
			serviceName, serviceTenant, secretName, secretTenant)
	}

	return tenantSecretStore.secretStore.GetSecretForService(ctx, serviceName, secretName)
}

// DeleteSecret removes a tenant-scoped secret.
func (tenantSecretStore *TenantSecretStore) DeleteSecret(ctx context.Context, tenantName, secretName string) error {
	scopedName := tenantName + "/" + secretName
	return tenantSecretStore.secretStore.DeleteSecret(ctx, scopedName)
}

// ListSecretsForTenant returns secret names belonging to a specific tenant.
func (tenantSecretStore *TenantSecretStore) ListSecretsForTenant(ctx context.Context, tenantName string) ([]string, error) {
	allSecrets, err := tenantSecretStore.secretStore.ListSecrets(ctx)
	if err != nil {
		return nil, err
	}

	prefix := tenantName + "/"
	var tenantSecrets []string
	for _, secretName := range allSecrets {
		if len(secretName) > len(prefix) && secretName[:len(prefix)] == prefix {
			tenantSecrets = append(tenantSecrets, secretName[len(prefix):])
		}
	}
	return tenantSecrets, nil
}
