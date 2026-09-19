package security

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// CloudCredential holds the result of a successful token exchange with a cloud
// provider. The format and contents of the credential data depend on the
// provider (AWS temporary credentials, GCP access token, Azure token).
type CloudCredential struct {
	Provider     string    // "aws", "gcp", or "azure"
	AccessKeyID  string    // AWS access key ID (empty for non-AWS)
	SecretKey    string    // AWS secret access key (empty for non-AWS)
	SessionToken string    // AWS session token or GCP/Azure access token
	ExpiresAt    time.Time // credential expiry time
}

// CloudIdentityConfig holds the configuration for a cloud identity as stored
// in the fact store. This is the runtime representation of a CloudIdentityDecl.
type CloudIdentityConfig struct {
	Name           string // identity name
	Provider       string // "aws", "gcp", or "azure"
	Role           string // AWS IAM role ARN
	ServiceAccount string // GCP service account email
	Pool           string // GCP workload identity pool
	ClientID       string // Azure AD application client ID
	TenantID       string // Azure AD tenant ID
}

// CloudProviderAdapter exchanges a CCattler-issued JWT for cloud-specific
// credentials. Each cloud provider (AWS, GCP, Azure) has its own adapter
// implementation that calls the provider's STS/token endpoint.
type CloudProviderAdapter interface {
	// ExchangeToken exchanges a signed JWT for cloud-specific credentials.
	// The identityConfig provides provider-specific parameters (role ARN,
	// service account, etc.) needed for the exchange.
	ExchangeToken(ctx context.Context, jwtToken string, identityConfig CloudIdentityConfig) (*CloudCredential, error)

	// ProviderName returns the cloud provider identifier ("aws", "gcp", or "azure").
	ProviderName() string
}

// AWSSTSAdapter exchanges CCattler JWTs for AWS temporary credentials using
// the STS AssumeRoleWithWebIdentity API.
type AWSSTSAdapter struct {
	stsEndpoint string // STS endpoint URL (empty = default)
	region      string // AWS region for STS calls
}

// NewAWSSTSAdapter creates an AWS STS adapter. The region defaults to
// "us-east-1" if empty. The stsEndpoint can be overridden for testing.
func NewAWSSTSAdapter(region string, stsEndpoint string) *AWSSTSAdapter {
	if region == "" {
		region = "us-east-1"
	}
	return &AWSSTSAdapter{
		stsEndpoint: stsEndpoint,
		region:      region,
	}
}

