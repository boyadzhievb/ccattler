// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/boyadzhievb/ccattler/store"
)

// defaultCloudCredentialExpiry is the placeholder expiry for cloud credentials
// returned by the stub STS/token exchange adapters.
const defaultCloudCredentialExpiry = 1 * time.Hour

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

// awsDefaultSTSRegion is the region used for STS calls when none is specified.
const awsDefaultSTSRegion = "us-east-1"

// awsSTSSessionNamePrefix is prepended to instance IDs for STS session names.
const awsSTSSessionNamePrefix = "ccattler-"

// awsSTSCredentialDurationSeconds is the default requested credential lifetime.
const awsSTSCredentialDurationSeconds = 3600

// AWSSTSAdapter exchanges CCattler JWTs for AWS temporary credentials using
// the STS AssumeRoleWithWebIdentity API via the AWS SDK v2.
type AWSSTSAdapter struct {
	stsClient *sts.Client // stsClient is the AWS STS API client.
	region    string      // region is the AWS region for STS calls.
}

// NewAWSSTSAdapter creates an AWS STS adapter. The region defaults to
// "us-east-1" if empty. Loads credentials from the default AWS credential
// chain. Returns an error if the SDK config cannot be loaded.
func NewAWSSTSAdapter(ctx context.Context, region string, stsEndpoint string) (*AWSSTSAdapter, error) {
	if region == "" {
		region = awsDefaultSTSRegion
	}
	sdkConfig, loadError := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if loadError != nil {
		return nil, fmt.Errorf("aws-sts: load SDK config: %w", loadError)
	}
	var stsOptions []func(*sts.Options)
	if stsEndpoint != "" {
		stsOptions = append(stsOptions, func(options *sts.Options) {
			options.BaseEndpoint = &stsEndpoint
		})
	}
	return &AWSSTSAdapter{
		stsClient: sts.NewFromConfig(sdkConfig, stsOptions...),
		region:    region,
	}, nil
}

// ExchangeToken calls AWS STS AssumeRoleWithWebIdentity to exchange the JWT
// for temporary AWS credentials bound to the configured IAM role. Returns a
// CloudCredential with the temporary access key, secret key, and session token.
func (adapter *AWSSTSAdapter) ExchangeToken(ctx context.Context, jwtToken string, identityConfig CloudIdentityConfig) (*CloudCredential, error) {
	if identityConfig.Role == "" {
		return nil, fmt.Errorf("aws: role ARN is required")
	}
	sessionName := awsSTSSessionNamePrefix + identityConfig.Name
	assumeInput := &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          &identityConfig.Role,
		RoleSessionName:  &sessionName,
		WebIdentityToken: &jwtToken,
		DurationSeconds:  intPtr(awsSTSCredentialDurationSeconds),
	}
	assumeOutput, assumeError := adapter.stsClient.AssumeRoleWithWebIdentity(ctx, assumeInput)
	if assumeError != nil {
		return nil, fmt.Errorf("aws: AssumeRoleWithWebIdentity(role=%s): %w", identityConfig.Role, assumeError)
	}
	if assumeOutput.Credentials == nil {
		return nil, fmt.Errorf("aws: AssumeRoleWithWebIdentity returned nil credentials")
	}
	return &CloudCredential{
		Provider:     "aws",
		AccessKeyID:  derefString(assumeOutput.Credentials.AccessKeyId),
		SecretKey:    derefString(assumeOutput.Credentials.SecretAccessKey),
		SessionToken: derefString(assumeOutput.Credentials.SessionToken),
		ExpiresAt:    derefTime(assumeOutput.Credentials.Expiration),
	}, nil
}

// ProviderName returns "aws".
func (adapter *AWSSTSAdapter) ProviderName() string {
	return "aws"
}

// intPtr returns a pointer to the given int32 value.
func intPtr(value int32) *int32 {
	return &value
}

// derefString returns the string value of a pointer, or empty string if nil.
func derefString(stringPtr *string) string {
	if stringPtr == nil {
		return ""
	}
	return *stringPtr
}

// derefTime returns the time value of a pointer, or zero time if nil.
func derefTime(timePtr *time.Time) time.Time {
	if timePtr == nil {
		return time.Time{}
	}
	return *timePtr
}

// gcpDefaultSTSEndpoint is the Google Security Token Service endpoint for
// workload identity federation token exchange.
const gcpDefaultSTSEndpoint = "https://sts.googleapis.com/v1/token"

