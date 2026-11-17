// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// previousRunLogSuffix is appended to an instance's run log name for the log
// of the run before the current one (M234, spec instance-logs).
const previousRunLogSuffix = ".previous"

// initContainerNamePrefix starts the name (and log directory) of every init
// step container; such directories belong to a running init step, never to an
// instance, and the instance data sweep leaves them alone.
const initContainerNamePrefix = "cca-init-"

// ErrPreviousLogNotAvailable is returned by runtimes that do not keep the log
// of an instance's previous run.
var ErrPreviousLogNotAvailable = errors.New("previous log not available on this runtime")

// InstanceIDPattern matches every instance ID: hex IDs, "<name>-<N>" stateful
// and job IDs from DSL names (Unicode letters, digits, "_", "-", ".", ":"),
// "cca-init-N". It starts with a letter, digit or "_", so "." and ".." cannot
// match, and has no "/", whitespace or control characters. Paths from an ID are
// guarded by its MatchString, a sanitizer CodeQL credits (M238 D7, B4).
var InstanceIDPattern = regexp.MustCompile(`^[\p{L}\p{N}_][\p{L}\p{N}_.:-]*$`)

// instanceRunLogPath is the log file of an instance's current run:
// <base>/<id>/<id>-json.log.
func instanceRunLogPath(logBaseDirectory string, instanceID string) string {
	return filepath.Join(logBaseDirectory, instanceID, instanceID+"-json.log")
}

// rotateInstanceRunLog moves the current run's log to the previous-run log,
// replacing an older one, so the next run starts with an empty log. No log yet
// (first start) or an empty one (a start that failed after creating the task)
// is left alone, so it never replaces the run that exited.
func rotateInstanceRunLog(logBaseDirectory string, instanceID string) error {
	currentLogPath := instanceRunLogPath(logBaseDirectory, instanceID)
	currentLogInfo, statError := os.Stat(currentLogPath)
	if errors.Is(statError, os.ErrNotExist) || (statError == nil && currentLogInfo.Size() == 0) {
		return nil
	}
	if statError != nil {
		return fmt.Errorf("keep previous log of %s: %w", instanceID, statError)
	}
	if renameError := os.Rename(currentLogPath, currentLogPath+previousRunLogSuffix); renameError != nil {
		return fmt.Errorf("keep previous log of %s: %w", instanceID, renameError)
	}
	return nil
}

// openPreviousRunLog opens the log of an instance's previous run; of the
// options only tail applies (as for the current log's static read). Following
// is refused: a finished run does not grow. instanceID must match InstanceIDPattern.
func openPreviousRunLog(logBaseDirectory string, instanceID string, options LogsOptions) (io.ReadCloser, error) {
	if !InstanceIDPattern.MatchString(instanceID) {
		return nil, fmt.Errorf("invalid instance ID %q", instanceID)
	}
	if options.Follow {
		return nil, fmt.Errorf("cannot follow the previous run of %s: it has finished", instanceID)
	}
	previousLogPath := instanceRunLogPath(logBaseDirectory, instanceID) + previousRunLogSuffix
	if _, statError := os.Stat(previousLogPath); statError != nil {
		if errors.Is(statError, os.ErrNotExist) {
			return nil, fmt.Errorf("instance %s has no previous run on this node", instanceID)
		}
		return nil, fmt.Errorf("previous log of %s: %w", instanceID, statError)
	}
	return newStaticLogReader(previousLogPath, options)
}

// removeInstanceLogDirectory deletes an instance's log directory (current and
// previous run, config files). The instance ID must match InstanceIDPattern,
// so the removal cannot leave logBaseDirectory.
func removeInstanceLogDirectory(logBaseDirectory string, instanceID string) error {
	if !InstanceIDPattern.MatchString(instanceID) {
		return fmt.Errorf("refusing to remove logs for instance ID %q", instanceID)
	}
	return os.RemoveAll(filepath.Join(logBaseDirectory, instanceID))
}

// listInstanceLogDirectories returns the instance IDs that have a log
// directory under logBaseDirectory, skipping init step directories and any
// name that does not match InstanceIDPattern. A missing base is no directories.
func listInstanceLogDirectories(logBaseDirectory string) ([]string, error) {
	directoryEntries, readError := os.ReadDir(logBaseDirectory)
	if errors.Is(readError, os.ErrNotExist) {
		return nil, nil
	}
	if readError != nil {
		return nil, fmt.Errorf("list instance log directories: %w", readError)
	}
	var instanceIDs []string
	for _, directoryEntry := range directoryEntries {
		entryName := directoryEntry.Name()
		if directoryEntry.IsDir() && InstanceIDPattern.MatchString(entryName) && !strings.HasPrefix(entryName, initContainerNamePrefix) {
			instanceIDs = append(instanceIDs, entryName)
		}
	}
	return instanceIDs, nil
}
