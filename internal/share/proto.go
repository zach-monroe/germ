package share

import (
	"strings"
	"unicode"
)

// Messages inside the encrypted tunnel start with a type byte.
const (
	msgHello    byte = 'H' // guest -> host: JSON Hello
	msgWelcome  byte = 'W' // host -> guest: JSON Welcome
	msgData     byte = 'D' // tmux control-mode stream, both directions
	msgPresence byte = 'P' // host -> guest: JSON []Participant
	msgBye      byte = 'B' // host -> guest: reason text
	msgResize   byte = 'R' // web guest -> host: JSON Size
)

// Size is a web guest's terminal size.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Hello introduces a guest.
type Hello struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	// Kind is "web" for the browser viewer, which gets a plain terminal
	// stream (tmux attach) instead of tmux control mode.
	Kind string `json:"kind,omitempty"`
	Size
}

// Welcome tells a guest what it joined.
type Welcome struct {
	Host     string `json:"host"`    // user@machine
	Session  string `json:"session"` // tmux session
	ReadOnly bool   `json:"readonly"`
}

// Participant is someone in the session.
type Participant struct {
	ID       uint32 `json:"id,omitempty"` // guest number (0 for the host)
	Name     string `json:"name"`
	Web      bool   `json:"web,omitempty"` // in a browser
	ReadOnly bool   `json:"readonly,omitempty"`
	Host     bool   `json:"host,omitempty"`
	Typing   bool   `json:"typing,omitempty"`
}

// protocolVersion 2: NaCl secretbox (1 was AES-GCM).
const protocolVersion = 2

// Guests drive the host's tmux through tmux control-mode commands, one per
// line. tmux's read-only client mode only blocks typing, not commands
// like new-window or kill-server, so the host filters every line itself.

// readOnlyCommands may be run by read-only guests: what germ needs to show
// a session, nothing that changes it.
var readOnlyCommands = map[string]bool{
	"list-windows": true, "lsw": true,
	"list-panes": true, "lsp": true,
	"capture-pane": true, "capturep": true,
	"display-message": true, "display": true,
	"refresh-client": true, "refresh": true,
	"show-options": true, "show": true,
}

// deniedCommands are refused even for guests who can type: they'd end or
// take over the host's session rather than work in it.
var deniedCommands = map[string]bool{
	"kill-server": true, "kill-session": true, "lock-server": true, "lock-session": true,
	"lock-client": true, "suspend-client": true, "switch-client": true, "attach-session": true,
	"attach": true, "new-session": true, "new": true, "source-file": true, "source": true,
	"set-environment": true, "setenv": true,
}

// blockedCommand fails harmlessly, so the guest's reply queue stays in step.
const blockedCommand = "select-window -t @999999999"

// filterCommand vets a guest's tmux command line, returning what to send to
// tmux and whether the original was allowed. own is the guest's own
// session ID ("$4"), which watchers may switch windows in.
func filterCommand(line string, readOnly bool, own string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" {
		return line, true
	}
	// One command per line: ";" chains commands, and "#(" in a format
	// runs a shell command.
	if strings.Contains(t, ";") || strings.Contains(t, "#(") || strings.ContainsAny(t, "\x00\r") {
		return blockedCommand, false
	}
	name := t
	if i := strings.IndexFunc(t, unicode.IsSpace); i >= 0 {
		name = t[:i]
	}
	if deniedCommands[name] {
		return blockedCommand, false
	}
	if readOnly {
		// Watchers may switch windows in their own (grouped) session only:
		// "$own:@N". (A bare "@N" may resolve to another session in the
		// group, such as yours.)
		if name == "select-window" || name == "selectw" {
			if f := strings.Fields(t); len(f) == 3 && f[1] == "-t" && own != "" {
				if s, w, ok := strings.Cut(f[2], ":"); ok && s == own && isWindowID(w) {
					return t, true
				}
			}
			return blockedCommand, false
		}
		if !readOnlyCommands[name] {
			return blockedCommand, false
		}
		if (name == "display" || name == "display-message") && !hasFlag(t, 'p') {
			return blockedCommand, false // only -p (print to the guest), not messages on the host's screen
		}
		if (name == "show" || name == "show-options") && hasFlag(t, 'u') {
			return blockedCommand, false
		}
	}
	return t, true
}

// hasFlag reports whether a single-letter flag appears among the leading
// options (e.g. -p, or combined like -pv).
func hasFlag(line string, f byte) bool {
	for _, w := range strings.Fields(line)[1:] {
		if !strings.HasPrefix(w, "-") || w == "--" {
			return false
		}
		if strings.IndexByte(w[1:], f) >= 0 {
			return true
		}
	}
	return false
}

func isWindowID(s string) bool {
	if len(s) < 2 || s[0] != '@' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
