#!/usr/bin/env sh
# Install the CCattler CLI binary.
#
#   curl -sSL https://github.com/boyadzhievb/ccattler/releases/latest/download/install.sh | sh
#
# Detects OS and architecture, downloads the correct binary from GitHub
# Releases, and installs it to /usr/local/bin/. That's it — no Ansible,
# no deployment, no cluster setup.
#
# Works with dash (Debian/Ubuntu), bash (RHEL/macOS), zsh, and any
# POSIX-compliant shell.
#
# Environment variables:
#   CCATTLER_VERSION   Pin a specific release (e.g. v1.92.0-beta). Default: latest.
#                      Set it for the shell that runs the script: curl ... | CCATTLER_VERSION=... sh
#   INSTALL_PATH       Override install directory. Default: /usr/local/bin

set -eu

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
    # github.com/.../releases/latest redirects to .../releases/tag/<version>. Reading the
    # redirect avoids the GitHub API, which allows 60 unauthenticated requests per hour per IP.
    latest_release_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
        "https://github.com/${GITHUB_REPO}/releases/latest" || true)
    case "$latest_release_url" in
        */releases/tag/*) version="${latest_release_url##*/releases/tag/}" ;;
        *) version="" ;;
    esac
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

checksum_file="checksums.txt"
curl -fsSL "${release_url}/${archive_name}" -o "${temp_dir}/${archive_name}"
curl -fsSL "${release_url}/${checksum_file}" -o "${temp_dir}/${checksum_file}"

log_info "Verifying checksum..."
expected_checksum=$(grep "${archive_name}" "${temp_dir}/${checksum_file}" | awk '{print $1}')
if [ -z "$expected_checksum" ]; then
    log_error "Checksum entry not found for ${archive_name} in ${checksum_file}"
    exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
    actual_checksum=$(sha256sum "${temp_dir}/${archive_name}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    actual_checksum=$(shasum -a 256 "${temp_dir}/${archive_name}" | awk '{print $1}')
else
    log_error "No sha256sum or shasum found — cannot verify archive integrity"
    exit 1
fi
if [ "$actual_checksum" != "$expected_checksum" ]; then
    log_error "Checksum mismatch!"
    log_error "  Expected: $expected_checksum"
    log_error "  Actual:   $actual_checksum"
    exit 1
fi
log_ok "Checksum verified"

tar -xzf "${temp_dir}/${archive_name}" -C "$temp_dir"

# --- Install binary ---
if [ ! -d "$INSTALL_PATH" ]; then
    if mkdir -p "$INSTALL_PATH" 2>/dev/null; then
        :
    else
        log_info "Creating ${INSTALL_PATH} requires elevated permissions."
        sudo mkdir -p "$INSTALL_PATH"
    fi
fi

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

# --- Post-install: report which cluster-host prerequisites are missing ---
INSTALL_GUIDE_URL="https://ccattler.org/installation"
if [ "$operating_system" = "linux" ]; then
    log_info "Checking cluster-host prerequisites..."
    # host_has_program also looks in the sbin directories, which are not in a
    # normal user's PATH on Debian (nft lives in /usr/sbin).
    host_has_program() {
        command -v "$1" >/dev/null 2>&1 || [ -x "/usr/sbin/$1" ] || [ -x "/sbin/$1" ]
    }
    missing_prerequisites=""
    host_has_program containerd || missing_prerequisites="$missing_prerequisites containerd"
    host_has_program nft || missing_prerequisites="$missing_prerequisites nftables"
    [ -x /opt/cni/bin/bridge ] || missing_prerequisites="$missing_prerequisites cni-plugins"
    host_has_program etcd || missing_prerequisites="$missing_prerequisites etcd(control-plane-only)"
    if [ -z "$missing_prerequisites" ]; then
        log_ok "containerd, nftables, CNI plugins and etcd found"
    else
        log_warn "Missing for a cluster host:${missing_prerequisites}"
        log_warn "Install them as described in 'Prepare a fresh Debian or Ubuntu host':"
        log_warn "  ${INSTALL_GUIDE_URL}#prepare-a-fresh-debian-or-ubuntu-host"
        log_warn "Simulation ('cca demo', 'cca apply') works without them."
    fi
fi

# --- Next steps ---
echo ""
log_info "Next steps:"
log_info "  Try it (simulation, nothing starts):  cca demo"
if [ "$operating_system" = "linux" ]; then
    log_info ""
    log_info "  Set up a control-plane host (runs etcd, server and agent):"
    log_info "    1. cca init --advertise-address <this-host-ip> --node-id <name> -o ~/cca-init"
    log_info "    2. Review, then run:  sudo ~/cca-init/setup.sh"
    log_info "    3. sudo systemctl enable --now cca-server cca-agent"
    log_info "    4. sudo cca status"
    log_info ""
    log_info "  Add worker nodes and use the CLI without sudo:"
    log_info "    ${INSTALL_GUIDE_URL}#set-up-a-cluster-by-hand"
fi
