#!/usr/bin/env bash
# Shared helpers for the tingly-shell end-to-end verification suite.
# See docs/07-verification-plan.md for the plan these scripts implement.

set -uo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
BIN=${TINGLY_BIN:-$REPO_ROOT/test/e2e/.bin/tingly-shell}
RUN_ID=$(date +%Y%m%d-%H%M%S)
ART=${ART_DIR:-$REPO_ROOT/test/e2e/artifacts/$RUN_ID}

SSHD_PORT=${SSHD_PORT:-2022}
TUNNEL_PORT=${TUNNEL_PORT:-7450}
ECHO_PORT=${ECHO_PORT:-2023}
NEXT_CLIENT_PORT=${NEXT_CLIENT_PORT:-2300}

PIDS=()
declare -A RESULT_STATUS RESULT_NOTE
CASE_ORDER=()

# --- output -----------------------------------------------------------------

if [ -t 1 ]; then C_RED=$'\e[31m'; C_GRN=$'\e[32m'; C_YEL=$'\e[33m'; C_OFF=$'\e[0m'
else C_RED=; C_GRN=; C_YEL=; C_OFF=; fi

log()  { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }
info() { log "· $*"; }
die()  { log "${C_RED}fatal${C_OFF} $*"; exit 1; }

# --- process management -----------------------------------------------------

track() { PIDS+=("$1"); }

kill_quiet() {
    local pid=$1
    [ -n "$pid" ] || return 0
    kill -CONT "$pid" 2>/dev/null
    kill "$pid" 2>/dev/null
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        kill -0 "$pid" 2>/dev/null || return 0
        sleep 0.2
    done
    kill -9 "$pid" 2>/dev/null
}

