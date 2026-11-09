// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import "testing"

func TestRequireSecureKeyTransport(t *testing.T) {
	testCases := map[string]bool{
		"https://10.0.0.5:9770":  true,
		"http://127.0.0.1:9770":  true,
		"http://localhost:9770":  true,
		"http://[::1]:9770":      true,
		"http://10.0.0.5:9770":   false,
		"http://api.example.com": false,
	}
	for apiBaseURL, allowed := range testCases {
		if transportError := requireSecureKeyTransport(apiBaseURL); (transportError == nil) != allowed {
			t.Errorf("%s: allowed=%v, error %v", apiBaseURL, allowed, transportError)
		}
	}
}
