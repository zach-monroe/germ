package terminfo

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestInstallAndQuery(t *testing.T) {
	if _, err := exec.LookPath("tic"); err != nil {
		t.Skip("tic not installed")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	env := Env()
	if len(env) != 2 || env[0] != "TERM=xterm-germ" {
		t.Fatalf("env = %v", env)
	}
	cmd := exec.Command("infocmp", "-x", "xterm-germ")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("infocmp: %v %s", err, out)
	}
	for _, cap := range []string{"Tc", "RGB", "Smulx=", "Sync=", "Ms="} {
		if !strings.Contains(string(out), cap) {
			t.Errorf("missing %s", cap)
		}
	}
	if Env() == nil { // second call: cached path
		t.Error("cached install failed")
	}
}
