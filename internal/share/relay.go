package share

import (
	"context"
	"crypto/subtle"
	"embed"
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
	// OnlyRoom, if set, makes this a private relay for one share (germ
	// share's built-in relay): other rooms are refused.
	OnlyRoom string
	// HostToken, if set, must accompany the host's connection. Through a
	// tunnel every request comes from localhost, so the address proves
	// nothing; the token does.
	HostToken string
}

type room struct {
	host   *websocket.Conn
	guests map[uint32]*websocket.Conn
	next   uint32
	joins  []time.Time // recent guest connections, for allowJoin
}

// allowJoin rate-limits guest connections to a room: a leaked link can't
// be used to flood the host with join requests. Caller holds rl.mu.
func (rm *room) allowJoin(now time.Time) bool {
	const window, limit = time.Minute, 20
	keep := rm.joins[:0]
	for _, t := range rm.joins {
		if now.Sub(t) < window {
			keep = append(keep, t)
		}
	}
	rm.joins = keep
	if len(rm.joins) >= limit {
		return false
	}
	rm.joins = append(rm.joins, now)
	return true
}

// NewRelay makes a relay.
func NewRelay() *Relay { return &Relay{rooms: map[string]*room{}, Log: log.Default()} }

// Handler serves the relay's HTTP endpoints.
func (rl *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/host/{room}", rl.serveHost)
	mux.HandleFunc("GET /ws/guest/{room}", rl.serveGuest)
	mux.HandleFunc("GET /j/{room}", rl.serveJoinPage)
	mux.HandleFunc("GET /web/{file}", rl.serveWeb)
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
	if rl.OnlyRoom != "" && name != rl.OnlyRoom {
		http.Error(w, "not here", http.StatusForbidden)
		return
	}
	if rl.HostToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(hostTokenHeader)), []byte(rl.HostToken)) != 1 {
		http.Error(w, "not the host", http.StatusForbidden)
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
	if rl.OnlyRoom != "" && name != rl.OnlyRoom {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	rl.mu.Lock()
	rm := rl.rooms[name]
	ok := rm != nil && rm.host != nil && len(rm.guests) < maxGuests
	limited := ok && !rm.allowJoin(time.Now())
	rl.mu.Unlock()
	if !ok {
		http.Error(w, "no such session (it may have ended)", http.StatusNotFound)
		return
	}
	if limited {
		http.Error(w, "too many attempts to join; wait a minute", http.StatusTooManyRequests)
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

// serveJoinPage serves the browser viewer for an invite link.
func (rl *Relay) serveJoinPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	// Everything the page loads comes from here; it only talks to here.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; "+
		"style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; frame-ancestors 'none'")
	prefix := strings.TrimSuffix(r.URL.Path, "/j/"+r.PathValue("room"))
	viewerPage.Execute(w, map[string]string{"Room": r.PathValue("room"), "Prefix": prefix})
}

func (rl *Relay) serveWeb(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	data, err := webFS.ReadFile("web/" + name)
	if err != nil || strings.HasSuffix(name, ".html") {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(data)
}

//go:embed web
var webFS embed.FS

var viewerPage = template.Must(template.ParseFS(webFS, "web/viewer.html"))

// dropAll cuts every connection abruptly, as a network failure would
// (for tests).
func (rl *Relay) dropAll() {
	rl.mu.Lock()
	var conns []*websocket.Conn
	for _, rm := range rl.rooms {
		if rm.host != nil {
			conns = append(conns, rm.host)
		}
		for _, g := range rm.guests {
			conns = append(conns, g)
		}
	}
	rl.mu.Unlock()
	for _, c := range conns {
		c.CloseNow()
	}
}
