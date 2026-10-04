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

// applyCommandConfig holds parsed flags for the "apply" command, which can
// optionally connect to a remote store instead of running a local simulation.
type applyCommandConfig struct {
	// configFilePath is the path to the .cca DSL file to apply.
	configFilePath string
	// storeBackend selects the state store implementation: "memory" or "etcd".
	storeBackend string
	// etcdEndpoints is the comma-separated list of etcd server addresses.
	etcdEndpoints string
	// storeKeyPrefix is the key prefix for namespacing within a shared etcd cluster.
	storeKeyPrefix string
	// valuesFilePaths holds paths to values files for template rendering (--values).
	valuesFilePaths []string
	// setOverrides holds key=value pairs for template overrides (--set).
	setOverrides []string
	// setFromEnvOverrides holds environment variable names for template overrides (--set-from-env).
	setFromEnvOverrides []string
	// dryRunEnabled skips writing to the store when true (--dry-run).
	dryRunEnabled bool
}

// parseApplyCommandArgs extracts the config file path and optional store flags
// from the arguments following "apply".
func parseApplyCommandArgs(args []string) applyCommandConfig {
	parsedConfig := applyCommandConfig{
		storeBackend:   "memory",
		etcdEndpoints:  "localhost:2379",
		storeKeyPrefix: "/ccattler/",
	}

	for argIndex := 0; argIndex < len(args); argIndex++ {
		currentArg := args[argIndex]
		switch currentArg {
		case "--store":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.storeBackend = args[argIndex]
			}
		case "--endpoints":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.etcdEndpoints = args[argIndex]
			}
		case "--store-prefix":
			if argIndex+1 < len(args) {
				argIndex++
				parsedConfig.storeKeyPrefix = args[argIndex]
			}
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
			parsedConfig.dryRunEnabled = true
		default:
			if parsedConfig.configFilePath == "" {
				parsedConfig.configFilePath = currentArg
			}
		}
	}

	if parsedConfig.storeBackend != "memory" && parsedConfig.storeBackend != "etcd" {
		fmt.Fprintf(os.Stderr, "error: unknown store backend %q (must be \"memory\" or \"etcd\")\n", parsedConfig.storeBackend)
		os.Exit(1)
	}

	return parsedConfig
}

// executeApplyCommand parses one or more .ccattler files and writes facts to the
// state store. Supports single files and directories (all .cca/.ccattler files).
// In remote mode (--store etcd), it connects to the shared store, writes facts,
// and exits — controllers running in "cca server" handle reconciliation.
// In local mode (default), it runs a local simulation with 3 simulated nodes.
func executeApplyCommand(parsedConfig applyCommandConfig) {
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

	if parsedConfig.storeBackend == "etcd" {
		factStore, storeCreationError := createStateStoreFromServerConfig(
			parsedConfig.storeBackend, parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix,
			"", "", "")
		if storeCreationError != nil {
			fmt.Fprintf(os.Stderr, "error connecting to etcd: %v\n", storeCreationError)
			os.Exit(1)
		}
		defer func() { _ = factStore.Close() }()

		fmt.Printf("Connected to etcd at %s (prefix: %s)\n", parsedConfig.etcdEndpoints, parsedConfig.storeKeyPrefix)
		applyRenderedFilesToStore(ctx, factStore, renderedFiles)
		fmt.Println("Facts written to store. Controllers will reconcile.")
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

	applyRenderedFilesToStore(ctx, factStore, renderedFiles)

	// Wait for reconciliation to settle before printing status.
	time.Sleep(applyConvergenceSettleTime)
	fmt.Print(buildStatusTextOutput(ctx, factStore))
}

// applyRenderedFilesToStore applies each rendered DSL file to the given store,
// printing progress and exiting on the first error.
func applyRenderedFilesToStore(ctx context.Context, factStore store.StateStore, renderedFiles []renderedDSLContent) {
	for _, rendered := range renderedFiles {
		fmt.Printf("Applying %s...\n", rendered.filePath)
		baseDir := filepath.Dir(rendered.filePath)
		if applyError := lang.ApplyWithBaseDir(ctx, factStore, rendered.content, baseDir); applyError != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", annotateErrorWithFileName(applyError, rendered.filePath))
			os.Exit(1)
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
		controllers.NewClusterAutoscaleController(infra.NewSimulatorInfraProvider(factStore)))

	return controllers.NewRunner(factStore, controllerList...)
}
