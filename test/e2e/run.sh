#!/usr/bin/env bash
# End-to-end verification for tingly-shell against a real sshd and a real ssh
# client. Neither side of SSH is modified: sshd runs with its own config file
# and ssh reaches it through an extra -F config that adds a ProxyCommand.
#
# Usage:
#   ./run.sh                 run every case
#   ./run.sh --quick         skip the slow outage cases (E8, E9)
#   ./run.sh E2 E7           run only the named cases
#
# Results and every log land in test/e2e/artifacts/<timestamp>/.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

QUICK=0
SELECT=()
for arg in "$@"; do
    case $arg in
        --quick) QUICK=1 ;;
        -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
        *) SELECT+=("$arg") ;;
    esac
done

selected() {
    [ ${#SELECT[@]} -eq 0 ] && return 0
    local id
    for id in "${SELECT[@]}"; do [ "$id" = "$1" ] && return 0; done
    return 1
}

# ---------------------------------------------------------------------------
# E1  the tunnel carries a plain TCP service, including half close
# ---------------------------------------------------------------------------
case_E1() {
    start_client e1 "127.0.0.1:$ECHO_PORT"
    local out
    out=$(printf 'hello tunnel' | timeout 20 python3 -c '
import socket, sys
s = socket.create_connection(("127.0.0.1", int(sys.argv[1])), 10)
s.sendall(sys.stdin.buffer.read())
s.shutdown(socket.SHUT_WR)          # must reach the far end as EOF
buf = b""
while True:
    d = s.recv(4096)
    if not d: break
    buf += d
sys.stdout.buffer.write(buf)' "$CLIENT_PORT")
    if [ "$out" = "HELLO TUNNEL" ]; then
        record E1 PASS "TCP echo + half close through the tunnel"
    else
        record E1 FAIL "expected 'HELLO TUNNEL', got '$out'"
    fi
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------
# E2  ssh through ProxyCommand, unmodified client and sshd
# ---------------------------------------------------------------------------
case_E2() {
    local out
    out=$(SSH_TIMEOUT=60 tssh tingly-proxy 'echo E2-OK; id -un' 2>"$ART/E2.err")
    if [[ $out == E2-OK* ]]; then
        record E2 PASS "ssh -o ProxyCommand reached sshd as $(echo "$out" | tail -1)"
    else
        record E2 FAIL "ssh output '$out' (stderr: $(head -c 200 "$ART/E2.err"))"
    fi
}

# ---------------------------------------------------------------------------
# E3  ssh through the client's local TCP listener
# ---------------------------------------------------------------------------
case_E3() {
    start_client e3 "127.0.0.1:$SSHD_PORT"
    local out
    out=$(SSH_TIMEOUT=60 tssh -p "$CLIENT_PORT" tingly-local 'echo E3-OK' 2>"$ART/E3.err")
    if [ "$out" = "E3-OK" ]; then
        record E3 PASS "ssh -p $CLIENT_PORT through the local listener"
    else
        record E3 FAIL "ssh output '$out' (stderr: $(head -c 200 "$ART/E3.err"))"
    fi
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------
# E4  stdin is forwarded and EOF propagates (`ssh host cat < file`)
# ---------------------------------------------------------------------------
case_E4() {
    start_client e4 "127.0.0.1:$SSHD_PORT"
    head -c 200000 /dev/urandom > "$ART/E4.in"
    SSH_TIMEOUT=60 tssh -p "$CLIENT_PORT" tingly-local 'cat' < "$ART/E4.in" > "$ART/E4.out" 2>"$ART/E4.err"
    local rc=$?
    if [ $rc -eq 0 ] && cmp -s "$ART/E4.in" "$ART/E4.out"; then
        record E4 PASS "200000 bytes over stdin, EOF propagated"
    else
        record E4 FAIL "rc=$rc, $(wc -c < "$ART/E4.out") bytes back of $(wc -c < "$ART/E4.in")"
    fi
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------
# E5  scp of a large file, checked by digest
# ---------------------------------------------------------------------------
case_E5() {
    start_client e5 "127.0.0.1:$SSHD_PORT"
    local src=$ART/E5.bin dst=$ART/E5.copy
    head -c $((8*1024*1024)) /dev/urandom > "$src"
    timeout 120 scp -F "$ART/ssh_config" -P "$CLIENT_PORT" -q "$src" "tingly-local:$dst" 2>"$ART/E5.err"
    local rc=$?
    local a b
    a=$(sha256sum "$src" | awk '{print $1}')
    b=$(sha256sum "$dst" 2>/dev/null | awk '{print $1}')
    if [ $rc -eq 0 ] && [ -n "$b" ] && [ "$a" = "$b" ]; then
        record E5 PASS "8 MiB scp, sha256 ${a:0:12}"
    else
        record E5 FAIL "rc=$rc digest $a vs ${b:-none}"
    fi
    rm -f "$src" "$dst"
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------
# E6  concurrent sessions share one tunnel session as separate streams
# ---------------------------------------------------------------------------
case_E6() {
    start_client e6 "127.0.0.1:$SSHD_PORT"
    local i pids=() ok=1
    for i in 1 2 3; do
        ( SSH_TIMEOUT=60 tssh -p "$CLIENT_PORT" tingly-local "sleep 1; echo E6-$i" > "$ART/E6.$i.out" 2>&1 ) &
        pids+=($!)
    done
    for i in "${!pids[@]}"; do wait "${pids[$i]}" || ok=0; done
    for i in 1 2 3; do
        [ "$(cat "$ART/E6.$i.out" 2>/dev/null)" = "E6-$i" ] || ok=0
    done
    local streams session
    streams=$(grep -c 'local connection bridged' "$CLIENT_LOG")
    session=$(grep -o 'session=[0-9a-f]*' "$CLIENT_LOG" | sort -u | wc -l)
    if [ $ok -eq 1 ] && [ "$streams" -eq 3 ] && [ "$session" -eq 1 ]; then
        record E6 PASS "3 concurrent ssh sessions as 3 streams of 1 session"
    else
        record E6 FAIL "ok=$ok streams=$streams sessions=$session"
    fi
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------
# E7  the link is destroyed twice during a live ssh session
# ---------------------------------------------------------------------------
case_E7() {
    start_client e7 "127.0.0.1:$SSHD_PORT"
    wait_log "$CLIENT_LOG" "link established" 15 || { record E7 FAIL "no link"; return; }
    ( SSH_TIMEOUT=90 tssh -p "$CLIENT_PORT" tingly-local \
        'for i in $(seq 1 20); do echo line$i; sleep 0.4; done' > "$ART/E7.out" 2>"$ART/E7.err" ) &
    local sshpid=$!
    sleep 2; kill -USR1 "$CLIENT_PID"
    sleep 2; kill -USR1 "$CLIENT_PID"
    wait $sshpid
    local rc=$? lines last drops links
    lines=$(wc -l < "$ART/E7.out")
    last=$(tail -1 "$ART/E7.out")
    drops=$(grep -c 'dropped link on SIGUSR1' "$CLIENT_LOG")
    links=$(grep -c 'link established' "$CLIENT_LOG")
    if [ $rc -eq 0 ] && [ "$lines" -eq 20 ] && [ "$last" = "line20" ] && [ "$drops" -eq 2 ] && [ "$links" -ge 3 ]; then
        record E7 PASS "20/20 lines across $drops forced link failures ($links links)"
    else
        record E7 FAIL "rc=$rc lines=$lines last=$last drops=$drops links=$links"
    fi
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------
# E8  the server is unreachable for 25s; the ssh session must survive
#     (SIGSTOP freezes the process exactly like a network black hole)
# ---------------------------------------------------------------------------
case_E8() {
    start_client e8 "127.0.0.1:$SSHD_PORT"
    wait_log "$CLIENT_LOG" "link established" 15 || { record E8 FAIL "no link"; return; }
    ( SSH_TIMEOUT=120 tssh -p "$CLIENT_PORT" tingly-local \
        'for i in $(seq 1 55); do echo line$i; sleep 1; done' > "$ART/E8.out" 2>"$ART/E8.err" ) &
    local sshpid=$!
    sleep 3
    info "freezing the tunnel server for 30s (default 20s QUIC idle timeout)"
    kill -STOP "$SERVER_PID"; sleep 30; kill -CONT "$SERVER_PID"
    wait $sshpid
    local rc=$? lines last losses relinks
    lines=$(wc -l < "$ART/E8.out")
    last=$(tail -1 "$ART/E8.out")
    losses=$(grep -c 'link lost' "$CLIENT_LOG")
    relinks=$(grep -c 'link established' "$CLIENT_LOG")
    # The outage must really have killed the link, otherwise the case proves
    # nothing about resumption.
    if [ $rc -eq 0 ] && [ "$lines" -eq 55 ] && [ "$last" = "line55" ] && [ "$losses" -ge 1 ] && [ "$relinks" -ge 2 ]; then
        record E8 PASS "55/55 lines across a 30s outage ($losses link losses, $relinks links)"
    else
        record E8 FAIL "rc=$rc lines=$lines last=$last losses=$losses relinks=$relinks"
    fi
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------
# E9  an outage longer than --session-linger must fail fast and cleanly
# ---------------------------------------------------------------------------
case_E9() {
    # A short idle timeout makes the link die 5s into the outage, so the case
    # measures the linger policy rather than QUIC's detection latency.
    start_client e9 "127.0.0.1:$SSHD_PORT" --session-linger 5s --idle-timeout 5s --keepalive 2s
    wait_log "$CLIENT_LOG" "link established" 15 || { record E9 FAIL "no link"; return; }
    ( SSH_TIMEOUT=120 tssh -p "$CLIENT_PORT" tingly-local \
        'for i in $(seq 1 60); do echo line$i; sleep 1; done' > "$ART/E9.out" 2>"$ART/E9.err" ) &
    local sshpid=$!
    local start=$SECONDS
    sleep 3
    info "freezing the tunnel server for 25s with a 5s linger"
    kill -STOP "$SERVER_PID"; sleep 25; kill -CONT "$SERVER_PID"
    wait $sshpid
    local rc=$? elapsed=$((SECONDS-start)) lines
    lines=$(wc -l < "$ART/E9.out")
    if [ $rc -ne 0 ] && [ "$lines" -lt 60 ] && [ $elapsed -lt 60 ] && grep -q 'giving up on session' "$CLIENT_LOG"; then
        record E9 PASS "gave up after the 5s linger; ssh failed cleanly in ${elapsed}s with $lines lines"
    else
        record E9 FAIL "rc=$rc lines=$lines elapsed=${elapsed}s (expected a clean failure)"
    fi
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------
# E10 a wrong token is refused and the client stops retrying
# ---------------------------------------------------------------------------
case_E10() {
    echo "not-the-right-token" > "$ART/bad-token"
    chmod 600 "$ART/bad-token"
    CLIENT_NO_WAIT=1 CLIENT_TOKEN_FILE=$ART/bad-token start_client e10 "127.0.0.1:$SSHD_PORT" --session-linger 5s
    wait_exit "$CLIENT_PID" 20
    local retries
    retries=$(grep -c 'reconnect failed' "$CLIENT_LOG")
    # The refusal reason must survive the teardown, so the client stops at once
    # instead of retrying until the linger window runs out.
    if [ "$WAIT_RC" != "0" ] && [ "$WAIT_RC" != "124" ] && grep -q 'unauthorized' "$CLIENT_LOG" && [ "$retries" -eq 0 ]; then
        record E10 PASS "refused as unauthorized in ${WAIT_ELAPSED}s, no retries"
    else
        record E10 FAIL "rc=$WAIT_RC retries=$retries elapsed=${WAIT_ELAPSED}s"
    fi
}

# ---------------------------------------------------------------------------
# E11 a mismatched server key pin is refused
# ---------------------------------------------------------------------------
case_E11() {
    local bad
    bad="sha256:$(head -c 32 /dev/urandom | base64)"
    CLIENT_NO_WAIT=1 CLIENT_PIN=$bad start_client e11 "127.0.0.1:$SSHD_PORT" --session-linger 5s
    wait_exit "$CLIENT_PID" 30
    if [ "$WAIT_RC" != "0" ] && [ "$WAIT_RC" != "124" ] && grep -q 'pin mismatch' "$CLIENT_LOG"; then
        record E11 PASS "pinned handshake refused, client exited in ${WAIT_ELAPSED}s"
    else
        record E11 FAIL "rc=$WAIT_RC (expected a pin mismatch)"
    fi
}

# ---------------------------------------------------------------------------
# E12 two hops: laptop -> tunnel -> jump host -> target, with ProxyJump.
#     The jump host is never modified; only the client's ssh config gains a
#     ProxyCommand for the first hop.
# ---------------------------------------------------------------------------
case_E12() {
    local out
    out=$(SSH_TIMEOUT=90 tssh behind-jump 'echo E12-OK; ss -ltnp 2>/dev/null | grep -c ":'"$TARGET_SSHD_PORT"'"' 2>"$ART/E12.err")
    if [[ $out == E12-OK* ]]; then
        record E12 PASS "ssh -J through an unmodified jump host, over the tunnel"
    else
        record E12 FAIL "output '$out' (stderr: $(head -c 300 "$ART/E12.err"))"
    fi
}

# ---------------------------------------------------------------------------
# E13 the first hop's link is destroyed during a live two hop session.
#     Only the laptop side roams; the jump-to-target hop never moves.
# ---------------------------------------------------------------------------
case_E13() {
    start_client e13 "127.0.0.1:$SSHD_PORT"
    wait_log "$CLIENT_LOG" "link established" 15 || { record E13 FAIL "no link"; return; }
    # ProxyJump through the client's local listener, so the test owns the
    # tunnel client's pid and can destroy its link on demand.
    ( SSH_TIMEOUT=90 tssh -J "$(id -un)@127.0.0.1:$CLIENT_PORT" behind-jump         'for i in $(seq 1 20); do echo line$i; sleep 0.4; done' > "$ART/E13.out" 2>"$ART/E13.err" ) &
    local sshpid=$!
    sleep 2; kill -USR1 "$CLIENT_PID"
    sleep 2; kill -USR1 "$CLIENT_PID"
    wait $sshpid
    local rc=$? lines last drops
    lines=$(wc -l < "$ART/E13.out")
    last=$(tail -1 "$ART/E13.out")
    drops=$(grep -c 'dropped link on SIGUSR1' "$CLIENT_LOG")
    if [ $rc -eq 0 ] && [ "$lines" -eq 20 ] && [ "$last" = "line20" ] && [ "$drops" -eq 2 ]; then
        record E13 PASS "two hop session survived $drops first-hop link failures"
    else
        record E13 FAIL "rc=$rc lines=$lines last=$last drops=$drops"
    fi
    kill_quiet "$CLIENT_PID"
}

# ---------------------------------------------------------------------------

main() {
    mkdir -p "$ART"
    log "tingly-shell end-to-end verification, run $RUN_ID"
    command -v ssh >/dev/null   || die "ssh is not installed"
    command -v /usr/sbin/sshd >/dev/null || die "sshd is not installed"
    build_binary
    make_token
    make_ssh_keys
    start_sshd jump "$SSHD_PORT"
    start_sshd target "$TARGET_SSHD_PORT"
    start_echo
    start_tunnel_server
    write_ssh_config

    local all=(E1 E2 E3 E4 E5 E6 E7 E8 E9 E10 E11 E12 E13) id
    for id in "${all[@]}"; do
        if ! selected "$id"; then continue; fi
        if [ $QUICK -eq 1 ] && { [ "$id" = E8 ] || [ "$id" = E9 ]; }; then
            record "$id" SKIP "skipped by --quick"
            continue
        fi
        log "--- $id"
        "case_$id"
    done
    summary
}

main
