#!/usr/bin/env bash
# Everyday SSH scenarios over the tunnel: interactive terminals, big output,
# forwarding, sftp, multiplexing. Every case is automated and reproducible.
#
#   ./run.sh              all scenarios
#   ./run.sh --quick      skip the slow ones (S3, S13)
#   ./run.sh S2 S6        only the named scenarios
#
# Results and logs land in test/scenarios/artifacts/<timestamp>/.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$HERE/../.." && pwd)
BIN=${TINGLY_BIN:-$REPO_ROOT/test/scenarios/.bin/tingly-shell}
RUN_ID=$(date +%Y%m%d-%H%M%S)
ART=${ART_DIR:-$HERE/artifacts/$RUN_ID}

# Ports of their own so this suite can run next to the e2e suite.
SSHD_PORT=${SSHD_PORT:-2032}
TARGET_SSHD_PORT=${TARGET_SSHD_PORT:-2234}
TUNNEL_PORT=${TUNNEL_PORT:-7460}
ECHO_PORT=${ECHO_PORT:-2033}
NEXT_CLIENT_PORT=${NEXT_CLIENT_PORT:-2400}
IDLE_SECONDS=${IDLE_SECONDS:-30}
BULK_MB=${BULK_MB:-32}

TEST_LIB=$(cd "$HERE/../lib" && pwd)
source "$TEST_LIB/common.sh"
source "$TEST_LIB/fixtures.sh"

SUMMARY_TITLE="tingly-shell SSH scenario suite"

QUICK=0
SELECT=()
for arg in "$@"; do
    case $arg in
        --quick) QUICK=1 ;;
        -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
        *) SELECT+=("$arg") ;;
    esac
done

# pty_run ID STEPS_FILE [ssh args...] runs a pty scenario against the shared
# tunnel client. Placeholders in the steps file are filled in first.
pty_run() {
    local id=$1 steps=$2; shift 2
    sed -e "s|@ART@|$ART|g" -e "s|@IDLE@|$IDLE_SECONDS|g" -e "s|@LOG@|${S17_LOG:-}|g" \
        "$steps" > "$ART/$id.steps"
    TERM=${TERM:-xterm-256color} \
    "$HERE/ptydrive.py" --script "$ART/$id.steps" --transcript "$ART/$id.transcript" \
        --timeout "${PTY_TIMEOUT:-60}" -- \
        ssh -F "$ART/ssh_config" -tt -p "$MAIN_PORT" tingly-local "$@" 2>"$ART/$id.err"
}

# rcmd runs one command on the far side over a fresh, non-interactive session.
rcmd() { SSH_TIMEOUT=${SSH_TIMEOUT:-60} tssh -p "$MAIN_PORT" tingly-local "$@"; }

# tmux_missing records a SKIP when the far side has no tmux.
tmux_missing() {
    if rcmd 'command -v tmux >/dev/null'; then return 1; fi
    record "$1" SKIP "tmux is not installed on the far side"
    return 0
}

# ---------------------------------------------------------------------------
# S1  interactive terminal: tty allocated, commands echo back, clean exit
# ---------------------------------------------------------------------------
case_S1() {
    if pty_run S1 "$HERE/steps/S1-interactive.steps"; then
        record S1 PASS "interactive shell with a real tty, clean exit"
    else
        record S1 FAIL "$(head -c 200 "$ART/S1.err")"
    fi
}

# ---------------------------------------------------------------------------
# S2  interactive session survives the link being destroyed under it
# ---------------------------------------------------------------------------
case_S2() {
    rm -f "$ART/S2.ready" "$ART/S2.proceed"
    ( pty_run S2 "$HERE/steps/S2-interactive-across-drop.steps" ) &
    local drv=$!
    if ! wait_file "$ART/S2.ready" 60; then
        kill_quiet "$drv"; record S2 FAIL "session never reached the first prompt"; return
    fi
    local before after
    before=$(grep -c 'link established' "$MAIN_LOG")
    kill -USR1 "$MAIN_PID"; sleep 2
    kill -USR1 "$MAIN_PID"; sleep 3
    after=$(grep -c 'link established' "$MAIN_LOG")
    touch "$ART/S2.proceed"
    wait "$drv"
    local rc=$?
    if [ $rc -eq 0 ] && [ "$after" -ge $((before+2)) ]; then
        record S2 PASS "typed before and after $((after-before)) link failures in one shell"
    else
        record S2 FAIL "rc=$rc links $before -> $after ($(head -c 200 "$ART/S2.err"))"
    fi
}

