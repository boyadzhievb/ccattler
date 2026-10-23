// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain initializes the package-level diagnosticLogger and nodeHostname so
// that handler functions can be called outside the real main() entry point.
func TestMain(mainRunner *testing.M) {
	diagnosticLogger = log.New(io.Discard, "", 0)
	nodeHostname = "test-host"
	os.Exit(mainRunner.Run())
}

// TestHandleInitializeReturnsProtocolVersionAndCapabilities verifies that the
// initialize handler returns the expected MCP handshake fields.
func TestHandleInitializeReturnsProtocolVersionAndCapabilities(testContext *testing.T) {
	initializeRequest := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	}

	initializeResponse := handleInitialize(initializeRequest)
	if initializeResponse == nil {
		testContext.Fatal("expected non-nil response from initialize")
	}
	if initializeResponse.JSONRPC != "2.0" {
		testContext.Errorf("expected jsonrpc 2.0, got %s", initializeResponse.JSONRPC)
	}

	resultMap, castOk := initializeResponse.Result.(map[string]any)
	if !castOk {
		testContext.Fatal("expected result to be map[string]any")
	}
	if resultMap["protocolVersion"] != "2024-11-05" {
		testContext.Errorf("expected protocolVersion 2024-11-05, got %v", resultMap["protocolVersion"])
	}
	serverInfo, serverInfoOk := resultMap["serverInfo"].(map[string]any)
	if !serverInfoOk {
		testContext.Fatal("expected serverInfo to be map[string]any")
	}
	expectedServerName := "cca-mcp-remote@test-host"
	if serverInfo["name"] != expectedServerName {
		testContext.Errorf("expected server name %q, got %v", expectedServerName, serverInfo["name"])
	}
}

// TestHandleToolsListReturnsFourTools verifies that tools/list returns all
// four registered tools with correct names.
func TestHandleToolsListReturnsFourTools(testContext *testing.T) {
	toolsListRequest := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	}

	toolsListResponse := handleToolsList(toolsListRequest)
	if toolsListResponse == nil {
		testContext.Fatal("expected non-nil response from tools/list")
	}

	resultMap, castOk := toolsListResponse.Result.(map[string]any)
	if !castOk {
		testContext.Fatal("expected result to be map[string]any")
	}
	toolsList, toolsOk := resultMap["tools"].([]mcpToolInfo)
	if !toolsOk {
		testContext.Fatal("expected tools to be []mcpToolInfo")
	}
	if len(toolsList) != 4 {
		testContext.Fatalf("expected 4 tools, got %d", len(toolsList))
	}

	expectedToolNames := map[string]bool{"exec": true, "read_file": true, "write_file": true, "list_dir": true}
	for _, toolInfo := range toolsList {
		if !expectedToolNames[toolInfo.Name] {
			testContext.Errorf("unexpected tool name: %s", toolInfo.Name)
		}
	}
}

// TestExecuteShellCommandRunsEchoAndReturnsOutput verifies that a simple echo
// command produces the expected output.
func TestExecuteShellCommandRunsEchoAndReturnsOutput(testContext *testing.T) {
	arguments := map[string]any{"command": "echo hello"}
	outputText, isError := executeShellCommand(arguments)
	if isError {
		testContext.Errorf("expected no error, got: %s", outputText)
	}
	if strings.TrimSpace(outputText) != "hello" {
		testContext.Errorf("expected 'hello', got %q", outputText)
	}
}

// TestExecuteShellCommandMissingCommandArgReturnsError verifies that omitting
// the command argument produces an error result.
func TestExecuteShellCommandMissingCommandArgReturnsError(testContext *testing.T) {
	arguments := map[string]any{}
	outputText, isError := executeShellCommand(arguments)
	if !isError {
		testContext.Error("expected error for missing command argument")
	}
	if !strings.Contains(outputText, "'command' argument is required") {
		testContext.Errorf("expected 'command' argument error, got: %s", outputText)
	}
}

// TestExecuteShellCommandRespectsWorkingDirectory verifies that the
// working_directory argument changes the command's working directory.
func TestExecuteShellCommandRespectsWorkingDirectory(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	arguments := map[string]any{
		"command":           "pwd",
		"working_directory": temporaryDirectory,
	}
	outputText, isError := executeShellCommand(arguments)
	if isError {
		testContext.Errorf("expected no error, got: %s", outputText)
	}
	if strings.TrimSpace(outputText) != temporaryDirectory {
		testContext.Errorf("expected working dir %q, got %q", temporaryDirectory, strings.TrimSpace(outputText))
	}
}

