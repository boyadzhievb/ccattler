// Package main_test contains CLI acceptance tests that exercise the cca binary
// as a subprocess. Each test builds the binary once via TestMain, then invokes
// it with arguments and asserts on stdout, stderr, and exit code. These tests
// validate the full CLI contract from the user's perspective without reaching
// into internal packages.
package main_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// cliTestDefaultTimeout is the maximum duration for any single cca invocation
	// before the test kills the process and fails.
	cliTestDefaultTimeout = 30 * time.Second

	// serverStartupTimeout is the maximum time to wait for the cca demo server
	// to start listening on its HTTP port.
	serverStartupTimeout = 10 * time.Second

	// serverStartupPollInterval is how often to check whether the server port
	// is reachable during startup.
	serverStartupPollInterval = 100 * time.Millisecond

	// serverListenAddress is the default HTTP API address that cca demo binds to.
	serverListenAddress = "127.0.0.1:9770"
)

// ccaBinaryPath holds the path to the compiled cca binary, built once in TestMain.
var ccaBinaryPath string

// TestMain builds the cca binary to a temporary directory before running tests,
// and removes the temp directory after all tests complete.
func TestMain(testRunner *testing.M) {
	temporaryDirectory, tempDirError := os.MkdirTemp("", "cca-cli-test-*")
	if tempDirError != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", tempDirError)
		os.Exit(1)
	}

	ccaBinaryPath = filepath.Join(temporaryDirectory, "cca")
	buildCommand := exec.Command("go", "build", "-o", ccaBinaryPath, ".") //nolint:gosec // test helper builds the binary under test
	buildCommand.Dir = findCmdCcaDirectory()
	buildOutput, buildError := buildCommand.CombinedOutput()
	if buildError != nil {
		fmt.Fprintf(os.Stderr, "failed to build cca binary: %v\n%s\n", buildError, string(buildOutput))
		os.RemoveAll(temporaryDirectory)
		os.Exit(1)
	}

	exitCode := testRunner.Run()
	os.RemoveAll(temporaryDirectory)
	os.Exit(exitCode)
}

// findCmdCcaDirectory returns the absolute path to the cmd/cca directory where
// the main package lives. It walks up from the current working directory looking
// for the cmd/cca/ path.
func findCmdCcaDirectory() string {
	currentWorkDir, workDirError := os.Getwd()
	if workDirError != nil {
		return "."
	}
	// When go test runs from cmd/cca, we are already there.
	if filepath.Base(currentWorkDir) == "cca" && filepath.Base(filepath.Dir(currentWorkDir)) == "cmd" {
		return currentWorkDir
	}
	// Fall back to looking for the cmd/cca subdirectory.
	candidatePath := filepath.Join(currentWorkDir, "cmd", "cca")
	if fileInfo, statErr := os.Stat(candidatePath); statErr == nil && fileInfo.IsDir() {
		return candidatePath
	}
	return "."
}

// runCCA executes the compiled cca binary with the given arguments, captures
// stdout and stderr separately, and returns them along with the process exit
// code. The process is killed if it exceeds cliTestDefaultTimeout.
func runCCA(testContext *testing.T, commandArgs ...string) (stdoutContent string, stderrContent string, processExitCode int) {
	return runCCAWithEnv(testContext, nil, commandArgs...)
}

