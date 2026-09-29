package security

import (
	"net/http"
	"time"
)

// localUserHeader is the HTTP header the CLI sets to identify the local OS
// user when connecting to a non-TLS server.
const localUserHeader = "X-CCattler-User"

// defaultLocalUserTokenExpiry is the authentication result expiry for local
// user authentication. Since the local user re-authenticates on every request,
// the expiry is generous.
const defaultLocalUserTokenExpiry = 24 * time.Hour

// LocalUserAuthenticator derives a principal from the X-CCattler-User header
// sent by the CLI. This authenticator is used in non-TLS mode so that
// authorization is always enforced — even without mTLS, every request carries
// an identity and the audit log records who did what.
//
// The CLI populates the header from the OS user (os/user.Current). The
// returned principal gets cluster-admin privileges via a role binding
// configured at server startup.
type LocalUserAuthenticator struct{}

// NewLocalUserAuthenticator creates a local user authenticator for non-TLS mode.
func NewLocalUserAuthenticator() *LocalUserAuthenticator {
	return &LocalUserAuthenticator{}
}

// Authenticate reads X-CCattler-User from the request. Returns nil result and
// nil error when the header is absent, allowing the chain to try the next
// authenticator.
func (authenticator *LocalUserAuthenticator) Authenticate(request *http.Request) (*AuthenticationResult, error) {
	username := request.Header.Get(localUserHeader)
	if username == "" {
		return nil, nil
	}
	return &AuthenticationResult{
		Principal:     "user:" + username,
		PrincipalKind: PrincipalKindUser,
		ExpiresAt:     time.Now().Add(defaultLocalUserTokenExpiry),
	}, nil
}
