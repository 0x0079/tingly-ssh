#!/usr/bin/env bash
# Shared plumbing for the verification harnesses under test/.
# Fixtures live with each suite; this file only holds generic machinery.

set -uo pipefail

PIDS=()
declare -A RESULT_STATUS RESULT_NOTE
CASE_ORDER=()
# A suite may parse its command line before sourcing this file, so never
# clobber a selection that is already there.
[ -n "${SELECT+set}" ] || SELECT=()
SUMMARY_TITLE=${SUMMARY_TITLE:-verification}
SUMMARY_META=()

# --- output -----------------------------------------------------------------

if [ -t 1 ]; then C_RED=$'\e[31m'; C_GRN=$'\e[32m'; C_YEL=$'\e[33m'; C_OFF=$'\e[0m'
else C_RED=; C_GRN=; C_YEL=; C_OFF=; fi

log()  { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*"; }
info() { log "· $*"; }
die()  { log "${C_RED}fatal${C_OFF} $*"; exit 1; }
ask()  { # ask PROMPT -- waits for the operator, used by the manual net controller
    printf '\n%s>>> %s\n>>> press Enter when done: %s' "${C_YEL}" "$*" "${C_OFF}"
    read -r _
}

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

# wait_file PATH [SECONDS] blocks until PATH exists.
wait_file() {
    local path=$1 secs=${2:-30} i
    for ((i=0; i<secs*10; i++)); do
        [ -e "$path" ] && return 0
        sleep 0.1
    done
    return 1
}

# wait_exit PID SECONDS -> sets WAIT_RC (124 on timeout) and WAIT_ELAPSED.
# Not a subshell: `wait` only works for a direct child of the running shell.
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

# require_port_free PORT NAME dies if something already listens there. Without
# it a leftover process from an earlier run gets adopted silently and the case
# fails for reasons that have nothing to do with the code under test.
require_port_free() {
    local port=$1 name=$2
    if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
        exec 3>&- 2>/dev/null
        die "port $port ($name) is already in use; stop the leftover process first"
    fi
}

# --- line timestamping ------------------------------------------------------

# stamp_lines prefixes each stdin line with a local monotonic-ish timestamp, so
# a heartbeat's arrival gaps can be measured. perl is present on macOS and
# Linux; python3 is the fallback.
stamp_lines() {
    if command -v perl >/dev/null 2>&1; then
        # $|=1 matters: block buffering would hide the heartbeat from the
        # measurement until the pipeline ends.
        perl -MTime::HiRes=time -ne 'BEGIN{$|=1} printf "%.3f %s", time, $_' -
    elif command -v python3 >/dev/null 2>&1; then
        python3 -u -c '
import sys, time
for line in sys.stdin:
    sys.stdout.write("%.3f %s" % (time.time(), line))'
    else
        die "need perl or python3 to timestamp the heartbeat"
    fi
}

# analyse_heartbeat FILE -> HB_LINES, HB_LAST, HB_MAX_GAP, HB_BREAKS
#
# The remote emits a counter, one line at a time. Continuity of the counter
# proves the session layer delivered every byte exactly once and in order; the
# largest arrival gap is how long the user actually saw the terminal freeze.
analyse_heartbeat() {
    local f=$1
    read -r HB_LINES HB_LAST HB_MAX_GAP HB_BREAKS <<<"$(
        awk 'NF>=2 {
                n++
                if (n == 1) { expect = $2 } 
                else { g = $1 - prev; if (g > max) max = g }
                if ($2 != expect) breaks++
                expect = $2 + 1
                prev = $1
                last = $2
             }
             END { printf "%d %d %.2f %d\n", n+0, last+0, max+0, breaks+0 }' "$f" 2>/dev/null
    )"
    HB_LINES=${HB_LINES:-0}; HB_LAST=${HB_LAST:-0}
    HB_MAX_GAP=${HB_MAX_GAP:-0}; HB_BREAKS=${HB_BREAKS:-0}
}

# --- case selection ---------------------------------------------------------

# selected ID reports whether a case should run, given the SELECT array the
# suite filled in from its command line (empty SELECT means "run everything").
selected() {
    [ ${#SELECT[@]} -eq 0 ] && return 0
    local id
    for id in "${SELECT[@]}"; do [ "$id" = "$1" ] && return 0; done
    return 1
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

# summary writes $ART/results.md and returns non-zero if anything failed.
summary() {
    local pass=0 fail=0 skip=0 id line
    {
        echo "# $SUMMARY_TITLE"
        echo
        for line in "${SUMMARY_META[@]}"; do echo "- $line"; done
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
