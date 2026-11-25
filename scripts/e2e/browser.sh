#!/bin/bash
# End-to-end test of a browser guest (germ's xterm.js viewer, in headless
# Chromium through Playwright) in a session shared from a host germ on a
# virtual X server: letting them in, Enter for the helm, typing, handing
# back, following the host, /helm and /take by name in the host's panel.
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   PLAYWRIGHT_CORE=/path/to/node_modules/playwright-core CHROME=/path/to/chrome \
#     scripts/e2e/browser.sh [shots-dir]
set -e
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$HERE/../..
GERM=${GERM:-$ROOT/germ}
export DISPLAY=${DISPLAY_E2E:-:99}
SHOTS=${1:-}
if ! node -e "require(process.env.PLAYWRIGHT_CORE || 'playwright-core')" 2>/dev/null; then
	echo "skip: no playwright-core (set PLAYWRIGHT_CORE)"; exit 0
fi
# Never talk to the real germ window or tmux this shell may be running in.
unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET GERM_CONFIG
TMP=$(mktemp -d)
export XDG_STATE_HOME="$TMP/state" XDG_DATA_HOME="$TMP/data"
SOCK=germ-browser-e2e
PORT=18095
cleanup() {
	exec 3>&- 2>/dev/null || true
	[ -n "$CTL" ] && { $CTL chat /stop >/dev/null 2>&1 || true; }
	kill $HOST $RELAY $NODE 2>/dev/null || true
	tmux -L $SOCK kill-server 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT
mkdir -p "$TMP/bin"
printf '#!/bin/sh\necho "$*" >> %s/notified\n' "$TMP" > "$TMP/bin/notify-send"
chmod +x "$TMP/bin/notify-send"
export PATH="$TMP/bin:$PATH"
tmux -L $SOCK kill-server 2>/dev/null || true
rm -f "/tmp/tmux-$(id -u)/$SOCK"

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

winof() { for _ in $(seq 50); do w=$(xdotool search --pid "$1" 2>/dev/null | head -1); [ -n "$w" ] && { echo "$w"; return; }; sleep 0.1; done; }
"$GERM" -config "$TMP/host.toml" 2>"$TMP/host.err" & HOST=$!
HW=$(winof $HOST); sleep 2
xdotool windowfocus --sync "$HW"
xdotool key ctrl+shift+p; sleep 2
CTL="env GERM_SOCKET=$XDG_RUNTIME_DIR/germ/$HOST.sock $GERM ctl"
GHELM="env GERM_SOCKET=$XDG_RUNTIME_DIR/germ/$HOST.sock $GERM helm"
for _ in $(seq 40); do $CTL chat 2>/dev/null | grep -q '^http' && break; sleep 0.5; done
LINK=$($CTL chat | awk '/^http/{l=$0; w=length($0); while ((getline n) > 0 && length(l)%w==0 && n !~ / /) l=l n; print l; exit}')
[ -n "$LINK" ] || { echo "FAIL no link"; exit 1; }
SL() { $CTL chat | tr '\n' ' ' | tr -s ' '; }
fail=0
check() { # description shell-condition: polled for up to 20 s
	for _ in $(seq 40); do eval "$2" && { echo "ok   $1"; return; }; sleep 0.5; done
	echo "FAIL $1"; fail=1
}

mkfifo "$TMP/b"
node "$HERE/browser.js" < "$TMP/b" > "$TMP/browser.log" 2>&1 & NODE=$!
exec 3>"$TMP/b"
b() { echo "$*" >&3; sleep 1; }

b open "$LINK"; sleep 2; b name carol
check "asked to let carol in" "SL | grep -q 'carol (browser) wants to join'"
$CTL chat /yes
check "carol joined" "SL | grep -q 'carol joined'"
b term; b type "echo nohelm"; b key Enter
check "Enter asked for the helm" "SL | grep -q 'carol asked for the helm of tab 0'"
$CTL chat /yes; sleep 1
b type "echo carol-typed"; b key Enter
check "carol typed at the helm" "tmux -L $SOCK capture-pane -p -t %0 | grep -qx carol-typed"
b click '#helm'
check "the button handed it back" "$GHELM status | grep -q '^0 .*: you'"
NEW=$(tmux -L $SOCK new-window -d -P -F '#{window_index}' -t germ:); tmux -L $SOCK select-window -t "germ:$NEW"; sleep 2
b term; b key Enter
check "carol followed and asked for the new tab" "SL | grep -q 'carol asked for the helm of tab $NEW'"
$CTL chat /yes; sleep 1
b type "echo carol-tab2"; b key Enter
check "carol typed in the new tab" "tmux -L $SOCK capture-pane -p -t 'germ:$NEW' | grep -qx carol-tab2"
tmux -L $SOCK select-window -t germ:0; sleep 1
$CTL chat /take
check "/take found carol's tab from another" "$GHELM status | grep -q '^$NEW .*: you'"
$CTL chat "/helm carol 0"
check "/helm carol, by name" "$GHELM status | grep -q '^0 .*: carol'"
b type "echo carol-at-0"; b key Enter
check "carol types in tab 0" "tmux -L $SOCK capture-pane -p -t %0 | grep -qx carol-at-0"
check "carol is named once" "SL | grep -q 'gave carol the helm' && ! SL | grep -q 'browser) (browser'"
[ -n "$SHOTS" ] && { b shot "$SHOTS/browser.png"; xdotool windowraise "$HW"; sleep 0.3; import -window root "$SHOTS/browser-host.png"; }
b quit
grep '^error' "$TMP/browser.log" && fail=1
exit $fail
