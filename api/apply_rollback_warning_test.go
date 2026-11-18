// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/boyadzhievb/ccattler/types"
)

// rolledBackTestReason is the rollback reason the apply warning test stores.
const rolledBackTestReason = "3 failures ≥ 3 on app:v2: exited with code 1"

// TestApplyWarnsForRolledBackImage: spec service-rollout, "Same broken image
// applied again" and "Other image applies without warning", on both apply paths.
func TestApplyWarnsForRolledBackImage(t *testing.T) {
	for applyPath, testServer := range newApplyLimitTestServers(t) {
		ctx := context.Background()
		rolledBackFacts := map[string]string{
			types.KeyDerivedServiceRolloutRolledBackImage("web"): "app:v2",
			types.KeyDerivedServiceRolloutRolledBackAt("web"):    time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
			types.KeyDerivedServiceRolloutRollbackReason("web"):  rolledBackTestReason,
		}
		for factKey, factValue := range rolledBackFacts {
			if _, putError := testServer.factStore.Put(ctx, factKey, []byte(factValue)); putError != nil {
				t.Fatal(putError)
			}
		}

		statusCode, body := applyAsUser(testServer.apiServer, "admin", "service web {\n  image app:v2\n  instances 1\n}\n", 0)
		if statusCode != http.StatusOK {
			t.Fatalf("%s: apply of the rolled-back image: HTTP %d %s, want 200", applyPath, statusCode, body)
		}
		for _, wantFragment := range []string{"web", "app:v2", "rolled back 5m", rolledBackTestReason} {
			if !strings.Contains(body, wantFragment) {
				t.Errorf("%s: apply answer %s does not mention %q", applyPath, body, wantFragment)
			}
		}

		statusCode, body = applyAsUser(testServer.apiServer, "admin", "service web {\n  image app:v3\n  instances 1\n}\n", 0)
		if statusCode != http.StatusOK || strings.Contains(body, "rolled back") {
			t.Errorf("%s: apply of another image: HTTP %d %s, want 200 without a rollback warning", applyPath, statusCode, body)
		}
	}
}
