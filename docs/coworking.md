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

The tab bar shows `◉ sharing · alice, bob (watching)` while people are
connected. The share pane logs joins and leaves. **Ctrl+C in the share
pane stops sharing** and disconnects everyone.

You can also run `germ share` in any pane of a germ tmux session, or
`germ share -session NAME` from anywhere.

## Join

Open the link in a browser. The page shows an **Open in germ** button and
the command to run if the button doesn't work:

```sh
germ join 'https://relay.example.com/j/ROOM#KEY'
```

A germ window opens on the shared session, with `⇄ alice, bob` in the tab
bar. Your home tab stays yours. Closing the window leaves the session.

Run `germ setup` once so `germ://` links (the **Open in germ** button)
launch germ. `germ share` and `germ join` also do this automatically.

## The relay

Two machines behind home or office routers usually can't connect to each
other directly, so germ meets in the middle at a small relay you host. The
relay only pairs connections and forwards encrypted bytes. It has no
accounts, keeps nothing on disk, and can't read anything.

### Deploy it on Fly.io

From the repository:

```sh
fly launch --copy-config --config deploy/relay/fly.toml \
           --dockerfile deploy/relay/Dockerfile --no-deploy   # pick an app name
fly deploy --config deploy/relay/fly.toml --dockerfile deploy/relay/Dockerfile
```

It sleeps when nobody is sharing, so it fits easily in the free allowance.

### Or anywhere else

`germ-relay` is a single static binary (no cgo):

```sh
CGO_ENABLED=0 go build -o germ-relay ./cmd/germ-relay
./germ-relay -addr :8080      # put it behind TLS: Caddy, nginx, a tunnel...
```

There's also a Dockerfile in `deploy/relay`. On a LAN you can skip TLS and
use `http://host:8080` directly.

### Point germ at it

```toml
[share]
relay = "https://your-relay.fly.dev"
```

Guests don't need this setting. The link carries the relay's address.

## Security

- **End-to-end encrypted.** Each share makes two random 256-bit keys, one
  for each link. They live only in the link's `#fragment`, which browsers
  never send to servers, so the relay (and its host) see who connects and
  how much traffic flows, but not a single keystroke or byte of output.
  Messages are sealed with AES-256-GCM.
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
- Guests share your tmux session's current window. When you switch tabs,
  they follow.

## How it works

```
 your germ ─┐                               ┌─ guest's germ
            │ tmux control mode             │ tmux controller
   tmux ◄── germ share ══ wss ══ relay ══ wss ══ germ join
            │ one `tmux -C attach` per guest │
            └─ filters guest commands        └─ native tabs/splits
```

`germ share` runs `tmux -C attach-session` for each guest (watchers with
`-f read-only,ignore-size`, so their window size doesn't change yours) and
tunnels it, so the guest's germ runs the same tmux control-mode client as
your own window. Code: `internal/share` (relay, host, guest, protocol) and
`cmd/germ-relay`.
