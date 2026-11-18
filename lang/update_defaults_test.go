// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/types"
)

// TestCompileUpdateWritesOnlySetPacing: design D12. An update block that
// leaves max_unavailable and max_extra unset compiles no fact for them (the
// rollout controller applies the 25 % defaults); an explicit 0 compiles and
// formats back as 0.
func TestCompileUpdateWritesOnlySetPacing(t *testing.T) {
	unsetFile, parseError := Parse("service web {\n    image nginx:1.28\n    update {\n        progress_deadline 5m\n    }\n}\n")
	if parseError != nil {
		t.Fatal(parseError)
	}
	unsetFacts, compileError := Compile(unsetFile)
	if compileError != nil {
		t.Fatal(compileError)
	}
	for _, unsetKey := range []string{types.KeyDesiredServiceUpdateMaxUnavailable("web"), types.KeyDesiredServiceUpdateMaxExtra("web")} {
		if unsetValue, hasFact := factMap(unsetFacts)[unsetKey]; hasFact {
			t.Errorf("%s = %q without the field in the update block, want no fact", unsetKey, unsetValue)
		}
	}

	explicitFile, parseError := Parse("service web {\n    image nginx:1.28\n    update {\n        max_unavailable 0\n        max_extra 2\n    }\n}\n")
	if parseError != nil {
		t.Fatal(parseError)
	}
	explicitFacts, compileError := Compile(explicitFile)
	if compileError != nil {
		t.Fatal(compileError)
	}
	if maxUnavailable := factMap(explicitFacts)[types.KeyDesiredServiceUpdateMaxUnavailable("web")]; maxUnavailable != "0" {
		t.Errorf("max_unavailable fact = %q, want 0", maxUnavailable)
	}
	formatted := Format(explicitFile)
	if !strings.Contains(formatted, "        max_unavailable 0\n") || !strings.Contains(formatted, "        max_extra 2\n") {
		t.Errorf("formatted configuration lost explicit pacing:\n%s", formatted)
	}
	verifyIdempotent(t, formatted)
}
