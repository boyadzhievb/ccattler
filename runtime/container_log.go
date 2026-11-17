// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// previousRunLogSuffix is appended to an instance's run log name for the log
// of the run before the current one (M234, spec instance-logs).
const previousRunLogSuffix = ".previous"

// ErrPreviousLogNotAvailable is returned by runtimes that do not keep the log
// of an instance's previous run.
var ErrPreviousLogNotAvailable = errors.New("previous log not available on this runtime")

// instanceRunLogPath is the log file of an instance's current run:
// <base>/<id>/<id>-json.log.
func instanceRunLogPath(logBaseDirectory string, instanceID string) string {
	return filepath.Join(logBaseDirectory, instanceID, instanceID+"-json.log")
}

// rotateInstanceRunLog moves the current run's log to the previous-run log,
// replacing an older one, so the next run starts with an empty log. No log
// yet (first start) is not an error.
func rotateInstanceRunLog(logBaseDirectory string, instanceID string) error {
	currentLogPath := instanceRunLogPath(logBaseDirectory, instanceID)
	renameError := os.Rename(currentLogPath, currentLogPath+previousRunLogSuffix)
	if renameError != nil && !errors.Is(renameError, os.ErrNotExist) {
		return fmt.Errorf("keep previous log of %s: %w", instanceID, renameError)
	}
	return nil
}

// openPreviousRunLog opens the log of an instance's previous run with the
// tail, since and timestamp options. Following is refused: a finished run
// does not grow.
func openPreviousRunLog(logBaseDirectory string, instanceID string, options LogsOptions) (io.ReadCloser, error) {
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
// previous run, config files). The instance ID must be a plain name, so the
// removal cannot leave logBaseDirectory.
func removeInstanceLogDirectory(logBaseDirectory string, instanceID string) error {
	if instanceID == "" || instanceID == "." || instanceID == ".." || filepath.Base(instanceID) != instanceID {
		return fmt.Errorf("refusing to remove logs for instance ID %q", instanceID)
	}
	return os.RemoveAll(filepath.Join(logBaseDirectory, instanceID))
}
