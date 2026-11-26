// Package control is germ's control protocol: each germ window listens on
// a Unix socket, and `germ ctl` and `germ mcp` (and anything else) can
// list, read, type into, open, and wait on its panes.
//
// The protocol is line-delimited JSON: one Request per line, answered by
// one Response per line, any number per connection.
package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zach-monroe/germ/internal/share"
)

// Commands.
const (
	CmdListPanes = "list_panes"
	CmdReadPane  = "read_pane"
	CmdSendText  = "send_text"
	CmdOpenPane  = "open_pane"
	CmdPaneState = "pane_state"
	CmdFocusPane = "focus_pane"
	CmdRenameTab = "rename_tab" // Text is the new name of Pane's tab
	CmdClosePane = "close_pane"
	// CmdSetStatus shows Text on the right of the tab bar for TTL seconds
	// (0: until replaced; empty Text clears it).
	CmdSetStatus = "set_status"
	// CmdAgentEvent is a coding agent reporting its own state from inside
	// Pane (Claude Code hooks, Codex notify; see `germ setup agents`):
	// Event is working, waiting, done, idle or end; Agent names it; Text is
	// an optional message (the question it's asking, its last reply).
	CmdAgentEvent = "agent_event"
	// CmdShareUpdate is `germ share` telling the window what's going on in
	// the shared session (Share); the response's Seq is the last chat
	// message the window has, so the next update sends what's after it.
	CmdShareUpdate = "share_update"
	// CmdShareWait waits (up to about 8 s) for the host to do something in
	// the window (chat, the helm); the response's Actions say what.
	CmdShareWait = "share_wait"
	// CmdShareGive gives Pane's tab to the person named Text, in the
	// session this window is sharing (germ agent -for).
	CmdShareGive = "share_give"
	// CmdHelm works the helm of Pane's tab (the active tab without Pane)
	// in the window's shared session: Text is "" (ask or hand back as a
	// guest, take back as the host), "status", "ask", "back",
	// "give NAME [TAB]" or "take [TAB|all]". The response's Text says
	// what happened.
	CmdHelm = "helm"
	// CmdShareStart starts sharing the window's tmux session in the
	// background (what Ctrl+Shift+P does); the controls are in the window.
	CmdShareStart = "share_start"
	// CmdChat reads the window's session chat as text (no Text), or sends
	// Text to it as if typed in the chat box (a message or a /command).
	CmdChat = "chat"
	// CmdShareStatus says who's in the window's shared session, if any
	// (Response.ShareStatus; nil when it isn't in one).
	CmdShareStatus = "share_status"
)

// ShareStatus summarizes a window's shared session (the coworking widget).
// It leaves out the invite link.
type ShareStatus struct {
	Host     bool                `json:"host"` // this window hosts it (else joined it)
	Session  string              `json:"session"`
	Via      string              `json:"via,omitempty"`
	You      uint32              `json:"you"`
	People   []share.Participant `json:"people"`
	Tabs     []share.Tab         `json:"tabs"`
	Current  string              `json:"current,omitempty"` // the host's tab
	Requests []share.HelmRequest `json:"requests,omitempty"`
	Joins    []share.JoinRequest `json:"joins,omitempty"`
	Unread   int                 `json:"unread,omitempty"`
	Last     *share.ChatMsg      `json:"last,omitempty"` // the newest chat message
}

// Request is a control command. Pane refers to a pane by germ ID ("3") or
// tmux pane ID ("%5").
type Request struct {
	Cmd   string `json:"cmd"`
	Pane  string `json:"pane,omitempty"`
	Text  string `json:"text,omitempty"`
	Enter bool   `json:"enter,omitempty"`
	// read_pane: how many lines from the bottom (screen + history).
	Lines int `json:"lines,omitempty"`
	// open_pane
	Command []string `json:"command,omitempty"`
	Where   string   `json:"where,omitempty"` // "tab" (default), "right", "down"
	Cwd     string   `json:"cwd,omitempty"`
	Focus   bool     `json:"focus,omitempty"`
	Size    int      `json:"size,omitempty"`    // split: percent of the target for the new pane
	TTL     int      `json:"ttl,omitempty"`     // set_status: seconds to show it
	History bool     `json:"history,omitempty"` // list_panes: include agent History
	// agent_event
	Event string `json:"event,omitempty"`
	Agent string `json:"agent,omitempty"`
	// share_update
	Share *share.Update `json:"share,omitempty"`
}

// PaneInfo describes a pane.
type PaneInfo struct {
	ID       int    `json:"id"`
	Tmux     string `json:"tmux,omitempty"` // tmux pane ID in control mode
	Tab      int    `json:"tab"`            // tab position (0 = home when shown)
	TabTitle string `json:"tab_title"`
	TabLabel string `json:"tab_label,omitempty"` // the number the tab bar shows
	Title    string `json:"title"`
	Cwd      string `json:"cwd,omitempty"`
	Command  string `json:"command,omitempty"`  // foreground process
	Pid      int    `json:"pid,omitempty"`      // the pane's top process (its shell), on this machine
	Window   int    `json:"window,omitempty"`   // the germ window's process ID (as in germ-pane: links)
	Agent    string `json:"agent,omitempty"`    // detected coding agent
	State    string `json:"state,omitempty"`    // agent state: idle, working, done, waiting
	Message  string `json:"message,omitempty"`  // last notification
	StateMs  int64  `json:"state_ms,omitempty"` // how long the agent has been working / done
	// History is the agent's last 30 minutes, one character per 10 s,
	// oldest first: '!' waiting, 'w' working, 'd' done, 'i' idle, ' ' none.
	History string `json:"history,omitempty"`
	Active  bool   `json:"active"`  // focused pane of its tab
	Visible bool   `json:"visible"` // on screen now
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	// IdleMs is how long since the pane last produced output.
	IdleMs int64 `json:"idle_ms"`
}

