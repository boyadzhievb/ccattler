package types

import "testing"

func TestSchemaRegistryRegisterAndGet(t *testing.T) {
	registry := NewSchemaRegistry()

	schema := FactSchema{
		Name:      "firewall_rule",
		KeyPrefix: "/ccattler/desired/firewall_rule/",
		Fields: []FieldSchema{
			{Name: "source", Type: FieldTypeString, Required: true},
			{Name: "destination", Type: FieldTypeString, Required: true},
			{Name: "port", Type: FieldTypeInteger, Required: true},
			{Name: "action", Type: FieldTypeEnum, Required: true, EnumValues: []string{"allow", "deny"}},
		},
	}

	err := registry.Register(schema)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	retrieved, err := registry.Get("firewall_rule")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if retrieved.Name != "firewall_rule" {
		t.Errorf("name = %q, want firewall_rule", retrieved.Name)
	}
	if len(retrieved.Fields) != 4 {
		t.Errorf("fields = %d, want 4", len(retrieved.Fields))
	}
}

func TestSchemaRegistryDuplicateReject(t *testing.T) {
	registry := NewSchemaRegistry()

	schema := FactSchema{
		Name:      "metric",
		KeyPrefix: "/ccattler/desired/metric/",
		Fields:    []FieldSchema{{Name: "value", Type: FieldTypeString}},
	}

	registry.Register(schema)
	err := registry.Register(schema)
	if err == nil {
		t.Fatal("duplicate registration should fail")
	}
}

func TestSchemaRegistryValidateSuccess(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "firewall_rule",
		KeyPrefix: "/ccattler/desired/firewall_rule/",
		Fields: []FieldSchema{
			{Name: "source", Type: FieldTypeString, Required: true},
			{Name: "destination", Type: FieldTypeString, Required: true},
			{Name: "port", Type: FieldTypeInteger, Required: true},
			{Name: "action", Type: FieldTypeEnum, Required: true, EnumValues: []string{"allow", "deny"}},
		},
	})

	fields := map[string]string{
		"source":      "10.0.0.0/8",
		"destination": "10.1.0.0/16",
		"port":        "443",
		"action":      "allow",
	}

	err := registry.Validate("firewall_rule", fields)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestSchemaRegistryValidateRequiredMissing(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "firewall_rule",
		KeyPrefix: "/ccattler/desired/firewall_rule/",
		Fields: []FieldSchema{
			{Name: "source", Type: FieldTypeString, Required: true},
			{Name: "port", Type: FieldTypeInteger, Required: true},
		},
	})

	fields := map[string]string{
		"source": "10.0.0.0/8",
	}

	err := registry.Validate("firewall_rule", fields)
	if err == nil {
		t.Fatal("should reject missing required field")
	}
}

func TestSchemaRegistryValidateInvalidInteger(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "rule",
		KeyPrefix: "/ccattler/desired/rule/",
		Fields: []FieldSchema{
			{Name: "port", Type: FieldTypeInteger},
		},
	})

	err := registry.Validate("rule", map[string]string{"port": "abc"})
	if err == nil {
		t.Fatal("should reject non-integer value")
	}
}

func TestSchemaRegistryValidateInvalidBoolean(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "toggle",
		KeyPrefix: "/ccattler/desired/toggle/",
		Fields: []FieldSchema{
			{Name: "enabled", Type: FieldTypeBoolean},
		},
	})

	err := registry.Validate("toggle", map[string]string{"enabled": "yes"})
	if err == nil {
		t.Fatal("should reject non-boolean value")
	}

	err = registry.Validate("toggle", map[string]string{"enabled": "true"})
	if err != nil {
		t.Fatalf("should accept 'true': %v", err)
	}
}

func TestSchemaRegistryValidateInvalidEnum(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "rule",
		KeyPrefix: "/ccattler/desired/rule/",
		Fields: []FieldSchema{
			{Name: "action", Type: FieldTypeEnum, EnumValues: []string{"allow", "deny"}},
		},
	})

	err := registry.Validate("rule", map[string]string{"action": "maybe"})
	if err == nil {
		t.Fatal("should reject invalid enum value")
	}
}

