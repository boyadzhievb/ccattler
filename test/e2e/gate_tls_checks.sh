#!/usr/bin/env bash
# gate_tls_checks.sh — M170 gate TLS end-to-end checks (T1–T15).
#
# Sourced by cluster_test.sh after the gate-tls workload converges. Uses its
# helpers (log, fail, ssh_vm, scp_to_vm) and variables (CTRL_NODE, VM_NAMES,
# NODE_IPS, CURL_TLS, CCA_API, WORKLOADS_DIR) plus gate-tls.workload's
# GATE_TLS_CERT_DIR and gate_tls_generate_manual_cert.
#
# All HTTPS requests run on the control node against a node's :443 listener
# with --resolve, so SNI and Host match the gate host. Plan and test IDs:
# design/milestones/m170-gate-tls-e2e.md.

GATE_TLS_CA="/etc/ccattler/pki/ca.pem"        # cluster CA that signs auto gate certs
GATE_TLS_PORT=443                              # agent --proxy-tls-listen port
GATE_HTTP_PORT=8080                            # agent --proxy-listen port (Ansible proxy_port)
GATE_TLS_ISSUE_TIMEOUT=90                      # seconds for RouteTLSController to issue certs
GATE_TLS_CONVERGE_TIMEOUT=30                   # seconds for agents to serve a changed gate
GATE_TLS_RECOVERY_TIMEOUT=90                   # seconds to recover after restarts/failures
GATE_TLS_AUTO_GATES=("tls-auto" "tls-auto-alt" "tls-sticky")
GATE_TLS_APPLIED_CCA="/tmp/gate-tls.cca"       # where cluster_test.sh copied the workload

# gate_tls_wait_until retries a command until it succeeds or timeout seconds pass.
gate_tls_wait_until() {
    local timeout="$1" description="$2"; shift 2
    local deadline=$((SECONDS + timeout))
    while [[ $SECONDS -lt $deadline ]]; do
        if "$@"; then
            return 0
        fi
        sleep 3
    done
    fail "gate-tls: timed out after ${timeout}s waiting for: $description"
}

# gate_tls_curl runs curl on the control node against node_ip:443 for host.
# Extra curl arguments follow; prints curl's output.
gate_tls_curl() {
    local node_ip="$1" gate_host="$2"; shift 2
    ssh_vm "$CTRL_NODE" "curl -s --max-time 5 --resolve ${gate_host}:${GATE_TLS_PORT}:${node_ip} $* https://${gate_host}/" 2>/dev/null
}

# gate_tls_status prints the HTTPS status code for host via node_ip (000 on failure).
gate_tls_status() {
    local node_ip="$1" gate_host="$2"; shift 2
    gate_tls_curl "$node_ip" "$gate_host" -o /dev/null -w "'%{http_code}'" "$@" || echo "000"
}

# gate_tls_fingerprint prints the SHA-256 fingerprint of the cert served for host.
gate_tls_fingerprint() {
    local node_ip="$1" gate_host="$2"
    ssh_vm "$CTRL_NODE" "echo | openssl s_client -connect ${node_ip}:${GATE_TLS_PORT} -servername ${gate_host} 2>/dev/null \
        | openssl x509 -noout -fingerprint -sha256 2>/dev/null" 2>/dev/null | cut -d= -f2 | tr -d '\r' || true
}

# gate_tls_file_fingerprint prints the fingerprint of the manual cert file on the control node.
gate_tls_file_fingerprint() {
    ssh_vm "$CTRL_NODE" "sudo openssl x509 -in ${GATE_TLS_CERT_DIR}/manual.pem -noout -fingerprint -sha256" 2>/dev/null | cut -d= -f2 | tr -d '\r' || true
}

# gate_tls_gates_json prints `cca get gates -o json` from the control node.
gate_tls_gates_json() {
    ssh_vm "$CTRL_NODE" "sudo /usr/local/bin/cca get gates -o json" 2>/dev/null || true
}

