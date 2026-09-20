package security

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestCertificateAuthorityCreation(t *testing.T) {
	certificateAuthority, err := NewCertificateAuthority(24 * time.Hour)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	caPEM := certificateAuthority.CACertificatePEM()
	if len(caPEM) == 0 {
		t.Fatal("empty CA certificate PEM")
	}
	if string(caPEM[:27]) != "-----BEGIN CERTIFICATE-----" {
		t.Fatalf("expected PEM header, got: %s", string(caPEM[:27]))
	}
}

func TestIssueCertificate(t *testing.T) {
	certificateAuthority, err := NewCertificateAuthority(24 * time.Hour)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	issuedCertificate, err := certificateAuthority.IssueCertificate(IssueCertificateRequest{
		CommonName:  "node-1",
		DNSNames:    []string{"node-1.ccattler.local"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		TTL:         1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("issue certificate: %v", err)
	}

	if len(issuedCertificate.CertificatePEM) == 0 {
		t.Fatal("empty certificate PEM")
	}
	if len(issuedCertificate.PrivateKeyPEM) == 0 {
		t.Fatal("empty private key PEM")
	}
	if issuedCertificate.NotAfter.Before(time.Now()) {
		t.Fatal("certificate already expired")
	}
}

func TestMutualTLSHandshake(t *testing.T) {
	certificateAuthority, err := NewCertificateAuthority(24 * time.Hour)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	serverCertificate, err := certificateAuthority.IssueCertificate(IssueCertificateRequest{
		CommonName:  "api-server",
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		TTL:         1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("issue server cert: %v", err)
	}

	clientCertificate, err := certificateAuthority.IssueCertificate(IssueCertificateRequest{
		CommonName: "node-1",
		TTL:        1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("issue client cert: %v", err)
	}

	serverTLSConfig, err := certificateAuthority.ServerTLSConfig(serverCertificate)
	if err != nil {
		t.Fatalf("server TLS config: %v", err)
	}

	clientTLSConfig, err := certificateAuthority.ClientTLSConfig(clientCertificate)
	if err != nil {
		t.Fatalf("client TLS config: %v", err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Write([]byte("pong"))
	})
	go http.Serve(listener, mux)

	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLSConfig},
	}

	resp, err := httpClient.Get("https://" + listener.Addr().String() + "/ping")
	if err != nil {
		t.Fatalf("mTLS request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "pong" {
		t.Fatalf("expected pong, got %s", string(body))
	}
}

func TestMTLSRejectsUnauthenticatedClient(t *testing.T) {
	certificateAuthority, err := NewCertificateAuthority(24 * time.Hour)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	serverCertificate, err := certificateAuthority.IssueCertificate(IssueCertificateRequest{
		CommonName:  "api-server",
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		TTL:         1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("issue server cert: %v", err)
	}

	serverTLSConfig, err := certificateAuthority.ServerTLSConfig(serverCertificate)
	if err != nil {
		t.Fatalf("server TLS config: %v", err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go http.Serve(listener, http.NewServeMux())

	// Client with no certificate should be rejected.
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		}},
	}

	_, err = httpClient.Get("https://" + listener.Addr().String() + "/ping")
	if err == nil {
		t.Fatal("expected TLS handshake failure for unauthenticated client")
	}
}

func TestCertificateRotator(t *testing.T) {
	certificateAuthority, err := NewCertificateAuthority(24 * time.Hour)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	rotator, err := NewCertificateRotator(
		certificateAuthority,
		IssueCertificateRequest{
			CommonName: "node-1",
			TTL:        1 * time.Hour,
		},
		0.7,
	)
	if err != nil {
		t.Fatalf("create rotator: %v", err)
	}

	currentCertificate := rotator.CurrentCertificate()
	if currentCertificate == nil {
		t.Fatal("no initial certificate")
	}

	tlsCert, err := rotator.GetCertificate(nil)
	if err != nil {
		t.Fatalf("get certificate: %v", err)
	}
	if tlsCert == nil {
		t.Fatal("nil TLS certificate")
	}

	clientCert, err := rotator.GetClientCertificate(nil)
	if err != nil {
		t.Fatalf("get client certificate: %v", err)
	}
	if clientCert == nil {
		t.Fatal("nil client TLS certificate")
	}
}

// TestRotatorBasedMTLSServer validates the pattern used by cca server --tls:
// a CertificateRotator provides the server cert via GetCertificate, and the
// tls.Config requires and verifies client certificates from the same CA.
func TestRotatorBasedMTLSServer(t *testing.T) {
	certificateAuthority, err := NewCertificateAuthority(24 * time.Hour)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}

	serverCertRotator, err := NewCertificateRotator(
		certificateAuthority,
		IssueCertificateRequest{
			CommonName:  "ccattler-server",
			DNSNames:    []string{"localhost"},
			IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
			TTL:         1 * time.Hour,
		},
		0.7,
	)
	if err != nil {
		t.Fatalf("create server rotator: %v", err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(certificateAuthority.CACertificatePEM())

	serverTLSConfig := &tls.Config{
		GetCertificate: serverCertRotator.GetCertificate,
		ClientCAs:      caCertPool,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		MinVersion:     tls.VersionTLS13,
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSConfig)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Write([]byte("ok"))
	})
	go http.Serve(listener, mux)

	clientCertificate, err := certificateAuthority.IssueCertificate(IssueCertificateRequest{
		CommonName: "agent-node-1",
		TTL:        1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("issue client cert: %v", err)
	}
	clientTLSConfig, err := certificateAuthority.ClientTLSConfig(clientCertificate)
	if err != nil {
		t.Fatalf("client TLS config: %v", err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLSConfig},
	}
	resp, err := httpClient.Get("https://" + listener.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("mTLS request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("expected ok, got %s", string(body))
	}

	unauthenticatedClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		}},
	}
	_, err = unauthenticatedClient.Get("https://" + listener.Addr().String() + "/health")
	if err == nil {
		t.Fatal("expected TLS handshake failure for unauthenticated client")
	}
}

func TestRBACAuthorizeAllowed(t *testing.T) {
	authorizer := NewRBACAuthorizer()

	authorizer.AddRole(Role{
		Name: "node-agent",
		Rules: []Rule{
			{KeyPrefix: "observed/", Operations: []Permission{PermissionRead, PermissionWrite}},
			{KeyPrefix: "desired/", Operations: []Permission{PermissionRead}},
		},
	})
	authorizer.BindRole(RoleBinding{Principal: "node:node-1", RoleName: "node-agent"})

	if err := authorizer.Authorize("node:node-1", PermissionRead, "observed/instance/i1/state"); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
	if err := authorizer.Authorize("node:node-1", PermissionWrite, "observed/instance/i1/state"); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
	if err := authorizer.Authorize("node:node-1", PermissionRead, "desired/service/web/image"); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
}

func TestRBACAuthorizeDenied(t *testing.T) {
	authorizer := NewRBACAuthorizer()

	authorizer.AddRole(Role{
		Name: "node-agent",
		Rules: []Rule{
			{KeyPrefix: "observed/", Operations: []Permission{PermissionRead, PermissionWrite}},
		},
	})
	authorizer.BindRole(RoleBinding{Principal: "node:node-1", RoleName: "node-agent"})

	// Write to desired/ should be denied for a node agent.
	if err := authorizer.Authorize("node:node-1", PermissionWrite, "desired/service/web/image"); err == nil {
		t.Fatal("expected deny for write to desired/")
	}

	// Delete should be denied (not in node-agent permissions).
	if err := authorizer.Authorize("node:node-1", PermissionDelete, "observed/instance/i1"); err == nil {
		t.Fatal("expected deny for delete")
	}

	// Unknown principal should be denied.
	if err := authorizer.Authorize("node:unknown", PermissionRead, "observed/instance/i1"); err == nil {
		t.Fatal("expected deny for unknown principal")
	}
}

func TestRBACBuiltinRoles(t *testing.T) {
	authorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		authorizer.AddRole(role)
	}

	authorizer.BindRole(RoleBinding{Principal: "admin", RoleName: "cluster-admin"})
	authorizer.BindRole(RoleBinding{Principal: "node:n1", RoleName: "node-agent"})
	authorizer.BindRole(RoleBinding{Principal: "controller:scheduler", RoleName: "scheduler"})

	// Admin can do anything.
	if err := authorizer.Authorize("admin", PermissionDelete, "desired/service/web"); err != nil {
		t.Fatalf("admin should be allowed: %v", err)
	}

	// Node can write observed.
	if err := authorizer.Authorize("node:n1", PermissionWrite, "observed/instance/i1/state"); err != nil {
		t.Fatalf("node should write observed: %v", err)
	}

	// Node cannot write desired.
	if err := authorizer.Authorize("node:n1", PermissionWrite, "desired/service/web/image"); err == nil {
		t.Fatal("node should not write desired")
	}

	// Scheduler can write placement.
	if err := authorizer.Authorize("controller:scheduler", PermissionWrite, "placement/instance/i1"); err != nil {
		t.Fatalf("scheduler should write placement: %v", err)
	}
}

