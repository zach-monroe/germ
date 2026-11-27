# Sourced by the e2e scripts. `e2e_init NAME` keeps a test away from your
# desktop session and real state before anything starts:
#
#   - the germ window or tmux this shell runs in: TMUX, GERM_* unset
#   - your state, data and config: XDG_*_HOME are under $TMP
#   - your desktop: notify-send records to $TMP/notified, the browser
#     (BROWSER and xdg-open) to $TMP/opened
#   - your tmux sessions: $SOCK is a fresh socket of the test's own
#
# At exit it runs the script's e2e_cleanup (if any), then kills background
# jobs and the tmux server and deletes $TMP. It sets HERE, ROOT, GERM,
# DISPLAY (DISPLAY_E2E, default :99), TMP and SOCK, and `fail` for the
# results: pass and flunk print them, and scripts end with `exit $fail`.

HERE=$(cd "$(dirname "${BASH_SOURCE[1]}")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
GERM=${GERM:-$ROOT/germ}
fail=0

e2e_init() { # name
	unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET GERM_CONFIG
	export DISPLAY=${DISPLAY_E2E:-:99}
	TMP=$(mktemp -d)
	export XDG_STATE_HOME="$TMP/state" XDG_DATA_HOME="$TMP/data" XDG_CONFIG_HOME="$TMP/config"
	mkdir -p "$TMP/bin"
	# notify-send's arguments, flags dropped.
	cat > "$TMP/bin/notify-send" <<SH
#!/bin/sh
out=
for a; do case \$a in -*) ;; *) out="\$out\${out:+ }\$a" ;; esac; done
echo "\$out" >> "$TMP/notified"
SH
	printf '#!/bin/sh\necho "$*" >> "%s/opened"\n' "$TMP" > "$TMP/bin/xdg-open"
	chmod +x "$TMP/bin/notify-send" "$TMP/bin/xdg-open"
	export PATH="$TMP/bin:$PATH" BROWSER="$TMP/bin/xdg-open"
	SOCK=germ-$1-e2e
	tmux -L "$SOCK" kill-server 2>/dev/null || true
	rm -f "/tmp/tmux-$(id -u)/$SOCK" # a stale socket makes tmux fail to start
	trap _e2e_exit EXIT
}

_e2e_exit() {
	local status=$? jobs
	set +e # clean up everything, whatever fails
	declare -F e2e_cleanup >/dev/null && e2e_cleanup
	jobs=$(jobs -p)
	[ -n "$jobs" ] && kill $jobs 2>/dev/null
	tmux -L "$SOCK" kill-server 2>/dev/null
	rm -f "/tmp/tmux-$(id -u)/$SOCK"
	rm -rf "$TMP"
	exit $status
}

# e2e_window PID: waits for the process's window and prints its id.
e2e_window() {
	local w
	for _ in $(seq 60); do
		w=$(xdotool search --pid "$1" 2>/dev/null | head -1)
		[ -n "$w" ] && { echo "$w"; return; }
		sleep 0.1
	done
}

# e2e_ctl PID: the germ ctl command for that germ window.
e2e_ctl() { echo "env GERM_SOCKET=${XDG_RUNTIME_DIR:-/tmp}/germ/$1.sock $GERM ctl"; }

pass() { echo "ok   $*"; }
flunk() { echo "FAIL $*"; fail=1; }
