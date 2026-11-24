// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// setupScriptTestAdvertiseAddress is the control-plane address the setup.sh tests use.
const setupScriptTestAdvertiseAddress = "192.0.2.10"

// setupScriptRealTools are host utilities the setup.sh steps need that the tests do not fake.
var setupScriptRealTools = []string{"cat", "mkdir", "chmod", "grep"}

// setupScriptFakeTools are logging fakes for the host-changing commands setup.sh runs.
// systemctl reports an etcd unit when FAKE_ETCD_UNIT_EXISTS is set; useradd and
// adduser create the marker file that the fake id checks for the etcd user.
var setupScriptFakeTools = map[string]string{
	"systemctl": `#!/bin/sh
echo "systemctl $*" >> "$FAKE_CALL_LOG"
if [ "$1" = "cat" ] && [ -z "${FAKE_ETCD_UNIT_EXISTS:-}" ]; then exit 1; fi
exit 0
`,
	"useradd": `#!/bin/sh
echo "useradd $*" >> "$FAKE_CALL_LOG"
: > "$FAKE_ETCD_USER_MARKER"
`,
	"adduser": `#!/bin/sh
echo "adduser $*" >> "$FAKE_CALL_LOG"
: > "$FAKE_ETCD_USER_MARKER"
`,
	"id": `#!/bin/sh
[ -f "$FAKE_ETCD_USER_MARKER" ]
`,
	"chown": `#!/bin/sh
echo "chown $*" >> "$FAKE_CALL_LOG"
`,
	"firewall-cmd": `#!/bin/sh
echo "firewall-cmd $*" >> "$FAKE_CALL_LOG"
if [ "$1" = "--state" ]; then echo running; fi
`,
	"ufw": `#!/bin/sh
echo "ufw $*" >> "$FAKE_CALL_LOG"
if [ "$1" = "status" ]; then echo "Status: active"; fi
`,
}

// setupScriptStepOptions places etcd and selects the fakes for one setup.sh step run.
type setupScriptStepOptions struct {
	etcdOnPath          bool     // a fake etcd is on the script's PATH
	etcdInRootLocations []string // root-relative paths of extra fake etcd binaries, e.g. /usr/local/bin/etcd
	etcdUnitExists      bool     // systemctl cat etcd.service succeeds
	omitFakeTools       []string // fake tools left off PATH (e.g. ufw for the firewalld case)
}

// setupScriptStepRun is the outcome of running one setup.sh step.
type setupScriptStepRun struct {
	output    string   // combined stdout and stderr
	exitError error    // non-nil when the step exited non-zero
	toolCalls []string // one line per fake tool call
	rootPath  string   // temp directory standing in for /
}

// readRootFile returns the content of a root-relative file, or "" when it does not exist.
func (setupScriptStepRun setupScriptStepRun) readRootFile(t *testing.T, rootRelativePath string) string {
	t.Helper()
	content, readError := os.ReadFile(filepath.Join(setupScriptStepRun.rootPath, rootRelativePath)) // #nosec G304 -- test temp file
	if readError != nil {
		return ""
	}
	return string(content)
}

