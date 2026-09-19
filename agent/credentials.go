package agent

import (
	"context"
	"fmt"
	"time"
)

// CredentialProvider is the interface the agent uses to retrieve cloud
// credentials for instances. The credential broker stores encrypted credentials;
// this interface decouples the agent from the storage implementation.
type CredentialProvider interface {
	// GetCredentialForInstance returns the cloud credential for the given
	// instance and identity binding. Returns an error if no credential exists.
	GetCredentialForInstance(ctx context.Context, instanceID string, identityName string) (*MaterializedCredential, error)
}

// MaterializedCredential holds a decrypted cloud credential ready for
// filesystem materialization.
type MaterializedCredential struct {
	Provider     string    // "aws", "gcp", or "azure"
	AccessKeyID  string    // AWS access key ID
	SecretKey    string    // AWS secret access key
	SessionToken string    // AWS session token or GCP/Azure access token
	ExpiresAt    time.Time // credential expiry
	DeliverMode  string    // "credentials" (provider-specific file) or "token" (raw JWT)
	MountPath    string    // filesystem path for the credential files
}

// CredentialFileContent returns the provider-specific credential file content
// based on the deliver mode and cloud provider.
func (credential *MaterializedCredential) CredentialFileContent() (string, string, error) {
	if credential.DeliverMode == "token" {
		return credential.SessionToken, "token", nil
	}

	switch credential.Provider {
	case "aws":
		return credential.formatAWSCredentials(), "credentials", nil
	case "gcp":
		return credential.formatGCPCredentials(), "application_default_credentials.json", nil
	case "azure":
		return credential.SessionToken, "azure-token", nil
	default:
		return "", "", fmt.Errorf("unknown credential provider %q", credential.Provider)
	}
}

// formatAWSCredentials returns INI-style AWS credentials file content
// compatible with the AWS SDK default credential chain.
func (credential *MaterializedCredential) formatAWSCredentials() string {
	return fmt.Sprintf("[default]\naws_access_key_id = %s\naws_secret_access_key = %s\naws_session_token = %s\n",
		credential.AccessKeyID,
		credential.SecretKey,
		credential.SessionToken,
	)
}

// formatGCPCredentials returns Application Default Credentials JSON content
// compatible with Google Cloud client libraries.
func (credential *MaterializedCredential) formatGCPCredentials() string {
	return fmt.Sprintf(`{
  "type": "external_account",
  "token_url": "https://sts.googleapis.com/v1/token",
  "credential_source": {
    "file": "%s/token"
  },
  "service_account_impersonation_url": ""
}`, credential.MountPath)
}

// TrackedCredential tracks a credential that was materialized to the filesystem
// for a running instance, enabling cleanup when the instance stops.
type TrackedCredential struct {
	InstanceID   string // instance this credential was materialized for
	IdentityName string // cloud identity name
	MountPath    string // filesystem directory where credential files were written
	ExpiresAt    time.Time
}
