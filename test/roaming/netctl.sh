#!/usr/bin/env bash
# Network control for the roaming suite.
#
# The physical part of a roaming test (turning a radio off) is the only piece
# that cannot be scripted portably, so it sits behind four verbs. Everything
# else in the suite -- timing, stream integrity, verdicts, evidence -- is
# automatic regardless of which controller is in use.
#
#   net_away   leave the current network for another one (Wi-Fi -> cellular)
#   net_back   return to it
#   net_down   lose connectivity entirely
#   net_up     restore it
#
# Controllers:
#   nmcli   Linux with NetworkManager, fully automatic
#   macos   macOS, fully automatic (needs a second path, e.g. cellular/tether)
#   manual  any OS: prompts the operator and waits, still measures everything
#   sim     no radios: destroys the link / freezes the server, for self-testing
#           the harness itself (see selftest.sh)

NET_CTL=${NET_CTL:-auto}
WIFI_IFACE=${WIFI_IFACE:-}

net_ctl_init() {
    if [ "$NET_CTL" = auto ]; then
        if command -v nmcli >/dev/null 2>&1 && nmcli -t radio wifi >/dev/null 2>&1; then
            NET_CTL=nmcli
        elif [ "$(uname -s)" = Darwin ] && command -v networksetup >/dev/null 2>&1; then
            NET_CTL=macos
        else
            NET_CTL=manual
        fi
    fi
    case $NET_CTL in
        nmcli) command -v nmcli >/dev/null || die "nmcli not found" ;;
        macos)
            [ -n "$WIFI_IFACE" ] || WIFI_IFACE=$(networksetup -listallhardwareports \
                | awk '/Wi-Fi|AirPort/{getline; print $2; exit}')
            [ -n "$WIFI_IFACE" ] || die "could not find the Wi-Fi interface; set WIFI_IFACE"
            ;;
        manual) ;;
        sim)
            [ -n "${TINGLY_SERVER_PID:-}" ] || die "NET_CTL=sim needs TINGLY_SERVER_PID"
            ;;
        *) die "unknown NET_CTL=$NET_CTL" ;;
    esac
    info "network controller: $NET_CTL${WIFI_IFACE:+ (iface $WIFI_IFACE)}"
}

net_describe() { echo "$NET_CTL"; }

# sim_signal freezes or thaws the local server. It tolerates a server that is
# already gone, because R7 deliberately restarts it.
sim_signal() {
    local sig=$1 pid=${TINGLY_SERVER_PID:?}
    if kill -0 "$pid" 2>/dev/null; then
        kill -"$sig" "$pid" || die "signal $sig to the simulated server"
    else
        info "simulated server $pid is gone; nothing to $sig"
    fi
}

net_away() {
    case $NET_CTL in
        nmcli)  nmcli radio wifi off >/dev/null || die "nmcli radio wifi off" ;;
        macos)  networksetup -setairportpower "$WIFI_IFACE" off || die "airport off" ;;
        manual) ask "switch the network now: turn Wi-Fi OFF so the laptop moves to cellular" ;;
        sim)    kill -USR1 "${CLIENT_PID:?}" || die "signal client" ;;
    esac
    info "network: moved away from the current path"
}

net_back() {
    case $NET_CTL in
        nmcli)  nmcli radio wifi on >/dev/null || die "nmcli radio wifi on" ;;
        macos)  networksetup -setairportpower "$WIFI_IFACE" on || die "airport on" ;;
        manual) ask "switch back: turn Wi-Fi ON" ;;
        sim)    kill -USR1 "${CLIENT_PID:?}" || die "signal client" ;;
    esac
    info "network: moved back"
}

net_down() {
    case $NET_CTL in
        nmcli)  nmcli networking off >/dev/null || die "nmcli networking off" ;;
        macos)
            networksetup -setairportpower "$WIFI_IFACE" off || die "airport off"
            info "note: this is only a true outage if no other interface is up"
            ;;
        manual) ask "cut the network completely now (airplane mode ON)" ;;
        sim)    sim_signal STOP ;;
    esac
    info "network: down"
}

net_up() {
    case $NET_CTL in
        nmcli)  nmcli networking on >/dev/null || die "nmcli networking on" ;;
        macos)  networksetup -setairportpower "$WIFI_IFACE" on || die "airport on" ;;
        manual) ask "restore the network now (airplane mode OFF)" ;;
        sim)    sim_signal CONT ;;
    esac
    info "network: up"
}
