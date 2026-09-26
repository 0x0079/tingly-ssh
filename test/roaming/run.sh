#!/usr/bin/env bash
# Roaming verification against a real deployment.
#
# The physical act of switching a radio is the only manual part, and even that
# is automatic on Linux with NetworkManager and on macOS (see netctl.sh).
# Everything else is measured: stream continuity, how long the terminal froze,
# how many links were lost, whether the session resumed or failed cleanly.
#
#   cp roaming.env.example roaming.env    # fill in your deployment
#   ./run.sh                              # all cases
#   ./run.sh --quick                      # skip the long ones (R5, R6)
#   ./run.sh R1 R3                        # only the named cases
#
# Results and logs land in test/roaming/artifacts/<timestamp>/.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$HERE/../.." && pwd)
RUN_ID=$(date +%Y%m%d-%H%M%S)
ART=${ART_DIR:-$HERE/artifacts/$RUN_ID}

CONFIG=${ROAMING_ENV:-$HERE/roaming.env}
QUICK=0
SELECT=()
for arg in "$@"; do
    case $arg in
        --quick) QUICK=1 ;;
        --config) shift; CONFIG=$1 ;;
        -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
        *) SELECT+=("$arg") ;;
    esac
done
[ -f "$CONFIG" ] && . "$CONFIG"

source "$(cd "$HERE/../lib" && pwd)/common.sh"
source "$HERE/netctl.sh"

SUMMARY_TITLE="tingly-ssh roaming verification"

# --- deployment under test --------------------------------------------------
TINGLY_BIN=${TINGLY_BIN:-$REPO_ROOT/tingly-ssh}
TINGLY_SERVER=${TINGLY_SERVER:-}          # host:port of the tunnel server
TINGLY_TARGET=${TINGLY_TARGET:-127.0.0.1:22}
TINGLY_TOKEN_FILE=${TINGLY_TOKEN_FILE:-}
TINGLY_PIN=${TINGLY_PIN:-}
SSH_USER=${SSH_USER:-$(id -un)}
CLIENT_PORT=${CLIENT_PORT:-2500}
# A laptop wants a link failure noticed in seconds, not in QUIC's default 20s.
TINGLY_CLIENT_FLAGS=${TINGLY_CLIENT_FLAGS:---idle-timeout 8s --keepalive 2s}
LINGER=${LINGER:-60}
SERVER_RESTART_CMD=${SERVER_RESTART_CMD:-}

# --- thresholds -------------------------------------------------------------
SETTLE_SECONDS=${SETTLE_SECONDS:-4}        # heartbeat warm-up before the event
OBSERVE_SECONDS=${OBSERVE_SECONDS:-25}     # watch window after the event
MAX_STALL=${MAX_STALL:-20}                 # acceptable freeze on a path change
OUTAGE_SECONDS=${OUTAGE_SECONDS:-40}       # shorter than LINGER: must survive
IDLE_SECONDS=${IDLE_SECONDS:-600}          # NAT idle test
BULK_MB=${BULK_MB:-64}

require_config() {
    [ -n "$TINGLY_SERVER" ] || die "TINGLY_SERVER is not set (see roaming.env.example)"
    [ -n "$TINGLY_TOKEN_FILE" ] || die "TINGLY_TOKEN_FILE is not set"
    [ -x "$TINGLY_BIN" ] || die "TINGLY_BIN=$TINGLY_BIN is not executable"
    [ -n "$TINGLY_PIN" ] || log "${C_YEL}warning${C_OFF} no TINGLY_PIN set; see docs/04-security-model.md §2.1"
}

# --- tunnel client ----------------------------------------------------------