// gcpIAMServiceAccountEndpoint is the base URL for the IAM Credentials API used
// to impersonate a service account after STS token exchange.
const gcpIAMServiceAccountEndpoint = "https://iamcredentials.googleapis.com/v1"

// gcpCloudPlatformScope is the OAuth scope for full GCP API access.
const gcpCloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// gcpAccessTokenLifetime is the requested lifetime for impersonated access tokens.
const gcpAccessTokenLifetime = "3600s"

// GCPSTSAdapter exchanges CCattler JWTs for GCP access tokens using the
// Google Security Token Service with workload identity federation, then
// impersonates the target service account via IAM Credentials.
type GCPSTSAdapter struct {
	stsEndpoint string       // stsEndpoint is the STS token exchange URL.
	httpClient  *http.Client // httpClient is used for STS and IAM API calls.
}

// NewGCPSTSAdapter creates a GCP STS adapter. The stsEndpoint can be
// overridden for testing; empty uses the default GCP STS endpoint.
func NewGCPSTSAdapter(stsEndpoint string) *GCPSTSAdapter {
	if stsEndpoint == "" {
		stsEndpoint = gcpDefaultSTSEndpoint
	}
	return &GCPSTSAdapter{
		stsEndpoint: stsEndpoint,
		httpClient:  &http.Client{Timeout: defaultCloudCredentialExpiry},
	}
}

// ExchangeToken exchanges a CCattler JWT for a GCP access token via workload
// identity federation. First exchanges the JWT for a federated STS token, then
// impersonates the configured service account to get a usable access token.
func (adapter *GCPSTSAdapter) ExchangeToken(ctx context.Context, jwtToken string, identityConfig CloudIdentityConfig) (*CloudCredential, error) {
	if identityConfig.ServiceAccount == "" {
		return nil, fmt.Errorf("gcp: service_account is required")
	}
	if identityConfig.Pool == "" {
		return nil, fmt.Errorf("gcp: workload identity pool is required")
	}
	federatedToken, stsError := adapter.exchangeSTSToken(ctx, jwtToken, identityConfig.Pool)
	if stsError != nil {
		return nil, stsError
	}
	accessToken, expiresAt, impersonateError := adapter.impersonateServiceAccount(ctx, federatedToken, identityConfig.ServiceAccount)
	if impersonateError != nil {
		return nil, impersonateError
	}
	return &CloudCredential{
		Provider:     "gcp",
		SessionToken: accessToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// ProviderName returns "gcp".
func (adapter *GCPSTSAdapter) ProviderName() string {
	return "gcp"
}

// gcpSTSTokenResponse holds the JSON response from the GCP STS token exchange.
type gcpSTSTokenResponse struct {
	AccessToken string `json:"access_token"` // AccessToken is the federated STS token.
	ExpiresIn   int    `json:"expires_in"`   // ExpiresIn is the token lifetime in seconds.
	TokenType   string `json:"token_type"`   // TokenType is typically "Bearer".
}

// exchangeSTSToken calls the GCP Security Token Service to exchange an
// external JWT for a federated STS access token.
func (adapter *GCPSTSAdapter) exchangeSTSToken(ctx context.Context, jwtToken string, workloadIdentityPool string) (string, error) {
	formData := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"audience":             {workloadIdentityPool},
		"scope":                {gcpCloudPlatformScope},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:jwt"},
		"subject_token":        {jwtToken},
	}
	httpRequest, requestError := http.NewRequestWithContext(ctx, http.MethodPost, adapter.stsEndpoint, strings.NewReader(formData.Encode()))
	if requestError != nil {
		return "", fmt.Errorf("gcp: build STS request: %w", requestError)
	}
	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpResponse, responseError := adapter.httpClient.Do(httpRequest)
	if responseError != nil {
		return "", fmt.Errorf("gcp: STS token exchange: %w", responseError)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		return "", fmt.Errorf("gcp: read STS response: %w", readError)
	}
	if httpResponse.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gcp: STS returned %d: %s", httpResponse.StatusCode, string(responseBody))
	}
	var tokenResponse gcpSTSTokenResponse
	if unmarshalError := json.Unmarshal(responseBody, &tokenResponse); unmarshalError != nil {
		return "", fmt.Errorf("gcp: parse STS response: %w", unmarshalError)
	}
	return tokenResponse.AccessToken, nil
}

