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
log_warn()  { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
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
        case "$dependency" in
            curl)
                log_error "Install curl:"
                log_error "  Ubuntu/Debian: sudo apt install curl"
                log_error "  macOS:         brew install curl"
                ;;
            tar)
                log_error "Install tar:"
                log_error "  Ubuntu/Debian: sudo apt install tar"
                log_error "  macOS: tar is included by default"
                ;;
        esac
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
echo ""

# --- Post-install: check container runtime ---
log_info "Checking container runtime availability..."
if command -v nerdctl >/dev/null 2>&1; then
    log_ok "nerdctl found — 'cca run-container' will use nerdctl"
elif command -v docker >/dev/null 2>&1; then
    log_ok "docker found — 'cca run-container' will use docker"
elif command -v lima >/dev/null 2>&1; then
    log_ok "lima found — 'cca run-container' will use lima nerdctl"
    log_info "Make sure a Lima VM is running: limactl start"
else
    log_warn "No container runtime found (nerdctl, docker, or lima)."
    log_warn "'cca apply' (simulation) and 'cca run' (local processes) still work."
    log_warn ""
    log_warn "To run real containers, install one of:"
    if [ "$operating_system" = "darwin" ]; then
        log_warn "  Docker Desktop: https://www.docker.com/products/docker-desktop/"
        log_warn "  Lima + nerdctl: brew install lima && limactl start"
    else
        log_warn "  Docker:   curl -fsSL https://get.docker.com | sh"
        log_warn "  nerdctl:  https://github.com/containerd/nerdctl/releases"
    fi
fi
