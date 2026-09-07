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
// every .ccattler file can be parsed, compiled, and applied to a fresh store
// without error. This ensures example configs stay valid as the DSL evolves.
func TestAllExamplesParseAndApply(t *testing.T) {
	examplesDirectory := filepath.Join("..", "examples")
	exampleFiles, err := filepath.Glob(filepath.Join(examplesDirectory, "*.ccattler"))
	if err != nil {
		t.Fatal(err)
	}
	if len(exampleFiles) == 0 {
		t.Fatal("no example files found in examples/")
	}

	for _, exampleFilePath := range exampleFiles {
		exampleName := filepath.Base(exampleFilePath)
		t.Run(exampleName, func(t *testing.T) {
			fileContents, err := os.ReadFile(exampleFilePath)
			if err != nil {
				t.Fatalf("reading %s: %v", exampleName, err)
			}

			factStore := store.NewMemoryStore()
			defer factStore.Close()
			ctx := context.Background()

			if err := lang.Apply(ctx, factStore, string(fileContents)); err != nil {
				t.Fatalf("apply %s: %v", exampleName, err)
			}

			// Verify at least one desired service was created.
			desiredFacts, _ := factStore.Scan(ctx, "/ccattler/desired/service/")
			if len(desiredFacts) == 0 {
				t.Errorf("%s: expected at least one desired service fact", exampleName)
			}
		})
	}
}
