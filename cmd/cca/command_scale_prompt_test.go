// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestScaleCountPromptRequiresSameCount: spec service-scaling, "Retyped count
// differs". Only the exact count typed again approves; anything else, including
// no answer, does not.
func TestScaleCountPromptRequiresSameCount(t *testing.T) {
	testCases := []struct {
		typedAnswer  string // typedAnswer is what the user types at the prompt.
		wantApproval bool   // wantApproval is whether the count counts as approved.
	}{
		{"100000\n", true},
		{"  100000  \n", true},
		{"10000\n", false},
		{"y\n", false},
		{"yes\n", false},
		{"\n", false},
		{"", false},
	}
	for _, testCase := range testCases {
		var promptOutput bytes.Buffer
		approved := confirmInstanceCountByRetyping(strings.NewReader(testCase.typedAnswer), &promptOutput, "web", 100000)
		if approved != testCase.wantApproval {
			t.Errorf("typed %q: approved=%v, want %v", testCase.typedAnswer, approved, testCase.wantApproval)
		}
		if !strings.Contains(promptOutput.String(), "100000") || !strings.Contains(promptOutput.String(), "600") {
			t.Errorf("prompt %q does not name the count and the limit", promptOutput.String())
		}
	}
}
