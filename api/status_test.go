package api

import (
	"context"
	"errors"
	"github.com/boyadzhievb/ccattler/security"
	"testing"

	"github.com/boyadzhievb/ccattler/network"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

func TestDiscoverSortedServiceNamesFlat(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredService("web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("web"), []byte("nginx:1.27"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("web"), []byte("3"))
	factStore.Put(ctx, types.KeyDesiredService("api"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("api"), []byte("my-api:v3"))

	serviceNames := discoverSortedServiceNames(mustLoadStatusFactSnapshot(t, ctx, factStore).factsUnder(types.ScanDesiredServices))

	if len(serviceNames) != 2 {
		t.Fatalf("expected 2 services, got %d: %v", len(serviceNames), serviceNames)
	}
	if serviceNames[0] != "api" || serviceNames[1] != "web" {
		t.Errorf("expected [api web], got %v", serviceNames)
	}
}

func TestRedactSensitiveConfigValuePasswordKey(t *testing.T) {
	result := redactSensitiveConfigValue("DATABASE_PASSWORD", "hunter2")
	if result != redactedConfigValuePlaceholder {
		t.Errorf("expected redacted, got %q", result)
	}
}

func TestRedactSensitiveConfigValueTokenKey(t *testing.T) {
	result := redactSensitiveConfigValue("API_TOKEN", "abc123")
	if result != redactedConfigValuePlaceholder {
		t.Errorf("expected redacted, got %q", result)
	}
}

func TestRedactSensitiveConfigValueSecretKey(t *testing.T) {
	result := redactSensitiveConfigValue("MY_SECRET_VALUE", "s3cr3t")
	if result != redactedConfigValuePlaceholder {
		t.Errorf("expected redacted, got %q", result)
	}
}

func TestRedactSensitiveConfigValueApiKeyKey(t *testing.T) {
	result := redactSensitiveConfigValue("STRIPE_API_KEY", "sk_live_xxx")
	if result != redactedConfigValuePlaceholder {
		t.Errorf("expected redacted, got %q", result)
	}
}

func TestRedactSensitiveConfigValueAuthKey(t *testing.T) {
	result := redactSensitiveConfigValue("OAUTH_CLIENT_SECRET", "secret")
	if result != redactedConfigValuePlaceholder {
		t.Errorf("expected redacted, got %q", result)
	}
}

func TestRedactSensitiveConfigValueConnCmdKey(t *testing.T) {
	result := redactSensitiveConfigValue("AIRFLOW__DATABASE__SQL_ALCHEMY_CONN_CMD", "cat /run/connections/db")
	if result != redactedConfigValuePlaceholder {
		t.Errorf("expected redacted, got %q", result)
	}
}

func TestRedactSensitiveConfigValueCaseInsensitive(t *testing.T) {
	result := redactSensitiveConfigValue("db_password", "hunter2")
	if result != redactedConfigValuePlaceholder {
		t.Errorf("expected redacted for lowercase key, got %q", result)
	}
}

func TestRedactSensitiveConfigValueSafeKey(t *testing.T) {
	result := redactSensitiveConfigValue("LOG_LEVEL", "debug")
	if result != "debug" {
		t.Errorf("expected original value %q, got %q", "debug", result)
	}
}

func TestRedactSensitiveConfigValuePortKey(t *testing.T) {
	result := redactSensitiveConfigValue("HTTP_PORT", "8080")
	if result != "8080" {
		t.Errorf("expected original value %q, got %q", "8080", result)
	}
}

func TestDiscoverSortedServiceNamesHierarchical(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyDesiredService("payments/checkout"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("payments/checkout"), []byte("checkout:v1"))
	factStore.Put(ctx, types.KeyDesiredServiceInstances("payments/checkout"), []byte("2"))
	factStore.Put(ctx, types.KeyDesiredService("payments/database"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("payments/database"), []byte("postgres:16"))
	factStore.Put(ctx, types.KeyDesiredService("frontend/web"), []byte(""))
	factStore.Put(ctx, types.KeyDesiredServiceImage("frontend/web"), []byte("nginx:1.27"))

	serviceNames := discoverSortedServiceNames(mustLoadStatusFactSnapshot(t, ctx, factStore).factsUnder(types.ScanDesiredServices))

	if len(serviceNames) != 3 {
		t.Fatalf("expected 3 services, got %d: %v", len(serviceNames), serviceNames)
	}
	expectedNames := []string{"frontend/web", "payments/checkout", "payments/database"}
	for i, expectedName := range expectedNames {
		if serviceNames[i] != expectedName {
			t.Errorf("service[%d]: got %q, want %q", i, serviceNames[i], expectedName)
		}
	}
}

// TestCollectEndpointStatusEnrichesFields verifies that endpoint collection
// populates hostname, internal, and external address fields.
func TestCollectEndpointStatusEnrichesFields(t *testing.T) {
	factStore := store.NewMemoryStore()
	ctx := context.Background()

	factStore.Put(ctx, types.KeyEndpoint("webserver", "ws-1", 8080), []byte("192.168.124.10:8080"))
	factStore.Put(ctx, types.KeyEndpoint("redis", "rd-1", 6379), []byte("10.100.2.3:6379"))

	factStore.Put(ctx, types.KeyObservedInstanceIP("ws-1"), []byte("10.100.1.2"))
	factStore.Put(ctx, types.KeyObservedInstanceIP("rd-1"), []byte("10.100.2.3"))

	factStore.Put(ctx, types.KeyObservedNodeAddress("node-1"), []byte("192.168.124.10"))
	factStore.Put(ctx, types.KeyObservedNodeAddress("node-2"), []byte("192.168.124.20"))

	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "ws-1", NodeID: "node-1"})
	types.WritePlacement(ctx, factStore, types.Placement{InstanceID: "rd-1", NodeID: "node-2"})

	endpoints := mustBuildClusterStatus(t, ctx, factStore).Endpoints

	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(endpoints))
	}

	redisEndpoint := endpoints[0]
	if redisEndpoint.Service != "redis" {
		t.Fatalf("first endpoint service: got %q, want %q", redisEndpoint.Service, "redis")
	}
	if redisEndpoint.Hostname != "redis."+network.DefaultDNSDomain {
		t.Errorf("redis hostname: got %q, want %q", redisEndpoint.Hostname, "redis."+network.DefaultDNSDomain)
	}
	if redisEndpoint.InternalAddress != "10.100.2.3:6379" {
		t.Errorf("redis internal: got %q, want %q", redisEndpoint.InternalAddress, "10.100.2.3:6379")
	}
	if redisEndpoint.ExternalAddress != "" {
		t.Errorf("redis external: got %q, want empty (overlay-only endpoint)", redisEndpoint.ExternalAddress)
	}

	webEndpoint := endpoints[1]
	if webEndpoint.Service != "webserver" {
		t.Fatalf("second endpoint service: got %q, want %q", webEndpoint.Service, "webserver")
	}
	if webEndpoint.Hostname != "webserver."+network.DefaultDNSDomain {
		t.Errorf("webserver hostname: got %q, want %q", webEndpoint.Hostname, "webserver."+network.DefaultDNSDomain)
	}
	if webEndpoint.InternalAddress != "10.100.1.2:8080" {
		t.Errorf("webserver internal: got %q, want %q", webEndpoint.InternalAddress, "10.100.1.2:8080")
	}
	if webEndpoint.ExternalAddress != "192.168.124.10:8080" {
		t.Errorf("webserver external: got %q, want %q", webEndpoint.ExternalAddress, "192.168.124.10:8080")
	}
}