// TestExecuteShellCommandRespectsTimeout verifies that a long-running command
// is killed when the timeout expires.
func TestExecuteShellCommandRespectsTimeout(testContext *testing.T) {
	arguments := map[string]any{
		"command":         "sleep 30",
		"timeout_seconds": 0.5,
	}
	outputText, isError := executeShellCommand(arguments)
	if !isError {
		testContext.Error("expected error from timed-out command")
	}
	if !strings.Contains(outputText, "exit error") {
		testContext.Errorf("expected exit error in output, got: %s", outputText)
	}
}

// TestExecuteShellCommandReportsNonZeroExit verifies that a failing command
// returns isError true.
func TestExecuteShellCommandReportsNonZeroExit(testContext *testing.T) {
	arguments := map[string]any{"command": "exit 42"}
	_, isError := executeShellCommand(arguments)
	if !isError {
		testContext.Error("expected error for non-zero exit code")
	}
}

// TestReadFileContentsReadsExistingFile verifies that read_file returns the
// content of a file.
func TestReadFileContentsReadsExistingFile(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	testFilePath := filepath.Join(temporaryDirectory, "test.txt")
	if writeError := os.WriteFile(testFilePath, []byte("file content here"), 0600); writeError != nil {
		testContext.Fatal(writeError)
	}
	arguments := map[string]any{"path": testFilePath}
	outputText, isError := readFileContents(arguments)
	if isError {
		testContext.Errorf("expected no error, got: %s", outputText)
	}
	if outputText != "file content here" {
		testContext.Errorf("expected 'file content here', got %q", outputText)
	}
}

// TestReadFileContentsMissingPathReturnsError verifies that omitting the path
// argument produces an error.
func TestReadFileContentsMissingPathReturnsError(testContext *testing.T) {
	arguments := map[string]any{}
	outputText, isError := readFileContents(arguments)
	if !isError {
		testContext.Error("expected error for missing path")
	}
	if !strings.Contains(outputText, "'path' argument is required") {
		testContext.Errorf("expected path error, got: %s", outputText)
	}
}

// TestReadFileContentsNonexistentFileReturnsError verifies that reading a
// nonexistent file produces an error.
func TestReadFileContentsNonexistentFileReturnsError(testContext *testing.T) {
	arguments := map[string]any{"path": "/tmp/nonexistent-cca-mcp-test-file-xyz"}
	outputText, isError := readFileContents(arguments)
	if !isError {
		testContext.Error("expected error for nonexistent file")
	}
	if !strings.Contains(outputText, "error reading") {
		testContext.Errorf("expected read error, got: %s", outputText)
	}
}

// TestWriteFileContentsCreatesFileAndParentDirs verifies that write_file
// creates the file and any missing parent directories.
func TestWriteFileContentsCreatesFileAndParentDirs(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	testFilePath := filepath.Join(temporaryDirectory, "sub", "dir", "output.txt")
	arguments := map[string]any{"path": testFilePath, "content": "written by test"}
	outputText, isError := writeFileContents(arguments)
	if isError {
		testContext.Errorf("expected no error, got: %s", outputText)
	}
	if !strings.Contains(outputText, "wrote 15 bytes") {
		testContext.Errorf("expected 'wrote 15 bytes' in output, got: %s", outputText)
	}
	readBack, readError := os.ReadFile(testFilePath) //nolint:gosec // test reads from t.TempDir()
	if readError != nil {
		testContext.Fatal(readError)
	}
	if string(readBack) != "written by test" {
		testContext.Errorf("expected 'written by test', got %q", string(readBack))
	}
}

// TestWriteFileContentsMissingPathReturnsError verifies that omitting the path
// argument produces an error.
func TestWriteFileContentsMissingPathReturnsError(testContext *testing.T) {
	arguments := map[string]any{"content": "stuff"}
	outputText, isError := writeFileContents(arguments)
	if !isError {
		testContext.Error("expected error for missing path")
	}
	if !strings.Contains(outputText, "'path' argument is required") {
		testContext.Errorf("expected path error, got: %s", outputText)
	}
}

