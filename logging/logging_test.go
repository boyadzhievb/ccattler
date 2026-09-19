package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestHumanFormatBasic(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelInfo, FormatHuman)

	logger.Info("server started", "addr", "0.0.0.0:9770")
	output := buffer.String()

	if !strings.Contains(output, "INFO") {
		t.Errorf("expected INFO level, got: %s", output)
	}
	if !strings.Contains(output, "server started") {
		t.Errorf("expected message, got: %s", output)
	}
	if !strings.Contains(output, "addr=0.0.0.0:9770") {
		t.Errorf("expected key-value field, got: %s", output)
	}
}

func TestJSONFormatBasic(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelInfo, FormatJSON)

	logger.Info("reconciliation complete", "controller", "instance", "changes", "3")

	var entry jsonEntry
	if err := json.Unmarshal(buffer.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if entry.Level != "INFO" {
		t.Errorf("expected INFO, got %s", entry.Level)
	}
	if entry.Message != "reconciliation complete" {
		t.Errorf("expected message, got %s", entry.Message)
	}
	if entry.Fields["controller"] != "instance" {
		t.Errorf("expected controller=instance, got %s", entry.Fields["controller"])
	}
	if entry.Fields["changes"] != "3" {
		t.Errorf("expected changes=3, got %s", entry.Fields["changes"])
	}
}

func TestLevelFiltering(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelWarn, FormatHuman)

	logger.Debug("should not appear")
	logger.Info("should not appear")
	logger.Warn("should appear")
	logger.Error("should also appear")

	output := buffer.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %s", len(lines), output)
	}
	if !strings.Contains(lines[0], "WARN") {
		t.Errorf("expected WARN, got: %s", lines[0])
	}
	if !strings.Contains(lines[1], "ERROR") {
		t.Errorf("expected ERROR, got: %s", lines[1])
	}
}

func TestWithComponent(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelInfo, FormatHuman).WithComponent("scheduler")

	logger.Info("placed instance", "node", "node-1")
	output := buffer.String()

	if !strings.Contains(output, "[scheduler]") {
		t.Errorf("expected [scheduler] component tag, got: %s", output)
	}
}

func TestJSONWithComponent(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelInfo, FormatJSON).WithComponent("agent")

	logger.Error("reconcile failed", "err", "connection refused")

	var entry jsonEntry
	if err := json.Unmarshal(buffer.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if entry.Component != "agent" {
		t.Errorf("expected component=agent, got %s", entry.Component)
	}
	if entry.Level != "ERROR" {
		t.Errorf("expected ERROR, got %s", entry.Level)
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		input    string
		expected Level
	}{
		{"debug", LevelDebug},
		{"DEBUG", LevelDebug},
		{"info", LevelInfo},
		{"INFO", LevelInfo},
		{"warn", LevelWarn},
		{"WARN", LevelWarn},
		{"warning", LevelWarn},
		{"error", LevelError},
		{"ERROR", LevelError},
		{"bogus", LevelInfo},
	}

	for _, testCase := range cases {
		result := ParseLevel(testCase.input)
		if result != testCase.expected {
			t.Errorf("ParseLevel(%q) = %v, want %v", testCase.input, result, testCase.expected)
		}
	}
}

func TestDebugLevel(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelDebug, FormatHuman)

	logger.Debug("verbose detail")
	output := buffer.String()

	if !strings.Contains(output, "DEBUG") {
		t.Errorf("expected DEBUG in output, got: %s", output)
	}
	if !strings.Contains(output, "verbose detail") {
		t.Errorf("expected message in output, got: %s", output)
	}
}

func TestJSONTimestampFormat(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelInfo, FormatJSON)

	logger.Info("test")

	var entry jsonEntry
	if err := json.Unmarshal(buffer.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if entry.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
	if !strings.Contains(entry.Timestamp, "T") {
		t.Errorf("expected RFC3339 timestamp, got: %s", entry.Timestamp)
	}
}

func TestNoFieldsOmitsFieldsKey(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelInfo, FormatJSON)

	logger.Info("simple message")

	output := buffer.String()
	if strings.Contains(output, `"fields"`) {
		t.Errorf("expected fields to be omitted when empty, got: %s", output)
	}
}

func TestHumanFormatNoComponent(t *testing.T) {
	buffer := &bytes.Buffer{}
	logger := New(buffer, LevelInfo, FormatHuman)

	logger.Info("hello")
	output := buffer.String()

	if strings.Contains(output, "[") {
		t.Errorf("expected no component brackets, got: %s", output)
	}
}
