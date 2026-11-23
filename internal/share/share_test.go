package share

import (
	"bufio"
	"context"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	rw, ro := h.Links()
	time.Sleep(200 * time.Millisecond)

	g, err := Join(ctx, rw, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.Welcome.Session != "shared" || g.Welcome.ReadOnly {
		t.Errorf("welcome %+v", g.Welcome)
	}
	lines := bufio.NewScanner(g)
	expect := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && lines.Scan() {
			if strings.Contains(lines.Text(), want) {
				return
			}
		}
		t.Fatalf("never saw %q", want)
	}
	g.Write([]byte("send-keys -t shared -l hello-from-guest\n"))
	expect("hello-from-guest") // echoed by cat via %output

	r, err := Join(ctx, ro, "bob")
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

	alice, err := Join(ctx, rw, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer alice.Close()
	bob, err := Join(ctx, ro, "bob")
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
	expect := func(want string) {
		t.Helper()
		deadline := time.After(8 * time.Second)
		for {
			select {
			case l, ok := <-aliceOut:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				if strings.Contains(l, want) {
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