// TestWriteFileContentsMissingContentReturnsError verifies that omitting the
// content argument produces an error.
func TestWriteFileContentsMissingContentReturnsError(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	arguments := map[string]any{"path": filepath.Join(temporaryDirectory, "f.txt")}
	outputText, isError := writeFileContents(arguments)
	if !isError {
		testContext.Error("expected error for missing content")
	}
	if !strings.Contains(outputText, "'content' argument is required") {
		testContext.Errorf("expected content error, got: %s", outputText)
	}
}

// TestListDirectoryContentsListsFilesAndDirs verifies that list_dir returns
// entries with correct type markers.
func TestListDirectoryContentsListsFilesAndDirs(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	if writeError := os.WriteFile(filepath.Join(temporaryDirectory, "afile.txt"), []byte("abc"), 0600); writeError != nil {
		testContext.Fatal(writeError)
	}
	if mkdirError := os.Mkdir(filepath.Join(temporaryDirectory, "subdir"), 0750); mkdirError != nil {
		testContext.Fatal(mkdirError)
	}
	arguments := map[string]any{"path": temporaryDirectory}
	outputText, isError := listDirectoryContents(arguments)
	if isError {
		testContext.Errorf("expected no error, got: %s", outputText)
	}
	if !strings.Contains(outputText, "afile.txt") {
		testContext.Errorf("expected afile.txt in output, got: %s", outputText)
	}
	if !strings.Contains(outputText, "subdir/") {
		testContext.Errorf("expected subdir/ in output, got: %s", outputText)
	}
	if !strings.Contains(outputText, "3 bytes") {
		testContext.Errorf("expected '3 bytes' for afile.txt, got: %s", outputText)
	}
}

// TestListDirectoryContentsNonexistentDirReturnsError verifies that listing a
// nonexistent directory produces an error.
func TestListDirectoryContentsNonexistentDirReturnsError(testContext *testing.T) {
	arguments := map[string]any{"path": "/tmp/nonexistent-cca-mcp-dir-xyz"}
	outputText, isError := listDirectoryContents(arguments)
	if !isError {
		testContext.Error("expected error for nonexistent directory")
	}
	if !strings.Contains(outputText, "error reading directory") {
		testContext.Errorf("expected directory error, got: %s", outputText)
	}
}

// TestListDirectoryContentsEmptyPathDefaultsToCwd verifies that an empty path
// defaults to the current directory without error.
func TestListDirectoryContentsEmptyPathDefaultsToCwd(testContext *testing.T) {
	arguments := map[string]any{}
	_, isError := listDirectoryContents(arguments)
	if isError {
		testContext.Error("expected no error when listing current directory")
	}
}

// TestHandleIncomingRequestDispatchesPing verifies that the ping method returns
// an empty result.
func TestHandleIncomingRequestDispatchesPing(testContext *testing.T) {
	pingRequest := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`10`),
		Method:  "ping",
	}
	pingResponse := handleIncomingRequest(pingRequest)
	if pingResponse == nil {
		testContext.Fatal("expected non-nil response from ping")
	}
	resultMap, castOk := pingResponse.Result.(map[string]any)
	if !castOk {
		testContext.Fatal("expected result to be map[string]any")
	}
	if len(resultMap) != 0 {
		testContext.Errorf("expected empty result for ping, got %v", resultMap)
	}
}

// TestHandleIncomingRequestNotificationReturnsNil verifies that the
// notifications/initialized method returns nil (no response sent).
func TestHandleIncomingRequestNotificationReturnsNil(testContext *testing.T) {
	notificationRequest := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	notificationResponse := handleIncomingRequest(notificationRequest)
	if notificationResponse != nil {
		testContext.Error("expected nil response for notification")
	}
}

// TestHandleIncomingRequestUnknownMethodReturnsError verifies that an unknown
// method returns a JSON-RPC -32601 error.
func TestHandleIncomingRequestUnknownMethodReturnsError(testContext *testing.T) {
	unknownRequest := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`11`),
		Method:  "nonexistent/method",
	}
	errorResponse := handleIncomingRequest(unknownRequest)
	if errorResponse == nil {
		testContext.Fatal("expected non-nil error response")
	}
	if errorResponse.Error == nil {
		testContext.Fatal("expected error field to be set")
	}
	if errorResponse.Error.Code != -32601 {
		testContext.Errorf("expected error code -32601, got %d", errorResponse.Error.Code)
	}
}

