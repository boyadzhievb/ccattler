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
	"slices"
	"strings"
	"testing"
)

// installScriptPath is the published root install.sh relative to the cmd/cca test directory.
const installScriptPath = "../../install.sh"

// installScriptTestVersion is the release version the install.sh tests ask for.
const installScriptTestVersion = "v0.0.0-test"

// installScriptRealTools are host utilities install.sh needs that the tests do not fake.
var installScriptRealTools = []string{"grep", "awk", "sha256sum", "tar", "gzip", "mktemp", "rm", "mv", "chmod", "mkdir", "cat"}

// installScriptCNIDirectories are the CNI plugin directories install.sh checks; the
// tests move them under a temp root so the host's own plugins do not count.
var installScriptCNIDirectories = []string{"/opt/cni/bin", "/usr/libexec/cni", "/usr/lib/cni"}

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

// installScriptFakeSudo logs its arguments, emulates a password prompt for -n when
// FAKE_SUDO_PROMPTS is set, runs "sh" with sudo's secure_path from FAKE_SUDO_PATH,
// and makes the read-only install directory writable while it runs a command.
const installScriptFakeSudo = `#!/bin/sh
echo "sudo $*" >> "$FAKE_CALL_LOG"
if [ "$1" = "-n" ]; then
  shift
  if [ -n "${FAKE_SUDO_PROMPTS:-}" ]; then
    echo "sudo: a password is required" >&2
    exit 1
  fi
fi
if [ "$1" = "sh" ]; then
  shift
  PATH="$FAKE_SUDO_PATH" exec /bin/sh "$@"
fi
chmod 755 "$FAKE_INSTALL_DIR"
status=0
"$@" || status=$?
chmod 555 "$FAKE_INSTALL_DIR"
exit "$status"
`

// installScriptFakeInstall logs its arguments and copies the file with the -m mode;
// the -o/-g owner flags are only recorded, as the tests do not run as root.
const installScriptFakeInstall = `#!/bin/sh
echo "install $*" >> "$FAKE_CALL_LOG"
mode=755
while [ $# -gt 2 ]; do
  case "$1" in
    -m) shift; mode="$1" ;;
    -o|-g) shift ;;
  esac
  shift
done
cat "$1" > "$2"
chmod "$mode" "$2"
`

// installScriptFakeStat prints the install directory's owner uid and octal mode
// from FAKE_STAT_OWNER_MODE, so a test can pose as a root-owned directory.
const installScriptFakeStat = `#!/bin/sh
echo "$FAKE_STAT_OWNER_MODE"
`

// installScriptFakeUname reports the kernel from FAKE_UNAME_S and an x86_64 machine.
const installScriptFakeUname = `#!/bin/sh
case "$1" in
  -s) echo "$FAKE_UNAME_S" ;;
  *) echo x86_64 ;;
esac
`

// installScriptFakeSELinuxEnabled exits 1 when FAKE_SELINUX_DISABLED is set.
const installScriptFakeSELinuxEnabled = `#!/bin/sh
[ -z "${FAKE_SELINUX_DISABLED:-}" ]
`

// installScriptFakeRestorecon logs its arguments and fails when FAKE_RESTORECON_FAILS is set.
const installScriptFakeRestorecon = `#!/bin/sh
echo "restorecon $*" >> "$FAKE_CALL_LOG"
if [ -n "${FAKE_RESTORECON_FAILS:-}" ]; then
  exit 1
fi
`

// installScriptRun is the outcome of one install.sh run against fake tools.
type installScriptRun struct {
	output     string   // combined stdout and stderr of install.sh
	exitError  error    // non-nil when install.sh exited non-zero
	toolCalls  []string // one line per fake tool call, "<tool> <args>"
	installDir string   // INSTALL_PATH the script installed into
	workDir    string   // temp directory holding every test file
}

// installScriptOptions selects which fakes exist and how they behave.
type installScriptOptions struct {
	withRestorecon     bool   // put fake restorecon and selinuxenabled on PATH
	selinuxDisabled    bool   // make the fake selinuxenabled exit 1
	statOwnerMode      string // install directory "uid mode" from stat; "" means "0 755" (root-owned)
	unameSystem        string // kernel name from uname -s; "" means Linux
	relativeInstall    bool   // pass INSTALL_PATH as the relative path "install"
	writableInstallDir bool   // leave the install directory writable, as for a root run (no sudo)
	restoreconFails    bool   // make the fake restorecon exit 1
	sudoSecurePath     string // PATH that commands run through sudo see; {install} is the install dir
	sudoPromptsForPass bool   // make "sudo -n" fail as if a password were needed
	cniPluginDirectory string // CNI directory (one of installScriptCNIDirectories) given a bridge plugin, "" for none
}

