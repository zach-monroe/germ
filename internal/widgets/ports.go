package widgets

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zach-monroe/germ/internal/control"
)

// ports lists the TCP ports your processes listen on, and which germ pane
// started each (a dev server in tab 3). Click the port to open it in the
// browser, the pane to go there. Options: ignore = ["spotify", ...]
// (process names to leave out), all = true (other users' ports too).
type ports struct {
	proc   string // root, for tests
	uid    int
	all    bool
	ignore map[string]bool
	panes  func() []paneRef // germ panes by process; swappable for tests

	mu   sync.Mutex
	rows []portRow
}

type portRow struct {
	port    int
	public  bool   // bound to every interface, not just loopback
	pid     int    // 0 if we can't see the process (another user's)
	command string // short name: "vite", "postgres"
	pane    *paneRef
}

// paneRef is a germ pane, by its top process.
type paneRef struct {
	pid    int
	window int    // germ's process ID (for germ-pane: links)
	ref    string // tmux pane ID or germ pane ID
	tab    string // as the tab bar shows it
	cwd    string
}

func newPorts(o Options) Widget {
	p := &ports{proc: "/proc", uid: os.Getuid(), all: o.Get("all", "") == "true",
		ignore: map[string]bool{}, panes: germPanes}
	for _, n := range strings.Split(o.Get("ignore", ""), ",") {
		if n = strings.TrimSpace(n); n != "" {
			p.ignore[n] = true
		}
	}
	return p
}

func (p *ports) Every() time.Duration { return 3 * time.Second }
func (p *ports) Tick() time.Duration  { return 0 }

func (p *ports) Update(context.Context) error {
	type key struct{ port, uid int }
	listens := map[key]*portRow{}
	var order []key
	inodes := map[uint64]*portRow{}
	for _, f := range []string{"net/tcp", "net/tcp6"} {
		ls, err := readListens(filepath.Join(p.proc, f))
		if err != nil {
			continue // no IPv6, say
		}
		for _, l := range ls {
			if !p.all && l.uid != p.uid {
				continue
			}
			// The same server on IPv4 and IPv6 is one row.
			k := key{l.port, l.uid}
			r := listens[k]
			if r == nil {
				r = &portRow{port: l.port}
				listens[k] = r
				order = append(order, k)
			}
			r.public = r.public || l.public
			inodes[l.inode] = r
		}
	}
	// Which processes hold each row's sockets. Forked workers share their
	// parent's, and a server's helper may hold one too: name the row by
	// the topmost (the one none of the others descends from).
	owners := map[*portRow]map[int]bool{}
	for inode, pids := range socketOwners(p.proc, inodes) {
		r := inodes[inode]
		if owners[r] == nil {
			owners[r] = map[int]bool{}
		}
		for _, pid := range pids {
			owners[r][pid] = true
		}
	}
	for r, set := range owners {
		r.pid = topmost(p.proc, set)
	}
	// Which pane each process descends from.
	byPid := map[int]*paneRef{}
	for _, pr := range p.panes() {
		pr := pr
		byPid[pr.pid] = &pr
	}
	var rows []portRow
	for _, k := range order {
		r := listens[k]
		if r.pid > 0 {
			r.command = commandName(procCmdline(p.proc, r.pid))
			r.pane = findPane(p.proc, r.pid, byPid)
		}
		if p.ignore[r.command] {
			continue
		}
		rows = append(rows, *r)
	}
	// Ports from your panes first, then by number.
	sort.SliceStable(rows, func(i, j int) bool {
		if (rows[i].pane != nil) != (rows[j].pane != nil) {
			return rows[i].pane != nil
		}
		return rows[i].port < rows[j].port
	})
	p.mu.Lock()
	p.rows = rows
	p.mu.Unlock()
	return nil
}

type listen struct {
	port   int
	public bool
	uid    int
	inode  uint64
}

