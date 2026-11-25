#!/usr/bin/env sh
# Install the CCattler CLI: download the release for this OS and CPU, check its
# SHA-256, and install it owned by root into /usr/local/bin. Nothing else.
#
#   curl -fsSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | sh
#
# Environment: CCATTLER_VERSION (default: the latest release), INSTALL_PATH (default: /usr/local/bin).
# Cluster prerequisites (containerd, nftables, CNI, etcd) are in the guide, not checked here.
set -eu

GITHUB_REPO="boyadzhievb/ccattler"
INSTALL_PATH="${INSTALL_PATH:-/usr/local/bin}"
GUIDE_URL="https://ccattler.org/installation"

ohai()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
abort() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

# run_as_root prints a command, then runs it, through sudo unless already root.
run_as_root() {
    ohai "$*"
    if [ "$(id -u)" -eq 0 ]; then "$@"; else sudo "$@"; fi
}

# --- Platform: one case per question; the RHEL family differs only in two lines below ---
case "$(uname -s)" in
    Linux)  operating_system=linux ;;
    Darwin) operating_system=darwin ;;
    *) abort "Unsupported OS $(uname -s): CCattler runs on Linux and macOS." ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  architecture=amd64 ;;
    aarch64|arm64) architecture=arm64 ;;
    *) abort "Unsupported CPU $(uname -m): CCattler runs on amd64 and arm64." ;;
esac
family=other
if [ "$operating_system" = linux ] && [ -r /etc/os-release ]; then
    # shellcheck source=/dev/null
    os_ids=$(. /etc/os-release && printf ' %s %s ' "${ID:-}" "${ID_LIKE:-}")
    case "$os_ids" in
        *" debian "*|*" ubuntu "*)            family=debian ;;
        *" rhel "*|*" fedora "*|*" centos "*) family=rhel ;;
    esac
fi
for tool in curl tar; do
    command -v "$tool" >/dev/null 2>&1 || abort "$tool is required; install it first (${GUIDE_URL}#prepare-a-fresh-host)."
done

# --- Version: CCATTLER_VERSION, else the tag the releases/latest redirect points to ---
version="${CCATTLER_VERSION:-}"
if [ -z "$version" ]; then
    latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${GITHUB_REPO}/releases/latest" || true)
    case "$latest_url" in
        */releases/tag/*) version="${latest_url##*/releases/tag/}" ;;
        *) abort "Could not find the latest release; set CCATTLER_VERSION." ;;
    esac
fi

# --- Download and verify: a mismatch installs nothing ---
archive="cca-${version}-${operating_system}-${architecture}.tar.gz"
release_url="https://github.com/${GITHUB_REPO}/releases/download/${version}"
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
ohai "Downloading CCattler ${version} (${operating_system}/${architecture})"
curl -fsSL -o "${work_dir}/${archive}" "${release_url}/${archive}"
curl -fsSL -o "${work_dir}/checksums.txt" "${release_url}/checksums.txt"
expected=$(awk -v name="$archive" '$2 == name { print $1 }' "${work_dir}/checksums.txt")
[ -n "$expected" ] || abort "No checksum for ${archive} in checksums.txt; nothing was installed."
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${work_dir}/${archive}" | awk '{ print $1 }')
else
    actual=$(shasum -a 256 "${work_dir}/${archive}" | awk '{ print $1 }')
fi
[ "$actual" = "$expected" ] || abort "Checksum mismatch for ${archive}; nothing was installed."
tar -xzf "${work_dir}/${archive}" -C "$work_dir" cca

# --- Install: owned by root, because systemd and sudo run cca as root ---
[ -d "$INSTALL_PATH" ] || run_as_root install -d -m 0755 "$INSTALL_PATH"
run_as_root install -o 0 -g 0 -m 0755 "${work_dir}/cca" "${INSTALL_PATH}/cca"
if [ "$family" = rhel ] && command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then
    run_as_root restorecon "${INSTALL_PATH}/cca"   # the SELinux label systemd needs to run it
fi

# --- Next steps: fixed text ---
ohai "Installed ${INSTALL_PATH}/cca"
echo "Next steps (${GUIDE_URL}):"
echo "  Try it, nothing starts:      cca demo"
echo "  Set up a cluster host:       ${GUIDE_URL}#set-up-a-cluster-by-hand"
if [ "$family" = rhel ]; then
    echo "  On RHEL, AlmaLinux and Rocky, sudo does not search /usr/local/bin: run 'sudo /usr/local/bin/cca ...',"
    echo "  or add it to sudo's secure_path as the guide shows (${GUIDE_URL}#prepare-a-fresh-host)."
fi
