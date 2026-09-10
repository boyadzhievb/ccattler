package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// maxOutputBytes is the maximum number of bytes returned from any single tool call
// to prevent flooding the MCP client.
const maxOutputBytes = 100 * 1024

// allowedComponents defines the CCattler components that can be managed by the MCP
// server. Each component has a process search pattern and a start command. Only these
// predefined components can be stopped, started, or queried for logs.
var allowedComponents = map[string]componentDefinition{
	"etcd": {
		displayName:  "etcd",
		processMatch: "etcd --data-dir",
		logFile:      "/tmp/ccattler-etcd.log",
		startArgs:    []string{"etcd", "--data-dir", "/tmp/ccattler-etcd"},
	},
	"server": {
		displayName:  "cca server",
		processMatch: "cca server",
		logFile:      "/tmp/ccattler-server.log",
	},
	"agent-1": {
		displayName:  "cca agent (node-1)",
		processMatch: "cca agent.*node-1",
		logFile:      "/tmp/ccattler-agent-1.log",
	},
	"agent-2": {
		displayName:  "cca agent (node-2)",
		processMatch: "cca agent.*node-2",
		logFile:      "/tmp/ccattler-agent-2.log",
	},
}

// componentDefinition describes a managed CCattler component for process lifecycle operations.
type componentDefinition struct {
	// displayName is the human-readable name shown in output.
	displayName string
	// processMatch is the grep pattern used to find this component's process.
	processMatch string
	// logFile is where the component's stdout/stderr is redirected when started.
	logFile string
	// startArgs is the command and arguments to start this component. Nil means
	// the start command is built dynamically (e.g., server/agent use the cca binary).
	startArgs []string
}

// toolRegistryEntry describes a single MCP tool with its metadata and handler function.
type toolRegistryEntry struct {
	// name is the tool name exposed to the MCP client.
	name string
	// description explains what the tool does (shown to the AI model).
	description string
	// inputSchema is the JSON Schema for the tool's parameters.
	inputSchema map[string]any
	// handler executes the tool and returns its text output or an error.
	handler func(arguments map[string]any) (string, error)
}

// validPackagePattern matches Go package paths that are safe to pass to `go test`.
// Only allows alphanumeric, underscore, forward slash, and dots. Prevents injection.
var validPackagePattern = regexp.MustCompile(`^\./[a-zA-Z0-9_/\.]+$`)

// buildToolRegistry returns the complete list of tools exposed by this MCP server.
// Each tool has a fixed implementation — there is no arbitrary command execution.
func buildToolRegistry() []toolRegistryEntry {
	componentNames := make([]string, 0, len(allowedComponents))
	for componentName := range allowedComponents {
		componentNames = append(componentNames, componentName)
	}

	return []toolRegistryEntry{
		{
			name:        "git_status",
			description: "Show the git working tree status of the CCattler repository.",
			inputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			handler:     handleGitStatus,
		},
		{
			name:        "git_log",
			description: "Show recent git commits in the CCattler repository.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"count": map[string]any{
						"type":        "integer",
						"description": "Number of commits to show (1-100, default 20)",
						"minimum":     1,
						"maximum":     100,
					},
				},
			},
			handler: handleGitLog,
		},
		{
			name:        "git_pull",
			description: "Pull latest changes from the remote repository (fast-forward only, safe).",
			inputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			handler:     handleGitPull,
		},
		{
			name:        "build",
			description: "Compile the CCattler binaries (cca and ccattler-mcp). Returns build output.",
			inputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			handler:     handleBuild,
		},
		{
			name:        "test",
			description: "Run Go tests for CCattler. Specify a package path or use './...' for all.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"package": map[string]any{
						"type":        "string",
						"description": "Go package path (e.g. './controllers/', './agent/', './...'), default './...'",
					},
				},
			},
			handler: handleTest,
		},
		{
			name:        "cluster_status",
			description: "Run 'cca status' to show the current state of the CCattler cluster.",
			inputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			handler:     handleClusterStatus,
		},
		{
			name:        "process_list",
			description: "List running CCattler-related processes (cca, etcd).",
			inputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			handler:     handleProcessList,
		},
		{
			name:        "container_list",
			description: "List Docker containers on this host.",
			inputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			handler:     handleContainerList,
		},
		{
			name:        "component_logs",
			description: "View recent log output from a CCattler component.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"component": map[string]any{
						"type":        "string",
						"description": "Component name",
						"enum":        componentNames,
					},
					"lines": map[string]any{
						"type":        "integer",
						"description": "Number of log lines to show (1-500, default 50)",
						"minimum":     1,
						"maximum":     500,
					},
				},
				"required": []string{"component"},
			},
			handler: handleComponentLogs,
		},
		{
			name:        "stop_component",
			description: "Stop a running CCattler component by sending SIGTERM.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"component": map[string]any{
						"type":        "string",
						"description": "Component to stop, or 'all' to stop everything",
						"enum":        append(componentNames, "all"),
					},
				},
				"required": []string{"component"},
			},
			handler: handleStopComponent,
		},
		{
			name:        "start_component",
			description: "Start a CCattler component as a background process with output redirected to its log file.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"component": map[string]any{
						"type":        "string",
						"description": "Component to start, or 'all' to start everything (etcd → server → agents)",
						"enum":        append(componentNames, "all"),
					},
				},
				"required": []string{"component"},
			},
			handler: handleStartComponent,
		},
		{
			name:        "deploy",
			description: "Full deployment cycle: stop all → git pull → build → start all. Returns step-by-step output.",
			inputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			handler:     handleDeploy,
		},
		{
			name:        "disk_usage",
			description: "Show disk usage on this host.",
			inputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			handler:     handleDiskUsage,
		},
		{
			name:        "apply_config",
			description: "Apply a .ccl configuration file to the running CCattler cluster.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file": map[string]any{
						"type":        "string",
						"description": "Path to .ccl file relative to the repo root (e.g. 'examples/web.ccl')",
					},
				},
				"required": []string{"file"},
			},
			handler: handleApplyConfig,
		},
	}
}

