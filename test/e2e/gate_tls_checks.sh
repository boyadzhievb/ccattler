#!/usr/bin/env bash
# gate_tls_checks.sh — gate TLS end-to-end checks: M170 (T1–T15),
# M176 gate-key sealing and delivery (T16–T18) and M280 gate certificates are no clients (T19).
#
# Sourced by cluster_test.sh after the gate-tls workload converges. Uses its
# helpers (log, fail, ssh_vm, scp_to_vm) and variables (CTRL_NODE, VM_NAMES,
# NODE_IPS, CCA_API, WORKLOADS_DIR, control_plane_api_curl) plus gate-tls.workload's
# GATE_TLS_CERT_DIR and gate_tls_generate_manual_cert.
#
# All HTTPS requests run on the control node against a node's :443 listener
# with --resolve, so SNI and Host match the gate host. Single-node checks
# target GATE_TLS_NODE: the control node when it runs an agent (deploy
# cluster), else the first agent node (bare metal: control plane only, no
# gate listener). Plan and test IDs: design/milestones/m170-gate-tls-e2e.md.

# Gate HTTPS CA that signs auto gate certs (M280; ca.pem on a cluster from
# before M280); a shell expression evaluated on the control node.
GATE_TLS_CA="$(pki_file_remote_expression gate-https-ca.pem ca.pem)"
GATE_TLS_PORT=443                              # agent --proxy-tls-listen port
GATE_HTTP_PORT=8080                            # agent --proxy-listen port (Ansible proxy_port)
GATE_TLS_ISSUE_TIMEOUT=90                      # seconds for RouteTLSController to issue certs
GATE_TLS_CONVERGE_TIMEOUT=30                   # seconds for agents to serve a changed gate
GATE_TLS_ROTATION_REQUESTS=45                  # requests the T5 background loop sends during rotation
GATE_TLS_ROTATION_LOOP_TIMEOUT=$((GATE_TLS_ROTATION_REQUESTS * 4 + 10))  # each request: up to 3s curl + 1s sleep
GATE_TLS_RECOVERY_TIMEOUT=90                   # seconds to recover after restarts/failures
GATE_TLS_AUTO_GATES=("tls-auto" "tls-auto-alt" "tls-sticky")
GATE_TLS_APPLIED_CCA="/tmp/gate-tls.cca"       # where cluster_test.sh copied the workload
GATE_TLS_NODE=""                               # agent node whose gate listener single-node checks target
GATE_TLS_NODE_IP=""                            # its IP (set by gate_tls_select_node)
GATE_TLS_AGENT_NODES=()                        # nodes running cca-agent, i.e. serving gates (T2)
GATE_TLS_CACHED_PAIR_DIR="/run/ccattler/gate-keys/tls-auto" # agent's root-only tmpfs copy of tls-auto's pair (T19)
GATE_TLS_HANDSHAKE_TIMEOUT=5                   # seconds per T19 handshake probe

