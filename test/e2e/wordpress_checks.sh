#!/usr/bin/env bash
# WordPress deploy checks (M231, spec deploy-test-runs "WordPress is a deploy
# workload"). Sourced by cluster_test.sh when the wordpress workload is
# deployed; uses its log, fail, ssh_vm, vm_has_active_agent, NODE_IPS, VM_NAMES,
# CTRL_NODE, CCA_API and CURL_TLS.

WORDPRESS_READY_TIMEOUT=300    # seconds for MariaDB, WordPress and nginx to answer (first start initialises the database)
WORDPRESS_GATE_HOST="wordpress.home.arpa"
WORDPRESS_GATE_PORT=443        # agent --proxy-tls-listen port
WORDPRESS_RETRY_SECONDS=5      # pause between attempts

# wordpress_answer_code prints the HTTP status of a request from the control
# node to url (extra curl arguments first); "000" when nothing answered.
wordpress_answer_code() {
    ssh_vm "$CTRL_NODE" "curl -s -o /dev/null -w '%{http_code}' --max-time 10 $*" 2>/dev/null || echo "000"
}

# wordpress_is_answering reports whether a status is a WordPress page: 200, or
# the 302 to wp-admin/install.php a fresh installation answers with.
wordpress_is_answering() {
    [[ "$1" == "200" || "$1" == "302" ]]
}

# wordpress_wait_for retries one request until WordPress answers or the
# timeout passes; description names what is being checked.
wordpress_wait_for() {
    local description="$1"; shift
    local deadline=$((SECONDS + WORDPRESS_READY_TIMEOUT)) answer_code="000"
    while [[ $SECONDS -lt $deadline ]]; do
        answer_code=$(wordpress_answer_code "$@")
        if wordpress_is_answering "$answer_code"; then
            log "  WordPress answers $answer_code through $description"
            return 0
        fi
        sleep "$WORDPRESS_RETRY_SECONDS"
    done
    fail "wordpress: no WordPress page through $description after ${WORDPRESS_READY_TIMEOUT}s (last HTTP status $answer_code)"
}

# run_wordpress_checks checks the site through the nginx service VIP and
# through the gate on an agent node's :443 listener.
run_wordpress_checks() {
    log "Validating WordPress (VIP and gate)..."
    local networking_json nginx_vip nginx_port gate_node=""
    networking_json=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/api/status" 2>/dev/null || echo "{}")
    nginx_vip=$(echo "$networking_json" | jq -r '.networking[]? | select(.service == "nginx") | .vip' | head -1)
    nginx_port=$(echo "$networking_json" | jq -r '.networking[]? | select(.service == "nginx") | .port' | head -1)
    [[ -n "$nginx_vip" && "$nginx_vip" != "null" ]] || fail "wordpress: service nginx has no VIP in /api/status"
    wordpress_wait_for "the nginx VIP ${nginx_vip}:${nginx_port}" "http://${nginx_vip}:${nginx_port}/"

    for vm in "${VM_NAMES[@]}"; do
        if vm_has_active_agent "$vm"; then
            gate_node="$vm"
            break
        fi
    done
    [[ -n "$gate_node" ]] || fail "wordpress: no node runs cca-agent to serve the gate"
    wordpress_wait_for "gate ${WORDPRESS_GATE_HOST} on ${gate_node}" \
        "-k --resolve ${WORDPRESS_GATE_HOST}:${WORDPRESS_GATE_PORT}:${NODE_IPS[$gate_node]} https://${WORDPRESS_GATE_HOST}/"
    log "WordPress checks PASSED"
}
