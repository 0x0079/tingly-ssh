#!/usr/bin/env bash
# Fixtures shared by the verification suites: a dedicated sshd (or two), a
# plain TCP service, the tunnel server and tunnel clients.
#
# The caller sets ART, REPO_ROOT, BIN and the port variables before calling
# anything here. Nothing in this file touches the system's SSH configuration:
# every sshd gets its own config file, host key and authorized_keys, which is
# the same constraint a real jump host imposes.

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

# make_ssh_keys creates the one key pair every sshd in the suite trusts.
make_ssh_keys() {
    SSH_KEYS=$ART/sshd
    mkdir -p "$SSH_KEYS"
    # ssh-keygen has no force flag: it prompts to overwrite and would hang
    # forever on a reused artifacts directory.
    rm -f "$SSH_KEYS/id_ed25519" "$SSH_KEYS/id_ed25519.pub"
    ssh-keygen -q -t ed25519 -N '' -f "$SSH_KEYS/id_ed25519" -C tingly-e2e </dev/null || die "client key"
    cp "$SSH_KEYS/id_ed25519.pub" "$SSH_KEYS/authorized_keys"
    chmod 600 "$SSH_KEYS/authorized_keys" "$SSH_KEYS/id_ed25519"
}

# start_sshd NAME PORT runs a dedicated sshd with its own config file and host
# key. The system's sshd configuration is never touched, which is the whole
# point: a real jump host cannot be modified either.
start_sshd() {
    local name=$1 port=$2
    require_port_free "$port" "sshd $name"
    local dir=$ART/sshd-$name
    mkdir -p "$dir" /run/sshd
    rm -f "$dir/host_ed25519" "$dir/host_ed25519.pub"
    ssh-keygen -q -t ed25519 -N '' -f "$dir/host_ed25519" </dev/null || die "host key"
    cat > "$dir/sshd_config" <<EOF
Port $port
ListenAddress 127.0.0.1
HostKey $dir/host_ed25519
PidFile $dir/sshd.pid
AuthorizedKeysFile $SSH_KEYS/authorized_keys
PermitRootLogin yes
PubkeyAuthentication yes
AllowTcpForwarding yes
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
PrintMotd no
X11Forwarding no
Subsystem sftp /usr/lib/openssh/sftp-server
LogLevel VERBOSE
EOF
    /usr/sbin/sshd -f "$dir/sshd_config" -D -e > "$ART/sshd-$name.log" 2>&1 &
    SSHD_PID=$!
    track "$SSHD_PID"
    wait_tcp 127.0.0.1 "$port" 15 || die "sshd '$name' did not listen on $port (see $ART/sshd-$name.log)"
    info "sshd '$name' listening on 127.0.0.1:$port (pid $SSHD_PID)"
}

# start_echo runs a TCP uppercase-echo service, standing in for any plain TCP
# service so the tunnel can be verified without SSH in the picture.
start_echo() {
    require_port_free "$ECHO_PORT" "echo service"
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

# start_tunnel_server_with_credentials NAME PORT CREDFILE starts a second
# server that authenticates per-device credentials instead of a shared token.
# Sets CRED_SERVER_PID, CRED_SERVER_LOG and CRED_SERVER_PIN.
start_tunnel_server_with_credentials() {
    local name=$1 port=$2 credfile=$3
    require_port_free "$port" "credential server $name"
    CRED_SERVER_LOG=$ART/tunnel-server-$name.log
    "$BIN" server \
        --listen "127.0.0.1:$port" \
        --target "127.0.0.1:$SSHD_PORT" \
        --credentials "$credfile" \
        --state-dir "$ART/server-state-$name" \
        --log-level "${TUNNEL_LOG_LEVEL:-info}" \
        > "$CRED_SERVER_LOG" 2>&1 &
    CRED_SERVER_PID=$!
    track "$CRED_SERVER_PID"
    wait_log "$CRED_SERVER_LOG" "server listening" 15 || die "credential server did not start"
    CRED_SERVER_PIN=$(grep -o 'sha256:[A-Za-z0-9+/=]*' "$CRED_SERVER_LOG" | head -1)
    info "credential server '$name' on udp/$port (pid $CRED_SERVER_PID)"
}

# mint_credential LABEL -> writes $ART/<label>.token (0600) and echoes the
# server record line, exactly the way an operator would run keygen.
mint_credential() {
    local label=$1
    "$BIN" keygen --label "$label" > "$ART/$label.token" 2> "$ART/$label.keygen"
    chmod 600 "$ART/$label.token"
    grep -E "^$label[[:space:]]+sha256:" "$ART/$label.keygen"
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

# Two hops: ssh reaches the jump host through the tunnel, then the jump host
# forwards to the machine behind it with stock direct-tcpip. Nothing on the
# jump host changes; ProxyJump is plain OpenSSH.
Host behind-jump
    HostName 127.0.0.1
    Port $TARGET_SSHD_PORT
    ProxyJump tingly-proxy

Host *
    User $(id -un)
    IdentityFile $SSH_KEYS/id_ed25519
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
tssh() { timeout "${SSH_TIMEOUT:-60}" ssh ${SSH_DEBUG:+-vv} -F "$ART/ssh_config" "$@"; }
