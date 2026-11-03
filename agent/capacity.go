// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"bufio"
	"fmt"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/boyadzhievb/ccattler/types"
)

// procMeminfoPath is the Linux file the agent reads total memory from.
const procMeminfoPath = "/proc/meminfo"

// minimumSystemReservedMemoryMiB is the least memory kept back from the
// scheduler for the OS, the agent, containerd and (on control-plane nodes)
// etcd and the API server.
const minimumSystemReservedMemoryMiB = 768

// systemReservedMemoryDivisor reserves a tenth of total memory when that is
// more than the minimum, so large machines keep proportional headroom.
const systemReservedMemoryDivisor = 10

// NodeCapacity is the CPU and memory a node offers to the scheduler.
type NodeCapacity struct {
	CPUMillicores int64  // CPUMillicores is the schedulable CPU in millicores
	MemoryMiB     int64  // MemoryMiB is the schedulable memory in MiB, after the system reserve
	Source        string // Source says where the values came from, for the startup log
}

// DetectNodeCapacity returns the node's real schedulable capacity: all CPUs
// and MemTotal from /proc/meminfo minus the system reserve. Where
// /proc/meminfo is unavailable (macOS, tests) it returns the simulated
// defaults, so scheduling still works but the log says so.
func DetectNodeCapacity() NodeCapacity {
	cpuMillicores := int64(goruntime.NumCPU()) * 1000
	meminfoContent, readError := os.ReadFile(procMeminfoPath)
	if readError != nil {
		return NodeCapacity{
			CPUMillicores: types.DefaultSimulatedNodeCPU,
			MemoryMiB:     types.DefaultSimulatedNodeMemory,
			Source:        "simulated defaults (" + procMeminfoPath + " unavailable)",
		}
	}
	totalMemoryMiB, parseError := parseMemTotalMiB(string(meminfoContent))
	if parseError != nil {
		return NodeCapacity{
			CPUMillicores: types.DefaultSimulatedNodeCPU,
			MemoryMiB:     types.DefaultSimulatedNodeMemory,
			Source:        "simulated defaults (" + parseError.Error() + ")",
		}
	}
	return NodeCapacity{
		CPUMillicores: cpuMillicores,
		MemoryMiB:     allocatableMemoryMiB(totalMemoryMiB),
		Source:        fmt.Sprintf("detected (%d CPUs, %d MiB total)", goruntime.NumCPU(), totalMemoryMiB),
	}
}

// parseMemTotalMiB extracts MemTotal (reported in kB) from /proc/meminfo
// content and returns it in MiB.
func parseMemTotalMiB(meminfoContent string) (int64, error) {
	lineScanner := bufio.NewScanner(strings.NewReader(meminfoContent))
	for lineScanner.Scan() {
		fields := strings.Fields(lineScanner.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			totalKiB, convertError := strconv.ParseInt(fields[1], 10, 64)
			if convertError != nil {
				return 0, fmt.Errorf("parse MemTotal %q: %w", fields[1], convertError)
			}
			return totalKiB / 1024, nil
		}
	}
	return 0, fmt.Errorf("MemTotal not found in %s", procMeminfoPath)
}

// allocatableMemoryMiB subtracts the system reserve, the larger of
// minimumSystemReservedMemoryMiB and a tenth of total memory, from the total.
func allocatableMemoryMiB(totalMemoryMiB int64) int64 {
	reservedMemoryMiB := max(int64(minimumSystemReservedMemoryMiB), totalMemoryMiB/systemReservedMemoryDivisor)
	return max(totalMemoryMiB-reservedMemoryMiB, 0)
}
