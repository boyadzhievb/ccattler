// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// installScriptPath is the published root install.sh relative to the cmd/cca test directory.
const installScriptPath = "../../install.sh"

// installScriptTestVersion is the release version the install.sh tests ask for.
const installScriptTestVersion = "v0.0.0-test"

// installScriptRealTools are host utilities install.sh needs that the tests do not fake.
var installScriptRealTools = []string{"sed", "grep", "awk", "sha256sum", "tar", "gzip", "mktemp", "rm", "mv", "chmod", "mkdir", "cat"}

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

// installScriptFakeStat prints "uid mode" for its last argument from the FAKE_STAT_TABLE
// lines "<path>\t<uid> <mode>" (tab, as paths may hold spaces), and "0 755" otherwise.
const installScriptFakeStat = `#!/bin/sh
for target_path; do :; done
while IFS='	' read -r table_path table_owner_mode; do
  if [ "$table_path" = "$target_path" ]; then echo "$table_owner_mode"; exit 0; fi
done < "$FAKE_STAT_TABLE"
echo "0 755"
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

// installPathSpelling is how a test spells INSTALL_PATH for the same directory.
type installPathSpelling string

const (
	installPathPlain         installPathSpelling = ""               // the directory as created
	installPathTrailingSlash installPathSpelling = "trailing slash" // "<dir>/"
	installPathDoubleSlash   installPathSpelling = "double slash"   // "<work>//<name>"
)

// installScriptOptions selects which fakes exist and how they behave.
type installScriptOptions struct {
	withRestorecon     bool                // put fake restorecon and selinuxenabled on PATH
	selinuxDisabled    bool                // make the fake selinuxenabled exit 1
	statTable          map[string]string   // "uid mode" stat reports per path ({work}, {install}, {home}); others "0 755"
	symlinkInstallDir  bool                // make the install directory a symlink to {work}/userdir
	unameSystem        string              // kernel name from uname -s; "" means Linux
	relativeInstall    bool                // pass INSTALL_PATH as the relative directory name
	installDirName     string              // install directory name under {work}; "" means "install"
	installPathForm    installPathSpelling // how INSTALL_PATH spells the install directory
	installPathValue   string              // raw INSTALL_PATH ({work} expanded); overrides the directory spellings
	unenterableDir     bool                // create {work}/locked with mode 000 (a directory that cannot be entered)
	writableInstallDir bool                // leave the install directory writable, as for a root run (no sudo)
	restoreconFails    bool                // make the fake restorecon exit 1
	sudoSecurePath     string              // PATH that commands run through sudo see; {install} is the install dir
	sudoPromptsForPass bool                // make "sudo -n" fail as if a password were needed
	cniPluginDirectory string              // CNI directory (one of installScriptCNIDirectories) given a bridge plugin, "" for none
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
	installDirectory := filepath.Join(workDirectory, valueOrDefault(options.installDirName, "install"))
	realInstallDirectory := installDirectory
	if options.symlinkInstallDir {
		realInstallDirectory = filepath.Join(workDirectory, "userdir")
	}
	for _, directory := range []string{fakeBinDirectory, realInstallDirectory, filepath.Join(workDirectory, "home", ".local", "bin")} {
		if mkdirError := os.MkdirAll(directory, 0o750); mkdirError != nil {
			t.Fatalf("mkdir: %v", mkdirError)
		}
	}
	if options.unenterableDir {
		lockedDirectory := filepath.Join(workDirectory, "locked")
		if mkdirError := os.Mkdir(lockedDirectory, 0o000); mkdirError != nil {
			t.Fatalf("mkdir: %v", mkdirError)
		}
		t.Cleanup(func() { _ = os.Chmod(lockedDirectory, 0o750) }) // #nosec G302 -- restore so TempDir cleanup can remove it
	}
	if options.symlinkInstallDir {
		if linkError := os.Symlink(realInstallDirectory, installDirectory); linkError != nil {
			t.Fatalf("symlink: %v", linkError)
		}
	}
	if !options.writableInstallDir {
		if chmodError := os.Chmod(realInstallDirectory, 0o555); chmodError != nil { // #nosec G302 -- read-only install dir forces the sudo path
			t.Fatalf("chmod: %v", chmodError)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(realInstallDirectory, 0o750) }) // #nosec G302 -- restore so TempDir cleanup can remove it
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
	statTablePath := writeFakeStatTable(t, workDirectory, installDirectory, options.statTable)
	command := exec.Command("/bin/sh", writeInstallScriptCopy(t, workDirectory)) // #nosec G204 -- test copy of the repository script
	command.Dir = workDirectory
	installPathValue := installDirectory
	switch {
	case options.relativeInstall:
		installPathValue = filepath.Base(installDirectory)
	case options.installPathValue != "":
		installPathValue = expandInstallTestPaths(options.installPathValue, workDirectory, installDirectory)
	case options.installPathForm == installPathTrailingSlash:
		installPathValue = installDirectory + "/"
	case options.installPathForm == installPathDoubleSlash:
		installPathValue = workDirectory + "//" + filepath.Base(installDirectory)
	}
	command.Env = []string{
		"PATH=" + fakeBinDirectory,
		"HOME=" + workDirectory,
		"TMPDIR=" + workDirectory,
		"CCATTLER_VERSION=" + installScriptTestVersion,
		"INSTALL_PATH=" + installPathValue,
		"FAKE_STAT_TABLE=" + statTablePath,
		"FAKE_UNAME_S=" + valueOrDefault(options.unameSystem, "Linux"),
		"FAKE_INSTALL_DIR=" + installDirectory,
		"FAKE_RELEASE_ARCHIVE=" + archivePath,
		"FAKE_CHECKSUMS=" + checksumsPath,
		"FAKE_CALL_LOG=" + callLogPath,
		"FAKE_SUDO_PATH=" + expandInstallTestPaths(options.sudoSecurePath, workDirectory, installDirectory),
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

// expandInstallTestPaths replaces {work}, {install} and {home} with the test's directories.
func expandInstallTestPaths(text string, workDirectory string, installDirectory string) string {
	return strings.NewReplacer(
		"{install}", installDirectory,
		"{home}", filepath.Join(workDirectory, "home"),
		"{work}", workDirectory,
	).Replace(text)
}

// writeFakeStatTable writes the fake stat's "<path>\t<uid> <mode>" lines and returns the file.
func writeFakeStatTable(t *testing.T, workDirectory string, installDirectory string, statTable map[string]string) string {
	t.Helper()
	var tableBuilder strings.Builder
	for _, tablePath := range slices.Sorted(maps.Keys(statTable)) {
		fmt.Fprintf(&tableBuilder, "%s\t%s\n", expandInstallTestPaths(tablePath, workDirectory, installDirectory), statTable[tablePath])
	}
	statTablePath := filepath.Join(workDirectory, "stat-table")
	if writeError := os.WriteFile(statTablePath, []byte(tableBuilder.String()), 0o600); writeError != nil {
		t.Fatalf("write stat table: %v", writeError)
	}
	return statTablePath
}

// valueOrDefault returns value, or defaultValue when value is empty.
func valueOrDefault(value string, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

// installedTo reports whether a logged sudo install call put a file at installedPath.
func installedTo(toolCalls []string, installedPath string) bool {
	for _, toolCall := range toolCalls {
		if strings.HasPrefix(toolCall, "sudo install ") && strings.HasSuffix(toolCall, " "+installedPath) {
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
		// The temp path carries the test name, which contains "SELinux".
		outputWithoutPaths := strings.ReplaceAll(run.output, run.workDir, "")
		if strings.Contains(strings.ToLower(outputWithoutPaths), "selinux") || strings.Contains(outputWithoutPaths, "restorecon") {
			t.Errorf("output mentions SELinux although it is disabled:\n%s", run.output)
		}
	})
	t.Run("restorecon fails", func(t *testing.T) {
		run := runInstallScriptWithFakes(t, installScriptOptions{withRestorecon: true, restoreconFails: true, sudoSecurePath: "/usr/bin:{install}"})
		if run.exitError != nil {
			t.Fatalf("install.sh must succeed when only restorecon fails: %v\n%s", run.exitError, run.output)
		}
		if !strings.Contains(run.output, "sudo restorecon '"+filepath.Join(physicalPath(t, run.installDir), "cca")+"'") {
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

// installScriptDoubleSlashPattern finds a path with "//" (not a URL scheme's "://").
var installScriptDoubleSlashPattern = regexp.MustCompile(`[^:/]//`)