# ---------------------------------------------------------------------------
# S3  a large command output arrives byte for byte
# ---------------------------------------------------------------------------
case_S3() {
    local remote=/tmp/tingly-s3-$RUN_ID.bin bytes=$((BULK_MB*1024*1024)) want got
    want=$(SSH_TIMEOUT=180 tssh -p "$MAIN_PORT" tingly-local \
        "head -c $bytes /dev/urandom > $remote; sha256sum $remote | cut -d' ' -f1" 2>"$ART/S3.err")
    SSH_TIMEOUT=180 tssh -p "$MAIN_PORT" tingly-local "cat $remote" > "$ART/S3.out" 2>>"$ART/S3.err"
    local rc=$?
    got=$(sha256sum "$ART/S3.out" | cut -d' ' -f1)
    SSH_TIMEOUT=60 tssh -p "$MAIN_PORT" tingly-local "rm -f $remote" >/dev/null 2>&1
    if [ $rc -eq 0 ] && [ -n "$want" ] && [ "$want" = "$got" ]; then
        record S3 PASS "${BULK_MB} MiB of stdout, sha256 ${want:0:12}"
    else
        record S3 FAIL "rc=$rc want ${want:-none} got $got"
    fi
    rm -f "$ART/S3.out"
}

# ---------------------------------------------------------------------------
# S4  the byte stream is 8-bit clean in both directions
# ---------------------------------------------------------------------------
case_S4() {
    python3 -c '
import sys
buf = bytes(range(256)) * 256          # every byte value, CR and LF included
sys.stdout.buffer.write(buf)' > "$ART/S4.in"
    SSH_TIMEOUT=60 tssh -p "$MAIN_PORT" tingly-local 'cat' < "$ART/S4.in" > "$ART/S4.out" 2>"$ART/S4.err"
    local rc=$?
    if [ $rc -eq 0 ] && cmp -s "$ART/S4.in" "$ART/S4.out"; then
        record S4 PASS "all 256 byte values round tripped unchanged"
    else
        record S4 FAIL "rc=$rc $(wc -c < "$ART/S4.out") of $(wc -c < "$ART/S4.in") bytes"
    fi
}

# ---------------------------------------------------------------------------
# S5  stdout and stderr stay separate and the exit status survives
# ---------------------------------------------------------------------------
case_S5() {
    local out
    out=$(SSH_TIMEOUT=60 tssh -p "$MAIN_PORT" tingly-local \
        'echo to-stdout; echo to-stderr >&2; exit 42' 2>"$ART/S5.err")
    local rc=$?
    local err; err=$(cat "$ART/S5.err")
    if [ "$rc" -eq 42 ] && [ "$out" = "to-stdout" ] && [ "$err" = "to-stderr" ]; then
        record S5 PASS "streams separate, exit status 42 propagated"
    else
        record S5 FAIL "rc=$rc out='$out' err='$err'"
    fi
}

# ---------------------------------------------------------------------------
# S6  Ctrl-C interrupts the foreground job, the shell stays alive
# ---------------------------------------------------------------------------
case_S6() {
    if pty_run S6 "$HERE/steps/S6-interrupt.steps" && \
       ! grep -q 'S6-RAN-42' "$ART/S6.transcript"; then
        record S6 PASS "Ctrl-C killed the remote sleep, shell survived"
    else
        record S6 FAIL "$(head -c 200 "$ART/S6.err")"
    fi
}