func TestRBACRemoveBinding(t *testing.T) {
	authorizer := NewRBACAuthorizer()
	authorizer.AddRole(Role{
		Name: "reader",
		Rules: []Rule{
			{KeyPrefix: "", Operations: []Permission{PermissionRead}},
		},
	})
	authorizer.BindRole(RoleBinding{Principal: "user:alice", RoleName: "reader"})

	if err := authorizer.Authorize("user:alice", PermissionRead, "desired/"); err != nil {
		t.Fatalf("should be allowed: %v", err)
	}

	authorizer.RemoveBinding("user:alice")

	if err := authorizer.Authorize("user:alice", PermissionRead, "desired/"); err == nil {
		t.Fatal("should be denied after removal")
	}
}

func TestAuthorizedStoreAllowed(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	authorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		authorizer.AddRole(role)
	}
	authorizer.BindRole(RoleBinding{Principal: "node:n1", RoleName: "node-agent"})

	auditLog := NewInMemoryAuditLog(100)
	authorizedStore := NewAuthorizedStore(memoryStore, authorizer, auditLog)

	ctx := WithPrincipal(context.Background(), "node:n1")

	// Node agent can write observed instance state.
	_, err := authorizedStore.Put(ctx, types.KeyObservedInstanceState("i1"), []byte("running"))
	if err != nil {
		t.Fatalf("put should be allowed: %v", err)
	}

	// Node agent can read observed state.
	fact, err := authorizedStore.Get(ctx, types.KeyObservedInstanceState("i1"))
	if err != nil {
		t.Fatalf("get should be allowed: %v", err)
	}
	if string(fact.Value) != "running" {
		t.Fatalf("expected running, got %s", string(fact.Value))
	}

	entries := auditLog.Entries()
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 audit entries, got %d", len(entries))
	}
}

func TestAuthorizedStoreDenied(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	authorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		authorizer.AddRole(role)
	}
	authorizer.BindRole(RoleBinding{Principal: "node:n1", RoleName: "node-agent"})

	auditLog := NewInMemoryAuditLog(100)
	authorizedStore := NewAuthorizedStore(memoryStore, authorizer, auditLog)

	ctx := WithPrincipal(context.Background(), "node:n1")

	// Node agent cannot write to desired/.
	_, err := authorizedStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.28"))
	if err == nil {
		t.Fatal("node should not be able to write desired/")
	}

	// Verify the denial was audited.
	entries := auditLog.Entries()
	foundDenial := false
	for _, entry := range entries {
		if entry.Decision == "deny" {
			foundDenial = true
			break
		}
	}
	if !foundDenial {
		t.Fatal("expected deny audit entry")
	}
}

func TestAuditLogCapacity(t *testing.T) {
	auditLog := NewInMemoryAuditLog(5)

	for i := 0; i < 10; i++ {
		auditLog.Log(AuditEntry{
			Principal: fmt.Sprintf("user:%d", i),
			Action:    "get",
			Target:    "/test",
			Decision:  "allow",
		})
	}

	entries := auditLog.Entries()
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	// Oldest entries should have been evicted; newest should be user:5 through user:9.
	if entries[0].Principal != "user:5" {
		t.Fatalf("expected user:5, got %s", entries[0].Principal)
	}
	if entries[4].Principal != "user:9" {
		t.Fatalf("expected user:9, got %s", entries[4].Principal)
	}
}

func TestSecretStorePutAndGet(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	masterKey, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	secretStore, err := NewSecretStore(memoryStore, masterKey)
	if err != nil {
		t.Fatalf("create secret store: %v", err)
	}

	ctx := context.Background()
	if err := secretStore.PutSecret(ctx, "db-password", []byte("hunter2")); err != nil {
		t.Fatalf("put secret: %v", err)
	}

	plaintext, err := secretStore.GetSecret(ctx, "db-password")
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(plaintext) != "hunter2" {
		t.Fatalf("expected hunter2, got %s", string(plaintext))
	}
}

func TestSecretStoreEncryptionAtRest(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	masterKey, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	secretStore, err := NewSecretStore(memoryStore, masterKey)
	if err != nil {
		t.Fatalf("create secret store: %v", err)
	}

	ctx := context.Background()
	secretStore.PutSecret(ctx, "api-key", []byte("sk-secret-value"))

	// Read the raw fact — it should be base64-encoded ciphertext, not plaintext.
	rawFact, err := memoryStore.Get(ctx, SecretStorePrefix+"api-key")
	if err != nil {
		t.Fatalf("read raw fact: %v", err)
	}
	if string(rawFact.Value) == "sk-secret-value" {
		t.Fatal("secret stored in plaintext!")
	}
}

