#!/usr/bin/env sh
set -eu

REPO="boyadzhievb/ccattler"
INSTALL_DIR="${CCA_INSTALL_DIR:-/usr/local/bin}"

detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux)  echo "linux" ;;
    *)      echo "Unsupported OS: $(uname -s)" >&2; exit 1 ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)             echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
}

get_latest_version() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'
  else
    echo "Neither curl nor wget found." >&2; exit 1
  fi
}

download() {
  url="$1"; dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$dest" "$url"
  else
    wget -qO "$dest" "$url"
  fi
}

main() {
  os="$(detect_os)"
  arch="$(detect_arch)"
  version="${CCA_VERSION:-$(get_latest_version)}"

  if [ -z "$version" ]; then
    echo "Error: could not determine latest version." >&2
    echo "Set CCA_VERSION=vX.Y.Z to install a specific version." >&2
    exit 1
  fi

  archive="cca-${version}-${os}-${arch}.tar.gz"
  url="https://github.com/${REPO}/releases/download/${version}/${archive}"

  echo "Installing cca ${version} (${os}/${arch})..."

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  echo "Downloading ${url}..."
  download "$url" "${tmpdir}/${archive}"

  echo "Extracting..."
  tar xzf "${tmpdir}/${archive}" -C "$tmpdir"

  if [ ! -w "$INSTALL_DIR" ]; then
    echo "Installing to ${INSTALL_DIR} (requires sudo)..."
    sudo install -m 755 "${tmpdir}/cca" "${INSTALL_DIR}/cca"
  else
    install -m 755 "${tmpdir}/cca" "${INSTALL_DIR}/cca"
  fi

  echo "Installed cca ${version} to ${INSTALL_DIR}/cca"
  echo ""
  echo "Run 'cca demo' to try it out."
}

main