# gate_tls_all_certs_issued succeeds when every TLS gate reports a certificate.
gate_tls_all_certs_issued() {
    local pending
    pending=$(gate_tls_gates_json | jq '[.[] | select(.tls != null and .tls.certificate_set != true)] | length' 2>/dev/null || true)
    [[ "$pending" == "0" ]]
}

# gate_tls_https_ok succeeds when host answers 200 with a whoami body via node_ip.
gate_tls_https_ok() {
    local node_ip="$1" gate_host="$2"
    gate_tls_curl "$node_ip" "$gate_host" --cacert "$GATE_TLS_CA" | grep -q "^Hostname:"
}

# gate_tls_fingerprint_is succeeds when host currently serves the expected fingerprint.
gate_tls_fingerprint_is() {
    local node_ip="$1" gate_host="$2" expected_fingerprint="$3"
    [[ -n "$expected_fingerprint" && "$(gate_tls_fingerprint "$node_ip" "$gate_host")" == "$expected_fingerprint" ]]
}

# gate_tls_reapply copies a .cca file to the control node and applies it.
gate_tls_reapply() {
    local local_cca="$1"
    scp_to_vm "$CTRL_NODE" "$local_cca" "$GATE_TLS_APPLIED_CCA"
    ssh_vm "$CTRL_NODE" "sudo /usr/local/bin/cca apply --values /tmp/e2e-values.ccv $GATE_TLS_APPLIED_CCA" \
        || fail "gate-tls: cca apply of $local_cca failed"
}

# gate_tls_check_issuance covers T1: auto certs issued with ~90-day expiry.
gate_tls_check_issuance() {
    log "  T1: waiting for gate certificates..."
    gate_tls_wait_until "$GATE_TLS_ISSUE_TIMEOUT" "all TLS gates to have certificates" gate_tls_all_certs_issued
    local gates_json gate_name days_left
    gates_json=$(gate_tls_gates_json)
    for gate_name in "${GATE_TLS_AUTO_GATES[@]}"; do
        days_left=$(echo "$gates_json" | jq --arg name "$gate_name" \
            '.[] | select(.name == $name) | ((.tls.not_after | fromdateiso8601) - now) / 86400 | floor')
        [[ "$days_left" -ge 85 && "$days_left" -le 90 ]] \
            || fail "gate-tls T1: $gate_name expires in ${days_left} days, expected ~90"
    done
    log "  T1 PASS: auto certificates issued, ~90 days validity"
}

# gate_tls_check_https_every_node covers T2: HTTPS 200 through every node.
gate_tls_check_https_every_node() {
    local vm
    for vm in "${VM_NAMES[@]}"; do
        gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "HTTPS tls-auto via $vm" \
            gate_tls_https_ok "${NODE_IPS[$vm]}" "tls-auto.home.arpa"
    done
    log "  T2 PASS: https://tls-auto.home.arpa answers on all ${#VM_NAMES[@]} nodes (cluster CA trusted)"
}

# gate_tls_check_identity covers T3: SAN, CA chain, and describe agree.
gate_tls_check_identity() {
    local s_client_output describe_output
    s_client_output=$(ssh_vm "$CTRL_NODE" "echo | openssl s_client -connect ${CTRL_IP}:${GATE_TLS_PORT} \
        -servername tls-auto.home.arpa -CAfile $GATE_TLS_CA 2>/dev/null; echo | openssl s_client \
        -connect ${CTRL_IP}:${GATE_TLS_PORT} -servername tls-auto.home.arpa 2>/dev/null \
        | openssl x509 -noout -ext subjectAltName 2>/dev/null" 2>/dev/null || true)
    echo "$s_client_output" | grep -q "Verify return code: 0" || fail "gate-tls T3: certificate does not verify against the cluster CA"
    echo "$s_client_output" | grep -q "DNS:tls-auto.home.arpa" || fail "gate-tls T3: SAN does not contain tls-auto.home.arpa"
    describe_output=$(ssh_vm "$CTRL_NODE" "sudo /usr/local/bin/cca describe gate tls-auto" 2>/dev/null || true)
    echo "$describe_output" | grep -q "Host:  *tls-auto.home.arpa" || fail "gate-tls T3: describe gate does not show certificate host"
    log "  T3 PASS: SAN = tls-auto.home.arpa, issued by cluster CA, matches describe"
}

