package widgets

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// system shows CPU, memory, disk, network and battery, read from /proc and
// /sys. Options: disks = ["/", "/home"] (default: / and your home's
// filesystem if it's a different one).
type system struct {
	proc, sys string // roots, for tests
	disks     []string

	mu        sync.Mutex
	cpu       []float64 // percent busy, newest last
	rx, tx    []float64 // bytes/s
	prevCPU   cpuTimes
	prevNet   [2]uint64
	prevAt    time.Time
	memUsed   uint64
	memTotal  uint64
	diskStats []diskStat
	battery   *battery
	load      string
	uptime    time.Duration
}

type cpuTimes struct{ busy, total uint64 }

type diskStat struct {
	path        string
	used, total uint64
}

type battery struct {
	percent int
	status  string // Charging, Discharging, Full, Not charging
}

const sysHistory = 120 // samples kept for sparklines (4 min at 2 s)

func newSystem(o Options) Widget {
	s := &system{proc: "/proc", sys: "/sys"}
	if d := o.Get("disks", ""); d != "" {
		for _, p := range strings.Split(d, ",") {
			if p = strings.TrimSpace(p); p != "" {
				s.disks = append(s.disks, expandTilde(p))
			}
		}
	} else {
		s.disks = []string{"/"}
		if home, err := os.UserHomeDir(); err == nil && !sameFS("/", home) {
			s.disks = append(s.disks, home)
		}
	}
	return s
}

func (s *system) Every() time.Duration { return 2 * time.Second }
func (s *system) Tick() time.Duration  { return 0 }

func (s *system) Update(context.Context) error {
	now := time.Now()
	cpu, cpuErr := readCPU(filepath.Join(s.proc, "stat"))
	used, total, memErr := readMem(filepath.Join(s.proc, "meminfo"))
	rx, tx, _ := readNet(filepath.Join(s.proc, "net/dev"))
	var disks []diskStat
	for _, d := range s.disks {
		var st unix.Statfs_t
		if unix.Statfs(d, &st) == nil && st.Blocks > 0 {
			bs := uint64(st.Bsize)
			disks = append(disks, diskStat{d, (st.Blocks - st.Bfree) * bs, st.Blocks * bs})
		}
	}
	bat := readBattery(filepath.Join(s.sys, "class/power_supply"))
	load := ""
	if b, err := os.ReadFile(filepath.Join(s.proc, "loadavg")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			load = f[0]
		}
	}
	var up time.Duration
	if b, err := os.ReadFile(filepath.Join(s.proc, "uptime")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if secs, err := strconv.ParseFloat(f[0], 64); err == nil {
				up = time.Duration(secs) * time.Second
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Rates need a previous sample; one from long ago (the widget slept
	// while hidden) would average over the gap, so start over.
	fresh := !s.prevAt.IsZero() && now.Sub(s.prevAt) < 10*time.Second
	if cpuErr == nil {
		if fresh && cpu.total > s.prevCPU.total {
			pct := float64(cpu.busy-s.prevCPU.busy) / float64(cpu.total-s.prevCPU.total) * 100
			s.cpu = push(s.cpu, math.Max(0, math.Min(100, pct)))
		}
		s.prevCPU = cpu
	}
	if fresh {
		secs := now.Sub(s.prevAt).Seconds()
		if rx >= s.prevNet[0] && tx >= s.prevNet[1] {
			s.rx = push(s.rx, float64(rx-s.prevNet[0])/secs)
			s.tx = push(s.tx, float64(tx-s.prevNet[1])/secs)
		}
	}
	s.prevNet, s.prevAt = [2]uint64{rx, tx}, now
	s.memUsed, s.memTotal = used, total
	s.diskStats, s.battery, s.load, s.uptime = disks, bat, load, up
	if cpuErr != nil {
		return cpuErr
	}
	return memErr
}

func push(h []float64, v float64) []float64 {
	h = append(h, v)
	if len(h) > sysHistory {
		h = h[len(h)-sysHistory:]
	}
	return h
}

// readCPU reads the aggregate "cpu" line of /proc/stat.
func readCPU(path string) (cpuTimes, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cpuTimes{}, err
	}
	line, _, _ := bytes.Cut(b, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}, fmt.Errorf("%s: no cpu line", path)
	}
	var t cpuTimes
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		if i >= 8 { // guest time is already counted in user
			break
		}
		t.total += n
		if i != 3 && i != 4 { // idle, iowait
			t.busy += n
		}
	}
	return t, nil
}

// readMem returns used (total - available) and total bytes.
func readMem(path string) (used, total uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		kb, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			total = kb << 10
		case "MemAvailable":
			avail = kb << 10
		}
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("%s: no MemTotal", path)
	}
	return total - min(avail, total), total, nil
}

// readNet sums received and sent bytes over every interface but loopback
// and virtual ones (docker, veth, bridges), which would count traffic twice.
func readNet(path string) (rx, tx uint64, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx, nil
}

// readBattery reads the first battery under /sys/class/power_supply.
func readBattery(dir string) *battery {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if typ, _ := os.ReadFile(filepath.Join(p, "type")); strings.TrimSpace(string(typ)) != "Battery" {
			continue
		}
		c, err := os.ReadFile(filepath.Join(p, "capacity"))
		if err != nil {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(c)))
		if err != nil {
			continue
		}
		st, _ := os.ReadFile(filepath.Join(p, "status"))
		return &battery{percent: n, status: strings.TrimSpace(string(st))}
	}
	return nil
}