// handleGitStatus runs git status in the CCattler repository.
func handleGitStatus(arguments map[string]any) (string, error) {
	return runCommandWithTimeout(30*time.Second, "git", "-C", serverConfig.repoPath, "status")
}

// handleGitLog shows recent commits in the CCattler repository.
func handleGitLog(arguments map[string]any) (string, error) {
	commitCount := 20
	if rawCount, hasCount := arguments["count"]; hasCount {
		if parsedCount, isFloat := rawCount.(float64); isFloat {
			commitCount = int(parsedCount)
		}
	}
	if commitCount < 1 {
		commitCount = 1
	}
	if commitCount > 100 {
		commitCount = 100
	}
	return runCommandWithTimeout(30*time.Second, "git", "-C", serverConfig.repoPath,
		"log", "--oneline", "--decorate", "-n", strconv.Itoa(commitCount))
}

// handleGitPull pulls the latest changes using fast-forward only (safe, no merge conflicts).
func handleGitPull(arguments map[string]any) (string, error) {
	return runCommandWithTimeout(60*time.Second, "git", "-C", serverConfig.repoPath, "pull", "--ff-only")
}

// handleBuild compiles both CCattler binaries.
func handleBuild(arguments map[string]any) (string, error) {
	var outputBuilder strings.Builder

	outputBuilder.WriteString("=== Building cca ===\n")
	ccaOutput, ccaBuildError := runCommandInDir(5*time.Minute, serverConfig.repoPath, "go", "build", "-o", "cca", "./cmd/cca/")
	outputBuilder.WriteString(ccaOutput)
	if ccaBuildError != nil {
		return outputBuilder.String(), fmt.Errorf("cca build failed: %w", ccaBuildError)
	}
	outputBuilder.WriteString("cca: OK\n\n")

	outputBuilder.WriteString("=== Building ccattler-mcp ===\n")
	mcpOutput, mcpBuildError := runCommandInDir(5*time.Minute, serverConfig.repoPath, "go", "build", "-o", "ccattler-mcp", "./cmd/mcp/")
	outputBuilder.WriteString(mcpOutput)
	if mcpBuildError != nil {
		return outputBuilder.String(), fmt.Errorf("mcp build failed: %w", mcpBuildError)
	}
	outputBuilder.WriteString("ccattler-mcp: OK\n")

	return outputBuilder.String(), nil
}

