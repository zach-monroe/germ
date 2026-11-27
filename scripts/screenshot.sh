#!/bin/bash
# Makes the README screenshot (docs/img/screenshot.png): a tab with code
# and tests, three agents in the background (one working, one waiting, one
# done), and the session shared with alice, who joins through a local relay
# and chats. It runs on its own virtual X server (Xvfb, 1920x1080) and is
# isolated like the e2e tests, so it never touches your desktop or tmux.
#
#   scripts/screenshot.sh [out.png]
#
# Needs Xvfb, xdotool, ImageMagick (import), nvim, column and tmux.
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=$(realpath -m "${1:-$ROOT/docs/img/screenshot.png}")
for c in Xvfb xdotool import nvim column tmux go; do
	command -v $c >/dev/null || { echo "screenshot.sh: needs $c" >&2; exit 1; }
done
# A free display number for our own Xvfb.
N=90
while [ -e /tmp/.X11-unix/X$N ] || [ -e /tmp/.X$N-lock ]; do N=$((N + 1)); done
export DISPLAY_E2E=:$N
GIVEN=$GERM
. "$ROOT/scripts/e2e/lib.sh"
e2e_init screenshot
Xvfb :$N -screen 0 1920x1080x24 -nolisten tcp >/dev/null 2>&1 &
for _ in $(seq 50); do [ -e /tmp/.X11-unix/X$N ] && break; sleep 0.1; done
[ -e /tmp/.X11-unix/X$N ] || { echo "screenshot.sh: Xvfb didn't start" >&2; exit 1; }
# germ as the source is now (unless GERM says otherwise).
GERM=$GIVEN
if [ -z "$GERM" ]; then
	GERM=$TMP/germ
	(cd "$ROOT" && go build -o "$GERM" ./cmd/germ)
fi
PORT=18097
e2e_cleanup() { [ -n "$CTL" ] && $CTL chat /stop >/dev/null 2>&1; }
export PS1='\[\e[1;34m\]~/germ\[\e[0m\] \[\e[35m\]❯\[\e[0m\] '
export SHELL=/bin/bash
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$TMP/germ-relay" ./cmd/germ-relay)
"$TMP/germ-relay" -addr 127.0.0.1:$PORT 2>"$TMP/relay.log" &
cat > "$TMP/host.toml" <<CFG
theme = "tokyonight"
[font]
family = "JetBrainsMono Nerd Font"
size = 12.0
[window]
columns = 172
rows = 38
padding = 8
[shell]
program = "/bin/bash"
args = ["--norc", "--noprofile"]
[tmux]
socket = "$SOCK"
[home]
enabled = false
[share]
relay = "http://127.0.0.1:$PORT"
approve = "none"
CFG
sed '/^\[tmux\]/,/^socket/d' "$TMP/host.toml" > "$TMP/guest.toml"

cd "$ROOT"
"$GERM" -config "$TMP/host.toml" 2>"$TMP/host.err" & HOST=$!
HW=$(e2e_window $HOST)
[ -n "$HW" ] || { echo "screenshot.sh: germ didn't open a window:" >&2; cat "$TMP/host.err" >&2; exit 1; }
sleep 2
CTL=$(e2e_ctl $HOST)
T="tmux -L $SOCK"
for _ in $(seq 50); do $T has-session -t germ 2>/dev/null && break; sleep 0.2; done
# The tab you're in: the code on the left, tests on the right.
$T rename-window -t germ:0 germ
$T send -t germ:0 "clear; nvim --clean -c 'set nowrap ts=4 nu' +8 -c 'normal zt' internal/term/sync.go" Enter
$T split-window -h -l 45% -t germ:0 -c "$ROOT"
$T send -t germ:0.1 "PS1='\\[\\e[1;34m\\]~/germ\\[\\e[0m\\] \\[\\e[35m\\]❯\\[\\e[0m\\] '; clear; go test ./internal/... 2>&1 | grep -v 'no test files' | sed 's/github.com.zach-monroe.germ.//' | column -t" Enter
# Three agents in the background.
agent() { # tab-name events...
	local f="$TMP/agent-$1.sh"; shift
	{ for e in "$@"; do echo "echo '$e' | $GERM ctl agent-event claude"; done; echo "while :; do echo thinking; sleep 2; done"; } > "$f"
	$T new-window -d -n "$name" -t germ: -c "$ROOT" "bash $f"
}
name=fix-flaky-test agent a1 '{"hook_event_name":"SessionStart"}' '{"hook_event_name":"UserPromptSubmit","prompt":"fix the flaky test"}'
name=docs agent a2 '{"hook_event_name":"SessionStart"}' '{"hook_event_name":"UserPromptSubmit","prompt":"docs"}' '{"hook_event_name":"PermissionRequest","tool_name":"Bash"}'
name=widgets agent a3 '{"hook_event_name":"SessionStart"}' '{"hook_event_name":"UserPromptSubmit","prompt":"x"}' '{"hook_event_name":"Stop","last_assistant_message":"All 42 tests pass."}'
sleep 1
# Share, and alice joins.
xdotool windowfocus --sync "$HW"; xdotool key ctrl+shift+p; sleep 2
for _ in $(seq 40); do $CTL chat 2>/dev/null | grep -q '^http' && break; sleep 0.5; done
LINK=$($CTL chat | awk '/^http/{l=$0; w=length($0); while ((getline n) > 0 && length(l)%w==0 && n !~ / /) l=l n; print l; exit}')
USER=alice "$GERM" -config "$TMP/guest.toml" join "$LINK" 2>"$TMP/alice.err" & ALICE=$!
AW=$(e2e_window $ALICE); sleep 3
ACTL=$(e2e_ctl $ALICE)
$ACTL chat "morning! want me to take the docs agent off your hands?" >/dev/null; sleep 1
$CTL chat "/give alice 2" >/dev/null; sleep 1
$CTL chat "yes please, it's yours. it's waiting on a Bash approval" >/dev/null; sleep 1
$ACTL chat "on it" >/dev/null; sleep 1
for _ in $(seq 120); do $T capture-pane -p -t germ:0.1 | grep -q "^ok.*widgets" && break; sleep 1; done
xdotool windowraise "$HW"; xdotool windowfocus --sync "$HW"; sleep 2
import -window "$HW" "$OUT"
echo "$OUT"