// Response answers a Request.
type Response struct {
	OK      bool       `json:"ok"`
	Error   string     `json:"error,omitempty"`
	Panes   []PaneInfo `json:"panes,omitempty"`
	Pane    *PaneInfo  `json:"pane,omitempty"`
	Text    string     `json:"text,omitempty"`
	Session string     `json:"session,omitempty"` // tmux session, if any
	Window  int        `json:"window,omitempty"`  // germ's process ID
	// share_update: the last chat message the window has; share_wait: what
	// the host did.
	Seq     uint64         `json:"seq,omitempty"`
	Actions []share.Action `json:"actions,omitempty"`
	// share_status
	ShareStatus *ShareStatus `json:"share_status,omitempty"`
}

// Dir is where germ windows put their sockets.
func Dir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "germ")
	}
	return filepath.Join(os.TempDir(), "germ-"+strconv.Itoa(os.Getuid()))
}

// SocketPath is the socket for the germ process pid.
func SocketPath(pid int) string { return filepath.Join(Dir(), strconv.Itoa(pid)+".sock") }

// Sockets lists live-looking sockets, newest first. It deletes the sockets
// of windows that are gone (killed or crashed ones leave theirs behind).
func Sockets() []string {
	matches, _ := filepath.Glob(filepath.Join(Dir(), "*.sock"))
	type st struct {
		path string
		mod  time.Time
	}
	var out []st
	for _, m := range matches {
		if stale(m) {
			os.Remove(m)
			continue
		}
		if fi, err := os.Stat(m); err == nil {
			out = append(out, st{m, fi.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	paths := make([]string, len(out))
	for i, s := range out {
		paths[i] = s.path
	}
	return paths
}

// stale reports whether a socket's window is gone: the process named by
// the file (PID.sock) doesn't exist and nothing answers. Both, so a window
// that's still starting (bound but not yet listening) is never removed.
func stale(path string) bool {
	pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(path), ".sock"))
	if err != nil || pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		return false
	}
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		c.Close()
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

// Client is a connection to one germ window.
type Client struct {
	Path string
	conn net.Conn
	r    *bufio.Reader
}

// Dial connects to a germ socket.
func Dial(path string) (*Client, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{Path: path, conn: c, r: bufio.NewReaderSize(c, 1<<16)}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Call sends a request and waits for the response.
func (c *Client) Call(req Request) (*Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return &resp, errors.New(resp.Error)
	}
	return &resp, nil
}

// selfCandidates are the pane IDs this process may run in: tmux's
// TMUX_PANE (control-mode panes) and germ's GERM_PANE_ID (local panes).
// Which one is right depends on the window, so callers check ownership.
func selfCandidates() []string {
	var out []string
	for _, k := range []string{"TMUX_PANE", "GERM_PANE_ID"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Self returns the pane this process runs in, if it's in this window.
func (c *Client) Self() string {
	for _, ref := range selfCandidates() {
		if c.owns(ref) {
			return ref
		}
	}
	return ""
}

// Connect finds the germ window to talk to: $GERM_SOCKET if it's alive and
// owns this process's pane, else the window that does, else the newest.
func Connect() (*Client, error) {
	mine := len(selfCandidates()) > 0
	if p := os.Getenv("GERM_SOCKET"); p != "" {
		if c, err := Dial(p); err == nil {
			if !mine || c.Self() != "" {
				return c, nil
			}
			c.Close()
		}
	}
	socks := Sockets()
	if mine {
		for _, s := range socks {
			if c, err := Dial(s); err == nil {
				if c.Self() != "" {
					return c, nil
				}
				c.Close()
			}
		}
	}
	for _, s := range socks {
		if c, err := Dial(s); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no running germ window found (looked in %s)", Dir())
}

// owns reports whether the window has the pane.
func (c *Client) owns(ref string) bool {
	_, err := c.Call(Request{Cmd: CmdPaneState, Pane: ref})
	return err == nil
}

// WaitResult reports how a wait ended.
type WaitResult struct {
	Matched bool   // pattern found
	Quiet   bool   // pane went quiet
	Text    string // the pane's last lines when the wait ended
	Info    *PaneInfo
}

// WaitFor polls a pane until its recent text matches pattern (if set), or
// it has produced no output for quiet (if > 0), or timeout passes. A
// pattern is matched against the last `lines` lines.
func (c *Client) WaitFor(pane string, match func(string) bool, quiet, timeout time.Duration, lines int) (*WaitResult, error) {
	deadline := time.Now().Add(timeout)
	if lines <= 0 {
		lines = 200
	}
	for {
		resp, err := c.Call(Request{Cmd: CmdReadPane, Pane: pane, Lines: lines})
		if err != nil {
			return nil, err
		}
		r := &WaitResult{Text: resp.Text, Info: resp.Pane}
		if match != nil && match(resp.Text) {
			r.Matched = true
			return r, nil
		}
		if quiet > 0 && resp.Pane != nil && resp.Pane.IdleMs >= quiet.Milliseconds() {
			r.Quiet = true
			return r, nil
		}
		if time.Now().After(deadline) {
			return r, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}
