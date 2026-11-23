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
| **13. Coworking** | Relay server + end-to-end-encrypted invite links (`germ share` / `germ join`), install page, read-only guests, presence, follow mode; guests can watch agents and approve prompts | A friend clicks a link and is typing in your session a minute later |

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

## Decisions (2026-11-23)
- **Home screen**: germ-native first tab; widgets are executables (any language) run in PTYs.
- **Coworking**: start with a tiny relay + E2E-encrypted invite links (no SSH setup for guests); hosting the relay is the user's call later.
- **Next**: agent attention (9) and agent tooling / MCP (10).

## Decisions (2026-11-22)
1. **Linux only.** We use OpenGL 3.3 core and don't need to plan for Metal or wgpu.
2. **tmux control mode is a must-have.** Phases 6 (tabs/splits) and 7 (control mode) are committed. The terminal state is built from the start so that a single window can hold many `term.Terminal` instances.
3. **Ligatures are optional.** v1 uses `golang.org/x/image/font/opentype` with one glyph per cell and no shaping. HarfBuzz-style shaping (`go-text/typesetting`) can be added later behind the `font` package interface.
4. **XWayland is fine to start.** We use GLFW 3.3 on X11 and can switch to GLFW 3.4 for native Wayland later.

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
