# germ: a GPU-accelerated terminal emulator in Go

## Goals

1. **Blazingly fast**: low input latency, high throughput (`cat` of a huge file), fast startup, low idle CPU.
2. **tmux integration**: first, work well *inside* tmux. Later, a native **tmux control mode** (`tmux -CC`), where tmux windows and panes show up as germ tabs and splits.
3. **Customizable color scheme**: a TOML config, theme files, and hot reload.
4. **fish integration**: germ knows where prompts start, which commands ran, their exit codes, and the current directory. It sets this up automatically, with no edits to the user's `config.fish`.

## Non-goals (for now)

- Windows and macOS support (Linux only, X11/XWayland first)
- Image protocols (sixel, kitty graphics)
- A built-in multiplexer (tmux does that job)

---

## Architecture

```
 keyboard/mouse ──► input encoder ──► PTY master ──► shell / tmux
                                          │
                          reader goroutine (64 KiB reads)
                                          ▼
                              VT parser (state machine)
                                          ▼
                     terminal state: grid + scrollback + modes
                                          │  dirty-row tracking
                                          ▼
               renderer (main OS thread, OpenGL, glyph atlas)
```

### Threading model
- **Main thread** (`runtime.LockOSThread`): window events and OpenGL calls. GLFW and GL require this.
- **PTY reader goroutine**: blocking reads into a reusable buffer, passed to the parser in batches.
- **Terminal state**: guarded by one mutex. The parser holds it while it applies a batch; the renderer holds it only long enough to snapshot dirty rows.
- **Frame pacing**: render only when something is dirty, capped at vsync. This gives zero CPU use at idle.