// handleTest runs Go tests for the specified package (or all packages).
func handleTest(arguments map[string]any) (string, error) {
	packagePath := "./..."
	if rawPackage, hasPackage := arguments["package"]; hasPackage {
		if parsedPackage, isString := rawPackage.(string); isString && parsedPackage != "" {
			packagePath = parsedPackage
		}
	}

	if packagePath != "./..." && !validPackagePattern.MatchString(packagePath) {
		return "", fmt.Errorf("invalid package path %q: must match pattern ./[alphanumeric/._]", packagePath)
	}

	return runCommandInDir(5*time.Minute, serverConfig.repoPath, "go", "test", "-v", "-count=1", packagePath)
}

// handleClusterStatus runs cca status against the running etcd-backed cluster.
func handleClusterStatus(arguments map[string]any) (string, error) {
	ccaBinaryPath := filepath.Join(serverConfig.repoPath, "cca")
	return runCommandWithTimeout(30*time.Second, ccaBinaryPath, "status", "--store", "etcd", "--endpoints", serverConfig.etcdEndpoints)
}

// handleProcessList shows running CCattler and etcd processes.
func handleProcessList(arguments map[string]any) (string, error) {
	psOutput, psError := runCommandWithTimeout(10*time.Second, "ps", "aux")
	if psError != nil {
		return "", psError
	}

	var filteredLines strings.Builder
	for _, processLine := range strings.Split(psOutput, "\n") {
		lowerLine := strings.ToLower(processLine)
		if strings.Contains(lowerLine, "cca") || strings.Contains(lowerLine, "etcd") || strings.Contains(processLine, "USER") {
			filteredLines.WriteString(processLine)
			filteredLines.WriteByte('\n')
		}
	}
	return filteredLines.String(), nil
}

// handleContainerList shows Docker containers on this host.
func handleContainerList(arguments map[string]any) (string, error) {
	return runCommandWithTimeout(15*time.Second, "docker", "ps", "-a",
		"--format", "table {{.ID}}\t{{.Image}}\t{{.Status}}\t{{.Names}}\t{{.Ports}}")
}

// handleComponentLogs reads the tail of a component's log file.
func handleComponentLogs(arguments map[string]any) (string, error) {
	componentName, validationError := extractComponentName(arguments)
	if validationError != nil {
		return "", validationError
	}

	componentDef, componentExists := allowedComponents[componentName]
	if !componentExists {
		return "", fmt.Errorf("unknown component: %s", componentName)
	}

	lineCount := 50
	if rawLines, hasLines := arguments["lines"]; hasLines {
		if parsedLines, isFloat := rawLines.(float64); isFloat {
			lineCount = int(parsedLines)
		}
	}
	if lineCount < 1 {
		lineCount = 1
	}
	if lineCount > 500 {
		lineCount = 500
	}

	return runCommandWithTimeout(10*time.Second, "tail", "-n", strconv.Itoa(lineCount), componentDef.logFile)
}

// handleStopComponent stops one or all CCattler components by sending SIGTERM.
func handleStopComponent(arguments map[string]any) (string, error) {
	componentName, validationError := extractComponentName(arguments)
	if validationError != nil {
		return "", validationError
	}

	if componentName == "all" {
		return stopAllComponents()
	}

	return stopSingleComponent(componentName)
}

// handleStartComponent starts one or all CCattler components as background processes.
func handleStartComponent(arguments map[string]any) (string, error) {
	componentName, validationError := extractComponentName(arguments)
	if validationError != nil {
		return "", validationError
	}

	if componentName == "all" {
		return startAllComponents()
	}

	return startSingleComponent(componentName)
}

// handleDeploy performs a full deployment cycle: stop → pull → build → start.
func handleDeploy(arguments map[string]any) (string, error) {
	var deployOutput strings.Builder

	deployOutput.WriteString("=== Step 1: Stopping all components ===\n")
	stopOutput, stopError := stopAllComponents()
	deployOutput.WriteString(stopOutput)
	if stopError != nil {
		deployOutput.WriteString(fmt.Sprintf("warning: stop had errors: %v\n", stopError))
	}
	deployOutput.WriteByte('\n')

	deployOutput.WriteString("=== Step 2: Pulling latest changes ===\n")
	pullOutput, pullError := handleGitPull(nil)
	deployOutput.WriteString(pullOutput)
	if pullError != nil {
		return deployOutput.String(), fmt.Errorf("git pull failed: %w", pullError)
	}
	deployOutput.WriteByte('\n')

	deployOutput.WriteString("=== Step 3: Building binaries ===\n")
	buildOutput, buildError := handleBuild(nil)
	deployOutput.WriteString(buildOutput)
	if buildError != nil {
		return deployOutput.String(), fmt.Errorf("build failed: %w", buildError)
	}
	deployOutput.WriteByte('\n')

	deployOutput.WriteString("=== Step 4: Starting all components ===\n")
	startOutput, startError := startAllComponents()
	deployOutput.WriteString(startOutput)
	if startError != nil {
		return deployOutput.String(), fmt.Errorf("start failed: %w", startError)
	}

	deployOutput.WriteString("\n=== Deploy complete ===\n")
	return deployOutput.String(), nil
}