// runCCAWithEnv executes the compiled cca binary with the given arguments and
// additional environment variables, captures stdout and stderr separately, and
// returns them along with the process exit code.
func runCCAWithEnv(testContext *testing.T, extraEnvironment []string, commandArgs ...string) (stdoutContent string, stderrContent string, processExitCode int) {
	testContext.Helper()

	timeoutContext, cancelTimeout := context.WithTimeout(context.Background(), cliTestDefaultTimeout)
	defer cancelTimeout()

	ccaCommand := exec.CommandContext(timeoutContext, ccaBinaryPath, commandArgs...) //nolint:gosec // test helper runs the binary under test
	if len(extraEnvironment) > 0 {
		ccaCommand.Env = append(os.Environ(), extraEnvironment...)
	}

	var stdoutBuffer strings.Builder
	var stderrBuffer strings.Builder
	ccaCommand.Stdout = &stdoutBuffer
	ccaCommand.Stderr = &stderrBuffer

	runError := ccaCommand.Run()

	stdoutContent = stdoutBuffer.String()
	stderrContent = stderrBuffer.String()
	processExitCode = 0

	if runError != nil {
		if exitError, isExitError := runError.(*exec.ExitError); isExitError {
			processExitCode = exitError.ExitCode()
		} else {
			testContext.Fatalf("failed to execute cca binary: %v", runError)
		}
	}

	return stdoutContent, stderrContent, processExitCode
}

// writeTempFile creates a file in the given directory with the specified name
// and content, and returns the absolute path to the created file. The file is
// automatically cleaned up when the test finishes.
func writeTempFile(testContext *testing.T, directory string, fileName string, fileContent string) string {
	testContext.Helper()
	filePath := filepath.Join(directory, fileName)
	writeError := os.WriteFile(filePath, []byte(fileContent), 0600)
	if writeError != nil {
		testContext.Fatalf("failed to write temp file %s: %v", filePath, writeError)
	}
	testContext.Cleanup(func() { os.Remove(filePath) })
	return filePath
}

// requireExitCode asserts that the actual exit code matches the expected value.
// On mismatch it logs the stderr content for debugging.
func requireExitCode(testContext *testing.T, expectedExitCode int, actualExitCode int, stderrContent string) {
	testContext.Helper()
	if actualExitCode != expectedExitCode {
		testContext.Fatalf("expected exit code %d, got %d\nstderr: %s", expectedExitCode, actualExitCode, stderrContent)
	}
}

// requireStdoutContains asserts that stdout contains the expected substring.
func requireStdoutContains(testContext *testing.T, stdoutContent string, expectedSubstring string) {
	testContext.Helper()
	if !strings.Contains(stdoutContent, expectedSubstring) {
		testContext.Errorf("expected stdout to contain %q, got:\n%s", expectedSubstring, stdoutContent)
	}
}

// requireStderrContains asserts that stderr contains the expected substring (case-insensitive).
func requireStderrContains(testContext *testing.T, stderrContent string, expectedSubstring string) {
	testContext.Helper()
	if !strings.Contains(strings.ToLower(stderrContent), strings.ToLower(expectedSubstring)) {
		testContext.Errorf("expected stderr to contain %q (case-insensitive), got:\n%s", expectedSubstring, stderrContent)
	}
}

// --- 57a harness tests above; 57c informational commands below ---

// TestCLIVersion verifies that "cca version" prints a version string to stdout
// and exits with code 0.
func TestCLIVersion(testContext *testing.T) {
	stdoutContent, stderrContent, exitCode := runCCA(testContext, "version")
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "cca")
}

// TestCLICompletionBash verifies that "cca completion bash" outputs a bash
// completion script and exits with code 0.
func TestCLICompletionBash(testContext *testing.T) {
	stdoutContent, stderrContent, exitCode := runCCA(testContext, "completion", "bash")
	requireExitCode(testContext, 0, exitCode, stderrContent)
	// The bash completion script should contain the complete command or function name.
	if !strings.Contains(stdoutContent, "complete") && !strings.Contains(stdoutContent, "_cca") {
		testContext.Errorf("expected bash completion output to contain 'complete' or '_cca', got:\n%s", stdoutContent)
	}
}

// --- 57b core command tests ---

