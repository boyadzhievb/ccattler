// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/boyadzhievb/ccattler/runtime"
)

// runtimeInspector defines the methods needed by agent debug to discover
// containers, stats, and images from a local container runtime.
type runtimeInspector interface {
	DiscoverAllContainers(ctx context.Context) ([]runtime.DiscoveredContainer, error)
	DiscoverAllContainerStats(ctx context.Context) ([]runtime.ContainerResourceUsage, error)
	ListImages(ctx context.Context) ([]runtime.ImageInfo, error)
}

// executeAgentDebugCommand inspects the local container runtime directly,
// bypassing the control plane and agent. Tries the containerd client API
// first (for nodes with containerd only), then falls back to nerdctl/docker.
func executeAgentDebugCommand() {
	ctx := context.Background()

	var inspector runtimeInspector
	runtimeLabel := "containerd"

	containerdRuntime, connectError := runtime.NewContainerdRuntime("")
	if connectError == nil {
		inspector = containerdRuntime
	} else {
		containerRuntime := runtime.NewContainerRuntime()
		inspector = containerRuntime
		runtimeLabel = containerRuntime.DetectedCommand()
	}

	fmt.Printf("CCattler Agent Debug — local runtime inspection\n")
	fmt.Printf("Container runtime: %s\n\n", runtimeLabel)

	displayDiscoveredContainers(ctx, inspector)
	displayDiscoveredStats(ctx, inspector)
	displayDiscoveredImages(ctx, inspector)
}

// displayDiscoveredContainers lists all containers found by the local
// container runtime.
func displayDiscoveredContainers(ctx context.Context, inspector runtimeInspector) {
	containers, discoverError := inspector.DiscoverAllContainers(ctx)
	if discoverError != nil {
		fmt.Fprintf(os.Stderr, "error discovering containers: %v\n", discoverError)
		return
	}

	runningCount := 0
	for _, container := range containers {
		if container.Running {
			runningCount++
		}
	}

	fmt.Printf("CONTAINERS (%d total, %d running)\n", len(containers), runningCount)
	if len(containers) == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Printf("  %-30s  %-14s  %-30s  %s\n", "NAME", "ID", "IMAGE", "STATUS")
		for _, container := range containers {
			containerIDShort := container.ID
			if len(containerIDShort) > 12 {
				containerIDShort = containerIDShort[:12]
			}
			fmt.Printf("  %-30s  %-14s  %-30s  %s\n",
				truncateString(container.Name, 30),
				containerIDShort,
				truncateString(container.Image, 30),
				container.Status)
		}
	}
	fmt.Println()
}

// displayDiscoveredStats shows resource usage for all running containers
// as reported by the container runtime.
func displayDiscoveredStats(ctx context.Context, inspector runtimeInspector) {
	usages, statsError := inspector.DiscoverAllContainerStats(ctx)
	if statsError != nil {
		fmt.Fprintf(os.Stderr, "error querying stats: %v\n", statsError)
		return
	}

	fmt.Printf("RESOURCE USAGE (%d containers)\n", len(usages))
	if len(usages) == 0 {
		fmt.Println("  (none)")
	} else {
		var totalCPUMillicores int64
		var totalMemoryBytes int64
		fmt.Printf("  %-30s  %-12s  %s\n", "NAME", "CPU", "MEMORY")
		for _, usage := range usages {
			cpuDisplay := fmt.Sprintf("%dm", usage.CPUMillicores)
			memoryDisplay := formatBytesHuman(usage.MemoryBytes)
			fmt.Printf("  %-30s  %-12s  %s\n",
				truncateString(usage.Name, 30), cpuDisplay, memoryDisplay)
			totalCPUMillicores += usage.CPUMillicores
			totalMemoryBytes += usage.MemoryBytes
		}
		fmt.Printf("  %-30s  %-12s  %s\n",
			"TOTAL", fmt.Sprintf("%dm", totalCPUMillicores), formatBytesHuman(totalMemoryBytes))
	}
	fmt.Println()
}

// displayDiscoveredImages lists locally cached container images from the
// container runtime.
func displayDiscoveredImages(ctx context.Context, inspector runtimeInspector) {
	images, listError := inspector.ListImages(ctx)
	if listError != nil {
		fmt.Fprintf(os.Stderr, "error listing images: %v\n", listError)
		return
	}

	fmt.Printf("IMAGES (%d)\n", len(images))
	if len(images) == 0 {
		fmt.Println("  (none)")
	} else {
		fmt.Printf("  %-40s  %-16s  %-14s  %s\n", "REPOSITORY", "TAG", "IMAGE ID", "SIZE")
		for _, imageEntry := range images {
			imageIDShort := imageEntry.ImageID
			if len(imageIDShort) > 12 {
				imageIDShort = imageIDShort[:12]
			}
			fmt.Printf("  %-40s  %-16s  %-14s  %s\n",
				imageEntry.Repository, imageEntry.Tag, imageIDShort,
				formatBytesHuman(imageEntry.SizeBytes))
		}
	}
}

// truncateString truncates a string to maxLength, appending "…" if truncated.
func truncateString(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	if maxLength <= 1 {
		return value[:maxLength]
	}
	return value[:maxLength-1] + "…"
}
