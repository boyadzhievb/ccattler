#!/usr/bin/env bash
# caddy-sync.sh — Reconcile Caddy reverse proxy config from CCattler route facts.
#
# Runs on the testbed host as a systemd timer. Queries the CCattler API for
# route definitions and resolved backends, builds a Caddyfile, and reloads
# Caddy when the config changes. Also maintains host routes to overlay subnets
# so the testbed host can reach container IPs directly.
#
# Usage:
#   scripts/caddy-sync.sh [--once]
#
# Environment:
#   CCA_API           CCattler API URL (default: https://192.168.124.10:9770)
#   CCA_CA            CA cert path (default: /etc/ccattler/pki/ca.pem)
#   CCA_CERT          Client cert path (default: /etc/ccattler/pki/node.pem)
#   CCA_KEY           Client key path (default: /etc/ccattler/pki/node-key.pem)
#   CADDY_FILE        Caddyfile path (default: /etc/caddy/Caddyfile)
#   SYNC_INTERVAL     Seconds between reconciliation cycles (default: 30)

set -euo pipefail

CCA_API="${CCA_API:-https://192.168.124.10:9770}"
CCA_CA="${CCA_CA:-/etc/ccattler/pki/ca.pem}"
CCA_CERT="${CCA_CERT:-/etc/ccattler/pki/node.pem}"
CCA_KEY="${CCA_KEY:-/etc/ccattler/pki/node-key.pem}"
CADDY_FILE="${CADDY_FILE:-/etc/caddy/Caddyfile}"
SYNC_INTERVAL="${SYNC_INTERVAL:-30}"

CURL_TLS="--cacert $CCA_CA --cert $CCA_CERT --key $CCA_KEY"

log() { echo "[$(date '+%H:%M:%S')] caddy-sync: $*"; }

# query_api fetches a CCattler API endpoint and returns the JSON body.
query_api() {
    local endpoint="$1"
    curl -sf $CURL_TLS "${CCA_API}${endpoint}" 2>/dev/null
}

# build_caddyfile reads route facts and resolved backends from the CCattler API,
# then writes a Caddyfile with one site block per route.
build_caddyfile() {
    local route_facts derived_facts status_json

    route_facts=$(query_api "/api/state?prefix=desired/route/") || { log "ERROR: failed to query route facts"; return 1; }
    derived_facts=$(query_api "/api/state?prefix=derived/route/") || { log "ERROR: failed to query derived routes"; return 1; }
    status_json=$(query_api "/status") || { log "ERROR: failed to query status"; return 1; }

    local route_names
    route_names=$(echo "$route_facts" | jq -r '.[].key' | sed 's|desired/route/||' | cut -d/ -f1 | sort -u)

    if [[ -z "$route_names" ]]; then
        log "no routes found in store"
        echo ""
        return 0
    fi

    local caddyfile=""
    caddyfile+="{\n"
    caddyfile+="    auto_https disable_redirects\n"
    caddyfile+="    skip_install_trust\n"
    caddyfile+="}\n"

    while IFS= read -r route_name; do
        [[ -z "$route_name" ]] && continue

        local host_value service_value port_value backends_value

        host_value=$(echo "$route_facts" | jq -r \
            --arg key "desired/route/${route_name}/host" \
            '.[] | select(.key == $key) | .value')

        service_value=$(echo "$route_facts" | jq -r \
            --arg key "desired/route/${route_name}/service" \
            '.[] | select(.key == $key) | .value')

        port_value=$(echo "$route_facts" | jq -r \
            --arg key "desired/route/${route_name}/port" \
            '.[] | select(.key == $key) | .value // empty')

        backends_value=$(echo "$derived_facts" | jq -r \
            --arg key "derived/route/${route_name}/backends" \
            '.[] | select(.key == $key) | .value // empty')

        if [[ -z "$host_value" ]] || [[ -z "$service_value" ]]; then
            log "skipping route ${route_name}: missing host or service"
            continue
        fi

        if [[ -z "$backends_value" ]]; then
            log "skipping route ${route_name}: no backends resolved for service ${service_value}"
            continue
        fi

        local upstream_list=""
        IFS=',' read -ra backend_addrs <<< "$backends_value"
        for addr in "${backend_addrs[@]}"; do
            upstream_list+=" ${addr}"
        done

        caddyfile+="\n${host_value} {\n"
        caddyfile+="    tls internal {\n"
        caddyfile+="        on_demand\n"
        caddyfile+="    }\n"
        caddyfile+="    reverse_proxy${upstream_list}\n"
        caddyfile+="}\n"

    done <<< "$route_names"

    echo -e "$caddyfile"
}

# ensure_overlay_routes adds host routes for each node's overlay subnet so the
# testbed host can reach container IPs directly. Node addresses are at
# observed/node/{id}/address, subnets at network/node/{id}/subnet.
ensure_overlay_routes() {
    local address_facts subnet_facts
    address_facts=$(query_api "/api/state?prefix=observed/node/") || { log "WARNING: failed to query node addresses"; return 0; }
    subnet_facts=$(query_api "/api/state?prefix=network/node/") || { log "WARNING: failed to query node subnets"; return 0; }

    local node_ids
    node_ids=$(echo "$address_facts" | jq -r '.[].key' | sed 's|observed/node/||' | cut -d/ -f1 | sort -u)

    while IFS= read -r node_id; do
        [[ -z "$node_id" ]] && continue

        local node_address subnet
        node_address=$(echo "$address_facts" | jq -r \
            --arg key "observed/node/${node_id}/address" \
            '.[] | select(.key == $key) | .value // empty')

        subnet=$(echo "$subnet_facts" | jq -r \
            --arg key "network/node/${node_id}/subnet" \
            '.[] | select(.key == $key) | .value // empty')

        if [[ -z "$node_address" ]] || [[ -z "$subnet" ]]; then
            continue
        fi

        if ! ip route show "$subnet" 2>/dev/null | grep -q "via ${node_address}"; then
            log "adding overlay route: ${subnet} via ${node_address}"
            sudo ip route replace "$subnet" via "$node_address" 2>/dev/null || \
                log "WARNING: failed to add route ${subnet} via ${node_address}"
        fi
    done <<< "$node_ids"
}

# reconcile_once runs one sync cycle: build Caddyfile, diff, reload if changed.
reconcile_once() {
    ensure_overlay_routes

    local new_caddyfile
    new_caddyfile=$(build_caddyfile) || return 1

    if [[ -z "$new_caddyfile" ]]; then
        return 0
    fi

    local current_caddyfile=""
    if [[ -f "$CADDY_FILE" ]]; then
        current_caddyfile=$(cat "$CADDY_FILE")
    fi

    if [[ "$new_caddyfile" == "$current_caddyfile" ]]; then
        return 0
    fi

    log "Caddyfile changed — reloading"
    echo "$new_caddyfile" | sudo tee "$CADDY_FILE" > /dev/null
    sudo caddy reload --config "$CADDY_FILE" 2>/dev/null || \
        sudo systemctl reload caddy 2>/dev/null || \
        log "WARNING: caddy reload failed"
}

# --- Main ---

if [[ "${1:-}" == "--once" ]]; then
    reconcile_once
    exit $?
fi

log "starting reconciliation loop (interval: ${SYNC_INTERVAL}s)"
log "API: ${CCA_API}"

while true; do
    reconcile_once || log "WARNING: reconciliation cycle failed"
    sleep "$SYNC_INTERVAL"
done
