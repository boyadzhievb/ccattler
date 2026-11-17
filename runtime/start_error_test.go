// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"errors"
	"testing"
)

// missingBinaryExecError is runc's own description of a missing entrypoint, as
// the short exit reason keeps it (M238 N5).
const missingBinaryExecError = `exec: "/no/such/binary": stat /no/such/binary: no such file or directory`

// TestStartErrorMarksProcessStartFailure checks that a containerd create task
// or start task error marks the start error ProcessFailed, with runc's exec
// failure as ProcessError, only when runc could not exec the workload's process
// (spec instance-restart, M238). The messages are runc 1.1.15 and 1.4.0 texts.
func TestStartErrorMarksProcessStartFailure(t *testing.T) {
	testCases := []struct {
		name             string // name describes the case.
		taskPhase        string // taskPhase is the containerd step that failed.
		taskErrorText    string // taskErrorText is the error containerd returned.
		wantProcessError string // wantProcessError is the expected ProcessError; "" means not the workload's process.
	}{
		{
			name:             "runc 1.1 missing binary in create task",
			taskPhase:        containerdTaskPhaseCreate,
			taskErrorText:    `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: exec: "/no/such/binary": stat /no/such/binary: no such file or directory: unknown`,
			wantProcessError: missingBinaryExecError,
		},
		{
			name:             "runc 1.1 non-executable entrypoint in create task",
			taskPhase:        containerdTaskPhaseCreate,
			taskErrorText:    `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: exec: "/notexec": permission denied: unknown`,
			wantProcessError: `exec: "/notexec": permission denied`,
		},
		{
			name:             "runc 1.1 command not in PATH in create task",
			taskPhase:        containerdTaskPhaseCreate,
			taskErrorText:    `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: exec: "nosuchcommand": executable file not found in $PATH: unknown`,
			wantProcessError: `exec: "nosuchcommand": executable file not found in $PATH`,
		},
		{
			name:             "runc 1.1 exec failure reported by start task",
			taskPhase:        containerdTaskPhaseStart,
			taskErrorText:    `OCI runtime start failed: unable to start container process: exec: "/no/such/binary": stat /no/such/binary: no such file or directory: unknown`,
			wantProcessError: missingBinaryExecError,
		},
		{
			name:             "runc 1.4 missing binary in create task",
			taskPhase:        containerdTaskPhaseCreate,
			taskErrorText:    `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: error during container init: exec: "/no/such/binary": stat /no/such/binary: no such file or directory`,
			wantProcessError: missingBinaryExecError,
		},
		{
			name:             "runc 1.4 non-executable entrypoint in create task",
			taskPhase:        containerdTaskPhaseCreate,
			taskErrorText:    `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: error during container init: exec: "/notexec": permission denied`,
			wantProcessError: `exec: "/notexec": permission denied`,
		},
		{
			name:          "cgroup setup failure",
			taskPhase:     containerdTaskPhaseCreate,
			taskErrorText: `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: error during container init: error setting cgroup config for procHooks process: unable to set memory limit to 1048576 (current usage: 2097152, peak usage: 2097152): unknown`,
		},
		{
			name:          "seccomp failure",
			taskPhase:     containerdTaskPhaseCreate,
			taskErrorText: `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: error during container init: error loading seccomp filter into kernel: error loading seccomp filter: invalid argument: unknown`,
		},
		{
			name:          "mount failure during container init",
			taskPhase:     containerdTaskPhaseCreate,
			taskErrorText: `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: error during container init: error mounting "/var/lib/ccattler/secrets/x" to rootfs at "/run/secrets": stat /var/lib/ccattler/secrets/x: no such file or directory: unknown`,
		},
		{
			name:          "exec text later in a container init failure",
			taskPhase:     containerdTaskPhaseCreate,
			taskErrorText: `failed to create shim task: OCI runtime create failed: runc create failed: unable to start container process: error during container init: error running prestart hook: exec: "/usr/bin/hook": stat /usr/bin/hook: no such file or directory`,
		},
		{
			name:          "shim connection lost",
			taskPhase:     containerdTaskPhaseCreate,
			taskErrorText: `failed to create shim task: ttrpc: closed: unknown`,
		},
		{
			name:          "shim failed to start",
			taskPhase:     containerdTaskPhaseStart,
			taskErrorText: `failed to start shim: start failed: io.containerd.runc.v2: fork/exec /usr/bin/containerd-shim-runc-v2: no such file or directory: exit status 1: unknown`,
		},
		{
			name:          "image layer error",
			taskPhase:     containerdTaskPhaseCreate,
			taskErrorText: `failed to mount rootfs component: invalid argument: unknown`,
		},
		{
			name:          "network namespace gone",
			taskPhase:     containerdTaskPhaseStart,
			taskErrorText: `OCI runtime start failed: unable to start container process: error during container init: error joining network namespace /var/run/netns/x: no such file or directory: unknown`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			startError := newContainerdTaskStartError("inst1", testCase.taskPhase, errors.New(testCase.taskErrorText))
			wantProcessFailed := testCase.wantProcessError != ""
			if startError.ProcessFailed != wantProcessFailed || startError.ProcessError != testCase.wantProcessError {
				t.Errorf("ProcessFailed, ProcessError = %v, %q, want %v, %q for %q",
					startError.ProcessFailed, startError.ProcessError, wantProcessFailed, testCase.wantProcessError, testCase.taskErrorText)
			}
			wantReason := testCase.taskPhase + ": " + testCase.taskErrorText
			if startError.ID != "inst1" || startError.Reason != wantReason {
				t.Errorf("start error = {ID: %q, Reason: %q}, want {ID: %q, Reason: %q}", startError.ID, startError.Reason, "inst1", wantReason)
			}
		})
	}
}

// TestStartErrorProcessFailedOnlyForRuncExecFailure checks the classification
// against start errors that are never the workload's own: image, network and
// container creation failures (M238).
func TestStartErrorProcessFailedOnlyForRuncExecFailure(t *testing.T) {
	infrastructureErrorTexts := []string{
		"image not found: docker.io/library/nosuch:1",
		"CNI init: failed to load CNI config: no networks found",
		"CNI setup: plugin type=\"bridge\" failed (add): failed to allocate for range 0: no IP addresses available",
		"create netns: mount --make-rshared /var/run/netns: permission denied",
		"create container: container \"web-inst1\": already exists",
	}
	for _, errorText := range infrastructureErrorTexts {
		if processError, isProcessFailure := extractRuncProcessExecFailure(errors.New(errorText)); isProcessFailure {
			t.Errorf("classified %q as a workload process failure (%q)", errorText, processError)
		}
	}
}
