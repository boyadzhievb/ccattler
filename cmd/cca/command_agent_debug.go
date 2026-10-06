// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/boyadzhievb/ccattler/runtime"
)

// executeAgentDebugCommand inspects the local container runtime directly,
// bypassing the control plane and agent. It shells out to nerdctl/docker to
// show ground-truth container state, resource usage, and cached images.
func executeAgentDebugCommand() {
	containerRuntime := runtime.NewContainerRuntime()
	ctx := context.Background()

	fmt.Printf("CCattler Agent Debug — local runtime inspection\n")
	fmt.Printf("Container CLI: %s\n\n", containerRuntime.DetectedCommand())

	displayDiscoveredContainers(ctx, containerRuntime)
	displayDiscoveredStats(ctx, containerRuntime)
	displayDiscoveredImages(ctx, containerRuntime)
}

// displayDiscoveredContainers lists all containers found by the local
// container runtime (nerdctl/docker ps -a).
func displayDiscoveredContainers(ctx context.Context, containerRuntime *runtime.ContainerRuntime) {
	containers, discoverError := containerRuntime.DiscoverAllContainers(ctx)
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
// as reported by nerdctl/docker stats.
func displayDiscoveredStats(ctx context.Context, containerRuntime *runtime.ContainerRuntime) {
	usages, statsError := containerRuntime.DiscoverAllContainerStats(ctx)
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
// container runtime (nerdctl/docker images).
func displayDiscoveredImages(ctx context.Context, containerRuntime *runtime.ContainerRuntime) {
	images, listError := containerRuntime.ListImages(ctx)
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