# gate_tls_check_manual_cert covers T4: the served manual cert is the generated one.
gate_tls_check_manual_cert() {
    local expected_fingerprint
    expected_fingerprint=$(gate_tls_file_fingerprint)
    gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "manual certificate served" \
        gate_tls_fingerprint_is "$CTRL_IP" "tls-manual.home.arpa" "$expected_fingerprint"
    log "  T4 PASS: tls-manual serves the generated certificate"
}

# gate_tls_check_round_robin covers T6: requests spread over both instances.
gate_tls_check_round_robin() {
    local distinct_backends
    distinct_backends=$(ssh_vm "$CTRL_NODE" "for i in \$(seq 1 20); do curl -s --max-time 5 --cacert $GATE_TLS_CA \
        --resolve tls-auto.home.arpa:${GATE_TLS_PORT}:${CTRL_IP} https://tls-auto.home.arpa/ | grep '^Hostname:'; done \
        | sort -u | wc -l" 2>/dev/null | tr -d ' \r' || true)
    [[ "$distinct_backends" -ge 2 ]] || fail "gate-tls T6: 20 requests reached $distinct_backends backend(s), expected 2"
    log "  T6 PASS: requests spread across $distinct_backends instances"
}

# gate_tls_check_sticky covers T7: Secure sticky cookie pins requests over HTTPS.
gate_tls_check_sticky() {
    local sticky_output
    sticky_output=$(ssh_vm "$CTRL_NODE" "rm -f /tmp/gate-tls-jar; curl -s --max-time 5 --cacert $GATE_TLS_CA \
        --resolve tls-sticky.home.arpa:${GATE_TLS_PORT}:${CTRL_IP} -c /tmp/gate-tls-jar -D - -o /dev/null \
        https://tls-sticky.home.arpa/ | grep -i '^set-cookie:'; for i in \$(seq 1 10); do curl -s --max-time 5 \
        --cacert $GATE_TLS_CA --resolve tls-sticky.home.arpa:${GATE_TLS_PORT}:${CTRL_IP} -b /tmp/gate-tls-jar \
        https://tls-sticky.home.arpa/ | grep '^Hostname:'; done | sort -u | sed 's/^/HOST /'" 2>/dev/null || true)
    echo "$sticky_output" | grep -qi "set-cookie: __CCA_STICKY_tls-sticky=.*Secure" \
        || fail "gate-tls T7: expected Secure __CCA_STICKY_tls-sticky cookie, got: $(echo "$sticky_output" | head -1)"
    [[ "$(echo "$sticky_output" | grep -c '^HOST ')" -eq 1 ]] || fail "gate-tls T7: sticky requests reached more than one instance"
    log "  T7 PASS: Secure sticky cookie pins 10 requests to one instance"
}

# gate_tls_check_two_gates covers T8: two gates on one service port, own cert each.
gate_tls_check_two_gates() {
    local primary_fingerprint alternate_fingerprint
    gate_tls_https_ok "$CTRL_IP" "tls-auto-alt.home.arpa" || fail "gate-tls T8: tls-auto-alt.home.arpa does not answer"
    primary_fingerprint=$(gate_tls_fingerprint "$CTRL_IP" "tls-auto.home.arpa")
    alternate_fingerprint=$(gate_tls_fingerprint "$CTRL_IP" "tls-auto-alt.home.arpa")
    [[ -n "$primary_fingerprint" && "$primary_fingerprint" != "$alternate_fingerprint" ]] \
        || fail "gate-tls T8: both gates served the same certificate"
    log "  T8 PASS: tls-auto and tls-auto-alt each serve their own certificate"
}

