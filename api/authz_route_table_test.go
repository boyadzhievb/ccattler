// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// authzGraphPath is the call graph that holds the route authorization table.
const authzGraphPath = "../design/graphs/AUTHZ_GRAPH.md"

// routeTableRowPattern matches a table row whose first cell is a code-quoted
// route path, such as "| `/api/scale` | POST | ...".
var routeTableRowPattern = regexp.MustCompile("^\\| `(/[^`]*)` \\|")

// readAuthzRouteTable returns the sorted route column of the "API Route
// Authorization" table in AUTHZ_GRAPH.md.
func readAuthzRouteTable(t *testing.T) []string {
	t.Helper()
	graphBytes, readError := os.ReadFile(authzGraphPath)
	if readError != nil {
		t.Fatal(readError)
	}
	graphSections := strings.SplitN(string(graphBytes), "## API Route Authorization", 2)
	if len(graphSections) != 2 {
		t.Fatal("AUTHZ_GRAPH.md has no \"API Route Authorization\" section")
	}
	tableSection := strings.SplitN(graphSections[1], "\n## ", 2)[0]
	var tableRoutes []string
	for _, line := range strings.Split(tableSection, "\n") {
		if match := routeTableRowPattern.FindStringSubmatch(line); match != nil {
			tableRoutes = append(tableRoutes, match[1])
		}
	}
	sort.Strings(tableRoutes)
	return tableRoutes
}

// TestDocCrossCheckAuthzRouteTable fails when a route registered in
// server.go is missing from the AUTHZ_GRAPH.md route table or the table lists
// a route that is not registered (spec api-route-authorization, "Route table
// matches the registered routes").
func TestDocCrossCheckAuthzRouteTable(t *testing.T) {
	registeredRoutes := collectRegisteredRoutes(t)
	tableRoutes := readAuthzRouteTable(t)

	inTable := make(map[string]bool, len(tableRoutes))
	for _, tableRoute := range tableRoutes {
		inTable[tableRoute] = true
	}
	inCode := make(map[string]bool, len(registeredRoutes))
	for _, registeredRoute := range registeredRoutes {
		inCode[registeredRoute] = true
		if !inTable[registeredRoute] {
			t.Errorf("route %s is registered in server.go but missing from the AUTHZ_GRAPH.md route table", registeredRoute)
		}
	}
	for _, tableRoute := range tableRoutes {
		if !inCode[tableRoute] {
			t.Errorf("route %s is in the AUTHZ_GRAPH.md route table but not registered in server.go", tableRoute)
		}
	}
}
