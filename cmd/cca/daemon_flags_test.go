// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// mustParseServerFlags parses `cca server` arguments and fails the test on an error.
func mustParseServerFlags(testContext *testing.T, args ...string) serverCommandConfig {
	testContext.Helper()
	parsedConfig, parseError := parseServerCommandArgs(args)
	if parseError != nil {
		testContext.Fatalf("cca server %v: %v", args, parseError)
	}
	return parsedConfig
}

// mustParseAgentFlags parses `cca agent` arguments and fails the test on an error.
func mustParseAgentFlags(testContext *testing.T, args ...string) agentCommandConfig {
	testContext.Helper()
	parsedConfig, parseError := parseAgentCommandArgs(args)
	if parseError != nil {
		testContext.Fatalf("cca agent %v: %v", args, parseError)
	}
	return parsedConfig
}

// mustParseInitFlags parses `cca init` arguments and fails the test on an error.
func mustParseInitFlags(testContext *testing.T, args ...string) initCommandConfig {
	testContext.Helper()
	parsedConfig, parseError := parseInitCommandArgs(args)
	if parseError != nil {
		testContext.Fatalf("cca init %v: %v", args, parseError)
	}
	return parsedConfig
}

// TestDaemonFlagsRefuseWhatTheyDoNotUnderstand is the M227 bug: an unknown
// flag, a flag without its value and a stray argument must be errors that name
// the offending text, for server, agent and init.
func TestDaemonFlagsRefuseWhatTheyDoNotUnderstand(testContext *testing.T) {
	parsersByCommand := map[string]func(args []string) error{
		"server": func(args []string) error { _, parseError := parseServerCommandArgs(args); return parseError },
		"agent":  func(args []string) error { _, parseError := parseAgentCommandArgs(args); return parseError },
		"init":   func(args []string) error { _, parseError := parseInitCommandArgs(args); return parseError },
	}
	refusedArguments := []struct {
		args          []string // args is the command line after the command name.
		expectedInMsg string   // expectedInMsg must appear in the error.
	}{
		{[]string{"--store", "memory", "--no-such-flag"}, "no-such-flag"},
		{[]string{"--store", "memory", "--kms-provider", "aws-kms"}, "kms-provider"},
		{[]string{"--store", "memory", "--node-id"}, "node-id"},
		{[]string{"--store", "memory", "stray-argument"}, "stray-argument"},
	}
	for commandName, parseCommandFlags := range parsersByCommand {
		for _, refusedCase := range refusedArguments {
			caseArgs := refusedCase.args
			if commandName == "init" {
				caseArgs = caseArgs[2:] // init has no --store flag
			}
			parseError := parseCommandFlags(caseArgs)
			if parseError == nil {
				testContext.Errorf("cca %s %v: accepted, want an error", commandName, caseArgs)
				continue
			}
			if !strings.Contains(parseError.Error(), refusedCase.expectedInMsg) {
				testContext.Errorf("cca %s %v: error %q does not name %q", commandName, caseArgs, parseError, refusedCase.expectedInMsg)
			}
		}
	}
}

// jinjaExpressionPattern matches an Ansible `{{ expression }}` in a unit template.
var jinjaExpressionPattern = regexp.MustCompile(`\{\{[^}]*\}\}`)

// jinjaStatementPattern matches an Ansible `{% statement %}`; removing them
// keeps the flags of every conditional branch.
var jinjaStatementPattern = regexp.MustCompile(`\{%[^%]*%\}`)

// jinjaDefaultPattern captures the value of `default('x')` or `default(8080)`.
var jinjaDefaultPattern = regexp.MustCompile(`default\('?([^')]*)'?\)`)

// renderJinjaExpressionForFlagTest replaces `{{ expression }}` with its
// default(), a valid storage backend, or a placeholder value.
func renderJinjaExpressionForFlagTest(jinjaExpression string) string {
	if defaultMatch := jinjaDefaultPattern.FindStringSubmatch(jinjaExpression); defaultMatch != nil {
		return defaultMatch[1]
	}
	if strings.Contains(jinjaExpression, "storage_backend") {
		return "local"
	}
	return "placeholder"
}

