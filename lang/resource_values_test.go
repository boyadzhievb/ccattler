// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// compileServiceSource parses and compiles a configuration, failing the test on
// a parse error so only compile errors reach the caller.
func compileServiceSource(t *testing.T, configuration string) ([]Fact, error) {
	t.Helper()
	parsedFile, parseError := Parse(configuration)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}
	return Compile(parsedFile)
}

// assertErrorNamesAll fails the test unless compileError is set and its text
// contains every wanted fragment.
func assertErrorNamesAll(t *testing.T, compileError error, wantFragments ...string) {
	t.Helper()
	if compileError == nil {
		t.Fatalf("want an error naming %v, got none", wantFragments)
	}
	for _, wantFragment := range wantFragments {
		if !strings.Contains(compileError.Error(), wantFragment) {
			t.Errorf("error %q does not name %q", compileError.Error(), wantFragment)
		}
	}
}

// TestCompileRefusesUnparseableResourceValue: spec resource-requests,
// "Unknown suffix" and "Too fine". The error names the service, the field and
// the value.
func TestCompileRefusesUnparseableResourceValue(t *testing.T) {
	testCases := []struct {
		fieldLine     string   // fieldLine is the resources line under test
		wantFragments []string // wantFragments must all appear in the error
	}{
		{fieldLine: "memory 512MB", wantFragments: []string{`"web"`, "memory", "512MB"}},
		{fieldLine: "cpu 0.5k", wantFragments: []string{`"web"`, "cpu", "0.5k"}},
		{fieldLine: "cpu_limit 2x", wantFragments: []string{`"web"`, "cpu_limit", "2x"}},
		{fieldLine: "memory_limit 1.5mi", wantFragments: []string{`"web"`, "memory_limit", "1.5mi"}},
	}
	for _, testCase := range testCases {
		_, compileError := compileServiceSource(t, `service web {
    image nginx:1.28
    instances 1
    resources {
        `+testCase.fieldLine+`
    }
}`)
		assertErrorNamesAll(t, compileError, testCase.wantFragments...)
	}

	_, acceptedError := compileServiceSource(t, `service web {
    image nginx:1.28
    instances 1
    resources {
        cpu 0
        memory 1.5Gi
        cpu_limit 2
        memory_limit 2G
    }
}`)
	if acceptedError != nil {
		t.Errorf("valid resource values refused: %v", acceptedError)
	}
}

// TestCompileRefusesUnparseableVerticalBounds: spec resource-requests,
// "Vertical scaling bounds".
func TestCompileRefusesUnparseableVerticalBounds(t *testing.T) {
	_, compileError := compileServiceSource(t, `service web {
    image nginx:1.28
    instances 1
    scale {
        vertical {
            cpu {
                min 250m
                max lots
            }
        }
    }
}`)
	assertErrorNamesAll(t, compileError, `"web"`, "cpu max", "lots")

	_, memoryError := compileServiceSource(t, `service web {
    image nginx:1.28
    instances 1
    scale {
        vertical {
            memory {
                min 256MB
                max 8Gi
            }
        }
    }
}`)
	assertErrorNamesAll(t, memoryError, `"web"`, "memory min", "256MB")
}

// TestClassifyQoSComparesParsedQuantities: spec resource-requests, "Equal
// values written differently are one QoS class".
func TestClassifyQoSComparesParsedQuantities(t *testing.T) {
	facts, compileError := compileServiceSource(t, `service web {
    image nginx:1.28
    instances 1
    resources {
        cpu 1
        cpu_limit 1000m
        memory 1Gi
        memory_limit 1024Mi
    }
}`)
	if compileError != nil {
		t.Fatalf("compile failed: %v", compileError)
	}
	if qosClass := factMap(facts)[types.KeyDerivedServiceQoSClass("web")]; qosClass != "Guaranteed" {
		t.Errorf("qos_class = %q, want Guaranteed", qosClass)
	}
	burstableFacts, burstableError := compileServiceSource(t, `service web {
    image nginx:1.28
    instances 1
    resources {
        cpu 1
        cpu_limit 1500m
        memory 1Gi
        memory_limit 1024Mi
    }
}`)
	if burstableError != nil {
		t.Fatalf("compile failed: %v", burstableError)
	}
	if qosClass := factMap(burstableFacts)[types.KeyDerivedServiceQoSClass("web")]; qosClass != "Burstable" {
		t.Errorf("qos_class = %q, want Burstable", qosClass)
	}
}

