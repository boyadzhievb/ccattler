package agent

import "context"

// SecretProvider is the interface the agent uses to retrieve decrypted secrets
// for services. The security package implements this; the agent depends only
// on this interface to stay decoupled.
type SecretProvider interface {
	// GetSecretForService returns the decrypted secret value and mount path if
	// the service has a grant. Returns an error if the service is not authorized.
	GetSecretForService(ctx context.Context, serviceName, secretName string) (plaintext []byte, mountPath string, err error)
}

// MaterializedSecret tracks a secret that was written to the filesystem for
// a running instance, so it can be cleaned up when the instance stops.
type MaterializedSecret struct {
	InstanceID string // instance this secret was materialized for
	SecretName string // name of the secret
	MountPath  string // filesystem path where the secret was written
}
