// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build ebpf

package network

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnsureKernelModulesLoadedLoadsEveryConntrackModule(testContext *testing.T) {
	var loadedModules []string
	recordingLoader := func(_ context.Context, moduleName string) error {
		loadedModules = append(loadedModules, moduleName)
		return nil
	}
	if loadError := ensureKernelModulesLoaded(context.Background(), ebpfRequiredKernelModules, recordingLoader); loadError != nil {
		testContext.Fatalf("ensureKernelModulesLoaded: %v", loadError)
	}
	if strings.Join(loadedModules, ",") != "nf_conntrack,nf_nat" {
		testContext.Fatalf("loaded modules = %v", loadedModules)
	}
}

func TestEnsureKernelModulesLoadedNamesEveryMissingModule(testContext *testing.T) {
	failingLoader := func(_ context.Context, moduleName string) error {
		return errors.New("modprobe " + moduleName + ": not found")
	}
	loadError := ensureKernelModulesLoaded(context.Background(), ebpfRequiredKernelModules, failingLoader)
	if loadError == nil || !strings.Contains(loadError.Error(), "nf_conntrack") || !strings.Contains(loadError.Error(), "nf_nat") {
		testContext.Fatalf("error must name both modules, got %v", loadError)
	}
}
