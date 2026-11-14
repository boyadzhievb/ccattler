// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// command_apply.go contains the apply command configuration, argument parsing,
// and execution logic extracted from main.go.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/controllers"
	"github.com/boyadzhievb/ccattler/infra"
	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/store"
)

const (
	// applyConvergenceSettleTime is the delay after applying DSL config to let
	// the reconciliation loop converge before printing status.
	applyConvergenceSettleTime = 500 * time.Millisecond
)

// configurationCommandConfig holds parsed flags for "apply" and "diff". Both
// reach the cluster only through the API server; --simulate runs in memory.
type configurationCommandConfig struct {
	// configFilePath is the path to the .cca DSL file or directory.
	configFilePath string
	// valuesFilePaths holds paths to values files for template rendering (--values).
	valuesFilePaths []string
	// setOverrides holds key=value pairs for template overrides (--set).
	setOverrides []string
	// setFromEnvOverrides holds environment variable names for template overrides (--set-from-env).
	setFromEnvOverrides []string
	// dryRunEnabled prints the rendered configuration without applying it (apply --dry-run).
	dryRunEnabled bool
	// simulateEnabled runs against a local in-memory store instead of the API server (--simulate).
	simulateEnabled bool
}

// parseConfigurationCommandArgs extracts the config path and flags following
// "apply" or "diff" (commandName). An unknown flag, including the removed
// --store/--endpoints/--store-prefix, exits with an error.
func parseConfigurationCommandArgs(commandName string, args []string) configurationCommandConfig {
	var parsedConfig configurationCommandConfig
	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--values", "-f":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.valuesFilePaths = append(parsedConfig.valuesFilePaths, args[argIndex])
			}
		case "--set":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.setOverrides = append(parsedConfig.setOverrides, args[argIndex])
			}
		case "--set-from-env":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.setFromEnvOverrides = append(parsedConfig.setFromEnvOverrides, args[argIndex])
			}
		case "--dry-run":
			if commandName != "apply" {
				exitWithUnknownFlag(commandName, currentArg)
			}
			parsedConfig.dryRunEnabled = true
		case "--simulate":
			parsedConfig.simulateEnabled = true
		default:
			if strings.HasPrefix(currentArg, "-") {
				exitWithUnknownFlag(commandName, currentArg)
			}
			if parsedConfig.configFilePath == "" {
				parsedConfig.configFilePath = currentArg
			}
		}
	}
	return parsedConfig
}

// exitWithUnknownFlag reports an unknown flag of commandName and exits. The
// direct-etcd flags were removed in M219: configurations go through the API.
func exitWithUnknownFlag(commandName string, flagName string) {
	fmt.Fprintf(os.Stderr, "error: cca %s: unknown flag %q (configurations go through the API server; use --simulate for a local simulation)\n", commandName, flagName)
	os.Exit(1)
}

// executeApplyCommand renders one or more .cca files (a file or a directory)
// and sends them to the API server, which authorizes and writes the facts;
// controllers in "cca server" reconcile. With --simulate it runs a local
// simulation with 3 simulated nodes instead.
func executeApplyCommand(parsedConfig configurationCommandConfig) {
	renderedFiles, resolveError := resolveAndRenderDSLFiles(
		parsedConfig.configFilePath, parsedConfig.valuesFilePaths,
		parsedConfig.setOverrides, parsedConfig.setFromEnvOverrides)
	if resolveError != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", resolveError)
		os.Exit(1)
	}

	if parsedConfig.dryRunEnabled {
		for _, rendered := range renderedFiles {
			fmt.Println(rendered.content)
		}
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if !parsedConfig.simulateEnabled {
		applyRenderedFilesThroughAPI(ctx, renderedFiles)
		return
	}

	factStore := store.NewMemoryStore()
	defer func() { _ = factStore.Close() }()

	controllerRunner := setupLocalSimulationEnvironment(ctx, factStore)
	go func() {
		if runError := controllerRunner.Run(ctx); runError != nil {
			logging.Default().Error("controller runner exited with error", "error", runError.Error())
		}
	}()

	applyRenderedFilesToSimulatedStore(ctx, factStore, renderedFiles)

	// Wait for reconciliation to settle before printing status.
	time.Sleep(applyConvergenceSettleTime)
	fmt.Print(buildStatusTextOutput(ctx, factStore))
}

// applyRenderedFilesToSimulatedStore applies each rendered DSL file to the
// local in-memory store (--simulate); inline gate keys are not stored.
func applyRenderedFilesToSimulatedStore(ctx context.Context, factStore store.StateStore, renderedFiles []renderedDSLContent) {
	for _, rendered := range renderedFiles {
		fmt.Printf("Applying %s...\n", rendered.filePath)
		printDeprecationWarnings(rendered)
		baseDir := filepath.Dir(rendered.filePath)
		if applyError := lang.ApplyWithBaseDir(ctx, factStore, rendered.content, baseDir, localSimulationGateKeyWriter{}); applyError != nil {
			exitWithFileError(applyError, rendered.filePath)
		}
	}
}

// setupLocalSimulationEnvironment registers 3 simulated nodes and creates all
// reconciliation controllers for a local (in-memory) simulation. Returns a
// controller runner ready to be started.
func setupLocalSimulationEnvironment(ctx context.Context, factStore store.StateStore) *controllers.Runner {
	registerSimulatedNodes(ctx, factStore, []string{"node-1", "node-2", "node-3"})
	fmt.Println("Registered 3 simulated nodes")

	controllerList := append(coreControllers(),
		controllers.NewWarmZeroController(),
		controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore), factStore))

	return controllers.NewRunner(factStore, controllerList...)
}

// printDeprecationWarnings prints deprecation warnings for a rendered file to
// stderr. Parse errors are left to the apply step, which reports them.
func printDeprecationWarnings(rendered renderedDSLContent) {
	parsedFile, parseError := lang.Parse(rendered.content)
	if parseError != nil {
		return
	}
	for _, diagnostic := range lang.DeprecationWarnings(parsedFile) {
		fmt.Fprintf(os.Stderr, "warning: %s:%d: %s\n", rendered.filePath, diagnostic.Line, diagnostic.Message)
	}
}