// installScriptSecurePathGuideLink is the guide step install.sh names for secure_path.
const installScriptSecurePathGuideLink = "https://ccattler.org/installation#prepare-a-fresh-host"

// TestInstallScriptWarnsWhenSudoPathLacksInstallDir: on Linux, when sudo's secure_path
// lacks the install directory, install.sh warns, names the guide's secure_path step and
// its next steps call cca by its full path; it prints the general note when sudo
// prompts. It never prints sudoers text, whatever the paths look like.
func TestInstallScriptWarnsWhenSudoPathLacksInstallDir(t *testing.T) {
	userOwned := "1000 755"
	testCases := []struct {
		name        string               // subcase name
		options     installScriptOptions // fake sudo and filesystem
		wantNotice  bool                 // any secure_path warning or note is printed
		wantWarning bool                 // the warning: guide link and full-path next steps
	}{
		{name: "missing", options: installScriptOptions{sudoSecurePath: "/sbin:/bin:/usr/sbin:/usr/bin"}, wantNotice: true, wantWarning: true},
		{name: "on path", options: installScriptOptions{sudoSecurePath: "/sbin:/bin:{install}:/usr/bin"}},
		{name: "sudo prompts", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", sudoPromptsForPass: true}, wantNotice: true},
		{name: "unsafe sudo path entries", options: installScriptOptions{sudoSecurePath: "/sbin:{home}/.local/bin:.:/usr/bin",
			statTable: map[string]string{"{home}/.local/bin": userOwned, "{home}": userOwned}}, wantNotice: true, wantWarning: true},
		{name: "user-owned dir", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", statTable: map[string]string{"{install}": userOwned}},
			wantNotice: true, wantWarning: true},
		{name: "user-owned parent", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", statTable: map[string]string{"{work}": userOwned}},
			wantNotice: true, wantWarning: true},
		{name: "symlink into a user-owned dir", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", symlinkInstallDir: true,
			statTable: map[string]string{"{work}/userdir": userOwned}}, wantNotice: true, wantWarning: true},
		{name: "relative path", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", relativeInstall: true}, wantNotice: true, wantWarning: true},
		{name: "macOS", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", sudoPromptsForPass: true, unameSystem: "Darwin"}},
		{name: "trailing slash on path", options: installScriptOptions{sudoSecurePath: "/sbin:{install}", installPathForm: installPathTrailingSlash}},
		{name: "double slash", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", installPathForm: installPathDoubleSlash},
			wantNotice: true, wantWarning: true},
		{name: "path with a space", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", installDirName: "my install"},
			wantNotice: true, wantWarning: true},
		{name: "path with a quote", options: installScriptOptions{sudoSecurePath: "/sbin:/bin", installDirName: "it's install"},
			wantNotice: true, wantWarning: true},
		{name: "symlinked dir on path", options: installScriptOptions{sudoSecurePath: "/sbin:{install}", symlinkInstallDir: true}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			run := runInstallScriptWithFakes(t, testCase.options)
			if run.exitError != nil {
				t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
			}
			if strings.Contains(run.output, "Defaults secure_path") || strings.Contains(run.output, "/etc/sudoers.d") {
				t.Errorf("install.sh printed sudoers text:\n%s", run.output)
			}
			if hasNotice := strings.Contains(run.output, "secure_path"); hasNotice != testCase.wantNotice {
				t.Errorf("secure_path notice = %v, want %v; output:\n%s", hasNotice, testCase.wantNotice, run.output)
			}
			if installScriptDoubleSlashPattern.MatchString(run.output) {
				t.Errorf("output has an unnormalized path:\n%s", run.output)
			}
			wantedNextStep := "4. sudo cca status"
			if testCase.wantWarning {
				// The path is normalized (symlinks resolved) and quoted in the printed command.
				wantedNextStep = "4. sudo " + shellQuotedForTest(physicalPath(t, run.installDir)+"/cca") + " status"
				if !strings.Contains(run.output, "secure_path step: "+installScriptSecurePathGuideLink) {
					t.Errorf("warning does not name the guide's secure_path step; output:\n%s", run.output)
				}
			}
			if testCase.options.unameSystem != "Darwin" && !strings.Contains(run.output, wantedNextStep) {
				t.Errorf("next steps lack %q; output:\n%s", wantedNextStep, run.output)
			}
		})
	}
}

// physicalPath returns path with symlinks resolved, as pwd -P prints it.
func physicalPath(t *testing.T, path string) string {
	t.Helper()
	resolvedPath, resolveError := filepath.EvalSymlinks(path)
	if resolveError != nil {
		t.Fatalf("resolve %s: %v", path, resolveError)
	}
	return resolvedPath
}

// shellQuotedForTest single-quotes text as install.sh's shell_quoted does.
func shellQuotedForTest(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// installScriptGuideQuickInstallLink is the guide section the ownership warning links.
const installScriptGuideQuickInstallLink = "https://ccattler.org/installation#quick-install"

// TestInstallScriptWarnsWhenBinaryNotRootOwned: on Linux, when the binary is not owned
// by uid 0, or the install directory or a parent up to / is not root-owned or is group-
// or world-writable (a sticky parent is safe when root owns its child on the path),
// install.sh warns naming the first unsafe path (quoted), says to use a directory
// writable only by root along its whole path, such as /usr/local/bin, and links the
// guide. It never prints chown, chmod or another privileged command.
func TestInstallScriptWarnsWhenBinaryNotRootOwned(t *testing.T) {
	userOwned := "1000 755"
	for _, testCase := range []struct {
		name           string            // subcase name
		installDirName string            // install directory name, "" for "install"
		statTable      map[string]string // owner and mode stat reports; unlisted paths are root-owned 0755
		wantUnsafePath string            // first unsafe path ({install}, {work}); "" for no warning
	}{
		{name: "root-owned"},
		{name: "user-owned binary", statTable: map[string]string{"{install}/cca": userOwned}, wantUnsafePath: "{install}/cca"},
		{name: "user-owned binary, path with a space", installDirName: "my install",
			statTable: map[string]string{"{install}/cca": userOwned}, wantUnsafePath: "{install}/cca"},
		{name: "group-writable dir", statTable: map[string]string{"{install}": "0 2775"}, wantUnsafePath: "{install}"},
		{name: "user-owned dir", statTable: map[string]string{"{install}": userOwned}, wantUnsafePath: "{install}"},
		{name: "user-owned parent", statTable: map[string]string{"{work}": userOwned}, wantUnsafePath: "{work}"},
		{name: "sticky world-writable dir such as /tmp", statTable: map[string]string{"{install}": "0 1777", "{install}/cca": userOwned},
			wantUnsafePath: "{install}/cca"},
		{name: "root-owned dir in a sticky parent", statTable: map[string]string{"{work}": "0 1777"}},
		{name: "user-owned dir in a sticky parent", statTable: map[string]string{"{work}": "0 1777", "{install}": userOwned},
			wantUnsafePath: "{install}"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			run := runInstallScriptWithFakes(t, installScriptOptions{sudoSecurePath: "/usr/bin:{install}", writableInstallDir: true,
				installDirName: testCase.installDirName, statTable: testCase.statTable})
			if run.exitError != nil {
				t.Fatalf("install.sh failed: %v\n%s", run.exitError, run.output)
			}
			if strings.Contains(run.output, "chown") || strings.Contains(run.output, "chmod") {
				t.Errorf("install.sh printed a chown or chmod command:\n%s", run.output)
			}
			wantWarning := testCase.wantUnsafePath != ""
			wantedParts := []string{"writable only by root along its whole path", "/usr/local/bin", installScriptGuideQuickInstallLink}
			if wantWarning {
				unsafePath := expandInstallTestPaths(testCase.wantUnsafePath, physicalPath(t, run.workDir), physicalPath(t, run.installDir))
				wantedParts = append(wantedParts, shellQuotedForTest(unsafePath)+" can be changed by someone other than root")
			}
			for _, wantedPart := range wantedParts {
				if hasPart := strings.Contains(run.output, wantedPart); hasPart != wantWarning {
					t.Errorf("warning part %q present = %v, want %v; output:\n%s", wantedPart, hasPart, wantWarning, run.output)
				}
			}
		})
	}
}

// installScriptErrorMarker starts every line install.sh's log_error prints.
const installScriptErrorMarker = "\033[1;31m==>"

// TestInstallScriptRefusesUnusableInstallPath: INSTALL_PATH "-", a value with a
// newline, or a directory that cannot be entered stops install.sh with an error
// containing the given path, and nothing is installed.
func TestInstallScriptRefusesUnusableInstallPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can enter a mode-000 directory")
	}
	for _, testCase := range []struct {
		name    string               // subcase name
		options installScriptOptions // the INSTALL_PATH under test
	}{
		{name: "dash", options: installScriptOptions{installPathValue: "-"}},
		{name: "newline", options: installScriptOptions{installPathValue: "{work}/new\nline"}},
		{name: "cannot be entered", options: installScriptOptions{installPathValue: "{work}/locked", unenterableDir: true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.options.sudoSecurePath = "/usr/bin"
			run := runInstallScriptWithFakes(t, testCase.options)
			if run.exitError == nil {
				t.Errorf("install.sh accepted INSTALL_PATH %q; output:\n%s", testCase.options.installPathValue, run.output)
			}
			givenPath := expandInstallTestPaths(testCase.options.installPathValue, run.workDir, run.installDir)
			// The path must be in install.sh's own error (log_error's red marker), not only in a shell message.
			errorIndex := strings.Index(run.output, installScriptErrorMarker)
			if errorIndex < 0 || !strings.Contains(run.output[errorIndex:], givenPath) {
				t.Errorf("install.sh error does not contain the given path %q; output:\n%s", givenPath, run.output)
			}
			for _, toolCall := range run.toolCalls {
				if strings.HasPrefix(toolCall, "install ") || strings.HasPrefix(toolCall, "sudo install ") {
					t.Errorf("installed although the path is unusable: %s", toolCall)
				}
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