func TestSecretStoreDeleteAndList(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	masterKey, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	secretStore, err := NewSecretStore(memoryStore, masterKey)
	if err != nil {
		t.Fatalf("create secret store: %v", err)
	}

	ctx := context.Background()
	secretStore.PutSecret(ctx, "secret-a", []byte("value-a"))
	secretStore.PutSecret(ctx, "secret-b", []byte("value-b"))

	names, err := secretStore.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(names))
	}

	secretStore.DeleteSecret(ctx, "secret-a")
	names, err = secretStore.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 secret after delete, got %d", len(names))
	}
}

func TestSecretGrantAuthorization(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	masterKey, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	secretStore, err := NewSecretStore(memoryStore, masterKey)
	if err != nil {
		t.Fatalf("create secret store: %v", err)
	}

	ctx := context.Background()
	secretStore.PutSecret(ctx, "db-password", []byte("hunter2"))

	// No grant yet — service should be denied.
	_, _, err = secretStore.GetSecretForService(ctx, "web", "db-password")
	if err == nil {
		t.Fatal("expected denial without grant")
	}

	// Add a grant via the fact store (as the DSL compiler would).
	memoryStore.Put(ctx, types.KeyDesiredServiceSecret("web", "db-password"), []byte("/run/secrets/db-password"))

	plaintext, mountPath, err := secretStore.GetSecretForService(ctx, "web", "db-password")
	if err != nil {
		t.Fatalf("get with grant: %v", err)
	}
	if string(plaintext) != "hunter2" {
		t.Fatalf("expected hunter2, got %s", string(plaintext))
	}
	if mountPath != "/run/secrets/db-password" {
		t.Fatalf("expected /run/secrets/db-password, got %s", mountPath)
	}

	// Different service without a grant should be denied.
	_, _, err = secretStore.GetSecretForService(ctx, "api", "db-password")
	if err == nil {
		t.Fatal("expected denial for unauthorized service")
	}
}

func TestNetworkPolicyDefaultDeny(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	policyEngine := NewNetworkPolicyEngine(memoryStore)
	ctx := context.Background()

	action, err := policyEngine.Evaluate(ctx, "web", "api", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if action != PolicyDeny {
		t.Fatalf("expected deny by default, got %s", action)
	}
}

func TestNetworkPolicyAllowRule(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	policyEngine := NewNetworkPolicyEngine(memoryStore)
	ctx := context.Background()

	policyEngine.AddRule(ctx, NetworkPolicyRule{
		Name: "web-to-api", SourceService: "web", TargetService: "api",
		Port: 8080, Action: PolicyAllow,
	})

	action, _ := policyEngine.Evaluate(ctx, "web", "api", 8080)
	if action != PolicyAllow {
		t.Fatalf("expected allow, got %s", action)
	}

	// Different source should still be denied.
	action, _ = policyEngine.Evaluate(ctx, "worker", "api", 8080)
	if action != PolicyDeny {
		t.Fatalf("expected deny for worker, got %s", action)
	}
}

func TestNetworkPolicyWildcardSource(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	policyEngine := NewNetworkPolicyEngine(memoryStore)
	ctx := context.Background()

	policyEngine.AddRule(ctx, NetworkPolicyRule{
		Name: "any-to-api", SourceService: "*", TargetService: "api",
		Port: 0, Action: PolicyAllow,
	})

	action, _ := policyEngine.Evaluate(ctx, "web", "api", 8080)
	if action != PolicyAllow {
		t.Fatalf("expected allow with wildcard, got %s", action)
	}

	action, _ = policyEngine.Evaluate(ctx, "worker", "api", 9090)
	if action != PolicyAllow {
		t.Fatalf("expected allow with wildcard on any port, got %s", action)
	}
}

func TestNetworkPolicyDenyOverridesAllow(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	policyEngine := NewNetworkPolicyEngine(memoryStore)
	ctx := context.Background()

	policyEngine.AddRule(ctx, NetworkPolicyRule{
		Name: "allow-all", SourceService: "*", TargetService: "db",
		Port: 0, Action: PolicyAllow,
	})
	policyEngine.AddRule(ctx, NetworkPolicyRule{
		Name: "deny-worker", SourceService: "worker", TargetService: "db",
		Port: 0, Action: PolicyDeny,
	})

	action, _ := policyEngine.Evaluate(ctx, "worker", "db", 5432)
	if action != PolicyDeny {
		t.Fatalf("expected deny to override allow, got %s", action)
	}

	action, _ = policyEngine.Evaluate(ctx, "web", "db", 5432)
	if action != PolicyAllow {
		t.Fatalf("expected allow for web, got %s", action)
	}
}

func TestNetworkPolicyRemoveRule(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	policyEngine := NewNetworkPolicyEngine(memoryStore)
	ctx := context.Background()

	policyEngine.AddRule(ctx, NetworkPolicyRule{
		Name: "web-to-api", SourceService: "web", TargetService: "api",
		Port: 8080, Action: PolicyAllow,
	})

	action, _ := policyEngine.Evaluate(ctx, "web", "api", 8080)
	if action != PolicyAllow {
		t.Fatal("expected allow")
	}

	policyEngine.RemoveRule(ctx, "web-to-api")
	action, _ = policyEngine.Evaluate(ctx, "web", "api", 8080)
	if action != PolicyDeny {
		t.Fatal("expected deny after removal")
	}
}

func TestABACTeamIsolation(t *testing.T) {
	authorizer := NewABACAuthorizer()

	authorizer.AddPolicy(ABACPolicy{
		Name:               "platform-team-desired",
		RequiredAttributes: []Attribute{{Key: "team", Value: "platform"}},
		TargetKeyPrefix:    "desired/",
		AllowedOperations:  []Permission{PermissionRead, PermissionWrite},
	})

	authorizer.SetPrincipalAttributes("user:alice", []Attribute{
		{Key: "team", Value: "platform"},
		{Key: "environment", Value: "production"},
	})
	authorizer.SetPrincipalAttributes("user:bob", []Attribute{
		{Key: "team", Value: "frontend"},
	})

	if err := authorizer.Authorize("user:alice", PermissionWrite, "desired/service/web/image"); err != nil {
		t.Fatalf("platform team should be allowed: %v", err)
	}

	if err := authorizer.Authorize("user:bob", PermissionWrite, "desired/service/web/image"); err == nil {
		t.Fatal("frontend team should be denied write to desired/")
	}
}

func TestABACProductionGate(t *testing.T) {
	authorizer := NewABACAuthorizer()

	authorizer.AddPolicy(ABACPolicy{
		Name: "production-write",
		RequiredAttributes: []Attribute{
			{Key: "environment", Value: "production"},
			{Key: "role", Value: "deployer"},
		},
		TargetKeyPrefix:   "desired/",
		AllowedOperations: []Permission{PermissionWrite},
	})

	authorizer.SetPrincipalAttributes("user:deployer", []Attribute{
		{Key: "environment", Value: "production"},
		{Key: "role", Value: "deployer"},
	})
	authorizer.SetPrincipalAttributes("user:dev", []Attribute{
		{Key: "environment", Value: "staging"},
		{Key: "role", Value: "deployer"},
	})

	if err := authorizer.Authorize("user:deployer", PermissionWrite, "desired/service/web/image"); err != nil {
		t.Fatalf("prod deployer should be allowed: %v", err)
	}

	if err := authorizer.Authorize("user:dev", PermissionWrite, "desired/service/web/image"); err == nil {
		t.Fatal("staging deployer should be denied production writes")
	}
}

func TestCombinedRBACAndABAC(t *testing.T) {
	rbacAuthorizer := NewRBACAuthorizer()
	rbacAuthorizer.AddRole(Role{
		Name: "reader",
		Rules: []Rule{
			{KeyPrefix: "", Operations: []Permission{PermissionRead}},
		},
	})
	rbacAuthorizer.BindRole(RoleBinding{Principal: "user:alice", RoleName: "reader"})

	abacAuthorizer := NewABACAuthorizer()
	abacAuthorizer.AddPolicy(ABACPolicy{
		Name:               "team-write",
		RequiredAttributes: []Attribute{{Key: "team", Value: "platform"}},
		TargetKeyPrefix:    "desired/",
		AllowedOperations:  []Permission{PermissionWrite},
	})
	abacAuthorizer.SetPrincipalAttributes("user:alice", []Attribute{{Key: "team", Value: "platform"}})

	combined := NewCombinedAuthorizer(rbacAuthorizer, abacAuthorizer)

	// Read allowed via RBAC.
	if err := combined.Authorize("user:alice", PermissionRead, "desired/service/web"); err != nil {
		t.Fatalf("read should be allowed via RBAC: %v", err)
	}

	// Write allowed via ABAC (RBAC denies it, but ABAC allows).
	if err := combined.Authorize("user:alice", PermissionWrite, "desired/service/web"); err != nil {
		t.Fatalf("write should be allowed via ABAC: %v", err)
	}

	// Delete denied by both.
	if err := combined.Authorize("user:alice", PermissionDelete, "desired/service/web"); err == nil {
		t.Fatal("delete should be denied by both RBAC and ABAC")
	}
}

func TestBootstrapTokenLifecycle(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	if IsBootstrapComplete(ctx, memoryStore) {
		t.Fatal("bootstrap should not be complete yet")
	}

	result, err := GenerateBootstrapToken(ctx, memoryStore, 5*time.Minute)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(result.Token) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(result.Token))
	}

	// Wrong token should fail.
	_, err = ValidateBootstrapToken(ctx, memoryStore, "wrong-token")
	if err == nil {
		t.Fatal("wrong token should fail")
	}

	// Correct token should succeed and return admin principal.
	principal, err := ValidateBootstrapToken(ctx, memoryStore, result.Token)
	if err != nil {
		t.Fatalf("valid token failed: %v", err)
	}
	if principal != "admin" {
		t.Fatalf("expected admin, got %s", principal)
	}

	if !IsBootstrapComplete(ctx, memoryStore) {
		t.Fatal("bootstrap should be complete after validation")
	}

	// Token should be destroyed — can't be reused.
	_, err = ValidateBootstrapToken(ctx, memoryStore, result.Token)
	if err == nil {
		t.Fatal("token should not be reusable")
	}

	// Can't generate a new token after bootstrap.
	_, err = GenerateBootstrapToken(ctx, memoryStore, 5*time.Minute)
	if err == nil {
		t.Fatal("should not be able to generate after bootstrap")
	}
}