// handleDiskUsage shows disk usage on the host.
func handleDiskUsage(arguments map[string]any) (string, error) {
	return runCommandWithTimeout(10*time.Second, "df", "-h")
}

// handleApplyConfig applies a .ccl file to the running cluster.
func handleApplyConfig(arguments map[string]any) (string, error) {
	rawFile, hasFile := arguments["file"]
	if !hasFile {
		return "", fmt.Errorf("missing required parameter: file")
	}
	filePath, isString := rawFile.(string)
	if !isString || filePath == "" {
		return "", fmt.Errorf("file must be a non-empty string")
	}

	if strings.Contains(filePath, "..") || strings.HasPrefix(filePath, "/") {
		return "", fmt.Errorf("file path must be relative to repo root and cannot contain '..'")
	}
	if !strings.HasSuffix(filePath, ".ccl") {
		return "", fmt.Errorf("file must have .ccl extension")
	}

	absoluteFilePath := filepath.Join(serverConfig.repoPath, filePath)
	if _, statError := os.Stat(absoluteFilePath); statError != nil {
		return "", fmt.Errorf("file not found: %s", filePath)
	}

	ccaBinaryPath := filepath.Join(serverConfig.repoPath, "cca")
	return runCommandWithTimeout(30*time.Second, ccaBinaryPath, "apply",
		"--store", "etcd", "--endpoints", serverConfig.etcdEndpoints, absoluteFilePath)
}

// stopAllComponents stops components in reverse dependency order: agents → server → etcd.
func stopAllComponents() (string, error) {
	var outputBuilder strings.Builder
	stopOrder := []string{"agent-2", "agent-1", "server", "etcd"}
	for _, componentName := range stopOrder {
		singleOutput, singleError := stopSingleComponent(componentName)
		outputBuilder.WriteString(singleOutput)
		if singleError != nil {
			outputBuilder.WriteString(fmt.Sprintf("  warning: %v\n", singleError))
		}
	}
	return outputBuilder.String(), nil
}

