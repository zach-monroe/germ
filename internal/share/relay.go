package share

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The relay pairs one host with its guests and forwards opaque, encrypted
// messages between them. It never sees keys or content.
//
// Host connection (/ws/host/ROOM), binary messages:
//
//	[kind 1][guest id uint32 BE][payload]
//	relay -> host: kindData (from guest), kindJoin, kindLeave
//	host -> relay: kindData (to guest; id 0 = all guests), kindKick
//
// Guest connection (/ws/guest/ROOM): each binary message is a payload.

const (
	kindData  byte = 1
	kindJoin  byte = 2
	kindLeave byte = 3
	kindKick  byte = 4

	maxMessage   = 1 << 20
	maxGuests    = 16
	maxRooms     = 1000
	pingInterval = 25 * time.Second
)

var roomRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// Relay is the relay server.
type Relay struct {
	mu    sync.Mutex
	rooms map[string]*room
	Log   *log.Logger
}

type room struct {
	host   *websocket.Conn
	guests map[uint32]*websocket.Conn
	next   uint32
}

// NewRelay makes a relay.
func NewRelay() *Relay { return &Relay{rooms: map[string]*room{}, Log: log.Default()} }

// Handler serves the relay's HTTP endpoints.
func (rl *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/host/{room}", rl.serveHost)
	mux.HandleFunc("GET /ws/guest/{room}", rl.serveGuest)
	mux.HandleFunc("GET /j/{room}", rl.serveJoinPage)
	mux.HandleFunc("GET /api/room/{room}", rl.serveRoomInfo)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "germ relay: https://github.com/zach-monroe/germ")
	})
	return mux
}

func accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxMessage + 16)
	return c, nil
}

// keepAlive pings so proxies don't drop idle connections.
func keepAlive(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func frame(kind byte, id uint32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:5], id)
	copy(b[5:], payload)
	return b
}

func (rl *Relay) serveHost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("room")
	if !roomRE.MatchString(name) {
		http.Error(w, "bad room", http.StatusBadRequest)
		return
	}
	rl.mu.Lock()
	if rl.rooms[name] != nil {
		rl.mu.Unlock()
		http.Error(w, "room in use", http.StatusConflict)
		return
	}
	if len(rl.rooms) >= maxRooms {
		rl.mu.Unlock()
		http.Error(w, "relay full", http.StatusServiceUnavailable)
		return
	}
	rm := &room{guests: map[uint32]*websocket.Conn{}}
	rl.rooms[name] = rm // reserve before upgrading
	rl.mu.Unlock()

	c, err := accept(w, r)
	if err != nil {
		rl.mu.Lock()
		delete(rl.rooms, name)
		rl.mu.Unlock()
		return
	}
	rl.mu.Lock()
	rm.host = c
	rl.mu.Unlock()
	rl.Log.Printf("room %s: host connected (%d rooms)", name[:4], len(rl.rooms))
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go keepAlive(ctx, c)

	defer func() {
		rl.mu.Lock()
		delete(rl.rooms, name)
		guests := rm.guests
		rm.guests = nil
		rl.mu.Unlock()
		for _, g := range guests {
			g.Close(websocket.StatusGoingAway, "the host ended the session")
		}
		c.CloseNow()
		rl.Log.Printf("room %s: closed", name[:4])
	}()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary || len(data) < 5 {
			continue
		}
		id := binary.BigEndian.Uint32(data[1:5])
		rl.mu.Lock()
		var targets []*websocket.Conn
		if id == 0 {
			for _, g := range rm.guests {
				targets = append(targets, g)
			}
		} else if g := rm.guests[id]; g != nil {
			targets = append(targets, g)
		}
		rl.mu.Unlock()
		switch data[0] {
		case kindData:
			for _, g := range targets {
				wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				g.Write(wctx, websocket.MessageBinary, data[5:])
				cancel()
			}
		case kindKick:
			for _, g := range targets {
				g.Close(websocket.StatusPolicyViolation, "removed by the host")
			}
		}
	}
}

