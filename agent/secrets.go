// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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