// writeFakeExecutable writes an executable shell script into directory.
func writeFakeExecutable(t *testing.T, directory string, name string, content string) {
	t.Helper()
	if writeError := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o755); writeError != nil { // #nosec G306 -- test fake must be executable
		t.Fatalf("write fake %s: %v", name, writeError)
	}
}

// linkRealTools symlinks each named host utility into directory, failing when one is missing.
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

// buildFakeReleaseArchive writes a release archive holding a dummy cca and returns
// a checksums.txt body naming it for each platform the tests pose as.
func buildFakeReleaseArchive(t *testing.T, archivePath string) string {
	t.Helper()
	var archiveBuffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&archiveBuffer)
	tarWriter := tar.NewWriter(gzipWriter)
	binaryContent := []byte("#!/bin/sh\necho fake cca\n")
	header := &tar.Header{Name: "cca", Mode: 0o755, Size: int64(len(binaryContent))}
	if headerError := tarWriter.WriteHeader(header); headerError != nil {
		t.Fatalf("tar header: %v", headerError)
	}
	if _, writeError := tarWriter.Write(binaryContent); writeError != nil {
		t.Fatalf("tar write: %v", writeError)
	}
	if closeError := tarWriter.Close(); closeError != nil {
		t.Fatalf("tar close: %v", closeError)
	}
	if closeError := gzipWriter.Close(); closeError != nil {
		t.Fatalf("gzip close: %v", closeError)
	}
	if writeError := os.WriteFile(archivePath, archiveBuffer.Bytes(), 0o600); writeError != nil {
		t.Fatalf("write archive: %v", writeError)
	}
	archiveDigest := sha256.Sum256(archiveBuffer.Bytes())
	var checksums strings.Builder
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-amd64"} {
		fmt.Fprintf(&checksums, "%x  cca-%s-%s.tar.gz\n", archiveDigest, installScriptTestVersion, platform)
	}
	return checksums.String()
}

// writeInstallScriptCopy writes install.sh with its CNI directories moved under rootPath.
func writeInstallScriptCopy(t *testing.T, rootPath string) string {
	t.Helper()
	scriptContent, readError := os.ReadFile(installScriptPath)
	if readError != nil {
		t.Fatalf("read install.sh: %v", readError)
	}
	var replacements []string
	for _, cniDirectory := range installScriptCNIDirectories {
		replacements = append(replacements, cniDirectory, rootPath+cniDirectory)
	}
	scriptCopyPath := filepath.Join(rootPath, "install.sh")
	rewritten := strings.NewReplacer(replacements...).Replace(string(scriptContent))
	if writeError := os.WriteFile(scriptCopyPath, []byte(rewritten), 0o600); writeError != nil { // #nosec G703 -- path under t.TempDir
		t.Fatalf("write install.sh copy: %v", writeError)
	}
	return scriptCopyPath
}

// prepareInstallScriptBin creates the fake bin directory, the read-only install
// directory and the optional CNI bridge plugin under workDirectory.
func prepareInstallScriptBin(t *testing.T, workDirectory string, options installScriptOptions) (string, string) {
	t.Helper()
	fakeBinDirectory := filepath.Join(workDirectory, "bin")
	installDirectory := filepath.Join(workDirectory, "install")
	for _, directory := range []string{fakeBinDirectory, installDirectory} {
		if mkdirError := os.MkdirAll(directory, 0o750); mkdirError != nil {
			t.Fatalf("mkdir: %v", mkdirError)
		}
	}
	if !options.writableInstallDir {
		if chmodError := os.Chmod(installDirectory, 0o555); chmodError != nil { // #nosec G302 -- read-only install dir forces the sudo path
			t.Fatalf("chmod: %v", chmodError)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(installDirectory, 0o750) }) // #nosec G302 -- restore so TempDir cleanup can remove it
	linkRealTools(t, fakeBinDirectory, installScriptRealTools)
	writeFakeExecutable(t, fakeBinDirectory, "curl", installScriptFakeCurl)
	writeFakeExecutable(t, fakeBinDirectory, "sudo", installScriptFakeSudo)
	writeFakeExecutable(t, fakeBinDirectory, "install", installScriptFakeInstall)
	writeFakeExecutable(t, fakeBinDirectory, "stat", installScriptFakeStat)
	writeFakeExecutable(t, fakeBinDirectory, "uname", installScriptFakeUname)
	if options.withRestorecon {
		writeFakeExecutable(t, fakeBinDirectory, "restorecon", installScriptFakeRestorecon)
		writeFakeExecutable(t, fakeBinDirectory, "selinuxenabled", installScriptFakeSELinuxEnabled)
	}
	if options.cniPluginDirectory != "" {
		cniDirectory := filepath.Join(workDirectory, options.cniPluginDirectory)
		if mkdirError := os.MkdirAll(cniDirectory, 0o750); mkdirError != nil {
			t.Fatalf("mkdir: %v", mkdirError)
		}
		writeFakeExecutable(t, cniDirectory, "bridge", "#!/bin/sh\n")
	}
	return fakeBinDirectory, installDirectory
}

