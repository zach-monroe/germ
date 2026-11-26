package pty

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func readAll(t *testing.T, s *Session) string {
	t.Helper()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&buf, s) // returns EIO when the child exits; that's expected
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading pty output")
	}
	return buf.String()
}

func TestStartEcho(t *testing.T) {
	s, err := Start([]string{"/bin/sh", "-c", "echo hello-germ"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if out := readAll(t, s); !strings.Contains(out, "hello-germ") {
		t.Fatalf("output %q missing hello-germ", out)
	}
}

func TestSizeAndEnv(t *testing.T) {
	s, err := Start([]string{"/bin/sh", "-c", "stty size; echo $TERM_PROGRAM"}, 132, 40)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := readAll(t, s)
	if !strings.Contains(out, "40 132") {
		t.Errorf("stty size: got %q, want 40 132", out)
	}
	if !strings.Contains(out, "germ") {
		t.Errorf("TERM_PROGRAM not set: %q", out)
	}
}

func TestWriteInput(t *testing.T) {
	s, err := Start([]string{"/bin/sh", "-c", "read x; echo got:$x"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Write([]byte("abc\r")); err != nil {
		t.Fatal(err)
	}
	if out := readAll(t, s); !strings.Contains(out, "got:abc") {
		t.Fatalf("output %q missing got:abc", out)
	}
}

func TestNoEcho(t *testing.T) {
	s, err := Start([]string{"/bin/sh", "-c", "read x; echo got-$x"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.NoEcho(); err != nil {
		t.Fatal(err)
	}
	s.Write([]byte("secret\n"))
	out := readAll(t, s)
	if !strings.Contains(out, "got-secret") {
		t.Fatalf("output %q missing got-secret", out)
	}
	if strings.Count(out, "secret") != 1 {
		t.Errorf("input was echoed: %q", out)
	}
}
