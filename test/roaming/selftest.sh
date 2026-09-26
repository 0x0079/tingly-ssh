#!/usr/bin/env bash
# Proves the roaming harness itself is sound, without any radios.
#
# It stands up a local sshd and tunnel server, then runs run.sh with
# NET_CTL=sim, where "switch networks" means destroying the QUIC link and
# "lose the network" means freezing the server so packets go nowhere. Every
# measurement, threshold and verdict is the same code that runs on a laptop;
# only the four network verbs differ.
#
#   ./selftest.sh              all cases, shortened timings
#   ./selftest.sh R1 R4        only the named cases

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$HERE/../.." && pwd)
RUN_ID=selftest-$(date +%Y%m%d-%H%M%S)
ART=$HERE/artifacts/$RUN_ID
BIN=${TINGLY_BIN:-$HERE/.bin/tingly-ssh}

SSHD_PORT=${SSHD_PORT:-2042}
TUNNEL_PORT=${TUNNEL_PORT:-7470}
ECHO_PORT=${ECHO_PORT:-2043}
NEXT_CLIENT_PORT=2600

TEST_LIB=$(cd "$HERE/../lib" && pwd)
source "$TEST_LIB/common.sh"
source "$TEST_LIB/fixtures.sh"

mkdir -p "$ART"
log "roaming harness self-test, run $RUN_ID"
build_binary
make_token
make_ssh_keys
start_sshd main "$SSHD_PORT"
start_tunnel_server

# A restart command for R7: kill this server and start an identical one.
cat > "$ART/restart-server.sh" <<EOF
#!/usr/bin/env bash
kill $SERVER_PID 2>/dev/null
sleep 1
nohup "$BIN" server --listen "127.0.0.1:$TUNNEL_PORT" \\
    --target "127.0.0.1:$SSHD_PORT" --token-file "$ART/token" \\
    --state-dir "$ART/server-state" > "$ART/tunnel-server-2.log" 2>&1 &
echo \$! > "$ART/server2.pid"
sleep 2
EOF
chmod +x "$ART/restart-server.sh"
cleanup_server2() { [ -f "$ART/server2.pid" ] && kill_quiet "$(cat "$ART/server2.pid")"; }
trap 'cleanup_server2; cleanup' EXIT

export TINGLY_BIN=$BIN
export TINGLY_SERVER=127.0.0.1:$TUNNEL_PORT
export TINGLY_TARGET=127.0.0.1:$SSHD_PORT
export TINGLY_TOKEN_FILE=$ART/token
export TINGLY_PIN=$PIN
export SSH_USER=$(id -un)
export NET_CTL=sim
export TINGLY_SERVER_PID=$SERVER_PID
export CLIENT_PORT=2610
export ART_DIR=$ART/roaming
export SERVER_RESTART_CMD=$ART/restart-server.sh
# Short timings: the point is to exercise the harness, not to wait.
# The outage must outlast the client's 8s idle timeout, otherwise the link
# never dies and R3 would prove nothing about resumption.
export LINGER=15 OUTAGE_SECONDS=12 OBSERVE_SECONDS=12 SETTLE_SECONDS=3
export MAX_STALL=15 IDLE_SECONDS=20 BULK_MB=8
export SSH_EXTRA_CONFIG="    IdentityFile $SSH_KEYS/id_ed25519
    IdentitiesOnly yes"

log "handing over to run.sh with NET_CTL=sim"
ROAMING_ENV=/dev/null "$HERE/run.sh" "$@"