// runInstallScriptWithFakes runs a copy of the root install.sh with only fake and
// linked tools on PATH and a read-only install directory, so it installs through sudo.
func runInstallScriptWithFakes(t *testing.T, options installScriptOptions) installScriptRun {
	t.Helper()
	workDirectory := t.TempDir()
	fakeBinDirectory, installDirectory := prepareInstallScriptBin(t, workDirectory, options)
	archivePath := filepath.Join(workDirectory, "release.tar.gz")
	checksumsPath := filepath.Join(workDirectory, "checksums.txt")
	if writeError := os.WriteFile(checksumsPath, []byte(buildFakeReleaseArchive(t, archivePath)), 0o600); writeError != nil {
		t.Fatalf("write checksums: %v", writeError)
	}
	callLogPath := filepath.Join(workDirectory, "calls.log")
	command := exec.Command("/bin/sh", writeInstallScriptCopy(t, workDirectory)) // #nosec G204 -- test copy of the repository script
	command.Dir = workDirectory
	installPathValue := installDirectory
	if options.relativeInstall {
		installPathValue = "install"
	}
	command.Env = []string{
		"PATH=" + fakeBinDirectory,
		"HOME=" + workDirectory,
		"TMPDIR=" + workDirectory,
		"CCATTLER_VERSION=" + installScriptTestVersion,
		"INSTALL_PATH=" + installPathValue,
		"FAKE_STAT_OWNER_MODE=" + valueOrDefault(options.statOwnerMode, "0 755"),
		"FAKE_UNAME_S=" + valueOrDefault(options.unameSystem, "Linux"),
		"FAKE_INSTALL_DIR=" + installDirectory,
		"FAKE_RELEASE_ARCHIVE=" + archivePath,
		"FAKE_CHECKSUMS=" + checksumsPath,
		"FAKE_CALL_LOG=" + callLogPath,
		"FAKE_SUDO_PATH=" + strings.ReplaceAll(options.sudoSecurePath, "{install}", installDirectory),
	}
	if options.restoreconFails {
		command.Env = append(command.Env, "FAKE_RESTORECON_FAILS=1")
	}
	if options.sudoPromptsForPass {
		command.Env = append(command.Env, "FAKE_SUDO_PROMPTS=1")
	}
	if options.selinuxDisabled {
		command.Env = append(command.Env, "FAKE_SELINUX_DISABLED=1")
	}
	output, runError := command.CombinedOutput()
	callLog, _ := os.ReadFile(callLogPath) // #nosec G304 -- test temp file
	return installScriptRun{
		output:     string(output),
		exitError:  runError,
		toolCalls:  strings.Split(strings.TrimSpace(string(callLog)), "\n"),
		installDir: installDirectory,
		workDir:    workDirectory,
	}
}

