// Package mcp is germ's Model Context Protocol server (`germ mcp`): it
// gives coding agents tools to see and drive the panes of the germ window
// they run in, over the control socket (package control).
//
// Register it with an agent, e.g. for Claude Code:
//
//	claude mcp add germ -- germ mcp
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zach-monroe/germ/internal/control"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	run         func(s *server, args map[string]any) (string, error)
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "number", "description": desc} }
func boolean(d string) map[string]any { return map[string]any{"type": "boolean", "description": d} }

const paneDesc = `Pane ID from list_panes (e.g. "3" or tmux-style "%5"), or "self" for your own pane.`

var tools = []tool{
	{
		Name: "list_panes",
		Description: "List the terminal panes (tabs and splits) in the germ terminal window you are running in: " +
			"pane IDs, the command running in each, working directory, size, and any coding agents with their " +
			"state (working / waiting for the user / done). Your own pane is marked (self). Use this to find a " +
			"dev server, test watcher, or log pane the user has open.",
		InputSchema: obj(map[string]any{}),
		run:         (*server).listPanes,
	},
	{
		Name: "read_pane",
		Description: "Read the last lines of a pane's screen and scrollback as plain text - for example the " +
			"output of a dev server, test run, or build in another pane, so the user doesn't have to paste it.",
		InputSchema: obj(map[string]any{
			"pane":  str(paneDesc),
			"lines": num("How many lines from the bottom to return (default 100, max 10000)."),
		}, "pane"),
		run: (*server).readPane,
	},
	{
		Name: "send_text",
		Description: "Type text into a pane as if the user typed it, then press Enter (unless enter is false). " +
			"Use it to run a command in a shell pane or answer a prompt in another program. Check the pane " +
			"with read_pane first so you know what's running there. You can't send to your own pane.",
		InputSchema: obj(map[string]any{
			"pane":  str(paneDesc),
			"text":  str("Text to type. Multi-line text is pasted."),
			"enter": boolean("Press Enter after the text (default true)."),
		}, "pane", "text"),
		run: (*server).sendText,
	},
	{
		Name: "open_pane",
		Description: "Open a new terminal tab or split in the user's germ window, optionally running a shell " +
			"command in it (for example a dev server, a test watcher, or a long build you want to keep an eye " +
			"on). Returns the new pane's ID for read_pane / wait_for. It doesn't take focus from the user " +
			"unless focus is true.",
		InputSchema: obj(map[string]any{
			"command": str("Shell command to run (default: an interactive shell)."),
			"where":   map[string]any{"type": "string", "enum": []string{"tab", "right", "down"}, "description": "New tab (default), or split the target pane to the right or below."},
			"pane":    str("Pane to split for where=right/down (default: your own pane, else the active one)."),
			"cwd":     str("Working directory (default: the target pane's)."),
			"focus":   boolean("Switch the user's view to the new pane (default false)."),
		}),
		run: (*server).openPane,
	},
	{
		Name: "wait_for",
		Description: "Wait until a pane's output matches a regular expression, or until the pane has been quiet " +
			"(no new output) for quiet_seconds - e.g. wait for \"compiled successfully|error\" from a dev server, " +
			"or for a test run to finish. Returns how the wait ended and the pane's last lines.",
		InputSchema: obj(map[string]any{
			"pane":            str(paneDesc),
			"pattern":         str("Regular expression (Go syntax) to look for in the pane's last lines."),
			"quiet_seconds":   num("Return once the pane has produced no output for this many seconds."),
			"timeout_seconds": num("Give up after this many seconds (default 60, max 600)."),
			"lines":           num("How many trailing lines to search and return (default 40)."),
		}, "pane"),
		run: (*server).waitFor,
	},
}

type server struct {
	mu sync.Mutex
	c  *control.Client
}

// Serve runs the MCP server on r/w (stdio) until r closes.
func Serve(r io.Reader, w io.Writer) error {
	s := &server{}
	defer func() {
		if s.c != nil {
			s.c.Close()
		}
	}()
	enc := json.NewEncoder(w)
	var wmu sync.Mutex
	send := func(resp rpcResponse) {
		wmu.Lock()
		defer wmu.Unlock()
		resp.JSONRPC = "2.0"
		enc.Encode(resp)
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			send(rpcResponse{ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // notifications (initialized, cancelled) need no answer
		}
		// Tool calls can block (wait_for), so each request runs on its own.
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, rerr := s.handle(req)
			resp := rpcResponse{ID: req.ID, Result: result, Error: rerr}
			if rerr == nil && result == nil {
				resp.Result = map[string]any{}
			}
			send(resp)
		}()
	}
	wg.Wait()
	return sc.Err()
}

var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

func (s *server) handle(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		for _, v := range protocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "germ", "version": "0.1.0"},
			"instructions": "These tools see and drive the germ terminal window you run in: list its panes, " +
				"read other panes' output (dev servers, tests, logs), type into them, open new panes for " +
				"long-running commands, and wait for output.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		for _, t := range tools {
			if t.Name == p.Name {
				text, err := t.run(s, p.Arguments)
				if err != nil {
					return toolResult(err.Error(), true), nil
				}
				return toolResult(text, false), nil
			}
		}
		return nil, &rpcError{-32602, "unknown tool " + p.Name}
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

// call sends a request to the germ window, reconnecting once if the
// window went away.
func (s *server) call(req control.Request) (*control.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if s.c == nil {
			c, err := control.Connect()
			if err != nil {
				return nil, err
			}
			s.c = c
		}
		resp, err := s.c.Call(req)
		if err == nil || (resp != nil && resp.Error != "") {
			return resp, err // success, or an error from germ itself
		}
		s.c.Close()
		s.c = nil
	}
	return nil, fmt.Errorf("lost connection to germ")
}

