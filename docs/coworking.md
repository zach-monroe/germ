# Coworking: sharing a session

Share your germ session with someone by sending them a link. They see your
tabs and splits in their own germ (with their own font, theme, scrollback
and selection), chat with you, and type when you hand them the helm. It's built for
pair programming and for watching or steering coding agents together.

## Share

In germ's tmux mode (the default), press **Ctrl+Shift+P**. The share pane
opens on the right with your invite link, already on your clipboard.
Anyone with it can ask to join; you let each person in, everyone can
watch and chat, and typing is by turns (see "The helm" below).

The share pane is where you run the session, all in one place:

```
◆ contagious · session "work" through Tailscale Funnel
https://box.tail1234.ts.net/j/ROOM#KEY
here: 1 alice ✎  2 bob (browser)
⎈ 0 zsh: alice · 1 server: you · 2 tests: bob (bob's tab)
──────────────────────────────────────────
· alice joined
alice · 14:02
  pushed the fix
» bob asks for the helm of tab 1 (server)  y yes · n no
──────────────────────────────────────────
› message · /help for commands
```

- **Who's here**, numbered, with ✎ on whoever is typing in the terminal.
- **Who has the helm** of each tab. The underlined tab is the one you're
  on: commands that take a tab act on it unless you give a number.
- **The chat and the session's log**: joins, leaves, who got the helm.
- **Questions to answer**: someone wants to join, or wants the helm.
  **y** / **n** answers, with the message box empty.
- **The message box**: type and press **Enter** to chat, or type a command:

