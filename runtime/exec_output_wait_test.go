// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package runtime

import (
	"testing"
	"time"

	"github.com/containerd/containerd/v2/pkg/cio"
)

// blockingProcessIO is a cio.IO whose Wait blocks until Cancel is called,
// like an exec whose background child keeps stdout open.
type blockingProcessIO struct {
	cancelled chan struct{} // cancelled is closed by Cancel and releases Wait.
}

// Config returns an empty IO configuration.
func (processIO *blockingProcessIO) Config() cio.Config { return cio.Config{} }

// Cancel releases Wait.
func (processIO *blockingProcessIO) Cancel() { close(processIO.cancelled) }

// Wait blocks until Cancel is called.
func (processIO *blockingProcessIO) Wait() { <-processIO.cancelled }

// Close does nothing.
func (processIO *blockingProcessIO) Close() error { return nil }

// TestWaitForExecOutputCopyCancelsAfterGracePeriod checks that an output copy
// that never finishes is cancelled after execOutputCopyGracePeriod.
func TestWaitForExecOutputCopyCancelsAfterGracePeriod(t *testing.T) {
	processIO := &blockingProcessIO{cancelled: make(chan struct{})}
	startTime := time.Now()
	waitForExecOutputCopy(processIO)
	elapsed := time.Since(startTime)
	if elapsed < execOutputCopyGracePeriod || elapsed > execOutputCopyGracePeriod+time.Second {
		t.Fatalf("waited %v, want about %v", elapsed, execOutputCopyGracePeriod)
	}
	select {
	case <-processIO.cancelled:
	default:
		t.Fatal("output copy was not cancelled")
	}
}
