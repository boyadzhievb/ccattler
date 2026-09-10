package security

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
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
			{KeyPrefix: "/ccattler/observed/", Operations: []Permission{PermissionRead, PermissionWrite}},
			{KeyPrefix: "/ccattler/desired/", Operations: []Permission{PermissionRead}},
		},
	})
	authorizer.BindRole(RoleBinding{Principal: "node:node-1", RoleName: "node-agent"})

	if err := authorizer.Authorize("node:node-1", PermissionRead, "/ccattler/observed/instance/i1/state"); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
	if err := authorizer.Authorize("node:node-1", PermissionWrite, "/ccattler/observed/instance/i1/state"); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
	if err := authorizer.Authorize("node:node-1", PermissionRead, "/ccattler/desired/service/web/image"); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
}

func TestRBACAuthorizeDenied(t *testing.T) {
	authorizer := NewRBACAuthorizer()

	authorizer.AddRole(Role{
		Name: "node-agent",
		Rules: []Rule{
			{KeyPrefix: "/ccattler/observed/", Operations: []Permission{PermissionRead, PermissionWrite}},
		},
	})
	authorizer.BindRole(RoleBinding{Principal: "node:node-1", RoleName: "node-agent"})

	// Write to desired/ should be denied for a node agent.
	if err := authorizer.Authorize("node:node-1", PermissionWrite, "/ccattler/desired/service/web/image"); err == nil {
		t.Fatal("expected deny for write to desired/")
	}

	// Delete should be denied (not in node-agent permissions).
	if err := authorizer.Authorize("node:node-1", PermissionDelete, "/ccattler/observed/instance/i1"); err == nil {
		t.Fatal("expected deny for delete")
	}

	// Unknown principal should be denied.
	if err := authorizer.Authorize("node:unknown", PermissionRead, "/ccattler/observed/instance/i1"); err == nil {
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
	if err := authorizer.Authorize("admin", PermissionDelete, "/ccattler/desired/service/web"); err != nil {
		t.Fatalf("admin should be allowed: %v", err)
	}

	// Node can write observed.
	if err := authorizer.Authorize("node:n1", PermissionWrite, "/ccattler/observed/instance/i1/state"); err != nil {
		t.Fatalf("node should write observed: %v", err)
	}

	// Node cannot write desired.
	if err := authorizer.Authorize("node:n1", PermissionWrite, "/ccattler/desired/service/web/image"); err == nil {
		t.Fatal("node should not write desired")
	}

	// Scheduler can write placement.
	if err := authorizer.Authorize("controller:scheduler", PermissionWrite, "/ccattler/placement/instance/i1"); err != nil {
		t.Fatalf("scheduler should write placement: %v", err)
	}
}

func TestRBACRemoveBinding(t *testing.T) {
	authorizer := NewRBACAuthorizer()
	authorizer.AddRole(Role{
		Name: "reader",
		Rules: []Rule{
			{KeyPrefix: "/", Operations: []Permission{PermissionRead}},
		},
	})
	authorizer.BindRole(RoleBinding{Principal: "user:alice", RoleName: "reader"})

	if err := authorizer.Authorize("user:alice", PermissionRead, "/ccattler/desired/"); err != nil {
		t.Fatalf("should be allowed: %v", err)
	}

	authorizer.RemoveBinding("user:alice")

	if err := authorizer.Authorize("user:alice", PermissionRead, "/ccattler/desired/"); err == nil {
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
		TargetKeyPrefix:    "/ccattler/desired/",
		AllowedOperations:  []Permission{PermissionRead, PermissionWrite},
	})

	authorizer.SetPrincipalAttributes("user:alice", []Attribute{
		{Key: "team", Value: "platform"},
		{Key: "environment", Value: "production"},
	})
	authorizer.SetPrincipalAttributes("user:bob", []Attribute{
		{Key: "team", Value: "frontend"},
	})

	if err := authorizer.Authorize("user:alice", PermissionWrite, "/ccattler/desired/service/web/image"); err != nil {
		t.Fatalf("platform team should be allowed: %v", err)
	}

	if err := authorizer.Authorize("user:bob", PermissionWrite, "/ccattler/desired/service/web/image"); err == nil {
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
		TargetKeyPrefix:   "/ccattler/desired/",
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

	if err := authorizer.Authorize("user:deployer", PermissionWrite, "/ccattler/desired/service/web/image"); err != nil {
		t.Fatalf("prod deployer should be allowed: %v", err)
	}

	if err := authorizer.Authorize("user:dev", PermissionWrite, "/ccattler/desired/service/web/image"); err == nil {
		t.Fatal("staging deployer should be denied production writes")
	}
}

func TestCombinedRBACAndABAC(t *testing.T) {
	rbacAuthorizer := NewRBACAuthorizer()
	rbacAuthorizer.AddRole(Role{
		Name: "reader",
		Rules: []Rule{
			{KeyPrefix: "/ccattler/", Operations: []Permission{PermissionRead}},
		},
	})
	rbacAuthorizer.BindRole(RoleBinding{Principal: "user:alice", RoleName: "reader"})

	abacAuthorizer := NewABACAuthorizer()
	abacAuthorizer.AddPolicy(ABACPolicy{
		Name:               "team-write",
		RequiredAttributes: []Attribute{{Key: "team", Value: "platform"}},
		TargetKeyPrefix:    "/ccattler/desired/",
		AllowedOperations:  []Permission{PermissionWrite},
	})
	abacAuthorizer.SetPrincipalAttributes("user:alice", []Attribute{{Key: "team", Value: "platform"}})

	combined := NewCombinedAuthorizer(rbacAuthorizer, abacAuthorizer)

	// Read allowed via RBAC.
	if err := combined.Authorize("user:alice", PermissionRead, "/ccattler/desired/service/web"); err != nil {
		t.Fatalf("read should be allowed via RBAC: %v", err)
	}

	// Write allowed via ABAC (RBAC denies it, but ABAC allows).
	if err := combined.Authorize("user:alice", PermissionWrite, "/ccattler/desired/service/web"); err != nil {
		t.Fatalf("write should be allowed via ABAC: %v", err)
	}

	// Delete denied by both.
	if err := combined.Authorize("user:alice", PermissionDelete, "/ccattler/desired/service/web"); err == nil {
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
	if err := rbacAuthorizer.Authorize("node:node-1", PermissionWrite, "/ccattler/observed/instance/i1/state"); err != nil {
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
	if err := rbacAuthorizer.Authorize("node:node-a", PermissionRead, "/ccattler/observed/"); err == nil {
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
