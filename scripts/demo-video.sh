#!/bin/bash
# Records the README demo (docs/img/demo.mp4, and demo.gif beside it,
# which GitHub plays inline): you share your session
# (right), alice joins (left), you chat, she asks for the helm, runs tests
# in your tab, and you take it back. Captions are burned in. It runs on its
# own virtual X server (Xvfb, 1920x1080) and is isolated like the e2e
# tests. alice's germ runs in its own UTS namespace (unshare) so her name
# is alice@laptop, not your hostname.
#
#   scripts/demo-video.sh [out.mp4]
#
# Needs Xvfb, xdotool, ffmpeg (with drawtext), gcc, unshare and tmux.
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUTV=$(realpath -m "${1:-$ROOT/docs/img/demo.mp4}")
for c in Xvfb xdotool ffmpeg gcc unshare tmux go; do
	command -v $c >/dev/null || { echo "demo-video.sh: needs $c" >&2; exit 1; }
done
N=90
while [ -e /tmp/.X11-unix/X$N ] || [ -e /tmp/.X$N-lock ]; do N=$((N + 1)); done
export DISPLAY_E2E=:$N
GIVEN=$GERM
. "$ROOT/scripts/e2e/lib.sh"
e2e_init demo-video
PORT=18098
e2e_cleanup() { [ -n "$REC" ] && kill -INT $REC 2>/dev/null && wait $REC; [ -n "$CTL" ] && $CTL chat /stop >/dev/null 2>&1; }
Xvfb :$N -screen 0 1920x1080x24 -nolisten tcp >/dev/null 2>&1 &
for _ in $(seq 50); do [ -e /tmp/.X11-unix/X$N ] && break; sleep 0.1; done
[ -e /tmp/.X11-unix/X$N ] || { echo "demo-video.sh: Xvfb didn't start" >&2; exit 1; }
GERM=$GIVEN
if [ -z "$GERM" ]; then
	GERM=$TMP/germ
	(cd "$ROOT" && go build -o "$GERM" ./cmd/germ)
fi
# Runs a program under another hostname (inside unshare --uts).
cat > "$TMP/sethost.c" <<'C'
#include <stdio.h>
#include <string.h>
#include <unistd.h>
int main(int argc, char **argv) {
	if (argc < 3 || sethostname(argv[1], strlen(argv[1])) != 0) { perror("sethostname"); return 1; }
	execvp(argv[2], argv + 2);
	perror("exec");
	return 1;
}
C
gcc -O2 -o "$TMP/sethost" "$TMP/sethost.c"
export SHELL=/bin/bash
xsetroot -solid '#101018' 2>/dev/null || true
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$TMP/germ-relay" ./cmd/germ-relay)
"$TMP/germ-relay" -addr 127.0.0.1:$PORT 2>"$TMP/relay.log" &
COMMON='theme = "tokyonight"
[font]
family = "JetBrainsMono Nerd Font"
size = 11.0
[window]
columns = 102
rows = 42
padding = 8
[shell]
program = "/bin/bash"
args = ["--norc", "--noprofile"]
[home]
enabled = false'
printf '%s\n[tmux]\nsocket = "%s"\n[share]\nrelay = "http://127.0.0.1:%s"\napprove = "all"\n' "$COMMON" "$SOCK" "$PORT" > "$TMP/host.toml"
printf '%s\n' "$COMMON" > "$TMP/guest.toml"

cd "$ROOT"
"$GERM" -config "$TMP/host.toml" 2>"$TMP/host.err" & HOST=$!
HW=$(e2e_window $HOST); sleep 1.5
eval "$(xdotool getwindowgeometry --shell "$HW")"; echo "window ${WIDTH}x${HEIGHT}"
xdotool windowmove "$HW" $((1920 - WIDTH - 16)) 16
CTL=$(e2e_ctl $HOST)
T="tmux -L $SOCK"
for _ in $(seq 50); do $T has-session -t germ 2>/dev/null && break; sleep 0.2; done
PROMPT="PS1='\\[\\e[1;34m\\]~/germ\\[\\e[0m\\] \\[\\e[35m\\]❯\\[\\e[0m\\] '"
$T rename-window -t germ:0 germ
$T send -t germ:0 "$PROMPT; clear; git --no-pager log --oneline --no-decorate -6" Enter
agent() { # name events...
	local n=$1 f="$TMP/agent-$1.sh"; shift
	{ for e in "$@"; do echo "echo '$e' | $GERM ctl agent-event claude"; done; echo "while :; do echo thinking; sleep 2; done"; } > "$f"
	$T new-window -d -n "$n" -t germ: -c "$ROOT" "bash $f"
}
agent fix-flaky-test '{"hook_event_name":"SessionStart"}' '{"hook_event_name":"UserPromptSubmit","prompt":"x"}'
agent docs '{"hook_event_name":"SessionStart"}' '{"hook_event_name":"UserPromptSubmit","prompt":"x"}' '{"hook_event_name":"PermissionRequest","tool_name":"Bash"}'
agent widgets '{"hook_event_name":"SessionStart"}' '{"hook_event_name":"UserPromptSubmit","prompt":"x"}' '{"hook_event_name":"Stop","last_assistant_message":"All 42 tests pass."}'
sleep 2

