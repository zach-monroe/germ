#!/bin/bash
# End-to-end test of right-click menus and closing tabs with the mouse, on
# a virtual X server: the pane menu, the tab menu (rename), the tab's ×,
# and middle-click. (The share panel's people menu is in share.sh.)
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/e2e/menus.sh [shots-dir]
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$HERE/../..
GERM=${GERM:-$ROOT/germ}
export DISPLAY=${DISPLAY_E2E:-:99}
SHOTS=${1:-}
unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET GERM_CONFIG
TMP=$(mktemp -d)
export XDG_STATE_HOME="$TMP/state" XDG_DATA_HOME="$TMP/data"
SOCK=germ-menus-e2e
cleanup() { kill $HOST 2>/dev/null; tmux -L $SOCK kill-server 2>/dev/null; rm -rf "$TMP"; }
trap cleanup EXIT
tmux -L $SOCK kill-server 2>/dev/null; rm -f "/tmp/tmux-$(id -u)/$SOCK"
printf '[tmux]\nsocket = "%s"\n[home]\nenabled = false\n[agents]\nnotify = false\n' $SOCK > "$TMP/c.toml"
"$GERM" -config "$TMP/c.toml" 2>"$TMP/err" & HOST=$!
for _ in $(seq 50); do HW=$(xdotool search --pid $HOST 2>/dev/null | head -1); [ -n "$HW" ] && break; sleep 0.1; done
sleep 2; xdotool windowfocus --sync "$HW"
CTL="env GERM_SOCKET=$XDG_RUNTIME_DIR/germ/$HOST.sock $GERM ctl"
eval $(xdotool getwindowgeometry --shell "$HW")
SIZE=$($CTL list | awk 'NR==2{print $(NF-1)}')
GCOLS=${SIZE%x*}; GROWS=$((${SIZE#*x} + 1)); CW=$((WIDTH/GCOLS)); CH=$((HEIGHT/GROWS))
at() { xdotool mousemove --window "$HW" $(( $1*CW + CW/2 )) $(( $2*CH + CH/2 )); } # grid col, row
panes() { tmux -L $SOCK list-panes -t germ -F '#{pane_id}' | wc -l; }
windows() { tmux -L $SOCK list-windows -t germ -F '#{window_index}' | tr '\n' ' '; }
fail=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi; }

# The pane menu: Paste, Split right, ... (row 0 the tab bar, 1 the menu's line).
at 50 15; xdotool click 3; sleep 0.5
[ -n "$SHOTS" ] && import -window root "$SHOTS/pane-menu.png"
at 10 3; xdotool click 1; sleep 1
check "pane menu: Split right" '[ "$(panes)" = 2 ] && tmux -L $SOCK display -p -t germ "#{window_layout}" | grep -q "{"'
# Escape closes a menu; so does a click elsewhere.
at 20 15; xdotool click 3; sleep 0.3; xdotool key Escape; sleep 0.3
at 20 15; xdotool click 3; sleep 0.3; at 80 20; xdotool click 1; sleep 0.3
check "menus close without doing anything" '[ "$(panes)" = 2 ]'
# The tab menu: rename.
at 9 0; xdotool click 3; sleep 0.5
[ -n "$SHOTS" ] && import -window root "$SHOTS/tab-menu.png"
at 10 2; xdotool click 1; sleep 0.5; xdotool key ctrl+u; xdotool type "renamed"; xdotool key Return; sleep 1
check "tab menu: Rename tab" '[ "$(tmux -L $SOCK list-windows -t germ -F "#{window_name}")" = renamed ]'
# The current tab's ×, and middle-click.
xdotool windowfocus --sync "$HW"; xdotool key ctrl+shift+t; sleep 1; xdotool key ctrl+shift+t
for _ in $(seq 40); do [ "$(windows)" = "0 1 2 " ] && break; sleep 0.25; done; sleep 1 # and the bar redrawn
# Where the × is: after " germ " and a gap, each tab is " N title " and a
# gap; the current (last) tab's × follows its label.
X=$($CTL list | awk 'NR>1 && !seen[$3]++{print $3, $4}' | awk -v x=7 '{w=length(" " $1 " " $2 " "); last=x; x+=w+1} END{print last+w}')
at "$X" 0; xdotool click 1; sleep 1
check "the × closed the current tab" '[ "$(windows)" = "0 1 " ]'
at 9 0; xdotool click 2; sleep 1 # on tab 0
check "middle-click closed a tab" '[ "$(windows)" = "1 " ]'
exit $fail
