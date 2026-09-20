package types

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// validResourceNamePattern matches service, node, and instance names: lowercase
// alphanumeric with hyphens allowed, 1-253 characters (same constraint as DNS
// labels). Prevents path traversal and store key injection.
var validResourceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,251}[a-z0-9])?$`)

// MaxResourceNameLength is the maximum allowed length for a resource name.
const MaxResourceNameLength = 253

// ValidateResourceName checks that a name is safe to use as a component of a
// store key path. Returns nil if valid, an error describing the problem
// otherwise. Use this at API boundaries before constructing store keys from
// user-supplied names.
func ValidateResourceName(resourceName string) error {
	if resourceName == "" {
		return fmt.Errorf("resource name must not be empty")
	}
	if len(resourceName) > MaxResourceNameLength {
		return fmt.Errorf("resource name %q exceeds maximum length %d", resourceName, MaxResourceNameLength)
	}
	if !validResourceNamePattern.MatchString(resourceName) {
		return fmt.Errorf("resource name %q is invalid: must be lowercase alphanumeric with hyphens, cannot start or end with hyphen", resourceName)
	}
	return nil
}

// ParseDurationSeconds parses a human-friendly duration string like "60s" or
// "5m" into whole seconds. Falls back to plain integer parsing. Returns 0 if
// the string cannot be parsed.
func ParseDurationSeconds(durationString string) int {
	durationString = strings.TrimSpace(durationString)
	if strings.HasSuffix(durationString, "s") {
		seconds, _ := strconv.Atoi(strings.TrimSuffix(durationString, "s"))
		return seconds
	}
	if strings.HasSuffix(durationString, "m") {
		minutes, _ := strconv.Atoi(strings.TrimSuffix(durationString, "m"))
		return minutes * 60
	}
	seconds, _ := strconv.Atoi(durationString)
	return seconds
}