func (s *server) self() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c == nil {
		c, err := control.Connect()
		if err != nil {
			return ""
		}
		s.c = c
	}
	return s.c.Self()
}

func argStr(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	if v, ok := args[k].(float64); ok {
		return fmt.Sprint(v)
	}
	return ""
}

func argNum(args map[string]any, k string, def float64) float64 {
	switch v := args[k].(type) {
	case float64:
		return v
	case string:
		var f float64
		if _, err := fmt.Sscan(v, &f); err == nil {
			return f
		}
	}
	return def
}

func argBool(args map[string]any, k string, def bool) bool {
	if v, ok := args[k].(bool); ok {
		return v
	}
	return def
}

// pane resolves a pane argument ("self" included).
func (s *server) pane(args map[string]any) (string, error) {
	p := strings.TrimSpace(argStr(args, "pane"))
	if p == "" {
		return "", fmt.Errorf("pane is required (see list_panes)")
	}
	if p == "self" {
		if self := s.self(); self != "" {
			return self, nil
		}
		return "", fmt.Errorf("couldn't tell which pane you're in")
	}
	return p, nil
}

func paneName(p control.PaneInfo) string {
	if p.Tmux != "" {
		return fmt.Sprintf("%d (%s)", p.ID, p.Tmux)
	}
	return fmt.Sprint(p.ID)
}

func (s *server) listPanes(map[string]any) (string, error) {
	resp, err := s.call(control.Request{Cmd: control.CmdListPanes})
	if err != nil {
		return "", err
	}
	self := s.self()
	var b strings.Builder
	if resp.Session != "" {
		fmt.Fprintf(&b, "tmux session %q\n", resp.Session)
	}
	for _, p := range resp.Panes {
		fmt.Fprintf(&b, "pane %s: tab %d %q, running %q in %s, %dx%d", paneName(p), p.Tab, p.TabTitle,
			p.Command, p.Cwd, p.Cols, p.Rows)
		if p.Agent != "" {
			fmt.Fprintf(&b, ", agent %s (%s)", p.Agent, p.State)
		} else if p.State == "waiting" {
			b.WriteString(", wants attention")
		}
		if p.Active && p.Visible {
			b.WriteString(", focused by the user")
		}
		if self != "" && (self == fmt.Sprint(p.ID) || self == p.Tmux) {
			b.WriteString(" (self)")
		}
		b.WriteString("\n")
	}
	if len(resp.Panes) == 0 {
		b.WriteString("no panes\n")
	}
	return b.String(), nil
}

func (s *server) readPane(args map[string]any) (string, error) {
	pane, err := s.pane(args)
	if err != nil {
		return "", err
	}
	resp, err := s.call(control.Request{Cmd: control.CmdReadPane, Pane: pane, Lines: int(argNum(args, "lines", 100))})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Text) == "" {
		return "(pane is empty)", nil
	}
	return resp.Text, nil
}

func (s *server) sendText(args map[string]any) (string, error) {
	pane, err := s.pane(args)
	if err != nil {
		return "", err
	}
	if self := s.self(); self != "" && pane == self {
		return "", fmt.Errorf("that's your own pane; send_text is for other panes")
	}
	text := argStr(args, "text")
	_, err = s.call(control.Request{Cmd: control.CmdSendText, Pane: pane, Text: text, Enter: argBool(args, "enter", true)})
	if err != nil {
		return "", err
	}
	return "sent", nil
}

func (s *server) openPane(args map[string]any) (string, error) {
	req := control.Request{Cmd: control.CmdOpenPane, Where: argStr(args, "where"), Cwd: argStr(args, "cwd"),
		Focus: argBool(args, "focus", false)}
	if cmd := argStr(args, "command"); cmd != "" {
		req.Command = []string{cmd}
	}
	if p := argStr(args, "pane"); p != "" {
		pane, err := s.pane(args)
		if err != nil {
			return "", err
		}
		req.Pane = pane
	} else if self := s.self(); self != "" {
		req.Pane = self
	}
	resp, err := s.call(req)
	if err != nil {
		return "", err
	}
	id := resp.Pane.Tmux
	if resp.Pane.ID != 0 {
		id = fmt.Sprint(resp.Pane.ID)
	}
	return "opened pane " + id, nil
}

func (s *server) waitFor(args map[string]any) (string, error) {
	pane, err := s.pane(args)
	if err != nil {
		return "", err
	}
	var match func(string) bool
	if pat := argStr(args, "pattern"); pat != "" {
		re, err := regexp.Compile(pat)
		if err != nil {
			return "", fmt.Errorf("bad pattern: %v", err)
		}
		match = re.MatchString
	}
	quiet := time.Duration(argNum(args, "quiet_seconds", 0) * float64(time.Second))
	if match == nil && quiet == 0 {
		quiet = 3 * time.Second
	}
	timeout := time.Duration(min(argNum(args, "timeout_seconds", 60), 600) * float64(time.Second))
	lines := int(argNum(args, "lines", 40))

	// Waiting gets its own connection so other tool calls aren't blocked.
	c, err := control.Connect()
	if err != nil {
		return "", err
	}
	defer c.Close()
	r, err := c.WaitFor(pane, match, quiet, timeout, lines)
	if err != nil {
		return "", err
	}
	how := "timed out"
	switch {
	case r.Matched:
		how = "pattern matched"
	case r.Quiet:
		how = "pane went quiet"
	}
	return how + "; last lines:\n" + r.Text, nil
}
