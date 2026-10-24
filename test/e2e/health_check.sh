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
#   - VM SSH key at test/e2e/vm-deploy/.vmherd/id_ed25519

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ANSIBLE_DIR="$REPO_DIR/deploy/ansible"

CTRL_IP="192.168.124.10"
WORKER1_IP="192.168.124.20"
WORKER2_IP="192.168.124.30"
ETCD_ENDPOINTS="https://${CTRL_IP}:2379"

ALL_VM_IPS=("$CTRL_IP" "$WORKER1_IP" "$WORKER2_IP")
ALL_VM_NAMES=("cca-deploy-ctrl" "cca-deploy-worker-1" "cca-deploy-worker-2")

VM_SSH_KEY="${REPO_DIR}/test/e2e/vm-deploy/.vmherd/id_ed25519"

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
    local ip
    case "$vm_name" in
        cca-deploy-ctrl)     ip="$CTRL_IP" ;;
        cca-deploy-worker-1) ip="$WORKER1_IP" ;;
        cca-deploy-worker-2) ip="$WORKER2_IP" ;;
        *) echo "Unknown VM: $vm_name" >&2; return 1 ;;
    esac
    ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR -o ConnectTimeout=10 \
        -i "$VM_SSH_KEY" "vmherd@$ip" "$@"
}

count_containers() {
    local vm_name="$1"
    ssh_vm "$vm_name" "sudo nerdctl ps -q 2>/dev/null | wc -l" 2>/dev/null | tr -d ' ' || echo "0"
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

if [[ "$REACHABLE_COUNT" -lt 3 ]]; then
    log "Only $REACHABLE_COUNT/3 VMs reachable — remaining checks may fail"
fi

# ---- Check 2: etcd health (via healthz endpoint) ----
log "Check 2: control plane health"
HEALTHZ_OK=false
HEALTHZ_MAX_RETRIES=3
for healthz_attempt in $(seq 1 "$HEALTHZ_MAX_RETRIES"); do
    if ssh_vm cca-deploy-ctrl "curl -sf --connect-timeout 10 --cacert /etc/ccattler/pki/ca.pem --cert /etc/ccattler/pki/node.pem --key /etc/ccattler/pki/node-key.pem https://${CTRL_IP}:9770/healthz" >/dev/null 2>&1; then
        HEALTHZ_OK=true
        break
    fi
    if [[ "$healthz_attempt" -lt "$HEALTHZ_MAX_RETRIES" ]]; then
        sleep 5
    fi
done
if [[ "$HEALTHZ_OK" == "true" ]]; then
    log "  healthz: OK"
else
    warn "healthz: UNHEALTHY (failed after $HEALTHZ_MAX_RETRIES attempts)"
fi

# ---- Check 3: cca server API ----
log "Check 3: cca server API"
CA_CERT="/tmp/cca-healthcheck-ca.pem"
CLIENT_CERT="/tmp/cca-healthcheck-client.pem"
CLIENT_KEY="/tmp/cca-healthcheck-client-key.pem"

CERTS_FETCHED=true
if ! ssh_vm cca-deploy-ctrl "sudo cat /etc/ccattler/pki/ca.pem" > "$CA_CERT" 2>/dev/null; then
    CERTS_FETCHED=false
fi
if ! ssh_vm cca-deploy-ctrl "sudo cat /etc/ccattler/pki/node.pem" > "$CLIENT_CERT" 2>/dev/null; then
    CERTS_FETCHED=false
fi
if ! ssh_vm cca-deploy-ctrl "sudo cat /etc/ccattler/pki/node-key.pem" > "$CLIENT_KEY" 2>/dev/null; then
    CERTS_FETCHED=false
fi

CCA_API="https://${CTRL_IP}:9770"
CURL_TLS="--cacert $CA_CERT --cert $CLIENT_CERT --key $CLIENT_KEY"

STATUS_JSON=""
if [[ "$CERTS_FETCHED" != "true" ]]; then
    warn "API: could not fetch TLS certs from ctrl node"
elif STATUS_JSON=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null); then
    log "  API /status: reachable"
else
    warn "API /status: UNREACHABLE"
fi

# ---- Check 4: Node count ----
log "Check 4: Node count"
if [[ -n "$STATUS_JSON" ]]; then
    alive_count=$(echo "$STATUS_JSON" | jq '[.nodes[] | select(.state == "alive")] | length')
    log "  Alive nodes: $alive_count/3"
    if [[ "$alive_count" -lt 3 ]]; then
        warn "expected 3 alive nodes, got $alive_count"
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

# ---- Check 7: Container count ----
log "Check 7: Container count"
total_containers=0
for vm in "${ALL_VM_NAMES[@]}"; do
    count=$(count_containers "$vm")
    log "  $vm: $count containers"
    total_containers=$((total_containers + count))
done
log "  Total containers: $total_containers"

expected_from_api=0
if [[ -n "$STATUS_JSON" ]]; then
    expected_from_api=$(echo "$STATUS_JSON" | jq '[.services[].desired] | add // 0')
fi

if [[ "$expected_from_api" -gt 0 ]]; then
    if [[ "$total_containers" -lt "$expected_from_api" ]]; then
        warn "expected at least $expected_from_api containers (from API desired count), found $total_containers"
    fi
elif [[ "$total_containers" -lt 1 ]]; then
    warn "no containers running on any node"
fi

# ---- Check 8: systemd services ----
log "Check 8: systemd services"
for vm in "${ALL_VM_NAMES[@]}"; do
    server_active=$(ssh_vm "$vm" "systemctl is-active cca-server 2>/dev/null || echo inactive" 2>/dev/null)
    agent_active=$(ssh_vm "$vm" "systemctl is-active cca-agent 2>/dev/null || echo inactive" 2>/dev/null)
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
            http_status=$(ssh_vm cca-deploy-ctrl \
                "curl -sf -o /dev/null -w '%{http_code}' --connect-timeout 5 http://${svc_vip}:${svc_port}/ 2>/dev/null || echo 000" \
                2>/dev/null | tr -d "'")
            if [[ "$http_status" =~ ^[23] ]]; then
                log "  $svc_name (${svc_vip}:${svc_port}): HTTP $http_status — ok"
            elif [[ "$http_status" == "000" ]]; then
                # HTTP failed — try TCP connectivity as fallback (for non-HTTP services like databases)
                tcp_ok=$(ssh_vm cca-deploy-ctrl \
                    "timeout 5 bash -c 'echo > /dev/tcp/${svc_vip}/${svc_port}' 2>/dev/null && echo yes || echo no" \
                    2>/dev/null)
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

# ---- Cleanup temp files ----
rm -f "$CA_CERT" "$CLIENT_CERT" "$CLIENT_KEY"

# ---- Result ----
echo ""
if [[ "$HEALTHY" == "true" ]]; then
    log "HEALTH CHECK: PASS (0 warnings)"
    exit 0
else
    log "HEALTH CHECK: FAIL ($WARNINGS warning(s))"
    exit 1
fi
