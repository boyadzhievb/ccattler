package security

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// OIDCConfig holds the configuration for OIDC/OAuth2 authentication.
type OIDCConfig struct {
	Issuer       string   // expected token issuer (e.g. "https://accounts.google.com")
	Audience     string   // expected audience claim
	ClaimMapping ClaimMapping // maps JWT claims to CCattler attributes
}

// ClaimMapping defines how JWT claims are mapped to CCattler principals and
// attributes for authorization.
type ClaimMapping struct {
	PrincipalClaim string // JWT claim to use as principal (default: "sub")
	TeamClaim      string // JWT claim to use as team attribute (default: "team")
	RoleClaim      string // JWT claim to use as role attribute (default: "role")
}

// OIDCAuthenticator validates JWT tokens and extracts principal identities
// and attributes for use with the RBAC/ABAC authorizers.
type OIDCAuthenticator struct {
	config       OIDCConfig
	verifyingKey crypto.PublicKey
}

// NewOIDCAuthenticator creates an authenticator with the given config and
// public key for signature verification.
func NewOIDCAuthenticator(config OIDCConfig, verifyingKey crypto.PublicKey) *OIDCAuthenticator {
	if config.ClaimMapping.PrincipalClaim == "" {
		config.ClaimMapping.PrincipalClaim = "sub"
	}
	return &OIDCAuthenticator{
		config:       config,
		verifyingKey: verifyingKey,
	}
}

// AuthenticationResult contains the identity extracted from a validated JWT.
type AuthenticationResult struct {
	Principal  string      // principal identity (e.g. "user:alice@example.com")
	Attributes []Attribute // extracted attributes for ABAC
	ExpiresAt  time.Time   // token expiry
}

// Authenticate validates a JWT token and returns the extracted identity. The
// token must be signed with the configured public key, not expired, and have
// the expected issuer and audience claims.
func (authenticator *OIDCAuthenticator) Authenticate(tokenString string) (*AuthenticationResult, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("oidc: invalid token format")
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("oidc: decode header: %w", err)
	}

	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("oidc: parse header: %w", err)
	}

	if header.Algorithm != "ES256" {
		return nil, fmt.Errorf("oidc: unsupported algorithm %q (only ES256)", header.Algorithm)
	}

	signedPayload := parts[0] + "." + parts[1]
	signatureBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("oidc: decode signature: %w", err)
	}

	ecdsaKey, ok := authenticator.verifyingKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("oidc: verifying key is not ECDSA")
	}

	payloadHash := sha256.Sum256([]byte(signedPayload))
	if !verifyECDSASignature(ecdsaKey, payloadHash[:], signatureBytes) {
		return nil, fmt.Errorf("oidc: invalid signature")
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("oidc: decode claims: %w", err)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("oidc: parse claims: %w", err)
	}

	if issuer, _ := claims["iss"].(string); issuer != authenticator.config.Issuer {
		return nil, fmt.Errorf("oidc: issuer mismatch: got %q, want %q", issuer, authenticator.config.Issuer)
	}

	if authenticator.config.Audience != "" {
		audience, _ := claims["aud"].(string)
		if audience != authenticator.config.Audience {
			return nil, fmt.Errorf("oidc: audience mismatch: got %q, want %q", audience, authenticator.config.Audience)
		}
	}

	expiresAt := time.Now().Add(1 * time.Hour)
	if expFloat, ok := claims["exp"].(float64); ok {
		expiresAt = time.Unix(int64(expFloat), 0)
		if time.Now().After(expiresAt) {
			return nil, fmt.Errorf("oidc: token expired")
		}
	}

	principalClaim := authenticator.config.ClaimMapping.PrincipalClaim
	principalValue, _ := claims[principalClaim].(string)
	if principalValue == "" {
		return nil, fmt.Errorf("oidc: missing principal claim %q", principalClaim)
	}
	principal := "user:" + principalValue

	var attributes []Attribute
	if teamClaim := authenticator.config.ClaimMapping.TeamClaim; teamClaim != "" {
		if teamValue, ok := claims[teamClaim].(string); ok && teamValue != "" {
			attributes = append(attributes, Attribute{Key: "team", Value: teamValue})
		}
	}
	if roleClaim := authenticator.config.ClaimMapping.RoleClaim; roleClaim != "" {
		if roleValue, ok := claims[roleClaim].(string); ok && roleValue != "" {
			attributes = append(attributes, Attribute{Key: "role", Value: roleValue})
		}
	}

	return &AuthenticationResult{
		Principal:  principal,
		Attributes: attributes,
		ExpiresAt:  expiresAt,
	}, nil
}

// verifyECDSASignature verifies an ECDSA signature in the IEEE P1363 format
// used by JWTs (r || s concatenation, not ASN.1 DER).
func verifyECDSASignature(publicKey *ecdsa.PublicKey, hash, signature []byte) bool {
	keySize := publicKey.Curve.Params().BitSize / 8
	if publicKey.Curve.Params().BitSize%8 != 0 {
		keySize++
	}

	if len(signature) != 2*keySize {
		return false
	}

	r := new(big.Int).SetBytes(signature[:keySize])
	s := new(big.Int).SetBytes(signature[keySize:])
	return ecdsa.Verify(publicKey, hash, r, s)
}

// CreateTestJWT creates a signed JWT for testing purposes. Not for production.
func CreateTestJWT(privateKey *ecdsa.PrivateKey, claims map[string]interface{}) (string, error) {
	header := map[string]string{"alg": "ES256", "typ": "JWT"}
	headerJSON, _ := json.Marshal(header)
	headerEncoded := base64.RawURLEncoding.EncodeToString(headerJSON)

	claimsJSON, _ := json.Marshal(claims)
	claimsEncoded := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signedPayload := headerEncoded + "." + claimsEncoded
	payloadHash := sha256.Sum256([]byte(signedPayload))

	r, s, err := ecdsa.Sign(rand.Reader, privateKey, payloadHash[:])
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	keySize := privateKey.Curve.Params().BitSize / 8
	if privateKey.Curve.Params().BitSize%8 != 0 {
		keySize++
	}

	rBytes := r.Bytes()
	sBytes := s.Bytes()
	signatureBytes := make([]byte, 2*keySize)
	copy(signatureBytes[keySize-len(rBytes):keySize], rBytes)
	copy(signatureBytes[2*keySize-len(sBytes):], sBytes)

	signatureEncoded := base64.RawURLEncoding.EncodeToString(signatureBytes)
	return signedPayload + "." + signatureEncoded, nil
}

// GenerateOIDCKeyPair generates an ECDSA P-256 key pair for OIDC testing.
func GenerateOIDCKeyPair() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}
