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

# shell_quoted prints $1 in single quotes, so a printed path stays one word when pasted.
shell_quoted() {
    printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

# --- Install binary ---
# requested_install_path keeps the value as given, for messages and the secure_path check.
requested_install_path=$INSTALL_PATH
line_break='
'
case "$requested_install_path" in
    - | *"$line_break"*)
        # cd would read '-' as the previous directory; a newline breaks every printed path.
        log_error "Refusing the install directory $(shell_quoted "$requested_install_path"): a lone dash and newlines are not allowed."
        exit 1
        ;;
esac
if [ ! -d "$requested_install_path" ]; then
    if mkdir -p -- "$requested_install_path" 2>/dev/null; then
        :
    else
        log_info "Creating ${requested_install_path} requires elevated permissions."
        sudo mkdir -p -- "$requested_install_path"
    fi
fi

# Normalize once: absolute, no trailing or double slash, symlinks resolved, so every
# check and printed path below uses the directory's one real name.
# The trailing x keeps $(...) from stripping newlines that belong to the resolved name.
if ! resolved_with_sentinel=$(CDPATH='' cd -P -- "$requested_install_path" && pwd -P && echo x); then
    log_error "Cannot enter the install directory $(shell_quoted "$requested_install_path")."
    exit 1
fi
INSTALL_PATH=${resolved_with_sentinel%x}
INSTALL_PATH=${INSTALL_PATH%"$line_break"}
case "$INSTALL_PATH" in
    *"$line_break"*)
        log_error "Refusing the install directory $(shell_quoted "$requested_install_path"): it resolves to a name with a newline."
        exit 1
        ;;
esac
installed_binary_quoted=$(shell_quoted "${INSTALL_PATH}/cca")

# run_as_installer runs a command with sudo when the install directory needed it.
use_sudo="no"
run_as_installer() {
    if [ "$use_sudo" = "yes" ]; then
        sudo "$@"
    else
        "$@"
    fi
}

if [ -w "$INSTALL_PATH" ]; then
    install -m 0755 "${temp_dir}/cca" "${INSTALL_PATH}/cca"
else
    log_info "Writing to ${INSTALL_PATH} requires elevated permissions."
    use_sudo="yes"
    # root:root, so the installing user cannot replace what systemd and sudo run as root.
    sudo install -o 0 -g 0 -m 0755 "${temp_dir}/cca" "${INSTALL_PATH}/cca"
fi

# A binary created from a temp download may not carry the policy label for its
# path (the old mv kept user_tmp_t on Rocky 9, which systemd cannot execute);
# restorecon applies the label the policy defines for the install path.
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled \
    && command -v restorecon >/dev/null 2>&1; then
    if ! run_as_installer restorecon "${INSTALL_PATH}/cca"; then
        log_warn "Could not restore the SELinux label of ${INSTALL_PATH}/cca; systemd cannot start it until you run:"
        log_warn "  sudo restorecon ${installed_binary_quoted}"
    fi
fi

log_ok "Installed cca to ${INSTALL_PATH}/cca"