// gcpGenerateAccessTokenResponse holds the JSON response from the IAM
// Credentials generateAccessToken API.
type gcpGenerateAccessTokenResponse struct {
	AccessToken string `json:"accessToken"` // AccessToken is the impersonated service account token.
	ExpireTime  string `json:"expireTime"`  // ExpireTime is the RFC3339 expiry.
}

// impersonateServiceAccount uses a federated STS token to impersonate a GCP
// service account and obtain an access token for that account.
func (adapter *GCPSTSAdapter) impersonateServiceAccount(ctx context.Context, federatedToken string, serviceAccountEmail string) (string, time.Time, error) {
	impersonateURL := fmt.Sprintf("%s/projects/-/serviceAccounts/%s:generateAccessToken",
		gcpIAMServiceAccountEndpoint, serviceAccountEmail)
	requestBody := fmt.Sprintf(`{"scope":["%s"],"lifetime":"%s"}`, gcpCloudPlatformScope, gcpAccessTokenLifetime)
	httpRequest, requestError := http.NewRequestWithContext(ctx, http.MethodPost, impersonateURL, strings.NewReader(requestBody))
	if requestError != nil {
		return "", time.Time{}, fmt.Errorf("gcp: build impersonate request: %w", requestError)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+federatedToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, responseError := adapter.httpClient.Do(httpRequest)
	if responseError != nil {
		return "", time.Time{}, fmt.Errorf("gcp: impersonate service account: %w", responseError)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		return "", time.Time{}, fmt.Errorf("gcp: read impersonate response: %w", readError)
	}
	if httpResponse.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("gcp: impersonate returned %d: %s", httpResponse.StatusCode, string(responseBody))
	}
	var tokenResponse gcpGenerateAccessTokenResponse
	if unmarshalError := json.Unmarshal(responseBody, &tokenResponse); unmarshalError != nil {
		return "", time.Time{}, fmt.Errorf("gcp: parse impersonate response: %w", unmarshalError)
	}
	expiresAt, parseError := time.Parse(time.RFC3339, tokenResponse.ExpireTime)
	if parseError != nil {
		expiresAt = time.Now().Add(defaultCloudCredentialExpiry)
	}
	return tokenResponse.AccessToken, expiresAt, nil
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
		ExpiresAt: time.Now().Add(defaultCloudCredentialExpiry),
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
// envelope encryption via a KeyProvider (same architecture as SecretStore).
type CredentialStore struct {
	factStore   store.StateStore // factStore holds the encrypted credential blobs.
	keyProvider KeyProvider      // keyProvider wraps and unwraps per-credential DEKs.
}

// NewCredentialStore creates a credential store backed by the given fact store
// and KeyProvider.
func NewCredentialStore(factStore store.StateStore, keyProvider KeyProvider) *CredentialStore {
	return &CredentialStore{
		factStore:   factStore,
		keyProvider: keyProvider,
	}
}

// NewCredentialStoreWithMasterKey creates a credential store using a local
// 32-byte master key. Returns an error if the key is not exactly 32 bytes.
func NewCredentialStoreWithMasterKey(factStore store.StateStore, masterKey []byte) (*CredentialStore, error) {
	localKeyProvider, err := NewLocalKeyProvider(masterKey)
	if err != nil {
		return nil, err
	}
	return NewCredentialStore(factStore, localKeyProvider), nil
}

// PutCredential encrypts and stores a cloud credential for the given instance
// and identity combination using envelope encryption.
func (credentialStore *CredentialStore) PutCredential(ctx context.Context, instanceID string, identityName string, credential *CloudCredential) error {
	serialized := serializeCredential(credential)
	envelopeBlob, err := SealEnvelope(ctx, credentialStore.keyProvider, []byte(serialized))
	if err != nil {
		return fmt.Errorf("encrypt credential: %w", err)
	}

	credentialKey := CredentialStorePrefix + instanceID + "/" + identityName
	_, err = credentialStore.factStore.Put(ctx, credentialKey, envelopeBlob)
	return err
}

// GetCredential retrieves and decrypts a cloud credential for the given
// instance and identity combination using envelope encryption.
func (credentialStore *CredentialStore) GetCredential(ctx context.Context, instanceID string, identityName string) (*CloudCredential, error) {
	credentialKey := CredentialStorePrefix + instanceID + "/" + identityName
	entry, err := credentialStore.factStore.Get(ctx, credentialKey)
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}

	plaintext, err := OpenEnvelope(ctx, credentialStore.keyProvider, entry.Value)
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