func sameFS(a, b string) bool {
	var sa, sb unix.Stat_t
	return unix.Stat(a, &sa) == nil && unix.Stat(b, &sb) == nil && sa.Dev == sb.Dev
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func (s *system) Draw(w, h int, err error) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := ""
	if s.load != "" {
		info = "load " + s.load
	}
	if s.uptime > 0 && w >= 34 {
		info = "up " + shortDuration(s.uptime) + "  " + info
	}
	lines := []string{title(w, "⚙", "system", info)}

	// Each row: a label, a value, then a bar or sparkline.
	const labelW, valueW = 7, 13
	graphW := w - labelW - valueW - 2
	row := func(label, value, graph string) string {
		if graphW < 4 { // too narrow for graphs: just "cpu 12%"
			return " " + dim + label + reset + " " + value
		}
		r := " " + dim + fmt.Sprintf("%-*s", labelW-1, label) + reset + value + strings.Repeat(" ", max(0, valueW-width(value)))
		return r + " " + graph
	}
	var rows []string
	if len(s.cpu) > 0 {
		cur := s.cpu[len(s.cpu)-1]
		rows = append(rows, row("cpu", fmt.Sprintf("%3.0f%%", cur), level(cur)+fixedSpark(tail(s.cpu, graphW), 100)+reset))
	}
	if s.memTotal > 0 {
		pct := float64(s.memUsed) / float64(s.memTotal) * 100
		rows = append(rows, row("mem", gib(s.memUsed)+"/"+gib(s.memTotal), bar(pct, graphW, level(pct))))
	}
	for _, d := range s.diskStats {
		pct := float64(d.used) / float64(d.total) * 100
		label := "disk"
		if len(s.diskStats) > 1 {
			label = shortPath(d.path)
		}
		rows = append(rows, row(label, gib(d.used)+"/"+gib(d.total), bar(pct, graphW, level(pct))))
	}
	if len(s.rx) > 0 {
		rows = append(rows,
			row("net ↓", rate(s.rx[len(s.rx)-1]), cyan+autoSpark(tail(s.rx, graphW))+reset),
			row("net ↑", rate(s.tx[len(s.tx)-1]), magenta+autoSpark(tail(s.tx, graphW))+reset))
	}
	if b := s.battery; b != nil {
		state := ""
		switch b.status {
		case "Charging":
			state = " charging"
		case "Full":
			state = " full"
		}
		rows = append(rows, row("bat", fmt.Sprintf("%d%%", b.percent)+dim+state+reset, bar(float64(b.percent), graphW, level(100-float64(b.percent)))))
	}
	if len(rows) == 0 && err == nil {
		rows = append(rows, center(dim+"measuring…"+reset, w))
	}
	// A blank line under the title, and between rows, if there's room.
	avail := h - 1
	if err != nil {
		avail--
	}
	gap := len(rows) > 0 && avail >= 2*len(rows)+1
	if avail > len(rows) {
		lines = append(lines, "")
	}
	for i, r := range rows {
		if gap && i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, r)
	}
	if len(lines) > h-boolInt(err != nil) {
		lines = lines[:max(1, h-boolInt(err != nil))]
	}
	return fill(lines, h, errLine(err, w))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// level colors a load percentage: green, then yellow, then red.
func level(pct float64) string {
	switch {
	case pct >= 85:
		return red
	case pct >= 60:
		return yellow
	}
	return green
}

// bar draws a w-cell meter filled to pct in color c.
func bar(pct float64, w int, c string) string {
	if w <= 0 {
		return ""
	}
	n := int(math.Round(math.Max(0, math.Min(100, pct)) / 100 * float64(w)))
	return c + strings.Repeat("█", n) + reset + dim + strings.Repeat("░", w-n) + reset
}

// fixedSpark is a sparkline scaled 0..hi (so a quiet CPU looks quiet).
func fixedSpark(vals []float64, hi float64) string {
	blocks := []rune("▁▂▃▄▅▆▇█")
	out := make([]rune, len(vals))
	for i, v := range vals {
		if math.IsNaN(v) {
			out[i] = ' '
			continue
		}
		k := int(math.Round(math.Max(0, math.Min(hi, v)) / hi * float64(len(blocks)-1)))
		out[i] = blocks[k]
	}
	return string(out)
}

// autoSpark scales to the largest value shown, with a floor so an idle
// link (a few hundred bytes/s) doesn't fill the graph.
func autoSpark(vals []float64) string {
	hi := 64.0 * 1024
	for _, v := range vals {
		if !math.IsNaN(v) {
			hi = math.Max(hi, v)
		}
	}
	return fixedSpark(vals, hi)
}

// tail returns the last n values, one per cell, padded at the front with
// NaN (drawn blank) so the newest is always at the right edge.
func tail(v []float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	if len(v) >= n {
		return v[len(v)-n:]
	}
	out := make([]float64, n-len(v), n)
	for i := range out {
		out[i] = math.NaN()
	}
	return append(out, v...)
}

func gib(b uint64) string {
	g := float64(b) / (1 << 30)
	switch {
	case g >= 1000:
		return fmt.Sprintf("%.1fT", g/1024)
	case g >= 100:
		return fmt.Sprintf("%.0fG", g)
	}
	return fmt.Sprintf("%.1fG", g)
}

func rate(bps float64) string {
	switch {
	case bps >= 1<<30:
		return fmt.Sprintf("%.1f GB/s", bps/(1<<30))
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0f KB/s", bps/(1<<10))
	}
	return fmt.Sprintf("%.0f B/s", bps)
}

func shortDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hrs := int(d.Hours()) % 24
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hrs)
	case hrs > 0:
		return fmt.Sprintf("%dh %dm", hrs, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// shortPath names a mount point in at most 5 cells: "/", "~", "/data"...
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && p == home {
		return "~"
	}
	return truncate(filepath.Base(p), 5)
}
