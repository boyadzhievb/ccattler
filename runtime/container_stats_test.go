package runtime

import (
	"testing"
)

// TestParseNerdctlStatsTypical verifies parsing of typical nerdctl stats output.
func TestParseNerdctlStatsTypical(t *testing.T) {
	resourceStats := parseNerdctlStats("1.23% 45.6MiB / 7.8GiB")
	if resourceStats.CPUMillicores != 12 {
		t.Errorf("cpu: got %d, want 12 (1.23%% * 10)", resourceStats.CPUMillicores)
	}
	expectedMemoryBytes := int64(47815065)
	if resourceStats.MemoryBytes != expectedMemoryBytes {
		t.Errorf("memory: got %d, want %d", resourceStats.MemoryBytes, expectedMemoryBytes)
	}
}

// TestParseNerdctlStatsHighCPU verifies parsing with high CPU usage.
func TestParseNerdctlStatsHighCPU(t *testing.T) {
	resourceStats := parseNerdctlStats("95.50% 1.2GiB / 16GiB")
	if resourceStats.CPUMillicores != 955 {
		t.Errorf("cpu: got %d, want 955 (95.50%% * 10)", resourceStats.CPUMillicores)
	}
	expectedMemoryBytes := int64(1288490188)
	if resourceStats.MemoryBytes != expectedMemoryBytes {
		t.Errorf("memory: got %d, want %d", resourceStats.MemoryBytes, expectedMemoryBytes)
	}
}

// TestParseNerdctlStatsEmpty verifies that empty input returns zero stats.
func TestParseNerdctlStatsEmpty(t *testing.T) {
	resourceStats := parseNerdctlStats("")
	if resourceStats.CPUMillicores != 0 || resourceStats.MemoryBytes != 0 {
		t.Errorf("expected zero stats for empty input, got cpu=%d mem=%d",
			resourceStats.CPUMillicores, resourceStats.MemoryBytes)
	}
}

// TestParseMemoryValueUnits verifies parsing of various memory unit suffixes.
func TestParseMemoryValueUnits(t *testing.T) {
	testCases := []struct {
		input    string
		expected int64
	}{
		{"512MiB", 512 * 1024 * 1024},
		{"1GiB", 1024 * 1024 * 1024},
		{"256KiB", 256 * 1024},
		{"100B", 100},
		{"2.5GiB", 2684354560},
		{"", 0},
	}

	for _, testCase := range testCases {
		result := parseMemoryValue(testCase.input)
		if result != testCase.expected {
			t.Errorf("parseMemoryValue(%q): got %d, want %d", testCase.input, result, testCase.expected)
		}
	}
}
