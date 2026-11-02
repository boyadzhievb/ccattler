// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/api"
)

// captureTopOutput runs printFunction with os.Stdout redirected and returns
// what it printed.
func captureTopOutput(t *testing.T, printFunction func()) string {
	t.Helper()
	readEnd, writeEnd, pipeError := os.Pipe()
	if pipeError != nil {
		t.Fatal(pipeError)
	}
	originalStdout := os.Stdout
	os.Stdout = writeEnd
	printFunction()
	os.Stdout = originalStdout
	writeEnd.Close()
	printedBytes, _ := io.ReadAll(readEnd)
	return string(printedBytes)
}

// TestPrintTopVolumesListsSharedAttachments verifies `cca top volumes` shows
// the access mode and every node and instance of a shared volume.
func TestPrintTopVolumesListsSharedAttachments(t *testing.T) {
	printedOutput := captureTopOutput(t, func() {
		printTopVolumes([]api.VolumeStatus{{
			Name: "persistent-nfs-volume", Size: "1Gi", State: "attached", Access: "shared",
			Nodes: []string{"cca-deploy-ctrl", "cca-deploy-worker-1"}, Instances: []string{"3471080b87", "6482f2b6b5"},
		}})
	})
	for _, expectedText := range []string{"ACCESS", "shared", "cca-deploy-ctrl,cca-deploy-worker-1", "3471080b87,6482f2b6b5"} {
		if !strings.Contains(printedOutput, expectedText) {
			t.Errorf("output missing %q:\n%s", expectedText, printedOutput)
		}
	}
}

// TestPrintTopWorkloadsAlignsAndHumanizesMemory verifies columns stay aligned
// for long names and memory is shown in binary units, not raw bytes.
func TestPrintTopWorkloadsAlignsAndHumanizesMemory(t *testing.T) {
	printedOutput := captureTopOutput(t, func() {
		printTopWorkloads([]api.InstanceStatus{
			{ServiceName: "whoami-multiport", ID: "973295799e", NodeID: "cca-deploy-worker-2", State: "running", MemoryBytes: "2510848", HealthState: "healthy"},
			{ServiceName: "web", ID: "a1", NodeID: "n1", State: "running", MemoryBytes: "", HealthState: ""},
		})
	})
	printedLines := strings.Split(strings.TrimRight(printedOutput, "\n"), "\n")
	if len(printedLines) != 3 {
		t.Fatalf("expected header + 2 rows, got:\n%s", printedOutput)
	}
	statusColumn := strings.Index(printedLines[0], "STATUS")
	for _, rowLine := range printedLines[1:] {
		if !strings.HasPrefix(rowLine[statusColumn:], "running") {
			t.Errorf("STATUS column misaligned:\n%s", printedOutput)
		}
	}
	if !strings.Contains(printedOutput, "2.4MiB") || strings.Contains(printedOutput, "2510848") {
		t.Errorf("memory not humanized:\n%s", printedOutput)
	}
}
