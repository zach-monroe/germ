# Using tmux with germ

germ works well *inside* tmux out of the box, and can also drive tmux
directly in control mode so tmux windows and panes become native germ tabs
and splits (see "Control mode" below).

## Inside tmux

germ sets `TERM=xterm-germ` and installs that terminfo entry automatically
(`~/.local/share/germ/terminfo`). Because the name matches `xterm*`, tmux
enables clipboard, cursor style, cursor color, focus, and title support by
default, and it sees true color from the terminfo entry.

Add this to `~/.tmux.conf` to enable the remaining features germ supports:

```tmux
# germ: synchronized output, styled/colored underlines, strikethrough,
# overline, and extended keys (Ctrl+Shift+letter, Ctrl+digit, ...).
set -as terminal-features ',xterm-germ:RGB:clipboard:ccolour:cstyle:extkeys:focus:overline:rectfill:strikethrough:sync:title:usstyle'

set -g extended-keys on          # pass modified keys through to apps
set -g set-clipboard on          # tmux copy mode -> system clipboard (OSC 52)
set -g focus-events on
set -g default-terminal "tmux-256color"
```

### SSH

Remote hosts usually don't have the `xterm-germ` terminfo entry. Either copy
it over once:

```sh
infocmp -x xterm-germ | ssh myhost -- tic -x -
```

or set `term = "xterm-256color"` under `[shell]` in germ's config.

## Control mode

With control mode, germ talks to tmux using its `-CC` protocol instead of
displaying tmux's own drawing:

- each tmux window is a germ tab and each pane a native split, with germ's
  own scrollback, selection, and clipboard per pane;
- splitting, switching, and closing work with germ shortcuts *and* with
  your tmux prefix bindings (`C-b c`, `C-b %`, `C-b 1`, custom binds...);
- sessions keep running after you close germ; the next window reattaches.

Control mode is the default. Each window gets its own session: `germ`,
then `germ-2`, `germ-3`... for windows opened while the others are in use.
A session nobody is attached to (say, from a window you closed) is reused
before a new one is made. Use `germ -session work` (or `session = "work"`
under `[tmux]`) to pick a session, `germ -no-tmux` for a plain shell, or
`[tmux] enabled = false` to turn it off. Running a command (`germ htop`)
skips tmux. If tmux isn't installed, germ falls back to a plain shell.

### After a reboot

tmux sessions live as long as the tmux server, so a reboot ends them.
germ saves each session's tabs, splits and directories every 30 seconds
(in `~/.local/state/germ/sessions/`). When it next creates that session
from scratch, it rebuilds them: same tabs in the same order, same splits
and sizes, each shell in its old directory. Panes that were running a
coding agent get its resume command typed in, ready for Enter
(`claude --continue`, `codex resume --last`, or `[agents] resume`).
Running programs and shell history aren't restored.

Ending a session yourself (exiting its last shell) forgets it.
`[tmux] restore = false` turns this off.

### tmux key bindings

Keys normally go straight to the pane (`send-keys`), which skips tmux's
key tables. So germ watches for your prefix (`prefix`/`prefix2`, read from
tmux, so a custom prefix works) and sends it plus the next key with
`send-keys -K`, which tmux looks up exactly as if typed into a regular
client. While the prefix is pending, the session label in the tab bar
shows it. Tabs are numbered with tmux's window indices, so `C-b 0`
selects the tab labelled 0.

tmux modes (copy mode, `choose-tree`, ...) can't be displayed in control
mode, so germ cancels them when a pane enters one; use germ's scrollback
(Shift+PgUp, mouse wheel) and selection instead of copy mode. The command
prompt (`C-b :`) isn't shown either; run `tmux` commands in a pane.

| Action | Default key | tmux command germ sends |
|---|---|---|
| New tab (window) | Ctrl+Shift+T | `new-window -c <cwd>` |
| Split right / down | Ctrl+Shift+Enter / Ctrl+Shift+D | `split-window -h` / `-v` |
| Focus pane | Ctrl+Shift+H/J/K/L or click | `select-pane` |
| Next / previous tab | Ctrl+Tab / Ctrl+Shift+Tab | `select-window` |
| Zoom pane | Ctrl+Shift+Z | `resize-pane -Z` |
| Close pane / tab | Ctrl+Shift+W / Ctrl+Shift+Q | `kill-pane` / `kill-window` |
| Detach | close the window (or bind `detach`) | `detach-client` |

Changes made from anywhere (another client, `tmux` commands inside a pane,
your tmux key bindings) show up in germ immediately: germ follows tmux's
`%layout-change`, `%window-add`, and related notifications.

How it works: germ runs `tmux -C new-session -A -s NAME` and reads its line
protocol. Pane output arrives as `%output` lines and is fed straight into
that pane's terminal; keystrokes go back as `send-keys -H` (hex, so every
byte arrives intact). On attach, germ loads each pane's screen and recent
history with `capture-pane`. Applications inside tmux answer to tmux, so
germ never replies to their terminal queries itself.

Limitations: dragging separators doesn't resize panes yet (use tmux's
`resize-pane`); tmux modes and the command prompt aren't shown (see above).