start_tunnel_client() {
    CLIENT_SEQ=$(( ${CLIENT_SEQ:-0} + 1 ))
    CLIENT_LOG=$ART/client-$CLIENT_SEQ.log
    # shellcheck disable=SC2086
    "$TINGLY_BIN" client \
        --server "$TINGLY_SERVER" \
        --listen "127.0.0.1:$CLIENT_PORT" \
        --target "$TINGLY_TARGET" \
        --token-file "$TINGLY_TOKEN_FILE" \
        ${TINGLY_PIN:+--pin "$TINGLY_PIN"} \
        --session-linger "${LINGER}s" \
        $TINGLY_CLIENT_FLAGS \
        --log-level info > "$CLIENT_LOG" 2>&1 &
    CLIENT_PID=$!
    track "$CLIENT_PID"
    wait_log "$CLIENT_LOG" "link established" 30 || die "client never linked to $TINGLY_SERVER"
    info "tunnel client on 127.0.0.1:$CLIENT_PORT (pid $CLIENT_PID)"
}

# restart_tunnel_client gives each case a fresh session and a fresh log, so
# link counts are per case rather than cumulative.
restart_tunnel_client() {
    [ -n "${CLIENT_PID:-}" ] && kill_quiet "$CLIENT_PID"
    start_tunnel_client
}

link_stat() { grep -c "$1" "$CLIENT_LOG" 2>/dev/null || echo 0; }

write_ssh_config() {
    cat > "$ART/ssh_config" <<EOF
Host roam
    HostName 127.0.0.1
    Port $CLIENT_PORT
    User $SSH_USER
    StrictHostKeyChecking accept-new
    UserKnownHostsFile $ART/known_hosts
    BatchMode yes
    ServerAliveInterval 0
    LogLevel ERROR
${SSH_EXTRA_CONFIG:-}
EOF
}

rssh() { timeout "${SSH_TIMEOUT:-60}" ssh -F "$ART/ssh_config" "$@"; }

# --- heartbeat --------------------------------------------------------------

# start_heartbeat ID SECONDS runs a counter on the remote host and timestamps
# every line as it arrives locally.
start_heartbeat() {
    local id=$1 secs=$2
    # Split from the line above on purpose: bash expands every word of a
    # `local` before assigning any of them, so referring to secs there would
    # be an unbound variable under `set -u`.
    local n=$(( (secs + 10) * 5 ))
    HB_FILE=$ART/$id.heartbeat
    : > "$HB_FILE"
    ( SSH_TIMEOUT=$((secs + 120)) rssh roam \
        "for i in \$(seq 1 $n); do echo \$i; sleep 0.2; done" 2>"$ART/$id.err" \
        | stamp_lines >> "$HB_FILE" ) &
    HB_PID=$!
    track "$HB_PID"
    # Wait for the stream to actually start before the case does anything.
    wait_file "$HB_FILE" 30 && sleep "$SETTLE_SECONDS"
}

heartbeat_seq() { awk 'END{print $2+0}' "$HB_FILE" 2>/dev/null; }

stop_heartbeat() {
    kill_quiet "$HB_PID"
    sleep 0.3   # let the last stamped lines reach the file
}

# verdict_survived ID SEQ_AT_EVENT MAX_STALL -> PASS/FAIL note in VERDICT/NOTE
verdict_survived() {
    local id=$1 at_event=$2 limit=$3
    analyse_heartbeat "$HB_FILE"
    local grew=$((HB_LAST - at_event)) losses
    losses=$(link_stat 'link lost')
    local relinks; relinks=$(link_stat 'link established')
    local stall; stall=$(printf '%.1f' "$HB_MAX_GAP")
    local how="migrated without losing the link"
    [ "$losses" -gt 0 ] && how="$losses link loss(es), $relinks links"
    if [ "$HB_BREAKS" -ne 0 ]; then
        VERDICT=FAIL; NOTE="stream corrupted: $HB_BREAKS discontinuities"
    elif [ "$grew" -lt 10 ]; then
        VERDICT=FAIL; NOTE="session did not continue after the event (seq $at_event -> $HB_LAST)"
    elif awk "BEGIN{exit !($HB_MAX_GAP > $limit)}"; then
        VERDICT=FAIL; NOTE="terminal froze ${stall}s, over the ${limit}s budget ($how)"
    else
        VERDICT=PASS; NOTE="survived, froze ${stall}s, $grew lines after the event, $how"
    fi
}

