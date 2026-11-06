// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build ebpf

package network

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ebpfRequiredKernelModules provide the kfuncs the data plane programs call:
// nf_conntrack (bpf_skb_ct_lookup, bpf_skb_ct_alloc, bpf_ct_insert_entry) and
// nf_nat (bpf_ct_set_nat_info). Built as modules on Ubuntu kernels, they are
// not loaded on a freshly booted node until something uses conntrack.
var ebpfRequiredKernelModules = []string{"nf_conntrack", "nf_nat"}

// kernelModuleLoadTimeout bounds one modprobe call.
const kernelModuleLoadTimeout = 10 * time.Second

// kernelModuleLoader loads one kernel module by name.
type kernelModuleLoader func(ctx context.Context, moduleName string) error

// runModprobe loads a kernel module with modprobe (idempotent when loaded or built in).
func runModprobe(ctx context.Context, moduleName string) error {
	modprobeContext, cancel := context.WithTimeout(ctx, kernelModuleLoadTimeout)
	defer cancel()
	output, runError := exec.CommandContext(modprobeContext, "modprobe", moduleName).CombinedOutput() //nolint:gosec // module names are package constants
	if runError != nil {
		return fmt.Errorf("modprobe %s: %w: %s", moduleName, runError, strings.TrimSpace(string(output)))
	}
	return nil
}

// ensureKernelModulesLoaded loads every module the BPF programs depend on
// before the collection is created, so kfunc resolution does not fail with
// "not supported" on a fresh boot. Returns an error naming each missing module.
func ensureKernelModulesLoaded(ctx context.Context, moduleNames []string, loadModule kernelModuleLoader) error {
	var loadErrors []string
	for _, moduleName := range moduleNames {
		if loadError := loadModule(ctx, moduleName); loadError != nil {
			loadErrors = append(loadErrors, loadError.Error())
		}
	}
	if len(loadErrors) > 0 {
		return fmt.Errorf("loading kernel modules for eBPF conntrack kfuncs: %s", strings.Join(loadErrors, "; "))
	}
	return nil
}