// readListens parses /proc/net/tcp{,6} for listening sockets.
func readListens(path string) ([]listen, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []listen
	for i, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 10 || f[3] != "0A" { // 0A: LISTEN
			continue
		}
		addr, port, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		pn, err1 := strconv.ParseUint(port, 16, 16)
		uid, err2 := strconv.Atoi(f[7])
		inode, err3 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		out = append(out, listen{port: int(pn), public: !loopback(addr), uid: uid, inode: inode})
	}
	return out, nil
}

// loopback reports whether a /proc/net/tcp address (hex, little-endian
// words) is 127.x.x.x or ::1 (also ::ffff:127.x.x.x).
func loopback(addr string) bool {
	b, err := hex.DecodeString(addr)
	if err != nil {
		return false
	}
	switch len(b) {
	case 4:
		return b[3] == 127 // stored little-endian: 0100007F
	case 16:
		// Four little-endian 32-bit words.
		w := func(i int) uint32 {
			return uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		}
		if w(0) == 0 && w(1) == 0 && w(2) == 0 && w(3) == 1 {
			return true // ::1
		}
		return w(0) == 0 && w(1) == 0 && w(2) == 0xffff && w(3)>>24 == 127
	}
	return false
}

// topmost picks the process in set that no other in set is an ancestor
// of (the lowest pid of those, to be stable).
func topmost(proc string, set map[int]bool) int {
	best := 0
	for pid := range set {
		top := true
		for a, i := parentPid(proc, pid), 0; a > 1 && i < 64; a, i = parentPid(proc, a), i+1 {
			if set[a] {
				top = false
				break
			}
		}
		if top && (best == 0 || pid < best) {
			best = pid
		}
	}
	return best
}

// socketOwners finds which processes hold each socket inode, by reading
// the fd links of the processes we're allowed to look into.
func socketOwners(proc string, want map[uint64]*portRow) map[uint64][]int {
	out := map[uint64][]int{}
	if len(want) == 0 {
		return out
	}
	ents, _ := os.ReadDir(proc)
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(proc, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // not ours
		}
		for _, fd := range fds {
			l, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(l, "socket:[") {
				continue
			}
			inode, err := strconv.ParseUint(strings.TrimSuffix(l[len("socket:["):], "]"), 10, 64)
			if err != nil {
				continue
			}
			if _, ok := want[inode]; ok {
				out[inode] = append(out[inode], pid)
			}
		}
	}
	return out
}

