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
| **19. Daily-driver core** ✅ | Opt-in ligatures (go-text shaping); synthetic italic/bold; DPI changes across monitors; crash log, `germ doctor`, config errors shown in the window; base16/iTerm2/Alacritty theme import | A broken config tells you what's wrong |
| **20. Survive a reboot** ✅ | Snapshot each tmux session's windows, layouts, directories and agents every 30 s; rebuild them when the session is created afresh; type agents' resume commands | Reboot, open germ: your tabs and splits are back in their directories, with `claude --continue` waiting in each agent pane |
| **21. Command palette** ✅ | Ctrl+Shift+Space: fuzzy-searchable list of every action (with its key) | Every feature is findable without memorizing chords |
| **22. Input latency** ✅ | Measure key-to-screen latency against kitty/ghostty; fix what's slow | germ is as quick to echo a key as the fastest terminal here |
| **23. Coworking v3: chat and the helm** ✅ | One invite link; a session chat (docked panel in germ, side panel in the browser); typing by turns: one person at the helm per tab, guests request it, the host (or the tab's owner) approves, Ctrl+Shift+O always takes it back; tabs assigned to a guest | You give alice the feature tab and she works there while you work in yours; bob asks for the helm in chat and you hand it over with one key |
| **24. More widgets** ✅ | Widget clicks (`germ-widget:`, `germ-open:`); system, ports, PRs, worktrees, coworking, calendar (iCal URL), todo (markdown checklist, click to toggle) | The home tab shows your dev servers, PRs and next meeting, and you tick off todos by clicking |

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
There is no read-only link.

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

## Notes

What building it taught us that the code doesn't make obvious. The
history of each phase is in `git log`.

**Performance** (i3-4030U, 4 cores; benchmarks on Xvfb/llvmpipe, which caps
germ near 22 fps, so judge the pipeline from logs, not fps)
- Headless throughput (`internal/term` benchmarks): ~112 MB/s plain text,
  ~42 MB/s SGR-heavy, 0 allocs once scrollback is full. End to end, the
  kernel PTY bounds `cat`, which is why the grid never became a ring buffer.
- scripts/bench/throughput.sh (100x30): plain germ 2.4 / 1.2 / 2.1 s for 40 MB
  log / 20 MB SGR / 20 MB UTF-8 vs kitty 3.0 / 1.5 / 3.8 and ghostty
  7.6 / 7.1 / 3.6. In tmux mode the tmux server pegs a core and is the
  ceiling (germ 5.6 / 6.0 / 15.9 vs kitty+tmux 5.8 / 6.7 / 20.3).
- scripts/bench/latency.sh (key to screen, terminals running `cat`): germ
  ~5 ms median, tmux mode 6–9 ms; kitty 54–68 ms and ghostty ~45 ms (their
  deliberate repaint delays).
- Synchronized output (mode 2026) is held *unparsed* until it ends
  (`term/sync.go`, 200 ms timeout). Under tmux, a `%output` chunk often
  ends one frame and begins the next, so gating draws on the mode (what
  the renderer used to do) hid finished frames: nvim with smear-cursor or
  snacks.scroll felt sluggish.
- fc-match answers are cached (~/.cache/germ/fontmatch.json): a warm font
  load is ~7 ms instead of ~95.

**tmux**
- Kill a tmux server before removing its socket, or it lives on (share
  tests once leaked 73).
- Guests' grouped sessions make `@w` ambiguous: window targets are
  session-qualified (`$id:@w`) everywhere.
- The host waits for `%session-changed` before welcoming a guest, or early
  commands return empty. tmux says nothing when a session is already on
  the window it's told to select (this broke following after a reconnect).
- Session restore types agents' resume commands 1.5 s after rebuilding
  (sooner, shells echo them before the prompt). `%exit` deletes the
  snapshot unless systemd says the system is stopping or the login
  session is closing. Restore and snapshots are skipped when germ runs an
  explicit command.
- The parser swallows screen/tmux `ESC k … ST` window-name strings.

**Sharing**
- Browser guests aren't `attach -r`: command-line tmux commands run as the
  most recently active client, and a read-only one makes tmux refuse them,
  the host's own included.
- NaCl secretbox, not Web Crypto: Web Crypto needs a secure context and
  direct links are plain http.
- The relay requires `X-Germ-Host-Token` from the host because tunnels make
  every request look local.
- Tailscale Funnel needs a Running backend and the `funnel` node attribute;
  it runs in the foreground on 443, 8443 or 10000.
- A goodbye (`msgBye`) is read with its own context so it gets out while
  the connection is being torn down.

**Shells and agents**
- zsh integration bootstraps through ZDOTDIR (restoring it and sourcing the
  user's .zshenv) and adds a PS1 suffix, since Oh My Zsh replaces
  zle-line-init. fish's `XDG_DATA_DIRS` change is for fish only and
  restored for its children.
- `germ ctl agent-event` never prints, always exits 0, gives up after 2 s.
  A hook-reported "working" lapses after 15 s without events or output
  (Claude Code sends no Stop on Esc).

**Rendering**
- Hinted freetype is used only for runes in its cmap (some Nerd Font icons
  fall back to x/image). Ligatures only use one-for-one glyph
  substitutions, so the grid never changes. Synthetic italic and bold are
  chosen by comparing face identity with the regular face.
- Native Wayland is deferred: the user runs Xorg + i3, and go-gl's GLFW 3.4
  bindings are pre-release.

**Testing**
- e2e scripts source `scripts/e2e/lib.sh`, which keeps them off your desktop,
  tmux sessions and state (see its header). Run them on `Xvfb :99`.
- `germ -screenshot out.png`, `GERM_CPUPROFILE=file` (written only on a
  normal exit), `GERM_DEBUG_TMUX=file` (the control-mode conversation),
  `go run ./cmd/germ-vtdump -- CMD` (a program's screen, headless).
