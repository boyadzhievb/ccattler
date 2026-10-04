// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/store"
)

// TestAllExamplesParseAndApply walks the examples/ directory and verifies that
// every .cca file can be parsed, compiled, and applied to a fresh store
// without error. This ensures example configs stay valid as the DSL evolves.
func TestAllExamplesParseAndApply(t *testing.T) {
	examplesDirectory := filepath.Join("..", "examples")
	exampleFiles, err := filepath.Glob(filepath.Join(examplesDirectory, "*.cca"))
	if err != nil {
		t.Fatal(err)
	}
	if len(exampleFiles) == 0 {
		t.Fatal("no example files found in examples/")
	}

	for _, exampleFilePath := range exampleFiles {
		exampleName := filepath.Base(exampleFilePath)
		t.Run(exampleName, func(t *testing.T) {
			fileContents, err := os.ReadFile(exampleFilePath) //nolint:gosec // test reads generated file
			if err != nil {
				t.Fatalf("reading %s: %v", exampleName, err)
			}

			factStore := store.NewMemoryStore()
			defer factStore.Close()
			ctx := context.Background()

			if err := lang.Apply(ctx, factStore, string(fileContents)); err != nil {
				t.Fatalf("apply %s: %v", exampleName, err)
			}

			// Verify at least one fact was created (services, auth, etc.).
			desiredFacts, _ := factStore.Scan(ctx, "desired/service/")
			authFacts, _ := factStore.Scan(ctx, "auth/")
			if len(desiredFacts) == 0 && len(authFacts) == 0 {
				t.Errorf("%s: expected at least one fact (desired service or auth)", exampleName)
			}
		})
	}
}
