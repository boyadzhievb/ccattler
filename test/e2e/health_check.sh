#!/usr/bin/env bash
# health_check.sh — Check health of a running CCattler deploy cluster
#
# Verifies: etcd health, API reachability, node count, service convergence,
# container count, systemd services, HTTP endpoint reachability.
# Designed for periodic cron execution against a long-running deploy cluster.
#
# Exit codes: 0 = healthy, 1 = unhealthy
#
# Prerequisites:
#   - Cluster deployed via cluster_test.sh --env deploy or deploy workflow
#   - vmherd installed and in PATH

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"
VMHERD_DIR="$SCRIPT_DIR/vm-deploy"

VMHERD_BIN=$(command -v vmherd 2>/dev/null || echo "")
if [[ -z "$VMHERD_BIN" ]]; then
    echo "ERROR: vmherd not found in PATH" >&2
    exit 1
fi

# Discover VM names and IPs from vmherd status.
ALL_VM_NAMES=()
ALL_VM_IPS=()
declare -A VM_IP_MAP
while read -r vm_name _ vm_ip _; do
    [[ "$vm_name" == "NAME" || "$vm_name" == "----" ]] && continue
    ALL_VM_NAMES+=("$vm_name")
    ALL_VM_IPS+=("$vm_ip")
    VM_IP_MAP["$vm_name"]="$vm_ip"
done < <(cd "$VMHERD_DIR" && "$VMHERD_BIN" status 2>/dev/null)

