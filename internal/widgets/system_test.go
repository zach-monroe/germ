package widgets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const netDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9999 1 0 0 0 0 0 0 9999 1 0 0 0 0 0 0
  eth0: 1000 1 0 0 0 0 0 0 200 1 0 0 0 0 0 0
 wlan0: 500 1 0 0 0 0 0 0 50 1 0 0 0 0 0 0
veth12: 7777 1 0 0 0 0 0 0 7777 1 0 0 0 0 0 0
`

func TestSystemReaders(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"proc/stat":                            "cpu  100 0 50 800 50 0 0 0 30 0\ncpu0 1 2 3 4\n",
		"proc/meminfo":                         "MemTotal:       8000 kB\nMemFree:        1000 kB\nMemAvailable:   6000 kB\n",
		"proc/net/dev":                         netDev,
		"sys/class/power_supply/AC/type":       "Mains\n",
		"sys/class/power_supply/BAT0/type":     "Battery\n",
		"sys/class/power_supply/BAT0/capacity": "42\n",
		"sys/class/power_supply/BAT0/status":   "Discharging\n",
	})
	cpu, err := readCPU(filepath.Join(root, "proc/stat"))
	if err != nil || cpu.busy != 150 || cpu.total != 1000 { // guest (30) not double-counted
		t.Errorf("cpu = %+v, %v", cpu, err)
	}
	used, total, err := readMem(filepath.Join(root, "proc/meminfo"))
	if err != nil || used != 2000<<10 || total != 8000<<10 {
		t.Errorf("mem = %d/%d, %v", used, total, err)
	}
	rx, tx, err := readNet(filepath.Join(root, "proc/net/dev"))
	if err != nil || rx != 1500 || tx != 250 { // no lo, no veth
		t.Errorf("net = %d/%d, %v", rx, tx, err)
	}
	b := readBattery(filepath.Join(root, "sys/class/power_supply"))
	if b == nil || b.percent != 42 || b.status != "Discharging" {
		t.Errorf("battery = %+v", b)
	}
	if readBattery(filepath.Join(root, "nope")) != nil {
		t.Error("battery from a missing dir")
	}
}

func TestSystemRatesAndDraw(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"proc/stat":    "cpu  100 0 0 900 0 0 0 0 0 0\n",
		"proc/meminfo": "MemTotal: 8000 kB\nMemAvailable: 6000 kB\n",
		"proc/net/dev": netDev,
		"proc/loadavg": "0.42 0.30 0.20 1/100 1\n",
	}
	writeFiles(t, root, files)
	s := &system{proc: filepath.Join(root, "proc"), sys: filepath.Join(root, "sys"), disks: []string{root}}
	s.Update(context.Background())
	if len(s.cpu) != 0 {
		t.Fatal("a rate from one sample")
	}
	// 200 more busy of 400 more total: 50%.
	files["proc/stat"] = "cpu  300 0 0 1100 0 0 0 0 0 0\n"
	writeFiles(t, root, files)
	s.prevAt = time.Now().Add(-2 * time.Second)
	s.Update(context.Background())
	if len(s.cpu) != 1 || s.cpu[0] != 50 {
		t.Fatalf("cpu history = %v", s.cpu)
	}
	if len(s.rx) != 1 || s.rx[0] != 0 {
		t.Errorf("rx history = %v", s.rx)
	}
	// After a long sleep (hidden), no rate averaged over the gap.
	s.prevAt = time.Now().Add(-time.Hour)
	s.Update(context.Background())
	if len(s.cpu) != 1 {
		t.Errorf("rate across a gap: %v", s.cpu)
	}

	for _, sz := range [][2]int{{60, 10}, {30, 4}, {16, 2}, {80, 30}} {
		lines := s.Draw(sz[0], sz[1], nil)
		if len(lines) > sz[1] {
			t.Errorf("%v: %d lines", sz, len(lines))
		}
		for _, l := range lines {
			if width(l) > sz[0] {
				t.Errorf("%v: line too wide (%d): %q", sz, width(l), l)
			}
		}
		if sz[1] >= 4 && !strings.Contains(strings.Join(lines, "\n"), "50%") {
			t.Errorf("%v: no cpu row: %q", sz, lines)
		}
	}
}
