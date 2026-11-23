// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"strings"
	"testing"
)

// TestLintCleanFile verifies no diagnostics for a valid file.
func TestLintCleanFile(t *testing.T) {
	input := `connection app-db {
    driver "postgresql+psycopg2"
    host "database"
    port 5432
}

volume app-data {
    size 50Gi
}

service web {
    image nginx:1.28
    instances 3
    expose 8080
    connection app-db
    volume app-data /var/lib/data
}

gate web-public {
    service web port 8080
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	if len(diagnostics) > 0 {
		t.Errorf("expected no diagnostics, got %d:\n%s", len(diagnostics), FormatDiagnostics(diagnostics))
	}
}

// TestLintDuplicateServiceNames detects duplicate service names.
func TestLintDuplicateServiceNames(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
}

service web {
    image apache:2.4
    instances 1
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundDuplicate := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "duplicate service name") {
			foundDuplicate = true
		}
	}
	if !foundDuplicate {
		t.Error("expected duplicate service name diagnostic")
	}
}

// TestLintDuplicateVolumeNames detects duplicate volume names.
func TestLintDuplicateVolumeNames(t *testing.T) {
	input := `volume data {
    size 10Gi
}

volume data {
    size 20Gi
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundDuplicate := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "duplicate volume name") {
			foundDuplicate = true
		}
	}
	if !foundDuplicate {
		t.Error("expected duplicate volume name diagnostic")
	}
}

// TestLintDuplicateRouteNames detects duplicate gate names.
func TestLintDuplicateRouteNames(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
    expose 8080
}

gate api {
    service web port 8080
}

gate api {
    service web port 8080
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundDuplicate := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "duplicate gate name") {
			foundDuplicate = true
		}
	}
	if !foundDuplicate {
		t.Error("expected duplicate gate name diagnostic")
	}
}

// TestLintMissingServiceImageOrCommand detects services with neither.
func TestLintMissingServiceImageOrCommand(t *testing.T) {
	parsedFile := &File{
		Services: []ServiceDecl{
			{Name: "empty-service", Instances: 1, Line: 1},
		},
	}

	diagnostics := Lint(parsedFile)
	foundMissing := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "requires at least one of: image, command") {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Error("expected missing image/command diagnostic")
	}
}

// TestLintPortRangeValidation detects invalid port numbers.
func TestLintPortRangeValidation(t *testing.T) {
	parsedFile := &File{
		Services: []ServiceDecl{
			{
				Name:      "bad-ports",
				Image:     "nginx:1.28",
				Instances: 1,
				Ports:     []int{0, 99999},
				Line:      1,
			},
		},
	}

	diagnostics := Lint(parsedFile)
	portErrors := 0
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "out of range") {
			portErrors++
		}
	}
	if portErrors != 2 {
		t.Errorf("expected 2 port range errors, got %d", portErrors)
	}
}

// TestLintUndefinedRouteService detects routes referencing non-existent services.
func TestLintUndefinedRouteService(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
    expose 8080
}

gate api {
    service nonexistent port 3000
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundUndefined := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "undefined service") && strings.Contains(diagnostic.Message, "nonexistent") {
			foundUndefined = true
		}
	}
	if !foundUndefined {
		t.Error("expected undefined service reference warning")
	}
}

// TestLintUndefinedServiceGroupProcess detects groups referencing non-existent services.
func TestLintUndefinedServiceGroupProcess(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
}

group frontend {
    process web
    process missing-service
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundUndefined := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "undefined service") && strings.Contains(diagnostic.Message, "missing-service") {
			foundUndefined = true
		}
	}
	if !foundUndefined {
		t.Error("expected undefined service reference in group")
	}
}

// TestLintUndefinedNetworkService detects network rules referencing non-existent services.
func TestLintUndefinedNetworkService(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
}

network {
    allow web -> nonexistent port 443
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundUndefined := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "undefined target service") && strings.Contains(diagnostic.Message, "nonexistent") {
			foundUndefined = true
		}
	}
	if !foundUndefined {
		t.Error("expected undefined target service in network rule")
	}
}

