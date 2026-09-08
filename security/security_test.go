package security

import (
	"context"
	"crypto/tls"
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
