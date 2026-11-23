# germ

A fast, GPU-accelerated terminal emulator for Linux, written in Go, with
built-in tmux and fish integration.

- **Fast**: OpenGL 3.3 renderer (glyph atlas, three instanced draw calls per
  frame, only changed rows rebuilt, zero CPU when idle), a zero-allocation
  escape-sequence parser, and synchronized output.
- **tmux, built in**: every germ window is a tmux session, driven in
  control mode, so tmux windows and panes become native germ tabs and
  splits — with germ's own scrollback, selection, and clipboard. Your tmux
  prefix bindings work as usual, and sessions survive closing the window. Inside plain tmux, germ's `xterm-germ` terminfo enables true
  color, styled underlines, clipboard (OSC 52), cursor styles, and more.
- **Coworking**: Ctrl+Shift+P shares your session as a link (can-type or
  watch-only), end-to-end encrypted through a tiny relay you host; guests
  get it as native tabs and splits in their own germ. See
  [docs/coworking.md](docs/coworking.md).
- **Built for coding agents**: tab badges show which agent is working,
  done, or needs you (with desktop notifications), a home-screen widget
  lists every agent, and `germ mcp` lets agents read and drive your other
  panes (dev servers, tests, logs). See [docs/agents.md](docs/agents.md).
- **Home screen**: the first tab is a dashboard of widgets (clock,
  weather, news, stocks), and any program can be a widget, so you can write
  your own in any language. See [docs/widgets.md](docs/widgets.md).
- **fish integration**, zero setup: jump between prompts, copy the last
  command's output, failed-command marks, new tabs/splits in the current
  directory, notifications for long commands, kitty keyboard protocol.
- **Customizable colors**: 9 bundled themes, your own theme files, per-color
  overrides, and live reload when you save the config.
- Tabs and splits without tmux too, selection (word/line), clipboard and
  X11 primary selection, scrollback, Ctrl+click URLs, zoom, true color,
  mouse reporting, and procedurally drawn box-drawing characters.

## Install

Linux (X11 or XWayland), x86_64 or arm64:

```sh
curl -fsSL https://raw.githubusercontent.com/zach-monroe/germ/main/install.sh | sh
```

installs the latest release to `~/.local/bin` (checksum-verified), adds an
app-launcher entry, and registers `germ://` invite links. Or use a package
manager:

```sh
yay -S germ-bin                       # Arch (AUR), prebuilt
brew install zach-monroe/germ/germ    # Homebrew on Linux
```

germ uses `tmux` (tabs, sharing), `fontconfig`, and optionally `xclip` or
`wl-clipboard`, `libnotify`, and `git`.

### From source

Go 1.27+, a C compiler, and the X11/OpenGL headers (Arch:
`libx11 libxrandr libxinerama libxcursor libxi libxxf86vm mesa`; Debian/Ubuntu:
`libgl1-mesa-dev libx11-dev libxrandr-dev libxinerama-dev libxcursor-dev libxi-dev libxxf86vm-dev`):

```sh
go install github.com/zach-monroe/germ/cmd/germ@latest
# or
git clone https://github.com/zach-monroe/germ && cd germ && go build -o germ ./cmd/germ
```

## Usage

```sh
germ                 # a tmux session (germ, germ-2, ...) running your $SHELL
germ -session work   # attach to (or create) tmux session "work"
germ -no-tmux        # a plain shell, no tmux
germ -theme dracula  # override the config for one window
germ htop            # run a program instead of the shell
germ -list-themes
germ widget weather  # run a home-screen widget on its own
germ agent "fix the flaky test"   # agent on a fresh worktree + branch
germ share          # share this tmux session (or Ctrl+Shift+P)
germ join LINK      # join someone's shared session
germ ctl list        # script the window: list/read/send/open/wait panes
germ mcp             # MCP server for coding agents (see docs/agents.md)
```

## Configuration

`~/.config/germ/config.toml` — see [`config.example.toml`](config.example.toml)
for every option. Changes to the theme, colors, font, padding, and key
bindings apply as soon as you save.

```toml
theme = "catppuccin-mocha"

[font]
family = "JetBrainsMono Nerd Font"
size = 10

[tmux]
enabled = false   # plain shells by default instead of tmux
```

## Default key bindings

| Keys | Action |
|---|---|
| Ctrl+Shift+C / Ctrl+Shift+V, Shift+Insert | copy / paste |
| Ctrl+Shift+= / - / 0 | zoom in / out / reset |
| Shift+PgUp / PgDn, Shift+Home / End | scroll history |
| Ctrl+Shift+Up / Down | previous / next prompt (fish) |
| Ctrl+Shift+X | copy last command's output (fish) |
| Ctrl+Shift+T, Ctrl+Shift+Q | new tab, close tab |
| Ctrl+Tab, Ctrl+Shift+Tab (or Ctrl+Shift+Left/Right) | next / previous tab |
| Ctrl+Shift+Enter, Ctrl+Shift+D | split right, split down |
| Ctrl+Shift+H/J/K/L | focus pane left/down/up/right |
| Ctrl+Shift+W, Ctrl+Shift+Z | close pane, zoom pane |
| Ctrl+Shift+\` | home tab ↔ last tab |
| Ctrl+Shift+A | new agent on its own git worktree (see docs/agents.md) |
| Ctrl+Shift+S | send the selection (or last output) to your agent pane |
| Ctrl+Shift+P | share this session (invite links) |
| Ctrl+click | open URL, or `file:line` in `$EDITOR` |

Rebind or remove any of these under `[keybindings]`.

## More

- [docs/tmux.md](docs/tmux.md) — tmux setup and control mode
- [docs/widgets.md](docs/widgets.md) — the home screen and writing widgets
- [docs/agents.md](docs/agents.md) — agent attention, `germ mcp`, `germ ctl`
- [docs/coworking.md](docs/coworking.md) — sharing sessions, the relay, security
- [PLAN.md](PLAN.md) — design, architecture, and project status
- `scripts/e2e/` — end-to-end tests on a virtual X server
