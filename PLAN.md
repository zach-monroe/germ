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
| **2. Renderer** | Font loading, glyph atlas, instanced quads, cursor, dirty-row rendering | A usable daily terminal at 60+ fps |
| **3. Input & UX** | Full key encoding, mouse reporting, selection, clipboard, scrollback, URL detection | Comfortable for daily use |
| **4. Config & themes** | TOML config, bundled themes, hot reload, OSC color queries | Changing the theme file recolors the window live |
| **4.5 fish integration** | Auto-injected fish script, OSC 133/7 parsing, prompt jumping, output selection, same-directory spawn, kitty keyboard protocol | Fresh fish (no config changes) → Ctrl+Shift+↑ jumps between prompts |
| **5. tmux Level 1** | terminfo, true color through tmux, OSC 52, CSI u, sync output | `tmux` + `nvim` looks and behaves perfectly |
| **6. Perf pass + tabs/splits** | Benchmarks, profiling, GC tuning; native tabs and splits | Competitive with Alacritty and kitty on vtebench |
| **7. tmux Level 2** | Control mode client | `germ --tmux` attaches with native tabs and splits |

### Testing approach
- `internal/vt` and `internal/term` are pure Go with no GL, so we can feed byte streams in and assert on the grid. These tests are the safety net.
- Conformance: `vttest` and esctest.
- Golden tests: record real sessions (`script`) and snapshot the resulting grid.

---

## Decisions (2026-11-22)
1. **Linux only.** We use OpenGL 3.3 core and don't need to plan for Metal or wgpu.
2. **tmux control mode is a must-have.** Phases 6 (tabs/splits) and 7 (control mode) are committed. The terminal state is built from the start so that a single window can hold many `term.Terminal` instances.
3. **Ligatures are optional.** v1 uses `golang.org/x/image/font/opentype` with one glyph per cell and no shaping. HarfBuzz-style shaping (`go-text/typesetting`) can be added later behind the `font` package interface.
4. **XWayland is fine to start.** We use GLFW 3.3 on X11 and can switch to GLFW 3.4 for native Wayland later.

## Status / known gaps (updated after Phase 1)
- Phase 1 baseline throughput (i3-4030U @ 1.9 GHz): plain text ~82 MB/s, color-heavy ~32 MB/s, 0 allocs once scrollback is full. Parser alone: ~130 MB/s.
- TODO: combining characters (zero-width runes are currently dropped).
- TODO: the grid scrolls by shifting row slices; switch to a ring-buffer grid in the Phase 6 perf pass.
- TODO: DECRQSS / XTGETTCAP (Phase 5), OSC colors (Phase 4), OSC 7/133 (Phase 4.5), kitty keyboard protocol (Phase 3).
- Debug tool: `go run ./cmd/germ-vtdump -- <cmd>` renders a program's screen headlessly.