// TestCLIApplySimple verifies that "cca apply" with a minimal service definition
// runs the local simulation, prints status output containing the service name
// and desired instance count, and exits with code 0.
func TestCLIApplySimple(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	dslFilePath := writeTempFile(testContext, temporaryDirectory, "web.cca", `service web {
    image nginx:1.27
    instances 3
    expose 8080
}
`)

	stdoutContent, stderrContent, exitCode := runCCA(testContext, "apply", dslFilePath)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "web")
	requireStdoutContains(testContext, stdoutContent, "Registered 3 simulated nodes")
}

// TestCLIApplyDryRun verifies that "cca apply --dry-run --values" renders the
// template and prints the rendered DSL to stdout without running the simulation.
func TestCLIApplyDryRun(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	templateFilePath := writeTempFile(testContext, temporaryDirectory, "template.cca", `service {{ .name }} {
    image {{ .image }}
    instances {{ .instances }}
    expose {{ .port }}
}
`)
	valuesFilePath := writeTempFile(testContext, temporaryDirectory, "values.txt", `name: web
image: nginx:1.27
instances: 3
port: 8080
`)

	stdoutContent, stderrContent, exitCode := runCCA(testContext,
		"apply", "--dry-run", "--values", valuesFilePath, templateFilePath)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "image nginx:1.27")
	requireStdoutContains(testContext, stdoutContent, "instances 3")
	// Dry-run should NOT contain "Registered" since no simulation runs.
	if strings.Contains(stdoutContent, "Registered") {
		testContext.Errorf("dry-run should not run simulation, but stdout contains 'Registered':\n%s", stdoutContent)
	}
}

// TestCLIApplyWithValues verifies that "cca apply --values" renders a template
// with values and applies it in simulation mode.
func TestCLIApplyWithValues(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	templateFilePath := writeTempFile(testContext, temporaryDirectory, "template.cca", `service {{ .name }} {
    image {{ .image }}
    instances {{ .instances }}
    expose {{ .port }}
}
`)
	valuesFilePath := writeTempFile(testContext, temporaryDirectory, "values.txt", `name: api
image: myapp:latest
instances: 2
port: 3000
`)

	stdoutContent, stderrContent, exitCode := runCCA(testContext,
		"apply", "--values", valuesFilePath, templateFilePath)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "Applying")
	requireStdoutContains(testContext, stdoutContent, "api")
}

// TestCLIApplyDirectory verifies that "cca apply dir/" processes all .cca files
// in the directory and applies them in simulation mode.
func TestCLIApplyDirectory(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	dslSubDirectory := filepath.Join(temporaryDirectory, "configs")
	if mkdirError := os.Mkdir(dslSubDirectory, 0750); mkdirError != nil {
		testContext.Fatalf("failed to create subdirectory: %v", mkdirError)
	}

	writeTempFile(testContext, dslSubDirectory, "service-a.cca", `service alpha {
    image alpha:1.0
    instances 2
    expose 8080
}
`)
	writeTempFile(testContext, dslSubDirectory, "service-b.cca", `service beta {
    image beta:2.0
    instances 1
    expose 9090
}
`)

	stdoutContent, stderrContent, exitCode := runCCA(testContext, "apply", dslSubDirectory)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "alpha")
	requireStdoutContains(testContext, stdoutContent, "beta")
}

// TestCLIRender verifies that "cca render --values" renders a template and
// prints the rendered DSL to stdout without applying.
func TestCLIRender(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	templateFilePath := writeTempFile(testContext, temporaryDirectory, "template.cca", `service {{ .name }} {
    image {{ .image }}
    instances {{ .instances }}
    expose {{ .port }}
}
`)
	valuesFilePath := writeTempFile(testContext, temporaryDirectory, "values.txt", `name: frontend
image: react:18
instances: 5
port: 3000
`)

	stdoutContent, stderrContent, exitCode := runCCA(testContext,
		"render", "--values", valuesFilePath, templateFilePath)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "service frontend")
	requireStdoutContains(testContext, stdoutContent, "image react:18")
	requireStdoutContains(testContext, stdoutContent, "instances 5")
	requireStdoutContains(testContext, stdoutContent, "expose 3000")
}

