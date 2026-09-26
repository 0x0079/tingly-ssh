#!/usr/bin/env bash
# Records the README / website demo: plain ssh and ssh through tingly-ssh,
# side by side, through the same two real network events:
#
#   1. the laptop's IP address changes (Wi-Fi -> cellular)
#   2. the laptop loses the network entirely for OUTAGE seconds
#
# Nothing is simulated inside tingly-ssh or ssh. The "laptop" is a network
# namespace joined to the host by a veth pair; the events are an address
# change and the link going down, the same things the kernel sees when a
# radio switches or drops. The remote side runs a long job with a progress
# bar, and the script checks that every progress update reached the terminal
# in order and exactly once before it shows the closing caption.
#
# Needs root, iproute2, OpenSSH (client and server), tmux, bc and python3, and
# changes network state: run it in a throwaway VM or container.
#
#   sudo ./demo/record.sh            -> demo/out/demo.cast
#   agg --font-size 15 --theme github-dark demo/out/demo.cast docs/assets/demo.gif
#
# The full publish workflow (GIF, website player, markers) is in
# .claude/skills/record-demo/SKILL.md.
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$HERE/.." && pwd)
OUT=${OUT:-$HERE/out}
W=$OUT/work
OUTAGE=${OUTAGE:-15}
COLS=${COLS:-112}
ROWS=${ROWS:-9}
NS=tingly-demo
SOCK=tingly-demo

[ "$(id -u)" = 0 ] || { echo "record.sh needs root (network namespaces)" >&2; exit 1; }

cleanup() {
    tmux -L "$SOCK" kill-server 2>/dev/null || true
    [ -f "$W/pids" ] && xargs -r kill 2>/dev/null < "$W/pids" || true
    ip netns del "$NS" 2>/dev/null || true
    ip link del tdemo0 2>/dev/null || true
    pkill -f "ssh-agent -a $W/agent.sock" 2>/dev/null || true
}
trap cleanup EXIT
cleanup

rm -rf "$OUT" && mkdir -p "$W/home/.ssh" "$W/bin" /run/sshd
chmod 700 "$W/home/.ssh"

# --- network: the host is the server, the namespace is the laptop ----------
ip netns add "$NS"
ip link add tdemo0 type veth peer name tdemo1
ip link set tdemo1 netns "$NS"
ip addr add 10.77.0.1/24 dev tdemo0 && ip link set tdemo0 up
ip netns exec "$NS" ip addr add 10.77.0.2/24 dev tdemo1
ip netns exec "$NS" ip link set tdemo1 up
ip netns exec "$NS" ip link set lo up

# --- server side: a private sshd and the tunnel server ----------------------
(cd "$REPO_ROOT" && go build -o "$W/bin/tingly-ssh" ./cmd/tingly-ssh)
BIN=$W/bin/tingly-ssh

ssh-keygen -q -t ed25519 -N '' -f "$W/host_key" </dev/null
ssh-keygen -q -t ed25519 -N '' -f "$W/home/.ssh/id_ed25519" -C laptop </dev/null
cp "$W/home/.ssh/id_ed25519.pub" "$W/authorized_keys"