# ---------------------------------------------------------------------------
# S7  a window resize reaches the remote tty
# ---------------------------------------------------------------------------
case_S7() {
    if pty_run S7 "$HERE/steps/S7-resize.steps"; then
        record S7 PASS "SIGWINCH propagated, remote stty size followed"
    else
        record S7 FAIL "$(head -c 200 "$ART/S7.err")"
    fi
}

# ---------------------------------------------------------------------------
# S8  local port forwarding (ssh -L) through the tunnel
# ---------------------------------------------------------------------------
case_S8() {
    local port=$((NEXT_CLIENT_PORT++))
    SSH_TIMEOUT=120 tssh -p "$MAIN_PORT" -N -L "127.0.0.1:$port:127.0.0.1:$ECHO_PORT" \
        tingly-local > "$ART/S8.err" 2>&1 &
    local fwd=$!
    track "$fwd"
    if wait_tcp 127.0.0.1 "$port" 20 && [ "$(echo_probe "$port" 'forward me')" = "FORWARD ME" ]; then
        record S8 PASS "ssh -L carried a TCP service over the tunnel"
    else
        record S8 FAIL "no answer on the forwarded port ($(head -c 200 "$ART/S8.err"))"
    fi
    kill_quiet "$fwd"
}

# ---------------------------------------------------------------------------
# S9  remote port forwarding (ssh -R)
# ---------------------------------------------------------------------------
case_S9() {
    local port=$((NEXT_CLIENT_PORT++))
    SSH_TIMEOUT=120 tssh -p "$MAIN_PORT" -N -R "127.0.0.1:$port:127.0.0.1:$ECHO_PORT" \
        tingly-local > "$ART/S9.err" 2>&1 &
    local fwd=$!
    track "$fwd"
    sleep 2
    local out
    out=$(SSH_TIMEOUT=60 tssh -p "$MAIN_PORT" tingly-local \
        "printf 'reverse me' | timeout 10 python3 -c '
import socket,sys
s=socket.create_connection((\"127.0.0.1\", $port), 5)
s.sendall(sys.stdin.buffer.read()); s.shutdown(socket.SHUT_WR)
print(s.recv(100).decode())'" 2>>"$ART/S9.err")
    if [ "$out" = "REVERSE ME" ]; then
        record S9 PASS "ssh -R reached back through the tunnel"
    else
        record S9 FAIL "got '$out' ($(head -c 200 "$ART/S9.err"))"
    fi
    kill_quiet "$fwd"
}

# ---------------------------------------------------------------------------
# S10 dynamic forwarding (ssh -D, SOCKS5)
# ---------------------------------------------------------------------------
case_S10() {
    local port=$((NEXT_CLIENT_PORT++))
    SSH_TIMEOUT=120 tssh -p "$MAIN_PORT" -N -D "127.0.0.1:$port" tingly-local > "$ART/S10.err" 2>&1 &
    local fwd=$!
    track "$fwd"
    local out
    if wait_tcp 127.0.0.1 "$port" 20; then
        out=$(timeout 30 python3 -c '
import socket, struct, sys
proxy, dst = int(sys.argv[1]), int(sys.argv[2])
s = socket.create_connection(("127.0.0.1", proxy), 10)
s.sendall(b"\x05\x01\x00"); assert s.recv(2) == b"\x05\x00"
s.sendall(b"\x05\x01\x00\x01" + socket.inet_aton("127.0.0.1") + struct.pack("!H", dst))
rep = s.recv(10); assert rep[1] == 0, rep
s.sendall(b"socks me"); s.shutdown(socket.SHUT_WR)
print(s.recv(100).decode())' "$port" "$ECHO_PORT" 2>>"$ART/S10.err")
    fi
    if [ "$out" = "SOCKS ME" ]; then
        record S10 PASS "ssh -D SOCKS5 proxied through the tunnel"
    else
        record S10 FAIL "got '${out:-nothing}' ($(head -c 200 "$ART/S10.err"))"
    fi
    kill_quiet "$fwd"
}

# ---------------------------------------------------------------------------
# S11 sftp put and get with digests
# ---------------------------------------------------------------------------
case_S11() {
    local src=$ART/S11.src up=/tmp/tingly-s11-$RUN_ID.bin down=$ART/S11.down
    head -c $((4*1024*1024)) /dev/urandom > "$src"
    cat > "$ART/S11.batch" <<EOF
put $src $up
get $up $down
EOF
    timeout 120 sftp -q -F "$ART/ssh_config" -P "$MAIN_PORT" -b "$ART/S11.batch" tingly-local \
        > "$ART/S11.log" 2>&1
    local rc=$? a b
    a=$(sha256sum "$src" | cut -d' ' -f1)
    b=$(sha256sum "$down" 2>/dev/null | cut -d' ' -f1)
    SSH_TIMEOUT=60 tssh -p "$MAIN_PORT" tingly-local "rm -f $up" >/dev/null 2>&1
    if [ $rc -eq 0 ] && [ -n "$b" ] && [ "$a" = "$b" ]; then
        record S11 PASS "sftp put and get, 4 MiB, sha256 ${a:0:12}"
    else
        record S11 FAIL "rc=$rc $a vs ${b:-none} ($(tail -2 "$ART/S11.log" | head -c 200))"
    fi
    rm -f "$src" "$down"
}

# ---------------------------------------------------------------------------
# S12 ControlMaster multiplexing: many sessions, one TCP connection
# ---------------------------------------------------------------------------
case_S12() {
    local sock=$ART/S12.sock before after ok=1 i
    before=$(grep -c 'local connection bridged' "$MAIN_LOG")
    timeout 60 ssh -F "$ART/ssh_config" -p "$MAIN_PORT" -M -S "$sock" -N -f tingly-local \
        > "$ART/S12.err" 2>&1 || { record S12 FAIL "master failed: $(head -c 200 "$ART/S12.err")"; return; }
    for i in 1 2 3; do
        [ "$(timeout 30 ssh -F "$ART/ssh_config" -S "$sock" tingly-local "echo S12-$i" 2>>"$ART/S12.err")" = "S12-$i" ] || ok=0
    done
    after=$(grep -c 'local connection bridged' "$MAIN_LOG")
    timeout 30 ssh -F "$ART/ssh_config" -S "$sock" -O exit tingly-local >/dev/null 2>&1
    # The master opens exactly one TCP connection; the three sessions ride it.
    if [ $ok -eq 1 ] && [ $((after-before)) -eq 1 ]; then
        record S12 PASS "3 multiplexed sessions over 1 tunnel stream"
    else
        record S12 FAIL "ok=$ok streams opened=$((after-before)) (want 1)"
    fi
}

# ---------------------------------------------------------------------------
# S13 an idle interactive session stays usable
# ---------------------------------------------------------------------------
case_S13() {
    if PTY_TIMEOUT=$((IDLE_SECONDS+60)) pty_run S13 "$HERE/steps/S13-idle.steps"; then
        record S13 PASS "shell still responsive after ${IDLE_SECONDS}s idle"
    else
        record S13 FAIL "$(head -c 200 "$ART/S13.err")"
    fi
}

# ---------------------------------------------------------------------------
# S15 tmux: detach and leave the session alive on the far side
# ---------------------------------------------------------------------------
case_S15() {
    tmux_missing S15 && return
    local ses=tingly-$RUN_ID-S15 found
    pty_run S15 "$HERE/steps/S15-tmux-detach-reattach.steps" \
        "TERM=xterm-256color tmux new-session -A -s $ses"
    local rc=$?
    # The pane still holds the output, which is what a reattach would show.
    # Only the evaluated marker can match: the echoed command line carries the
    # expression instead.
    found=$(rcmd "tmux capture-pane -p -t $ses 2>/dev/null | grep -c S15-IN-42" 2>>"$ART/S15.err")
    rcmd "tmux kill-session -t $ses" >/dev/null 2>&1
    if [ $rc -eq 0 ] && [ "${found:-0}" -ge 1 ]; then
        record S15 PASS "detached with Ctrl-B d, session and pane content survived"
    else
        record S15 FAIL "rc=$rc marker in pane: ${found:-0} ($(head -c 200 "$ART/S15.err"))"
    fi
}

# ---------------------------------------------------------------------------
# S16 tmux: output keeps scrolling while the link is destroyed underneath
# ---------------------------------------------------------------------------
case_S16() {
    tmux_missing S16 && return
    local ses=tingly-$RUN_ID-S16
    rm -f "$ART/S16.ready" "$ART/S16.proceed"
    ( PTY_TIMEOUT=120 pty_run S16 "$HERE/steps/S16-tmux-across-drop.steps" \
        "TERM=xterm-256color tmux new-session -A -s $ses" ) &
    local drv=$!
    if ! wait_file "$ART/S16.ready" 90; then
        kill_quiet "$drv"; rcmd "tmux kill-session -t $ses" >/dev/null 2>&1
        record S16 FAIL "tmux session never started producing output"; return
    fi
    local before after
    before=$(grep -c 'link established' "$MAIN_LOG")
    kill -USR1 "$MAIN_PID"; sleep 2
    kill -USR1 "$MAIN_PID"; sleep 3
    after=$(grep -c 'link established' "$MAIN_LOG")
    touch "$ART/S16.proceed"
    wait "$drv"
    local rc=$? alive=no
    rcmd "tmux has-session -t $ses" >/dev/null 2>&1 && alive=yes
    rcmd "tmux kill-session -t $ses" >/dev/null 2>&1
    if [ $rc -eq 0 ] && [ "$after" -ge $((before+2)) ] && [ "$alive" = yes ]; then
        record S16 PASS "tmux output continued across $((after-before)) link failures"
    else
        record S16 FAIL "rc=$rc links $before -> $after alive=$alive ($(head -c 200 "$ART/S16.err"))"
    fi
}

# ---------------------------------------------------------------------------
# S17 tmux: the ssh client is killed outright; the work keeps running and the
#     session is reattachable, which is why people run tmux in the first place
# ---------------------------------------------------------------------------
case_S17() {
    tmux_missing S17 && return
    local ses=tingly-$RUN_ID-S17
    S17_LOG=/tmp/tingly-$RUN_ID-S17.log
    rcmd "rm -f $S17_LOG" >/dev/null 2>&1
    rm -f "$ART/S17.ready"
    ( PTY_TIMEOUT=120 pty_run S17 "$HERE/steps/S17-tmux-hard-kill.steps" \
        "TERM=xterm-256color tmux new-session -A -s $ses" ) &
    local drv=$!
    if ! wait_file "$ART/S17.ready" 90; then
        kill_quiet "$drv"; rcmd "tmux kill-session -t $ses" >/dev/null 2>&1
        record S17 FAIL "tmux session never came up"; return
    fi
    # SIGKILL the driver and the ssh under it: no orderly shutdown at all, the
    # way a laptop lid closing or a process crash looks from the far side.
    # Match on the driver and on this suite's ssh invocation only: a pattern
    # containing the session name would also hit tmux itself, which is exactly
    # the process that has to survive.
    pkill -9 -f "ptydrive.py --script $ART/S17.steps" 2>/dev/null
    pkill -9 -f "ssh -F $ART/ssh_config -tt -p $MAIN_PORT" 2>/dev/null
    kill_quiet "$drv"
    local n1 n2 found
    sleep 2; n1=$(rcmd "wc -l < $S17_LOG" 2>>"$ART/S17.err")
    sleep 3; n2=$(rcmd "wc -l < $S17_LOG" 2>>"$ART/S17.err")
    found=$(rcmd "tmux capture-pane -p -t $ses 2>/dev/null | grep -c S17-STARTED-42" 2>>"$ART/S17.err")
    rcmd "tmux kill-session -t $ses; rm -f $S17_LOG" >/dev/null 2>&1
    unset S17_LOG
    if [ "${n2:-0}" -gt "${n1:-0}" ] && [ "${found:-0}" -ge 1 ]; then
        record S17 PASS "work kept running after a hard kill ($n1 -> $n2 lines), pane reattachable"
    else
        record S17 FAIL "lines $n1 -> $n2, marker in pane ${found:-0}"
    fi
}

# ---------------------------------------------------------------------------
# S14 rsync over ssh, when rsync is available
# ---------------------------------------------------------------------------
case_S14() {
    if ! command -v rsync >/dev/null 2>&1; then
        record S14 SKIP "rsync is not installed"
        return
    fi
    local src=$ART/S14.src dst=/tmp/tingly-s14-$RUN_ID.bin
    head -c $((8*1024*1024)) /dev/urandom > "$src"
    timeout 180 rsync -q -e "ssh -F $ART/ssh_config -p $MAIN_PORT" "$src" "tingly-local:$dst" \
        > "$ART/S14.err" 2>&1
    local rc=$? a b
    a=$(sha256sum "$src" | cut -d' ' -f1)
    b=$(SSH_TIMEOUT=60 tssh -p "$MAIN_PORT" tingly-local "sha256sum $dst | cut -d' ' -f1" 2>>"$ART/S14.err")
    SSH_TIMEOUT=60 tssh -p "$MAIN_PORT" tingly-local "rm -f $dst" >/dev/null 2>&1
    if [ $rc -eq 0 ] && [ "$a" = "$b" ]; then
        record S14 PASS "rsync over ssh, 8 MiB, sha256 ${a:0:12}"
    else
        record S14 FAIL "rc=$rc $a vs ${b:-none}"
    fi
    rm -f "$src"
}

# ---------------------------------------------------------------------------

# echo_probe PORT TEXT sends TEXT to a TCP port and prints the reply.
echo_probe() {
    printf '%s' "$2" | timeout 20 python3 -c '
import socket, sys
s = socket.create_connection(("127.0.0.1", int(sys.argv[1])), 10)
s.sendall(sys.stdin.buffer.read()); s.shutdown(socket.SHUT_WR)
out = b""
while True:
    d = s.recv(4096)
    if not d: break
    out += d
sys.stdout.buffer.write(out)' "$1"
}

set_summary_meta() {
    SUMMARY_META=(
        "run: $RUN_ID"
        "host: $(uname -srm)"
        "commit: $(cd "$REPO_ROOT" && git rev-parse --short HEAD 2>/dev/null || echo unknown)"
        "ssh: $(ssh -V 2>&1 | cut -d, -f1)"
    )
}

main() {
    mkdir -p "$ART"
    log "tingly-shell SSH scenarios, run $RUN_ID"
    command -v ssh >/dev/null || die "ssh is not installed"
    command -v python3 >/dev/null || die "python3 is required by the pty driver"
    build_binary
    make_token
    make_ssh_keys
    start_sshd main "$SSHD_PORT"
    start_echo
    start_tunnel_server
    write_ssh_config

    # One resident client shared by every scenario, so multiplexing and link
    # drops are observable across cases.
    start_client main "127.0.0.1:$SSHD_PORT"
    MAIN_PORT=$CLIENT_PORT MAIN_PID=$CLIENT_PID MAIN_LOG=$CLIENT_LOG
    wait_log "$MAIN_LOG" "link established" 20 || die "tunnel client never linked"

    local all=(S1 S2 S3 S4 S5 S6 S7 S8 S9 S10 S11 S12 S13 S14 S15 S16 S17) id
    for id in "${all[@]}"; do
        selected "$id" || continue
        if [ $QUICK -eq 1 ] && { [ "$id" = S3 ] || [ "$id" = S13 ]; }; then
            record "$id" SKIP "skipped by --quick"
            continue
        fi
        log "--- $id"
        "case_$id"
    done
    set_summary_meta
    summary
}

main
