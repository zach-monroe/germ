#!/bin/bash
# End-to-end test of tmux control mode on a virtual X server, using an
# isolated tmux server so real sessions are untouched.
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/e2e/tmux.sh
set -e
. "$(dirname "$0")/lib.sh"
e2e_init tmux
printf '[tmux]\nsocket = "%s"\nsession = "e2e"\n[home]\nenabled = false\n' "$SOCK" > "$TMP/config.toml"

"$GERM" -config "$TMP/config.toml" -tmux -screenshot "$TMP/shot.png" -screenshot-delay 14s 2>/dev/null &
GPID=$!
WID=$(e2e_window $GPID)
sleep 2
xdotool windowfocus --sync "$WID"
k() { xdotool "$@"; sleep 0.8; }
# Wait until the window has N panes and the newest is active (a split can
# take a moment on a slow machine; typing before it lands goes elsewhere).
panes() {
	for _ in $(seq 50); do
		[ "$(tmux -L $SOCK list-panes -t e2e:0 2>/dev/null | wc -l)" = "$1" ] && break
		sleep 0.1
	done
	sleep 0.5 # and for germ to focus it
}
k type --delay 10 'echo left'; k key Return
k key ctrl+shift+Return; panes 2
k type --delay 10 'echo right'; k key Return
k key ctrl+shift+d; panes 3
k type --delay 10 'echo bottom'; k key Return
k key ctrl+shift+h
k type --delay 10 'echo again'; k key Return
wait $GPID || true

check() { # pane-index expected-line
	local id
	id=$(tmux -L $SOCK list-panes -t e2e:0 -F '#{pane_id}' | sed -n "$1p")
	if tmux -L $SOCK capture-pane -p -t "$id" | grep -qx "$2"; then
		pass "pane $1 has '$2'"
	else
		flunk "pane $1 missing '$2'"
	fi
}
check 1 left; check 1 again; check 2 right; check 3 bottom
[ "$(tmux -L $SOCK list-panes -t e2e:0 | wc -l)" = 3 ] && pass "3 panes" || flunk "pane count"
exit $fail