// TestDispatchToolCallUnknownToolReturnsError verifies that dispatching an
// unknown tool name returns an error.
func TestDispatchToolCallUnknownToolReturnsError(testContext *testing.T) {
	outputText, isError := dispatchToolCall("nonexistent_tool", nil)
	if !isError {
		testContext.Error("expected error for unknown tool")
	}
	if !strings.Contains(outputText, "unknown tool") {
		testContext.Errorf("expected 'unknown tool' in output, got: %s", outputText)
	}
}

// TestRunProtocolLoopEndToEnd verifies the full request/response cycle through
// the protocol loop by writing JSON-RPC lines to a pipe and reading responses.
func TestRunProtocolLoopEndToEnd(testContext *testing.T) {
	initializeRequestJSON := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"
	toolsListRequestJSON := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	pingRequestJSON := `{"jsonrpc":"2.0","id":3,"method":"ping"}` + "\n"

	inputBuffer := bytes.NewBufferString(initializeRequestJSON + toolsListRequestJSON + pingRequestJSON)
	var outputBuffer bytes.Buffer

	runProtocolLoop(inputBuffer, &outputBuffer)

	responseLines := strings.Split(strings.TrimSpace(outputBuffer.String()), "\n")
	if len(responseLines) != 3 {
		testContext.Fatalf("expected 3 response lines, got %d: %v", len(responseLines), responseLines)
	}

	var initializeResponseParsed jsonRPCResponse
	if unmarshalError := json.Unmarshal([]byte(responseLines[0]), &initializeResponseParsed); unmarshalError != nil {
		testContext.Fatalf("failed to parse initialize response: %v", unmarshalError)
	}
	if string(initializeResponseParsed.ID) != "1" {
		testContext.Errorf("expected id 1, got %s", string(initializeResponseParsed.ID))
	}

	var toolsListResponseParsed jsonRPCResponse
	if unmarshalError := json.Unmarshal([]byte(responseLines[1]), &toolsListResponseParsed); unmarshalError != nil {
		testContext.Fatalf("failed to parse tools/list response: %v", unmarshalError)
	}
	if string(toolsListResponseParsed.ID) != "2" {
		testContext.Errorf("expected id 2, got %s", string(toolsListResponseParsed.ID))
	}

	var pingResponseParsed jsonRPCResponse
	if unmarshalError := json.Unmarshal([]byte(responseLines[2]), &pingResponseParsed); unmarshalError != nil {
		testContext.Fatalf("failed to parse ping response: %v", unmarshalError)
	}
	if string(pingResponseParsed.ID) != "3" {
		testContext.Errorf("expected id 3, got %s", string(pingResponseParsed.ID))
	}
}

