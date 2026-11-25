// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// installScriptPath is the published root install.sh relative to the cmd/cca test directory.
const installScriptPath = "../../install.sh"

// installScriptOSReleasePath is the os-release file install.sh reads; the tests point a copy at a fixture.
const installScriptOSReleasePath = "/etc/os-release"

// installScriptTestVersion is the release version the install.sh tests ask for.
const installScriptTestVersion = "v0.0.0-test"

// installScriptTestArchive is the archive name install.sh downloads for the faked linux/amd64 host.
const installScriptTestArchive = "cca-" + installScriptTestVersion + "-linux-amd64.tar.gz"

// installScriptSudoNote is the fixed RHEL-family note that sudo does not search /usr/local/bin.
const installScriptSudoNote = "sudo does not search /usr/local/bin"

// installScriptShells are the shells every install.sh test runs under (dash is Debian's /bin/sh).
var installScriptShells = []string{"dash", "bash"}

// installScriptRealTools are host utilities install.sh and the fakes need that the tests do not fake.
var installScriptRealTools = []string{"awk", "sha256sum", "tar", "gzip", "mktemp", "rm", "cat", "chmod", "mkdir"}

// installScriptForbiddenAdvice are words install.sh output must never hold: no host-computed privileged advice.
var installScriptForbiddenAdvice = []string{"sudoers", "secure_path=", "chown", "chmod"}

// Fixture os-release bodies, as the named distributions ship them (ID and ID_LIKE only).
const (
	osReleaseRocky     = "NAME=\"Rocky Linux\"\nID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n"   // osReleaseRocky is Rocky Linux 9.
	osReleaseAlmaLinux = "NAME=\"AlmaLinux\"\nID=\"almalinux\"\nID_LIKE=\"rhel centos fedora\"\n" // osReleaseAlmaLinux is AlmaLinux 10.
	osReleaseFedora    = "NAME=\"Fedora Linux\"\nID=fedora\n"                                     // osReleaseFedora is Fedora.
	osReleaseDebian    = "NAME=\"Debian GNU/Linux\"\nID=debian\n"                                 // osReleaseDebian is Debian 13.
	osReleaseUbuntu    = "NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n"                           // osReleaseUbuntu is Ubuntu.
	osReleaseArch      = "NAME=\"Arch Linux\"\nID=arch\n"                                         // osReleaseArch is Arch Linux (other Linux).
)

// installScriptFakeCurl writes the local release archive or checksum file to the -o destination.
const installScriptFakeCurl = `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift; destination="$1" ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
case "$url" in
  *checksums.txt) cat "$FAKE_CHECKSUMS" > "$destination" ;;
  *) cat "$FAKE_RELEASE_ARCHIVE" > "$destination" ;;
esac
`

// installScriptFakeSudo logs its arguments, marks on stdout that it ran, and runs the command.
const installScriptFakeSudo = `#!/bin/sh
echo "sudo $*" >> "$FAKE_CALL_LOG"
echo "fake-sudo-ran $1"
exec "$@"
`

// installScriptFakeInstall logs its arguments and creates the directory (-d) or copies the
// file with the -m mode; the -o/-g owner flags are only recorded, as the tests do not run as root.
const installScriptFakeInstall = `#!/bin/sh
echo "install $*" >> "$FAKE_CALL_LOG"
mode=755
make_directory=""
while [ $# -gt 0 ]; do
  case "$1" in
    -d) make_directory=1 ;;
    -m) shift; mode="$1" ;;
    -o|-g) shift ;;
    *) break ;;
  esac
  shift
done
if [ -n "$make_directory" ]; then mkdir -p "$1"; chmod "$mode" "$1"; exit 0; fi
cat "$1" > "$2"
chmod "$mode" "$2"
`

// installScriptFakeID reports a non-root user, so install.sh takes its sudo path even when the tests run as root.
const installScriptFakeID = `#!/bin/sh
echo 1000
`

