#!/bin/bash
# End-to-end test of tmux control mode on a virtual X server, using an
# isolated tmux server (socket "germ-e2e") so real sessions are untouched.
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/e2e/tmux.sh
set -e
tmux -L germ-e2e kill-server 2>/dev/null || true
rm -f "/tmp/tmux-$(id -u)/germ-e2e"
unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET
HERE=$(cd "$(dirname "$0")" && pwd)
GERM=${GERM:-$HERE/../../germ}
export DISPLAY=${DISPLAY_E2E:-:99}
TMP=$(mktemp -d)
export XDG_STATE_HOME="$TMP/state" # no session snapshots in the real state dir
trap 'tmux -L germ-e2e kill-server 2>/dev/null; rm -rf "$TMP"' EXIT
printf '[tmux]\nsocket = "germ-e2e"\nsession = "e2e"\n[home]\nenabled = false\n' > "$TMP/config.toml"

"$GERM" -config "$TMP/config.toml" -tmux -screenshot "$TMP/shot.png" -screenshot-delay 14s 2>/dev/null &
GPID=$!
for _ in $(seq 60); do WID=$(xdotool search --pid $GPID 2>/dev/null | head -1); [ -n "$WID" ] && break; sleep 0.1; done
sleep 2
xdotool windowfocus --sync "$WID"
k() { xdotool "$@"; sleep 0.8; }
k type --delay 10 'echo left'; k key Return
k key ctrl+shift+Return
k type --delay 10 'echo right'; k key Return
k key ctrl+shift+d
k type --delay 10 'echo bottom'; k key Return
k key ctrl+shift+h
k type --delay 10 'echo again'; k key Return
wait $GPID || true

fail=0
check() { # pane-index expected-line
	local id
	id=$(tmux -L germ-e2e list-panes -t e2e:0 -F '#{pane_id}' | sed -n "$1p")
	if tmux -L germ-e2e capture-pane -p -t "$id" | grep -qx "$2"; then
		echo "ok   pane $1 has '$2'"
	else
		echo "FAIL pane $1 missing '$2'"; fail=1
	fi
}
check 1 left; check 1 again; check 2 right; check 3 bottom
[ "$(tmux -L germ-e2e list-panes -t e2e:0 | wc -l)" = 3 ] && echo "ok   3 panes" || { echo "FAIL pane count"; fail=1; }
exit $fail
