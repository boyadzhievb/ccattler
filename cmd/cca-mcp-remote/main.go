// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// jsonRPCRequest represents an incoming JSON-RPC 2.0 request from the MCP client.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse represents an outgoing JSON-RPC 2.0 response to the MCP client.
type jsonRPCResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any            `json:"result,omitempty"`
	Error   *jsonRPCError  `json:"error,omitempty"`
}

// jsonRPCError represents a JSON-RPC 2.0 error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpContent represents a content block in a tool result.
type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// mcpToolCallParams holds the parameters of a tools/call request.
type mcpToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// mcpToolCallResult is the response to a tools/call request.
type mcpToolCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// mcpToolInfo describes a single tool in the tools/list response.
type mcpToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// diagnosticLogger writes to a log file (never stdout — that's the MCP transport).
var diagnosticLogger *log.Logger

// nodeHostname is the hostname of the machine, included in server identification.
var nodeHostname string

func main() {
	hostname, hostnameError := os.Hostname()
	if hostnameError != nil {
		hostname = "unknown"
	}
	nodeHostname = hostname

	logFile, logOpenError := os.OpenFile("/tmp/cca-mcp-remote.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if logOpenError != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file: %v\n", logOpenError)
		os.Exit(1)
	}
	defer func() { _ = logFile.Close() }()
	diagnosticLogger = log.New(logFile, "mcp-remote: ", log.LstdFlags)

	diagnosticLogger.Printf("starting on %s (pid %d)\n", nodeHostname, os.Getpid())
	runProtocolLoop(os.Stdin, os.Stdout)
}

// runProtocolLoop reads JSON-RPC requests line-by-line from stdin and writes responses to stdout.
func runProtocolLoop(inputReader io.Reader, outputWriter io.Writer) {
	bufferedReader := bufio.NewReader(inputReader)
	bufferedWriter := bufio.NewWriter(outputWriter)

	for {
		requestLine, readError := bufferedReader.ReadBytes('\n')
		if readError != nil {
			if readError == io.EOF {
				diagnosticLogger.Println("client disconnected (EOF)")
				return
			}
			diagnosticLogger.Printf("read error: %v\n", readError)
			return
		}

		var incomingRequest jsonRPCRequest
		if unmarshalError := json.Unmarshal(requestLine, &incomingRequest); unmarshalError != nil {
			diagnosticLogger.Printf("invalid JSON-RPC: %v\n", unmarshalError)
			continue
		}

		outgoingResponse := handleIncomingRequest(incomingRequest)
		if outgoingResponse == nil {
			continue
		}

		responseBytes, marshalError := json.Marshal(outgoingResponse)
		if marshalError != nil {
			diagnosticLogger.Printf("marshal error: %v\n", marshalError)
			continue
		}

		_, _ = bufferedWriter.Write(responseBytes)
		_ = bufferedWriter.WriteByte('\n')
		_ = bufferedWriter.Flush()
	}
}

// handleIncomingRequest dispatches a JSON-RPC request to the appropriate handler.
func handleIncomingRequest(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	switch incomingRequest.Method {
	case "initialize":
		return handleInitialize(incomingRequest)
	case "notifications/initialized":
		return nil
	case "ping":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: incomingRequest.ID, Result: map[string]any{}}
	case "tools/list":
		return handleToolsList(incomingRequest)
	case "tools/call":
		return handleToolsCall(incomingRequest)
	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0", ID: incomingRequest.ID,
			Error: &jsonRPCError{Code: -32601, Message: fmt.Sprintf("unknown method: %s", incomingRequest.Method)},
		}
	}
}

// handleInitialize responds to the MCP initialize handshake.
func handleInitialize(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	diagnosticLogger.Println("client initializing")
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      incomingRequest.ID,
		Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "cca-mcp-remote@" + nodeHostname, "version": "1.0.0"},
			"instructions":    fmt.Sprintf("Remote shell access to %s. Tools: exec (run commands), read_file, write_file, list_dir.", nodeHostname),
		},
	}
}

// handleToolsList returns the list of available remote-access tools.
func handleToolsList(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      incomingRequest.ID,
		Result: map[string]any{
			"tools": []mcpToolInfo{
				buildExecToolInfo(),
				buildReadFileToolInfo(),
				buildWriteFileToolInfo(),
				buildListDirToolInfo(),
			},
		},
	}
}

// handleToolsCall dispatches to the appropriate tool handler.
func handleToolsCall(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	var callParams mcpToolCallParams
	if unmarshalError := json.Unmarshal(incomingRequest.Params, &callParams); unmarshalError != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0", ID: incomingRequest.ID,
			Error: &jsonRPCError{Code: -32602, Message: fmt.Sprintf("invalid params: %v", unmarshalError)},
		}
	}

	diagnosticLogger.Printf("tool call: %s\n", callParams.Name)
	resultText, isError := dispatchToolCall(callParams.Name, callParams.Arguments)
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      incomingRequest.ID,
		Result:  mcpToolCallResult{Content: []mcpContent{{Type: "text", Text: resultText}}, IsError: isError},
	}
}