// TestApplyRefusesUnparseableTemplatedResourceValue: spec resource-requests,
// "Template value". The check runs on the rendered value, and a refused apply
// leaves the store unchanged.
func TestApplyRefusesUnparseableTemplatedResourceValue(t *testing.T) {
	ctx := context.Background()
	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	if applyError := Apply(ctx, factStore, `service db {
    image postgres:16
    instances 1
    resources {
        cpu 250m
    }
}`); applyError != nil {
		t.Fatalf("initial apply failed: %v", applyError)
	}
	factsBefore, _ := factStore.Scan(ctx, "")

	valuesPath := filepath.Join(t.TempDir(), "values.ccv")
	if writeError := os.WriteFile(valuesPath, []byte("db.cpu: half\n"), 0o600); writeError != nil {
		t.Fatal(writeError)
	}
	loadedValues, loadError := LoadValuesFile(valuesPath)
	if loadError != nil {
		t.Fatalf("load values: %v", loadError)
	}
	renderedConfiguration, renderError := RenderTemplate(`service db {
    image postgres:16
    instances 1
    resources {
        cpu {{ .db.cpu }}
    }
}`, loadedValues)
	if renderError != nil {
		t.Fatalf("render failed: %v", renderError)
	}

	applyError := Apply(ctx, factStore, renderedConfiguration)
	assertErrorNamesAll(t, applyError, `"db"`, "cpu", "half")

	factsAfter, _ := factStore.Scan(ctx, "")
	if len(factsAfter) != len(factsBefore) {
		t.Fatalf("store has %d facts after the refused apply, want %d", len(factsAfter), len(factsBefore))
	}
	for factIndex := range factsBefore {
		if factsAfter[factIndex].Key != factsBefore[factIndex].Key || string(factsAfter[factIndex].Value) != string(factsBefore[factIndex].Value) {
			t.Errorf("fact %d changed: %s=%q, was %s=%q", factIndex,
				factsAfter[factIndex].Key, factsAfter[factIndex].Value, factsBefore[factIndex].Key, factsBefore[factIndex].Value)
		}
	}
}

// TestCompileAcceptsFinerResourceValues: spec resource-requests, "Finer than
// the unit rounds up". Values finer than 1m or 1 byte are accepted (readers
// round them up), and the compiled fact keeps the text as written.
func TestCompileAcceptsFinerResourceValues(t *testing.T) {
	facts, compileError := compileServiceSource(t, `service web {
    image nginx:1.28
    instances 1
    resources {
        cpu 0.0005
        memory 1.1Gi
    }
}`)
	if compileError != nil {
		t.Fatalf("finer values refused: %v", compileError)
	}
	compiledFacts := factMap(facts)
	if compiledFacts[types.KeyDesiredServiceResourcesCPU("web")] != "0.0005" || compiledFacts[types.KeyDesiredServiceResourcesMemory("web")] != "1.1Gi" {
		t.Errorf("compiled values = %q / %q, want the text as written", compiledFacts[types.KeyDesiredServiceResourcesCPU("web")],
			compiledFacts[types.KeyDesiredServiceResourcesMemory("web")])
	}
}

// TestDeprecationWarningsFlagBareCPUOfSixtyFourOrMore: spec resource-requests,
// "Bare CPU of 64 or more" (D10). A CPU value without m that is 64 cores or
// more warns (it probably meant millicores); smaller or suffixed values do not.
func TestDeprecationWarningsFlagBareCPUOfSixtyFourOrMore(t *testing.T) {
	parsedFile, parseError := Parse(`service web {
    image nginx:1.28
    instances 1
    resources {
        cpu 500
        cpu_limit 64
        memory 512Mi
    }
    scale {
        vertical {
            cpu {
                min 2
                max 4000
            }
        }
    }
}
service api {
    image nginx:1.28
    instances 1
    resources {
        cpu 2
        cpu_limit 500m
    }
}`)
	if parseError != nil {
		t.Fatal(parseError)
	}
	var warningTexts []string
	for _, diagnostic := range DeprecationWarnings(parsedFile) {
		warningTexts = append(warningTexts, diagnostic.Message)
	}
	allWarnings := strings.Join(warningTexts, "\n")
	for _, wantFragment := range []string{`"web" cpu 500`, "500m", `"web" cpu_limit 64`, `"web" cpu max 4000`, "cores"} {
		if !strings.Contains(allWarnings, wantFragment) {
			t.Errorf("warnings lack %q:\n%s", wantFragment, allWarnings)
		}
	}
	if strings.Contains(allWarnings, `"api"`) || strings.Contains(allWarnings, "cpu min 2") {
		t.Errorf("warned about a small or suffixed value:\n%s", allWarnings)
	}
}