// TestLintUndefinedGrantRole detects grants referencing non-existent roles.
func TestLintUndefinedGrantRole(t *testing.T) {
	input := `grant missing-role to group developers
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundUndefined := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "undefined role") && strings.Contains(diagnostic.Message, "missing-role") {
			foundUndefined = true
		}
	}
	if !foundUndefined {
		t.Error("expected undefined role warning")
	}
}

// TestLintUndefinedConnection detects services referencing non-existent connections.
func TestLintUndefinedConnection(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
    connection nonexistent-db
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundUndefined := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "undefined connection") {
			foundUndefined = true
		}
	}
	if !foundUndefined {
		t.Error("expected undefined connection warning")
	}
}

// TestLintUndefinedVolume detects services referencing non-existent volumes.
func TestLintUndefinedVolume(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
    volume nonexistent-vol /data
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}

	diagnostics := Lint(parsedFile)
	foundUndefined := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "undefined volume") {
			foundUndefined = true
		}
	}
	if !foundUndefined {
		t.Error("expected undefined volume warning")
	}
}

// TestLintDiagnosticSorting verifies errors come before warnings.
func TestLintDiagnosticSorting(t *testing.T) {
	diagnostics := []LintDiagnostic{
		{Severity: "warning", Message: "warning first", Line: 1},
		{Severity: "error", Message: "error second", Line: 2},
	}

	formatted := FormatDiagnostics(diagnostics)
	errorIndex := strings.Index(formatted, "error")
	warningIndex := strings.Index(formatted, "warning")
	if errorIndex > warningIndex {
		t.Error("errors should be sorted before warnings")
	}
}

// TestLintConnectionPortRange detects invalid connection port numbers.
func TestLintConnectionPortRange(t *testing.T) {
	parsedFile := &File{
		Connections: []ConnectionDecl{
			{Name: "bad-conn", Port: 70000, Line: 1},
		},
	}

	diagnostics := Lint(parsedFile)
	foundPortError := false
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "out of range") {
			foundPortError = true
		}
	}
	if !foundPortError {
		t.Error("expected port range error for connection")
	}
}

// TestLintTCPHealthWithoutExpose warns when a service has a TCP health check
// but no exposed ports.
func TestLintTCPHealthWithoutExpose(t *testing.T) {
	parsedFile := &File{
		Services: []ServiceDecl{
			{
				Name:  "scheduler",
				Image: "airflow:2.10.5",
				Health: &HealthDecl{
					Method:   "tcp",
					Interval: "30s",
				},
				Ports: nil,
				Line:  1,
			},
		},
	}

	diagnostics := Lint(parsedFile)
	foundWarning := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "warning" && strings.Contains(diagnostic.Message, "TCP health check but no exposed ports") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Error("expected warning for TCP health check without exposed ports")
	}
}

// TestLintTCPHealthWithExposeNoWarning ensures no warning when TCP health has
// an exposed port.
func TestLintTCPHealthWithExposeNoWarning(t *testing.T) {
	parsedFile := &File{
		Services: []ServiceDecl{
			{
				Name:  "redis",
				Image: "redis:7",
				Health: &HealthDecl{
					Method:   "tcp",
					Interval: "10s",
				},
				Ports: []int{6379},
				Line:  1,
			},
		},
	}

	diagnostics := Lint(parsedFile)
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "TCP health check but no exposed ports") {
			t.Error("should not warn when service has exposed ports")
		}
	}
}

// TestLintRefusesUnreadableResourceValue: 3g.2 (testbed F4). cca lint reports
// an unreadable resource value as an error with the compiler's message, the
// same check apply runs, so a lint pass never hides an apply refusal.
func TestLintRefusesUnreadableResourceValue(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 1
    resources {
        memory 512MB
    }
}
`
	parsedFile, parseError := Parse(input)
	if parseError != nil {
		t.Fatalf("parse failed: %v", parseError)
	}
	_, compileError := Compile(parsedFile)
	compileParseError, isParseError := compileError.(*ParseError)
	if !isParseError {
		t.Fatalf("compile error = %v, want the *ParseError apply reports", compileError)
	}
	var lintErrors []LintDiagnostic
	for _, diagnostic := range Lint(parsedFile) {
		if diagnostic.Severity == "error" {
			lintErrors = append(lintErrors, diagnostic)
		}
	}
	if len(lintErrors) != 1 || lintErrors[0].Message != compileParseError.Message || lintErrors[0].Line != compileParseError.Line {
		t.Errorf("lint errors = %+v, want one error with the compiler's message %q at line %d",
			lintErrors, compileParseError.Message, compileParseError.Line)
	}
}