// installScriptFakeUname reports a Linux kernel on an x86_64 machine.
const installScriptFakeUname = `#!/bin/sh
case "$1" in
  -s) echo Linux ;;
  *) echo x86_64 ;;
esac
`

// installScriptFakeSELinuxEnabled exits 1 when FAKE_SELINUX_DISABLED is set.
const installScriptFakeSELinuxEnabled = `#!/bin/sh
[ -z "${FAKE_SELINUX_DISABLED:-}" ]
`

// installScriptFakeRestorecon logs its arguments.
const installScriptFakeRestorecon = `#!/bin/sh
echo "restorecon $*" >> "$FAKE_CALL_LOG"
`

// selinuxToolsState selects whether the fake selinuxenabled and restorecon exist and what selinuxenabled reports.
type selinuxToolsState string

// selinuxToolsState values.
const (
	selinuxEnabled  selinuxToolsState = "enabled"  // selinuxEnabled puts both tools on PATH; selinuxenabled succeeds.
	selinuxDisabled selinuxToolsState = "disabled" // selinuxDisabled puts both tools on PATH; selinuxenabled fails.
	selinuxMissing  selinuxToolsState = "missing"  // selinuxMissing leaves both tools off PATH.
)

// installScriptOptions describes the host one install.sh run poses as.
type installScriptOptions struct {
	osRelease               string                            // body of the os-release fixture
	selinuxTools            selinuxToolsState                 // whether the SELinux tools exist and report enabled
	checksumsOf             func(archiveDigest string) string // builds checksums.txt from the archive's real digest; nil means a correct entry
	missingInstallDirectory bool                              // leave INSTALL_PATH uncreated, so install.sh makes it
}

// installScriptRun is the outcome of one install.sh run against fake tools.
type installScriptRun struct {
	output        string   // combined stdout and stderr of install.sh, the temp directory (named after the test) shown as {work}
	exitError     error    // non-nil when install.sh exited non-zero
	toolCalls     []string // one line per logged fake tool call, "<tool> <args>"
	installedPath string   // INSTALL_PATH/cca, where install.sh puts the binary
}

// writeFakeExecutable writes an executable shell script into directory.
func writeFakeExecutable(t *testing.T, directory string, name string, content string) {
	t.Helper()
	if writeError := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o755); writeError != nil { // #nosec G306 -- test fake must be executable
		t.Fatalf("write fake %s: %v", name, writeError)
	}
}

// linkRealTools symlinks each named host utility into directory, skipping the test when one is missing.
func linkRealTools(t *testing.T, directory string, toolNames []string) {
	t.Helper()
	for _, toolName := range toolNames {
		toolPath, lookError := exec.LookPath(toolName)
		if lookError != nil {
			t.Skipf("host tool %s not found: %v", toolName, lookError)
		}
		if linkError := os.Symlink(toolPath, filepath.Join(directory, toolName)); linkError != nil {
			t.Fatalf("link %s: %v", toolName, linkError)
		}
	}
}

// writeTestFile writes content to path, failing the test on error.
func writeTestFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if writeError := os.WriteFile(path, content, 0o600); writeError != nil { // #nosec G703 -- path under t.TempDir
		t.Fatalf("write %s: %v", path, writeError)
	}
}

// buildFakeReleaseArchive writes a release archive holding a dummy cca and k8s-to-cca
// and returns the archive's SHA-256 in hex.
func buildFakeReleaseArchive(t *testing.T, archivePath string) string {
	t.Helper()
	var archiveBuffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&archiveBuffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, binaryName := range []string{"cca", "k8s-to-cca"} {
		binaryContent := []byte("#!/bin/sh\necho fake " + binaryName + "\n")
		header := &tar.Header{Name: binaryName, Mode: 0o755, Size: int64(len(binaryContent))}
		if headerError := tarWriter.WriteHeader(header); headerError != nil {
			t.Fatalf("tar header: %v", headerError)
		}
		if _, writeError := tarWriter.Write(binaryContent); writeError != nil {
			t.Fatalf("tar write: %v", writeError)
		}
	}
	if closeError := tarWriter.Close(); closeError != nil {
		t.Fatalf("tar close: %v", closeError)
	}
	if closeError := gzipWriter.Close(); closeError != nil {
		t.Fatalf("gzip close: %v", closeError)
	}
	writeTestFile(t, archivePath, archiveBuffer.Bytes())
	return fmt.Sprintf("%x", sha256.Sum256(archiveBuffer.Bytes()))
}

