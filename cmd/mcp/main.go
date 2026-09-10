package main

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

// mcpServerConfig holds all configuration for the MCP server instance.
type mcpServerConfig struct {
	// repoPath is the absolute path to the CCattler repository on this host.
	repoPath string
	// logFilePath is where the server writes its diagnostic log (not stdout — that's the transport).
	logFilePath string
	// etcdEndpoints is the comma-separated list of etcd endpoints for cca commands.
	etcdEndpoints string
	// authToken is the shared secret that must be presented during initialization.
	// Empty means authentication is disabled (SSH key is the only gate).
	authToken string
	// readOnly when true disables all mutation tools (stop, start, deploy, build, pull, apply).
	readOnly bool
	// auditLogPath is the path to a structured JSON audit log. Empty means no audit logging.
	auditLogPath string
}

// mutationTools lists the tool names that modify state. Blocked when readOnly is true.
var mutationTools = map[string]bool{
	"git_pull":        true,
	"build":           true,
	"test":            true,
	"stop_component":  true,
	"start_component": true,
	"deploy":          true,
	"apply_config":    true,
}

// clientAuthenticated tracks whether the current session has passed token validation.
// Set to true during handleInitialize if the token matches (or if no token is configured).
var clientAuthenticated bool

// auditLogger writes structured JSON entries to the audit log file.
var auditLogger *log.Logger