// dispatchToolCall routes to the tool implementation and returns (output, isError).
func dispatchToolCall(toolName string, arguments map[string]any) (string, bool) {
	switch toolName {
	case "exec":
		return executeShellCommand(arguments)
	case "read_file":
		return readFileContents(arguments)
	case "write_file":
		return writeFileContents(arguments)
	case "list_dir":
		return listDirectoryContents(arguments)
	default:
		return fmt.Sprintf("unknown tool: %s", toolName), true
	}
}

// executeShellCommand runs a command via /bin/sh and returns combined stdout+stderr.
func executeShellCommand(arguments map[string]any) (string, bool) {
	commandString, commandOk := arguments["command"].(string)
	if !commandOk || commandString == "" {
		return "error: 'command' argument is required", true
	}

	timeoutSeconds := 120.0
	if timeoutValue, hasTimeout := arguments["timeout_seconds"].(float64); hasTimeout && timeoutValue > 0 {
		timeoutSeconds = timeoutValue
	}

	workingDirectory, _ := arguments["working_directory"].(string)

	commandContext, cancelFunc := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancelFunc()

	shellCommand := exec.CommandContext(commandContext, "/bin/sh", "-c", commandString)
	if workingDirectory != "" {
		shellCommand.Dir = workingDirectory
	}

	combinedOutput, runError := shellCommand.CombinedOutput()
	outputText := string(combinedOutput)

	if runError != nil {
		return fmt.Sprintf("%s\nexit error: %v", outputText, runError), true
	}
	return outputText, false
}

// readFileContents reads a file and returns its content.
func readFileContents(arguments map[string]any) (string, bool) {
	filePath, pathOk := arguments["path"].(string)
	if !pathOk || filePath == "" {
		return "error: 'path' argument is required", true
	}

	fileContent, readError := os.ReadFile(filePath)
	if readError != nil {
		return fmt.Sprintf("error reading %s: %v", filePath, readError), true
	}
	return string(fileContent), false
}

// writeFileContents writes content to a file, creating parent directories as needed.
func writeFileContents(arguments map[string]any) (string, bool) {
	filePath, pathOk := arguments["path"].(string)
	if !pathOk || filePath == "" {
		return "error: 'path' argument is required", true
	}

	content, contentOk := arguments["content"].(string)
	if !contentOk {
		return "error: 'content' argument is required", true
	}

	parentDir := filepath.Dir(filePath)
	if mkdirError := os.MkdirAll(parentDir, 0755); mkdirError != nil {
		return fmt.Sprintf("error creating directory %s: %v", parentDir, mkdirError), true
	}

	if writeError := os.WriteFile(filePath, []byte(content), 0644); writeError != nil {
		return fmt.Sprintf("error writing %s: %v", filePath, writeError), true
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), filePath), false
}

// listDirectoryContents lists files in a directory with type and size info.
func listDirectoryContents(arguments map[string]any) (string, bool) {
	dirPath, pathOk := arguments["path"].(string)
	if !pathOk || dirPath == "" {
		dirPath = "."
	}

	entries, readError := os.ReadDir(dirPath)
	if readError != nil {
		return fmt.Sprintf("error reading directory %s: %v", dirPath, readError), true
	}

	var resultBuilder strings.Builder
	for _, entry := range entries {
		entryInfo, infoError := entry.Info()
		if infoError != nil {
			fmt.Fprintf(&resultBuilder, "%s (error: %v)\n", entry.Name(), infoError)
			continue
		}
		if entry.IsDir() {
			fmt.Fprintf(&resultBuilder, "%s/\n", entry.Name())
		} else {
			fmt.Fprintf(&resultBuilder, "%s  %d bytes\n", entry.Name(), entryInfo.Size())
		}
	}
	return resultBuilder.String(), false
}

// buildExecToolInfo returns the tool descriptor for the exec tool.
func buildExecToolInfo() mcpToolInfo {
	return mcpToolInfo{
		Name:        "exec",
		Description: "Execute a shell command on this node. Returns combined stdout+stderr and exit status.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":           map[string]any{"type": "string", "description": "Shell command to execute"},
				"working_directory": map[string]any{"type": "string", "description": "Working directory (optional, defaults to home)"},
				"timeout_seconds":   map[string]any{"type": "number", "description": "Timeout in seconds (default 120)"},
			},
			"required": []string{"command"},
		},
	}
}

// buildReadFileToolInfo returns the tool descriptor for the read_file tool.
func buildReadFileToolInfo() mcpToolInfo {
	return mcpToolInfo{
		Name:        "read_file",
		Description: "Read a file's contents from this node.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Absolute path to the file"},
			},
			"required": []string{"path"},
		},
	}
}

// buildWriteFileToolInfo returns the tool descriptor for the write_file tool.
func buildWriteFileToolInfo() mcpToolInfo {
	return mcpToolInfo{
		Name:        "write_file",
		Description: "Write content to a file on this node. Creates parent directories as needed.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Absolute path to write"},
				"content": map[string]any{"type": "string", "description": "File content to write"},
			},
			"required": []string{"path", "content"},
		},
	}
}

// buildListDirToolInfo returns the tool descriptor for the list_dir tool.
func buildListDirToolInfo() mcpToolInfo {
	return mcpToolInfo{
		Name:        "list_dir",
		Description: "List files and directories at a given path on this node.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory path (defaults to current directory)"},
			},
		},
	}
}