if [[ ${#ALL_VM_NAMES[@]} -eq 0 ]]; then
    echo "ERROR: no VMs found in $VMHERD_DIR — is the Herdfile present?" >&2
    exit 1
fi

# First VM is the control plane node.
CTRL_NAME="${ALL_VM_NAMES[0]}"
CTRL_IP="${VM_IP_MAP[$CTRL_NAME]}"

HEALTHY=true
WARNINGS=0

log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"; }

warn() {
    log "  WARNING: $*"
    HEALTHY=false
    WARNINGS=$((WARNINGS + 1))
}

ssh_vm() {
    local vm_name="$1"; shift
    (cd "$VMHERD_DIR" && "$VMHERD_BIN" ssh "$vm_name" -- "$@")
}

count_running_instances() {
    local node_name="$1"
    if [[ -n "$STATUS_JSON" ]]; then
        echo "$STATUS_JSON" | jq --arg node "$node_name" '[.instances[] | select(.node == $node and .state == "running")] | length'
    else
        echo "0"
    fi
}

# ---- Check 1: VM reachability ----
log "Check 1: VM reachability"
REACHABLE_COUNT=0
for vm in "${ALL_VM_NAMES[@]}"; do
    if ssh_vm "$vm" "true" 2>/dev/null; then
        log "  $vm: reachable"
        REACHABLE_COUNT=$((REACHABLE_COUNT + 1))
    else
        warn "$vm: UNREACHABLE"
    fi
done

EXPECTED_NODES=${#ALL_VM_NAMES[@]}
if [[ "$REACHABLE_COUNT" -lt "$EXPECTED_NODES" ]]; then
    log "Only $REACHABLE_COUNT/$EXPECTED_NODES VMs reachable — remaining checks may fail"
fi

# ---- Check 2: control plane health (via cca status) ----
log "Check 2: control plane health"
HEALTHZ_OK=false
HEALTHZ_MAX_RETRIES=3
for healthz_attempt in $(seq 1 "$HEALTHZ_MAX_RETRIES"); do
    if ssh_vm $CTRL_NAME "sudo cca status" >/dev/null 2>&1; then
        HEALTHZ_OK=true
        break
    fi
    if [[ "$healthz_attempt" -lt "$HEALTHZ_MAX_RETRIES" ]]; then
        sleep 5
    fi
done
if [[ "$HEALTHZ_OK" == "true" ]]; then
    log "  cca status: OK"
else
    warn "cca status: UNHEALTHY (failed after $HEALTHZ_MAX_RETRIES attempts)"
fi

# ---- Check 3: cca server API (JSON via curl on node) ----
log "Check 3: cca server API"
STATUS_JSON=""
CCA_TLS_FLAGS="--cacert /etc/ccattler/pki/ca.pem --cert /etc/ccattler/pki/node.pem --key /etc/ccattler/pki/node-key.pem"
if STATUS_JSON=$(ssh_vm "$CTRL_NAME" "sudo curl -sf $CCA_TLS_FLAGS https://localhost:9770/api/status" 2>/dev/null); then
    log "  API /status: reachable"
else
    warn "API /status: UNREACHABLE"
fi

# ---- Check 4: Node count ----
log "Check 4: Node count"
if [[ -n "$STATUS_JSON" ]]; then
    alive_count=$(echo "$STATUS_JSON" | jq '[.nodes[] | select(.state == "alive")] | length')
    log "  Alive nodes: $alive_count/$EXPECTED_NODES"
    if [[ "$alive_count" -lt "$EXPECTED_NODES" ]]; then
        warn "expected $EXPECTED_NODES alive nodes, got $alive_count"
    fi
else
    log "  Skipped (no status JSON)"
fi

# ---- Check 5: Service convergence ----
log "Check 5: Service convergence"
if [[ -n "$STATUS_JSON" ]]; then
    service_count=$(echo "$STATUS_JSON" | jq '.services | length')
    log "  Deployed services: $service_count"

    if [[ "$service_count" -eq 0 ]]; then
        warn "no services deployed"
    else
        converged=true
        echo "$STATUS_JSON" | jq -r '.services[] | "\(.name) \(.desired) \(.running)"' | while read -r svc_name svc_desired svc_running; do
            if [[ "$svc_desired" -ne "$svc_running" ]]; then
                log "  $svc_name: desired=$svc_desired running=$svc_running — NOT CONVERGED"
                converged=false
            else
                log "  $svc_name: desired=$svc_desired running=$svc_running — ok"
            fi
        done

        not_converged=$(echo "$STATUS_JSON" | jq '[.services[] | select(.desired != .running)] | length')
        if [[ "$not_converged" -gt 0 ]]; then
            warn "$not_converged service(s) not converged (desired != running)"
        fi
    fi
else
    log "  Skipped (no status JSON)"
fi

# ---- Check 6: Instance health ----
log "Check 6: Instance health"
if [[ -n "$STATUS_JSON" ]]; then
    total_instances=$(echo "$STATUS_JSON" | jq '.instances | length')
    running_instances=$(echo "$STATUS_JSON" | jq '[.instances[] | select(.state == "running")] | length')
    failed_instances=$(echo "$STATUS_JSON" | jq '[.instances[] | select(.state == "failed")] | length')

    log "  Total: $total_instances, Running: $running_instances, Failed: $failed_instances"

    if [[ "$failed_instances" -gt 0 ]]; then
        warn "$failed_instances instance(s) in failed state"
        echo "$STATUS_JSON" | jq -r '.instances[] | select(.state == "failed") | "    \(.id) (\(.service)) on \(.node)"'
    fi

    # health="-" means no probe configured yet — not unhealthy, just unprobed
    unhealthy_filter='[.instances[] | select(.health != null and .health != "" and .health != "healthy" and .health != "-")] | length'
    unhealthy_instances=$(echo "$STATUS_JSON" | jq "$unhealthy_filter")
    if [[ "$unhealthy_instances" -gt 0 ]]; then
        warn "$unhealthy_instances instance(s) unhealthy"
        echo "$STATUS_JSON" | jq -r '.instances[] | select(.health != null and .health != "" and .health != "healthy" and .health != "-") | "    \(.id) (\(.service)) health=\(.health)"'
    fi

    unprobed_instances=$(echo "$STATUS_JSON" | jq '[.instances[] | select(.health == "-")] | length')
    if [[ "$unprobed_instances" -gt 0 ]]; then
        log "  Info: $unprobed_instances instance(s) have no health probe data yet"
    fi
else
    log "  Skipped (no status JSON)"
fi

# ---- Check 7: Running instance count (from API) ----
log "Check 7: Running instances"
if [[ -n "$STATUS_JSON" ]]; then
    total_running=0
    for vm in "${ALL_VM_NAMES[@]}"; do
        count=$(count_running_instances "$vm")
        log "  $vm: $count running instances"
        total_running=$((total_running + count))
    done
    log "  Total running: $total_running"

    expected_total=$(echo "$STATUS_JSON" | jq '[.services[].desired] | add // 0')
    if [[ "$expected_total" -gt 0 && "$total_running" -lt "$expected_total" ]]; then
        warn "expected $expected_total running instances, found $total_running"
    elif [[ "$total_running" -lt 1 ]]; then
        warn "no running instances on any node"
    fi
else
    log "  Skipped (no status JSON)"
fi

# ---- Check 8: systemd services ----
log "Check 8: systemd services"
for vm in "${ALL_VM_NAMES[@]}"; do
    server_active=$(ssh_vm "$vm" "systemctl is-active cca-server 2>/dev/null || echo inactive" 2>/dev/null | tr -d '\n\r')
    agent_active=$(ssh_vm "$vm" "systemctl is-active cca-agent 2>/dev/null || echo inactive" 2>/dev/null | tr -d '\n\r')
    log "  $vm: server=$server_active agent=$agent_active"
    if [[ "$agent_active" != "active" ]]; then
        warn "$vm cca-agent not running"
    fi
done

# ---- Check 9: Endpoint reachability ----
log "Check 9: Endpoint reachability"
if [[ -n "$STATUS_JSON" ]]; then
    networking_count=$(echo "$STATUS_JSON" | jq '.networking | length')
    if [[ "$networking_count" -gt 0 ]]; then
        echo "$STATUS_JSON" | jq -r '.networking[] | "\(.service) \(.vip) \(.port)"' | while read -r svc_name svc_vip svc_port; do
            if [[ -z "$svc_vip" ]] || [[ "$svc_vip" == "null" ]]; then
                log "  $svc_name: no VIP assigned, skipping"
                continue
            fi
            http_status=$(ssh_vm "$CTRL_NAME" \
                "curl -sf -o /dev/null -w '%{http_code}' --connect-timeout 5 http://${svc_vip}:${svc_port}/" \
                2>/dev/null | tr -d "'\n\r")
            http_status="${http_status:-000}"
            if [[ "$http_status" =~ ^[23] ]]; then
                log "  $svc_name (${svc_vip}:${svc_port}): HTTP $http_status — ok"
            elif [[ "$http_status" == "000" ]]; then
                # HTTP failed — try TCP connectivity as fallback (for non-HTTP services like databases)
                tcp_ok=$(ssh_vm "$CTRL_NAME" \
                    "timeout 5 bash -c 'echo > /dev/tcp/${svc_vip}/${svc_port}' 2>/dev/null && echo yes || echo no" \
                    2>/dev/null | tr -d '\n\r')
                if [[ "$tcp_ok" == "yes" ]]; then
                    log "  $svc_name (${svc_vip}:${svc_port}): TCP open (non-HTTP service)"
                else
                    log "  $svc_name (${svc_vip}:${svc_port}): unreachable"
                fi
            else
                log "  $svc_name (${svc_vip}:${svc_port}): HTTP $http_status"
            fi
        done
    else
        log "  No networking entries in status"
    fi
else
    log "  Skipped (no status JSON)"
fi

# ---- Result ----
echo ""
if [[ "$HEALTHY" == "true" ]]; then
    log "HEALTH CHECK: PASS (0 warnings)"
    exit 0
else
    log "HEALTH CHECK: FAIL ($WARNINGS warning(s))"
    exit 1
fi
