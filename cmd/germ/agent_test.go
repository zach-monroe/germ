package main

import (
	"reflect"
	"testing"
)

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
