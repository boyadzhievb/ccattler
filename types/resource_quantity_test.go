// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import (
	"strings"
	"testing"
)

// TestParseCPUMillicoresAcceptsKubernetesForms: spec resource-requests, "CPU
// forms". Millicores with m, and cores with or without a fraction.
func TestParseCPUMillicoresAcceptsKubernetesForms(t *testing.T) {
	testCases := []struct {
		cpuText        string // cpuText is the value as written in the configuration
		wantMillicores int64  // wantMillicores is the parsed request
	}{
		{cpuText: "250m", wantMillicores: 250},
		{cpuText: "0.5", wantMillicores: 500},
		{cpuText: "2", wantMillicores: 2000},
		{cpuText: "0", wantMillicores: 0},
		{cpuText: "0m", wantMillicores: 0},
		{cpuText: "1000m", wantMillicores: 1000},
		{cpuText: "1.25", wantMillicores: 1250},
		{cpuText: "0.001", wantMillicores: 1},
		{cpuText: "0.5000", wantMillicores: 500},
		{cpuText: "1500m", wantMillicores: 1500},
	}
	for _, testCase := range testCases {
		gotMillicores, parseError := ParseCPUMillicores(testCase.cpuText)
		if parseError != nil {
			t.Errorf("ParseCPUMillicores(%q): unexpected error %v", testCase.cpuText, parseError)
			continue
		}
		if gotMillicores != testCase.wantMillicores {
			t.Errorf("ParseCPUMillicores(%q) = %d, want %d", testCase.cpuText, gotMillicores, testCase.wantMillicores)
		}
	}
}

// TestParseMemoryBytesAcceptsKubernetesForms: spec resource-requests, "Memory
// forms". Binary and decimal suffixes, and plain bytes.
func TestParseMemoryBytesAcceptsKubernetesForms(t *testing.T) {
	testCases := []struct {
		memoryText string // memoryText is the value as written in the configuration
		wantBytes  int64  // wantBytes is the parsed request in bytes
	}{
		{memoryText: "512Mi", wantBytes: 536870912},
		{memoryText: "1.5Gi", wantBytes: 1610612736},
		{memoryText: "1G", wantBytes: 1000000000},
		{memoryText: "1048576", wantBytes: 1048576},
		{memoryText: "0", wantBytes: 0},
		{memoryText: "1Ki", wantBytes: 1024},
		{memoryText: "64Gi", wantBytes: 64 << 30},
		{memoryText: "2Ti", wantBytes: 2 << 40},
		{memoryText: "1Pi", wantBytes: 1 << 50},
		{memoryText: "7Ei", wantBytes: 7 << 60},
		{memoryText: "1k", wantBytes: 1000},
		{memoryText: "128M", wantBytes: 128000000},
		{memoryText: "1.1G", wantBytes: 1100000000},
		{memoryText: "1T", wantBytes: 1000000000000},
		{memoryText: "1P", wantBytes: 1000000000000000},
		{memoryText: "9E", wantBytes: 9000000000000000000},
		{memoryText: "0.5Ki", wantBytes: 512},
	}
	for _, testCase := range testCases {
		gotBytes, parseError := ParseMemoryBytes(testCase.memoryText)
		if parseError != nil {
			t.Errorf("ParseMemoryBytes(%q): unexpected error %v", testCase.memoryText, parseError)
			continue
		}
		if gotBytes != testCase.wantBytes {
			t.Errorf("ParseMemoryBytes(%q) = %d, want %d", testCase.memoryText, gotBytes, testCase.wantBytes)
		}
	}
}

// TestParseResourceQuantitiesRefuseOtherForms: spec resource-requests, "An
// apply refuses resource values it cannot read". Each value is refused with an
// error naming it, never read as 0.
func TestParseResourceQuantitiesRefuseOtherForms(t *testing.T) {
	refusedCPUTexts := []string{
		"", " 1", "1 ", "-1", "+1", "1e3", "512MB", "lots", "1.5mm", "-1m",
		"1Gi", ".5", "5.", "1..5", "m", "9223372036854775808m", "9999999999999999",
	}
	for _, cpuText := range refusedCPUTexts {
		if gotMillicores, parseError := ParseCPUMillicores(cpuText); parseError == nil {
			t.Errorf("ParseCPUMillicores(%q) = %d, want an error", cpuText, gotMillicores)
		} else if cpuText != "" && !strings.Contains(parseError.Error(), cpuText) {
			t.Errorf("ParseCPUMillicores(%q) error %q does not name the value", cpuText, parseError)
		}
	}
	refusedMemoryTexts := []string{
		"", " 1", "1 ", "512MB", "-1", "+1", "1e3", "0.3k1", "512mi", "1m",
		"9999999Ei", "8Ei", "9223372036854775808", "Mi", ".5Gi", "1.Gi", "1 Gi",
	}
	for _, memoryText := range refusedMemoryTexts {
		if gotBytes, parseError := ParseMemoryBytes(memoryText); parseError == nil {
			t.Errorf("ParseMemoryBytes(%q) = %d, want an error", memoryText, gotBytes)
		} else if memoryText != "" && !strings.Contains(parseError.Error(), memoryText) {
			t.Errorf("ParseMemoryBytes(%q) error %q does not name the value", memoryText, parseError)
		}
	}
}

