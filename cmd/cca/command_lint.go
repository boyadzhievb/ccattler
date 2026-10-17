// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"os"

	"github.com/boyadzhievb/ccattler/lang"
)

// executeLintCommand parses .cca files and reports lint diagnostics without
// writing to the fact store. Exits non-zero if any errors are found.
func executeLintCommand(commandArgs []string) {
	if len(commandArgs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cca lint <file> [file...]")
		os.Exit(1)
	}

	exitCode := 0
	for _, filePath := range commandArgs {
		lintResult := lintSingleFile(filePath)
		if lintResult != 0 {
			exitCode = lintResult
		}
	}
	os.Exit(exitCode)
}

// lintSingleFile parses and lints one .cca file. Returns 0 if clean, 1 if
// errors were found, 1 on parse failure.
func lintSingleFile(filePath string) int {
	sourceBytes, readError := os.ReadFile(filePath) //nolint:gosec // CLI reads user-specified files
	if readError != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read %s: %v\n", filePath, readError)
		return 1
	}

	parsedFile, parseError := lang.Parse(string(sourceBytes))
	if parseError != nil {
		fmt.Fprintf(os.Stderr, "%s: parse error: %v\n", filePath, parseError)
		return 1
	}

	diagnostics := lang.Lint(parsedFile)
	if len(diagnostics) == 0 {
		fmt.Printf("%s: ok\n", filePath)
		return 0
	}

	fmt.Fprintf(os.Stderr, "%s:\n", filePath)
	fmt.Fprint(os.Stderr, lang.FormatDiagnostics(diagnostics))

	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			return 1
		}
	}
	return 0
}