# gate_tls_check_service_vip covers T9: the service VIP still speaks plain HTTP.
gate_tls_check_service_vip() {
    local service_vip vip_status
    service_vip=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null \
        | jq -r '.networking[] | select(.service == "tls-auto") | .vip' 2>/dev/null || true)
    [[ -n "$service_vip" && "$service_vip" != "null" ]] || fail "gate-tls T9: no VIP for tls-auto"
    vip_status=$(ssh_vm "$CTRL_NODE" "curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://${service_vip}:80/" 2>/dev/null || true)
    [[ "$vip_status" == "200" ]] || fail "gate-tls T9: plain HTTP to service VIP returned $vip_status, expected 200"
    log "  T9 PASS: service VIP ${service_vip}:80 still serves plain HTTP"
}

# gate_tls_check_redirect covers T10: plain gate listener redirects to https.
gate_tls_check_redirect() {
    local redirect_result
    redirect_result=$(ssh_vm "$CTRL_NODE" "curl -s -o /dev/null -w '%{http_code} %{redirect_url}' --max-time 5 \
        -H 'Host: tls-auto.home.arpa' 'http://${CTRL_IP}:${GATE_HTTP_PORT}/path?query=1'" 2>/dev/null || true)
    [[ "$redirect_result" == "308 https://tls-auto.home.arpa/path?query=1" ]] \
        || fail "gate-tls T10: expected 308 to https://tls-auto.home.arpa/path?query=1, got '$redirect_result'"
    log "  T10 PASS: plain listener 308-redirects to https"
}

# gate_tls_check_rejections covers T11: unknown SNI fails, SNI/Host mismatch is 421.
gate_tls_check_rejections() {
    local unknown_status mismatch_status
    unknown_status=$(gate_tls_status "$CTRL_IP" "unknown.home.arpa" -k)
    [[ "$unknown_status" == "000" ]] || fail "gate-tls T11: unknown SNI got HTTP $unknown_status, expected handshake failure"
    mismatch_status=$(gate_tls_status "$CTRL_IP" "tls-auto.home.arpa" --cacert "$GATE_TLS_CA" -H "'Host: tls-manual.home.arpa'")
    [[ "$mismatch_status" == "421" ]] || fail "gate-tls T11: SNI/Host mismatch got $mismatch_status, expected 421"
    log "  T11 PASS: unknown SNI rejected at handshake, SNI/Host mismatch → 421"
}

# gate_tls_check_no_key_leakage covers T15: no PEM in CLI output or agent logs.
gate_tls_check_no_key_leakage() {
    local vm pem_hits
    pem_hits=$(ssh_vm "$CTRL_NODE" "{ sudo /usr/local/bin/cca get gates -o json; sudo /usr/local/bin/cca describe gate tls-auto; \
        sudo /usr/local/bin/cca describe gate tls-manual; } | grep -c 'BEGIN'" 2>/dev/null | tr -d ' \r' || true)
    [[ "$pem_hits" == "0" ]] || fail "gate-tls T15: PEM material found in CLI output"
    for vm in "${VM_NAMES[@]}"; do
        pem_hits=$(ssh_vm "$vm" "sudo journalctl -u cca-agent --since '-60min' --no-pager | grep -c 'BEGIN'" 2>/dev/null | tr -d ' \r' || true)
        [[ "${pem_hits:-0}" == "0" ]] || fail "gate-tls T15: PEM material found in $vm agent log"
    done
    log "  T15 PASS: no PEM material in get/describe output or agent logs"
}

