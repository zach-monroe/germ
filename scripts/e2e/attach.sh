#!/bin/bash
# End-to-end test of attaching to a tmux session whose pane has history
# but a part-empty screen (here from a clear: tmux's scroll-on-clear moves
# the screen into the history): typing must land on the prompt's row, not
# rows higher. Guests joining a pane tmux had reflowed hit the same bug.
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/e2e/attach.sh
set -e
. "$(dirname "$0")/lib.sh"
e2e_init attach
export SHELL=/bin/bash
tmux -L $SOCK -f /dev/null new -d -s e2e -x 100 -y 30 "bash --norc --noprofile"
sleep 0.5
tmux -L $SOCK send -t e2e "seq 1 8; printf '\\033[H\\033[2J'; echo one; echo two" Enter
sleep 0.5
[ "$(tmux -L $SOCK display -p -t e2e '#{history_size}')" -gt 0 ] || { flunk "setup: no history"; exit 1; }
printf '[tmux]\nsocket = "%s"\nsession = "e2e"\n[home]\nenabled = false\n' $SOCK > "$TMP/c.toml"
"$GERM" -config "$TMP/c.toml" -tmux 2>"$TMP/err" & G=$!
W=$(e2e_window $G); sleep 2
xdotool windowfocus --sync "$W"
xdotool type --delay 20 "echo MARK"; sleep 1
got=$($(e2e_ctl $G) read %0 -n 40)
# The prompt's own line, and no other (e.g. "2         echo MARK").
if grep -qx '.*\$ echo MARK' <<<"$got" && [ "$(grep -c 'echo MARK' <<<"$got")" = 1 ]; then
	pass "typing after attach lands on the prompt"
else
	flunk "typing after attach:"; echo "$got"
fi
exit $fail
