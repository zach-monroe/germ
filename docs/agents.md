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
window, most urgent first, with its message ("Do you want to make this
edit to parser.go?"), how long it's been working, where it is, and a
**timeline** of its last 30 minutes, one cell per minute: ▅ working,
▇ waiting for you, ▂ done, ▁ idle. **Click an agent** to jump to its pane,
even in another germ window (which comes to the front).

How germ knows (exactly, after `germ setup agents`; see below — otherwise
by watching):
- **Which panes are agents**: the pane's foreground process is matched
  against `[agents] commands` (for `node`/`python` scripts, the script name
  counts).
- **Working**: the agent's screen shows its working line (Claude Code and
  Codex print "esc to interrupt"), or it's producing output that isn't just
  the echo of your typing, or it sent an OSC 9;4 progress report.
- **Needs you**: a prompt on the agent's screen ("Do you want to make this
  edit…?", "❯ 1. Yes", "(y/n)", ...), or a notification it sent: OSC 9
  (iTerm2), OSC 777 (rxvt/Ghostty), OSC 99 (kitty), or the bell. Reading
  the screen means this works even when the agent isn't set up to send
  notifications. In tmux mode germ sees everything even though tmux itself
  would swallow notifications.

The screen patterns are configurable (matched case-insensitively against
the bottom of the agent's screen):

```toml
[agents]
working_patterns = ["esc to interrupt", "ctrl+c to interrupt", "esc to cancel"]
waiting_patterns = ["do you want to", "would you like to", "❯ 1. yes", "allow this",
                    "(y/n)", "[y/n]", "press enter to continue", "waiting for your"]
```

### Exact state: `germ setup agents`

Out of the box germ guesses from the screen. For exact badges, run once:

```sh
germ setup agents
```

This adds hooks to Claude Code (`~/.claude/settings.json`, or
`$CLAUDE_CONFIG_DIR`) and sets Codex's `notify` program
(`~/.codex/config.toml`). Each calls `germ ctl agent-event`, so the agent
tells germ itself when it starts working, needs your permission (with
what it wants to do: "wants to use Bash"), finishes, or fails. You also get
a **"finished" notification** with the first line of its answer when an
agent you aren't looking at completes (`notify_done = false` turns that
off). Nothing else in either file changes; the original is saved as
`settings.json.germ-bak`. Re-run after moving germ; `germ setup agents
-remove` takes the hooks out; `-n` shows what would change.

Details:
- Reported state wins over screen guessing while that agent process runs.
  Claude Code doesn't report being interrupted (Esc), so "working" lapses
  after 15 seconds without events or output.
- Codex only reports finished turns; its permission prompts are still
  read from the screen. If Codex's `notify` is already set to another
  program, germ leaves it and tells you.
- Any agent can do the same:
  `germ ctl agent-event -agent NAME working|waiting|done|idle|end
  [-message TEXT]`. Outside germ the command does nothing and always
  exits 0.

Without hooks, you can still get Claude's own wording in notifications by
telling it to use the iTerm2 protocol (OSC 9): `claude config set --global
preferredNotifChannel iterm2`.

### Config

```toml
[agents]
commands = ["claude", "codex", "aider", "gemini", "opencode", "goose", "amp",
            "cursor-agent", "crush", "qwen", "copilot"]
notify = true        # desktop notifications when an agent needs you
notify_done = true   # ...and when one finishes (agents set up with germ setup agents)
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

## Review agents: Ctrl+Shift+R

With several agents on their own branches, **Ctrl+Shift+R** (or `germ
review` in the repository) opens one pane listing every agent worktree:
what it was asked to do, whether its agent is working, needs you, or is
done (live, from the badges), its commits, and the files it changed. It
refreshes every two seconds; you don't have to wait for agents to exit.

| Key | |
|---|---|
| ↑ ↓ / j k | select |
| Enter / `r` | review the full diff (including new files) in your git pager |
| `f` | feedback: type a message, and it's sent to that agent (as if typed at its prompt) |
| `o` | go to the agent's pane |
| `c` | commit everything, with the task as the message |
| `m` | merge into the branch it started from (commits uncommitted work and closes the agent's panes first, after asking), then remove the worktree and branch |
| `d` | discard the worktree and branch (asks first) |
| `q` | quit |

Sending feedback needs `[control] allow_input` (on by default).

## Jump to code: Ctrl+click file:line

Hold Ctrl over a file reference in any pane (`internal/app/x.go:45:3`,
`./src/a.ts:12`, `README.md`) and it underlines if the file exists
(relative to the pane's directory). Ctrl+click opens it at that line in
`$VISUAL`/`$EDITOR`: terminal editors (nvim, vim, helix, kakoune, nano,
...) in a split beside the pane, GUI editors (VS Code, Cursor, Zed,
Sublime) in their own window.

## Hints: Ctrl+Shift+E

Agents print paths, links, commit hashes and dev-server addresses all day.
Press **Ctrl+Shift+E** and each one on screen gets a letter (the newest
output gets the easiest keys):

| Type | Letter opens | Shift+letter |
|---|---|---|
| URL, OSC 8 link | in the browser (`$BROWSER`, else xdg-open) | copies |
| `path/to/file.go:42` (existing files) | in `$EDITOR` at the line, like Ctrl+click | copies |
| `localhost:5173`, `127.0.0.1:8080` | `http://…` in the browser | copies |
| git hash, bare IP address | copies | copies |

More than 26 on screen get two-letter labels. Esc or Backspace cancels.

## Typing to agents

- **Shift+Enter inserts a newline** in agent panes. Most terminals send
  Shift+Enter as a plain Enter, which submits the prompt; germ sends
  ESC+Enter (what agents treat as "new line") unless the agent turned on
  the kitty keyboard protocol and can tell Shift+Enter apart itself.
  `[agents] shift_enter_newline = false` turns this off.
- **Paste images**: when the clipboard holds an image (a screenshot, say),
  Ctrl+Shift+V saves it under `~/.cache/germ/paste/` (cleaned after a
  week) and pastes the file path, which Claude Code and other agents
  attach as an image. Uses `xclip` (X11) or `wl-paste` (Wayland).
- **Drop files** onto the window to paste their paths.
- **Ctrl+Shift+S sends to the agent**: the selection (or, with no
  selection, the last command's output in fish) goes to the agent pane you
  used most recently, as a fenced block saying where it came from:

  ````
  selection from `bash` in /home/you/project:
  ```
  FAIL auth_test.go:42 expected 200, got 500
  ```
  ````

  and that pane is focused so you can add your question.

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