# The remote "work": a long job with a progress bar, the kind of thing people
# leave running over SSH. It redraws one line per update; each update names
# its sequence number, which is what the continuity check counts.
JOB_STEPS=${JOB_STEPS:-150}
cat > "$W/bin/reindex" <<EOF
#!/usr/bin/env python3
import sys, time
N, W = $JOB_STEPS, 24
t0 = time.time()
out = sys.stdout
out.write("rebuilding the search index: %d shards\\n\\n" % N)
for i in range(1, N + 1):
    time.sleep(0.3)
    fill, el = i * W // N, int(time.time() - t0)
    out.write("\\x1b[1A\\x1b[2K\\x1b[32m%s\\x1b[90m%s\\x1b[0m %3d%%  shard %03d/%d  %02d:%02d\\n"
              % ("\u2588" * fill, "\u2591" * (W - fill), i * 100 // N, i, N, el // 60, el % 60))
    out.flush()
out.write("\\x1b[1;32m\u2713 done:\\x1b[0m all %d shards indexed in %02d:%02d\\n" % (N, el // 60, el % 60))
EOF
chmod +x "$W/bin/reindex"

cat > "$W/sshd_config" <<EOF
Port 2222
ListenAddress 0.0.0.0
HostKey $W/host_key
PidFile $W/sshd.pid
AuthorizedKeysFile $W/authorized_keys
PermitRootLogin yes
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
PrintMotd no
SetEnv PATH=$W/bin:/usr/bin:/bin
EOF
/usr/sbin/sshd -f "$W/sshd_config" -D -e 2>"$W/sshd.log" &
echo $! >> "$W/pids"

"$BIN" server --listen 10.77.0.1:7443 --target 127.0.0.1:2222 \
    --authorized-keys "$W/authorized_keys" --state-dir "$W/server-state" \
    --log-level info 2>"$W/tunnel-server.log" &
echo $! >> "$W/pids"
sleep 1

# --- laptop side: one ssh config, two hosts ---------------------------------
# ServerAliveInterval on the plain host is the usual advice for "make ssh
# notice a dead network"; without it plain ssh just hangs forever instead.
cat > "$W/home/.ssh/config" <<EOF
Host devbox
    HostName 10.77.0.1
    Port 2222
    ServerAliveInterval 2
    ServerAliveCountMax 3

Host devbox-tingly
    HostName 127.0.0.1
    Port 2222
    ProxyCommand tingly-ssh proxy --server 10.77.0.1:7443 --target 127.0.0.1:2222 --identity $W/home/.ssh/id_ed25519.pub --log-level error

Host *
    User root
    IdentityFile $W/home/.ssh/id_ed25519
    IdentitiesOnly yes
    StrictHostKeyChecking accept-new
    UserKnownHostsFile $W/home/.ssh/known_hosts
EOF

# ssh reads ~/.ssh/config from the passwd entry, not $HOME: point it at ours.
# The wrapper also keeps a copy of everything ssh writes to the terminal, per
# host, so the continuity check reads exactly the bytes the viewer saw.
mkdir -p "$W/laptop-bin"
cat > "$W/laptop-bin/ssh" <<EOF
#!/bin/bash
exec > >(tee -a "$W/stdout-\$1.log")
exec /usr/bin/ssh -F "$W/home/.ssh/config" "\$@"
EOF
chmod +x "$W/laptop-bin/ssh"
LAPTOP_ENV=(env -i HOME="$W/home" PATH="$W/laptop-bin:$W/bin:/usr/bin:/bin:/usr/sbin"
    TERM=xterm-256color LANG=C.UTF-8)
AGENT=$W/agent.sock
ip netns exec "$NS" "${LAPTOP_ENV[@]}" ssh-agent -a "$AGENT" >/dev/null
ip netns exec "$NS" "${LAPTOP_ENV[@]}" SSH_AUTH_SOCK="$AGENT" ssh-add -q "$W/home/.ssh/id_ed25519"
# Warm both paths once so host keys and the tunnel key are already trusted.
ip netns exec "$NS" "${LAPTOP_ENV[@]}" SSH_AUTH_SOCK="$AGENT" ssh devbox true
ip netns exec "$NS" "${LAPTOP_ENV[@]}" SSH_AUTH_SOCK="$AGENT" ssh devbox-tingly true

# --- the recording: tmux inside the laptop namespace ------------------------
T() { tmux -L "$SOCK" "$@"; }
# Each pane gets the laptop environment spelled out: a pane split from outside
# tmux takes the environment of the tmux client, not the server's.
echo "PS1='\[\e[1;32m\]laptop\[\e[0m\]\$ '" > "$W/laptop.rc"
PANE_CMD="${LAPTOP_ENV[*]} SSH_AUTH_SOCK=$AGENT bash --noprofile --rcfile $W/laptop.rc"
ip netns exec "$NS" tmux -L "$SOCK" -f /dev/null new-session -d -s demo -x "$COLS" -y "$ROWS" "$PANE_CMD"
T split-window -h -t demo "$PANE_CMD"
T set -g status-position bottom
T set -g status-style "bg=colour236,fg=colour252"
T set -g status-left-length 200
T set -g status-right ""
T set -g window-status-format ""
T set -g window-status-current-format ""
T set -g pane-border-status top
T set -g pane-border-format " #{pane_title} "
T set -g pane-active-border-style "fg=colour240"
T set -g pane-border-style "fg=colour240"
T select-pane -t demo:0.0 -T "plain ssh"
T select-pane -t demo:0.1 -T "ssh + tingly-ssh"
T set -g history-limit 10000
caption() { T set -g status-left " #[bold]$1"; }
# mark KEY: the recording time of a story beat, for the website's chapter
# buttons (demo/out/markers.json, asciinema-player markers).
mark() {
    printf '%s %s\n' "$(echo "$(date +%s.%N) - $REC_START" | bc)" "$1" >> "$OUT/markers.txt"
}
type_in() { # pane text: type like a person
    local p=$1 s=$2 i
    for ((i = 0; i < ${#s}; i++)); do T send-keys -t "demo:0.$p" -l "${s:i:1}"; sleep 0.04; done
}

caption "#[fg=colour75]Same laptop, same server, same long job. Left: plain ssh.  Right: ssh through tingly-ssh."
python3 "$HERE/cast.py" "$OUT/demo.cast" "$COLS" "$ROWS" -- tmux -L "$SOCK" attach -t demo &
REC=$!
REC_START=$(date +%s.%N)
sleep 1.5
T send-keys -t demo:0.0 C-l; T send-keys -t demo:0.1 C-l
sleep 0.8
type_in 0 "ssh devbox reindex"; T send-keys -t demo:0.0 Enter
type_in 1 "ssh devbox-tingly reindex"; T send-keys -t demo:0.1 Enter
sleep 6

mark switch
caption "#[fg=colour214]Wi-Fi -> 5G: the laptop's IP address changes (10.77.0.2 -> 10.77.0.3)"
ip netns exec "$NS" ip addr del 10.77.0.2/24 dev tdemo1
ip netns exec "$NS" ip addr add 10.77.0.3/24 dev tdemo1
sleep 11

mark outage
caption "#[fg=colour203]Into a tunnel: no network at all for ${OUTAGE} seconds"
ip netns exec "$NS" ip link set tdemo1 down
for ((s = OUTAGE; s > 0; s--)); do
    caption "#[fg=colour203]Into a tunnel: no network at all ... ${s}s left"
    sleep 1
done
ip netns exec "$NS" ip link set tdemo1 up
mark back
caption "#[fg=colour114]Back online: the same session resumes and catches up with the job"

# Wait for the job to finish on the right, then check what actually reached
# the terminal: every progress update, in order, exactly once. Only a passing
# check earns the closing caption.
TLOG=$W/stdout-devbox-tingly.log
for ((i = 0; i < ${RESUME_WAIT:-40}; i++)); do
    grep -q 'done:' "$TLOG" 2>/dev/null && break
    sleep 1
done
sleep 1
if VERDICT=$(python3 - "$TLOG" "$JOB_STEPS" <<'PY'
import re, sys
text = open(sys.argv[1], encoding="utf-8", errors="replace").read()
n = int(sys.argv[2])
seen = [int(m) for m in re.findall(r"shard (\d+)/%d" % n, text)]
ok = seen == list(range(1, n + 1)) and "done:" in text
print(f"{len(seen)}/{n} progress updates, in order, none repeated" if ok
      else f"FAILED: {len(seen)} updates, first problem near {next((i + 1 for i, v in enumerate(seen) if v != i + 1), len(seen) + 1)}")
sys.exit(0 if ok else 1)
PY
); then
    caption "#[fg=colour114]Checked: all $VERDICT. Same ssh session, sshd untouched."
    sleep 6
else
    echo "continuity check $VERDICT (see $TLOG)" >&2
    kill "$REC"; exit 1
fi
kill "$REC"; wait "$REC" 2>/dev/null || true
T capture-pane -p -S - -t demo:0.0 > "$OUT/plain-ssh.txt"
T capture-pane -p -S - -t demo:0.1 > "$OUT/tingly.txt"
T kill-server
echo "continuity: $VERDICT"
cp "$W/tunnel-server.log" "$TLOG" "$OUT/"
awk 'BEGIN { printf "[" } { printf "%s[%.2f, \"%s\"]", (NR > 1 ? ", " : ""), $1, $2 } END { print "]" }' \
    "$OUT/markers.txt" > "$OUT/markers.json"
echo "recorded $OUT/demo.cast (markers: $(cat "$OUT/markers.json"))"
