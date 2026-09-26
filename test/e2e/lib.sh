#!/usr/bin/env bash
# Environment for the end-to-end suite. See docs/07-verification-plan.md.

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
BIN=${TINGLY_BIN:-$REPO_ROOT/test/e2e/.bin/tingly-ssh}
RUN_ID=$(date +%Y%m%d-%H%M%S)
ART=${ART_DIR:-$REPO_ROOT/test/e2e/artifacts/$RUN_ID}

SSHD_PORT=${SSHD_PORT:-2022}                # stands in for the jump host
TARGET_SSHD_PORT=${TARGET_SSHD_PORT:-2224}  # stands in for the machine behind it
TUNNEL_PORT=${TUNNEL_PORT:-7450}
KEY_TUNNEL_PORT=${KEY_TUNNEL_PORT:-7452}    # the server that admits SSH keys
ECHO_PORT=${ECHO_PORT:-2023}
NEXT_CLIENT_PORT=${NEXT_CLIENT_PORT:-2300}

TEST_LIB=$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)
source "$TEST_LIB/common.sh"
source "$TEST_LIB/fixtures.sh"

SUMMARY_TITLE="tingly-ssh end-to-end verification"

# set_summary_meta stamps the environment into results.md.
set_summary_meta() {
    SUMMARY_META=(
        "run: $RUN_ID"
        "host: $(uname -srm)"
        "go: $(cd "$REPO_ROOT" && go version | awk '{print $3}')"
        "commit: $(cd "$REPO_ROOT" && git rev-parse --short HEAD 2>/dev/null || echo unknown)"
        "ssh: $(ssh -V 2>&1 | cut -d, -f1)"
    )
}
