// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"fmt"
	"sort"
)

// maxValidPort is the highest valid TCP/UDP port number.
const maxValidPort = 65535

// LintDiagnostic represents a single lint finding with severity and location.
type LintDiagnostic struct {
	Severity string // "error" or "warning"
	Message  string // human-readable description of the issue
	Line     int    // source line number (0 if not available)
}

// Lint performs static analysis on a parsed AST and returns diagnostics. It
// checks for undefined references, duplicate names, missing required fields,
// and invalid values — without writing anything to the fact store.
func Lint(file *File) []LintDiagnostic {
	var diagnostics []LintDiagnostic
	diagnostics = append(diagnostics, lintDuplicateNames(file)...)
	diagnostics = append(diagnostics, lintMissingRequiredFields(file)...)
	diagnostics = append(diagnostics, lintPortRanges(file)...)
	diagnostics = append(diagnostics, lintUndefinedReferences(file)...)
	return diagnostics
}

// lintDuplicateNames checks for duplicate names within each top-level type.
func lintDuplicateNames(file *File) []LintDiagnostic {
	var diagnostics []LintDiagnostic

	serviceNames := make(map[string]int)
	for _, serviceDecl := range file.Services {
		if previousLine, alreadySeen := serviceNames[serviceDecl.Name]; alreadySeen {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  fmt.Sprintf("duplicate service name %q (first defined at line %d)", serviceDecl.Name, previousLine),
				Line:     serviceDecl.Line,
			})
		} else {
			serviceNames[serviceDecl.Name] = serviceDecl.Line
		}
	}

	volumeNames := make(map[string]int)
	for _, volumeDecl := range file.Volumes {
		if previousLine, alreadySeen := volumeNames[volumeDecl.Name]; alreadySeen {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  fmt.Sprintf("duplicate volume name %q (first defined at line %d)", volumeDecl.Name, previousLine),
				Line:     volumeDecl.Line,
			})
		} else {
			volumeNames[volumeDecl.Name] = volumeDecl.Line
		}
	}

	connectionNames := make(map[string]int)
	for _, connectionDecl := range file.Connections {
		if previousLine, alreadySeen := connectionNames[connectionDecl.Name]; alreadySeen {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  fmt.Sprintf("duplicate connection name %q (first defined at line %d)", connectionDecl.Name, previousLine),
				Line:     connectionDecl.Line,
			})
		} else {
			connectionNames[connectionDecl.Name] = connectionDecl.Line
		}
	}

	routeNames := make(map[string]int)
	for _, routeDecl := range file.Routes {
		if previousLine, alreadySeen := routeNames[routeDecl.Name]; alreadySeen {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  fmt.Sprintf("duplicate gate name %q (first defined at line %d)", routeDecl.Name, previousLine),
				Line:     routeDecl.Line,
			})
		} else {
			routeNames[routeDecl.Name] = routeDecl.Line
		}
	}

	roleNames := make(map[string]int)
	for _, roleDecl := range file.Roles {
		if previousLine, alreadySeen := roleNames[roleDecl.Name]; alreadySeen {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  fmt.Sprintf("duplicate role name %q (first defined at line %d)", roleDecl.Name, previousLine),
				Line:     roleDecl.Line,
			})
		} else {
			roleNames[roleDecl.Name] = roleDecl.Line
		}
	}

	return diagnostics
}

// lintMissingRequiredFields checks that mandatory fields are present.
func lintMissingRequiredFields(file *File) []LintDiagnostic {
	var diagnostics []LintDiagnostic

	for _, serviceDecl := range file.Services {
		if serviceDecl.Image == "" && serviceDecl.Command == "" {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  fmt.Sprintf("service %q requires at least one of: image, command", serviceDecl.Name),
				Line:     serviceDecl.Line,
			})
		}
	}

	for _, routeDecl := range file.Routes {
		if routeDecl.ServiceName == "" {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  fmt.Sprintf("gate %q requires a service target", routeDecl.Name),
				Line:     routeDecl.Line,
			})
		}
	}

	for _, grantDecl := range file.Grants {
		if grantDecl.RoleName == "" {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  "grant requires a role name",
				Line:     grantDecl.Line,
			})
		}
	}

	return diagnostics
}

