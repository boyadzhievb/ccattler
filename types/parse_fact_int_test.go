// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "testing"

// TestParseFactInt verifies that valid integers parse and that corrupt or
// empty values are reported as failures instead of being read as zero.
func TestParseFactInt(t *testing.T) {
	testCases := []struct {
		name          string
		rawValue      string
		expectedValue int
		expectedOK    bool
	}{
		{name: "valid positive", rawValue: "42", expectedValue: 42, expectedOK: true},
		{name: "valid zero", rawValue: "0", expectedValue: 0, expectedOK: true},
		{name: "valid negative", rawValue: "-7", expectedValue: -7, expectedOK: true},
		{name: "corrupt text", rawValue: "abc", expectedValue: 0, expectedOK: false},
		{name: "empty value", rawValue: "", expectedValue: 0, expectedOK: false},
		{name: "trailing garbage", rawValue: "12x", expectedValue: 0, expectedOK: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			parsedValue, parsedOK := ParseFactInt("desired/service/web/instances", testCase.rawValue)
			if parsedOK != testCase.expectedOK {
				t.Fatalf("ParseFactInt(%q) ok = %v, want %v", testCase.rawValue, parsedOK, testCase.expectedOK)
			}
			if parsedValue != testCase.expectedValue {
				t.Errorf("ParseFactInt(%q) = %d, want %d", testCase.rawValue, parsedValue, testCase.expectedValue)
			}
		})
	}
}