// TestCLIDiff verifies that "cca diff" with a .cca file prints fact additions
// (prefixed with +) and a summary line, and exits with code 0.
func TestCLIDiff(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	dslFilePath := writeTempFile(testContext, temporaryDirectory, "web.cca", `service web {
    image nginx:1.27
    instances 3
    expose 8080
}
`)

	stdoutContent, stderrContent, exitCode := runCCA(testContext, "diff", dslFilePath)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "+ desired/service/")
	requireStdoutContains(testContext, stdoutContent, "added")
}

// TestCLIDiffWithValues verifies that "cca diff --values" renders a template
// then diffs the resulting facts against an empty store.
func TestCLIDiffWithValues(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	templateFilePath := writeTempFile(testContext, temporaryDirectory, "template.cca", `service {{ .name }} {
    image {{ .image }}
    instances {{ .instances }}
    expose {{ .port }}
}
`)
	valuesFilePath := writeTempFile(testContext, temporaryDirectory, "values.txt", `name: checkout
image: checkout:v2
instances: 4
port: 8443
`)

	stdoutContent, stderrContent, exitCode := runCCA(testContext,
		"diff", "--values", valuesFilePath, templateFilePath)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "+ desired/service/")
	requireStdoutContains(testContext, stdoutContent, "added")
}

// --- 57d error paths & edge cases ---

// TestCLIApplyInvalidFile verifies that "cca apply nonexistent.cca" prints an
// error to stderr and exits with code 1.
func TestCLIApplyInvalidFile(testContext *testing.T) {
	_, stderrContent, exitCode := runCCA(testContext, "apply", "nonexistent-file-that-does-not-exist.cca")
	requireExitCode(testContext, 1, exitCode, stderrContent)
	requireStderrContains(testContext, stderrContent, "error")
}

// TestCLIApplyInvalidDSL verifies that "cca apply" with a file containing
// invalid DSL syntax prints a parse error to stderr and exits with code 1.
func TestCLIApplyInvalidDSL(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	badFilePath := writeTempFile(testContext, temporaryDirectory, "bad.cca", `this is not valid DSL {{{{{`)

	_, stderrContent, exitCode := runCCA(testContext, "apply", badFilePath)
	requireExitCode(testContext, 1, exitCode, stderrContent)
	requireStderrContains(testContext, stderrContent, "error")
}

// TestCLIApplyEmptyDirectory verifies that "cca apply" on an empty directory
// (with no .cca or .ccattler files) prints an error and exits with code 1.
func TestCLIApplyEmptyDirectory(testContext *testing.T) {
	emptyDirectory := testContext.TempDir()

	_, stderrContent, exitCode := runCCA(testContext, "apply", emptyDirectory)
	requireExitCode(testContext, 1, exitCode, stderrContent)
	requireStderrContains(testContext, stderrContent, "error")
}

// TestCLIUnknownCommand verifies that an unrecognized command prints usage
// information to stderr and exits with a non-zero code.
func TestCLIUnknownCommand(testContext *testing.T) {
	_, stderrContent, exitCode := runCCA(testContext, "foobar")
	if exitCode == 0 {
		testContext.Fatalf("expected non-zero exit code for unknown command, got 0")
	}
	requireStderrContains(testContext, stderrContent, "usage")
}

// TestCLINoArgs verifies that running cca with no arguments prints usage
// information to stderr and exits with a non-zero code.
func TestCLINoArgs(testContext *testing.T) {
	_, stderrContent, exitCode := runCCA(testContext)
	if exitCode == 0 {
		testContext.Fatalf("expected non-zero exit code for no args, got 0")
	}
	requireStderrContains(testContext, stderrContent, "usage")
}