// mustLoadStatusFactSnapshot loads the status snapshot or fails the test.
func mustLoadStatusFactSnapshot(t *testing.T, ctx context.Context, factStore store.StateStore) *statusFactSnapshot {
	t.Helper()
	snapshot, loadError := loadStatusFactSnapshot(ctx, factStore)
	if loadError != nil {
		t.Fatalf("load status snapshot: %v", loadError)
	}
	return snapshot
}

// mustBuildClusterStatus builds the cluster-scope status or fails the test.
func mustBuildClusterStatus(t *testing.T, ctx context.Context, factStore store.StateStore) ClusterStatus {
	t.Helper()
	clusterStatus, buildError := buildStatusFromStore(ctx, factStore, nil, true)
	if buildError != nil {
		t.Fatalf("build status: %v", buildError)
	}
	return clusterStatus
}

// scanFailingStore wraps a StateStore and fails every Scan whose prefix is in
// failingPrefixes, to exercise status error semantics.
type scanFailingStore struct {
	store.StateStore                 // StateStore is the wrapped in-memory store
	failingPrefixes  map[string]bool // failingPrefixes lists the prefixes whose Scan returns an error
}

// Scan fails for configured prefixes and delegates otherwise.
func (failingStore *scanFailingStore) Scan(ctx context.Context, prefix string) ([]store.Fact, error) {
	if failingStore.failingPrefixes[prefix] {
		return nil, errors.New("permission denied")
	}
	return failingStore.StateStore.Scan(ctx, prefix)
}

