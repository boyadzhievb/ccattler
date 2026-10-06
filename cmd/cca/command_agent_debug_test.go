// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"testing"

	"github.com/boyadzhievb/ccattler/runtime"
)

// TestTruncateString verifies string truncation with ellipsis.
func TestTruncateString(testContext *testing.T) {
	testCases := []struct {
		input     string
		maxLength int
		expected  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this-is-a-very-long-container-name", 20, "this-is-a-very-long…"},
		{"ab", 1, "a"},
		{"", 5, ""},
	}

	for _, testCase := range testCases {
		result := truncateString(testCase.input, testCase.maxLength)
		if result != testCase.expected {
			testContext.Errorf("truncateString(%q, %d) = %q, want %q",
				testCase.input, testCase.maxLength, result, testCase.expected)
		}
	}
}

// TestDetectedCommand verifies that NewContainerRuntime exposes the detected CLI.
func TestDetectedCommand(testContext *testing.T) {
	containerRuntime := runtime.NewContainerRuntime()
	detectedCommand := containerRuntime.DetectedCommand()
	validCommands := map[string]bool{"nerdctl": true, "docker": true, "lima": true}
	if !validCommands[detectedCommand] {
		testContext.Errorf("unexpected detected command %q", detectedCommand)
	}
}