# Captions: "start end text" lines, burned in afterwards.
T0=
cap() { awk -v a="$(date +%s.%N)" -v b="$T0" -v t="$*" 'BEGIN{printf "%.2f %s\n", a-b, t}' >> "$TMP/captions"; }
host() { xdotool windowfocus --sync "$HW"; sleep 0.3; }
inpanel() { host; xdotool key ctrl+shift+m; sleep 0.6; }
outpanel() { xdotool key Escape; sleep 0.4; }
say() { xdotool type --delay 45 "$1"; sleep 0.3; xdotool key Return; sleep 0.8; }

ffmpeg -loglevel error -y -f x11grab -draw_mouse 0 -framerate 30 -video_size 1920x1080 -i "$DISPLAY" \
	-c:v libx264 -preset veryfast -crf 18 -pix_fmt yuv420p "$TMP/raw.mp4" < /dev/null & REC=$!
T0=$(date +%s.%N); sleep 1.5

cap "Ctrl+Shift+P shares your terminal as a link"
host; xdotool key ctrl+shift+p
for _ in $(seq 40); do $CTL chat 2>/dev/null | grep -q '^http' && break; sleep 0.5; done
LINK=$($CTL chat | awk '/^http/{l=$0; w=length($0); while ((getline n) > 0 && length(l)%w==0 && n !~ / /) l=l n; print l; exit}')
sleep 2.5
cap "alice opens it, and you let her in"
USER=alice unshare -r --uts "$TMP/sethost" laptop "$GERM" -config "$TMP/guest.toml" join "$LINK" 2>"$TMP/alice.err" & ALICE=$!
for _ in $(seq 40); do $CTL chat | grep -q 'wants to join' && break; sleep 0.25; done
sleep 1.5
inpanel; xdotool key y; sleep 0.3; outpanel
AW=$(e2e_window $ALICE); xdotool windowmove "$AW" 16 16; sleep 2.5
cap "Everyone in the session can chat"
xdotool windowfocus --sync "$AW"; xdotool key ctrl+shift+m; sleep 0.6
say "hey! can I run the terminal tests?"
xdotool key Escape; sleep 0.5
inpanel; say "sure, hit Enter to ask for the helm"; outpanel
sleep 0.5
cap "One person types at a time: she asks for the helm"
xdotool windowfocus --sync "$AW"; sleep 0.4; xdotool key Return; sleep 1.5
inpanel; sleep 0.6; xdotool key y; sleep 0.3; outpanel
sleep 1
cap "and types in your tab"
xdotool windowfocus --sync "$AW"; sleep 0.3
say "go test ./internal/term/ ./internal/vt/"
for _ in $(seq 60); do $T capture-pane -p -t germ:0 | grep -q "^ok.*internal/vt" && break; sleep 0.5; done
sleep 1.5
cap "Ctrl+Shift+O takes the helm back"
host; xdotool key ctrl+shift+o; sleep 1
say "echo thanks alice"
sleep 3
cap END
kill -INT $REC; wait $REC || true; REC=

# Burn in the captions.
FONT=/usr/share/fonts/TTF/JetBrainsMonoNerdFont-Regular.ttf
mapfile -t L < "$TMP/captions"
f=
for ((i = 0; i < ${#L[@]} - 1; i++)); do
	s=${L[i]%% *}; text=${L[i]#* }; e=${L[i+1]%% *}
	printf '%s' "$text" > "$TMP/cap$i.txt"
	f+="${f:+,}drawtext=fontfile=$FONT:textfile=$TMP/cap$i.txt:fontsize=40:fontcolor=0xc0caf5:x=(w-tw)/2:y=h-90:enable='between(t,$s,$e)'"
done
ffmpeg -loglevel error -y -i "$TMP/raw.mp4" -vf "$f" -c:v libx264 -preset slow -crf 22 -pix_fmt yuv420p -movflags +faststart "$OUTV"
# A GIF too: GitHub plays those in a README, not videos in the repo.
ffmpeg -loglevel error -y -i "$OUTV" -vf "fps=10,scale=1600:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=96:stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" "${OUTV%.mp4}.gif"
echo "$OUTV ${OUTV%.mp4}.gif"
