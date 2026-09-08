package controllers

import (
	"context"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
)

func TestFactMapEntities(t *testing.T) {
	facts := []store.Fact{
		{Key: "/ccattler/desired/service/web/image", Value: []byte("nginx:1.27")},
		{Key: "/ccattler/desired/service/web/instances", Value: []byte("3")},
		{Key: "/ccattler/desired/service/api/image", Value: []byte("api:v2")},
		{Key: "/ccattler/desired/service/api/instances", Value: []byte("5")},
	}

	factMap := NewFactMap(facts, "/ccattler/desired/service/")

	entities := factMap.Entities()
	if len(entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(entities))
	}

	webImage := factMap.GetField("web", "image")
	if webImage != "nginx:1.27" {
		t.Errorf("web image = %q, want nginx:1.27", webImage)
	}

	apiInstances := factMap.GetField("api", "instances")
	if apiInstances != "5" {
		t.Errorf("api instances = %q, want 5", apiInstances)
	}
}

func TestFactMapHasEntity(t *testing.T) {
	facts := []store.Fact{
		{Key: "/prefix/web/image", Value: []byte("nginx")},
	}

	factMap := NewFactMap(facts, "/prefix/")

	if !factMap.HasEntity("web") {
		t.Error("should have entity 'web'")
	}
	if factMap.HasEntity("api") {
		t.Error("should not have entity 'api'")
	}
}

func TestFactMapEntityFields(t *testing.T) {
	facts := []store.Fact{
		{Key: "/prefix/web/image", Value: []byte("nginx")},
		{Key: "/prefix/web/instances", Value: []byte("3")},
	}

	factMap := NewFactMap(facts, "/prefix/")
	fields := factMap.EntityFields("web")

	if len(fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(fields))
	}
	if fields["image"] != "nginx" {
		t.Errorf("image = %q, want nginx", fields["image"])
	}

	// Returned map should be a copy.
	fields["image"] = "changed"
	if factMap.GetField("web", "image") != "nginx" {
		t.Error("EntityFields should return a copy")
	}
}

func TestFactMapGetFieldMissing(t *testing.T) {
	factMap := NewFactMap(nil, "/prefix/")

	value := factMap.GetField("nonexistent", "field")
	if value != "" {
		t.Errorf("expected empty string for missing entity, got %q", value)
	}
}

func TestFactMapIgnoresNonMatchingPrefix(t *testing.T) {
	facts := []store.Fact{
		{Key: "/other/prefix/web/image", Value: []byte("nginx")},
		{Key: "/ccattler/desired/service/api/image", Value: []byte("api:v1")},
	}

	factMap := NewFactMap(facts, "/ccattler/desired/service/")

	entities := factMap.Entities()
	if len(entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(entities))
	}
	if !factMap.HasEntity("api") {
		t.Error("should have entity 'api'")
	}
}

func TestFactMapRaw(t *testing.T) {
	facts := []store.Fact{
		{Key: "/prefix/web/image", Value: []byte("nginx")},
	}

	factMap := NewFactMap(facts, "/prefix/")
	raw := factMap.Raw()
	if len(raw) != 1 {
		t.Fatalf("expected 1 raw fact, got %d", len(raw))
	}
}

func TestCustomControllerInterface(t *testing.T) {
	var reconciled bool

	controller := NewCustomController("test-ctrl", []string{"/ccattler/desired/service/"}, func(ctx context.Context, facts *FactMap) ([]Change, error) {
		reconciled = true

		entities := facts.Entities()
		changes := make([]Change, 0)
		for _, entity := range entities {
			image := facts.GetField(entity, "image")
			if image != "" {
				changes = append(changes, PutChange("/ccattler/processed/"+entity, image))
			}
		}
		return changes, nil
	})

	if controller.Name() != "test-ctrl" {
		t.Errorf("name = %q, want test-ctrl", controller.Name())
	}

	prefixes := controller.Watch()
	if len(prefixes) != 1 || prefixes[0] != "/ccattler/desired/service/" {
		t.Errorf("unexpected prefixes: %v", prefixes)
	}

	facts := []store.Fact{
		{Key: "/ccattler/desired/service/web/image", Value: []byte("nginx:1.27")},
		{Key: "/ccattler/desired/service/api/image", Value: []byte("api:v2")},
	}

	changes, err := controller.Reconcile(context.Background(), facts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !reconciled {
		t.Fatal("reconcile function was not called")
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(changes))
	}
}

func TestCustomControllerWithRunner(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	ctx := context.Background()

	memoryStore.Put(ctx, "/ccattler/desired/custom/item-1/status", []byte("pending"))
	memoryStore.Put(ctx, "/ccattler/desired/custom/item-2/status", []byte("active"))

	controller := NewCustomController("custom", []string{"/ccattler/desired/custom/"}, func(ctx context.Context, facts *FactMap) ([]Change, error) {
		changes := make([]Change, 0)
		for _, entity := range facts.Entities() {
			status := facts.GetField(entity, "status")
			if status == "pending" {
				changes = append(changes, PutChange("/ccattler/observed/custom/"+entity+"/status", "processing"))
			}
		}
		return changes, nil
	})

	// Verify it satisfies the Controller interface by using it with the runner.
	var _ Controller = controller

	// Simulate a reconciliation cycle.
	allFacts, _ := memoryStore.Scan(ctx, "/ccattler/desired/custom/")
	changes, err := controller.Reconcile(ctx, allFacts)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change (only pending item), got %d", len(changes))
	}
	if changes[0].Key != "/ccattler/observed/custom/item-1/status" {
		t.Errorf("key = %q, want /ccattler/observed/custom/item-1/status", changes[0].Key)
	}
}

func TestPutChangeAndDeleteChange(t *testing.T) {
	putChange := PutChange("/key", "value")
	if putChange.Type != store.OpPut {
		t.Error("PutChange should produce OpPut")
	}
	if string(putChange.Value) != "value" {
		t.Errorf("value = %q, want value", string(putChange.Value))
	}

	deleteChange := DeleteChange("/key")
	if deleteChange.Type != store.OpDelete {
		t.Error("DeleteChange should produce OpDelete")
	}
}
