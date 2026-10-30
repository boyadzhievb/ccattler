#!/usr/bin/env bash
# health_check.sh — Check health of a running CCattler deploy cluster
#
# Verifies: VM reachability, API health, node count, service convergence,
# instance health, container count, systemd services, endpoint reachability,
# container drift.
#
# All cluster topology and configuration is discovered dynamically from
# vmherd and the running cluster — no hardcoded VM names, ports, or paths.
#
# Exit codes: 0 = healthy, 1 = unhealthy
#
# Prerequisites:
#   - Cluster deployed via cluster_test.sh --env deploy or deploy workflow
#   - vmherd installed and in PATH

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
VMHERD_DIR="$SCRIPT_DIR/vm-deploy"

VMHERD_BIN=$(command -v vmherd 2>/dev/null || echo "")
if [[ -z "$VMHERD_BIN" ]]; then
    echo "ERROR: vmherd not found in PATH" >&2
    exit 1
fi

# ---- Discover VMs from vmherd ----
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
REACHABLE_VMS=()
for vm in "${ALL_VM_NAMES[@]}"; do
    if ssh_vm "$vm" "true" 2>/dev/null; then
        log "  $vm: reachable"
        REACHABLE_VMS+=("$vm")
    else
        warn "$vm: UNREACHABLE"
    fi
done

