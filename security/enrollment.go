// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
)

// JoinTokenPrefix is the fact store prefix for node join tokens.
const JoinTokenPrefix = "enrollment/token/"

// EnrolledNodePrefix is the fact store prefix for enrolled node records.
const EnrolledNodePrefix = "enrollment/node/"

// tokenHashPrefix is the number of characters from the raw token value stored
// in the JoinToken struct for display in token listings.
const tokenHashPrefix = 8

// JoinToken represents a token that authorizes a node to join the cluster.
type JoinToken struct {
	Token         string    // display prefix of the token (first 8 chars) — full value NOT stored
	NodeID        string    // the node ID this token is for (empty means any node)
	ExpiresAt     time.Time // when the token expires
	CAFingerprint string    // SHA256 hex fingerprint of the DER-encoded CA certificate (empty if CA unavailable)
}

// hashTokenValue returns the hex-encoded SHA-256 hash of a raw token string.
// Used as the etcd key suffix so plaintext tokens are not stored.
func hashTokenValue(rawToken string) string {
	tokenHash := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(tokenHash[:])
}

// CompositeTokenString returns the user-facing token string that embeds the CA
// fingerprint. The format is "<hex-token>.<ca-fingerprint>". If no fingerprint
// is available (e.g. CA was not provided), returns just the raw hex token for
// backward compatibility.
func (joinToken *JoinToken) CompositeTokenString() string {
	if joinToken.CAFingerprint == "" {
		return joinToken.Token
	}
	return joinToken.Token + "." + joinToken.CAFingerprint
}

// EnrolledNode records a node that has successfully joined the cluster.
type EnrolledNode struct {
	NodeID     string    // unique node identifier
	Principal  string    // security principal (e.g. "node:node-1")
	EnrolledAt time.Time // when the node joined
	CertExpiry time.Time // when the node's certificate expires
}

// EnrollmentService manages the node enrollment lifecycle: token generation,
// validation, certificate issuance, and enrolled node tracking.
type EnrollmentService struct {
	factStore            store.StateStore
	certificateAuthority *CertificateAuthority
	rbacAuthorizer       *RBACAuthorizer
	certificateTTL       time.Duration
}

// NewEnrollmentService creates an enrollment service backed by the given store
// and CA. Issued node certificates use the given TTL.
func NewEnrollmentService(
	factStore store.StateStore,
	certificateAuthority *CertificateAuthority,
	rbacAuthorizer *RBACAuthorizer,
	certificateTTL time.Duration,
) *EnrollmentService {
	return &EnrollmentService{
		factStore:            factStore,
		certificateAuthority: certificateAuthority,
		rbacAuthorizer:       rbacAuthorizer,
		certificateTTL:       certificateTTL,
	}
}

// GenerateJoinToken creates a join token that a node can use to enroll. If
// nodeID is empty, the token can be used by any node. The token expires after
// the given duration.
func (enrollmentService *EnrollmentService) GenerateJoinToken(ctx context.Context, nodeID string, tokenTTL time.Duration) (*JoinToken, error) {
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		return nil, fmt.Errorf("generate join token: %w", err)
	}

	tokenValue := hex.EncodeToString(tokenBytes)
	expiresAt := time.Now().Add(tokenTTL)

	// Compute CA certificate fingerprint if the certificate authority is available.
	// This allows the token to carry the fingerprint so that joining nodes can verify
	// the server identity without needing a separate --ca-cert file.
	caFingerprint := ""
	if enrollmentService.certificateAuthority != nil {
		caFingerprint = computeCAFingerprint(enrollmentService.certificateAuthority.CACertificatePEM())
	}

	hashedTokenKey := hashTokenValue(tokenValue)

	// The stored JoinToken contains only the display prefix (first 8 chars) of
	// the raw token — the full plaintext is never persisted to the fact store.
	storedToken := &JoinToken{
		Token:         tokenValue[:tokenHashPrefix],
		NodeID:        nodeID,
		ExpiresAt:     expiresAt,
		CAFingerprint: caFingerprint,
	}

	tokenJSON, err := json.Marshal(storedToken)
	if err != nil {
		return nil, fmt.Errorf("marshal token: %w", err)
	}

	tokenKey := JoinTokenPrefix + hashedTokenKey
	if _, err := enrollmentService.factStore.Put(ctx, tokenKey, tokenJSON); err != nil {
		return nil, fmt.Errorf("store join token: %w", err)
	}

	// Return the full token value to the caller (for display/output), but with
	// the stored fingerprint and metadata from the persisted struct.
	returnedToken := &JoinToken{
		Token:         tokenValue,
		NodeID:        nodeID,
		ExpiresAt:     expiresAt,
		CAFingerprint: caFingerprint,
	}
	return returnedToken, nil
}

