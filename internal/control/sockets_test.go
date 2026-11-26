package control

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSocketsRemovesStale(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}

	// A dead process's socket: bound once, nothing listening now.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := SocketPath(cmd.Process.Pid)
	ln, err := net.Listen("unix", dead)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	// A live window's socket.
	live := SocketPath(os.Getpid())
	ln2, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()

	// A live process whose socket isn't listening yet (starting up): kept.
	starting := filepath.Join(Dir(), strconv.Itoa(os.Getppid())+".sock")
	ln3, err := net.Listen("unix", starting)
	if err != nil {
		t.Fatal(err)
	}
	ln3.(*net.UnixListener).SetUnlinkOnClose(false)
	ln3.Close()

	got := Sockets()
	has := map[string]bool{}
	for _, s := range got {
		has[s] = true
	}
	if !has[live] || !has[starting] || has[dead] {
		t.Fatalf("Sockets() = %v; want %s and %s, not %s", got, live, starting, dead)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("stale socket %s not removed", dead)
	}
	if _, err := os.Stat(starting); err != nil {
		t.Errorf("starting window's socket removed: %v", err)
	}
}
