package shellint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zach-monroe/germ/internal/pty"
)

func TestEnvOnlyForFish(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if Env([]string{"/bin/sh"}) != nil {
		t.Error("sh should get no integration env")
	}
	env := Env([]string{"/usr/bin/fish"})
	if len(env) != 2 || !strings.HasPrefix(env[0], "XDG_DATA_DIRS=") {
		t.Fatalf("env = %v", env)
	}
	dir := strings.SplitN(strings.TrimPrefix(env[0], "XDG_DATA_DIRS="), ":", 2)[0]
	if _, err := os.Stat(filepath.Join(dir, "fish", "vendor_conf.d", "germ-shell-integration.fish")); err != nil {
		t.Error(err)
	}
}

// TestFishLoadsIntegration runs a real interactive fish and checks it
// emits the OSC 133 marks.
func TestFishLoadsIntegration(t *testing.T) {
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not installed")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // skip the user's own config
	cmd := exec.Command("fish", "-i", "-c", "true")
	cmd.Env = append(os.Environ(), "TERM_PROGRAM=germ")
	cmd.Env = append(cmd.Env, Env([]string{"fish"})...)
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "\x1b]7;file://") {
		t.Errorf("fish did not report cwd; output %q", out)
	}
}

// runShell drives an interactive shell in a pty with an empty HOME (so no
// personal config loads) and returns everything it printed.
func runShell(t *testing.T, argv []string, input string) string {
	t.Helper()
	if _, err := exec.LookPath(argv[0]); err != nil {
		t.Skip(argv[0], "not installed")
	}
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	env := append([]string{"HOME=" + home, "TERM_PROGRAM=germ", "TERM=xterm-256color", "PS1=$ "}, Env(argv)...)
	s, err := pty.Start(argv, 80, 24, env...)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := s.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				close(done)
				return
			}
		}
	}()
	time.Sleep(500 * time.Millisecond)
	for _, line := range strings.Split(input, "\n") {
		s.Write([]byte(line + "\r"))
		time.Sleep(300 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	return out.String()
}

func checkMarks(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{"\x1b]133;A\a", "\x1b]133;B\a", "\x1b]133;C\a", "\x1b]133;D;3\a", "\x1b]7;file://"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output %q", want, out)
		}
	}
}

func TestZshIntegration(t *testing.T) {
	out := runShell(t, []string{"zsh", "-i"}, "cd /tmp\n(exit 3)\necho $ZDOTDIR-$+GERM_ORIG_ZDOTDIR\nexit")
	checkMarks(t, out)
	if !strings.Contains(out, "\x1b]133;C\a-0") {
		t.Errorf("ZDOTDIR not restored: %q", out)
	}
}

func TestBashIntegration(t *testing.T) {
	out := runShell(t, []string{"bash", "-i"}, "cd /tmp\n(exit 3)\necho pc=$(env | grep -c ^PROMPT_COMMAND=)\nexit")
	checkMarks(t, out)
	if !strings.Contains(out, "pc=0") {
		t.Errorf("PROMPT_COMMAND leaked into children: %q", out)
	}
}
