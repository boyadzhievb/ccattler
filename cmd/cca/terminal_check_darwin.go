// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import "golang.org/x/sys/unix"

// terminalAttributesRequest is the ioctl that reads terminal attributes; it
// succeeds only on a terminal (macOS name).
const terminalAttributesRequest = unix.TIOCGETA
