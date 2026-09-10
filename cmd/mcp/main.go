package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
)

// mcpServerConfig holds all configuration for the MCP server instance.
type mcpServerConfig struct {
	// repoPath is the absolute path to the CCattler repository on this host.
	repoPath string
	// logFilePath is where the server writes its diagnostic log (not stdout — that's the transport).
	logFilePath string
	// etcdEndpoints is the comma-separated list of etcd endpoints for cca commands.
	etcdEndpoints string
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
	flag.Parse()

	serverConfig = mcpServerConfig{
		repoPath:      *repoPathFlag,
		logFilePath:   *logFileFlag,
		etcdEndpoints: *etcdFlag,
	}

	logFile, logOpenError := os.OpenFile(serverConfig.logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logOpenError != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file: %v\n", logOpenError)
		os.Exit(1)
	}
	defer logFile.Close()
	diagnosticLogger = log.New(logFile, "mcp: ", log.LstdFlags)
	diagnosticLogger.Println("CCattler MCP server starting")

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
func handleInitialize(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	diagnosticLogger.Println("handling initialize")
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
				Version: "0.1.0",
			},
			Instructions: "CCattler remote management server. Provides tools for building, testing, deploying, and monitoring CCattler on this host. All operations are guardrailed — no arbitrary command execution.",
		},
	}
}

// handleToolsList returns the list of all registered tools with their schemas.
func handleToolsList(incomingRequest jsonRPCRequest) *jsonRPCResponse {
	registeredTools := buildToolRegistry()
	toolInfoList := make([]mcpToolInfo, 0, len(registeredTools))
	for _, registeredTool := range registeredTools {
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
func handleToolsCall(incomingRequest jsonRPCRequest) *jsonRPCResponse {
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

	diagnosticLogger.Printf("tool call: %s args=%v\n", callParams.Name, callParams.Arguments)

	registeredTools := buildToolRegistry()
	for _, registeredTool := range registeredTools {
		if registeredTool.name == callParams.Name {
			toolOutput, toolError := registeredTool.handler(callParams.Arguments)
			if toolError != nil {
				diagnosticLogger.Printf("tool %s error: %v\n", callParams.Name, toolError)
				return &jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      incomingRequest.ID,
					Result: mcpToolCallResult{
						Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("error: %v", toolError)}},
						IsError: true,
					},
				}
			}
			return &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      incomingRequest.ID,
				Result: mcpToolCallResult{
					Content: []mcpContent{{Type: "text", Text: toolOutput}},
				},
			}
		}
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      incomingRequest.ID,
		Error: &jsonRPCError{
			Code:    -32602,
			Message: fmt.Sprintf("unknown tool: %s", callParams.Name),
		},
	}
}
