// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/tenant"
)

// workloadVisibility is the set of teams whose workloads a principal may read
// through state, watch and status (Flow 25). It comes only from workload.read
// grants: a cluster-scope grant sees every team, team/<name> grants see those
// teams, and team membership or the platform group adds nothing.
type workloadVisibility struct {
	allTeams bool     // allTeams is true for a cluster-scope grant
	teams    []string // teams lists the sorted team names granted at team scope when allTeams is false
}

// isEmpty reports whether the principal may read no workloads at all.
func (visibility workloadVisibility) isEmpty() bool {
	return !visibility.allTeams && len(visibility.teams) == 0
}

// allowsFactKey reports whether a fact key is visible: everything for a
// cluster-scope grant, otherwise the tenant package's per-key team filter.
func (visibility workloadVisibility) allowsFactKey(factKey string) bool {
	return visibility.allTeams || tenant.IsFactVisibleToTeams(factKey, visibility.teams)
}

// resolveWorkloadVisibility derives a principal's workload visibility from
// its workload.read grants. Only exact team/<name> scopes count as team
// visibility; deeper, node and service scopes are ignored (fail closed).
func resolveWorkloadVisibility(apiAuthorizer *security.APIAuthorizer, principal security.Principal) workloadVisibility {
	if apiAuthorizer == nil {
		return workloadVisibility{}
	}
	teamSet := make(map[string]bool)
	for _, grantedScope := range apiAuthorizer.GrantedScopes(principal, security.CapabilityWorkloadRead) {
		if grantedScope == security.ScopeCluster {
			return workloadVisibility{allTeams: true}
		}
		teamName, isTeamScope := strings.CutPrefix(string(grantedScope), "team/")
		if isTeamScope && teamName != "" && !strings.Contains(teamName, "/") {
			teamSet[teamName] = true
		}
	}
	teamNames := make([]string, 0, len(teamSet))
	for teamName := range teamSet {
		teamNames = append(teamNames, teamName)
	}
	sort.Strings(teamNames)
	return workloadVisibility{teams: teamNames}
}

// requireWorkloadVisibility resolves the caller's workload visibility and
// writes 403 when it has workload.read at no scope. Returns false when the
// request must stop.
func (apiServer *Server) requireWorkloadVisibility(responseWriter http.ResponseWriter, request *http.Request) (workloadVisibility, bool) {
	if apiServer.apiAuthorizer == nil {
		writeJSONError(responseWriter, "authorization not configured", http.StatusInternalServerError)
		return workloadVisibility{}, false
	}
	visibility := resolveWorkloadVisibility(apiServer.apiAuthorizer, security.PrincipalStructFromContext(request.Context()))
	if visibility.isEmpty() {
		writeJSONError(responseWriter, "forbidden: no workload.read grant at any scope", http.StatusForbidden)
		return workloadVisibility{}, false
	}
	return visibility, true
}
