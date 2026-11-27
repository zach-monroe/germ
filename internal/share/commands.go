package share

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The host's /commands, typed in the chat: in germ's chat panel and in the
// share pane. ParseCommand reads them; each UI shows help, copies the link,
// answers requests and opens /branch tabs its own way, and carries out the
// Actions.

// HostHelp lists the host's /commands.
var HostHelp = []string{
	"/copy: copy the invite link · /newlink: a new link (the old one stops working)",
	"/helm NAME [TAB]: hand NAME the helm · /take [TAB|all]: take it back",
	"/give NAME [TAB]: make it NAME's tab · /takeback [TAB|all]: take it back",
	"/branch NAME [task]: a tab on a new branch for NAME",
	"/kick NAME: remove someone · /mute NAME · /unmute NAME · /save",
	"/yes /no: answer (or y/n, box empty) · /stop: stop sharing",
	"NAME: a name or its number · TAB: a tab's number (default: yours) · //text sends /text",
}

// Command is a parsed /command.
type Command struct {
	// Name is the command's canonical name: help, copy, newlink, save,
	// stop, yes, no, kick, mute, unmute, helm, give, take, takeback or
	// branch.
	Name    string
	Args    []string
	Person  Participant // the NAME argument, when the command takes one
	Task    string      // branch: what they'll work on
	Actions []Action    // what to do in the session
	Note    string      // what happened, to tell the host
}

var commandNames = map[string]string{
	"help": "help", "h": "help", "?": "help",
	"copy": "copy", "c": "copy", "link": "copy",
	"newlink": "newlink", "rotate": "newlink",
	"save": "save",
	"stop": "stop", "quit": "stop", "q": "stop",
	"yes": "yes", "y": "yes", "no": "no", "n": "no",
	"kick": "kick", "remove": "kick",
	"mute": "mute", "unmute": "unmute",
	"helm": "helm", "hand": "helm", "release": "helm",
	"give":     "give",
	"take":     "take",
	"takeback": "takeback", "untake": "takeback",
	"branch": "branch",
}

// IsCommand reports whether a chat line is a /command rather than a
// message ("//text" sends "/text").
func IsCommand(line string) bool {
	return strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//")
}

// ParseCommand reads a /command typed by the host. people is who's here
// (numbered as in the share header, the host first), st the session, and
// current the window of the tab the host is looking at ("" if unknown).
// Errors are messages for the host. helm without arguments is returned
// with no Actions: what it does depends on the UI.
func ParseCommand(line string, people []Participant, st State, current string) (Command, error) {
	word, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	c := Command{Name: commandNames[word], Args: strings.Fields(rest)}
	person := func(i int) error {
		if len(c.Args) <= i {
			return errors.New("whom? · /help")
		}
		p, ok := findPerson(people, c.Args[i])
		if !ok {
			return fmt.Errorf("nobody here called %s", c.Args[i])
		}
		c.Person = p
		return nil
	}
	switch c.Name {
	case "":
		return c, fmt.Errorf("no command /%s · /help lists them", word)
	case "newlink":
		c.Actions = []Action{{Op: OpRotate}}
	case "save":
		c.Actions = []Action{{Op: OpSave}}
	case "stop":
		c.Actions = []Action{{Op: OpStop}}
	case "kick", "mute", "unmute":
		if err := person(0); err != nil {
			return c, err
		}
		op := map[string]string{"kick": OpKick, "mute": OpMute, "unmute": OpUnmute}[c.Name]
		c.Actions = []Action{{Op: op, Person: c.Person.ID}}
	case "helm", "give":
		if c.Name == "helm" && len(c.Args) == 0 {
			return c, nil
		}
		if err := person(0); err != nil {
			return c, err
		}
		t, err := FindTab(st, c.Args, 1, current)
		if err != nil {
			return c, err
		}
		if c.Name == "helm" {
			c.Actions = []Action{{Op: OpHand, Window: t.Window, Person: c.Person.ID}}
			c.Note = "⎈ gave " + ShortName(c.Person.Name) + " the helm of " + st.TabName(t.Window)
		} else {
			c.Actions = []Action{{Op: OpGive, Window: t.Window, Person: c.Person.ID}}
		}
	case "take", "takeback":
		var tabs []Tab
		switch {
		case len(c.Args) > 0 && c.Args[0] == "all",
			// No tab given and a guest doesn't have this one: wherever one does.
			len(c.Args) == 0 && c.Name == "take" && st.HelmOf(current) == 0:
			tabs = st.Tabs
		default:
			t, err := FindTab(st, c.Args, 0, current)
			if err != nil {
				return c, err
			}
			tabs = []Tab{*t}
		}
		for _, t := range tabs {
			switch {
			case c.Name == "take" && t.Helm != 0:
				c.Actions = append(c.Actions, Action{Op: OpTake, Window: t.Window})
			case c.Name == "takeback" && (t.Owner != 0 || t.Helm != 0):
				c.Actions = append(c.Actions, Action{Op: OpUntake, Window: t.Window})
			}
		}
		switch n := len(c.Actions); {
		case n == 0 && c.Name == "take":
			c.Note = "⎈ you already have the helm there"
		case n == 0:
			c.Note = "that's already your tab"
		case c.Name == "take":
			c.Note = fmt.Sprintf("⎈ took the helm back (%s)", plural(n, "tab"))
		default:
			c.Note = fmt.Sprintf("took back %s", plural(n, "tab"))
		}
	case "branch":
		if err := person(0); err != nil {
			return c, err
		}
		c.Task = strings.Join(c.Args[1:], " ")
	}
	return c, nil
}

// findPerson finds a guest by name, short name or number (as listed in
// the share header, where the host is 0).
func findPerson(people []Participant, arg string) (Participant, bool) {
	if n, err := strconv.Atoi(strings.TrimPrefix(arg, "#")); err == nil && n >= 1 && n < len(people) {
		return people[n], true
	}
	for _, p := range people {
		if !p.Host && NameMatches(p.Name, arg) {
			return p, true
		}
	}
	return Participant{}, false
}

// FindTab finds the tab numbered args[i] (by its tmux index), or without
// one the current tab (or the only one).
func FindTab(st State, args []string, i int, current string) (*Tab, error) {
	if len(args) <= i {
		if t := st.Tab(current); t != nil {
			return t, nil
		}
		if len(st.Tabs) == 1 {
			return &st.Tabs[0], nil
		}
		return nil, errors.New("which tab? give its number")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(args[i], "#"))
	for j := range st.Tabs {
		if err == nil && st.Tabs[j].Index == n {
			return &st.Tabs[j], nil
		}
	}
	return nil, fmt.Errorf("no tab %s", args[i])
}

// TabName is "tab 2 (zsh)".
func (s State) TabName(window string) string {
	if t := s.Tab(window); t != nil {
		if t.Name != "" {
			return fmt.Sprintf("tab %d (%s)", t.Index, t.Name)
		}
		return fmt.Sprintf("tab %d", t.Index)
	}
	return "a tab"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
