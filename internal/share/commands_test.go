package share

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	people := []Participant{{Name: "zach@box", Host: true}, {ID: 7, Name: "alice@laptop"}, {ID: 9, Name: "bob (browser)", Web: true}}
	st := State{Current: "@1", Tabs: []Tab{
		{Window: "@1", Index: 1, Name: "zsh"},
		{Window: "@2", Index: 2, Name: "nvim", Helm: 7},
		{Window: "@3", Index: 3, Owner: 9, Helm: 9},
	}}
	cases := []struct {
		line    string
		name    string
		actions []Action
		err     string
		note    string
	}{
		{line: "/rotate", name: "newlink", actions: []Action{{Op: OpRotate}}},
		{line: "/q", name: "stop", actions: []Action{{Op: OpStop}}},
		{line: "/y", name: "yes"},
		{line: "/remove alice", name: "kick", actions: []Action{{Op: OpKick, Person: 7}}},
		{line: "/mute #2", name: "mute", actions: []Action{{Op: OpMute, Person: 9}}},
		{line: "/unmute bob", name: "unmute", actions: []Action{{Op: OpUnmute, Person: 9}}},
		{line: "/mute", err: "whom?"},
		{line: "/mute carol", err: "nobody here called carol"},
		{line: "/mute zach", err: "nobody here called zach"}, // not the host
		{line: "/helm", name: "helm"},
		{line: "/hand alice", name: "helm", actions: []Action{{Op: OpHand, Window: "@1", Person: 7}}, note: "⎈ gave alice the helm of tab 1 (zsh)"},
		{line: "/give 1 2", name: "give", actions: []Action{{Op: OpGive, Window: "@2", Person: 7}}},
		{line: "/give alice 5", err: "no tab 5"},
		// /take with no tab: this one if a guest has it, else wherever they do.
		{line: "/take", name: "take", actions: []Action{{Op: OpTake, Window: "@2"}, {Op: OpTake, Window: "@3"}}, note: "⎈ took the helm back (2 tabs)"},
		{line: "/take 1", name: "take", note: "⎈ you already have the helm there"},
		{line: "/untake all", name: "takeback", actions: []Action{{Op: OpUntake, Window: "@2"}, {Op: OpUntake, Window: "@3"}}},
		{line: "/takeback 3", name: "takeback", actions: []Action{{Op: OpUntake, Window: "@3"}}, note: "took back 1 tab"},
		{line: "/branch bob fix the login", name: "branch"},
		{line: "/frob", err: "no command /frob"},
	}
	for _, c := range cases {
		got, err := ParseCommand(c.line, people, st, st.Current)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.line, err, c.err)
			}
			continue
		}
		if err != nil || got.Name != c.name || !reflect.DeepEqual(got.Actions, c.actions) || (c.note != "" && got.Note != c.note) {
			t.Errorf("%s: got %q %+v note %q err %v", c.line, got.Name, got.Actions, got.Note, err)
		}
	}
	if c, _ := ParseCommand("/branch bob fix the login", people, st, ""); c.Person.ID != 9 || c.Task != "fix the login" {
		t.Errorf("branch: %+v", c)
	}
	// /take from a tab a guest has takes just that one.
	if c, _ := ParseCommand("/take", people, st, "@2"); !reflect.DeepEqual(c.Actions, []Action{{Op: OpTake, Window: "@2"}}) {
		t.Errorf("take on @2: %+v", c.Actions)
	}
	// No current tab, several tabs: which one?
	if _, err := ParseCommand("/give alice", people, st, ""); err == nil {
		t.Error("give without a tab or a current one: no error")
	}
	if !IsCommand("/help") || IsCommand("//not") || IsCommand("hi") {
		t.Error("IsCommand")
	}
}
