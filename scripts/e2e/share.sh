#!/bin/bash
# shellcheck disable=SC2030,SC2031
# End-to-end test of session sharing on a virtual X server: a local relay,
# a host germ (tmux mode on an isolated tmux server) that shares with
# Ctrl+Shift+P, and two guest germs: the helm, the chat, and tabs given
# to a guest.
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
export XDG_STATE_HOME="$TMP/state" # no session snapshots in the real state dir
SOCK=germ-share-e2e
PORT=18093
cleanup() {
	kill $HOST $ALICE $BOB $RELAY 2>/dev/null || true
	tmux -L $SOCK kill-server 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT
# Record desktop notifications instead of showing them.
mkdir -p "$TMP/bin"
printf '#!/bin/sh\necho "$*" >> %s/notified\n' "$TMP" > "$TMP/bin/notify-send"
chmod +x "$TMP/bin/notify-send"
export PATH="$TMP/bin:$PATH"
tmux -L $SOCK kill-server 2>/dev/null || true
rm -f "/tmp/tmux-$(id -u)/$SOCK" # a stale socket makes tmux fail to start

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
approve = "none"
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
LINK=$(grep -o "http://127.0.0.1:$PORT/j/[^ ]*" "$TMP/share.txt" | head -1)
[ -n "$LINK" ] && [ "$(grep -c "http://127.0.0.1:$PORT/j/" "$TMP/share.txt")" = 1 ] || { echo "FAIL no link"; cat "$TMP/share.txt"; exit 1; }
echo "ok   share pane printed one link"
sharelog() { $CTL read "$SHARE" -n 60; }
pane0() { tmux -L $SOCK capture-pane -p -t %0; }
fail=0
check() { # description command...
	local what=$1; shift
	if "$@"; then echo "ok   $what"; else echo "FAIL $what"; fail=1; fi
}
typein() { # window text
	xdotool windowfocus --sync "$1"; sleep 0.2
	xdotool type --delay 15 "echo $2"; xdotool key Return; sleep 1.2
}

USER=alice GERM_DEBUG_TMUX=${SHOTS:-$TMP}/alice.tmux.log "$GERM" -config "$TMP/guest.toml" join "$LINK" 2>"$TMP/alice.err" & ALICE=$!
AW=$(winof $ALICE); sleep 2
[ -n "$AW" ] || { echo "FAIL guest window"; cat "$TMP/alice.err"; exit 1; }
typein "$AW" typed-without-helm
check "a guest can't type without the helm" sh -c "! tmux -L $SOCK capture-pane -p -t %0 | grep -q typed-without-helm"
xdotool key ctrl+shift+t; sleep 1
check "a guest can't open tabs without the helm" [ "$(tmux -L $SOCK list-windows -t germ | wc -l)" = 1 ]

# alice asks for the helm; the host says yes in the chat panel.
xdotool windowfocus --sync "$AW"; xdotool key ctrl+shift+o; sleep 1
check "the share pane logs the request" sh -c "$CTL read $SHARE -n 60 | grep -q 'alice asked for the helm'"
xdotool windowfocus --sync "$HW"; xdotool key ctrl+shift+m; sleep 0.5
[ -n "$SHOTS" ] && { xdotool windowraise "$HW"; sleep 0.3; import -window root "$SHOTS/host-request.png"; }
xdotool type y; sleep 1
check "the host handed over the helm from the chat panel" sh -c "$CTL read $SHARE -n 60 | grep -q 'gave alice the helm'"
xdotool key Escape
typein "$AW" typed-at-the-helm
check "the guest types at the helm" sh -c "tmux -L $SOCK capture-pane -p -t %0 | grep -qx typed-at-the-helm"
typein "$HW" host-while-alice-drives
check "the host's keys don't go through while a guest has the helm" sh -c "! tmux -L $SOCK capture-pane -p -t %0 | grep -q host-while-alice-drives"
# The host takes it back with one key.
xdotool windowfocus --sync "$HW"; xdotool key ctrl+shift+o; sleep 1
typein "$AW" alice-after-takeback
typein "$HW" host-after-takeback
check "Ctrl+Shift+O took the helm back" sh -c "! tmux -L $SOCK capture-pane -p -t %0 | grep -q alice-after-takeback && tmux -L $SOCK capture-pane -p -t %0 | grep -qx host-after-takeback"

# Chat both ways.
xdotool windowfocus --sync "$AW"; xdotool key ctrl+shift+m; sleep 0.5
xdotool type --delay 15 'hello from alice'; xdotool key Return; sleep 1
check "a guest's chat reaches the host" sh -c "$CTL read $SHARE -n 60 | grep -q 'alice: hello from alice'"
xdotool key Escape
xdotool windowfocus --sync "$HW"; xdotool key ctrl+shift+m; sleep 0.5
xdotool type --delay 15 'hi alice'; xdotool key Return; sleep 1
check "the host's chat goes out" sh -c "$CTL read $SHARE -n 60 | grep -q ': hi alice'"
xdotool key Escape
[ -n "$SHOTS" ] && { xdotool windowraise "$HW"; sleep 0.3; import -window root "$SHOTS/host-chat.png"; xdotool windowraise "$AW"; sleep 0.3; import -window root "$SHOTS/alice-chat.png"; }

# bob joins; the host gives him the tab with /give.
USER=bob "$GERM" -config "$TMP/guest.toml" join "$LINK" 2>"$TMP/bob.err" & BOB=$!
BW=$(winof $BOB); sleep 2
xdotool windowfocus --sync "$HW"; xdotool key ctrl+shift+m; sleep 0.3
xdotool type --delay 15 '/give bob'; xdotool key Return; sleep 1; xdotool key Escape
check "the host gave bob the tab" sh -c "$CTL read $SHARE -n 60 | grep -q 'gave tab 0 (.*) to bob'"
typein "$BW" bobs-tab
check "bob types in his tab" sh -c "tmux -L $SOCK capture-pane -p -t %0 | grep -qx bobs-tab"
[ -n "$SHOTS" ] && { xdotool windowraise "$BW"; sleep 0.3; import -window root "$SHOTS/bob.png"; }

# Guests follow the host's window, and can look around on their own.
guest_session() { tmux -L $SOCK list-sessions -F '#{session_name}' | grep -- "-guest-" | head -1; }
tmux -L $SOCK new-window -d -t germ
tmux -L $SOCK select-window -t germ:1; sleep 1.5
GS=$(guest_session)
check "guest follows the host to a new tab" [ "$(tmux -L $SOCK display -p -t "$GS" '#{window_index}')" = 1 ]
xdotool windowfocus --sync "$AW"; xdotool key ctrl+shift+Tab; sleep 1
check "guest switched tabs without moving the host" sh -c "[ \"\$(tmux -L $SOCK display -p -t '$GS' '#{window_index}')\" = 0 ] && [ \"\$(tmux -L $SOCK display -p -t germ '#{window_index}')\" = 1 ]"
tmux -L $SOCK select-window -t germ:0

# The relay restarts: everyone reconnects by themselves, and bob still
# has his tab.
kill $RELAY; wait $RELAY 2>/dev/null || true; sleep 1
"$TMP/germ-relay" -addr 127.0.0.1:$PORT 2>>"$TMP/relay.log" & RELAY=$!
for _ in $(seq 40); do grep -q "host connected" <(tail -3 "$TMP/relay.log") && break; sleep 0.5; done
sleep 7
typein "$BW" after-reconnect # bob follows the host, who is back on tab 0
if pane0 | grep -qx 'after-reconnect'; then echo "ok   reconnected after the relay restarted, tab still bob's"
else echo "FAIL reconnect"; tail -5 "$TMP/relay.log"; sharelog | tail -4; tail -5 "$TMP/bob.err"; tmux -L $SOCK list-panes -a -F '#{session_name} #{window_index} #{pane_id} active=#{pane_active} win_active=#{window_active}'; [ -n "$SHOTS" ] && { xdotool windowraise "$BW"; sleep 0.3; import -window root "$SHOTS/bob-reconnect.png"; }; fail=1; fi

# Remove bob from the share pane, without a new link.
sleep 1
N=$(sharelog | grep -E '^ +1 ' | tail -1 | grep -oE '[0-9] bob' | grep -oE '^[0-9]')
$CTL send "$SHARE" -no-enter "${N:-2}" >/dev/null; sleep 0.3
$CTL send "$SHARE" -no-enter y >/dev/null; sleep 0.3
$CTL send "$SHARE" -no-enter n >/dev/null
for _ in $(seq 30); do kill -0 $BOB 2>/dev/null || break; sleep 0.2; done
if kill -0 $BOB 2>/dev/null; then echo "FAIL bob still connected after removal"; fail=1
else echo "ok   removed bob ($(cat "$TMP/bob.err"))"; fi

xdotool windowfocus --sync "$HW"; sleep 0.5
[ -n "$SHOTS" ] && import -window root "$SHOTS/host.png"
# Stopping the share disconnects guests.
$CTL send "$SHARE" -no-enter q >/dev/null
for _ in $(seq 30); do kill -0 $ALICE 2>/dev/null || break; sleep 0.2; done
if kill -0 $ALICE 2>/dev/null; then echo "FAIL guest still connected after sharing stopped"; fail=1
else echo "ok   guests disconnected when sharing stopped ($(cat "$TMP/alice.err"))"; fi
exit $fail
