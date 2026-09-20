package types

import (
	"strings"
	"testing"
)

func TestValidateResourceNameValid(t *testing.T) {
	validNames := []string{
		"web",
		"api",
		"my-service",
		"frontend-v2",
		"a",
		"a1",
		"payments-checkout",
		"node-1",
		strings.Repeat("a", 253),
	}

	for _, name := range validNames {
		if validateError := ValidateResourceName(name); validateError != nil {
			t.Errorf("ValidateResourceName(%q) returned error: %v", name, validateError)
		}
	}
}

func TestValidateResourceNameInvalid(t *testing.T) {
	invalidNames := []struct {
		name   string
		reason string
	}{
		{"", "empty"},
		{"-web", "starts with hyphen"},
		{"web-", "ends with hyphen"},
		{"Web", "uppercase"},
		{"my_service", "underscore"},
		{"my.service", "dot"},
		{"my/service", "slash"},
		{"../admin", "path traversal"},
		{"../../desired/service/web", "deep path traversal"},
		{"web\x00admin", "null byte"},
		{"web service", "space"},
		{strings.Repeat("a", 254), "exceeds max length"},
	}

	for _, testCase := range invalidNames {
		if validateError := ValidateResourceName(testCase.name); validateError == nil {
			t.Errorf("ValidateResourceName(%q) should fail (%s) but returned nil", testCase.name, testCase.reason)
		}
	}
}
