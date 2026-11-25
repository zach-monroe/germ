#!/bin/bash
# End-to-end test of agents reporting their own state: a fake agent in a
# background tab sends Claude Code hook payloads through
# `germ ctl agent-event claude`, and we check what germ says about it
# (and which desktop notifications it would have sent).
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/e2e/agents.sh
set -e
SOCK=germ-e2e-agents
tmux -L $SOCK kill-server 2>/dev/null || true
rm -f "/tmp/tmux-$(id -u)/$SOCK"
unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET
HERE=$(cd "$(dirname "$0")" && pwd)
GERM=${GERM:-$(cd "$HERE/../.." && pwd)/germ}
export DISPLAY=${DISPLAY_E2E:-:99}
TMP=$(mktemp -d)
export XDG_STATE_HOME="$TMP/state" # no session snapshots in the real state dir
GPID=
trap '[ -n "$GPID" ] && kill $GPID 2>/dev/null; tmux -L $SOCK kill-server 2>/dev/null; rm -rf "$TMP"' EXIT
printf '[tmux]\nsocket = "%s"\nsession = "e2e"\n[home]\nenabled = false\n' $SOCK > "$TMP/config.toml"

# Record notifications instead of showing them.
mkdir -p "$TMP/bin"
printf '#!/bin/sh\nshift\necho "$*" >> %s/notified\n' "$TMP" > "$TMP/bin/notify-send"
chmod +x "$TMP/bin/notify-send"

PATH="$TMP/bin:$PATH" "$GERM" -config "$TMP/config.toml" -tmux 2>"$TMP/germ.err" &
GPID=$!
for _ in $(seq 60); do WID=$(xdotool search --pid $GPID 2>/dev/null | head -1); [ -n "$WID" ] && break; sleep 0.1; done
sleep 2
xdotool windowfocus --sync "$WID" 2>/dev/null || true
export GERM_SOCKET=${XDG_RUNTIME_DIR:-/tmp}/germ/$GPID.sock

# The fake agent: one hook payload per step, waiting for the checker.
cat > "$TMP/agent.sh" <<EOF
ev() { echo "\$2" | "$GERM" ctl agent-event claude; touch "$TMP/step\$1"; while [ ! -e "$TMP/ok\$1" ]; do sleep 0.1; done; }
ev 1 '{"hook_event_name":"SessionStart","source":"startup"}'
ev 2 '{"hook_event_name":"UserPromptSubmit","prompt":"fix the tests"}'
ev 3 '{"hook_event_name":"PermissionRequest","tool_name":"Bash"}'
ev 4 '{"hook_event_name":"PostToolUse","tool_name":"Bash"}'
ev 5 '{"hook_event_name":"Stop","last_assistant_message":"All 42 tests pass.\n\nI changed..."}'
ev 6 '{"hook_event_name":"SessionEnd","reason":"prompt_input_exit"}'
sleep 30
EOF
# A second tab, in the background, so the agent isn't on screen.
PANE=$(tmux -L $SOCK new-window -d -P -F '#{pane_id}' -t e2e "bash $TMP/agent.sh")

fail=0
state() { "$GERM" ctl json "{\"cmd\":\"pane_state\",\"pane\":\"$PANE\"}" | python3 -c 'import json,sys; p=json.load(sys.stdin)["pane"]; print(p.get("agent",""), p.get("state",""), "|", p.get("message",""))'; }
step() { # n expected-agent expected-state [message-substring]
	for _ in $(seq 50); do [ -e "$TMP/step$1" ] && break; sleep 0.1; done
	sleep 0.3
	local got; got=$(state)
	if [[ "$got" == "$2 $3 |"*"$4"* ]]; then echo "ok   step $1: $got"
	else echo "FAIL step $1: got '$got', want '$2 $3 | *$4*'"; fail=1; fi
	touch "$TMP/ok$1"
}
step 1 claude idle
step 2 claude working
step 3 claude waiting "wants to use Bash"
step 4 claude working
step 5 claude done "All 42 tests pass."
step 6 "" ""

sleep 0.5
grep -q "wants to use Bash" "$TMP/notified" 2>/dev/null && echo "ok   notified: permission" || { echo "FAIL no permission notification"; fail=1; }
grep -q "finished: All 42 tests pass." "$TMP/notified" 2>/dev/null && echo "ok   notified: finished" || { echo "FAIL no finished notification"; fail=1; }
[ "$(wc -l < "$TMP/notified")" = 2 ] && echo "ok   exactly 2 notifications" || { echo "FAIL notifications:"; cat "$TMP/notified"; fail=1; }

# Outside germ, the command is silent and succeeds.
out=$(env -u GERM_SOCKET sh -c 'echo "{\"hook_event_name\":\"Stop\"}" | "$0" ctl agent-event claude; echo "exit=$?"' "$GERM" 2>&1)
[ "$out" = "exit=0" ] && echo "ok   silent outside germ" || { echo "FAIL outside germ: $out"; fail=1; }
exit $fail