# --- Ownership (Linux): systemd and sudo run cca as root, so only root may be able to change it ---
# find_first_unsafe_path sets first_unsafe_path to the first of $1 and its parents up to /
# that is not root-owned or that others can write; lstat, so a symlink is judged itself.
# Write bits count for files and directories (a sticky directory's only when everything
# below it on the path is root-owned, which the walk has checked); a symlink's never do.
# Nothing privileged is printed (review round 5): detection only adds or misses a warning.
find_first_unsafe_path() {
    first_unsafe_path=""
    checked_path=$1
    while :; do
        owner_mode_type=$(stat -c '%u %a %F' -- "$checked_path" 2>/dev/null) || owner_mode_type="unknown 777 unknown"
        mode_and_type=${owner_mode_type#* }
        checked_mode=${mode_and_type%% *}
        case "${mode_and_type#* }" in
            "symbolic link") others_can_write=no ;;
            directory) others_can_write=$(( (0${checked_mode} & 022) != 0 && (0${checked_mode} & 01000) == 0 )) ;;
            *) others_can_write=$(( (0${checked_mode} & 022) != 0 )) ;;
        esac
        if [ "${owner_mode_type%% *}" != "0" ] || [ "$others_can_write" = "1" ]; then
            first_unsafe_path=$checked_path
            return 0
        fi
        if [ "$checked_path" = "/" ]; then
            return 0
        fi
        checked_path=${checked_path%/*}
        [ -n "$checked_path" ] || checked_path=/
    done
}

if [ "$operating_system" = "linux" ]; then
    find_first_unsafe_path "${INSTALL_PATH}/cca"
    if [ -n "$first_unsafe_path" ]; then
        log_warn "$(shell_quoted "$first_unsafe_path") can be changed by someone other than root, and whoever can change it"
        log_warn "can change what systemd and sudo run as root. Use a directory writable only by root along its whole path,"
        log_warn "such as /usr/local/bin: https://ccattler.org/installation#quick-install"
    fi
fi

# --- sudo's secure_path (Linux): 'sudo cca' fails when INSTALL_PATH is not on it ---
# No sudoers text is printed: the guide has the one fixed snippet (review round 3).
SECURE_PATH_GUIDE_URL="https://ccattler.org/installation#prepare-a-fresh-host"
# sudo_cca is how the next steps call cca with sudo: the quoted full path after the warning.
sudo_cca="cca"
# given_install_spelling is INSTALL_PATH as given, without trailing slashes.
given_install_spelling=$requested_install_path
while [ "$given_install_spelling" != "/" ] && [ "${given_install_spelling%/}" != "$given_install_spelling" ]; do
    given_install_spelling=${given_install_spelling%/}
done
if [ "$operating_system" = "linux" ] && command -v sudo >/dev/null 2>&1; then
    # -n never prompts; without cached credentials sudo's PATH cannot be read.
    if ! sudo_path=$(sudo -n sh -c 'echo "$PATH"' 2>/dev/null); then
        log_info "If 'sudo cca' says 'command not found', ${INSTALL_PATH} is not on sudo's secure_path: call"
        log_info "  sudo ${installed_binary_quoted}, or see the guide's secure_path step: ${SECURE_PATH_GUIDE_URL}"
    else
        install_directory_on_sudo_path=no
        case ":${sudo_path}:" in
            *":${INSTALL_PATH}:"*) install_directory_on_sudo_path=yes ;;
            *":${given_install_spelling}:"*)
                # As given (rpm-ostree links /usr/local to /var/usrlocal) only when that spelling is
                # absolute and root alone controls each of its components, symlinks not followed.
                case "$given_install_spelling" in
                    /*)
                        find_first_unsafe_path "$given_install_spelling"
                        if [ -z "$first_unsafe_path" ]; then install_directory_on_sudo_path=yes; fi
                        ;;
                esac
                ;;
        esac
        if [ "$install_directory_on_sudo_path" = "no" ]; then
            sudo_cca=$installed_binary_quoted
            log_warn "'sudo cca' will not find cca: ${INSTALL_PATH} is not on sudo's secure_path."
            log_warn "Call sudo ${sudo_cca}, or see the guide's secure_path step: ${SECURE_PATH_GUIDE_URL}"
        fi
    fi
fi
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
    # The agent searches these directories in this order (runtime/containerd.go);
    # EL9's containernetworking-plugins installs to /usr/libexec/cni.
    cni_plugins_found="no"
    for cni_directory in /opt/cni/bin /usr/libexec/cni /usr/lib/cni; do
        if [ -x "${cni_directory}/bridge" ]; then cni_plugins_found="yes"; fi
    done
    [ "$cni_plugins_found" = "yes" ] || missing_prerequisites="$missing_prerequisites cni-plugins"
    host_has_program etcd || missing_prerequisites="$missing_prerequisites etcd(control-plane-only)"
    if [ -z "$missing_prerequisites" ]; then
        log_ok "containerd, nftables, CNI plugins and etcd found"
    else
        log_warn "Missing for a cluster host:${missing_prerequisites}"
        log_warn "Install them as described in 'Prepare a fresh host':"
        log_warn "  ${INSTALL_GUIDE_URL}#prepare-a-fresh-host"
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
    log_info "    4. sudo ${sudo_cca} status"
    log_info ""
    log_info "  Add worker nodes and use the CLI without sudo:"
    log_info "    ${INSTALL_GUIDE_URL}#set-up-a-cluster-by-hand"
fi
