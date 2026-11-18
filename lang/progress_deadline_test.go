// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// progressDeadlineConfiguration returns a service with the given
// update { progress_deadline } value.
func progressDeadlineConfiguration(deadlineText string) string {
	return "service web {\n    image nginx:1.28\n    instances 3\n    update {\n        max_unavailable 1\n        progress_deadline " +
		deadlineText + "\n    }\n}\n"
}

// TestCompileUpdateProgressDeadline: update { progress_deadline 15m } compiles
// to desired/service/{svc}/update/progress_deadline and formats back (M241 D7).
func TestCompileUpdateProgressDeadline(t *testing.T) {
	parsedFile, parseError := Parse(progressDeadlineConfiguration("15m"))
	if parseError != nil {
		t.Fatal(parseError)
	}
	facts, compileError := Compile(parsedFile)
	if compileError != nil {
		t.Fatal(compileError)
	}
	if deadlineValue := factMap(facts)[types.KeyDesiredServiceUpdateProgressDeadline("web")]; deadlineValue != "15m" {
		t.Errorf("progress_deadline fact = %q, want 15m", deadlineValue)
	}
	formatted := Format(parsedFile)
	if !strings.Contains(formatted, "        progress_deadline 15m") {
		t.Errorf("formatted configuration lacks progress_deadline:\n%s", formatted)
	}
	verifyIdempotent(t, formatted)
}

// TestCompileUpdateWithoutProgressDeadline: no progress_deadline, no fact (the
// controller uses its 10 minute default).
func TestCompileUpdateWithoutProgressDeadline(t *testing.T) {
	parsedFile, parseError := Parse("service web {\n    image nginx:1.28\n    update {\n        max_unavailable 1\n    }\n}\n")
	if parseError != nil {
		t.Fatal(parseError)
	}
	facts, compileError := Compile(parsedFile)
	if compileError != nil {
		t.Fatal(compileError)
	}
	if deadlineValue, hasDeadline := factMap(facts)[types.KeyDesiredServiceUpdateProgressDeadline("web")]; hasDeadline {
		t.Errorf("progress_deadline fact = %q without a progress_deadline, want none", deadlineValue)
	}
}

// TestParseRefusesInvalidProgressDeadline: an invalid or non-positive deadline
// is a parse error, never read as 0 (M241 D7).
func TestParseRefusesInvalidProgressDeadline(t *testing.T) {
	for _, deadlineText := range []string{"soon", "0s", "0", "10"} {
		if _, parseError := Parse(progressDeadlineConfiguration(deadlineText)); parseError == nil ||
			!strings.Contains(parseError.Error(), "progress_deadline") {
			t.Errorf("progress_deadline %s: parse error = %v, want an error naming progress_deadline", deadlineText, parseError)
		}
	}
}
