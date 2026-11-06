// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// legacySecretAPIConfig is a service that opts into the deprecated secret API.
//
//nolint:gosec // G101: DSL fixture, not a credential
const legacySecretAPIConfig = `service web {
    image "nginx:1.27"
    instances 1
    legacy_secret_api true
}
`

func TestLegacySecretAPICompilesToOptInFact(testContext *testing.T) {
	parsedFile, parseError := Parse(legacySecretAPIConfig)
	if parseError != nil {
		testContext.Fatalf("Parse: %v", parseError)
	}
	facts, compileError := Compile(parsedFile)
	if compileError != nil {
		testContext.Fatalf("Compile: %v", compileError)
	}
	foundOptIn := false
	for _, fact := range facts {
		if fact.Key == types.KeyDesiredServiceLegacySecretAPI("web") && fact.Value == "true" {
			foundOptIn = true
		}
	}
	if !foundOptIn {
		testContext.Fatal("legacy_secret_api true must emit desired/service/web/legacy_secret_api = true")
	}
	warnings := DeprecationWarnings(parsedFile)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "deprecated") {
		testContext.Fatalf("expected one deprecation warning, got %+v", warnings)
	}
	if formatted := Format(parsedFile); !strings.Contains(formatted, "legacy_secret_api true") {
		testContext.Fatalf("cca fmt must keep legacy_secret_api:\n%s", formatted)
	}
}

func TestServiceWithoutLegacySecretAPIEmitsNoOptIn(testContext *testing.T) {
	parsedFile, _ := Parse(strings.Replace(legacySecretAPIConfig, "    legacy_secret_api true\n", "", 1))
	facts, _ := Compile(parsedFile)
	for _, fact := range facts {
		if strings.HasSuffix(fact.Key, "/legacy_secret_api") {
			testContext.Fatalf("unexpected opt-in fact %s", fact.Key)
		}
	}
	if warnings := DeprecationWarnings(parsedFile); len(warnings) != 0 {
		testContext.Fatalf("unexpected warnings %+v", warnings)
	}
}
