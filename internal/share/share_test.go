package share

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKeyAndBox(t *testing.T) {
	k, _ := NewKey()
	k2, err := ParseKey(k.String())
	if err != nil || k2 != k {
		t.Fatal("key round trip")
	}
	b := newBox(k)
	msg, err := b.open(b.seal([]byte("hello")))
	if err != nil || string(msg) != "hello" {
		t.Fatal("box round trip")
	}
	other, _ := NewKey()
	if _, err := newBox(other).open(b.seal([]byte("x"))); err == nil {
		t.Fatal("wrong key opened")
	}
}

func TestLinks(t *testing.T) {
	k, _ := NewKey()
	for _, relay := range []string{"https://relay.example.com", "http://localhost:8080", "https://example.com/germ"} {
		u, _ := url.Parse(relay)
		l := Link{Relay: u, Room: "abcdefgh12", Key: k}
		for _, s := range []string{l.String(), l.GermURL()} {
			got, err := ParseLink(s)
			if err != nil {
				t.Fatalf("%s: %v", s, err)
			}
			if got.Relay.String() != u.String() || got.Room != l.Room || got.Key != k {
				t.Errorf("%s: got %v %s", s, got.Relay, got.Room)
			}
		}
	}
	if _, err := ParseLink("https://relay.example.com/j/abcdefgh12"); err == nil {
		t.Error("link without key accepted")
	}
}

func TestFilterCommand(t *testing.T) {
	for _, c := range []struct {
		line   string
		class  int
		target string
	}{
		{`list-windows -F "#{window_id}"`, cmdView, ""},
		{`capture-pane -p -e -t %1 -S -2000`, cmdView, ""},
		{`display -p -t %1 "#{cursor_x}"`, cmdView, ""},
		{`display "hi"`, cmdBlocked, ""},
		{`send-keys -t %1 -H 41`, cmdHelm, "%1"},
		{`send-keys -X -t %3 cancel`, cmdHelm, "%3"},
		{`send-keys -K -c /dev/pts/3 C-b c`, cmdBlocked, ""}, // tmux bindings: the prefix and command prompt
		{`split-window -h -t %2 -c '/tmp/x'`, cmdHelm, "%2"},
		{`kill-window -t @4`, cmdHelm, "@4"},
		{`resize-pane -Z -t %2`, cmdHelm, "%2"},
		{`new-window -c '/tmp'`, cmdNewWindow, ""},
		{`new-window -t $1`, cmdBlocked, ""},
		{`kill-server`, cmdBlocked, ""},
		{`run-shell "rm -rf ~"`, cmdBlocked, ""},
		{`command-prompt`, cmdBlocked, ""},
		{`list-windows ; kill-server`, cmdBlocked, ""},
		{`display -p "#(touch /tmp/x)"`, cmdBlocked, ""},
		{`set-environment GERM_SOCKET /x`, cmdBlocked, ""},
		{`select-window -t $4:@2`, cmdView, ""},
		{`select-window -t '$4:@2'`, cmdView, ""},
		{`select-window -t $1:@2`, cmdBlocked, ""}, // someone else's session
		{`select-window -t @2`, cmdBlocked, ""},    // ambiguous in a group
	} {
		if class, target := filterCommand(c.line, "$4"); class != c.class || target != c.target {
			t.Errorf("%q: class %d target %q, want %d %q", c.line, class, target, c.class, c.target)
		}
	}
}

