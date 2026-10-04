// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/store"
)

// TestTemplateMultiEnvironmentRoundTrip creates a template .ccattler file and
// multiple per-environment values files, then for each environment: renders the
// template with its values, validates the rendered DSL, applies it to a fresh
// memory store, and verifies that the resulting facts differ per environment
// (different instance counts, images, resource allocations).
func TestTemplateMultiEnvironmentRoundTrip(t *testing.T) {
	tempDirectory := t.TempDir()

	// Write the template file that all environments share.
	templateContent := `service {{ .name }} {
    image {{ .image }}
    instances {{ .instances }}
    expose {{ .port }}

    resources {
        cpu {{ .cpu }}
        memory {{ .memory }}
    }

    health {
        http {{ .healthPath }}
        every {{ .healthInterval }}
    }
}
`
	templateFilePath := filepath.Join(tempDirectory, "app.ccattler")
	if writeError := os.WriteFile(templateFilePath, []byte(templateContent), 0600); writeError != nil {
		t.Fatalf("writing template file: %v", writeError)
	}

	// Define per-environment values and expected outcomes.
	type environmentSpec struct {
		valuesContent     string
		expectedInstances string
		expectedImage     string
		expectedCPU       string
	}

	environments := map[string]environmentSpec{
		"dev": {
			valuesContent: strings.Join([]string{
				"name: app",
				"image: myapp:dev",
				"instances: 1",
				"port: 8080",
				"cpu: 250m",
				"memory: 256Mi",
				"healthPath: /health",
				"healthInterval: 30s",
			}, "\n"),
			expectedInstances: "1",
			expectedImage:     "myapp:dev",
			expectedCPU:       "250m",
		},
		"staging": {
			valuesContent: strings.Join([]string{
				"name: app",
				"image: myapp:v1.0.0-rc1",
				"instances: 3",
				"port: 8080",
				"cpu: 500m",
				"memory: 512Mi",
				"healthPath: /health",
				"healthInterval: 15s",
			}, "\n"),
			expectedInstances: "3",
			expectedImage:     "myapp:v1.0.0-rc1",
			expectedCPU:       "500m",
		},
		"prod": {
			valuesContent: strings.Join([]string{
				"name: app",
				"image: myapp:v1.0.0",
				"instances: 10",
				"port: 8080",
				"cpu: 2000m",
				"memory: 4Gi",
				"healthPath: /health",
				"healthInterval: 5s",
			}, "\n"),
			expectedInstances: "10",
			expectedImage:     "myapp:v1.0.0",
			expectedCPU:       "2000m",
		},
	}

	// Process each environment: write values file, render, validate, apply, verify.
	for envName, envSpec := range environments {
		t.Run(envName, func(t *testing.T) {
			// Write the environment-specific values file.
			valuesFilePath := filepath.Join(tempDirectory, "values-"+envName+".yaml")
			if writeError := os.WriteFile(valuesFilePath, []byte(envSpec.valuesContent), 0600); writeError != nil {
				t.Fatalf("writing %s values file: %v", envName, writeError)
			}

			// Render the template with this environment's values.
			renderedContent, renderError := lang.RenderWithValuesFiles(
				templateContent, []string{valuesFilePath}, nil, nil)
			if renderError != nil {
				t.Fatalf("render error for %s: %v", envName, renderError)
			}

			// Validate the rendered output is valid DSL.
			if validationError := lang.ValidateRenderedDSL(renderedContent); validationError != nil {
				t.Fatalf("validation error for %s: %v\nrendered:\n%s", envName, validationError, renderedContent)
			}

			// Apply the rendered DSL to a fresh memory store.
			factStore := store.NewMemoryStore()
			defer func() { _ = factStore.Close() }()
			ctx := context.Background()

			if applyError := lang.Apply(ctx, factStore, renderedContent); applyError != nil {
				t.Fatalf("apply error for %s: %v", envName, applyError)
			}

			// Verify environment-specific fact values in the store.
			instanceFact, _ := factStore.Get(ctx, "desired/service/app/instances")
			if string(instanceFact.Value) != envSpec.expectedInstances {
				t.Errorf("instances: expected %q, got %q", envSpec.expectedInstances, string(instanceFact.Value))
			}

			imageFact, _ := factStore.Get(ctx, "desired/service/app/image")
			if string(imageFact.Value) != envSpec.expectedImage {
				t.Errorf("image: expected %q, got %q", envSpec.expectedImage, string(imageFact.Value))
			}

			cpuFact, _ := factStore.Get(ctx, "desired/service/app/resources/cpu")
			if string(cpuFact.Value) != envSpec.expectedCPU {
				t.Errorf("cpu: expected %q, got %q", envSpec.expectedCPU, string(cpuFact.Value))
			}
		})
	}
}