// TestCLISetOverride verifies that "--set" overrides a value from the values file.
func TestCLISetOverride(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	templateFilePath := writeTempFile(testContext, temporaryDirectory, "template.cca", `service {{ .name }} {
    image {{ .image }}
    instances {{ .instances }}
    expose {{ .port }}
}
`)
	valuesFilePath := writeTempFile(testContext, temporaryDirectory, "values.txt", `name: web
image: nginx:1.27
instances: 3
port: 8080
`)

	stdoutContent, stderrContent, exitCode := runCCA(testContext,
		"apply", "--dry-run", "--set", "instances=20", "--values", valuesFilePath, templateFilePath)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "instances 20")
	// The default value of 3 should NOT appear.
	if strings.Contains(stdoutContent, "instances 3") {
		testContext.Errorf("--set should override instances to 20, but found 'instances 3' in stdout:\n%s", stdoutContent)
	}
}

// TestCLISetFromEnv verifies that "--set-from-env" reads a value from an
// environment variable and uses it in template rendering.
func TestCLISetFromEnv(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	templateFilePath := writeTempFile(testContext, temporaryDirectory, "template.cca", `service {{ .name }} {
    image {{ .image }}:{{ .tag }}
    instances {{ .instances }}
    expose {{ .port }}
}
`)
	valuesFilePath := writeTempFile(testContext, temporaryDirectory, "values.txt", `name: web
image: nginx
tag: latest
instances: 3
port: 8080
`)

	testContext.Setenv("CCA_TEST_TAG", "v1.2.3")

	stdoutContent, stderrContent, exitCode := runCCAWithEnv(testContext,
		[]string{"CCA_TEST_TAG=v1.2.3"},
		"apply", "--dry-run", "--set-from-env", "tag=CCA_TEST_TAG", "--values", valuesFilePath, templateFilePath)
	requireExitCode(testContext, 0, exitCode, stderrContent)
	requireStdoutContains(testContext, stdoutContent, "v1.2.3")
}

// TestCLIApplyTemplateError verifies that a template with a required() call for
// a missing value prints a template error to stderr and exits with code 1.
func TestCLIApplyTemplateError(testContext *testing.T) {
	temporaryDirectory := testContext.TempDir()
	templateFilePath := writeTempFile(testContext, temporaryDirectory, "template.cca", `service {{ required "image is needed" .image }} {
    instances 1
    expose 8080
}
`)
	emptyValuesPath := writeTempFile(testContext, temporaryDirectory, "empty.txt", `# no values defined
`)

	_, stderrContent, exitCode := runCCA(testContext,
		"apply", "--values", emptyValuesPath, templateFilePath)
	requireExitCode(testContext, 1, exitCode, stderrContent)
	requireStderrContains(testContext, stderrContent, "template error")
	requireStderrContains(testContext, stderrContent, "image is needed")
}

// --- 57b/57c server-dependent tests ---

// startCCARunServer starts a "cca run --watch" process in the background with
// a simple DSL file and waits for the HTTP API server to become reachable.
// Returns a cleanup function that kills the process. The cleanup is also
// registered via t.Cleanup to ensure the process is killed on test failure.
// Uses "sleep 300" as the image, which runs real sleep processes (safe on any OS).
func startCCARunServer(testContext *testing.T) func() {
	testContext.Helper()

	temporaryDirectory := testContext.TempDir()
	dslFilePath := writeTempFile(testContext, temporaryDirectory, "server-test.cca", `service web {
    image "sleep 300"
    instances 2
    expose 8080
}
`)

	runCommand := exec.Command(ccaBinaryPath, "run", "--watch", dslFilePath) //nolint:gosec // test runs the binary under test
	// Use /dev/null for stdout to prevent pipe buffer issues on process kill.
	devNull, devNullErr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if devNullErr != nil {
		testContext.Fatalf("failed to open /dev/null: %v", devNullErr)
	}
	runCommand.Stdout = devNull
	runCommand.Stderr = devNull

	startError := runCommand.Start()
	if startError != nil {
		devNull.Close()
		testContext.Fatalf("failed to start cca run --watch: %v", startError)
	}

	cleanupFunction := func() {
		if runCommand.Process != nil {
			_ = runCommand.Process.Kill()
			_ = runCommand.Wait()
		}
		devNull.Close()
	}
	testContext.Cleanup(cleanupFunction)

	// Poll until the server port is reachable or timeout.
	serverReady := waitForServerReady(serverListenAddress, serverStartupTimeout)
	if !serverReady {
		cleanupFunction()
		testContext.Fatalf("cca run server did not become ready within %s", serverStartupTimeout)
	}

	return cleanupFunction
}

