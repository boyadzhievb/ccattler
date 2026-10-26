package api

import (
	"context"
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

	serviceNames := discoverSortedServiceNamesFromStore(ctx, factStore)

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

	serviceNames := discoverSortedServiceNamesFromStore(ctx, factStore)

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

	endpoints := collectEndpointStatusFromStore(ctx, factStore)

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
