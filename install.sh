#!/usr/bin/env bash
# Install the CCattler CLI binary.
#
#   curl -sSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | bash
#
# Detects OS and architecture, downloads the correct binary from GitHub
# Releases, and installs it to /usr/local/bin/. That's it — no Ansible,
# no deployment, no cluster setup.
#
# Environment variables:
#   CCATTLER_VERSION   Pin a specific release (e.g. v0.38.0). Default: latest.
#   INSTALL_PATH       Override install directory. Default: /usr/local/bin

set -euo pipefail

GITHUB_REPO="boyadzhievb/ccattler"
INSTALL_PATH="${INSTALL_PATH:-/usr/local/bin}"

log_info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
log_ok()    { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
log_error() { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; }

# --- Detect OS ---
detect_os() {
    case "$(uname -s)" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *)
            log_error "Unsupported OS: $(uname -s). CCattler supports Linux and macOS."
            exit 1
            ;;
    esac
}

# --- Detect architecture ---
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  echo "amd64" ;;
        arm64|aarch64) echo "arm64" ;;
        *)
            log_error "Unsupported architecture: $(uname -m). CCattler supports amd64 and arm64."
            exit 1
            ;;
    esac
}

# --- Check dependencies ---
for dependency in curl tar; do
    if ! command -v "$dependency" >/dev/null; then
        log_error "Missing required tool: $dependency"
        exit 1
    fi
done

# --- Resolve version ---
if [ -n "${CCATTLER_VERSION:-}" ]; then
    version="$CCATTLER_VERSION"
else
    log_info "Detecting latest release..."
    version=$(curl -fsSL "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    if [ -z "$version" ]; then
        log_error "Could not detect latest release. Set CCATTLER_VERSION manually."
        exit 1
    fi
fi

operating_system=$(detect_os)
architecture=$(detect_arch)

release_url="https://github.com/${GITHUB_REPO}/releases/download/${version}"
archive_name="cca-${version}-${operating_system}-${architecture}.tar.gz"

log_info "Installing CCattler $version (${operating_system}/${architecture})"

# --- Download and extract ---
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

curl -fsSL "${release_url}/${archive_name}" -o "${temp_dir}/${archive_name}"
tar -xzf "${temp_dir}/${archive_name}" -C "$temp_dir"

# --- Install binary ---
if [ -w "$INSTALL_PATH" ]; then
    mv "${temp_dir}/cca" "${INSTALL_PATH}/cca"
else
    log_info "Writing to ${INSTALL_PATH} requires elevated permissions."
    sudo mv "${temp_dir}/cca" "${INSTALL_PATH}/cca"
fi
chmod +x "${INSTALL_PATH}/cca"

log_ok "Installed cca to ${INSTALL_PATH}/cca"
log_info "Run 'cca version' to verify."