// runSetupScriptStep runs a setup.sh step under set -eu in a temp root: /etc and /usr
// paths are moved under it, and PATH holds only fakes and a few linked host tools.
func runSetupScriptStep(t *testing.T, scriptStep string, options setupScriptStepOptions) setupScriptStepRun {
	t.Helper()
	rootPath := t.TempDir()
	fakeBinDirectory := filepath.Join(rootPath, "fakebin")
	for _, directory := range []string{fakeBinDirectory, filepath.Join(rootPath, "etc/systemd/system"), filepath.Join(rootPath, "pki")} {
		if mkdirError := os.MkdirAll(directory, 0o750); mkdirError != nil {
			t.Fatalf("mkdir: %v", mkdirError)
		}
	}
	linkRealTools(t, fakeBinDirectory, setupScriptRealTools)
	for toolName, toolScript := range setupScriptFakeTools {
		if !slices.Contains(options.omitFakeTools, toolName) {
			writeFakeExecutable(t, fakeBinDirectory, toolName, toolScript)
		}
	}
	fakeEtcd := "#!/bin/sh\nexit 0\n"
	if options.etcdOnPath {
		writeFakeExecutable(t, fakeBinDirectory, "etcd", fakeEtcd)
	}
	for _, etcdLocation := range options.etcdInRootLocations {
		etcdDirectory := filepath.Join(rootPath, filepath.Dir(etcdLocation))
		if mkdirError := os.MkdirAll(etcdDirectory, 0o750); mkdirError != nil {
			t.Fatalf("mkdir: %v", mkdirError)
		}
		writeFakeExecutable(t, etcdDirectory, filepath.Base(etcdLocation), fakeEtcd)
	}
	pathRewriter := strings.NewReplacer("/etc/", rootPath+"/etc/", "/usr/", rootPath+"/usr/")
	script := "set -eu\nPKI_DIR=" + filepath.Join(rootPath, "pki") + "\n" + pathRewriter.Replace(scriptStep)
	callLogPath := filepath.Join(rootPath, "calls.log")
	command := exec.Command("/bin/sh", "-c", script) // #nosec G204 -- generated test script
	command.Env = []string{
		"PATH=" + fakeBinDirectory,
		"FAKE_CALL_LOG=" + callLogPath,
		"FAKE_ETCD_USER_MARKER=" + filepath.Join(rootPath, "etcd-user-created"),
	}
	if options.etcdUnitExists {
		command.Env = append(command.Env, "FAKE_ETCD_UNIT_EXISTS=1")
	}
	output, runError := command.CombinedOutput()
	callLog, _ := os.ReadFile(callLogPath) // #nosec G304 -- test temp file
	return setupScriptStepRun{
		output:    string(output),
		exitError: runError,
		toolCalls: strings.Split(strings.TrimSpace(string(callLog)), "\n"),
		rootPath:  rootPath,
	}
}

// etcdUnitPath and etcdDropInPath are the root-relative files the etcd step writes.
const (
	etcdUnitPath   = "/etc/systemd/system/etcd.service"
	etcdDropInPath = "/etc/systemd/system/etcd.service.d/tls.conf"
)

// TestSetupScriptWritesEtcdUnitWhenNoneExists: with a release etcd in /usr/local/bin
// and no etcd.service, the etcd step creates the etcd user and writes a full unit
// with the explicit Debian-layout data directory.
func TestSetupScriptWritesEtcdUnitWhenNoneExists(t *testing.T) {
	stepRun := runSetupScriptStep(t, buildSetupScriptEtcdStep(setupScriptTestAdvertiseAddress),
		setupScriptStepOptions{etcdInRootLocations: []string{"/usr/local/bin/etcd"}})
	if stepRun.exitError != nil {
		t.Fatalf("etcd step failed: %v\n%s", stepRun.exitError, stepRun.output)
	}
	useraddCalled := false
	for _, toolCall := range stepRun.toolCalls {
		if strings.HasPrefix(toolCall, "useradd ") && strings.Contains(toolCall, "--system") && strings.HasSuffix(toolCall, " etcd") {
			useraddCalled = true
		}
	}
	if !useraddCalled {
		t.Errorf("etcd system user not created; calls:\n%s", strings.Join(stepRun.toolCalls, "\n"))
	}
	unitContent := stepRun.readRootFile(t, etcdUnitPath)
	for _, wantedLine := range []string{
		"Type=notify",
		"User=etcd",
		"ExecStart=" + filepath.Join(stepRun.rootPath, "/usr/local/bin/etcd"),
		"Environment=ETCD_DATA_DIR=/var/lib/etcd/default",
		"Environment=ETCD_NAME=%H",
		"Restart=on-failure",
		"LimitNOFILE=65536",
	} {
		if !strings.Contains(unitContent, wantedLine+"\n") {
			t.Errorf("etcd.service lacks %q; content:\n%s", wantedLine, unitContent)
		}
	}
	if dropIn := stepRun.readRootFile(t, etcdDropInPath); !strings.Contains(dropIn, "ETCD_CERT_FILE") {
		t.Errorf("TLS drop-in not written; content:\n%s", dropIn)
	}
	if !slices.Contains(stepRun.toolCalls, "systemctl enable etcd.service") {
		t.Errorf("the written etcd.service is not enabled; calls:\n%s", strings.Join(stepRun.toolCalls, "\n"))
	}
}

