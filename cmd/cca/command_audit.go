// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// Audit log CLI: query and stream persistent audit entries.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// auditEntryResponse mirrors the JSON shape returned by GET /api/audit.
type auditEntryResponse struct {
	Timestamp  time.Time `json:"timestamp"`
	Principal  string    `json:"principal"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	Decision   string    `json:"decision"`
	AuthMethod string    `json:"auth_method"`
	Policy     string    `json:"policy"`
}

// executeAuditCommand dispatches `cca audit` with optional filter flags.
func executeAuditCommand(args []string) {
	principalFilter := ""
	actionFilter := ""
	sinceFilter := ""
	limitValue := "100"
	followMode := false

	for argIndex := 0; argIndex < len(args); argIndex++ {
		switch args[argIndex] {
		case "--principal":
			if argIndex+1 < len(args) {
				argIndex++
				principalFilter = args[argIndex]
			}
		case "--action":
			if argIndex+1 < len(args) {
				argIndex++
				actionFilter = args[argIndex]
			}
		case "--since":
			if argIndex+1 < len(args) {
				argIndex++
				sinceFilter = resolveAuditSinceValue(args[argIndex])
			}
		case "--limit":
			if argIndex+1 < len(args) {
				argIndex++
				limitValue = args[argIndex]
			}
		case "--follow", "-f":
			followMode = true
		default:
			fmt.Fprintln(os.Stderr, "usage: cca audit [--principal <p>] [--action <a>] [--since <time>] [--limit N] [--follow]")
			os.Exit(1)
		}
	}

	queryParams := buildAuditQueryParams(principalFilter, actionFilter, sinceFilter, limitValue, followMode)
	requestURL := fmt.Sprintf("%s/api/audit?%s", statusAPIBaseURL(), queryParams.Encode())

	if followMode {
		executeAuditFollow(requestURL)
	} else {
		executeAuditQuery(requestURL)
	}
}

// buildAuditQueryParams assembles URL query parameters from the parsed flags.
func buildAuditQueryParams(principalFilter, actionFilter, sinceFilter, limitValue string, followMode bool) url.Values {
	queryParams := url.Values{}
	if principalFilter != "" {
		queryParams.Set("principal", principalFilter)
	}
	if actionFilter != "" {
		queryParams.Set("action", actionFilter)
	}
	if sinceFilter != "" {
		queryParams.Set("since", sinceFilter)
	}
	if limitValue != "" && limitValue != "100" {
		queryParams.Set("limit", limitValue)
	}
	if followMode {
		queryParams.Set("follow", "true")
	}
	return queryParams
}

// resolveAuditSinceValue converts a --since flag value to an RFC3339 timestamp.
// Accepts RFC3339 directly or duration strings like "1h", "24h", "7d".
func resolveAuditSinceValue(sinceInput string) string {
	if _, parseError := time.Parse(time.RFC3339, sinceInput); parseError == nil {
		return sinceInput
	}

	durationInput := sinceInput
	if strings.HasSuffix(durationInput, "d") {
		dayCount, conversionError := strconv.Atoi(strings.TrimSuffix(durationInput, "d"))
		if conversionError == nil {
			return time.Now().Add(-time.Duration(dayCount) * 24 * time.Hour).UTC().Format(time.RFC3339)
		}
	}

	parsedDuration, durationError := time.ParseDuration(durationInput)
	if durationError == nil {
		return time.Now().Add(-parsedDuration).UTC().Format(time.RFC3339)
	}

	fmt.Fprintf(os.Stderr, "invalid --since value: %q (use RFC3339 or duration like 1h, 24h, 7d)\n", sinceInput)
	os.Exit(1)
	return ""
}

// executeAuditQuery fetches audit entries and prints them as a table.
func executeAuditQuery(requestURL string) {
	httpResponse, requestError := buildAuthenticatedHTTPClient().Get(requestURL) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	checkAPIResponseStatus(httpResponse)

	responseBody, readError := io.ReadAll(httpResponse.Body)
	if readError != nil {
		fmt.Fprintf(os.Stderr, "error reading response: %v\n", readError)
		os.Exit(1)
	}

	var auditEntries []auditEntryResponse
	if unmarshalError := json.Unmarshal(responseBody, &auditEntries); unmarshalError != nil {
		fmt.Fprintf(os.Stderr, "error parsing response: %v\n", unmarshalError)
		os.Exit(1)
	}

	if len(auditEntries) == 0 {
		fmt.Println("No audit entries found.")
		return
	}

	printAuditTable(auditEntries)
}

// executeAuditFollow streams new audit entries via SSE until interrupted.
func executeAuditFollow(requestURL string) {
	httpResponse, requestError := buildAuthenticatedHTTPClient().Get(requestURL) //nolint:gosec // CLI connects to user-configured API server
	if requestError != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to ccattler — is the server running?")
		os.Exit(1)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	checkAPIResponseStatus(httpResponse)

	headerLine := fmt.Sprintf("%-19s  %-24s  %-12s  %-40s  %-6s  %-12s",
		"TIMESTAMP", "PRINCIPAL", "ACTION", "TARGET", "DECISION", "AUTH_METHOD")
	fmt.Println(headerLine)

	scanner := bufio.NewScanner(httpResponse.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		jsonPayload := strings.TrimPrefix(line, "data: ")

		var entry auditEntryResponse
		if unmarshalError := json.Unmarshal([]byte(jsonPayload), &entry); unmarshalError != nil {
			continue
		}
		printAuditEntryRow(entry)
	}
}

// printAuditTable prints a formatted table of audit entries with aligned columns.
func printAuditTable(auditEntries []auditEntryResponse) {
	header := []string{"TIMESTAMP", "PRINCIPAL", "ACTION", "TARGET", "DECISION", "AUTH_METHOD"}
	var rows [][]string
	for _, entry := range auditEntries {
		formattedTimestamp := entry.Timestamp.Local().Format("2006-01-02 15:04:05")
		authMethodDisplay := entry.AuthMethod
		if authMethodDisplay == "" {
			authMethodDisplay = "-"
		}
		rows = append(rows, []string{
			formattedTimestamp,
			entry.Principal,
			entry.Action,
			truncateAuditValue(entry.Target, 50),
			entry.Decision,
			authMethodDisplay,
		})
	}

	columnWidths := make([]int, len(header))
	for columnIndex, columnHeader := range header {
		columnWidths[columnIndex] = len(columnHeader)
	}
	for _, row := range rows {
		for columnIndex, cellValue := range row {
			if len(cellValue) > columnWidths[columnIndex] {
				columnWidths[columnIndex] = len(cellValue)
			}
		}
	}

	formatParts := make([]string, len(header))
	for columnIndex, columnWidth := range columnWidths {
		formatParts[columnIndex] = fmt.Sprintf("%%-%ds", columnWidth)
	}
	formatString := strings.Join(formatParts, "  ")

	headerValues := make([]interface{}, len(header))
	for columnIndex, columnHeader := range header {
		headerValues[columnIndex] = columnHeader
	}
	fmt.Printf(formatString+"\n", headerValues...)

	for _, row := range rows {
		rowValues := make([]interface{}, len(row))
		for columnIndex, cellValue := range row {
			rowValues[columnIndex] = cellValue
		}
		fmt.Printf(formatString+"\n", rowValues...)
	}
}

// printAuditEntryRow prints a single audit entry as a fixed-width row for follow mode.
func printAuditEntryRow(entry auditEntryResponse) {
	formattedTimestamp := entry.Timestamp.Local().Format("2006-01-02 15:04:05")
	authMethodDisplay := entry.AuthMethod
	if authMethodDisplay == "" {
		authMethodDisplay = "-"
	}
	fmt.Printf("%-19s  %-24s  %-12s  %-40s  %-6s  %-12s\n",
		formattedTimestamp,
		entry.Principal,
		entry.Action,
		truncateAuditValue(entry.Target, 40),
		entry.Decision,
		authMethodDisplay,
	)
}

// truncateAuditValue shortens a string to maxLength, appending "..." if truncated.
func truncateAuditValue(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	return value[:maxLength-3] + "..."
}
