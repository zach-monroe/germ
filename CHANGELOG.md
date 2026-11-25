# Changelog

## Unreleased

### Themes
- **Live previews in the palette**: Ctrl+Shift+Space, "theme", lists every
  theme, yours included; moving onto one recolors the window, Enter keeps
  it (only the config's `theme =` line changes), Esc goes back.
- **`germ theme new NAME`** starts your own theme from the current one,
  with a comment on every color, and switches to it: edit and save to see
  it live. `germ theme use NAME` picks one from a shell.
- A themes folder created while germ runs is watched too.
- **`[shell] program_colors = false`** keeps your theme when the shell
  sets its own colors (pywal, desktop theme scripts): before, every new
  tab took those colors while reattached tabs kept the theme.
- A config given with `-config`, or linked from a dotfiles repo, reloads
  when saved.
- **Follow your desktop theme**: a theme linked to, or given as the path
  of, a file elsewhere recolors germ when that file is rewritten, so a
  desktop theme tool (caelestia, pywal, matugen) that writes a germ theme
  from a template switches germ along with everything else.
- [docs/themes.md](docs/themes.md).

### Coworking: one share panel
- **Your controls are private**: `germ share` runs in the background and
  the share panel is drawn by your window, so guests never see it or
  your drafts (they could, in a share pane: it was part of the session).
  Sharing keeps going if the window closes, and stops with `/stop` or
  when the session ends. `germ ctl chat [TEXT]` reads or types into the
  panel.
- **The share panel is where you run a session**: the invite link, who's
  here, who has the helm of each tab, the chat with a message box, and
  questions to answer (joins, the helm) with y/n. Everything else is a
  command typed in the box: `/copy`, `/newlink`, `/helm alice`, `/take
  all`, `/give`, `/branch`, `/kick`, `/mute`, `/save`, `/stop` (`/help`).
  Before, requests went to the window's chat panel when one was showing
  and the share pane ignored y, so a host looking at the share pane
  couldn't let anyone in. Ctrl+Shift+M (and Ctrl+Shift+P, once sharing)
  shows it; it opens by itself when someone asks to join.
- **The helm is a key away**: a guest without the helm just starts typing,
  and Enter asks for it; the host's Enter in a tab a guest holds takes it
  back. `/helm alice` hands it over unasked. Guests hear when they get the
  helm and when it goes.
- **`germ helm`** from a shell: `status`, `give NAME [TAB]`, `take
  [TAB|all]`, and for guests `ask` / `back`; plain `germ helm` is
  Ctrl+Shift+O.
- **Browser guests are named like everyone else**: the viewer added
  " (browser)" to their name, so `/helm carol` and `germ helm give carol`
  didn't find them, and the share pane said "carol (browser) (browser)".
  Names match by name, short name or number. Plain `/take` (and
  `germ helm take`) takes the helm back wherever a guest has it when they
  don't have the tab you're on.
- In a plain tmux pane, `germ share` still runs there; guests never type
  into it, even at the helm of its tab.
- germ:// links: hosting or joining from a temporary build (go run, a
  test) no longer takes them over from the installed germ.

### Right-click menus, and closing tabs
- **Right-click a pane**: copy, paste, open or copy a link, split, zoom,
  **restart pane** (kills what runs in it, starts a new shell), close
  pane, new tab; while sharing, hand the helm or take it back.
  (Shift+right-click when the program uses the mouse.)
- **Right-click a tab**: rename, move left/right, restart every pane,
  close; while sharing, give it to a guest or take it back.
- **Right-click a person in the share panel**: hand them the helm, give
  them the tab, a branch of their own, mute, remove. Elsewhere in the
  panel: answer a question, copy or renew the link, save, stop sharing.
- The current tab has a **×**; **middle-click** any tab closes it.

### Tabs
- Tabs are in tmux's order, the numbers they show, and a new tab goes
  last. Before, after closing the first tab a new one took its number
  but showed at the end, so tabs read 1, 0, 2.

## v0.6.0 (2026-11-25)

**Spread your germs.** Highly contagious coworking.

### Coworking: chat and the helm
- **One invite link.** Everyone who joins can watch and chat; typing is
  by turns.
- **Session chat, Ctrl+Shift+M**: a panel beside the terminal in germ, and
  in the browser. Messages are grouped by sender with times, joins and
  helm changes show as notices, `@name` / `@here` notify. Saved only when
  the host asks (`/save`, `s` in the share pane).
- **The helm, Ctrl+Shift+O**: one person types in a tab at a time. Guests
  ask, the host hands it over with `y` in the chat panel, and Ctrl+Shift+O
  takes it back at once. Enforced on the host's machine.
- **Give a guest a tab** (`/give alice`, or the palette): it's theirs to
  type in, and others ask them for it.
- **A branch for a guest** (`/branch alice TASK`, or "New tab for alice on
  its own branch"): a git worktree and a tab of their own, which you then
  review and merge with `germ review`.
- Browser guests get a tab strip (look at any tab, or follow the host).
- `germ share`'s guests can no longer use the host's tmux key bindings or
  command prompt.

### Agents
- **Exact agent status** (`germ setup agents`): Claude Code hooks and Codex's
  notify report working / needs you / finished to germ directly, instead of
  germ guessing from the screen. Permission prompts say what the agent wants
  ("wants to use Bash"), and agents you aren't watching send a "finished"
  notification with the first line of their answer (`[agents] notify_done`).
- **Review agents, Ctrl+Shift+R** (`germ review`): every agent worktree with
  its live state, commits and changed files. Review the diff, send the agent
  feedback, commit, merge or discard, without waiting for agents to exit.
- **Hints, Ctrl+Shift+E**: label every URL, `file:line`, git hash and address
  on screen; a letter opens it (files in `$EDITOR`, `localhost:5173` in the
  browser), Shift+letter copies.

### Daily use
- **Survive a reboot**: tabs, splits and directories of each tmux session
  come back after a reboot, with `claude --continue` typed into agent panes
  (`[tmux] restore`).
- **Command palette, Ctrl+Shift+Space**: every action and its key, jump to any
  running agent, open the config, `germ doctor`, live theme previews.
- **`germ doctor`** checks fonts, tmux, shell integration, agent hooks and
  sharing, and says how to fix what's wrong.
- **Config problems show in the window** (tab bar and a notification), not
  only on stderr. **Crashes are logged** to `~/.local/state/germ/crash.log`
  and reported on the next launch.
- **Ligatures** for programming fonts (`[font] ligatures = true`).
- **Synthetic italic and bold** for fonts without them; DPI changes between
  monitors are followed.
- **`germ theme import`** for base16/base24, iTerm2 and Alacritty schemes.
- Share status in the tab bar: `◉ contagious · alice ✎, bob`.

### Performance (measured, Xvfb)
- Throughput: 40 MB of logs in 2.4 s (kitty 3.0 s, ghostty 7.6 s); in tmux
  mode on par with or ahead of kitty + tmux (tmux is the ceiling).
- Key-to-screen latency: ~5 ms median (kitty ~55 ms, ghostty ~45 ms).
- `scripts/bench/throughput.sh` and `scripts/bench/latency.sh` to re-run.

### Fixes
- `germ -tmux -shell PROGRAM` ignored the program.
- The share tests leaked a tmux server on every run.
- The cursor could draw over the search bar.