// auditLogEntry represents a single entry in the structured audit log.
type auditLogEntry struct {
	Timestamp  string         `json:"timestamp"`
	Tool       string         `json:"tool"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	DurationMs int64          `json:"duration_ms"`
	Success    bool           `json:"success"`
	Error      string         `json:"error,omitempty"`
}

// jsonRPCRequest represents an incoming JSON-RPC 2.0 request from the MCP client.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse represents an outgoing JSON-RPC 2.0 response to the MCP client.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError represents a JSON-RPC 2.0 error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpInitializeParams holds the client's initialize request parameters.
type mcpInitializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
	// Token is the authentication token presented by the client. Validated against
	// the server's configured token during initialization.
	Token string `json:"token,omitempty"`
}

// mcpInitializeResult is the server's response to an initialize request.
type mcpInitializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    mcpCapabilities   `json:"capabilities"`
	ServerInfo      mcpServerInfo     `json:"serverInfo"`
	Instructions    string            `json:"instructions"`
}

// mcpCapabilities describes what the server supports.
type mcpCapabilities struct {
	Tools *mcpToolCapability `json:"tools,omitempty"`
}

// mcpToolCapability indicates the server provides tools.
type mcpToolCapability struct {
	ListChanged bool `json:"listChanged"`
}

// mcpServerInfo identifies this server to the client.
type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// mcpToolInfo describes a single tool in the tools/list response.
type mcpToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpToolsListResult is the response to tools/list.
type mcpToolsListResult struct {
	Tools []mcpToolInfo `json:"tools"`
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

// mcpContent represents a content block in a tool result.
type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// diagnosticLogger is the package-level logger that writes to a file (never stdout).
var diagnosticLogger *log.Logger

// serverConfig is the global server configuration set during startup.
var serverConfig mcpServerConfig

func main() {
	repoPathFlag := flag.String("repo", "/home/bojan/ccattler", "path to CCattler repository")
	logFileFlag := flag.String("log", "/tmp/ccattler-mcp.log", "path to diagnostic log file")
	etcdFlag := flag.String("etcd", "localhost:2379", "etcd endpoints")
	tokenFlag := flag.String("token", "", "authentication token (clients must present this during init)")
	tokenFileFlag := flag.String("token-file", "", "path to file containing the authentication token")
	readOnlyFlag := flag.Bool("read-only", false, "disable all mutation tools (status and logs only)")
	auditLogFlag := flag.String("audit-log", "", "path to structured JSON audit log (empty = disabled)")
	flag.Parse()

	resolvedToken := *tokenFlag
	if *tokenFileFlag != "" {
		tokenBytes, tokenReadError := os.ReadFile(*tokenFileFlag)
		if tokenReadError != nil {
			fmt.Fprintf(os.Stderr, "failed to read token file: %v\n", tokenReadError)
			os.Exit(1)
		}
		resolvedToken = strings.TrimSpace(string(tokenBytes))
	}

	serverConfig = mcpServerConfig{
		repoPath:      *repoPathFlag,
		logFilePath:   *logFileFlag,
		etcdEndpoints: *etcdFlag,
		authToken:     resolvedToken,
		readOnly:      *readOnlyFlag,
		auditLogPath:  *auditLogFlag,
	}

	logFile, logOpenError := os.OpenFile(serverConfig.logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logOpenError != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file: %v\n", logOpenError)
		os.Exit(1)
	}
	defer logFile.Close()
	diagnosticLogger = log.New(logFile, "mcp: ", log.LstdFlags)

	if serverConfig.auditLogPath != "" {
		auditFile, auditOpenError := os.OpenFile(serverConfig.auditLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if auditOpenError != nil {
			fmt.Fprintf(os.Stderr, "failed to open audit log: %v\n", auditOpenError)
			os.Exit(1)
		}
		defer auditFile.Close()
		auditLogger = log.New(auditFile, "", 0)
	}

	diagnosticLogger.Printf("CCattler MCP server starting (auth=%t, read-only=%t, audit=%t)\n",
		serverConfig.authToken != "", serverConfig.readOnly, auditLogger != nil)

	if serverConfig.authToken == "" {
		clientAuthenticated = true
	} else if transportToken := os.Getenv("CCATTLER_MCP_TOKEN"); transportToken != "" {
		if subtle.ConstantTimeCompare([]byte(serverConfig.authToken), []byte(transportToken)) == 1 {
			clientAuthenticated = true
			diagnosticLogger.Println("pre-authenticated via CCATTLER_MCP_TOKEN environment variable")
			writeAuditEntry("initialize", nil, 0, true, "pre-authenticated via env var")
		} else {
			fmt.Fprintf(os.Stderr, "CCATTLER_MCP_TOKEN does not match configured token\n")
			os.Exit(1)
		}
	}

	runProtocolLoop(os.Stdin, os.Stdout)
}

// runProtocolLoop reads JSON-RPC requests from the reader and writes responses to the writer.
// It processes one request at a time until EOF.
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

		diagnosticLogger.Printf("received: method=%s id=%s\n", incomingRequest.Method, string(incomingRequest.ID))

		outgoingResponse := handleIncomingRequest(incomingRequest)
		if outgoingResponse == nil {
			continue
		}

		responseBytes, marshalError := json.Marshal(outgoingResponse)
		if marshalError != nil {
			diagnosticLogger.Printf("marshal error: %v\n", marshalError)
			continue
		}

		bufferedWriter.Write(responseBytes)
		bufferedWriter.WriteByte('\n')
		bufferedWriter.Flush()
	}
}

// handleIncomingRequest dispatches an incoming JSON-RPC request to the appropriate handler.
// Returns nil for notifications (requests without an ID that don't expect a response).
func handleIncomingRequest(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	switch incomingRequest.Method {
	case "initialize":
		return handleInitialize(incomingRequest)
	case "notifications/initialized":
		diagnosticLogger.Println("client initialized")
		return nil
	case "ping":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      incomingRequest.ID,
			Result:  map[string]any{},
		}
	case "tools/list":
		return handleToolsList(incomingRequest)
	case "tools/call":
		return handleToolsCall(incomingRequest)
	default:
		diagnosticLogger.Printf("unknown method: %s\n", incomingRequest.Method)
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      incomingRequest.ID,
			Error: &jsonRPCError{
				Code:    -32601,
				Message: fmt.Sprintf("unknown method: %s", incomingRequest.Method),
			},
		}
	}
}

// handleInitialize responds to the MCP initialize handshake with server capabilities.
// If a token is configured, validates the client's token using constant-time comparison.
func handleInitialize(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	diagnosticLogger.Println("handling initialize")

	if serverConfig.authToken != "" && !clientAuthenticated {
		var initParams mcpInitializeParams
		if incomingRequest.Params != nil {
			json.Unmarshal(incomingRequest.Params, &initParams)
		}

		tokenMatch := subtle.ConstantTimeCompare(
			[]byte(serverConfig.authToken),
			[]byte(initParams.Token),
		) == 1

		if !tokenMatch {
			diagnosticLogger.Println("authentication failed: invalid token")
			writeAuditEntry("initialize", nil, 0, false, "authentication failed")
			clientAuthenticated = false
			return &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      incomingRequest.ID,
				Error: &jsonRPCError{
					Code:    -32001,
					Message: "authentication failed: invalid token",
				},
			}
		}

		clientAuthenticated = true
		diagnosticLogger.Println("authentication successful via init token")
		writeAuditEntry("initialize", nil, 0, true, "")
	}

	modeLabel := "full access"
	if serverConfig.readOnly {
		modeLabel = "read-only mode"
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      incomingRequest.ID,
		Result: mcpInitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: mcpCapabilities{
				Tools: &mcpToolCapability{ListChanged: false},
			},
			ServerInfo: mcpServerInfo{
				Name:    "ccattler-mcp",
				Version: "0.2.0",
			},
			Instructions: fmt.Sprintf("CCattler remote management server (%s). Provides tools for building, testing, deploying, and monitoring CCattler on this host. All operations are guardrailed — no arbitrary command execution.", modeLabel),
		},
	}
}

// handleToolsList returns the list of all registered tools with their schemas.
// In read-only mode, mutation tools are excluded from the list entirely.
func handleToolsList(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	if !clientAuthenticated {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      incomingRequest.ID,
			Error: &jsonRPCError{
				Code:    -32001,
				Message: "not authenticated: send a valid token in the initialize request",
			},
		}
	}

	registeredTools := buildToolRegistry()
	toolInfoList := make([]mcpToolInfo, 0, len(registeredTools))
	for _, registeredTool := range registeredTools {
		if serverConfig.readOnly && mutationTools[registeredTool.name] {
			continue
		}
		toolInfoList = append(toolInfoList, mcpToolInfo{
			Name:        registeredTool.name,
			Description: registeredTool.description,
			InputSchema: registeredTool.inputSchema,
		})
	}
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      incomingRequest.ID,
		Result:  mcpToolsListResult{Tools: toolInfoList},
	}
}

// handleToolsCall executes a named tool with the provided arguments and returns the result.
// Enforces authentication (token must have been validated during init), read-only mode
// (blocks mutation tools), and writes a structured audit log entry for every call.
func handleToolsCall(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	if !clientAuthenticated {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      incomingRequest.ID,
			Error: &jsonRPCError{
				Code:    -32001,
				Message: "not authenticated: send a valid token in the initialize request",
			},
		}
	}

	var callParams mcpToolCallParams
	if unmarshalError := json.Unmarshal(incomingRequest.Params, &callParams); unmarshalError != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      incomingRequest.ID,
			Error: &jsonRPCError{
				Code:    -32602,
				Message: fmt.Sprintf("invalid params: %v", unmarshalError),
			},
		}
	}

	if serverConfig.readOnly && mutationTools[callParams.Name] {
		diagnosticLogger.Printf("blocked mutation tool %s in read-only mode\n", callParams.Name)
		writeAuditEntry(callParams.Name, callParams.Arguments, 0, false, "blocked: read-only mode")
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      incomingRequest.ID,
			Result: mcpToolCallResult{
				Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("error: tool %q is blocked in read-only mode", callParams.Name)}},
				IsError: true,
			},
		}
	}

	diagnosticLogger.Printf("tool call: %s args=%v\n", callParams.Name, callParams.Arguments)

	callStartTime := time.Now()

	registeredTools := buildToolRegistry()
	for _, registeredTool := range registeredTools {
		if registeredTool.name == callParams.Name {
			toolOutput, toolError := registeredTool.handler(callParams.Arguments)
			callDuration := time.Since(callStartTime).Milliseconds()

			if toolError != nil {
				diagnosticLogger.Printf("tool %s error: %v\n", callParams.Name, toolError)
				writeAuditEntry(callParams.Name, callParams.Arguments, callDuration, false, toolError.Error())
				return &jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      incomingRequest.ID,
					Result: mcpToolCallResult{
						Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("error: %v", toolError)}},
						IsError: true,
					},
				}
			}

			writeAuditEntry(callParams.Name, callParams.Arguments, callDuration, true, "")
			return &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      incomingRequest.ID,
				Result: mcpToolCallResult{
					Content: []mcpContent{{Type: "text", Text: toolOutput}},
				},
			}
		}
	}

	writeAuditEntry(callParams.Name, callParams.Arguments, 0, false, "unknown tool")
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      incomingRequest.ID,
		Error: &jsonRPCError{
			Code:    -32602,
			Message: fmt.Sprintf("unknown tool: %s", callParams.Name),
		},
	}
}

// writeAuditEntry appends a structured JSON entry to the audit log. No-op if audit logging
// is disabled. Redacts sensitive argument values before writing.
func writeAuditEntry(toolName string, arguments map[string]any, durationMs int64, success bool, errorMessage string) {
	if auditLogger == nil {
		return
	}

	sanitizedArguments := redactSensitiveArguments(arguments)

	entry := auditLogEntry{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Tool:       toolName,
		Arguments:  sanitizedArguments,
		DurationMs: durationMs,
		Success:    success,
		Error:      errorMessage,
	}

	entryBytes, marshalError := json.Marshal(entry)
	if marshalError != nil {
		diagnosticLogger.Printf("audit marshal error: %v\n", marshalError)
		return
	}
	auditLogger.Println(string(entryBytes))
}

// redactSensitiveArguments returns a copy of the arguments map with sensitive values replaced.
// Redacts values for keys containing "token", "password", "secret", or "key".
func redactSensitiveArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}

	redacted := make(map[string]any, len(arguments))
	for argumentKey, argumentValue := range arguments {
		lowerKey := strings.ToLower(argumentKey)
		if strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "password") ||
			strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "key") {
			redacted[argumentKey] = "[REDACTED]"
		} else {
			redacted[argumentKey] = argumentValue
		}
	}
	return redacted
}
