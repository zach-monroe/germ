# Coding agents in germ

germ is built for running coding agents (Claude Code, Codex, aider,
Gemini CLI, opencode, ...) side by side.

## Agent attention

germ notices which panes are running an agent and shows what each is doing
in the tab bar:

| Badge | Meaning |
|---|---|
| ⠋ (spinning) | working: producing output |
| ● | needs you: the agent sent a notification or rang the bell |
| ✓ | done: it stopped working since you last looked |

Badges clear when you look at the tab. When an agent needs you and the
pane isn't on screen, germ also sends a desktop notification (via
`notify-send`) with the agent's message and marks the window urgent.

The **agents** widget on the home tab lists every agent in every germ
window, most urgent first, with its message ("Claude needs your
permission to use Bash"), how long it's been working, and where it is.

How germ knows:
- **Which panes are agents**: the pane's foreground process is matched
  against `[agents] commands` (for `node`/`python` scripts, the script name
  counts).
- **Needs you**: notifications agents already send: OSC 9 (iTerm2),
  OSC 777 (rxvt/Ghostty), OSC 99 (kitty), and the bell. In tmux mode germ
  sees these even though tmux itself would swallow them.
- **Working**: recent output that isn't just the echo of your typing, or an
  OSC 9;4 progress report.

### Claude Code setup

Claude Code only sends notifications to terminals it recognizes. Tell it
to use the iTerm2 protocol (OSC 9), which germ understands:

```sh
claude config set --global preferredNotifChannel iterm2
```

(`terminal_bell` works too, but carries no message.)

### Config

```toml
[agents]
commands = ["claude", "codex", "aider", "gemini", "opencode", "goose", "amp",
            "cursor-agent", "crush", "qwen", "copilot"]
notify = true   # desktop notifications
```

## Parallel agents: Ctrl+Shift+A

Ctrl+Shift+A (or `germ agent [TASK]` in any pane) starts an agent on its
own git worktree, so several can work on the same repository at once
without stepping on each other or on you:

1. it asks what the agent should do;
2. creates a worktree on a new branch, `germ/<task-slug>`, in
   `<repo>.worktrees/` next to your repository;
3. names the tab after the task and opens a live **changes** pane beside
   the agent: files touched with +/- bars, and commits, updated every two
   seconds;
4. runs the agent with the task (`claude "<task>"` by default).

When the agent exits you get a menu:

| Key | |
|---|---|
| `r` | review the diff (including new files) in your pager |
| `c` | commit everything, with the task as the message |
| `m` | merge the branch into the branch you started from (`--no-ff`), then remove the worktree and branch |
| `a` | resume the agent (`claude --continue`) |
| `s` | open a shell in the worktree; exit it to come back |
| `k` | keep the worktree and branch, close the tab |
| `d` | discard: delete the worktree and branch |

Outside a git repository the agent just runs in place.

```toml
[agents]
launch = ["claude", "{task}"]          # or ["codex", "{task}"], ["aider", "--message", "{task}"]
resume = ["claude", "--continue"]      # used when launch starts with the same program
# worktrees = "~/worktrees/{repo}"     # default: {repo}.worktrees beside the repo
```

## Jump to code: Ctrl+click file:line

Hold Ctrl over a file reference in any pane (`internal/app/x.go:45:3`,
`./src/a.ts:12`, `README.md`) and it underlines if the file exists
(relative to the pane's directory). Ctrl+click opens it at that line in
`$VISUAL`/`$EDITOR`: terminal editors (nvim, vim, helix, kakoune, nano,
...) in a split beside the pane, GUI editors (VS Code, Cursor, Zed,
Sublime) in their own window.

## germ as a tool for agents (MCP)

`germ mcp` is an [MCP](https://modelcontextprotocol.io) server that lets an
agent see and drive the germ window it runs in:

| Tool | What it does |
|---|---|
| `list_panes` | panes with their command, directory, size, agent state; marks the agent's own pane |
| `read_pane` | the last N lines of a pane (screen + scrollback) as text |
| `send_text` | type into a pane (Enter after, unless `enter: false`) |
| `open_pane` | new tab or split, optionally running a command; doesn't steal focus |
| `wait_for` | wait until a pane's output matches a regex, or it goes quiet |

So an agent can read the dev server's errors from the pane next to it,
start a test watcher in a new split and wait for it to finish, or check
on another agent.

Register it once:

```sh
# Claude Code
claude mcp add --scope user germ -- germ mcp

# Codex (~/.codex/config.toml)
[mcp_servers.germ]
command = "germ"
args = ["mcp"]
```

(`germ` must be on your `PATH`, e.g. `go install ./cmd/germ`, or use the
full path.)

## germ ctl

The same operations from a shell or script:

```sh
germ ctl list
germ ctl read 3 -n 100
germ ctl send %2 'npm test'
germ ctl open -where right -- npm run dev
germ ctl wait %4 -match 'ready|error' -timeout 2m
germ ctl focus self
```

`PANE` is a germ pane ID from `germ ctl list`, a tmux pane ID like `%3`,
or `self`.

## How it works

Each germ window listens on a Unix socket,
`$XDG_RUNTIME_DIR/germ/<pid>.sock` (mode 0600, so only you can connect),
and exports it to its panes as `GERM_SOCKET`. The protocol is one JSON
request per line (see `internal/control`). `germ ctl` and `germ mcp` find
the window that owns their pane (`GERM_PANE_ID`, or `TMUX_PANE` in tmux
mode), falling back to the newest window.

Reading is always allowed. Set `[control] allow_input = false` to stop
clients from typing into panes or opening new ones, or
`[control] enabled = false` to turn the socket off.