# gate_tls_check_rotation covers T5: manual cert rotation without failed requests.
gate_tls_check_rotation() {
    local old_fingerprint new_fingerprint total_requests failed_requests
    old_fingerprint=$(gate_tls_fingerprint "$CTRL_IP" "tls-manual.home.arpa")
    ssh_vm "$CTRL_NODE" "rm -f /tmp/gate-tls-rotation.log; nohup bash -c 'for i in \$(seq 1 45); do curl -sk -o /dev/null \
        -w \"%{http_code}\n\" --max-time 3 --resolve tls-manual.home.arpa:${GATE_TLS_PORT}:${CTRL_IP} \
        https://tls-manual.home.arpa/ >> /tmp/gate-tls-rotation.log; sleep 1; done' >/dev/null 2>&1 &" 2>/dev/null
    gate_tls_generate_manual_cert
    new_fingerprint=$(gate_tls_file_fingerprint)
    [[ "$new_fingerprint" != "$old_fingerprint" ]] || fail "gate-tls T5: regenerated certificate has the same fingerprint"
    ssh_vm "$CTRL_NODE" "sudo /usr/local/bin/cca apply --values /tmp/e2e-values.ccv $GATE_TLS_APPLIED_CCA" >/dev/null \
        || fail "gate-tls T5: re-apply after rotation failed"
    gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "rotated certificate served" \
        gate_tls_fingerprint_is "$CTRL_IP" "tls-manual.home.arpa" "$new_fingerprint"
    sleep 47
    total_requests=$(ssh_vm "$CTRL_NODE" "wc -l < /tmp/gate-tls-rotation.log" 2>/dev/null | tr -d ' \r' || true)
    [[ "${total_requests:-0}" -ge 40 ]] || fail "gate-tls T5: background loop logged ${total_requests:-0} requests, expected ~45"
    failed_requests=$(ssh_vm "$CTRL_NODE" "grep -vc '^200$' /tmp/gate-tls-rotation.log" 2>/dev/null | tr -d ' \r' || true)
    [[ "${failed_requests:-0}" == "0" ]] || fail "gate-tls T5: $failed_requests request(s) failed during rotation"
    log "  T5 PASS: rotated certificate served, 0 failed requests during rotation"
}

# gate_tls_check_restart_stability covers T12: restarts keep the same auto cert.
gate_tls_check_restart_stability() {
    local fingerprint_before worker_ip="${NODE_IPS[$WORKER1_NODE]}"
    fingerprint_before=$(gate_tls_fingerprint "$CTRL_IP" "tls-auto.home.arpa")
    ssh_vm "$CTRL_NODE" "sudo systemctl restart cca-server" || fail "gate-tls T12: cca-server restart failed"
    ssh_vm "$WORKER1_NODE" "sudo systemctl restart cca-agent" || fail "gate-tls T12: cca-agent restart failed"
    gate_tls_wait_until "$GATE_TLS_RECOVERY_TIMEOUT" "HTTPS via restarted $WORKER1_NODE" \
        gate_tls_https_ok "$worker_ip" "tls-auto.home.arpa"
    [[ "$(gate_tls_fingerprint "$worker_ip" "tls-auto.home.arpa")" == "$fingerprint_before" ]] \
        || fail "gate-tls T12: certificate changed after restarts (re-issue churn)"
    log "  T12 PASS: same certificate after cca-server and agent restarts, traffic recovered"
}

# gate_tls_running_instances prints the number of running instances of a service.
gate_tls_running_instances() {
    curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null \
        | jq --arg service "$1" '[.instances[] | select(.service == $service and .state == "running")] | length'
}

# gate_tls_running_at_least succeeds when a service has at least count running instances.
gate_tls_running_at_least() {
    local running_count
    running_count=$(gate_tls_running_instances "$1")
    [[ "${running_count:-0}" -ge "$2" ]]
}