// lintPortRanges validates that port numbers are in the valid range (1–65535).
func lintPortRanges(file *File) []LintDiagnostic {
	var diagnostics []LintDiagnostic

	for _, serviceDecl := range file.Services {
		for _, portNumber := range serviceDecl.Ports {
			if portNumber < 1 || portNumber > maxValidPort {
				diagnostics = append(diagnostics, LintDiagnostic{
					Severity: "error",
					Message:  fmt.Sprintf("service %q: port %d is out of range (1-%d)", serviceDecl.Name, portNumber, maxValidPort),
					Line:     serviceDecl.Line,
				})
			}
		}
		for _, externalPort := range serviceDecl.ExternalPorts {
			if externalPort.Port < 1 || externalPort.Port > maxValidPort {
				diagnostics = append(diagnostics, LintDiagnostic{
					Severity: "error",
					Message:  fmt.Sprintf("service %q: external port %d is out of range (1-%d)", serviceDecl.Name, externalPort.Port, maxValidPort),
					Line:     serviceDecl.Line,
				})
			}
		}
	}

	for _, connectionDecl := range file.Connections {
		if connectionDecl.Port != 0 && (connectionDecl.Port < 1 || connectionDecl.Port > maxValidPort) {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "error",
				Message:  fmt.Sprintf("connection %q: port %d is out of range (1-%d)", connectionDecl.Name, connectionDecl.Port, maxValidPort),
				Line:     connectionDecl.Line,
			})
		}
	}

	for _, networkDecl := range file.Networks {
		for _, rule := range networkDecl.Rules {
			if rule.Port != 0 && (rule.Port < 1 || rule.Port > maxValidPort) {
				diagnostics = append(diagnostics, LintDiagnostic{
					Severity: "error",
					Message:  fmt.Sprintf("network rule: port %d is out of range (1-%d)", rule.Port, maxValidPort),
					Line:     rule.Line,
				})
			}
		}
	}

	return diagnostics
}

// lintUndefinedReferences checks that service names referenced in routes,
// service groups, and network rules actually exist in the file.
func lintUndefinedReferences(file *File) []LintDiagnostic {
	var diagnostics []LintDiagnostic

	definedServices := make(map[string]bool)
	for _, serviceDecl := range file.Services {
		definedServices[serviceDecl.Name] = true
	}

	definedVolumes := make(map[string]bool)
	for _, volumeDecl := range file.Volumes {
		definedVolumes[volumeDecl.Name] = true
	}

	definedConnections := make(map[string]bool)
	for _, connectionDecl := range file.Connections {
		definedConnections[connectionDecl.Name] = true
	}

	definedRoles := make(map[string]bool)
	for _, roleDecl := range file.Roles {
		definedRoles[roleDecl.Name] = true
	}

	diagnostics = append(diagnostics, lintRouteServiceReferences(file, definedServices)...)
	diagnostics = append(diagnostics, lintServiceGroupReferences(file, definedServices)...)
	diagnostics = append(diagnostics, lintNetworkServiceReferences(file, definedServices)...)
	diagnostics = append(diagnostics, lintGrantRoleReferences(file, definedRoles)...)
	diagnostics = append(diagnostics, lintServiceConnectionReferences(file, definedConnections)...)
	diagnostics = append(diagnostics, lintServiceVolumeReferences(file, definedVolumes)...)

	return diagnostics
}

// lintRouteServiceReferences checks route targets reference defined services.
func lintRouteServiceReferences(file *File, definedServices map[string]bool) []LintDiagnostic {
	var diagnostics []LintDiagnostic
	for _, routeDecl := range file.Routes {
		if routeDecl.ServiceName != "" && !definedServices[routeDecl.ServiceName] {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "warning",
				Message:  fmt.Sprintf("route %q references undefined service %q", routeDecl.Name, routeDecl.ServiceName),
				Line:     routeDecl.Line,
			})
		}
	}
	return diagnostics
}

// lintServiceGroupReferences checks service group processes reference defined services.
func lintServiceGroupReferences(file *File, definedServices map[string]bool) []LintDiagnostic {
	var diagnostics []LintDiagnostic
	for _, serviceGroupDecl := range file.ServiceGroups {
		for _, processName := range serviceGroupDecl.Processes {
			if !definedServices[processName] {
				diagnostics = append(diagnostics, LintDiagnostic{
					Severity: "warning",
					Message:  fmt.Sprintf("group %q references undefined service %q", serviceGroupDecl.Name, processName),
					Line:     serviceGroupDecl.Line,
				})
			}
		}
	}
	return diagnostics
}