// RevokeJoinToken removes a join token from the store. The caller must pass the
// full raw token value; it is hashed to find the stored key.
func (enrollmentService *EnrollmentService) RevokeJoinToken(ctx context.Context, tokenValue string) error {
	hashedTokenKey := hashTokenValue(tokenValue)
	return enrollmentService.factStore.Delete(ctx, JoinTokenPrefix+hashedTokenKey)
}

// EnrollmentRequest is the request a node sends to join the cluster. If CSRPEM
// is non-empty, the server signs the CSR instead of generating a keypair.
type EnrollmentRequest struct {
	Token       string   // join token value
	NodeID      string   // requested node ID
	IPAddresses []net.IP // node's IP addresses for the certificate
	CSRPEM      []byte   // PEM-encoded certificate signing request (empty = legacy flow)
}

// EnrollmentResponse is returned to the node after successful enrollment.
type EnrollmentResponse struct {
	CertificatePEM []byte // PEM-encoded node certificate
	PrivateKeyPEM  []byte // PEM-encoded private key
	CACertPEM      []byte // PEM-encoded CA certificate for peer verification
	Principal      string // assigned security principal
}

// EnrollNode validates the join token and issues a certificate for the node.
// On success, it records the node as enrolled, binds the node-agent RBAC role,
// and destroys the join token. If the request contains a CSR, the server signs
// it instead of generating a new keypair.
func (enrollmentService *EnrollmentService) EnrollNode(ctx context.Context, request EnrollmentRequest) (*EnrollmentResponse, error) {
	hashedTokenKey := hashTokenValue(request.Token)
	tokenKey := JoinTokenPrefix + hashedTokenKey

	joinToken, validateError := enrollmentService.validateJoinToken(ctx, tokenKey, request)
	if validateError != nil {
		return nil, validateError
	}
	_ = joinToken // token metadata used only for validation above

	dnsNames := []string{
		request.NodeID,
		request.NodeID + ".cca.local",
	}

	certificatePEM, privateKeyPEM, certExpiry, issueError := enrollmentService.issueOrSignCertificate(request, dnsNames)
	if issueError != nil {
		return nil, issueError
	}

	principal := "node:" + request.NodeID
	enrollmentService.bindRBACRoleForNode(principal)
	enrollmentService.recordEnrolledNode(ctx, request.NodeID, principal, certExpiry)

	// Destroy the join token (one-time use).
	if deleteError := enrollmentService.factStore.Delete(ctx, tokenKey); deleteError != nil {
		logging.Default().Error("failed to delete used join token", "key", tokenKey, "error", deleteError.Error())
	}

	return &EnrollmentResponse{
		CertificatePEM: certificatePEM,
		PrivateKeyPEM:  privateKeyPEM,
		CACertPEM:      enrollmentService.certificateAuthority.CACertificatePEM(),
		Principal:      principal,
	}, nil
}

// validateJoinToken reads and validates the join token from the fact store.
// Returns the parsed token or an error.
func (enrollmentService *EnrollmentService) validateJoinToken(ctx context.Context, tokenKey string, request EnrollmentRequest) (*JoinToken, error) {
	tokenFact, err := enrollmentService.factStore.Get(ctx, tokenKey)
	if err != nil {
		return nil, fmt.Errorf("invalid join token")
	}

	var joinToken JoinToken
	if unmarshalErr := json.Unmarshal(tokenFact.Value, &joinToken); unmarshalErr != nil {
		return nil, fmt.Errorf("corrupt join token")
	}

	if time.Now().After(joinToken.ExpiresAt) {
		if deleteError := enrollmentService.factStore.Delete(ctx, tokenKey); deleteError != nil {
			logging.Default().Error("failed to delete expired join token", "key", tokenKey, "error", deleteError.Error())
		}
		return nil, fmt.Errorf("join token expired")
	}

	if joinToken.NodeID != "" && joinToken.NodeID != request.NodeID {
		return nil, fmt.Errorf("token is for node %q, not %q", joinToken.NodeID, request.NodeID)
	}

	return &joinToken, nil
}

// issueOrSignCertificate either signs a CSR (new flow) or generates a full
// keypair (legacy flow) depending on whether the request contains a CSR.
func (enrollmentService *EnrollmentService) issueOrSignCertificate(
	request EnrollmentRequest, dnsNames []string,
) (certificatePEM []byte, privateKeyPEM []byte, certExpiry time.Time, issueError error) {
	if len(request.CSRPEM) > 0 {
		return enrollmentService.signCSRFromRequest(request, dnsNames)
	}
	return nil, nil, time.Time{}, fmt.Errorf("enrollment rejected: CSR required (legacy server-generated keypair is no longer supported)")
}

