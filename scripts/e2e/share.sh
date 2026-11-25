#!/bin/bash
# shellcheck disable=SC2030,SC2031
# End-to-end test of session sharing on a virtual X server: a local relay,
# a host germ (tmux mode on an isolated tmux server) that shares with
# Ctrl+Shift+P, and two guest germs: letting them in, the helm, the chat,
# and tabs given to a guest.
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
export XDG_DATA_HOME="$TMP/data"   # no germ:// handler or saved chats in the real data dir
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
approve = "all"
CFG
printf '[home]\nenabled = false\n[agents]\nnotify = false\n' > "$TMP/guest.toml"

winof() { for _ in $(seq 50); do w=$(xdotool search --pid "$1" 2>/dev/null | head -1); [ -n "$w" ] && { echo "$w"; return; }; sleep 0.1; done; }

"$GERM" -config "$TMP/host.toml" 2>"$TMP/host.err" & HOST=$!
HW=$(winof $HOST); sleep 2
xdotool windowfocus --sync "$HW"
xdotool key ctrl+shift+p; sleep 2
CTL="env GERM_SOCKET=$XDG_RUNTIME_DIR/germ/$HOST.sock $GERM ctl"
SHARE=$($CTL list | awk '/ germ /{gsub(/\*/,"",$1); print $1}' | tail -1)
$CTL read "$SHARE" -n 60 > "$TMP/share.txt"
# The link wraps over full-width rows.
LINK=$(awk '/^http/{l=$0; w=length($0); while ((getline n) > 0 && length(l)%w==0 && n !~ / /) l=l n; print l; exit}' "$TMP/share.txt")
[ -n "$LINK" ] || { echo "FAIL no link"; cat "$TMP/share.txt"; exit 1; }
echo "ok   share pane shows the link"
sharelog() { $CTL read "$SHARE" -n 60; }
# The share pane's text with wrapped lines joined, for grep.
SL="$CTL read $SHARE -n 60 | tr '\\n' ' ' | tr -s ' '"
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
toshell() { tmux -L $SOCK select-pane -t %0; sleep 0.3; } # the host's cursor out of the share pane
inshare() { xdotool windowfocus --sync "$HW"; xdotool key ctrl+shift+m; sleep 0.5; } # Ctrl+Shift+M: to the share pane

USER=alice GERM_DEBUG_TMUX=${SHOTS:-$TMP}/alice.tmux.log "$GERM" -config "$TMP/guest.toml" join "$LINK" 2>"$TMP/alice.err" & ALICE=$!
sleep 2
check "the share pane asks to let alice in" sh -c "$SL | grep -q 'alice.* wants to join'"
[ -n "$SHOTS" ] && { xdotool windowraise "$HW"; sleep 0.3; import -window root "$SHOTS/host-join.png"; }
$CTL send "$SHARE" -no-enter y; sleep 2
check "let alice in from the share pane" sh -c "$SL | grep -q 'alice joined'"
AW=$(winof $ALICE); sleep 2
[ -n "$AW" ] || { echo "FAIL guest window"; cat "$TMP/alice.err"; exit 1; }
toshell
xdotool windowfocus --sync "$AW"; sleep 0.2; xdotool type --delay 15 "echo typed-without-helm"; sleep 1
check "a guest can't type without the helm" sh -c "! tmux -L $SOCK capture-pane -p -t %0 | grep -q typed-without-helm"
xdotool key ctrl+shift+t; sleep 1
check "a guest can't open tabs without the helm" [ "$(tmux -L $SOCK list-windows -t germ | wc -l)" = 1 ]

# Enter asks for the helm; the host says yes in the share pane, reached
# with Ctrl+Shift+M.
xdotool key Return; sleep 1
check "Enter asked for the helm" sh -c "$SL | grep -q 'alice asks for the helm'"
inshare
[ -n "$SHOTS" ] && { xdotool windowraise "$HW"; sleep 0.3; import -window root "$SHOTS/host-request.png"; }
xdotool type y; sleep 1
check "the host handed over the helm from the share pane" sh -c "$SL | grep -q 'gave alice the helm'"
# The host's cursor is still in the share pane: alice's typing goes to
# the shell, not there.
typein "$AW" typed-at-the-helm
check "the guest types at the helm (in the shell, not the share pane)" sh -c "tmux -L $SOCK capture-pane -p -t %0 | grep -qx typed-at-the-helm"
toshell
typein "$HW" host-while-alice-drives
check "the host's keys don't go through while a guest has the helm" sh -c "! tmux -L $SOCK capture-pane -p -t %0 | grep -q host-while-alice-drives"
# The host takes it back with one key.
xdotool windowfocus --sync "$HW"; xdotool key ctrl+shift+o; sleep 1
typein "$AW" alice-after-takeback
typein "$HW" host-after-takeback
check "Ctrl+Shift+O took the helm back" sh -c "! tmux -L $SOCK capture-pane -p -t %0 | grep -q alice-after-takeback && tmux -L $SOCK capture-pane -p -t %0 | grep -qx host-after-takeback"
GHELM="env GERM_SOCKET=$XDG_RUNTIME_DIR/germ/$HOST.sock $GERM helm"
# /helm hands it over unasked; the host's Enter takes it back.
$CTL send "$SHARE" "/helm alice 0"; sleep 1
check "/helm alice handed it over" sh -c "$GHELM status | grep -q '^0 .*: alice'"
xdotool windowfocus --sync "$HW"; xdotool key Return; sleep 1
check "the host's Enter took it back" sh -c "$GHELM status | grep -q '^0 .*: you'"
# germ helm, from a shell.
$GHELM give alice 0 >/dev/null; sleep 1
check "germ helm give" sh -c "$GHELM status | grep -q '^0 .*: alice'"
$GHELM take all >/dev/null; sleep 1
check "germ helm take all" sh -c "$GHELM status | grep -q '^0 .*: you'"

