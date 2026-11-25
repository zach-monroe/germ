# germ

**Spread your germs.** Highly contagious coworking.

A fast, GPU-accelerated terminal for Linux, built for coding agents and
for sharing your session with anyone, with a link. Written in Go, with
built-in tmux and fish integration.

- **Fast**: OpenGL 3.3 renderer (glyph atlas, three instanced draw calls per
  frame, only changed rows rebuilt, zero CPU when idle), a zero-allocation
  escape-sequence parser, and synchronized output.
- **tmux, built in**: every germ window is a tmux session, driven in
  control mode, so tmux windows and panes become native germ tabs and
  splits — with germ's own scrollback, selection, and clipboard. Your tmux
  prefix bindings work as usual, and sessions survive closing the window. Inside plain tmux, germ's `xterm-germ` terminfo enables true
  color, styled underlines, clipboard (OSC 52), cursor styles, and more.
- **Coworking (highly contagious)**: Ctrl+Shift+P shares your session as a link,
  end-to-end encrypted, and you approve each person who joins. You run it
  from one panel in your window, which guests never see: the link, who's
  here, the chat, who has the helm.
  Typing is by turns: one person at the helm of each tab; guests ask by
  typing and pressing Enter, and you hand it out and take it back with one
  key, and you can give a guest a tab of their own. Guests open the link in any
  browser, or in germ for native tabs and splits.
  Free: served from your machine over Tailscale Funnel if you use
  Tailscale, else a free Cloudflare tunnel; no router ports opened. See [docs/coworking.md](docs/coworking.md).
- **Built for coding agents**: tab badges show which agent is working,
  done, or needs you (with desktop notifications; exact for Claude Code
  and Codex after `germ setup agents`), a home-screen widget
  lists every agent, and `germ mcp` lets agents read and drive your other
  panes (dev servers, tests, logs). See [docs/agents.md](docs/agents.md).
- **Home screen**: the first tab is a dashboard of widgets (clock,
  weather, news, stocks), and any program can be a widget, so you can write
  your own in any language. See [docs/widgets.md](docs/widgets.md).
- **Shell integration for fish, zsh, and bash**, zero setup: jump between
  prompts, copy the last command's output, failed-command marks, new
  tabs/splits in the current directory, notifications for long commands.
  Works with Oh My Zsh, powerlevel10k, starship, and in tmux panes.
- **Fonts**: any fontconfig font, color emoji, optional programming
  ligatures (`[font] ligatures = true`), and synthesized italic/bold for
  fonts without them.
- **Themes**: 9 bundled, previewed live from the palette (Ctrl+Shift+Space,
  "theme"; Enter keeps one), `germ theme new` to make your own with live
  reload as you edit, and `germ theme import` for iTerm2, base16 and
  Alacritty schemes. See [docs/themes.md](docs/themes.md).
- Tabs and splits without tmux too, selection (word/line), clipboard and
  X11 primary selection, scrollback, Ctrl+click URLs and OSC 8 hyperlinks,
  drag separators to resize panes, combining characters, zoom, true color,
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

Then run `germ doctor` to check fonts, tmux, shell integration, agent
hooks and sharing, with a fix for anything missing. Config mistakes show up
in the tab bar (and as a notification) when germ starts or reloads, and if
germ ever crashes, the next launch tells you where the log is
(`~/.local/state/germ/crash.log`).

## Usage

```sh
germ                 # a tmux session (germ, germ-2, ...) running your $SHELL
germ -session work   # attach to (or create) tmux session "work"
germ -no-tmux        # a plain shell, no tmux
germ -theme dracula  # override the config for one window
germ htop            # run a program instead of the shell
germ theme list     # every theme; germ theme use NAME picks one
germ theme new mine  # your own, from the current theme (edit it, it reloads live)
germ theme import ~/Downloads/Ocean.itermcolors  # or base16 YAML, Alacritty TOML
germ widget weather  # run a home-screen widget on its own
germ agent "fix the flaky test"   # agent on a fresh worktree + branch
germ share          # share this tmux session (or Ctrl+Shift+P)
germ join LINK      # join someone's shared session
germ helm status    # who has the helm; germ helm give alice / take all
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
| Ctrl+Shift+Up / Down | previous / next prompt |
| Ctrl+Shift+X | copy last command's output |
| Ctrl+Shift+T, Ctrl+Shift+Q | new tab, close tab |
| Ctrl+Tab, Ctrl+Shift+Tab (or Ctrl+Shift+Left/Right) | next / previous tab |
| Ctrl+Shift+Enter, Ctrl+Shift+D | split right, split down |
| Ctrl+Shift+H/J/K/L | focus pane left/down/up/right |
| Ctrl+Shift+W, Ctrl+Shift+Z | close pane, zoom pane |
| Ctrl+Shift+\` | home tab ↔ last tab |
| Ctrl+Shift+A | new agent on its own git worktree (see docs/agents.md) |
| Ctrl+Shift+S | send the selection (or last output) to your agent pane |
| Ctrl+Shift+P | share this session (invite link) |
| Ctrl+Shift+M | the session panel: link, people, helm and chat (host), or the chat (guest); Esc back to the terminal, again to close |
| Ctrl+Shift+O | the helm: take it back (host), or ask for it / hand it back (guest); Enter in a tab you can't type in does the same |
| Ctrl+Shift+F | search the pane's scrollback (Enter older, Shift+Enter newer, Esc) |
| Ctrl+Shift+Space | command palette: every action and its key, filtered as you type; also open the config, germ doctor, theme previews |
| Ctrl+Shift+R | review agents: every agent worktree with its state and diff; review, send feedback, merge or discard |
| Ctrl+Shift+E | hints: label every URL, `file:line`, git hash and address on screen; a letter opens it, Shift+letter copies |
| Ctrl+Shift+G | as a guest: follow the host's tab again |
| Ctrl+click | open URL or hyperlink (`$BROWSER`, else xdg-open), or `file:line` in `$EDITOR` |
| drag a separator | resize panes |

Rebind or remove any of these under `[keybindings]`.

## More

- [docs/tmux.md](docs/tmux.md) — tmux setup and control mode
- [docs/widgets.md](docs/widgets.md) — the home screen and writing widgets
- [docs/agents.md](docs/agents.md) — agent attention, `germ mcp`, `germ ctl`
- [docs/coworking.md](docs/coworking.md) — sharing sessions, the relay, security
- [docs/themes.md](docs/themes.md) — picking, making and importing themes
- [PLAN.md](PLAN.md) — design, architecture, and project status
- `scripts/e2e/` — end-to-end tests on a virtual X server