// writeInstallScriptCopy writes install.sh with its os-release path pointed at osReleasePath.
func writeInstallScriptCopy(t *testing.T, workDirectory string, osReleasePath string) string {
	t.Helper()
	scriptContent, readError := os.ReadFile(installScriptPath)
	if readError != nil {
		t.Fatalf("read install.sh: %v", readError)
	}
	if !bytes.Contains(scriptContent, []byte(installScriptOSReleasePath)) {
		t.Fatalf("install.sh no longer reads %s; update the test fixture", installScriptOSReleasePath)
	}
	scriptCopyPath := filepath.Join(workDirectory, "install.sh")
	writeTestFile(t, scriptCopyPath, bytes.ReplaceAll(scriptContent, []byte(installScriptOSReleasePath), []byte(osReleasePath)))
	return scriptCopyPath
}

// prepareInstallScriptBin creates the fake bin directory, with only fake and linked tools, under workDirectory.
func prepareInstallScriptBin(t *testing.T, workDirectory string, selinuxTools selinuxToolsState) string {
	t.Helper()
	fakeBinDirectory := filepath.Join(workDirectory, "bin")
	if mkdirError := os.Mkdir(fakeBinDirectory, 0o750); mkdirError != nil {
		t.Fatalf("mkdir: %v", mkdirError)
	}
	linkRealTools(t, fakeBinDirectory, installScriptRealTools)
	writeFakeExecutable(t, fakeBinDirectory, "curl", installScriptFakeCurl)
	writeFakeExecutable(t, fakeBinDirectory, "sudo", installScriptFakeSudo)
	writeFakeExecutable(t, fakeBinDirectory, "install", installScriptFakeInstall)
	writeFakeExecutable(t, fakeBinDirectory, "id", installScriptFakeID)
	writeFakeExecutable(t, fakeBinDirectory, "uname", installScriptFakeUname)
	if selinuxTools != selinuxMissing {
		writeFakeExecutable(t, fakeBinDirectory, "selinuxenabled", installScriptFakeSELinuxEnabled)
		writeFakeExecutable(t, fakeBinDirectory, "restorecon", installScriptFakeRestorecon)
	}
	return fakeBinDirectory
}