# gate_tls_check_backend_failure covers T13: HTTPS survives killing one instance.
gate_tls_check_backend_failure() {
    local victim_json victim_id victim_node
    victim_json=$(curl -sf $CURL_TLS -H "Accept: application/json" "${CCA_API}/status" 2>/dev/null \
        | jq -c '[.instances[] | select(.service == "tls-auto" and .state == "running")][0]' || true)
    victim_id=$(echo "$victim_json" | jq -r '.id')
    victim_node=$(echo "$victim_json" | jq -r '.node')
    [[ -n "$victim_id" && "$victim_id" != "null" ]] || fail "gate-tls T13: no running tls-auto instance"
    ssh_vm "$victim_node" "sudo ctr -n ccattler tasks kill -s SIGKILL tls-auto-${victim_id}" \
        || fail "gate-tls T13: could not kill tls-auto-${victim_id} on $victim_node"
    gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "HTTPS after killing an instance" \
        gate_tls_https_ok "$CTRL_IP" "tls-auto.home.arpa"
    gate_tls_wait_until "$GATE_TLS_RECOVERY_TIMEOUT" "tls-auto back to 2 running instances" \
        gate_tls_running_at_least tls-auto 2
    log "  T13 PASS: HTTPS kept answering after killing tls-auto-${victim_id}; instance replaced"
}

# gate_tls_manual_handshake_fails succeeds when the tls-manual handshake fails.
gate_tls_manual_handshake_fails() {
    [[ "$(gate_tls_status "$CTRL_IP" "tls-manual.home.arpa" -k)" == "000" ]]
}

# gate_tls_check_removal covers T14: removing tls stops HTTPS, plain HTTP serves.
gate_tls_check_removal() {
    local without_tls_cca plain_status
    without_tls_cca=$(mktemp)
    awk '/^gate tls-manual /{in_gate=1} in_gate && /^    tls \{/{skip=1} !skip{print} skip && /^    \}/{skip=0} /^\}/{in_gate=0}' \
        "$WORKLOADS_DIR/gate-tls.cca" > "$without_tls_cca"
    grep -q 'manual.pem' "$without_tls_cca" && fail "gate-tls T14: could not strip tls block from tls-manual"
    gate_tls_reapply "$without_tls_cca"
    gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "tls-manual HTTPS to stop" gate_tls_manual_handshake_fails
    [[ "$(gate_tls_gates_json | jq '.[] | select(.name == "tls-manual") | .tls')" == "null" ]] \
        || fail "gate-tls T14: cca get gates still shows TLS for tls-manual"
    plain_status=$(ssh_vm "$CTRL_NODE" "curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
        -H 'Host: tls-manual.home.arpa' http://${CTRL_IP}:${GATE_HTTP_PORT}/" 2>/dev/null || true)
    [[ "$plain_status" == "200" ]] || fail "gate-tls T14: plain HTTP for tls-manual returned $plain_status, expected 200"
    gate_tls_reapply "$WORKLOADS_DIR/gate-tls.cca"
    rm -f "$without_tls_cca"
    log "  T14 PASS: removing tls stops HTTPS, plain HTTP serves without redirect (original re-applied)"
}

# run_gate_tls_checks runs T1–T15, read-only checks first, disruptive ones last.
run_gate_tls_checks() {
    log "Validating gate TLS (M170, node :${GATE_TLS_PORT} listener)..."
    gate_tls_check_issuance
    gate_tls_check_https_every_node
    gate_tls_check_identity
    gate_tls_check_manual_cert
    gate_tls_check_round_robin
    gate_tls_check_sticky
    gate_tls_check_two_gates
    gate_tls_check_service_vip
    gate_tls_check_redirect
    gate_tls_check_rejections
    gate_tls_check_no_key_leakage
    gate_tls_check_rotation
    gate_tls_check_restart_stability
    gate_tls_check_backend_failure
    gate_tls_check_removal
    log "Gate TLS checks PASSED (T1–T15)"
}