| Command | |
|---|---|
| `/copy` | copy the invite link |
| `/newlink` | make a new link: the old one stops working, people already here stay |
| `/helm NAME [TAB]` | hand NAME the helm |
| `/take [TAB\|all]` | take the helm back (no TAB: the tab you're on if a guest has it, else every tab a guest has) |
| `/give NAME [TAB]` | make it NAME's tab (see "Giving someone a tab") |
| `/takeback [TAB]` | take a given tab back |
| `/branch NAME [task]` | a tab on a new branch for NAME |
| `/kick NAME` | remove someone (`/newlink` too, so they can't come back) |
| `/mute NAME`, `/unmute NAME` | mute someone in chat |
| `/save` | save the chat to a file |
| `/yes`, `/no` | answer the question shown |
| `/stop` (or Ctrl+C) | stop sharing and disconnect everyone |

NAME is a name or its number from the list; TAB is a tab's number.
**Up** brings back what you sent, **PgUp/PgDn** scroll.

**Ctrl+Shift+M** takes you to the share pane from anywhere in the window
(Ctrl+Shift+P too, once you're sharing). The tab bar shows
`◉ contagious · alice ✎, bob`, and new messages and questions appear
there too (`» bob wants to join · Ctrl+Shift+M`), with a desktop
notification if you're looking elsewhere.

Guests see the share pane (it's part of your session), but never type
into it, even at the helm of its tab: their typing goes to the tab's other
pane.

If your connection to the relay drops, `germ share` reconnects by itself,
and guests reconnect too.

You can also run `germ share` in any pane of a germ tmux session, or
`germ share -session NAME` from anywhere.

## The chat

Everyone in the session is in one channel, guests in a browser included,
and sees the same messages in the same order, each with who sent it and
when. You chat in the share pane; guests with germ chat in a panel on
the right of their window (**Ctrl+Shift+M**), and browser guests beside
the terminal.

- In the guest's panel, your keys go to the message box, not the terminal
  (its cursor turns hollow). **Enter** sends, **Shift+Enter** starts a new
  line, **Up** brings back what you sent, **PgUp/PgDn** scroll. **Esc**
  goes back to the terminal with the panel still open; Ctrl+Shift+M again
  closes it.
- Closed, new messages show in the tab bar (`alice: pushed the fix`, then
  `2 new`).
- `@alice` or `@here` highlights the message for them and sends a desktop
  notification.
- Joins, leaves and who got the helm show as dim one-line notices, so the
  chat doubles as a log of who drove when.
- Nothing is saved unless you ask: `/save` in the share pane, or the
  palette. Chats go to `~/.local/share/germ/chats/`.
- Messages are limited to 4 KB and a few per second. To send a message
  that starts with `/`, start it with `//`.

## The helm

One person types in a tab at a time: whoever has its **helm**. At first
that's you, the host, in every tab.

- **A guest just starts typing.** Their keys don't go through, and germ
  says `⎈ zach has the helm here · Enter asks for it`; **Enter** asks.
  (Or **Ctrl+Shift+O**, `/helm` in the chat, **Request the helm** in the
  browser, or `germ helm` in a shell.) You get a notification and the
  question in the share pane: **y** hands it over, **n** says no.
- **You can hand it over unasked**: `/helm alice` in the share pane, or
  `germ helm give alice`.
- **While a guest has the helm**, only their keystrokes reach that tab,
  and yours don't: germ says `⎈ alice has the helm here · Enter takes it
  back`. The tab bar marks the tab `⎈alice`.
- **Taking it back is one key**: **Enter** in that tab, **Ctrl+Shift+O**
  anywhere in it, or `/take` (`/take all` for every tab). It's yours: a
  guest can't refuse or hold on to it.
- A guest hands it back with Ctrl+Shift+O, `/helm` in the chat, or
  **Hand back the helm**. germ tells them when they get it, and when it
  goes.

Without the helm, a guest can still look at every tab, scroll, select
and copy, and chat.

### From a shell: germ helm

```sh
germ helm                   # ask for it or hand it back (guests); take it back (host)
germ helm status            # who has the helm of each tab
germ helm give alice [TAB]  # hand alice the helm (host)
germ helm take [TAB|all]    # take it back (host)
germ helm ask / back [TAB]  # guests
```

It acts on the tab the shell is in (or the window's current tab), in the
germ window it runs in: handy in scripts, and for coding agents.

### Giving someone a tab

Say alice is building a feature: give her its tab, and she can work there
while you work in yours.

- `/give alice` in the share pane (the tab you're on, or `/give alice 2`),
  or **Ctrl+Shift+Space** (the palette) → **Give this tab to alice**.
- alice owns the tab and has its helm. Others ask *her* for it, not you.
- `/takeback` (or **Take this tab back** in the palette) returns it to
  you. Ctrl+Shift+O still takes the helm at any moment.
- If alice's connection drops, the tab waits for her (she gets it back
  when she reconnects). If she's gone for good, it comes back to you after
  90 seconds.

A tab a guest opens while they have the helm starts out with them at its
helm.

### A branch of their own

For real work, give them a branch too, the way `germ agent` gives one to
a coding agent:

- `/branch alice fix the login page` in the share pane (the branch starts
  from the directory of the tab you're on), or **Ctrl+Shift+Space** →
  **New tab for alice on its own branch** from a pane in your project.
- germ makes a git worktree on a new branch (`germ/alice-fix-the-login-page`),
  opens a tab there with a shell and a live diff beside it, and gives the
  tab to alice. Her changes stay on her branch; your checkout is untouched.
- When she's done, **germ review** (Ctrl+Shift+R) lists her branch as
  `alice: fix the login page`, with its commits and diff: merge it, or
  discard it. Merging stays with you.

## Join

Open the link in a browser: the session appears right there (see
"Joining in a browser" below). To use the germ app instead, click **Open in
germ**, or run:

```sh
germ join 'https://relay.example.com/j/ROOM#KEY'
```

A germ window opens on the shared session, with `⇄ zach, alice ✎` in the
tab bar. Closing the window leaves the session. **Ctrl+Shift+M** chats;
to type, start typing and press **Enter** to ask for the helm (or
**Ctrl+Shift+O**).

**Following**: you start out following the host: when they switch tabs, so
do you. Switch tabs yourself to look around on your own (the host's view
doesn't move); **Ctrl+Shift+G** follows them again.

**Dropped connection**: if your network blips (or the host's does), germ
shows `⇄ reconnecting…` and picks up where it left off, for up to 90
seconds, helm and tabs included.

Guests without germ can install it with one command (shown on the invite
page): `curl -fsSL https://raw.githubusercontent.com/zach-monroe/germ/main/install.sh | sh`,
or `brew install zach-monroe/germ/germ`.

Run `germ setup` once so `germ://` links (the **Open in germ** button)
launch germ. `germ share` and `germ join` also do this automatically.

## How guests reach you (free, no accounts needed)

`germ share` serves the session from your own machine and gives it a
public https address. It never opens a port on your router unless you ask.

1. **Tailscale Funnel**, if you use Tailscale and have Funnel enabled:
   links like `https://yourmachine.your-tailnet.ts.net/...`. The
   certificate belongs to your machine, so the connection is encrypted all
   the way to you: neither Tailscale nor anyone else in between can read
   or alter what guests load. Guests don't need Tailscale.
2. **Cloudflare quick tunnel** otherwise: `https://...trycloudflare.com/...`.
   No account; germ downloads Cloudflare's `cloudflared` once into
   `~/.cache/germ/bin`. HTTPS ends at Cloudflare, so Cloudflare is the one
   party that could in principle tamper with the page; your session itself
   stays end-to-end encrypted either way.

The link works while `germ share` runs; stopping it (or your laptop
sleeping) ends it.

### Setting up Tailscale Funnel (once)

```sh
sudo systemctl enable --now tailscaled
sudo tailscale up                       # log in (free personal plan)
sudo tailscale set --operator=$USER     # let germ run `tailscale funnel` without sudo
```

Then allow Funnel for your tailnet in the admin console (Access controls,
the `funnel` node attribute; see https://tailscale.com/kb/1223/funnel).
`germ share` tells you what's missing if any of this isn't done, and uses
the Cloudflare tunnel meanwhile.

### Options

```toml
[share]
mode = "auto"      # auto (Tailscale Funnel if set up, else Cloudflare), tailscale, tunnel
approve = "all"    # approve each person who joins: all (default) or none
```

`mode = "direct"` also exists: a UPnP port forward on your router and a
plain-http link to your public IP (`port` / `public_address` for a manual
forward). It's never used automatically, since it exposes your address and
the page isn't https.

## Letting people in

By default you approve every guest. When someone opens your link, germ
sends a desktop notification and asks in the share pane:
`» alex (browser) wants to join  y yes · n no`.

Their browser says "Waiting for the host to let you in…". So a link that
ends up somewhere it shouldn't is useless without your OK. If a guest's
connection drops and comes back, they get straight back in without being
asked again. Unanswered requests are turned away after two minutes, and
the relay limits how often a link can be tried.

## Joining in a browser

Opening a link shows the session right in the browser (xterm.js), with no
install. Guests enter a name and they're in: your tabs as tmux draws them,
live, with the chat on the right. A strip of tabs above the terminal lets
them look at any tab (or **Follow the host**), and **Request the helm**
asks to type in the one they're looking at. Guests with germ can click
**Open in germ** for native tabs and splits.

## Your own relay (optional)

Teams who want a stable address can still run `germ-relay` somewhere and
set `[share] relay = "https://..."`; then germ share uses it instead of
serving from your machine. It pairs connections and forwards encrypted
bytes it can't read; no accounts, nothing on disk.

- **Anywhere**: `CGO_ENABLED=0 go build -o germ-relay ./cmd/germ-relay`,
  or the Dockerfile in `deploy/relay`, behind TLS (Caddy, nginx, ...).

## Security

- **End-to-end encrypted.** Each share makes a random 256-bit key. It
  lives only in the link's `#fragment`, which browsers never send to
  servers, so a relay or tunnel sees who connects and how much traffic
  flows, but not a single keystroke, chat message or byte of output.
  Messages are sealed with NaCl secretbox (XSalsa20-Poly1305), which the
  browser viewer runs in plain JavaScript (tweetnacl), so it works on
  plain-http direct links too.
- **The link is the key, and you're the door.** A link carries the key,
  but by default you approve each person who uses it. Each `germ share`
  makes a new link, and old ones stop working (`/newlink` in the share
  pane makes a new one mid-session).
- **Your machine only serves your share.** germ's built-in relay accepts
  one session, and only `germ share` (holding a secret token) can be its
  host. Joins are rate-limited.
- **The helm is enforced on your machine**, not trusted to the guest's
  germ. Guests drive tmux by sending it commands, and `germ share` checks
  every one against an allowlist: commands that only look are allowed;
  commands that type into or change a tab only for whoever has its helm;
  everything else never (tmux's key bindings and command prompt
  included, and format strings that would run shell commands, `#(...)`).
  Browser guests' keystrokes are typed into the pane by `germ share`
  itself (`send-keys`), never through tmux's key tables. No guest types
  into the share pane, helm or not.
- **Nobody can speak for someone else.** Chat messages go through your
  `germ share`, which stamps who sent each one by the connection it came
  on, and numbers them, so everyone sees the same order.
- Each guest gets their own tmux session grouped with yours (same windows,
  own current window), so they can look around without moving your view,
  and never resizes your windows. Guest sessions are removed when guests
  leave (and swept up if `germ share` died).

## How it works

```
 your germ ─┐                               ┌─ guest's germ
            │ tmux control mode             │ tmux controller
   tmux ◄── germ share ══ wss ══ relay ══ wss ══ germ join
            │ one `tmux -C attach` per guest │
            └─ filters guest commands        └─ native tabs/splits
```

`germ share` creates a session grouped with yours for each guest and runs
`tmux -C attach-session -f ignore-size` on it (so their window size doesn't
change yours), and tunnels it, so the guest's germ runs the same tmux
control-mode client as your own window. The chat, the tabs and the helm
travel in the same encrypted tunnel; your germ window gets them from
`germ share` over its control socket (for the tab bar, the helm, and
Ctrl+Shift+O). Code: `internal/share` (relay, host, guest, protocol,
`room.go` for the chat and the helm), `cmd/germ-relay`,
`cmd/germ/sharepane.go` (the share pane), `internal/app/chat.go` (the
guest's panel) and `internal/chatview` (drawing the chat, for both).