// TestBuildStatusFailsOnRequiredScanError verifies a failed required scan is
// an error, never a partial status that looks like an empty cluster.
func TestBuildStatusFailsOnRequiredScanError(t *testing.T) {
	ctx := context.Background()
	for _, scanPrefix := range statusScanPrefixes {
		if !scanPrefix.required {
			continue
		}
		failingStore := &scanFailingStore{StateStore: store.NewMemoryStore(), failingPrefixes: map[string]bool{scanPrefix.prefix: true}}
		seedStatusFixture(t, ctx, failingStore, statusFixtureSize{serviceCount: 1, instancesPerService: 1, nodeCount: 1, secretCount: 1})
		if _, buildError := buildStatusFromStore(ctx, failingStore, nil, false); buildError == nil {
			t.Errorf("scan of %s failed but status build returned no error", scanPrefix.prefix)
		}
	}
}

// TestBuildStatusWithoutSecretAccess verifies the api-server case: secrets/
// is denied by the store role, the rest of the status is still served and the
// secret list is empty.
func TestBuildStatusWithoutSecretAccess(t *testing.T) {
	ctx := context.Background()
	failingStore := &scanFailingStore{StateStore: store.NewMemoryStore(), failingPrefixes: map[string]bool{security.SecretStorePrefix: true}}
	seedStatusFixture(t, ctx, failingStore, statusFixtureSize{serviceCount: 1, instancesPerService: 1, nodeCount: 1, secretCount: 1})
	clusterStatus, buildError := buildStatusFromStore(ctx, failingStore, nil, false)
	if buildError != nil {
		t.Fatalf("build status: %v", buildError)
	}
	if len(clusterStatus.Secrets) != 0 || len(clusterStatus.Instances) != 1 {
		t.Fatalf("expected no secrets and 1 instance, got %d secrets, %d instances", len(clusterStatus.Secrets), len(clusterStatus.Instances))
	}
}

// TestBuildStatusSnapshotFields verifies every section built from the
// snapshot carries the per-instance, per-node and per-service facts that the
// per-item store reads used to provide.
func TestBuildStatusSnapshotFields(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	seedStatusFixture(t, ctx, factStore, statusFixtureSize{serviceCount: 2, instancesPerService: 2, nodeCount: 2, secretCount: 1})
	factStore.Put(ctx, types.KeyDerivedInstanceInitPhase("svc00-00"), []byte("done"))
	factStore.Put(ctx, types.KeyDesiredServiceConfigEnv("svc00", "DB_PASSWORD"), []byte("hunter2"))
	factStore.Put(ctx, types.KeyObservedInstanceState("svc01-01"), []byte(string(types.InstanceStopped)))

	clusterStatus := mustBuildClusterStatus(t, ctx, factStore)

	firstInstance := clusterStatus.Instances[0]
	if firstInstance.ID != "svc00-00" || firstInstance.NodeID != "node00" || firstInstance.CPUMillis != "120" ||
		firstInstance.MemoryBytes != "52428800" || firstInstance.Readiness != "passing" || firstInstance.InitPhase != "done" ||
		firstInstance.Restarts != "0" || firstInstance.CreatedAt == "" {
		t.Errorf("instance fields not filled from snapshot: %+v", firstInstance)
	}
	if clusterStatus.Nodes[0].PlacedInstances != 2 || clusterStatus.Nodes[1].PlacedInstances != 1 || clusterStatus.Nodes[0].UtilizationCPU != "25" {
		t.Errorf("node placement counts (stopped excluded) or utilization wrong: %+v", clusterStatus.Nodes)
	}
	firstService := clusterStatus.Services[0]
	if firstService.RunningCount != 2 || firstService.Tags["tier"] != "web" || len(firstService.ExposedPorts) != 1 {
		t.Errorf("service fields wrong: %+v", firstService)
	}
	if len(clusterStatus.Secrets) != 1 || len(clusterStatus.Secrets[0].GrantedTo) != 2 {
		t.Errorf("secret grants wrong: %+v", clusterStatus.Secrets)
	}
	for _, configEntry := range clusterStatus.Config {
		if configEntry.Key == "DB_PASSWORD" && configEntry.Value != redactedConfigValuePlaceholder {
			t.Errorf("sensitive config not redacted: %+v", configEntry)
		}
	}
}