func TestBootstrapTokenExpiry(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	result, err := GenerateBootstrapToken(ctx, memoryStore, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	_, err = ValidateBootstrapToken(ctx, memoryStore, result.Token)
	if err == nil {
		t.Fatal("expired token should fail")
	}
}

func TestEnrollmentFullLifecycle(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	certificateAuthority, _ := NewCertificateAuthority(24 * time.Hour)
	rbacAuthorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		rbacAuthorizer.AddRole(role)
	}

	enrollmentService := NewEnrollmentService(memoryStore, certificateAuthority, rbacAuthorizer, 1*time.Hour)
	ctx := context.Background()

	// Generate a join token.
	joinToken, err := enrollmentService.GenerateJoinToken(ctx, "", 5*time.Minute)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if len(joinToken.Token) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(joinToken.Token))
	}

	// Enroll a node.
	response, err := enrollmentService.EnrollNode(ctx, EnrollmentRequest{
		Token:       joinToken.Token,
		NodeID:      "node-1",
		IPAddresses: []net.IP{net.ParseIP("10.0.0.1")},
	})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}

	if response.Principal != "node:node-1" {
		t.Fatalf("expected node:node-1, got %s", response.Principal)
	}
	if len(response.CertificatePEM) == 0 {
		t.Fatal("no certificate issued")
	}
	if len(response.CACertPEM) == 0 {
		t.Fatal("no CA cert returned")
	}

	// Node should be enrolled.
	if !enrollmentService.IsNodeEnrolled(ctx, "node-1") {
		t.Fatal("node-1 should be enrolled")
	}

	// RBAC binding should exist.
	if err := rbacAuthorizer.Authorize("node:node-1", PermissionWrite, "observed/instance/i1/state"); err != nil {
		t.Fatalf("node-1 should have node-agent role: %v", err)
	}

	// Token should be consumed — can't reuse.
	_, err = enrollmentService.EnrollNode(ctx, EnrollmentRequest{
		Token:  joinToken.Token,
		NodeID: "node-2",
	})
	if err == nil {
		t.Fatal("reused token should fail")
	}
}

