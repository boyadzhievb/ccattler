// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux

package agent

import "errors"

// isMemoryFilesystem cannot verify the filesystem type outside Linux, so a
// store that requires memory backing refuses to write.
func isMemoryFilesystem(_ string) (bool, error) {
	return false, errors.New("memory filesystem check is only supported on linux")
}