// TestRunProtocolLoopToolsCallExecEndToEnd verifies that a tools/call request
// for the exec tool produces a correct result through the protocol loop.
func TestRunProtocolLoopToolsCallExecEndToEnd(testContext *testing.T) {
	execRequestJSON := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"exec","arguments":{"command":"echo protocol-test"}}}` + "\n"

	inputBuffer := bytes.NewBufferString(execRequestJSON)
	var outputBuffer bytes.Buffer

	runProtocolLoop(inputBuffer, &outputBuffer)

	responseLines := strings.Split(strings.TrimSpace(outputBuffer.String()), "\n")
	if len(responseLines) != 1 {
		testContext.Fatalf("expected 1 response line, got %d", len(responseLines))
	}

	var rawResponse map[string]json.RawMessage
	if unmarshalError := json.Unmarshal([]byte(responseLines[0]), &rawResponse); unmarshalError != nil {
		testContext.Fatalf("failed to parse response: %v", unmarshalError)
	}

	var toolCallResult mcpToolCallResult
	if unmarshalError := json.Unmarshal(rawResponse["result"], &toolCallResult); unmarshalError != nil {
		testContext.Fatalf("failed to parse tool call result: %v", unmarshalError)
	}
	if toolCallResult.IsError {
		testContext.Errorf("expected no error, got isError=true: %v", toolCallResult.Content)
	}
	if len(toolCallResult.Content) == 0 {
		testContext.Fatal("expected at least one content block")
	}
	if !strings.Contains(toolCallResult.Content[0].Text, "protocol-test") {
		testContext.Errorf("expected 'protocol-test' in output, got: %s", toolCallResult.Content[0].Text)
	}
}

// TestRunProtocolLoopSkipsInvalidJSON verifies that an invalid JSON line is
// silently skipped and does not break subsequent requests.
func TestRunProtocolLoopSkipsInvalidJSON(testContext *testing.T) {
	inputLines := "this is not json\n" + `{"jsonrpc":"2.0","id":99,"method":"ping"}` + "\n"
	inputBuffer := bytes.NewBufferString(inputLines)
	var outputBuffer bytes.Buffer

	runProtocolLoop(inputBuffer, &outputBuffer)

	responseLines := strings.Split(strings.TrimSpace(outputBuffer.String()), "\n")
	if len(responseLines) != 1 {
		testContext.Fatalf("expected 1 response (ping only), got %d: %v", len(responseLines), responseLines)
	}
	if !strings.Contains(responseLines[0], `"id":99`) {
		testContext.Errorf("expected ping response with id 99, got: %s", responseLines[0])
	}
}

// TestHandleToolsCallWithInvalidParamsReturnsError verifies that a tools/call
// request with unparseable params returns a -32602 error.
func TestHandleToolsCallWithInvalidParamsReturnsError(testContext *testing.T) {
	badParamsRequest := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`20`),
		Method:  "tools/call",
		Params:  json.RawMessage(`"not an object"`),
	}
	errorResponse := handleToolsCall(badParamsRequest)
	if errorResponse == nil {
		testContext.Fatal("expected non-nil error response")
	}
	if errorResponse.Error == nil {
		testContext.Fatal("expected error field to be set")
	}
	if errorResponse.Error.Code != -32602 {
		testContext.Errorf("expected error code -32602, got %d", errorResponse.Error.Code)
	}
}

// TestWriteFileContentsTightensPermissions verifies that written files get 0600
// permissions and parent directories get 0750.
func TestWriteFileContentsTightensPermissions(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	nestedDirPath := filepath.Join(temporaryDirectory, "perm-test")
	testFilePath := filepath.Join(nestedDirPath, "secret.txt")

	arguments := map[string]any{"path": testFilePath, "content": "secret data"}
	outputText, isError := writeFileContents(arguments)
	if isError {
		testContext.Fatalf("expected no error, got: %s", outputText)
	}

	fileInfo, statError := os.Stat(testFilePath)
	if statError != nil {
		testContext.Fatal(statError)
	}
	filePermissions := fileInfo.Mode().Perm()
	if filePermissions != 0600 {
		testContext.Errorf("expected file permissions 0600, got %04o", filePermissions)
	}

	dirInfo, dirStatError := os.Stat(nestedDirPath)
	if dirStatError != nil {
		testContext.Fatal(dirStatError)
	}
	dirPermissions := dirInfo.Mode().Perm()
	if dirPermissions != 0750 {
		testContext.Errorf("expected dir permissions 0750, got %04o", dirPermissions)
	}
}

// TestBuildToolInfoDescriptorsAreNonEmpty verifies that all tool info builders
// return non-empty names, descriptions, and input schemas.
func TestBuildToolInfoDescriptorsAreNonEmpty(testContext *testing.T) {
	toolInfoBuilders := []struct {
		builderName string
		builderFunc func() mcpToolInfo
	}{
		{"buildExecToolInfo", buildExecToolInfo},
		{"buildReadFileToolInfo", buildReadFileToolInfo},
		{"buildWriteFileToolInfo", buildWriteFileToolInfo},
		{"buildListDirToolInfo", buildListDirToolInfo},
	}

	for _, toolInfoBuilder := range toolInfoBuilders {
		testContext.Run(toolInfoBuilder.builderName, func(subTestContext *testing.T) {
			toolInfo := toolInfoBuilder.builderFunc()
			if toolInfo.Name == "" {
				subTestContext.Error("expected non-empty Name")
			}
			if toolInfo.Description == "" {
				subTestContext.Error("expected non-empty Description")
			}
			if len(toolInfo.InputSchema) == 0 {
				subTestContext.Error("expected non-empty InputSchema")
			}
			schemaType, hasType := toolInfo.InputSchema["type"]
			if !hasType || schemaType != "object" {
				subTestContext.Errorf("expected InputSchema type 'object', got %v", schemaType)
			}
		})
	}
}

// TestExecuteShellCommandEmptyStringReturnsError verifies that an empty command
// string is treated as a missing argument.
func TestExecuteShellCommandEmptyStringReturnsError(testContext *testing.T) {
	arguments := map[string]any{"command": ""}
	_, isError := executeShellCommand(arguments)
	if !isError {
		testContext.Error("expected error for empty command string")
	}
}

// TestReadFileContentsEmptyPathReturnsError verifies that an empty path string
// produces an error.
func TestReadFileContentsEmptyPathReturnsError(testContext *testing.T) {
	arguments := map[string]any{"path": ""}
	_, isError := readFileContents(arguments)
	if !isError {
		testContext.Error("expected error for empty path")
	}
}

// TestWriteFileContentsEmptyPathReturnsError verifies that an empty path string
// produces an error.
func TestWriteFileContentsEmptyPathReturnsError(testContext *testing.T) {
	arguments := map[string]any{"path": "", "content": "stuff"}
	_, isError := writeFileContents(arguments)
	if !isError {
		testContext.Error("expected error for empty path")
	}
}

// testToolCallViaProtocol is a helper that sends a tools/call JSON-RPC request
// through the protocol loop and returns the parsed tool call result.
func testToolCallViaProtocol(testContext *testing.T, toolName string, arguments map[string]any) mcpToolCallResult {
	testContext.Helper()
	argumentsJSON, marshalError := json.Marshal(arguments)
	if marshalError != nil {
		testContext.Fatalf("failed to marshal arguments: %v", marshalError)
	}
	requestLine := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"%s","arguments":%s}}`, toolName, string(argumentsJSON)) + "\n"

	inputBuffer := bytes.NewBufferString(requestLine)
	var outputBuffer bytes.Buffer
	runProtocolLoop(inputBuffer, &outputBuffer)

	responseLine := strings.TrimSpace(outputBuffer.String())
	var rawResponse map[string]json.RawMessage
	if unmarshalError := json.Unmarshal([]byte(responseLine), &rawResponse); unmarshalError != nil {
		testContext.Fatalf("failed to parse response: %v", unmarshalError)
	}
	var toolCallResult mcpToolCallResult
	if unmarshalError := json.Unmarshal(rawResponse["result"], &toolCallResult); unmarshalError != nil {
		testContext.Fatalf("failed to parse tool call result: %v", unmarshalError)
	}
	return toolCallResult
}