// testSession starts a scratch tmux server with session name running cat,
// and a relay, and returns a host sharing it (not yet running).
func testSession(t *testing.T, name string) (h *Host, tmux func(...string) (string, error), relay *Relay) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := "germ-share-test-" + randomID(4)
	tmux = func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).Output()
		return string(out), err
	}
	if _, err := tmux("new-session", "-d", "-s", name, "-x", "80", "-y", "24", "cat"); err != nil {
		t.Skip("can't start tmux:", err)
	}
	t.Cleanup(func() {
		tmux("kill-server") // before removing its socket, or it can't be reached
		os.Remove("/tmp/tmux-" + itoa(os.Getuid()) + "/" + sock)
	})
	relay = NewRelay()
	relay.Log.SetOutput(new(strings.Builder))
	srv := httptest.NewServer(relay.Handler())
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	h, _ = NewHost(u, name)
	h.TmuxArgs = []string{"-L", sock}
	h.Name = "host@test"
	h.Approval = "none"
	h.Log = func(f string, a ...any) { t.Logf(f, a...) }
	return h, tmux, relay
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestHelmAndChat runs a relay, a host and two guests in-process: nobody
// types until they have the helm, the host hands it over and takes it
// back, and the chat reaches everyone in one order with trusted senders.
func TestHelmAndChat(t *testing.T) {
	h, tmux, _ := testSession(t, "shared")
	var hostChat []ChatMsg
	var mu sync.Mutex
	h.OnChat = func(m ChatMsg) { mu.Lock(); hostChat = append(hostChat, m); mu.Unlock() }
	asked := make(chan HelmRequest, 4)
	h.OnHelmRequest = func(r HelmRequest) { asked <- r }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	join := func(name string) *Guest {
		g, err := Join(ctx, h.Link(), name, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { g.Close() })
		go io.Copy(io.Discard, g)
		return g
	}
	pane := func() string { out, _ := tmux("capture-pane", "-p", "-t", "shared"); return out }

	alice := join("alice")
	if alice.Welcome.Session != "shared" || alice.Welcome.You == 0 {
		t.Errorf("welcome %+v", alice.Welcome)
	}
	var win string
	waitFor(t, "state", func() bool {
		if st := alice.State(); len(st.Tabs) == 1 {
			win = st.Tabs[0].Window
		}
		return win != ""
	})

	// Without the helm, typing goes nowhere.
	alice.Write([]byte("send-keys -t %0 -l no-helm\n"))
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(pane(), "no-helm") {
		t.Fatal("alice typed without the helm")
	}
	// She asks; the host is asked and says yes; now she types.
	alice.Act(Action{Op: OpRequest, Window: win})
	var r HelmRequest
	select {
	case r = <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("host wasn't asked")
	}
	if r.Name != "alice" || r.Window != win || r.Approver != 0 {
		t.Errorf("request %+v", r)
	}
	h.Act(Action{Op: OpDecide, Request: r.ID, Allow: true})
	if got := h.State().HelmOf(win); got != alice.Welcome.You {
		t.Fatalf("helm is %d", got)
	}
	alice.Write([]byte("send-keys -t %0 -l with-helm\n"))
	waitFor(t, "alice's typing", func() bool { return strings.Contains(pane(), "with-helm") })

	// bob can't type while alice has it, and can't take it.
	bob := join("bob")
	bob.Write([]byte("send-keys -t %0 -l bob-was-here\n"))
	bob.Act(Action{Op: OpTake, Window: win})
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(pane(), "bob-was-here") || h.State().HelmOf(win) != alice.Welcome.You {
		t.Fatal("bob typed or took the helm")
	}
	// The host takes it back at once.
	h.Act(Action{Op: OpTake, Window: win})
	alice.Write([]byte("send-keys -t %0 -l after-take\n"))
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(pane(), "after-take") {
		t.Fatal("alice typed after the host took the helm")
	}

	// The host hands bob the helm without his asking; the tab stays the
	// host's, and guests can't hand it out.
	alice.Act(Action{Op: OpHand, Window: win, Person: alice.Welcome.You})
	h.Act(Action{Op: OpHand, Window: win, Person: bob.Welcome.You})
	if st := h.State().Tab(win); st.Helm != bob.Welcome.You || st.Owner != 0 {
		t.Fatalf("after hand: %+v", st)
	}
	bob.Write([]byte("send-keys -t %0 -l bob-handed\n"))
	waitFor(t, "bob's typing", func() bool { return strings.Contains(pane(), "bob-handed") })
	h.Act(Action{Op: OpTake, Window: win})

	// A tab given to bob: he types there, and approves others' requests.
	h.Act(Action{Op: OpGive, Window: win, Person: bob.Welcome.You})
	bob.Write([]byte("send-keys -t %0 -l bobs-tab\n"))
	waitFor(t, "bob's typing", func() bool { return strings.Contains(pane(), "bobs-tab") })
	alice.Act(Action{Op: OpRequest, Window: win})
	waitFor(t, "request to bob", func() bool {
		rs := bob.State().Requests
		return len(rs) == 1 && rs[0].Approver == bob.Welcome.You
	})
	rid := bob.State().Requests[0].ID
	bob.Act(Action{Op: OpDecide, Request: rid, Allow: true})
	waitFor(t, "alice has bob's tab", func() bool { return h.State().HelmOf(win) == alice.Welcome.You })
	alice.Act(Action{Op: OpRelease, Window: win})
	waitFor(t, "back to bob", func() bool { return h.State().HelmOf(win) == bob.Welcome.You })

	// Chat: one order everywhere, senders stamped by the host.
	alice.Act(Action{Op: OpSay, Text: "hi from alice", Person: 0})
	time.Sleep(100 * time.Millisecond)
	h.Act(Action{Op: OpSay, Text: "hi from the host"})
	bob.Act(Action{Op: OpSay, Text: "  \x1b[31mhi from bob\n\n"})
	said := func(chat []ChatMsg) []string {
		var out []string
		for _, m := range chat {
			if !m.Event {
				out = append(out, m.Name+": "+m.Text)
			}
		}
		return out
	}
	want := "alice: hi from alice|host: hi from the host|bob: [31mhi from bob"
	for _, g := range []*Guest{alice, bob} {
		waitFor(t, "chat", func() bool { return strings.Join(said(g.Chat()), "|") == want })
	}
	mu.Lock()
	if got := strings.Join(said(hostChat), "|"); got != want {
		t.Errorf("host chat %q", got)
	}
	mu.Unlock()

	// Someone joining later gets the history, events included.
	carol := join("alice") // same name: gets a unique one
	waitFor(t, "history", func() bool { return strings.Contains(strings.Join(said(carol.Chat()), "|"), want) })
	ps := h.Participants()
	if len(ps) != 4 || ps[3].Name != "alice (2)" {
		t.Errorf("participants %+v", ps)
	}
	events := 0
	for _, m := range carol.Chat() {
		if m.Event && strings.Contains(m.Text, "gave alice the helm") {
			events++
		}
	}
	if events == 0 {
		t.Error("no helm events in the history")
	}

	// Saved on command, only.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path, err := h.SaveChat()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "alice: hi from alice") {
		t.Errorf("saved chat:\n%s", b)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestShareV2 covers typing presence, grouped guest sessions, removing a
// guest, and reconnecting after the relay drops every connection (keeping
// the helm).
func TestShareV2(t *testing.T) {
	h, tmux, relay := testSession(t, "s")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { // like germ share: keep serving across relay drops
		for ctx.Err() == nil {
			h.Run(ctx)
			time.Sleep(200 * time.Millisecond)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	alice, err := Join(ctx, h.Link(), "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	bob, err := Join(ctx, h.Link(), "bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string, cond func() bool) { t.Helper(); waitFor(t, what, cond) }
	go io.Copy(io.Discard, bob)
	aliceOut := make(chan string, 1000)
	go func() {
		sc := bufio.NewScanner(alice)
		for sc.Scan() {
			aliceOut <- sc.Text()
		}
		close(aliceOut)
	}()
	var seen strings.Builder
	expect := func(want string) {
		t.Helper()
		deadline := time.After(8 * time.Second)
		for {
			select {
			case l, ok := <-aliceOut:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				seen.WriteString(outputText(l))
				if strings.Contains(seen.String(), want) {
					seen.Reset()
					return
				}
			case <-deadline:
				t.Fatalf("never saw %q", want)
			}
		}
	}

	// Each guest gets its own session grouped with the host's.
	waitFor("guest sessions", func() bool {
		out, _ := tmux("list-sessions", "-F", "#{session_name}")
		return strings.Count(out, "s-guest-") == 2
	})
	// Give alice the tab. Typing shows up in presence.
	waitFor("tabs", func() bool { return len(h.State().Tabs) == 1 })
	win := h.State().Tabs[0].Window
	h.Act(Action{Op: OpGive, Window: win, Person: alice.Welcome.You})
	alice.Write([]byte("send-keys -t %0 -l typing\n"))
	expect("typing")
	waitFor("typing", func() bool {
		for _, p := range h.Participants() {
			if p.Name == "alice" && p.Typing {
				return true
			}
		}
		return false
	})
	// Removing bob ends his session for good, with a reason.
	var bobID uint32
	for _, p := range h.Participants() {
		if p.Name == "bob" {
			bobID = p.ID
		}
	}
	h.Remove(bobID)
	waitFor("bob removed", func() bool { return strings.Contains(bob.Reason(), "removed") })
	waitFor("bob's session gone", func() bool {
		out, _ := tmux("list-sessions", "-F", "#{session_name}")
		return strings.Count(out, "s-guest-") == 1
	})

	// The relay drops everyone: alice comes back on her own, still at the
	// helm of her tab.
	relay.dropAll()
	expect(ReconnectMarker)
	alice.Write([]byte("send-keys -t %0 -l back-again\n"))
	expect("back-again")
	if st := h.State(); st.Tab(win).Owner != alice.Welcome.You {
		t.Errorf("alice's tab after reconnecting: %+v", st.Tabs)
	}
}

// TestBrowserCrypto checks the browser viewer's crypto (tweetnacl, as
// shipped in web/) against Go's, in both directions.
func TestBrowserCrypto(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	k, _ := NewKey()
	b := newBox(k)
	sealed := base64.StdEncoding.EncodeToString(b.seal([]byte("from go")))
	nacl, _ := filepath.Abs("web/nacl-fast.min.js")
	script := `
const nacl = require(process.argv[1]);
const key = Buffer.from(process.argv[2], "base64url");
const data = Buffer.from(process.argv[3], "base64");
const msg = nacl.secretbox.open(data.subarray(24), data.subarray(0, 24), key);
if (!msg) { console.log("OPEN FAILED"); process.exit(1); }
const nonce = nacl.randomBytes(24);
const box = nacl.secretbox(Buffer.from(Buffer.from(msg).toString() + " and back from js"), nonce, key);
console.log(Buffer.concat([nonce, box]).toString("base64"));`
	out, err := exec.Command("node", "-e", script, nacl, k.String(), sealed).Output()
	if err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	data, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	msg, err := b.open(data)
	if err != nil || string(msg) != "from go and back from js" {
		t.Fatalf("got %q, %v", msg, err)
	}
}

// TestPrivateRelay checks germ share's built-in relay serves only its own
// room, and only lets this machine host it.
func TestPrivateRelay(t *testing.T) {
	rl := NewRelay()
	rl.OnlyRoom = "myroom12345"
	rl.HostToken = "tok"
	srv := httptest.NewServer(rl.Handler())
	defer srv.Close()
	get := func(path string, hdr map[string]string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get("/ws/host/otherroom123", nil); c != 403 {
		t.Errorf("other room hosted: %d", c)
	}
	if c := get("/ws/host/myroom12345", nil); c != 403 {
		t.Errorf("host without the token allowed: %d", c)
	}
	if c := get("/ws/host/myroom12345", map[string]string{hostTokenHeader: "nope"}); c != 403 {
		t.Errorf("host with a wrong token allowed: %d", c)
	}
	if c := get("/ws/guest/otherroom123", nil); c != 404 {
		t.Errorf("other room joinable: %d", c)
	}
	if c := get("/j/myroom12345", nil); c != 200 {
		t.Errorf("viewer page: %d", c)
	}
	if c := get("/web/viewer.js", nil); c != 200 {
		t.Errorf("viewer script: %d", c)
	}
	if c := get("/web/viewer.html", nil); c != 404 {
		t.Errorf("raw template served: %d", c)
	}
}

// TestApproval: guests wait for the host; denied ones are turned away;
// approved ones get back in without asking after a reconnect. The relay
// only accepts the host with its token.
func TestApproval(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := "germ-share-test-" + randomID(4)
	if err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", "s", "cat").Run(); err != nil {
		t.Skip("can't start tmux:", err)
	}
	defer func() {
		exec.Command("tmux", "-L", sock, "kill-server").Run()
		os.Remove("/tmp/tmux-" + itoa(os.Getuid()) + "/" + sock)
	}()

	relay := NewRelay()
	relay.Log.SetOutput(new(strings.Builder))
	relay.HostToken = "secret-token"
	srv := httptest.NewServer(relay.Handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	// Without the token, nobody can be the host.
	impostor, _ := NewHost(u, "s")
	if err := impostor.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("host without token: %v", err)
	}

	h, _ := NewHost(u, "s")
	h.TmuxArgs = []string{"-L", sock}
	h.HostToken = "secret-token"
	asked := make(chan JoinRequest, 4)
	h.OnRequest = func(r JoinRequest) { asked <- r }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			h.Run(ctx)
			time.Sleep(100 * time.Millisecond)
		}
	}()
	rw := h.Link()
	time.Sleep(200 * time.Millisecond)

	// alice is let in.
	waited := make(chan bool, 1)
	go func() {
		r := <-asked
		if r.Name != "alice" {
			t.Errorf("request %+v", r)
		}
		h.Decide(r.ID, true)
	}()
	alice, err := Join(ctx, rw, "alice", func() { waited <- true })
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	select {
	case <-waited:
	default:
		t.Error("alice wasn't told to wait")
	}
	go io.Copy(io.Discard, alice)

	// bob is turned away.
	go func() { h.Decide((<-asked).ID, false) }()
	if _, err := Join(ctx, rw, "bob", nil); err == nil || !strings.Contains(err.Error(), "didn't let you in") {
		t.Errorf("bob: %v", err)
	}

	// alice reconnects after a drop without being asked again.
	relay.dropAll()
	time.Sleep(3 * time.Second)
	select {
	case r := <-asked:
		t.Errorf("asked again after reconnect: %+v", r)
	default:
	}
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		for _, p := range h.Participants() {
			if p.Name == "alice" {
				return
			}
		}
	}
	t.Error("alice didn't get back in")
}