// runInstallScript runs a copy of the root install.sh under shellName with only fake and
// linked tools on PATH, a local release archive and checksums.txt, and an os-release fixture.
func runInstallScript(t *testing.T, shellName string, options installScriptOptions) installScriptRun {
	t.Helper()
	shellPath, lookError := exec.LookPath(shellName)
	if lookError != nil {
		t.Skipf("shell %s not found: %v", shellName, lookError)
	}
	workDirectory := t.TempDir()
	fakeBinDirectory := prepareInstallScriptBin(t, workDirectory, options.selinuxTools)
	installDirectory := filepath.Join(workDirectory, "install")
	if !options.missingInstallDirectory {
		if mkdirError := os.Mkdir(installDirectory, 0o750); mkdirError != nil {
			t.Fatalf("mkdir: %v", mkdirError)
		}
	}
	archivePath := filepath.Join(workDirectory, "release.tar.gz")
	archiveDigest := buildFakeReleaseArchive(t, archivePath)
	checksums := fmt.Sprintf("%s  %s\n", archiveDigest, installScriptTestArchive)
	if options.checksumsOf != nil {
		checksums = options.checksumsOf(archiveDigest)
	}
	checksumsPath := filepath.Join(workDirectory, "checksums.txt")
	writeTestFile(t, checksumsPath, []byte(checksums))
	osReleasePath := filepath.Join(workDirectory, "os-release")
	writeTestFile(t, osReleasePath, []byte(options.osRelease))
	callLogPath := filepath.Join(workDirectory, "calls.log")
	command := exec.Command(shellPath, writeInstallScriptCopy(t, workDirectory, osReleasePath)) // #nosec G204 -- test copy of the repository script
	command.Dir = workDirectory
	command.Env = []string{
		"PATH=" + fakeBinDirectory,
		"HOME=" + workDirectory,
		"TMPDIR=" + workDirectory,
		"CCATTLER_VERSION=" + installScriptTestVersion,
		"INSTALL_PATH=" + installDirectory,
		"FAKE_RELEASE_ARCHIVE=" + archivePath,
		"FAKE_CHECKSUMS=" + checksumsPath,
		"FAKE_CALL_LOG=" + callLogPath,
	}
	if options.selinuxTools == selinuxDisabled {
		command.Env = append(command.Env, "FAKE_SELINUX_DISABLED=1")
	}
	output, runError := command.CombinedOutput()
	callLog, _ := os.ReadFile(callLogPath) // #nosec G304 -- test temp file
	var toolCalls []string
	if trimmedLog := strings.TrimSpace(string(callLog)); trimmedLog != "" {
		toolCalls = strings.Split(trimmedLog, "\n")
	}
	return installScriptRun{
		output:        strings.ReplaceAll(string(output), workDirectory, "{work}"),
		exitError:     runError,
		toolCalls:     toolCalls,
		installedPath: filepath.Join(installDirectory, "cca"),
	}
}

// forEachInstallShell runs check against an install.sh run with options under every shell in installScriptShells.
func forEachInstallShell(t *testing.T, options installScriptOptions, check func(t *testing.T, run installScriptRun)) {
	t.Helper()
	for _, shellName := range installScriptShells {
		t.Run(shellName, func(t *testing.T) {
			run := runInstallScript(t, shellName, options)
			check(t, run)
		})
	}
}

// requireInstallSucceeded fails the test when install.sh exited non-zero.
func requireInstallSucceeded(t *testing.T, run installScriptRun) {
	t.Helper()
	if run.exitError != nil {
		t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
	}
}

// toolCallIndex returns the index of the first logged call with prefix, or -1.
func toolCallIndex(toolCalls []string, prefix string) int {
	for callIndex, toolCall := range toolCalls {
		if strings.HasPrefix(toolCall, prefix) {
			return callIndex
		}
	}
	return -1
}

// rootInstallCallIndex returns the index of the sudo install call that put a root-owned cca at installedPath, or -1.
func rootInstallCallIndex(toolCalls []string, installedPath string) int {
	for callIndex, toolCall := range toolCalls {
		if strings.HasPrefix(toolCall, "sudo install -o 0 -g 0 -m 0755 ") && strings.HasSuffix(toolCall, " "+installedPath) {
			return callIndex
		}
	}
	return -1
}

// ranRestorecon reports whether install.sh relabelled installedPath through sudo.
func ranRestorecon(run installScriptRun) bool {
	return toolCallIndex(run.toolCalls, "sudo restorecon "+run.installedPath) >= 0
}

