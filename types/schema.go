package types

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// FieldType defines the data type of a schema field.
type FieldType string

const (
	// FieldTypeString accepts any string value.
	FieldTypeString FieldType = "string"

	// FieldTypeInteger accepts values parseable as integers.
	FieldTypeInteger FieldType = "integer"

	// FieldTypeBoolean accepts "true" or "false".
	FieldTypeBoolean FieldType = "boolean"

	// FieldTypeEnum accepts only values from a predefined set.
	FieldTypeEnum FieldType = "enum"
)

// FieldSchema defines a single field within a fact schema. Each field maps
// to a sub-key under the entity's key prefix in the fact store.
type FieldSchema struct {
	// Name is the field identifier, used as the sub-key suffix.
	Name string
	// Type constrains the values accepted for this field.
	Type FieldType
	// Required means the field must be present when validating a fact set.
	Required bool
	// EnumValues lists the allowed values when Type is FieldTypeEnum.
	EnumValues []string
}

// FactSchema defines the structure of a custom fact type. Plugins register
// schemas to declare typed facts that are validated before being committed
// to the store.
type FactSchema struct {
	// Name uniquely identifies this schema (e.g. "firewall_rule", "metric_target").
	Name string
	// KeyPrefix is the fact store prefix where facts of this type live
	// (e.g. "/ccattler/desired/firewall_rule/").
	KeyPrefix string
	// Fields lists the sub-key fields for this fact type.
	Fields []FieldSchema
}

// SchemaRegistry is a thread-safe registry of typed fact schemas. Controllers
// and the policy gate use it to validate facts before committing them.
type SchemaRegistry struct {
	schemas map[string]*FactSchema
	mutex   sync.RWMutex
}

// NewSchemaRegistry creates an empty schema registry.
func NewSchemaRegistry() *SchemaRegistry {
	return &SchemaRegistry{
		schemas: make(map[string]*FactSchema),
	}
}

// Register adds a schema to the registry. Returns an error if a schema with
// the same name already exists or if the schema definition is invalid.
func (registry *SchemaRegistry) Register(schema FactSchema) error {
	if schema.Name == "" {
		return fmt.Errorf("schema name must not be empty")
	}
	if schema.KeyPrefix == "" {
		return fmt.Errorf("schema %q: key prefix must not be empty", schema.Name)
	}
	if !strings.HasSuffix(schema.KeyPrefix, "/") {
		return fmt.Errorf("schema %q: key prefix must end with /", schema.Name)
	}
	if len(schema.Fields) == 0 {
		return fmt.Errorf("schema %q: must have at least one field", schema.Name)
	}

	for _, field := range schema.Fields {
		if field.Name == "" {
			return fmt.Errorf("schema %q: field name must not be empty", schema.Name)
		}
		if field.Type == FieldTypeEnum && len(field.EnumValues) == 0 {
			return fmt.Errorf("schema %q: enum field %q must have at least one allowed value", schema.Name, field.Name)
		}
	}

	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	if _, exists := registry.schemas[schema.Name]; exists {
		return fmt.Errorf("schema %q already registered", schema.Name)
	}

	schemaCopy := schema
	registry.schemas[schema.Name] = &schemaCopy
	return nil
}

// Get returns the schema with the given name, or an error if not found.
func (registry *SchemaRegistry) Get(name string) (*FactSchema, error) {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()

	schema, exists := registry.schemas[name]
	if !exists {
		return nil, fmt.Errorf("schema %q not found", name)
	}
	return schema, nil
}

// Unregister removes a schema from the registry.
func (registry *SchemaRegistry) Unregister(name string) error {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	if _, exists := registry.schemas[name]; !exists {
		return fmt.Errorf("schema %q not found", name)
	}
	delete(registry.schemas, name)
	return nil
}

// Validate checks a set of field name-value pairs against the named schema.
// Returns an error describing the first validation failure, or nil if valid.
func (registry *SchemaRegistry) Validate(schemaName string, fields map[string]string) error {
	registry.mutex.RLock()
	schema, exists := registry.schemas[schemaName]
	registry.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("schema %q not found", schemaName)
	}

	fieldSchemaMap := make(map[string]FieldSchema, len(schema.Fields))
	for _, field := range schema.Fields {
		fieldSchemaMap[field.Name] = field
	}

	// Check required fields are present.
	for _, field := range schema.Fields {
		if field.Required {
			if _, present := fields[field.Name]; !present {
				return fmt.Errorf("schema %q: required field %q is missing", schemaName, field.Name)
			}
		}
	}

	// Validate provided field values against their type constraints.
	for fieldName, fieldValue := range fields {
		fieldSchema, known := fieldSchemaMap[fieldName]
		if !known {
			return fmt.Errorf("schema %q: unknown field %q", schemaName, fieldName)
		}

		if err := validateFieldValue(fieldSchema, fieldValue); err != nil {
			return fmt.Errorf("schema %q: field %q: %w", schemaName, fieldName, err)
		}
	}

	return nil
}

// ValidateFactKey checks whether a fact store key matches a registered schema
// and returns the schema name, entity name, and field name. Returns an error
// if the key does not match any registered schema.
func (registry *SchemaRegistry) ValidateFactKey(factKey string) (schemaName string, entityName string, fieldName string, err error) {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()

	for name, schema := range registry.schemas {
		if strings.HasPrefix(factKey, schema.KeyPrefix) {
			relativePath := strings.TrimPrefix(factKey, schema.KeyPrefix)
			parts := strings.SplitN(relativePath, "/", 2)
			if len(parts) == 2 {
				return name, parts[0], parts[1], nil
			}
			if len(parts) == 1 {
				return name, parts[0], "", nil
			}
		}
	}

	return "", "", "", fmt.Errorf("key %q does not match any registered schema", factKey)
}

// ListSchemas returns all registered schemas.
func (registry *SchemaRegistry) ListSchemas() []FactSchema {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()

	schemas := make([]FactSchema, 0, len(registry.schemas))
	for _, schema := range registry.schemas {
		schemas = append(schemas, *schema)
	}
	return schemas
}

// validateFieldValue checks a single value against its field schema type.
func validateFieldValue(fieldSchema FieldSchema, value string) error {
	switch fieldSchema.Type {
	case FieldTypeString:
		return nil

	case FieldTypeInteger:
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("expected integer, got %q", value)
		}
		return nil

	case FieldTypeBoolean:
		if value != "true" && value != "false" {
			return fmt.Errorf("expected boolean (true/false), got %q", value)
		}
		return nil

	case FieldTypeEnum:
		for _, allowed := range fieldSchema.EnumValues {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("value %q not in allowed set %v", value, fieldSchema.EnumValues)

	default:
		return fmt.Errorf("unknown field type %q", fieldSchema.Type)
	}
}
