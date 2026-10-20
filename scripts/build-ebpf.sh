#!/usr/bin/env bash
# build-ebpf.sh — Compile CCattler BPF C programs to ELF objects.
# Run on a Linux machine with clang and linux-headers-$(uname -r) installed.
#
# Usage:
#   scripts/build-ebpf.sh                    # output to /usr/lib/cca/
#   scripts/build-ebpf.sh <output_dir>       # output to custom directory
#
# Compiles three ELF files:
#   cca_ebpf.o      — cgroup socket LB programs (connect4, sendmsg4, getpeername4)
#   cca_tc_dnat.o   — tc ingress DNAT for container bridge traffic
#   cca_tc_snat.o   — tc egress SNAT for outbound container traffic

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

SOCKET_LB_SOURCE="${REPO_ROOT}/network/ebpf/cca_socket_lb.c"
TC_DNAT_SOURCE="${REPO_ROOT}/network/ebpf/cca_tc_dnat.c"
TC_SNAT_SOURCE="${REPO_ROOT}/network/ebpf/cca_tc_snat.c"
OUTPUT_DIR="${1:-/usr/lib/cca}"

if [ ! -f "${SOCKET_LB_SOURCE}" ]; then
    echo "error: socket LB source not found: ${SOCKET_LB_SOURCE}" >&2
    exit 1
fi

if [ ! -f "${TC_DNAT_SOURCE}" ]; then
    echo "error: tc DNAT source not found: ${TC_DNAT_SOURCE}" >&2
    exit 1
fi

if [ ! -f "${TC_SNAT_SOURCE}" ]; then
    echo "error: tc SNAT source not found: ${TC_SNAT_SOURCE}" >&2
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

if [ ! -d "${OUTPUT_DIR}" ]; then
    mkdir -p "${OUTPUT_DIR}"
fi

BPF_CFLAGS="-O2 -g -Wall -Werror -target bpf -D__TARGET_ARCH_x86"
BPF_INCLUDES="-I${BPF_INCLUDE} -I/usr/include/x86_64-linux-gnu"

echo "Compiling socket LB: ${SOCKET_LB_SOURCE}"
clang ${BPF_CFLAGS} ${BPF_INCLUDES} -c "${SOCKET_LB_SOURCE}" -o "${OUTPUT_DIR}/cca_ebpf.o"
echo "  -> ${OUTPUT_DIR}/cca_ebpf.o"

echo "Compiling tc DNAT: ${TC_DNAT_SOURCE}"
clang ${BPF_CFLAGS} ${BPF_INCLUDES} -c "${TC_DNAT_SOURCE}" -o "${OUTPUT_DIR}/cca_tc_dnat.o"
echo "  -> ${OUTPUT_DIR}/cca_tc_dnat.o"

echo "Compiling tc SNAT: ${TC_SNAT_SOURCE}"
clang ${BPF_CFLAGS} ${BPF_INCLUDES} -c "${TC_SNAT_SOURCE}" -o "${OUTPUT_DIR}/cca_tc_snat.o"
echo "  -> ${OUTPUT_DIR}/cca_tc_snat.o"

echo "BPF compilation complete: 3 ELF objects in ${OUTPUT_DIR}"
