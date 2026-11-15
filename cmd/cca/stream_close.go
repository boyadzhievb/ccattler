// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// stream_close.go recognizes the close frame the API server sends before it
// ends a stream whose credentials or access changed (M216), so cca watch,
// cca events --follow and cca audit --follow say why and exit non-zero.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// sseCloseFrameEvent is the event line of the server's close frame.
	sseCloseFrameEvent = "event: closed"
	// sseMaxLineBytes is the longest SSE line the CLI reads (a fact value
	// such as a config file can be large).
	sseMaxLineBytes = 4 << 20
	// sseInitialLineBufferBytes is the scanner's starting buffer; it grows up
	// to sseMaxLineBytes.
	sseInitialLineBufferBytes = 64 << 10
)

// streamCloseTracker follows SSE lines and remembers the close frame's reason.
type streamCloseTracker struct {
	closeFramePending bool   // closeFramePending is set after the close event line.
	closeReason       string // closeReason is the reason the server sent, "" if none.
}

// newSSELineScanner reads SSE lines up to sseMaxLineBytes long.
func newSSELineScanner(streamBody io.Reader) *bufio.Scanner {
	lineScanner := bufio.NewScanner(streamBody)
	lineScanner.Buffer(make([]byte, 0, sseInitialLineBufferBytes), sseMaxLineBytes)
	return lineScanner
}

// observe reports whether line belongs to the close frame; such lines are not
// events and must not be printed.
func (tracker *streamCloseTracker) observe(line string) bool {
	if line == sseCloseFrameEvent {
		tracker.closeFramePending = true
		return true
	}
	if !tracker.closeFramePending || !strings.HasPrefix(line, "data: ") {
		return false
	}
	tracker.closeFramePending = false
	var closeFrame struct {
		Reason string `json:"reason"` // Reason is the generic close reason.
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &closeFrame) != nil || closeFrame.Reason == "" {
		closeFrame.Reason = "closed by the server"
	}
	tracker.closeReason = closeFrame.Reason
	return true
}

// exitIfClosedByServer prints the server's reason and exits 1 when the server
// ended the stream; a reconnect shows whether access is still there.
func (tracker *streamCloseTracker) exitIfClosedByServer() {
	if tracker.closeReason == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "stream ended by the server: %s\n", tracker.closeReason)
	os.Exit(1)
}
