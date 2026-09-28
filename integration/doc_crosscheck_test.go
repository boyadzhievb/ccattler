// Package integration contains integration tests for the CCattler container
// orchestrator. This file cross-checks documentation claims in CLAUDE.md
// against the actual codebase to detect documentation drift.
package integration

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// projectRoot is the absolute path to the CCattler repository root, computed
// once from the test file's location (integration/ is one level below root).
var projectRoot = func() string {
	workingDirectory, _ := os.Getwd()
	// integration tests run from the integration/ directory
	if strings.HasSuffix(workingDirectory, "/integration") {
		return strings.TrimSuffix(workingDirectory, "/integration")
	}
	return workingDirectory
}()

// readProjectFile reads a file relative to the project root and returns its
// contents as a string. It calls t.Fatal if the file cannot be read.
func readProjectFile(testContext *testing.T, relativePath string) string {
	testContext.Helper()
	absolutePath := projectRoot + "/" + relativePath
	fileContents, readError := os.ReadFile(absolutePath) //nolint:gosec // test reads project files by known relative path
	if readError != nil {
		testContext.Fatalf("failed to read %s: %v", absolutePath, readError)
	}
	return string(fileContents)
}

// TestDocCrossCheckCLICommandsExist reads CLAUDE.md, extracts all cca
// subcommand patterns from the CLI section, and verifies each subcommand
// string appears in the main.go command registration switch statement.
func TestDocCrossCheckCLICommandsExist(testContext *testing.T) {
	claudeDocContents := readProjectFile(testContext, "CLAUDE.md")
	mainGoContents := readProjectFile(testContext, "cmd/cca/main.go")

	// Extract the CLI section from CLAUDE.md — starts at "## CLI" and ends
	// at the next "---" or "## " heading.
	cliSectionPattern := regexp.MustCompile(`(?s)## CLI\n(.*?)(\n---|\n## )`)
	cliSectionMatch := cliSectionPattern.FindStringSubmatch(claudeDocContents)
	if cliSectionMatch == nil {
		testContext.Fatal("could not find ## CLI section in CLAUDE.md")
	}
	cliSectionText := cliSectionMatch[1]

	// Extract "cca <subcommand>" patterns. The subcommand is the first word
	// after "cca ". We handle multi-word subcommands like "get services" by
	// only checking the primary subcommand (the first word after cca).
	subcommandPattern := regexp.MustCompile(`cca\s+([a-z][-a-z]*)`)
	subcommandMatches := subcommandPattern.FindAllStringSubmatch(cliSectionText, -1)
	if len(subcommandMatches) == 0 {
		testContext.Fatal("no cca subcommands found in CLAUDE.md CLI section")
	}

	// Deduplicate subcommands.
	uniqueSubcommands := make(map[string]bool)
	for _, matchGroup := range subcommandMatches {
		subcommandName := matchGroup[1]
		uniqueSubcommands[subcommandName] = true
	}

	// Each subcommand should appear as a case in the switch statement or as a
	// recognized string in main.go's command dispatch logic.
	for subcommandName := range uniqueSubcommands {
		// The main.go uses case "subcommand": for dispatching, or the
		// subcommand string appears in usage/help text.
		casePattern := `case "` + subcommandName + `"`
		usagePattern := `"` + subcommandName + `"`

		if !strings.Contains(mainGoContents, casePattern) && !strings.Contains(mainGoContents, usagePattern) {
			testContext.Errorf("CLAUDE.md documents 'cca %s' but %q not found in cmd/cca/main.go",
				subcommandName, subcommandName)
		}
	}

	testContext.Logf("verified %d unique CLI subcommands from CLAUDE.md exist in main.go", len(uniqueSubcommands))
}

// TestDocCrossCheckControllerInterfaceMethods verifies that CLAUDE.md's claim
// about the Controller interface having Name(), Watch(), and Reconcile()
// methods matches the actual interface definition in controllers/controller.go.
func TestDocCrossCheckControllerInterfaceMethods(testContext *testing.T) {
	controllerSourceContents := readProjectFile(testContext, "controllers/controller.go")

	// The documented Controller interface methods from CLAUDE.md.
	expectedInterfaceMethods := []string{
		"Name()",
		"Watch()",
		"Reconcile(",
	}

	// Verify the file contains a Controller interface definition.
	if !strings.Contains(controllerSourceContents, "type Controller interface") {
		testContext.Fatal("controllers/controller.go does not contain 'type Controller interface'")
	}

	for _, methodSignature := range expectedInterfaceMethods {
		if !strings.Contains(controllerSourceContents, methodSignature) {
			testContext.Errorf("CLAUDE.md documents Controller.%s but not found in controllers/controller.go",
				methodSignature)
		}
	}

	testContext.Log("verified Controller interface methods: Name(), Watch(), Reconcile()")
}