// TestBuildStatusTeamVisibilityUsesOwnerFacts verifies team-scoped status
// keeps only services owned by the team (explicit owner or hierarchical name)
// and their instances.
func TestBuildStatusTeamVisibilityUsesOwnerFacts(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	seedStatusFixture(t, ctx, factStore, statusFixtureSize{serviceCount: 2, instancesPerService: 1, nodeCount: 1, secretCount: 0})
	factStore.Put(ctx, types.KeyDesiredServiceOwner("svc01"), []byte("platform"))

	teamStatus, buildError := buildStatusFromStore(ctx, factStore, &workloadVisibility{teams: []string{"payments"}}, true)
	if buildError != nil {
		t.Fatalf("build status: %v", buildError)
	}
	if len(teamStatus.Services) != 1 || teamStatus.Services[0].Name != "svc00" {
		t.Fatalf("payments should see only svc00, got %+v", teamStatus.Services)
	}
	if len(teamStatus.Instances) != 1 || teamStatus.Instances[0].ServiceName != "svc00" {
		t.Fatalf("payments should see only svc00 instances, got %+v", teamStatus.Instances)
	}
}

// TestStatusSnapshotRejectsUnloadedPrefix verifies a lookup outside every
// loaded prefix panics instead of silently returning nothing.
func TestStatusSnapshotRejectsUnloadedPrefix(t *testing.T) {
	snapshot := mustLoadStatusFactSnapshot(t, context.Background(), store.NewMemoryStore())
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for a prefix the snapshot never loaded")
		}
	}()
	snapshot.factsUnder("audit/")
}

// TestBuildStatusThroughAPIServerStoreRole verifies the production path: the
// API server's store is limited by the api-server role, which must allow every
// required status prefix; secrets/ stays denied and the secret list empty.
func TestBuildStatusThroughAPIServerStoreRole(t *testing.T) {
	ctx := context.Background()
	rawStore := store.NewMemoryStore()
	seedStatusFixture(t, ctx, rawStore, statusFixtureSize{serviceCount: 1, instancesPerService: 1, nodeCount: 1, secretCount: 1})
	rbacAuthorizer := security.NewRBACAuthorizer()
	for _, builtinRole := range security.BuiltinRoles() {
		rbacAuthorizer.AddRole(builtinRole)
	}
	rbacAuthorizer.BindRole(security.RoleBinding{Principal: security.APIServerPrincipal, RoleName: "api-server"})
	apiServerStore := security.NewAuthorizedStore(rawStore, rbacAuthorizer, nil)
	apiServerStore.SetServicePrincipal(security.APIServerPrincipal)

	clusterStatus, buildError := buildStatusFromStore(ctx, apiServerStore, nil, false)
	if buildError != nil {
		t.Fatalf("api-server role must read every required status prefix: %v", buildError)
	}
	if len(clusterStatus.Instances) != 1 || len(clusterStatus.Secrets) != 0 {
		t.Fatalf("expected 1 instance and no secrets, got %d instances, %d secrets", len(clusterStatus.Instances), len(clusterStatus.Secrets))
	}
}
