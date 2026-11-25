# Changelog

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
- Share status in the tab bar: `◉ contagious · alice ✎, bob (watching)`.

### Performance (measured, Xvfb)
- Throughput: 40 MB of logs in 2.4 s (kitty 3.0 s, ghostty 7.6 s); in tmux
  mode on par with or ahead of kitty + tmux (tmux is the ceiling).
- Key-to-screen latency: ~5 ms median (kitty ~55 ms, ghostty ~45 ms).
- `scripts/bench/throughput.sh` and `scripts/bench/latency.sh` to re-run.

### Fixes
- `germ -tmux -shell PROGRAM` ignored the program.
- The share tests leaked a tmux server on every run.
- The cursor could draw over the search bar.