# gate_tls_select_node fills GATE_TLS_AGENT_NODES and picks GATE_TLS_NODE,
# preferring the control node so the deploy cluster keeps its old target.
gate_tls_select_node() {
    local vm
    for vm in "${VM_NAMES[@]}"; do
        vm_has_active_agent "$vm" && GATE_TLS_AGENT_NODES+=("$vm")
    done
    [[ ${#GATE_TLS_AGENT_NODES[@]} -gt 0 ]] || fail "gate-tls: no node runs cca-agent"
    GATE_TLS_NODE="${GATE_TLS_AGENT_NODES[0]}"
    for vm in "${GATE_TLS_AGENT_NODES[@]}"; do
        [[ "$vm" == "$CTRL_NODE" ]] && GATE_TLS_NODE="$vm"
    done
    GATE_TLS_NODE_IP="${NODE_IPS[$GATE_TLS_NODE]}"
    log "  Gate listener under test: $GATE_TLS_NODE ($GATE_TLS_NODE_IP); agent nodes: ${GATE_TLS_AGENT_NODES[*]}"
}

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
# curl already prints 000 for a failed handshake while exiting non-zero, so the
# code is captured once instead of appending a fallback to it.
gate_tls_status() {
    local node_ip="$1" gate_host="$2" status_code; shift 2
    status_code=$(gate_tls_curl "$node_ip" "$gate_host" -o /dev/null -w "'%{http_code}'" "$@" || true)
    echo "${status_code:-000}"
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

# gate_tls_check_https_every_node covers T2: HTTPS 200 through every agent node.
gate_tls_check_https_every_node() {
    local vm
    for vm in "${GATE_TLS_AGENT_NODES[@]}"; do
        gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "HTTPS tls-auto via $vm" \
            gate_tls_https_ok "${NODE_IPS[$vm]}" "tls-auto.home.arpa"
    done
    log "  T2 PASS: https://tls-auto.home.arpa answers on all ${#GATE_TLS_AGENT_NODES[@]} agent nodes (cluster CA trusted)"
}

# gate_tls_check_identity covers T3: SAN, CA chain, and describe agree.
gate_tls_check_identity() {
    local s_client_output describe_output
    s_client_output=$(ssh_vm "$CTRL_NODE" "echo | openssl s_client -connect ${GATE_TLS_NODE_IP}:${GATE_TLS_PORT} \
        -servername tls-auto.home.arpa -CAfile $GATE_TLS_CA 2>/dev/null; echo | openssl s_client \
        -connect ${GATE_TLS_NODE_IP}:${GATE_TLS_PORT} -servername tls-auto.home.arpa 2>/dev/null \
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
        gate_tls_fingerprint_is "$GATE_TLS_NODE_IP" "tls-manual.home.arpa" "$expected_fingerprint"
    log "  T4 PASS: tls-manual serves the generated certificate"
}

# gate_tls_check_round_robin covers T6: requests spread over both instances.
gate_tls_check_round_robin() {
    local distinct_backends
    distinct_backends=$(ssh_vm "$CTRL_NODE" "for i in \$(seq 1 20); do curl -s --max-time 5 --cacert $GATE_TLS_CA \
        --resolve tls-auto.home.arpa:${GATE_TLS_PORT}:${GATE_TLS_NODE_IP} https://tls-auto.home.arpa/ | grep '^Hostname:'; done \
        | sort -u | wc -l" 2>/dev/null | tr -d ' \r' || true)
    [[ "$distinct_backends" -ge 2 ]] || fail "gate-tls T6: 20 requests reached $distinct_backends backend(s), expected 2"
    log "  T6 PASS: requests spread across $distinct_backends instances"
}

# gate_tls_check_sticky covers T7: Secure sticky cookie pins requests over HTTPS.
gate_tls_check_sticky() {
    local sticky_output
    sticky_output=$(ssh_vm "$CTRL_NODE" "rm -f /tmp/gate-tls-jar; curl -s --max-time 5 --cacert $GATE_TLS_CA \
        --resolve tls-sticky.home.arpa:${GATE_TLS_PORT}:${GATE_TLS_NODE_IP} -c /tmp/gate-tls-jar -D - -o /dev/null \
        https://tls-sticky.home.arpa/ | grep -i '^set-cookie:'; for i in \$(seq 1 10); do curl -s --max-time 5 \
        --cacert $GATE_TLS_CA --resolve tls-sticky.home.arpa:${GATE_TLS_PORT}:${GATE_TLS_NODE_IP} -b /tmp/gate-tls-jar \
        https://tls-sticky.home.arpa/ | grep '^Hostname:'; done | sort -u | sed 's/^/HOST /'" 2>/dev/null || true)
    echo "$sticky_output" | grep -qi "set-cookie: __CCA_STICKY_tls-sticky=.*Secure" \
        || fail "gate-tls T7: expected Secure __CCA_STICKY_tls-sticky cookie, got: $(echo "$sticky_output" | head -1)"
    [[ "$(echo "$sticky_output" | grep -c '^HOST ')" -eq 1 ]] || fail "gate-tls T7: sticky requests reached more than one instance"
    log "  T7 PASS: Secure sticky cookie pins 10 requests to one instance"
}

# gate_tls_check_two_gates covers T8: two gates on one service port, own cert each.
gate_tls_check_two_gates() {
    local primary_fingerprint alternate_fingerprint
    gate_tls_https_ok "$GATE_TLS_NODE_IP" "tls-auto-alt.home.arpa" || fail "gate-tls T8: tls-auto-alt.home.arpa does not answer"
    primary_fingerprint=$(gate_tls_fingerprint "$GATE_TLS_NODE_IP" "tls-auto.home.arpa")
    alternate_fingerprint=$(gate_tls_fingerprint "$GATE_TLS_NODE_IP" "tls-auto-alt.home.arpa")
    [[ -n "$primary_fingerprint" && "$primary_fingerprint" != "$alternate_fingerprint" ]] \
        || fail "gate-tls T8: both gates served the same certificate"
    log "  T8 PASS: tls-auto and tls-auto-alt each serve their own certificate"
}

# gate_tls_check_service_vip covers T9: the service VIP still speaks plain HTTP.
gate_tls_check_service_vip() {
    local service_vip vip_status
    service_vip=$(control_plane_api_curl -sf -H "Accept: application/json" "${CCA_API}/api/status" 2>/dev/null \
        | jq -r '.networking[] | select(.service == "tls-auto") | .vip' 2>/dev/null || true)
    [[ -n "$service_vip" && "$service_vip" != "null" ]] || fail "gate-tls T9: no VIP for tls-auto"
    vip_status=$(ssh_vm "$GATE_TLS_NODE" "curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://${service_vip}:80/" 2>/dev/null || true)
    [[ "$vip_status" == "200" ]] || fail "gate-tls T9: plain HTTP to service VIP returned $vip_status, expected 200"
    log "  T9 PASS: service VIP ${service_vip}:80 still serves plain HTTP"
}

# gate_tls_check_redirect covers T10: plain gate listener redirects to https.
gate_tls_check_redirect() {
    local redirect_result
    redirect_result=$(ssh_vm "$CTRL_NODE" "curl -s -o /dev/null -w '%{http_code} %{redirect_url}' --max-time 5 \
        -H 'Host: tls-auto.home.arpa' 'http://${GATE_TLS_NODE_IP}:${GATE_HTTP_PORT}/path?query=1'" 2>/dev/null || true)
    [[ "$redirect_result" == "308 https://tls-auto.home.arpa/path?query=1" ]] \
        || fail "gate-tls T10: expected 308 to https://tls-auto.home.arpa/path?query=1, got '$redirect_result'"
    log "  T10 PASS: plain listener 308-redirects to https"
}

# gate_tls_check_rejections covers T11: unknown SNI fails, SNI/Host mismatch is 421.
gate_tls_check_rejections() {
    local unknown_status mismatch_status
    unknown_status=$(gate_tls_status "$GATE_TLS_NODE_IP" "unknown.home.arpa" -k)
    [[ "$unknown_status" == "000" ]] || fail "gate-tls T11: unknown SNI got HTTP $unknown_status, expected handshake failure"
    mismatch_status=$(gate_tls_status "$GATE_TLS_NODE_IP" "tls-auto.home.arpa" --cacert "$GATE_TLS_CA" -H "'Host: tls-manual.home.arpa'")
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

# gate_tls_client_probe prints "<http code> <curl exit>" for a request from
# GATE_TLS_NODE to url, trusting the CA named by ca_expression, with the client
# certificate and other curl flags given (evaluated on that node). The private
# key stays in its root-only file there; only the two codes come back.
gate_tls_client_probe() {
    local url="$1" ca_expression="$2" client_certificate_flags="$3"
    ssh_vm "$GATE_TLS_NODE" "sudo curl -s -o /dev/null -w '%{http_code}' --max-time ${GATE_TLS_HANDSHAKE_TIMEOUT} \
        --cacert $ca_expression $client_certificate_flags $url; echo \" \$?\"" 2>/dev/null | tr -d '\r' || true
}

# gate_tls_handshake_refused succeeds only when the server refused the client
# certificate at the handshake: no HTTP response and curl exit 35 (TLS
# handshake failed) or 56 (connection closed by the peer, TLS 1.3 alert after
# the handshake). An unusable pair (58), an unreachable endpoint (7) or a
# timeout (28) is not a refusal (SA3-L3).
gate_tls_handshake_refused() {
    [[ "$1" == "000 35" || "$1" == "000 56" ]]
}

# gate_tls_response_received succeeds when a probe got an HTTP response (curl exit 0).
gate_tls_response_received() {
    [[ "$1" != "000 "* && "$1" == *" 0" ]]
}

# gate_tls_check_gate_cert_not_client covers T19, check deploy-run:gate-cert-not-client
# (M280 D5): tls-auto's real gate certificate and key, presented as a client
# certificate, are refused at the handshake by etcd (2379) and the API (9770),
# and so is a ClientAuth certificate from the gate CA (CA separation, SA4-L1).
# Positive controls: curl loads the same gate pair (presented to the gate
# listener :443, which does not ask for client certificates, SA3-L3), the node's
# etcd client certificate is accepted by etcd, and the API answers /healthz
# without a client certificate.
gate_tls_check_gate_cert_not_client() {
    local gate_pair_flags="--cert ${GATE_TLS_CACHED_PAIR_DIR}/certificate.pem --key ${GATE_TLS_CACHED_PAIR_DIR}/private-key.pem"
    local etcd_ca cluster_ca node_etcd_flags probe_result
    etcd_ca=$(pki_file_remote_expression etcd-access-ca.pem ca.pem)
    cluster_ca=$(pki_file_remote_expression cluster-identity-ca.pem ca.pem)
    node_etcd_flags="--cert $(pki_file_remote_expression node-etcd-client-certificate.pem node.pem)"
    node_etcd_flags+=" --key $(pki_file_remote_expression node-etcd-client-certificate-key.pem node-key.pem)"
    ssh_vm "$GATE_TLS_NODE" "sudo test -s ${GATE_TLS_CACHED_PAIR_DIR}/certificate.pem && sudo test -s ${GATE_TLS_CACHED_PAIR_DIR}/private-key.pem" \
        || fail "gate-tls T19: $GATE_TLS_NODE has no cached tls-auto pair in ${GATE_TLS_CACHED_PAIR_DIR}"
    probe_result=$(gate_tls_client_probe "https://tls-auto.home.arpa:${GATE_TLS_PORT}/" "$GATE_TLS_CA" \
        "$gate_pair_flags --resolve tls-auto.home.arpa:${GATE_TLS_PORT}:${GATE_TLS_NODE_IP}")
    gate_tls_response_received "$probe_result" || fail "gate-tls T19: curl could not use the gate pair against the gate listener (positive control: $probe_result)"
    probe_result=$(gate_tls_client_probe "https://${CTRL_IP}:2379/health" "$etcd_ca" "$node_etcd_flags")
    [[ "$probe_result" == "200 0" ]] || fail "gate-tls T19: etcd refused the node etcd client certificate (positive control: $probe_result)"
    probe_result=$(gate_tls_client_probe "https://${CTRL_IP}:2379/health" "$etcd_ca" "$gate_pair_flags")
    gate_tls_handshake_refused "$probe_result" || fail "gate-tls T19: etcd did not refuse the gate certificate at the handshake ($probe_result)"
    probe_result=$(gate_tls_client_probe "https://${CTRL_IP}:9770/healthz" "$cluster_ca" "")
    [[ "$probe_result" == "200 0" ]] || fail "gate-tls T19: API /healthz unreachable without a client certificate (positive control: $probe_result)"
    probe_result=$(gate_tls_client_probe "https://${CTRL_IP}:9770/healthz" "$cluster_ca" "$gate_pair_flags")
    gate_tls_handshake_refused "$probe_result" || fail "gate-tls T19: the API did not refuse the gate certificate at the handshake ($probe_result)"
    gate_tls_check_gate_ca_client_refused
    log "  T19 PASS (deploy-run:gate-cert-not-client): etcd and the API refuse tls-auto's gate certificate and a" \
        "ClientAuth certificate from the gate CA at the handshake; the same gate pair loads against the gate listener;" \
        "the node etcd client certificate is accepted"
}

# gate_tls_gate_ca_client_remote_command prints a command for the control node
# (one line, POSIX sh, run as root): it mints a ClientAuth certificate from the
# gate HTTPS CA in a root-only mktemp -d directory, checks it (openssl verify,
# key matches), presents it to etcd :2379 and the API :9770 and prints one
# "<http code> <curl exit>" line for each. Key and certificate are never printed
# and are removed on exit. Prints "single-ca" when the cluster has no gate CA key
# (before M280), "mint-failed" when the certificate cannot be made.
gate_tls_gate_ca_client_remote_command() {
    # $1 PKI directory, $2 control-plane IP, $3 curl timeout (seconds)
    # shellcheck disable=SC2016 # expanded by the remote sh, not here
    local remote_script='pki=$1; host=$2; timeout=$3; [ -f "$pki/gate-https-ca-key.pem" ] || { echo single-ca; exit 0; }; umask 077; work=$(mktemp -d) || exit 1; trap "rm -f \"$work\"/key.pem \"$work\"/request.pem \"$work\"/extensions.cnf \"$work\"/certificate.pem; rmdir \"$work\"" EXIT; printf "extendedKeyUsage=clientAuth\n" > "$work/extensions.cnf"; serial=$(od -An -N8 -tx1 /dev/urandom | tr -d " \n"); { openssl ecparam -name prime256v1 -genkey -noout -out "$work/key.pem" && openssl req -new -key "$work/key.pem" -subj /CN=t19-gate-ca-client -out "$work/request.pem" && openssl x509 -req -in "$work/request.pem" -CA "$pki/gate-https-ca.pem" -CAkey "$pki/gate-https-ca-key.pem" -set_serial "0x$serial" -days 1 -extfile "$work/extensions.cnf" -out "$work/certificate.pem" && openssl verify -CAfile "$pki/gate-https-ca.pem" -purpose sslclient "$work/certificate.pem" && [ "$(openssl x509 -noout -pubkey -in "$work/certificate.pem")" = "$(openssl pkey -pubout -in "$work/key.pem")" ]; } >/dev/null 2>&1 || { echo mint-failed; exit 0; }; for target in "2379/health etcd-access-ca.pem" "9770/healthz cluster-identity-ca.pem"; do set -- $target; curl -s -o /dev/null -w "%{http_code}" --max-time "$timeout" --cacert "$pki/$2" --cert "$work/certificate.pem" --key "$work/key.pem" "https://$host:$1"; echo " $?"; done'
    local quoted_arguments
    printf -v quoted_arguments ' %q' "$CLUSTER_PKI_DIRECTORY" "$CTRL_IP" "$GATE_TLS_HANDSHAKE_TIMEOUT"
    printf 'sudo sh -c %q gate-ca-client%s' "$remote_script" "$quoted_arguments"
}

# gate_tls_check_gate_ca_client_refused is the CA-separation part of T19
# (SA4-L1): a ClientAuth certificate from the gate HTTPS CA must be refused at
# the handshake (curl exit 35 or 56) by etcd and the API, so T19 does not rest
# on the real gate certificate's missing ClientAuth alone. A cluster from before
# M280 has one CA for everything (no separation to prove) and is skipped with a log line.
gate_tls_check_gate_ca_client_refused() {
    local probe_output
    probe_output=$(ssh_vm "$CTRL_NODE" "$(gate_tls_gate_ca_client_remote_command)" 2>/dev/null | tr -d '\r' || true)
    if [[ "$probe_output" == "single-ca" ]]; then
        log "  T19 SKIP (gate CA client): single-CA cluster from before M280, the gate CA is the cluster CA"
        return 0
    fi
    [[ "$probe_output" != "mint-failed" && -n "$probe_output" ]] \
        || fail "gate-tls T19: could not mint a gate-CA client certificate on $CTRL_NODE (${probe_output:-no output})"
    local etcd_result api_result
    etcd_result=$(sed -n 1p <<< "$probe_output")
    api_result=$(sed -n 2p <<< "$probe_output")
    gate_tls_handshake_refused "$etcd_result" \
        || fail "gate-tls T19: etcd did not refuse a ClientAuth certificate from the gate CA at the handshake ($etcd_result)"
    gate_tls_handshake_refused "$api_result" \
        || fail "gate-tls T19: the API did not refuse a ClientAuth certificate from the gate CA at the handshake ($api_result)"
}

# gate_tls_rotation_loop_finished reports whether the T5 background loop
# has logged all of its requests.
gate_tls_rotation_loop_finished() {
    local logged_requests
    logged_requests=$(ssh_vm "$CTRL_NODE" "wc -l < /tmp/gate-tls-rotation.log" 2>/dev/null | tr -d ' \r' || true)
    [[ "${logged_requests:-0}" -ge "$GATE_TLS_ROTATION_REQUESTS" ]]
}

# gate_tls_fact_value prints the value of one store fact via the state API
# (empty when missing or unreadable).
gate_tls_fact_value() {
    control_plane_api_curl -sf "${CCA_API}/api/state?key=$1" 2>/dev/null | jq -r '.value // empty' 2>/dev/null || true
}

# gate_tls_node_keys_ready succeeds when a node reports gate_key_delivery = v1
# and gate_keys state ready.
gate_tls_node_keys_ready() {
    local vm="$1"
    [[ "$(gate_tls_fact_value "observed/node/${vm}/gate_key_delivery")" == "v1" ]] \
        && [[ "$(gate_tls_fact_value "observed/node/${vm}/gate_keys" | jq -r '.state' 2>/dev/null)" == "ready" ]]
}

# gate_tls_migration_completed succeeds when the plaintext gate-key migration
# reports completed with no plaintext key fact left.
gate_tls_migration_completed() {
    local migration_status
    migration_status=$(gate_tls_fact_value "observed/gate-keys/migration")
    [[ "$(echo "$migration_status" | jq -r '.state' 2>/dev/null)" == "completed" ]] \
        && [[ "$(echo "$migration_status" | jq -r '.plaintext_count' 2>/dev/null)" == "0" ]]
}

# gate_tls_check_key_delivery covers T16 (M176): every agent node fetched the
# gate keys through the API, and no plaintext gate key fact is left.
gate_tls_check_key_delivery() {
    local vm
    for vm in "${GATE_TLS_AGENT_NODES[@]}"; do
        gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "gate keys ready on $vm" gate_tls_node_keys_ready "$vm"
    done
    gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "gate-key migration completed" gate_tls_migration_completed
    log "  T16 PASS: gate keys delivered to all ${#GATE_TLS_AGENT_NODES[@]} agent nodes, no plaintext key facts"
}

# gate_tls_node_without_instance prints an agent node that runs no instance of
# the service (empty when every agent node runs one).
# Fails the test when the status query fails or lists no instance of the
# service, so the check never passes on an empty answer.
gate_tls_node_without_instance() {
    local service="$1" vm hosting_nodes
    hosting_nodes=$(control_plane_api_curl -sf -H "Accept: application/json" "${CCA_API}/api/status" 2>/dev/null \
        | jq -r --arg service "$service" '.instances[] | select(.service == $service) | .node' 2>/dev/null)
    [[ -n "$hosting_nodes" ]] || fail "gate-tls T17: status lists no $service instance (query failed?)"
    for vm in "${GATE_TLS_AGENT_NODES[@]}"; do
        grep -qx "$vm" <<<"$hosting_nodes" || { echo "$vm"; return; }
    done
}

# gate_tls_check_remote_backend covers T17 (M176 acceptance B): a node with no
# local instance of the gate's service still terminates TLS for it.
gate_tls_check_remote_backend() {
    local remote_node
    remote_node=$(gate_tls_node_without_instance "tls-manual") || exit 1
    if [[ -z "$remote_node" ]]; then
        log "  T17 SKIP: every agent node runs a tls-manual instance"
        return
    fi
    gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "tls-manual HTTPS via $remote_node (no local backend)" \
        gate_tls_fingerprint_is "${NODE_IPS[$remote_node]}" "tls-manual.home.arpa" "$(gate_tls_file_fingerprint)"
    log "  T17 PASS: $remote_node (no local tls-manual instance) serves the manual certificate"
}

# gate_tls_check_cache_without_api covers T18 (M176): with cca-server stopped,
# a restarted agent serves gate TLS from its tmpfs gate-key cache.
gate_tls_check_cache_without_api() {
    local worker_ip="${NODE_IPS[$WORKER1_NODE]}"
    ssh_vm "$CTRL_NODE" "sudo systemctl stop cca-server" || gate_tls_fail_with_server_started "cca-server stop failed"
    ssh_vm "$WORKER1_NODE" "sudo systemctl restart cca-agent" || gate_tls_fail_with_server_started "cca-agent restart failed"
    gate_tls_wait_until_quiet "$GATE_TLS_RECOVERY_TIMEOUT" gate_tls_https_ok "$worker_ip" "tls-auto.home.arpa" \
        || gate_tls_fail_with_server_started "$WORKER1_NODE stopped serving TLS after an agent restart with the API down"
    ssh_vm "$CTRL_NODE" "sudo systemctl start cca-server" || fail "gate-tls T18: cca-server start failed"
    gate_tls_wait_until "$GATE_TLS_RECOVERY_TIMEOUT" "gate keys ready on $WORKER1_NODE after API recovery" \
        gate_tls_node_keys_ready "$WORKER1_NODE"
    log "  T18 PASS: restarted agent served TLS from its gate-key cache while cca-server was down"
}

# gate_tls_fail_with_server_started starts cca-server again (T18 stops it)
# and then fails T18, so a failing check never leaves the cluster without an API.
gate_tls_fail_with_server_started() {
    ssh_vm "$CTRL_NODE" "sudo systemctl start cca-server" || true
    fail "gate-tls T18: $1"
}

# gate_tls_wait_until_quiet retries a command until it succeeds or timeout
# seconds pass; returns non-zero instead of failing the test.
gate_tls_wait_until_quiet() {
    local timeout="$1"; shift
    local deadline=$((SECONDS + timeout))
    while [[ $SECONDS -lt $deadline ]]; do
        "$@" && return 0
        sleep 3
    done
    return 1
}

# gate_tls_check_rotation covers T5: manual cert rotation without failed requests.
gate_tls_check_rotation() {
    local old_fingerprint new_fingerprint total_requests failed_requests
    old_fingerprint=$(gate_tls_fingerprint "$GATE_TLS_NODE_IP" "tls-manual.home.arpa")
    ssh_vm "$CTRL_NODE" "rm -f /tmp/gate-tls-rotation.log; nohup bash -c 'for i in \$(seq 1 $GATE_TLS_ROTATION_REQUESTS); do curl -sk -o /dev/null \
        -w \"%{http_code}\n\" --max-time 3 --resolve tls-manual.home.arpa:${GATE_TLS_PORT}:${GATE_TLS_NODE_IP} \
        https://tls-manual.home.arpa/ >> /tmp/gate-tls-rotation.log; sleep 1; done' >/dev/null 2>&1 &" 2>/dev/null
    gate_tls_generate_manual_cert
    new_fingerprint=$(gate_tls_file_fingerprint)
    [[ "$new_fingerprint" != "$old_fingerprint" ]] || fail "gate-tls T5: regenerated certificate has the same fingerprint"
    ssh_vm "$CTRL_NODE" "sudo /usr/local/bin/cca apply --values /tmp/e2e-values.ccv $GATE_TLS_APPLIED_CCA" >/dev/null \
        || fail "gate-tls T5: re-apply after rotation failed"
    gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "rotated certificate served" \
        gate_tls_fingerprint_is "$GATE_TLS_NODE_IP" "tls-manual.home.arpa" "$new_fingerprint"
    # Wait for the whole loop instead of a fixed sleep: on a loaded cluster
    # each request takes longer (run 41673229739 logged 39 of 45 after 47s).
    gate_tls_wait_until "$GATE_TLS_ROTATION_LOOP_TIMEOUT" "all $GATE_TLS_ROTATION_REQUESTS rotation requests logged" \
        gate_tls_rotation_loop_finished
    total_requests=$(ssh_vm "$CTRL_NODE" "wc -l < /tmp/gate-tls-rotation.log" 2>/dev/null | tr -d ' \r' || true)
    failed_requests=$(ssh_vm "$CTRL_NODE" "grep -vc '^200$' /tmp/gate-tls-rotation.log" 2>/dev/null | tr -d ' \r' || true)
    [[ "${failed_requests:-0}" == "0" ]] || fail "gate-tls T5: $failed_requests request(s) failed during rotation"
    log "  T5 PASS: rotated certificate served, 0 failed requests during rotation"
}

# gate_tls_check_restart_stability covers T12: restarts keep the same auto cert.
gate_tls_check_restart_stability() {
    local fingerprint_before worker_ip="${NODE_IPS[$WORKER1_NODE]}"
    fingerprint_before=$(gate_tls_fingerprint "$GATE_TLS_NODE_IP" "tls-auto.home.arpa")
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
    control_plane_api_curl -sf -H "Accept: application/json" "${CCA_API}/api/status" 2>/dev/null \
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
    victim_json=$(control_plane_api_curl -sf -H "Accept: application/json" "${CCA_API}/api/status" 2>/dev/null \
        | jq -c '[.instances[] | select(.service == "tls-auto" and .state == "running")][0]' || true)
    victim_id=$(echo "$victim_json" | jq -r '.id')
    victim_node=$(echo "$victim_json" | jq -r '.node')
    [[ -n "$victim_id" && "$victim_id" != "null" ]] || fail "gate-tls T13: no running tls-auto instance"
    ssh_vm "$victim_node" "sudo ctr -n ccattler tasks kill -s SIGKILL tls-auto-${victim_id}" \
        || fail "gate-tls T13: could not kill tls-auto-${victim_id} on $victim_node"
    gate_tls_wait_until "$GATE_TLS_CONVERGE_TIMEOUT" "HTTPS after killing an instance" \
        gate_tls_https_ok "$GATE_TLS_NODE_IP" "tls-auto.home.arpa"
    gate_tls_wait_until "$GATE_TLS_RECOVERY_TIMEOUT" "tls-auto back to 2 running instances" \
        gate_tls_running_at_least tls-auto 2
    log "  T13 PASS: HTTPS kept answering after killing tls-auto-${victim_id}; instance replaced"
}

# gate_tls_manual_handshake_fails succeeds when the tls-manual handshake fails.
gate_tls_manual_handshake_fails() {
    [[ "$(gate_tls_status "$GATE_TLS_NODE_IP" "tls-manual.home.arpa" -k)" == "000" ]]
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
        -H 'Host: tls-manual.home.arpa' http://${GATE_TLS_NODE_IP}:${GATE_HTTP_PORT}/" 2>/dev/null || true)
    [[ "$plain_status" == "200" ]] || fail "gate-tls T14: plain HTTP for tls-manual returned $plain_status, expected 200"
    gate_tls_reapply "$WORKLOADS_DIR/gate-tls.cca"
    rm -f "$without_tls_cca"
    log "  T14 PASS: removing tls stops HTTPS, plain HTTP serves without redirect (original re-applied)"
}

# run_gate_tls_checks runs T1–T19, read-only checks first, disruptive ones last.
run_gate_tls_checks() {
    log "Validating gate TLS (M170, node :${GATE_TLS_PORT} listener)..."
    gate_tls_select_node
    gate_tls_check_issuance
    gate_tls_check_https_every_node
    gate_tls_check_key_delivery
    gate_tls_check_identity
    gate_tls_check_manual_cert
    gate_tls_check_remote_backend
    gate_tls_check_round_robin
    gate_tls_check_sticky
    gate_tls_check_two_gates
    gate_tls_check_service_vip
    gate_tls_check_redirect
    gate_tls_check_rejections
    gate_tls_check_no_key_leakage
    gate_tls_check_gate_cert_not_client
    gate_tls_check_rotation
    gate_tls_check_restart_stability
    gate_tls_check_cache_without_api
    gate_tls_check_backend_failure
    gate_tls_check_removal
    log "Gate TLS checks PASSED (T1–T19)"
}