# Chat both ways.
xdotool windowfocus --sync "$AW"; xdotool key ctrl+shift+m; sleep 0.5
xdotool type --delay 15 'hello from alice'; xdotool key Return; sleep 1
check "a guest's chat reaches the host" sh -c "$SL | grep -q 'hello from alice'"
xdotool key Escape
inshare
xdotool type --delay 15 'hi alice'; xdotool key Return; sleep 1
check "the host's chat goes out" sh -c "$SL | grep -q 'hi alice'"
[ -n "$SHOTS" ] && { xdotool windowraise "$HW"; sleep 0.3; import -window root "$SHOTS/host-chat.png"; xdotool windowraise "$AW"; sleep 0.3; import -window root "$SHOTS/alice-chat.png"; }

# bob joins; the host lets him in and gives him the tab, in the share pane.
USER=bob "$GERM" -config "$TMP/guest.toml" join "$LINK" 2>"$TMP/bob.err" & BOB=$!
sleep 2
inshare
xdotool type y; sleep 2
check "let bob in" sh -c "$SL | grep -q 'bob joined'"
BW=$(winof $BOB); sleep 2
inshare
xdotool type --delay 15 '/give bob'; xdotool key Return; sleep 1
check "the host gave bob the tab" sh -c "$SL | grep -q 'gave tab 0 (.*) to bob'"
typein "$BW" bobs-tab
check "bob types in his tab" sh -c "tmux -L $SOCK capture-pane -p -t %0 | grep -qx bobs-tab"
[ -n "$SHOTS" ] && { xdotool windowraise "$BW"; sleep 0.3; import -window root "$SHOTS/bob.png"; }

# A tab on its own branch for alice: /branch makes a worktree in the
# host's repo (the shell's directory) and gives her the tab.
git init -q -b main "$TMP/repo" && git -C "$TMP/repo" -c user.name=t -c user.email=t@t commit -q --allow-empty -m init
tmux -L $SOCK send-keys -t %0 "cd $TMP/repo" Enter; sleep 0.5
toshell; inshare
xdotool type --delay 15 '/branch alice fix the readme'; xdotool key Return; sleep 3
check "/branch made alice's branch" sh -c "git -C '$TMP/repo' branch --list 'germ/alice-*' | grep -q fix-the-readme"
check "the branch tab is alice's" sh -c "$SL | grep -q 'gave tab 1 (.*) to alice'"
BRANCHWIN=$(tmux -L $SOCK list-windows -t germ -F '#{window_id}' | tail -1)
typein "$AW" alices-branch
check "alice types on her branch" sh -c "tmux -L $SOCK capture-pane -p -t '$BRANCHWIN' | grep -qx alices-branch"
check "the branch is marked as alice's for germ review" sh -c "git -C '$TMP/repo' config branch.germ/alice-fix-the-readme.germ-for | grep -qx alice"
[ -n "$SHOTS" ] && { xdotool windowraise "$AW"; sleep 0.3; import -window root "$SHOTS/alice-branch.png"; }
tmux -L $SOCK select-window -t germ:0; sleep 1

# Guests follow the host's window, and can look around on their own.
guest_session() { tmux -L $SOCK list-sessions -F '#{session_name}' | grep -- "-guest-" | head -1; }
NEW=$(tmux -L $SOCK new-window -d -P -F '#{window_index}' -t germ:)
tmux -L $SOCK select-window -t germ:$NEW; sleep 1.5
GS=$(guest_session)
check "guest follows the host to a new tab" [ "$(tmux -L $SOCK display -p -t "$GS" '#{window_index}')" = "$NEW" ]
xdotool windowfocus --sync "$AW"; xdotool key ctrl+shift+Tab; sleep 1
check "guest switched tabs without moving the host" sh -c "[ \"\$(tmux -L $SOCK display -p -t '$GS' '#{window_index}')\" != $NEW ] && [ \"\$(tmux -L $SOCK display -p -t germ: '#{window_index}')\" = $NEW ]"
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
$CTL send "$SHARE" "/kick bob" >/dev/null
for _ in $(seq 30); do kill -0 $BOB 2>/dev/null || break; sleep 0.2; done
if kill -0 $BOB 2>/dev/null; then echo "FAIL bob still connected after removal"; fail=1
else echo "ok   removed bob ($(cat "$TMP/bob.err"))"; fi

xdotool windowfocus --sync "$HW"; sleep 0.5
[ -n "$SHOTS" ] && import -window root "$SHOTS/host.png"
# Stopping the share disconnects guests.
$CTL send "$SHARE" "/stop" >/dev/null
for _ in $(seq 30); do kill -0 $ALICE 2>/dev/null || break; sleep 0.2; done
if kill -0 $ALICE 2>/dev/null; then echo "FAIL guest still connected after sharing stopped"; fail=1
else echo "ok   guests disconnected when sharing stopped ($(cat "$TMP/alice.err"))"; fi
exit $fail
