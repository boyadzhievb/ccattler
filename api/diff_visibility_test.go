// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/types"
)

// diffVisibilityRuleKey is the policy/network key the configuration below compiles to.
var diffVisibilityRuleKey = types.KeyNetworkPolicyRule("web-to-db-0")

// TestDiffHidesStateTheCallerCannotRead: a stored network rule is outside the
// state API's readable roots. A writer (no cluster.admin) diffing a config
// that declares that rule gets it as "hidden" without the stored value; a
// cluster admin gets the normal "modify" with the old value (review SEC-M3).
func TestDiffHidesStateTheCallerCannotRead(t *testing.T) {
	apiServer, rawStore := newApplyPathServer(t, false)
	if _, putError := rawStore.Put(context.Background(), diffVisibilityRuleKey, []byte("web:db:5432:allow")); putError != nil {
		t.Fatal(putError)
	}
	configuration := "network {\n    deny web -> db port 5432\n}\n"

	writerChanges := diffAsUser(t, apiServer, "writer", configuration)
	writerChange, found := writerChanges[diffVisibilityRuleKey]
	if !found || writerChange.Type != lang.FactChangeHidden || writerChange.OldValue != "" {
		t.Errorf("writer diff of %s = %+v (found %v), want type %q and no old value", diffVisibilityRuleKey, writerChange, found, lang.FactChangeHidden)
	}

	adminChanges := diffAsUser(t, apiServer, "admin", configuration)
	if adminChange := adminChanges[diffVisibilityRuleKey]; adminChange.Type != "modify" || adminChange.OldValue != "web:db:5432:allow" {
		t.Errorf("admin diff of %s = %+v, want modify with the stored value", diffVisibilityRuleKey, adminChange)
	}
}

// diffAsUser posts a configuration to /api/diff as user:userName and returns
// the changes by key; any status other than 200 fails the test.
func diffAsUser(t *testing.T, apiServer *Server, userName string, configuration string) map[string]lang.FactChange {
	t.Helper()
	response := postConfigurationAsUser(apiServer, userName, "/api/diff", map[string]any{"config": configuration})
	if response.Code != http.StatusOK {
		t.Fatalf("diff as %s: HTTP %d %s", userName, response.Code, response.Body.String())
	}
	if strings.Contains(userName, "writer") && strings.Contains(response.Body.String(), "web:db:5432:allow") {
		t.Errorf("diff as %s returned the stored network rule value: %s", userName, response.Body.String())
	}
	var changes []lang.FactChange
	if decodeError := json.Unmarshal(response.Body.Bytes(), &changes); decodeError != nil {
		t.Fatalf("diff as %s: %v", userName, decodeError)
	}
	changesByKey := make(map[string]lang.FactChange, len(changes))
	for _, change := range changes {
		changesByKey[change.Key] = change
	}
	return changesByKey
}
