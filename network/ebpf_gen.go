// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build ignore

// This file holds the go:generate directive for the BPF program build pipeline.
// It is tagged 'ignore' because the standard build uses the compiled ELF
// embedded via go:embed in ebpf_embed.go. The ELF is produced by
// scripts/build-ebpf.sh and committed to the repository so contributors
// do not need clang or BPF headers installed.
//
// To regenerate with bpf2go as an alternative pipeline, run this command
// manually on a Linux machine with clang and BPF headers:
//
//   go generate -run CcaDataplane ./network/

package network

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags "-O2 -g -Wall -Werror" -target bpfel CcaDataplane ./ebpf/cca_dataplane.c
