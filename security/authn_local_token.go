package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// localTokenLength is the number of random bytes generated for the local
// authentication token. The token is hex-encoded to 64 characters.
const localTokenLength = 32

// localTokenDirectoryName is the directory under the current working directory
// where the token file is stored.
const localTokenDirectoryName = ".ccattler"

// localTokenFileName is the name of the file containing the local auth token.
const localTokenFileName = "local-token"

// localTokenPrincipal is the fixed principal identity returned when the local
// token authenticates successfully. Unlike the old header-based authenticator,
// this principal is not attacker-controlled.
const localTokenPrincipal = "user:local-admin"

// localTokenExpiry is the authentication result expiry for local token
// authentication. The token re-authenticates on every request so the expiry
// is generous.
const localTokenExpiry = 24 * time.Hour

// LocalTokenAuthenticator validates requests against a file-based bearer
// token generated at server startup. This replaces the header-based
// LocalUserAuthenticator that trusted an attacker-controllable header value.
//
// The token is written to .ccattler/local-token with mode 0600 so that only
// the server's OS user can read it. CLI commands read this file and include
// the token as Authorization: Bearer <token>.
type LocalTokenAuthenticator struct {
	expectedTokenHex string // hex-encoded expected token for constant-time comparison
	tokenFilePath    string // filesystem path where the token was written
}

// NewLocalTokenAuthenticator generates a cryptographically random token,
// writes it to .ccattler/local-token (mode 0600), and returns an
// authenticator that validates requests against that token. Returns an error
// if token generation or file writing fails.
func NewLocalTokenAuthenticator() (*LocalTokenAuthenticator, error) {
	tokenBytes := make([]byte, localTokenLength)
	if _, readError := rand.Read(tokenBytes); readError != nil {
		return nil, fmt.Errorf("local token: generate random bytes: %w", readError)
	}
	tokenHex := hex.EncodeToString(tokenBytes)

	tokenDirectory := localTokenDirectoryName
	if mkdirError := os.MkdirAll(tokenDirectory, 0700); mkdirError != nil {
		return nil, fmt.Errorf("local token: create directory %s: %w", tokenDirectory, mkdirError)
	}

	tokenFilePath := filepath.Join(tokenDirectory, localTokenFileName)
	if writeError := os.WriteFile(tokenFilePath, []byte(tokenHex), 0600); writeError != nil {
		return nil, fmt.Errorf("local token: write %s: %w", tokenFilePath, writeError)
	}

	return &LocalTokenAuthenticator{
		expectedTokenHex: tokenHex,
		tokenFilePath:    tokenFilePath,
	}, nil
}

// LoadLocalTokenAuthenticator reads an existing token from the given file path
// and returns an authenticator that validates requests against it. Used by
// tests and secondary processes that share the same token file.
func LoadLocalTokenAuthenticator(tokenFilePath string) (*LocalTokenAuthenticator, error) {
	tokenBytes, readError := os.ReadFile(tokenFilePath) //nolint:gosec // reading the local auth token file is intentional
	if readError != nil {
		return nil, fmt.Errorf("local token: read %s: %w", tokenFilePath, readError)
	}
	tokenHex := strings.TrimSpace(string(tokenBytes))
	if tokenHex == "" {
		return nil, fmt.Errorf("local token: %s is empty", tokenFilePath)
	}
	return &LocalTokenAuthenticator{
		expectedTokenHex: tokenHex,
		tokenFilePath:    tokenFilePath,
	}, nil
}

// Authenticate reads the Authorization header for a Bearer token and compares
// it against the expected local token using constant-time comparison. Returns
// nil result and nil error when no Authorization header is present, allowing
// the chain to try the next authenticator. Returns an error when the token is
// present but does not match.
func (authenticator *LocalTokenAuthenticator) Authenticate(request *http.Request) (*AuthenticationResult, error) {
	authorizationHeader := request.Header.Get("Authorization")
	if authorizationHeader == "" {
		return nil, nil
	}
	if !strings.HasPrefix(authorizationHeader, bearerTokenPrefix) {
		return nil, nil
	}
	presentedToken := strings.TrimPrefix(authorizationHeader, bearerTokenPrefix)
	if presentedToken == "" {
		return nil, fmt.Errorf("local token: empty bearer token")
	}

	expectedBytes := []byte(authenticator.expectedTokenHex)
	presentedBytes := []byte(presentedToken)
	if subtle.ConstantTimeCompare(expectedBytes, presentedBytes) != 1 {
		return nil, fmt.Errorf("local token: invalid token")
	}

	return &AuthenticationResult{
		Principal:     localTokenPrincipal,
		PrincipalKind: PrincipalKindUser,
		ExpiresAt:     time.Now().Add(localTokenExpiry),
	}, nil
}

// TokenFilePath returns the filesystem path where the token file was written.
// CLI commands use this to discover and read the token.
func (authenticator *LocalTokenAuthenticator) TokenFilePath() string {
	return authenticator.tokenFilePath
}

// ReadLocalToken reads the local authentication token from the default file
// path (.ccattler/local-token). CLI commands call this to obtain the bearer
// token for non-TLS server communication.
func ReadLocalToken() (string, error) {
	tokenFilePath := filepath.Join(localTokenDirectoryName, localTokenFileName)
	tokenBytes, readError := os.ReadFile(tokenFilePath) //nolint:gosec // reading the local auth token is intentional
	if readError != nil {
		return "", fmt.Errorf("read local token: %w (is the server running?)", readError)
	}
	return strings.TrimSpace(string(tokenBytes)), nil
}