// extractDaemonInvocations returns the arguments of every `cca server` or
// `cca agent` ExecStart in unitText, keyed "server"/"agent", with shell line
// continuations joined and Ansible template syntax rendered.
func extractDaemonInvocations(unitText string) map[string][][]string {
	renderedText := jinjaStatementPattern.ReplaceAllString(unitText, "")
	renderedText = jinjaExpressionPattern.ReplaceAllStringFunc(renderedText, renderJinjaExpressionForFlagTest)
	invocationsByCommand := map[string][][]string{}
	unitLines := strings.Split(renderedText, "\n")
	for lineIndex := 0; lineIndex < len(unitLines); lineIndex++ {
		_, commandLine, isExecStart := strings.Cut(unitLines[lineIndex], "ExecStart=")
		if !isExecStart {
			continue
		}
		for strings.HasSuffix(strings.TrimSpace(commandLine), `\`) && lineIndex+1 < len(unitLines) {
			lineIndex++
			commandLine = strings.TrimSuffix(strings.TrimSpace(commandLine), `\`) + " " + unitLines[lineIndex]
		}
		commandFields := strings.Fields(commandLine)
		if len(commandFields) < 2 || !strings.HasSuffix(commandFields[0], "/cca") {
			continue
		}
		invocationsByCommand[commandFields[1]] = append(invocationsByCommand[commandFields[1]], commandFields[2:])
	}
	return invocationsByCommand
}

// TestShippedUnitFilesPassOnlyKnownFlags feeds every `cca server` and `cca
// agent` command line we ship (the `cca init` setup script, the worker unit in
// the install docs, the Ansible and Lima unit templates) through the real parsers.
func TestShippedUnitFilesPassOnlyKnownFlags(testContext *testing.T) {
	unitTextBySource := map[string]string{
		"cca init setup script": buildSetupScript("192.0.2.10", "node-1", "/tmp/cca-init", "10.100.0.0/16", "10.200.0.0/24"),
	}
	expectedInvocationCount := 2 // setup script: one server unit, one agent unit
	for _, repositoryPath := range []string{
		"website/installation.md", "deploy/lima/lima-deploy.yml",
		"deploy/ansible/roles/ha-controlplane/templates/cca-server.service.j2",
		"deploy/ansible/roles/worker/templates/cca-agent.service.j2",
	} {
		fileContent, readError := os.ReadFile(filepath.Join("..", "..", repositoryPath)) //nolint:gosec // G304: fixed repository paths
		if readError != nil {
			testContext.Fatalf("read %s: %v", repositoryPath, readError)
		}
		unitTextBySource[repositoryPath] = string(fileContent)
	}
	expectedInvocationCount += 1 + 2 + 1 + 1 // install docs: worker agent; Lima: server and agent; one per template

	checkedInvocationCount := 0
	for sourceName, unitText := range unitTextBySource {
		invocationsByCommand := extractDaemonInvocations(unitText)
		for _, serverArgs := range invocationsByCommand["server"] {
			checkedInvocationCount++
			if _, parseError := parseServerCommandArgs(serverArgs); parseError != nil {
				testContext.Errorf("%s: cca server %v: %v", sourceName, serverArgs, parseError)
			}
		}
		for _, agentArgs := range invocationsByCommand["agent"] {
			checkedInvocationCount++
			if _, parseError := parseAgentCommandArgs(agentArgs); parseError != nil {
				testContext.Errorf("%s: cca agent %v: %v", sourceName, agentArgs, parseError)
			}
		}
	}
	if checkedInvocationCount != expectedInvocationCount {
		testContext.Errorf("checked %d shipped command lines, want %d: a unit file moved or the extraction broke",
			checkedInvocationCount, expectedInvocationCount)
	}
}

// TestSetupScriptAgentUsesLocalAPIServer: the agent unit that `cca init`
// writes into setup.sh names the API server installed by the same script, on a
// host the server listens on and the node certificate covers, so an init host
// (which never ran `cca join`) has secret delivery (M248).
func TestSetupScriptAgentUsesLocalAPIServer(testContext *testing.T) {
	testCases := []struct {
		advertiseAddress         string // advertiseAddress is the address given to `cca init`
		expectedAPIServerAddress string // expectedAPIServerAddress is the agent's --api-server value
	}{
		{advertiseAddress: "192.0.2.10", expectedAPIServerAddress: "192.0.2.10:" + defaultAPIListenPort},
		{advertiseAddress: "fd00::1", expectedAPIServerAddress: "[fd00::1]:" + defaultAPIListenPort},
	}
	for _, testCase := range testCases {
		testContext.Run(testCase.advertiseAddress, func(subtestContext *testing.T) {
			setupScript := buildSetupScript(testCase.advertiseAddress, "node-1", "/tmp/cca-init", "10.100.0.0/16", "10.200.0.0/24")
			invocationsByCommand := extractDaemonInvocations(setupScript)
			if len(invocationsByCommand["agent"]) != 1 || len(invocationsByCommand["server"]) != 1 {
				subtestContext.Fatalf("setup.sh has %d agent and %d server units, want one of each",
					len(invocationsByCommand["agent"]), len(invocationsByCommand["server"]))
			}

			agentConfig, agentParseError := parseAgentCommandArgs(invocationsByCommand["agent"][0])
			if agentParseError != nil {
				subtestContext.Fatalf("parse setup.sh agent unit: %v", agentParseError)
			}
			if agentConfig.apiServerAddress != testCase.expectedAPIServerAddress {
				subtestContext.Errorf("setup.sh agent --api-server = %q, want %q",
					agentConfig.apiServerAddress, testCase.expectedAPIServerAddress)
			}

			serverConfig, serverParseError := parseServerCommandArgs(invocationsByCommand["server"][0])
			if serverParseError != nil {
				subtestContext.Fatalf("parse setup.sh server unit: %v", serverParseError)
			}
			listenHost, listenPort, splitError := net.SplitHostPort(serverConfig.listenAddress)
			if splitError != nil || listenPort != defaultAPIListenPort ||
				(listenHost != "0.0.0.0" && listenHost != testCase.advertiseAddress) {
				subtestContext.Errorf("setup.sh server --listen = %q, want 0.0.0.0 or %s on port %s (the agent's --api-server)",
					serverConfig.listenAddress, testCase.advertiseAddress, defaultAPIListenPort)
			}

			certificateIPAddresses, _ := buildCertificateSANs(initCommandConfig{advertiseAddress: testCase.advertiseAddress, nodeID: "node-1"})
			if !slices.ContainsFunc(certificateIPAddresses, net.ParseIP(testCase.advertiseAddress).Equal) {
				subtestContext.Errorf("node certificate IP SANs %v lack %s, so the agent's TLS check would fail",
					certificateIPAddresses, testCase.advertiseAddress)
			}
		})
	}
}

// TestJoinFlagsAnywhereAndRefusals pins `cca join`: flags before, between or
// after the two arguments; a mistyped flag (which dropped the CA file), a flag
// without its value and a third argument are errors that never echo the token.
func TestJoinFlagsAnywhereAndRefusals(testContext *testing.T) {
	const joinTokenForTest = "0123456789abcdef.fedcba9876543210"
	acceptedCommandLines := [][]string{
		{"https://ctrl:9770", joinTokenForTest, "--node-id", "worker-1", "--ca-cert", "/pki/ca.pem", "--data-dir", "/pki"},
		{"--node-id", "worker-1", "https://ctrl:9770", "--ca-cert", "/pki/ca.pem", joinTokenForTest, "--data-dir", "/pki"},
		{"--data-dir=/pki", "--ca-cert", "/pki/ca.pem", "--node-id", "worker-1", "https://ctrl:9770", joinTokenForTest},
	}
	expectedConfig := joinCommandConfig{
		serverAddress: "https://ctrl:9770", joinToken: joinTokenForTest, nodeID: "worker-1",
		caCertPath: "/pki/ca.pem", dataDirectory: "/pki",
	}
	for _, acceptedArgs := range acceptedCommandLines {
		parsedConfig, parseError := parseJoinCommandArgs(acceptedArgs)
		if parseError != nil || parsedConfig != expectedConfig {
			testContext.Errorf("cca join %v: got %+v, %v", acceptedArgs, parsedConfig, parseError)
		}
	}

	refusedCommandLines := [][]string{
		// After "--" a flag is an argument, so this is four arguments, not a flag silently honoured.
		{"https://ctrl:9770", joinTokenForTest, "--node-id", "worker-1", "--", "--ca-cert", "/pki/ca.pem"},
		{"https://ctrl:9770", "--", joinTokenForTest, "--node-id=worker-1"},
		{"https://ctrl:9770", joinTokenForTest, "--node-id", "worker-1", "--ca-crt", "/pki/ca.pem"},
		{"https://ctrl:9770", joinTokenForTest, "--node-id"},
		{"https://ctrl:9770", joinTokenForTest, "--node-id", "worker-1", "third-argument"},
	}
	for _, refusedArgs := range refusedCommandLines {
		_, parseError := parseJoinCommandArgs(refusedArgs)
		if parseError == nil {
			testContext.Errorf("cca join %v: accepted, want an error", refusedArgs)
			continue
		}
		if strings.Contains(parseError.Error(), joinTokenForTest) {
			testContext.Errorf("cca join error echoes the join token: %v", parseError)
		}
	}
}

// TestServerFlagsOIDCIssuerNeedsClientID is the M228 bug: an OIDC issuer
// without a client ID means no audience check, so tokens the provider issued
// for other applications would be accepted. Both flags or neither.
func TestServerFlagsOIDCIssuerNeedsClientID(testContext *testing.T) {
	_, issuerOnlyError := parseServerCommandArgs([]string{"--store", "memory", "--oidc-issuer", "https://issuer.example"})
	if issuerOnlyError == nil || !strings.Contains(issuerOnlyError.Error(), "--oidc-client-id") {
		testContext.Errorf("--oidc-issuer without --oidc-client-id: got %v, want an error naming --oidc-client-id", issuerOnlyError)
	}
	_, clientOnlyError := parseServerCommandArgs([]string{"--store", "memory", "--oidc-client-id", "cca-client"})
	if clientOnlyError == nil || !strings.Contains(clientOnlyError.Error(), "--oidc-issuer") {
		testContext.Errorf("--oidc-client-id without --oidc-issuer: got %v, want an error naming --oidc-issuer", clientOnlyError)
	}
	bothConfig := mustParseServerFlags(testContext, "--store", "memory",
		"--oidc-issuer", "https://issuer.example", "--oidc-client-id", "cca-client")
	if bothConfig.oidcIssuerURL == "" || bothConfig.oidcClientID == "" {
		testContext.Errorf("both OIDC flags: %+v", bothConfig)
	}
}

// TestDaemonFlagsRefuseBadValues checks values the old parsers ignored or
// exited on: they are now returned as errors.
func TestDaemonFlagsRefuseBadValues(testContext *testing.T) {
	if _, parseError := parseInitCommandArgs([]string{"--ca-validity", "ten-years"}); parseError == nil {
		testContext.Error("cca init --ca-validity ten-years: accepted, want an error")
	}
	if _, parseError := parseServerCommandArgs([]string{"--store", "memory", "--keyring-retire-grace", "1m"}); parseError == nil {
		testContext.Error("cca server --keyring-retire-grace 1m: accepted, want an error (minimum 1h)")
	}
	if _, parseError := parseServerCommandArgs([]string{"--store", "memory", "--api-only", "--controllers-only"}); parseError == nil {
		testContext.Error("cca server --api-only --controllers-only: accepted, want an error")
	}
	if _, parseError := parseAgentCommandArgs([]string{"--store", "memory", "--runtime", "docker"}); parseError == nil {
		testContext.Error("cca agent --runtime docker: accepted, want an error")
	}
	if _, parseError := parseServerCommandArgs([]string{"--help"}); !errors.Is(parseError, flag.ErrHelp) {
		testContext.Errorf("cca server --help: got %v, want flag.ErrHelp", parseError)
	}
}

// TestServerFlagsEveryFlagReachesItsField pins the meaning of every `cca server`
// flag, so the parser can be replaced without changing what a unit file does.
func TestServerFlagsEveryFlagReachesItsField(testContext *testing.T) {
	parsedConfig := mustParseServerFlags(testContext,
		"--store", "etcd", "--endpoints", "10.0.0.1:2379", "--store-prefix", "/cca-test/",
		"--listen", "10.0.0.1:9770", "--cert", "/pki/server.crt", "--key", "/pki/server.key",
		"--ca", "/pki/ca.crt", "--ca-key", "/pki/ca.key",
		"--etcd-cert", "/pki/etcd.crt", "--etcd-key", "/pki/etcd.key", "--etcd-ca", "/pki/etcd-ca.crt",
		"--dns-listen", "10.0.0.1:53", "--log-level", "debug", "--log-format", "json",
		"--api-only", "--node-id", "ctrl-1", "--cloud-provider", "aws", "--cloud-region", "us-east-1",
		"--secret-root-key", "/keys/root.key", "--root-key-provider", "aws-kms",
		"--root-key-id", "alias/ccattler-secrets", "--keyring-retire-grace", "48h",
		"--oidc-issuer", "https://issuer.example", "--oidc-client-id", "cca-client",
		"--oidc-claim-team", "groups", "--authn-webhook-url", "https://authn.example/check",
		"--cluster-cidr", "10.50.0.0/16", "--vip-cidr", "10.60.0.0/24",
	)
	expectedConfig := serverCommandConfig{ //nolint:gosec // G101: key file paths, not credentials
		storeBackend: "etcd", etcdEndpoints: "10.0.0.1:2379", storeKeyPrefix: "/cca-test/",
		listenAddress: "10.0.0.1:9770", tlsEnabled: true, tlsCertPath: "/pki/server.crt",
		tlsKeyPath: "/pki/server.key", tlsCACertPath: "/pki/ca.crt", tlsCAKeyPath: "/pki/ca.key",
		etcdCertPath: "/pki/etcd.crt", etcdKeyPath: "/pki/etcd.key", etcdCACertPath: "/pki/etcd-ca.crt",
		dnsEnabled: true, dnsListenAddress: "10.0.0.1:53", logLevel: "debug", logFormat: "json",
		apiOnly: true, nodeID: "ctrl-1", cloudProviderName: "aws", cloudRegion: "us-east-1",
		oidcIssuerURL: "https://issuer.example", oidcClientID: "cca-client", oidcClaimTeam: "groups",
		authnWebhookURL: "https://authn.example/check", clusterCIDR: "10.50.0.0/16", vipCIDR: "10.60.0.0/24",
		secretRootKeyPath: "/keys/root.key", rootKeyProviderName: "aws-kms",
		rootKeyID: "alias/ccattler-secrets", keyringRetireGrace: 48 * time.Hour,
	}
	if !reflect.DeepEqual(parsedConfig, expectedConfig) {
		testContext.Errorf("parsed server flags differ:\n got  %+v\n want %+v", parsedConfig, expectedConfig)
	}
}

// TestServerFlagsSwitchesAndDerivedValues pins the flags without a value and
// the settings one flag derives for another.
func TestServerFlagsSwitchesAndDerivedValues(testContext *testing.T) {
	switchConfig := mustParseServerFlags(testContext, "--store", "memory", "--tls", "--dns", "--controllers-only")
	if !switchConfig.tlsEnabled || !switchConfig.dnsEnabled || !switchConfig.controllersOnly {
		testContext.Errorf("--tls, --dns or --controllers-only not set: %+v", switchConfig)
	}
	if switchConfig.dnsListenAddress != ":"+defaultDNSListenPort {
		testContext.Errorf("--dns default listen address: got %q", switchConfig.dnsListenAddress)
	}
	if switchConfig.listenAddress != "0.0.0.0:"+defaultAPIListenPort {
		testContext.Errorf("TLS server default listen address: got %q", switchConfig.listenAddress)
	}

	plainConfig := mustParseServerFlags(testContext, "--store", "memory")
	if plainConfig.listenAddress != "127.0.0.1:"+defaultAPIListenPort {
		testContext.Errorf("non-TLS server must default to loopback, got %q", plainConfig.listenAddress)
	}

	endpointsConfig := mustParseServerFlags(testContext, "--store", "memory", "--endpoints", "10.0.0.1:2379")
	if endpointsConfig.storeBackend != "etcd" {
		testContext.Errorf("--endpoints after --store must select etcd, got %q", endpointsConfig.storeBackend)
	}
	laterStoreConfig := mustParseServerFlags(testContext, "--endpoints", "10.0.0.1:2379", "--store", "memory")
	if laterStoreConfig.storeBackend != "memory" {
		testContext.Errorf("--store after --endpoints must win, got %q", laterStoreConfig.storeBackend)
	}
}

// TestAgentFlagsEveryFlagReachesItsField pins the meaning of every `cca agent` flag.
func TestAgentFlagsEveryFlagReachesItsField(testContext *testing.T) {
	parsedConfig := mustParseAgentFlags(testContext,
		"--store", "etcd", "--endpoints", "10.0.0.1:2379", "--store-prefix", "/cca-test/",
		"--node-id", "worker-1", "--runtime", "process", "--cert", "/pki/node.crt", "--key", "/pki/node.key",
		"--ca", "/pki/ca.crt", "--etcd-cert", "/pki/etcd.crt", "--etcd-key", "/pki/etcd.key",
		"--etcd-ca", "/pki/etcd-ca.crt", "--advertise-address", "10.0.0.2",
		"--proxy-listen", "10.0.0.2:8080", "--proxy-tls-listen", "10.0.0.2:8443",
		"--log-level", "debug", "--log-format", "json", "--api-server", "10.0.0.1:9770",
		"--debug-listen", "10.0.0.2:9771", "--secret-api-listen", "10.0.0.2:9772",
		"--dataplane", "nftables", "--cluster-cidr", "10.50.0.0/16", "--network-mode", "vxlan",
		"--dns-upstream", "10.0.0.1:15353", "--storage-backend", "nfs", "--storage-dir", "/mnt/nfs",
	)
	expectedConfig := agentCommandConfig{
		storeBackend: "etcd", etcdEndpoints: "10.0.0.1:2379", storeKeyPrefix: "/cca-test/",
		nodeID: "worker-1", runtimeBackend: "process", advertiseAddress: "10.0.0.2",
		tlsCertPath: "/pki/node.crt", tlsKeyPath: "/pki/node.key", tlsCACertPath: "/pki/ca.crt",
		etcdCertPath: "/pki/etcd.crt", etcdKeyPath: "/pki/etcd.key", etcdCACertPath: "/pki/etcd-ca.crt",
		proxyEnabled: true, proxyListenAddress: "10.0.0.2:8080", proxyTLSListenAddress: "10.0.0.2:8443",
		logLevel: "debug", logFormat: "json", apiServerAddress: "10.0.0.1:9770",
		debugListenAddress: "10.0.0.2:9771", secretAPIListenAddress: "10.0.0.2:9772",
		dataPlaneBackend: "nftables", clusterCIDR: "10.50.0.0/16", networkMode: "vxlan",
		dnsUpstream: "10.0.0.1:15353", storageBackend: "nfs", storageDirectory: "/mnt/nfs",
	}
	if !reflect.DeepEqual(parsedConfig, expectedConfig) {
		testContext.Errorf("parsed agent flags differ:\n got  %+v\n want %+v", parsedConfig, expectedConfig)
	}

	proxyConfig := mustParseAgentFlags(testContext, "--store", "memory", "--node-id", "worker-1", "--proxy")
	if !proxyConfig.proxyEnabled || proxyConfig.proxyListenAddress != "0.0.0.0:80" {
		testContext.Errorf("--proxy defaults: %+v", proxyConfig)
	}
	if proxyConfig.runtimeBackend != "containerd" || proxyConfig.dataPlaneBackend != "auto" {
		testContext.Errorf("agent defaults: runtime %q dataplane %q", proxyConfig.runtimeBackend, proxyConfig.dataPlaneBackend)
	}
}

// TestInitFlagsEveryFlagReachesItsField pins the meaning of every `cca init` flag.
func TestInitFlagsEveryFlagReachesItsField(testContext *testing.T) {
	parsedConfig := mustParseInitFlags(testContext,
		"--advertise-address", "10.0.0.1", "--node-id", "ctrl-1", "--output", "/tmp/cca-init",
		"--san", "ctrl-1.example", "--san", "10.0.0.9", "--ca-validity", "100h", "--cert-validity", "50h",
		"--cluster-cidr", "10.50.0.0/16", "--vip-cidr", "10.60.0.0/24",
		"--ca", "/pki/ca.crt", "--ca-key", "/pki/ca.key",
	)
	expectedConfig := initCommandConfig{
		outputDirectory: "/tmp/cca-init", advertiseAddress: "10.0.0.1", nodeID: "ctrl-1",
		extraSANs: []string{"ctrl-1.example", "10.0.0.9"}, caValidity: 100 * time.Hour,
		certValidity: 50 * time.Hour, clusterCIDR: "10.50.0.0/16", vipCIDR: "10.60.0.0/24",
		existingCAPath: "/pki/ca.crt", existingCAKeyPath: "/pki/ca.key",
	}
	if !reflect.DeepEqual(parsedConfig, expectedConfig) {
		testContext.Errorf("parsed init flags differ:\n got  %+v\n want %+v", parsedConfig, expectedConfig)
	}

	shortOutputConfig := mustParseInitFlags(testContext, "--node-id", "ctrl-1", "--advertise-address", "10.0.0.1", "-o", "/tmp/short")
	if shortOutputConfig.outputDirectory != "/tmp/short" {
		testContext.Errorf("-o: got %q", shortOutputConfig.outputDirectory)
	}
	if shortOutputConfig.caValidity != defaultCACertificateValidityInit ||
		shortOutputConfig.certValidity != defaultNodeCertificateValidityInit {
		testContext.Errorf("init validity defaults: %+v", shortOutputConfig)
	}
}
