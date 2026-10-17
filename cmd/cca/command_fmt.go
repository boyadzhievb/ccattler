// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/boyadzhievb/ccattler/lang"
)

// executeFmtCommand parses a .cca file, re-emits it in canonical format, and
// overwrites the file. With --check, it exits non-zero if the file would change
// (for CI dry-run).
func executeFmtCommand(commandArgs []string) {
	checkOnly := false
	var filePaths []string

	for _, argument := range commandArgs {
		switch {
		case argument == "--check":
			checkOnly = true
		case strings.HasPrefix(argument, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n", argument)
			fmt.Fprintln(os.Stderr, "usage: cca fmt [--check] <file> [file...]")
			os.Exit(1)
		default:
			filePaths = append(filePaths, argument)
		}
	}

	if len(filePaths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cca fmt [--check] <file> [file...]")
		os.Exit(1)
	}

	exitCode := 0
	for _, filePath := range filePaths {
		formatResult := formatSingleFile(filePath, checkOnly)
		if formatResult != 0 {
			exitCode = formatResult
		}
	}
	os.Exit(exitCode)
}

// formatSingleFile formats one .cca file. Returns 0 on success, 1 if the file
// would change (--check mode), or 1 on error.
func formatSingleFile(filePath string, checkOnly bool) int {
	sourceBytes, readError := os.ReadFile(filePath) //nolint:gosec // CLI reads user-specified files
	if readError != nil {
		fmt.Fprintf(os.Stderr, "error: cannot read %s: %v\n", filePath, readError)
		return 1
	}
	source := string(sourceBytes)

	parsedFile, parseError := lang.Parse(source)
	if parseError != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", filePath, parseError)
		return 1
	}

	formatted := lang.Format(parsedFile)

	if checkOnly {
		if source != formatted {
			fmt.Fprintf(os.Stderr, "%s: not formatted\n", filePath)
			return 1
		}
		return 0
	}

	if source == formatted {
		return 0
	}

	writeError := os.WriteFile(filePath, []byte(formatted), 0644) //nolint:gosec // config files are world-readable
	if writeError != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write %s: %v\n", filePath, writeError)
		return 1
	}
	fmt.Printf("formatted %s\n", filePath)
	return 0
}
