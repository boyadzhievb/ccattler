// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// newTestEnrollmentService returns an enrollment service on a fresh memory
// store with the builtin RBAC roles loaded.
func newTestEnrollmentService(t *testing.T) (*EnrollmentService, store.StateStore) {
	t.Helper()
	memoryStore := store.NewMemoryStore()
	t.Cleanup(func() { memoryStore.Close() })
	certificateAuthority, caError := NewCertificateAuthority(24 * time.Hour)
	if caError != nil {
		t.Fatalf("create CA: %v", caError)
	}
	rbacAuthorizer := NewRBACAuthorizer()
	for _, builtinRole := range BuiltinRoles() {
		rbacAuthorizer.AddRole(builtinRole)
	}
	return NewEnrollmentService(memoryStore, certificateAuthority, rbacAuthorizer, time.Hour), memoryStore
}

// enrollWithNewToken creates a token bound to boundNodeID (empty = any node)
// and enrolls requestedNodeID with a CSR whose CN is csrCommonName.
func enrollWithNewToken(t *testing.T, enrollmentService *EnrollmentService, boundNodeID, requestedNodeID, csrCommonName string) (*EnrollmentResponse, error) {
	t.Helper()
	joinToken, tokenError := enrollmentService.GenerateJoinToken(context.Background(), boundNodeID, 5*time.Minute)
	if tokenError != nil {
		t.Fatalf("generate token: %v", tokenError)
	}
	return enrollmentService.EnrollNode(context.Background(), EnrollmentRequest{
		Token:  joinToken.Token,
		NodeID: requestedNodeID,
		CSRPEM: generateTestCSR(t, csrCommonName, nil),
	})
}

// parseEnrolledCertificate decodes the certificate PEM from an enrollment response.
func parseEnrolledCertificate(t *testing.T, response *EnrollmentResponse) *x509.Certificate {
	t.Helper()
	certificateBlock, _ := pem.Decode(response.CertificatePEM)
	if certificateBlock == nil {
		t.Fatal("enrollment returned no certificate PEM")
	}
	enrolledCertificate, parseError := x509.ParseCertificate(certificateBlock.Bytes)
	if parseError != nil {
		t.Fatalf("parse enrolled certificate: %v", parseError)
	}
	return enrolledCertificate
}

func TestEnrollNode_CSRCannotChooseIdentity(t *testing.T) {
	enrollmentService, _ := newTestEnrollmentService(t)

	response, enrollError := enrollWithNewToken(t, enrollmentService, "", "worker-9", "ccattler-server")
	if enrollError != nil {
		t.Fatalf("enroll: %v", enrollError)
	}

	enrolledCertificate := parseEnrolledCertificate(t, response)
	if enrolledCertificate.Subject.CommonName != "worker-9" {
		t.Fatalf("CN must be the node ID, got %q", enrolledCertificate.Subject.CommonName)
	}
	if response.Principal != "node:worker-9" {
		t.Fatalf("expected principal node:worker-9, got %q", response.Principal)
	}
	for _, extendedUsage := range enrolledCertificate.ExtKeyUsage {
		if extendedUsage == x509.ExtKeyUsageServerAuth {
			t.Fatal("enrolled node certificate must not carry server-auth")
		}
	}
}

func TestEnrollNode_RejectsInvalidNodeID(t *testing.T) {
	enrollmentService, _ := newTestEnrollmentService(t)

	for _, invalidNodeID := range []string{"", "Node-1", "a/b", "../etc", "node_1", "-node", "node."} {
		if _, enrollError := enrollWithNewToken(t, enrollmentService, "", invalidNodeID, "x"); enrollError == nil {
			t.Errorf("node ID %q: expected enrollment to be rejected", invalidNodeID)
		}
	}
}

func TestEnrollNode_RejectsReservedNodeID(t *testing.T) {
	enrollmentService, _ := newTestEnrollmentService(t)

	_, enrollError := enrollWithNewToken(t, enrollmentService, "ccattler-server", "ccattler-server", "ccattler-server")
	if enrollError == nil || !strings.Contains(enrollError.Error(), "reserved") {
		t.Fatalf("expected reserved node ID rejection, got %v", enrollError)
	}
}

func TestEnrollNode_RejectsNodeIDHoldingGrants(t *testing.T) {
	enrollmentService, memoryStore := newTestEnrollmentService(t)
	if _, putError := memoryStore.Put(context.Background(), types.KeyAuthGrant("node", "ctrl", "cluster-admin"), []byte("true")); putError != nil {
		t.Fatalf("seed grant: %v", putError)
	}

	_, enrollError := enrollWithNewToken(t, enrollmentService, "ctrl", "ctrl", "ctrl")
	if enrollError == nil || !strings.Contains(enrollError.Error(), "holds role grants") {
		t.Fatalf("expected rejection for node ID holding grants, got %v", enrollError)
	}
}

func TestEnrollNode_ReenrollRequiresBoundToken(t *testing.T) {
	enrollmentService, _ := newTestEnrollmentService(t)

	if _, enrollError := enrollWithNewToken(t, enrollmentService, "", "worker-1", "worker-1"); enrollError != nil {
		t.Fatalf("first enrollment: %v", enrollError)
	}
	if _, enrollError := enrollWithNewToken(t, enrollmentService, "", "worker-1", "worker-1"); enrollError == nil {
		t.Fatal("re-enrollment with an unbound token must be rejected")
	}
	if _, enrollError := enrollWithNewToken(t, enrollmentService, "worker-1", "worker-1", "worker-1"); enrollError != nil {
		t.Fatalf("re-enrollment with a bound token: %v", enrollError)
	}
}

func TestEnrollNode_TokenSingleUseUnderConcurrency(t *testing.T) {
	enrollmentService, _ := newTestEnrollmentService(t)
	joinToken, tokenError := enrollmentService.GenerateJoinToken(context.Background(), "", 5*time.Minute)
	if tokenError != nil {
		t.Fatalf("generate token: %v", tokenError)
	}

	const concurrentAttempts = 16
	csrPEMs := make([][]byte, concurrentAttempts)
	for attemptIndex := range csrPEMs {
		csrPEMs[attemptIndex] = generateTestCSR(t, fmt.Sprintf("racer-%d", attemptIndex), nil)
	}

	var waitGroup sync.WaitGroup
	var successMutex sync.Mutex
	successCount := 0
	startSignal := make(chan struct{})
	for attemptIndex := 0; attemptIndex < concurrentAttempts; attemptIndex++ {
		waitGroup.Add(1)
		go func(attemptIndex int) {
			defer waitGroup.Done()
			<-startSignal
			_, enrollError := enrollmentService.EnrollNode(context.Background(), EnrollmentRequest{
				Token:  joinToken.Token,
				NodeID: fmt.Sprintf("racer-%d", attemptIndex),
				CSRPEM: csrPEMs[attemptIndex],
			})
			if enrollError == nil {
				successMutex.Lock()
				successCount++
				successMutex.Unlock()
			}
		}(attemptIndex)
	}
	close(startSignal)
	waitGroup.Wait()

	if successCount != 1 {
		t.Fatalf("one token must enroll exactly one node, got %d successes", successCount)
	}
	enrolledNodes, _ := enrollmentService.ListEnrolledNodes(context.Background())
	if len(enrolledNodes) != 1 {
		t.Fatalf("expected 1 enrolled node record, got %d", len(enrolledNodes))
	}
}