func TestEnrollmentTokenForSpecificNode(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	certificateAuthority, _ := NewCertificateAuthority(24 * time.Hour)
	enrollmentService := NewEnrollmentService(memoryStore, certificateAuthority, nil, 1*time.Hour)
	ctx := context.Background()

	joinToken, _ := enrollmentService.GenerateJoinToken(ctx, "node-5", 5*time.Minute)

	// Wrong node should be rejected.
	_, err := enrollmentService.EnrollNode(ctx, EnrollmentRequest{
		Token:  joinToken.Token,
		NodeID: "node-99",
	})
	if err == nil {
		t.Fatal("wrong node should be rejected")
	}

	// Correct node should succeed.
	response, err := enrollmentService.EnrollNode(ctx, EnrollmentRequest{
		Token:  joinToken.Token,
		NodeID: "node-5",
	})
	if err != nil {
		t.Fatalf("correct node failed: %v", err)
	}
	if response.Principal != "node:node-5" {
		t.Fatalf("expected node:node-5, got %s", response.Principal)
	}
}

func TestEnrollmentTokenExpiry(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	certificateAuthority, _ := NewCertificateAuthority(24 * time.Hour)
	enrollmentService := NewEnrollmentService(memoryStore, certificateAuthority, nil, 1*time.Hour)
	ctx := context.Background()

	joinToken, _ := enrollmentService.GenerateJoinToken(ctx, "", 1*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	_, err := enrollmentService.EnrollNode(ctx, EnrollmentRequest{
		Token:  joinToken.Token,
		NodeID: "node-1",
	})
	if err == nil {
		t.Fatal("expired token should fail")
	}
}

func TestEnrollmentListAndRemove(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	certificateAuthority, _ := NewCertificateAuthority(24 * time.Hour)
	rbacAuthorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		rbacAuthorizer.AddRole(role)
	}
	enrollmentService := NewEnrollmentService(memoryStore, certificateAuthority, rbacAuthorizer, 1*time.Hour)
	ctx := context.Background()

	token1, _ := enrollmentService.GenerateJoinToken(ctx, "", 5*time.Minute)
	token2, _ := enrollmentService.GenerateJoinToken(ctx, "", 5*time.Minute)

	enrollmentService.EnrollNode(ctx, EnrollmentRequest{Token: token1.Token, NodeID: "node-a"})
	enrollmentService.EnrollNode(ctx, EnrollmentRequest{Token: token2.Token, NodeID: "node-b"})

	nodes, _ := enrollmentService.ListEnrolledNodes(ctx)
	if len(nodes) != 2 {
		t.Fatalf("expected 2 enrolled nodes, got %d", len(nodes))
	}

	enrollmentService.RemoveNode(ctx, "node-a")
	nodes, _ = enrollmentService.ListEnrolledNodes(ctx)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node after removal, got %d", len(nodes))
	}

	// RBAC binding should be removed.
	if err := rbacAuthorizer.Authorize("node:node-a", PermissionRead, "observed/"); err == nil {
		t.Fatal("removed node should not have RBAC binding")
	}
}

func TestOIDCAuthentication(t *testing.T) {
	privateKey, _ := GenerateOIDCKeyPair()

	authenticator := NewOIDCAuthenticator(OIDCConfig{
		Issuer:   "https://auth.example.com",
		Audience: "ccattler",
		ClaimMapping: ClaimMapping{
			PrincipalClaim: "email",
			TeamClaim:      "team",
			RoleClaim:      "role",
		},
	}, &privateKey.PublicKey)

	claims := map[string]interface{}{
		"iss":   "https://auth.example.com",
		"aud":   "ccattler",
		"email": "alice@example.com",
		"team":  "platform",
		"role":  "deployer",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	}

	token, err := CreateTestJWT(privateKey, claims)
	if err != nil {
		t.Fatalf("create JWT: %v", err)
	}

	result, err := authenticator.Authenticate(token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	if result.Principal != "user:alice@example.com" {
		t.Fatalf("expected user:alice@example.com, got %s", result.Principal)
	}

	foundTeam := false
	foundRole := false
	for _, attr := range result.Attributes {
		if attr.Key == "team" && attr.Value == "platform" {
			foundTeam = true
		}
		if attr.Key == "role" && attr.Value == "deployer" {
			foundRole = true
		}
	}
	if !foundTeam {
		t.Fatal("missing team attribute")
	}
	if !foundRole {
		t.Fatal("missing role attribute")
	}
}

func TestOIDCRejectsWrongIssuer(t *testing.T) {
	privateKey, _ := GenerateOIDCKeyPair()

	authenticator := NewOIDCAuthenticator(OIDCConfig{
		Issuer: "https://auth.example.com",
	}, &privateKey.PublicKey)

	claims := map[string]interface{}{
		"iss": "https://evil.com",
		"sub": "alice",
		"exp": float64(time.Now().Add(1 * time.Hour).Unix()),
	}

	token, _ := CreateTestJWT(privateKey, claims)
	_, err := authenticator.Authenticate(token)
	if err == nil {
		t.Fatal("wrong issuer should fail")
	}
}

func TestOIDCRejectsExpiredToken(t *testing.T) {
	privateKey, _ := GenerateOIDCKeyPair()

	authenticator := NewOIDCAuthenticator(OIDCConfig{
		Issuer: "https://auth.example.com",
	}, &privateKey.PublicKey)

	claims := map[string]interface{}{
		"iss": "https://auth.example.com",
		"sub": "alice",
		"exp": float64(time.Now().Add(-1 * time.Hour).Unix()),
	}

	token, _ := CreateTestJWT(privateKey, claims)
	_, err := authenticator.Authenticate(token)
	if err == nil {
		t.Fatal("expired token should fail")
	}
}

func TestOIDCRejectsInvalidSignature(t *testing.T) {
	signingKey, _ := GenerateOIDCKeyPair()
	wrongKey, _ := GenerateOIDCKeyPair()

	authenticator := NewOIDCAuthenticator(OIDCConfig{
		Issuer: "https://auth.example.com",
	}, &wrongKey.PublicKey)

	claims := map[string]interface{}{
		"iss": "https://auth.example.com",
		"sub": "alice",
		"exp": float64(time.Now().Add(1 * time.Hour).Unix()),
	}

	token, _ := CreateTestJWT(signingKey, claims)
	_, err := authenticator.Authenticate(token)
	if err == nil {
		t.Fatal("wrong key should fail")
	}
}

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	if principal := PrincipalFromContext(ctx); principal != "" {
		t.Fatalf("expected empty principal, got %s", principal)
	}

	ctx = WithPrincipal(ctx, "node:n1")
	if principal := PrincipalFromContext(ctx); principal != "node:n1" {
		t.Fatalf("expected node:n1, got %s", principal)
	}
}

func TestWorkloadTokenIssuerCreation(t *testing.T) {
	tokenIssuer, err := NewWorkloadTokenIssuer("https://ccattler.example.com")
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	if tokenIssuer.IssuerURL() != "https://ccattler.example.com" {
		t.Errorf("issuer URL: got %q", tokenIssuer.IssuerURL())
	}
	if tokenIssuer.PublicKey() == nil {
		t.Fatal("public key is nil")
	}
	if tokenIssuer.KeyID() == "" {
		t.Fatal("key ID is empty")
	}
}