// TestInstallScriptPrintsOnlyFixedAdvice: on every family install.sh prints each privileged
// command before running it, then fixed next steps, and never sudoers, secure_path=, chown or chmod.
func TestInstallScriptPrintsOnlyFixedAdvice(t *testing.T) {
	testCases := []struct {
		name      string // name is the host family
		osRelease string // osRelease is the os-release fixture
	}{
		{name: "Debian", osRelease: osReleaseDebian},
		{name: "RHEL", osRelease: osReleaseRocky},
		{name: "other Linux", osRelease: osReleaseArch},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			options := installScriptOptions{osRelease: testCase.osRelease, selinuxTools: selinuxEnabled, missingInstallDirectory: true}
			forEachInstallShell(t, options, func(t *testing.T, run installScriptRun) {
				requireInstallSucceeded(t, run)
				for _, forbiddenWord := range installScriptForbiddenAdvice {
					if strings.Contains(run.output, forbiddenWord) {
						t.Errorf("output holds %q:\n%s", forbiddenWord, run.output)
					}
				}
				for _, privilegedCommand := range []string{"install -d -m 0755 ", "install -o 0 -g 0 -m 0755 "} {
					announcedIndex := strings.Index(run.output, "==>\033[0m "+privilegedCommand)
					if announcedIndex < 0 || !strings.Contains(run.output[announcedIndex:], "fake-sudo-ran install") {
						t.Errorf("%q not printed before sudo ran it:\n%s", privilegedCommand, run.output)
					}
				}
				if !strings.Contains(run.output, "Next steps") || !strings.Contains(run.output, "cca demo") {
					t.Errorf("output lacks the fixed next steps:\n%s", run.output)
				}
			})
		})
	}
}

// TestInstallScriptAbortsOnBadChecksum: a digest mismatch or a checksums.txt without the
// archive's exact name exits non-zero and installs nothing.
func TestInstallScriptAbortsOnBadChecksum(t *testing.T) {
	testCases := []struct {
		name        string                            // name describes the bad checksums.txt
		checksumsOf func(archiveDigest string) string // checksumsOf builds the bad checksums.txt
		wantMessage string                            // wantMessage is the abort reason install.sh prints
	}{
		{
			name: "mismatch",
			checksumsOf: func(string) string {
				return strings.Repeat("0", sha256.Size*2) + "  " + installScriptTestArchive + "\n"
			},
			wantMessage: "Checksum mismatch",
		},
		{
			name: "missing entry",
			checksumsOf: func(archiveDigest string) string {
				return archiveDigest + "  " + installScriptTestArchive + ".sig\n" + archiveDigest + "  old-" + installScriptTestArchive + "\n"
			},
			wantMessage: "No checksum for " + installScriptTestArchive,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			options := installScriptOptions{osRelease: osReleaseDebian, selinuxTools: selinuxMissing, checksumsOf: testCase.checksumsOf}
			forEachInstallShell(t, options, func(t *testing.T, run installScriptRun) {
				if run.exitError == nil {
					t.Fatalf("install.sh succeeded with a bad checksum:\n%s", run.output)
				}
				if !strings.Contains(run.output, testCase.wantMessage) {
					t.Errorf("output lacks %q:\n%s", testCase.wantMessage, run.output)
				}
				if _, statError := os.Lstat(run.installedPath); !os.IsNotExist(statError) {
					t.Errorf("cca was installed at %s (stat: %v)", run.installedPath, statError)
				}
				if installIndex := toolCallIndex(run.toolCalls, "sudo install"); installIndex >= 0 {
					t.Errorf("install ran after a bad checksum: %v", run.toolCalls)
				}
			})
		})
	}
}

// TestInstallScriptInstallsRootOwnedBinary: a non-root run installs cca with
// "sudo install -o 0 -g 0 -m 0755", and no install step runs without sudo.
func TestInstallScriptInstallsRootOwnedBinary(t *testing.T) {
	forEachInstallShell(t, installScriptOptions{osRelease: osReleaseDebian, selinuxTools: selinuxMissing}, func(t *testing.T, run installScriptRun) {
		requireInstallSucceeded(t, run)
		if rootInstallCallIndex(run.toolCalls, run.installedPath) < 0 {
			t.Errorf("no root-owned sudo install of %s: %v", run.installedPath, run.toolCalls)
		}
		for callIndex, toolCall := range run.toolCalls {
			if strings.HasPrefix(toolCall, "install ") && (callIndex == 0 || run.toolCalls[callIndex-1] != "sudo "+toolCall) {
				t.Errorf("install ran without sudo: %q in %v", toolCall, run.toolCalls)
			}
		}
		if installedInfo, statError := os.Stat(run.installedPath); statError != nil || installedInfo.Mode().Perm() != 0o755 {
			t.Errorf("installed cca: %v, stat error %v", installedInfo, statError)
		}
	})
}

