#!/usr/bin/env bash
# demo-multihost.sh — Real multi-host CCattler cluster demo
#
# Topology:
#   .43  (192.168.100.43)  — control plane: etcd + cca server + worker-2
#   .215 (192.168.100.215) — worker-1: cca agent
#   Mac  (local machine)   — applies workloads via cca apply → etcd
#
# Both workers run containers (--runtime container) with real Docker images.
#
# Usage:
#   ./deploy/demo-multihost.sh          # full run: start cluster, deploy, verify
#   ./deploy/demo-multihost.sh teardown  # kill everything

set -euo pipefail

CTRL_HOST="192.168.100.43"
CTRL_USER="bojan"
CTRL_KEY="$HOME/.ssh/id_rsa_lenovo_bojan"

WORKER_HOST="192.168.100.215"
WORKER_USER="bozhan"
WORKER_KEY="$HOME/.ssh/id_rsa_dell_bozhan"

ETCD_ENDPOINTS="http://${CTRL_HOST}:2379"
SERVER_LISTEN="0.0.0.0:9770"
CCA_REMOTE="/tmp/cca"

SSH_OPTS="-o StrictHostKeyChecking=no -o LogLevel=ERROR"

ssh_ctrl() { ssh -i "$CTRL_KEY" $SSH_OPTS "$CTRL_USER@$CTRL_HOST" "$@"; }
ssh_worker() { ssh -i "$WORKER_KEY" $SSH_OPTS "$WORKER_USER@$WORKER_HOST" "$@"; }

teardown() {
    echo "==> Tearing down cluster..."
    ssh_worker 'kill $(pgrep -f "/tmp/cca agent") 2>/dev/null; docker rm -f $(docker ps -q --filter name=cca-) 2>/dev/null; true'
    ssh_ctrl 'kill $(pgrep -f "/tmp/cca agent") 2>/dev/null; kill $(pgrep -f "/tmp/cca server") 2>/dev/null; docker rm -f $(docker ps -q --filter name=cca-) 2>/dev/null; true'
    echo "==> Done."
}

if [[ "${1:-}" == "teardown" ]]; then
    teardown
    exit 0
fi

teardown 2>/dev/null || true
sleep 1

echo ""
echo "╔══════════════════════════════════════════════════════════╗"
echo "║  CCattler Multi-Host Demo                               ║"
echo "║                                                         ║"
echo "║  Control Plane:  $CTRL_HOST  (etcd + server + worker-2) ║"
echo "║  Worker Node:    $WORKER_HOST  (worker-1)               ║"
echo "╚══════════════════════════════════════════════════════════╝"
echo ""

# ---- Step 1: Cross-compile and deploy binary ----
echo "==> [1/6] Deploying cca binary to servers..."
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
LINUX_BINARY="/tmp/cca-linux-amd64"

if [[ ! -f "$LINUX_BINARY" ]] || [[ "$REPO_DIR/cmd/cca/main.go" -nt "$LINUX_BINARY" ]]; then
    echo "    Cross-compiling for linux/amd64..."
    (cd "$REPO_DIR" && GOOS=linux GOARCH=amd64 go build -o "$LINUX_BINARY" ./cmd/cca/)
fi

scp -i "$CTRL_KEY" $SSH_OPTS -q "$LINUX_BINARY" "${CTRL_USER}@${CTRL_HOST}:${CCA_REMOTE}"
ssh_ctrl "chmod +x ${CCA_REMOTE}"
echo "    $CTRL_HOST: binary deployed"

scp -i "$WORKER_KEY" $SSH_OPTS -q "$LINUX_BINARY" "${WORKER_USER}@${WORKER_HOST}:${CCA_REMOTE}"
ssh_worker "chmod +x ${CCA_REMOTE}"
echo "    $WORKER_HOST: binary deployed"
echo ""

# ---- Step 2: Verify etcd ----
echo "==> [2/6] Verifying etcd on $CTRL_HOST..."
ssh_ctrl "ETCDCTL_API=3 etcdctl --endpoints=${ETCD_ENDPOINTS} endpoint health"
echo ""

# ---- Step 3: Start cca server ----
echo "==> [3/6] Starting cca server on $CTRL_HOST..."
ssh_ctrl "nohup ${CCA_REMOTE} server \
    --store etcd \
    --endpoints ${ETCD_ENDPOINTS} \
    --listen ${SERVER_LISTEN} \
    > /tmp/cca-server.log 2>&1 &"
sleep 2
ssh_ctrl "cat /tmp/cca-server.log | head -2"
echo ""

# ---- Step 4: Start agents ----
echo "==> [4/6] Starting agents..."

ssh_ctrl "nohup ${CCA_REMOTE} agent \
    --node-id worker-2 \
    --store etcd \
    --endpoints ${ETCD_ENDPOINTS} \
    --runtime container \
    > /tmp/cca-agent.log 2>&1 &"
echo "    worker-2 started on $CTRL_HOST"

ssh_worker "nohup ${CCA_REMOTE} agent \
    --node-id worker-1 \
    --store etcd \
    --endpoints ${ETCD_ENDPOINTS} \
    --runtime container \
    > /tmp/cca-agent.log 2>&1 &"
echo "    worker-1 started on $WORKER_HOST"

echo "    Waiting for agents to register..."
sleep 4
echo ""

# ---- Step 5: Verify cluster ----
echo "==> [5/6] Cluster status (empty)..."
curl -s "http://${CTRL_HOST}:9770/status"
echo ""

# ---- Step 6: Deploy workload ----
echo "==> [6/6] Deploying workload..."
(cd "$REPO_DIR" && ./cca apply deploy/cluster-test.ccattler --store etcd --endpoints "${ETCD_ENDPOINTS}")
echo ""
echo "    Waiting for containers to start..."
sleep 10

echo ""
echo "=== FINAL CLUSTER STATUS ==="
curl -s "http://${CTRL_HOST}:9770/status"
echo ""

echo "=== DOCKER CONTAINERS ==="
echo "--- $CTRL_HOST (worker-2) ---"
ssh_ctrl 'docker ps --format "table {{.Names}}\t{{.Image}}\t{{.Status}}"'
echo ""
echo "--- $WORKER_HOST (worker-1) ---"
ssh_worker 'docker ps --format "table {{.Names}}\t{{.Image}}\t{{.Status}}"'
echo ""

echo "╔══════════════════════════════════════════════════════════╗"
echo "║  Multi-host cluster is running!                         ║"
echo "║                                                         ║"
echo "║  Real containers on two physical servers.               ║"
echo "║  Scheduler distributed instances across both workers.   ║"
echo "║                                                         ║"
echo "║  Tear down: ./deploy/demo-multihost.sh teardown         ║"
echo "╚══════════════════════════════════════════════════════════╝"