func TestSchemaRegistryValidateUnknownField(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "rule",
		KeyPrefix: "/ccattler/desired/rule/",
		Fields: []FieldSchema{
			{Name: "port", Type: FieldTypeInteger},
		},
	})

	err := registry.Validate("rule", map[string]string{"unknown_field": "value"})
	if err == nil {
		t.Fatal("should reject unknown field")
	}
}

func TestSchemaRegistryValidateFactKey(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "firewall_rule",
		KeyPrefix: "/ccattler/desired/firewall_rule/",
		Fields: []FieldSchema{
			{Name: "port", Type: FieldTypeInteger},
		},
	})

	schemaName, entityName, fieldName, err := registry.ValidateFactKey("/ccattler/desired/firewall_rule/rule-1/port")
	if err != nil {
		t.Fatalf("validate key: %v", err)
	}
	if schemaName != "firewall_rule" {
		t.Errorf("schema = %q, want firewall_rule", schemaName)
	}
	if entityName != "rule-1" {
		t.Errorf("entity = %q, want rule-1", entityName)
	}
	if fieldName != "port" {
		t.Errorf("field = %q, want port", fieldName)
	}
}

func TestSchemaRegistryValidateFactKeyNoMatch(t *testing.T) {
	registry := NewSchemaRegistry()

	_, _, _, err := registry.ValidateFactKey("/ccattler/desired/service/web/image")
	if err == nil {
		t.Fatal("should fail for unregistered key prefix")
	}
}

func TestSchemaRegistryListSchemas(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "rule_a",
		KeyPrefix: "/ccattler/desired/rule_a/",
		Fields:    []FieldSchema{{Name: "value", Type: FieldTypeString}},
	})
	registry.Register(FactSchema{
		Name:      "rule_b",
		KeyPrefix: "/ccattler/desired/rule_b/",
		Fields:    []FieldSchema{{Name: "value", Type: FieldTypeString}},
	})

	schemas := registry.ListSchemas()
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schemas, got %d", len(schemas))
	}
}

func TestSchemaRegistryUnregister(t *testing.T) {
	registry := NewSchemaRegistry()

	registry.Register(FactSchema{
		Name:      "temp",
		KeyPrefix: "/ccattler/desired/temp/",
		Fields:    []FieldSchema{{Name: "value", Type: FieldTypeString}},
	})

	err := registry.Unregister("temp")
	if err != nil {
		t.Fatalf("unregister: %v", err)
	}

	_, err = registry.Get("temp")
	if err == nil {
		t.Fatal("unregistered schema should not be found")
	}
}

func TestSchemaRegistryRejectInvalidSchema(t *testing.T) {
	registry := NewSchemaRegistry()

	// Empty name.
	err := registry.Register(FactSchema{Name: "", KeyPrefix: "/p/", Fields: []FieldSchema{{Name: "x", Type: FieldTypeString}}})
	if err == nil {
		t.Fatal("should reject empty name")
	}

	// Empty prefix.
	err = registry.Register(FactSchema{Name: "s", KeyPrefix: "", Fields: []FieldSchema{{Name: "x", Type: FieldTypeString}}})
	if err == nil {
		t.Fatal("should reject empty prefix")
	}

	// Prefix without trailing slash.
	err = registry.Register(FactSchema{Name: "s", KeyPrefix: "/p", Fields: []FieldSchema{{Name: "x", Type: FieldTypeString}}})
	if err == nil {
		t.Fatal("should reject prefix without trailing slash")
	}

	// No fields.
	err = registry.Register(FactSchema{Name: "s", KeyPrefix: "/p/", Fields: []FieldSchema{}})
	if err == nil {
		t.Fatal("should reject schema with no fields")
	}

	// Enum field with no values.
	err = registry.Register(FactSchema{
		Name:      "s",
		KeyPrefix: "/p/",
		Fields:    []FieldSchema{{Name: "x", Type: FieldTypeEnum, EnumValues: []string{}}},
	})
	if err == nil {
		t.Fatal("should reject enum field with no values")
	}
}
