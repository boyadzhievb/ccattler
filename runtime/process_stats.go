package runtime

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readProcessStats reads CPU and memory usage for the given PID. On Linux,
// this reads /proc/{pid}/stat and /proc/{pid}/statm. On other platforms,
// it returns zero values (metrics unavailable).
func readProcessStats(pid int) (ResourceStats, error) {
	memoryBytes, memoryError := readProcessMemoryFromProc(pid)
	if memoryError != nil {
		return ResourceStats{}, nil
	}

	cpuMillicores, cpuError := readProcessCPUFromProc(pid)
	if cpuError != nil {
		return ResourceStats{}, nil
	}

	return ResourceStats{
		CPUMillicores: cpuMillicores,
		MemoryBytes:   memoryBytes,
	}, nil
}

// readProcessMemoryFromProc reads the resident set size from /proc/{pid}/statm.
// The second field is RSS in pages. Returns 0 on non-Linux or any read error.
func readProcessMemoryFromProc(pid int) (int64, error) {
	statmPath := fmt.Sprintf("/proc/%d/statm", pid)
	statmContent, readError := os.ReadFile(statmPath)
	if readError != nil {
		return 0, readError
	}

	fields := strings.Fields(string(statmContent))
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected statm format for pid %d", pid)
	}

	rssPages, parseError := strconv.ParseInt(fields[1], 10, 64)
	if parseError != nil {
		return 0, parseError
	}

	pageSize := int64(os.Getpagesize())
	return rssPages * pageSize, nil
}

// readProcessCPUFromProc reads user+system CPU time from /proc/{pid}/stat
// and converts it to approximate millicores based on elapsed wall time.
// Fields 14 and 15 (0-indexed from the comm field end) are utime and stime
// in clock ticks. Returns 0 on non-Linux or any read error.
func readProcessCPUFromProc(pid int) (int64, error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	statContent, readError := os.ReadFile(statPath)
	if readError != nil {
		return 0, readError
	}

	// The comm field (field 2) is parenthesized and may contain spaces,
	// so find the closing paren and parse from there.
	closingParen := strings.LastIndex(string(statContent), ")")
	if closingParen < 0 {
		return 0, fmt.Errorf("unexpected stat format for pid %d", pid)
	}
	remainingFields := strings.Fields(string(statContent)[closingParen+2:])
	if len(remainingFields) < 13 {
		return 0, fmt.Errorf("not enough fields in stat for pid %d", pid)
	}

	// Fields after the closing paren: index 0 = state, ..., 11 = utime, 12 = stime.
	userTicks, userParseError := strconv.ParseInt(remainingFields[11], 10, 64)
	if userParseError != nil {
		return 0, userParseError
	}
	systemTicks, systemParseError := strconv.ParseInt(remainingFields[12], 10, 64)
	if systemParseError != nil {
		return 0, systemParseError
	}

	totalTicks := userTicks + systemTicks
	// Convert ticks to millicores: (ticks / CLK_TCK) * 1000.
	// CLK_TCK is typically 100 on Linux, so millicores = totalTicks * 10.
	clockTicksPerSecond := int64(100)
	cpuMillicores := (totalTicks * 1000) / clockTicksPerSecond

	return cpuMillicores, nil
}
