package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// Problems with the config, tmux, or a previous crash are shown in the
// window (the tab bar's status area, and a desktop notification): germ is
// usually started from a launcher, where nobody sees stderr.

// reportProblems shows problems found while loading the config.
func (a *App) reportProblems(problems []string) {
	if len(problems) == 0 {
		return
	}
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "germ: %s: %s\n", a.opts.ConfigPath, p)
	}
	msg := problems[0]
	if len(problems) > 1 {
		msg += fmt.Sprintf(" (+%d more)", len(problems)-1)
	}
	a.setStatus("⚠ config: "+msg, 20*time.Second)
	body := strings.Join(problems, "\n")
	if a.opts.ConfigPath != "" {
		body += "\n\n" + a.opts.ConfigPath
	}
	go desktopNotify("germ: config problem", body)
}

// StateDir is where germ keeps logs ($XDG_STATE_HOME/germ).
func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "germ")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "germ")
}

// CrashLog is where a crash's stack trace goes.
func CrashLog() string { return filepath.Join(StateDir(), "crash.log") }

// RecordCrashes sends the runtime's fatal error output (an unrecovered
// panic in any goroutine) to CrashLog as well as stderr. The previous log
// is kept until a new crash replaces it; reportLastCrash mentions it once.
func RecordCrashes() {
	if err := os.MkdirAll(StateDir(), 0o755); err != nil {
		return
	}
	// Write to a fresh file and swap it in only if something was written,
	// so a clean exit doesn't wipe the last crash.
	tmp := filepath.Join(StateDir(), fmt.Sprintf("crash.%d.tmp", os.Getpid()))
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	debug.SetCrashOutput(f, debug.CrashOptions{})
	f.Close() // SetCrashOutput keeps its own descriptor
	cleanCrashTemps()
}

// cleanCrashTemps turns non-empty leftovers of crashed processes into the
// crash log and removes empty ones (of processes that are gone).
func cleanCrashTemps() {
	matches, _ := filepath.Glob(filepath.Join(StateDir(), "crash.*.tmp"))
	for _, m := range matches {
		var pid int
		fmt.Sscanf(filepath.Base(m), "crash.%d.tmp", &pid)
		if pid == os.Getpid() || processAlive(pid) {
			continue
		}
		if fi, err := os.Stat(m); err == nil && fi.Size() > 0 {
			os.Rename(m, CrashLog())
			os.WriteFile(CrashLog()+".new", nil, 0o644) // not reported yet
		} else {
			os.Remove(m)
		}
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

// RemoveCrashTemp deletes this process's (empty) crash file on a clean exit.
func RemoveCrashTemp() {
	os.Remove(filepath.Join(StateDir(), fmt.Sprintf("crash.%d.tmp", os.Getpid())))
}

// reportLastCrash says, once, that germ crashed last time.
func (a *App) reportLastCrash() {
	marker := CrashLog() + ".new"
	if _, err := os.Stat(marker); err != nil {
		return
	}
	os.Remove(marker)
	a.setStatus("⚠ germ crashed last time · log: "+tildeHome(CrashLog()), 30*time.Second)
	go desktopNotify("germ crashed last time",
		"The error is in "+CrashLog()+". Please report it at https://github.com/zach-monroe/germ/issues")
}

func tildeHome(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}