// TestParseQuantitiesRoundFinerValuesUp: spec resource-requests, "Finer than
// the unit rounds up" (D2, user decision D-b): a value finer than 1 millicore
// or 1 byte is rounded up to the next one, as Kubernetes does.
func TestParseQuantitiesRoundFinerValuesUp(t *testing.T) {
	cpuTestCases := map[string]int64{"0.0005": 1, "1.5m": 2, "0.0011": 2, "0.0001m": 1, "2.0000": 2000}
	for cpuText, wantMillicores := range cpuTestCases {
		if gotMillicores, parseError := ParseCPUMillicores(cpuText); parseError != nil || gotMillicores != wantMillicores {
			t.Errorf("ParseCPUMillicores(%q) = %d, %v; want %d", cpuText, gotMillicores, parseError, wantMillicores)
		}
	}
	memoryTestCases := map[string]int64{"1.1Gi": 1181116007, "0.1Gi": 107374183, "1.5": 2, "0.0001k": 1}
	for memoryText, wantBytes := range memoryTestCases {
		if gotBytes, parseError := ParseMemoryBytes(memoryText); parseError != nil || gotBytes != wantBytes {
			t.Errorf("ParseMemoryBytes(%q) = %d, %v; want %d", memoryText, gotBytes, parseError, wantBytes)
		}
	}
}

// TestIsBareInteger checks the shared test for a value written without unit
// or fraction (legacy recommendations, bare CPU warnings).
func TestIsBareInteger(t *testing.T) {
	for text, wantBare := range map[string]bool{"750": true, "0": true, "750m": false, "": false, "1.5": false, "-1": false, "1 2": false} {
		if gotBare := IsBareInteger(text); gotBare != wantBare {
			t.Errorf("IsBareInteger(%q) = %v, want %v", text, gotBare, wantBare)
		}
	}
}

// TestFormatResourceQuantitiesWithUnits checks the unit forms the vertical
// autoscaler writes: CPU in millicores with m, memory in MiB rounded up.
func TestFormatResourceQuantitiesWithUnits(t *testing.T) {
	if gotText := FormatCPUMillicores(750); gotText != "750m" {
		t.Errorf("FormatCPUMillicores(750) = %q, want 750m", gotText)
	}
	memoryTestCases := []struct {
		memoryBytes int64  // memoryBytes is the value to format
		wantText    string // wantText is the MiB form, rounded up
		wantMiB     int64  // wantMiB is MemoryBytesToMiBRoundedUp of memoryBytes
	}{
		{memoryBytes: 0, wantText: "0Mi", wantMiB: 0},
		{memoryBytes: 1, wantText: "1Mi", wantMiB: 1},
		{memoryBytes: 805306368, wantText: "768Mi", wantMiB: 768},
		{memoryBytes: 805306369, wantText: "769Mi", wantMiB: 769},
		{memoryBytes: 64 << 30, wantText: "65536Mi", wantMiB: 65536},
	}
	for _, testCase := range memoryTestCases {
		if gotText := FormatMemoryMiB(testCase.memoryBytes); gotText != testCase.wantText {
			t.Errorf("FormatMemoryMiB(%d) = %q, want %q", testCase.memoryBytes, gotText, testCase.wantText)
		}
		if gotMiB := MemoryBytesToMiBRoundedUp(testCase.memoryBytes); gotMiB != testCase.wantMiB {
			t.Errorf("MemoryBytesToMiBRoundedUp(%d) = %d, want %d", testCase.memoryBytes, gotMiB, testCase.wantMiB)
		}
	}
}

// TestFormattedQuantitiesParseBack checks that what the autoscaler writes is
// read back to the same value by every reader.
func TestFormattedQuantitiesParseBack(t *testing.T) {
	if gotMillicores, parseError := ParseCPUMillicores(FormatCPUMillicores(1234)); parseError != nil || gotMillicores != 1234 {
		t.Errorf("CPU round trip = %d, %v; want 1234", gotMillicores, parseError)
	}
	if gotBytes, parseError := ParseMemoryBytes(FormatMemoryMiB(768 << 20)); parseError != nil || gotBytes != 768<<20 {
		t.Errorf("memory round trip = %d, %v; want %d", gotBytes, parseError, int64(768<<20))
	}
}
