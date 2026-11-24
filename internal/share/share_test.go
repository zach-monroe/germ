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
		line     string
		readOnly bool
		ok       bool
	}{
		{`list-windows -F "#{window_id}"`, true, true},
		{`capture-pane -p -e -t %1 -S -2000`, true, true},
		{`display -p -t %1 "#{cursor_x}"`, true, true},
		{`display "hi"`, true, false},
		{`send-keys -t %1 -H 41`, true, false},
		{`new-window`, true, false},
		{`new-window`, false, true},
		{`send-keys -t %1 -H 41`, false, true},
		{`kill-server`, false, false},
		{`list-windows ; kill-server`, true, false},
		{`display -p "#(touch /tmp/x)"`, true, false},
		{`set-environment GERM_SOCKET /x`, false, false},
		{`select-window -t $4:@2`, true, true},
		{`select-window -t $1:@2`, true, false}, // someone else's session
		{`select-window -t @2`, true, false},    // ambiguous in a group
	} {
		if _, ok := filterCommand(c.line, c.readOnly, "$4"); ok != c.ok {
			t.Errorf("%q (ro=%v): ok=%v", c.line, c.readOnly, ok)
		}
	}
}

// TestShareEndToEnd runs a relay, a host sharing a scratch tmux session,
// and two guests (one read-only) in-process.
func TestShareEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := "germ-share-test-" + randomID(4)
	tmux := func(args ...string) error {
		return exec.Command("tmux", append([]string{"-L", sock}, args...)...).Run()
	}
	if err := tmux("new-session", "-d", "-s", "shared", "-x", "80", "-y", "24", "cat"); err != nil {
		t.Skip("can't start tmux:", err)
	}
	defer tmux("kill-server")
	defer os.Remove("/tmp/tmux-" + itoa(os.Getuid()) + "/" + sock)

	relay := NewRelay()
	relay.Log.SetOutput(new(strings.Builder))
	srv := httptest.NewServer(relay.Handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	h, _ := NewHost(u, "shared")
	h.TmuxArgs = []string{"-L", sock}
	h.Name = "host@test"
	h.Approval = "none"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	rw, ro := h.Links()
	time.Sleep(200 * time.Millisecond)

	g, err := Join(ctx, rw, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.Welcome.Session != "shared" || g.Welcome.ReadOnly {
		t.Errorf("welcome %+v", g.Welcome)
	}
	lines := bufio.NewScanner(g)
	var seen strings.Builder // pane output so far: tmux may split it across %output lines
	expect := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && lines.Scan() {
			seen.WriteString(outputText(lines.Text()))
			if strings.Contains(seen.String(), want) {
				return
			}
		}
		t.Fatalf("never saw %q", want)
	}
	g.Write([]byte("send-keys -t shared -l hello-from-guest\n"))
	expect("hello-from-guest") // echoed by cat via %output

	r, err := Join(ctx, ro, "bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Welcome.ReadOnly {
		t.Error("read-only link joined read-write")
	}
	rlines := bufio.NewScanner(r)
	r.Write([]byte("new-window\n"))
	deadline := time.Now().Add(5 * time.Second)
	sawError := false
	for time.Now().Before(deadline) && rlines.Scan() {
		if strings.HasPrefix(rlines.Text(), "%error") {
			sawError = true
			break
		}
	}
	if !sawError {
		t.Error("read-only guest's new-window wasn't refused")
	}
	time.Sleep(200 * time.Millisecond)
	if ps := h.Participants(); len(ps) != 3 {
		t.Errorf("participants %+v", ps)
	}
	out, _ := exec.Command("tmux", "-L", sock, "list-windows", "-t", "shared").Output()
	if n := strings.Count(string(out), "\n"); n != 1 {
		t.Errorf("windows: %d", n)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestShareV2 covers typing presence, grouped guest sessions, removing a
// guest, and reconnecting after the relay drops every connection.
func TestShareV2(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := "germ-share-test-" + randomID(4)
	tmux := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).Output()
		return string(out), err
	}
	if _, err := tmux("new-session", "-d", "-s", "s", "-x", "80", "-y", "24", "cat"); err != nil {
		t.Skip("can't start tmux:", err)
	}
	defer tmux("kill-server")

	relay := NewRelay()
	relay.Log.SetOutput(new(strings.Builder))
	srv := httptest.NewServer(relay.Handler())
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	h, _ := NewHost(u, "s")
	h.TmuxArgs = []string{"-L", sock}
	h.Approval = "none"
	h.Log = func(f string, a ...any) { t.Logf(f, a...) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { // like germ share: keep serving across relay drops
		for ctx.Err() == nil {
			h.Run(ctx)
			time.Sleep(200 * time.Millisecond)
		}
	}()
	rw, ro := h.Links()
	time.Sleep(200 * time.Millisecond)

	alice, err := Join(ctx, rw, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	bob, err := Join(ctx, ro, "bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
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
	// Typing shows up in presence, then clears.
	alice.Write([]byte("send-keys -t s -l typing\n"))
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

	// The relay drops everyone: alice comes back on her own.
	relay.dropAll()
	expect(ReconnectMarker)
	alice.Write([]byte("send-keys -t s -l back-again\n"))
	expect("back-again")
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
	defer exec.Command("tmux", "-L", sock, "kill-server").Run()

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
	rw, ro := h.Links()
	time.Sleep(200 * time.Millisecond)

	// alice is let in.
	waited := make(chan bool, 1)
	go func() {
		r := <-asked
		if r.Name != "alice" || r.ReadOnly {
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
	if _, err := Join(ctx, ro, "bob", nil); err == nil || !strings.Contains(err.Error(), "didn't let you in") {
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