// valueOrDefault returns value, or defaultValue when value is empty.
func valueOrDefault(value string, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

// installedTo reports whether a logged sudo call put a file at installedPath.
func installedTo(toolCalls []string, installedPath string) bool {
	for _, toolCall := range toolCalls {
		if strings.HasPrefix(toolCall, "sudo ") && strings.HasSuffix(toolCall, " "+installedPath) {
			return true
		}
	}
	return false
}

// TestInstallScriptRestoresSELinuxLabel: with restorecon on PATH, install.sh relabels
// the installed binary after copying it, and a failing restorecon only warns.
func TestInstallScriptRestoresSELinuxLabel(t *testing.T) {
	t.Run("relabels after install", func(t *testing.T) {
		run := runInstallScriptWithFakes(t, installScriptOptions{withRestorecon: true, sudoSecurePath: "/usr/bin:{install}"})
		if run.exitError != nil {
			t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
		}
		installedPath := filepath.Join(run.installDir, "cca")
		installIndex, restoreconIndex := -1, -1
		for callIndex, toolCall := range run.toolCalls {
			if installedTo([]string{toolCall}, installedPath) {
				installIndex = callIndex
			}
			if toolCall == "restorecon "+installedPath && restoreconIndex < 0 {
				restoreconIndex = callIndex
			}
		}
		if installIndex < 0 || restoreconIndex < installIndex {
			t.Errorf("want install then restorecon %s; calls:\n%s", installedPath, strings.Join(run.toolCalls, "\n"))
		}
	})
	t.Run("writable dir, no sudo", func(t *testing.T) {
		run := runInstallScriptWithFakes(t, installScriptOptions{withRestorecon: true, writableInstallDir: true, sudoSecurePath: "/usr/bin:{install}"})
		if run.exitError != nil {
			t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
		}
		if !slices.Contains(run.toolCalls, "restorecon "+filepath.Join(run.installDir, "cca")) {
			t.Errorf("want restorecon without sudo; calls:\n%s", strings.Join(run.toolCalls, "\n"))
		}
	})
	t.Run("SELinux disabled", func(t *testing.T) {
		run := runInstallScriptWithFakes(t, installScriptOptions{withRestorecon: true, selinuxDisabled: true, sudoSecurePath: "/usr/bin:{install}"})
		if run.exitError != nil {
			t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
		}
		for _, toolCall := range run.toolCalls {
			if strings.Contains(toolCall, "restorecon") {
				t.Errorf("restorecon ran although SELinux is disabled: %s", toolCall)
			}
		}
	})
	t.Run("restorecon fails", func(t *testing.T) {
		run := runInstallScriptWithFakes(t, installScriptOptions{withRestorecon: true, restoreconFails: true, sudoSecurePath: "/usr/bin:{install}"})
		if run.exitError != nil {
			t.Fatalf("install.sh must succeed when only restorecon fails: %v\n%s", run.exitError, run.output)
		}
		if !strings.Contains(run.output, "restorecon "+filepath.Join(run.installDir, "cca")) {
			t.Errorf("want a warning naming the restorecon command; output:\n%s", run.output)
		}
	})
}

// TestInstallScriptWithoutSELinuxTools: without restorecon, install.sh installs,
// exits 0 and says nothing about SELinux.
func TestInstallScriptWithoutSELinuxTools(t *testing.T) {
	run := runInstallScriptWithFakes(t, installScriptOptions{sudoSecurePath: "/usr/bin:{install}"})
	if run.exitError != nil {
		t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
	}
	if !installedTo(run.toolCalls, filepath.Join(run.installDir, "cca")) {
		t.Errorf("cca was not installed; calls:\n%s", strings.Join(run.toolCalls, "\n"))
	}
	// The temp path carries the test name, which contains "SELinux".
	outputWithoutPaths := strings.ReplaceAll(run.output, run.workDir, "")
	if strings.Contains(strings.ToLower(outputWithoutPaths), "selinux") || strings.Contains(outputWithoutPaths, "restorecon") {
		t.Errorf("output mentions SELinux on a host without it:\n%s", run.output)
	}
}

// TestInstallScriptWarnsWhenSudoPathLacksInstallDir: install.sh warns, with the
// sudoers drop-in fix, only when sudo's secure_path lacks the install directory,
// and prints the general note when it cannot ask sudo without a password. Both
// only on Linux, for an absolute, root-owned, not group/world-writable directory.
func TestInstallScriptWarnsWhenSudoPathLacksInstallDir(t *testing.T) {
	testCases := []struct {
		name          string               // subcase name
		options       installScriptOptions // fake sudo behaviour
		wantDropIn    bool                 // the sudoers drop-in command is printed
		wantAnyNotice bool                 // any secure_path notice is printed
	}{
		{name: "missing", options: installScriptOptions{sudoSecurePath: "/sbin:/bin:/usr/sbin:/usr/bin"}, wantDropIn: true, wantAnyNotice: true},
		{name: "on path", options: installScriptOptions{sudoSecurePath: "/sbin:/bin:{install}:/usr/bin"}},
		{name: "sudo prompts", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", sudoPromptsForPass: true}, wantAnyNotice: true},
		{name: "user-owned dir", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", statOwnerMode: "1000 755"}},
		{name: "group-writable dir", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", statOwnerMode: "0 775"}},
		{name: "world-writable dir", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", statOwnerMode: "0 1777"}},
		{name: "relative path", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", relativeInstall: true}},
		{name: "macOS sudo prompts", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", sudoPromptsForPass: true, unameSystem: "Darwin"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			run := runInstallScriptWithFakes(t, testCase.options)
			if run.exitError != nil {
				t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
			}
			hasNotice := strings.Contains(run.output, "secure_path")
			hasDropIn := strings.Contains(run.output, "/etc/sudoers.d/") && strings.Contains(run.output, ":"+run.installDir)
			if hasNotice != testCase.wantAnyNotice || hasDropIn != testCase.wantDropIn {
				t.Errorf("notice=%v dropIn=%v, want notice=%v dropIn=%v; output:\n%s",
					hasNotice, hasDropIn, testCase.wantAnyNotice, testCase.wantDropIn, run.output)
			}
			// After the warning, the next steps call cca by its full path, which sudo finds.
			wantedNextStep := "4. sudo cca status"
			if testCase.wantDropIn {
				wantedNextStep = "4. sudo " + run.installDir + "/cca status"
			}
			if testCase.options.unameSystem != "Darwin" && !strings.Contains(run.output, wantedNextStep) {
				t.Errorf("next steps lack %q; output:\n%s", wantedNextStep, run.output)
			}
		})
	}
}

// TestInstallScriptCNIPrerequisiteCheck: the prerequisite check accepts the CNI
// bridge plugin in /opt/cni/bin or in a distribution directory, and links the
// renamed "Prepare a fresh host" section when plugins are missing.
func TestInstallScriptCNIPrerequisiteCheck(t *testing.T) {
	testCases := []struct {
		name               string // subcase name
		cniPluginDirectory string // directory holding the bridge plugin, "" for none
		wantMissing        bool   // cni-plugins is reported missing
	}{
		{name: "opt cni bin", cniPluginDirectory: "/opt/cni/bin"},
		{name: "usr libexec cni", cniPluginDirectory: "/usr/libexec/cni"},
		{name: "usr lib cni", cniPluginDirectory: "/usr/lib/cni"},
		{name: "none", wantMissing: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			run := runInstallScriptWithFakes(t, installScriptOptions{sudoSecurePath: "/usr/bin:{install}", cniPluginDirectory: testCase.cniPluginDirectory})
			if run.exitError != nil {
				t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
			}
			if reportedMissing := strings.Contains(run.output, "cni-plugins"); reportedMissing != testCase.wantMissing {
				t.Errorf("cni-plugins reported missing = %v, want %v; output:\n%s", reportedMissing, testCase.wantMissing, run.output)
			}
			if testCase.wantMissing && !strings.Contains(run.output, "installation#prepare-a-fresh-host") {
				t.Errorf("missing-prerequisite hint does not link #prepare-a-fresh-host:\n%s", run.output)
			}
		})
	}
}

// TestInstallScriptInstallsRootOwnedBinary: when the install needs sudo, the binary
// is installed root:root 0755, so the user who ran install.sh cannot replace what
// systemd and sudo later run as root.
func TestInstallScriptInstallsRootOwnedBinary(t *testing.T) {
	run := runInstallScriptWithFakes(t, installScriptOptions{sudoSecurePath: "/usr/bin:{install}"})
	if run.exitError != nil {
		t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
	}
	installedPath := filepath.Join(run.installDir, "cca")
	rootOwnedInstall := false
	for _, toolCall := range run.toolCalls {
		if strings.HasPrefix(toolCall, "sudo install -o 0 -g 0 -m 0755 ") && strings.HasSuffix(toolCall, " "+installedPath) {
			rootOwnedInstall = true
		}
		if strings.HasPrefix(toolCall, "sudo mv ") {
			t.Errorf("binary moved with its owner kept: %s", toolCall)
		}
	}
	if !rootOwnedInstall {
		t.Errorf("want sudo install -o 0 -g 0 -m 0755 ... %s; calls:\n%s", installedPath, strings.Join(run.toolCalls, "\n"))
	}
	if fileInfo, statError := os.Stat(installedPath); statError != nil || fileInfo.Mode().Perm() != 0o755 {
		t.Errorf("installed file mode: %v, %v; want 0755", fileInfo, statError)
	}
}