// ExchangeToken calls AWS STS AssumeRoleWithWebIdentity to exchange the JWT
// for temporary AWS credentials bound to the configured IAM role.
func (adapter *AWSSTSAdapter) ExchangeToken(ctx context.Context, jwtToken string, identityConfig CloudIdentityConfig) (*CloudCredential, error) {
	if identityConfig.Role == "" {
		return nil, fmt.Errorf("aws: role ARN is required")
	}

	// In production, this would call STS AssumeRoleWithWebIdentity.
	// The adapter is designed for pluggable STS endpoints to enable testing
	// without real AWS credentials.
	return &CloudCredential{
		Provider:  "aws",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, fmt.Errorf("aws: STS exchange not yet connected (role=%s)", identityConfig.Role)
}

// ProviderName returns "aws".
func (adapter *AWSSTSAdapter) ProviderName() string {
	return "aws"
}

// GCPSTSAdapter exchanges CCattler JWTs for GCP access tokens using the
// Google Security Token Service with workload identity federation.
type GCPSTSAdapter struct {
	stsEndpoint string // STS endpoint URL (empty = default)
}

// NewGCPSTSAdapter creates a GCP STS adapter. The stsEndpoint can be
// overridden for testing.
func NewGCPSTSAdapter(stsEndpoint string) *GCPSTSAdapter {
	return &GCPSTSAdapter{
		stsEndpoint: stsEndpoint,
	}
}

// ExchangeToken calls GCP Security Token Service to exchange the JWT for a
// GCP access token via workload identity federation.
func (adapter *GCPSTSAdapter) ExchangeToken(ctx context.Context, jwtToken string, identityConfig CloudIdentityConfig) (*CloudCredential, error) {
	if identityConfig.ServiceAccount == "" {
		return nil, fmt.Errorf("gcp: service_account is required")
	}
	if identityConfig.Pool == "" {
		return nil, fmt.Errorf("gcp: workload identity pool is required")
	}

	return &CloudCredential{
		Provider:  "gcp",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, fmt.Errorf("gcp: STS exchange not yet connected (sa=%s, pool=%s)", identityConfig.ServiceAccount, identityConfig.Pool)
}

// ProviderName returns "gcp".
func (adapter *GCPSTSAdapter) ProviderName() string {
	return "gcp"
}

// AzureADAdapter exchanges CCattler JWTs for Azure access tokens using
// Azure AD federated credential exchange.
type AzureADAdapter struct {
	tokenEndpoint string // Azure AD token endpoint (empty = default)
}

// NewAzureADAdapter creates an Azure AD adapter. The tokenEndpoint can be
// overridden for testing.
func NewAzureADAdapter(tokenEndpoint string) *AzureADAdapter {
	return &AzureADAdapter{
		tokenEndpoint: tokenEndpoint,
	}
}

// ExchangeToken calls Azure AD to exchange the JWT for an Azure access token
// using federated credential configuration.
func (adapter *AzureADAdapter) ExchangeToken(ctx context.Context, jwtToken string, identityConfig CloudIdentityConfig) (*CloudCredential, error) {
	if identityConfig.ClientID == "" {
		return nil, fmt.Errorf("azure: client_id is required")
	}
	if identityConfig.TenantID == "" {
		return nil, fmt.Errorf("azure: tenant_id is required")
	}

	return &CloudCredential{
		Provider:  "azure",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}, fmt.Errorf("azure: AD exchange not yet connected (client=%s, tenant=%s)", identityConfig.ClientID, identityConfig.TenantID)
}

// ProviderName returns "azure".
func (adapter *AzureADAdapter) ProviderName() string {
	return "azure"
}

// SimulatorCloudAdapter is a test double that returns preconfigured credentials
// without calling any real cloud provider.
type SimulatorCloudAdapter struct {
	providerName        string
	simulatedCredential *CloudCredential
	exchangeCount       int
}

// NewSimulatorCloudAdapter creates a test adapter that returns the given
// credential on every ExchangeToken call.
func NewSimulatorCloudAdapter(providerName string, credential *CloudCredential) *SimulatorCloudAdapter {
	return &SimulatorCloudAdapter{
		providerName:        providerName,
		simulatedCredential: credential,
	}
}

// ExchangeToken returns the preconfigured credential without any network call.
func (adapter *SimulatorCloudAdapter) ExchangeToken(ctx context.Context, jwtToken string, identityConfig CloudIdentityConfig) (*CloudCredential, error) {
	adapter.exchangeCount++
	return adapter.simulatedCredential, nil
}

// ProviderName returns the configured provider name.
func (adapter *SimulatorCloudAdapter) ProviderName() string {
	return adapter.providerName
}

// ExchangeCount returns how many times ExchangeToken was called.
func (adapter *SimulatorCloudAdapter) ExchangeCount() int {
	return adapter.exchangeCount
}

// CredentialStorePrefix is the fact store prefix for encrypted cloud credentials.
const CredentialStorePrefix = "credentials/"

// CredentialStore manages encrypted cloud credentials in the fact store using
// the same AES-256-GCM envelope encryption as the secret store.
type CredentialStore struct {
	factStore store.StateStore
	masterKey []byte // 32-byte AES-256 key encryption key
}

// NewCredentialStore creates a credential store backed by the given fact store.
// The masterKey must be exactly 32 bytes for AES-256 encryption.
func NewCredentialStore(factStore store.StateStore, masterKey []byte) (*CredentialStore, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("credential store: master key must be 32 bytes, got %d", len(masterKey))
	}
	return &CredentialStore{
		factStore: factStore,
		masterKey: masterKey,
	}, nil
}

// PutCredential encrypts and stores a cloud credential for the given instance
// and identity combination.
func (credentialStore *CredentialStore) PutCredential(ctx context.Context, instanceID string, identityName string, credential *CloudCredential) error {
	serialized := serializeCredential(credential)
	ciphertext, err := encryptAESGCM(credentialStore.masterKey, []byte(serialized))
	if err != nil {
		return fmt.Errorf("encrypt credential: %w", err)
	}

	credentialKey := CredentialStorePrefix + instanceID + "/" + identityName
	_, err = credentialStore.factStore.Put(ctx, credentialKey, ciphertext)
	return err
}

// GetCredential retrieves and decrypts a cloud credential for the given
// instance and identity combination.
func (credentialStore *CredentialStore) GetCredential(ctx context.Context, instanceID string, identityName string) (*CloudCredential, error) {
	credentialKey := CredentialStorePrefix + instanceID + "/" + identityName
	entry, err := credentialStore.factStore.Get(ctx, credentialKey)
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}

	plaintext, err := decryptAESGCM(credentialStore.masterKey, entry.Value)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}

	return deserializeCredential(string(plaintext))
}

// DeleteCredential removes an encrypted credential from the store.
func (credentialStore *CredentialStore) DeleteCredential(ctx context.Context, instanceID string, identityName string) error {
	credentialKey := CredentialStorePrefix + instanceID + "/" + identityName
	return credentialStore.factStore.Delete(ctx, credentialKey)
}

// ListCredentials returns all stored credential keys as instanceID/identityName pairs.
func (credentialStore *CredentialStore) ListCredentials(ctx context.Context) ([]string, error) {
	facts, err := credentialStore.factStore.Scan(ctx, CredentialStorePrefix)
	if err != nil {
		return nil, err
	}
	var credentialKeys []string
	for _, fact := range facts {
		credentialKeys = append(credentialKeys, strings.TrimPrefix(fact.Key, CredentialStorePrefix))
	}
	return credentialKeys, nil
}

// serializeCredential encodes a CloudCredential as a pipe-delimited string.
func serializeCredential(credential *CloudCredential) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d",
		credential.Provider,
		credential.AccessKeyID,
		credential.SecretKey,
		credential.SessionToken,
		credential.ExpiresAt.Unix(),
	)
}

// deserializeCredential decodes a pipe-delimited string into a CloudCredential.
func deserializeCredential(serialized string) (*CloudCredential, error) {
	parts := strings.SplitN(serialized, "|", 5)
	if len(parts) != 5 {
		return nil, fmt.Errorf("invalid credential format: expected 5 fields, got %d", len(parts))
	}

	var expiresUnix int64
	if _, err := fmt.Sscanf(parts[4], "%d", &expiresUnix); err != nil {
		return nil, fmt.Errorf("parse expires_at: %w", err)
	}

	return &CloudCredential{
		Provider:     parts[0],
		AccessKeyID:  parts[1],
		SecretKey:    parts[2],
		SessionToken: parts[3],
		ExpiresAt:    time.Unix(expiresUnix, 0),
	}, nil
}
