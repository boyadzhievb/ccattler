// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package agent

import "syscall"

// tmpfsFilesystemMagic and ramfsFilesystemMagic are the statfs f_type values
// of memory-backed filesystems (linux/magic.h).
const (
	tmpfsFilesystemMagic = 0x01021994
	ramfsFilesystemMagic = 0x858458f6
)

// isMemoryFilesystem reports whether directory is on tmpfs or ramfs.
func isMemoryFilesystem(directory string) (bool, error) {
	var filesystemStats syscall.Statfs_t
	if statError := syscall.Statfs(directory, &filesystemStats); statError != nil {
		return false, statError
	}
	filesystemType := int64(filesystemStats.Type) //nolint:unconvert // Statfs_t.Type width differs by architecture
	return filesystemType == tmpfsFilesystemMagic || filesystemType == ramfsFilesystemMagic, nil
}
