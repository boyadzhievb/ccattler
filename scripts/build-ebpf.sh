#!/usr/bin/env bash
# build-ebpf.sh — Compile CCattler unified BPF data plane to a single ELF.
# Uses Docker for cross-compilation — works on macOS and Linux.
#
# Usage:
#   scripts/build-ebpf.sh           # output to network/ebpf/cca_dataplane.o
#
# The compiled ELF is embedded in the Go binary via go:embed (ebpf_embed.go).
# Commit the .o file after compilation so cross-builds from macOS work.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

BPF_IMAGE="cca-bpf-builder"
BPF_DOCKERFILE="${SCRIPT_DIR}/Dockerfile.bpf"
OUTPUT_FILE="${REPO_ROOT}/network/ebpf/cca_dataplane.o"

if ! command -v docker >/dev/null 2>&1; then
    echo "error: docker not found" >&2
    exit 1
fi

echo "Building BPF compiler image..."
docker build -q -t "${BPF_IMAGE}" -f "${BPF_DOCKERFILE}" "${REPO_ROOT}"

echo "Compiling unified BPF data plane..."
docker run --rm --platform linux/amd64 \
    -v "${REPO_ROOT}/network/ebpf:/src" \
    "${BPF_IMAGE}" \
    -c /src/cca_dataplane.c -o /src/cca_dataplane.o

echo "  -> ${OUTPUT_FILE} ($(wc -c < "${OUTPUT_FILE}" | tr -d ' ') bytes)"
echo "Done. Commit this file to the repository."
