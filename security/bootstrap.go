package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// BootstrapTokenKey is the fact store key for the one-time bootstrap token.
const BootstrapTokenKey = "/ccattler/bootstrap/token"

// BootstrapExpiryKey is the fact store key for the bootstrap token expiry timestamp.
const BootstrapExpiryKey = "/ccattler/bootstrap/expiry"

// BootstrapUsedKey is the fact store key marking the bootstrap token as consumed.
const BootstrapUsedKey = "/ccattler/bootstrap/used"

// BootstrapResult contains the generated bootstrap token and its expiry.
type BootstrapResult struct {
	Token  string    // hex-encoded bootstrap token
	Expiry time.Time // when the token expires
}

// GenerateBootstrapToken creates a one-time bootstrap token and stores it in
// the fact store. The token expires after the given duration. It can only be
// used once to create the initial admin credential.
func GenerateBootstrapToken(ctx context.Context, factStore store.StateStore, tokenTTL time.Duration) (*BootstrapResult, error) {
	existingToken, err := factStore.Get(ctx, BootstrapUsedKey)
	if err == nil && string(existingToken.Value) == "true" {
		return nil, fmt.Errorf("bootstrap already completed")
	}

	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	token := hex.EncodeToString(tokenBytes)
	expiry := time.Now().Add(tokenTTL)

	if _, err := factStore.Put(ctx, BootstrapTokenKey, []byte(token)); err != nil {
		return nil, fmt.Errorf("store token: %w", err)
	}
	if _, err := factStore.Put(ctx, BootstrapExpiryKey, []byte(expiry.Format(time.RFC3339))); err != nil {
		return nil, fmt.Errorf("store expiry: %w", err)
	}

	return &BootstrapResult{Token: token, Expiry: expiry}, nil
}

// ValidateBootstrapToken checks whether the given token matches the stored
// bootstrap token and hasn't expired. If valid, it marks the token as used
// so it cannot be reused. Returns the principal name for the admin.
func ValidateBootstrapToken(ctx context.Context, factStore store.StateStore, token string) (string, error) {
	usedFact, err := factStore.Get(ctx, BootstrapUsedKey)
	if err == nil && string(usedFact.Value) == "true" {
		return "", fmt.Errorf("bootstrap token already used")
	}

	storedTokenFact, err := factStore.Get(ctx, BootstrapTokenKey)
	if err != nil {
		return "", fmt.Errorf("no bootstrap token found")
	}

	if string(storedTokenFact.Value) != token {
		return "", fmt.Errorf("invalid bootstrap token")
	}

	expiryFact, err := factStore.Get(ctx, BootstrapExpiryKey)
	if err != nil {
		return "", fmt.Errorf("no expiry found")
	}

	expiryTime, err := time.Parse(time.RFC3339, string(expiryFact.Value))
	if err != nil {
		return "", fmt.Errorf("invalid expiry format")
	}
	if time.Now().After(expiryTime) {
		return "", fmt.Errorf("bootstrap token expired")
	}

	// Mark as used and destroy the token.
	factStore.Put(ctx, BootstrapUsedKey, []byte("true"))
	factStore.Delete(ctx, BootstrapTokenKey)
	factStore.Delete(ctx, BootstrapExpiryKey)

	return "admin", nil
}

// IsBootstrapComplete returns true if the cluster has already been bootstrapped.
func IsBootstrapComplete(ctx context.Context, factStore store.StateStore) bool {
	usedFact, err := factStore.Get(ctx, BootstrapUsedKey)
	return err == nil && string(usedFact.Value) == "true"
}
