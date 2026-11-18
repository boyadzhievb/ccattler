// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// minHealthyTimeConfiguration returns a service with the given
// update { min_healthy_time } value.
func minHealthyTimeConfiguration(minHealthyTimeText string) string {
	return "service web {\n    image nginx:1.28\n    instances 3\n    update {\n        max_unavailable 1\n        min_healthy_time " +
		minHealthyTimeText + "\n    }\n}\n"
}

// TestCompileUpdateMinHealthyTime: spec service-rollout, "DSL" (M241 D10).
// update { min_healthy_time 30s } compiles and formats back, 0s is allowed,
// and a negative or invalid value is refused by the parser.
func TestCompileUpdateMinHealthyTime(t *testing.T) {
	for _, validText := range []string{"30s", "0s"} {
		parsedFile, parseError := Parse(minHealthyTimeConfiguration(validText))
		if parseError != nil {
			t.Fatalf("min_healthy_time %s: %v", validText, parseError)
		}
		facts, compileError := Compile(parsedFile)
		if compileError != nil {
			t.Fatal(compileError)
		}
		if minHealthyValue := factMap(facts)[types.KeyDesiredServiceUpdateMinHealthyTime("web")]; minHealthyValue != validText {
			t.Errorf("min_healthy_time fact = %q, want %s", minHealthyValue, validText)
		}
		formatted := Format(parsedFile)
		if !strings.Contains(formatted, "        min_healthy_time "+validText) {
			t.Errorf("formatted configuration lacks min_healthy_time %s:\n%s", validText, formatted)
		}
		verifyIdempotent(t, formatted)
	}
	for _, invalidText := range []string{"-1s", "soon", "10"} {
		if _, parseError := Parse(minHealthyTimeConfiguration(invalidText)); parseError == nil ||
			!strings.Contains(parseError.Error(), "min_healthy_time") {
			t.Errorf("min_healthy_time %s: parse error = %v, want an error naming min_healthy_time", invalidText, parseError)
		}
	}
}

// TestCompileUpdateWithoutMinHealthyTime: no min_healthy_time, no fact (the
// rollout controller uses its 10 second default).
func TestCompileUpdateWithoutMinHealthyTime(t *testing.T) {
	parsedFile, parseError := Parse("service web {\n    image nginx:1.28\n    update {\n        max_unavailable 1\n    }\n}\n")
	if parseError != nil {
		t.Fatal(parseError)
	}
	facts, compileError := Compile(parsedFile)
	if compileError != nil {
		t.Fatal(compileError)
	}
	if minHealthyValue, hasValue := factMap(facts)[types.KeyDesiredServiceUpdateMinHealthyTime("web")]; hasValue {
		t.Errorf("min_healthy_time fact = %q without a min_healthy_time, want none", minHealthyValue)
	}
}
