// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// secretMountBinding is one validated read-only secret bind mount.
type secretMountBinding struct {
	hostPath      string // hostPath is the tmpfs file prepared by the agent.
	containerPath string // containerPath is where the file appears in the container.
}

// sortedSecretMountBindings validates secretMounts and returns them sorted by
// container path. Both paths must be absolute and clean, and the host path must
// not contain ':' (nerdctl -v syntax), so a bad fact cannot mount another path.
func sortedSecretMountBindings(secretMounts map[string]string) ([]secretMountBinding, error) {
	bindings := make([]secretMountBinding, 0, len(secretMounts))
	for containerPath, hostPath := range secretMounts {
		for _, mountPath := range []string{containerPath, hostPath} {
			if !filepath.IsAbs(mountPath) || filepath.Clean(mountPath) != mountPath || strings.Contains(mountPath, ":") {
				return nil, fmt.Errorf("invalid secret mount path %q", mountPath)
			}
		}
		bindings = append(bindings, secretMountBinding{hostPath: hostPath, containerPath: containerPath})
	}
	sort.Slice(bindings, func(leftIndex, rightIndex int) bool {
		return bindings[leftIndex].containerPath < bindings[rightIndex].containerPath
	})
	return bindings, nil
}

// buildSecretMountArguments returns nerdctl "-v host:container:ro" arguments
// for the secret mounts.
func buildSecretMountArguments(secretMounts map[string]string) ([]string, error) {
	bindings, validationError := sortedSecretMountBindings(secretMounts)
	if validationError != nil {
		return nil, validationError
	}
	arguments := make([]string, 0, 2*len(bindings))
	for _, binding := range bindings {
		arguments = append(arguments, "-v", binding.hostPath+":"+binding.containerPath+":ro")
	}
	return arguments, nil
}
