// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestApplyWarnsForIgnoredMaxExtra: design D14. A stateful or per-node service
// never surges, so an explicit max_extra is ignored and the apply says so, on
// both apply paths; a stateless one does not warn.
func TestApplyWarnsForIgnoredMaxExtra(t *testing.T) {
	for applyPath, testServer := range newApplyLimitTestServers(t) {
		statusCode, body := applyAsUser(testServer.apiServer, "admin",
			"service db {\n  image postgres:17\n  instances 1\n  stateful\n  update {\n    max_extra 2\n  }\n}\n", 0)
		if statusCode != http.StatusOK {
			t.Fatalf("%s: apply: HTTP %d %s, want 200", applyPath, statusCode, body)
		}
		for _, wantFragment := range []string{"db", "max_extra", "ignored", "stateful"} {
			if !strings.Contains(body, wantFragment) {
				t.Errorf("%s: apply answer %s does not mention %q", applyPath, body, wantFragment)
			}
		}
		statusCode, body = applyAsUser(testServer.apiServer, "admin",
			"service agentd {\n  image monitor:1.0\n  placement {\n    mode per-node\n  }\n  update {\n    max_extra 1\n  }\n}\n", 0)
		if statusCode != http.StatusOK {
			t.Fatalf("%s: per-node apply: HTTP %d %s, want 200", applyPath, statusCode, body)
		}
		for _, wantFragment := range []string{"agentd", "max_extra", "ignored", "per-node"} {
			if !strings.Contains(body, wantFragment) {
				t.Errorf("%s: per-node apply answer %s does not mention %q", applyPath, body, wantFragment)
			}
		}
		statusCode, body = applyAsUser(testServer.apiServer, "admin",
			"service web {\n  image app:v1\n  instances 2\n  update {\n    max_extra 2\n  }\n}\n", 0)
		if statusCode != http.StatusOK || strings.Contains(body, "ignored") {
			t.Errorf("%s: stateless apply: HTTP %d %s, want 200 without the max_extra warning", applyPath, statusCode, body)
		}
	}
}
