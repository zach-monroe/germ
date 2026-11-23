package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	if s := slug("Fix the login bug in auth/session.go!"); s != "fix-the-login-bug-in-auth" {
		t.Errorf("slug = %q", s)
	}
	if s := slug("   "); !strings.HasPrefix(s, "agent-") {
		t.Errorf("empty slug = %q", s)
	}
}

func TestExpandLaunch(t *testing.T) {
	if got := expandLaunch([]string{"claude", "{task}"}, "do it"); !reflect.DeepEqual(got, []string{"claude", "do it"}) {
		t.Errorf("got %q", got)
	}
	if got := expandLaunch([]string{"claude", "{task}"}, ""); !reflect.DeepEqual(got, []string{"claude"}) {
		t.Errorf("got %q", got)
	}
	if got := expandLaunch([]string{"aider", "--message={task}"}, "x"); got[1] != "--message=x" {
		t.Errorf("got %q", got)
	}
}
