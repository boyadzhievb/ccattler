package security

import "strings"

// ConditionOperator represents a comparison operator used in ABAC policy conditions.
type ConditionOperator string

const (
	// ConditionOperatorEqual checks exact equality between two values.
	ConditionOperatorEqual ConditionOperator = "=="

	// ConditionOperatorNotEqual checks that two values are not equal.
	ConditionOperatorNotEqual ConditionOperator = "!="

	// ConditionOperatorIn checks whether the left value is a member of a comma-separated list.
	ConditionOperatorIn ConditionOperator = "in"

	// ConditionOperatorNotIn checks whether the left value is NOT a member of a comma-separated list.
	ConditionOperatorNotIn ConditionOperator = "not_in"
)

// Condition represents a single attribute comparison in an ABAC policy.
// Field is a dotted path like "subject.team" or "resource.name". Value is
// either a literal string (e.g. "production") or a field reference
// (e.g. "resource.team") that is resolved at evaluation time.
type Condition struct {
	Field    string            // left-hand side: dotted path (e.g. "subject.team")
	Operator ConditionOperator // comparison operator (==, !=, in, not_in)
	Value    string            // right-hand side: literal or field reference (e.g. "resource.team")
}

// ResourceContext provides attributes about the resource being accessed.
// Controllers and API handlers populate this from fact metadata so that
// ABAC conditions can match on resource properties.
type ResourceContext struct {
	Team string // owning tenant (derived from hierarchical path)
	Name string // resource name (e.g. service name, node ID)
	Type string // resource type (e.g. "service", "node", "secret")
}

// EvaluateConditions checks whether all conditions in the slice evaluate to
// true for the given principal and resource context. Uses AND semantics —
// all conditions must pass. Returns true if the slice is empty. Returns
// false (fail-closed) if any attribute is missing.
func EvaluateConditions(conditions []Condition, principal Principal, resourceContext *ResourceContext) bool {
	for _, condition := range conditions {
		if !evaluateSingleCondition(condition, principal, resourceContext) {
			return false
		}
	}
	return true
}

// evaluateSingleCondition evaluates one condition against the principal and
// resource context. Returns false on missing attributes (fail-closed).
func evaluateSingleCondition(condition Condition, principal Principal, resourceContext *ResourceContext) bool {
	leftValues, leftFound := resolveField(condition.Field, principal, resourceContext)
	if !leftFound {
		return false
	}

	var rightValue string
	if isFieldReference(condition.Value) {
		rightValues, rightFound := resolveField(condition.Value, principal, resourceContext)
		if !rightFound {
			return false
		}
		if len(rightValues) > 0 {
			rightValue = rightValues[0]
		}
	} else {
		rightValue = condition.Value
	}

	if len(leftValues) > 1 {
		return evaluateMultiValueCondition(condition.Operator, leftValues, rightValue)
	}

	leftValue := ""
	if len(leftValues) > 0 {
		leftValue = leftValues[0]
	}

	switch condition.Operator {
	case ConditionOperatorEqual:
		return leftValue == rightValue
	case ConditionOperatorNotEqual:
		return leftValue != rightValue
	case ConditionOperatorIn:
		return isValueInCommaSeparatedList(leftValue, rightValue)
	case ConditionOperatorNotIn:
		return !isValueInCommaSeparatedList(leftValue, rightValue)
	default:
		return false
	}
}

// evaluateMultiValueCondition handles conditions where the left side resolves
// to multiple values (e.g. subject.groups). For == and in, returns true if
// ANY left value matches. For != and not_in, returns true if NO left value matches.
func evaluateMultiValueCondition(operator ConditionOperator, leftValues []string, rightValue string) bool {
	switch operator {
	case ConditionOperatorEqual:
		for _, leftValue := range leftValues {
			if leftValue == rightValue {
				return true
			}
		}
		return false
	case ConditionOperatorNotEqual:
		for _, leftValue := range leftValues {
			if leftValue == rightValue {
				return false
			}
		}
		return true
	case ConditionOperatorIn:
		for _, leftValue := range leftValues {
			if isValueInCommaSeparatedList(leftValue, rightValue) {
				return true
			}
		}
		return false
	case ConditionOperatorNotIn:
		for _, leftValue := range leftValues {
			if isValueInCommaSeparatedList(leftValue, rightValue) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// resolveField resolves a dotted field path like "subject.team" or
// "resource.name" into its value(s). Returns the resolved values and whether
// the field was found. Multi-value fields (like groups) return multiple values.
func resolveField(fieldPath string, principal Principal, resourceContext *ResourceContext) ([]string, bool) {
	dotIndex := strings.IndexByte(fieldPath, '.')
	if dotIndex < 0 {
		return nil, false
	}

	objectName := fieldPath[:dotIndex]
	attributeName := fieldPath[dotIndex+1:]

	switch objectName {
	case "subject":
		return resolveSubjectField(attributeName, principal)
	case "resource":
		return resolveResourceField(attributeName, resourceContext)
	default:
		return nil, false
	}
}

// resolveSubjectField resolves a subject attribute from the Principal struct.
// Built-in fields: "name", "groups". All other attribute names are looked up
// in Principal.Attributes.
func resolveSubjectField(attributeName string, principal Principal) ([]string, bool) {
	switch attributeName {
	case "name":
		if principal.Name == "" {
			return nil, false
		}
		return []string{principal.Name}, true
	case "groups":
		if len(principal.Groups) == 0 {
			return nil, false
		}
		return principal.Groups, true
	default:
		for _, attribute := range principal.Attributes {
			if attribute.Key == attributeName {
				return []string{attribute.Value}, true
			}
		}
		return nil, false
	}
}

// resolveResourceField resolves a resource attribute from ResourceContext.
func resolveResourceField(attributeName string, resourceContext *ResourceContext) ([]string, bool) {
	if resourceContext == nil {
		return nil, false
	}
	switch attributeName {
	case "team":
		if resourceContext.Team == "" {
			return nil, false
		}
		return []string{resourceContext.Team}, true
	case "name":
		if resourceContext.Name == "" {
			return nil, false
		}
		return []string{resourceContext.Name}, true
	case "type":
		if resourceContext.Type == "" {
			return nil, false
		}
		return []string{resourceContext.Type}, true
	default:
		return nil, false
	}
}

// isFieldReference returns true if the value looks like a dotted field path
// that should be resolved rather than treated as a literal string.
func isFieldReference(value string) bool {
	return strings.HasPrefix(value, "subject.") || strings.HasPrefix(value, "resource.")
}

// isValueInCommaSeparatedList checks if needle is one of the comma-separated
// entries in list. Entries are trimmed of whitespace before comparison.
func isValueInCommaSeparatedList(needle string, commaSeparatedList string) bool {
	for _, entry := range strings.Split(commaSeparatedList, ",") {
		if strings.TrimSpace(entry) == needle {
			return true
		}
	}
	return false
}
