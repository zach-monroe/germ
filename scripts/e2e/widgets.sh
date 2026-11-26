#!/bin/bash
# End-to-end test of widget clicks: a live script widget gets each
# germ-widget: token on its input, an interval widget is rerun with it in
# GERM_WIDGET_CLICK, and a germ-open: link opens a tab in that directory.
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/e2e/widgets.sh
set -e
unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET
HERE=$(cd "$(dirname "$0")" && pwd)
GERM=${GERM:-$(cd "$HERE/../.." && pwd)/germ}
export DISPLAY=${DISPLAY_E2E:-:99}
TMP=$(mktemp -d)
export XDG_STATE_HOME="$TMP/state" XDG_DATA_HOME="$TMP/data" BROWSER=/bin/false
GPID=
trap '[ -n "$GPID" ] && kill $GPID 2>/dev/null; rm -rf "$TMP"' EXIT
mkdir -p "$TMP/opened here"

# Every widget fills its box with one link, so any click in it lands.
fillbox() { # url text
	local cols rows; cols=$(tput cols) rows=$(tput lines)
	for ((i = 0; i < rows; i++)); do
		printf '\033]8;;%s\033\\%-*s\033]8;;\033\\' "$1" "$cols" "$2"
		((i < rows - 1)) && printf '\r\n'
	done
}
cat > "$TMP/live" <<SH
#!/bin/bash
$(declare -f fillbox)
printf '\033[?25l\033[H'; fillbox germ-widget:live%3Aone "click me (live)"
while read -r tok; do echo "\$tok" >> "$TMP/live.got"; done
SH
cat > "$TMP/interval" <<SH
#!/bin/bash
$(declare -f fillbox)
[ -n "\$GERM_WIDGET_CLICK" ] && echo "\$GERM_WIDGET_CLICK" >> "$TMP/interval.got"
fillbox germ-widget:iv "click me (interval)"
SH
cat > "$TMP/opener" <<SH
#!/bin/bash
$(declare -f fillbox)
printf '\033[?25l\033[H'; fillbox "germ-open:$(python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "$TMP/opened here")" "open a tab"
sleep 600
SH
chmod +x "$TMP/live" "$TMP/interval" "$TMP/opener"
cat > "$TMP/config.toml" <<TOML
[tmux]
enabled = false
[home]
enabled = true
focus_on_start = true
layout = [["live", "interval"], ["opener"], ["todo"]]
[widgets.live]
command = "$TMP/live"
[widgets.interval]
command = "$TMP/interval"
interval = "1h"
[widgets.opener]
command = "$TMP/opener"
[widgets.todo]
file = "$TMP/todo.md"
TOML
# Enough items that a click anywhere in the todo box lands on one.
for i in $(seq 40); do echo "- [ ] item $i"; done > "$TMP/todo.md"

"$GERM" -config "$TMP/config.toml" 2>"$TMP/germ.err" &
GPID=$!
for _ in $(seq 60); do WID=$(xdotool search --pid $GPID 2>/dev/null | head -1); [ -n "$WID" ] && break; sleep 0.1; done
sleep 2
eval "$(xdotool getwindowgeometry --shell "$WID")" # WIDTH HEIGHT
export GERM_SOCKET=${XDG_RUNTIME_DIR:-/tmp}/germ/$GPID.sock

fail=0
clickat() { xdotool mousemove --window "$WID" "$1" "$2" click 1; sleep 0.7; }
check() { # name file expected
	if [ "$(cat "$2" 2>/dev/null)" = "$3" ]; then echo "ok   $1"
	else echo "FAIL $1: got '$(cat "$2" 2>/dev/null)', want '$3'"; fail=1; fi
}

clickat $((WIDTH / 4)) $((HEIGHT / 6))
check "live widget got its token" "$TMP/live.got" "live:one"
clickat $((WIDTH * 3 / 4)) $((HEIGHT / 6))
check "interval widget rerun with GERM_WIDGET_CLICK" "$TMP/interval.got" "iv"

clickat $((WIDTH / 2)) $((HEIGHT / 2))
sleep 1
if "$GERM" ctl json '{"cmd":"list_panes"}' | grep -q "opened here"; then echo "ok   germ-open: opened a tab there"
else echo "FAIL germ-open: no pane in '$TMP/opened here'"; "$GERM" ctl json '{"cmd":"list_panes"}'; fail=1; fi

xdotool key ctrl+shift+grave; sleep 0.7 # germ-open moved us to the new tab: back home
clickat $((WIDTH / 2)) $((HEIGHT - 15)) # the todo box's last row: an item
n=$(grep -c '^- \[x\]' "$TMP/todo.md" || true)
[ "$n" = 1 ] && echo "ok   clicking a todo ticked it in the file" || { echo "FAIL todo: $n ticked"; fail=1; }
clickat $((WIDTH / 2)) $((HEIGHT - 15)) # the todo box's last row: an item
n=$(grep -c '^- \[x\]' "$TMP/todo.md" || true)
[ "$n" = 0 ] && echo "ok   clicking it again unticked it" || { echo "FAIL todo untick: $n ticked"; fail=1; }
exit $fail
