package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/store"
)

// JoinTokenPrefix is the fact store prefix for node join tokens.
const JoinTokenPrefix = "/ccattler/enrollment/token/"

// EnrolledNodePrefix is the fact store prefix for enrolled node records.
const EnrolledNodePrefix = "/ccattler/enrollment/node/"

// JoinToken represents a token that authorizes a node to join the cluster.
type JoinToken struct {
	Token     string    // hex-encoded token value
	NodeID    string    // the node ID this token is for (empty means any node)
	ExpiresAt time.Time // when the token expires
}

// EnrolledNode records a node that has successfully joined the cluster.
type EnrolledNode struct {
	NodeID       string    // unique node identifier
	Principal    string    // security principal (e.g. "node:node-1")
	EnrolledAt   time.Time // when the node joined
	CertExpiry   time.Time // when the node's certificate expires
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

	joinToken := &JoinToken{
		Token:     tokenValue,
		NodeID:    nodeID,
		ExpiresAt: expiresAt,
	}

	tokenJSON, err := json.Marshal(joinToken)
	if err != nil {
		return nil, fmt.Errorf("marshal token: %w", err)
	}

	tokenKey := JoinTokenPrefix + tokenValue
	if _, err := enrollmentService.factStore.Put(ctx, tokenKey, tokenJSON); err != nil {
		return nil, fmt.Errorf("store join token: %w", err)
	}

	return joinToken, nil
}

// RevokeJoinToken removes a join token from the store.
func (enrollmentService *EnrollmentService) RevokeJoinToken(ctx context.Context, tokenValue string) error {
	return enrollmentService.factStore.Delete(ctx, JoinTokenPrefix+tokenValue)
}

// EnrollmentRequest is the request a node sends to join the cluster.
type EnrollmentRequest struct {
	Token       string   // join token value
	NodeID      string   // requested node ID
	IPAddresses []net.IP // node's IP addresses for the certificate
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
// and destroys the join token.
func (enrollmentService *EnrollmentService) EnrollNode(ctx context.Context, request EnrollmentRequest) (*EnrollmentResponse, error) {
	tokenKey := JoinTokenPrefix + request.Token
	tokenFact, err := enrollmentService.factStore.Get(ctx, tokenKey)
	if err != nil {
		return nil, fmt.Errorf("invalid join token")
	}

	var joinToken JoinToken
	if err := json.Unmarshal(tokenFact.Value, &joinToken); err != nil {
		return nil, fmt.Errorf("corrupt join token")
	}

	if time.Now().After(joinToken.ExpiresAt) {
		enrollmentService.factStore.Delete(ctx, tokenKey)
		return nil, fmt.Errorf("join token expired")
	}

	if joinToken.NodeID != "" && joinToken.NodeID != request.NodeID {
		return nil, fmt.Errorf("token is for node %q, not %q", joinToken.NodeID, request.NodeID)
	}

	dnsNames := []string{
		request.NodeID,
		request.NodeID + ".ccattler.local",
	}

	issuedCertificate, err := enrollmentService.certificateAuthority.IssueCertificate(IssueCertificateRequest{
		CommonName:  request.NodeID,
		DNSNames:    dnsNames,
		IPAddresses: request.IPAddresses,
		TTL:         enrollmentService.certificateTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("issue certificate: %w", err)
	}

	principal := "node:" + request.NodeID

	if enrollmentService.rbacAuthorizer != nil {
		enrollmentService.rbacAuthorizer.BindRole(RoleBinding{
			Principal: principal,
			RoleName:  "node-agent",
		})
	}

	enrolledNode := EnrolledNode{
		NodeID:     request.NodeID,
		Principal:  principal,
		EnrolledAt: time.Now(),
		CertExpiry: issuedCertificate.NotAfter,
	}
	enrolledJSON, _ := json.Marshal(enrolledNode)
	enrollmentService.factStore.Put(ctx, EnrolledNodePrefix+request.NodeID, enrolledJSON)

	// Destroy the join token (one-time use).
	enrollmentService.factStore.Delete(ctx, tokenKey)

	return &EnrollmentResponse{
		CertificatePEM: issuedCertificate.CertificatePEM,
		PrivateKeyPEM:  issuedCertificate.PrivateKeyPEM,
		CACertPEM:      enrollmentService.certificateAuthority.CACertificatePEM(),
		Principal:      principal,
	}, nil
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

// ListJoinTokens returns all active (non-expired) join tokens.
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
			// Mask the token value for listing — show only prefix.
			if len(token.Token) > 8 {
				token.Token = token.Token[:8] + strings.Repeat("*", len(token.Token)-8)
			}
			tokens = append(tokens, token)
		}
	}
	return tokens, nil
}
