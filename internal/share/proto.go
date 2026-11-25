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
	msgWaiting  byte = 'Q' // host -> guest: waiting for the host to approve the join
	msgChat     byte = 'C' // host -> guest: JSON ChatMsg
	msgHistory  byte = 'Y' // host -> guest: JSON []ChatMsg, the recent chat (on joining)
	msgState    byte = 'S' // host -> guest: JSON State (tabs, the helm, requests)
	msgAction   byte = 'A' // guest -> host: JSON Action
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
	// Client is a random ID the guest keeps across reconnects, so a guest
	// the host let in isn't asked about again after a network blip.
	Client string `json:"client,omitempty"`
	Size
}

// JoinRequest is a guest waiting for the host's approval.
type JoinRequest struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
	Web  bool   `json:"web,omitempty"`
}

// Welcome tells a guest what it joined.
type Welcome struct {
	Host    string `json:"host"`    // user@machine
	Session string `json:"session"` // tmux session
	You     uint32 `json:"you"`     // the guest's person ID (Participant.ID)
}

// Participant is someone in the session. IDs stay the same across
// reconnects.
type Participant struct {
	ID        uint32 `json:"id,omitempty"` // person ID (0 for the host)
	Name      string `json:"name"`
	Web       bool   `json:"web,omitempty"` // in a browser
	Host      bool   `json:"host,omitempty"`
	Typing    bool   `json:"typing,omitempty"`    // typing in the terminal
	Composing bool   `json:"composing,omitempty"` // typing a chat message
	Muted     bool   `json:"muted,omitempty"`
}

// ChatMsg is one line of the session chat. The host assigns Seq, From,
// Name and At, so they can be trusted.
type ChatMsg struct {
	Seq   uint64 `json:"seq,omitempty"` // 0: a notice for one person, not kept
	From  uint32 `json:"from,omitempty"`
	Name  string `json:"name,omitempty"`
	Host  bool   `json:"host,omitempty"`
	Text  string `json:"text"`
	At    int64  `json:"at"`              // Unix milliseconds, host clock
	Event bool   `json:"event,omitempty"` // a session event (joins, the helm)
}

// Tab is one tmux window and who may type in it.
type Tab struct {
	Window string `json:"window"` // tmux window ID, "@3"
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Owner  uint32 `json:"owner,omitempty"` // person ID; 0 the host
	Helm   uint32 `json:"helm,omitempty"`  // who types here now; 0 the host
}

// HelmRequest is someone asking for the helm of a tab.
type HelmRequest struct {
	ID       uint32 `json:"id"`
	From     uint32 `json:"from"`
	Name     string `json:"name"`
	Window   string `json:"window"`
	Approver uint32 `json:"approver"` // who decides (0: the host, who can always decide)
}

// State is the session's tabs and who has the helm, as one person sees it.
type State struct {
	Tabs     []Tab         `json:"tabs"`
	Current  string        `json:"current,omitempty"` // the host's tab
	Requests []HelmRequest `json:"requests,omitempty"`
	You      uint32        `json:"you"`
	// Browser guests: the tab they're looking at, and whether it follows
	// the host's.
	Viewing string `json:"viewing,omitempty"`
	Follow  bool   `json:"follow,omitempty"`
}

// Tab returns the tab for a window ID.
func (s State) Tab(window string) *Tab {
	for i := range s.Tabs {
		if s.Tabs[i].Window == window {
			return &s.Tabs[i]
		}
	}
	return nil
}

// HelmOf says who has the helm of a window (0, the host, if unknown).
func (s State) HelmOf(window string) uint32 {
	if t := s.Tab(window); t != nil {
		return t.Helm
	}
	return 0
}

// Action is something a person does: guests send them to the host, and
// the host's germ sends the host's to `germ share`.
type Action struct {
	Op      string `json:"op"`
	Text    string `json:"text,omitempty"`    // say
	Window  string `json:"window,omitempty"`  // request, release, take, give, untake, view
	Request uint32 `json:"request,omitempty"` // decide, join
	Person  uint32 `json:"person,omitempty"`  // give, mute, unmute
	Allow   bool   `json:"allow,omitempty"`   // decide, join
}

// Actions.
const (
	OpSay       = "say"       // send Text to the chat
	OpComposing = "composing" // typing a chat message (send every few seconds)
	OpRequest   = "request"   // ask for the helm of Window
	OpRelease   = "release"   // hand the helm of Window back
	OpDecide    = "decide"    // answer helm request Request
	OpView      = "view"      // browser guests: look at Window
	OpFollow    = "follow"    // browser guests: follow the host's tab
	// The host only:
	OpTake   = "take"   // take the helm of Window
	OpGive   = "give"   // make Person the owner (and holder) of Window
	OpUntake = "untake" // take Window back from its owner
	OpMute   = "mute"
	OpUnmute = "unmute"
	OpJoin   = "join" // answer join request Request
	OpSave   = "save" // save the chat to a file
)