// TestDocCrossCheckStoreInterfaceMethods verifies that CLAUDE.md's claim about
// StateStore operations (Get, Put, Delete, Scan, Watch, Transaction, Revision)
// matches the actual interface definition in store/store.go.
func TestDocCrossCheckStoreInterfaceMethods(testContext *testing.T) {
	storeSourceContents := readProjectFile(testContext, "store/store.go")

	// The documented StateStore operations from CLAUDE.md's Fact Store Interface section.
	expectedStoreMethods := []string{
		"Get(",
		"Put(",
		"Delete(",
		"Scan(",
		"Watch(",
		"Transaction(",
		"Revision(",
	}

	// Verify the file contains a StateStore interface definition.
	if !strings.Contains(storeSourceContents, "type StateStore interface") {
		testContext.Fatal("store/store.go does not contain 'type StateStore interface'")
	}

	for _, methodSignature := range expectedStoreMethods {
		if !strings.Contains(storeSourceContents, methodSignature) {
			testContext.Errorf("CLAUDE.md documents StateStore.%s but not found in store/store.go",
				methodSignature)
		}
	}

	testContext.Log("verified StateStore interface methods: Get, Put, Delete, Scan, Watch, Transaction, Revision")
}

// TestDocCrossCheckFactPrefixesExist verifies that CLAUDE.md's mention of key
// prefixes (desired, observed, effective, placement, endpoint, intent, lease,
// event, network, derived) matches the actual constant definitions in
// types/keys.go.
func TestDocCrossCheckFactPrefixesExist(testContext *testing.T) {
	keysSourceContents := readProjectFile(testContext, "types/keys.go")

	// The documented key prefixes from CLAUDE.md. Each should be defined as a
	// Prefix* constant in types/keys.go.
	documentedPrefixes := []struct {
		prefixName    string
		constantName  string
		expectedValue string
	}{
		{"desired", "PrefixDesired", `"desired"`},
		{"observed", "PrefixObserved", `"observed"`},
		{"effective", "PrefixEffective", `"effective"`},
		{"placement", "PrefixPlacement", `"placement"`},
		{"endpoint", "PrefixEndpoint", `"endpoint"`},
		{"intent", "PrefixIntent", `"intent"`},
		{"lease", "PrefixLease", `"lease"`},
		{"event", "PrefixEvent", `"event"`},
		{"network", "PrefixNetwork", `"network"`},
		{"derived", "PrefixDerived", `"derived"`},
	}

	for _, prefixEntry := range documentedPrefixes {
		// Check that the constant name exists in the source file.
		if !strings.Contains(keysSourceContents, prefixEntry.constantName) {
			testContext.Errorf("CLAUDE.md references prefix %q but constant %s not found in types/keys.go",
				prefixEntry.prefixName, prefixEntry.constantName)
			continue
		}

		// Check that the constant has the expected value.
		expectedAssignment := prefixEntry.constantName + ` = ` + prefixEntry.expectedValue
		if !strings.Contains(keysSourceContents, expectedAssignment) {
			testContext.Errorf("CLAUDE.md references prefix %q — constant %s exists but expected assignment %q not found in types/keys.go",
				prefixEntry.prefixName, prefixEntry.constantName, expectedAssignment)
		}
	}

	testContext.Logf("verified %d fact key prefixes from CLAUDE.md exist in types/keys.go", len(documentedPrefixes))
}

// TestDocCrossCheckRuntimeAdapters verifies that CLAUDE.md's claim about
// runtime adapters (SimulatorRuntime, ProcessRuntime, ContainerRuntime) matches
// the actual struct definitions in the runtime package.
func TestDocCrossCheckRuntimeAdapters(testContext *testing.T) {
	// Read all Go source files in the runtime package to find struct definitions.
	runtimeDirectoryEntries, readDirError := os.ReadDir(projectRoot + "/runtime")
	if readDirError != nil {
		testContext.Fatalf("failed to read runtime directory: %v", readDirError)
	}

	// Concatenate all Go source files in the runtime package into one string
	// for simpler searching.
	var combinedRuntimeSource strings.Builder
	for _, directoryEntry := range runtimeDirectoryEntries {
		if strings.HasSuffix(directoryEntry.Name(), ".go") {
			fileContents := readProjectFile(testContext, "runtime/"+directoryEntry.Name())
			combinedRuntimeSource.WriteString(fileContents)
			combinedRuntimeSource.WriteString("\n")
		}
	}
	runtimeSourceText := combinedRuntimeSource.String()

	// The documented runtime adapters from CLAUDE.md's Runtime Adapters section.
	documentedRuntimeAdapters := []string{
		"SimulatorRuntime",
		"ProcessRuntime",
		"ContainerRuntime",
	}

	for _, adapterStructName := range documentedRuntimeAdapters {
		// Look for the type definition: "type SimulatorRuntime struct"
		structDefinition := "type " + adapterStructName + " struct"
		if !strings.Contains(runtimeSourceText, structDefinition) {
			testContext.Errorf("CLAUDE.md documents %s but %q not found in runtime/*.go",
				adapterStructName, structDefinition)
		}
	}

	testContext.Logf("verified %d runtime adapter structs from CLAUDE.md exist in runtime package",
		len(documentedRuntimeAdapters))
}
