package api

import (
	"context"
	"testing"

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