// TestProtocolLoopReadFileEndToEnd verifies that read_file works through the
// full protocol loop.
func TestProtocolLoopReadFileEndToEnd(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	testFilePath := filepath.Join(temporaryDirectory, "proto-read.txt")
	if writeError := os.WriteFile(testFilePath, []byte("proto-content"), 0600); writeError != nil {
		testContext.Fatal(writeError)
	}

	toolCallResult := testToolCallViaProtocol(testContext, "read_file", map[string]any{"path": testFilePath})
	if toolCallResult.IsError {
		testContext.Errorf("expected no error, got: %v", toolCallResult.Content)
	}
	if len(toolCallResult.Content) == 0 || toolCallResult.Content[0].Text != "proto-content" {
		testContext.Errorf("expected 'proto-content', got: %v", toolCallResult.Content)
	}
}

// TestProtocolLoopWriteFileEndToEnd verifies that write_file works through the
// full protocol loop.
func TestProtocolLoopWriteFileEndToEnd(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	testFilePath := filepath.Join(temporaryDirectory, "proto-write.txt")

	toolCallResult := testToolCallViaProtocol(testContext, "write_file", map[string]any{"path": testFilePath, "content": "via-proto"})
	if toolCallResult.IsError {
		testContext.Errorf("expected no error, got: %v", toolCallResult.Content)
	}

	readBack, readError := os.ReadFile(testFilePath) //nolint:gosec // test reads from t.TempDir()
	if readError != nil {
		testContext.Fatal(readError)
	}
	if string(readBack) != "via-proto" {
		testContext.Errorf("expected 'via-proto', got %q", string(readBack))
	}
}
