// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

//go:build ignore

// This file holds the go:generate directive for the BPF program build pipeline.
// It is tagged 'ignore' because the standard build uses Rust/aya-ebpf compiled
// by scripts/build-ebpf.sh, not bpf2go. The compiled ELF objects (.o files)
// are committed to the repository so contributors do not need a Rust toolchain.
//
// To regenerate with bpf2go as an alternative to the Rust pipeline, run
// this command manually on a Linux machine with clang and BPF headers:
//
//   go generate -run CcaEbpf ./network/

package network

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags "-O2 -g -Wall -Werror" -target bpfel CcaEbpf ./ebpf/cca_socket_lb.c
