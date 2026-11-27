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
. "$(dirname "$0")/lib.sh"
SHOTS=${1:-}
if ! node -e "require(process.env.PLAYWRIGHT_CORE || 'playwright-core')" 2>/dev/null; then
	echo "skip: no playwright-core (set PLAYWRIGHT_CORE)"; exit 0
fi
e2e_init browser
PORT=18095
e2e_cleanup() {
	exec 3>&- 2>/dev/null
	[ -n "$CTL" ] && $CTL chat /stop >/dev/null 2>&1
}

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

"$GERM" -config "$TMP/host.toml" 2>"$TMP/host.err" & HOST=$!
HW=$(e2e_window $HOST); sleep 2
xdotool windowfocus --sync "$HW"
xdotool key ctrl+shift+p; sleep 2
CTL=$(e2e_ctl $HOST)
GHELM="env GERM_SOCKET=$XDG_RUNTIME_DIR/germ/$HOST.sock $GERM helm"
for _ in $(seq 40); do $CTL chat 2>/dev/null | grep -q '^http' && break; sleep 0.5; done
LINK=$($CTL chat | awk '/^http/{l=$0; w=length($0); while ((getline n) > 0 && length(l)%w==0 && n !~ / /) l=l n; print l; exit}')
[ -n "$LINK" ] || { flunk "no link"; exit 1; }
SL() { $CTL chat | tr '\n' ' ' | tr -s ' '; }
check() { # description shell-condition: polled for up to 20 s
	for _ in $(seq 40); do eval "$2" && { pass "$1"; return; }; sleep 0.5; done
	flunk "$1"
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