// TestInstallScriptPicksFamilyPath: ID or ID_LIKE naming rhel, fedora or centos takes the RHEL
// path (restorecon and the sudo note); debian, ubuntu and others do neither.
func TestInstallScriptPicksFamilyPath(t *testing.T) {
	testCases := []struct {
		name      string // name is the os-release ID
		osRelease string // osRelease is the os-release fixture
		wantRHEL  bool   // wantRHEL is true when the RHEL path must be taken
	}{
		{name: "rocky", osRelease: osReleaseRocky, wantRHEL: true},
		{name: "almalinux", osRelease: osReleaseAlmaLinux, wantRHEL: true},
		{name: "fedora", osRelease: osReleaseFedora, wantRHEL: true},
		{name: "debian", osRelease: osReleaseDebian, wantRHEL: false},
		{name: "ubuntu", osRelease: osReleaseUbuntu, wantRHEL: false},
		{name: "arch", osRelease: osReleaseArch, wantRHEL: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			forEachInstallShell(t, installScriptOptions{osRelease: testCase.osRelease, selinuxTools: selinuxEnabled}, func(t *testing.T, run installScriptRun) {
				requireInstallSucceeded(t, run)
				if gotRestorecon := ranRestorecon(run); gotRestorecon != testCase.wantRHEL {
					t.Errorf("restorecon ran = %v, want %v: %v", gotRestorecon, testCase.wantRHEL, run.toolCalls)
				}
				if gotNote := strings.Contains(run.output, installScriptSudoNote); gotNote != testCase.wantRHEL {
					t.Errorf("sudo note printed = %v, want %v:\n%s", gotNote, testCase.wantRHEL, run.output)
				}
			})
		})
	}
}

// TestInstallScriptRestoresSELinuxLabel: on the RHEL path restorecon relabels the installed
// binary after the install only when selinuxenabled succeeds; the sudo note is printed regardless.
func TestInstallScriptRestoresSELinuxLabel(t *testing.T) {
	for _, selinuxTools := range []selinuxToolsState{selinuxEnabled, selinuxDisabled, selinuxMissing} {
		t.Run(string(selinuxTools), func(t *testing.T) {
			forEachInstallShell(t, installScriptOptions{osRelease: osReleaseRocky, selinuxTools: selinuxTools}, func(t *testing.T, run installScriptRun) {
				requireInstallSucceeded(t, run)
				installIndex := rootInstallCallIndex(run.toolCalls, run.installedPath)
				restoreconIndex := toolCallIndex(run.toolCalls, "sudo restorecon "+run.installedPath)
				if selinuxTools == selinuxEnabled {
					if installIndex < 0 || restoreconIndex < installIndex {
						t.Errorf("restorecon did not follow the install (install %d, restorecon %d): %v", installIndex, restoreconIndex, run.toolCalls)
					}
				} else {
					if restoreconIndex >= 0 || toolCallIndex(run.toolCalls, "restorecon") >= 0 {
						t.Errorf("restorecon ran with SELinux %s: %v", selinuxTools, run.toolCalls)
					}
					if strings.Contains(run.output, "SELinux") || strings.Contains(run.output, "restorecon") {
						t.Errorf("output mentions SELinux with SELinux %s:\n%s", selinuxTools, run.output)
					}
				}
				if !strings.Contains(run.output, installScriptSudoNote) {
					t.Errorf("sudo note missing:\n%s", run.output)
				}
			})
		})
	}
}