// Update is what `germ share` tells the host's germ window about the
// session (over the control socket).
type Update struct {
	Session string        `json:"session"`
	Link    string        `json:"link,omitempty"`
	People  []Participant `json:"people,omitempty"`
	State   State         `json:"state"`
	Joins   []JoinRequest `json:"joins,omitempty"` // waiting to be let in
	Chat    []ChatMsg     `json:"chat,omitempty"`  // messages after the window's last
	Notice  string        `json:"notice,omitempty"`
	Ended   bool          `json:"ended,omitempty"` // sharing stopped
}

// protocolVersion 3: one link, chat, and the helm (2: NaCl secretbox; 1
// was AES-GCM).
const protocolVersion = 3

// Guests drive the host's tmux through tmux control-mode commands, one per
// line. The host vets every line: commands that only look are allowed,
// commands that type into or change a tab only for the tab's helm holder,
// and everything else (tmux key bindings and its command prompt
// included) never.

// viewCommands may be run by anyone: what germ needs to show a session,
// nothing that changes it.
var viewCommands = map[string]bool{
	"list-windows": true, "lsw": true,
	"list-panes": true, "lsp": true,
	"capture-pane": true, "capturep": true,
	"display-message": true, "display": true,
	"refresh-client": true, "refresh": true,
	"show-options": true, "show": true,
	"detach-client": true, "detach": true,
}

// helmCommands type into or change the tab their -t target is in.
var helmCommands = map[string]bool{
	"send-keys": true, "send": true,
	"select-pane": true, "selectp": true,
	"resize-pane": true, "resizep": true,
	"split-window": true, "splitw": true,
	"kill-pane": true, "killp": true,
	"kill-window": true, "killw": true,
	"rename-window": true, "renamew": true,
	"select-layout": true, "selectl": true,
	"copy-mode": true,
}

// blockedCommand fails harmlessly, so the guest's reply queue stays in step.
const blockedCommand = "select-window -t @999999999"

// Command classes (filterCommand).
const (
	cmdBlocked   = iota
	cmdView      // always allowed
	cmdHelm      // needs the helm of target's tab
	cmdNewWindow // needs the helm of the guest's current tab
)

// filterCommand vets a guest's tmux command line: its class, and for
// cmdHelm the -t target ("%3", "@2", "$4:@2"; "" means the guest's
// current pane). own is the guest's own session ID ("$4"), which they may
// switch windows in.
func filterCommand(line string, own string) (class int, target string) {
	t := strings.TrimSpace(line)
	if t == "" {
		return cmdView, ""
	}
	// One command per line: ";" chains commands, and "#(" in a format
	// runs a shell command.
	if strings.Contains(t, ";") || strings.Contains(t, "#(") || strings.ContainsAny(t, "\x00\r") {
		return cmdBlocked, ""
	}
	name := t
	if i := strings.IndexFunc(t, unicode.IsSpace); i >= 0 {
		name = t[:i]
	}
	switch {
	case name == "select-window" || name == "selectw":
		// Their own (grouped) session only: "$own:@N". (A bare "@N" may
		// resolve to another session in the group, such as yours.)
		if f := strings.Fields(t); len(f) == 3 && f[1] == "-t" && own != "" {
			if s, w, ok := strings.Cut(unquote(f[2]), ":"); ok && s == own && isWindowID(w) {
				return cmdView, ""
			}
		}
		return cmdBlocked, ""
	case viewCommands[name]:
		if (name == "display" || name == "display-message") && !hasFlag(t, 'p') {
			return cmdBlocked, "" // only -p (print to the guest), not messages on the host's screen
		}
		if (name == "show" || name == "show-options") && hasFlag(t, 'u') {
			return cmdBlocked, ""
		}
		return cmdView, ""
	case name == "new-window" || name == "neww":
		if flagArg(t, 't') != "" {
			return cmdBlocked, "" // in their own session, where it lands by default
		}
		return cmdNewWindow, ""
	case helmCommands[name]:
		if (name == "send-keys" || name == "send") && (hasFlag(t, 'K') || hasFlag(t, 'c')) {
			return cmdBlocked, "" // tmux key bindings: the host's prefix and command prompt
		}
		return cmdHelm, flagArg(t, 't')
	}
	return cmdBlocked, ""
}

// flagArg returns the argument of a single-letter flag among the leading
// options ("-t %3" or "-t%3"), unquoted.
func flagArg(line string, f byte) string {
	fs := strings.Fields(line)
	for i := 1; i < len(fs); i++ {
		w := fs[i]
		if !strings.HasPrefix(w, "-") || w == "--" || len(w) < 2 {
			return ""
		}
		if j := strings.IndexByte(w[1:], f); j >= 0 {
			if rest := w[2+j:]; rest != "" {
				return unquote(rest)
			}
			if i+1 < len(fs) {
				return unquote(fs[i+1])
			}
			return ""
		}
	}
	return ""
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
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