cleanup() {
    local i
    for ((i=${#PIDS[@]}-1; i>=0; i--)); do kill_quiet "${PIDS[$i]}"; done
}
trap cleanup EXIT

# wait_tcp HOST PORT [SECONDS]
wait_tcp() {
    local host=$1 port=$2 secs=${3:-15} i
    for ((i=0; i<secs*10; i++)); do
        if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then exec 3>&- 2>/dev/null; return 0; fi
        sleep 0.1
    done
    return 1
}

# wait_log FILE PATTERN [SECONDS]
wait_log() {
    local file=$1 pat=$2 secs=${3:-15} i
    for ((i=0; i<secs*10; i++)); do
        [ -f "$file" ] && grep -q -- "$pat" "$file" && return 0
        sleep 0.1
    done
    return 1
}

# --- fixtures ---------------------------------------------------------------

build_binary() {
    mkdir -p "$(dirname "$BIN")"
    if [ -n "${TINGLY_BIN:-}" ]; then
        [ -x "$BIN" ] || die "TINGLY_BIN=$BIN is not executable"
        info "using prebuilt binary $BIN"
        return
    fi
    info "building tingly-shell"
    (cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/tingly-shell) || die "build failed"
}

make_token() {
    "$BIN" keygen 2>/dev/null > "$ART/token" || die "keygen failed"
    chmod 600 "$ART/token"
}

# start_sshd runs a dedicated sshd with its own config, host key and
# authorized_keys. The system's sshd configuration is never touched.
start_sshd() {
    local dir=$ART/sshd
    mkdir -p "$dir" /run/sshd
    ssh-keygen -q -t ed25519 -N '' -f "$dir/host_ed25519" || die "host key"
    ssh-keygen -q -t ed25519 -N '' -f "$dir/id_ed25519" -C tingly-e2e || die "client key"
    cp "$dir/id_ed25519.pub" "$dir/authorized_keys"
    chmod 600 "$dir/authorized_keys" "$dir/id_ed25519"
    cat > "$dir/sshd_config" <<EOF
Port $SSHD_PORT
ListenAddress 127.0.0.1
HostKey $dir/host_ed25519
PidFile $dir/sshd.pid
AuthorizedKeysFile $dir/authorized_keys
PermitRootLogin yes
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
PrintMotd no
X11Forwarding no
Subsystem sftp /usr/lib/openssh/sftp-server
LogLevel VERBOSE
EOF
    /usr/sbin/sshd -f "$dir/sshd_config" -D -e > "$ART/sshd.log" 2>&1 &
    SSHD_PID=$!
    track "$SSHD_PID"
    wait_tcp 127.0.0.1 "$SSHD_PORT" 15 || die "sshd did not listen on $SSHD_PORT (see $ART/sshd.log)"
    info "sshd listening on 127.0.0.1:$SSHD_PORT (pid $SSHD_PID)"
}

# start_echo runs a TCP uppercase-echo service, standing in for any plain TCP
# service so the tunnel can be verified without SSH in the picture.
start_echo() {
    python3 - "$ECHO_PORT" > "$ART/echo.log" 2>&1 <<'PY' &
import socketserver, sys
class H(socketserver.BaseRequestHandler):
    def handle(self):
        while True:
            d = self.request.recv(4096)
            if not d: break
            self.request.sendall(d.upper())
class S(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
S(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY
    ECHO_PID=$!
    track "$ECHO_PID"
    wait_tcp 127.0.0.1 "$ECHO_PORT" 10 || die "echo service did not start"
    info "echo service listening on 127.0.0.1:$ECHO_PORT (pid $ECHO_PID)"
}

start_tunnel_server() {
    "$BIN" server \
        --listen "127.0.0.1:$TUNNEL_PORT" \
        --target "127.0.0.1:$SSHD_PORT,127.0.0.1:$ECHO_PORT" \
        --token-file "$ART/token" \
        --state-dir "$ART/server-state" \
        --log-level "${TUNNEL_LOG_LEVEL:-info}" \
        > "$ART/tunnel-server.log" 2>&1 &
    SERVER_PID=$!
    track "$SERVER_PID"
    wait_log "$ART/tunnel-server.log" "server listening" 15 || die "tunnel server did not start"
    PIN=$(grep -o 'sha256:[A-Za-z0-9+/=]*' "$ART/tunnel-server.log" | head -1)
    [ -n "$PIN" ] || die "could not read the server pin"
    info "tunnel server on udp/$TUNNEL_PORT (pid $SERVER_PID) pin=$PIN"
}

# start_client NAME TARGET [extra flags...] -> CLIENT_PORT, CLIENT_PID, CLIENT_LOG
start_client() {
    local name=$1 target=$2; shift 2
    CLIENT_PORT=$((NEXT_CLIENT_PORT++))
    CLIENT_LOG=$ART/client-$name.log
    "$BIN" client \
        --server "127.0.0.1:$TUNNEL_PORT" \
        --listen "127.0.0.1:$CLIENT_PORT" \
        --target "$target" \
        --token-file "${CLIENT_TOKEN_FILE:-$ART/token}" \
        --pin "${CLIENT_PIN:-$PIN}" \
        --log-level "${CLIENT_LOG_LEVEL:-info}" \
        "$@" > "$CLIENT_LOG" 2>&1 &
    CLIENT_PID=$!
    track "$CLIENT_PID"
    # Wait on the log rather than a TCP probe: probing would open a tunnel
    # stream of its own and pollute the per-case stream counts. Cases that
    # expect the client to be refused set CLIENT_NO_WAIT.
    if [ -z "${CLIENT_NO_WAIT:-}" ]; then
        wait_log "$CLIENT_LOG" "client listening" 10 || die "client $name did not listen"
    fi
    return 0
}

# ssh_config writes a config that adds a ProxyCommand host and a direct host.
# Neither the system ssh_config nor sshd_config is modified: the tunnel is
# introduced purely through this extra config file.
write_ssh_config() {
    cat > "$ART/ssh_config" <<EOF
Host tingly-proxy
    HostName 127.0.0.1
    Port $SSHD_PORT
    ProxyCommand $BIN proxy --server 127.0.0.1:$TUNNEL_PORT --target 127.0.0.1:$SSHD_PORT --token-file $ART/token --pin $PIN --log-level warn

Host tingly-local
    HostName 127.0.0.1

Host *
    User $(id -un)
    IdentityFile $ART/sshd/id_ed25519
    IdentitiesOnly yes
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    LogLevel ERROR
    ServerAliveInterval 0
    BatchMode yes
EOF
}

# tssh wraps the real ssh client. The timeout lives inside the function
# because `timeout` cannot run a shell function.
tssh() { timeout "${SSH_TIMEOUT:-60}" ssh -F "$ART/ssh_config" "$@"; }

# wait_exit PID SECONDS -> sets WAIT_RC to the exit code, or 124 on timeout,
# and WAIT_ELAPSED to how long it took. Not a subshell: `wait` only works for
# a direct child of the running shell.
wait_exit() {
    local pid=$1 secs=$2 i start=$SECONDS
    WAIT_RC=124
    for ((i=0; i<secs*10; i++)); do
        if ! kill -0 "$pid" 2>/dev/null; then
            wait "$pid" 2>/dev/null
            WAIT_RC=$?
            break
        fi
        sleep 0.1
    done
    WAIT_ELAPSED=$((SECONDS-start))
}

# --- case bookkeeping -------------------------------------------------------

record() { # record ID STATUS NOTE
    CASE_ORDER+=("$1")
    RESULT_STATUS[$1]=$2
    RESULT_NOTE[$1]=$3
    case $2 in
        PASS) log "${C_GRN}PASS${C_OFF} $1 ${RESULT_NOTE[$1]}" ;;
        SKIP) log "${C_YEL}SKIP${C_OFF} $1 ${RESULT_NOTE[$1]}" ;;
        *)    log "${C_RED}FAIL${C_OFF} $1 ${RESULT_NOTE[$1]}" ;;
    esac
}

summary() {
    local pass=0 fail=0 skip=0 id
    {
        echo "# tingly-shell end-to-end verification"
        echo
        echo "- run: $RUN_ID"
        echo "- host: $(uname -srm)"
        echo "- go: $(cd "$REPO_ROOT" && go version | awk '{print $3}')"
        echo "- commit: $(cd "$REPO_ROOT" && git rev-parse --short HEAD 2>/dev/null || echo unknown)"
        echo "- ssh: $(ssh -V 2>&1 | cut -d, -f1)"
        echo
        echo "| case | result | note |"
        echo "| --- | --- | --- |"
        for id in "${CASE_ORDER[@]}"; do
            printf '| %s | %s | %s |\n' "$id" "${RESULT_STATUS[$id]}" "${RESULT_NOTE[$id]}"
        done
    } > "$ART/results.md"

    for id in "${CASE_ORDER[@]}"; do
        case ${RESULT_STATUS[$id]} in
            PASS) pass=$((pass+1)) ;;
            SKIP) skip=$((skip+1)) ;;
            *)    fail=$((fail+1)) ;;
        esac
    done
    echo
    log "results: $pass passed, $fail failed, $skip skipped"
    log "artifacts: $ART"
    [ "$fail" -eq 0 ]
}
