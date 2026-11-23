# Coworking: sharing a session

Share your germ session with someone by sending them a link. They see your
tabs and splits in their own germ (with their own font, theme, scrollback
and selection) and, if you let them, type alongside you. It's built for
pair programming and for watching or steering coding agents together.

## Share

In germ's tmux mode (the default), press **Ctrl+Shift+P**. A pane opens
with two links:

- **Can type**: the guest can type in your terminal and open tabs and
  splits, which is like handing them your keyboard. Only give it to people
  you trust.
- **Watch only**: the guest sees everything live but can't change anything.
  This one is copied to your clipboard.

The tab bar shows `◉ sharing · alice ✎, bob (watching)` while people are
connected (✎: typing right now). The share pane lists who's here, numbered,
and logs joins and leaves. In the share pane:

| Key | |
|---|---|
| `c` / `w` | copy the can-type / watch-only link |
| `1`-`9` | remove that person (and optionally make new links so they can't rejoin) |
| `r` | make new links: the old ones stop working, people already here stay |
| `q` (or Ctrl+C) | stop sharing and disconnect everyone |

If your connection to the relay drops, `germ share` reconnects by itself,
and guests reconnect too.

You can also run `germ share` in any pane of a germ tmux session, or
`germ share -session NAME` from anywhere.

## Join

Open the link in a browser: the session appears right there (see
"Joining in a browser" below). To use the germ app instead, click **Open in
germ**, or run:

```sh
germ join 'https://relay.example.com/j/ROOM#KEY'
```

A germ window opens on the shared session, with `⇄ zach, alice ✎` in the
tab bar. Your home tab stays yours. Closing the window leaves the session.

**Following**: you start out following the host: when they switch tabs, so
do you. Switch tabs yourself to look around on your own (the host's view
doesn't move); **Ctrl+Shift+F** follows them again. Typing goes to the pane
you're looking at.

**Dropped connection**: if your network blips (or the host's does), germ
shows `⇄ reconnecting…` and picks up where it left off, for up to 90
seconds.

Guests without germ can install it with one command (shown on the invite
page): `curl -fsSL https://raw.githubusercontent.com/zach-monroe/germ/main/install.sh | sh`,
or `yay -S germ-bin` / `brew install zach-monroe/germ/germ`.

Run `germ setup` once so `germ://` links (the **Open in germ** button)
launch germ. `germ share` and `germ join` also do this automatically.

## How guests reach you (free, no accounts)

`germ share` serves the session from your own machine. It tries, in order:

1. **Direct**: it asks your router to forward a port to this machine
   (UPnP) and gives an `http://your-ip:port/...` link. Nothing in between.
2. **Cloudflare quick tunnel**: if the router won't (UPnP is off, or your
   ISP shares one public address between customers), it starts Cloudflare's
   free `cloudflared` (downloaded once into `~/.cache/germ/bin`; no
   account) and gives an `https://...trycloudflare.com/...` link that works
   from anywhere.

Either way it's free and there's nothing to sign up for. The link works
while `germ share` runs; stopping it (or your laptop sleeping) ends it.

```toml
[share]
mode = "auto"              # "direct", "tunnel", or "auto" (direct, else tunnel)
# port = 7681              # fixed port (default: any free one)
# public_address = "me.example.com"   # you forward the port yourself; skips UPnP
```

Notes:
- Many ISP routers ship with UPnP off. Turn it on in the router's settings
  for direct links, forward a port yourself (set `port` and
  `public_address`), or just let germ use the tunnel.
- Direct links are plain `http`, since there's no certificate for a home IP.
  Everything you type and see is still end-to-end encrypted in the page.
  But someone on the network path could tamper with the page itself as it
  loads, which tunnel links (`https`) rule out. Prefer the tunnel on
  networks you don't trust (`mode = "tunnel"`).
- Cloudflare offers quick tunnels for testing and development, without an
  uptime guarantee.
- germ's built-in relay only serves your one share: nobody can use your
  machine to relay anything else.

## Joining in a browser

Opening a link shows the session right in the browser (xterm.js), with no
install. Guests enter a name and they're in: your tabs as tmux draws them,
live. Can-type links type; watch-only links can't (their keystrokes are
dropped on your machine, and tmux also runs their view read-only).
Guests with germ can click **Open in germ** for native tabs and splits.

## Your own relay (optional)

Teams who want a stable address can still run `germ-relay` somewhere and
set `[share] relay = "https://..."`; then germ share uses it instead of
serving from your machine. It pairs connections and forwards encrypted
bytes it can't read; no accounts, nothing on disk.

- **Anywhere**: `CGO_ENABLED=0 go build -o germ-relay ./cmd/germ-relay`,
  or the Dockerfile in `deploy/relay`, behind TLS (Caddy, nginx, ...).

## Security

- **End-to-end encrypted.** Each share makes two random 256-bit keys, one
  for each link. They live only in the link's `#fragment`, which browsers
  never send to servers, so a relay or tunnel sees who connects and how
  much traffic flows, but not a single keystroke or byte of output.
  Messages are sealed with NaCl secretbox (XSalsa20-Poly1305), which the
  browser viewer runs in plain JavaScript (tweetnacl), so it works on
  plain-http direct links too.
- **The link is the password.** Anyone with a link can join until you stop
  sharing. Each `germ share` makes new links, and old ones stop working.
- **Watch-only is enforced on your machine**, not trusted to the guest's
  germ. Guests drive tmux by sending it commands, and your `germ share`
  checks every command against an allowlist that only reads the session.
  (tmux's own read-only mode only blocks typing; it would still let a
  guest open windows or kill the server.) Format strings that would run
  shell commands (`#(...)`) are refused too.
- **"Can type" guests** can do anything you could do from your keyboard
  in that session, but germ still refuses commands that would end or
  hijack it (`kill-server`, `kill-session`, `attach`, `source-file`, ...).
- Each guest gets their own tmux session grouped with yours (same windows,
  own current window), so they can look around without moving your view.
  Window switches are always qualified with the guest's own session, and
  watchers may only switch windows in theirs. Guest sessions are removed
  when guests leave (and swept up if `germ share` died).

## How it works

```
 your germ ─┐                               ┌─ guest's germ
            │ tmux control mode             │ tmux controller
   tmux ◄── germ share ══ wss ══ relay ══ wss ══ germ join
            │ one `tmux -C attach` per guest │
            └─ filters guest commands        └─ native tabs/splits
```

`germ share` creates a session grouped with yours for each guest and runs
`tmux -C attach-session` on it (watchers with `-f read-only,ignore-size`,
so their window size doesn't change yours), and tunnels it, so the guest's germ runs the same tmux control-mode client as
your own window. Code: `internal/share` (relay, host, guest, protocol) and
`cmd/germ-relay`.
