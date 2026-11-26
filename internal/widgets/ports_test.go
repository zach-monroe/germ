package widgets

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1435 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0000000000000000 100 0 0 10 0
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 222 1 0000000000000000 100 0 0 10 0
   2: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 333 1 0000000000000000 100 0 0 10 0
   3: 0100007F:1435 0100007F:C000 01 00000000:00000000 00:00000000 00000000  1000        0 444 1 0000000000000000 100 0 0 10 0
`

// ::1 port 0x1435 (5173): the same server as line 0 above, on IPv6.
const procTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1435 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 555 1 0000000000000000 100 0 0 10 0
`

func TestReadListens(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"tcp": procTCP})
	ls, err := readListens(filepath.Join(dir, "tcp"))
	if err != nil || len(ls) != 3 {
		t.Fatalf("listens = %+v, %v", ls, err)
	}
	if ls[0].port != 5173 || ls[0].public || ls[0].inode != 111 {
		t.Errorf("loopback listener = %+v", ls[0])
	}
	if ls[1].port != 8080 || !ls[1].public {
		t.Errorf("public listener = %+v", ls[1])
	}
	if ls[2].uid != 0 {
		t.Errorf("root listener = %+v", ls[2])
	}
}

func TestLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"0100007F":                         true,  // 127.0.0.1
		"0101007F":                         true,  // 127.0.1.1
		"00000000":                         false, // 0.0.0.0
		"0101A8C0":                         false, // 192.168.1.1
		"00000000000000000000000001000000": true,  // ::1
		"00000000000000000000000000000000": false, // ::
		"0000000000000000FFFF00000100007F": true,  // ::ffff:127.0.0.1
	} {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%s) = %v", addr, got)
		}
	}
}

func TestCommandName(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"/usr/bin/postgres", "-D", "/data"}, "postgres"},
		{[]string{"node", "/app/node_modules/.bin/vite", "--port", "5173"}, "vite"},
		{[]string{"node", "/app/node_modules/next/dist/bin/next", "dev"}, "next"},
		{[]string{"node", "/app/node_modules/serve/build/main.js"}, "serve"},
		{[]string{"/usr/bin/python3.12", "-m", "http.server", "8000"}, "http.server"},
		{[]string{"python3", "manage.py", "runserver"}, "manage"},
		{[]string{"bun", "run", "dev"}, "dev"},
		{[]string{"node"}, "node"},
		{nil, "?"},
	} {
		if got := commandName(c.argv); got != c.want {
			t.Errorf("commandName(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// fakeProc makes a /proc with processes (pid -> ppid, cmdline, sockets).
func fakeProc(t *testing.T, procs map[int]struct {
	ppid  int
	argv  string
	inode string
}) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"net/tcp": procTCP, "net/tcp6": procTCP6})
	for pid, p := range procs {
		d := filepath.Join(root, strconv.Itoa(pid))
		writeFiles(t, d, map[string]string{
			"stat":    strconv.Itoa(pid) + " (some (odd) name) S " + strconv.Itoa(p.ppid) + " 1 1 0",
			"cmdline": strings.ReplaceAll(p.argv, " ", "\x00") + "\x00",
		})
		os.MkdirAll(filepath.Join(d, "fd"), 0o755)
		if p.inode != "" {
			if err := os.Symlink("socket:["+p.inode+"]", filepath.Join(d, "fd", "3")); err != nil {
				t.Fatal(err)
			}
		}
		os.Symlink("/dev/null", filepath.Join(d, "fd", "0"))
	}
	return root
}

func TestPortsUpdate(t *testing.T) {
	type proc = struct {
		ppid  int
		argv  string
		inode string
	}
	root := fakeProc(t, map[int]proc{
		100: {1, "zsh", ""},                                   // a germ pane's shell
		200: {100, "npm run dev", ""},                         // its child
		300: {200, "node /app/node_modules/.bin/vite", "111"}, // vite on 127.0.0.1:5173
		301: {300, "esbuild --service", "555"},                // a helper holding the IPv6 socket
		400: {1, "spotify", "222"},                            // 0.0.0.0:8080, no pane
	})
	pane := paneRef{pid: 100, window: 4242, ref: "%7", tab: "2", cwd: "/app"}
	p := &ports{proc: root, uid: 1000, ignore: map[string]bool{}, panes: func() []paneRef { return []paneRef{pane} }}
	if err := p.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.rows) != 2 {
		t.Fatalf("rows = %+v (root's port 22 should be hidden, 5173 shown once)", p.rows)
	}
	vite := p.rows[0]
	if vite.port != 5173 || vite.command != "vite" || vite.public || vite.pane == nil || vite.pane.ref != "%7" {
		t.Errorf("first row = %+v", vite)
	}
	if r := p.rows[1]; r.port != 8080 || !r.public || r.pane != nil || r.command != "spotify" {
		t.Errorf("second row = %+v", r)
	}
	out := strings.Join(p.Draw(60, 6, nil), "\n")
	for _, want := range []string{"http://localhost:5173", "germ-pane:4242/%7", "/app · tab 2", "spotify"} {
		if !strings.Contains(out, want) {
			t.Errorf("draw lacks %q:\n%s", want, out)
		}
	}
	for _, sz := range [][2]int{{60, 6}, {24, 3}, {100, 20}} {
		for _, l := range p.Draw(sz[0], sz[1], nil) {
			if width(l) > sz[0] {
				t.Errorf("%v: line too wide: %q", sz, l)
			}
		}
	}

	p.ignore["spotify"] = true
	p.all = true
	p.Update(context.Background())
	if len(p.rows) != 2 || p.rows[1].port != 22 || p.rows[1].pid != 0 {
		t.Errorf("all, ignoring spotify: %+v", p.rows)
	}
}

func TestTruncateLeft(t *testing.T) {
	if got := truncateLeft("/tmp/abc/myapp", 8); got != "…c/myapp" {
		t.Errorf("truncateLeft = %q", got)
	}
	if got := truncateLeft("~/app", 8); got != "~/app" {
		t.Errorf("short = %q", got)
	}
}