func procCmdline(proc string, pid int) []string {
	b, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

func parentPid(proc string, pid int) int {
	b, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// pid (comm) state ppid ...; comm may hold spaces and parens.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}

// findPane walks up from pid to the germ pane whose shell started it.
func findPane(proc string, pid int, byPid map[int]*paneRef) *paneRef {
	for i := 0; i < 64 && pid > 1; i++ {
		if pr := byPid[pid]; pr != nil {
			return pr
		}
		pid = parentPid(proc, pid)
	}
	return nil
}

var interpreters = map[string]bool{"node": true, "nodejs": true, "bun": true, "deno": true,
	"python": true, "python3": true, "ruby": true, "php": true, "perl": true, "java": true, "uv": true}

// commandName names a server: the program, or for an interpreter the
// script or module it runs ("node .../vite.js" -> "vite").
func commandName(argv []string) string {
	if len(argv) == 0 {
		return "?"
	}
	name := filepath.Base(argv[0])
	if strings.HasPrefix(name, "python") {
		name = "python" // python3.12
	}
	if !interpreters[name] {
		return filepath.Base(argv[0])
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-m" && i+1 < len(argv): // python -m http.server
			return argv[i+1]
		case a == "run" || a == "x" || a == "exec": // bun run dev, deno run
			continue
		case strings.HasPrefix(a, "-"):
			continue
		}
		base := filepath.Base(a)
		for _, ext := range []string{".js", ".mjs", ".cjs", ".ts", ".py", ".rb"} {
			base = strings.TrimSuffix(base, ext)
		}
		if base == "index" || base == "main" || base == "cli" {
			// node_modules/vite/bin/vite.js is "vite"; .../next/dist/bin/next
			// is "next"; but node_modules/foo/cli.js: name it by package.
			if pkg := packageOf(a); pkg != "" {
				return pkg
			}
		}
		return base
	}
	return name
}

// packageOf returns the npm package a path is inside, if any.
func packageOf(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i := len(parts) - 2; i >= 0; i-- {
		if parts[i] == "node_modules" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func (p *ports) Draw(w, h int, err error) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	info := ""
	if n := len(p.rows); n > 0 {
		info = fmt.Sprintf("%d listening", n)
	}
	lines := []string{title(w, "⇄", "ports", info), ""}
	if len(p.rows) == 0 {
		if err == nil {
			lines = append(lines, dim+"  Nothing listening."+reset)
			if h > 5 {
				lines = append(lines, "", dim+"  Start a dev server in any tab and"+reset,
					dim+"  it shows up here."+reset)
			}
		}
		return fill(lines, h, errLine(err, w))
	}
	for _, r := range p.rows {
		if len(lines) >= h-boolInt(err != nil) {
			break
		}
		lines = append(lines, p.row(r, w))
	}
	return fill(lines, h, errLine(err, w))
}

// row draws " :5173 * vite        ~/app · tab 3": the port opens in the
// browser, the place goes to the pane.
func (p *ports) row(r portRow, w int) string {
	pub := " "
	if r.public {
		pub = yellow + "*" + reset // reachable from the network
	}
	port := link(fmt.Sprintf("http://localhost:%d", r.port), bold+fmt.Sprintf(":%-5d", r.port)+reset)
	left := " " + port + pub + " "
	cmdW := min(14, max(6, w-width(left)-16))
	cmd := truncate(r.command, cmdW)
	pad := strings.Repeat(" ", max(0, cmdW-width(cmd)))
	if r.pid == 0 {
		cmd = dim + "?" + reset // another user's process
	}
	left += cmd + pad
	room := w - width(left) - 2
	if r.pane == nil || room < 6 {
		return left
	}
	where := "tab " + r.pane.tab
	if dir := shortDir(r.pane.cwd); dir != "" && room >= len(where)+6 {
		where = truncateLeft(dir, room-len(where)-3) + " · " + where
	}
	return left + "  " + link(fmt.Sprintf("germ-pane:%d/%s", r.pane.window, r.pane.ref), dim+where+reset)
}

// shortDir shows a directory with ~ for home.
func shortDir(d string) string {
	if d == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil {
		if d == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(d, home+"/"); ok {
			return "~/" + rest
		}
	}
	return d
}

// tabLabel is a pane's tab as the tab bar numbers it (older germs don't
// say: their position).
func tabLabel(p control.PaneInfo) string {
	if p.TabLabel != "" {
		return p.TabLabel
	}
	return strconv.Itoa(p.Tab)
}

// germPanes lists the panes of every germ window, with their processes.
func germPanes() []paneRef {
	var out []paneRef
	for _, sock := range control.Sockets() {
		c, err := control.Dial(sock)
		if err != nil {
			continue
		}
		resp, err := c.Call(control.Request{Cmd: control.CmdListPanes})
		c.Close()
		if err != nil {
			continue
		}
		for _, pi := range resp.Panes {
			if pi.Pid <= 0 {
				continue
			}
			ref := pi.Tmux
			if ref == "" {
				ref = strconv.Itoa(pi.ID)
			}
			out = append(out, paneRef{pid: pi.Pid, window: resp.Window, ref: ref, tab: tabLabel(pi), cwd: pi.Cwd})
		}
	}
	return out
}

// truncateLeft cuts s to w cells from the left, keeping its end ("…/app").
func truncateLeft(s string, w int) string {
	r := []rune(s)
	if w <= 0 {
		return ""
	}
	if len(r) <= w {
		return s
	}
	return "…" + string(r[len(r)-w+1:])
}
