#!/bin/bash
# End-to-end test of session sharing on a virtual X server: a local relay,
# a host germ (tmux mode on an isolated tmux server) that shares with
# Ctrl+Shift+P, and guest germs joining with the read-write and watch-only
# links.
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/e2e/share.sh [shots-dir]
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$HERE/../..
GERM=${GERM:-$ROOT/germ}
export DISPLAY=${DISPLAY_E2E:-:99}
SHOTS=${1:-}
# Never talk to the real germ window or tmux this shell may be running in.
unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET GERM_CONFIG
TMP=$(mktemp -d)
SOCK=germ-share-e2e
PORT=18093
cleanup() {
	kill $HOST $RWG $ROG $RELAY 2>/dev/null || true
	tmux -L $SOCK kill-server 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT

(cd "$ROOT" && CGO_ENABLED=0 go build -o "$TMP/germ-relay" ./cmd/germ-relay)
"$TMP/germ-relay" -addr 127.0.0.1:$PORT 2>"$TMP/relay.log" & RELAY=$!

cat > "$TMP/host.toml" <<CFG
[tmux]
socket = "$SOCK"
[home]
enabled = false
[agents]
notify = false
[share]
relay = "http://127.0.0.1:$PORT"
CFG
printf '[home]\nenabled = false\n[agents]\nnotify = false\n' > "$TMP/guest.toml"

winof() { for _ in $(seq 50); do w=$(xdotool search --pid "$1" 2>/dev/null | head -1); [ -n "$w" ] && { echo "$w"; return; }; sleep 0.1; done; }

"$GERM" -config "$TMP/host.toml" 2>"$TMP/host.err" & HOST=$!
HW=$(winof $HOST); sleep 2
xdotool windowfocus --sync "$HW"
xdotool key ctrl+shift+p; sleep 2
CTL="env GERM_SOCKET=$XDG_RUNTIME_DIR/germ/$HOST.sock $GERM ctl"
SHARE=$($CTL list | awk '/ germ /{gsub(/\*/,"",$1); print $1}' | tail -1)
$CTL read "$SHARE" -n 20 > "$TMP/share.txt"
RW=$(grep -o "http://127.0.0.1:$PORT/j/[^ ]*" "$TMP/share.txt" | head -1)
RO=$(grep -o "http://127.0.0.1:$PORT/j/[^ ]*" "$TMP/share.txt" | tail -1)
[ -n "$RW" ] && [ "$RW" != "$RO" ] || { echo "FAIL no links"; cat "$TMP/share.txt"; exit 1; }
echo "ok   share pane printed two links"

"$GERM" -config "$TMP/guest.toml" join "$RW" 2>"$TMP/rw.err" & RWG=$!
GW=$(winof $RWG); sleep 2
[ -n "$GW" ] || { echo "FAIL guest window"; cat "$TMP/rw.err"; exit 1; }
xdotool windowfocus --sync "$GW"
xdotool type --delay 15 'echo typed-by-guest'; xdotool key Return; sleep 1.5
[ -n "$SHOTS" ] && import -window root "$SHOTS/guest.png"
fail=0
if tmux -L $SOCK capture-pane -p -t %0 | grep -qx 'typed-by-guest'; then echo "ok   read-write guest typed into the host's pane"
else echo "FAIL read-write guest's typing didn't arrive"; fail=1; fi

"$GERM" -config "$TMP/guest.toml" join "$RO" 2>"$TMP/ro.err" & ROG=$!
GW2=$(winof $ROG); sleep 2
xdotool windowfocus --sync "$GW2"
xdotool type --delay 15 'echo typed-by-watcher'; xdotool key Return
xdotool key ctrl+shift+t; sleep 1.5
if tmux -L $SOCK capture-pane -p -t %0 | grep -q 'typed-by-watcher'; then echo "FAIL watch-only guest could type"; fail=1
else echo "ok   watch-only guest can't type"; fi
if [ "$(tmux -L $SOCK list-windows -t germ | wc -l)" = 1 ]; then echo "ok   watch-only guest can't open windows"
else echo "FAIL watch-only guest opened a window"; fail=1; fi
$CTL read "$SHARE" -n 40 | grep -q 'joined (watching)' && echo "ok   host sees the watcher join" || { echo "FAIL presence log"; fail=1; }

xdotool windowfocus --sync "$HW"; sleep 0.5
[ -n "$SHOTS" ] && import -window root "$SHOTS/host.png"
# Stopping the share disconnects guests.
$CTL send "$SHARE" --no-enter "$(printf '\003')" >/dev/null 2>&1 || kill -INT $(pgrep -f "germ share" | head -1) 2>/dev/null || true
for _ in $(seq 30); do kill -0 $RWG 2>/dev/null || break; sleep 0.2; done
if kill -0 $RWG 2>/dev/null; then echo "FAIL guest still connected after sharing stopped"; fail=1
else echo "ok   guests disconnected when sharing stopped ($(cat "$TMP/rw.err"))"; fi
exit $fail
