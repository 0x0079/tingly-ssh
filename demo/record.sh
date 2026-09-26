#!/usr/bin/env bash
# Records the README / website demo: plain ssh and ssh through tingly-shell,
# side by side, through the same two real network events:
#
#   1. the laptop's IP address changes (Wi-Fi -> cellular)
#   2. the laptop loses the network entirely for OUTAGE seconds
#
# Nothing is simulated inside tingly-shell or ssh. The "laptop" is a network
# namespace joined to the host by a veth pair; the events are an address
# change and the link going down, the same things the kernel sees when a
# radio switches or drops. The remote side runs a counter, so continuity (no
# missing or repeated numbers) is visible in the recording itself.
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
ROWS=${ROWS:-26}
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
(cd "$REPO_ROOT" && go build -o "$W/bin/tingly-shell" ./cmd/tingly-shell)
BIN=$W/bin/tingly-shell

ssh-keygen -q -t ed25519 -N '' -f "$W/host_key" </dev/null
ssh-keygen -q -t ed25519 -N '' -f "$W/home/.ssh/id_ed25519" -C laptop </dev/null
cp "$W/home/.ssh/id_ed25519.pub" "$W/authorized_keys"

# The remote "work": one numbered line every half second.
cat > "$W/bin/heartbeat" <<'EOF'
#!/bin/sh
i=0
while :; do
    i=$((i + 1))
    printf '%s  build step %03d  ok\n' "$(date +%T)" "$i"
    sleep 0.5
done
EOF
chmod +x "$W/bin/heartbeat"

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
    ProxyCommand tingly-shell proxy --server 10.77.0.1:7443 --target 127.0.0.1:2222 --identity $W/home/.ssh/id_ed25519.pub --log-level error

Host *
    User root
    IdentityFile $W/home/.ssh/id_ed25519
    IdentitiesOnly yes
    StrictHostKeyChecking accept-new
    UserKnownHostsFile $W/home/.ssh/known_hosts
EOF

# ssh reads ~/.ssh/config from the passwd entry, not $HOME: point it at ours.
mkdir -p "$W/laptop-bin"
printf '#!/bin/sh\nexec /usr/bin/ssh -F "%s" "$@"\n' "$W/home/.ssh/config" > "$W/laptop-bin/ssh"
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
T select-pane -t demo:0.1 -T "ssh + tingly-shell"
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

caption "#[fg=colour75]Same laptop, same server. Left: plain ssh.  Right: ssh through tingly-shell."
python3 "$HERE/cast.py" "$OUT/demo.cast" "$COLS" "$ROWS" -- tmux -L "$SOCK" attach -t demo &
REC=$!
REC_START=$(date +%s.%N)
sleep 1.5
T send-keys -t demo:0.0 C-l; T send-keys -t demo:0.1 C-l
sleep 0.8
type_in 0 "ssh devbox heartbeat"; T send-keys -t demo:0.0 Enter
type_in 1 "ssh devbox-tingly heartbeat"; T send-keys -t demo:0.1 Enter
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
caption "#[fg=colour114]Back online: the same session resumes, and every line printed meanwhile arrives"
sleep ${RESUME_WAIT:-22}

caption "#[fg=colour114]Same ssh session, nothing re-run, no line lost or repeated. sshd untouched."
sleep 5
kill "$REC"; wait "$REC" 2>/dev/null || true
# The whole scrollback of both panes, so the claim "no lost or repeated lines"
# is checked rather than eyeballed.
T capture-pane -p -S - -t demo:0.0 > "$OUT/plain-ssh.txt"
T capture-pane -p -S - -t demo:0.1 > "$OUT/tingly.txt"
T kill-server
python3 - "$OUT/tingly.txt" <<'EOF'
import re, sys
steps = [int(m) for m in re.findall(r"build step (\d+)", open(sys.argv[1]).read())]
gaps = [n for n in range(1, steps[-1] + 1) if n not in steps]
dups = len(steps) - len(set(steps))
print(f"tingly pane: steps 1..{steps[-1]}, missing={gaps or 'none'}, repeated={dups}")
sys.exit(1 if gaps or dups else 0)
EOF
cp "$W/tunnel-server.log" "$OUT/"
awk 'BEGIN { printf "[" } { printf "%s[%.2f, \"%s\"]", (NR > 1 ? ", " : ""), $1, $2 } END { print "]" }' \
    "$OUT/markers.txt" > "$OUT/markers.json"
echo "recorded $OUT/demo.cast (markers: $(cat "$OUT/markers.json"))"