EXPECTED_NODES=${#ALL_VM_NAMES[@]}
if [[ ${#REACHABLE_VMS[@]} -lt "$EXPECTED_NODES" ]]; then
    log "Only ${#REACHABLE_VMS[@]}/$EXPECTED_NODES VMs reachable — remaining checks may fail"
fi

# ---- Auto-detect control plane node ----
# The control plane runs cca-server. Find it dynamically instead of assuming
# the first VM. Fall back to first reachable VM if detection fails.
CTRL_NAME=""
for vm in "${REACHABLE_VMS[@]}"; do
    if ssh_vm "$vm" "systemctl is-active cca-server" >/dev/null 2>&1; then
        CTRL_NAME="$vm"
        break
    fi
done
if [[ -z "$CTRL_NAME" ]]; then
    CTRL_NAME="${REACHABLE_VMS[0]:-${ALL_VM_NAMES[0]}}"
    log "  Could not detect control plane — falling back to $CTRL_NAME"
fi
log "  Control plane: $CTRL_NAME"

# ---- Auto-detect API connection settings ----
# Discover the API port and TLS configuration from the running cca-server process.
CCA_API_PORT=""
CCA_TLS_FLAGS=""
CCA_API_SCHEME="https"

SERVER_CMDLINE=$(ssh_vm "$CTRL_NAME" "tr '\\0' ' ' < /proc/\$(pgrep -f 'cca server' | head -1)/cmdline 2>/dev/null || true" 2>/dev/null || true)

PORT_PATTERN='--api-port[= ]([0-9]+)'
CA_PATTERN='--tls-ca-file[= ]([^ ]+)'
CERT_PATTERN='--tls-cert-file[= ]([^ ]+)'
KEY_PATTERN='--tls-key-file[= ]([^ ]+)'

if [[ -n "$SERVER_CMDLINE" ]]; then
    if [[ "$SERVER_CMDLINE" =~ $PORT_PATTERN ]]; then
        CCA_API_PORT="${BASH_REMATCH[1]}"
    fi
fi
CCA_API_PORT="${CCA_API_PORT:-9770}"

CCA_TLS_CA=""
CCA_TLS_CERT=""
CCA_TLS_KEY=""

if [[ -n "$SERVER_CMDLINE" ]]; then
    if [[ "$SERVER_CMDLINE" =~ $CA_PATTERN ]]; then
        CCA_TLS_CA="${BASH_REMATCH[1]}"
    fi
    if [[ "$SERVER_CMDLINE" =~ $CERT_PATTERN ]]; then
        CCA_TLS_CERT="${BASH_REMATCH[1]}"
    fi
    if [[ "$SERVER_CMDLINE" =~ $KEY_PATTERN ]]; then
        CCA_TLS_KEY="${BASH_REMATCH[1]}"
    fi
fi

# Fall back to standard PKI paths.
CCA_TLS_CA="${CCA_TLS_CA:-/etc/ccattler/pki/ca.pem}"
CCA_TLS_CERT="${CCA_TLS_CERT:-/etc/ccattler/pki/node.pem}"
CCA_TLS_KEY="${CCA_TLS_KEY:-/etc/ccattler/pki/node-key.pem}"

# Check if TLS certs actually exist on the control plane.
if ssh_vm "$CTRL_NAME" "test -f $CCA_TLS_CA" 2>/dev/null; then
    CCA_TLS_FLAGS="--cacert $CCA_TLS_CA --cert $CCA_TLS_CERT --key $CCA_TLS_KEY"
else
    CCA_API_SCHEME="http"
    CCA_TLS_FLAGS=""
fi

CCA_API_URL="${CCA_API_SCHEME}://localhost:${CCA_API_PORT}"
log "  API: $CCA_API_URL (TLS: $([ -n "$CCA_TLS_FLAGS" ] && echo "yes" || echo "no"))"

# ---- Check 2: control plane health (via cca status) ----
log "Check 2: control plane health"
HEALTHZ_OK=false
HEALTHZ_MAX_RETRIES=3
for healthz_attempt in $(seq 1 "$HEALTHZ_MAX_RETRIES"); do
    if ssh_vm "$CTRL_NAME" "sudo cca status" >/dev/null 2>&1; then
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
if STATUS_JSON=$(ssh_vm "$CTRL_NAME" "sudo curl -sf $CCA_TLS_FLAGS ${CCA_API_URL}/api/status" 2>/dev/null); then
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

# ---- Diagnostic helper: collect logs for NOT CONVERGED services ----
collect_convergence_diagnostics() {
    log "  --- Convergence diagnostics ---"
    for svc_name in "${NOT_CONVERGED_SERVICES[@]}"; do
        log "  [$svc_name] Instance states:"
        echo "$STATUS_JSON" | jq -r --arg svc "$svc_name" \
            '.instances[] | select(.service == $svc) | "    \(.id) state=\(.state) node=\(.node) health=\(.health) init=\(.init_phase // "-")"'

        instance_ids=$(echo "$STATUS_JSON" | jq -r --arg svc "$svc_name" \
            '.instances[] | select(.service == $svc and .state != "running") | .id')

        for instance_id in $instance_ids; do
            log "  [$svc_name/$instance_id] Probe progress:"
            probe_json=$(ssh_vm "$CTRL_NAME" \
                "sudo curl -sf $CCA_TLS_FLAGS ${CCA_API_URL}/api/state?prefix=observed/instance/${instance_id}/probe-progress" \
                2>/dev/null || true)
            if [[ -n "$probe_json" ]] && echo "$probe_json" | jq -e '.[0].value' >/dev/null 2>&1; then
                echo "$probe_json" | jq -r '.[0].value' | jq -r \
                    '"    startup_failures=\(.startup_failures // 0)/\(.startup_threshold // "-") error=\(.startup_error // "-")"' \
                    2>/dev/null || log "    (probe data unparseable)"
            else
                log "    (no probe data)"
            fi
        done

        log "  [$svc_name] Container logs (last 20 lines):"
        container_logs=$(ssh_vm "$CTRL_NAME" \
            "sudo cca logs $svc_name --tail 20" 2>/dev/null || true)
        if [[ -n "$container_logs" ]]; then
            echo "$container_logs" | sed 's/^/    /'
        else
            log "    (no logs available)"
        fi
    done
    log "  --- End diagnostics ---"
}

# ---- Check 5: Service convergence ----
log "Check 5: Service convergence"
if [[ -n "$STATUS_JSON" ]]; then
    service_count=$(echo "$STATUS_JSON" | jq '.services | length')
    log "  Deployed services: $service_count"

    if [[ "$service_count" -eq 0 ]]; then
        warn "no services deployed"
    else
        NOT_CONVERGED_SERVICES=()
        while read -r svc_name svc_desired svc_running; do
            if [[ "$svc_desired" -ne "$svc_running" ]]; then
                log "  $svc_name: desired=$svc_desired running=$svc_running — NOT CONVERGED"
                NOT_CONVERGED_SERVICES+=("$svc_name")
            else
                log "  $svc_name: desired=$svc_desired running=$svc_running — ok"
            fi
        done < <(echo "$STATUS_JSON" | jq -r '.services[] | "\(.name) \(.desired) \(.running)"')

        not_converged=$(echo "$STATUS_JSON" | jq '[.services[] | select(.desired != .running)] | length')
        if [[ "$not_converged" -gt 0 ]]; then
            warn "$not_converged service(s) not converged (desired != running)"
            collect_convergence_diagnostics
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
    server_active=$(ssh_vm "$vm" "systemctl is-active cca-server 2>/dev/null || echo inactive" 2>/dev/null | tr -d '\n\r' || true)
    agent_active=$(ssh_vm "$vm" "systemctl is-active cca-agent 2>/dev/null || echo inactive" 2>/dev/null | tr -d '\n\r' || true)
    log "  $vm: server=${server_active:-unknown} agent=${agent_active:-unknown}"
    if [[ "${agent_active:-inactive}" != "active" ]]; then
        warn "$vm cca-agent not running"
    fi
done

# ---- Check 9: Endpoint reachability ----
log "Check 9: Endpoint reachability"
if [[ -n "$STATUS_JSON" ]]; then
    networking_count=$(echo "$STATUS_JSON" | jq '.networking | length // 0' 2>/dev/null || echo 0)
    if [[ "$networking_count" -gt 0 ]]; then
        endpoint_failures=0
        while read -r svc_name svc_vip svc_port; do
            if [[ -z "$svc_vip" ]] || [[ "$svc_vip" == "null" ]]; then
                log "  $svc_name: no VIP assigned, skipping"
                continue
            fi
            http_status=$(ssh_vm "$CTRL_NAME" \
                "curl -sf -o /dev/null -w '%{http_code}' --connect-timeout 5 http://${svc_vip}:${svc_port}/" \
                2>/dev/null | tr -d "'\n\r" || true)
            http_status="${http_status:-000}"
            if [[ "$http_status" =~ ^[23] ]]; then
                log "  $svc_name (${svc_vip}:${svc_port}): HTTP $http_status — ok"
            elif [[ "$http_status" == "000" ]]; then
                tcp_ok=$(ssh_vm "$CTRL_NAME" \
                    "timeout 5 bash -c 'echo > /dev/tcp/${svc_vip}/${svc_port}' 2>/dev/null && echo yes || echo no" \
                    2>/dev/null | tr -d '\n\r' || true)
                if [[ "$tcp_ok" == "yes" ]]; then
                    log "  $svc_name (${svc_vip}:${svc_port}): TCP open (non-HTTP service)"
                else
                    log "  $svc_name (${svc_vip}:${svc_port}): unreachable"
                    endpoint_failures=$((endpoint_failures + 1))
                fi
            else
                log "  $svc_name (${svc_vip}:${svc_port}): HTTP $http_status"
            fi
        done < <(echo "$STATUS_JSON" | jq -r '.networking[] | "\(.service) \(.vip) \(.port)"' 2>/dev/null)
        if [[ "$endpoint_failures" -gt 0 ]]; then
            warn "$endpoint_failures endpoint(s) unreachable"
        fi
    else
        log "  No networking entries in status"
    fi
else
    log "  Skipped (no status JSON)"
fi

# ---- Check 10: Container drift (debug API) ----
log "Check 10: Container drift"
if [[ -n "$STATUS_JSON" ]]; then
    for vm in "${ALL_VM_NAMES[@]}"; do
        debug_address=$(echo "$STATUS_JSON" | jq -r --arg node "$vm" '.nodes[] | select(.id == $node) | .debug_address // empty' 2>/dev/null || true)
        if [[ -z "$debug_address" ]]; then
            debug_address=$(ssh_vm "$CTRL_NAME" \
                "sudo curl -sf $CCA_TLS_FLAGS ${CCA_API_URL}/api/state?prefix=observed/node/${vm}/debug_address" 2>/dev/null \
                | jq -r '.[0].value // empty' 2>/dev/null || true)
        fi
        if [[ -z "$debug_address" ]]; then
            log "  $vm: no debug address — skipping"
            continue
        fi
        stats_json=$(ssh_vm "$vm" "curl -sf http://${debug_address}/debug/stats" 2>/dev/null || echo "")
        if [[ -z "$stats_json" ]]; then
            log "  $vm: debug API unreachable at $debug_address — skipping"
            continue
        fi
        workload_count=$(echo "$stats_json" | jq '.workload_count // 0' 2>/dev/null || echo 0)
        running_count=$(echo "$stats_json" | jq '.running_count // 0' 2>/dev/null || echo 0)
        stopped_count=$(echo "$stats_json" | jq '.stopped_count // 0' 2>/dev/null || echo 0)
        log_dir_count=$(echo "$stats_json" | jq '.log_dir_count // 0' 2>/dev/null || echo 0)

        expected_running=$(count_running_instances "$vm")
        log "  $vm: containers=$workload_count (running=$running_count stopped=$stopped_count) log_dirs=$log_dir_count expected_running=$expected_running"

        if [[ "$stopped_count" -gt 5 ]]; then
            warn "$vm: $stopped_count stopped containers (threshold: 5)"
        fi
        total_double=$((expected_running * 2))
        if [[ "$expected_running" -gt 0 && "$workload_count" -gt "$total_double" ]]; then
            warn "$vm: total containers ($workload_count) exceeds 2x expected ($expected_running)"
        fi
        log_threshold=$((workload_count + 5))
        if [[ "$log_dir_count" -gt "$log_threshold" ]]; then
            log "  INFO: $vm: log dirs ($log_dir_count) exceed container count + 5 ($log_threshold)"
        fi
    done
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