// waitForServerReady polls the given address with TCP dials until it accepts a
// connection or the timeout expires. Returns true if the server became reachable.
func waitForServerReady(listenAddress string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tcpConnection, dialError := net.DialTimeout("tcp", listenAddress, serverStartupPollInterval)
		if dialError == nil {
			_ = tcpConnection.Close()
			return true
		}
		time.Sleep(serverStartupPollInterval)
	}
	return false
}

// TestCLIServerDependentCommands groups all tests that require a running cca
// demo server. Starting the server once and running subtests avoids the
// overhead of multiple server startups. Skipped in short mode since the demo
// server takes a few seconds to start.
func TestCLIServerDependentCommands(testContext *testing.T) {
	if testing.Short() {
		testContext.Skip("skipping server-dependent tests in -short mode")
	}

	cleanupFunction := startCCARunServer(testContext)
	defer cleanupFunction()

	// Give the demo a moment to finish initial reconciliation after we connected.
	time.Sleep(1 * time.Second)

	testContext.Run("status", func(subtestContext *testing.T) {
		stdoutContent, stderrContent, exitCode := runCCA(subtestContext, "status")
		requireExitCode(subtestContext, 0, exitCode, stderrContent)
		// The status output contains the cluster status header or service info.
		if !strings.Contains(stdoutContent, "SERVICE") && !strings.Contains(stdoutContent, "CLUSTER") && !strings.Contains(stdoutContent, "web") {
			subtestContext.Errorf("expected status output to contain 'SERVICE', 'CLUSTER', or 'web', got:\n%s", stdoutContent)
		}
	})

	testContext.Run("get_services", func(subtestContext *testing.T) {
		stdoutContent, stderrContent, exitCode := runCCA(subtestContext, "get", "services")
		requireExitCode(subtestContext, 0, exitCode, stderrContent)
		// The demo deploys a "web" service, and the get services table has headers.
		requireStdoutContains(subtestContext, stdoutContent, "NAME")
		requireStdoutContains(subtestContext, stdoutContent, "web")
	})

	testContext.Run("get_nodes", func(subtestContext *testing.T) {
		stdoutContent, stderrContent, exitCode := runCCA(subtestContext, "get", "nodes")
		requireExitCode(subtestContext, 0, exitCode, stderrContent)
		// The demo registers a "local" node.
		requireStdoutContains(subtestContext, stdoutContent, "ID")
		requireStdoutContains(subtestContext, stdoutContent, "local")
	})

	testContext.Run("get_instances", func(subtestContext *testing.T) {
		stdoutContent, stderrContent, exitCode := runCCA(subtestContext, "get", "instances")
		requireExitCode(subtestContext, 0, exitCode, stderrContent)
		// The demo creates instances for the "web" service.
		requireStdoutContains(subtestContext, stdoutContent, "SERVICE")
		requireStdoutContains(subtestContext, stdoutContent, "web")
	})

	testContext.Run("scale", func(subtestContext *testing.T) {
		stdoutContent, stderrContent, exitCode := runCCA(subtestContext, "scale", "web", "10")
		requireExitCode(subtestContext, 0, exitCode, stderrContent)
		requireStdoutContains(subtestContext, stdoutContent, "scaled")
		requireStdoutContains(subtestContext, stdoutContent, "web")
		requireStdoutContains(subtestContext, stdoutContent, "10")
	})
}
