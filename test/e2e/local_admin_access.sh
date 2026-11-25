#!/usr/bin/env bash
# local_admin_access.sh — admin access to the cluster API for the e2e and
# deploy scripts (M280 CR-H1, user decision option c). Sourced, not run.
#
# No certificate is an admin credential. Admin calls run `sudo cca …` on a
# control-plane host, which authenticates with the root-only local token that
# cca server writes at every start (LocalTokenAuthenticator, user:local-admin).
# Where no cca command returns what a check needs (the raw /api/status JSON,
# /api/state, the agent debug proxy), local_admin_curl_remote_command builds a
# curl for that host which reads the token inside the remote command and hands
# it to curl on stdin: the token is never printed and never in an argv.
#
# A replica's local token is valid only on that replica, so the URL must name
# the host the command runs on, never a load balancer.

LOCAL_ADMIN_TOKEN_FILE="${LOCAL_ADMIN_TOKEN_FILE:-/etc/ccattler/local-token}" # root-only token cca server writes at start
# CA candidates in the CLI's order (installedClusterCACandidatePaths): M280
# names first, then the old clusters' ca.pem. Space-separated paths.
LOCAL_ADMIN_CA_CANDIDATES="${LOCAL_ADMIN_CA_CANDIDATES:-/etc/ccattler/cluster-identity-ca.pem /etc/ccattler/pki/cluster-identity-ca.pem /etc/ccattler/ca.pem /etc/ccattler/pki/ca.pem}"

# local_admin_curl_remote_command prints a command for a control-plane host:
# sudo curl with the host's local admin token (read there, sent on stdin with
# -H @-) and the first installed cluster CA. Arguments are curl's.
local_admin_curl_remote_command() {
    # $1 token file, $2 CA candidates, then curl's arguments; POSIX sh, one line.
    # shellcheck disable=SC2016 # expanded by the remote sh, not here
    local remote_script='token_file=$1; ca_candidates=$2; shift 2; [ -r "$token_file" ] || { echo "local admin token $token_file not readable (is cca server running?)" >&2; exit 2; }; for ca_candidate in $ca_candidates; do if [ -r "$ca_candidate" ]; then set -- --cacert "$ca_candidate" "$@"; break; fi; done; printf "Authorization: Bearer %s\n" "$(cat "$token_file")" | curl -H @- "$@"'
    local quoted_arguments
    printf -v quoted_arguments ' %q' "$LOCAL_ADMIN_TOKEN_FILE" "$LOCAL_ADMIN_CA_CANDIDATES" "$@"
    printf 'sudo sh -c %q local-admin-curl%s' "$remote_script" "$quoted_arguments"
}

# local_admin_cca_remote_command prints `sudo cca <arguments>` for a
# control-plane host, pinned to api_url (that host's own replica). The CLI
# reads the local token itself.
local_admin_cca_remote_command() {
    local api_url="$1"; shift
    local quoted_arguments
    printf -v quoted_arguments ' %q' "$@"
    printf 'sudo env CCA_API=%q /usr/local/bin/cca%s' "$api_url" "$quoted_arguments"
}