# ---------------------------------------------------------------------------
# R1  leave the current network (Wi-Fi -> cellular) during a live session
# ---------------------------------------------------------------------------
case_R1() {
    restart_tunnel_client
    start_heartbeat R1 $((SETTLE_SECONDS + OBSERVE_SECONDS))
    local at; at=$(heartbeat_seq)
    net_away
    sleep "$OBSERVE_SECONDS"
    stop_heartbeat
    verdict_survived R1 "$at" "$MAX_STALL"
    record R1 "$VERDICT" "$NOTE"
}

# ---------------------------------------------------------------------------
# R2  come back to the original network
# ---------------------------------------------------------------------------
case_R2() {
    restart_tunnel_client
    start_heartbeat R2 $((SETTLE_SECONDS + OBSERVE_SECONDS))
    local at; at=$(heartbeat_seq)
    net_back
    sleep "$OBSERVE_SECONDS"
    stop_heartbeat
    verdict_survived R2 "$at" "$MAX_STALL"
    record R2 "$VERDICT" "$NOTE"
}

# ---------------------------------------------------------------------------
# R3  an outage shorter than --session-linger: the session must come back
# ---------------------------------------------------------------------------
case_R3() {
    [ "$OUTAGE_SECONDS" -lt "$LINGER" ] || die "OUTAGE_SECONDS must be below LINGER for R3"
    restart_tunnel_client
    start_heartbeat R3 $((SETTLE_SECONDS + OUTAGE_SECONDS + OBSERVE_SECONDS))
    local at; at=$(heartbeat_seq)
    net_down
    sleep "$OUTAGE_SECONDS"
    net_up
    sleep "$OBSERVE_SECONDS"
    stop_heartbeat
    verdict_survived R3 "$at" $((OUTAGE_SECONDS + MAX_STALL))
    record R3 "$VERDICT" "$NOTE"
}

# ---------------------------------------------------------------------------
# R4  an outage longer than --session-linger: fail fast, never hang
# ---------------------------------------------------------------------------
case_R4() {
    restart_tunnel_client
    local long=$((LINGER + 30))
    start_heartbeat R4 $((SETTLE_SECONDS + long + 30))
    local start=$SECONDS
    net_down
    sleep "$long"
    net_up
    # The ssh should already be gone; give it a moment either way.
    wait_exit "$HB_PID" 30
    local elapsed=$((SECONDS - start))
    analyse_heartbeat "$HB_FILE"
    if [ "$WAIT_RC" = "0" ]; then
        record R4 FAIL "the session outlived the linger window, which it must not"
    elif [ "$WAIT_RC" = "124" ]; then
        record R4 FAIL "ssh hung instead of failing after ${elapsed}s"
    elif grep -q 'giving up on session' "$CLIENT_LOG"; then
        record R4 PASS "gave up after the ${LINGER}s linger, ssh failed cleanly in ${elapsed}s"
    else
        record R4 FAIL "ssh exited rc=$WAIT_RC but the client never gave up"
    fi
}

# ---------------------------------------------------------------------------
# R5  a long idle period: NAT mappings expire, keepalive must hold the path
# ---------------------------------------------------------------------------
case_R5() {
    restart_tunnel_client
    local before after out
    before=$(link_stat 'link established')
    info "idling for ${IDLE_SECONDS}s"
    sleep "$IDLE_SECONDS"
    out=$(SSH_TIMEOUT=60 rssh roam 'echo R5-ALIVE' 2>"$ART/R5.err")
    after=$(link_stat 'link established')
    if [ "$out" != "R5-ALIVE" ]; then
        record R5 FAIL "session unusable after ${IDLE_SECONDS}s idle"
    elif [ "$after" -ne "$before" ]; then
        record R5 PASS "usable after ${IDLE_SECONDS}s idle, but the path was rebuilt $((after-before)) time(s): raise --keepalive"
    else
        record R5 PASS "path held through ${IDLE_SECONDS}s idle, no reconnect"
    fi
}