// TestSetupScriptKeepsDistributionEtcdUnit: when etcd.service exists, the etcd step
// writes only the TLS drop-in, which never sets the data directory or member name.
func TestSetupScriptKeepsDistributionEtcdUnit(t *testing.T) {
	stepRun := runSetupScriptStep(t, buildSetupScriptEtcdStep(setupScriptTestAdvertiseAddress),
		setupScriptStepOptions{etcdOnPath: true, etcdUnitExists: true})
	if stepRun.exitError != nil {
		t.Fatalf("etcd step failed: %v\n%s", stepRun.exitError, stepRun.output)
	}
	if unitContent := stepRun.readRootFile(t, etcdUnitPath); unitContent != "" {
		t.Errorf("distribution etcd.service was replaced:\n%s", unitContent)
	}
	for _, toolCall := range stepRun.toolCalls {
		if strings.HasPrefix(toolCall, "useradd ") || strings.HasPrefix(toolCall, "systemctl enable") {
			t.Errorf("distribution unit exists, yet setup.sh ran: %s", toolCall)
		}
	}
	dropIn := stepRun.readRootFile(t, etcdDropInPath)
	if !strings.Contains(dropIn, "ETCD_CERT_FILE") {
		t.Errorf("TLS drop-in not written; content:\n%s", dropIn)
	}
	// An empty EnvironmentFile= drops the base unit's files, which could override the TLS settings.
	if !strings.Contains(dropIn, "\nEnvironmentFile=\n") {
		t.Errorf("drop-in does not reset EnvironmentFile:\n%s", dropIn)
	}
	for _, forbiddenSetting := range []string{"ETCD_DATA_DIR", "ETCD_NAME"} {
		if strings.Contains(dropIn, forbiddenSetting) {
			t.Errorf("drop-in sets %s, which would override the distribution unit:\n%s", forbiddenSetting, dropIn)
		}
	}
}

// TestSetupScriptFindsEtcdInUsrLocalBin: the etcd step finds etcd outside root's
// PATH, preferring PATH, then /usr/local/bin, then /usr/bin.
func TestSetupScriptFindsEtcdInUsrLocalBin(t *testing.T) {
	testCases := []struct {
		name            string                 // subcase name
		options         setupScriptStepOptions // where etcd is
		wantRootRelBin  string                 // root-relative binary in ExecStart, "" for the PATH one
		wantFakeBinEtcd bool                   // ExecStart names the etcd on PATH
	}{
		{name: "only in /usr/local/bin", options: setupScriptStepOptions{etcdUnitExists: true, etcdInRootLocations: []string{"/usr/local/bin/etcd"}}, wantRootRelBin: "/usr/local/bin/etcd"},
		{name: "/usr/local/bin before /usr/bin", options: setupScriptStepOptions{etcdUnitExists: true, etcdInRootLocations: []string{"/usr/bin/etcd", "/usr/local/bin/etcd"}}, wantRootRelBin: "/usr/local/bin/etcd"},
		{name: "PATH first", options: setupScriptStepOptions{etcdUnitExists: true, etcdOnPath: true, etcdInRootLocations: []string{"/usr/local/bin/etcd"}}, wantFakeBinEtcd: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stepRun := runSetupScriptStep(t, buildSetupScriptEtcdStep(setupScriptTestAdvertiseAddress), testCase.options)
			if stepRun.exitError != nil {
				t.Fatalf("etcd step failed: %v\n%s", stepRun.exitError, stepRun.output)
			}
			wantedBinary := filepath.Join(stepRun.rootPath, testCase.wantRootRelBin)
			if testCase.wantFakeBinEtcd {
				wantedBinary = filepath.Join(stepRun.rootPath, "fakebin", "etcd")
			}
			if dropIn := stepRun.readRootFile(t, etcdDropInPath); !strings.Contains(dropIn, "ExecStart="+wantedBinary+"\n") {
				t.Errorf("drop-in ExecStart is not %s:\n%s", wantedBinary, dropIn)
			}
		})
	}
}