// outputText is what a control-mode line contributes to the text tests
// search: a %output line's data, or the line itself (e.g. markers).
func outputText(line string) string {
	if rest, ok := strings.CutPrefix(line, "%output "); ok {
		if _, data, ok := strings.Cut(rest, " "); ok {
			return string(unescapeTmux(data))
		}
	}
	return "\n" + line + "\n"
}

// unescapeTmux undoes tmux's octal escaping in %output data.
func unescapeTmux(s string) []byte {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				out = append(out, byte(v))
				i += 3
				continue
			}
		}
		out = append(out, s[i])
	}
	return out
}

// The share pane is protected: a guest at the helm of its tab types into
// the tab's other pane, and can't close it.
func TestProtectedPane(t *testing.T) {
	h, tmux, _ := testSession(t, "prot")
	out, _ := tmux("split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "prot", "cat")
	h.Protect = strings.TrimSpace(out) // the share pane: %1; the shell: %0
	tmux("select-pane", "-t", h.Protect)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	g, err := Join(ctx, h.Link(), "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	go io.Copy(io.Discard, g)
	var win string
	waitFor(t, "state", func() bool {
		if st := g.State(); len(st.Tabs) == 1 {
			win = st.Tabs[0].Window
		}
		return win != ""
	})
	h.Act(Action{Op: OpHand, Window: win, Person: g.Welcome.You})
	capture := func(p string) string { out, _ := tmux("capture-pane", "-p", "-t", p); return out }
	g.Write([]byte("send-keys -t " + h.Protect + " -l aimed-at-share\n"))
	g.Write([]byte("send-keys -l no-target\n"))
	waitFor(t, "typing in the other pane", func() bool {
		return strings.Contains(capture("%0"), "aimed-at-share") && strings.Contains(capture("%0"), "no-target")
	})
	if strings.Contains(capture(h.Protect), "aimed") {
		t.Fatal("typed into the share pane")
	}
	g.Write([]byte("kill-pane -t " + h.Protect + "\n"))
	time.Sleep(500 * time.Millisecond)
	if n, _ := tmux("list-panes", "-t", "prot"); strings.Count(n, "\n") != 2 {
		t.Fatalf("panes after kill-pane:\n%s", n)
	}
}

func TestNameMatches(t *testing.T) {
	for _, c := range []struct {
		name, want string
		ok         bool
	}{
		{"alice@laptop", "alice", true},
		{"alice@laptop", "@Alice", true},
		{"alice@laptop", "alice@laptop", true},
		{"carol", "carol", true},
		{"carol (browser)", "carol", true}, // a browser from before the viewer stopped adding it
		{"alice@laptop (2)", "alice (2)", true},
		{"alice@laptop", "bob", false},
		{"alice", "", false},
	} {
		if got := NameMatches(c.name, c.want); got != c.ok {
			t.Errorf("NameMatches(%q, %q) = %v", c.name, c.want, got)
		}
	}
}