# ---------------------------------------------------------------------------
# R6  a bulk transfer that spans a network change
# ---------------------------------------------------------------------------
case_R6() {
    restart_tunnel_client
    local src=$ART/R6.bin dst=/tmp/tingly-r6-$RUN_ID.bin a b
    head -c $((BULK_MB*1024*1024)) /dev/urandom > "$src"
    a=$(sha256sum "$src" | cut -d' ' -f1)
    ( timeout $((BULK_MB*10 + 300)) scp -F "$ART/ssh_config" -q "$src" "roam:$dst" \
        > "$ART/R6.err" 2>&1 ) &
    local scp_pid=$!
    track "$scp_pid"
    sleep 5
    net_away
    wait_exit "$scp_pid" $((BULK_MB*10 + 300))
    b=$(SSH_TIMEOUT=120 rssh roam "sha256sum $dst 2>/dev/null | cut -d' ' -f1" 2>>"$ART/R6.err")
    SSH_TIMEOUT=60 rssh roam "rm -f $dst" >/dev/null 2>&1
    rm -f "$src"
    if [ "$WAIT_RC" = "0" ] && [ -n "$b" ] && [ "$a" = "$b" ]; then
        record R6 PASS "${BULK_MB} MiB transfer crossed a network change, sha256 ${a:0:12}"
    else
        record R6 FAIL "rc=$WAIT_RC digest $a vs ${b:-none}"
    fi
}

# ---------------------------------------------------------------------------
# R7  the tunnel server restarts: v0 must fail clearly, not hang
# ---------------------------------------------------------------------------
case_R7() {
    if [ -z "$SERVER_RESTART_CMD" ]; then
        record R7 SKIP "set SERVER_RESTART_CMD to run this case"
        return
    fi
    restart_tunnel_client
    start_heartbeat R7 60
    info "restarting the tunnel server"
    sh -c "$SERVER_RESTART_CMD" > "$ART/R7.restart.log" 2>&1 || die "restart command failed"
    wait_exit "$HB_PID" 90
    if [ "$WAIT_RC" = "124" ]; then
        record R7 FAIL "ssh hung after the server restarted"
    elif [ "$WAIT_RC" = "0" ]; then
        record R7 PASS "session survived the restart (server-side resumption is live)"
    elif grep -q 'session_unknown' "$CLIENT_LOG"; then
        record R7 PASS "clean SESSION_UNKNOWN failure, the documented v0 behaviour"
    elif grep -q 'peer closed session' "$CLIENT_LOG"; then
        # A graceful shutdown tells clients not to resume, which is the same
        # contract reached by a nicer route.
        record R7 PASS "server said it was shutting down, client stopped cleanly"
    else
        record R7 FAIL "ssh exited rc=$WAIT_RC with no reason in the client log"
    fi
}

# ---------------------------------------------------------------------------

set_summary_meta() {
    SUMMARY_META=(
        "run: $RUN_ID"
        "host: $(uname -srm)"
        "network controller: $(net_describe)"
        "server: $TINGLY_SERVER  target: $TINGLY_TARGET"
        "client flags: --session-linger ${LINGER}s $TINGLY_CLIENT_FLAGS"
        "thresholds: max stall ${MAX_STALL}s, outage ${OUTAGE_SECONDS}s, idle ${IDLE_SECONDS}s"
        "commit: $(cd "$REPO_ROOT" && git rev-parse --short HEAD 2>/dev/null || echo unknown)"
    )
}

main() {
    mkdir -p "$ART"
    log "tingly-ssh roaming verification, run $RUN_ID"
    require_config
    require_port_free "$CLIENT_PORT" "tunnel client"
    net_ctl_init
    write_ssh_config

    local all=(R1 R2 R3 R4 R5 R6 R7) id
    for id in "${all[@]}"; do
        selected "$id" || continue
        if [ $QUICK -eq 1 ] && { [ "$id" = R5 ] || [ "$id" = R6 ]; }; then
            record "$id" SKIP "skipped by --quick"
            continue
        fi
        log "--- $id"
        "case_$id"
    done
    # Leave the network the way we found it. Never let this step abort the
    # run: the results are the point.
    if [ "$NET_CTL" != manual ]; then
        net_up >/dev/null 2>&1 || log "could not restore the network automatically"
    fi
    set_summary_meta
    summary
}

main