// TestSetupScriptEtcdMissingHintNamesRealSources: without etcd, the etcd step exits
// non-zero and names etcd-server for Debian/Ubuntu and the release binary for RHEL.
func TestSetupScriptEtcdMissingHintNamesRealSources(t *testing.T) {
	stepRun := runSetupScriptStep(t, buildSetupScriptEtcdStep(setupScriptTestAdvertiseAddress), setupScriptStepOptions{})
	if stepRun.exitError == nil {
		t.Errorf("etcd step exited 0 without etcd; output:\n%s", stepRun.output)
	}
	if !strings.Contains(stepRun.output, "apt install etcd-server") {
		t.Errorf("hint lacks the Debian/Ubuntu package:\n%s", stepRun.output)
	}
	if !strings.Contains(stepRun.output, "github.com/etcd-io/etcd/releases") {
		t.Errorf("hint lacks the etcd release download for RHEL-family hosts:\n%s", stepRun.output)
	}
	if strings.Contains(stepRun.output, "dnf install etcd") {
		t.Errorf("hint suggests dnf install etcd, which no EL9 repository ships:\n%s", stepRun.output)
	}
}

// TestSetupScriptOpensEtcdClientPort: the firewall step opens 2379/tcp next to the
// API port and VXLAN in firewalld and in ufw.
func TestSetupScriptOpensEtcdClientPort(t *testing.T) {
	testCases := []struct {
		name        string   // subcase name
		omitTools   []string // the other firewall's fake is left off PATH
		wantedCalls []string // calls that must appear in the log
	}{
		{name: "firewalld", omitTools: []string{"ufw"}, wantedCalls: []string{
			"firewall-cmd --permanent --add-port=2379/tcp",
			"firewall-cmd --permanent --add-port=" + defaultAPIListenPort + "/tcp",
			"firewall-cmd --permanent --add-port=4789/udp",
			"firewall-cmd --reload",
		}},
		{name: "ufw", omitTools: []string{"firewall-cmd"}, wantedCalls: []string{
			"ufw allow 2379/tcp comment CCattler etcd clients",
			"ufw allow " + defaultAPIListenPort + "/tcp comment CCattler API",
			"ufw allow 4789/udp comment CCattler VXLAN",
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stepRun := runSetupScriptStep(t, buildSetupScriptFirewallStep(), setupScriptStepOptions{omitFakeTools: testCase.omitTools})
			if stepRun.exitError != nil {
				t.Fatalf("firewall step failed: %v\n%s", stepRun.exitError, stepRun.output)
			}
			for _, wantedCall := range testCase.wantedCalls {
				if !slices.Contains(stepRun.toolCalls, wantedCall) {
					t.Errorf("missing call %q; calls:\n%s", wantedCall, strings.Join(stepRun.toolCalls, "\n"))
				}
			}
		})
	}
}

// TestSetupScriptEtcdUserWithoutUseradd: without useradd the etcd step falls back
// to adduser --system, and without both it stops with a message.
func TestSetupScriptEtcdUserWithoutUseradd(t *testing.T) {
	t.Run("adduser fallback", func(t *testing.T) {
		stepRun := runSetupScriptStep(t, buildSetupScriptEtcdStep(setupScriptTestAdvertiseAddress),
			setupScriptStepOptions{etcdInRootLocations: []string{"/usr/local/bin/etcd"}, omitFakeTools: []string{"useradd"}})
		if stepRun.exitError != nil {
			t.Fatalf("etcd step failed: %v\n%s", stepRun.exitError, stepRun.output)
		}
		adduserCalled := false
		for _, toolCall := range stepRun.toolCalls {
			if strings.HasPrefix(toolCall, "adduser ") && strings.Contains(toolCall, "--system") && strings.HasSuffix(toolCall, " etcd") {
				adduserCalled = true
			}
		}
		if !adduserCalled {
			t.Errorf("adduser --system not called; calls:\n%s", strings.Join(stepRun.toolCalls, "\n"))
		}
	})
	t.Run("neither", func(t *testing.T) {
		stepRun := runSetupScriptStep(t, buildSetupScriptEtcdStep(setupScriptTestAdvertiseAddress),
			setupScriptStepOptions{etcdInRootLocations: []string{"/usr/local/bin/etcd"}, omitFakeTools: []string{"useradd", "adduser"}})
		if stepRun.exitError == nil || !strings.Contains(stepRun.output, "etcd user") {
			t.Errorf("want a non-zero exit naming the etcd user; err=%v output:\n%s", stepRun.exitError, stepRun.output)
		}
		if unitContent := stepRun.readRootFile(t, etcdUnitPath); unitContent != "" {
			t.Errorf("etcd.service written although the etcd user could not be created")
		}
	})
}