// startAllComponents starts components in dependency order: etcd → server → agents.
func startAllComponents() (string, error) {
	var outputBuilder strings.Builder
	startOrder := []string{"etcd", "server", "agent-1", "agent-2"}
	for _, componentName := range startOrder {
		singleOutput, singleError := startSingleComponent(componentName)
		outputBuilder.WriteString(singleOutput)
		if singleError != nil {
			return outputBuilder.String(), fmt.Errorf("failed to start %s: %w", componentName, singleError)
		}
		if componentName == "etcd" {
			time.Sleep(2 * time.Second)
		} else {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return outputBuilder.String(), nil
}

// stopSingleComponent sends SIGTERM to a single component's process.
func stopSingleComponent(componentName string) (string, error) {
	componentDef, componentExists := allowedComponents[componentName]
	if !componentExists {
		return "", fmt.Errorf("unknown component: %s", componentName)
	}

	pkillOutput, _ := runCommandWithTimeout(10*time.Second, "pkill", "-f", componentDef.processMatch)
	time.Sleep(500 * time.Millisecond)
	return fmt.Sprintf("stopped %s\n%s", componentDef.displayName, pkillOutput), nil
}

// startSingleComponent launches a component as a detached background process.
func startSingleComponent(componentName string) (string, error) {
	componentDef, componentExists := allowedComponents[componentName]
	if !componentExists {
		return "", fmt.Errorf("unknown component: %s", componentName)
	}

	commandArgs := buildStartCommand(componentName, componentDef)
	if len(commandArgs) == 0 {
		return "", fmt.Errorf("no start command defined for %s", componentName)
	}

	logFile, logOpenError := os.OpenFile(componentDef.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logOpenError != nil {
		return "", fmt.Errorf("cannot open log file %s: %w", componentDef.logFile, logOpenError)
	}

	backgroundCommand := exec.Command(commandArgs[0], commandArgs[1:]...)
	backgroundCommand.Dir = serverConfig.repoPath
	backgroundCommand.Stdout = logFile
	backgroundCommand.Stderr = logFile
	backgroundCommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if startError := backgroundCommand.Start(); startError != nil {
		logFile.Close()
		return "", fmt.Errorf("failed to start %s: %w", componentDef.displayName, startError)
	}

	logFile.Close()
	diagnosticLogger.Printf("started %s (pid %d)\n", componentDef.displayName, backgroundCommand.Process.Pid)
	return fmt.Sprintf("started %s (pid %d, log: %s)\n",
		componentDef.displayName, backgroundCommand.Process.Pid, componentDef.logFile), nil
}

// buildStartCommand returns the command-line arguments to start a component.
// Components with predefined startArgs use those directly; server and agent
// components use the built cca binary with appropriate flags.
func buildStartCommand(componentName string, componentDef componentDefinition) []string {
	if componentDef.startArgs != nil {
		return componentDef.startArgs
	}

	ccaBinaryPath := filepath.Join(serverConfig.repoPath, "cca")

	switch componentName {
	case "server":
		return []string{ccaBinaryPath, "server", "--store", "etcd", "--endpoints", serverConfig.etcdEndpoints}
	case "agent-1":
		return []string{ccaBinaryPath, "agent", "--store", "etcd", "--endpoints", serverConfig.etcdEndpoints, "--node-id", "node-1"}
	case "agent-2":
		return []string{ccaBinaryPath, "agent", "--store", "etcd", "--endpoints", serverConfig.etcdEndpoints, "--node-id", "node-2"}
	default:
		return nil
	}
}

// extractComponentName validates and extracts the "component" argument from a tool call.
func extractComponentName(arguments map[string]any) (string, error) {
	rawComponent, hasComponent := arguments["component"]
	if !hasComponent {
		return "", fmt.Errorf("missing required parameter: component")
	}
	componentName, isString := rawComponent.(string)
	if !isString || componentName == "" {
		return "", fmt.Errorf("component must be a non-empty string")
	}
	if componentName != "all" {
		if _, componentExists := allowedComponents[componentName]; !componentExists {
			return "", fmt.Errorf("unknown component %q: allowed values are etcd, server, agent-1, agent-2, all", componentName)
		}
	}
	return componentName, nil
}

// runCommandWithTimeout executes a command with a timeout and returns its combined output.
// The command is executed directly (no shell), preventing command injection.
func runCommandWithTimeout(timeout time.Duration, commandName string, commandArgs ...string) (string, error) {
	commandContext, cancelFunc := context.WithTimeout(context.Background(), timeout)
	defer cancelFunc()

	execCommand := exec.CommandContext(commandContext, commandName, commandArgs...)
	var combinedOutput bytes.Buffer
	execCommand.Stdout = &combinedOutput
	execCommand.Stderr = &combinedOutput

	runError := execCommand.Run()
	outputText := truncateOutput(combinedOutput.String())

	if runError != nil {
		return outputText, fmt.Errorf("command %s failed: %w", commandName, runError)
	}
	return outputText, nil
}

// runCommandInDir executes a command in a specific working directory with a timeout.
func runCommandInDir(timeout time.Duration, workingDirectory string, commandName string, commandArgs ...string) (string, error) {
	commandContext, cancelFunc := context.WithTimeout(context.Background(), timeout)
	defer cancelFunc()

	execCommand := exec.CommandContext(commandContext, commandName, commandArgs...)
	execCommand.Dir = workingDirectory
	var combinedOutput bytes.Buffer
	execCommand.Stdout = &combinedOutput
	execCommand.Stderr = &combinedOutput

	runError := execCommand.Run()
	outputText := truncateOutput(combinedOutput.String())

	if runError != nil {
		return outputText, fmt.Errorf("command %s failed: %w", commandName, runError)
	}
	return outputText, nil
}

// truncateOutput limits output to maxOutputBytes to prevent flooding the MCP client.
func truncateOutput(fullOutput string) string {
	if len(fullOutput) <= maxOutputBytes {
		return fullOutput
	}
	return fullOutput[:maxOutputBytes] + "\n... [output truncated at 100KB]"
}
