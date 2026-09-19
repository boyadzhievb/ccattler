package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"time"
)

// WorkloadTokenIssuer signs JWTs for workload identity federation. Cloud
// providers trust these tokens via OIDC discovery, allowing workloads to
// assume cloud IAM roles without static credentials.
type WorkloadTokenIssuer struct {
	signingKey *ecdsa.PrivateKey
	issuerURL  string
	keyID      string
}

// NewWorkloadTokenIssuer creates an issuer with a freshly generated ECDSA P-256
// signing key. The issuerURL must match the OIDC discovery document's "issuer"
// field (e.g. "https://ccattler.example.com").
func NewWorkloadTokenIssuer(issuerURL string) (*WorkloadTokenIssuer, error) {
	signingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}

	keyID := computeKeyID(&signingKey.PublicKey)

	return &WorkloadTokenIssuer{
		signingKey: signingKey,
		issuerURL:  issuerURL,
		keyID:      keyID,
	}, nil
}

// NewWorkloadTokenIssuerWithKey creates an issuer using an existing ECDSA
// private key. Used when the signing key is loaded from persistent storage.
func NewWorkloadTokenIssuerWithKey(issuerURL string, signingKey *ecdsa.PrivateKey) *WorkloadTokenIssuer {
	return &WorkloadTokenIssuer{
		signingKey: signingKey,
		issuerURL:  issuerURL,
		keyID:      computeKeyID(&signingKey.PublicKey),
	}
}

// MintWorkloadToken creates a signed JWT for the given SPIFFE identity and
// audience. The token includes standard OIDC claims (iss, sub, aud, iat, exp)
// and is signed with ES256.
func (issuer *WorkloadTokenIssuer) MintWorkloadToken(spiffeID string, audience string, tokenTTL time.Duration) (string, error) {
	currentTime := time.Now()

	header := map[string]string{
		"alg": "ES256",
		"typ": "JWT",
		"kid": issuer.keyID,
	}

	claims := map[string]interface{}{
		"iss": issuer.issuerURL,
		"sub": spiffeID,
		"aud": audience,
		"iat": currentTime.Unix(),
		"exp": currentTime.Add(tokenTTL).Unix(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}
	headerEncoded := base64.RawURLEncoding.EncodeToString(headerJSON)

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	claimsEncoded := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signedPayload := headerEncoded + "." + claimsEncoded
	payloadHash := sha256.Sum256([]byte(signedPayload))

	r, s, err := ecdsa.Sign(rand.Reader, issuer.signingKey, payloadHash[:])
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	keySize := (issuer.signingKey.Curve.Params().BitSize + 7) / 8
	signatureBytes := make([]byte, 2*keySize)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(signatureBytes[keySize-len(rBytes):keySize], rBytes)
	copy(signatureBytes[2*keySize-len(sBytes):], sBytes)

	signatureEncoded := base64.RawURLEncoding.EncodeToString(signatureBytes)
	return signedPayload + "." + signatureEncoded, nil
}

// IssuerURL returns the OIDC issuer URL configured for this issuer.
func (issuer *WorkloadTokenIssuer) IssuerURL() string {
	return issuer.issuerURL
}

// PublicKey returns the ECDSA public key for signature verification.
func (issuer *WorkloadTokenIssuer) PublicKey() *ecdsa.PublicKey {
	return &issuer.signingKey.PublicKey
}

// KeyID returns the key ID (kid) used in JWT headers and the JWKS document.
func (issuer *WorkloadTokenIssuer) KeyID() string {
	return issuer.keyID
}

// OIDCDiscoveryDocument returns the OpenID Connect discovery metadata document
// for this issuer. Cloud providers fetch this to discover the JWKS endpoint.
func (issuer *WorkloadTokenIssuer) OIDCDiscoveryDocument() OIDCDiscoveryResponse {
	return OIDCDiscoveryResponse{
		Issuer:                issuer.issuerURL,
		JWKSURI:               issuer.issuerURL + "/oidc/jwks",
		ResponseTypesSupported: []string{"id_token"},
		SubjectTypesSupported:  []string{"public"},
		IDTokenSigningAlgValues: []string{"ES256"},
	}
}

// OIDCDiscoveryResponse is the JSON structure returned by the
// /.well-known/openid-configuration endpoint.
type OIDCDiscoveryResponse struct {
	Issuer                  string   `json:"issuer"`
	JWKSURI                 string   `json:"jwks_uri"`
	ResponseTypesSupported  []string `json:"response_types_supported"`
	SubjectTypesSupported   []string `json:"subject_types_supported"`
	IDTokenSigningAlgValues []string `json:"id_token_signing_alg_values_supported"`
}

// JWKSDocument returns the JSON Web Key Set containing the public signing key.
// Cloud providers fetch this to verify workload token signatures.
func (issuer *WorkloadTokenIssuer) JWKSDocument() JWKSResponse {
	publicKey := &issuer.signingKey.PublicKey

	return JWKSResponse{
		Keys: []JWKEntry{
			{
				KeyType:   "EC",
				Use:       "sig",
				Algorithm: "ES256",
				KeyID:     issuer.keyID,
				Curve:     "P-256",
				X:         base64.RawURLEncoding.EncodeToString(padKeyBytes(publicKey.X, 32)),
				Y:         base64.RawURLEncoding.EncodeToString(padKeyBytes(publicKey.Y, 32)),
			},
		},
	}
}

// JWKSResponse is the JSON structure returned by the /oidc/jwks endpoint.
type JWKSResponse struct {
	Keys []JWKEntry `json:"keys"`
}

// JWKEntry represents a single JSON Web Key in the JWKS document.
type JWKEntry struct {
	KeyType   string `json:"kty"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Curve     string `json:"crv"`
	X         string `json:"x"`
	Y         string `json:"y"`
}

// computeKeyID derives a stable key ID from the public key by hashing its
// coordinates. This ensures the kid is deterministic for the same key pair.
func computeKeyID(publicKey *ecdsa.PublicKey) string {
	xBytes := publicKey.X.Bytes()
	yBytes := publicKey.Y.Bytes()
	combined := append(xBytes, yBytes...)
	hash := sha256.Sum256(combined)
	return base64.RawURLEncoding.EncodeToString(hash[:8])
}

// padKeyBytes pads ECDSA coordinate bytes to the full key size, used for JWK
// encoding where coordinates must be exactly 32 bytes for P-256.
func padKeyBytes(coordinate *big.Int, keySize int) []byte {
	coordBytes := coordinate.Bytes()
	if len(coordBytes) >= keySize {
		return coordBytes
	}
	padded := make([]byte, keySize)
	copy(padded[keySize-len(coordBytes):], coordBytes)
	return padded
}
