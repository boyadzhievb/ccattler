#!/usr/bin/env bash
# build-ebpf.sh — Compile CCattler BPF C programs to ELF object.
# Run on a Linux machine with clang and linux-headers-$(uname -r) installed.
#
# Usage:
#   scripts/build-ebpf.sh                    # output to /usr/lib/cca/cca_ebpf.o
#   scripts/build-ebpf.sh ./cca_ebpf.o       # output to custom path
#
# The compiled ELF contains all three cgroup socket LB programs (connect4,
# sendmsg4, getpeername4) and shared BPF map definitions. The Go agent loads
# this file at runtime via ebpf.LoadCollectionSpec().

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BPF_SOURCE="${REPO_ROOT}/network/ebpf/cca_socket_lb.c"
OUTPUT_PATH="${1:-/usr/lib/cca/cca_ebpf.o}"

if [ ! -f "${BPF_SOURCE}" ]; then
    echo "error: BPF source not found: ${BPF_SOURCE}" >&2
    exit 1
fi

if ! command -v clang >/dev/null 2>&1; then
    echo "error: clang not found — install clang (apt install clang)" >&2
    exit 1
fi

# Check for BPF headers (libbpf-dev or linux-headers package).
BPF_INCLUDE=""
if [ -d "/usr/include/bpf" ]; then
    BPF_INCLUDE="/usr/include"
elif [ -d "/usr/local/include/bpf" ]; then
    BPF_INCLUDE="/usr/local/include"
else
    echo "error: BPF headers not found — install libbpf-dev (apt install libbpf-dev)" >&2
    exit 1
fi

# Create output directory if it doesn't exist.
OUTPUT_DIR="$(dirname "${OUTPUT_PATH}")"
if [ ! -d "${OUTPUT_DIR}" ]; then
    mkdir -p "${OUTPUT_DIR}"
fi

echo "Compiling BPF: ${BPF_SOURCE} -> ${OUTPUT_PATH}"
clang \
    -O2 \
    -g \
    -Wall \
    -Werror \
    -target bpf \
    -D__TARGET_ARCH_x86 \
    -I"${BPF_INCLUDE}" \
    -c "${BPF_SOURCE}" \
    -o "${OUTPUT_PATH}"

echo "BPF object compiled successfully: $(stat --printf='%s bytes' "${OUTPUT_PATH}" 2>/dev/null || wc -c < "${OUTPUT_PATH}") at ${OUTPUT_PATH}"
