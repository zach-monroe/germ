#!/bin/bash
# Key-to-screen latency: germ (plain and tmux mode) and, if installed,
# kitty and ghostty, each running `cat`, on a virtual X server.
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/bench/latency.sh [terminals...]
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
GERM=${GERM:-$HERE/../../germ}
export DISPLAY=${DISPLAY_E2E:-:99}
unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET
B=$(mktemp -d)
export XDG_STATE_HOME="$B/state"
trap 'rm -rf "$B"; tmux -L germ-lat kill-server 2>/dev/null' EXIT
gcc -O2 -o "$B/keylat" "$HERE/keylat.c" -lX11 -lXtst || exit 1
printf '[tmux]\nenabled = false\n[home]\nenabled = false\n[window]\ncolumns = 100\nrows = 30\n' > "$B/raw.toml"
printf '[tmux]\nsocket = "germ-lat"\nsession = "l"\n[home]\nenabled = false\n[window]\ncolumns = 100\nrows = 30\n' > "$B/tmux.toml"

measure() {
	local t=$1
	case $t in
	germ) "$GERM" -config "$B/raw.toml" -no-tmux -shell /bin/cat 2>/dev/null & ;;
	germ-tmux) tmux -L germ-lat kill-server 2>/dev/null; "$GERM" -config "$B/tmux.toml" -tmux -shell /bin/cat 2>/dev/null & ;;
	kitty) kitty --config NONE -o initial_window_width=100c -o initial_window_height=30c /bin/cat 2>/dev/null & ;;
	ghostty) ghostty --config-default-files=false --window-width=100 --window-height=30 -e /bin/cat 2>/dev/null & ;;
	esac
	local p=$! wid=""
	sleep 3 # let the window map (and settle)
	wid=$(xdotool search --pid $p 2>/dev/null | tail -1)
	if [ -n "$wid" ]; then
		printf '%-10s %s\n' "$t" "$("$B/keylat" "$wid" 40 2>&1 | tail -1)"
	else
		printf '%-10s no window\n' "$t"
	fi
	kill $p 2>/dev/null; wait $p 2>/dev/null
	tmux -L germ-lat kill-server 2>/dev/null
}

terms=("$@")
if [ ${#terms[@]} -eq 0 ]; then
	terms=(germ germ-tmux)
	command -v kitty >/dev/null && terms+=(kitty)
	command -v ghostty >/dev/null && terms+=(ghostty)
fi
for t in "${terms[@]}"; do measure "$t"; done