func TestMintWorkloadToken(t *testing.T) {
	tokenIssuer, err := NewWorkloadTokenIssuer("https://ccattler.example.com")
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	spiffeID := "spiffe://ccattler/payments/checkout"
	audience := "sts.amazonaws.com"
	tokenString, err := tokenIssuer.MintWorkloadToken(spiffeID, audience, 1*time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if tokenString == "" {
		t.Fatal("token is empty")
	}

	// Verify the token using the existing OIDCAuthenticator
	authenticator := NewOIDCAuthenticator(OIDCConfig{
		Issuer:   "https://ccattler.example.com",
		Audience: "sts.amazonaws.com",
	}, tokenIssuer.PublicKey())

	authResult, err := authenticator.Authenticate(tokenString)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	expectedPrincipal := "user:" + spiffeID
	if authResult.Principal != expectedPrincipal {
		t.Errorf("principal: got %q, want %q", authResult.Principal, expectedPrincipal)
	}
}

func TestMintWorkloadTokenIncludesKeyID(t *testing.T) {
	tokenIssuer, err := NewWorkloadTokenIssuer("https://ccattler.example.com")
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	tokenString, err := tokenIssuer.MintWorkloadToken("spiffe://ccattler/web", "sts.amazonaws.com", 1*time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	// Decode header to check kid
	parts := splitJWT(tokenString)
	if parts == nil {
		t.Fatal("invalid JWT format")
	}

	headerJSON, _ := decodeBase64URL(parts[0])
	var header map[string]string
	if err := jsonUnmarshalMap(headerJSON, &header); err != nil {
		t.Fatalf("parse header: %v", err)
	}

	if header["kid"] != tokenIssuer.KeyID() {
		t.Errorf("kid in header: got %q, want %q", header["kid"], tokenIssuer.KeyID())
	}
	if header["alg"] != "ES256" {
		t.Errorf("alg: got %q, want ES256", header["alg"])
	}
}

func TestOIDCDiscoveryDocument(t *testing.T) {
	tokenIssuer, err := NewWorkloadTokenIssuer("https://ccattler.example.com")
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	discoveryDoc := tokenIssuer.OIDCDiscoveryDocument()
	if discoveryDoc.Issuer != "https://ccattler.example.com" {
		t.Errorf("issuer: got %q", discoveryDoc.Issuer)
	}
	if discoveryDoc.JWKSURI != "https://ccattler.example.com/oidc/jwks" {
		t.Errorf("jwks_uri: got %q", discoveryDoc.JWKSURI)
	}
	if len(discoveryDoc.IDTokenSigningAlgValues) != 1 || discoveryDoc.IDTokenSigningAlgValues[0] != "ES256" {
		t.Errorf("signing algs: got %v", discoveryDoc.IDTokenSigningAlgValues)
	}
}

func TestJWKSDocument(t *testing.T) {
	tokenIssuer, err := NewWorkloadTokenIssuer("https://ccattler.example.com")
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	jwksDoc := tokenIssuer.JWKSDocument()
	if len(jwksDoc.Keys) != 1 {
		t.Fatalf("expected 1 JWK, got %d", len(jwksDoc.Keys))
	}

	jwkEntry := jwksDoc.Keys[0]
	if jwkEntry.KeyType != "EC" {
		t.Errorf("kty: got %q, want EC", jwkEntry.KeyType)
	}
	if jwkEntry.Algorithm != "ES256" {
		t.Errorf("alg: got %q, want ES256", jwkEntry.Algorithm)
	}
	if jwkEntry.Curve != "P-256" {
		t.Errorf("crv: got %q, want P-256", jwkEntry.Curve)
	}
	if jwkEntry.KeyID != tokenIssuer.KeyID() {
		t.Errorf("kid: got %q, want %q", jwkEntry.KeyID, tokenIssuer.KeyID())
	}
	if jwkEntry.X == "" || jwkEntry.Y == "" {
		t.Error("JWK coordinates are empty")
	}
}

func TestJWKSVerifiesMintedToken(t *testing.T) {
	tokenIssuer, err := NewWorkloadTokenIssuer("https://ccattler.example.com")
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	tokenString, err := tokenIssuer.MintWorkloadToken("spiffe://ccattler/web", "sts.amazonaws.com", 1*time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	// Reconstruct public key from JWKS document to verify the token
	jwksDoc := tokenIssuer.JWKSDocument()
	jwkEntry := jwksDoc.Keys[0]

	xBytes, _ := decodeBase64URL(jwkEntry.X)
	yBytes, _ := decodeBase64URL(jwkEntry.Y)

	reconstructedKey := reconstructECPublicKey(xBytes, yBytes)

	authenticator := NewOIDCAuthenticator(OIDCConfig{
		Issuer:   "https://ccattler.example.com",
		Audience: "sts.amazonaws.com",
	}, reconstructedKey)

	_, err = authenticator.Authenticate(tokenString)
	if err != nil {
		t.Fatalf("token minted by issuer should verify against JWKS public key: %v", err)
	}
}

func TestWorkloadTokenIssuerWithExistingKey(t *testing.T) {
	originalIssuer, err := NewWorkloadTokenIssuer("https://ccattler.example.com")
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}

	// Create a second issuer with the same key
	restoredIssuer := NewWorkloadTokenIssuerWithKey("https://ccattler.example.com", originalIssuer.signingKey)

	if restoredIssuer.KeyID() != originalIssuer.KeyID() {
		t.Errorf("key ID should be deterministic: got %q, want %q", restoredIssuer.KeyID(), originalIssuer.KeyID())
	}

	// Token from restored issuer should verify against original public key
	tokenString, err := restoredIssuer.MintWorkloadToken("spiffe://ccattler/web", "test", 1*time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	authenticator := NewOIDCAuthenticator(OIDCConfig{
		Issuer: "https://ccattler.example.com",
	}, originalIssuer.PublicKey())

	_, err = authenticator.Authenticate(tokenString)
	if err != nil {
		t.Fatalf("restored issuer token should verify: %v", err)
	}
}

// --- test helpers for JWT parsing ---

func splitJWT(tokenString string) []string {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil
	}
	return parts
}

func decodeBase64URL(encoded string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(encoded)
}

func jsonUnmarshalMap(data []byte, target *map[string]string) error {
	return json.Unmarshal(data, target)
}

func TestRBACCredentialBrokerRole(t *testing.T) {
	authorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		authorizer.AddRole(role)
	}
	authorizer.BindRole(RoleBinding{Principal: "controller:credential-broker", RoleName: "credential-broker"})

	// Broker can read cloud identity config.
	if err := authorizer.Authorize("controller:credential-broker", PermissionRead, "desired/cloud_identity/payments_s3/provider"); err != nil {
		t.Fatalf("broker should read cloud identities: %v", err)
	}

	// Broker can write derived credentials.
	if err := authorizer.Authorize("controller:credential-broker", PermissionWrite, "derived/credential/inst-1/payments_s3/state"); err != nil {
		t.Fatalf("broker should write derived credentials: %v", err)
	}

	// Broker can write to encrypted credential store.
	if err := authorizer.Authorize("controller:credential-broker", PermissionWrite, "credentials/inst-1/payments_s3"); err != nil {
		t.Fatalf("broker should write credentials: %v", err)
	}

	// Broker cannot write desired service config.
	if err := authorizer.Authorize("controller:credential-broker", PermissionWrite, "desired/service/web/image"); err == nil {
		t.Fatal("broker should not write desired service")
	}
}

func TestRBACNodeAgentCredentialAccess(t *testing.T) {
	authorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		authorizer.AddRole(role)
	}
	authorizer.BindRole(RoleBinding{Principal: "node:n1", RoleName: "node-agent"})

	// Node agent can read credentials.
	if err := authorizer.Authorize("node:n1", PermissionRead, "credentials/inst-1/payments_s3"); err != nil {
		t.Fatalf("node agent should read credentials: %v", err)
	}

	// Node agent can read derived credential state.
	if err := authorizer.Authorize("node:n1", PermissionRead, "derived/credential/inst-1/payments_s3/state"); err != nil {
		t.Fatalf("node agent should read derived credential state: %v", err)
	}

	// Node agent cannot write credentials.
	if err := authorizer.Authorize("node:n1", PermissionWrite, "credentials/inst-1/payments_s3"); err == nil {
		t.Fatal("node agent should not write credentials")
	}
}