func (rl *Relay) serveGuest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("room")
	rl.mu.Lock()
	rm := rl.rooms[name]
	ok := rm != nil && rm.host != nil && len(rm.guests) < maxGuests
	rl.mu.Unlock()
	if !ok {
		http.Error(w, "no such session (it may have ended)", http.StatusNotFound)
		return
	}
	c, err := accept(w, r)
	if err != nil {
		return
	}
	rl.mu.Lock()
	if rm.guests == nil { // host left meanwhile
		rl.mu.Unlock()
		c.Close(websocket.StatusGoingAway, "the host ended the session")
		return
	}
	rm.next++
	id := rm.next
	rm.guests[id] = c
	host := rm.host
	rl.mu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go keepAlive(ctx, c)

	send := func(kind byte, payload []byte) error {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return host.Write(wctx, websocket.MessageBinary, frame(kind, id, payload))
	}
	defer func() {
		rl.mu.Lock()
		if rm.guests != nil {
			delete(rm.guests, id)
		}
		rl.mu.Unlock()
		send(kindLeave, nil)
		c.CloseNow()
	}()
	if send(kindJoin, nil) != nil {
		return
	}
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary && send(kindData, data) != nil {
			return
		}
	}
}

func (rl *Relay) serveRoomInfo(w http.ResponseWriter, r *http.Request) {
	rl.mu.Lock()
	rm := rl.rooms[r.PathValue("room")]
	info := map[string]any{"live": rm != nil && rm.host != nil}
	if rm != nil {
		info["guests"] = len(rm.guests)
	}
	rl.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (rl *Relay) serveJoinPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	joinPage.Execute(w, map[string]string{"Room": r.PathValue("room")})
}

var joinPage = template.Must(template.New("join").Parse(strings.TrimSpace(`
<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Join a germ session</title>
<style>
 :root { color-scheme: dark; }
 body { font: 16px/1.5 ui-monospace, "JetBrains Mono", Menlo, monospace; background: #1a1b26; color: #c0caf5;
        max-width: 44rem; margin: 3rem auto; padding: 0 1.25rem; }
 h1 { color: #7aa2f7; font-size: 1.4rem; } h2 { color: #bb9af7; font-size: 1.05rem; margin-top: 2rem; }
 a.button { display: inline-block; background: #7aa2f7; color: #1a1b26; padding: .6rem 1.1rem; border-radius: 6px;
            text-decoration: none; font-weight: bold; }
 pre { background: #24283b; padding: .8rem 1rem; border-radius: 6px; overflow-x: auto; white-space: pre-wrap; word-break: break-all; }
 .dim { color: #565f89; } #status { margin: 1rem 0; } .live { color: #9ece6a; } .dead { color: #f7768e; }
</style></head>
<body>
<h1>◆ You're invited to a germ session</h1>
<p>Someone shared their terminal with you: <a href="https://github.com/zach-monroe/germ">germ</a> is a terminal built
for pair programming with people and coding agents.</p>
<p id="status" class="dim">Checking the session…</p>

<h2>Have germ?</h2>
<p><a class="button" id="open" href="#">Open in germ</a></p>
<p class="dim">Nothing happened? Run this instead:</p>
<pre id="cmd">germ join '…'</pre>

<h2>New to germ?</h2>
<p>Install it (Linux; needs Go 1.27+, a C compiler, and X11/OpenGL headers):</p>
<pre>go install github.com/zach-monroe/germ/cmd/germ@latest
germ setup   # lets "Open in germ" links open germ</pre>
<p>then come back and click <b>Open in germ</b>, or run the command above.</p>

<p class="dim" style="margin-top:2.5rem">The session is end-to-end encrypted. The key is in this page's address after
"#", which your browser never sends to the server, so the relay can't read anything you see or type.</p>
<script>
(function () {
  var key = location.hash.slice(1);
  var room = {{.Room}};
  var link = location.href;
  var prefix = location.pathname.replace(/\/j\/[^/]*$/, "");
  var germ = "germ://join/" + (location.protocol === "http:" ? "http:" : "") + location.host + prefix + "/" + room + "#" + key;
  document.getElementById("open").href = germ;
  document.getElementById("cmd").textContent = "germ join '" + link + "'";
  var st = document.getElementById("status");
  if (!key) { st.textContent = "This link is missing its key (the part after #). Ask for the full link."; st.className = "dead"; return; }
  fetch(prefix + "/api/room/" + encodeURIComponent(room)).then(function (r) { return r.json(); }).then(function (j) {
    if (j.live) { st.textContent = "● Session is live" + (j.guests ? " (" + j.guests + " already joined)" : ""); st.className = "live"; }
    else { st.textContent = "This session isn't running right now. It may have ended."; st.className = "dead"; }
  }).catch(function () { st.textContent = ""; });
})();
</script>
</body></html>
`)))
