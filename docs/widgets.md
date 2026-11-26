# Home screen and widgets

germ's first tab (⌂ home) is a dashboard of widgets. It's germ-native: it
isn't a tmux window, so it's personal to you even in a shared session.
Switch to it like any tab (Ctrl+Tab / Ctrl+Shift+Tab or click it). Your
tmux prefix bindings work from there too, so `C-b c` opens a new window.

## Layout

```toml
[home]
enabled = true
focus_on_start = true           # open germ on the home tab
layout = [                      # rows, top to bottom
  ["clock", "weather", "agents"],
  ["news:3", "stocks:2"],       # ":N" = relative width
]
row_heights = [2, 3]            # relative row heights
```

Only the widgets named in `layout` run, so `layout` is where you pick
them: any built-in below, or your own. Saving the config rebuilds the
home tab right away. A dev dashboard, say:

```toml
layout = [
  ["clock", "calendar:2", "system:2"],
  ["agents:2", "worktrees:2", "ports:2"],
  ["prs:3", "todo:2", "coworking:2"],
]
row_heights = [2, 3, 3]
```

[config.example.toml](../config.example.toml) lists every widget's options.

## Built-in widgets

| Widget | Shows | Options (`[widgets.NAME]`) |
|---|---|---|
| `clock` | big digital clock, date, day progress | `format = "12h"`, `seconds = false` |
| `weather` | conditions, art, 5-day forecast ([Open-Meteo](https://open-meteo.com)) | `location = "Denver"` (default: from your IP), `units = "celsius"` |
| `news` | top story with summary + headlines, rotating pages (RSS/Atom) | `feeds = ["https://hnrss.org/frontpage", ...]` |
| `agents` | coding agents in every germ window and what they're doing ([agents.md](agents.md)) | |
| `calendar` | your next events with a countdown, today's all-day events, and a join link for Meet / Zoom / Teams calls; from iCal feeds, no account | `urls = ["https://…/basic.ics"]` (feeds or `.ics` files), `days = 2`, `format = "12h"` |
| `coworking` | sessions you share or joined: who's there (typing, in a browser), who has the helm of which tab, join and helm requests waiting on you, unread chat ([coworking.md](coworking.md)) | |
| `todo` | a markdown checklist (`- [ ] task`); click an item to tick it off (saved to the file, so edit it anywhere) | `file = "~/todo.md"`, `done = "hide"` |
| `git` | changes since a commit: files with +/- bars, commits | `base = "main"`, `dir = "~/code/project"` |
| `ports` | TCP ports your processes listen on, the server's name, and the tab that started it; `*` = reachable from the network. Click the port to open it, the tab to go there | `ignore = ["spotify"]`, `all = true` (other users' ports too) |
| `prs` | your open pull requests with check status (✓ ✗ ●), draft / approved / changes requested, and PRs waiting for your review. Click to open. Uses the GitHub CLI (`gh auth login`) | `filter = "org:acme"` (added to both searches) |
| `worktrees` | agent worktrees (`germ agent`, Ctrl+Shift+A) of the repos open in your tabs: task, +/- lines, commits, uncommitted files, what the agent is doing. Click to go to the agent, or open a tab there | `repos = ["~/code/app"]` (also look in these) |
| `stocks` | price, daily change, intraday sparkline (Yahoo Finance) | `symbols = ["AAPL", "NVDA", "BTC-USD"]` |
| `system` | CPU and network sparklines, memory, disk and battery meters, load, uptime | `disks = ["/", "~"]` (default: `/`, plus your home if it's a separate filesystem) |

None need API keys. Built-ins run inside germ (no process each); try one
on its own with `germ widget clock`.

**Click to open**: news headlines open the article, stock rows their quote
page, and the weather a detailed forecast map, in your browser (`$BROWSER`,
else `xdg-open`). Widgets don't take typing, so no Ctrl is needed.

**Calendar feeds**: every calendar app gives you a private iCal address.
Google Calendar: Settings → your calendar → "Secret address in iCal
format". Outlook: Settings → Calendar → Shared calendars → Publish a
calendar → ICS. Fastmail and iCloud: share the calendar and copy its
link. The address is a password to your calendar, so germ never shows it
(errors name only the host). Recurring events, exceptions and time zones
are handled; feeds refresh every 10 minutes.

## Writing your own widget

A widget is any program. germ runs it in a pseudo-terminal sized to the
widget's box and shows whatever it prints, so you can write widgets in any
language, and even use an existing TUI (`btop`, `cava`, ...) as a widget.

There are two kinds:

**Interval widgets** print once and exit. germ reruns them every
`interval` and swaps in the new output all at once:

```toml
[widgets.todo]
command = "cat ~/todo.txt"
interval = "30s"
```

**Live widgets** (no `interval`) keep running and redraw themselves. If
one exits, germ restarts it with backoff:

```toml
[widgets.load]
command = ["watch", "-t", "-n", "5", "uptime"]
```

Instead of `command`, you can drop an executable at
`~/.config/germ/widgets/NAME` and just put `NAME` in the layout. That
makes widgets easy to share: one file to copy.

### The protocol

- **Size**: the terminal size is your box (`tput cols`/`tput lines`, or
  `TIOCGWINSZ`). Live widgets get `SIGWINCH` on resize; interval widgets
  are rerun immediately.
- **Options**: every key in `[widgets.NAME]` besides `command` and
  `interval` arrives as an environment variable `GERM_WIDGET_<KEY>`
  (lists are comma-separated). `GERM_WIDGET` is the widget's name.
- **Colors**: use the 16 ANSI colors (`\e[31m` ... `\e[97m`) so your widget
  follows the theme. True color works too.
- **Drawing**: live widgets should redraw from the top (`\e[H`), clear
  leftovers (`\e[J`), and wrap frames in synchronized output
  (`\e[?2026h` ... `\e[?2026l`) to avoid flicker. Hide the cursor with
  `\e[?25l`.
- **Links**: print OSC 8 hyperlinks (`\e]8;;URL\e\\text\e]8;;\e\\`) and
  clicking the text opens the URL.
- **Clicks**: a link to `germ-widget:TOKEN` sends `TOKEN` back to your
  widget when clicked. A live widget reads it as a line on stdin (germ
  turns echo off for you). An interval widget is rerun at once with
  `GERM_WIDGET_CLICK=TOKEN`. Percent-encode anything unusual in the token.
- **Open a tab**: a link to `germ-open:/some/dir` opens a new tab there
  (`~/` works; percent-encode spaces).
- These two only work in widgets. In a normal pane germ ignores them, so
  `cat`-ing a file can't plant one.
- Widgets don't receive keyboard input (yet).

A complete live widget in shell:

```sh
#!/bin/sh
# ~/.config/germ/widgets/uptime — then add "uptime" to [home] layout
printf '\033[?25l'
while :; do
  printf '\033[?2026h\033[H\033[1;34m ⏻ uptime\033[0m\n\n  %s\033[J\033[?2026l' "$(uptime -p)"
  sleep 60
done
```

A clickable live widget: a counter you bump by clicking it.

```sh
#!/bin/bash
# ~/.config/germ/widgets/counter
n=0
draw() { printf '\033[?25l\033[H\033[J \033]8;;germ-widget:inc\033\\[ +1 ]\033]8;;\033\\  %d' "$n"; }
draw
while read -r tok; do [ "$tok" = inc ] && n=$((n + 1)); draw; done
```
