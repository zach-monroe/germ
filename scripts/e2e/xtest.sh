#!/bin/bash
# End-to-end input test on a virtual X server (safe: never touches your desktop).
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/e2e/xtest.sh out.log '\x1b[>1u' -- "key ctrl+a" "type hello"
#
# Runs germ with keylog.py (which optionally writes SETUP, e.g. mode
# changes, to the terminal), performs each xdotool action, and leaves the
# bytes germ sent in LOG, one read per line.
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
GERM=${GERM:-$HERE/../../germ}
LOG=$1; SETUP=$2; shift 3
export DISPLAY=${DISPLAY_E2E:-:99}
"$GERM" python3 "$HERE/keylog.py" "$LOG" "$SETUP" >/dev/null 2>&1 &
GPID=$!
for _ in $(seq 50); do WID=$(xdotool search --pid $GPID 2>/dev/null | head -1); [ -n "$WID" ] && break; sleep 0.1; done
sleep 0.8
xdotool windowfocus --sync "$WID"
for act in "$@"; do eval "xdotool $act"; sleep 0.15; done
xdotool type --delay 20 QUIT
wait $GPID
