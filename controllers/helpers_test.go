// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package controllers

import "testing"

func TestParseResourceQuantity(t *testing.T) {
	testCases := []struct {
		input    string
		expected int
	}{
		{"500m", 500},
		{"1000m", 1000},
		{"0m", 0},
		{"256Mi", 256 * 1024 * 1024},
		{"512Mi", 512 * 1024 * 1024},
		{"1Gi", 1024 * 1024 * 1024},
		{"2Gi", 2 * 1024 * 1024 * 1024},
		{"100Ki", 100 * 1024},
		{"3", 3},
		{"1024", 1024},
		{"0", 0},
		{"", 0},
		{"bogus", 0},
	}
	for _, testCase := range testCases {
		result := parseResourceQuantity(testCase.input)
		if result != testCase.expected {
			t.Errorf("parseResourceQuantity(%q): got %d, want %d", testCase.input, result, testCase.expected)
		}
	}
}
