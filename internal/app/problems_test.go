package app

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCrashLog(t *testing.T) {
	if os.Getenv("GERM_CRASH_HELPER") == "1" {
		RecordCrashes()
		done := make(chan bool)
		go func() { panic("boom from a goroutine") }()
		<-done
		return
	}
	state := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashLog$")
	cmd.Env = append(os.Environ(), "GERM_CRASH_HELPER=1", "XDG_STATE_HOME="+state)
	if err := cmd.Run(); err == nil {
		t.Fatal("helper didn't crash")
	}
	time.Sleep(50 * time.Millisecond)

	t.Setenv("XDG_STATE_HOME", state)
	cleanCrashTemps() // what the next launch does
	log, err := os.ReadFile(CrashLog())
	if err != nil || !strings.Contains(string(log), "boom from a goroutine") {
		t.Fatalf("crash log: %v %q", err, log)
	}
	if _, err := os.Stat(CrashLog() + ".new"); err != nil {
		t.Error("crash not marked for reporting")
	}

	// A clean run leaves the last crash alone.
	RecordCrashes()
	RemoveCrashTemp()
	cleanCrashTemps()
	if log2, _ := os.ReadFile(CrashLog()); string(log2) != string(log) {
		t.Error("a clean run replaced the crash log")
	}
}
