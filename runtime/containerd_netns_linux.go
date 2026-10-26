// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"

	"golang.org/x/sys/unix"
)

// createNetworkNamespace creates an isolated network namespace at
// /var/run/netns/cca-{id} using unshare + bind-mount. Returns the path.
func createNetworkNamespace(instanceID string) (string, error) {
	netnsPath := filepath.Join(netnsBaseDirectory, "cca-"+instanceID)

	if mkdirError := os.MkdirAll(netnsBaseDirectory, 0750); mkdirError != nil {
		return "", fmt.Errorf("create netns dir: %w", mkdirError)
	}

	netnsFile, createError := os.Create(netnsPath) // #nosec G304 — path is constructed from constant base + sanitized instance ID
	if createError != nil {
		return "", fmt.Errorf("create netns file: %w", createError)
	}
	_ = netnsFile.Close()

	errChan := make(chan error, 1)
	go func() {
		// LockOSThread without UnlockOSThread — the thread has been moved into
		// a new network namespace and must be terminated, not returned to the pool.
		goruntime.LockOSThread()

		if unshareError := unix.Unshare(unix.CLONE_NEWNET); unshareError != nil {
			errChan <- fmt.Errorf("unshare CLONE_NEWNET: %w", unshareError)
			return
		}

		threadNetnsPath := fmt.Sprintf("/proc/%d/task/%d/ns/net", os.Getpid(), unix.Gettid())
		if mountError := unix.Mount(threadNetnsPath, netnsPath, "", unix.MS_BIND, ""); mountError != nil {
			errChan <- fmt.Errorf("bind mount netns: %w", mountError)
			return
		}
		errChan <- nil
	}()

	if bindError := <-errChan; bindError != nil {
		_ = os.Remove(netnsPath)
		return "", bindError
	}
	return netnsPath, nil
}

// deleteNetworkNamespace unmounts and removes a named network namespace.
func deleteNetworkNamespace(instanceID string) {
	netnsPath := filepath.Join(netnsBaseDirectory, "cca-"+instanceID)
	_ = unix.Unmount(netnsPath, unix.MNT_DETACH)
	_ = os.Remove(netnsPath)
}