// TestTemplateDirectoryRoundTrip verifies that CollectDSLFiles finds and sorts
// .cca and .ccattler files in a directory, and that each can be independently
// rendered and applied.
func TestTemplateDirectoryRoundTrip(t *testing.T) {
	tempDirectory := t.TempDir()

	// Write two DSL template files in the directory.
	webTemplate := `service web {
    image {{ .webImage }}
    instances {{ .webInstances }}
    expose 8080
}
`
	apiTemplate := `service api {
    image {{ .apiImage }}
    instances {{ .apiInstances }}
    expose 9090
}
`
	if writeError := os.WriteFile(filepath.Join(tempDirectory, "01-web.ccattler"), []byte(webTemplate), 0600); writeError != nil {
		t.Fatal(writeError)
	}
	if writeError := os.WriteFile(filepath.Join(tempDirectory, "02-api.cca"), []byte(apiTemplate), 0600); writeError != nil {
		t.Fatal(writeError)
	}

	// Collect and verify ordering.
	collectedFiles, collectError := lang.CollectDSLFiles(tempDirectory)
	if collectError != nil {
		t.Fatalf("CollectDSLFiles error: %v", collectError)
	}
	if len(collectedFiles) != 2 {
		t.Fatalf("expected 2 files, got %d", len(collectedFiles))
	}
	// Sorted: 01-web.ccattler comes before 02-api.cca alphabetically.
	if !strings.HasSuffix(collectedFiles[0], "01-web.ccattler") {
		t.Errorf("expected first file to be 01-web.ccattler, got %s", collectedFiles[0])
	}
	if !strings.HasSuffix(collectedFiles[1], "02-api.cca") {
		t.Errorf("expected second file to be 02-api.cca, got %s", collectedFiles[1])
	}

	// Write a values file and render + apply each template.
	valuesContent := "webImage: nginx:1.27\nwebInstances: 3\napiImage: myapi:v2\napiInstances: 2\n"
	valuesFilePath := filepath.Join(tempDirectory, "values.yaml")
	if writeError := os.WriteFile(valuesFilePath, []byte(valuesContent), 0600); writeError != nil {
		t.Fatal(writeError)
	}

	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()
	ctx := context.Background()

	for _, filePath := range collectedFiles {
		fileData, readError := os.ReadFile(filePath) //nolint:gosec // test reads temp file
		if readError != nil {
			t.Fatalf("reading %s: %v", filePath, readError)
		}

		renderedContent, renderError := lang.RenderWithValuesFiles(
			string(fileData), []string{valuesFilePath}, nil, nil)
		if renderError != nil {
			t.Fatalf("render error for %s: %v", filePath, renderError)
		}

		if applyError := lang.Apply(ctx, factStore, renderedContent); applyError != nil {
			t.Fatalf("apply error for %s: %v", filePath, applyError)
		}
	}

	// Verify both services exist in the store with correct values.
	webImageFact, _ := factStore.Get(ctx, "desired/service/web/image")
	if string(webImageFact.Value) != "nginx:1.27" {
		t.Errorf("web image: expected %q, got %q", "nginx:1.27", string(webImageFact.Value))
	}

	apiImageFact, _ := factStore.Get(ctx, "desired/service/api/image")
	if string(apiImageFact.Value) != "myapi:v2" {
		t.Errorf("api image: expected %q, got %q", "myapi:v2", string(apiImageFact.Value))
	}

	webInstancesFact, _ := factStore.Get(ctx, "desired/service/web/instances")
	if string(webInstancesFact.Value) != "3" {
		t.Errorf("web instances: expected %q, got %q", "3", string(webInstancesFact.Value))
	}

	apiInstancesFact, _ := factStore.Get(ctx, "desired/service/api/instances")
	if string(apiInstancesFact.Value) != "2" {
		t.Errorf("api instances: expected %q, got %q", "2", string(apiInstancesFact.Value))
	}
}

// TestTemplateValidateRenderedDSL verifies that ValidateRenderedDSL catches
// invalid DSL after template rendering.
func TestTemplateValidateRenderedDSL(t *testing.T) {
	// Valid rendered DSL should pass.
	validDSL := `service web {
    image nginx:1.27
    instances 3
    expose 8080
}`
	if validationError := lang.ValidateRenderedDSL(validDSL); validationError != nil {
		t.Errorf("expected valid DSL to pass validation, got: %v", validationError)
	}

	// Invalid DSL should fail.
	invalidDSL := `service web { image }`
	if validationError := lang.ValidateRenderedDSL(invalidDSL); validationError == nil {
		t.Error("expected invalid DSL to fail validation, got nil")
	}
}

// TestTemplateDetectUnusedValues verifies that DetectUnusedValues identifies
// top-level keys in the values map that are not referenced by the template.
func TestTemplateDetectUnusedValues(t *testing.T) {
	templateContent := `service {{ .name }} {
    image {{ .image }}
    instances {{ .count }}
}`
	valuesMap := map[string]any{
		"name":    "web",
		"image":   "nginx",
		"count":   3,
		"unused1": "not referenced",
		"unused2": "also not referenced",
	}

	unusedKeys := lang.DetectUnusedValues(templateContent, valuesMap)

	if len(unusedKeys) != 2 {
		t.Fatalf("expected 2 unused keys, got %d: %v", len(unusedKeys), unusedKeys)
	}
	// Keys should be sorted.
	if unusedKeys[0] != "unused1" || unusedKeys[1] != "unused2" {
		t.Errorf("expected [unused1 unused2], got %v", unusedKeys)
	}
}