// signCSRFromRequest decodes the PEM-encoded CSR from the enrollment request,
// parses it, and signs it using the cluster CA. Returns only the certificate
// PEM — the private key PEM is nil because the key stays on the node.
func (enrollmentService *EnrollmentService) signCSRFromRequest(
	request EnrollmentRequest, dnsNames []string,
) ([]byte, []byte, time.Time, error) {
	csrBlock, _ := pem.Decode(request.CSRPEM)
	if csrBlock == nil {
		return nil, nil, time.Time{}, fmt.Errorf("invalid CSR PEM encoding")
	}

	parsedCSR, parseError := x509.ParseCertificateRequest(csrBlock.Bytes)
	if parseError != nil {
		return nil, nil, time.Time{}, fmt.Errorf("parse CSR: %w", parseError)
	}

	signResponse, signError := enrollmentService.certificateAuthority.SignCSR(SignCSRRequest{
		CSR:         parsedCSR,
		TTL:         enrollmentService.certificateTTL,
		DNSNames:    dnsNames,
		IPAddresses: request.IPAddresses,
	})
	if signError != nil {
		return nil, nil, time.Time{}, fmt.Errorf("sign CSR: %w", signError)
	}

	return signResponse.CertificatePEM, nil, signResponse.NotAfter, nil
}

// bindRBACRoleForNode binds the node-agent RBAC role to the given principal.
func (enrollmentService *EnrollmentService) bindRBACRoleForNode(principal string) {
	if enrollmentService.rbacAuthorizer != nil {
		enrollmentService.rbacAuthorizer.BindRole(RoleBinding{
			Principal: principal,
			RoleName:  "node-agent",
		})
	}
}

// recordEnrolledNode persists the enrolled node record to the fact store.
func (enrollmentService *EnrollmentService) recordEnrolledNode(ctx context.Context, nodeID string, principal string, certExpiry time.Time) {
	enrolledNode := EnrolledNode{
		NodeID:     nodeID,
		Principal:  principal,
		EnrolledAt: time.Now(),
		CertExpiry: certExpiry,
	}
	enrolledJSON, _ := json.Marshal(enrolledNode)
	if _, putError := enrollmentService.factStore.Put(ctx, EnrolledNodePrefix+nodeID, enrolledJSON); putError != nil {
		logging.Default().Error("failed to store enrolled node", "nodeID", nodeID, "error", putError.Error())
	}
}

// ListEnrolledNodes returns all nodes that have successfully enrolled.
func (enrollmentService *EnrollmentService) ListEnrolledNodes(ctx context.Context) ([]EnrolledNode, error) {
	facts, err := enrollmentService.factStore.Scan(ctx, EnrolledNodePrefix)
	if err != nil {
		return nil, err
	}

	enrolledNodes := make([]EnrolledNode, 0, len(facts))
	for _, fact := range facts {
		var node EnrolledNode
		if err := json.Unmarshal(fact.Value, &node); err != nil {
			continue
		}
		enrolledNodes = append(enrolledNodes, node)
	}
	return enrolledNodes, nil
}

// IsNodeEnrolled checks whether a node has been enrolled.
func (enrollmentService *EnrollmentService) IsNodeEnrolled(ctx context.Context, nodeID string) bool {
	_, err := enrollmentService.factStore.Get(ctx, EnrolledNodePrefix+nodeID)
	return err == nil
}

// RemoveNode removes a node's enrollment record and RBAC binding.
func (enrollmentService *EnrollmentService) RemoveNode(ctx context.Context, nodeID string) error {
	principal := "node:" + nodeID
	if enrollmentService.rbacAuthorizer != nil {
		enrollmentService.rbacAuthorizer.RemoveBinding(principal)
	}
	return enrollmentService.factStore.Delete(ctx, EnrolledNodePrefix+nodeID)
}

// ListJoinTokens returns all active (non-expired) join tokens. The Token field
// contains only the stored prefix (first 8 chars) followed by asterisks.
func (enrollmentService *EnrollmentService) ListJoinTokens(ctx context.Context) ([]JoinToken, error) {
	facts, err := enrollmentService.factStore.Scan(ctx, JoinTokenPrefix)
	if err != nil {
		return nil, err
	}

	tokens := make([]JoinToken, 0, len(facts))
	for _, fact := range facts {
		var token JoinToken
		if err := json.Unmarshal(fact.Value, &token); err != nil {
			continue
		}
		if time.Now().Before(token.ExpiresAt) {
			// The stored Token field is already just the prefix (8 chars).
			// Pad with asterisks for display consistency.
			token.Token += strings.Repeat("*", 56)
			tokens = append(tokens, token)
		}
	}
	return tokens, nil
}

// computeCAFingerprint returns the SHA256 hex digest of the DER-encoded CA
// certificate extracted from the given PEM data. Returns an empty string if the
// PEM cannot be decoded.
func computeCAFingerprint(caCertificatePEM []byte) string {
	pemBlock, _ := pem.Decode(caCertificatePEM)
	if pemBlock == nil {
		return ""
	}
	fingerprintHash := sha256.Sum256(pemBlock.Bytes)
	return hex.EncodeToString(fingerprintHash[:])
}