### Performance strategy
| Area | Technique |
|---|---|
| Memory/GC | Store cells as a flat `[]Cell` of pointer-free structs, so the GC never scans the grid. Scrollback is a ring buffer of rows. Reuse buffers on the hot paths. |
| Parsing | A table-driven VT state machine (Paul Williams' DEC parser), with a fast path for runs of printable ASCII. |
| Rendering | A glyph atlas texture and **instanced quads**: one draw call for backgrounds and one for glyphs. Only dirty rows are re-uploaded. |
| Throughput | Coalesce PTY output, so the screen redraws once per frame however many bytes arrived. |
| Sync output | Support DEC mode 2026 (synchronized updates), so tmux and editors never show half-drawn frames. |
| Startup | No heavy init and a lazy atlas: only rasterize glyphs when they're first used. |

How we'll measure it: `vtebench` for throughput, `typometer` for latency, `go test -bench` on the parser and grid, `pprof` for profiles, and `hyperfine` for startup time.

---

## Tech choices

| Concern | Choice | Why |
|---|---|---|
| Window/input | `github.com/go-gl/glfw/v3.3` (X11/XWayland; 3.4 for native Wayland later) | Mature and gives low-level control |
| GPU | `github.com/go-gl/gl` (OpenGL 3.3 core) | Widely supported and enough for instanced quads |
| PTY | `github.com/creack/pty` | The de-facto standard in Go |
| Font loading/rasterization | `golang.org/x/image/font/opentype` | Pure Go; one glyph per cell (no ligatures in v1) |
| Shaping (later, optional) | `github.com/go-text/typesetting` | Port of HarfBuzz, for ligatures |
| Config | `github.com/BurntSushi/toml` | Simple and well known |
| Hot reload | `github.com/fsnotify/fsnotify` | Watches the config and theme files |
| VT parser | **Written ourselves** | This is the core of the terminal and where the speed comes from |

The alternatives were Gio and Ebitengine. Both are easier, but they give less control over the render loop and over how text is batched. We can revisit this if raw GL becomes painful.

---

## Package layout

```
germ/
├── cmd/germ/main.go          # entry point, flags
├── internal/
│   ├── app/                  # main loop, wires everything together
│   ├── pty/                  # spawns the shell, resizes (TIOCSWINSZ)
│   ├── vt/                   # parser state machine → actions (Print, Execute, CSI, OSC, DCS)
│   ├── term/                 # grid, cursor, scrollback, modes, SGR, alt screen
│   ├── render/               # GL setup, shaders, glyph atlas, instanced draw
│   ├── font/                 # font discovery (fontconfig), shaping, rasterization
│   ├── input/                # key/mouse → byte encoding (legacy + CSI u / kitty keyboard)
│   ├── config/               # TOML schema, defaults, themes, hot reload
│   └── tmux/                 # control-mode client (Phase 7)
├── shell-integration/fish/vendor_conf.d/germ.fish
├── themes/                   # bundled themes (*.toml)
├── terminfo/germ.ti          # terminfo source
└── PLAN.md
```

---

## Color scheme design

Example `~/.config/germ/config.toml`:
```toml
theme = "tokyonight"          # loads themes/tokyonight.toml, or a path

[font]
family = "JetBrains Mono"
size   = 12.0

[colors]                      # optional overrides on top of the theme
background = "#1a1b26"
cursor     = "#c0caf5"
```

A theme file sets `foreground`, `background`, `cursor`, `cursor_text`, `selection_fg`, `selection_bg`, and `normal[8]` + `bright[8]`. The other colors in the 256-color palette are derived by the standard formulas.

Features:
- **Hot reload**: saving a config or theme file recolors open windows instantly. Colors are uniforms or a palette texture, so no re-parse is needed.
- **OSC 4/10/11/12 set and query**, so programs (and tmux) can read and change colors at runtime.
- **Importers** (later): base16 YAML, iTerm2 `.itermcolors`, Alacritty TOML.

---

## fish integration design

### How it gets installed (zero config)
- When germ spawns fish, it prepends its own data dir to `XDG_DATA_DIRS`. fish then automatically sources `germ/fish/vendor_conf.d/germ.fish` at startup. (kitty and Ghostty use the same trick.)
- germ sets `TERM_PROGRAM=germ` so the script knows where it's running, and the script restores the original `XDG_DATA_DIRS` so child processes don't inherit the change.
- Config switch: `shell_integration = "auto" | "off"`.

### What the script emits (fish event hooks → escape sequences)
| fish event | Sequence | Purpose |
|---|---|---|
| `fish_prompt` | `OSC 133;A` / `OSC 133;B` | Marks where the prompt starts and ends |
| `fish_preexec` | `OSC 133;C` + `OSC 2` (title = command) | Marks where command output starts |
| `fish_postexec` | `OSC 133;D;<status>` | Marks where the command ends, with its exit code |
| `PWD` change | `OSC 7;file://host/path` | Tracks the current directory |

### What germ does with it
- **Jump to the previous/next prompt** in scrollback (e.g. Ctrl+Shift+↑/↓).
- **Select or copy the last command's output** with one keybinding.
- **New tab or split opens in the same directory** (from OSC 7).
- **Exit-status gutter marks**: a colored mark beside each prompt (red for failure, taken from the theme).
- **Notify when a long command finishes** while the window is unfocused.
- **Reflow awareness**: on resize, fish redraws its own prompt instead of germ reflowing it, so you don't get duplicated prompt lines.
- **Keyboard**: fish 4 uses the kitty keyboard protocol (CSI u). Implementing it in `internal/input` gives fish unambiguous key bindings.

The terminal side is just parsing OSC 133 and OSC 7, so other shells (zsh, bash) can get the same features later with a small script each.

**Inside tmux:** tmux 3.4+ understands OSC 133 for its own prompt jumping. With tmux control mode (Level 2), germ sees each pane's output directly, so these features work per pane.

---

## tmux integration design

### Level 1: work well inside tmux (Phase 5)
- Ship a `germ` terminfo entry (falling back to `xterm-256color`) that declares `Tc`/`RGB`, `Smulx`, `Setulc`, `Ss`/`Se`, `Sync`.
- 24-bit true color, styled and colored underlines, cursor shape changes.
- **OSC 52** clipboard, so tmux copy mode lands in the system clipboard.
- Focus events (mode 1004), bracketed paste (2004), SGR mouse (1006).
- Extended keys (CSI u / `extended-keys`), so key combos like Ctrl+Shift+X work through tmux.
- Synchronized output (2026).
- A recommended `tmux.conf` snippet in the docs.

### Level 2: tmux control mode (Phase 7)
- germ starts `tmux -CC new -A -s main` and talks the control protocol (`%begin/%end`, `%output`, `%layout-change`, `%window-add`, …).
- Each tmux pane gets its own `term.Terminal` fed from `%output`. Each tmux window becomes a germ tab, and the tmux layout string maps onto native germ splits.
- Keystrokes go out as `send-keys -H`, and resizes as `refresh-client -C`.
- Detach and reattach keep the session, with native scrollback, native selection, and no prefix key needed.
- This **requires native tabs and splits** in the renderer (Phase 6).

---

## Milestones

| Phase | Deliverable | Done when |
|---|---|---|
| **0. Skeleton** ✅ | `go mod init`, a GLFW window, spawn `$SHELL` on a PTY, log output to stdout | Typing in the window reaches the shell |
| **1. VT core** ✅ | Parser, grid, cursor movement, SGR, scroll regions, alt screen, line wrap, resize/reflow | Headless unit tests pass; `vim`, `htop`, `less` look right in a debug text dump |
| **2. Renderer** ✅ | Font loading, glyph atlas, instanced quads, cursor, dirty-row rendering | A usable daily terminal at 60+ fps |
| **3. Input & UX** ✅ | Full key encoding, mouse reporting, selection, clipboard, scrollback, URL detection | Comfortable for daily use |
| **4. Config & themes** ✅ | TOML config, bundled themes, hot reload, OSC color queries | Changing the theme file recolors the window live |
| **4.5 fish integration** ✅ | Auto-injected fish script, OSC 133/7 parsing, prompt jumping, output selection, same-directory spawn, kitty keyboard protocol | Fresh fish (no config changes) → Ctrl+Shift+↑ jumps between prompts |
| **5. tmux Level 1** ✅ | terminfo, true color through tmux, OSC 52, CSI u, sync output | `tmux` + `nvim` looks and behaves perfectly |
| **6. Perf pass + tabs/splits** ✅ | Benchmarks, profiling, GC tuning; native tabs and splits | Competitive with Alacritty and kitty on vtebench |
| **7. tmux Level 2** ✅ | Control mode client | `germ --tmux` attaches with native tabs and splits |
| **8. Home screen** ✅ | Widget dashboard tab; executable widget plugins; clock, weather, news, stocks | `germ` opens on a live dashboard; a shell script can be a widget |
| **9. Agent attention** ✅ | Detect agent panes; working / waiting / done state per tab; OSC 9/777/99 desktop notifications; agents home widget | Running 3 agents in tabs, you can see which one needs you without visiting each |
| **10. germ as a tool for agents** ✅ | Control socket + `germ ctl` CLI + `germ mcp` MCP server: list/read/send/open panes | Claude Code, connected to `germ mcp`, reads the test-runner pane and starts a build in a new pane |
| **11. Parallel agents** ✅ | Ctrl+Shift+A: task → `git worktree` → tmux window running the agent; live diff-stat side pane; clickable `file:line` → `$EDITOR` | One key spawns an isolated agent; its changes are visible at a glance |
| **12. Agent input polish** ✅ | Shift+Enter newlines, image paste, "send selection to agent pane" | Multi-line prompts and screenshots just work in agent UIs |
| **13. Coworking** ✅ | Relay server + end-to-end-encrypted invite links (`germ share` / `germ join`), install page, read-only guests, presence, follow mode; guests can watch agents and approve prompts | A friend clicks a link and is typing in your session a minute later |
| **14. Coworking v2** ✅ | Remove/ban guests from the share pane, rotate links; guests navigate tabs independently (grouped tmux sessions) with a follow-the-host toggle; "who's typing" presence; automatic reconnect for guests and host | A guest's Wi-Fi blips and they're back a few seconds later; you remove one guest without stopping the share |
| **15. Daily-driver polish** ✅ | Drag separators to resize; OSC 8 hyperlinks; combining characters; zsh/bash shell integration | Your zsh gets prompt jumping, command marks, and notifications like fish |
| **16. Agent tuning** ✅ | Tune working/done/needs-you against real agents; clickable widgets (agents widget jumps to the pane); agent activity timeline | Badges match what agents are actually doing |
| **17. Agent precision** ✅ | Agents report their own state: `germ setup agents` installs Claude Code hooks and Codex `notify` that call `germ ctl agent-event`, overriding screen heuristics; "finished" notifications; keyboard hints mode (Ctrl+Shift+E: label every URL, `file:line`, git hash, IP on screen; a letter opens, Shift+letter copies) | Badges and `wait_for` are exact for Claude Code, and you never reach for the mouse to open what an agent printed |
| **18. Agent review & throughput** ✅ | Review tab for `germ agent` worktrees: diff with file list, merge / discard / send feedback to the agent in one key; throughput, latency and memory benchmarks against kitty and alacritty, then fixes | Three agents finish, and you review and merge each from one tab; a 100 MB log scrolls past without germ falling behind |
| **19. Daily-driver core** ✅ | Native Wayland (GLFW 3.4) with fractional scaling and IME; opt-in ligatures (go-text shaping); synthetic italic/bold; DPI changes across monitors; crash log, `germ doctor`, config errors shown in the window; base16/iTerm2/Alacritty theme import | Crisp text on a 1.25× Wayland display, and a broken config tells you what's wrong |
| **20. Survive a reboot** ✅ | Snapshot each tmux session's windows, layouts, directories and agents every 30 s; rebuild them when the session is created afresh; type agents' resume commands | Reboot, open germ: your tabs and splits are back in their directories, with `claude --continue` waiting in each agent pane |
| **21. Command palette** ✅ | Ctrl+Shift+Space: fuzzy-searchable list of every action (with its key) | Every feature is findable without memorizing chords |
| **22. Input latency** ✅ | Measure key-to-screen latency against kitty/ghostty; fix what's slow | germ is as quick to echo a key as the fastest terminal here |
| **23. Coworking v3: chat and the helm** ✅ | One invite link; a session chat (docked panel in germ, side panel in the browser); typing by turns: one person at the helm per tab, guests request it, the host (or the tab's owner) approves, Ctrl+Shift+O always takes it back; tabs assigned to a guest | You give alice the feature tab and she works there while you work in yours; bob asks for the helm in chat and you hand it over with one key |

### Testing approach
- `internal/vt` and `internal/term` are pure Go with no GL, so we can feed byte streams in and assert on the grid. These tests are the safety net.
- Conformance: `vttest` and esctest.
- Golden tests: record real sessions (`script`) and snapshot the resulting grid.

---

## Agentic coding design (Phases 9–12)

germ owns the screen, the tmux session, and the input path, so it can know
things about agents that other terminals can't.

**Agent attention (9).** A pane's agent is identified by its foreground
process (`/proc`, or `#{pane_current_command}`/`#{pane_pid}` under tmux):
`claude`, `codex`, `aider`, `gemini`, `opencode`, ... (configurable).
State comes from signals agents already emit:
- *waiting*: a notification (OSC 9, OSC 777;notify, OSC 99) or bell from
  the pane — agents send these when they need permission or input;
- *working*: output in the last few seconds;
- *done/idle*: quiet after working, without a notification.
The tab bar shows a badge per tab (spinner / ● needs you / ✓), unfocused
notifications go to the desktop (notify-send), and an `agents` widget on
the home tab lists every agent pane in every germ window with its state
and elapsed time.

**germ as a tool (10).** Each germ window listens on a Unix socket
(`$XDG_RUNTIME_DIR/germ/<pid>.sock`, exported as `GERM_SOCKET`) speaking
line-delimited JSON. `germ ctl` is a CLI over it, and `germ mcp` is a
stdio MCP server exposing the same operations as tools:
`list_panes`, `read_pane` (screen and/or scrollback as text),
`send_text`, `open_pane` (new tab or split, running a command),
`wait_for` (until text appears or the pane goes quiet), `pane_state`.
Agents reach the window they run in via `GERM_SOCKET`; `germ mcp`
falls back to the newest socket. Writes are confined to panes in the
same window; the socket is 0600.

**Parallel agents (11)** and **input polish (12)** build on both.

## Coworking v3 design (Phase 23)

**One link.** Everyone who joins can watch and chat; typing is by turns.
The read-only link is gone (`[share] approve = "typing"` now means "all").

**The helm, per tab.** Each tab (tmux window) has an owner (the host
unless assigned) and a helm holder (the owner unless handed over). Only
the holder's keystrokes reach the tab, the host's included: one person
types at a time.
- A guest asks for the helm (Ctrl+Shift+O in germ, a button in the
  browser). The tab's owner approves if a guest owns it and holds it;
  otherwise the host. The host can answer any request.
- The holder can hand it back (Ctrl+Shift+O again).
- The host's Ctrl+Shift+O takes the helm of the tab they're looking at,
  from anyone, immediately.
- Assigning a tab to a guest makes them its owner and holder ("Give this
  tab to alice" in the palette, `/give alice` in chat); taking it back
  returns both to the host. Owners and holders survive reconnects (people
  are identified by their client ID, not their connection); 90 s after
  someone leaves for good, their tabs go back to the host.
- Enforced on the host: guest tmux commands are allowlisted (view
  commands always; commands that type or change a tab only for the tab's
  holder; `send-keys -K`, i.e. tmux key bindings, never). Browser guests
  attach to their own grouped session, but nothing is ever written to that
  client: their keystrokes go through `send-keys -H` to the active pane, so
  tmux's prefix and command prompt are out of reach. (Not `attach -r`:
  command-line tmux commands run as the most recently active client, and a
  read-only one makes tmux refuse them, the host's own included.) The host's germ drops its own keystrokes in a
  tab a guest holds.

**Chat.** The host's `germ share` process owns the channel: guests send
text to it, it stamps sender (by connection, so nobody can impersonate),
sequence number and time, and broadcasts. Everyone renders in the host's
order. Last 500 messages in memory, last 200 sent to newcomers; nothing
on disk unless the host saves it (`/save`, the palette, or `s` in the
share pane: `~/.local/share/germ/chats/`). Session events (joins, helm,
tabs) are dim one-line notices. 4 KB per message, rate-limited, the host
can mute. `@name` / `@here` highlight and notify. "alice is typing…"
throttled.
- germ: a docked panel on the right (Ctrl+Shift+M opens and focuses; Esc
  back to the terminal; Ctrl+Shift+M again closes). While focused, keys go
  to the message box, and the terminal cursor goes hollow. Enter sends,
  Shift+Enter newline, Up recalls. With an empty box, y/n answer a pending
  helm or join request. Closed, new messages show an unread count and a
  preview in the tab bar.
- The host's germ talks to `germ share` over the control socket:
  `share_update` (share → germ: people, tabs and helm, requests, new
  messages) and `share_wait` (germ long-polls for the host's chat and
  decisions).
- Browser: a chat panel, a tab strip (look at any tab, or follow the
  host), and "Request the helm".

## Decisions (2026-11-23)
- **Home screen**: germ-native first tab; widgets are executables (any language) run in PTYs.
- **Coworking**: start with a tiny relay + E2E-encrypted invite links (no SSH setup for guests); hosting the relay is the user's call later.
- **Next**: agent attention (9) and agent tooling / MCP (10).

## Decisions (2026-11-22)
1. **Linux only.** We use OpenGL 3.3 core and don't need to plan for Metal or wgpu.
2. **tmux control mode is a must-have.** Phases 6 (tabs/splits) and 7 (control mode) are committed. The terminal state is built from the start so that a single window can hold many `term.Terminal` instances.
3. **Ligatures are optional.** v1 uses `golang.org/x/image/font/opentype` with one glyph per cell and no shaping. HarfBuzz-style shaping (`go-text/typesetting`) can be added later behind the `font` package interface.
4. **XWayland is fine to start.** We use GLFW 3.3 on X11 and can switch to GLFW 3.4 for native Wayland later.

## Status (Phase 23)
- One invite link (protocol v3; the watch-only link and `approve = "typing"` are gone). People are identified by client ID across reconnects (`share.Participant.ID` is a person ID); names are unique by short name ("alice", "alice (2)").
- The room (`internal/share/room.go`): chat (host-stamped sender, seq and time; 500 kept, 200 sent to newcomers; 4 KB, 5-message burst then 1/s; mute), per-tab owner and helm, helm requests (approver: the tab's owner if a guest owns and holds it, else the host; host can always decide), events in the chat, 90 s grace for people who leave. Saved only on command (`~/.local/share/germ/chats/`).
- Enforcement in `germ share`: guest tmux commands allowlisted by class (view / needs the helm of the -t target's tab / new-window needs the helm of the guest's current tab); `send-keys -K` never. Browser guests get grouped sessions too (tab strip, follow the host) and type via `send-keys -H`. Not `attach -r`: a read-only client makes tmux refuse command-line commands, the host's own included (found testing the browser).
- germ: `internal/app/chat.go`, a docked panel drawn as a small `term.Terminal` (Ctrl+Shift+M `toggle_chat`), Ctrl+Shift+O `helm`; the host's own keystrokes and mouse reports are dropped in a tab a guest holds; `⎈alice` tab tags; toasts and unread count in the tab bar; @mentions notify; y/n answer requests with an empty box; `/give /takeback /mute /unmute /save /yes /no /helm`; palette items. The host's window talks to `germ share` with `share_update` / `share_wait` (long poll) on its own control connection.
- Browser: chat panel, tab strip, Request / Hand back the helm, mention highlights, unread count in the title; mouse and focus reports never sent.
- A branch for a guest: `germ agent -for NAME [TASK]` makes a worktree on `germ/NAME-task` (git config `germ-for`), runs a shell with the live diff beside it, and gives the tab away (`share_give` control command → the window → `germ share`). `/branch NAME TASK` in the chat and "New tab for NAME on its own branch" in the palette open it. `germ review` labels it "NAME: task" and won't type feedback into a person's shell.
- Fixed: a guest following the host could show the wrong tab after a reconnect (tmux says nothing when the session is already on the host's window). `germ share`'s control connection was used from several goroutines at once. Guests' chat panels didn't hear about people joining after them.
- Tests: `TestHelmAndChat` (in-process relay, host, three guests), `TestFilterCommand`, chat rendering tests; `scripts/e2e/share.sh` drives two germ guests through the helm, chat, `/give`, a relay restart and removal (21 checks, including /branch). The browser viewer was checked by hand in Firefox on Xvfb.

## Status (Phase 22)
- Key-to-screen latency (scripts/bench/latency.sh + keylat.c: XTest key, XGetImage poll until the window changes; terminals run `cat`; Xvfb/llvmpipe): germ ~5 ms median (p95 10–21 ms, one emulated 60 Hz swap), tmux mode ~6–9 ms; kitty 54–68 ms, ghostty ~45 ms (their deliberate input/repaint delays, tuned for real vsync). No fix needed.
- Fixed (found by the benchmark): session restore ran for `germ -tmux -shell PROGRAM` and replaced the program; restore and snapshots are now skipped for explicit commands. e2e/bench scripts use a temporary XDG_STATE_HOME.

## Status (Phase 21)
- Command palette (Ctrl+Shift+Space, `command_palette`): `render.View.TopOverlay` (rows drawn instead of the top rows, no cursor; the cursor no longer draws over the search bar either). Items: every labeled action with up to two keys from the live bindings, plus Open config file, germ doctor / germ setup agents in a tab, and live theme previews. Running agents come first ("Go to agent: claude · needs you · <tab> · <message>", most urgent first). Fuzzy subsequence scoring (word starts, runs). Enter runs, ↑↓/Tab/Ctrl+N/P move, Esc closes.

## Status (Phase 20)
- Snapshots: `list-panes -s` every 30 s (agent tick) into $XDG_STATE_HOME/germ/sessions/<session>.json (window name/layout/active, pane cwd/agent/active), only once restore is done. Restore when startTmux creates a session that didn't exist: one command line per window (new-window -c, split-window -c, select-layout, select-pane {next}), kill the initial window, select the active one relative to `:^`; resume commands typed 1.5 s later by pane ID (typed earlier, shells echo them before the prompt). `%exit` not caused by germ deletes the snapshot unless systemd says the system is stopping or the login session is closing. e2e verified by hand (simulated reboot: kill -9 germ, kill-server, relaunch); `[tmux] restore`.

## Status (Phase 19)
- Native Wayland deferred: the user runs Xorg + i3, and go-gl's GLFW 3.4 bindings are pre-release (v0.1.0-pre.2).
- Config problems (parse errors, unknown keys, bad theme, font load, tmux start failure) go to the tab bar status for 20 s and a desktop notification, at startup and on reload.
- Crash log: `debug.SetCrashOutput` to a per-process temp in $XDG_STATE_HOME/germ, renamed to crash.log when its process is gone and non-empty; the next launch reports it once.
- Synthetic italic (0.2 shear around the baseline, sub-pixel) and bold (n-px horizontal dilation) when fontconfig returns the upright/regular face for a style; checked by face identity.
- Content-scale callback reloads fonts and padding at the new DPI (no-op on X11, where the scale is global).
- `germ theme import FILE` (base16/base24 YAML incl. tinted `palette:` layout, iTerm2 .itermcolors plist, Alacritty TOML; validated by theme.Parse) and `germ theme list`.
- Ligatures (`[font] ligatures`, off by default): runs of ASCII punctuation in one style are shaped with go-text HarfBuzz; when the font substitutes glyphs one-for-one (calt-style coding ligatures), cells draw those glyph IDs (sfnt outline + x/image/vector, `atlas.getGlyph`). Fewer-glyph shapings are ignored, so the grid never changes. Cached per (text, style); no throughput cost measured.
- `germ doctor`: config, theme, font (fc-match family vs requested), emoji font, tmux ≥ 3.2, shell integration, notify-send, xdg-open, germ:// handler, git, Claude/Codex hooks, relay reachability / Funnel / cloudflared, last crash.

## Status (Phase 18)
- `germ review` / Ctrl+Shift+R (`review_agents`): lists `germ/*` worktrees of the repo (from any of its worktrees), task/base/from from `branch.<b>.germ-*` git config (written by `germ agent`; merge-base fallback), commits, numstat, untracked, dirty; agent pane matched by cwd with live state and message. Enter diff in git's pager, f feedback (send_text), o focus, c commit, m merge (commit + close panes + merge --no-ff + remove), d discard. Polls stdin (no reader goroutine) so the pager/prompt get keys. `mergeBranch` / `removeWorktreeAt` shared with the post-exit menu.

- Throughput (scripts/bench/throughput.sh, Xvfb/llvmpipe, 4 cores, 100x30): plain germ 2.4 s / 1.2 s / 2.1 s for 40 MB log / 20 MB SGR / 20 MB UTF-8 vs kitty 3.0 / 1.5 / 3.8 and ghostty 7.6 / 7.1 / 3.6. tmux mode 5.6 / 6.0 / 15.9 vs kitty+tmux 5.8 / 6.7 / 20.3: the tmux server pegs a core, so it's the ceiling there. RSS after 40 MB: germ 237 MB, kitty 245 MB. No optimization needed; latency not measured yet.
- Fixed: `-shell` ignored in tmux mode (now the first pane's command of a new session); share tests leaked a tmux server per run (socket removed before kill-server; 73 orphans).

## Status (Phase 17)
- Agent self-reporting: `agent_event` control command; `germ ctl agent-event` (claude: hook JSON on stdin; codex: notify JSON arg; or a plain state). Never prints to stdout, always exits 0, 2 s cap, only reports to the window owning its pane (requires GERM_SOCKET). ~9 ms per call.
- `paneAgent.hook` overrides screen/output heuristics while the reporting foreground pgid lives; reported "working" lapses after 15 s without events or output (no Stop on Esc); OSC/bell notes ignored for hook-reporting agents (no duplicates); "finished" notifications (`[agents] notify_done`).
- Hints mode (Ctrl+Shift+E, `hints` action): URLs, OSC 8 links, existing `file:line` refs, git hashes, IPs and host:port; labels drawn via `render.Highlight.Label`; newest output gets home-row keys; lowercase opens (host:port as http), uppercase copies. `urlMatches` / `resolveFile` shared with Ctrl+click.
- `germ setup agents [-remove] [-n]`: Claude Code hooks (10 events) merged into settings.json preserving key order and the user's hooks, backup `.germ-bak`; Codex top-level `notify` (left alone if set to another program). e2e: scripts/e2e/agents.sh.

## Status (after the polish pass)
- Scrollback search (Ctrl+Shift+F): bar on the pane's bottom row (render.View.Overlay), highlights (View.Highlights), smart case, matches across soft wraps, Enter/Shift+Enter or Up/Down, Esc. Guest follow moved to Ctrl+Shift+G.
- CI on every push/PR (`ci.yml`): gofmt, vet, race tests, builds; Xvfb e2e suites (non-blocking for now; green so far).
- Built-in widgets run in-process (`widgets.RunInProcess`, per-widget Options); custom widgets still run as programs.
- Rendering: anti-aliased rounded corners; cursor blink when requested (solid while active); selection autoscroll past pane edges (unclamped pointer row); color emoji via go-text bitmap glyphs into a second RGBA atlas and a fourth draw pass (pictographs + Emoji_Presentation chars prefer color).
- Startup: fc-match answers cached in ~/.cache/germ/fontmatch.json (invalidated when fontconfig caches are newer); style lookups in parallel. Warm font load ~7 ms (was ~95 ms of fc-match).

## Status (after sharing security pass)
- Exposure order: Tailscale Funnel (`tailscale funnel --https=443|8443|10000` in the foreground; needs Running backend + `funnel` node attribute; clear errors for operator/enablement) → Cloudflare quick tunnel. Direct/UPnP only with `mode = "direct"`.
- Join approval (`[share] approve`, default all): host sends `Q` (waiting), `OnRequest` → share pane y/N with bell + notify-send; 2-minute timeout; max 5 waiting. Approved `client` IDs (random per guest, kept across reconnects) get back in without asking.
- Built-in relay: host must present `X-Germ-Host-Token` (tunnels make every request look local); 20 guest connections/minute per room.
- Verified: approval flow in Firefox through the Cloudflare tunnel; `TestApproval` (wait, deny, token, reconnect without re-asking).

## Status (after zero-setup sharing)
- `germ share` without `[share] relay` runs a private relay in-process (one room; host only from loopback and not via the tunnel) and exposes it: UPnP port mapping on the router (public, non-CGNAT address required) else a Cloudflare quick tunnel (`cloudflared` downloaded once to ~/.cache/germ/bin).
- Browser viewer at the invite link: xterm.js + addon-fit + tweetnacl vendored in `internal/share/web` (THIRD_PARTY_LICENSES.txt). Browser guests get a plain `tmux attach` in a pty (watchers: `-r -f ignore-size`, input dropped on the host); native germ guests keep control mode.
- Crypto switched to NaCl secretbox (protocol v2) because Web Crypto needs a secure context and direct links are http; `TestBrowserCrypto` checks Go ↔ tweetnacl.
- Verified: direct (manual address) and tunnel modes with Firefox on Xvfb (type / watch-only), native join through the tunnel.

## Status (after Phase 16)
- Agent state also comes from the screen: the bottom 12 non-blank lines are matched against `[agents] working_patterns` ("esc to interrupt", which Claude Code and Codex show while working) and `waiting_patterns` (permission/question prompts). A prompt appearing acts like a notification (desktop notify when not visible; the question line becomes the message) and shows as waiting while it lasts. Works without the agent's notification setup.
- Per-second state history (30 min ring) → `PaneInfo.History` (10 s buckets) with `list_panes {"history": true}`; the agents widget draws a one-cell-per-minute timeline.
- `germ-pane:PID/PANE` hyperlinks focus a pane via that window's control socket (raising the window); agents widget rows use them.
- Verified on Xvfb with a fake Claude Code UI: working held across slow updates, prompt → waiting with the question as message, click → focused.

## Status (after Phase 15)
- Shell integration for zsh (ZDOTDIR bootstrap that restores ZDOTDIR and sources the user's .zshenv; zle line-init hook after `zmodload zsh/zle`, plus a PS1 suffix since Oh My Zsh replaces zle-line-init) and bash (PROMPT_COMMAND bootstrap + DEBUG-trap preexec). tmux mode sets the variables on germ's session too. Tests run real interactive shells in a pty.
- OSC 8 hyperlinks: `Cell.Link` indexes a per-terminal URI table (survives SGR resets; http/https/mailto/file/ftp/sftp/ssh/man only); Ctrl+hover underlines the run, Ctrl+click opens via `$BROWSER`/xdg-open, hand cursor. Widget panes need no Ctrl; news/stocks/weather emit links.
- Drag separators: native tabs set the split ratio; tmux tabs send `resize-pane -L/-R/-U/-D` for the pane bordering the separator.
- Combining characters: NFC-compose with the previous cell when possible, else `Cell.Mark` (one per cell) drawn after the base (fonts place zero-advance marks back over it). Joiners/variation selectors dropped.
- `germ <command>` skips the home tab.

## Status (after Phase 14)
- Guests get grouped tmux sessions (`<session>-guest-N-xxx`, tagged `@germ-guest`, removed on leave, swept on start); window targets are session-qualified (`$id:@w`) everywhere since `@w` is ambiguous in a group; watchers may only `select-window -t $own:@w`.
- Follow mode: guests track `%session-window-changed` for the host's session until they switch tabs; Ctrl+Shift+G resumes. Each germ only reacts to its own session's window changes.
- Presence carries IDs and typing (send-keys within 3 s); share pane is interactive (copy links, remove by number, rotate links, quit).
- Reconnect: guests retry for 90 s unless removed/said goodbye (`msgBye` before an intentional stop; reads use their own context so the goodbye gets out); the stream restarts with `%germ-reconnect`, on which the controller drops stale callbacks and re-reads everything (commands are dropped while offline). The host waits for tmux's `%session-changed` before welcoming, or early commands return empty. `germ share` reconnects to the relay with backoff.
- `germ <command>` skips the home tab. `GERM_DEBUG_TMUX=file` logs the control-mode conversation.
- Tests: `TestShareV2` (grouped sessions, typing, removal, relay drop + reconnect); `scripts/e2e/share.sh` now 10 checks incl. follow, independent tabs, relay restart, removal.

## Status (after Phase 13)
- `germ share` / Ctrl+Shift+P: per-guest `tmux -C attach` tunnelled over WebSocket through `germ-relay`; two links (can-type / watch-only) with separate AES-256-GCM keys in the URL fragment; host-side command filter (allowlist for watchers, denylist for typists, no `;` or `#(`); presence in the tab bar via `set_status`.
- `germ join LINK`: germ's tmux controller runs over the tunnel (no pane PIDs or local file links for remote panes). `germ setup` registers `germ://` links. Join page served by the relay.
- `cmd/germ-relay` (pure Go) + `deploy/relay` (Dockerfile). `docs/coworking.md`.
- Verified: `internal/share` in-process test with real tmux; `scripts/e2e/share.sh` (relay + host + two guests on Xvfb).
- TODO: prebuilt binaries so guests can install without Go; per-guest independent window (grouped sessions); host UI to kick guests; guest cursors.

## Status (after Phase 12)
- Shift+Enter → ESC CR in agent panes when no extended key protocol is on (`[agents] shift_enter_newline`).
- Ctrl+Shift+V with an image on the clipboard: saved to `~/.cache/germ/paste/` via wl-paste/xclip, path pasted. File drops paste quoted paths.
- Ctrl+Shift+S (`send_to_agent`): selection or last output → most recently used agent pane, fenced with its source, then focused.
- Verified on Xvfb with a key logger posing as `claude`.

## Status (after Phase 11)
- `germ agent [TASK]` / Ctrl+Shift+A: worktree on `germ/<slug>` in `<repo>.worktrees/`, tab renamed via the control socket (`rename_tab`), `git` widget opened as a 35% split (`open_pane` size), agent run in its own foreground process group (so attention detection sees it), then a review menu (diff / commit / merge --no-ff + cleanup / resume / shell / keep / discard).
- Ctrl+click `path:line[:col]` (existing files, relative to the pane's cwd) opens `$EDITOR` in a split (GUI editors detached).
- Ctrl+Shift+` toggles the home tab (tmux's own window keys can't reach it).
- `GERM_CONFIG` is exported so subcommands read the launching window's config.

## Status (after Phases 8–10)
- Home tab: widgets are PTY-run programs (live or interval), built-ins via `germ widget NAME` (clock, weather, news, stocks, agents). `docs/widgets.md`.
- Agent attention: `/proc` tpgid → foreground command matched against `[agents] commands`; OSC 9/777/99 + BEL → "needs you"; output not explained by typing echo, or OSC 9;4 progress → "working"; tab badges (spinner / ● / ✓) clear on view; notify-send when not visible. Main loop ticks at 10 fps only while a spinner shows, plus a 1 s detection tick.
- Control socket `$XDG_RUNTIME_DIR/germ/<pid>.sock` (JSON lines, executed on the main thread), `GERM_SOCKET` exported (and `set-environment` in tmux sessions). `germ ctl` CLI; `germ mcp` stdio MCP server with list_panes / read_pane / send_text / open_pane / wait_for. `docs/agents.md`.
- Verified on Xvfb: fake agent badges; ctl open/wait/read/send in tmux and local modes; MCP session over stdio.
- TODO: agent working/done heuristics need tuning against real Claude Code/Codex sessions; mouse clicks on widgets; `wait_for` push notifications instead of polling.

## Status (after Phase 7) — all planned phases complete
- tmux control mode: `germ -tmux [-session NAME]` or `[tmux] enabled = true`. tmux windows ↔ germ tabs, panes ↔ native splits with exact tmux geometry (layout strings parsed into the same split tree), per-pane germ scrollback/selection/clipboard, output via `%output`, input via `send-keys -H`, existing screens loaded with `capture-pane`, zoom via the visible layout, session kept on exit and restored on reattach. The tab bar shows the session name.
- Parser now swallows screen/tmux `ESC k … ST` window-name strings (apps emit them whenever TERM is tmux*/screen*).
- E2E: `scripts/e2e/tmux.sh` (isolated tmux server on Xvfb) checks splits, focus, and input routing.
- Possible next steps: drag-to-resize separators (tmux `resize-pane` / native ratios), OSC 8 hyperlinks, kitty keyboard flags 4/16, color emoji, combining characters, native Wayland (GLFW 3.4), zsh/bash shell integration.

## Status (after Phase 6)
- Native tabs and splits: binary split tree in cells with tmux-style 1-cell separators; new panes open in the active pane's directory (OSC 7, else /proc). Tab bar appears with 2+ tabs (click or wheel to switch).
- Defaults: Ctrl+Shift+T new tab, Ctrl+Shift+Q close tab, Ctrl+Tab / Ctrl+Shift+Tab or Ctrl+Shift+Left/Right switch, Ctrl+Shift+Enter split right, Ctrl+Shift+D split down, Ctrl+Shift+W close pane, Ctrl+Shift+H/J/K/L focus, Ctrl+Shift+Z zoom pane. Click a pane to focus it.
- Renderer split into a shared Renderer (GL, fonts, atlas) and per-pane Surfaces with their own row caches; one frame draws all visible panes in three instanced draws.
- Panes have pluggable backends (local PTY now, tmux next).
- Perf: profiling showed the kernel PTY, not germ, bounds `cat` throughput (see Phase 2), so the planned ring-buffer grid was not needed.
- TODO: drag separators to resize; per-tab layouts persisted across restarts.

## Status (after Phase 5)
- `TERM=xterm-germ`: terminfo compiled with `tic -x` into `~/.local/share/germ/terminfo` and exposed via `TERMINFO_DIRS`; the `xterm*` name makes tmux enable clipboard/cstyle/ccolour/focus/title by default. `[shell] term` overrides.
- OSC 52 clipboard writes (reads refused for safety), DECRQSS (SGR, margins, cursor style), XTGETTCAP, xterm modifyOtherKeys levels 1–2 (tmux `extended-keys`).
- Verified on Xvfb: tmux 3.7 reports `bpaste,ccolour,clipboard,cstyle,focus,RGB,title`; `set-buffer -w` reaches the system clipboard. `docs/tmux.md` has the recommended tmux.conf.

## Status (after Phase 4.5)
- fish integration is automatic: germ writes `germ-shell-integration.fish` to `~/.local/share/germ/shell-integration/fish/vendor_conf.d/` and prepends that dir to `XDG_DATA_DIRS` for fish only; the script restores the variable for children. Disable with `[shell_integration] mode = "off"`.
- OSC 133 A/B/C/D and OSC 7 are tracked: prompt marks live on lines (survive scrollback and reflow), last command output range, exit status, cwd.
- Actions: `previous_prompt`/`next_prompt` (Ctrl+Shift+Up/Down), `copy_last_output` (Ctrl+Shift+X). Failed commands get a red mark in the left padding. Commands over 10 s that finish while unfocused trigger notify-send + window urgency.
- Fonts: hinted golang/freetype rendering is used only for runes its cmap maps; others (e.g. some Nerd Font icons) fall back to x/image.
- TODO: clear the prompt before reflow so resizing doesn't duplicate fish's prompt; zsh/bash scripts.

## Status (after Phase 4)
- Config: `~/.config/germ/config.toml` (see `config.example.toml`), layered over defaults, unknown keys warned, errors keep the previous config. Command-line flags override it.
- 9 bundled themes (`germ -list-themes`), user themes in `~/.config/germ/themes/`, `[colors]` overrides.
- Live reload via fsnotify (directory watch, debounced): theme, colors, font, padding, keybindings.
- Keybindings are configurable chords → actions; `none` unbinds.
- OSC 4/10/11/12 set and query, OSC 104/110/111/112 reset; app overrides survive theme reloads.
- TODO: importers for base16/iTerm2/Alacritty themes.

## Status / known gaps (after Phase 3)
- Input: full xterm legacy key encoding plus the kitty keyboard protocol (flags 1, 2, 8; per-screen flag stack; fish 4 enables it automatically).
- Mouse: X10/normal/button/any reporting with default, UTF-8, SGR, URXVT, and SGR-pixel encodings; Shift bypasses reporting.
- Selection: drag, double-click word, triple-click line; copies to PRIMARY (xclip/xsel) on select; Ctrl+Shift+C/V clipboard; middle-click pastes PRIMARY; bracketed paste with end-marker stripping.
- Scrollback viewing: wheel / Shift+PgUp/PgDn, anchored while new output arrives; alt screen wheel sends arrows (mode 1007).
- Ctrl+hover underlines URLs, Ctrl+click opens them (xdg-open). Ctrl+Shift+=/-/0 zoom.
- E2E: `scripts/e2e/xtest.sh` drives germ on Xvfb with xdotool and logs the bytes it sends.
- TODO: kitty flags 4 (alternate keys) and 16 (associated text); OSC 8 hyperlinks; cursor blink; selection auto-scroll while dragging past the edge.

## Status / known gaps (after Phase 2)
- Renderer: three instanced draw calls per frame (backgrounds, glyphs, decorations) with per-row instance caches rebuilt only for dirty rows; idle = 0 CPU; synchronized output (2026) holds frames up to 200 ms.
- Box drawing (U+2500–257F) and block elements (U+2580–259F) are drawn procedurally so tmux borders join seamlessly.
- End-to-end `cat` of a 22 MB file: ~4 s, versus ~3.5 s for a do-nothing PTY reader on the same machine — the kernel PTY is the bottleneck.
- TODO: synthetic italic when the font has no italic face; proper double-line junctions; rounded corners as arcs.
- TODO: color emoji (CBDT/COLR) are not rendered; handle DPI changes when moving between monitors.
- TODO: font discovery shells out to `fc-match` (~100 ms at startup); cache results or use fontconfig directly.
- Testing: `germ -screenshot out.png -- <cmd>` saves a PNG of the window; `GERM_CPUPROFILE=file` writes a CPU profile.

- Phase 1 baseline throughput (headless) (i3-4030U @ 1.9 GHz): plain text ~82 MB/s, color-heavy ~32 MB/s, 0 allocs once scrollback is full. Parser alone: ~130 MB/s.
- TODO: combining characters (zero-width runes are currently dropped).
- TODO: the grid scrolls by shifting row slices; switch to a ring-buffer grid in the Phase 6 perf pass.
- TODO: DECRQSS / XTGETTCAP (Phase 5), OSC colors (Phase 4), OSC 7/133 (Phase 4.5), kitty keyboard protocol (Phase 3).
- Debug tool: `go run ./cmd/germ-vtdump -- <cmd>` renders a program's screen headlessly.