// lintNetworkServiceReferences checks network rules reference defined services.
func lintNetworkServiceReferences(file *File, definedServices map[string]bool) []LintDiagnostic {
	var diagnostics []LintDiagnostic
	for _, networkDecl := range file.Networks {
		for _, rule := range networkDecl.Rules {
			if !isWildcardIdentifier(rule.Source) && !definedServices[rule.Source] {
				diagnostics = append(diagnostics, LintDiagnostic{
					Severity: "warning",
					Message:  fmt.Sprintf("network rule references undefined source service %q", rule.Source),
					Line:     rule.Line,
				})
			}
			if !isWildcardIdentifier(rule.Target) && !definedServices[rule.Target] {
				diagnostics = append(diagnostics, LintDiagnostic{
					Severity: "warning",
					Message:  fmt.Sprintf("network rule references undefined target service %q", rule.Target),
					Line:     rule.Line,
				})
			}
		}
	}
	return diagnostics
}

// lintGrantRoleReferences checks grants reference defined roles.
func lintGrantRoleReferences(file *File, definedRoles map[string]bool) []LintDiagnostic {
	var diagnostics []LintDiagnostic
	for _, grantDecl := range file.Grants {
		if grantDecl.RoleName != "" && !definedRoles[grantDecl.RoleName] {
			diagnostics = append(diagnostics, LintDiagnostic{
				Severity: "warning",
				Message:  fmt.Sprintf("grant references undefined role %q", grantDecl.RoleName),
				Line:     grantDecl.Line,
			})
		}
	}
	return diagnostics
}

// lintServiceConnectionReferences checks services reference defined connections.
func lintServiceConnectionReferences(file *File, definedConnections map[string]bool) []LintDiagnostic {
	var diagnostics []LintDiagnostic
	for _, serviceDecl := range file.Services {
		for _, connectionRef := range serviceDecl.Connections {
			if !definedConnections[connectionRef.ConnectionName] {
				diagnostics = append(diagnostics, LintDiagnostic{
					Severity: "warning",
					Message:  fmt.Sprintf("service %q references undefined connection %q", serviceDecl.Name, connectionRef.ConnectionName),
					Line:     serviceDecl.Line,
				})
			}
		}
	}
	return diagnostics
}

// lintServiceVolumeReferences checks service volume mounts reference defined volumes.
func lintServiceVolumeReferences(file *File, definedVolumes map[string]bool) []LintDiagnostic {
	var diagnostics []LintDiagnostic
	for _, serviceDecl := range file.Services {
		for _, volumeMount := range serviceDecl.VolumeMounts {
			if !definedVolumes[volumeMount.VolumeName] {
				diagnostics = append(diagnostics, LintDiagnostic{
					Severity: "warning",
					Message:  fmt.Sprintf("service %q references undefined volume %q", serviceDecl.Name, volumeMount.VolumeName),
					Line:     serviceDecl.Line,
				})
			}
		}
	}
	return diagnostics
}

// isWildcardIdentifier returns true for special identifiers that match any
// service (e.g. "*" or "any"). Network rules may use wildcards for broad rules.
func isWildcardIdentifier(identifier string) bool {
	return identifier == "*" || identifier == "any"
}

// FormatDiagnostics returns a sorted, human-readable string of all diagnostics.
func FormatDiagnostics(diagnostics []LintDiagnostic) string {
	sort.Slice(diagnostics, func(indexA, indexB int) bool {
		if diagnostics[indexA].Severity != diagnostics[indexB].Severity {
			return diagnostics[indexA].Severity == "error"
		}
		return diagnostics[indexA].Line < diagnostics[indexB].Line
	})

	var result string
	for _, diagnostic := range diagnostics {
		if diagnostic.Line > 0 {
			result += fmt.Sprintf("  line %d: %s: %s\n", diagnostic.Line, diagnostic.Severity, diagnostic.Message)
		} else {
			result += fmt.Sprintf("  %s: %s\n", diagnostic.Severity, diagnostic.Message)
		}
	}
	return result
}
