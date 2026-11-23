package shellint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvOnlyForFish(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if Env([]string{"/bin/bash"}) != nil {
		t.Error("bash should get no integration env")
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
