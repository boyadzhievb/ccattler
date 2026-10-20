// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

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

func TestValidateSecretNameValid(t *testing.T) {
	validNames := []string{
		"a",
		"db-password",
		"my-secret-v2",
		"payments/db-password",
		"frontend/cdn-key",
		"team/app/secret",
		"a/b/c",
		"a1/b2",
		"airflow.db-password",
		"airflow.admin-password",
		"team.app.secret",
		"a.b/c",
		strings.Repeat("a", 253),
	}

	for _, name := range validNames {
		if validateError := ValidateSecretName(name); validateError != nil {
			t.Errorf("ValidateSecretName(%q) returned error: %v", name, validateError)
		}
	}
}

func TestValidateSecretNameInvalid(t *testing.T) {
	invalidNames := []struct {
		name   string
		reason string
	}{
		{"", "empty"},
		{"-secret", "starts with hyphen"},
		{"secret-", "ends with hyphen"},
		{"Secret", "uppercase"},
		{"my_secret", "underscore"},
		{"../etc/passwd", "path traversal"},
		{"team/../admin", "mid-path traversal"},
		{"secret/..", "trailing path traversal"},
		{"/absolute/path", "leading slash"},
		{"trailing/", "trailing slash"},
		{"team//secret", "empty segment"},
		{"team/-bad/secret", "segment starts with hyphen"},
		{"team/bad-/secret", "segment ends with hyphen"},
		{".leading-dot", "leading dot"},
		{"trailing-dot.", "trailing dot"},
		{"team..secret", "double dot"},
		{"team./secret", "dot then slash"},
		{"secret\x00evil", "null byte"},
		{"secret name", "space"},
		{strings.Repeat("a", 254), "exceeds max length"},
	}

	for _, testCase := range invalidNames {
		if validateError := ValidateSecretName(testCase.name); validateError == nil {
			t.Errorf("ValidateSecretName(%q) should fail (%s) but returned nil", testCase.name, testCase.reason)
		}
	}
}