func reconstructECPublicKey(xBytes, yBytes []byte) *ecdsa.PublicKey {
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}
}

func TestSimulatorCloudAdapterExchangeToken(t *testing.T) {
	expectedCredential := &CloudCredential{
		Provider:     "aws",
		AccessKeyID:  "AKIAIOSFODNN7EXAMPLE",
		SecretKey:    "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		SessionToken: "FwoGZXIvYXdzEBYaDH...",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}

	simulatorAdapter := NewSimulatorCloudAdapter("aws", expectedCredential)
	if simulatorAdapter.ProviderName() != "aws" {
		t.Errorf("provider: got %q, want aws", simulatorAdapter.ProviderName())
	}

	credential, err := simulatorAdapter.ExchangeToken(context.Background(), "test-jwt", CloudIdentityConfig{
		Name:     "test",
		Provider: "aws",
		Role:     "arn:aws:iam::123456789012:role/test",
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if credential.AccessKeyID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("access key: got %q", credential.AccessKeyID)
	}
	if simulatorAdapter.ExchangeCount() != 1 {
		t.Errorf("exchange count: got %d, want 1", simulatorAdapter.ExchangeCount())
	}
}

func TestAWSSTSAdapterRequiresRole(t *testing.T) {
	adapter := NewAWSSTSAdapter("us-east-1", "")
	_, err := adapter.ExchangeToken(context.Background(), "jwt", CloudIdentityConfig{
		Provider: "aws",
	})
	if err == nil || !strings.Contains(err.Error(), "role ARN is required") {
		t.Fatalf("expected role required error, got: %v", err)
	}
}

func TestGCPSTSAdapterRequiresServiceAccount(t *testing.T) {
	adapter := NewGCPSTSAdapter("")
	_, err := adapter.ExchangeToken(context.Background(), "jwt", CloudIdentityConfig{
		Provider: "gcp",
	})
	if err == nil || !strings.Contains(err.Error(), "service_account is required") {
		t.Fatalf("expected service_account required error, got: %v", err)
	}
}

func TestGCPSTSAdapterRequiresPool(t *testing.T) {
	adapter := NewGCPSTSAdapter("")
	_, err := adapter.ExchangeToken(context.Background(), "jwt", CloudIdentityConfig{
		Provider:       "gcp",
		ServiceAccount: "sa@proj.iam.gserviceaccount.com",
	})
	if err == nil || !strings.Contains(err.Error(), "pool is required") {
		t.Fatalf("expected pool required error, got: %v", err)
	}
}

func TestAzureADAdapterRequiresClientID(t *testing.T) {
	adapter := NewAzureADAdapter("")
	_, err := adapter.ExchangeToken(context.Background(), "jwt", CloudIdentityConfig{
		Provider: "azure",
	})
	if err == nil || !strings.Contains(err.Error(), "client_id is required") {
		t.Fatalf("expected client_id required error, got: %v", err)
	}
}

func TestAzureADAdapterRequiresTenantID(t *testing.T) {
	adapter := NewAzureADAdapter("")
	_, err := adapter.ExchangeToken(context.Background(), "jwt", CloudIdentityConfig{
		Provider: "azure",
		ClientID: "abc-123",
	})
	if err == nil || !strings.Contains(err.Error(), "tenant_id is required") {
		t.Fatalf("expected tenant_id required error, got: %v", err)
	}
}

func TestCredentialStoreRoundTrip(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	masterKey := make([]byte, 32)
	for index := range masterKey {
		masterKey[index] = byte(index)
	}

	credentialStore, err := NewCredentialStore(factStore, masterKey)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	originalCredential := &CloudCredential{
		Provider:     "aws",
		AccessKeyID:  "AKIAIOSFODNN7EXAMPLE",
		SecretKey:    "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		SessionToken: "FwoGZXIvYXdzEBYaDH...",
		ExpiresAt:    time.Unix(1695200000, 0),
	}

	ctx := context.Background()
	if err := credentialStore.PutCredential(ctx, "instance-1", "payments_s3", originalCredential); err != nil {
		t.Fatalf("put: %v", err)
	}

	retrieved, err := credentialStore.GetCredential(ctx, "instance-1", "payments_s3")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if retrieved.Provider != originalCredential.Provider {
		t.Errorf("provider: got %q, want %q", retrieved.Provider, originalCredential.Provider)
	}
	if retrieved.AccessKeyID != originalCredential.AccessKeyID {
		t.Errorf("access key: got %q", retrieved.AccessKeyID)
	}
	if retrieved.SecretKey != originalCredential.SecretKey {
		t.Errorf("secret key: got %q", retrieved.SecretKey)
	}
	if retrieved.SessionToken != originalCredential.SessionToken {
		t.Errorf("session token: got %q", retrieved.SessionToken)
	}
	if !retrieved.ExpiresAt.Equal(originalCredential.ExpiresAt) {
		t.Errorf("expires: got %v, want %v", retrieved.ExpiresAt, originalCredential.ExpiresAt)
	}
}

func TestCredentialStoreDelete(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	masterKey := make([]byte, 32)
	for index := range masterKey {
		masterKey[index] = byte(index)
	}

	credentialStore, err := NewCredentialStore(factStore, masterKey)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()
	credentialStore.PutCredential(ctx, "instance-1", "test_id", &CloudCredential{
		Provider:  "gcp",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})

	if err := credentialStore.DeleteCredential(ctx, "instance-1", "test_id"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err = credentialStore.GetCredential(ctx, "instance-1", "test_id")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestCredentialStoreListCredentials(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	masterKey := make([]byte, 32)
	for index := range masterKey {
		masterKey[index] = byte(index)
	}

	credentialStore, err := NewCredentialStore(factStore, masterKey)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()
	credentialStore.PutCredential(ctx, "inst-a", "id1", &CloudCredential{Provider: "aws", ExpiresAt: time.Now()})
	credentialStore.PutCredential(ctx, "inst-b", "id2", &CloudCredential{Provider: "gcp", ExpiresAt: time.Now()})

	credentialKeys, err := credentialStore.ListCredentials(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(credentialKeys) != 2 {
		t.Fatalf("expected 2 credentials, got %d", len(credentialKeys))
	}
}

func TestCredentialStoreInvalidKeyLength(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()

	_, err := NewCredentialStore(factStore, []byte("short"))
	if err == nil {
		t.Fatal("expected error for short master key")
	}
}

func TestCredentialSerializationRoundTrip(t *testing.T) {
	original := &CloudCredential{
		Provider:     "azure",
		AccessKeyID:  "",
		SecretKey:    "",
		SessionToken: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test",
		ExpiresAt:    time.Unix(1695200000, 0),
	}

	serialized := serializeCredential(original)
	restored, err := deserializeCredential(serialized)
	if err != nil {
		t.Fatalf("deserialize: %v", err)
	}

	if restored.Provider != original.Provider {
		t.Errorf("provider: got %q", restored.Provider)
	}
	if restored.SessionToken != original.SessionToken {
		t.Errorf("session token mismatch")
	}
	if !restored.ExpiresAt.Equal(original.ExpiresAt) {
		t.Errorf("expires mismatch")
	}
}

func TestAuthorizedStoreDeleteDenied(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	authorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		authorizer.AddRole(role)
	}
	authorizer.BindRole(RoleBinding{Principal: "node:n1", RoleName: "node-agent"})

	authorizedStore := NewAuthorizedStore(memoryStore, authorizer, nil)
	setupContext := WithPrincipal(context.Background(), "node:n1")

	err := authorizedStore.Delete(setupContext, types.KeyDesiredServiceImage("web"))
	if err == nil {
		t.Fatal("node agent should not be able to delete desired/ keys")
	}
}

func TestAuthorizedStoreScanDenied(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	authorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		authorizer.AddRole(role)
	}
	authorizer.BindRole(RoleBinding{Principal: "node:n1", RoleName: "node-agent"})

	authorizedStore := NewAuthorizedStore(memoryStore, authorizer, nil)
	nodeContext := WithPrincipal(context.Background(), "node:n1")

	_, scanError := authorizedStore.Scan(nodeContext, "desired/service/")
	if scanError != nil {
		t.Fatalf("node agent should be able to scan desired/service/ for reads: %v", scanError)
	}
}

func TestAuthorizedStoreTransactionDenied(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	authorizer := NewRBACAuthorizer()
	for _, role := range BuiltinRoles() {
		authorizer.AddRole(role)
	}
	authorizer.BindRole(RoleBinding{Principal: "node:n1", RoleName: "node-agent"})

	authorizedStore := NewAuthorizedStore(memoryStore, authorizer, nil)
	nodeContext := WithPrincipal(context.Background(), "node:n1")

	_, transactionError := authorizedStore.Transaction(nodeContext,
		nil,
		[]store.Op{{Type: store.OpPut, Key: types.KeyDesiredServiceImage("web"), Value: []byte("nginx")}},
		nil,
	)
	if transactionError == nil {
		t.Fatal("node agent should not be able to write desired/ via transaction")
	}
}

func TestAuditLogJSONSerialization(t *testing.T) {
	auditLog := NewInMemoryAuditLog(10)
	auditLog.Log(AuditEntry{
		Principal: "user:alice",
		Action:    "put",
		Target:    "desired/service/web/image",
		Decision:  "allow",
	})

	jsonBytes, marshalError := auditLog.MarshalJSON()
	if marshalError != nil {
		t.Fatalf("marshal: %v", marshalError)
	}

	var entries []AuditEntry
	if parseError := json.Unmarshal(jsonBytes, &entries); parseError != nil {
		t.Fatalf("unmarshal: %v", parseError)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Principal != "user:alice" {
		t.Errorf("principal: got %q", entries[0].Principal)
	}
	if entries[0].Timestamp.IsZero() {
		t.Error("timestamp should be set")
	}
}

func TestABACRemovePolicy(t *testing.T) {
	authorizer := NewABACAuthorizer()
	authorizer.AddPolicy(ABACPolicy{
		Name:               "team-write",
		RequiredAttributes: []Attribute{{Key: "team", Value: "platform"}},
		TargetKeyPrefix:    "desired/",
		AllowedOperations:  []Permission{PermissionWrite},
	})
	authorizer.SetPrincipalAttributes("user:alice", []Attribute{{Key: "team", Value: "platform"}})

	if err := authorizer.Authorize("user:alice", PermissionWrite, "desired/service/web"); err != nil {
		t.Fatalf("should be allowed before removal: %v", err)
	}

	authorizer.RemovePolicy("team-write")

	if err := authorizer.Authorize("user:alice", PermissionWrite, "desired/service/web"); err == nil {
		t.Fatal("should be denied after policy removal")
	}
}

func TestABACNoPrincipalAttributes(t *testing.T) {
	authorizer := NewABACAuthorizer()
	authorizer.AddPolicy(ABACPolicy{
		Name:               "team-write",
		RequiredAttributes: []Attribute{{Key: "team", Value: "platform"}},
		TargetKeyPrefix:    "desired/",
		AllowedOperations:  []Permission{PermissionWrite},
	})

	if err := authorizer.Authorize("user:unknown", PermissionWrite, "desired/service/web"); err == nil {
		t.Fatal("principal with no attributes should be denied")
	}
}

func TestAuthorizedStoreNoPrincipal(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()

	authorizer := NewRBACAuthorizer()
	authorizedStore := NewAuthorizedStore(memoryStore, authorizer, nil)

	_, getError := authorizedStore.Get(context.Background(), "observed/instance/i1/state")
	if getError == nil {
		t.Fatal("request without principal should be denied")
	}
}
