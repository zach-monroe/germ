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
- splitting, switching, resizing, and closing use germ shortcuts (no prefix
  key);
- sessions keep running after you close germ; the next window reattaches.

Start it with `germ -tmux` (session `main`, created if needed), pick a
session with `germ -tmux -session work`, or set `[tmux] enabled = true` to
make it the default (`germ -no-tmux` then opts out for one window).

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
`resize-pane`), and copy mode isn't needed — use germ's own scrollback and
selection.
