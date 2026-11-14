#!/usr/bin/env bash
# Run the AWS cloud-provider integration tests against a local AWS Emulator
# (https://docs.aws.amazon.com/emulator/latest/userguide/) instead of real AWS.
#
# Usage:
#   scripts/aws-emulator.sh start   # build image if missing, start, wait healthy
#   scripts/aws-emulator.sh env     # print SDK exports: eval "$(scripts/aws-emulator.sh env)"
#   scripts/aws-emulator.sh test    # start if needed, run go test -tags cloudprovider, stop if started here
#   scripts/aws-emulator.sh stop    # stop the container (emulated resources are lost)
#
# Environment: AWS_EMULATOR_PORT (default 9297), AWS_EMULATOR_REBUILD=1 to rebuild the image.
# Telemetry is off. Real AWS is never called: tests run only after STS reports
# the emulator's fixed account 000000000000.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EMULATOR_PORT="${AWS_EMULATOR_PORT:-9297}"
EMULATOR_IMAGE="aws-emulator"
EMULATOR_CONTAINER="cca-aws-emulator-${EMULATOR_PORT}"
EMULATOR_STATE_DIR="${HOME}/.aws/emulator"
EMULATOR_FILES_DIR="${EMULATOR_STATE_DIR}/${EMULATOR_PORT}"
EMULATOR_ACCOUNT="000000000000"
HEALTH_WAIT_SECONDS=30

# Print a message and exit non-zero.
fatal() {
  echo "aws-emulator: $*" >&2
  exit 1
}

# Report whether the emulator on EMULATOR_PORT answers its health check.
emulator_is_healthy() {
  [[ -f "${EMULATOR_FILES_DIR}/ca.pem" ]] || return 1
  curl -sf --cacert "${EMULATOR_FILES_DIR}/ca.pem" \
    "https://127.0.0.1:${EMULATOR_PORT}/__admin/health" >/dev/null 2>&1
}

# Build the image from test/aws-emulator/Dockerfile for the Docker daemon's
# CPU, which decides the base image's CPU (uname -m can differ, e.g. a shell
# under Rosetta on an Apple Silicon Mac).
build_emulator_image() {
  local emulator_arch
  emulator_arch="$(docker info --format '{{.Architecture}}')" || fatal "cannot query the Docker daemon"
  case "$emulator_arch" in
    arm64|aarch64) emulator_arch="aarch64" ;;
    x86_64|amd64)  emulator_arch="x86_64" ;;
    *) fatal "unsupported Docker CPU ${emulator_arch}" ;;
  esac
  docker build -t "$EMULATOR_IMAGE" --build-arg "EMULATOR_ARCH=${emulator_arch}" \
    "${REPO_ROOT}/test/aws-emulator"
}

# Start the emulator container unless one is already healthy on the port.
start_emulator() {
  command -v docker >/dev/null || fatal "docker not found"
  if emulator_is_healthy; then
    echo "aws-emulator: already running on 127.0.0.1:${EMULATOR_PORT}"
    return 0
  fi
  if [[ "${AWS_EMULATOR_REBUILD:-0}" == "1" ]] || ! docker image inspect "$EMULATOR_IMAGE" >/dev/null 2>&1; then
    build_emulator_image
  fi
  mkdir -p "$EMULATOR_STATE_DIR"
  # A run under sudo leaves the directory root-owned (Docker creates missing
  # mount sources as root); the emulator then fails with "Permission denied".
  [[ -w "$EMULATOR_STATE_DIR" ]] \
    || fatal "${EMULATOR_STATE_DIR} is not writable (owner: $(stat -c %U "$EMULATOR_STATE_DIR" 2>/dev/null || echo unknown)); run: sudo chown -R $(id -un) ${HOME}/.aws"
  local user_args=()
  if [[ "$(uname -s)" == "Linux" ]]; then
    user_args=(--user "$(id -u):$(id -g)")
  fi
  # 127.0.0.1 only: the plain -p PORT:PORT form would open it to the network.
  docker run -d --rm --name "$EMULATOR_CONTAINER" "${user_args[@]}" \
    -e EMULATOR_TELEMETRY=off --memory=2g --memory-swap=2g \
    -p "127.0.0.1:${EMULATOR_PORT}:${EMULATOR_PORT}" \
    -e HOME="$HOME" -v "${EMULATOR_STATE_DIR}:${EMULATOR_STATE_DIR}" \
    "$EMULATOR_IMAGE" -p "$EMULATOR_PORT" >/dev/null
  local attempt
  for ((attempt = 0; attempt < HEALTH_WAIT_SECONDS; attempt++)); do
    emulator_is_healthy && { echo "aws-emulator: healthy on 127.0.0.1:${EMULATOR_PORT}"; return 0; }
    sleep 1
  done
  docker logs "$EMULATOR_CONTAINER" 2>&1 | tail -20 >&2 || true
  fatal "not healthy after ${HEALTH_WAIT_SECONDS}s"
}

# Stop the container started by this script (it removes itself, --rm).
stop_emulator() {
  if docker ps -q --filter "name=^${EMULATOR_CONTAINER}$" | grep -q .; then
    docker stop "$EMULATOR_CONTAINER" >/dev/null
    echo "aws-emulator: stopped ${EMULATOR_CONTAINER}"
  else
    echo "aws-emulator: ${EMULATOR_CONTAINER} not running (an emulator started elsewhere is left alone)"
  fi
}

# Print the SDK environment from the user guide ("To point your SDK at a local emulator").
print_sdk_environment() {
  cat <<EOF
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE
export AWS_ENDPOINT_URL=https://127.0.0.1:${EMULATOR_PORT}
export AWS_SHARED_CREDENTIALS_FILE=${EMULATOR_FILES_DIR}/emulator_credentials
export AWS_CONFIG_FILE=${EMULATOR_FILES_DIR}/config
export AWS_CA_BUNDLE=${EMULATOR_FILES_DIR}/ca.pem
export AWS_EC2_METADATA_DISABLED=true
export AWS_DISABLE_HOST_PREFIX_INJECTION=true
export AWS_REGION=us-east-1
export AWS_DEFAULT_REGION=us-east-1
EOF
}

# Start if needed, prove requests reach the emulator, run the AWS tests.
run_emulator_tests() {
  command -v aws >/dev/null \
    || fatal "aws CLI not found; it proves requests reach the emulator, not real AWS (install awscli)"
  local started_here=0
  if ! emulator_is_healthy; then
    start_emulator
    started_here=1
  fi
  eval "$(print_sdk_environment)"
  local caller_account
  caller_account=$(aws sts get-caller-identity --query Account --output text 2>&1) \
    || fatal "sts get-caller-identity failed: ${caller_account}"
  [[ "$caller_account" == "$EMULATOR_ACCOUNT" ]] \
    || fatal "requests reach account '${caller_account}', not the emulator; refusing to run"
  local test_exit=0
  (cd "$REPO_ROOT" && go test -tags cloudprovider -count=1 -run 'TestAWSIntegration' ./cloud/ -v) || test_exit=$?
  if [[ "$started_here" == "1" ]]; then
    stop_emulator
  fi
  return "$test_exit"
}

case "${1:-}" in
  start) start_emulator ;;
  stop)  stop_emulator ;;
  env)   print_sdk_environment ;;
  test)  run_emulator_tests ;;
  *)     sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
