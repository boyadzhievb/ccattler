package controllers

import (
	"context"
	"strings"

	"github.com/boyadzhievb/ccattler/store"
)

// FactMap provides structured access to facts organized by entity and field.
// It groups raw fact store entries by their entity name (the path segment after
// the prefix) and field name (the remaining sub-key).
type FactMap struct {
	// entities maps entity names to their field-value pairs.
	entities map[string]map[string]string
	// rawFacts preserves the original facts for direct access.
	rawFacts []store.Fact
}

// NewFactMap organizes a flat list of facts into a structured map. Each fact's
// key is split into an entity name and field name relative to the given prefix.
// Facts that don't match the prefix or have no field component are stored with
// an empty field name.
func NewFactMap(facts []store.Fact, prefix string) *FactMap {
	factMap := &FactMap{
		entities: make(map[string]map[string]string),
		rawFacts: facts,
	}

	for _, fact := range facts {
		if !strings.HasPrefix(fact.Key, prefix) {
			continue
		}
		relativePath := strings.TrimPrefix(fact.Key, prefix)
		parts := strings.SplitN(relativePath, "/", 2)

		entityName := parts[0]
		fieldName := ""
		if len(parts) == 2 {
			fieldName = parts[1]
		}

		if factMap.entities[entityName] == nil {
			factMap.entities[entityName] = make(map[string]string)
		}
		factMap.entities[entityName][fieldName] = string(fact.Value)
	}

	return factMap
}

// Entities returns the names of all entities in the fact map.
func (factMap *FactMap) Entities() []string {
	names := make([]string, 0, len(factMap.entities))
	for name := range factMap.entities {
		names = append(names, name)
	}
	return names
}

// GetField returns the value of a field for the given entity, or empty string
// if not found.
func (factMap *FactMap) GetField(entityName, fieldName string) string {
	entityFields, exists := factMap.entities[entityName]
	if !exists {
		return ""
	}
	return entityFields[fieldName]
}

// HasEntity returns true if the entity exists in the fact map.
func (factMap *FactMap) HasEntity(entityName string) bool {
	_, exists := factMap.entities[entityName]
	return exists
}

// EntityFields returns all field-value pairs for the given entity, or nil
// if the entity is not found.
func (factMap *FactMap) EntityFields(entityName string) map[string]string {
	entityFields, exists := factMap.entities[entityName]
	if !exists {
		return nil
	}
	fieldsCopy := make(map[string]string, len(entityFields))
	for key, value := range entityFields {
		fieldsCopy[key] = value
	}
	return fieldsCopy
}

// Raw returns the unmodified fact list.
func (factMap *FactMap) Raw() []store.Fact {
	return factMap.rawFacts
}

// ReconcileFunc is the user-supplied reconciliation function for a custom
// controller. It receives the current facts organized into a FactMap and
// returns a list of proposed changes.
type ReconcileFunc func(ctx context.Context, facts *FactMap) ([]Change, error)

// CustomController wraps a user-provided reconciliation function in the
// Controller interface. This is the primary building block of the controller
// SDK — plugin authors create controllers by specifying a name, watch
// prefixes, and a reconcile function.
type CustomController struct {
	// controllerName identifies this controller in logs and runner bookkeeping.
	controllerName string
	// watchPrefixes are the fact store key prefixes that trigger reconciliation.
	watchPrefixes []string
	// reconcileFunc is the user-provided logic that computes changes.
	reconcileFunc ReconcileFunc
	// primaryPrefix is the prefix used to organize facts in the FactMap.
	// Defaults to the first watch prefix.
	primaryPrefix string
}

// NewCustomController creates a controller backed by user-provided logic.
// The name identifies the controller in logs. The prefixes determine which
// fact changes trigger reconciliation. The reconcileFn receives organized
// facts and returns proposed changes.
func NewCustomController(name string, prefixes []string, reconcileFn ReconcileFunc) *CustomController {
	primaryPrefix := ""
	if len(prefixes) > 0 {
		primaryPrefix = prefixes[0]
	}
	return &CustomController{
		controllerName: name,
		watchPrefixes:  prefixes,
		reconcileFunc:  reconcileFn,
		primaryPrefix:  primaryPrefix,
	}
}

// SetPrimaryPrefix overrides which prefix is used to organize facts in the
// FactMap. By default, the first watch prefix is used.
func (customController *CustomController) SetPrimaryPrefix(prefix string) {
	customController.primaryPrefix = prefix
}

// Name returns the controller's identifier.
func (customController *CustomController) Name() string {
	return customController.controllerName
}

// Watch returns the fact store prefixes that trigger reconciliation.
func (customController *CustomController) Watch() []string {
	return customController.watchPrefixes
}

// Reconcile organizes the raw facts into a FactMap using the primary prefix,
// then delegates to the user-provided reconcile function.
func (customController *CustomController) Reconcile(ctx context.Context, facts []store.Fact) ([]Change, error) {
	factMap := NewFactMap(facts, customController.primaryPrefix)
	return customController.reconcileFunc(ctx, factMap)
}

// PutChange creates a Change that writes a value to the given key.
func PutChange(key string, value string) Change {
	return Change{Type: store.OpPut, Key: key, Value: []byte(value)}
}

// DeleteChange creates a Change that removes the given key.
func DeleteChange(key string) Change {
	return Change{Type: store.OpDelete, Key: key}
}
