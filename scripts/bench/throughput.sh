#!/bin/bash
# Throughput benchmark: how long `cat` of large files takes inside germ
# (plain and tmux mode) and, if installed, kitty, kitty+tmux and ghostty,
# on a virtual X server.
#
#   Xvfb :99 -screen 0 1280x800x24 &
#   scripts/bench/throughput.sh [terminals...]   (default: all found)
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
GERM=${GERM:-$HERE/../../germ}
export DISPLAY=${DISPLAY_E2E:-:99}
unset TMUX TMUX_PANE GERM_PANE_ID GERM_SOCKET
B=$(mktemp -d)
export XDG_STATE_HOME="$B/state"
trap 'rm -rf "$B"; tmux -L germ-bench kill-server 2>/dev/null; tmux -L kitty-bench kill-server 2>/dev/null' EXIT

python3 - "$B" <<'PY'
import random, sys
d = sys.argv[1]; random.seed(1)
w = "the quick brown fox jumps over lazy dog error warning info debug request response latency".split()
u = "héllo wörld 日本語 テキスト 中文字符 ✓ ★ → λ ∑ ∞ ┌─┐ │ └─┘ 한국어".split()
def gen(name, size, line):
    with open(f"{d}/{name}.txt", "w") as f:
        n = 0
        while n < size:
            l = line(); f.write(l); n += len(l.encode())
gen("ascii", 40_000_000, lambda: "2026-11-24T12:00:00Z " + " ".join(random.choice(w) for _ in range(random.randint(4, 16))) + "\n")
gen("sgr", 20_000_000, lambda: " ".join("\x1b[%d;%dm%s\x1b[0m \x1b[38;2;%d;%d;%dm%s" % (random.choice([1, 2, 3, 4, 22]), random.randint(30, 37), random.choice(w), random.randint(0, 255), random.randint(0, 255), random.randint(0, 255), random.choice(w)) for _ in range(random.randint(4, 12))) + "\x1b[0m\n")
gen("unicode", 20_000_000, lambda: " ".join(random.choice(u) for _ in range(random.randint(4, 14))) + "\n")
PY
printf '[tmux]\nenabled = false\n[home]\nenabled = false\n[window]\ncolumns = 100\nrows = 30\n' > "$B/raw.toml"
printf '[tmux]\nsocket = "germ-bench"\nsession = "b"\n[home]\nenabled = false\n[window]\ncolumns = 100\nrows = 30\n' > "$B/tmux.toml"
KITTY=(kitty --config NONE -o initial_window_width=100c -o initial_window_height=30c -o remember_window_size=no)

run() { # terminal file -> seconds
	local t=$1 f=$2 out=$B/out.$1.$2 w=$B/w.$1.$2
	printf '#!/bin/sh\ns=$(date +%%s.%%N); cat %s/%s.txt; e=$(date +%%s.%%N)\necho "$s $e" | awk %s > %s\nsleep 0.3\n' \
		"$B" "$f" "'{printf \"%.2f\\n\", \$2-\$1}'" "$out" > "$w"
	chmod +x "$w"
	case $t in
	germ) "$GERM" -config "$B/raw.toml" -no-tmux -shell "$w" 2>/dev/null & ;;
	germ-tmux) tmux -L germ-bench kill-server 2>/dev/null; "$GERM" -config "$B/tmux.toml" -tmux -shell "$w" 2>/dev/null & ;;
	kitty) "${KITTY[@]}" "$w" 2>/dev/null & ;;
	kitty-tmux) tmux -L kitty-bench kill-server 2>/dev/null; "${KITTY[@]}" tmux -L kitty-bench -f /dev/null new-session "$w" 2>/dev/null & ;;
	ghostty) ghostty --config-default-files=false --window-width=100 --window-height=30 -e "$w" 2>/dev/null & ;;
	esac
	local p=$!
	for _ in $(seq 1800); do [ -s "$out" ] && break; sleep 0.1; done
	kill $p 2>/dev/null; wait $p 2>/dev/null
	tmux -L germ-bench kill-server 2>/dev/null; tmux -L kitty-bench kill-server 2>/dev/null
	[ -s "$out" ] && cat "$out" || echo timeout
}

terms=("$@")
if [ ${#terms[@]} -eq 0 ]; then
	terms=(germ germ-tmux)
	command -v kitty >/dev/null && terms+=(kitty kitty-tmux)
	command -v ghostty >/dev/null && terms+=(ghostty)
fi
printf '%-12s %8s %8s %8s\n' terminal "40MB log" "20MB sgr" "20MB utf8"
for t in "${terms[@]}"; do
	printf '%-12s %7ss %7ss %7ss\n' "$t" "$(run "$t" ascii)" "$(run "$t" sgr)" "$(run "$t" unicode)"
done
